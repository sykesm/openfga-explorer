package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sykesm/openfga-explorer/internal/scenario"
)

func (x *testExplorer) capture(form url.Values) captureResponse {
	x.t.Helper()
	resp, err := http.PostForm(x.url("~/scenarios/capture"), form)
	if err != nil {
		x.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out captureResponse
	if err := json.Unmarshal(b, &out); err != nil {
		x.t.Fatalf("%s: %s", err, b)
	}
	return out
}

// TestCaptureReproduces: a captured check, with the tuples on its granting
// path, passes on its own in the sandbox — masked or not, conditions included.
func TestCaptureReproduces(t *testing.T) {
	x := newTestExplorer(t)
	ctx := `{"current_time":"2026-11-24T12:00:00Z","user_ip":"10.1.2.3"}`
	draft := ""
	for _, tc := range []struct{ user, rel, obj string }{
		{"user:eng-002", "can_view", "document:doc-0007"},
		{"user:frank", "can_view", "document:offsite-plan"}, // through a conditional (time-limited) grant
		{"user:mallory", "can_view", "document:doc-0004"},   // denied
	} {
		for _, mask := range []bool{false, true} {
			form := url.Values{"user": {tc.user}, "relation": {tc.rel}, "object": {tc.obj}, "context": {ctx}, "draft": {draft}}
			if mask {
				form.Set("mask", "1")
			}
			res := x.capture(form)
			if res.Error != "" {
				t.Fatalf("%s: %s", tc.user, res.Error)
			}
			draft = res.Draft
			if mask && strings.Contains(res.Name, strings.TrimPrefix(tc.user, "user:")) {
				t.Errorf("masked test name still has the real ID: %q", res.Name)
			}
		}
	}
	d, err := scenario.Parse([]byte(draft))
	if err != nil || len(d.Tests) != 6 {
		t.Fatalf("draft: %v (%d tests)\n%s", err, len(d.Tests), draft)
	}
	if !strings.Contains(draft, "name: non_expired_grant") || !strings.Contains(draft, "grant_duration:") {
		t.Errorf("the conditional tuple should keep its condition and parameters:\n%s", draft)
	}
	// Tests alternate unmasked, masked: masked ones carry none of the real IDs.
	for i, tc := range d.Tests {
		b, _ := json.Marshal(tc)
		real := strings.Contains(string(b), "eng-002") || strings.Contains(string(b), "frank") || strings.Contains(string(b), "mallory") ||
			strings.Contains(string(b), "doc-0007") || strings.Contains(string(b), "offsite-plan")
		if masked := i%2 == 1; masked == real {
			t.Errorf("test %d (masked %v) has real IDs: %v\n%s", i, masked, real, b)
		}
	}
	_, body := x.post("~/scenarios/run", url.Values{"doc": {draft}})
	mustContain(t, "replay", body, "✓ All 6 passed")

	// A broken draft is reported, not replaced.
	res := x.capture(url.Values{"user": {"user:anne"}, "relation": {"viewer"}, "object": {"document:roadmap"}, "draft": {"tests: [oops\n"}})
	if res.Error == "" || res.Draft != "" {
		t.Errorf("broken draft: %+v", res)
	}
}
