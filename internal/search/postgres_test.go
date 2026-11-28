package search

import (
	"slices"

	"os"
	"strings"
	"testing"

	"github.com/sykesm/openfga-explorer/internal/datastore"
)

// TestPostgresMatchesBruteForce runs against the local stack (deploy/) and
// compares the prefix walk with an exact byte-wise scan, including under a
// tiny scan budget that forces incomplete pages and cursor continuation.
//
//	FGA_TEST_DATASTORE_URI=postgres://openfga:openfga@localhost:5433/openfga FGA_TEST_STORE=<id> go test ./internal/search
func TestPostgresMatchesBruteForce(t *testing.T) {
	uri, store := os.Getenv("FGA_TEST_DATASTORE_URI"), os.Getenv("FGA_TEST_STORE")
	if uri == "" || store == "" {
		t.Skip("FGA_TEST_DATASTORE_URI and FGA_TEST_STORE not set")
	}
	ctx := t.Context()
	db, err := datastore.Open(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := NewPostgres(db)
	t.Logf("searcher: %s (byte order: %v)", p.Name(), db.ByteOrder)

	brute := func(side, typ, prefix string) []string {
		var sql string
		var args []any
		if side == "object" {
			sql = `SELECT DISTINCT object_id FROM tuple WHERE store = $1 AND object_type = $2 AND starts_with(object_id, $3)`
			args = []any{store, typ, prefix}
		} else {
			sql = `SELECT DISTINCT split_part(substr(_user, length($2) + 2), '#', 1) FROM tuple WHERE store = $1 AND starts_with(_user, $2 || ':' || $3) AND _user <> $2 || ':*'`
			args = []any{store, typ, prefix}
		}
		ids, err := p.collect(ctx, sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(ids)
		return ids
	}

	cases := []struct{ side, typ, prefix string }{
		{"user", "principal", "CN%3DA"},
		{"user", "principal", "CN%3D"},
		{"user", "principal", "CN%3DZ"},
		{"user", "principal", "CN"},
		{"user", "principal", "CN3DA00"},
		{"user", "principal", "CN-"},
		{"user", "principal", "CN."},
		{"user", "principal", "cn%3d"},
		{"user", "principal", "SVC%3A"},
		{"user", "principal", "ZZZ"},
		{"user", "group", "team-1"},
		{"user", "user", ""}, // includes the user:* wildcard tuple, which must not be listed
		{"object", "document", "doc-00"},
		{"object", "document", "doc-0400"},
		{"object", "document", "r"},
		{"object", "group", "team-0"},
		{"object", "group", ""},
	}
	for _, budget := range []int{5000, 150} {
		p.MaxScan, p.Batch = budget, min(budget, 500)
		for _, c := range cases {
			want := brute(c.side, c.typ, c.prefix)
			var got []string
			cursorStr, pages, incomplete := "", 0, 0
			for {
				pg, err := p.Search(ctx, Query{Store: store, Type: c.typ, Prefix: c.prefix, Limit: 10000, Cursor: cursorStr, Sides: []string{c.side}})
				if err != nil {
					t.Fatal(err)
				}
				pages++
				for _, r := range pg.Results {
					got = append(got, r.ID)
				}
				if pg.Complete {
					break
				}
				incomplete++
				if pg.Note == "" {
					t.Errorf("%v: incomplete page without a note", c)
				}
				cursorStr = pg.Cursor
				if pages > 200 {
					t.Fatalf("%v: did not converge", c)
				}
			}
			slices.Sort(got)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("budget %d %v: got %d ids, want %d\n missing: %v", budget, c, len(got), len(want), diff(want, got))
			}
			if incomplete > 0 {
				t.Logf("budget %d %v: %d ids over %d pages (%d flagged incomplete)", budget, c, len(got), pages, incomplete)
			}
		}
	}
}

func diff(want, got []string) []string {
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	var out []string
	for _, w := range want {
		if !have[w] {
			out = append(out, w)
		}
		if len(out) > 10 {
			break
		}
	}
	return out
}
