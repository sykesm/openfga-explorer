package tuples

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sykesm/openfga-explorer/internal/datastore"
	"github.com/sykesm/openfga-explorer/internal/fga"
)

func TestMinimalPrefixes(t *testing.T) {
	got := minimalPrefixes([]string{"CN%3DA", "user:plat-01", "CN%3D", "CN-", "CN%3DAL"})
	if strings.Join(got, ",") != "CN%3D,CN-,user:plat-01" {
		t.Errorf("minimalPrefixes = %v", got)
	}
}

func TestUpperAndCommas(t *testing.T) {
	if u, ok := upper("ab"); !ok || u != "ac" {
		t.Errorf("upper(ab) = %q %v", u, ok)
	}
	if u, ok := upper("a\xff"); !ok || u != "b" {
		t.Errorf("upper(a\\xff) = %q %v", u, ok)
	}
	if _, ok := upper(""); ok {
		t.Error("upper(\"\") should have no bound")
	}
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 2533: "2,533", 1234567: "1,234,567"} {
		if got := commas(n); got != want {
			t.Errorf("commas(%d) = %q", n, got)
		}
	}
}

// drain follows cursors until the query is exhausted.
func drain(t *testing.T, s Source, q Query) []string {
	t.Helper()
	var out []string
	for i := 0; ; i++ {
		p, err := s.Edges(t.Context(), q)
		if err != nil {
			t.Fatalf("%s %+v: %v", s.Name(), q, err)
		}
		for _, k := range p.Tuples {
			out = append(out, Peer(q, k)+"|"+k.GetCondition().GetName())
		}
		if !p.More {
			break
		}
		q.Cursor = p.Cursor
		if i > 1000 {
			t.Fatal("did not converge")
		}
	}
	slices.Sort(out)
	return out
}

// TestDatastoreMatchesAPI compares both sources against the sample store
// (deploy/seed), including prefix filters and cursor paging.
func TestDatastoreMatchesAPI(t *testing.T) {
	apiURL, store, uri := os.Getenv("FGA_TEST_API_URL"), os.Getenv("FGA_TEST_STORE"), os.Getenv("FGA_TEST_DATASTORE_URI")
	if apiURL == "" || store == "" || uri == "" {
		t.Skip("FGA_TEST_API_URL, FGA_TEST_STORE, and FGA_TEST_DATASTORE_URI not set")
	}
	ctx := t.Context()
	c, _ := fga.New(fga.Options{APIURL: apiURL})
	db, err := datastore.Open(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	api, ds := NewAPI(c), NewDatastore(db)
	api.ScanLimit = 1 << 30

	queries := []Query{
		{Dir: Out, Object: "group:platform", Relation: "member"},   // 2,500+ users and principals
		{Dir: Out, Object: "document:roadmap", Relation: "viewer"}, // 150 usersets
		{Dir: Out, Object: "document:offsite-plan", Relation: "viewer"},
		{Dir: Out, Object: "document:nope", Relation: "viewer"},
		{Dir: In, User: "user:anne", Relation: "member", ObjectType: "group"},
		{Dir: In, User: "organization:acme", Relation: "org", ObjectType: "document"}, // 400+
		{Dir: In, User: "group:sales#member", Relation: "member", ObjectType: "group"},
	}
	for _, q := range queries {
		q.Store, q.Limit = store, 37 // odd page size exercises cursors
		a, d := drain(t, api, q), drain(t, ds, q)
		if strings.Join(a, "\n") != strings.Join(d, "\n") {
			t.Errorf("%s %s#%s %s: api %d edges, datastore %d", q.Dir, q.Object+q.User, q.Relation, q.ObjectType, len(a), len(d))
		}
		cnt, err := ds.Count(ctx, q, 100000)
		if err != nil || !cnt.Exact || cnt.N != len(a) {
			t.Errorf("count %+v = %v, %v; want %d", q, cnt, err, len(a))
		}
	}

	// Bulk panel queries must agree with per-relation counts and edges.
	for _, obj := range []string{"group:platform", "document:roadmap", "document:offsite-plan", "folder:root", "organization:acme"} {
		rels := []string{"member", "manager", "viewer", "editor", "owner", "parent", "org", "restricted", "admin", "blocked"}
		counts, err := ds.OutCounts(ctx, store, obj, rels, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var small []string
		for _, rel := range rels {
			want, err := ds.Count(ctx, Query{Store: store, Dir: Out, Object: obj, Relation: rel}, 1000)
			if err != nil || counts[rel] != want {
				t.Errorf("OutCounts %s#%s = %v, Count = %v (%v)", obj, rel, counts[rel], want, err)
			}
			if want.Exact && want.N > 0 && want.N <= 8 {
				small = append(small, rel)
			}
		}
		edges, err := ds.OutEdges(ctx, store, obj, small)
		if err != nil {
			t.Fatal(err)
		}
		for _, rel := range small {
			want := drain(t, api, Query{Store: store, Dir: Out, Object: obj, Relation: rel})
			var got []string
			for _, k := range edges[rel] {
				got = append(got, k.GetUser()+"|"+k.GetCondition().GetName())
			}
			slices.Sort(got)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("OutEdges %s#%s = %v, want %v", obj, rel, got, want)
			}
		}
	}

	// Prefix filters must equal "all edges whose peer starts with the prefix".
	prefixed := []struct {
		q        Query
		prefixes []string
	}{
		{Query{Dir: Out, Object: "group:platform", Relation: "member"}, []string{"principal:CN%3DA", "principal:CN%3D", "user:plat-01", "principal:cn%3d", "principal:CN-"}},
		{Query{Dir: Out, Object: "group:platform", Relation: "member"}, []string{"principal:"}},
		{Query{Dir: Out, Object: "document:roadmap", Relation: "viewer"}, []string{"group:team-14"}},
		{Query{Dir: In, User: "organization:acme", Relation: "org", ObjectType: "document"}, []string{"doc-03", "r"}},
	}
	for _, c := range prefixed {
		q := c.q
		q.Store = store
		all := drain(t, api, q)
		var want []string
		for _, e := range all {
			peer := e
			if q.Dir == In { // In prefixes apply to the object ID, without "type:"
				peer = strings.TrimPrefix(e, q.ObjectType+":")
			}
			for _, p := range c.prefixes {
				if strings.HasPrefix(peer, p) {
					want = append(want, e)
					break
				}
			}
		}
		for _, limit := range []int{5, 1000} {
			q.Prefixes, q.Limit = c.prefixes, limit
			got := drain(t, ds, q)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("prefixes %v (limit %d): got %d, want %d", c.prefixes, limit, len(got), len(want))
			}
		}
	}
}
