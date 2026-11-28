// Command fgaexplorer explores OpenFGA authorization graphs and explains
// check decisions.
//
//	fgaexplorer serve [--config explorer.yaml] [--listen 127.0.0.1:8090]
//	fgaexplorer trace --store ID --user user:anne --relation viewer --object document:1 [--context '{...}']
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sykesm/openfga-explorer/internal/config"
	"github.com/sykesm/openfga-explorer/internal/datastore"
	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/search"
	"github.com/sykesm/openfga-explorer/internal/trace"
	"github.com/sykesm/openfga-explorer/internal/tuples"
	"github.com/sykesm/openfga-explorer/internal/web"
	"google.golang.org/protobuf/types/known/structpb"
)

func main() {
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "serve" || args[0] == "trace") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "trace":
		err = runTrace(args, os.Stdout)
	default:
		err = runServe(args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loadConfig(fs *flag.FlagSet, args []string) (config.Config, error) {
	path := fs.String("config", os.Getenv("FGAX_CONFIG"), "YAML config file")
	listen := fs.String("listen", "", "address to serve on (overrides config; default 127.0.0.1:8090, this machine only). "+
		"One interface: 192.0.2.10:8090; every interface: 0.0.0.0:8090, [::]:8090, or :8090")
	apiURL := fs.String("api-url", "", "OpenFGA API URL (overrides config and OPENFGA_API_URL)")
	dsURI := fs.String("datastore-uri", "", "OpenFGA Postgres URI for instance search (overrides config and FGAX_DATASTORE_URI)")
	if err := fs.Parse(args); err != nil {
		return config.Config{}, err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return cfg, err
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *apiURL != "" {
		cfg.OpenFGA.APIURL = *apiURL
	}
	if *dsURI != "" {
		cfg.Datastore.URI = *dsURI
	}
	return cfg, nil
}

func newClient(cfg config.Config) (*fga.Client, error) {
	return fga.New(fga.Options{
		APIURL: cfg.OpenFGA.APIURL, APIToken: cfg.OpenFGA.APIToken,
		ClientID: cfg.OpenFGA.ClientID, ClientSecret: cfg.OpenFGA.ClientSecret,
		TokenURL: cfg.OpenFGA.TokenURL, Audience: cfg.OpenFGA.Audience,
	})
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	anonAdmin := fs.Bool("dangerously-treat-anonymous-as-admin", false,
		"treat requests without an identity header as admins (for deployments without authentication; admins can run queries over real data)")
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *anonAdmin {
		cfg.Auth.DangerouslyTreatAnonymousAsAdmin = true
		log.Warn("requests without an identity are treated as admins (--dangerously-treat-anonymous-as-admin)")
	}
	client, err := newClient(cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The datastore connection is optional. Without it, instance search takes
	// exact IDs and edges are read through the API.
	var searcher search.Searcher
	sources := []tuples.Source{tuples.NewAPI(client)}
	if cfg.Datastore.URI != "" {
		db, err := datastore.Open(ctx, cfg.Datastore.URI)
		if err != nil {
			return fmt.Errorf("datastore: %w", err)
		}
		defer db.Close()
		searcher = search.NewPostgres(db)
		ds := tuples.NewDatastore(db)
		if cfg.Tuples.Source == "api" {
			sources = append(sources, ds)
		} else {
			sources = append([]tuples.Source{ds}, sources...)
		}
		log.Info("datastore connected", "collation", db.Collation, "instance_search", "prefix", "default_tuple_source", sources[0].Name())
	} else {
		log.Info("no datastore configured: instance search takes exact IDs; edges are read through the API")
	}
	srv, err := web.New(cfg, client, searcher, sources, log)
	if err != nil {
		return err
	}
	hs := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	log.Info("listening", "addr", cfg.Listen, "openfga", cfg.OpenFGA.APIURL)
	if !loopback(cfg.Listen) {
		log.Warn("reachable from other machines: the explorer has no authentication of its own; put it behind an authenticating proxy (auth.identity_header) or listen on 127.0.0.1", "addr", cfg.Listen)
	}
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// runTrace explains one check on the command line, writing to out.
func runTrace(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	store := fs.String("store", os.Getenv("OPENFGA_STORE_ID"), "store ID")
	model := fs.String("model", "", "authorization model ID (default: latest)")
	user := fs.String("user", "", "user, e.g. user:anne or group:eng#member")
	relation := fs.String("relation", "", "relation")
	object := fs.String("object", "", "object, e.g. document:roadmap")
	ctxJSON := fs.String("context", "", "request context as a JSON object")
	allPaths := fs.Bool("all-paths", false, "evaluate every path instead of short-circuiting (same as --order all)")
	order := fs.String("order", "guided", "evaluation order: guided (shortest explanation), model (model order), all (every path)")
	maxCalls := fs.Int("max-calls", 0, "call budget (default from config)")
	asJSON := fs.Bool("json", false, "print the trace graph as JSON")
	reading := fs.String("explain", "up", "how to read the explanation: up (user to object) or down (object to user)")
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	if *store == "" || *user == "" || *relation == "" || *object == "" {
		fs.Usage()
		return errors.New("--store, --user, --relation, and --object are required")
	}
	switch *order {
	case trace.OrderGuided, trace.OrderModel, trace.OrderAll:
	default:
		return fmt.Errorf("--order must be guided, model, or all; got %q", *order)
	}
	client, err := newClient(cfg)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if *model == "" {
		if *model, err = client.LatestModelID(ctx, *store); err != nil {
			return err
		}
	}
	pm, err := client.Model(ctx, *store, *model)
	if err != nil {
		return err
	}
	m, err := schema.Load(pm)
	if err != nil {
		return err
	}
	req := trace.Request{Store: *store, Model: *model, User: *user, Relation: *relation, Object: *object}
	if *ctxJSON != "" {
		var mm map[string]any
		if err := json.Unmarshal([]byte(*ctxJSON), &mm); err != nil {
			return fmt.Errorf("--context: %w", err)
		}
		if req.Context, err = structpb.NewStruct(mm); err != nil {
			return err
		}
	}
	opt := trace.DefaultOptions()
	opt.MaxCalls, opt.MaxFanout, opt.ProbeLimit, opt.Timeout = cfg.Trace.MaxCalls, cfg.Trace.MaxFanout, cfg.Trace.ProbeLimit, cfg.Trace.Timeout
	if *maxCalls > 0 {
		opt.MaxCalls = *maxCalls
	}
	opt.SetOrder(*order)
	if *allPaths {
		opt.AllPaths = true
	}
	tr, err := trace.Explain(ctx, client, m, nil, req, opt)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(tr.Graph())
	}
	fmt.Fprint(out, tr.Text())
	ex := tr.Explanation(nil)
	fmt.Fprintln(out)
	fmt.Fprintln(out, ex.Headline)
	if *reading == "down" {
		fmt.Fprint(out, ex.FormatDown())
	} else {
		fmt.Fprint(out, ex.Format())
	}
	fmt.Fprintf(out, "\n%s (walker: %s) · %d tuples read · %d walk + %d verify calls · %v", tr.Decision(), tr.Root.Outcome, tr.TuplesRead, tr.Calls, tr.VerifyCalls, tr.Duration.Round(time.Millisecond))
	if tr.Mismatches > 0 {
		fmt.Fprintf(out, " · %d MISMATCHES", tr.Mismatches)
	}
	if tr.Exhausted {
		fmt.Fprint(out, " · budget exhausted")
	}
	fmt.Fprintln(out)
	if gaps := tr.Gaps(); len(gaps) > 0 {
		fmt.Fprintln(out, "not exhaustive — granting paths may have been missed:")
		for _, g := range gaps {
			fmt.Fprintln(out, "  -", g)
		}
	} else if opt.AllPaths {
		fmt.Fprintln(out, "exhaustive: every branch was examined")
	}
	return nil
}

// loopback reports whether a listen address only accepts local connections.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
