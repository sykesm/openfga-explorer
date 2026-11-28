package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sykesm/openfga-explorer/internal/config"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
)

// patchIDs returns the IDs of the graph elements an HTML fragment carries
// in its <fga-graph-patch>.
func patchIDs(t *testing.T, body string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<fga-graph-patch><script type="application/json">(.*?)</script>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no graph patch")
	}
	var patch instanceGraph
	if err := json.Unmarshal([]byte(m[1]), &patch); err != nil {
		t.Fatal(err)
	}
	return elementIDs(patch.Elements)
}

func elementIDs(els []graphElement) []string {
	var ids []string
	for _, el := range els {
		ids = append(ids, el.Data.ID)
	}
	return ids
}

func getGraph(t *testing.T, x *testExplorer, objects ...string) instanceGraph {
	t.Helper()
	_, body := x.get("~/api/instance-graph?"+url.Values{"object": objects}.Encode(), http.StatusOK)
	var g instanceGraph
	if err := json.Unmarshal([]byte(body), &g); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return g
}

// TestInstanceGraphMatchesPanel: the instance graph of an instance is what
// its panel and its Incoming tab add to the graph, both directions.
func TestInstanceGraphMatchesPanel(t *testing.T) {
	x := newTestExplorer(t)
	const obj = "group:platform"
	_, panel := x.get("~/explore/instance?object="+obj, http.StatusOK)
	_, incoming := x.get("~/explore/incoming?object="+obj, http.StatusOK)
	want := append(patchIDs(t, panel), patchIDs(t, incoming)...)
	slices.Sort(want)
	want = slices.Compact(want)

	g := getGraph(t, x, obj)
	got := elementIDs(g.Elements)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("instance graph:\n got %v\nwant %v", got, want)
	}
	if !slices.Equal(g.Objects, []string{obj}) || len(g.Errors) > 0 {
		t.Errorf("objects %v, errors %v", g.Objects, g.Errors)
	}
	var in, out bool
	for _, el := range g.Elements {
		if el.Group != "edges" {
			continue
		}
		out = out || el.Data.Target == obj
		in = in || strings.HasPrefix(el.Data.Source, obj)
	}
	if !in || !out {
		t.Errorf("edges in both directions: in %v, out %v", in, out)
	}
}

// TestInstanceGraphSeveral: several instances in one request, each element
// once, and every instance asked for marked as expanded (focus).
func TestInstanceGraphSeveral(t *testing.T) {
	x := newTestExplorer(t)
	objs := []string{"group:platform", "document:doc-0007"}
	g := getGraph(t, x, objs...)
	ids := elementIDs(g.Elements)
	if len(slices.Compact(slices.Sorted(slices.Values(ids)))) != len(ids) {
		t.Errorf("repeated elements: %v", ids)
	}
	for _, o := range objs {
		i := slices.Index(ids, o)
		if i < 0 || !g.Elements[i].Data.Focus {
			t.Errorf("%s is not marked expanded", o)
		}
	}
	alone := getGraph(t, x, objs[1])
	for _, id := range elementIDs(alone.Elements) {
		if !slices.Contains(ids, id) {
			t.Errorf("%s from %s alone is missing when asked with another", id, objs[1])
		}
	}
}

func TestInstanceGraphBadRequests(t *testing.T) {
	x := newTestExplorer(t)
	x.get("~/api/instance-graph", http.StatusBadRequest)
	x.get("~/api/instance-graph?object=nosuchtype:1", http.StatusBadRequest)
	x.get("~/api/instance-graph?object=document", http.StatusBadRequest)
	many := url.Values{}
	for i := range config.MaxExpansionInstances + 1 {
		many.Add("object", fmt.Sprintf("document:doc-%04d", i))
	}
	x.get("~/api/instance-graph?"+many.Encode(), http.StatusBadRequest)
}

// someDocuments are instances of the sample data, for requests of several.
func someDocuments(n int) []string {
	var objs []string
	for i := range n {
		objs = append(objs, fmt.Sprintf("document:doc-%04d", i+1))
	}
	return objs
}

// TestInstanceGraphLimits: everyone's expansions are bounded (instances per
// request, a read budget, a rate); admins get their own, higher limits.
func TestInstanceGraphLimits(t *testing.T) {
	limits := func(c *config.Config) {
		c.GraphExpansion.ExpansionLimits = config.ExpansionLimits{MaxInstances: 3, MaxReads: 4, Timeout: time.Minute, PerMinute: 3}
		c.GraphExpansion.Admin = config.ExpansionLimits{MaxInstances: 6, MaxReads: 1000, Timeout: time.Minute, PerMinute: 10}
	}
	x := explorerFor(t, fgatest.Seed(t), limits)

	// More instances than the person's limit: refused, with the limit said.
	_, body := x.get("~/api/instance-graph?"+url.Values{"object": someDocuments(4)}.Encode(), http.StatusBadRequest)
	if !strings.Contains(body, `"error":"at most 3 instances at a time"`) {
		t.Errorf("too many instances: %s", body)
	}

	// The read budget runs out: what was not read is reported, not hidden,
	// and an instance not fully read is not marked expanded.
	g := getGraph(t, x, someDocuments(3)...)
	if len(g.Incomplete) == 0 || !strings.Contains(g.Limit, "read budget (4 reads)") {
		t.Fatalf("budget of 4 reads for 3 instances: complete %v, incomplete %v, limit %q", g.Objects, g.Incomplete, g.Limit)
	}
	for _, el := range g.Elements {
		if slices.Contains(g.Incomplete, el.Data.ID) && el.Data.Focus {
			t.Errorf("%s is marked expanded but was not fully read", el.Data.ID)
		}
	}

	// The rate: 3 a minute. One ran above (the refused request did not
	// start, so it does not count).
	getGraph(t, x, "group:platform")
	getGraph(t, x, "group:platform")
	_, body = x.get("~/api/instance-graph?object=group:platform", http.StatusTooManyRequests)
	if !strings.Contains(body, "graph expansions in the last minute (the limit)") {
		t.Errorf("rate limit: %s", body)
	}

	// An admin may ask for more, and its budget reads them fully.
	admin := explorerFor(t, fgatest.Seed(t), limits, func(c *config.Config) { c.Auth.DangerouslyTreatAnonymousAsAdmin = true })
	g = getGraph(t, admin, someDocuments(6)...)
	if len(g.Objects) != 6 || len(g.Incomplete) > 0 {
		t.Errorf("admin: complete %v, incomplete %v (%s)", g.Objects, g.Incomplete, g.Limit)
	}
	admin.get("~/api/instance-graph?"+url.Values{"object": someDocuments(7)}.Encode(), http.StatusBadRequest)
}

func TestInstanceGraphOff(t *testing.T) {
	x := explorerFor(t, fgatest.Seed(t), func(c *config.Config) { c.GraphExpansion.Enabled = false })
	_, body := x.get("~/api/instance-graph?object=group:platform", http.StatusForbidden)
	if !strings.Contains(body, "graph_expansion.enabled") {
		t.Errorf("off: %s", body)
	}
}
