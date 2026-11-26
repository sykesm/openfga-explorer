package trace

import (
	"fmt"
	"slices"
	"strings"
)

// Explanation is a plain-language account of a decision: one headline
// sentence plus the reasons behind it.
type Explanation struct {
	Headline string
	// Facts are ordered from the user up to the object for allowed decisions,
	// and are the blocking reasons for denied ones. Only FactTuple entries are
	// facts in the strict sense (stored tuples); the rest follow from them.
	Facts []Fact
}

// FactKind says where a line of an explanation comes from.
type FactKind string

const (
	// FactTuple is a relationship tuple stored in OpenFGA.
	FactTuple FactKind = "tuple"
	// FactRule is a conclusion the model draws (a computed relation, a
	// relation inherited through a parent, an intersection or exclusion).
	FactRule FactKind = "rule"
	// FactReason explains why something does not hold (denied decisions).
	FactReason FactKind = "reason"
)

// Fact is one line of an explanation.
type Fact struct {
	Kind FactKind
	Text string
}

func (f Fact) String() string { return f.Text }

func tupleFact(format string, a ...any) Fact {
	return Fact{FactTuple, fmt.Sprintf(format, a...)}
}

func ruleFact(format string, a ...any) Fact {
	return Fact{FactRule, fmt.Sprintf(format, a...)}
}

func reasonFact(format string, a ...any) Fact {
	return Fact{FactReason, fmt.Sprintf(format, a...)}
}

// Kinds lists the kinds of line present, tuples first.
func (e Explanation) Kinds() []FactKind {
	var out []FactKind
	for _, k := range []FactKind{FactTuple, FactRule, FactReason} {
		if slices.ContainsFunc(e.Facts, func(f Fact) bool { return f.Kind == k }) {
			out = append(out, k)
		}
	}
	return out
}

// Summary counts the lines by kind, e.g. "2 tuples, 3 rules".
func (e Explanation) Summary() string {
	n := map[FactKind]int{}
	for _, f := range e.Facts {
		n[f.Kind]++
	}
	var parts []string
	for _, k := range e.Kinds() {
		if c := n[k]; c > 0 {
			s := fmt.Sprintf("%d %s", c, k)
			if c != 1 {
				s += "s"
			}
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

// Explain describes the trace in words. name renders object IDs for people
// (e.g. decoding percent-encoded IDs); nil uses them as is.
func (t *Trace) Explanation(name func(string) string) Explanation {
	if name == nil {
		name = func(s string) string { return s }
	}
	x := explainer{name: name, user: t.Request.User}
	root := t.Root
	subject := fmt.Sprintf("%s has %s on %s", name(t.Request.User), t.Request.Relation, name(t.Request.Object))
	switch root.Outcome {
	case Allowed:
		facts := x.grant(root, map[int]bool{})
		e := Explanation{Facts: dedupe(facts)}
		hops := x.hops()
		if len(x.cleared) > 0 && x.requires != "" {
			x.requires += fmt.Sprintf(", and the exclusion %s does not apply", strings.Join(x.cleared, ", "))
		}
		switch {
		case x.requires != "":
			// An "and" is involved: name what was required rather than one chain.
			e.Headline = fmt.Sprintf("Allowed: %s — %s.", subject, x.requires)
		case len(hops) > 1 || (len(hops) == 1 && hops[0] != name(t.Request.Object)):
			e.Headline = fmt.Sprintf("Allowed: %s through %s.", subject, strings.Join(hops, " → "))
		case len(facts) > 0:
			e.Headline = fmt.Sprintf("Allowed: %s because %s.", subject, facts[0])
		default:
			e.Headline = fmt.Sprintf("Allowed: %s.", subject)
		}
		return e
	case Denied:
		reason := x.why(root, 0, map[int]bool{})
		facts := dedupe(x.details)
		// Say when part of the denial rests on the server's word, not the walk.
		if t.GroupsRuledOut > 0 {
			facts = append(facts, reasonFact("%d group(s) were not walked: the server's Check says %s is not in them (per the server's Check; those groups were not walked — trace one to see why)", t.GroupsRuledOut, name(t.Request.User)))
		}
		return Explanation{
			Headline: fmt.Sprintf("Denied: %s does not have %s on %s — %s.", name(t.Request.User), t.Request.Relation, name(t.Request.Object), reason),
			Facts:    facts,
		}
	default:
		return Explanation{Headline: fmt.Sprintf("Undecided: the walk could not settle whether %s within its limits.", subject)}
	}
}

type explainer struct {
	name     func(string) string
	user     string
	requires string   // set when an intersection is part of the granting path
	cleared  []string // exclusions checked on the granting path that did not apply
	path     []string // objects along the granting path, user side first
	details  []Fact
}

// hops lists the intermediate objects of the granting path, user side first.
func (x *explainer) hops() []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range x.path {
		if !seen[p] {
			seen[p] = true
			out = append(out, x.name(p))
		}
	}
	return out
}

func (x *explainer) via(obj string) {
	x.path = append(x.path, obj)
}

// grant returns the facts that make an allowed node allowed, ordered from
// the user upward.
func (x *explainer) grant(n *Node, seen map[int]bool) []Fact {
	if n == nil || seen[n.ID] {
		return nil
	}
	seen[n.ID] = true
	op := n.Op
	if n.Kind == KindUnion || n.Kind == KindIntersection || n.Kind == KindExclusion {
		op = n.Kind
	}
	var out []Fact
	switch op {
	case KindIntersection:
		for _, l := range n.Children {
			if l.Outcome == Allowed {
				out = append(out, x.grantLink(n, l, seen)...)
			}
		}
		var parts []string
		for _, l := range n.Children {
			parts = append(parts, x.describeBranch(l))
		}
		out = append(out, ruleFact("%s on %s requires %s — all hold", n.Relation, x.name(n.Object), strings.Join(parts, " and ")))
		if x.requires == "" {
			x.requires = fmt.Sprintf("%s on %s requires %s, and each holds", n.Relation, x.name(n.Object), strings.Join(parts, " and "))
		}
	case KindExclusion:
		for _, l := range n.Children {
			switch {
			case l.Role == "base" && l.Outcome == Allowed:
				out = append(out, x.grantLink(n, l, seen)...)
			case l.Role == "subtract":
				what := strings.TrimPrefix(x.describeBranch(l), "but not ")
				out = append(out, ruleFact("the exclusion “but not %s” does not apply", what))
				x.cleared = append(x.cleared, "“but not "+what+"”")
			}
		}
	default:
		for _, l := range n.Children {
			if l.Outcome == Allowed && l.Role != "subtract" {
				out = append(out, x.grantLink(n, l, seen)...)
				break
			}
		}
	}
	return out
}

func (x *explainer) grantLink(parent *Node, l *Link, seen map[int]bool) []Fact {
	c := l.Node
	cond := ""
	if l.Cond != nil {
		cond = fmt.Sprintf(" (condition %s is met)", l.Cond.Name)
	}
	switch {
	case c.Kind == KindSubject && l.Tuple != nil:
		if strings.HasSuffix(c.Label, ":*") {
			return []Fact{tupleFact("everyone (%s) has %s on %s%s", c.Label, l.Tuple.Relation, x.name(l.Tuple.Object), cond)}
		}
		x.via(l.Tuple.Object)
		return []Fact{tupleFact("%s has %s on %s%s", x.name(l.Tuple.User), l.Tuple.Relation, x.name(l.Tuple.Object), cond)}
	case l.Tuple != nil && c.Kind == KindRelation && strings.Contains(l.Tuple.User, "#"):
		// Userset edge: members of the group hold the relation.
		facts := x.grant(c, seen)
		x.via(l.Tuple.Object)
		return append(facts, tupleFact("%s has %s on %s%s", x.name(l.Tuple.User), l.Tuple.Relation, x.name(l.Tuple.Object), cond))
	case l.Tuple != nil && c.Kind == KindRelation:
		// Tuple-to-userset edge: the object inherits through its parent (or org, …).
		facts := x.grant(c, seen)
		x.via(l.Tuple.Object)
		branch := parent.Relation
		if parent.Kind == KindTTU {
			branch = parent.Label // e.g. "viewer from parent"
		}
		// The stored tuple, then the rule that carries the relation across it.
		return append(facts,
			tupleFact("%s's %s is %s%s", x.name(l.Tuple.Object), l.Tuple.Relation, x.name(l.Tuple.User), cond),
			ruleFact("%s on %s counts as “%s” on %s", c.Relation, x.name(l.Tuple.User), branch, x.name(l.Tuple.Object)))
	case c.Kind == KindRelation && c.Object == parent.Object && l.Label == "computed":
		facts := x.grant(c, seen)
		return append(facts, ruleFact("%s implies %s on %s", c.Relation, parent.Relation, x.name(parent.Object)))
	default:
		return x.grant(c, seen)
	}
}

// describeBranch names a rewrite branch, e.g. "editor" or "member from org".
func (x *explainer) describeBranch(l *Link) string {
	n := l.Node
	switch n.Kind {
	case KindRelation:
		return n.Relation
	case KindDirect:
		return "a direct tuple"
	case KindTTU, KindSkipped:
		return n.Label
	case KindUnion, KindIntersection, KindExclusion:
		return "(" + n.Expr + ")"
	}
	return n.Label
}

// why explains a denied node in one clause and records supporting details.
func (x *explainer) why(n *Node, depth int, seen map[int]bool) string {
	if seen[n.ID] {
		return "no " + n.Relation
	}
	seen[n.ID] = true
	op := n.Op
	if n.Kind == KindUnion || n.Kind == KindIntersection || n.Kind == KindExclusion {
		op = n.Kind
	}
	switch op {
	case KindExclusion:
		for _, l := range n.Children {
			if l.Role == "subtract" && l.Blocking {
				// The exclusion applied: explain how the user got the excluded relation.
				sub := &explainer{name: x.name, user: x.user}
				facts := sub.grant(l.Node, map[int]bool{})
				x.details = append(x.details, facts...)
				what := strings.TrimPrefix(x.describeBranch(l), "but not ")
				if len(facts) > 0 {
					// The user-side fact is the one people act on (e.g. "is blocked on the org").
					return fmt.Sprintf("they are excluded by “but not %s” because %s", what, facts[0])
				}
				return fmt.Sprintf("they are excluded by “but not %s”", what)
			}
		}
		for _, l := range n.Children {
			if l.Role == "base" && l.Outcome == Denied {
				return x.why(l.Node, depth+1, seen)
			}
		}
	case KindIntersection:
		var req, missing []string
		for _, l := range n.Children {
			req = append(req, x.describeBranch(l))
			if l.Outcome == Denied {
				missing = append(missing, x.describeBranch(l))
				if depth < 3 {
					x.details = append(x.details, reasonFact("%s: %s", x.describeBranch(l), x.why(l.Node, depth+1, seen)))
				}
			}
		}
		return fmt.Sprintf("%s requires %s, and %s does not hold", n.Relation, strings.Join(req, " and "), strings.Join(missing, ", "))
	}
	switch n.Kind {
	case KindDirect:
		for _, l := range n.Children {
			if l.Cond != nil && l.Outcome != Allowed && l.Tuple != nil {
				return fmt.Sprintf("the %s tuple's condition %s is %s", l.Tuple.Relation, l.Cond.Name, condWord(string(l.Cond.Status)))
			}
		}
		ruled := 0
		for _, l := range n.Children {
			if l.Outcome == Skipped && l.Node != nil && l.Node.Hint == Denied {
				ruled++
			}
		}
		if ruled > 0 {
			return fmt.Sprintf("there is no direct tuple, and they are not in any of the %d group(s) that grant it (per the server's Check; those groups were not walked)", ruled)
		}
		return "there is no direct tuple"
	case KindTTU:
		if strings.HasPrefix(n.Reason, "no ") {
			return n.Reason
		}
		var parents []string
		for _, l := range n.Children {
			if l.Tuple != nil {
				parents = append(parents, x.name(l.Tuple.User))
			}
		}
		if len(parents) > 0 {
			return fmt.Sprintf("not %s (on %s)", n.Label, strings.Join(parents, ", "))
		}
		return "not " + n.Label
	case KindCycle:
		return "a cycle"
	case KindBlocked:
		return n.Reason
	}
	// A union (or a single-branch relation): every branch failed.
	var parts []string
	for _, l := range n.Children {
		if l.Outcome == Skipped {
			continue
		}
		c := l.Node
		switch c.Kind {
		case KindRelation:
			if c.Object == n.Object {
				parts = append(parts, "no "+c.Relation)
			} else if l.Tuple != nil && strings.Contains(l.Tuple.User, "#") {
				parts = append(parts, fmt.Sprintf("not in %s", x.name(l.Tuple.User)))
			} else {
				parts = append(parts, x.why(c, depth+1, seen))
			}
		case KindBlocked:
			parts = append(parts, c.Reason)
		default:
			parts = append(parts, x.why(c, depth+1, seen))
		}
		if len(parts) >= 6 {
			parts = append(parts, "…")
			break
		}
	}
	if len(parts) == 0 {
		return "nothing grants " + n.Relation
	}
	if depth == 0 && len(parts) > 1 {
		return "none of the ways to get it applies: " + strings.Join(parts, "; ")
	}
	return strings.Join(parts, "; ")
}

func condWord(status string) string {
	switch status {
	case "not-met":
		return "false"
	case "missing-params":
		return "missing parameters"
	default:
		return "an error"
	}
}

func dedupe(in []Fact) []Fact {
	seen := map[Fact]bool{}
	var out []Fact
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
