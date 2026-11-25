package schema

import (
	"strings"
	"testing"

	"github.com/openfga/language/pkg/go/transformer"
)

func loadDSL(t *testing.T, dsl string) *Model {
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

func TestDiff(t *testing.T) {
	old := loadDSL(t, "model\n  schema 1.1\ntype user\ntype group\n  relations\n    define member: [user]\ntype doc\n  relations\n    define editor: [user]\n    define viewer: [user] or editor\n")
	new := loadDSL(t, "model\n  schema 1.1\ntype user\ntype group\n  relations\n    define member: [user]\ntype doc\n  relations\n    define editor: [user, group#member]\n    define viewer: [user] or editor\n    define owner: [user]\n")
	d := Diff(old, new)
	var rels []string
	for _, r := range d.Relations {
		rels = append(rels, r.Type+"#"+r.Relation+" "+r.Change)
	}
	if strings.Join(rels, ",") != "doc#editor changed,doc#owner added" {
		t.Errorf("relations = %v", rels)
	}
	// viewer's rule is unchanged, but it gains group#member through editor.
	var access []string
	for _, a := range d.Access {
		access = append(access, a.Type+"#"+a.Relation+" +"+strings.Join(a.Gained, ",")+" direct="+map[bool]string{true: "y", false: "n"}[a.Direct])
	}
	if strings.Join(access, ";") != "doc#editor +group#member direct=y;doc#viewer +group#member direct=n" {
		t.Errorf("access = %v", access)
	}
	if !Diff(old, old).Empty() {
		t.Error("a model compared with itself should have no differences")
	}
}

// Removing one of several routes to a relation shows as a lost route even
// when the kinds of user that can hold it stay the same.
func TestDiffRoutes(t *testing.T) {
	old := loadDSL(t, "model\n  schema 1.1\ntype user\ntype folder\n  relations\n    define owner: [user]\n    define viewer: [user] or owner\ntype doc\n  relations\n    define parent: [folder]\n    define blocked: [user]\n    define editor: [user]\n    define viewer: [user] or editor or viewer from parent\n")
	new := loadDSL(t, "model\n  schema 1.1\ntype user\ntype folder\n  relations\n    define owner: [user]\n    define viewer: [user]\ntype doc\n  relations\n    define parent: [folder]\n    define blocked: [user]\n    define editor: [user]\n    define viewer: ([user] or viewer from parent) but not blocked\n")
	d := Diff(old, new)
	if len(d.Access) != 0 {
		t.Errorf("kinds of user are unchanged, got %+v", d.Access)
	}
	got := map[string]RouteChange{}
	for _, r := range d.Routes {
		got[r.Type+"#"+r.Relation] = r
	}
	v := got["doc#viewer"]
	// Including indirectly: doc#viewer is granted through folder#viewer, which lost owner.
	if strings.Join(v.Lost, "; ") != "direct user on doc#editor; direct user on folder#owner; doc#editor; folder#owner" || !v.Direct {
		t.Errorf("doc#viewer lost %q (direct %v)", v.Lost, v.Direct)
	}
	if strings.Join(v.ExclusionsAdded, ",") != "doc#blocked" {
		t.Errorf("doc#viewer exclusions added %v", v.ExclusionsAdded)
	}
	// folder#viewer's own rule changed; doc#viewer also lost folder#owner through it.
	if f := got["folder#viewer"]; strings.Join(f.Lost, "; ") != "direct user on folder#owner; folder#owner" {
		t.Errorf("folder#viewer lost %q", f.Lost)
	}
}
