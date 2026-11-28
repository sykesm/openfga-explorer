package trace_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
	"google.golang.org/protobuf/types/known/structpb"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/")

// TestGolden pins the rendered output of representative traces: the text
// trace, the explanation, and the graph's granting path and folds. Run with
// -update after an intended change, and review the diff.
func TestGolden(t *testing.T) {
	// Always the in-process server: tuple order (and so the text) can differ
	// on other datastores.
	st := fgatest.Seed(t)
	c, err := fga.New(fga.Options{APIURL: st.URL})
	if err != nil {
		t.Fatal(err)
	}
	store := st.ID
	ctx := t.Context()
	modelID, err := c.LatestModelID(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	pm, err := c.Model(ctx, store, modelID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := schema.Load(pm)
	if err != nil {
		t.Fatal(err)
	}
	rc, _ := structpb.NewStruct(map[string]any{"current_time": "2026-11-24T12:00:00Z", "user_ip": "10.1.2.3"})
	cases := []struct {
		name, user, rel, obj, order string
	}{
		{"allowed-guided", "user:eng-002", "can_view", "document:doc-0007", trace.OrderGuided},
		{"allowed-model-order", "user:eng-002", "can_view", "document:doc-0007", trace.OrderModel},
		{"allowed-all-paths", "user:sales-007", "viewer", "document:roadmap", trace.OrderAll},
		{"denied-exclusion", "user:mallory", "can_view", "document:doc-0004", trace.OrderGuided},
		{"denied-fanout", "user:nobody", "viewer", "document:roadmap", trace.OrderGuided},
		{"condition", "user:erin", "can_view", "document:offsite-plan", trace.OrderGuided},
		{"encoded-principal", "principal:SVC%3ABUILD%23CI", "can_view", "document:doc-0001", trace.OrderGuided},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opt := trace.DefaultOptions()
			opt.SetOrder(tc.order)
			req := trace.Request{Store: store, Model: modelID, User: tc.user, Relation: tc.rel, Object: tc.obj, Context: rc}
			tr, err := trace.Walk(ctx, c, m, nil, req, opt)
			if err != nil {
				t.Fatal(err)
			}
			got := render(tr)
			path := filepath.Join("testdata", tc.name+".golden")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create)", err)
			}
			if got != string(want) {
				t.Errorf("%s differs from %s (run with -update if intended):\n%s", tc.name, path, diff(string(want), got))
			}
		})
	}
}

// render is the golden form of a trace: stable across runs (no timings or IDs
// of stores and models).
func render(tr *trace.Trace) string {
	var b strings.Builder
	ex := tr.Explanation(nil)
	fmt.Fprintf(&b, "decision: %s (walker %s)\n%s\n", tr.Decision(), tr.Root.Outcome, ex.Headline)
	b.WriteString(ex.Format())
	if ex.Steps != nil {
		b.WriteString("top-down:\n" + ex.FormatDown())
	}
	fmt.Fprintf(&b, "gaps: %q\nguided skips: %d\ngroups ruled out: %d\n\n%s\n", tr.Gaps(), tr.GuidedSkips, tr.GroupsRuledOut, tr.Text())
	g := tr.Graph()
	var path []string
	for _, e := range g.Elements {
		if e.HasClass("flowpath") {
			id := e.Data.ID
			if d := e.Data.Depth; d != nil {
				id += fmt.Sprintf("@%v", *d)
			}
			path = append(path, id)
		}
	}
	fmt.Fprintf(&b, "granting path: %s\n", strings.Join(path, " "))
	for _, f := range g.Folds {
		fmt.Fprintf(&b, "fold %s under %s hides %d: %s\n", f.ID, f.Parent, len(f.Hidden), strings.ReplaceAll(f.Label, "\n", " "))
	}
	fmt.Fprintf(&b, "elements: %d, steps: %d\n", len(g.Elements), len(g.Steps))
	return b.String()
}

// diff shows the first differing lines.
func diff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	n := 0
	for i := 0; i < max(len(w), len(g)) && n < 12; i++ {
		var a, c string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			c = g[i]
		}
		if a != c {
			fmt.Fprintf(&b, "line %d:\n  - %s\n  + %s\n", i+1, a, c)
			n++
		}
	}
	return b.String()
}
