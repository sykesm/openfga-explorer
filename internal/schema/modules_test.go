package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// loadModules combines a fga.mod directory into a model, as `fga model write` does.
func loadModules(t *testing.T, dir string) *openfgav1.AuthorizationModel {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "fga.mod"))
	if err != nil {
		t.Fatal(err)
	}
	mod, err := transformer.TransformModFile(string(b))
	if err != nil {
		t.Fatal(err)
	}
	var files []transformer.ModuleFile
	for _, f := range mod.Contents.Value {
		c, err := os.ReadFile(filepath.Join(dir, f.Value))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, transformer.ModuleFile{Name: f.Value, Contents: string(c)})
	}
	pm, err := transformer.TransformModuleFilesToModel(files, mod.Schema.Value)
	if err != nil {
		t.Fatal(err)
	}
	return viaJSON(t, pm)
}

// viaJSON round-trips a model through JSON, as the API does. The module
// transformer (language v0.3.2) leaves direct relations as an empty oneof
// ("this" with a nil value), which its own DSL printer rejects; models read
// from the API never look like that.
func viaJSON(t *testing.T, pm *openfgav1.AuthorizationModel) *openfgav1.AuthorizationModel {
	t.Helper()
	b, err := protojson.Marshal(pm)
	if err != nil {
		t.Fatal(err)
	}
	out := &openfgav1.AuthorizationModel{}
	if err := protojson.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	return out
}

const modularDir = "../../deploy/seed/modular"

func TestModuleSources(t *testing.T) {
	m, err := Load(loadModules(t, modularDir))
	if err != nil {
		t.Fatal(err)
	}
	if !m.Modular {
		t.Fatal("not detected as modular")
	}
	for _, tc := range []struct {
		typ, rel string
		want     Source
		extended bool
	}{
		{"organization", "admin", Source{"core", "core.fga"}, false},
		{"organization", "can_create_folder", Source{"docs", "docs/docs.fga"}, true},
		{"document", "viewer", Source{"docs", "docs/docs.fga"}, false},
		{"document", "can_audit", Source{"compliance", "compliance.fga"}, true},
		{"audit_policy", "auditor", Source{"compliance", "compliance.fga"}, false},
	} {
		r, ok := m.Relation(tc.typ, tc.rel)
		if !ok {
			t.Fatalf("%s#%s missing", tc.typ, tc.rel)
		}
		if r.Source != tc.want || r.Extended != tc.extended {
			t.Errorf("%s#%s: source %+v extended %v, want %+v %v", tc.typ, tc.rel, r.Source, r.Extended, tc.want, tc.extended)
		}
	}
	if s := m.Types["document"].Source; s != (Source{"docs", "docs/docs.fga"}) {
		t.Errorf("document defined in %+v", s)
	}
	if s := m.ConditionSources["business_hours"]; s.Module != "docs" {
		t.Errorf("condition source %+v", s)
	}

	mods := m.Modules()
	if len(mods) != 3 || mods[0].Name != "compliance" || mods[1].Name != "core" || mods[2].Name != "docs" {
		t.Fatalf("modules = %+v", mods)
	}
	comp := mods[0]
	if strings.Join(comp.Types, ",") != "audit_policy" || len(comp.Extends) != 1 || comp.Extends[0].Type != "document" ||
		strings.Join(comp.Extends[0].Relations, ",") != "can_audit,can_view,policy" {
		t.Errorf("compliance = %+v", comp)
	}
	docs := mods[2]
	if strings.Join(docs.Types, ",") != "document,folder" || len(docs.Extends) != 1 || docs.Extends[0].Type != "organization" ||
		strings.Join(docs.Conditions, ",") != "business_hours" {
		t.Errorf("docs = %+v", docs)
	}
}

// TestModuleFilesRoundTrip: files rebuilt from a model's metadata combine
// back into the same model, so module scoping survives viewing and editing.
func TestModuleFilesRoundTrip(t *testing.T) {
	orig := loadModules(t, modularDir)
	m, err := Load(orig)
	if err != nil {
		t.Fatal(err)
	}
	files, manifest, err := m.ModuleFiles()
	if err != nil {
		t.Fatal(err)
	}
	if manifest != "schema: '1.2'\ncontents:\n  - compliance.fga\n  - core.fga\n  - docs/docs.fga\n" {
		t.Errorf("manifest:\n%s", manifest)
	}
	var mf []transformer.ModuleFile
	for _, f := range files {
		mf = append(mf, transformer.ModuleFile{Name: f.Name, Contents: f.DSL})
	}
	again, err := transformer.TransformModuleFilesToModel(mf, "1.2")
	if err != nil {
		for _, f := range files {
			t.Logf("--- %s\n%s", f.Name, f.DSL)
		}
		t.Fatal(err)
	}
	again = viaJSON(t, again)
	// Type order follows file order, which may differ: compare by type.
	byType := func(pm *openfgav1.AuthorizationModel) map[string]*openfgav1.TypeDefinition {
		out := map[string]*openfgav1.TypeDefinition{}
		for _, td := range pm.GetTypeDefinitions() {
			out[td.GetType()] = td
		}
		return out
	}
	a, b := byType(orig), byType(again)
	if len(a) != len(b) {
		t.Fatalf("%d types, rebuilt %d", len(a), len(b))
	}
	for name, td := range a {
		if !proto.Equal(td, b[name]) {
			t.Errorf("type %s differs after the round trip:\n  was %v\n  now %v", name, td, b[name])
		}
	}
	for name, c := range orig.GetConditions() {
		if !proto.Equal(c, again.GetConditions()[name]) {
			t.Errorf("condition %s differs:\n  was %v\n  now %v", name, c, again.GetConditions()[name])
		}
	}
	for _, f := range files {
		if f.Name == "compliance.fga" && !strings.Contains(f.DSL, "extend type document") {
			t.Errorf("compliance.fga lost its extension:\n%s", f.DSL)
		}
	}
	t.Logf("compliance.fga:\n%s", files[0].DSL)
}

func TestNotModular(t *testing.T) {
	m := load(t)
	if m.Modular || m.Modules() != nil || m.Types["doc"].Source != (Source{}) {
		t.Error("a single-file model is not modular")
	}
	if _, _, err := m.ModuleFiles(); err == nil {
		t.Error("ModuleFiles on a non-modular model")
	}
}
