package schema

import (
	"slices"
	"sort"
	"strings"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
)

// ModelDiff compares two versions of a model: what changed in the
// definitions, and what changed in who can hold each relation. The second
// part catches relations whose definition is unchanged but whose access
// changed through a relation they depend on.
type ModelDiff struct {
	TypesAdded, TypesRemoved []string
	Relations                []RelationChange
	Conditions               []ConditionChange
	// Routes lists relations in both versions whose ways of being granted
	// changed: a route lost here can cut access for real people even when
	// the kinds of user that can hold the relation (Access) stay the same.
	Routes []RouteChange
	// Access lists relations in both versions whose holders (kinds of user)
	// changed.
	Access []AccessChange
}

// RouteChange lists the ways a relation can be granted (and the exclusions
// that limit it) that one version has and the other lacks. Routes include
// those reached through other relations, so a change to editor shows on
// viewer when viewer is granted through editor.
type RouteChange struct {
	Type, Relation string
	Lost, Gained   []string
	// ExclusionsAdded remove access; ExclusionsRemoved restore it.
	ExclusionsAdded, ExclusionsRemoved []string
	// Direct is set when the relation's own rule changed.
	Direct bool
}

// RelationChange is an added, removed, changed, or moved relation.
type RelationChange struct {
	Type, Relation string
	Change         string // "added", "removed", "changed", or "moved" (same rule, another module)
	Old, New       string // the rules, e.g. "[user] or owner"
	OldSource      Source
	NewSource      Source
	// Holders are the user types that can hold an added relation.
	Holders []string
}

// ConditionChange is an added, removed, or changed condition.
type ConditionChange struct {
	Name     string
	Change   string
	Old, New string // "(params) expression"
}

// AccessChange lists the user types that gained or lost a relation.
type AccessChange struct {
	Type, Relation string
	Gained, Lost   []string
	// Direct is set when the relation's own rule changed; otherwise the
	// change comes from a relation it depends on.
	Direct bool
}

// Empty reports whether the versions are equivalent.
func (d *ModelDiff) Empty() bool {
	return len(d.TypesAdded)+len(d.TypesRemoved)+len(d.Relations)+len(d.Conditions)+len(d.Routes)+len(d.Access) == 0
}

// Diff compares an older and a newer model.
func Diff(old, new *Model) *ModelDiff {
	d := &ModelDiff{}
	for _, tn := range new.TypeNames {
		if old.Types[tn] == nil {
			d.TypesAdded = append(d.TypesAdded, tn)
		}
	}
	for _, tn := range old.TypeNames {
		if new.Types[tn] == nil {
			d.TypesRemoved = append(d.TypesRemoved, tn)
		}
	}
	types := unionNames(old.TypeNames, new.TypeNames)
	for _, tn := range types {
		ot, nt := old.Types[tn], new.Types[tn]
		var rels []string
		if ot != nil {
			rels = append(rels, ot.RelationNames...)
		}
		if nt != nil {
			rels = append(rels, nt.RelationNames...)
		}
		for _, rn := range unionNames(rels) {
			or, oOK := old.Relation(tn, rn)
			nr, nOK := new.Relation(tn, rn)
			switch {
			case !oOK:
				d.Relations = append(d.Relations, RelationChange{Type: tn, Relation: rn, Change: "added", New: nr.Expr, NewSource: nr.Source,
					Holders: new.Holders(tn, rn)})
			case !nOK:
				d.Relations = append(d.Relations, RelationChange{Type: tn, Relation: rn, Change: "removed", Old: or.Expr, OldSource: or.Source})
			default:
				changed := or.Expr != nr.Expr
				if changed {
					d.Relations = append(d.Relations, RelationChange{Type: tn, Relation: rn, Change: "changed", Old: or.Expr, New: nr.Expr, OldSource: or.Source, NewSource: nr.Source})
				} else if or.Source != nr.Source {
					d.Relations = append(d.Relations, RelationChange{Type: tn, Relation: rn, Change: "moved", Old: or.Expr, New: nr.Expr, OldSource: or.Source, NewSource: nr.Source})
				}
				ob, oe := old.Routes(tn, rn)
				nb, ne := new.Routes(tn, rn)
				gainedR, lostR := setDiff(ob, nb)
				exclAdded, exclRemoved := setDiff(oe, ne)
				if len(gainedR)+len(lostR)+len(exclAdded)+len(exclRemoved) > 0 {
					d.Routes = append(d.Routes, RouteChange{Type: tn, Relation: rn, Lost: lostR, Gained: gainedR,
						ExclusionsAdded: exclAdded, ExclusionsRemoved: exclRemoved, Direct: changed})
				}
				gained, lost := setDiff(old.Holders(tn, rn), new.Holders(tn, rn))
				if len(gained)+len(lost) > 0 {
					d.Access = append(d.Access, AccessChange{Type: tn, Relation: rn, Gained: gained, Lost: lost, Direct: changed})
				}
			}
		}
	}
	for _, name := range unionNames(mapKeys(old), mapKeys(new)) {
		oc, nc := conditionText(old, name), conditionText(new, name)
		switch {
		case oc == "":
			d.Conditions = append(d.Conditions, ConditionChange{Name: name, Change: "added", New: nc})
		case nc == "":
			d.Conditions = append(d.Conditions, ConditionChange{Name: name, Change: "removed", Old: oc})
		case oc != nc:
			d.Conditions = append(d.Conditions, ConditionChange{Name: name, Change: "changed", Old: oc, New: nc})
		}
	}
	return d
}

// Holders lists the user types (and usersets) that can hold a relation
// given suitable tuples. Computed once per model.
func (m *Model) Holders(typ, rel string) []string {
	m.holdersOnce.Do(func() { m.holderSets = m.holders() })
	var out []string
	for k := range m.holderSets[typ+"#"+rel] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Routes lists the ways a relation can be granted, transitively: direct
// assignments ("direct user", "direct group#member with cond"), and the
// relations it is granted through ("doc#editor", "folder#viewer via parent"),
// including theirs. Exclusions are the relations subtracted ("but not") on
// the way. Both are sorted.
func (m *Model) Routes(typ, rel string) (routes, exclusions []string) {
	r, e := map[string]bool{}, map[string]bool{}
	seen := map[string]bool{}
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
		for u, exclude := range Leaves(rr.Rewrite) {
			add := func(s string) {
				if exclude {
					e[s] = true
				} else {
					r[s] = true
				}
			}
			switch v := u.GetUserset().(type) {
			case *openfgav1.Userset_This:
				for _, ref := range rr.DirectTypes {
					s := "direct " + ref.GetType()
					switch {
					case ref.GetWildcard() != nil:
						s += ":*"
					case ref.GetRelation() != "":
						s += "#" + ref.GetRelation()
					}
					if c := ref.GetCondition(); c != "" {
						s += " with " + c
					}
					add(s + " on " + key)
				}
			case *openfgav1.Userset_ComputedUserset:
				add(typ + "#" + v.ComputedUserset.GetRelation())
				if !exclude {
					visit(typ, v.ComputedUserset.GetRelation())
				}
			case *openfgav1.Userset_TupleToUserset:
				via := v.TupleToUserset.GetTupleset().GetRelation()
				cr := v.TupleToUserset.GetComputedUserset().GetRelation()
				if ts, ok := m.Relation(typ, via); ok {
					for _, ref := range ts.DirectTypes {
						if _, ok := m.Relation(ref.GetType(), cr); ok {
							add(ref.GetType() + "#" + cr + " via " + typ + "#" + via)
							if !exclude {
								visit(ref.GetType(), cr)
							}
						}
					}
				}
			}
		}
	}
	visit(typ, rel)
	return sortedSet(r), sortedSet(e)
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func conditionText(m *Model, name string) string {
	c, ok := m.Conditions[name]
	if !ok {
		return ""
	}
	var params []string
	for p, t := range c.GetParameters() {
		params = append(params, p+": "+paramType(t))
	}
	sort.Strings(params)
	return "(" + strings.Join(params, ", ") + ") " + strings.TrimSpace(c.GetExpression())
}

// paramType renders a condition parameter type, e.g. "list<string>".
func paramType(t *openfgav1.ConditionParamTypeRef) string {
	s := strings.ToLower(strings.TrimPrefix(t.GetTypeName().String(), "TYPE_NAME_"))
	if g := t.GetGenericTypes(); len(g) > 0 {
		var parts []string
		for _, x := range g {
			parts = append(parts, paramType(x))
		}
		s += "<" + strings.Join(parts, ", ") + ">"
	}
	return s
}

func mapKeys(m *Model) []string {
	out := make([]string, 0, len(m.Conditions))
	for k := range m.Conditions {
		out = append(out, k)
	}
	return out
}

func unionNames(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, s := range l {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

// setDiff returns what b has that a lacks, and what a has that b lacks.
func setDiff(a, b []string) (gained, lost []string) {
	for _, s := range b {
		if !slices.Contains(a, s) {
			gained = append(gained, s)
		}
	}
	for _, s := range a {
		if !slices.Contains(b, s) {
			lost = append(lost, s)
		}
	}
	return gained, lost
}
