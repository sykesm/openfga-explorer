package web

import (
	"fmt"
	"testing"
)

// The model cache keeps the most recently used models, up to maxModels.
func TestModelCacheEviction(t *testing.T) {
	s := &Server{models: map[string]*modelEntry{}}
	ready := func() *modelEntry {
		e := &modelEntry{ready: make(chan struct{})}
		close(e.ready)
		return e
	}
	for i := range maxModels {
		s.models[fmt.Sprintf("st/m%d", i)] = ready()
	}
	// Ask for every model, m0 last, so m1 becomes the least recently used.
	for i := 1; i <= maxModels; i++ {
		if _, err := s.model(t.Context(), "st", fmt.Sprintf("m%d", i%maxModels)); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	s.models["st/new"] = ready()
	s.modelSeq++
	s.models["st/new"].used = s.modelSeq
	s.evictModels()
	_, m1 := s.models["st/m1"]
	_, m0 := s.models["st/m0"]
	n := len(s.models)
	s.mu.Unlock()
	if n != maxModels || m1 || !m0 {
		t.Errorf("after one more model: %d kept, m1 kept %v (want evicted), m0 kept %v (want kept)", n, m1, m0)
	}
}
