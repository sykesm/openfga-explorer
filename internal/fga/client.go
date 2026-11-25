// Package fga is a thin OpenFGA HTTP API client built on the official
// protobuf message types, so the model, tuples, and conditions share one
// representation with the rest of the application.
package fga

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// API is the subset of the OpenFGA API used by the trace walker and explorer.
type API interface {
	Read(ctx context.Context, req *openfgav1.ReadRequest) (*openfgav1.ReadResponse, error)
	Check(ctx context.Context, req *openfgav1.CheckRequest) (*openfgav1.CheckResponse, error)
	ListObjects(ctx context.Context, req *openfgav1.ListObjectsRequest) (*openfgav1.ListObjectsResponse, error)
}

// Options configures a Client.
type Options struct {
	APIURL string
	// APIToken is a static bearer token (pre-shared key).
	APIToken string
	// Client credentials (OIDC) authentication; used when ClientID is set.
	ClientID, ClientSecret, TokenURL, Audience string
	Timeout                                    time.Duration
}

// Client calls the OpenFGA HTTP API.
type Client struct {
	base *url.URL
	http *http.Client

	mu     sync.Mutex
	models map[string]*openfgav1.AuthorizationModel // immutable; keyed by store/model
}

// New creates a client.
func New(opts Options) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(opts.APIURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid OpenFGA API URL %q", opts.APIURL)
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	tr := transport()
	hc := &http.Client{Timeout: timeout, Transport: tr}
	switch {
	case opts.ClientID != "":
		cc := clientcredentials.Config{ClientID: opts.ClientID, ClientSecret: opts.ClientSecret, TokenURL: opts.TokenURL}
		if opts.Audience != "" {
			cc.EndpointParams = url.Values{"audience": {opts.Audience}}
		}
		hc = cc.Client(context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Transport: tr}))
		hc.Timeout = timeout
	case opts.APIToken != "":
		hc.Transport = bearer{token: opts.APIToken, next: tr}
	}
	return &Client{base: u, http: hc, models: map[string]*openfgav1.AuthorizationModel{}}, nil
}

// transport keeps enough idle connections for concurrent walks: each walk
// issues up to 16 requests at once, and the default (2 idle per host) would
// close and reopen connections constantly, exhausting local ports under load.
func transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 256
	t.MaxIdleConnsPerHost = 64
	return t
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// Error is an error response from the OpenFGA API.
type Error struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("openfga: %s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("openfga: HTTP %d: %s", e.Status, e.Message)
}

var (
	marshal   = protojson.MarshalOptions{UseProtoNames: true}
	unmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}
)

func (c *Client) do(ctx context.Context, method, path string, in, out proto.Message) error {
	var body io.Reader
	if in != nil {
		b, err := marshal.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		e := &Error{Status: resp.StatusCode}
		if json.Unmarshal(data, e) != nil || e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		return e
	}
	return unmarshal.Unmarshal(data, out)
}

func storePath(store string, rest string) string {
	return "/stores/" + url.PathEscape(store) + rest
}

// ListStores returns all stores.
func (c *Client) ListStores(ctx context.Context) ([]*openfgav1.Store, error) {
	var out []*openfgav1.Store
	token := ""
	for {
		q := url.Values{"page_size": {"100"}}
		if token != "" {
			q.Set("continuation_token", token)
		}
		var resp openfgav1.ListStoresResponse
		if err := c.do(ctx, http.MethodGet, "/stores?"+q.Encode(), nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.GetStores()...)
		token = resp.GetContinuationToken()
		if token == "" || len(out) > 10000 {
			return out, nil
		}
	}
}

// GetStore returns one store.
func (c *Client) GetStore(ctx context.Context, store string) (*openfgav1.GetStoreResponse, error) {
	var resp openfgav1.GetStoreResponse
	return &resp, c.do(ctx, http.MethodGet, storePath(store, ""), nil, &resp)
}

// ListModels returns up to limit authorization models, newest first.
func (c *Client) ListModels(ctx context.Context, store string, limit int) ([]*openfgav1.AuthorizationModel, error) {
	var out []*openfgav1.AuthorizationModel
	token := ""
	for len(out) < limit {
		q := url.Values{"page_size": {"50"}}
		if token != "" {
			q.Set("continuation_token", token)
		}
		var resp openfgav1.ReadAuthorizationModelsResponse
		if err := c.do(ctx, http.MethodGet, storePath(store, "/authorization-models?"+q.Encode()), nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.GetAuthorizationModels()...)
		token = resp.GetContinuationToken()
		if token == "" {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ReadChanges reads one page of a store's change log, oldest first. Pages
// continue from ContinuationToken; StartTime applies to the first page only.
func (c *Client) ReadChanges(ctx context.Context, req *openfgav1.ReadChangesRequest) (*openfgav1.ReadChangesResponse, error) {
	q := url.Values{}
	if req.GetType() != "" {
		q.Set("type", req.GetType())
	}
	if req.GetPageSize() != nil {
		q.Set("page_size", fmt.Sprint(req.GetPageSize().GetValue()))
	}
	if req.GetContinuationToken() != "" {
		q.Set("continuation_token", req.GetContinuationToken())
	} else if req.GetStartTime() != nil {
		q.Set("start_time", req.GetStartTime().AsTime().UTC().Format(time.RFC3339Nano))
	}
	var resp openfgav1.ReadChangesResponse
	return &resp, c.do(ctx, http.MethodGet, storePath(req.GetStoreId(), "/changes?"+q.Encode()), nil, &resp)
}

// LatestModelID returns the newest model ID in a store.
func (c *Client) LatestModelID(ctx context.Context, store string) (string, error) {
	q := url.Values{"page_size": {"1"}}
	var resp openfgav1.ReadAuthorizationModelsResponse
	if err := c.do(ctx, http.MethodGet, storePath(store, "/authorization-models?"+q.Encode()), nil, &resp); err != nil {
		return "", err
	}
	if len(resp.GetAuthorizationModels()) == 0 {
		return "", fmt.Errorf("store %s has no authorization models", store)
	}
	return resp.GetAuthorizationModels()[0].GetId(), nil
}

// Model returns an authorization model. Models are immutable, so results are cached.
func (c *Client) Model(ctx context.Context, store, id string) (*openfgav1.AuthorizationModel, error) {
	key := store + "/" + id
	c.mu.Lock()
	m, ok := c.models[key]
	c.mu.Unlock()
	if ok {
		return m, nil
	}
	var resp openfgav1.ReadAuthorizationModelResponse
	if err := c.do(ctx, http.MethodGet, storePath(store, "/authorization-models/"+url.PathEscape(id)), nil, &resp); err != nil {
		return nil, err
	}
	m = resp.GetAuthorizationModel()
	c.mu.Lock()
	c.models[key] = m
	c.mu.Unlock()
	return m, nil
}

// Read reads tuples.
func (c *Client) Read(ctx context.Context, req *openfgav1.ReadRequest) (*openfgav1.ReadResponse, error) {
	var resp openfgav1.ReadResponse
	return &resp, c.do(ctx, http.MethodPost, storePath(req.GetStoreId(), "/read"), req, &resp)
}

// Check runs a check.
func (c *Client) Check(ctx context.Context, req *openfgav1.CheckRequest) (*openfgav1.CheckResponse, error) {
	var resp openfgav1.CheckResponse
	return &resp, c.do(ctx, http.MethodPost, storePath(req.GetStoreId(), "/check"), req, &resp)
}

// ListObjects lists objects a user has a relation with.
func (c *Client) ListObjects(ctx context.Context, req *openfgav1.ListObjectsRequest) (*openfgav1.ListObjectsResponse, error) {
	var resp openfgav1.ListObjectsResponse
	return &resp, c.do(ctx, http.MethodPost, storePath(req.GetStoreId(), "/list-objects"), req, &resp)
}

// BatchChecker is implemented by clients that support the BatchCheck API
// (OpenFGA v1.8+), which evaluates many checks in one request.
type BatchChecker interface {
	BatchCheck(ctx context.Context, req *openfgav1.BatchCheckRequest) (*openfgav1.BatchCheckResponse, error)
}

// BatchCheck runs several checks in one request.
func (c *Client) BatchCheck(ctx context.Context, req *openfgav1.BatchCheckRequest) (*openfgav1.BatchCheckResponse, error) {
	var resp openfgav1.BatchCheckResponse
	return &resp, c.do(ctx, http.MethodPost, storePath(req.GetStoreId(), "/batch-check"), req, &resp)
}

// ListUsers lists users that have a relation with an object.
func (c *Client) ListUsers(ctx context.Context, req *openfgav1.ListUsersRequest) (*openfgav1.ListUsersResponse, error) {
	var resp openfgav1.ListUsersResponse
	return &resp, c.do(ctx, http.MethodPost, storePath(req.GetStoreId(), "/list-users"), req, &resp)
}

// ReadPage is a convenience wrapper that reads a single page of tuples.
func ReadPage(ctx context.Context, api API, store string, key *openfgav1.ReadRequestTupleKey, pageSize int32, token string, consistency openfgav1.ConsistencyPreference) (*openfgav1.ReadResponse, error) {
	return api.Read(ctx, &openfgav1.ReadRequest{
		StoreId:           store,
		TupleKey:          key,
		PageSize:          wrapperspb.Int32(pageSize),
		ContinuationToken: token,
		Consistency:       consistency,
	})
}
