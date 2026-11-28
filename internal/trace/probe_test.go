package trace_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
)

// fakeAPI answers Read from a fixed tuple list and ListObjects from a fixed
// map (or a fixed error per type), recording which types were listed.
type fakeAPI struct {
	tuples  []*openfgav1.TupleKey
	objects map[string][]string // type → objects the user holds the relation on
	fail    map[string]error    // type → ListObjects error
	mu      sync.Mutex
	listed  []string
}

func (f *fakeAPI) Read(_ context.Context, r *openfgav1.ReadRequest) (*openfgav1.ReadResponse, error) {
	k := r.GetTupleKey()
	var out []*openfgav1.Tuple
	for _, t := range f.tuples {
		if t.GetObject() == k.GetObject() && (k.GetRelation() == "" || t.GetRelation() == k.GetRelation()) && (k.GetUser() == "" || t.GetUser() == k.GetUser()) {
			out = append(out, &openfgav1.Tuple{Key: t})
		}
	}
	return &openfgav1.ReadResponse{Tuples: out}, nil
}

func (f *fakeAPI) Check(context.Context, *openfgav1.CheckRequest) (*openfgav1.CheckResponse, error) {
	return nil, errors.New("not used")
}

func (f *fakeAPI) ListObjects(_ context.Context, r *openfgav1.ListObjectsRequest) (*openfgav1.ListObjectsResponse, error) {
	f.mu.Lock()
	f.listed = append(f.listed, r.GetType())
	f.mu.Unlock()
	if err := f.fail[r.GetType()]; err != nil {
		return nil, err
	}
	return &openfgav1.ListObjectsResponse{Objects: f.objects[r.GetType()]}, nil
}

// TestTTUProbe covers the probe an "X from Y" branch falls back on when it
// has more parents than it may examine: it stops once a probe finds access,
// and says when a probe fails.
func TestTTUProbe(t *testing.T) {
	pm, err := transformer.TransformDSLToProto(`model
  schema 1.1
type user
type team
  relations
    define member: [user]
type org
  relations
    define member: [user]
type doc
  relations
    define parent: [team, org]
    define viewer: member from parent`)
	if err != nil {
		t.Fatal(err)
	}
	m, err := schema.Load(pm)
	if err != nil {
		t.Fatal(err)
	}
	tuples := []*openfgav1.TupleKey{{Object: "team:t4", Relation: "member", User: "user:anne"}}
	for _, p := range []string{"team:t0", "team:t1", "team:t2", "team:t3", "team:t4", "org:o0", "org:o1"} {
		tuples = append(tuples, &openfgav1.TupleKey{Object: "doc:1", Relation: "parent", User: p})
	}
	walk := func(api *fakeAPI) *trace.Trace {
		opt := trace.DefaultOptions()
		opt.MaxFanout, opt.Verify = 2, trace.VerifyNone // too many parents: probe
		tr, err := trace.Walk(t.Context(), api, m, nil, trace.Request{Store: "s", Model: "m", User: "user:anne", Relation: "viewer", Object: "doc:1"}, opt)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}

	api := &fakeAPI{tuples: tuples, objects: map[string][]string{"team": {"team:t4"}, "org": {"org:o9"}}}
	if tr := walk(api); tr.Root.Outcome != trace.Allowed {
		t.Fatalf("outcome %s\n%s", tr.Root.Outcome, tr.Text())
	}
	if !slices.Equal(api.listed, []string{"team"}) {
		t.Errorf("listed %v: the probe should stop once team:t4 grants access", api.listed)
	}

	api = &fakeAPI{tuples: tuples, objects: map[string][]string{"org": {"org:o9"}}, fail: map[string]error{"team": errors.New("unavailable")}}
	tr := walk(api)
	if tr.Root.Outcome != trace.Unknown {
		t.Errorf("outcome %s: a failed probe leaves the branch undecided\n%s", tr.Root.Outcome, tr.Text())
	}
	if n := findKind(tr.Root, trace.KindTTU); n == nil || !slices.ContainsFunc(n.Notes, func(s string) bool { return strings.Contains(s, "probe team#member failed: unavailable") }) {
		t.Errorf("want a note that the team probe failed; got %v", n)
	}
}

func findKind(n *trace.Node, k trace.Kind) *trace.Node {
	if n == nil {
		return nil
	}
	if n.Kind == k {
		return n
	}
	for _, l := range n.Children {
		if f := findKind(l.Node, k); f != nil {
			return f
		}
	}
	return nil
}
