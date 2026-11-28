package web

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
	"github.com/sykesm/openfga-explorer/internal/config"
	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
	"github.com/sykesm/openfga-explorer/internal/ids"
	"github.com/sykesm/openfga-explorer/internal/trace"
)

// testExplorer runs the explorer against an in-process OpenFGA loaded with
// the sample store (deploy/seed), reading tuples through the API.
type testExplorer struct {
	t    *testing.T
	srv  *httptest.Server
	base string // /s/<store>/m/latest
}

func newTestExplorer(t *testing.T) *testExplorer {
	t.Helper()
	return explorerFor(t, fgatest.Seed(t))
}

// explorerFor runs the explorer against a store of an in-process OpenFGA.
func explorerFor(t *testing.T, st *fgatest.Store, opts ...func(*config.Config)) *testExplorer {
	t.Helper()
	cfg := config.Default()
	for _, o := range opts {
		o(&cfg)
	}
	cfg.OpenFGA.APIURL = st.URL
	cfg.Types = ids.Rules{"principal": {Trim: true, Case: "upper", Encode: ":#= ,", Display: "decoded"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	client, err := fga.New(fga.Options{APIURL: st.URL})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, client, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &testExplorer{t: t, srv: srv, base: "/s/" + st.ID + "/m/latest"}
}

// get fetches path (relative to the store base when it starts with "~")
// and checks the status.
func (x *testExplorer) do(req *http.Request, want int) (*http.Response, string) {
	x.t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		x.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		x.t.Fatalf("%s %s: status %d, want %d\n%s", req.Method, req.URL, resp.StatusCode, want, b)
	}
	return resp, string(b)
}

func (x *testExplorer) url(path string) string {
	if strings.HasPrefix(path, "~") {
		path = x.base + path[1:]
	}
	return x.srv.URL + path
}

func (x *testExplorer) get(path string, want int) (*http.Response, string) {
	x.t.Helper()
	req, _ := http.NewRequest("GET", x.url(path), nil)
	return x.do(req, want)
}

func (x *testExplorer) post(path string, form url.Values) (*http.Response, string) {
	x.t.Helper()
	req, _ := http.NewRequest("POST", x.url(path), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	return x.do(req, http.StatusOK)
}

func mustContain(t *testing.T, what, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("%s: missing %q", what, w)
		}
	}
}

func TestPages(t *testing.T) {
	x := newTestExplorer(t)
	resp, _ := x.get("/", http.StatusFound) // one store: straight to it
	if loc := resp.Header.Get("Location"); !strings.HasSuffix(loc, "/m/latest/explore") {
		t.Errorf("index redirects to %q", loc)
	}
	_, body := x.get("/?all=1", http.StatusOK)
	mustContain(t, "stores", body, "explorer-demo")

	for _, tc := range []struct {
		path  string
		wants []string
	}{
		{"~/explore", []string{`<h1 class="sr-only">`, `role="tablist" aria-label="Graph"`, `aria-controls="schema-canvas"`, `class="skip" href="#main"`, `id="main"`}},
		{"~/explore?type=document&id=doc-0007", []string{`data-initial-type="document"`, `data-initial-id="doc-0007"`}},
		{"~/check", []string{`id="check-form"`, `name="order"`, `value="guided"`}},
		{"~/check?user=user:anne&relation=viewer&object=document:roadmap&order=model", []string{`value="model" selected`, `hx-trigger="submit, load"`}},
		{"~/model", []string{"document", "can_view", "non_expired_grant"}},
	} {
		_, body := x.get(tc.path, http.StatusOK)
		mustContain(t, tc.path, body, tc.wants...)
		// Every page loads the module entry point and no inline script.
		mustContain(t, tc.path, body, `<script type="module" src="/static/v/`)
		if regexp.MustCompile(`<script>`).MatchString(body) {
			t.Errorf("%s: inline script", tc.path)
		}
	}
}

func TestSecurityHeadersAndAssets(t *testing.T) {
	x := newTestExplorer(t)
	resp, body := x.get("~/check", http.StatusOK)
	csp := resp.Header.Get("Content-Security-Policy")
	if strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
	m := regexp.MustCompile(`src="(/static/v/[0-9a-f]+/js/main\.js)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no versioned module URL")
	}
	resp, js := x.get(m[1], http.StatusOK)
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("module Cache-Control = %q", cc)
	}
	// Relative imports resolve under the same version.
	mustContain(t, "main.js", js, `from './pickers.js'`)
	x.get(strings.Replace(m[1], "main.js", "pickers.js", 1), http.StatusOK)
	resp, _ = x.get("/static/v/stale/js/main.js", http.StatusOK)
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("stale version Cache-Control = %q", cc)
	}
}

func TestExploreFragments(t *testing.T) {
	x := newTestExplorer(t)
	_, body := x.get("~/explore/type?type=document", http.StatusOK)
	mustContain(t, "type panel", body, `data-selected-type="document"`, "Pick an instance", "can_view")

	_, body = x.get("~/explore/instance?object=group:platform", http.StatusOK)
	mustContain(t, "instance panel", body, `data-object="group:platform"`, `aria-controls="ipanel-edges"`, `id="ipanel-edges"`, "graph-patch")

	_, body = x.get("~/explore/incoming?object=group:platform", http.StatusOK)
	mustContain(t, "incoming", body, "folder:platform")

	_, body = x.get("~/explore/edges?dir=out&object=group:platform&relation=member&filter=plat-00", http.StatusOK)
	mustContain(t, "edge chooser", body, "user:plat-001")

	_, body = x.get("~/explore/listusers?object=document:roadmap&relation=viewer&user_type=user", http.StatusOK)
	mustContain(t, "listusers", body, "user:anne")
	// A bad request context is reported, not silently dropped.
	_, body = x.get("~/explore/listusers?object=document:roadmap&relation=viewer&user_type=user&context=not-json", http.StatusOK)
	mustContain(t, "listusers bad context", body, "must be a JSON object")

	// Without a datastore, search confirms exact IDs only.
	_, body = x.get("~/search?field=inst&inst_type=group&inst_q=platform&mode=explore", http.StatusOK)
	mustContain(t, "search", body, `data-value="group:platform"`, "exact IDs only")
	// Typed IDs are normalized per type (upper-cased, percent-encoded).
	_, body = x.get("~/search?field=user&user_type=principal&user_q=svc:build%23ci&mode=form", http.StatusOK)
	mustContain(t, "normalized search", body, `data-value="principal:SVC%3ABUILD%23CI"`)

	// Whether a typed ID is in any tuple: found, not found (every place it
	// can appear was checked), or unsure (user can appear in more relations
	// than the check covers, so it says so rather than "no tuples").
	_, body = x.get("~/search?field=user&user_type=user&user_q=anne&mode=form", http.StatusOK)
	mustContain(t, "exists", body, `data-value="user:anne"`)
	if strings.Contains(body, "may still exist") || strings.Contains(body, "No tuples reference") {
		t.Errorf("user:anne is in tuples:\n%s", body)
	}
	_, body = x.get("~/search?field=inst&inst_type=group&inst_q=no-such-group&mode=explore", http.StatusOK)
	mustContain(t, "missing", body, "No tuples reference <code>group:no-such-group</code>")
	_, body = x.get("~/search?field=user&user_type=user&user_q=no-such-user&mode=form", http.StatusOK)
	mustContain(t, "unsure", body, `data-value="user:no-such-user"`, "it may still exist")
	if strings.Contains(body, "No tuples reference") {
		t.Error("a partial existence check claimed no tuples reference the ID")
	}
	_, body = x.get("~/explore/instance?object=user:no-such-user", http.StatusOK)
	mustContain(t, "instance unsure", body, `data-exists="false"`, "it may still exist")
	_, body = x.get("~/explore/instance?object=group:no-such-group", http.StatusOK)
	mustContain(t, "instance missing", body, "No tuples reference <code>group:no-such-group</code>")

	// An empty search lists the recent objects the browser sends, of this
	// type only; the server keeps none of its own.
	recent := url.QueryEscape(`["user:anne","group:eng","user:bob","user:anne"]`)
	_, body = x.get("~/search?field=user&user_type=user&user_q=&mode=form&recent="+recent, http.StatusOK)
	mustContain(t, "recent objects", body, `data-value="user:anne"`, `data-value="user:bob"`, ">recent<")
	if strings.Contains(body, "group:eng") || strings.Count(body, `data-value="user:anne"`) > 1 {
		t.Errorf("recent objects should be of the searched type, once each:\n%s", body)
	}
	_, body = x.get("~/search?field=user&user_type=user&user_q=&mode=form&recent=not-json", http.StatusOK)
	if strings.Contains(body, ">recent<") {
		t.Error("a malformed recent list should be ignored")
	}

	_, body = x.get("~/relations?object_type=document&user_type=organization", http.StatusOK)
	if !strings.Contains(body, "disabled") {
		t.Errorf("relations for an unreachable user type: nothing disabled\n%s", body)
	}

	_, body = x.get("~/api/schema", http.StatusOK)
	var schema struct{ Elements []map[string]any }
	if err := json.Unmarshal([]byte(body), &schema); err != nil || len(schema.Elements) == 0 {
		t.Errorf("schema JSON: %v (%d elements)", err, len(schema.Elements))
	}
}

// traceData extracts the graph JSON embedded in a check result.
func traceData(t *testing.T, body string) trace.Graph {
	t.Helper()
	m := regexp.MustCompile(`(?s)<script type="application/json" id="trace-data">(.*?)</script>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no trace data")
	}
	var g trace.Graph
	if err := json.Unmarshal([]byte(m[1]), &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestCheckRun(t *testing.T) {
	x := newTestExplorer(t)
	ctx := `{"current_time":"2026-11-24T12:00:00Z","user_ip":"10.1.2.3"}`
	form := url.Values{"user": {"user:eng-002"}, "relation": {"can_view"}, "object": {"document:doc-0007"}, "context": {ctx}, "verify": {"all"}}
	resp, body := x.post("~/check/run", form)
	if push := resp.Header.Get("HX-Push-Url"); !strings.Contains(push, "order=guided") || !strings.Contains(push, "user=user%3Aeng-002") {
		t.Errorf("HX-Push-Url = %q", push)
	}
	mustContain(t, "allowed result", body, `class="verdict v-allowed"`, `<h2 class="big">✓ Allowed</h2>`,
		`data-summary="user:eng-002 → can_view → document:doc-0007 · with context"`,
		"Showing the shortest explanation", `data-done-path="Access is allowed.`)

	g := traceData(t, body)
	var details, skippedWithHint, path int
	for _, e := range g.Elements {
		if e.HasClass("flowpath") {
			path++
		}
		if e.Group != "nodes" || e.Data.Detail == nil {
			continue
		}
		details++
		d := e.Data.Detail.(map[string]any)
		if e.Data.Kind == trace.KindSkipped && e.Data.Hint == trace.Denied {
			skippedWithHint++
			rows, _ := json.Marshal(d["rows"])
			if !strings.Contains(string(rows), "this branch does not hold") {
				t.Errorf("skipped node detail rows: %s", rows)
			}
		}
	}
	if details == 0 || skippedWithHint == 0 || path == 0 {
		t.Errorf("details=%d skipped-with-hint=%d flowpath=%d", details, skippedWithHint, path)
	}

	// Model order evaluates more and skips nothing because of guidance.
	form.Set("order", "model")
	_, body = x.post("~/check/run", form)
	mustContain(t, "model order", body, "Branches were tried in the order the model lists them")

	// A denial explains itself, and its detail links keep the request context.
	form = url.Values{"user": {"user:mallory"}, "relation": {"can_view"}, "object": {"document:doc-0004"}, "context": {ctx}}
	_, body = x.post("~/check/run", form)
	mustContain(t, "denied result", body, `class="verdict v-denied"`, "excluded by “but not blocked from org”")
	g = traceData(t, body)
	found := false
	for _, e := range g.Elements {
		if d, ok := e.Data.Detail.(map[string]any); ok {
			b, _ := json.Marshal(d)
			if strings.Contains(html.UnescapeString(string(b)), "Trace from here") && strings.Contains(string(b), "context=") {
				found = true
			}
		}
	}
	if !found {
		t.Error("no trace-from-here link carrying the request context")
	}

	// Bad input is reported, not a 500.
	req, _ := http.NewRequest("POST", x.url("~/check/run"), strings.NewReader(url.Values{"user": {"nope"}, "relation": {"viewer"}, "object": {"document:x"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	x.do(req, http.StatusBadRequest)
}

func TestNodeDetailLinks(t *testing.T) {
	l := traceLinks{base: "/s/S/m/latest", user: "user:anne", context: `{"a":1}`}
	if got := l.check("viewer", "document:x"); got != `/s/S/m/latest/check?context=%7B%22a%22%3A1%7D&object=document%3Ax&relation=viewer&user=user%3Aanne` {
		t.Errorf("check link = %s", got)
	}
	if got := l.explore("group:eng", "member"); got != "/s/S/m/latest/explore?id=eng&relation=member&type=group" {
		t.Errorf("explore link = %s", got)
	}
	d := detailFor(&trace.Node{Kind: trace.KindSkipped, Relation: "viewer", Object: "document:x", Outcome: trace.Skipped, Label: "editor",
		Hint: trace.Allowed, Checked: []trace.BranchCheck{{Object: "document:x", Relation: "editor", Known: true, Allowed: true}}}, "editor", l, nil)
	b, _ := json.Marshal(d)
	for _, want := range []string{`"this branch also grants access"`, `"✓ holds"`, `"editor on document:x"`, `"Explore object"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("skipped detail missing %s: %s", want, b)
		}
	}
	d = detailFor(&trace.Node{Kind: trace.KindSkipped, Relation: "viewer", Label: "[user]"}, "", l, nil)
	if len(d.Links) != 1 || d.Links[0].Act != "order" || d.Links[0].Order != trace.OrderModel {
		t.Errorf("direct skipped branch should offer a model-order re-run: %+v", d.Links)
	}
}

func TestDoneTexts(t *testing.T) {
	if d := doneTexts(trace.Allowed, trace.OrderAll, true); !strings.Contains(d.Path, "Every route") {
		t.Errorf("exhaustive all-paths: %q", d.Path)
	}
	if d := doneTexts(trace.Allowed, trace.OrderAll, false); !strings.Contains(d.Path, "others may exist") {
		t.Errorf("non-exhaustive all-paths: %q", d.Path)
	}
	if d := doneTexts(trace.Denied, trace.OrderGuided, true); d.Path != d.Other || !strings.HasPrefix(d.Path, "Access is denied") {
		t.Errorf("denied: %+v", d)
	}
	if got := checkSummary("user:a", "viewer", "doc:1", " "); got != "user:a → viewer → doc:1" {
		t.Errorf("summary = %q", got)
	}
}

func TestOrderParam(t *testing.T) {
	for _, tc := range [][3]string{
		{"", "", trace.OrderGuided}, {"model", "", trace.OrderModel}, {"all", "", trace.OrderAll},
		{"", "on", trace.OrderAll}, {"model", "on", trace.OrderModel}, {"bogus", "", trace.OrderGuided},
	} {
		if got := orderParam(tc[0], tc[1]); got != tc[2] {
			t.Errorf("orderParam(%q, %q) = %q, want %q", tc[0], tc[1], got, tc[2])
		}
	}
}

// TestModularModel: a model written as modules shows its module scoping in
// the model page (per-file DSL rebuilt from metadata), Explore, and traces.
func TestModularModel(t *testing.T) {
	srv := fgatest.Start(t)
	dir := filepath.Join(fgatest.SeedDir(), "modular")
	tuples, err := fgatest.ReadTuples(filepath.Join(dir, "tuples.json"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := srv.CreateModularStore(context.Background(), "modular-demo", dir, tuples)
	if err != nil {
		t.Fatal(err)
	}
	x := explorerFor(t, st)

	_, body := x.get("~/model", http.StatusOK)
	mustContain(t, "model page", body, `id="module-compliance"`, `<code>docs/docs.fga</code>`,
		`aria-controls="mfile-mod"`, "fga.mod", "schema: &#39;1.2&#39;", "Combined",
		"OpenFGA doesn't store comments or formatting")
	// The rebuilt compliance file keeps its extension of document.
	if text := html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(body, "")); !strings.Contains(text, "extend type document") {
		t.Error("compliance.fga should contain its extend type block")
	}

	_, body = x.get("~/explore", http.StatusOK)
	mustContain(t, "explore", body, `class="type-find by-module"`, `<span class="modtag">compliance</span>`, `class="type-chip ext" data-type="document"`)

	_, body = x.get("~/explore/type?type=document", http.StatusOK)
	mustContain(t, "type panel", body, `module docs`, `from compliance`)

	form := url.Values{"user": {"user:erin"}, "relation": {"can_view"}, "object": {"document:onboarding"}}
	_, body = x.post("~/check/run", form)
	mustContain(t, "check", body, `class="verdict v-allowed"`)
	found := false
	for _, e := range traceData(t, body).Elements {
		if d, ok := e.Data.Detail.(map[string]any); ok && strings.HasSuffix(e.Data.Label, "#can_audit") {
			b, _ := json.Marshal(d)
			found = strings.Contains(string(b), "compliance (compliance.fga), added to document (extend type)")
		}
	}
	if !found {
		t.Error("can_audit node detail should name its module")
	}
}

// TestModelChecksShown: model checks appear on the model page and next to
// the relations they concern. The model is one OpenFGA accepts.
func TestModelChecksShown(t *testing.T) {
	srv := fgatest.Start(t)
	st, err := srv.CreateStore(context.Background(), "lint", `model
  schema 1.1
type user
type drone
type folder
  relations
    define viewer: [user]
type bucket
  relations
    define owner: [user]
type document
  relations
    define parent: [folder, bucket]
    define viewer: [user] or viewer from parent
    define human: [user]
    define machine: [drone]
    define both: human and machine
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	x := explorerFor(t, st)
	_, body := x.get("~/model", http.StatusOK)
	mustContain(t, "model page", body, "Model checks", "document#both can never be granted", "which has no viewer")
	_, body = x.get("~/explore/type?type=document", http.StatusOK)
	mustContain(t, "type panel", body, `class="lint warning"`, "document#both can never be granted")

	// A clean model says so.
	_, body = newTestExplorer(t).get("~/model", http.StatusOK)
	mustContain(t, "clean model", body, "No problems found")
}

// TestModelDiffPage: the newest version compares with the one before it by
// default, showing rule changes and their effect on who can hold what.
func TestModelDiffPage(t *testing.T) {
	srv := fgatest.Start(t)
	ctx := context.Background()
	st, err := srv.CreateStore(ctx, "versions", "model\n  schema 1.1\ntype user\ntype group\n  relations\n    define member: [user]\ntype doc\n  relations\n    define editor: [user]\n    define viewer: [user] or editor\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	oldest := st.Model
	pm, err := transformer.TransformDSLToProto("model\n  schema 1.1\ntype user\ntype group\n  relations\n    define member: [user]\ntype doc\n  relations\n    define editor: [user, group#member]\n    define viewer: [user] or editor\n    define owner: [user]\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{StoreId: st.ID, SchemaVersion: pm.GetSchemaVersion(), TypeDefinitions: pm.GetTypeDefinitions()}); err != nil {
		t.Fatal(err)
	}
	x := explorerFor(t, st)

	_, body := x.get("~/model", http.StatusOK)
	mustContain(t, "model page", body, "What changed in this version?", "/model/diff?base="+oldest)

	_, body = x.get("~/model/diff", http.StatusOK)
	mustContain(t, "diff", body, `value="`+oldest+`" selected`, "Kinds of user that can hold each relation", "Ways each relation can be granted",
		"+ direct group#member on doc#editor",
		"doc#viewer", "+ group#member", "a relation it depends on changed", "its rule changed",
		"doc#owner", "added")

	// The oldest version has nothing earlier.
	_, body = x.get("/s/"+st.ID+"/m/"+oldest+"/model/diff", http.StatusOK)
	mustContain(t, "oldest", body, "nothing earlier to compare with")
	// Comparing a version with itself finds nothing.
	_, body = x.get("/s/"+st.ID+"/m/"+oldest+"/model/diff?base="+oldest, http.StatusOK)
	mustContain(t, "same", body, "No differences")
}

// TestScenarios: the page offers a starter for the model; runs are
// stateless and report each expectation with its reason.
func TestScenarios(t *testing.T) {
	x := newTestExplorer(t)
	_, body := x.get("~/scenarios", http.StatusOK)
	mustContain(t, "page", body, `id="scn-doc"`, "name: My scenario", "assertions:", `aria-current="page">Scenarios`)

	sample, err := os.ReadFile(filepath.Join(fgatest.SeedDir(), "scenarios.fga.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, body = x.post("~/scenarios/run", url.Values{"doc": {string(sample)}})
	mustContain(t, "run", body, "✓ All 12 passed", "ran against the viewed model", "excluded by “but not blocked from org”",
		`data-grants="0 3 4 "`, "grants 2 checks")

	_, body = x.post("~/scenarios/run", url.Values{"doc": {"model_file: m.fga\ntests: []\n"}})
	mustContain(t, "invalid", body, "The scenario can't run", "file references")

	_, body = x.post("~/scenarios/run", url.Values{"doc": {"tests: [{name: t, check: [{user: 'user:anne', object: 'document:x', assertions: {can_view: true}}]}]\n"}})
	mustContain(t, "failing", body, "✗ 1 failed")
}

// TestSameSite: other sites can't make a browser start work here; same-origin
// requests (loopback demos), link navigations, and non-browser clients can.
func TestSameSite(t *testing.T) {
	x := newTestExplorer(t)
	send := func(method, path string, hdr map[string]string) int {
		req, _ := http.NewRequest(method, x.url(path), strings.NewReader("user=user:anne&relation=viewer&object=document:roadmap"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	host := strings.TrimPrefix(x.srv.URL, "http://")
	for name, tc := range map[string]struct {
		method, path string
		hdr          map[string]string
		want         int
	}{
		"same-origin post (loopback demo)": {"POST", "~/check/run", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": x.srv.URL}, 200},
		"cross-site post":                  {"POST", "~/check/run", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, 403},
		"cross-site fetch of a live query": {"GET", "~/explore/access?object=user:anne&run=1", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "cors"}, 403},
		"link from a ticket (navigation)":  {"GET", "~/check?user=user:anne", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate"}, 200},
		"curl (no browser headers)":        {"POST", "~/check/run", nil, 200},
		"old browser, foreign origin":      {"POST", "~/check/run", map[string]string{"Origin": "https://evil.example"}, 403},
		"old browser, same origin":         {"POST", "~/check/run", map[string]string{"Origin": "http://" + host}, 200},
		"behind a proxy (forwarded host)":  {"POST", "~/check/run", map[string]string{"Origin": "https://explorer.example", "X-Forwarded-Host": "explorer.example"}, 200},
	} {
		if got := send(tc.method, tc.path, tc.hdr); got != tc.want {
			t.Errorf("%s: status %d, want %d", name, got, tc.want)
		}
	}
}

// TestCheckSaysGroupsWereRuledOut: when guided evaluation takes the server's
// word that the user is in none of the groups with access, the page says so
// and offers both ways to dig in (trace a group, or walk every group).
func TestCheckSaysGroupsWereRuledOut(t *testing.T) {
	x := newTestExplorer(t)
	_, body := x.post("~/check/run", url.Values{"user": {"user:nobody"}, "relation": {"viewer"}, "object": {"document:roadmap"}})
	mustContain(t, "ruled-out groups", body,
		"25 groups were not walked</b>: the server's Check says user:nobody is not in them",
		"walk every group",
		"per the server&#39;s Check; those groups were not walked")
}
