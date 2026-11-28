package schema

import (
	"cmp"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
)

// Model checks find things OpenFGA accepts but that are almost always
// mistakes: rules that can never grant access, "X from Y" parents that can
// never contribute, and unused conditions or parameters.

// Severity ranks a finding.
type Severity string

const (
	// Warning: very likely a mistake (something that can never work).
	Warning Severity = "warning"
	// Notice: possibly intended, worth a look.
	Notice Severity = "notice"
)

// Finding is one model check result. Type, Relation, and Condition locate it.
type Finding struct {
	Severity  Severity
	Code      string // stable identifier, e.g. "unreachable-relation"
	Type      string
	Relation  string
	Condition string
	Message   string
	// Source is the module and file of the definition (modular models).
	Source Source
}

// Check runs the model checks (once per model; models never change).
// Findings are ordered by severity, then location.
func (m *Model) Check() []Finding {
	m.checkOnce.Do(func() { m.findings = m.runChecks() })
	return m.findings
}

func (m *Model) runChecks() []Finding {
	var out []Finding
	out = append(out, m.checkUnreachable()...)
	out = append(out, m.checkTupleToUserset()...)
	out = append(out, m.checkConditions()...)
	out = append(out, m.checkOrphanTypes()...)
	rank := map[Severity]int{Warning: 0, Notice: 1}
	slices.SortStableFunc(out, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(rank[a.Severity], rank[b.Severity]), cmp.Compare(a.Type, b.Type),
			cmp.Compare(a.Relation, b.Relation), cmp.Compare(a.Condition, b.Condition))
	})
	return out
}

// FindingsFor returns the findings about one relation.
func FindingsFor(fs []Finding, typ, rel string) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Type == typ && f.Relation == rel {
			out = append(out, f)
		}
	}
	return out
}

// checkUnreachable: relations no kind of user can ever hold, e.g. an
// intersection with a branch nothing grants, or a cycle of relations with no
// way in. (Reachable, which greys out relations in the check form, is
// deliberately optimistic; this needs the exact answer.)
func (m *Model) checkUnreachable() []Finding {
	var out []Finding
	for _, tn := range m.TypeNames {
		t := m.Types[tn]
		for _, rn := range t.RelationNames {
			if len(m.Holders(tn, rn)) == 0 {
				out = append(out, Finding{Severity: Warning, Code: "unreachable-relation", Type: tn, Relation: rn, Source: t.Relations[rn].Source,
					Message: fmt.Sprintf("%s#%s can never be granted: no type of user can satisfy %s", tn, rn, t.Relations[rn].Expr)})
			}
		}
	}
	return out
}

// holders computes, for every relation, the user types (and usersets) that
// can hold it given suitable tuples: the least fixed point of the rewrite
// rules, where "and" needs every branch and "but not" keeps the base.
func (m *Model) holders() map[string]map[string]bool {
	h := map[string]map[string]bool{}
	for _, tn := range m.TypeNames {
		for _, rn := range m.Types[tn].RelationNames {
			h[tn+"#"+rn] = map[string]bool{}
		}
	}
	var eval func(typ string, r *Relation, u *openfgav1.Userset) map[string]bool
	eval = func(typ string, r *Relation, u *openfgav1.Userset) map[string]bool {
		out := map[string]bool{}
		add := func(set map[string]bool) {
			for k := range set {
				out[k] = true
			}
		}
		switch v := u.GetUserset().(type) {
		case *openfgav1.Userset_This:
			for _, ref := range r.DirectTypes {
				if ref.GetRelation() != "" {
					us := ref.GetType() + "#" + ref.GetRelation()
					out[us] = true
					add(h[us]) // whoever holds the userset holds this too
				} else {
					out[ref.GetType()] = true
				}
			}
		case *openfgav1.Userset_ComputedUserset:
			add(h[typ+"#"+v.ComputedUserset.GetRelation()])
		case *openfgav1.Userset_TupleToUserset:
			if ts, ok := m.Relation(typ, v.TupleToUserset.GetTupleset().GetRelation()); ok {
				for _, ref := range ts.DirectTypes {
					add(h[ref.GetType()+"#"+v.TupleToUserset.GetComputedUserset().GetRelation()])
				}
			}
		case *openfgav1.Userset_Union:
			for _, c := range v.Union.GetChild() {
				add(eval(typ, r, c))
			}
		case *openfgav1.Userset_Intersection:
			for i, c := range v.Intersection.GetChild() {
				set := eval(typ, r, c)
				if i == 0 {
					add(set)
					continue
				}
				for k := range out {
					if !set[k] {
						delete(out, k)
					}
				}
			}
		case *openfgav1.Userset_Difference:
			add(eval(typ, r, v.Difference.GetBase()))
		}
		return out
	}
	for changed := true; changed; {
		changed = false
		for _, tn := range m.TypeNames {
			t := m.Types[tn]
			for _, rn := range t.RelationNames {
				key := tn + "#" + rn
				for k := range eval(tn, t.Relations[rn], t.Relations[rn].Rewrite) {
					if !h[key][k] {
						h[key][k] = true
						changed = true
					}
				}
			}
		}
	}
	return h
}

// checkTupleToUserset: in "X from Y", every type Y may hold should have
// relation X; parents of the other types silently never grant anything.
func (m *Model) checkTupleToUserset() []Finding {
	var out []Finding
	for _, tn := range m.TypeNames {
		t := m.Types[tn]
		for _, rn := range t.RelationNames {
			r := t.Relations[rn]
			seen := map[string]bool{}
			for u := range Leaves(r.Rewrite) {
				ttu := u.GetTupleToUserset()
				if ttu == nil {
					continue
				}
				via, target := ttu.GetTupleset().GetRelation(), ttu.GetComputedUserset().GetRelation()
				ts, ok := t.Relations[via]
				if !ok {
					continue
				}
				var missing []string
				for _, ref := range ts.DirectTypes {
					if ref.GetRelation() != "" || ref.GetWildcard() != nil || seen[ref.GetType()+"#"+target] {
						continue
					}
					seen[ref.GetType()+"#"+target] = true
					if _, ok := m.Relation(ref.GetType(), target); !ok {
						missing = append(missing, ref.GetType())
					}
				}
				if len(missing) > 0 {
					out = append(out, Finding{Severity: Warning, Code: "from-missing-relation", Type: tn, Relation: rn, Source: r.Source,
						Message: fmt.Sprintf("%s#%s uses “%s from %s”, but %s can be a %s, which has no %s: those never grant %s",
							tn, rn, target, via, via, strings.Join(missing, " or "), target, rn)})
				}
			}
		}
	}
	return out
}

// checkConditions: conditions no type restriction uses, and parameters a
// condition's expression never reads.
func (m *Model) checkConditions() []Finding {
	used := map[string]bool{}
	for _, t := range m.Types {
		for _, r := range t.Relations {
			for _, ref := range r.DirectTypes {
				if c := ref.GetCondition(); c != "" {
					used[c] = true
				}
			}
		}
	}
	var out []Finding
	for _, name := range slices.Sorted(maps.Keys(m.Conditions)) {
		c := m.Conditions[name]
		src := m.ConditionSources[name]
		if !used[name] {
			out = append(out, Finding{Severity: Notice, Code: "unused-condition", Condition: name, Source: src,
				Message: fmt.Sprintf("condition %s is not used by any type restriction (no tuple can carry it)", name)})
		}
		for _, p := range slices.Sorted(maps.Keys(c.GetParameters())) {
			if !regexp.MustCompile(`\b` + regexp.QuoteMeta(p) + `\b`).MatchString(c.GetExpression()) {
				out = append(out, Finding{Severity: Notice, Code: "unused-parameter", Condition: name, Source: src,
					Message: fmt.Sprintf("condition %s declares parameter %s but its expression never uses it", name, p)})
			}
		}
	}
	return out
}

// checkOrphanTypes: types with no relations that nothing refers to.
func (m *Model) checkOrphanTypes() []Finding {
	referenced := map[string]bool{}
	for _, t := range m.Types {
		for _, r := range t.Relations {
			for _, ref := range r.DirectTypes {
				referenced[ref.GetType()] = true
			}
		}
	}
	var out []Finding
	for _, tn := range m.TypeNames {
		t := m.Types[tn]
		if len(t.Relations) == 0 && !referenced[tn] {
			out = append(out, Finding{Severity: Notice, Code: "orphan-type", Type: tn, Source: t.Source,
				Message: fmt.Sprintf("type %s has no relations and no relation refers to it", tn)})
		}
	}
	return out
}
