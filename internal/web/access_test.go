package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sykesm/openfga-explorer/internal/config"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
)

func TestLiveGuard(t *testing.T) {
	g := newLiveGuard()
	now := time.Now()
	done, err := g.start("anne", 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.start("anne", 2, now); err == nil || !strings.Contains(err.Error(), "already have") {
		t.Errorf("concurrent run: %v", err)
	}
	if d, err := g.start("bob", 2, now); err != nil {
		t.Errorf("another person: %v", err)
	} else {
		d()
	}
	done()
	done, err = g.start("anne", 2, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	done()
	if _, err := g.start("anne", 2, now.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("third run in an hour: %v", err)
	}
	if d, err := g.start("anne", 2, now.Add(61*time.Minute)); err != nil {
		t.Errorf("after an hour: %v", err)
	} else {
		d()
	}
	b := &budget{max: 2}
	if b.spend(1) != nil || b.spend(1) != nil || b.spend(1) == nil {
		t.Error("budget of 2 should allow two calls")
	}
}

func TestIdentityAndAdminFlag(t *testing.T) {
	st := fgatest.Seed(t)
	live := func(c *config.Config) { c.LiveQueries.Enabled = true }
	withHeader := func(c *config.Config) { c.Auth.IdentityHeader = "X-Email" }
	flag := func(c *config.Config) { c.Auth.DangerouslyTreatAnonymousAsAdmin = true }

	// Without the flag, nobody anonymous is an admin.
	x := explorerFor(t, st, live)
	_, body := x.get("~/explore/instance?object=user:anne", http.StatusOK)
	if strings.Contains(body, `data-tab="access"`) {
		t.Error("access tab shown to a non-admin")
	}
	_, body = x.get("~/explore/access?object=user:anne&type=document&rel=viewer&run=1", http.StatusOK)
	mustContain(t, "refused", body, "for admins")

	// With the flag, anonymous requests are admins…
	x = explorerFor(t, st, live, withHeader, flag)
	_, body = x.get("~/explore/instance?object=user:anne", http.StatusOK)
	mustContain(t, "admin tab", body, `data-tab="access"`)
	// …but a named identity is not upgraded by the flag.
	req, _ := http.NewRequest("GET", x.url("~/explore/instance?object=user:anne"), nil)
	req.Header.Set("X-Email", "someone@example.com")
	_, body = x.do(req, http.StatusOK)
	if strings.Contains(body, `data-tab="access"`) {
		t.Error("the flag upgraded a named identity")
	}

	// default_role: admin from the config file applies to named identities
	// only; it never makes anonymous requests admins.
	adminDefault := func(c *config.Config) { c.Auth.DefaultRole = "admin" }
	x = explorerFor(t, st, live, adminDefault)
	_, body = x.get("~/explore/instance?object=user:anne", http.StatusOK)
	if strings.Contains(body, `data-tab="access"`) {
		t.Error("default_role: admin made an anonymous request an admin")
	}
	x = explorerFor(t, st, live, withHeader, adminDefault)
	req, _ = http.NewRequest("GET", x.url("~/explore/instance?object=user:anne"), nil)
	req.Header.Set("X-Email", "someone@example.com")
	_, body = x.do(req, http.StatusOK)
	mustContain(t, "named identity gets default_role", body, `data-tab="access"`)
	_, body = x.get("~/explore/instance?object=user:anne", http.StatusOK)
	if strings.Contains(body, `data-tab="access"`) {
		t.Error("default_role: admin made a request without the identity header an admin")
	}

	// Live queries must also be enabled.
	x = explorerFor(t, st, flag)
	_, body = x.get("~/explore/access?object=user:anne", http.StatusOK)
	mustContain(t, "disabled", body, "must be enabled")
}

func TestAccessRun(t *testing.T) {
	st := fgatest.Seed(t)
	x := explorerFor(t, st, func(c *config.Config) {
		c.LiveQueries.Enabled, c.LiveQueries.RunsPerHour = true, 2
		c.Auth.DangerouslyTreatAnonymousAsAdmin = true
	})
	// A group can be a user only as its members here (no relation accepts a
	// bare group), so that is the one subject; reachable relations are offered.
	_, body := x.get("~/explore/access?object=group:eng", http.StatusOK)
	mustContain(t, "form", body, `name="subject" value="group:eng#member"`, `name="rel" value="viewer"`)

	ctx := `{"current_time":"2026-11-24T12:00:00Z","user_ip":"10.1.2.3"}`
	_, body = x.get("~/explore/access?object=user:eng-002&subject=user:eng-002&type=document&rel=can_view&rel=owner&run=1&context="+urlq(ctx), http.StatusOK)
	mustContain(t, "results", body, "2 calls", "<code>can_view</code>", "document:doc-0007", "<code>owner</code>")
	mustContain(t, "explain link", body, "relation=can_view&amp;user=user%3Aeng-002")

	// The hourly limit (2 here) applies per person.
	x.get("~/explore/access?object=user:anne&type=document&rel=viewer&run=1", http.StatusOK)
	_, body = x.get("~/explore/access?object=user:anne&type=document&rel=viewer&run=1", http.StatusOK)
	mustContain(t, "rate limit", body, "in the last hour (the limit)")
}

func urlq(s string) string { return url.QueryEscape(s) }
