package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sykesm/openfga-explorer/internal/fgatest"
)

func TestTraceCommand(t *testing.T) {
	st := fgatest.Seed(t)
	t.Setenv("FGAX_CONFIG", "")
	base := []string{"--api-url", st.URL, "--store", st.ID, "--context", `{"current_time":"2026-11-24T12:00:00Z","user_ip":"10.1.2.3"}`}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runTrace(append(append([]string{}, base...), args...), &out)
		return out.String(), err
	}

	out, err := run("--user", "user:eng-002", "--relation", "can_view", "--object", "document:doc-0007")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"✓ document:doc-0007#can_view", "Allowed: user:eng-002 has can_view", "allowed (walker: allowed)"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}

	out, err = run("--user", "user:sales-007", "--relation", "viewer", "--object", "document:roadmap", "--order", "all")
	if err != nil || !strings.Contains(out, "exhaustive: every branch was examined") {
		t.Errorf("--order all: %v\n%s", err, out)
	}

	out, err = run("--user", "user:mallory", "--relation", "can_view", "--object", "document:doc-0004", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Elements []json.RawMessage
		Root     string
	}
	if err := json.Unmarshal([]byte(out), &g); err != nil || len(g.Elements) == 0 || g.Root == "" {
		t.Errorf("--json: %v (%d elements)", err, len(g.Elements))
	}

	for name, args := range map[string][]string{
		"missing object": {"--user", "user:anne", "--relation", "viewer"},
		"bad order":      {"--user", "user:anne", "--relation", "viewer", "--object", "document:roadmap", "--order", "fastest"},
		"bad context":    {"--user", "user:anne", "--relation", "viewer", "--object", "document:roadmap", "--context", "{"},
		"unknown flag":   {"--nope"},
	} {
		if _, err := run(args...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8090": true, "localhost:8090": true, "[::1]:8090": true,
		"0.0.0.0:8090": false, ":8090": false, "[::]:8090": false, "192.0.2.10:8090": false, "bad": false,
	} {
		if got := loopback(addr); got != want {
			t.Errorf("loopback(%q) = %v, want %v", addr, got, want)
		}
	}
}
