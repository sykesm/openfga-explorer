package fgatest_test

import (
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestSeedOverHTTP(t *testing.T) {
	st := fgatest.Seed(t)
	c, err := fga.New(fga.Options{APIURL: st.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	id, err := c.LatestModelID(ctx, st.ID)
	if err != nil || id != st.Model {
		t.Fatalf("latest model: %q %v (want %q)", id, err, st.Model)
	}
	reqCtx, _ := structpb.NewStruct(map[string]any{"current_time": "2026-11-24T12:00:00Z"})
	for _, tc := range []struct {
		user, rel, obj string
		want           bool
	}{
		{"user:eng-002", "can_view", "document:doc-0007", true},
		{"user:mallory", "can_view", "document:doc-0004", false},
	} {
		resp, err := c.Check(ctx, &openfgav1.CheckRequest{StoreId: st.ID, Context: reqCtx,
			TupleKey: &openfgav1.CheckRequestTupleKey{User: tc.user, Relation: tc.rel, Object: tc.obj}})
		if err != nil || resp.GetAllowed() != tc.want {
			t.Errorf("%s %s %s: allowed=%v err=%v, want %v", tc.user, tc.rel, tc.obj, resp.GetAllowed(), err, tc.want)
		}
	}
	// Request validation runs as on a deployed server.
	_, err = c.Check(ctx, &openfgav1.CheckRequest{StoreId: st.ID,
		TupleKey: &openfgav1.CheckRequestTupleKey{User: "not a user", Relation: "viewer", Object: "document:x"}})
	if err == nil {
		t.Error("invalid user accepted")
	}
}
