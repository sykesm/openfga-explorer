package schema

import (
	"testing"

	"github.com/openfga/language/pkg/go/transformer"
)

const dsl = `model
  schema 1.1
type user
type group
  relations
    define member: [user, group#member]
type folder
  relations
    define parent: [folder]
    define owner: [user]
    define viewer: [user, user:*, group#member] or owner or viewer from parent
type doc
  relations
    define parent: [folder]
    define blocked: [user]
    define can_view: viewer from parent but not blocked
`

func load(t *testing.T) *Model {
	t.Helper()
	pm, err := transformer.TransformDSLToProto(dsl)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Load(pm)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestKinds(t *testing.T) {
	m := load(t)
	for _, c := range []struct {
		typ, rel string
		want     Kind
	}{
		{"folder", "owner", Direct}, {"folder", "viewer", Hybrid}, {"doc", "can_view", Derived}, {"group", "member", Direct},
	} {
		r, ok := m.Relation(c.typ, c.rel)
		if !ok || r.Kind != c.want {
			t.Errorf("%s#%s kind = %v, want %v", c.typ, c.rel, r.Kind, c.want)
		}
	}
	r, _ := m.Relation("folder", "viewer")
	if r.Expr != "[user, user:*, group#member] or owner or viewer from parent" {
		t.Errorf("Expr = %q", r.Expr)
	}
	if r.DerivedExpr != "owner or viewer from parent" {
		t.Errorf("DerivedExpr = %q", r.DerivedExpr)
	}
	if got := m.Types["folder"].RelationNames; got[0] != "owner" || got[len(got)-1] != "viewer" {
		t.Errorf("relation order = %v (want direct first, hybrid last)", got)
	}
}

func TestDependenciesAndIncoming(t *testing.T) {
	m := load(t)
	r, _ := m.Relation("doc", "can_view")
	deps := m.Dependencies(r)
	if len(deps) != 2 || deps[0].Via != "parent" || deps[0].TargetTypes[0] != "folder" || deps[1].Relation != "blocked" {
		t.Errorf("deps = %+v", deps)
	}
	in := m.IncomingFor("group")
	if len(in) != 2 {
		t.Fatalf("incoming for group = %+v", in)
	}
	for _, i := range in {
		if i.UsersetRelation != "member" {
			t.Errorf("unexpected incoming %+v", i)
		}
	}
}

func TestParseSubject(t *testing.T) {
	for in, want := range map[string]Subject{
		"user:anne":        {Type: "user", ID: "anne"},
		"group:eng#member": {Type: "group", ID: "eng", Relation: "member"},
		"user:*":           {Type: "user", ID: "*"},
		"principal:CN%23X": {Type: "principal", ID: "CN%23X"},
	} {
		got, err := ParseSubject(in)
		if err != nil || got != want {
			t.Errorf("ParseSubject(%q) = %+v, %v", in, got, err)
		}
	}
	if _, err := ParseSubject("nocolon"); err == nil {
		t.Error("expected error")
	}
}

func TestExplainAndSubjectTypes(t *testing.T) {
	m := load(t)
	r, _ := m.Relation("doc", "can_view")
	c := m.Explain(r)
	if c.Kind != "except" || c.Children[0].Kind != "via" || c.Children[0].Via != "parent" || c.Children[0].Relation != "viewer" ||
		c.Children[1].Kind != "relation" || c.Children[1].Relation != "blocked" {
		t.Fatalf("explain = %+v", c)
	}
	// can_view reaches folder#viewer: [user, user:*, group#member] and group#member: [user, group#member].
	// The excluded side (blocked) must not add holders.
	got := m.SubjectTypes(r)
	want := []string{"user", "group#member"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("SubjectTypes = %v, want %v", got, want)
	}
}

func TestReachable(t *testing.T) {
	m := load(t)
	r := m.Reachable("doc", "user")
	if !r["can_view"] || !r["blocked"] || r["parent"] {
		t.Errorf("user reach on doc = %v", r)
	}
	r = m.Reachable("doc", "group#member")
	if !r["can_view"] || r["blocked"] {
		t.Errorf("group#member reach on doc = %v (blocked only accepts user)", r)
	}
	r = m.Reachable("group", "group#member")
	if !r["member"] {
		t.Errorf("a userset holds its own relation: %v", r)
	}
	if got := m.UserTypes(); len(got) != 3 || got[0] != "folder" || got[1] != "user" || got[2] != "group#member" {
		t.Errorf("UserTypes = %v", got)
	}
}

func TestHubTypes(t *testing.T) {
	m := load(t)
	// folder and user each link to 3 other types (ties keep TypeNames order);
	// group and doc to 2.
	if got := m.HubTypes(2); len(got) != 2 || got[0] != "folder" || got[1] != "user" {
		t.Errorf("HubTypes(2) = %v", got)
	}
	if got := m.HubTypes(10); len(got) != 4 {
		t.Errorf("HubTypes(10) = %v", got)
	}
}
