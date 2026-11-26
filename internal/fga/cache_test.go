package fga

import (
	"context"
	"errors"
	"testing"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
)

// countingAPI answers Read with a fresh response per call and counts calls.
type countingAPI struct {
	API
	reads int
	fail  bool
}

func (c *countingAPI) Read(_ context.Context, r *openfgav1.ReadRequest) (*openfgav1.ReadResponse, error) {
	c.reads++
	if c.fail {
		return nil, errors.New("boom")
	}
	return &openfgav1.ReadResponse{ContinuationToken: r.GetTupleKey().GetObject()}, nil
}

func read(obj string, consistency openfgav1.ConsistencyPreference) *openfgav1.ReadRequest {
	return &openfgav1.ReadRequest{StoreId: "s", TupleKey: &openfgav1.ReadRequestTupleKey{Object: obj, Relation: "viewer"}, Consistency: consistency}
}

func TestCacheServesFreshPages(t *testing.T) {
	api := &countingAPI{}
	c := NewCache(api, time.Minute)
	ctx := context.Background()
	for range 3 {
		resp, err := c.Read(ctx, read("doc:1", openfgav1.ConsistencyPreference_UNSPECIFIED))
		if err != nil || resp.GetContinuationToken() != "doc:1" {
			t.Fatalf("read: %v %v", resp, err)
		}
	}
	if api.reads != 1 {
		t.Errorf("reads = %d, want 1", api.reads)
	}
	if _, err := c.Read(ctx, read("doc:2", openfgav1.ConsistencyPreference_UNSPECIFIED)); err != nil || api.reads != 2 {
		t.Errorf("a different key must miss: reads = %d", api.reads)
	}
	if h, m := c.Stats(); h != 2 || m != 2 {
		t.Errorf("stats = %d hits, %d misses", h, m)
	}
}

func TestCacheBypassAndExpiry(t *testing.T) {
	api := &countingAPI{}
	c := NewCache(api, time.Minute)
	ctx := context.Background()
	for range 2 {
		c.Read(ctx, read("doc:1", openfgav1.ConsistencyPreference_HIGHER_CONSISTENCY))
	}
	if api.reads != 2 {
		t.Errorf("higher consistency must bypass the cache: reads = %d", api.reads)
	}
	c.TTL = time.Nanosecond
	c.Read(ctx, read("doc:3", 0))
	time.Sleep(time.Millisecond)
	c.Read(ctx, read("doc:3", 0))
	if api.reads != 4 {
		t.Errorf("expired entries must be re-read: reads = %d", api.reads)
	}
	c.TTL = 0
	c.Read(ctx, read("doc:3", 0))
	if api.reads != 5 {
		t.Errorf("TTL 0 disables caching: reads = %d", api.reads)
	}
}

func TestCacheErrorsAndEviction(t *testing.T) {
	api := &countingAPI{fail: true}
	c := NewCache(api, time.Minute)
	ctx := context.Background()
	for range 2 {
		if _, err := c.Read(ctx, read("doc:1", 0)); err == nil {
			t.Fatal("error not returned")
		}
	}
	if api.reads != 2 {
		t.Errorf("errors must not be cached: reads = %d", api.reads)
	}
	api.fail = false
	c.MaxSize = 10
	for i := range 25 {
		c.Read(ctx, read(string(rune('a'+i)), 0))
	}
	if n := len(c.entries); n > c.MaxSize {
		t.Errorf("cache grew to %d entries, max %d", n, c.MaxSize)
	}
}

func TestCacheBatchCheckForwarding(t *testing.T) {
	c := NewCache(&countingAPI{}, time.Minute) // no BatchCheck
	_, err := c.BatchCheck(context.Background(), &openfgav1.BatchCheckRequest{})
	var e *Error
	if !errors.As(err, &e) || e.Status != 501 {
		t.Errorf("BatchCheck without support: %v", err)
	}
}

// TestCacheReport: a Read served from the cache's entries reports a hit; a
// Read that reaches the API behind it (first read, or higher consistency)
// does not; Reads without a report context still work.
func TestCacheReport(t *testing.T) {
	api := &countingAPI{}
	c := NewCache(api, time.Minute)
	hit := func(r *openfgav1.ReadRequest) bool {
		ctx, hit := WithCacheReport(context.Background())
		if _, err := c.Read(ctx, r); err != nil {
			t.Fatal(err)
		}
		return *hit
	}
	if hit(read("doc:1", openfgav1.ConsistencyPreference_UNSPECIFIED)) {
		t.Error("the first read reported a cache hit")
	}
	if !hit(read("doc:1", openfgav1.ConsistencyPreference_UNSPECIFIED)) {
		t.Error("the repeated read did not report a cache hit")
	}
	if hit(read("doc:1", openfgav1.ConsistencyPreference_HIGHER_CONSISTENCY)) {
		t.Error("a higher-consistency read reported a cache hit")
	}
	if api.reads != 2 {
		t.Errorf("the API was read %d times, want 2", api.reads)
	}
	if _, err := c.Read(context.Background(), read("doc:1", openfgav1.ConsistencyPreference_UNSPECIFIED)); err != nil {
		t.Fatal(err)
	}
}
