package tuples

import (
	"strings"
	"testing"

	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
)

// TestAPISource runs the API source against the in-process sample store.
func TestAPISource(t *testing.T) {
	st := fgatest.Seed(t)
	c, err := fga.New(fga.Options{APIURL: st.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	api := NewAPI(c)
	q := Query{Store: st.ID, Dir: Out, Object: "group:platform", Relation: "member", Limit: 37}

	// Unfiltered paging visits every edge once.
	all := drain(t, api, q)
	if len(all) < 2500 {
		t.Fatalf("group:platform#member has %d edges", len(all))
	}
	n, err := api.Count(ctx, q, 1000)
	if err != nil || n.Exact || n.N != 100 {
		t.Errorf("count beyond one page must be inexact: %+v %v", n, err)
	}

	// The contains filter is case-insensitive and keeps reading until it has
	// a page of matches.
	q.Contains = "PLAT-00"
	page, err := api.Edges(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tuples) == 0 {
		t.Fatal("no matches for plat-00")
	}
	for _, k := range page.Tuples {
		if !strings.Contains(strings.ToLower(k.GetUser()), "plat-00") {
			t.Errorf("filter let through %s", k.GetUser())
		}
	}

	// A small scan limit stops early and reports more.
	api.ScanLimit = 100
	q.Contains = "no-such-user"
	page, err = api.Edges(ctx, q)
	if err != nil || len(page.Tuples) != 0 || !page.More || page.Scanned != 100 || page.Cursor == "" {
		t.Errorf("bounded scan: %+v %v", page, err)
	}

	// Incoming edges: objects of a type that a user points at.
	in := drain(t, api, Query{Store: st.ID, Dir: In, User: "organization:acme", Relation: "org", ObjectType: "document", Limit: 50})
	if len(in) < 400 {
		t.Errorf("organization:acme is the org of %d documents", len(in))
	}
	small := Query{Store: st.ID, Dir: Out, Object: "document:offsite-plan", Relation: "viewer"}
	if n, err := api.Count(ctx, small, 100); err != nil || !n.Exact {
		t.Errorf("small count: %+v %v", n, err)
	}
	if api.Name() != "api" || api.FilterMode() != "contains" || api.Label() == "" {
		t.Error("API source identity")
	}
}
