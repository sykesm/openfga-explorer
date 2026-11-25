// Package scenario runs authorization scenarios: tuples plus expected
// results, in the format of `fga model test` (.fga.yaml). JSON documents
// work too (JSON is YAML). Each test runs in a fresh in-memory OpenFGA
// engine inside the request, so nothing is written to a shared server and no
// state outlives the request.
package scenario

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Document is a scenario file. Field names match the fga CLI's store test
// format, so the same document runs with `fga model test`.
type Document struct {
	Name string `json:"name" yaml:"name"`
	// Model is the authorization model in DSL. When empty, the explorer runs
	// the scenario against the model being viewed (as `fga model test
	// --store-id` runs it against a store).
	Model      string   `json:"model,omitempty" yaml:"model,omitempty"`
	ModelFile  string   `json:"model_file,omitempty" yaml:"model_file,omitempty"`
	Tuples     []Tuple  `json:"tuples,omitempty" yaml:"tuples,omitempty"`
	TupleFile  string   `json:"tuple_file,omitempty" yaml:"tuple_file,omitempty"`
	TupleFiles []string `json:"tuple_files,omitempty" yaml:"tuple_files,omitempty"`
	Tests      []Test   `json:"tests" yaml:"tests"`
}

// Test is one named test: its own tuples (added to the document's) and
// expectations.
type Test struct {
	Name        string        `json:"name" yaml:"name"`
	Description string        `json:"description,omitempty" yaml:"description,omitempty"`
	Tuples      []Tuple       `json:"tuples,omitempty" yaml:"tuples,omitempty"`
	TupleFile   string        `json:"tuple_file,omitempty" yaml:"tuple_file,omitempty"`
	Check       []Check       `json:"check,omitempty" yaml:"check,omitempty"`
	ListObjects []ListObjects `json:"list_objects,omitempty" yaml:"list_objects,omitempty"`
	ListUsers   []ListUsers   `json:"list_users,omitempty" yaml:"list_users,omitempty"`
}

// Tuple is a relationship tuple, optionally with a condition.
type Tuple struct {
	User      string     `json:"user" yaml:"user"`
	Relation  string     `json:"relation" yaml:"relation"`
	Object    string     `json:"object" yaml:"object"`
	Condition *Condition `json:"condition,omitempty" yaml:"condition,omitempty"`
}

func (t Tuple) String() string {
	s := t.User + " " + t.Relation + " " + t.Object
	if t.Condition != nil {
		s += " [" + t.Condition.Name + "]"
	}
	return s
}

// Condition names a model condition and its tuple-side parameters.
type Condition struct {
	Name    string         `json:"name" yaml:"name"`
	Context map[string]any `json:"context,omitempty" yaml:"context,omitempty"`
}

// Check expects, for each user and object, whether each relation holds.
type Check struct {
	User       string          `json:"user,omitempty" yaml:"user,omitempty"`
	Users      []string        `json:"users,omitempty" yaml:"users,omitempty"`
	Object     string          `json:"object,omitempty" yaml:"object,omitempty"`
	Objects    []string        `json:"objects,omitempty" yaml:"objects,omitempty"`
	Context    map[string]any  `json:"context,omitempty" yaml:"context,omitempty"`
	Assertions map[string]bool `json:"assertions" yaml:"assertions"`
}

// ListObjects expects, for a user, the objects of a type each relation reaches.
type ListObjects struct {
	User       string              `json:"user" yaml:"user"`
	Type       string              `json:"type" yaml:"type"`
	Context    map[string]any      `json:"context,omitempty" yaml:"context,omitempty"`
	Assertions map[string][]string `json:"assertions" yaml:"assertions"`
}

// ListUsers expects, for an object, the users (of the filtered types) each
// relation reaches.
type ListUsers struct {
	Object     string                        `json:"object" yaml:"object"`
	UserFilter []UserFilter                  `json:"user_filter" yaml:"user_filter"`
	Context    map[string]any                `json:"context,omitempty" yaml:"context,omitempty"`
	Assertions map[string]ListUsersAssertion `json:"assertions" yaml:"assertions"`
}

// UserFilter selects user types (or usersets, with Relation) for ListUsers.
type UserFilter struct {
	Type     string `json:"type" yaml:"type"`
	Relation string `json:"relation,omitempty" yaml:"relation,omitempty"`
}

// ListUsersAssertion is the expected users.
type ListUsersAssertion struct {
	Users []string `json:"users" yaml:"users"`
}

// Limits bound one run, so a scenario can't monopolize a replica.
type Limits struct {
	MaxTuples     int // per test, including the document's shared tuples
	MaxAssertions int // across all tests
}

// DefaultLimits are generous for hand-written scenarios.
var DefaultLimits = Limits{MaxTuples: 5000, MaxAssertions: 500}

// ErrInvalid wraps problems with a document (as opposed to running it).
var ErrInvalid = errors.New("invalid scenario")

// Parse reads a scenario document in YAML or JSON. Unknown fields are
// errors, as in the fga CLI, so typos don't silently drop expectations.
func Parse(data []byte) (*Document, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: the document is empty", ErrInvalid)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var d Document
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return &d, nil
}

// Validate checks a document can run here, within the limits.
func (d *Document) Validate(l Limits) error {
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if d.ModelFile != "" || d.TupleFile != "" || len(d.TupleFiles) > 0 {
		bad("file references (model_file, tuple_file, tuple_files) can't be read here: put the model and tuples in the document")
	}
	if len(d.Tests) == 0 {
		bad("there are no tests")
	}
	assertions := 0
	for i, t := range d.Tests {
		where := fmt.Sprintf("test %d", i+1)
		if t.Name != "" {
			where = fmt.Sprintf("test %q", t.Name)
		}
		if t.TupleFile != "" {
			bad("%s: tuple_file can't be read here: put the tuples in the test", where)
		}
		if n := len(d.Tuples) + len(t.Tuples); n > l.MaxTuples {
			bad("%s has %d tuples; the limit is %d", where, n, l.MaxTuples)
		}
		for j, tp := range append(append([]Tuple{}, d.Tuples...), t.Tuples...) {
			if tp.User == "" || tp.Relation == "" || tp.Object == "" {
				bad("%s: tuple %d needs user, relation, and object", where, j+1)
			}
		}
		for j, c := range t.Check {
			if (c.User == "") == (len(c.Users) == 0) {
				bad("%s: check %d needs exactly one of user or users", where, j+1)
			}
			if (c.Object == "") == (len(c.Objects) == 0) {
				bad("%s: check %d needs exactly one of object or objects", where, j+1)
			}
			if len(c.Assertions) == 0 {
				bad("%s: check %d has no assertions", where, j+1)
			}
			assertions += max(1, len(c.Users)) * max(1, len(c.Objects)) * len(c.Assertions)
		}
		for j, lo := range t.ListObjects {
			if lo.User == "" || lo.Type == "" || len(lo.Assertions) == 0 {
				bad("%s: list_objects %d needs user, type, and assertions", where, j+1)
			}
			assertions += len(lo.Assertions)
		}
		for j, lu := range t.ListUsers {
			if lu.Object == "" || len(lu.UserFilter) == 0 || len(lu.Assertions) == 0 {
				bad("%s: list_users %d needs object, user_filter, and assertions", where, j+1)
			}
			assertions += len(lu.Assertions)
		}
		if len(t.Check)+len(t.ListObjects)+len(t.ListUsers) == 0 {
			bad("%s has no check, list_objects, or list_users expectations", where)
		}
	}
	if assertions > l.MaxAssertions {
		bad("the document has %d assertions; the limit is %d", assertions, l.MaxAssertions)
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w:\n- %s", ErrInvalid, strings.Join(problems, "\n- "))
	}
	return nil
}
