package trace_test

import (
	"context"
	"fmt"
	"testing"

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
	ctx := context.Background()
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
		tr, err := trace.Explain(ctx, c, m, nil, trace.Request{Store: st.ID, Model: st.Model, User: "user:anne", Relation: "viewer", Object: "doc:1"}, opt)
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
