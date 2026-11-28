package trace

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// graphOf builds element graphs from "a->b:class,class" edge specs; nodes
// get the classes given in nodes (e.g. "root allowed").
func graphOf(root string, nodes map[string]string, edges ...string) *Graph {
	g := &Graph{Root: root}
	var ids []string
	for id := range nodes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	// Root first, like Graph() emits it.
	slices.SortStableFunc(ids, func(a, b string) int {
		switch {
		case a == root:
			return -1
		case b == root:
			return 1
		}
		return 0
	})
	for _, id := range ids {
		g.Elements = append(g.Elements, Element{Group: "nodes", Data: ElementData{ID: id}, Classes: nodes[id]})
	}
	for i, spec := range edges {
		ends, cls, _ := strings.Cut(spec, ":")
		src, dst, _ := strings.Cut(ends, "->")
		g.Elements = append(g.Elements, Element{Group: "edges", Data: ElementData{ID: fmt.Sprintf("e%d", i), Source: src, Target: dst}, Classes: strings.ReplaceAll(cls, ",", " ")})
	}
	return g
}

func classed(g *Graph, class string) []string {
	var out []string
	for _, e := range g.Elements {
		if e.HasClass(class) {
			out = append(out, e.id())
		}
	}
	return out
}

func TestGrantingPath(t *testing.T) {
	g := graphOf("r", map[string]string{"r": "root allowed", "a": "allowed", "b": "allowed", "u": "subject allowed", "x": "allowed", "m": "more"},
		"r->a:allowed", "a->u:allowed", "r->b:denied", "r->x:allowed,subtract", "a->m:more")
	g.markGrantingPath()
	if got := classed(g, "flowpath"); !slices.Equal(got, []string{"r", "a", "u", "e0", "e1"}) {
		t.Errorf("flowpath = %v", got)
	}
	for id, want := range map[string]int{"r": 0, "a": 1, "u": 2} {
		n := g.index().node(id)
		if n.Data.Depth == nil || *n.Data.Depth != want {
			t.Errorf("depth(%s) = %v, want %d", id, n.Data.Depth, want)
		}
	}
	// A denied root has no granting path.
	g = graphOf("r", map[string]string{"r": "root denied", "a": "allowed"}, "r->a:allowed")
	g.markGrantingPath()
	if got := classed(g, "flowpath"); len(got) != 0 {
		t.Errorf("denied root: flowpath = %v", got)
	}
}

func TestFolds(t *testing.T) {
	// r has 5 failed branches f1..f5 plus the granting branch a; f1 leads to
	// g, reachable only through f1, and to s, shared with the granting path.
	nodes := map[string]string{"r": "root allowed", "a": "allowed", "u": "subject allowed", "g": "denied", "s": "denied"}
	edges := []string{"r->a:allowed", "a->u:allowed", "f1->g:denied", "f1->s:denied", "a->s:denied"}
	for i := 1; i <= 5; i++ {
		nodes[fmt.Sprintf("f%d", i)] = "denied"
		edges = append(edges, fmt.Sprintf("r->f%d:denied", i))
	}
	g := graphOf("r", nodes, edges...)
	g.markGrantingPath()
	folds := g.folds(FoldMin)
	if len(folds) != 1 || folds[0].Parent != "r" {
		t.Fatalf("folds = %+v", folds)
	}
	h := folds[0].Hidden
	for _, id := range []string{"f1", "f5", "g", "e2"} {
		if !slices.Contains(h, id) {
			t.Errorf("fold hides %v; missing %s", h, id)
		}
	}
	for _, id := range []string{"a", "u", "s", "e0"} { // granting path, and s (also reached from a)
		if slices.Contains(h, id) {
			t.Errorf("fold hides %s: %v", id, h)
		}
	}
	if !slices.Contains(h, "e3") { // the f1->s edge goes, s stays
		t.Errorf("fold keeps the hidden branch's edge to s: %v", h)
	}
	if g.folds(6) != nil && len(g.folds(6)) != 0 {
		t.Errorf("5 failed branches fold at min 6")
	}
}

func TestNestedFolds(t *testing.T) {
	// r has 5 failed branches; one of them (p) has 5 failed branches itself.
	nodes := map[string]string{"r": "root denied", "p": "denied"}
	var edges []string
	for i := 1; i <= 4; i++ {
		nodes[fmt.Sprintf("q%d", i)] = "denied"
		edges = append(edges, fmt.Sprintf("r->q%d:denied", i))
	}
	edges = append(edges, "r->p:denied")
	for i := 1; i <= 5; i++ {
		nodes[fmt.Sprintf("c%d", i)] = "denied"
		edges = append(edges, fmt.Sprintf("p->c%d:denied", i))
	}
	g := graphOf("r", nodes, edges...)
	g.markGrantingPath()
	folds := g.folds(FoldMin)
	if len(folds) != 2 {
		t.Fatalf("folds = %+v", folds)
	}
	outer, inner := folds[0], folds[1]
	if outer.Parent != "r" || inner.Parent != "p" {
		t.Fatalf("folds = %+v", folds)
	}
	// Expanding the outer fold shows p and the inner fold node, not p's branches.
	if !slices.Contains(outer.Hidden, "p") || !slices.Contains(outer.Hidden, inner.ID) || !slices.Contains(outer.Hidden, inner.ID+"-e") {
		t.Errorf("outer hides %v", outer.Hidden)
	}
	if slices.Contains(outer.Hidden, "c1") {
		t.Errorf("outer fold should leave c1 to the inner fold: %v", outer.Hidden)
	}
	if !slices.Contains(inner.Hidden, "c1") {
		t.Errorf("inner hides %v", inner.Hidden)
	}
}
