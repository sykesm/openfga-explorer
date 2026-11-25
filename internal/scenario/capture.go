package scenario

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Capture turns an explained check into a self-contained test: the
// expectation, plus the tuples on its granting path (with their condition
// parameters), so the test reproduces the result on its own.
type Capture struct {
	User, Relation, Object string
	Allowed                bool
	Context                map[string]any
	Tuples                 []Tuple
}

// Test builds the test.
func (c Capture) Test(description string) Test {
	return Test{
		Name:        c.User + " " + c.Relation + " " + c.Object,
		Description: description,
		Tuples:      c.Tuples,
		Check: []Check{{User: c.User, Object: c.Object, Context: c.Context,
			Assertions: map[string]bool{c.Relation: c.Allowed}}},
	}
}

// Mask renames every ID in a test consistently, per type, in order of first
// appearance: user:anne becomes user:user-1, group:eng#member becomes
// group:group-1#member. Wildcards (user:*) and condition parameters are kept.
// The name is rebuilt from the masked check.
func Mask(t *Test) {
	ids := map[string]string{}
	count := map[string]int{}
	obj := func(o string) string {
		typ, id, ok := strings.Cut(o, ":")
		if !ok || id == "*" || id == "" {
			return o
		}
		if m, ok := ids[o]; ok {
			return m
		}
		count[typ]++
		m := fmt.Sprintf("%s:%s-%d", typ, typ, count[typ])
		ids[o] = m
		return m
	}
	user := func(u string) string {
		o, rel, ok := strings.Cut(u, "#")
		if ok {
			return obj(o) + "#" + rel
		}
		return obj(u)
	}
	for i := range t.Check {
		c := &t.Check[i]
		if c.User != "" {
			c.User = user(c.User)
		}
		for j := range c.Users {
			c.Users[j] = user(c.Users[j])
		}
		if c.Object != "" {
			c.Object = obj(c.Object)
		}
		for j := range c.Objects {
			c.Objects[j] = obj(c.Objects[j])
		}
	}
	for i := range t.Tuples {
		t.Tuples[i].User = user(t.Tuples[i].User)
		t.Tuples[i].Object = obj(t.Tuples[i].Object)
	}
	if len(t.Check) == 1 && len(t.Check[0].Assertions) == 1 {
		for rel := range t.Check[0].Assertions {
			t.Name = t.Check[0].User + " " + rel + " " + t.Check[0].Object
		}
	}
}

// AppendTest adds a test to a scenario document, keeping the rest of the
// document (comments included; indentation may be normalized). An empty
// draft becomes a new document. A test whose name is taken gets a suffix.
func AppendTest(draft []byte, t Test, docName string) ([]byte, error) {
	if len(bytes.TrimSpace(draft)) == 0 {
		return encode(&Document{Name: docName, Tests: []Test{t}})
	}
	var root yaml.Node
	if err := yaml.Unmarshal(draft, &root); err != nil {
		return nil, fmt.Errorf("%w: your draft isn't valid YAML, so the test can't be added: %v", ErrInvalid, err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: your draft isn't a scenario document (a mapping with tests:)", ErrInvalid)
	}
	doc := root.Content[0]
	var tests *yaml.Node
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "tests" {
			tests = doc.Content[i+1]
		}
	}
	if tests == nil {
		tests = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "tests"}, tests)
	}
	if tests.Kind == yaml.ScalarNode && (tests.Tag == "!!null" || tests.Value == "" || tests.Value == "[]") {
		*tests = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	}
	if tests.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%w: tests: in your draft isn't a list", ErrInvalid)
	}
	tests.Style = 0 // block style, so the new test reads like the rest
	names := map[string]bool{}
	for _, n := range tests.Content {
		var existing Test
		if n.Decode(&existing) == nil {
			names[existing.Name] = true
		}
	}
	name := t.Name
	for i := 2; names[t.Name]; i++ {
		t.Name = fmt.Sprintf("%s (%d)", name, i)
	}
	var n yaml.Node
	if err := n.Encode(t); err != nil {
		return nil, err
	}
	tests.Content = append(tests.Content, &n)
	out, err := encode(&root)
	if err != nil {
		return nil, err
	}
	if _, err := Parse(out); err != nil {
		return nil, err
	}
	return out, nil
}

func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
