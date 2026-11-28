package fga_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/fgatest"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestClientAgainstServer(t *testing.T) {
	st := fgatest.Seed(t)
	c, err := fga.New(fga.Options{APIURL: st.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	stores, err := c.ListStores(ctx)
	if err != nil || len(stores) == 0 {
		t.Fatalf("ListStores: %v %v", stores, err)
	}
	if s, err := c.GetStore(ctx, st.ID); err != nil || s.GetName() != "explorer-demo" {
		t.Errorf("GetStore: %v %v", s, err)
	}
	models, err := c.ListModels(ctx, st.ID, 10)
	if err != nil || len(models) != 1 {
		t.Errorf("ListModels: %d %v", len(models), err)
	}
	m, err := c.Model(ctx, st.ID, st.Model)
	if err != nil || len(m.GetTypeDefinitions()) == 0 {
		t.Fatalf("Model: %v", err)
	}
	reqCtx, _ := structpb.NewStruct(map[string]any{"current_time": "2026-11-24T12:00:00Z", "user_ip": "10.1.2.3"})

	lo, err := c.ListObjects(ctx, &openfgav1.ListObjectsRequest{StoreId: st.ID, User: "user:anne", Relation: "member", Type: "group"})
	if err != nil || len(lo.GetObjects()) == 0 {
		t.Errorf("ListObjects: %v %v", lo.GetObjects(), err)
	}
	lu, err := c.ListUsers(ctx, &openfgav1.ListUsersRequest{StoreId: st.ID, Object: &openfgav1.Object{Type: "document", Id: "offsite-plan"},
		Relation: "can_view", UserFilters: []*openfgav1.UserTypeFilter{{Type: "user"}}, Context: reqCtx})
	if err != nil || len(lu.GetUsers()) == 0 {
		t.Errorf("ListUsers: %v %v", lu.GetUsers(), err)
	}
	bc, err := c.BatchCheck(ctx, &openfgav1.BatchCheckRequest{StoreId: st.ID, Checks: []*openfgav1.BatchCheckItem{
		{TupleKey: &openfgav1.CheckRequestTupleKey{User: "user:eng-002", Relation: "can_view", Object: "document:doc-0007"}, Context: reqCtx, CorrelationId: "a"},
		{TupleKey: &openfgav1.CheckRequestTupleKey{User: "user:mallory", Relation: "can_view", Object: "document:doc-0004"}, Context: reqCtx, CorrelationId: "b"},
	}})
	if err != nil || !bc.GetResult()["a"].GetAllowed() || bc.GetResult()["b"].GetAllowed() {
		t.Errorf("BatchCheck: %v %v", bc.GetResult(), err)
	}

	// Server errors decode into *fga.Error with the OpenFGA error code.
	_, err = c.Check(ctx, &openfgav1.CheckRequest{StoreId: st.ID, TupleKey: &openfgav1.CheckRequestTupleKey{User: "user:anne", Relation: "nope", Object: "document:roadmap"}})
	var fe *fga.Error
	if !errors.As(err, &fe) || fe.Status != http.StatusBadRequest || fe.Code == "" {
		t.Errorf("Check with an unknown relation: %#v", err)
	}
}

func TestClientOptions(t *testing.T) {
	if _, err := fga.New(fga.Options{APIURL: "::not a url"}); err == nil {
		t.Error("invalid URL accepted")
	}
	// A bearer token is sent on every request; non-JSON errors keep their text.
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer srv.Close()
	c, err := fga.New(fga.Options{APIURL: srv.URL, APIToken: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListStores(t.Context())
	var fe *fga.Error
	if !errors.As(err, &fe) || fe.Status != http.StatusBadGateway || fe.Message != "upstream unavailable" {
		t.Errorf("error = %#v", err)
	}
	if auth != "Bearer s3cret" {
		t.Errorf("Authorization = %q", auth)
	}
	if fe.Error() == "" {
		t.Error("empty error text")
	}
}

// An error body's own "status" field (proxies send one) does not replace
// the HTTP status.
func TestErrorKeepsHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status": 500, "code": "store_id_not_found", "message": "store not found"}`))
	}))
	defer srv.Close()
	c, err := fga.New(fga.Options{APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListStores(t.Context())
	var fe *fga.Error
	if !errors.As(err, &fe) || fe.Status != http.StatusNotFound || fe.Code != "store_id_not_found" {
		t.Errorf("error = %#v", err)
	}
}
