package scenario

import (
	"context"
	"strings"
	"testing"
)

func capturedTest() Test {
	return Capture{
		User: "user:anne", Relation: "viewer", Object: "document:roadmap", Allowed: true,
		Tuples: []Tuple{
			{User: "user:anne", Relation: "member", Object: "group:eng"},
			{User: "group:eng#member", Relation: "viewer", Object: "folder:eng"},
			{User: "folder:eng", Relation: "parent", Object: "document:roadmap"},
			{User: "user:*", Relation: "viewer", Object: "folder:public"},
		},
	}.Test("captured")
}

func TestMask(t *testing.T) {
	tc := capturedTest()
	Mask(&tc)
	var got []string
	for _, tp := range tc.Tuples {
		got = append(got, tp.String())
	}
	want := "user:user-1 member group:group-1; group:group-1#member viewer folder:folder-1; folder:folder-1 parent document:document-1; user:* viewer folder:folder-2"
	if strings.Join(got, "; ") != want {
		t.Errorf("masked tuples:\n%s\nwant:\n%s", strings.Join(got, "; "), want)
	}
	if c := tc.Check[0]; c.User != "user:user-1" || c.Object != "document:document-1" || tc.Name != "user:user-1 viewer document:document-1" {
		t.Errorf("masked check %+v, name %q", c, tc.Name)
	}
	// The masked test still passes: renaming is consistent.
	d := &Document{Tests: []Test{tc}}
	r, err := Run(context.Background(), d, seedModel(t))
	if err != nil || r.Passed != 1 {
		t.Errorf("masked test: %+v %v", r, err)
	}
}

func TestAppendTest(t *testing.T) {
	// An empty draft becomes a document.
	out, err := AppendTest(nil, capturedTest(), "Captured")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(out)
	if err != nil || d.Name != "Captured" || len(d.Tests) != 1 {
		t.Fatalf("new document: %v\n%s", err, out)
	}

	// An existing draft keeps its content and comments; a taken name gets a suffix.
	draft := []byte("# my notes\nname: Mine\ntests:\n  # first test\n  - name: user:anne viewer document:roadmap\n    check:\n      - user: user:x\n        object: document:y\n        assertions:\n          viewer: false\n")
	out, err = AppendTest(draft, capturedTest(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"# my notes", "# first test", "name: Mine", "user:anne viewer document:roadmap (2)", "object: folder:eng"} {
		if !strings.Contains(s, want) {
			t.Errorf("merged draft missing %q:\n%s", want, s)
		}
	}
	if d, err := Parse(out); err != nil || len(d.Tests) != 2 {
		t.Errorf("merged draft: %v", err)
	}

	// A draft without tests, or with an empty list, gets one.
	for _, draft := range []string{"name: x\n", "name: x\ntests: []\n"} {
		out, err := AppendTest([]byte(draft), capturedTest(), "")
		if d, perr := Parse(out); err != nil || perr != nil || len(d.Tests) != 1 {
			t.Errorf("%q: %v %v\n%s", draft, err, perr, out)
		}
	}
	// Broken drafts are reported, not overwritten.
	if _, err := AppendTest([]byte("tests: [unclosed\n"), capturedTest(), ""); err == nil || !strings.Contains(err.Error(), "isn't valid YAML") {
		t.Errorf("broken draft: %v", err)
	}
	if _, err := AppendTest([]byte("- just\n- a list\n"), capturedTest(), ""); err == nil {
		t.Error("a list is not a scenario document")
	}
}
