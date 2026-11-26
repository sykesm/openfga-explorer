package web

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Live queries run against the store's real tuples, so they cost the shared
// OpenFGA service real work. Each run is started explicitly by an admin and
// is bounded: one at a time per person, a number of runs per hour (counted
// per replica), a call budget, and a time limit.

// errNotAllowed is returned when live queries are off or the person is not an admin.
var errNotAllowed = errors.New("queries over real data are for admins, and must be enabled (live_queries.enabled)")

// guard limits runs per person: one at a time, and a number per window.
// Its state is a rate limit, not user data: losing it (a restart, another
// replica) only loosens the limit.
type guard struct {
	one, many string // what a run is called: "graph expansion", "graph expansions"
	window    time.Duration
	unit      string // the window, in words: "hour"

	mu      sync.Mutex
	running map[string]bool
	runs    map[string][]time.Time
}

func newGuard(one, many string, window time.Duration, unit string) *guard {
	return &guard{one: one, many: many, window: window, unit: unit, running: map[string]bool{}, runs: map[string][]time.Time{}}
}

// newLiveGuard limits queries over real data (per hour).
func newLiveGuard() *guard {
	return newGuard("query over real data", "queries over real data", time.Hour, "hour")
}

// start reserves a run for who, or says why not. The returned func ends it.
func (g *guard) start(who string, limit int, now time.Time) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running[who] {
		return nil, fmt.Errorf("you already have a %s running; wait for it to finish", g.one)
	}
	recent := g.runs[who][:0]
	for _, t := range g.runs[who] {
		if now.Sub(t) < g.window {
			recent = append(recent, t)
		}
	}
	if len(recent) >= limit {
		wait := g.window - now.Sub(recent[0])
		return nil, fmt.Errorf("you have run %d %s in the last %s (the limit); try again in %v", len(recent), g.many, g.unit, wait.Round(g.window/60))
	}
	g.runs[who] = append(recent, now)
	g.running[who] = true
	return func() {
		g.mu.Lock()
		delete(g.running, who)
		g.mu.Unlock()
	}, nil
}

// budget counts API calls in a run.
type budget struct {
	used atomic.Int64
	max  int64
}

var errBudget = errors.New("the run used its call budget")

// spend reserves n calls, or fails when the budget would be exceeded.
func (b *budget) spend(n int) error {
	if b.used.Add(int64(n)) > b.max {
		return errBudget
	}
	return nil
}

// liveAllowed reports whether the request may run live queries.
func (s *Server) liveAllowed(r *http.Request) bool {
	return s.cfg.LiveQueries.Enabled && identity(r).Admin()
}
