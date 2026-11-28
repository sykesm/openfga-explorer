package trace

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sykesm/openfga-explorer/internal/cond"
)

// Explanation is a plain-language account of a decision: one headline
// sentence plus the steps behind it.
type Explanation struct {
	Headline string
	// Steps run from the user up to the object for allowed decisions, each
	// saying what it establishes for the user; for denied decisions they are
	// the reasons, from what the object requires down to what is missing.
	Steps []WhyStep
}

// WhyStep is one step of an explanation: a sentence, and the stored tuples and
// model rules it rests on.
type WhyStep struct {
	Text string
	// Reason marks a step that says why something does not hold.
	Reason   bool
	Evidence []Evidence
	// from holds the text of the steps this one builds on (see cite).
	from []string
	// base is Text before cite added "(from step N)".
	base string
	// down is the sentence for the top-down reading ("X, because Y"); empty
	// when Text reads both ways.
	down string
}

func (s WhyStep) String() string { return s.Text }

// EvidenceKind says what a step rests on.
type EvidenceKind string

const (
	// EvidenceTuple is a relationship tuple (stored, or contextual).
	EvidenceTuple EvidenceKind = "tuple"
	// EvidenceRule is part of the model: the definition of a relation.
	EvidenceRule EvidenceKind = "rule"
)

// Evidence is a tuple or a model rule a step rests on.
type Evidence struct {
	Kind EvidenceKind
	// Tuple is the tuple, IDs as stored (EvidenceTuple).
	Tuple Tuple
	// Ref is the relation the tuple is written under, or the rule's relation.
	Ref Ref
}

// Ref names a relation of the model as it applies to one object: the
// relation is defined on the object's type.
type Ref struct {
	Object, Relation string
	// Via is the part of the relation's definition the step uses, as the
	// model writes it: a type restriction for a tuple ("group#member",
	// "user with cond"), a branch for a rule ("viewer from parent"). Empty
	// when the step rests on the whole definition.
	Via string
}

// Type is the object's type ("document" for "document:readme").
func (r Ref) Type() string {
	t, _, _ := strings.Cut(r.Object, ":")
	return t
}

// String is "type#relation", or empty for the zero Ref.
func (r Ref) String() string {
	if r.Relation == "" {
		return ""
	}
	return r.Type() + "#" + r.Relation
}

// on is the Ref for the relation a node evaluates, through one part of it.
func on(n *Node, via string) Ref { return Ref{n.Object, n.Relation, via} }

// userRef is how a type restriction names a tuple's user: "user" for
// "user:anne", "user:*" for everyone, "group#member" for "group:eng#member".
func userRef(user string) string {
	typ, rest, _ := strings.Cut(user, ":")
	if rest == "*" {
		return typ + ":*"
	}
	if _, after, ok := strings.CutLast(rest, "#"); ok {
		return typ + "#" + after
	}
	return typ
}

// tupleEv is a link's tuple as evidence, through the type restriction its
// user matches.
func tupleEv(l *Link) Evidence {
	via := userRef(l.Tuple.User)
	if l.Cond != nil {
		via += " with " + l.Cond.Name
	}
	return Evidence{Kind: EvidenceTuple, Tuple: *l.Tuple, Ref: Ref{l.Tuple.Object, l.Tuple.Relation, via}}
}

func ruleEv(ref Ref) Evidence { return Evidence{Kind: EvidenceRule, Ref: ref} }

func step(text string, ev ...Evidence) WhyStep { return WhyStep{Text: text, Evidence: ev} }

// withDown sets the sentence for the top-down reading.
func (s WhyStep) withDown(text string) WhyStep { s.down = text; return s }

func reason(ref Ref, format string, a ...any) WhyStep {
	s := WhyStep{Text: fmt.Sprintf(format, a...), Reason: true}
	if ref.Relation != "" {
		s.Evidence = []Evidence{ruleEv(ref)}
	}
	return s
}

// Summary counts the steps and the tuples they rest on, e.g. "4 steps,
// from 3 tuples".
func (e Explanation) Summary() string {
	tuples := map[Tuple]bool{}
	for _, s := range e.Steps {
		for _, ev := range s.Evidence {
			if ev.Kind == EvidenceTuple {
				tuples[ev.Tuple] = true
			}
		}
	}
	out := plural(len(e.Steps), "step")
	if len(tuples) > 0 {
		out += ", from " + plural(len(tuples), "tuple")
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Explanation describes the trace in words. name renders object IDs for people
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
		steps := cite(dedupe(x.grant(root, map[int]bool{}, false)))
		e := Explanation{Steps: steps}
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
		case len(steps) > 0:
			e.Headline = fmt.Sprintf("Allowed: %s because %s.", subject, steps[0])
		default:
			e.Headline = fmt.Sprintf("Allowed: %s.", subject)
		}
		return e
	case Denied:
		why := x.why(root, 0, map[int]bool{})
		steps := cite(dedupe(x.details))
		// Say when part of the denial rests on the server's word, not the walk.
		if t.GroupsRuledOut > 0 {
			steps = append(steps, reason(Ref{}, "%d group(s) were not walked: the server's Check says %s is not in them (per the server's Check; those groups were not walked — trace one to see why)", t.GroupsRuledOut, name(t.Request.User)))
		}
		return Explanation{
			Headline: fmt.Sprintf("Denied: %s does not have %s on %s — %s.", name(t.Request.User), t.Request.Relation, name(t.Request.Object), why),
			Steps:    steps,
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
	details  []WhyStep
	// last is the last step that established each node, so a later step
	// resting on a node already explained can still cite it.
	last map[int]string
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

// grant returns the steps that make an allowed node allowed, ordered from
// the user upward. part is set inside an intersection or exclusion, where a
// branch establishes only part of its relation, so its step must not claim
// the whole relation.
func (x *explainer) grant(n *Node, seen map[int]bool, part bool) []WhyStep {
	if n == nil || seen[n.ID] {
		return nil
	}
	seen[n.ID] = true
	out := x.grantNode(n, seen, part)
	if len(out) > 0 {
		if x.last == nil {
			x.last = map[int]string{}
		}
		x.last[n.ID] = out[len(out)-1].Text
	}
	return out
}

// grantNode is grant for a node not yet explained.
func (x *explainer) grantNode(n *Node, seen map[int]bool, part bool) []WhyStep {
	op := n.Op
	if n.Kind == KindUnion || n.Kind == KindIntersection || n.Kind == KindExclusion {
		op = n.Kind
	}
	who := x.name(x.user)
	var out []WhyStep
	var from []string // intersection: the last step of each branch
	switch op {
	case KindIntersection:
		for _, l := range n.Children {
			if l.Outcome == Allowed {
				branch := x.grantLink(n, l, seen, true)
				if len(branch) > 0 {
					from = append(from, branch[len(branch)-1].Text)
				} else if t, ok := x.last[l.Node.ID]; ok {
					from = append(from, t)
				}
				out = append(out, branch...)
			}
		}
		var parts []string
		for _, l := range n.Children {
			parts = append(parts, quoted(x.describeBranch(l)))
		}
		all := strings.Join(parts, " and ")
		each := "both "
		if len(parts) > 2 {
			each = "all of "
		}
		if part {
			out = append(out, WhyStep{Text: fmt.Sprintf("so %s has %s on %s", who, all, x.name(n.Object)), Evidence: []Evidence{ruleEv(on(n, ""))}, from: from,
				down: fmt.Sprintf("%s has %s%s on %s", who, each, all, x.name(n.Object))})
		} else {
			out = append(out, WhyStep{Text: fmt.Sprintf("so %s has %s on %s, which requires %s", who, n.Relation, x.name(n.Object), all), Evidence: []Evidence{ruleEv(on(n, ""))}, from: from,
				down: fmt.Sprintf("%s has %s on %s, because they have %s%s", who, n.Relation, x.name(n.Object), each, all)})
		}
		if x.requires == "" {
			x.requires = fmt.Sprintf("%s on %s requires %s, and each holds", n.Relation, x.name(n.Object), all)
		}
	case KindExclusion:
		for _, l := range n.Children {
			switch {
			case l.Role == "base" && l.Outcome == Allowed:
				out = append(out, x.grantLink(n, l, seen, true)...)
			case l.Role == "subtract":
				what := strings.TrimPrefix(x.describeBranch(l), "but not ")
				ev := ruleEv(on(n, "but not "+what))
				if part {
					out = append(out, step(fmt.Sprintf("%s does not have %s, so “but not %s” does not apply", who, quoted(what), what), ev))
				} else {
					out = x.after(nil, out, step(fmt.Sprintf("so %s has %s on %s: they do not have %s, so “but not %s” does not apply", who, n.Relation, x.name(n.Object), quoted(what), what), ev).
						withDown(fmt.Sprintf("%s has %s on %s, because they have what it needs and do not have %s", who, n.Relation, x.name(n.Object), quoted(what))))
				}
				x.cleared = append(x.cleared, "“but not "+what+"”")
			}
		}
	default:
		for _, l := range n.Children {
			if l.Outcome == Allowed && l.Role != "subtract" {
				out = append(out, x.grantLink(n, l, seen, part)...)
				break
			}
		}
	}
	return out
}

func (x *explainer) grantLink(parent *Node, l *Link, seen map[int]bool, part bool) []WhyStep {
	c := l.Node
	who := x.name(x.user)
	cond := ""
	if l.Cond != nil {
		cond = fmt.Sprintf(" (condition %s is met)", l.Cond.Name)
	}
	switch {
	case c.Kind == KindSubject && l.Tuple != nil:
		rel := l.Tuple.Relation
		if part {
			rel = "a direct " + rel + " tuple"
		}
		if strings.HasSuffix(c.Label, ":*") {
			return []WhyStep{step(fmt.Sprintf("%s has %s on %s, as everyone (%s) does%s", who, rel, x.name(l.Tuple.Object), c.Label, cond), tupleEv(l))}
		}
		x.via(l.Tuple.Object)
		return []WhyStep{step(fmt.Sprintf("%s has %s on %s%s", x.name(l.Tuple.User), rel, x.name(l.Tuple.Object), cond), tupleEv(l))}
	case l.Tuple != nil && c.Kind == KindRelation && strings.Contains(l.Tuple.User, "#"):
		// Userset edge: members of the group hold the relation.
		steps := x.grant(c, seen, false)
		x.via(l.Tuple.Object)
		rel := l.Tuple.Relation
		if part {
			rel = "a direct " + rel + " tuple"
		}
		return x.after(c, steps, step(fmt.Sprintf("so %s has %s on %s, granted to %s%s", who, rel, x.name(l.Tuple.Object), x.name(l.Tuple.User), cond), tupleEv(l)).
			withDown(fmt.Sprintf("%s has %s on %s, because %s has it%s", who, rel, x.name(l.Tuple.Object), x.name(l.Tuple.User), cond)))
	case l.Tuple != nil && c.Kind == KindRelation:
		// Tuple-to-userset edge: the object inherits through its parent (or org, …).
		steps := x.grant(c, seen, false)
		x.via(l.Tuple.Object)
		branch := c.Relation + " from " + l.Tuple.Relation
		rel := parent.Relation
		if part {
			rel = quoted(branch)
		}
		return x.after(c, steps, step(fmt.Sprintf("so %s has %s on %s, through %s on its %s %s%s", who, rel, x.name(l.Tuple.Object), c.Relation, l.Tuple.Relation, x.name(l.Tuple.User), cond),
			tupleEv(l), ruleEv(on(parent, branch))).
			withDown(fmt.Sprintf("%s has %s on %s, because they have %s on its %s %s%s", who, rel, x.name(l.Tuple.Object), c.Relation, l.Tuple.Relation, x.name(l.Tuple.User), cond)))
	case c.Kind == KindRelation && c.Object == parent.Object && l.Label == "computed":
		steps := x.grant(c, seen, false)
		if part {
			// The intersection or exclusion step names this branch.
			return steps
		}
		return x.after(c, steps, step(fmt.Sprintf("so %s has %s on %s, as anyone with %s does", who, parent.Relation, x.name(parent.Object), c.Relation), ruleEv(on(parent, c.Relation))).
			withDown(fmt.Sprintf("%s has %s on %s, because anyone with %s has it", who, parent.Relation, x.name(parent.Object), c.Relation)))
	default:
		return x.grant(c, seen, part)
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
				steps := sub.grant(l.Node, map[int]bool{}, true)
				x.details = append(x.details, steps...)
				what := strings.TrimPrefix(x.describeBranch(l), "but not ")
				if len(steps) > 0 {
					// The user-side fact is the one people act on (e.g. "is blocked on the org").
					return fmt.Sprintf("they are excluded by “but not %s” because %s", what, steps[0])
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
					x.details = append(x.details, reason(on(n, x.describeBranch(l)), "%s: %s", x.describeBranch(l), x.why(l.Node, depth+1, seen)))
				}
			}
		}
		return fmt.Sprintf("%s requires %s, and %s does not hold", n.Relation, strings.Join(req, " and "), strings.Join(missing, ", "))
	}
	switch n.Kind {
	case KindDirect:
		for _, l := range n.Children {
			if l.Cond != nil && l.Outcome != Allowed && l.Tuple != nil {
				return fmt.Sprintf("the %s tuple's condition %s is %s", l.Tuple.Relation, l.Cond.Name, condWord(l.Cond.Status))
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

func condWord(status cond.Status) string {
	switch status {
	case cond.NotMet:
		return "false"
	case cond.Missing:
		return "missing parameters"
	default:
		return "an error"
	}
}

func dedupe(in []WhyStep) []WhyStep {
	seen := map[string]bool{}
	var out []WhyStep
	for _, s := range in {
		if !seen[s.Text] {
			seen[s.Text] = true
			out = append(out, s)
		}
	}
	return out
}

// Format writes the steps as text: each numbered step, then its evidence
// indented below, one line each ("tuple  object#relation@user  (type#relation
// via restriction)" or "rule   type#relation via branch"). Reason steps are
// marked "✗" instead of numbered.
func (e Explanation) Format() string {
	var b strings.Builder
	n := 0
	for _, s := range e.Steps {
		if s.Reason {
			fmt.Fprintf(&b, "  ✗ %s\n", s.Text)
		} else {
			n++
			fmt.Fprintf(&b, "  %d. %s\n", n, s.Text)
		}
		for _, ev := range s.Evidence {
			fmt.Fprintf(&b, "       %s\n", evidenceLine(ev))
		}
	}
	return b.String()
}

// quoted sets off a branch of more than one word ("member from org") so it
// reads as one term in a sentence.
func quoted(branch string) string {
	if strings.Contains(branch, " ") && !strings.HasPrefix(branch, "(") {
		return "“" + branch + "”"
	}
	return branch
}

// after appends a step that builds on the last of steps or, when node c
// was explained earlier (so steps is empty), on the step that explained it.
func (x *explainer) after(c *Node, steps []WhyStep, s WhyStep) []WhyStep {
	if len(steps) > 0 {
		s.from = []string{steps[len(steps)-1].Text}
	} else if c != nil {
		if t, ok := x.last[c.ID]; ok {
			s.from = []string{t}
		}
	}
	return append(steps, s)
}

// cite numbers the steps (from 1) and notes "(from step N)" on a step that
// builds on any step other than the one just before it, so a "so …" is never
// read as following from the wrong step.
func cite(steps []WhyStep) []WhyStep {
	num := map[string]int{}
	for i := range steps {
		steps[i].base = steps[i].Text
		var refs []int
		other := false
		for _, f := range steps[i].from {
			if k, ok := num[f]; ok && !slices.Contains(refs, k) {
				refs = append(refs, k)
				other = other || k != i
			}
		}
		num[steps[i].Text] = i + 1
		if !other {
			continue
		}
		slices.Sort(refs)
		steps[i].Text += " (" + stepRefs("from", refs) + ")"
	}
	return steps
}

// DownStep is a step in the top-down reading of an explanation (from the
// object to the user): the step, and under it the steps it builds on.
type DownStep struct {
	WhyStep
	// Sentence is the step for this reading ("X, because Y"); the embedded
	// Text is the user-up reading's.
	Sentence string
	// Repeat marks a step already shown higher up; it is named, not expanded.
	Repeat bool
	Under  []DownStep
}

// Down is the top-down reading of the steps (the way the walk reasons): the
// conclusion first, each step's supporting steps under it. Reasons, which
// nothing builds on, are roots of their own. The other reading of Steps.
func (e Explanation) Down() []DownStep {
	byText := map[string]int{}
	used := map[int]bool{}
	for i, s := range e.Steps {
		byText[s.base] = i
	}
	for _, s := range e.Steps {
		for _, f := range s.from {
			if k, ok := byText[f]; ok {
				used[k] = true
			}
		}
	}
	shown := map[int]bool{}
	var build func(i int) DownStep
	build = func(i int) DownStep {
		s := e.Steps[i]
		text := s.down
		if text == "" {
			text = strings.TrimPrefix(s.base, "so ")
		}
		d := DownStep{WhyStep: s, Sentence: text}
		if shown[i] {
			d.Repeat = true
			return d
		}
		shown[i] = true
		for _, f := range s.from {
			if k, ok := byText[f]; ok {
				d.Under = append(d.Under, build(k))
			}
		}
		return d
	}
	var out []DownStep
	for i := range slices.Backward(e.Steps) {
		if !used[i] {
			out = append(out, build(i))
		}
	}
	return out
}

// FormatDown writes Down as indented text, like Format.
func (e Explanation) FormatDown() string {
	var b strings.Builder
	var write func(d DownStep, depth int)
	write = func(d DownStep, depth int) {
		pad := strings.Repeat("  ", depth)
		if d.Repeat {
			fmt.Fprintf(&b, "  %s- %s (see above)\n", pad, d.Sentence)
			return
		}
		mark := "-"
		if d.Reason {
			mark = "✗"
		}
		fmt.Fprintf(&b, "  %s%s %s\n", pad, mark, d.Sentence)
		for _, ev := range d.Evidence {
			fmt.Fprintf(&b, "  %s    %s\n", pad, evidenceLine(ev))
		}
		for _, u := range d.Under {
			write(u, depth+1)
		}
	}
	for _, d := range e.Down() {
		write(d, 0)
	}
	return b.String()
}

// DownFlat is the top-down reading as a numbered list instead of a tree: the
// steps in the same order as Down, each listed once, and a step whose
// supporting steps are not simply the next one says which they are
// ("(see steps 3 and 7)").
func (e Explanation) DownFlat() []DownStep {
	var out []DownStep
	num := map[string]int{} // base text → number in out
	var add func(d DownStep)
	add = func(d DownStep) {
		if d.Repeat {
			return
		}
		out = append(out, d)
		num[d.base] = len(out)
		for _, u := range d.Under {
			add(u)
		}
	}
	for _, d := range e.Down() {
		add(d)
	}
	for i := range out {
		var refs []int
		for _, u := range out[i].Under {
			if k, ok := num[u.base]; ok {
				refs = append(refs, k)
			}
		}
		if len(refs) == 0 || (len(refs) == 1 && refs[0] == i+2) {
			continue
		}
		out[i].Sentence += " (" + stepRefs("see", refs) + ")"
	}
	return out
}

// evidenceLine is one piece of evidence as text: "tuple  object#relation@user
// with cond (contextual, not stored)  (type#relation via restriction)" or
// "rule   type#relation via branch".
func evidenceLine(ev Evidence) string {
	ref := ev.Ref.String()
	if ev.Ref.Via != "" {
		ref += " via " + ev.Ref.Via
	}
	if ev.Kind != EvidenceTuple {
		return "rule   " + ref
	}
	t := ev.Tuple.Key()
	if ev.Tuple.Condition != "" {
		t += " with " + ev.Tuple.Condition
	}
	if ev.Tuple.Contextual {
		t += " (contextual, not stored)"
	}
	return "tuple  " + t + "  (" + ref + ")"
}

// stepRefs names steps by number after a word: "from step 2", "see steps 1,
// 3 and 4".
func stepRefs(word string, refs []int) string {
	words := make([]string, len(refs))
	for i, k := range refs {
		words[i] = strconv.Itoa(k)
	}
	if len(words) == 1 {
		return word + " step " + words[0]
	}
	return word + " steps " + strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}
