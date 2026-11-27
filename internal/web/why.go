package web

import (
	"strings"

	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
)

// whyStep is one step of a check's "Why" list, with its evidence.
type whyStep struct {
	Text     string
	Reason   bool
	Evidence []whyEvidence
}

// whyEvidence is a tuple or model rule a step rests on, tied to the model
// relation it belongs to.
type whyEvidence struct {
	trace.Evidence
	// Rel is "type#relation"; Href the relation on the object in Explore;
	// Module where a modular model defines it. Each is empty when not known.
	Rel, Href, Module string
	// Def is the relation's definition; the page shows Pre, Hit, Post, where
	// Hit is the part of it the evidence uses (empty when it uses all of it).
	// The first mention of a relation shows its whole definition; later ones
	// only the part they use, with "…" for the rest (short definitions are
	// always shown whole).
	Def, Pre, Hit, Post string
	// ShowDef is false for a later mention that uses the whole definition:
	// the relation is named and the definition is above.
	ShowDef bool
}

// TupleText writes the tuple Zanzibar style, object#relation@user.
func (e whyEvidence) TupleText() string {
	return e.Tuple.Object + "#" + e.Tuple.Relation + "@" + e.Tuple.User
}

// shortDef is the longest definition repeated in full at every mention.
const shortDef = 40

// whySteps ties each step's evidence to its relation in the model.
func whySteps(steps []trace.WhyStep, m *schema.Model, l traceLinks) []whyStep {
	out := make([]whyStep, 0, len(steps))
	shown := map[string]bool{}
	for _, s := range steps {
		ws := whyStep{Text: s.Text, Reason: s.Reason}
		for _, ev := range s.Evidence {
			ws.Evidence = append(ws.Evidence, evidence(ev, m, l, shown))
		}
		out = append(out, ws)
	}
	return out
}

func evidence(ev trace.Evidence, m *schema.Model, l traceLinks, shown map[string]bool) whyEvidence {
	w := whyEvidence{Evidence: ev, Rel: ev.Ref.String()}
	if w.Rel == "" {
		return w
	}
	w.Href = l.explore(ev.Ref.Object, ev.Ref.Relation)
	if m != nil {
		if r, ok := m.Relation(ev.Ref.Type(), ev.Ref.Relation); ok {
			w.Def = r.Expr
		}
		w.Module = relModule(m, ev.Ref.Type(), ev.Ref.Relation)
	}
	if w.Def == "" {
		return w
	}
	via := ev.Ref.Via
	i := findPart(w.Def, via, ev.Kind == trace.EvidenceTuple)
	first := !shown[w.Rel]
	shown[w.Rel] = true
	switch {
	case i < 0:
		w.Pre, w.ShowDef = w.Def, first
	case first || len(w.Def) <= shortDef:
		w.Pre, w.Hit, w.Post, w.ShowDef = w.Def[:i], via, w.Def[i+len(via):], true
	default:
		w.Hit, w.ShowDef = via, true
		if i > 0 {
			w.Pre = "… "
		}
		if i+len(via) < len(w.Def) {
			w.Post = " …"
		}
	}
	return w
}

// findPart finds where a definition writes part (a type restriction when
// restriction is set, otherwise a branch) and returns its index, or -1.
// It matches whole terms: "user" is not found in "user:*", nor "member" in
// "member from org".
func findPart(def, part string, restriction bool) int {
	if part == "" {
		return -1
	}
	for i := 0; i < len(def); {
		j := strings.Index(def[i:], part)
		if j < 0 {
			return -1
		}
		j += i
		end := j + len(part)
		before, after := byte(' '), byte(' ')
		if j > 0 {
			before = def[j-1]
		}
		if end < len(def) {
			after = def[end]
		}
		inside := strings.Count(def[:j], "[") > strings.Count(def[:j], "]")
		var ok bool
		if restriction {
			ok = inside && (before == '[' || before == ' ') && (after == ',' || after == ']')
		} else {
			ok = !inside && (before == ' ' || before == '(') && (after == ' ' || after == ')') &&
				(strings.Contains(part, " from ") || !strings.HasPrefix(def[end:], " from "))
		}
		if ok {
			return j
		}
		i = j + 1
	}
	return -1
}

// relModule says which module (and file) of a modular model defines a
// relation; empty for other models.
func relModule(m *schema.Model, typ, rel string) string {
	if m == nil || !m.Modular {
		return ""
	}
	r, ok := m.Relation(typ, rel)
	if !ok || r.Source.IsZero() {
		return ""
	}
	src := r.Source.String()
	if r.Extended {
		src += ", added to " + typ + " (extend type)"
	}
	return src
}
