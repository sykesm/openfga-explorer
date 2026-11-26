package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/search"
	"github.com/sykesm/openfga-explorer/internal/tuples"
	"google.golang.org/protobuf/types/known/structpb"
)

// lowCardinality is the number of edges shown inline before switching to a count and chooser.
const lowCardinality = 8

func (s *Server) handleExplore(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	s.withStoreName(r.Context(), v)
	q := r.URL.Query()
	v.Title, v.Active = "Explore", "explore"
	typ := q.Get("type")
	if _, ok := v.Model.Types[typ]; !ok {
		typ = ""
	}
	d := &exploreData{Type: typ, ID: q.Get("id"), Relation: q.Get("relation"), Many: len(v.Model.TypeNames) > manyTypes,
		Large: len(v.Model.TypeNames) > largeModel}
	d.Modules = v.Model.Modules()
	if d.Many {
		d.Hubs = v.Model.HubTypes(8)
		ext := map[string][]string{}
		for _, mod := range d.Modules {
			for _, e := range mod.Extends {
				ext[e.Type] = append(ext[e.Type], mod.Name)
			}
		}
		for _, tn := range v.Model.TypeNames {
			t := v.Model.Types[tn]
			d.Index = append(d.Index, typeEntry{T: tn, R: t.RelationNames, M: t.Source.Module, X: ext[tn]})
		}
	}
	v.Data = d
	s.render(w, "explore", v)
}

// largeModel is the type count above which the type graph starts empty
// (a whole model that size can't be read) and shows neighborhoods instead.
const largeModel = 40

type exploreData struct {
	Type, ID, Relation string
	// Many: the type list becomes a search with recents and starting points.
	Many bool
	// Large: the type graph starts empty until a type is chosen.
	Large bool
	// Hubs are the most connected types, offered as starting points.
	Hubs []string
	// Index is every type with its relations, for searching in the browser.
	Index []typeEntry
	// Modules of a modular model (nil otherwise): types are grouped by module.
	Modules []schema.Module
}

type typeEntry struct {
	T string   `json:"t"`
	R []string `json:"r"`
	// M is the module defining the type; X the modules extending it.
	M string   `json:"m,omitempty"`
	X []string `json:"x,omitempty"`
}

type typePanel struct {
	Type     *schema.Type
	Groups   []relGroup
	Incoming []schema.Incoming
	Hint     string
	Pick     pickerData
}

type relGroup struct {
	Kind      schema.Kind
	Relations []relView
}

type relView struct {
	*schema.Relation
	Deps []schema.Dependency
	// Findings are the model checks about this relation.
	Findings []schema.Finding
}

func (s *Server) handleTypePanel(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	typ := r.URL.Query().Get("type")
	t, ok := v.Model.Types[typ]
	if !ok {
		s.fail(w, r, http.StatusNotFound, fmt.Errorf("type %q is not in the model", typ))
		return
	}
	p := typePanel{Type: t, Incoming: v.Model.IncomingFor(typ), Hint: s.rule(typ).Hint}
	byKind := t.ByKind()
	for _, k := range []schema.Kind{schema.Direct, schema.Derived, schema.Hybrid} {
		g := relGroup{Kind: k}
		for _, rel := range byKind[k] {
			g.Relations = append(g.Relations, relView{Relation: rel, Deps: v.Model.Dependencies(rel), Findings: schema.FindingsFor(v.Model.Check(), typ, rel.Name)})
		}
		p.Groups = append(p.Groups, g)
	}
	p.Pick = pickerData{Field: "inst", Types: []pickerType{{Value: typ, Label: typ, Hint: s.rule(typ).Hint}}, Mode: "explore"}
	v.Data = p
	s.render(w, "type-panel", v)
}

// ---- instance panel --------------------------------------------------------

type edgeView struct {
	// Peer is the object at the other end ("type:id"); Via is the userset relation, if any.
	Peer      string
	Via       string
	Relation  string
	Condition string
	// Source/Target are graph node IDs (user → object), with display labels.
	Source, Target           string
	SourceLabel, TargetLabel string
}

func (e edgeView) GraphData() map[string]string {
	label := e.Relation
	if e.Via != "" {
		label += " ← #" + e.Via
	}
	if e.Condition != "" {
		label += " [" + e.Condition + "]"
	}
	return map[string]string{"source": e.Source, "target": e.Target, "label": label, "relation": e.Relation, "peer": e.Peer, "sourceLabel": e.SourceLabel, "targetLabel": e.TargetLabel}
}

type edgeSet struct {
	Edges []edgeView
	// N is the number of edges when known exactly, or -1 ("100+").
	N      int
	Count  string
	More   bool
	Inline bool
	Err    string
	Token  string
}

// Empty reports that the relation has no edges at all.
func (e edgeSet) Empty() bool { return e.Err == "" && e.N == 0 }

type outRelation struct {
	Rel  *schema.Relation
	Deps []schema.Dependency
	edgeSet
	// Explain and Holders are set for derived relations.
	Explain *clauseView
	Holders []string
}

// clauseView is a schema.Clause annotated with what this instance actually has.
type clauseView struct {
	*schema.Clause
	Children []*clauseView
	// Here summarizes the instance's edges for this clause, e.g. "organization:acme" or "none".
	Here string
	// Empty is set when the instance has no edges for this clause, so it cannot grant anything.
	Empty bool
}

func (s *Server) annotate(c *schema.Clause, p *instancePanel) *clauseView {
	v := &clauseView{Clause: c}
	for _, ch := range c.Children {
		v.Children = append(v.Children, s.annotate(ch, p))
	}
	find := func(rel string) *outRelation {
		for _, o := range p.Out {
			if o.Rel.Name == rel {
				return o
			}
		}
		return nil
	}
	summarize := func(o *outRelation) {
		also := ""
		if o != nil && o.Rel.Kind == schema.Hybrid {
			also = "; also = " + o.Rel.DerivedExpr
		}
		defer func() {
			if v.Here != "" && also != "" {
				v.Here += also
			}
		}()
		switch {
		case o == nil:
		case o.Err != "":
			v.Here = "could not read edges"
		case o.Empty() && also != "":
			// A hybrid relation can still be granted through its computed part.
			v.Here = "no direct tuples"
		case o.Empty():
			v.Here, v.Empty = "none on this "+p.Type.Name, true
		case len(o.Edges) == 0:
			v.Here = o.Count + " edges"
		case o.Inline || len(o.Edges) <= 3:
			var names []string
			for i, e := range o.Edges {
				if i == 3 {
					names = append(names, "…")
					break
				}
				n := s.displayObject(e.Peer)
				if e.Via != "" {
					n += "#" + e.Via
				}
				names = append(names, n)
			}
			v.Here = strings.Join(names, ", ")
		default:
			v.Here = o.Count + " edges"
		}
	}
	switch c.Kind {
	case "relation":
		if o := find(c.Relation); o != nil {
			summarize(o)
		} else {
			v.Here = "computed — see " + c.Relation
		}
	case "via":
		summarize(find(c.Via))
	}
	return v
}

type inRelation struct {
	In   schema.Incoming
	User string // the user filter used (object or object#rel)
	edgeSet
}

type instancePanel struct {
	Object   string
	Type     *schema.Type
	Out      []*outRelation
	Derived  []*outRelation
	In       []*inRelation
	Exists   bool
	UserType []string // types usable as ListUsers filters
	// HasIncoming is set when the type can appear as the user of tuples.
	HasIncoming bool
	// HasAccess offers "what can this access?" (admins, live queries enabled).
	HasAccess bool
}

func (s *Server) handleInstance(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	q := r.URL.Query()
	typ, id := q.Get("type"), q.Get("id")
	if obj := q.Get("object"); obj != "" {
		typ, id, _ = schema.SplitObject(obj)
	}
	t, ok := v.Model.Types[typ]
	if !ok || id == "" {
		s.fail(w, r, http.StatusBadRequest, fmt.Errorf("choose a type and an ID"))
		return
	}
	obj := typ + ":" + id
	p := &instancePanel{Object: obj, Type: t}
	ctx := r.Context()

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			f()
		}()
	}
	var tupleable []string
	for _, rn := range t.RelationNames {
		rel := t.Relations[rn]
		o := &outRelation{Rel: rel, Deps: v.Model.Dependencies(rel)}
		if rel.Kind == schema.Derived {
			p.Derived = append(p.Derived, o)
			continue
		}
		p.Out = append(p.Out, o)
		tupleable = append(tupleable, rel.Name)
	}
	if bulk, ok := s.sources[0].(tuples.Bulk); ok && len(tupleable) > 0 {
		// Datastore: one query for every relation's count, one for the small edge lists.
		s.bulkOut(ctx, bulk, v.StoreID, obj, p)
	} else {
		for _, o := range p.Out {
			run(func() {
				o.edgeSet = s.readEdges(ctx, tuples.Query{Store: v.StoreID, Dir: tuples.Out, Object: obj, Relation: o.Rel.Name}, obj)
			})
		}
	}
	wg.Wait()
	for _, o := range p.Derived {
		o.Explain = s.annotate(v.Model.Explain(o.Rel), p)
		o.Holders = v.Model.SubjectTypes(o.Rel)
	}
	for _, o := range p.Out {
		o.Holders = v.Model.SubjectTypes(o.Rel)
		if !o.Empty() && o.Err == "" {
			p.Exists = true
		}
	}
	p.HasIncoming = len(v.Model.IncomingFor(typ)) > 0
	p.HasAccess = s.liveAllowed(r) && len(accessSubjects(v.Model, p.Object)) > 0
	if !p.Exists {
		// Incoming edges load on demand; check existence cheaply instead.
		p.Exists = s.exists(ctx, v, typ, id)
	}
	if p.Exists {
		// Only remember instances that exist, so typos don't come back as suggestions.
		s.recent.add(v.User.Name, obj)
	}
	seen := map[string]bool{}
	for _, e := range v.Model.TypeGraph() {
		if e.To == typ && e.Kind != "ttu" {
			k := e.From
			if e.Kind == "userset" {
				// e.Label is "relation ← #member"
				if i := strings.LastIndex(e.Label, "#"); i >= 0 {
					k += e.Label[i:]
				}
			}
			if !seen[k] {
				seen[k] = true
				p.UserType = append(p.UserType, k)
			}
		}
	}
	sort.Strings(p.UserType)
	v.Data = p
	s.render(w, "instance", v)
}

// bulkOut fills every relation row of an instance panel with two queries.
func (s *Server) bulkOut(ctx context.Context, bulk tuples.Bulk, store, obj string, p *instancePanel) {
	var rels []string
	for _, o := range p.Out {
		rels = append(rels, o.Rel.Name)
	}
	counts, err := bulk.OutCounts(ctx, store, obj, rels, countCap)
	if err != nil {
		for _, o := range p.Out {
			o.Err = err.Error()
		}
		return
	}
	var small []string
	for _, o := range p.Out {
		c := counts[o.Rel.Name]
		o.Count, o.N, o.More = c.String(), c.N, c.N > lowCardinality || !c.Exact
		if !c.Exact {
			o.N = -1
		}
		if c.Exact && c.N > 0 && c.N <= lowCardinality {
			small = append(small, o.Rel.Name)
		}
	}
	edges, err := bulk.OutEdges(ctx, store, obj, small)
	for _, o := range p.Out {
		if err != nil {
			o.Err = err.Error()
			continue
		}
		for _, k := range edges[o.Rel.Name] {
			o.Edges = append(o.Edges, s.edgeOf(tuples.Out, k, obj))
		}
		o.Inline = !o.More && len(o.Edges) == o.N
	}
}

// handleIncoming lists where an instance appears as the user of tuples. It is
// loaded on demand by the instance panel's "Incoming" tab.
func (s *Server) handleIncoming(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	obj := r.URL.Query().Get("object")
	typ, _, ok := schema.SplitObject(obj)
	if !ok {
		s.fail(w, r, http.StatusBadRequest, fmt.Errorf("invalid object %q", obj))
		return
	}
	p := &instancePanel{Object: obj, Type: v.Model.Types[typ]}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, in := range v.Model.IncomingFor(typ) {
		if in.Wildcard {
			continue
		}
		user := obj
		if in.UsersetRelation != "" {
			user += "#" + in.UsersetRelation
		}
		ir := &inRelation{In: in, User: user}
		p.In = append(p.In, ir)
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ir.edgeSet = s.readEdges(r.Context(), tuples.Query{Store: v.StoreID, Dir: tuples.In, User: user, Relation: in.Relation, ObjectType: in.ObjectType}, obj)
		}()
	}
	wg.Wait()
	kept := p.In[:0]
	for _, i := range p.In {
		if !i.Empty() {
			kept = append(kept, i)
		}
	}
	p.In = kept
	v.Data = p
	s.render(w, "incoming", v)
}

// readEdges reads the first page of edges around self from the default
// source and counts them (exactly, up to countCap, when the source can).
func (s *Server) readEdges(ctx context.Context, q tuples.Query, self string) edgeSet {
	src := s.sources[0]
	q.Limit = 100
	page, err := src.Edges(ctx, q)
	if err != nil {
		return edgeSet{Err: err.Error()}
	}
	es := edgeSet{More: page.More}
	for _, k := range page.Tuples {
		es.Edges = append(es.Edges, s.edgeOf(q.Dir, k, self))
	}
	es.Count, es.N = fmt.Sprint(len(es.Edges)), len(es.Edges)
	if es.More {
		es.Count, es.N = fmt.Sprintf("%d+", len(es.Edges)), -1
		if c, err := src.Count(ctx, q, countCap); err == nil && (c.Exact || c.N > len(es.Edges)) {
			es.Count = c.String()
			if c.Exact {
				es.N = c.N
			}
		}
	}
	es.Inline = !es.More && len(es.Edges) <= lowCardinality
	return es
}

// countCap bounds exact edge counts so a huge relation costs one bounded scan.
const countCap = 10000

func (s *Server) edgeOf(dir tuples.Dir, k *openfgav1.TupleKey, self string) edgeView {
	e := edgeView{Relation: k.GetRelation(), Condition: k.GetCondition().GetName()}
	if dir == tuples.Out {
		peer, via := k.GetUser(), ""
		if i := strings.LastIndexByte(peer, '#'); i >= 0 {
			peer, via = peer[:i], peer[i+1:]
		}
		e.Peer, e.Via, e.Source, e.Target = peer, via, peer, self
	} else {
		if i := strings.LastIndexByte(k.GetUser(), '#'); i >= 0 {
			e.Via = k.GetUser()[i+1:]
		}
		e.Peer, e.Source, e.Target = k.GetObject(), self, k.GetObject()
	}
	e.SourceLabel, e.TargetLabel = s.displayObject(e.Source), s.displayObject(e.Target)
	return e
}

// ---- edge chooser ------------------------------------------------------------

type edgeChooser struct {
	Object, Relation, Dir, User, ObjectType string
	Filter                                  string
	Shown                                   []edgeView
	// Offset counts edges shown before this batch (no filter) or matches so far.
	Offset, Scanned int
	More            bool
	Err, Note       string
	NextQuery       string
	// Source is the tuple source used (chosen by configuration, not by users).
	Source tuples.Source
	// Prefixes are the normalized ID prefixes searched (datastore source).
	Prefixes []string
	Total    string
}

func (s *Server) handleEdges(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	q := r.URL.Query()
	c := &edgeChooser{Object: q.Get("object"), Relation: q.Get("relation"), Dir: q.Get("dir"), User: q.Get("user"), ObjectType: q.Get("object_type"),
		Filter: strings.TrimSpace(q.Get("filter")), Source: s.source(q.Get("source"))}
	fmt.Sscan(q.Get("offset"), &c.Offset)
	tq := tuples.Query{Store: v.StoreID, Relation: c.Relation, Cursor: q.Get("cursor"), Limit: 100}
	if c.Dir == "in" {
		tq.Dir, tq.User, tq.ObjectType = tuples.In, c.User, c.ObjectType
	} else {
		c.Dir = "out"
		tq.Dir, tq.Object = tuples.Out, c.Object
	}
	if c.Filter != "" {
		if c.Source.FilterMode() == "prefix" {
			c.Prefixes = s.edgePrefixes(v.Model, tq, c.Filter)
			tq.Prefixes = c.Prefixes
			if len(tq.Prefixes) == 0 {
				c.Err = "no user type of this relation matches “" + c.Filter + "”"
			}
		} else {
			f := strings.ToLower(c.Filter)
			tq.Match = func(peer string) bool {
				return strings.Contains(strings.ToLower(peer), f) || strings.Contains(strings.ToLower(s.displayObject(peer)), f)
			}
		}
	}
	if c.Err == "" {
		page, err := c.Source.Edges(r.Context(), tq)
		if err != nil {
			c.Err = err.Error()
		} else {
			for _, k := range page.Tuples {
				c.Shown = append(c.Shown, s.edgeOf(tq.Dir, k, c.Object))
			}
			c.Scanned, c.More, c.Note = page.Scanned, page.More, page.Note
			if c.More {
				next := url.Values{"object": {c.Object}, "relation": {c.Relation}, "dir": {c.Dir}, "user": {c.User}, "object_type": {c.ObjectType},
					"cursor": {page.Cursor}, "offset": {fmt.Sprint(c.Offset + len(c.Shown))}, "filter": {c.Filter}, "source": {c.Source.Name()}}
				c.NextQuery = next.Encode()
			}
		}
		if c.Offset == 0 {
			unfiltered := tq
			unfiltered.Prefixes, unfiltered.Match = nil, nil
			if cnt, err := c.Source.Count(r.Context(), unfiltered, countCap); err == nil {
				c.Total = cnt.String()
			}
		}
	}
	v.Data = c
	s.render(w, "edges", v)
}

// edgePrefixes turns a typed filter into datastore prefixes. Out edges are
// matched on the full user ("principal:CN%3DAL"), one prefix per user type
// the relation accepts, each normalized with that type's ID rules; typing
// "type:…" restricts to that type. In edges are matched on the object ID.
func (s *Server) edgePrefixes(m *schema.Model, q tuples.Query, filter string) []string {
	if q.Dir == tuples.In {
		f := strings.TrimPrefix(filter, q.ObjectType+":")
		return []string{s.rule(q.ObjectType).Normalize(f)}
	}
	typ, _, _ := schema.SplitObject(q.Object)
	rel, ok := m.Relation(typ, q.Relation)
	if !ok {
		return nil
	}
	only, rest := "", filter
	if t, after, ok := strings.Cut(filter, ":"); ok {
		if _, known := m.Types[t]; known {
			only, rest = t, after
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, ref := range rel.DirectTypes {
		t := ref.GetType()
		if seen[t] || (only != "" && t != only) {
			continue
		}
		seen[t] = true
		out = append(out, t+":"+s.rule(t).Normalize(rest))
	}
	return out
}

// ---- ListUsers ---------------------------------------------------------------

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	q := r.URL.Query()
	typ, id, _ := schema.SplitObject(q.Get("object"))
	filter := q.Get("user_type")
	ft, frel := filter, ""
	if i := strings.IndexByte(filter, '#'); i >= 0 {
		ft, frel = filter[:i], filter[i+1:]
	}
	req := &openfgav1.ListUsersRequest{
		StoreId: v.StoreID, AuthorizationModelId: v.ModelID,
		Object:      &openfgav1.Object{Type: typ, Id: id},
		Relation:    q.Get("relation"),
		UserFilters: []*openfgav1.UserTypeFilter{{Type: ft, Relation: frel}},
	}
	// From the Check view, evaluate with the same context and contextual
	// tuples as the check, so conditional grants are decided the same way.
	data := map[string]any{"Object": q.Get("object"), "Relation": q.Get("relation"), "Filter": filter,
		"Mode": q.Get("mode"), "Current": q.Get("current")}
	if c := strings.TrimSpace(q.Get("context")); c != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(c), &m); err == nil {
			req.Context, _ = structpb.NewStruct(m)
		}
	}
	if ct, err := parseContextual(q.Get("contextual")); err == nil && len(ct) > 0 {
		req.ContextualTuples = ct
	}
	resp, err := s.fga.ListUsers(r.Context(), req)
	if err != nil {
		data["Err"] = err.Error()
	} else {
		var users []string
		for _, u := range resp.GetUsers() {
			switch x := u.GetUser().(type) {
			case *openfgav1.User_Object:
				users = append(users, x.Object.GetType()+":"+x.Object.GetId())
			case *openfgav1.User_Userset:
				users = append(users, x.Userset.GetType()+":"+x.Userset.GetId()+"#"+x.Userset.GetRelation())
			case *openfgav1.User_Wildcard:
				users = append(users, x.Wildcard.GetType()+":*")
			}
		}
		sort.Strings(users)
		data["Users"] = users
		shown := 20
		if q.Get("mode") == "check" {
			shown = 10
		}
		if len(users) > shown {
			data["First"], data["Rest"] = users[:shown], users[shown:]
		} else {
			data["First"] = users
		}
		data["Noun"] = plural(filter, len(users))
	}
	v.Data = data
	s.render(w, "listusers", v)
}

// ---- instance search ---------------------------------------------------------

type pickerType struct {
	Value, Label, Hint string
	// Rels are the type's relation names, space separated, so the type
	// chooser can find types by relation (e.g. "can_approve").
	Rels string
}

// manyTypes is the number of types above which choosers get a find box.
const manyTypes = 12

// Many reports whether the picker should offer a type find box.
func (p pickerData) Many() bool { return len(p.Types) > manyTypes }

// SelectedLabel is the label of the selected type.
func (p pickerData) SelectedLabel() string {
	for _, t := range p.Types {
		if t.Value == p.Selected {
			return t.Label
		}
	}
	return p.Selected
}

type pickerData struct {
	Field string
	Types []pickerType
	// Mode is "explore" (picking loads the instance panel) or "form" (fills a field).
	Mode     string
	Value    string
	Selected string
	Required bool
}

type searchResults struct {
	Field      string
	Mode       string
	Type       string
	Suffix     string
	Typed      string
	Normalized string
	Results    []search.Result
	Page       *search.Page
	Recent     []string
	Err        string
	NoSearch   bool
	Exists     *bool
	MoreQuery  string
	Continued  bool
	// Browse is set when listing instances without a prefix.
	Browse bool
	// InResults is set when the typed ID is one of the results (no separate row needed).
	InResults bool
	// AsObject and AsUser describe where IDs of this type appear in tuples,
	// so an empty result can be explained from the model.
	AsObject []string
	AsUser   []string
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	q := r.URL.Query()
	field := q.Get("field")
	sel := q.Get(field + "_type")
	typ, suffix := sel, ""
	if i := strings.IndexByte(sel, '#'); i >= 0 {
		typ, suffix = sel[:i], sel[i:]
	}
	if _, ok := v.Model.Types[typ]; !ok {
		v.Data = &searchResults{Field: field, Err: "choose a type first"}
		s.render(w, "search-results", v)
		return
	}
	typed := q.Get(field + "_q")
	if strings.HasPrefix(typed, typ+":") {
		typed = strings.TrimPrefix(typed, typ+":")
	}
	res := &searchResults{Field: field, Mode: q.Get("mode"), Type: typ, Suffix: suffix, Typed: typed, Normalized: s.rule(typ).Normalize(typed)}
	ctx := r.Context()
	t := v.Model.Types[typ]
	for _, rn := range t.RelationNames {
		if t.Relations[rn].Tupleable() {
			res.AsObject = append(res.AsObject, rn)
		}
	}
	for _, in := range v.Model.IncomingFor(typ) {
		d := in.ObjectType + "#" + in.Relation
		switch {
		case in.UsersetRelation != "":
			d += " (as " + typ + "#" + in.UsersetRelation + ")"
		case in.Wildcard:
			d += " (as " + typ + ":*)"
		}
		res.AsUser = append(res.AsUser, d)
	}
	if res.Normalized == "" {
		for _, o := range s.recent.list(v.User.Name) {
			if strings.HasPrefix(o, typ+":") {
				res.Recent = append(res.Recent, strings.TrimPrefix(o, typ+":"))
			}
		}
	}
	if s.search == nil {
		res.NoSearch = true
		if res.Normalized != "" {
			e := s.exists(ctx, v, typ, res.Normalized)
			res.Exists = &e
		}
	} else {
		// An empty prefix browses the type's instances in ID order.
		res.Browse = res.Normalized == ""
		var sides []string
		if len(res.AsObject) > 0 {
			sides = append(sides, "object")
		}
		if len(v.Model.IncomingFor(typ)) > 0 {
			sides = append(sides, "user")
		}
		pg, err := s.search.Search(ctx, search.Query{Store: v.StoreID, Type: typ, Prefix: res.Normalized, Limit: 25, Cursor: q.Get("cursor"), Sides: sides})
		if err != nil {
			res.Err = err.Error()
		} else {
			res.Page = pg
			res.Results = pg.Results
		}
	}
	res.Continued = q.Get("cursor") != ""
	// Say whether the typed ID exists, so a typo never looks like a match.
	if res.Normalized != "" && !res.Continued && res.Exists == nil {
		e := false
		for _, r := range res.Results {
			if r.ID == res.Normalized {
				e, res.InResults = true, true
			}
		}
		if !e {
			e = s.exists(ctx, v, typ, res.Normalized)
		}
		res.Exists = &e
	}
	if res.Page != nil && !res.Page.Complete {
		mq := url.Values{}
		for k, vs := range q {
			mq[k] = vs
		}
		mq.Set("cursor", res.Page.Cursor)
		res.MoreQuery = mq.Encode()
	}
	v.Data = res
	s.render(w, "search-results", v)
}

// exists checks whether any tuple references the object, as object or user.
func (s *Server) exists(ctx context.Context, v *view, typ, id string) bool {
	obj := typ + ":" + id
	if resp, err := fga.ReadPage(ctx, s.fga, v.StoreID, &openfgav1.ReadRequestTupleKey{Object: obj}, 1, "", 0); err == nil && len(resp.GetTuples()) > 0 {
		return true
	}
	for _, in := range v.Model.IncomingFor(typ) {
		user := obj
		if in.UsersetRelation != "" {
			user += "#" + in.UsersetRelation
		}
		if resp, err := fga.ReadPage(ctx, s.fga, v.StoreID, &openfgav1.ReadRequestTupleKey{User: user, Relation: in.Relation, Object: in.ObjectType + ":"}, 1, "", 0); err == nil && len(resp.GetTuples()) > 0 {
			return true
		}
	}
	return false
}

// ---- recently used -------------------------------------------------------------

type recent struct {
	mu sync.Mutex
	m  map[string][]string
}

func newRecent() *recent { return &recent{m: map[string][]string{}} }

func (r *recent) add(user, obj string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := []string{obj}
	for _, o := range r.m[user] {
		if o != obj && len(l) < 30 {
			l = append(l, o)
		}
	}
	r.m[user] = l
}

func (r *recent) list(user string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.m[user]...)
}

// graphPatch converts an instance panel into Cytoscape elements: the focus
// node, its low-cardinality edges, and one "count" node per high-cardinality
// relation that opens the edge chooser when clicked.
func (s *Server) graphPatch(p *instancePanel) map[string]any {
	var els []map[string]any
	seen := map[string]bool{}
	node := func(id string, extra map[string]any) {
		if seen[id] {
			return
		}
		seen[id] = true
		t, _, _ := schema.SplitObject(id)
		d := map[string]any{"id": id, "label": s.displayObject(id), "type": t}
		for k, v := range extra {
			d[k] = v
		}
		els = append(els, map[string]any{"group": "nodes", "data": d})
	}
	edge := func(e edgeView) {
		g := e.GraphData()
		id := g["source"] + "|" + g["label"] + "|" + g["target"]
		if seen[id] {
			return
		}
		seen[id] = true
		node(e.Peer, nil)
		els = append(els, map[string]any{"group": "edges", "data": map[string]any{"id": id, "source": g["source"], "target": g["target"], "label": g["label"], "relation": g["relation"]}})
	}
	node(p.Object, map[string]any{"focus": true})
	for _, o := range p.Out {
		switch {
		case o.Inline:
			for _, e := range o.Edges {
				edge(e)
			}
		case !o.Empty() && o.Err == "":
			id := p.Object + "|out|" + o.Rel.Name
			node(id, map[string]any{"label": o.Count + " " + o.Rel.Name, "kind": "count", "chooser": "out-" + o.Rel.Name, "owner": p.Object})
			els = append(els, map[string]any{"group": "edges", "data": map[string]any{"id": id + "|e", "source": id, "target": p.Object, "label": o.Rel.Name, "relation": o.Rel.Name}, "classes": "count"})
		}
	}
	for _, in := range p.In {
		name := in.In.ObjectType + "#" + in.In.Relation
		switch {
		case in.Inline:
			for _, e := range in.Edges {
				edge(e)
			}
		case !in.Empty() && in.Err == "":
			id := p.Object + "|in|" + name
			node(id, map[string]any{"label": in.Count + " " + name, "kind": "count", "chooser": "in-" + name, "owner": p.Object})
			els = append(els, map[string]any{"group": "edges", "data": map[string]any{"id": id + "|e", "source": p.Object, "target": id, "label": in.In.Relation, "relation": in.In.Relation}, "classes": "count"})
		}
	}
	return map[string]any{"focus": p.Object, "elements": els}
}

// plural names a user type for a count: "1 user", "3 users", "2 group#member usersets".
func plural(typ string, n int) string {
	if strings.Contains(typ, "#") {
		if n == 1 {
			return typ + " userset"
		}
		return typ + " usersets"
	}
	if n == 1 {
		return typ
	}
	return typ + "s"
}
