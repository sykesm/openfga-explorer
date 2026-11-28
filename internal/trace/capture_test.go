package trace

import (
	"reflect"
	"testing"

	"github.com/sykesm/openfga-explorer/internal/cond"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestGrantingPathTuplesKeepValues: a captured tuple's condition parameters
// are the values the tuple supplied, not their display form read back (a
// string "123" stays a string), and the request's parameters are left out.
func TestGrantingPathTuplesKeepValues(t *testing.T) {
	list, _ := structpb.NewList([]any{"a", "b"})
	tr := &Trace{Root: &Node{ID: 1, Outcome: Allowed, Children: []*Link{{
		Outcome: Allowed,
		Node:    &Node{ID: 2, Kind: KindSubject, Outcome: Allowed},
		Tuple:   &Tuple{User: "user:anne", Relation: "viewer", Object: "doc:1", Condition: "c"},
		Cond: &cond.Result{Name: "c", Status: cond.Met, Params: []cond.Param{
			{Name: "code", Type: "any", Value: "123", Source: "tuple", Raw: structpb.NewStringValue("123")},
			{Name: "tags", Type: "list<string>", Value: `["a","b"]`, Source: "tuple", Raw: structpb.NewListValue(list)},
			{Name: "now", Type: "timestamp", Value: "2026-01-01T00:00:00Z", Source: "request", Raw: structpb.NewStringValue("2026-01-01T00:00:00Z")},
		}},
	}}}}
	got := tr.GrantingPathTuples()
	want := map[string]any{"code": "123", "tags": []any{"a", "b"}}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Context, want) {
		t.Errorf("got %+v, want context %v", got, want)
	}
}
