package trace_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
)

// TestNestedGroupTree: proving membership through a large tree of nested
// groups must not walk the whole tree. Guided evaluation asks the server
// which child groups contain the user and walks only those; before, the
// walker spent its whole call budget and answered "undecided".
func TestNestedGroupTree(t *testing.T) {
	srv := fgatest.Start(t)
	ctx := context.Background()
	key := func(u, r, o string) *openfgav1.TupleKey { return &openfgav1.TupleKey{User: u, Relation: r, Object: o} }
	// A tree of 255 groups, 8 levels deep: each group's members include two
	// child groups' members. One user is a member of a single leaf group.
	var keys []*openfgav1.TupleKey
	const groups = 255
	for g := 1; g < groups; g++ {
		keys = append(keys, key(fmt.Sprintf("group:g%03d#member", g), "member", fmt.Sprintf("group:g%03d", (g-1)/2)))
	}
	for g := groups / 2; g < groups; g++ {
		keys = append(keys, key(fmt.Sprintf("user:u%03d", g), "member", fmt.Sprintf("group:g%03d", g)))
	}
	keys = append(keys, key("group:g000#member", "viewer", "doc:1"))
	st, err := srv.CreateStore(ctx, "group-tree", `model
  schema 1.1
type user
type group
  relations
    define member: [user, group#member]
type doc
  relations
    define viewer: [user, group#member]
`, keys)
	if err != nil {
		t.Fatal(err)
	}
	c, err := fga.New(fga.Options{APIURL: st.URL})
	if err != nil {
		t.Fatal(err)
	}
	pm, err := c.Model(ctx, st.ID, st.Model)
	if err != nil {
		t.Fatal(err)
	}
	m, err := schema.Load(pm)
	if err != nil {
		t.Fatal(err)
	}
	opt := trace.DefaultOptions()
	opt.MaxCalls = 60 // far fewer than the tree has groups
	for user, want := range map[string]trace.Outcome{
		"user:u200":   trace.Allowed, // a leaf member, 7 levels down
		"user:nobody": trace.Denied,
	} {
		tr, err := trace.Explain(ctx, c, m, nil, trace.Request{Store: st.ID, Model: st.Model, User: user, Relation: "viewer", Object: "doc:1"}, opt)
		if err != nil {
			t.Fatal(err)
		}
		if got := tr.Root.Outcome; got != want {
			t.Errorf("%s: walker %v, want %v (%d calls, budget %d)", user, got, want, tr.Calls, opt.MaxCalls)
		}
		// Allowed: the walk follows the one child group (per level) that the
		// server says contains the user, so it stays well within budget.
		// Denied: the server's Check rules out the root's child groups at once.
		if want == trace.Allowed && tr.Calls > 30 {
			t.Errorf("%s: %d calls; the walk should follow one group per level", user, tr.Calls)
		}
		if want == trace.Denied {
			if tr.GroupsRuledOut == 0 {
				t.Errorf("%s: no group was ruled out by the server's Check", user)
			}
			// The explanation says the groups were taken on the server's word
			// (in the headline, or in the step-by-step facts when deeper).
			e := tr.Explanation(nil)
			all := e.Headline
			for _, f := range e.Facts {
				all += "\n" + f.Text
			}
			if !strings.Contains(all, "per the server's Check; those groups were not walked") {
				t.Errorf("%s: explanation hides the ruled-out groups:\n%s", user, all)
			}
		}
	}
}
