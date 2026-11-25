// Package fgatest runs a real OpenFGA server in-process, with an in-memory
// datastore, for tests that need the server's own Check semantics without
// Docker. Requests go through the same HTTP gateway, gRPC request validation,
// and error encoding as a deployed server.
package fgatest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	grpc_runtime "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
	httpmiddleware "github.com/openfga/openfga/pkg/middleware/http"
	"github.com/openfga/openfga/pkg/middleware/validator"
	"github.com/openfga/openfga/pkg/server"
	serverErrors "github.com/openfga/openfga/pkg/server/errors"
	"github.com/openfga/openfga/pkg/storage/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
)

// Server is a running in-process OpenFGA.
type Server struct {
	// URL is the HTTP API base URL (as for --api-url).
	URL    string
	Client openfgav1.OpenFGAServiceClient
	http   *httptest.Server
	stop   func()
}

// Store is a store loaded with a model and tuples.
type Store struct {
	*Server
	ID, Model string
}

// Start runs a new server; it stops when the test ends.
func Start(t testing.TB) *Server {
	t.Helper()
	s, err := start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func start() (*Server, error) {
	srv, err := server.NewServerWithOpts(server.WithDatastore(memory.New(memory.WithMaxTypesPerAuthorizationModel(1000))))
	if err != nil {
		return nil, err
	}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.ChainUnaryInterceptor(validator.UnaryServerInterceptor()))
	openfgav1.RegisterOpenFGAServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	// The same gateway options as `openfga run`, so HTTP errors look the same.
	mux := grpc_runtime.NewServeMux(
		grpc_runtime.WithForwardResponseOption(httpmiddleware.HTTPResponseModifier),
		grpc_runtime.WithErrorHandler(func(c context.Context, _ *grpc_runtime.ServeMux, _ grpc_runtime.Marshaler, w http.ResponseWriter, r *http.Request, e error) {
			code := serverErrors.ConvertToEncodedErrorCode(status.Convert(e))
			httpmiddleware.CustomHTTPErrorHandler(c, w, r, serverErrors.NewEncodedError(code, e.Error()))
		}),
	)
	client := openfgav1.NewOpenFGAServiceClient(conn)
	if err := openfgav1.RegisterOpenFGAServiceHandlerClient(context.Background(), mux, client); err != nil {
		return nil, err
	}
	hs := httptest.NewServer(mux)
	return &Server{URL: hs.URL, Client: client, http: hs, stop: func() {
		hs.Close()
		_ = conn.Close()
		gs.Stop()
		srv.Close()
	}}, nil
}

// Close stops the server.
func (s *Server) Close() { s.stop() }

// CreateStore creates a store with the model (DSL) and tuples.
func (s *Server) CreateStore(ctx context.Context, name, dsl string, tuples []*openfgav1.TupleKey) (*Store, error) {
	model, err := transformer.TransformDSLToProto(dsl)
	if err != nil {
		return nil, fmt.Errorf("model: %w", err)
	}
	return s.writeStore(ctx, name, model, tuples)
}

func (s *Server) writeStore(ctx context.Context, name string, model *openfgav1.AuthorizationModel, tuples []*openfgav1.TupleKey) (*Store, error) {
	st, err := s.Client.CreateStore(ctx, &openfgav1.CreateStoreRequest{Name: name})
	if err != nil {
		return nil, err
	}
	wm, err := s.Client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
		StoreId: st.GetId(), SchemaVersion: model.GetSchemaVersion(),
		TypeDefinitions: model.GetTypeDefinitions(), Conditions: model.GetConditions(),
	})
	if err != nil {
		return nil, fmt.Errorf("model: %w", err)
	}
	for i := 0; i < len(tuples); i += 100 {
		batch := tuples[i:min(i+100, len(tuples))]
		if _, err := s.Client.Write(ctx, &openfgav1.WriteRequest{
			StoreId: st.GetId(), AuthorizationModelId: wm.GetAuthorizationModelId(),
			Writes: &openfgav1.WriteRequestWrites{TupleKeys: batch},
		}); err != nil {
			return nil, fmt.Errorf("tuples %d-%d: %w", i, i+len(batch), err)
		}
	}
	return &Store{Server: s, ID: st.GetId(), Model: wm.GetAuthorizationModelId()}, nil
}

// CreateModularStore creates a store from a modular model directory (fga.mod
// and its module files), as `fga model write --file fga.mod` does.
func (s *Server) CreateModularStore(ctx context.Context, name, dir string, tuples []*openfgav1.TupleKey) (*Store, error) {
	b, err := os.ReadFile(filepath.Join(dir, "fga.mod"))
	if err != nil {
		return nil, err
	}
	mod, err := transformer.TransformModFile(string(b))
	if err != nil {
		return nil, err
	}
	var files []transformer.ModuleFile
	for _, f := range mod.Contents.Value {
		c, err := os.ReadFile(filepath.Join(dir, f.Value))
		if err != nil {
			return nil, err
		}
		files = append(files, transformer.ModuleFile{Name: f.Value, Contents: string(c)})
	}
	model, err := transformer.TransformModuleFilesToModel(files, mod.Schema.Value)
	if err != nil {
		return nil, err
	}
	return s.writeStore(ctx, name, model, tuples)
}

var (
	seedOnce  sync.Once
	seedStore *Store
	seedErr   error
)

// Seed returns the sample store from deploy/seed (model.fga, tuples.json and
// collation-tuples.json), loaded once per test binary and shared: tests must
// not write to it.
func Seed(t testing.TB) *Store {
	t.Helper()
	seedOnce.Do(func() {
		var s *Server
		if s, seedErr = start(); seedErr != nil {
			return
		}
		dir := SeedDir()
		var dsl []byte
		if dsl, seedErr = os.ReadFile(filepath.Join(dir, "model.fga")); seedErr != nil {
			return
		}
		var tuples []*openfgav1.TupleKey
		for _, f := range []string{"tuples.json", "collation-tuples.json"} {
			var ts []*openfgav1.TupleKey
			if ts, seedErr = ReadTuples(filepath.Join(dir, f)); seedErr != nil {
				return
			}
			tuples = append(tuples, ts...)
		}
		seedStore, seedErr = s.CreateStore(context.Background(), "explorer-demo", string(dsl), tuples)
	})
	if seedErr != nil {
		t.Fatalf("seed store: %v", seedErr)
	}
	return seedStore
}

// SeedDir is deploy/seed in this repository.
func SeedDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "deploy", "seed")
}

// ReadTuples reads tuples in the `fga tuple write` JSON format.
func ReadTuples(path string) ([]*openfgav1.TupleKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make([]*openfgav1.TupleKey, 0, len(raw))
	for i, r := range raw {
		k := &openfgav1.TupleKey{}
		if err := protojson.Unmarshal(r, k); err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", path, i, err)
		}
		out = append(out, k)
	}
	return out, nil
}
