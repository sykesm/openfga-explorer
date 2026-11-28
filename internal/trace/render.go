package trace

import (
	"fmt"
	"iter"
	"strconv"
	"strings"
)

// Line is one row of the textual trace.
type Line struct {
	Prefix string
	// Cont is the prefix for continuation lines (details) under this line.
	Cont    string
	Outcome Outcome
	// Link describes how this node was reached from its parent.
	Link     string
	Label    string
	Kind     Kind
	Detail   string
	Cond     string
	Server   *ServerVerdict
	NodeID   int
	Repeat   bool // already shown above
	Blocking bool
	Probe    bool
	Object   string
	Relation string
}

// ElementID is the graph element ID of the line's node (see NodeID).
func (l Line) ElementID() string { return NodeID(l.NodeID) }

// Icon returns a glyph for an outcome.
func Icon(o Outcome) string {
	switch o {
	case Allowed:
		return "✓"
	case Denied:
		return "✗"
	case Unknown:
		return "?"
	case Errored:
		return "!"
	default:
		return "·"
	}
}

// Lines renders the trace as an indented tree.
func (t *Trace) Lines() []Line {
	var out []Line
	shown := map[int]bool{}
	var walk func(n *Node, l *Link, prefix, childPrefix string)
	walk = func(n *Node, l *Link, prefix, childPrefix string) {
		line := Line{Prefix: prefix, Outcome: n.Outcome, Label: n.Label, Kind: n.Kind, NodeID: n.ID, Server: n.Server, Object: n.Object, Relation: n.Relation}
		if l != nil {
			line.Outcome = l.Outcome
			line.Link = l.Label
			if l.Role == "subtract" && !strings.HasPrefix(l.Label, "but not") {
				line.Link = "but not"
			}
			if l.Tuple != nil {
				line.Link += " " + l.Tuple.String()
				if l.Tuple.Contextual {
					line.Link += " (contextual)"
				}
			}
			if l.Cond != nil {
				line.Cond = l.Cond.Summary()
			}
			line.Blocking, line.Probe = l.Blocking, l.Probe
		}
		switch {
		case n.Kind == KindSubject:
			line.Detail = "the user"
			if strings.HasSuffix(n.Label, ":*") {
				line.Detail = "public wildcard"
			}
		case shown[n.ID] && n.Kind == KindRelation:
			line.Repeat = true
			line.Detail = "evaluated above"
		default:
			d := []string{}
			if n.Op != "" || n.Kind == KindRelation {
				if n.Expr != "" {
					d = append(d, "= "+n.Expr)
				}
			}
			if n.Reason != "" {
				d = append(d, n.Reason)
			}
			if n.Truncated != nil {
				d = append(d, n.Truncated.Summary())
			}
			if n.Outcome != Denied || (n.Kind != KindDirect && n.Kind != KindRelation) {
				d = append(d, n.Notes...)
			}
			line.Detail = strings.Join(d, " · ")
		}
		line.Cont = childPrefix + "   "
		if !line.Repeat && n.Kind != KindSubject && len(n.Children) > 0 {
			line.Cont = childPrefix + "│  "
		}
		out = append(out, line)
		if line.Repeat || n.Kind == KindSubject {
			return
		}
		shown[n.ID] = true
		for i, c := range n.Children {
			last := i == len(n.Children)-1
			p, cp := childPrefix+"├─ ", childPrefix+"│  "
			if last {
				p, cp = childPrefix+"└─ ", childPrefix+"   "
			}
			walk(c.Node, c, p, cp)
		}
	}
	walk(t.Root, nil, "", "")
	return out
}

// Text renders the trace as plain text.
func (t *Trace) Text() string {
	var b strings.Builder
	for _, l := range t.Lines() {
		b.WriteString(l.Prefix)
		b.WriteString(Icon(l.Outcome))
		b.WriteString(" ")
		if l.Link != "" {
			b.WriteString("[" + l.Link + "] ")
		}
		b.WriteString(l.Label)
		if l.Cond != "" {
			b.WriteString(" {" + l.Cond + "}")
		}
		if l.Detail != "" {
			b.WriteString("  — " + l.Detail)
		}
		if l.Server != nil {
			switch {
			case l.Server.Err != "":
				b.WriteString("  [server error: " + l.Server.Err + "]")
			case l.Server.Mismatch:
				b.WriteString(fmt.Sprintf("  [MISMATCH: server says allowed=%v]", l.Server.Allowed))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Element is a Cytoscape.js element: a node or an edge (Group "nodes" or
// "edges").
type Element struct {
	Group   string      `json:"group"`
	Data    ElementData `json:"data"`
	Classes string      `json:"classes,omitempty"`
}

// ElementData is what the page reads from an element (trace.js). Nodes use
// the fields above Source; edges use ID, Label, Source, Target and Tuple.
type ElementData struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Short is the label when zoomed out.
	Short   string  `json:"short,omitempty"`
	Kind    Kind    `json:"kind,omitempty"`
	Outcome Outcome `json:"outcome,omitempty"`
	// Hint is the server's verdict on a skipped branch.
	Hint Outcome `json:"hint,omitempty"`
	// Object and Relation are for the web layer's node details; not sent.
	Object   string `json:"-"`
	Relation string `json:"-"`
	// Depth is a granting-path node's distance from the root (see markGrantingPath).
	Depth *int `json:"depth,omitempty"`
	// Detail is what the side panel shows for a node, attached by the web
	// layer (web.nodeDetail).
	Detail any `json:"detail,omitempty"`

	Source string `json:"source,omitempty"`
	Target string `json:"target,omitempty"`
	// Tuple is an edge's tuple, object#relation@user (with its condition).
	Tuple string `json:"tuple,omitempty"`
}

// NodeID is the graph element ID of a trace node ("n7"); the text trace's
// lines and the explanation's steps refer to nodes by it.
func NodeID(id int) string { return "n" + strconv.Itoa(id) }

// GraphStep is an animation step in Cytoscape element IDs.
type GraphStep struct {
	Phase   Phase   `json:"phase"`
	Node    string  `json:"node"`
	From    string  `json:"from,omitempty"`
	Outcome Outcome `json:"outcome,omitempty"`
}

// Graph is the trace in Cytoscape.js form.
type Graph struct {
	Elements []Element   `json:"elements"`
	Steps    []GraphStep `json:"steps"`
	Root     string      `json:"root"`
	// Folds group failed sibling branches; the browser applies them unless
	// the viewer turned folding off.
	Folds []Fold `json:"folds,omitempty"`
}

// Graph converts the trace into Cytoscape.js elements plus an animation timeline.
func (t *Trace) Graph() *Graph {
	g := &Graph{Root: NodeID(t.Root.ID)}
	seen := map[int]bool{}
	edgeN := 0
	var visit func(n *Node)
	visit = func(n *Node) {
		if seen[n.ID] {
			return
		}
		seen[n.ID] = true
		label := n.Label
		if n.Kind == KindRelation || n.Kind == KindCycle {
			label = n.Object + "\n#" + n.Relation
		}
		if n.Kind == KindDirect {
			label = "direct tuples"
		}
		if n.Op != "" {
			label += "\n" + n.Op.Symbol()
		}
		data := ElementData{
			ID: NodeID(n.ID), Label: label, Short: shortLabel(n), Kind: n.Kind, Outcome: n.Outcome,
			Hint: n.Hint, Object: n.Object, Relation: n.Relation,
		}
		classes := []string{string(n.Kind), string(n.Outcome)}
		if n.Hint != "" {
			classes = append(classes, "hint-"+string(n.Hint))
		}
		if n.Server != nil && n.Server.Mismatch {
			classes = append(classes, "mismatch")
		}
		if n == t.Root {
			classes = append(classes, "root")
		}
		g.Elements = append(g.Elements, Element{Group: "nodes", Data: data, Classes: strings.Join(classes, " ")})
		for _, c := range n.Children {
			edgeN++
			ed := ElementData{ID: "e" + strconv.Itoa(edgeN), Source: NodeID(n.ID), Target: NodeID(c.Node.ID), Label: c.Label}
			if c.Tuple != nil {
				ed.Tuple = c.Tuple.String()
			}
			if c.Cond != nil {
				ed.Label = c.Label + " [" + c.Cond.Name + "]"
			}
			cls := []string{string(c.Outcome)}
			if c.Blocking {
				cls = append(cls, "blocking")
			}
			if c.Probe {
				cls = append(cls, "probe")
			}
			if c.Role != "" {
				cls = append(cls, c.Role)
			}
			g.Elements = append(g.Elements, Element{Group: "edges", Data: ed, Classes: strings.Join(cls, " ")})
			visit(c.Node)
		}
		if tr := n.Truncated; tr != nil {
			id := NodeID(n.ID) + "-more"
			g.Elements = append(g.Elements,
				Element{Group: "nodes", Data: ElementData{ID: id, Label: tr.Summary(), Kind: KindMore, Object: tr.Object, Relation: tr.Relation}, Classes: "more"},
				Element{Group: "edges", Data: ElementData{ID: id + "-e", Source: NodeID(n.ID), Target: id, Label: "…"}, Classes: "more"},
			)
		}
	}
	visit(t.Root)
	for _, s := range t.Steps {
		gs := GraphStep{Phase: s.Phase, Node: NodeID(s.Node), Outcome: s.Outcome}
		if s.Phase == Traverse {
			gs.From = NodeID(s.From)
		}
		g.Steps = append(g.Steps, gs)
	}
	g.markGrantingPath()
	g.Folds = g.folds(FoldMin)
	return g
}

// grantingLinks yields each tuple link on a granting path once (by tuple
// key): tuples traversed along allowed links from the root, excluding the
// subtracted side of exclusions, in walk order. Contextual tuples are
// included.
func (t *Trace) grantingLinks() iter.Seq[*Link] {
	return func(yield func(*Link) bool) {
		seen, have := map[int]bool{}, map[string]bool{}
		var walk func(n *Node) bool
		walk = func(n *Node) bool {
			if seen[n.ID] || n.Outcome != Allowed {
				return true
			}
			seen[n.ID] = true
			for _, l := range n.Children {
				if l.Outcome != Allowed || l.Role == "subtract" || l.Node == nil {
					continue
				}
				if l.Tuple != nil && !have[l.Tuple.Key()] {
					have[l.Tuple.Key()] = true
					if !yield(l) {
						return false
					}
				}
				if !walk(l.Node) {
					return false
				}
			}
			return true
		}
		walk(t.Root)
	}
}

// GrantingTuples counts the distinct tuples that end a granting path: those
// that reach the user (or a wildcard).
func (t *Trace) GrantingTuples() int {
	n := 0
	for l := range t.grantingLinks() {
		if l.Node.Kind == KindSubject {
			n++
		}
	}
	return n
}

// PathTuples lists the tuples on granting paths (object#relation@user).
func (t *Trace) PathTuples() []string {
	var out []string
	for l := range t.grantingLinks() {
		out = append(out, l.Tuple.Key())
	}
	return out
}

// PathTuple is a tuple on a granting path, with the condition parameters
// the tuple supplied, so the path can be reproduced elsewhere (a scenario).
type PathTuple struct {
	User, Relation, Object string
	Condition              string
	// Context is the tuple's condition parameters (not the request's).
	Context    map[string]any
	Contextual bool
}

// GrantingPathTuples returns the tuples on granting paths (as PathTuples
// does), with their conditions.
func (t *Trace) GrantingPathTuples() []PathTuple {
	var out []PathTuple
	for l := range t.grantingLinks() {
		tp := l.Tuple
		pt := PathTuple{User: tp.User, Relation: tp.Relation, Object: tp.Object, Condition: tp.Condition, Contextual: tp.Contextual}
		if l.Cond != nil {
			for _, p := range l.Cond.Params {
				if p.Source != "tuple" {
					continue
				}
				if pt.Context == nil {
					pt.Context = map[string]any{}
				}
				pt.Context[p.Name] = p.Raw.AsInterface()
			}
		}
		out = append(out, pt)
	}
	return out
}

// shortLabel is the compact label shown when the graph is zoomed out.
func shortLabel(n *Node) string {
	switch n.Kind {
	case KindRelation, KindCycle:
		if n.Op != "" {
			return n.Relation + " " + n.Op.Symbol()
		}
		return n.Relation
	case KindUnion:
		return "∪"
	case KindIntersection:
		return "∩"
	case KindExclusion:
		return "∖"
	case KindDirect:
		return "direct"
	case KindSubject:
		return n.Label
	case KindMore:
		return "…"
	}
	return n.Label
}
