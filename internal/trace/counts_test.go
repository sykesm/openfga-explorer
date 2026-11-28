package trace_test

import (
	"fmt"
	"testing"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
)

// TestCallAndTupleCounts: the walk reports its calls by kind (adding up to
// the total) and the tuples its reads returned.
func TestCallAndTupleCounts(t *testing.T) {
	srv := fgatest.Start(t)
	ctx := t.Context()
	// doc:1 is shared with 40 groups; the user is a member of the last one.
	const groups = 40
	var keys []*openfgav1.TupleKey
	for g := range groups {
		keys = append(keys, &openfgav1.TupleKey{User: fmt.Sprintf("group:g%02d#member", g), Relation: "viewer", Object: "doc:1"})
	}
	keys = append(keys, &openfgav1.TupleKey{User: "user:anne", Relation: "member", Object: fmt.Sprintf("group:g%02d", groups-1)})
	st, err := srv.CreateStore(ctx, "counts", "model\n  schema 1.1\ntype user\ntype group\n  relations\n    define member: [user]\ntype doc\n  relations\n    define viewer: [group#member]\n", keys)
	if err != nil {
		t.Fatal(err)
	}
	c, err := fga.New(fga.Options{APIURL: st.URL})
	if err != nil {
		t.Fatal(err)
	}
	pm, err := c.Model(ctx, st.ID, st.Model)
	if err != nil {
		t.Fatal(err)
	}
	m, err := schema.Load(pm)
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range []string{trace.OrderGuided, trace.OrderModel, trace.OrderAll} {
		opt := trace.DefaultOptions()
		opt.SetOrder(order)
		opt.MaxFanout = 100 // read every group edge rather than probing
		tr, err := trace.Walk(ctx, c, m, nil, trace.Request{Store: st.ID, Model: st.Model, User: "user:anne", Relation: "viewer", Object: "doc:1"}, opt)
		if err != nil {
			t.Fatal(err)
		}
		if got := tr.ReadCalls + tr.CheckCalls + tr.ListCalls; got != tr.Calls {
			t.Errorf("%s: %d reads + %d checks + %d lists = %d, want %d calls", order, tr.ReadCalls, tr.CheckCalls, tr.ListCalls, got, tr.Calls)
		}
		if tr.ReadCalls == 0 || tr.TuplesRead < groups {
			t.Errorf("%s: %d reads returned %d tuples; reading doc:1#viewer alone returns %d", order, tr.ReadCalls, tr.TuplesRead, groups)
		}
	}
}

// TestCachedReadCounts: reads answered by a read cache in front of the API
// are counted apart from reads that reached OpenFGA.
func TestCachedReadCounts(t *testing.T) {
	srv := fgatest.Start(t)
	ctx := t.Context()
	var keys []*openfgav1.TupleKey
	for g := range 10 {
		keys = append(keys, &openfgav1.TupleKey{User: fmt.Sprintf("group:g%d#member", g), Relation: "viewer", Object: "doc:1"})
	}
	st, err := srv.CreateStore(ctx, "cached", "model\n  schema 1.1\ntype user\ntype group\n  relations\n    define member: [user]\ntype doc\n  relations\n    define viewer: [group#member]\n", keys)
	if err != nil {
		t.Fatal(err)
	}
	client, err := fga.New(fga.Options{APIURL: st.URL})
	if err != nil {
		t.Fatal(err)
	}
	pm, err := client.Model(ctx, st.ID, st.Model)
	if err != nil {
		t.Fatal(err)
	}
	m, err := schema.Load(pm)
	if err != nil {
		t.Fatal(err)
	}
	cache := fga.NewCache(client, time.Minute)
	opt := trace.DefaultOptions()
	opt.SetOrder(trace.OrderModel)
	opt.Parallel = 1 // no lookahead: both runs make the same reads
	req := trace.Request{Store: st.ID, Model: st.Model, User: "user:anne", Relation: "viewer", Object: "doc:1"}
	run := func(r trace.Request) *trace.Trace {
		tr, err := trace.Walk(ctx, cache, m, nil, r, opt)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	first := run(req)
	if first.CachedReads != 0 || first.TuplesCached != 0 || first.TuplesRead < 10 {
		t.Errorf("first run: %d of %d reads and %d of %d tuples from the cache", first.CachedReads, first.ReadCalls, first.TuplesCached, first.TuplesRead)
	}
	second := run(req)
	if second.CachedReads != second.ReadCalls || second.TuplesCached != second.TuplesRead || second.TuplesRead != first.TuplesRead {
		t.Errorf("second run: %d of %d reads and %d of %d tuples from the cache (first run read %d)", second.CachedReads, second.ReadCalls, second.TuplesCached, second.TuplesRead, first.TuplesRead)
	}
	req.Consistency = openfgav1.ConsistencyPreference_HIGHER_CONSISTENCY
	if hc := run(req); hc.CachedReads != 0 || hc.TuplesCached != 0 {
		t.Errorf("higher consistency: %d reads, %d tuples from the cache", hc.CachedReads, hc.TuplesCached)
	}
}
