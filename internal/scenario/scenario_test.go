package scenario

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
	"gopkg.in/yaml.v3"
)

func seedModel(t *testing.T) *openfgav1.AuthorizationModel {
	t.Helper()
	b, err := os.ReadFile("../../deploy/seed/model.fga")
	if err != nil {
		t.Fatal(err)
	}
	pm, err := transformer.TransformDSLToProto(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return viaJSON(pm)
}

func TestSampleScenariosPass(t *testing.T) {
	b, err := os.ReadFile("../../deploy/seed/scenarios.fga.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(DefaultLimits); err != nil {
		t.Fatal(err)
	}
	r, err := Run(context.Background(), d, seedModel(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.Failed != 0 || r.Passed != 12 || r.ModelFrom != "the viewed model" {
		for _, tr := range r.Tests {
			t.Logf("%s: %s %+v %+v %+v", tr.Name, tr.Err, tr.Checks, tr.ListObjects, tr.ListUsers)
		}
		t.Fatalf("passed %d failed %d (model from %s)", r.Passed, r.Failed, r.ModelFrom)
	}
	// Granting tuples: anne's viewer comes through group membership and the folder.
	first := r.Tests[0]
	c := first.Checks[2] // anne viewer roadmap (assertions run in sorted order: can_edit, can_view, viewer)
	if c.Relation != "viewer" || !c.Got {
		t.Fatalf("check = %+v", c)
	}
	var granted []string
	for _, i := range c.GrantedBy {
		granted = append(granted, first.Tuples[i].String())
	}
	if strings.Join(granted, "; ") != "user:anne member group:eng; folder:eng parent document:roadmap; group:eng#member viewer folder:eng" {
		t.Errorf("granted by %v", granted)
	}
	if !strings.HasPrefix(c.Because, "Allowed: user:anne has viewer on document:roadmap") {
		t.Errorf("because = %q", c.Because)
	}
	if len(first.Tuples[0].UsedBy) == 0 || !first.Tuples[0].Shared {
		t.Errorf("anne's membership should be shared and used: %+v", first.Tuples[0])
	}
	// The blocked test's explanation names the exclusion.
	blocked := r.Tests[1].Checks[0]
	if blocked.Relation != "can_view" || blocked.Got || !strings.Contains(blocked.Because, "blocked") {
		t.Errorf("blocked check = %+v", blocked)
	}
}

// Failures report expected vs got, and list mismatches name the difference.
func TestFailuresAreExplained(t *testing.T) {
	d, err := Parse([]byte(`
tests:
  - name: wrong expectations
    tuples:
      - {user: "user:anne", relation: owner, object: "document:x"}
    check:
      - user: user:anne
        object: document:x
        assertions: {can_edit: true}
    list_objects:
      - user: user:anne
        type: document
        assertions: {owner: ["document:y"]}
`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Run(context.Background(), d, seedModel(t))
	if err != nil {
		t.Fatal(err)
	}
	tr := r.Tests[0]
	if r.Failed != 2 || tr.Checks[0].Pass || tr.Checks[0].Got {
		t.Fatalf("report = %+v", tr)
	}
	if !strings.Contains(tr.Checks[0].Because, "member from org") {
		t.Errorf("because = %q", tr.Checks[0].Because)
	}
	lo := tr.ListObjects[0]
	if strings.Join(lo.Missing, ",") != "document:y" || strings.Join(lo.Extra, ",") != "document:x" {
		t.Errorf("list objects = %+v", lo)
	}
}

// A JSON document is the same document; the model can come with it.
func TestJSONAndInlineModel(t *testing.T) {
	doc := map[string]any{
		"name":  "inline",
		"model": "model\n  schema 1.1\ntype user\ntype doc\n  relations\n    define viewer: [user]\n",
		"tests": []any{map[string]any{
			"name":   "direct",
			"tuples": []any{map[string]any{"user": "user:a", "relation": "viewer", "object": "doc:1"}},
			"check":  []any{map[string]any{"user": "user:a", "object": "doc:1", "assertions": map[string]any{"viewer": true}}},
		}},
	}
	b, _ := json.Marshal(doc)
	d, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Run(context.Background(), d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Passed != 1 || r.ModelFrom != "the document" {
		t.Errorf("report = %+v", r)
	}
	// And YAML round-trips the same structure (what an export produces).
	y, err := yaml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if d2, err := Parse(y); err != nil || d2.Tests[0].Check[0].Assertions["viewer"] != true {
		t.Errorf("YAML round trip: %v", err)
	}
}

func TestValidation(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"empty":          {"", "empty"},
		"unknown field":  {"tests: []\nmodle: x\n", "field modle not found"},
		"file refs":      {"model_file: m.fga\ntests: [{name: t, check: [{user: 'user:a', object: 'doc:1', assertions: {viewer: true}}]}]\n", "file references"},
		"no tests":       {"name: x\n", "no tests"},
		"user and users": {"tests: [{name: t, check: [{user: 'user:a', users: ['user:b'], object: 'doc:1', assertions: {viewer: true}}]}]\n", "exactly one of user or users"},
		"no assertions":  {"tests: [{name: t, check: [{user: 'user:a', object: 'doc:1'}]}]\n", "no assertions"},
		"bad tuple":      {"tests: [{name: t, tuples: [{user: 'user:a'}], check: [{user: 'user:a', object: 'doc:1', assertions: {viewer: true}}]}]\n", "needs user, relation, and object"},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := Parse([]byte(tc.doc))
			if err == nil {
				err = d.Validate(DefaultLimits)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	// Limits.
	d, _ := Parse([]byte("tests: [{name: t, check: [{users: ['user:a', 'user:b'], objects: ['doc:1', 'doc:2'], assertions: {viewer: true, editor: true}}]}]\n"))
	if err := d.Validate(Limits{MaxTuples: 10, MaxAssertions: 7}); err == nil || !strings.Contains(err.Error(), "8 assertions; the limit is 7") {
		t.Errorf("assertion limit: %v", err)
	}
}

// A tuple the model rejects fails its test with the reason, not the run.
func TestRejectedTuple(t *testing.T) {
	d, _ := Parse([]byte("tests: [{name: t, tuples: [{user: 'user:a', relation: nope, object: 'document:1'}], check: [{user: 'user:a', object: 'document:1', assertions: {viewer: false}}]}]\n"))
	r, err := Run(context.Background(), d, seedModel(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.Tests[0].Err == "" || r.Failed != 1 {
		t.Errorf("report = %+v", r.Tests[0])
	}
}
