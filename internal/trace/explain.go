package trace

import (
	"fmt"
	"strings"
)

// Explanation is a plain-language account of a decision: one headline
// sentence plus the facts behind it.
type Explanation struct {
	Headline string
	// Facts are ordered from the user up to the object for allowed decisions,
	// and are the blocking reasons for denied ones.
	Facts []string
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
			facts = append(facts, fmt.Sprintf("%d group(s) were not walked: the server's Check says %s is not in them (per the server's Check; those groups were not walked — trace one to see why)", t.GroupsRuledOut, name(t.Request.User)))
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
	details  []string
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
func (x *explainer) grant(n *Node, seen map[int]bool) []string {
	if n == nil || seen[n.ID] {
		return nil
	}
	seen[n.ID] = true
	op := n.Op
	if n.Kind == KindUnion || n.Kind == KindIntersection || n.Kind == KindExclusion {
		op = n.Kind
	}
	var out []string
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
		out = append(out, fmt.Sprintf("%s on %s requires %s — all hold", n.Relation, x.name(n.Object), strings.Join(parts, " and ")))
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
				out = append(out, fmt.Sprintf("the exclusion “but not %s” does not apply", what))
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

func (x *explainer) grantLink(parent *Node, l *Link, seen map[int]bool) []string {
	c := l.Node
	cond := ""
	if l.Cond != nil {
		cond = fmt.Sprintf(" (condition %s is met)", l.Cond.Name)
	}
	switch {
	case c.Kind == KindSubject && l.Tuple != nil:
		if strings.HasSuffix(c.Label, ":*") {
			return []string{fmt.Sprintf("everyone (%s) has %s on %s%s", c.Label, l.Tuple.Relation, x.name(l.Tuple.Object), cond)}
		}
		x.via(l.Tuple.Object)
		return []string{fmt.Sprintf("%s has %s on %s%s", x.name(l.Tuple.User), l.Tuple.Relation, x.name(l.Tuple.Object), cond)}
	case l.Tuple != nil && c.Kind == KindRelation && strings.Contains(l.Tuple.User, "#"):
		// Userset edge: members of the group hold the relation.
		facts := x.grant(c, seen)
		x.via(l.Tuple.Object)
		return append(facts, fmt.Sprintf("%s has %s on %s%s", x.name(l.Tuple.User), l.Tuple.Relation, x.name(l.Tuple.Object), cond))
	case l.Tuple != nil && c.Kind == KindRelation:
		// Tuple-to-userset edge: the object inherits through its parent (or org, …).
		facts := x.grant(c, seen)
		x.via(l.Tuple.Object)
		branch := parent.Relation
		if parent.Kind == KindTTU {
			branch = parent.Label // e.g. "viewer from parent"
		}
		return append(facts, fmt.Sprintf("%s's %s is %s, so %s there counts as “%s” on %s%s",
			x.name(l.Tuple.Object), l.Tuple.Relation, x.name(l.Tuple.User), c.Relation, branch, x.name(l.Tuple.Object), cond))
	case c.Kind == KindRelation && c.Object == parent.Object && l.Label == "computed":
		facts := x.grant(c, seen)
		return append(facts, fmt.Sprintf("%s implies %s on %s", c.Relation, parent.Relation, x.name(parent.Object)))
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
					x.details = append(x.details, fmt.Sprintf("%s: %s", x.describeBranch(l), x.why(l.Node, depth+1, seen)))
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

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
