package trace

import (
	"fmt"
	"slices"
	"strings"
)

// Post-passes over the Cytoscape elements: which elements form the granting
// path, and which failed branches fold away. They work on the element graph
// (not the trace tree) because relation nodes are shared between parents.

// Fold hides the failed branches of one node behind a single
// "N other branches checked" node, so the granting path stays readable.
type Fold struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
	Label  string `json:"label"`
	// Hidden are the element IDs hidden while folded. Expanding shows them;
	// folds nested inside are shown folded (their own Hidden stay hidden).
	Hidden []string `json:"hidden"`
}

// FoldMin is the number of failed sibling branches at which they fold.
const FoldMin = 5

type elemIndex struct {
	g     *Graph
	nodes map[string]int   // node ID -> element index
	edges map[string]int   // edge ID -> element index
	out   map[string][]int // node ID -> outgoing edge indices, in element order
	in    map[string][]int // node ID -> incoming edge indices
}

func (g *Graph) index() *elemIndex {
	ix := &elemIndex{g: g, nodes: map[string]int{}, edges: map[string]int{}, out: map[string][]int{}, in: map[string][]int{}}
	for i, e := range g.Elements {
		id := e.id()
		if e.Group == "nodes" {
			ix.nodes[id] = i
			continue
		}
		ix.edges[id] = i
		ix.out[e.str("source")] = append(ix.out[e.str("source")], i)
		ix.in[e.str("target")] = append(ix.in[e.str("target")], i)
	}
	return ix
}

func (e *Element) id() string          { return e.str("id") }
func (e *Element) str(k string) string { s, _ := e.Data[k].(string); return s }

// HasClass reports whether the element has the class.
func (e *Element) HasClass(c string) bool { return slices.Contains(strings.Fields(e.Classes), c) }

func (e *Element) addClass(c string) {
	if !e.HasClass(c) {
		e.Classes = strings.TrimSpace(e.Classes + " " + c)
	}
}

func (ix *elemIndex) node(id string) *Element {
	if i, ok := ix.nodes[id]; ok {
		return &ix.g.Elements[i]
	}
	return nil
}

// markGrantingPath marks the granting path with the class "flowpath": the
// allowed edges reachable from an allowed root, excluding the subtracted side
// of exclusions (it only ever removes access) and "… more" edges. Path nodes
// get "depth", their distance from the root, for the travelling highlight.
func (g *Graph) markGrantingPath() {
	ix := g.index()
	root := ix.node(g.Root)
	if root == nil || !root.HasClass(string(Allowed)) {
		return
	}
	depth := map[string]int{g.Root: 0}
	root.addClass("flowpath")
	root.Data["depth"] = 0
	for queue := []string{g.Root}; len(queue) > 0; queue = queue[1:] {
		id := queue[0]
		for _, ei := range ix.out[id] {
			e := &g.Elements[ei]
			if !e.HasClass(string(Allowed)) || e.HasClass("subtract") || e.HasClass("more") {
				continue
			}
			e.addClass("flowpath")
			t := e.str("target")
			if _, ok := depth[t]; ok {
				continue
			}
			depth[t] = depth[id] + 1
			if n := ix.node(t); n != nil {
				n.addClass("flowpath")
				n.Data["depth"] = depth[t]
			}
			queue = append(queue, t)
		}
	}
}

// folds finds nodes with at least min failed branches (not allowed, not
// blocking, not "… more", not ending at the user) and hides each such branch
// together with everything reachable only through it. Nothing on the
// granting path is hidden. Call after markGrantingPath.
func (g *Graph) folds(min int) []Fold {
	ix := g.index()
	type found struct {
		Fold
		orig map[string]bool
	}
	var all []*found
	for _, el := range g.Elements {
		if el.Group != "nodes" {
			continue
		}
		parent := el.id()
		var failed []int
		for _, ei := range ix.out[parent] {
			e := &g.Elements[ei]
			t := ix.node(e.str("target"))
			if e.HasClass(string(Allowed)) || e.HasClass("blocking") || e.HasClass("more") || t == nil || t.HasClass("subject") {
				continue
			}
			failed = append(failed, ei)
		}
		if len(failed) < min {
			continue
		}
		hidden := map[string]bool{}
		var order []string
		add := func(id string) {
			if !hidden[id] {
				hidden[id] = true
				order = append(order, id)
			}
		}
		for _, ei := range failed {
			add(g.Elements[ei].id())
			add(g.Elements[ei].str("target"))
		}
		// Extend through descendants reachable only through hidden elements.
		for changed := true; changed; {
			changed = false
			for _, id := range slices.Clone(order) {
				if ix.node(id) == nil {
					continue
				}
				for _, ei := range ix.out[id] {
					e := &g.Elements[ei]
					t := e.str("target")
					tn := ix.node(t)
					if hidden[t] || tn == nil || tn.HasClass("subject") || tn.HasClass("root") {
						continue
					}
					only := true
					for _, ii := range ix.in[t] {
						in := &g.Elements[ii]
						if !hidden[in.id()] && !hidden[in.str("source")] {
							only = false
							break
						}
					}
					if only {
						add(e.id())
						add(t)
						changed = true
					} else {
						add(e.id())
					}
				}
			}
		}
		f := &found{Fold: Fold{
			ID: "fold-" + parent, Parent: parent,
			Label: fmt.Sprintf("%d other branches checked — none grant access\n(click to expand)", len(failed)),
		}, orig: map[string]bool{}}
		for _, id := range order {
			if el := g.element(ix, id); el != nil && !el.HasClass("flowpath") {
				f.orig[id] = true
				f.Hidden = append(f.Hidden, id)
			}
		}
		all = append(all, f)
	}
	// Nested folds: a fold whose parent another fold hides is shown (folded)
	// when that fold expands, and keeps its own branches hidden.
	direct := func(f *found) *found { // the innermost fold hiding f's parent
		var best *found
		for _, g := range all {
			if g != f && g.orig[f.Parent] && (best == nil || len(g.orig) < len(best.orig)) {
				best = g
			}
		}
		return best
	}
	inner := map[*found][]*found{}
	for _, f := range all {
		if d := direct(f); d != nil {
			inner[d] = append(inner[d], f)
		}
	}
	out := make([]Fold, 0, len(all))
	for _, f := range all {
		drop := map[string]bool{}
		for _, h := range all {
			if h != f && f.orig[h.Parent] {
				for id := range h.orig {
					drop[id] = true
				}
			}
		}
		hidden := f.Hidden[:0:0]
		for _, id := range f.Hidden {
			if !drop[id] {
				hidden = append(hidden, id)
			}
		}
		for _, n := range inner[f] {
			hidden = append(hidden, n.ID, n.ID+"-e")
		}
		f.Hidden = hidden
		out = append(out, f.Fold)
	}
	return out
}

func (g *Graph) element(ix *elemIndex, id string) *Element {
	if i, ok := ix.nodes[id]; ok {
		return &g.Elements[i]
	}
	if i, ok := ix.edges[id]; ok {
		return &g.Elements[i]
	}
	return nil
}
