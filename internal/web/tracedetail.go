package web

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
)

// nodeDetail is the content of the trace's node detail panel. It is computed
// here, with the links it offers, so the browser only lays it out.
type nodeDetail struct {
	Title string       `json:"title"`
	Rows  []detailRow  `json:"rows"`
	Links []detailLink `json:"links,omitempty"`
}

type detailRow struct {
	Key   string       `json:"key"`
	Value string       `json:"value,omitempty"`
	Items []detailLink `json:"items,omitempty"`
}

// detailLink is a link (Href) or an action button (Act, e.g. re-running the
// check in another evaluation Order).
type detailLink struct {
	Text  string `json:"text"`
	Href  string `json:"href,omitempty"`
	Note  string `json:"note,omitempty"`
	Act   string `json:"act,omitempty"`
	Order string `json:"order,omitempty"`
	Ghost bool   `json:"ghost,omitempty"`
}

// traceLinks builds the links a trace offers, keeping the request's context.
type traceLinks struct {
	base, user, context, contextual string
}

func (l traceLinks) check(relation, object string) string {
	q := url.Values{"user": {l.user}, "relation": {relation}, "object": {object}}
	if l.context != "" {
		q.Set("context", l.context)
	}
	if l.contextual != "" {
		q.Set("contextual", l.contextual)
	}
	return l.base + "/check?" + q.Encode()
}

func (l traceLinks) explore(object, relation string) string {
	typ, id, _ := schema.SplitObject(object)
	q := url.Values{"type": {typ}, "id": {id}}
	if relation != "" {
		q.Set("relation", relation)
	}
	return l.base + "/explore?" + q.Encode()
}

// addNodeDetails attaches a "detail" to every trace node in the graph.
func addNodeDetails(t *trace.Trace, g *trace.Graph, l traceLinks, m *schema.Model) {
	nodes := map[string]*trace.Node{}
	var walk func(n *trace.Node)
	walk = func(n *trace.Node) {
		id := fmt.Sprintf("n%d", n.ID)
		if nodes[id] != nil {
			return
		}
		nodes[id] = n
		for _, c := range n.Children {
			walk(c.Node)
		}
	}
	walk(t.Root)
	for i := range g.Elements {
		e := &g.Elements[i]
		if e.Group != "nodes" {
			continue
		}
		id, _ := e.Data["id"].(string)
		label, _ := e.Data["label"].(string)
		title := strings.ReplaceAll(label, "\n", " ")
		switch {
		case nodes[id] != nil:
			e.Data["detail"] = detailFor(nodes[id], title, l, m)
		case e.Data["kind"] == "more":
			obj, _ := e.Data["object"].(string)
			rel, _ := e.Data["relation"].(string)
			e.Data["detail"] = nodeDetail{Title: title, Links: []detailLink{{Text: "Browse these edges", Href: l.explore(obj, rel), Ghost: true}}}
		}
	}
}

func detailFor(n *trace.Node, title string, l traceLinks, m *schema.Model) nodeDetail {
	d := nodeDetail{Title: title}
	row := func(k, v string) {
		if v != "" {
			d.Rows = append(d.Rows, detailRow{Key: k, Value: v})
		}
	}
	row("outcome", string(n.Outcome))
	if s := n.Server; s != nil {
		switch {
		case s.Err != "":
			row("server", "error: "+s.Err)
		case s.Allowed:
			row("server", "allowed")
		default:
			row("server", "denied")
		}
	}
	expr := n.Expr
	if expr == "" {
		expr = strings.TrimPrefix(n.Label, "direct ")
	}
	if expr != "" && n.Relation != "" {
		row("rule", n.Relation+" = "+expr)
	}
	// Modular models: which module (and file) defines the relation.
	if m != nil && m.Modular && n.Object != "" && n.Relation != "" {
		typ, _, _ := schema.SplitObject(n.Object)
		if r, ok := m.Relation(typ, n.Relation); ok && !r.Source.IsZero() {
			src := r.Source.String()
			if r.Extended {
				src += ", added to " + typ + " (extend type)"
			}
			row("module", src)
		}
	}
	row("why", n.Reason)
	row("notes", strings.Join(n.Notes, "\n"))
	if n.Kind == trace.KindSkipped {
		// A branch the walk did not need: what the server said, and how to trace it.
		switch n.Hint {
		case trace.Allowed:
			row("server", "this branch also grants access")
		case trace.Denied:
			row("server", "this branch does not hold")
		}
		if len(n.Checked) > 0 {
			r := detailRow{Key: "trace it"}
			for i, c := range n.Checked {
				if i == 8 {
					r.Items = append(r.Items, detailLink{Text: fmt.Sprintf("…and %d more", len(n.Checked)-8)})
					break
				}
				note := "not checked"
				if c.Known {
					note = map[bool]string{true: "✓ holds", false: "✗ does not hold"}[c.Allowed]
				}
				r.Items = append(r.Items, detailLink{Text: c.Relation + " on " + c.Object, Href: l.check(c.Relation, c.Object), Note: note})
			}
			d.Rows = append(d.Rows, r)
		} else {
			d.Links = append(d.Links, detailLink{Text: "Re-run in model order", Act: "order", Order: trace.OrderModel})
		}
	}
	if n.Object != "" && n.Relation != "" && (n.Kind == trace.KindRelation || n.Kind == trace.KindCycle) {
		d.Links = append(d.Links, detailLink{Text: "Trace from here", Href: l.check(n.Relation, n.Object)})
	}
	if n.Object != "" {
		d.Links = append(d.Links, detailLink{Text: "Explore object", Href: l.explore(n.Object, ""), Ghost: true})
	}
	return d
}

// checkSummary is the one-line form summary shown once a check has run.
func checkSummary(user, relation, object, context string) string {
	s := user + " → " + relation + " → " + object
	if strings.TrimSpace(context) != "" {
		s += " · with context"
	}
	return s
}

// doneText is the player's status once the replay ends. Path is shown while
// the granting path is highlighted, NoFocus when the highlight is turned
// off, Other when there is no granting path to highlight.
type doneText struct{ Path, NoFocus, Other string }

func doneTexts(decision trace.Outcome, order string, exhaustive bool) doneText {
	switch decision {
	case trace.Allowed:
		d := doneText{
			NoFocus: "Access is allowed. Turn on “highlight granting path” to see the path that granted it.",
			Other:   "Access is allowed by the server; the walker could not trace the full path within its budget (amber nodes).",
		}
		switch {
		case order == trace.OrderAll && exhaustive:
			d.Path = "Access is allowed. Every route that grants it is highlighted: follow the moving arrows from the object down to the user."
		case order == trace.OrderAll:
			d.Path = "Access is allowed. The highlighted routes are the ones found within the walk’s limits; others may exist (see “not exhaustive” above)."
		default:
			d.Path = "Access is allowed. The moving green path is the route that grants access: follow the arrows from the object down to the user. Other routes may exist; “Show all granting paths” evaluates them all."
		}
		return d
	case trace.Denied:
		s := "Access is denied. Red edges are branches that failed; thick red dashed edges mark what blocked access (a failed “and”, or a “but not” that applied)."
		return doneText{Path: s, NoFocus: s, Other: s}
	}
	s := "Some branches are undecided (amber). Click one and choose “Trace from here” to dig in."
	return doneText{Path: s, NoFocus: s, Other: s}
}
