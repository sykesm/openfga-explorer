// Package web serves the explorer UI: an htmx application whose graph views
// are rendered client-side with Cytoscape.js.
package web

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
	"github.com/sykesm/openfga-explorer/internal/cond"
	"github.com/sykesm/openfga-explorer/internal/config"
	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/ids"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/search"
	"github.com/sykesm/openfga-explorer/internal/trace"
	"github.com/sykesm/openfga-explorer/internal/tuples"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server is the explorer HTTP server.
type Server struct {
	cfg      config.Config
	fga      *fga.Client
	search   search.Searcher
	sources  []tuples.Source // first is the default
	walkAPI  fga.API         // the client behind a short-lived read cache, for check walks
	progress *progressRegistry
	log      *slog.Logger
	tpl      *template.Template
	traceSem chan struct{}
	// sandboxSem bounds concurrent scenario runs (each starts an engine).
	sandboxSem chan struct{}
	// live limits queries over real data per person.
	live   *guard
	expand *guard
	assets map[string]string // static path -> content hash

	mu       sync.Mutex
	models   map[string]*modelEntry
	modelSeq uint64 // counts model lookups, for evictModels
	latest   map[string]latestEntry
}

// modelEntry is an analysed model in the cache (see Server.model). m, ev and
// err are set before ready closes.
type modelEntry struct {
	m     *schema.Model
	ev    *cond.Evaluator
	err   error
	ready chan struct{}
	used  uint64 // s.modelSeq when last asked for; guarded by s.mu
}

type latestEntry struct {
	id string
	at time.Time
}

// New creates a server. searcher may be nil when no datastore is configured.
// sources lists the tuple sources, default first; the API source is added if missing.
func New(cfg config.Config, client *fga.Client, searcher search.Searcher, sources []tuples.Source, log *slog.Logger) (*Server, error) {
	if len(sources) == 0 {
		sources = []tuples.Source{tuples.NewAPI(client)}
	}
	s := &Server{
		cfg: cfg, fga: client, search: searcher, sources: sources, log: log,
		walkAPI:    fga.NewCache(client, walkCacheTTL),
		progress:   newProgressRegistry(),
		traceSem:   make(chan struct{}, max(1, cfg.Trace.MaxConcurrent)),
		sandboxSem: make(chan struct{}, max(1, cfg.Trace.MaxConcurrent)),
		live:       newLiveGuard(),
		expand:     newGuard("graph expansion", "graph expansions", time.Minute, "minute"),
		models:     map[string]*modelEntry{},
		latest:     map[string]latestEntry{},
	}
	assets, err := assetVersions()
	if err != nil {
		return nil, err
	}
	s.assets = assets
	var tpl *template.Template
	tpl, err = template.New("").Funcs(s.funcs()).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s.tpl = tpl
	return s, nil
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", s.cacheStatic(http.FileServerFS(static))))
	// JavaScript modules are served under a version prefix covering all of
	// them, so their relative imports resolve to the same version.
	mux.Handle("GET /static/v/{version}/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := r.PathValue("version")
		if v == s.assets[moduleVersionKey] {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.StripPrefix("/static/v/"+v+"/", http.FileServerFS(static)).ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /s/{store}/{$}", s.handleStoreRoot)
	mux.HandleFunc("GET /s/{store}/m/{model}/{$}", s.handleStoreRoot)
	mux.HandleFunc("GET /s/{store}/m/{model}/explore", s.handleExplore)
	mux.HandleFunc("GET /s/{store}/m/{model}/explore/type", s.handleTypePanel)
	mux.HandleFunc("GET /s/{store}/m/{model}/explore/instance", s.handleInstance)
	mux.HandleFunc("GET /s/{store}/m/{model}/explore/edges", s.handleEdges)
	mux.HandleFunc("GET /s/{store}/m/{model}/explore/listusers", s.handleListUsers)
	mux.HandleFunc("GET /s/{store}/m/{model}/search", s.handleSearch)
	mux.HandleFunc("GET /s/{store}/m/{model}/relations", s.handleRelationOptions)
	mux.HandleFunc("GET /s/{store}/m/{model}/api/schema", s.handleSchemaJSON)
	mux.HandleFunc("GET /s/{store}/m/{model}/api/types", s.handleTypeIndex)
	mux.HandleFunc("GET /s/{store}/m/{model}/api/instance-graph", s.handleInstanceGraph)
	mux.HandleFunc("GET /s/{store}/m/{model}/check", s.handleCheckPage)
	mux.HandleFunc("POST /s/{store}/m/{model}/check/run", s.handleCheckRun)
	mux.HandleFunc("GET /s/{store}/m/{model}/check/progress", s.handleCheckProgress)
	mux.HandleFunc("GET /s/{store}/m/{model}/explore/incoming", s.handleIncoming)
	mux.HandleFunc("GET /s/{store}/m/{model}/explore/access", s.handleAccess)
	mux.HandleFunc("GET /s/{store}/m/{model}/model", s.handleModel)
	mux.HandleFunc("GET /s/{store}/m/{model}/model/diff", s.handleModelDiff)
	mux.HandleFunc("GET /s/{store}/m/{model}/model/impact", s.handleImpact)
	mux.HandleFunc("GET /s/{store}/m/{model}/scenarios", s.handleScenarios)
	mux.HandleFunc("GET /s/{store}/m/{model}/changes", s.handleChanges)
	mux.HandleFunc("POST /s/{store}/m/{model}/scenarios/run", s.handleScenarioRun)
	mux.HandleFunc("POST /s/{store}/m/{model}/scenarios/capture", s.handleScenarioCapture)
	return s.withIdentity(sameSite(securityHeaders(mux)))
}

// cacheStatic lets browsers keep a fingerprinted asset URL (?v=<hash>, see
// assetVersions) forever, but only when the hash is the file's current one:
// an old or made-up hash must not pin stale content. Anything else must be
// revalidated, so a reload always picks up a new build.
func (s *Server) cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.URL.Query().Get("v"); v != "" && v == s.assets[r.URL.Path] {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.ServeHTTP(w, r)
	})
}

// moduleVersionKey is the assets entry holding the version of the JS modules.
const moduleVersionKey = "js/"

// walkCacheTTL is how long check walks reuse a Read page (fga.Cache), so
// stepping through related checks does not re-read the same tuples.
const walkCacheTTL = 30 * time.Second

// assetVersions fingerprints every embedded static file by content hash, and
// the JavaScript modules (static/js) together under moduleVersionKey.
func assetVersions() (map[string]string, error) {
	out := map[string]string{}
	modules := sha256.New()
	err := fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		rel := strings.TrimPrefix(path, "static/")
		out[rel] = hex.EncodeToString(sum[:6])
		if strings.HasPrefix(rel, moduleVersionKey) { // WalkDir is in lexical order
			modules.Write([]byte(rel))
			modules.Write(sum[:])
		}
		return nil
	})
	out[moduleVersionKey] = hex.EncodeToString(modules.Sum(nil)[:6])
	return out, err
}

// assetURL is the fingerprinted URL of a static file.
func (s *Server) assetURL(path string) string {
	if strings.HasPrefix(path, moduleVersionKey) {
		return "/static/v/" + s.assets[moduleVersionKey] + "/" + path
	}
	return "/static/" + path + "?v=" + s.assets[path]
}

// sameSite rejects requests another site makes a browser send: fetches and
// form posts that could start work (traces, scenario runs, live queries) as
// a signed-in person behind an SSO proxy. Same-origin requests (every
// loopback demo), plain navigations (links from tickets), and non-browser
// clients pass. Browsers mark requests with Sec-Fetch-Site; for older ones,
// non-GET requests must carry an Origin matching the host.
func sameSite(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		navigation := (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.Header.Get("Sec-Fetch-Mode") == "navigate"
		switch site := r.Header.Get("Sec-Fetch-Site"); {
		case navigation:
		case site == "cross-site":
			http.Error(w, "cross-site requests are not allowed", http.StatusForbidden)
			return
		case site == "" && r.Method != http.MethodGet && r.Method != http.MethodHead && !originMatches(r):
			http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// originMatches reports whether a request without Sec-Fetch-Site comes
// from this host (or carries no Origin, as non-browser clients don't).
func originMatches(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return strings.EqualFold(u.Host, host)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.ServeHTTP(w, r)
	})
}

// ---- identity -------------------------------------------------------------

// Identity is the requesting user, as asserted by the authenticating proxy.
type Identity struct {
	Name string
	Role string
	// Anonymous is set when the request carried no identity.
	Anonymous bool
}

// Admin reports whether the identity may run queries over real data.
func (id Identity) Admin() bool { return id.Role == "admin" }

type identityKey struct{}

func (s *Server) withIdentity(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Anonymous requests are viewers: only the serve flag below makes them
		// admins, never the config file (default_role is for named identities).
		id := Identity{Name: "local", Role: "viewer", Anonymous: true}
		if hdr := s.cfg.Auth.IdentityHeader; hdr != "" {
			v := strings.TrimSpace(r.Header.Get(hdr))
			switch {
			case v != "":
				id.Name, id.Role, id.Anonymous = v, s.cfg.Auth.DefaultRole, false
			case s.cfg.Auth.RequireIdentity && r.URL.Path != "/healthz":
				http.Error(w, "missing identity: this service must be accessed through the authenticating proxy", http.StatusUnauthorized)
				return
			}
		}
		if role, ok := s.cfg.Auth.Roles[id.Name]; ok && !id.Anonymous {
			id.Role = role
		}
		if id.Anonymous && s.cfg.Auth.DangerouslyTreatAnonymousAsAdmin {
			id.Role = "admin"
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	})
}

func identity(r *http.Request) Identity {
	id, _ := r.Context().Value(identityKey{}).(Identity)
	return id
}

// ---- store/model resolution ----------------------------------------------

// view is the common template data.
type view struct {
	Title      string
	Active     string
	User       Identity
	StoreID    string
	StoreName  string
	ModelParam string // "latest" or a model ID
	ModelID    string
	Model      *schema.Model
	Base       string
	Searcher   string
	Data       any
}

func (s *Server) resolve(r *http.Request) (*view, *modelEntry, error) {
	store, modelParam := r.PathValue("store"), r.PathValue("model")
	if modelParam == "" {
		modelParam = "latest"
	}
	ctx := r.Context()
	modelID := modelParam
	if modelParam == "latest" {
		var err error
		if modelID, err = s.latestModel(ctx, store); err != nil {
			return nil, nil, err
		}
	}
	me, err := s.model(ctx, store, modelID)
	if err != nil {
		return nil, nil, err
	}
	v := &view{
		User: identity(r), StoreID: store, ModelParam: modelParam, ModelID: modelID, Model: me.m,
		Base: "/s/" + url.PathEscape(store) + "/m/" + url.PathEscape(modelParam),
	}
	if s.search != nil {
		v.Searcher = s.search.Name()
	}
	return v, me, nil
}

func (s *Server) latestModel(ctx context.Context, store string) (string, error) {
	s.mu.Lock()
	e, ok := s.latest[store]
	s.mu.Unlock()
	if ok && time.Since(e.at) < 15*time.Second {
		return e.id, nil
	}
	id, err := s.fga.LatestModelID(ctx, store)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.latest[store] = latestEntry{id: id, at: time.Now()}
	s.mu.Unlock()
	return id, nil
}

// maxModels bounds the analysed models kept in memory (most recently used
// first): enough for a few stores and model versions being compared.
const maxModels = 16

// model returns the analysed model (fetched and loaded once, then kept).
// Concurrent first requests share one load.
func (s *Server) model(ctx context.Context, store, id string) (*modelEntry, error) {
	key := store + "/" + id
	s.mu.Lock()
	me, ok := s.models[key]
	if !ok {
		me = &modelEntry{ready: make(chan struct{})}
		s.models[key] = me
	}
	s.modelSeq++
	me.used = s.modelSeq
	s.evictModels()
	s.mu.Unlock()
	if !ok {
		// The load outlives the request that started it: others may be waiting.
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		me.m, me.ev, me.err = s.loadModel(lctx, store, id)
		cancel()
		if me.err != nil {
			s.mu.Lock()
			if s.models[key] == me {
				delete(s.models, key) // let a later request try again
			}
			s.mu.Unlock()
		}
		close(me.ready)
	}
	select {
	case <-me.ready:
		return me, me.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) loadModel(ctx context.Context, store, id string) (*schema.Model, *cond.Evaluator, error) {
	pm, err := s.fga.Model(ctx, store, id)
	if err != nil {
		return nil, nil, err
	}
	m, err := schema.Load(pm)
	if err != nil {
		return nil, nil, err
	}
	return m, cond.New(m.Conditions), nil
}

// evictModels drops the least recently used models beyond maxModels.
// Call with s.mu held.
func (s *Server) evictModels() {
	for len(s.models) > maxModels {
		oldest := ""
		for k, e := range s.models {
			if oldest == "" || e.used < s.models[oldest].used {
				oldest = k
			}
		}
		delete(s.models, oldest)
	}
}

// ---- rendering -------------------------------------------------------------

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	s.renderStatus(w, http.StatusOK, name, data)
}

// renderStatus renders a template into memory first, so a template error
// sends a 500 rather than a page cut off under a success status.
func (s *Server) renderStatus(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render", "template", name, "err", err)
		http.Error(w, "the page could not be rendered", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// fail reports an error. When the caller blames OpenFGA (500 or 502), the
// upstream answer refines it: something the request named that OpenFGA does
// not have (404) or rejects (400) is the visitor's to fix; any other upstream
// refusal (its credentials, rate limits) stays a gateway error.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, err error) {
	s.log.Warn("request failed", "path", r.URL.Path, "err", err)
	var fe *fga.Error
	if errors.As(err, &fe) && (status == http.StatusInternalServerError || status == http.StatusBadGateway) {
		switch {
		case fe.Status == http.StatusNotFound || strings.HasSuffix(fe.Code, "_not_found"): // OpenFGA sends an unknown model as 400
			status = http.StatusNotFound
		case fe.Status == http.StatusBadRequest:
			status = http.StatusBadRequest
		default:
			status = http.StatusBadGateway
		}
	}
	if r.Header.Get("HX-Request") != "" {
		s.renderStatus(w, status, "error-fragment", err.Error())
		return
	}
	s.renderStatus(w, status, "error-page", &view{Title: "Error", User: identity(r), Data: err.Error()})
}

func (s *Server) funcs() template.FuncMap {
	return template.FuncMap{
		"display": s.displayObject,
		"hint":    func(typ string) string { return s.cfg.Types.For(typ).Hint },
		"json": func(v any) (template.JS, error) {
			b, err := json.Marshal(v)
			return template.JS(b), err
		},
		"jsonAttr": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
		"refs":      schema.RenderRefs,
		"kindLabel": func(k schema.Kind) string { return k.Label() },
		"query": func(kv ...string) template.URL {
			q := url.Values{}
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i+1] != "" {
					q.Set(kv[i], kv[i+1])
				}
			}
			return template.URL(q.Encode())
		},
		"split":    strings.Split,
		"join":     strings.Join,
		"contains": slices.Contains[[]string],
		"short":    truncate,
		"dsl":      highlightDSL,
		"add":      func(a, b int) int { return a + b },
		"sub":      func(a, b int) int { return a - b },
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict: odd arguments")
			}
			m := map[string]any{}
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
		"displayID": func(typ, id string) string { return s.rule(typ).DisplayID(id) },
		"pickerText": func(v string) string {
			if i := strings.LastIndexByte(v, '#'); i >= 0 {
				v = v[:i]
			}
			t, id, ok := schema.SplitObject(v)
			if !ok {
				return v
			}
			return s.rule(t).DisplayID(id)
		},
		"graphPatch":  s.graphPatch,
		"deref":       func(b *bool) bool { return b != nil && *b },
		"trimVerdict": trimVerdict,
		"impactShown": func() int { return impactShown },
		// asset returns a fingerprinted static URL so browsers never run stale code.
		"asset": s.assetURL,
		"icon":  trace.Icon,
	}
}

// displayObject renders "type:id[#rel]" using the type's ID display rule.
func (s *Server) displayObject(obj string) string {
	rel := ""
	if i := strings.LastIndexByte(obj, '#'); i >= 0 {
		obj, rel = obj[:i], obj[i:]
	}
	t, id, ok := schema.SplitObject(obj)
	if !ok {
		return obj + rel
	}
	return t + ":" + s.cfg.Types.For(t).DisplayID(id) + rel
}

func (s *Server) rule(typ string) ids.Rule { return s.cfg.Types.For(typ) }

// source returns the named tuple source, or the default.
func (s *Server) source(name string) tuples.Source {
	for _, src := range s.sources {
		if src.Name() == name {
			return src
		}
	}
	return s.sources[0]
}

// ---- pages ----------------------------------------------------------------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if st := s.cfg.OpenFGA.DefaultStore; st != "" && r.URL.Query().Get("all") == "" {
		http.Redirect(w, r, "/s/"+url.PathEscape(st)+"/m/latest/explore", http.StatusFound)
		return
	}
	stores, err := s.fga.ListStores(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, fmt.Errorf("listing stores: %w", err))
		return
	}
	if len(stores) == 1 && r.URL.Query().Get("all") == "" {
		http.Redirect(w, r, "/s/"+url.PathEscape(stores[0].GetId())+"/m/latest/explore", http.StatusFound)
		return
	}
	slices.SortFunc(stores, func(a, b *openfgav1.Store) int { return cmp.Compare(a.GetName(), b.GetName()) })
	s.render(w, "stores", &view{Title: "Stores", User: identity(r), Data: stores})
}

func (s *Server) handleStoreRoot(w http.ResponseWriter, r *http.Request) {
	m := r.PathValue("model")
	if m == "" {
		m = "latest"
	}
	http.Redirect(w, r, "/s/"+url.PathEscape(r.PathValue("store"))+"/m/"+url.PathEscape(m)+"/explore", http.StatusFound)
}

func (s *Server) withStoreName(ctx context.Context, v *view) {
	if st, err := s.fga.GetStore(ctx, v.StoreID); err == nil {
		v.StoreName = st.GetName()
	}
}

type modelPage struct {
	Models []*openfgav1.AuthorizationModel
	// Modular models: the modules, the files rebuilt from module metadata,
	// the fga.mod manifest, and the combined DSL annotated with sources.
	Modules  []schema.Module
	Files    []schema.ModuleFile
	Manifest string
	Combined string
	FilesErr string
	// Findings are the model checks.
	Findings []schema.Finding
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	s.withStoreName(r.Context(), v)
	models, err := s.fga.ListModels(r.Context(), v.StoreID, 50)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	page := modelPage{Models: models, Findings: v.Model.Check()}
	if v.Model.Modular {
		page.Modules = v.Model.Modules()
		if files, manifest, err := v.Model.ModuleFiles(); err != nil {
			page.FilesErr = err.Error()
		} else {
			page.Files, page.Manifest = files, manifest
		}
		if dsl, err := transformer.TransformJSONProtoToDSL(v.Model.Proto, transformer.WithIncludeSourceInformation(true)); err == nil {
			page.Combined = dsl
		}
	}
	v.Title, v.Active, v.Data = "Model", "model", page
	s.render(w, "model", v)
}

// diffPage compares the viewed model (New) with an older version (Base).
type diffPage struct {
	Models  []*openfgav1.AuthorizationModel
	BaseID  string
	Diff    *schema.ModelDiff
	NoOlder bool
	// Live offers "check real people" (admins, live queries enabled).
	Live bool
}

// handleModelDiff compares the viewed model with ?base=<model id>, by
// default the version before it.
func (s *Server) handleModelDiff(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	s.withStoreName(r.Context(), v)
	models, err := s.fga.ListModels(r.Context(), v.StoreID, 50)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	page := &diffPage{Models: models, BaseID: r.URL.Query().Get("base"), Live: s.liveAllowed(r)}
	if page.BaseID == "" {
		// Models are listed newest first: the base is the one after this.
		for i, m := range models {
			if m.GetId() == v.ModelID && i+1 < len(models) {
				page.BaseID = models[i+1].GetId()
			}
		}
	}
	v.Title, v.Active, v.Data = "Model changes", "model", page
	if page.BaseID == "" {
		page.NoOlder = true
		s.render(w, "model-diff", v)
		return
	}
	base, err := s.model(r.Context(), v.StoreID, page.BaseID)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, fmt.Errorf("base model %s: %w", page.BaseID, err))
		return
	}
	page.Diff = schema.Diff(base.m, v.Model)
	s.render(w, "model-diff", v)
}

// handleRelationOptions returns <option>s for the relations of an object's type.
func (s *Server) handleRelationOptions(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	q := r.URL.Query()
	v.Data = relOptions(v.Model, q.Get("object_type"), q.Get("user_type"), q.Get("relation"))
	s.render(w, "relation-options", v)
}

// ---- schema graph ------------------------------------------------------------

// handleTypeIndex lists every type with its relations and module, for
// searching in the browser (the command palette).
func (s *Server) handleTypeIndex(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	out := make([]typeEntry, 0, len(v.Model.TypeNames))
	for _, tn := range v.Model.TypeNames {
		t := v.Model.Types[tn]
		out = append(out, typeEntry{T: tn, R: t.RelationNames, M: t.Source.Module})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=60")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleSchemaJSON(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	var els []any // cyElement[schemaNode], then cyElement[schemaEdge]
	for _, tn := range v.Model.TypeNames {
		t := v.Model.Types[tn]
		k := t.ByKind()
		els = append(els, cyElement[schemaNode]{Group: "nodes", Data: schemaNode{
			ID: tn, Label: tn, Relations: len(t.RelationNames), Module: t.Source.Module,
			Direct: len(k[schema.Direct]), Derived: len(k[schema.Derived]), Hybrid: len(k[schema.Hybrid]),
		}})
	}
	// Merge parallel edges of the same kind so dense models stay legible.
	type key struct{ from, to, kind string }
	var order []key
	labels := map[key][]string{}
	rels := map[key][]string{}
	for _, e := range v.Model.TypeGraph() {
		k := key{e.From, e.To, e.Kind}
		if _, ok := labels[k]; !ok {
			order = append(order, k)
		}
		labels[k] = append(labels[k], e.Label)
		if !slices.Contains(rels[k], e.Relation) {
			rels[k] = append(rels[k], e.Relation)
		}
	}
	for i, k := range order {
		els = append(els, cyElement[schemaEdge]{Group: "edges", Classes: k.kind, Data: schemaEdge{
			ID: "se" + strconv.Itoa(i), Source: k.from, Target: k.to, Kind: k.kind,
			Label: strings.Join(labels[k], "\n"), Count: len(labels[k]), Relations: rels[k],
		}})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Elements []any `json:"elements"`
	}{els})
}

// cyElement is a Cytoscape.js element (a node or an edge) with data D.
type cyElement[D any] struct {
	Group   string `json:"group"`
	Data    D      `json:"data"`
	Classes string `json:"classes,omitempty"`
}

// schemaNode is a type in the model graph (explore.js): its relations, by kind.
type schemaNode struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Relations int    `json:"relations"`
	Module    string `json:"module"`
	Direct    int    `json:"direct"`
	Derived   int    `json:"derived"`
	Hybrid    int    `json:"hybrid"`
}

// schemaEdge is the model graph's edges of one kind between two types,
// merged: Count of them, their labels one per line, and their Relations.
type schemaEdge struct {
	ID        string   `json:"id"`
	Source    string   `json:"source"`
	Target    string   `json:"target"`
	Kind      string   `json:"kind"`
	Label     string   `json:"label"`
	Count     int      `json:"count"`
	Relations []string `json:"relations"`
}

// trimVerdict drops the "Allowed: " prefix where the verdict is already
// shown, capitalizing the sentence only when it starts with a plain word:
// a leading object or user ID (e.g. "user:eng-002") is case-sensitive.
func trimVerdict(h string) string {
	for _, p := range []string{"Allowed: ", "Denied: ", "Undecided: "} {
		h = strings.TrimPrefix(h, p)
	}
	first, _, _ := strings.Cut(h, " ")
	if h != "" && !strings.ContainsAny(first, ":#") {
		h = strings.ToUpper(h[:1]) + h[1:]
	}
	return h
}

// slotWait is how long a request waits for a trace or sandbox slot before
// it is turned away as too busy.
const slotWait = 5 * time.Second

// acquire takes a slot of sem (traceSem, sandboxSem), waiting up to slotWait,
// or less if the request is canceled. ok is false when no slot came free;
// otherwise call release when done.
func acquire(ctx context.Context, sem chan struct{}) (release func(), ok bool) {
	t := time.NewTimer(slotWait)
	defer t.Stop()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, true
	case <-t.C:
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
}
