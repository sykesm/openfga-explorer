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

// liveGuard limits runs per person. Its state is a rate limit, not user
// data: losing it (a restart, another replica) only loosens the limit.
type liveGuard struct {
	mu      sync.Mutex
	running map[string]bool
	runs    map[string][]time.Time
}

func newLiveGuard() *liveGuard {
	return &liveGuard{running: map[string]bool{}, runs: map[string][]time.Time{}}
}

// start reserves a run for who, or says why not. The returned func ends it.
func (g *liveGuard) start(who string, perHour int, now time.Time) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running[who] {
		return nil, errors.New("you already have a query over real data running; wait for it to finish")
	}
	recent := g.runs[who][:0]
	for _, t := range g.runs[who] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= perHour {
		wait := time.Hour - now.Sub(recent[0])
		return nil, fmt.Errorf("you have run %d queries over real data in the last hour (the limit); try again in %v", len(recent), wait.Round(time.Minute))
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
