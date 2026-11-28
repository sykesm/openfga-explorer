package trace_test

import (
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestAgainstServer compares walker decisions with server Check results across
// the sample store (deploy/seed); see testStore for which server it uses.
func TestAgainstServer(t *testing.T) {
	if testing.Short() {
		t.Skip("compares thousands of checks; skipped with -short")
	}
	c, store := testStore(t)
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
	reqCtx, _ := structpb.NewStruct(map[string]any{"current_time": "2026-11-24T12:00:00Z", "user_ip": "10.1.2.3"})

	users := []string{
		"user:anne", "user:mallory", "user:eng-001", "user:plat-001", "user:team150-u1", "user:team007-u2",
		"user:sales-007", "user:sales-020", "user:carol", "user:dave", "user:erin", "user:frank", "user:gina",
		"user:hank", "user:nobody", "principal:SVC%3ABUILD%23CI", "group:platform#member", "group:sales#member",
	}
	objects := []string{
		"document:doc-0001", "document:doc-0002", "document:doc-0003", "document:doc-0004", "document:doc-0020",
		"document:doc-0070", "document:roadmap", "document:spec", "document:handbook", "document:offsite-plan",
		"document:globex-secret", "folder:root", "folder:design-docs", "group:eng", "group:team-150", "organization:acme",
	}
	budgets := map[string]func(*trace.Options){
		"default": func(*trace.Options) {},
		// Tiny budgets force truncation, probes, and undecided branches. The
		// walker may be undecided, but must never contradict the server.
		"tiny":   func(o *trace.Options) { o.MaxCalls, o.MaxFanout, o.ProbeLimit = 10, 3, 2 },
		"narrow": func(o *trace.Options) { o.MaxFanout, o.ProbeLimit = 2, 1 },
	}
	for name, tweak := range budgets {
		t.Run(name, func(t *testing.T) { compareAll(t, c, m, store, modelID, reqCtx, users, objects, tweak) })
	}
}

func compareAll(t *testing.T, c *fga.Client, m *schema.Model, store, modelID string, reqCtx *structpb.Struct, users, objects []string, tweak func(*trace.Options)) {
	ctx := t.Context()
	var checked, unknown, mismatched int
	for _, obj := range objects {
		typ, _, _ := schema.SplitObject(obj)
		for _, rel := range m.Types[typ].RelationNames {
			for _, u := range users {
				req := trace.Request{Store: store, Model: modelID, User: u, Relation: rel, Object: obj, Context: reqCtx}
				opt := trace.DefaultOptions()
				opt.Verify = trace.VerifyNone
				tweak(&opt)
				tr, err := trace.Walk(ctx, c, m, nil, req, opt)
				if err != nil {
					t.Fatalf("%s %s %s: %v", u, rel, obj, err)
				}
				resp, err := c.Check(ctx, &openfgav1.CheckRequest{
					StoreId: store, AuthorizationModelId: modelID, Context: reqCtx,
					TupleKey: &openfgav1.CheckRequestTupleKey{User: u, Relation: rel, Object: obj},
				})
				if err != nil {
					if strings.Contains(err.Error(), "validation_error") || strings.Contains(err.Error(), "invalid") {
						continue // user type not allowed for relation, etc.
					}
					t.Fatalf("server check %s %s %s: %v", u, rel, obj, err)
				}
				checked++
				// Every decided trace must explain itself consistently with its outcome.
				if ex := tr.Explanation(nil); tr.Root.Outcome == trace.Allowed && !strings.HasPrefix(ex.Headline, "Allowed: ") ||
					tr.Root.Outcome == trace.Denied && !strings.HasPrefix(ex.Headline, "Denied: ") ||
					tr.Root.Outcome == trace.Allowed && len(ex.Steps) == 0 {
					t.Errorf("explanation for %s %s %s (%s): %q %q", u, rel, obj, tr.Root.Outcome, ex.Headline, ex.Steps)
				}
				got := tr.Root.Outcome
				want := trace.Denied
				if resp.GetAllowed() {
					want = trace.Allowed
				}
				switch {
				case got == trace.Unknown:
					unknown++
				case got != want:
					mismatched++
					t.Errorf("MISMATCH %s %s %s: walker=%s server=%s\n%s", u, rel, obj, got, want, tr.Text())
				}
			}
		}
	}
	t.Logf("checked %d, undecided %d, mismatched %d", checked, unknown, mismatched)
}

// TestReachabilityIsSound verifies that relations the check form disables
// (unreachable from the user type) are never allowed by the server.
func TestReachabilityIsSound(t *testing.T) {
	c, store := testStore(t)
	ctx := t.Context()
	modelID, err := c.LatestModelID(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	pm, _ := c.Model(ctx, store, modelID)
	m, _ := schema.Load(pm)
	reqCtx, _ := structpb.NewStruct(map[string]any{"current_time": "2026-11-24T12:00:00Z", "user_ip": "10.1.2.3"})
	users := []string{"user:anne", "user:mallory", "user:carol", "user:sales-007", "user:frank", "user:gina",
		"principal:SVC%3ABUILD%23CI", "group:platform#member", "group:eng#member", "folder:root", "organization:acme"}
	objects := []string{"document:doc-0001", "document:doc-0004", "document:roadmap", "document:spec", "document:handbook",
		"document:offsite-plan", "folder:root", "folder:design-docs", "group:eng", "group:platform", "organization:acme"}
	var unreachable int
	for _, obj := range objects {
		typ, _, _ := schema.SplitObject(obj)
		for _, u := range users {
			subj, _ := schema.ParseSubject(u)
			ut := subj.Type
			if subj.Relation != "" {
				ut += "#" + subj.Relation
			}
			reach := m.Reachable(typ, ut)
			for _, rel := range m.Types[typ].RelationNames {
				if reach[rel] {
					continue
				}
				unreachable++
				resp, err := c.Check(ctx, &openfgav1.CheckRequest{
					StoreId: store, AuthorizationModelId: modelID, Context: reqCtx,
					TupleKey: &openfgav1.CheckRequestTupleKey{User: u, Relation: rel, Object: obj},
				})
				if err == nil && resp.GetAllowed() {
					t.Errorf("%s %s %s: marked unreachable but the server allows it", u, rel, obj)
				}
			}
		}
	}
	t.Logf("%d unreachable (user, relation, object) combinations, none allowed", unreachable)
}
