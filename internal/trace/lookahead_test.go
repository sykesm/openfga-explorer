package trace_test

import (
	"testing"

	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
	"google.golang.org/protobuf/types/known/structpb"
)

// Lookahead reads only save time: under any budget, a walk with lookahead
// decides, counts calls and steps exactly as one without, and lookahead
// never marks the walk as out of budget.
func TestLookaheadDoesNotChangeTheWalk(t *testing.T) {
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
	cases := []struct{ user, rel, obj string }{
		{"user:eng-002", "can_view", "document:doc-0007"},
		{"user:mallory", "can_view", "document:doc-0004"},
		{"user:nobody", "viewer", "document:roadmap"},
		{"user:sales-007", "viewer", "document:roadmap"},
	}
	walk := func(req trace.Request, order string, maxCalls, parallel int) *trace.Trace {
		t.Helper()
		opt := trace.DefaultOptions()
		opt.SetOrder(order)
		opt.Verify = trace.VerifyNone
		opt.MaxCalls, opt.Parallel = maxCalls, parallel
		tr, err := trace.Explain(ctx, c, m, nil, req, opt)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	for _, tc := range cases {
		for _, order := range []string{trace.OrderGuided, trace.OrderModel, trace.OrderAll} {
			req := trace.Request{Store: store, Model: modelID, User: tc.user, Relation: tc.rel, Object: tc.obj, Context: reqCtx}
			need := walk(req, order, 5000, 1).Calls
			// The exact budget the walk needs, and tighter ones that cut it short.
			for _, budget := range []int{need, need / 2, 3} {
				seq, ahead := walk(req, order, budget, 1), walk(req, order, budget, 16)
				if seq.Root.Outcome != ahead.Root.Outcome || seq.Calls != ahead.Calls || seq.Exhausted != ahead.Exhausted || seq.Text() != ahead.Text() {
					t.Errorf("%s %s %s (%s, budget %d): without lookahead %s, %d calls, exhausted %v; with lookahead %s, %d calls, exhausted %v",
						tc.user, tc.rel, tc.obj, order, budget, seq.Root.Outcome, seq.Calls, seq.Exhausted, ahead.Root.Outcome, ahead.Calls, ahead.Exhausted)
				}
				if budget == need && ahead.Exhausted {
					t.Errorf("%s %s %s (%s): lookahead exhausted a budget the walk fits in (%d calls)", tc.user, tc.rel, tc.obj, order, need)
				}
			}
		}
	}
}
