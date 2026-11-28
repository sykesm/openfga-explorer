package trace_test

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
	"google.golang.org/protobuf/types/known/structpb"
)

// slowAPI adds a fixed round-trip delay to every call, simulating a remote
// OpenFGA cluster. batch controls whether BatchCheck is exposed.
type slowAPI struct {
	fga.API
	delay time.Duration
	calls atomic.Int64
}

func (s *slowAPI) wait() { s.calls.Add(1); time.Sleep(s.delay) }
func (s *slowAPI) Read(ctx context.Context, r *openfgav1.ReadRequest) (*openfgav1.ReadResponse, error) {
	s.wait()
	return s.API.Read(ctx, r)
}
func (s *slowAPI) Check(ctx context.Context, r *openfgav1.CheckRequest) (*openfgav1.CheckResponse, error) {
	s.wait()
	return s.API.Check(ctx, r)
}
func (s *slowAPI) ListObjects(ctx context.Context, r *openfgav1.ListObjectsRequest) (*openfgav1.ListObjectsResponse, error) {
	s.wait()
	return s.API.ListObjects(ctx, r)
}

type slowBatchAPI struct{ *slowAPI }

func (s slowBatchAPI) BatchCheck(ctx context.Context, r *openfgav1.BatchCheckRequest) (*openfgav1.BatchCheckResponse, error) {
	s.wait()
	return s.API.(fga.BatchChecker).BatchCheck(ctx, r)
}

// TestLatency compares sequential reads + per-node verification with
// lookahead + BatchCheck at 10ms per call, and requires identical decisions
// and step sequences. Run with LATENCY=1 and the in-process or live server (see testStore).
func TestLatency(t *testing.T) {
	if os.Getenv("LATENCY") == "" {
		t.Skip("set LATENCY=1 (takes a while: every call is delayed 10ms)")
	}
	c, store := testStore(t)
	ctx := t.Context()
	modelID, _ := c.LatestModelID(ctx, store)
	pm, _ := c.Model(ctx, store, modelID)
	m, _ := schema.Load(pm)
	rc, _ := structpb.NewStruct(map[string]any{"current_time": "2026-11-24T12:00:00Z", "user_ip": "10.1.2.3"})
	cases := [][3]string{
		{"user:sales-007", "viewer", "document:roadmap"},
		{"user:nobody", "viewer", "document:roadmap"},
		{"user:anne", "can_view", "document:doc-0003"},
		{"user:mallory", "can_view", "document:doc-0004"},
		{"user:plat-001", "can_share", "document:spec"},
	}
	for _, allPaths := range []bool{false, true} {
		var seqTotal, parTotal time.Duration
		for _, q := range cases {
			req := trace.Request{Store: store, Model: modelID, User: q[0], Relation: q[1], Object: q[2], Context: rc}
			run := func(parallel int, batch bool) (*trace.Trace, time.Duration, int64) {
				slow := &slowAPI{API: c, delay: 10 * time.Millisecond}
				var api fga.API = slow
				if batch {
					api = slowBatchAPI{slow}
				}
				opt := trace.DefaultOptions()
				opt.Parallel, opt.AllPaths = parallel, allPaths
				start := time.Now()
				tr, err := trace.Walk(ctx, api, m, nil, req, opt)
				if err != nil {
					t.Fatal(err)
				}
				return tr, time.Since(start), slow.calls.Load()
			}
			seq, seqDur, seqCalls := run(1, false)
			par, parDur, parCalls := run(16, true)
			if seq.Root.Outcome != par.Root.Outcome || seq.Text() != par.Text() {
				t.Errorf("%v: lookahead changed the result\n--- sequential\n%s--- parallel\n%s", q, seq.Text(), par.Text())
			}
			seqTotal += seqDur
			parTotal += parDur
			t.Logf("allPaths=%-5v %-46s sequential %6s (%3d calls)  parallel+batch %6s (%3d calls)  %.1fx",
				allPaths, q[0]+" "+q[1]+" "+q[2], seqDur.Round(time.Millisecond), seqCalls, parDur.Round(time.Millisecond), parCalls,
				float64(seqDur)/float64(parDur))
		}
		t.Logf("allPaths=%v total: sequential %s, parallel+batch %s (%.1fx)", allPaths, seqTotal.Round(time.Millisecond), parTotal.Round(time.Millisecond), float64(seqTotal)/float64(parTotal))
	}
}
