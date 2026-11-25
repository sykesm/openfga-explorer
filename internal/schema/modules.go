package schema

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
)

// Modular models (schema 1.2) are written as several files, each a
// "module". OpenFGA keeps where every definition came from in the model's
// metadata: types and conditions record their module and file, and
// relations that a module adds to another module's type (`extend type`)
// record their own. Nothing else about the files is stored, so the files
// are rebuilt from that metadata (ModuleFiles).

// Source is where a definition is written in a modular model. It is zero
// for models that are not modular.
type Source struct {
	Module string
	File   string
}

// IsZero reports whether the definition has no module (a non-modular model).
func (s Source) IsZero() bool { return s.Module == "" && s.File == "" }

func (s Source) String() string {
	switch {
	case s.IsZero():
		return ""
	case s.File == "":
		return s.Module
	}
	return s.Module + " (" + s.File + ")"
}

// Module summarizes one module of a modular model.
type Module struct {
	Name  string
	Files []string
	// Types are the types the module defines.
	Types []string
	// Extends are relations the module adds to types defined elsewhere.
	Extends []Extension
	// Conditions are the conditions the module defines.
	Conditions []string
}

// Extension is a module's `extend type` block.
type Extension struct {
	Type      string
	File      string
	Relations []string
}

func metaSource(module string, si *openfgav1.SourceInfo) Source {
	return Source{Module: module, File: si.GetFile()}
}

// loadSources fills in the module of every type, relation, and condition.
// Relations without their own module belong to their type's module.
func (m *Model) loadSources() {
	m.ConditionSources = map[string]Source{}
	for _, td := range m.Proto.GetTypeDefinitions() {
		t := m.Types[td.GetType()]
		if t == nil {
			continue
		}
		t.Source = metaSource(td.GetMetadata().GetModule(), td.GetMetadata().GetSourceInfo())
		if !t.Source.IsZero() {
			m.Modular = true
		}
		for name, r := range t.Relations {
			rm := td.GetMetadata().GetRelations()[name]
			r.Source = t.Source
			if rm.GetModule() != "" || rm.GetSourceInfo().GetFile() != "" {
				r.Source = metaSource(rm.GetModule(), rm.GetSourceInfo())
			}
			r.Extended = !t.Source.IsZero() && r.Source != t.Source
		}
	}
	for name, c := range m.Conditions {
		s := metaSource(c.GetMetadata().GetModule(), c.GetMetadata().GetSourceInfo())
		m.ConditionSources[name] = s
		if !s.IsZero() {
			m.Modular = true
		}
	}
}

// Modules lists the modules of a modular model, by name; nil otherwise.
func (m *Model) Modules() []Module {
	if !m.Modular {
		return nil
	}
	byName := map[string]*Module{}
	get := func(s Source) *Module {
		mod := byName[s.Module]
		if mod == nil {
			mod = &Module{Name: s.Module}
			byName[s.Module] = mod
		}
		if s.File != "" && !slices.Contains(mod.Files, s.File) {
			mod.Files = append(mod.Files, s.File)
		}
		return mod
	}
	for _, tn := range m.TypeNames {
		t := m.Types[tn]
		get(t.Source).Types = append(get(t.Source).Types, tn)
		ext := map[Source][]string{}
		for _, rn := range t.RelationNames {
			if r := t.Relations[rn]; r.Extended {
				ext[r.Source] = append(ext[r.Source], rn)
			}
		}
		for s, rels := range ext {
			sort.Strings(rels)
			mod := get(s)
			mod.Extends = append(mod.Extends, Extension{Type: tn, File: s.File, Relations: rels})
		}
	}
	for name, s := range m.ConditionSources {
		get(s).Conditions = append(get(s).Conditions, name)
	}
	out := make([]Module, 0, len(byName))
	for _, mod := range byName {
		sort.Strings(mod.Files)
		sort.Strings(mod.Conditions)
		sort.Slice(mod.Extends, func(i, j int) bool { return mod.Extends[i].Type < mod.Extends[j].Type })
		out = append(out, *mod)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ModuleFile is one rebuilt file of a modular model.
type ModuleFile struct {
	Name   string // path, as listed in fga.mod
	Module string
	DSL    string
}

// ModuleFiles rebuilds the files of a modular model from its metadata, in
// path order, plus the fga.mod manifest listing them. Combining the files
// again (transformer.TransformModuleFilesToModel) gives back the same model.
func (m *Model) ModuleFiles() (files []ModuleFile, manifest string, err error) {
	if !m.Modular {
		return nil, "", fmt.Errorf("the model is not modular")
	}
	type part struct {
		module   string
		owned    []*openfgav1.TypeDefinition // types defined in the file
		extended []*openfgav1.TypeDefinition // relations added to other types
		conds    map[string]*openfgav1.Condition
	}
	parts := map[string]*part{}
	get := func(s Source) *part {
		p := parts[s.File]
		if p == nil {
			p = &part{module: s.Module, conds: map[string]*openfgav1.Condition{}}
			parts[s.File] = p
		}
		return p
	}
	for _, td := range m.Proto.GetTypeDefinitions() {
		t := m.Types[td.GetType()]
		// Split the type's relations by the file that defines them.
		byFile := map[Source]*openfgav1.TypeDefinition{}
		sub := func(s Source) *openfgav1.TypeDefinition {
			if byFile[s] == nil {
				byFile[s] = &openfgav1.TypeDefinition{Type: td.GetType(), Relations: map[string]*openfgav1.Userset{},
					Metadata: &openfgav1.Metadata{Relations: map[string]*openfgav1.RelationMetadata{}}}
			}
			return byFile[s]
		}
		sub(t.Source) // a type is written in its own file even without relations
		for name, rw := range td.GetRelations() {
			s := t.Relations[name].Source
			d := sub(s)
			d.Relations[name] = rw
			if rm := td.GetMetadata().GetRelations()[name]; rm != nil {
				d.Metadata.Relations[name] = &openfgav1.RelationMetadata{DirectlyRelatedUserTypes: rm.GetDirectlyRelatedUserTypes()}
			}
		}
		for s, d := range byFile {
			if len(d.GetRelations()) == 0 {
				d.Relations, d.Metadata = nil, nil
			}
			if s == t.Source {
				get(s).owned = append(get(s).owned, d)
			} else {
				get(s).extended = append(get(s).extended, d)
			}
		}
	}
	for name, c := range m.Conditions {
		s := m.ConditionSources[name]
		cc := &openfgav1.Condition{Name: c.GetName(), Expression: c.GetExpression(), Parameters: c.GetParameters()}
		get(s).conds[name] = cc
	}
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	sort.Strings(names)
	var mod strings.Builder
	mod.WriteString("schema: '1.2'\ncontents:\n")
	for _, name := range names {
		p := parts[name]
		dsl, err := renderModule(p.module, p.owned, p.extended, p.conds)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", name, err)
		}
		files = append(files, ModuleFile{Name: name, Module: p.module, DSL: dsl})
		fmt.Fprintf(&mod, "  - %s\n", name)
	}
	return files, mod.String(), nil
}

// renderModule writes one module file with the official DSL printer: the
// file's own types, then its `extend type` blocks, then its conditions.
func renderModule(module string, owned, extended []*openfgav1.TypeDefinition, conds map[string]*openfgav1.Condition) (string, error) {
	sortTypes := func(ts []*openfgav1.TypeDefinition) {
		sort.SliceStable(ts, func(i, j int) bool { return ts[i].GetType() < ts[j].GetType() })
	}
	sortTypes(owned)
	sortTypes(extended)
	print := func(tds []*openfgav1.TypeDefinition, cs map[string]*openfgav1.Condition) (string, error) {
		if len(tds) == 0 && len(cs) == 0 {
			return "", nil
		}
		dsl, err := transformer.TransformJSONProtoToDSL(&openfgav1.AuthorizationModel{SchemaVersion: "1.1", TypeDefinitions: tds, Conditions: cs})
		if err != nil {
			return "", err
		}
		// Drop the printer's "model / schema 1.1" header: modules have their own.
		_, body, _ := strings.Cut(dsl, "schema 1.1\n")
		return strings.TrimSpace(body), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "module %s\n", module)
	ownedDSL, err := print(owned, nil)
	if err != nil {
		return "", err
	}
	if ownedDSL != "" {
		b.WriteString("\n" + ownedDSL + "\n")
	}
	extDSL, err := print(extended, nil)
	if err != nil {
		return "", err
	}
	if extDSL != "" {
		lines := strings.Split(extDSL, "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "type ") {
				lines[i] = "extend " + l
			}
		}
		b.WriteString("\n" + strings.Join(lines, "\n") + "\n")
	}
	condDSL, err := print(nil, conds)
	if err != nil {
		return "", err
	}
	if condDSL != "" {
		b.WriteString("\n" + condDSL + "\n")
	}
	return b.String(), nil
}
