package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

type checkForm struct {
	User, Relation, Object string
	Context                string
	Contextual             string
	AllPaths               bool
	Order                  string // trace.OrderGuided, OrderModel or OrderAll
	Verify                 string
	Consistency            string
	MaxCalls               int
	MaxFanout, ProbeLimit  int
	UserPick, ObjectPick   pickerData
	Relations              relationOptions
	AutoRun                bool
}

// userTypes lists the picker options for check users: the types and usersets
// that can appear as the user of a tuple somewhere in the model. Types that
// never appear as a user (e.g. document) cannot hold any relation.
func userTypes(m *schema.Model, rules func(string) string) []pickerType {
	var out []pickerType
	for _, u := range m.UserTypes() {
		t, label := u, u
		if i := strings.IndexByte(u, '#'); i >= 0 {
			t, label = u[:i], u+" (userset)"
		}
		pt := pickerType{Value: u, Label: label, Hint: rules(t)}
		if ty := m.Types[t]; ty != nil && !strings.Contains(u, "#") {
			pt.Rels = strings.Join(ty.RelationNames, " ")
		}
		out = append(out, pt)
	}
	return out
}

// relationOptions is the data for the relation <select>: every relation of
// the object type, with those the user type can never reach disabled.
type relationOptions struct {
	Type     *schema.Type
	Selected string
	UserType string
	Reach    map[string]bool // nil when no user type is known: everything enabled
	None     bool            // no relation is reachable
}

func (o relationOptions) Enabled(rel string) bool { return o.Reach == nil || o.Reach[rel] }

func relOptions(m *schema.Model, objType, userType, selected string) relationOptions {
	o := relationOptions{Type: m.Types[objType], Selected: selected, UserType: userType}
	if o.Type == nil || userType == "" {
		return o
	}
	o.Reach = m.Reachable(objType, userType)
	o.None = true
	for _, ok := range o.Reach {
		if ok {
			o.None = false
		}
	}
	// Keep the selection only if it is still evaluable; the browser then
	// selects the first enabled option.
	if !o.Reach[selected] {
		o.Selected = ""
	}
	return o
}

// orderParam reads the evaluation order, accepting the older all_paths flag.
func orderParam(order, allPaths string) string {
	switch {
	case order == trace.OrderModel || order == trace.OrderAll:
		return order
	case order == "" && allPaths != "":
		return trace.OrderAll
	}
	return trace.OrderGuided
}

func (s *Server) handleCheckPage(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	s.withStoreName(r.Context(), v)
	q := r.URL.Query()
	f := &checkForm{
		User: q.Get("user"), Relation: q.Get("relation"), Object: q.Get("object"),
		Context: q.Get("context"), Contextual: q.Get("contextual"),
		AllPaths: q.Get("all_paths") != "", Verify: q.Get("verify"), Consistency: q.Get("consistency"),
		Order: orderParam(q.Get("order"), q.Get("all_paths")),
	}
	if f.Verify == "" {
		f.Verify = "all"
	}
	f.MaxCalls, _ = strconv.Atoi(q.Get("max_calls"))
	if f.MaxCalls <= 0 {
		f.MaxCalls = s.cfg.Trace.MaxCalls
	}
	f.MaxFanout, _ = strconv.Atoi(q.Get("max_fanout"))
	if f.MaxFanout <= 0 {
		f.MaxFanout = s.cfg.Trace.MaxFanout
	}
	f.ProbeLimit, _ = strconv.Atoi(q.Get("probe_limit"))
	if f.ProbeLimit <= 0 {
		f.ProbeLimit = s.cfg.Trace.ProbeLimit
	}
	hint := func(t string) string { return s.rule(t).Hint }
	f.UserPick = pickerData{Field: "user", Types: userTypes(v.Model, hint), Mode: "form", Required: true}
	var objTypes []pickerType
	for _, tn := range v.Model.TypeNames {
		if len(v.Model.Types[tn].RelationNames) > 0 {
			objTypes = append(objTypes, pickerType{Value: tn, Label: tn, Hint: hint(tn), Rels: strings.Join(v.Model.Types[tn].RelationNames, " ")})
		}
	}
	f.ObjectPick = pickerData{Field: "object", Types: objTypes, Mode: "form", Required: true}
	if subj, err := schema.ParseSubject(f.User); err == nil {
		f.UserPick.Value = f.User
		f.UserPick.Selected = subj.Type
		if subj.Relation != "" {
			f.UserPick.Selected += "#" + subj.Relation
		}
	}
	if f.UserPick.Selected == "" {
		// Default to the conventional end-user type rather than the first type alphabetically.
		for _, pt := range f.UserPick.Types {
			if pt.Value == "user" {
				f.UserPick.Selected = "user"
			}
		}
		if f.UserPick.Selected == "" && len(f.UserPick.Types) > 0 {
			f.UserPick.Selected = f.UserPick.Types[0].Value
		}
	}
	objType := ""
	if t, _, ok := schema.SplitObject(f.Object); ok {
		f.ObjectPick.Value, f.ObjectPick.Selected = f.Object, t
		objType = t
	} else if len(objTypes) > 0 {
		objType = objTypes[0].Value
	}
	f.Relations = relOptions(v.Model, objType, f.UserPick.Selected, f.Relation)
	f.AutoRun = f.User != "" && f.Relation != "" && f.Object != ""
	v.Title, v.Active, v.Data = "Check", "check", f
	s.render(w, "check", v)
}

type checkResult struct {
	Trace    *trace.Trace
	Lines    []trace.Line
	Graph    *trace.Graph
	Decision trace.Outcome
	Walker   trace.Outcome
	Link     string
	// Context and Contextual echo the request for "trace from here" links.
	Context, Contextual string
	// Grants counts the granting tuples reachable from the root along allowed links.
	Grants int
	// Gaps explains where the walk was cut short; empty means exhaustive.
	Gaps []string
	// Holders are the user types that can hold the relation (for "who else has this?");
	// UserType is the checked user's type, preselected.
	Holders  []string
	UserType string
	// Explanation is the plain-language summary; Text the copyable trace.
	Explanation trace.Explanation
	Text        string
	// Summary is the one-line form summary; Done the player's closing status.
	Summary string
	Done    doneText
}

// ---- live progress ---------------------------------------------------------

type runState struct {
	mu      sync.Mutex
	calls   int
	current string
	done    chan struct{}
}

func (r *runState) update(p trace.Progress) {
	r.mu.Lock()
	r.calls, r.current = p.Calls, p.Current
	r.mu.Unlock()
}

type progressRegistry struct {
	mu   sync.Mutex
	runs map[string]*runState
}

func newProgressRegistry() *progressRegistry { return &progressRegistry{runs: map[string]*runState{}} }

func (p *progressRegistry) start(id string) *runState {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.runs[id]
	if !ok {
		st = &runState{done: make(chan struct{})}
		p.runs[id] = st
	}
	return st
}

func (p *progressRegistry) get(id string) *runState {
	return p.start(id) // the stream may connect before the run registers
}

func (p *progressRegistry) finish(id string) {
	p.mu.Lock()
	st := p.runs[id]
	p.mu.Unlock()
	if st != nil {
		close(st.done)
	}
	time.AfterFunc(time.Minute, func() {
		p.mu.Lock()
		delete(p.runs, id)
		p.mu.Unlock()
	})
}

func validRunID(id string) bool {
	if len(id) < 8 || len(id) > 40 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// handleCheckProgress streams a walk's progress as server-sent events.
func (s *Server) handleCheckProgress(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !validRunID(id) {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	st := s.progress.get(id)
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	limit := time.After(3 * time.Minute)
	send := func(event string) {
		st.mu.Lock()
		b, _ := json.Marshal(map[string]any{"calls": st.calls, "current": s.displayObject(st.current)})
		st.mu.Unlock()
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		fl.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-limit:
			return
		case <-st.done:
			send("done")
			return
		case <-tick.C:
			send("progress")
		}
	}
}

func (s *Server) handleCheckRun(w http.ResponseWriter, r *http.Request) {
	v, me, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, err)
		return
	}
	pick := func(field string) string {
		if val := strings.TrimSpace(r.PostForm.Get(field)); val != "" {
			return val
		}
		sel, typed := r.PostForm.Get(field+"_type"), strings.TrimSpace(r.PostForm.Get(field+"_q"))
		if sel == "" || typed == "" {
			return ""
		}
		typ, suffix := sel, ""
		if i := strings.IndexByte(sel, '#'); i >= 0 {
			typ, suffix = sel[:i], sel[i:]
		}
		typed = strings.TrimPrefix(typed, typ+":")
		return typ + ":" + s.rule(typ).Normalize(typed) + suffix
	}
	req := trace.Request{
		Store: v.StoreID, Model: v.ModelID,
		User: pick("user"), Relation: r.PostForm.Get("relation"), Object: pick("object"),
	}
	if req.User == "" || req.Relation == "" || req.Object == "" {
		s.fail(w, r, http.StatusBadRequest, errors.New("user, relation, and object are required"))
		return
	}
	ctxText := strings.TrimSpace(r.PostForm.Get("context"))
	if ctxText != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(ctxText), &m); err != nil {
			s.fail(w, r, http.StatusBadRequest, fmt.Errorf("context must be a JSON object: %w", err))
			return
		}
		if req.Context, err = structpb.NewStruct(m); err != nil {
			s.fail(w, r, http.StatusBadRequest, fmt.Errorf("context: %w", err))
			return
		}
	}
	ctText := r.PostForm.Get("contextual")
	if req.Contextual, err = parseContextual(ctText); err != nil {
		s.fail(w, r, http.StatusBadRequest, err)
		return
	}
	if r.PostForm.Get("consistency") == "higher" {
		req.Consistency = openfgav1.ConsistencyPreference_HIGHER_CONSISTENCY
	}
	opt := trace.DefaultOptions()
	opt.MaxFanout, opt.ProbeLimit = s.cfg.Trace.MaxFanout, s.cfg.Trace.ProbeLimit
	opt.Timeout = s.cfg.Trace.Timeout
	opt.MaxCalls = s.cfg.Trace.MaxCalls
	if n, err := strconv.Atoi(r.PostForm.Get("max_calls")); err == nil && n > 0 {
		opt.MaxCalls = min(n, 5000)
	}
	if n, err := strconv.Atoi(r.PostForm.Get("max_fanout")); err == nil && n > 0 {
		opt.MaxFanout = min(n, 1000)
	}
	if n, err := strconv.Atoi(r.PostForm.Get("probe_limit")); err == nil && n > 0 {
		opt.ProbeLimit = min(n, 1000)
	}
	order := orderParam(r.PostForm.Get("order"), r.PostForm.Get("all_paths"))
	opt.SetOrder(order)
	switch r.PostForm.Get("verify") {
	case "none":
		opt.Verify = trace.VerifyNone
	case "root":
		opt.Verify = trace.VerifyRoot
	default:
		opt.Verify = trace.VerifyAll
	}

	select {
	case s.traceSem <- struct{}{}:
		defer func() { <-s.traceSem }()
	case <-time.After(5 * time.Second):
		s.fail(w, r, http.StatusTooManyRequests, errors.New("too many traces are running; try again shortly"))
		return
	}
	// Remember only IDs that appear in tuples, so typos don't come back as suggestions.
	if t, id, ok := schema.SplitObject(req.Object); ok && s.exists(r.Context(), v, t, id) {
		s.recent.add(v.User.Name, req.Object)
	}
	if subj, err := schema.ParseSubject(req.User); err == nil && !subj.IsWildcard() && s.exists(r.Context(), v, subj.Type, subj.ID) {
		s.recent.add(v.User.Name, subj.Type+":"+subj.ID)
	}
	// Live progress for the browser (see handleCheckProgress); the walk stops
	// if the browser cancels the request.
	if id := r.PostForm.Get("run_id"); validRunID(id) {
		st := s.progress.start(id)
		defer s.progress.finish(id)
		opt.Progress = st.update
	}
	tr, err := trace.Explain(r.Context(), s.walkAPI, me.m, me.ev, req, opt)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err)
		return
	}
	if r.Context().Err() != nil {
		return // canceled by the browser
	}
	s.log.Info("trace", "identity", v.User.Name, "store", v.StoreID, "user", req.User, "relation", req.Relation, "object", req.Object,
		"decision", tr.Decision(), "calls", tr.Calls, "verify_calls", tr.VerifyCalls, "mismatches", tr.Mismatches, "duration", tr.Duration)

	link := v.Base + "/check?" + string(queryOf(map[string]string{
		"user": req.User, "relation": req.Relation, "object": req.Object, "context": ctxText, "contextual": ctText,
		"order": order, "verify": r.PostForm.Get("verify"), "consistency": r.PostForm.Get("consistency"),
		"max_calls": r.PostForm.Get("max_calls"), "max_fanout": r.PostForm.Get("max_fanout"), "probe_limit": r.PostForm.Get("probe_limit"),
	}))
	w.Header().Set("HX-Push-Url", link)
	graph := tr.Graph()
	addNodeDetails(tr, graph, traceLinks{base: v.Base, user: req.User, context: ctxText, contextual: ctText}, me.m)
	gaps := tr.Gaps()
	v.Data = &checkResult{Trace: tr, Lines: tr.Lines(), Graph: graph, Decision: tr.Decision(), Walker: tr.Root.Outcome, Link: link, Context: ctxText, Contextual: ctText, Grants: tr.GrantingTuples(), Gaps: gaps,
		Explanation: tr.Explanation(s.displayObject), Text: tr.Text(),
		Summary: checkSummary(req.User, req.Relation, req.Object, ctxText), Done: doneTexts(tr.Decision(), order, len(gaps) == 0)}
	if subj, err := schema.ParseSubject(req.User); err == nil {
		res := v.Data.(*checkResult)
		res.UserType = subj.Type
		if subj.Relation != "" {
			res.UserType += "#" + subj.Relation
		}
		if typ, _, ok := schema.SplitObject(req.Object); ok {
			if rel, ok := me.m.Relation(typ, req.Relation); ok {
				res.Holders = me.m.SubjectTypes(rel)
			}
		}
	}
	s.render(w, "check-result", v)
}

func queryOf(m map[string]string) string {
	var parts []string
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if m[k] != "" {
			parts = append(parts, k+"="+urlQueryEscape(m[k]))
		}
	}
	return strings.Join(parts, "&")
}

// parseContextual accepts a JSON array of tuple keys, or one tuple per line:
//
//	user:anne viewer document:roadmap
//	user:anne viewer document:roadmap non_expired_grant {"grant_time":"2026-01-01T00:00:00Z","grant_duration":"1h"}
func parseContextual(text string) ([]*openfgav1.TupleKey, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	var out []*openfgav1.TupleKey
	if strings.HasPrefix(text, "[") {
		var raw []json.RawMessage
		if err := json.Unmarshal([]byte(text), &raw); err != nil {
			return nil, fmt.Errorf("contextual tuples: %w", err)
		}
		for i, r := range raw {
			var k openfgav1.TupleKey
			if err := protojson.Unmarshal(r, &k); err != nil {
				return nil, fmt.Errorf("contextual tuple %d: %w", i+1, err)
			}
			out = append(out, &k)
		}
		return out, nil
	}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, " ", 5)
		var fields []string
		for _, p := range parts {
			if p != "" {
				fields = append(fields, p)
			}
		}
		if len(fields) < 3 {
			return nil, fmt.Errorf("contextual tuple line %d: expected \"user relation object [condition {context}]\"", i+1)
		}
		k := &openfgav1.TupleKey{User: fields[0], Relation: fields[1], Object: fields[2]}
		if len(fields) >= 4 {
			k.Condition = &openfgav1.RelationshipCondition{Name: fields[3]}
			if len(fields) == 5 {
				var m map[string]any
				if err := json.Unmarshal([]byte(fields[4]), &m); err != nil {
					return nil, fmt.Errorf("contextual tuple line %d: condition context: %w", i+1, err)
				}
				st, err := structpb.NewStruct(m)
				if err != nil {
					return nil, err
				}
				k.Condition.Context = st
			}
		}
		out = append(out, k)
	}
	return out, nil
}
