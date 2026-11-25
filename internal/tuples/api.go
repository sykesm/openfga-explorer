package tuples

import (
	"context"
	"strings"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/fga"
)

// API reads edges through the OpenFGA Read API.
type API struct {
	Client fga.API
	// ScanLimit bounds how many tuples one filtered request reads.
	ScanLimit int
}

// NewAPI returns an API source.
func NewAPI(c fga.API) *API { return &API{Client: c, ScanLimit: 1000} }

func (a *API) Name() string       { return "api" }
func (a *API) Label() string      { return "OpenFGA API · contains filter" }
func (a *API) FilterMode() string { return "contains" }

func (a *API) key(q Query) *openfgav1.ReadRequestTupleKey {
	if q.Dir == In {
		return &openfgav1.ReadRequestTupleKey{User: q.User, Relation: q.Relation, Object: q.ObjectType + ":"}
	}
	return &openfgav1.ReadRequestTupleKey{Object: q.Object, Relation: q.Relation}
}

// Edges reads pages of 100. With a filter it keeps reading until it has
// Limit matches or has examined ScanLimit tuples.
func (a *API) Edges(ctx context.Context, q Query) (*Page, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	match := q.Match
	if match == nil && q.Contains != "" {
		f := strings.ToLower(q.Contains)
		match = func(p string) bool { return strings.Contains(strings.ToLower(p), f) }
	}
	page := &Page{}
	token := q.Cursor
	for {
		resp, err := fga.ReadPage(ctx, a.Client, q.Store, a.key(q), 100, token, openfgav1.ConsistencyPreference_UNSPECIFIED)
		if err != nil {
			return nil, err
		}
		for _, t := range resp.GetTuples() {
			page.Scanned++
			if match == nil || match(Peer(q, t.GetKey())) {
				page.Tuples = append(page.Tuples, t.GetKey())
			}
		}
		token = resp.GetContinuationToken()
		page.More = token != ""
		if !page.More || match == nil || len(page.Tuples) >= q.Limit || page.Scanned >= a.ScanLimit {
			break
		}
	}
	if page.More {
		page.Cursor = token
	}
	return page, nil
}

// Count reads one page: the API cannot count beyond it.
func (a *API) Count(ctx context.Context, q Query, limit int) (Count, error) {
	resp, err := fga.ReadPage(ctx, a.Client, q.Store, a.key(q), 100, "", openfgav1.ConsistencyPreference_UNSPECIFIED)
	if err != nil {
		return Count{}, err
	}
	n := len(resp.GetTuples())
	return Count{N: n, Exact: resp.GetContinuationToken() == ""}, nil
}
