// Package schema wraps an OpenFGA authorization model with the derived
// information the explorer needs: relation classification (direct, derived,
// hybrid), readable rewrite expressions, and the type-level graph.
package schema

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
)

// Kind classifies a relation by whether tuples can be written for it.
type Kind int

const (
	// Direct relations are defined only by type restrictions; every edge is a tuple.
	Direct Kind = iota
	// Derived relations have no type restrictions; they are computed from other relations.
	Derived
	// Hybrid relations accept tuples and are also computed from other relations.
	Hybrid
)

func (k Kind) String() string {
	switch k {
	case Direct:
		return "direct"
	case Derived:
		return "derived"
	default:
		return "hybrid"
	}
}

// Label is the human-readable group heading for a kind.
func (k Kind) Label() string {
	switch k {
	case Direct:
		return "Direct"
	case Derived:
		return "Derived"
	default:
		return "Direct + derived"
	}
}

// Model is an analyzed authorization model.
type Model struct {
	ID         string
	Proto      *openfgav1.AuthorizationModel
	Types      map[string]*Type
	TypeNames  []string
	Conditions map[string]*openfgav1.Condition
	DSL        string
	// Modular is set for models written as modules (schema 1.2, fga.mod).
	Modular bool
	// ConditionSources are the conditions' modules (modular models).
	ConditionSources map[string]Source

	checkOnce   sync.Once
	findings    []Finding
	holdersOnce sync.Once
	holderSets  map[string]map[string]bool
}

// Type is an analyzed type definition.
type Type struct {
	Name      string
	Relations map[string]*Relation
	// RelationNames is sorted: direct, hybrid, derived, then alphabetically.
	RelationNames []string
	// Source is the module that defines the type (modular models).
	Source Source
}

// Relation is an analyzed relation definition.
type Relation struct {
	Name        string
	Type        string
	Rewrite     *openfgav1.Userset
	DirectTypes []*openfgav1.RelationReference
	Kind        Kind
	// Expr is the DSL rendering of the rewrite, e.g. "[user] or owner or viewer from parent".
	Expr string
	// DerivedExpr renders only the computed part of a hybrid relation (without the type restrictions).
	DerivedExpr string
	// Source is the module that defines the relation (modular models).
	Source Source
	// Extended marks a relation another module adds to the type (`extend type`).
	Extended bool
}

// Load analyzes an authorization model.
func Load(m *openfgav1.AuthorizationModel) (*Model, error) {
	if m == nil {
		return nil, fmt.Errorf("nil authorization model")
	}
	out := &Model{
		ID:         m.GetId(),
		Proto:      m,
		Types:      map[string]*Type{},
		Conditions: m.GetConditions(),
	}
	if dsl, err := transformer.TransformJSONProtoToDSL(m); err == nil {
		out.DSL = dsl
	}
	for _, td := range m.GetTypeDefinitions() {
		t := &Type{Name: td.GetType(), Relations: map[string]*Relation{}}
		meta := td.GetMetadata().GetRelations()
		for name, rw := range td.GetRelations() {
			r := &Relation{Name: name, Type: t.Name, Rewrite: rw}
			r.DirectTypes = meta[name].GetDirectlyRelatedUserTypes()
			hasThis := containsThis(rw)
			_, onlyThis := rw.GetUserset().(*openfgav1.Userset_This)
			switch {
			case onlyThis:
				r.Kind = Direct
			case hasThis:
				r.Kind = Hybrid
			default:
				r.Kind = Derived
			}
			r.Expr = RenderRewrite(rw, r.DirectTypes)
			r.DerivedExpr = renderDerivedOnly(rw, r.DirectTypes)
			t.Relations[name] = r
		}
		for name := range t.Relations {
			t.RelationNames = append(t.RelationNames, name)
		}
		sort.Slice(t.RelationNames, func(i, j int) bool {
			a, b := t.Relations[t.RelationNames[i]], t.Relations[t.RelationNames[j]]
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			return a.Name < b.Name
		})
		out.Types[t.Name] = t
		out.TypeNames = append(out.TypeNames, t.Name)
	}
	sort.Strings(out.TypeNames)
	out.loadSources()
	return out, nil
}

// Relation looks up a relation by type and name.
func (m *Model) Relation(typ, rel string) (*Relation, bool) {
	t, ok := m.Types[typ]
	if !ok {
		return nil, false
	}
	r, ok := t.Relations[rel]
	return r, ok
}

// ByKind returns the type's relations grouped by kind, in Direct, Derived, Hybrid order.
func (t *Type) ByKind() map[Kind][]*Relation {
	out := map[Kind][]*Relation{}
	for _, n := range t.RelationNames {
		r := t.Relations[n]
		out[r.Kind] = append(out[r.Kind], r)
	}
	return out
}

// Tupleable reports whether tuples can be written for the relation.
func (r *Relation) Tupleable() bool { return r.Kind != Derived }

// AcceptsUsersets reports whether any type restriction is a userset (type#relation).
func (r *Relation) AcceptsUsersets() bool {
	for _, d := range r.DirectTypes {
		if d.GetRelation() != "" {
			return true
		}
	}
	return false
}

// RenderRef renders a type restriction, e.g. "user", "user:*", "group#member", "user with cond".
func RenderRef(ref *openfgav1.RelationReference) string {
	s := ref.GetType()
	switch {
	case ref.GetWildcard() != nil:
		s += ":*"
	case ref.GetRelation() != "":
		s += "#" + ref.GetRelation()
	}
	if c := ref.GetCondition(); c != "" {
		s += " with " + c
	}
	return s
}

// RenderRefs renders a list of type restrictions in DSL form.
func RenderRefs(refs []*openfgav1.RelationReference) string {
	parts := make([]string, len(refs))
	for i, r := range refs {
		parts[i] = RenderRef(r)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// RenderRewrite renders a rewrite as DSL.
func RenderRewrite(u *openfgav1.Userset, direct []*openfgav1.RelationReference) string {
	return render(u, direct, true)
}

func render(u *openfgav1.Userset, direct []*openfgav1.RelationReference, top bool) string {
	switch v := u.GetUserset().(type) {
	case *openfgav1.Userset_This:
		return RenderRefs(direct)
	case *openfgav1.Userset_ComputedUserset:
		return v.ComputedUserset.GetRelation()
	case *openfgav1.Userset_TupleToUserset:
		return v.TupleToUserset.GetComputedUserset().GetRelation() + " from " + v.TupleToUserset.GetTupleset().GetRelation()
	case *openfgav1.Userset_Union:
		return joinChildren(v.Union.GetChild(), " or ", direct, top)
	case *openfgav1.Userset_Intersection:
		return joinChildren(v.Intersection.GetChild(), " and ", direct, top)
	case *openfgav1.Userset_Difference:
		s := render(v.Difference.GetBase(), direct, false) + " but not " + render(v.Difference.GetSubtract(), direct, false)
		if top {
			return s
		}
		return "(" + s + ")"
	}
	return "?"
}

func joinChildren(children []*openfgav1.Userset, sep string, direct []*openfgav1.RelationReference, top bool) string {
	parts := make([]string, len(children))
	for i, c := range children {
		parts[i] = render(c, direct, false)
	}
	s := strings.Join(parts, sep)
	if top {
		return s
	}
	return "(" + s + ")"
}

// renderDerivedOnly renders a hybrid union without its direct branch.
func renderDerivedOnly(u *openfgav1.Userset, direct []*openfgav1.RelationReference) string {
	un, ok := u.GetUserset().(*openfgav1.Userset_Union)
	if !ok {
		return RenderRewrite(u, direct)
	}
	var parts []string
	for _, c := range un.Union.GetChild() {
		if _, isThis := c.GetUserset().(*openfgav1.Userset_This); isThis {
			continue
		}
		parts = append(parts, render(c, direct, false))
	}
	return strings.Join(parts, " or ")
}

func containsThis(u *openfgav1.Userset) bool {
	switch v := u.GetUserset().(type) {
	case *openfgav1.Userset_This:
		return true
	case *openfgav1.Userset_Union:
		for _, c := range v.Union.GetChild() {
			if containsThis(c) {
				return true
			}
		}
	case *openfgav1.Userset_Intersection:
		for _, c := range v.Intersection.GetChild() {
			if containsThis(c) {
				return true
			}
		}
	case *openfgav1.Userset_Difference:
		return containsThis(v.Difference.GetBase()) || containsThis(v.Difference.GetSubtract())
	}
	return false
}

// Dependency is a reference from one relation's rewrite to another relation.
type Dependency struct {
	// Via is the tupleset relation for tuple-to-userset rewrites; empty for computed usersets.
	Via string
	// Relation is the computed relation on the same object (Via == "") or on the
	// objects reached through Via.
	Relation string
	// TargetTypes are the types reachable through Via (only for tuple-to-userset).
	TargetTypes []string
}

// Dependencies lists the computed and tuple-to-userset references in a relation's rewrite.
func (m *Model) Dependencies(r *Relation) []Dependency {
	var out []Dependency
	var walk func(u *openfgav1.Userset)
	walk = func(u *openfgav1.Userset) {
		switch v := u.GetUserset().(type) {
		case *openfgav1.Userset_ComputedUserset:
			out = append(out, Dependency{Relation: v.ComputedUserset.GetRelation()})
		case *openfgav1.Userset_TupleToUserset:
			ts := v.TupleToUserset.GetTupleset().GetRelation()
			cr := v.TupleToUserset.GetComputedUserset().GetRelation()
			d := Dependency{Via: ts, Relation: cr}
			if tr, ok := m.Relation(r.Type, ts); ok {
				for _, ref := range tr.DirectTypes {
					if _, has := m.Relation(ref.GetType(), cr); has {
						d.TargetTypes = appendUnique(d.TargetTypes, ref.GetType())
					}
				}
			}
			out = append(out, d)
		case *openfgav1.Userset_Union:
			for _, c := range v.Union.GetChild() {
				walk(c)
			}
		case *openfgav1.Userset_Intersection:
			for _, c := range v.Intersection.GetChild() {
				walk(c)
			}
		case *openfgav1.Userset_Difference:
			walk(v.Difference.GetBase())
			walk(v.Difference.GetSubtract())
		}
	}
	walk(r.Rewrite)
	return out
}

// Incoming describes a place where instances of a type can appear as the user of a tuple.
type Incoming struct {
	ObjectType string
	Relation   string
	// UsersetRelation is set when the type appears as a userset (type:id#rel).
	UsersetRelation string
	Wildcard        bool
	Condition       string
}

// IncomingFor lists every (object type, relation) whose type restrictions accept
// the given user type, either directly or as a userset.
func (m *Model) IncomingFor(userType string) []Incoming {
	var out []Incoming
	seen := map[string]bool{}
	for _, tn := range m.TypeNames {
		t := m.Types[tn]
		for _, rn := range t.RelationNames {
			for _, ref := range t.Relations[rn].DirectTypes {
				if ref.GetType() != userType {
					continue
				}
				in := Incoming{ObjectType: tn, Relation: rn, UsersetRelation: ref.GetRelation(), Wildcard: ref.GetWildcard() != nil}
				k := fmt.Sprintf("%s|%s|%s|%v", in.ObjectType, in.Relation, in.UsersetRelation, in.Wildcard)
				if seen[k] {
					continue
				}
				seen[k] = true
				out = append(out, in)
			}
		}
	}
	return out
}

// GraphEdge is an edge in the type-level graph.
type GraphEdge struct {
	From, To string
	Label    string
	// Kind is "direct", "userset", "wildcard", or "ttu".
	Kind string
}

// TypeGraph returns the type-level edges: user types that can be related to
// each object type, and tuple-to-userset traversals between types.
func (m *Model) TypeGraph() []GraphEdge {
	var out []GraphEdge
	seen := map[string]bool{}
	add := func(e GraphEdge) {
		k := e.From + "|" + e.To + "|" + e.Label + "|" + e.Kind
		if !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	for _, tn := range m.TypeNames {
		t := m.Types[tn]
		for _, rn := range t.RelationNames {
			r := t.Relations[rn]
			for _, ref := range r.DirectTypes {
				e := GraphEdge{From: ref.GetType(), To: tn, Label: rn, Kind: "direct"}
				if ref.GetWildcard() != nil {
					e.Kind = "wildcard"
					e.Label = rn + " (*)"
				} else if ref.GetRelation() != "" {
					e.Kind = "userset"
					e.Label = rn + " ← #" + ref.GetRelation()
				}
				add(e)
			}
			for _, d := range m.Dependencies(r) {
				if d.Via == "" {
					continue
				}
				for _, tt := range d.TargetTypes {
					add(GraphEdge{From: tt, To: tn, Label: rn + " ← " + d.Relation + " from " + d.Via, Kind: "ttu"})
				}
			}
		}
	}
	return out
}

// HubTypes returns up to n types with the most distinct neighbor types in
// the type graph (ties in TypeNames order): starting points for exploring a model
// too large to draw at once.
func (m *Model) HubTypes(n int) []string {
	nb := map[string]map[string]bool{}
	link := func(a, b string) {
		if nb[a] == nil {
			nb[a] = map[string]bool{}
		}
		nb[a][b] = true
	}
	for _, e := range m.TypeGraph() {
		if e.From != e.To {
			link(e.From, e.To)
			link(e.To, e.From)
		}
	}
	order := append([]string(nil), m.TypeNames...)
	sort.SliceStable(order, func(i, j int) bool { return len(nb[order[i]]) > len(nb[order[j]]) })
	return order[:min(n, len(order))]
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// SplitObject splits "type:id" into its parts.
func SplitObject(s string) (typ, id string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// Subject is a parsed check user: "type:id", "type:*", or "type:id#relation".
type Subject struct {
	Type, ID, Relation string
}

// ParseSubject parses a user string.
func ParseSubject(s string) (Subject, error) {
	obj, rel := s, ""
	if i := strings.LastIndexByte(s, '#'); i >= 0 {
		obj, rel = s[:i], s[i+1:]
	}
	t, id, ok := SplitObject(obj)
	if !ok || id == "" {
		return Subject{}, fmt.Errorf("invalid user %q: expected type:id, type:*, or type:id#relation", s)
	}
	return Subject{Type: t, ID: id, Relation: rel}, nil
}

func (s Subject) String() string {
	out := s.Type + ":" + s.ID
	if s.Relation != "" {
		out += "#" + s.Relation
	}
	return out
}

// IsWildcard reports whether the subject is "type:*".
func (s Subject) IsWildcard() bool { return s.ID == "*" && s.Relation == "" }

// Clause is a plain-language explanation of a rewrite, e.g. "owner of this
// document" or "admin of its org".
type Clause struct {
	// Kind is "any", "all", "except", "direct", "relation" (computed on the
	// same object), or "via" (tuple-to-userset).
	Kind string
	// Relation is the computed relation ("relation") or the relation on the
	// related objects ("via").
	Relation string
	// Via is the tupleset relation for "via" clauses.
	Via         string
	TargetTypes []string
	// Refs are the type restrictions for "direct" clauses.
	Refs     []string
	Children []*Clause
}

// Explain describes who a relation is granted to.
func (m *Model) Explain(r *Relation) *Clause {
	var walk func(u *openfgav1.Userset) *Clause
	walk = func(u *openfgav1.Userset) *Clause {
		switch v := u.GetUserset().(type) {
		case *openfgav1.Userset_This:
			c := &Clause{Kind: "direct"}
			for _, ref := range r.DirectTypes {
				c.Refs = append(c.Refs, RenderRef(ref))
			}
			return c
		case *openfgav1.Userset_ComputedUserset:
			return &Clause{Kind: "relation", Relation: v.ComputedUserset.GetRelation()}
		case *openfgav1.Userset_TupleToUserset:
			ts, cr := v.TupleToUserset.GetTupleset().GetRelation(), v.TupleToUserset.GetComputedUserset().GetRelation()
			c := &Clause{Kind: "via", Relation: cr, Via: ts}
			if tr, ok := m.Relation(r.Type, ts); ok {
				for _, ref := range tr.DirectTypes {
					if _, has := m.Relation(ref.GetType(), cr); has {
						c.TargetTypes = appendUnique(c.TargetTypes, ref.GetType())
					}
				}
			}
			return c
		case *openfgav1.Userset_Union:
			c := &Clause{Kind: "any"}
			for _, ch := range v.Union.GetChild() {
				c.Children = append(c.Children, walk(ch))
			}
			return c
		case *openfgav1.Userset_Intersection:
			c := &Clause{Kind: "all"}
			for _, ch := range v.Intersection.GetChild() {
				c.Children = append(c.Children, walk(ch))
			}
			return c
		case *openfgav1.Userset_Difference:
			return &Clause{Kind: "except", Children: []*Clause{walk(v.Difference.GetBase()), walk(v.Difference.GetSubtract())}}
		}
		return &Clause{Kind: "direct"}
	}
	return walk(r.Rewrite)
}

// SubjectTypes lists the user types that can ultimately hold a relation,
// following computed relations, tuple-to-userset traversals, and usersets.
// Plain types come first (types with no relations of their own, such as
// "user", lead), then usersets like "group#member".
func (m *Model) SubjectTypes(r *Relation) []string {
	seen := map[string]bool{}
	out := map[string]bool{}
	var visit func(typ, rel string)
	visit = func(typ, rel string) {
		key := typ + "#" + rel
		if seen[key] {
			return
		}
		seen[key] = true
		rr, ok := m.Relation(typ, rel)
		if !ok {
			return
		}
		var walk func(u *openfgav1.Userset)
		walk = func(u *openfgav1.Userset) {
			switch v := u.GetUserset().(type) {
			case *openfgav1.Userset_This:
				for _, ref := range rr.DirectTypes {
					if ref.GetRelation() != "" {
						out[ref.GetType()+"#"+ref.GetRelation()] = true
						visit(ref.GetType(), ref.GetRelation())
					} else {
						out[ref.GetType()] = true
					}
				}
			case *openfgav1.Userset_ComputedUserset:
				visit(typ, v.ComputedUserset.GetRelation())
			case *openfgav1.Userset_TupleToUserset:
				cr := v.TupleToUserset.GetComputedUserset().GetRelation()
				if tr, ok := m.Relation(typ, v.TupleToUserset.GetTupleset().GetRelation()); ok {
					for _, ref := range tr.DirectTypes {
						visit(ref.GetType(), cr)
					}
				}
			case *openfgav1.Userset_Union:
				for _, c := range v.Union.GetChild() {
					walk(c)
				}
			case *openfgav1.Userset_Intersection:
				for _, c := range v.Intersection.GetChild() {
					walk(c)
				}
			case *openfgav1.Userset_Difference:
				// Only the base grants; the subtracted side never adds holders.
				walk(v.Difference.GetBase())
			}
		}
		walk(rr.Rewrite)
	}
	visit(r.Type, r.Name)
	var list []string
	for t := range out {
		list = append(list, t)
	}
	rank := func(s string) int {
		switch {
		case s == "user":
			return -1 // the conventional end-user type, when the model has one
		case strings.Contains(s, "#"):
			return 2
		case len(m.Types[s].RelationNames) == 0:
			return 0
		default:
			return 1
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if a, b := rank(list[i]), rank(list[j]); a != b {
			return a < b
		}
		return list[i] < list[j]
	})
	return list
}

// Reachable reports, for each relation of objType, whether a user of
// userType ("user" or a userset such as "group#member") can ever hold it.
// Relations that are unreachable always evaluate to false for that user type.
func (m *Model) Reachable(objType, userType string) map[string]bool {
	t, ok := m.Types[objType]
	if !ok {
		return nil
	}
	out := make(map[string]bool, len(t.RelationNames))
	for _, rn := range t.RelationNames {
		r := t.Relations[rn]
		for _, st := range m.SubjectTypes(r) {
			if st == userType {
				out[rn] = true
				break
			}
		}
		// A userset trivially holds its own relation (Check(group:x#member, member, group:x) is true).
		if userType == objType+"#"+rn {
			out[rn] = true
		}
	}
	return out
}

// UserTypes lists the types and usersets that can appear as the user of a
// tuple somewhere in the model, i.e. the user types a check can be run for.
func (m *Model) UserTypes() []string {
	seen := map[string]bool{}
	var out []string
	for _, tn := range m.TypeNames {
		for _, rn := range m.Types[tn].RelationNames {
			for _, ref := range m.Types[tn].Relations[rn].DirectTypes {
				k := ref.GetType()
				if ref.GetRelation() != "" {
					k += "#" + ref.GetRelation()
				}
				if !seen[k] {
					seen[k] = true
					out = append(out, k)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.Contains(out[i], "#"), strings.Contains(out[j], "#")
		if a != b {
			return !a
		}
		return out[i] < out[j]
	})
	return out
}
