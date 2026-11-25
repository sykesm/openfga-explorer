package schema

import (
	"fmt"
	"os"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
)

const lintDSL = `model
  schema 1.1
type user
type robot
type drone
type team
  relations
    define member: [user]
type folder
  relations
    define viewer: [user]
type bucket
  relations
    define owner: [user]
type document
  relations
    define parent: [folder, bucket]
    define viewer: [user with fresh] or viewer from parent
    define approver: [user]
    define can_approve: approver and nobody
    define nobody: can_approve
    define human: [user]
    define machine: [drone]
    define both: human and machine
condition fresh(now: timestamp, issued: timestamp, unused_param: string) {
  now < issued + duration("1h")
}
condition dead(x: bool) {
  x
}
`

func TestModelChecks(t *testing.T) {
	pm, err := transformer.TransformDSLToProto(lintDSL)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Load(pm)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range m.Check() {
		got = append(got, fmt.Sprintf("%s %s %s#%s%s", f.Severity, f.Code, f.Type, f.Relation, f.Condition))
	}
	want := []string{
		"warning unreachable-relation document#both",
		"warning unreachable-relation document#can_approve",
		"warning unreachable-relation document#nobody",
		"warning from-missing-relation document#viewer",
		"notice unused-condition #dead",
		"notice unused-parameter #fresh",
		"notice orphan-type robot#",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, f := range m.Check() {
		if f.Code == "from-missing-relation" && !strings.Contains(f.Message, "parent can be a bucket, which has no viewer") {
			t.Errorf("message: %s", f.Message)
		}
		if f.Code == "unused-parameter" && !strings.Contains(f.Message, "unused_param") {
			t.Errorf("message: %s", f.Message)
		}
	}
	if fs := FindingsFor(m.Check(), "document", "viewer"); len(fs) != 1 {
		t.Errorf("FindingsFor(document, viewer) = %v", fs)
	}
}

// The sample models are clean: no warnings (notices are allowed).
func TestSampleModelsHaveNoWarnings(t *testing.T) {
	b, err := os.ReadFile("../../deploy/seed/model.fga")
	if err != nil {
		t.Fatal(err)
	}
	seed, err := transformer.TransformDSLToProto(string(b))
	if err != nil {
		t.Fatal(err)
	}
	for name, pm := range map[string]*openfgav1.AuthorizationModel{"seed": seed, "modular": loadModules(t, modularDir)} {
		m, err := Load(pm)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range m.Check() {
			if f.Severity == Warning {
				t.Errorf("%s: %s", name, f.Message)
			} else {
				t.Logf("%s: %s", name, f.Message)
			}
		}
	}
}
