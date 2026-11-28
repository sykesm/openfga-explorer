package web

import (
	"context"
	"testing"
	"time"
)

// The progress registry pairs a run with its stream: finishing twice must
// not panic, and streams for IDs no run uses must not accumulate.
func TestProgressRegistry(t *testing.T) {
	p := newProgressRegistry()

	// A stream whose run never starts leaves nothing behind.
	st := p.watch("idle0000")
	p.unwatch("idle0000", st)
	if len(p.runs) != 0 {
		t.Errorf("an unused stream left %d entries", len(p.runs))
	}

	// Stream first, then the run: they share the entry, and finishing it
	// (twice, as a reused run ID would) closes done once.
	st = p.watch("run00000")
	run := p.start("run00000")
	if run != st {
		t.Fatal("the run and its stream should share one entry")
	}
	p.finish("run00000")
	p.finish("run00000")
	select {
	case <-st.done:
	default:
		t.Error("finish did not mark the run done")
	}
	// The stream ending does not drop a run's entry (a late stream still sees done).
	p.unwatch("run00000", st)
	if p.runs["run00000"] != run {
		t.Error("the stream ending dropped the run's entry")
	}

	// Finishing an ID that never started is a no-op.
	p.finish("never000")
	if _, ok := p.runs["never000"]; ok {
		t.Error("finishing an unknown ID created an entry")
	}
}

// A request waiting for a slot gives up when it is canceled, not only after
// the wait.
func TestAcquire(t *testing.T) {
	sem := make(chan struct{}, 1)
	release, ok := acquire(t.Context(), sem)
	if !ok {
		t.Fatal("a free slot was not taken")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	if _, ok := acquire(ctx, sem); ok {
		t.Error("took a slot that was in use")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("a canceled request waited %v", d)
	}
	release()
	if r2, ok := acquire(t.Context(), sem); !ok {
		t.Error("the released slot was not free")
	} else {
		r2()
	}
}
