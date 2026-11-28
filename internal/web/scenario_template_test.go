package web

import (
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
	"github.com/sykesm/openfga-explorer/internal/scenario"
	"github.com/sykesm/openfga-explorer/internal/schema"
)

func templateModel(t *testing.T, dsl string) *schema.Model {
	t.Helper()
	pm, err := transformer.TransformDSLToProto(dsl)
	if err != nil {
		t.Fatal(err)
	}
	m, err := schema.Load(pm)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestTemplateExample: the starter scenario uses a person holding a familiar
// relation, not whatever direct relation comes first in the model.
func TestTemplateExample(t *testing.T) {
	for name, tc := range map[string]struct{ dsl, user, rel, typ string }{
		// organization#member and document#org come first; user/viewer/document wins.
		"sample-like": {`model
  schema 1.1
type user
type organization
  relations
    define member: [user]
type folder
  relations
    define viewer: [user]
type document
  relations
    define org: [organization]
    define owner: [user]
    define parent: [folder]
    define viewer: [user, user:*] or owner`, "user", "viewer", "document"},
		"person type not named user": {`model
  schema 1.1
type employee
type team
  relations
    define member: [employee]
    define lead: [employee]`, "employee", "member", "team"},
		"only usersets and wildcards": {`model
  schema 1.1
type user
type group
  relations
    define member: [group#member, user:*]`, "user", "viewer", "document"},
	} {
		m := templateModel(t, tc.dsl)
		if u, r, ty := templateExample(m); u != tc.user || r != tc.rel || ty != tc.typ {
			t.Errorf("%s: got %s %s %s, want %s %s %s", name, u, r, ty, tc.user, tc.rel, tc.typ)
		}
		// The starter document parses and is a well-formed scenario. (Whether
		// it runs is model-specific: see TestTemplateRuns.)
		doc, err := scenario.Parse([]byte(scenarioTemplate(m)))
		if err != nil {
			t.Fatalf("%s: template does not parse: %v", name, err)
		}
		if err := doc.Validate(scenario.Limits{MaxTuples: 10, MaxAssertions: 10}); err != nil {
			t.Errorf("%s: template invalid: %v", name, err)
		}
	}
}

// TestTemplateRuns: on the sample model, the starter scenario runs in the
// sandbox and every check passes, as someone opening Scenarios first sees it.
func TestTemplateRuns(t *testing.T) {
	st := fgatest.Seed(t)
	ctx := t.Context()
	resp, err := st.Client.ReadAuthorizationModel(ctx, &openfgav1.ReadAuthorizationModelRequest{StoreId: st.ID, Id: st.Model})
	if err != nil {
		t.Fatal(err)
	}
	m, err := schema.Load(resp.GetAuthorizationModel())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := scenario.Parse([]byte(scenarioTemplate(m)))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := scenario.Run(ctx, doc, resp.GetAuthorizationModel())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed != 0 || rep.Passed != 2 {
		t.Errorf("starter scenario: %d passed, %d failed; want 2 passed", rep.Passed, rep.Failed)
	}
}
