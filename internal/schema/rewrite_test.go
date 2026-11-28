package schema

import (
	"slices"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
)

func TestLeaves(t *testing.T) {
	pm, err := transformer.TransformDSLToProto(`model
  schema 1.1
type user
type doc
  relations
    define parent: [doc]
    define owner: [user]
    define blocked: [user]
    define editor: [user]
    define viewer: (([user] or owner or viewer from parent) and editor) but not (blocked or owner from parent)
`)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Load(pm)
	if err != nil {
		t.Fatal(err)
	}
	name := func(u *openfgav1.Userset) string {
		switch v := u.GetUserset().(type) {
		case *openfgav1.Userset_This:
			return "this"
		case *openfgav1.Userset_ComputedUserset:
			return v.ComputedUserset.GetRelation()
		case *openfgav1.Userset_TupleToUserset:
			return v.TupleToUserset.GetComputedUserset().GetRelation() + " from " + v.TupleToUserset.GetTupleset().GetRelation()
		}
		return "?"
	}
	r, _ := m.Relation("doc", "viewer")
	var got []string
	for u, sub := range Leaves(r.Rewrite) {
		s := name(u)
		if sub {
			s = "-" + s
		}
		got = append(got, s)
	}
	if want := "this owner viewer from parent editor -blocked -owner from parent"; strings.Join(got, " ") != want {
		t.Errorf("Leaves = %q, want %q", strings.Join(got, " "), want)
	}
	// Stopping early stops the walk.
	var first []string
	for u := range Leaves(r.Rewrite) {
		if first = append(first, name(u)); len(first) == 2 {
			break
		}
	}
	if !slices.Equal(first, []string{"this", "owner"}) {
		t.Errorf("early stop: %q", first)
	}
}

// SubjectTypes is cached per relation: the same answer every time, a copy
// the caller may change, and safe to ask from many requests at once.
func TestSubjectTypesCached(t *testing.T) {
	m := load(t)
	r, _ := m.Relation("folder", "viewer")
	want := m.SubjectTypes(r)
	if len(want) == 0 {
		t.Fatal("folder#viewer has no subject types")
	}
	got := m.SubjectTypes(r)
	got[0] = "changed"
	if again := m.SubjectTypes(r); !slices.Equal(again, want) {
		t.Errorf("changing a returned list changed the cache: %q, want %q", again, want)
	}
	done := make(chan []string)
	for range 8 {
		go func() { done <- m.SubjectTypes(r) }()
	}
	for range 8 {
		if l := <-done; !slices.Equal(l, want) {
			t.Errorf("concurrent SubjectTypes = %q, want %q", l, want)
		}
	}
}
