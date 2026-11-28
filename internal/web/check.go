package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/config"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

type checkForm struct {
	User, Relation, Object string
	Context                string
	Contextual             string
	AllPaths               bool
	Order                  string // trace.OrderGuided, OrderModel or OrderAll
	Verify                 string
	Consistency            string
	MaxCalls               int
	MaxFanout, ProbeLimit  int
	UserPick, ObjectPick   pickerData
	Relations              relationOptions
	AutoRun                bool
}

// userTypes lists the picker options for check users: the types and usersets
// that can appear as the user of a tuple somewhere in the model. Types that
// never appear as a user (e.g. document) cannot hold any relation.
func userTypes(m *schema.Model, rules func(string) string) []pickerType {
	var out []pickerType
	for _, u := range m.UserTypes() {
		t, _, userset := strings.Cut(u, "#")
		label := u
		if userset {
			label += " (userset)"
		}
		pt := pickerType{Value: u, Label: label, Hint: rules(t)}
		if ty := m.Types[t]; ty != nil && !strings.Contains(u, "#") {
			pt.Rels = strings.Join(ty.RelationNames, " ")
		}
		out = append(out, pt)
	}
	return out
}

// relationOptions is the data for the relation <select>: every relation of
// the object type, with those the user type can never reach disabled.
type relationOptions struct {
	Type     *schema.Type
	Selected string
	UserType string
	Reach    map[string]bool // nil when no user type is known: everything enabled
	None     bool            // no relation is reachable
}

func (o relationOptions) Enabled(rel string) bool { return o.Reach == nil || o.Reach[rel] }

func relOptions(m *schema.Model, objType, userType, selected string) relationOptions {
	o := relationOptions{Type: m.Types[objType], Selected: selected, UserType: userType}
	if o.Type == nil || userType == "" {
		return o
	}
	o.Reach = m.Reachable(objType, userType)
	o.None = true
	for _, ok := range o.Reach {
		if ok {
			o.None = false
		}
	}
	// Keep the selection only if it is still evaluable; the browser then
	// selects the first enabled option.
	if !o.Reach[selected] {
		o.Selected = ""
	}
	return o
}

// orderParam reads the evaluation order, accepting the older all_paths flag.
func orderParam(order, allPaths string) string {
	switch {
	case order == trace.OrderModel || order == trace.OrderAll:
		return order
	case order == "" && allPaths != "":
		return trace.OrderAll
	}
	return trace.OrderGuided
}

func (s *Server) handleCheckPage(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	s.withStoreName(r.Context(), v)
	q := r.URL.Query()
	f := &checkForm{
		User: q.Get("user"), Relation: q.Get("relation"), Object: q.Get("object"),
		Context: q.Get("context"), Contextual: q.Get("contextual"),
		AllPaths: q.Get("all_paths") != "", Verify: q.Get("verify"), Consistency: q.Get("consistency"),
		Order: orderParam(q.Get("order"), q.Get("all_paths")),
	}
	if f.Verify == "" {
		f.Verify = "all"
	}
	f.MaxCalls, _ = strconv.Atoi(q.Get("max_calls"))
	if f.MaxCalls <= 0 {
		f.MaxCalls = s.cfg.Trace.MaxCalls
	}
	f.MaxFanout, _ = strconv.Atoi(q.Get("max_fanout"))
	if f.MaxFanout <= 0 {
		f.MaxFanout = s.cfg.Trace.MaxFanout
	}
	f.ProbeLimit, _ = strconv.Atoi(q.Get("probe_limit"))
	if f.ProbeLimit <= 0 {
		f.ProbeLimit = s.cfg.Trace.ProbeLimit
	}
	hint := func(t string) string { return s.rule(t).Hint }
	f.UserPick = pickerData{Field: "user", Types: userTypes(v.Model, hint), Mode: "form", Required: true}
	var objTypes []pickerType
	for _, tn := range v.Model.TypeNames {
		if len(v.Model.Types[tn].RelationNames) > 0 {
			objTypes = append(objTypes, pickerType{Value: tn, Label: tn, Hint: hint(tn), Rels: strings.Join(v.Model.Types[tn].RelationNames, " ")})
		}
	}
	f.ObjectPick = pickerData{Field: "object", Types: objTypes, Mode: "form", Required: true}
	if subj, err := schema.ParseSubject(f.User); err == nil {
		f.UserPick.Value = f.User
		f.UserPick.Selected = subj.Type
		if subj.Relation != "" {
			f.UserPick.Selected += "#" + subj.Relation
		}
	}
	if f.UserPick.Selected == "" {
		// Default to the conventional end-user type rather than the first type alphabetically.
		for _, pt := range f.UserPick.Types {
			if pt.Value == "user" {
				f.UserPick.Selected = "user"
			}
		}
		if f.UserPick.Selected == "" && len(f.UserPick.Types) > 0 {
			f.UserPick.Selected = f.UserPick.Types[0].Value
		}
	}
	objType := ""
	if t, _, ok := schema.SplitObject(f.Object); ok {
		f.ObjectPick.Value, f.ObjectPick.Selected = f.Object, t
		objType = t
	} else if len(objTypes) > 0 {
		objType = objTypes[0].Value
	}
	f.Relations = relOptions(v.Model, objType, f.UserPick.Selected, f.Relation)
	f.AutoRun = f.User != "" && f.Relation != "" && f.Object != ""
	v.Title, v.Active, v.Data = "Check", "check", f
	s.render(w, "check", v)
}

type checkResult struct {
	// Order is how the walk chose branches (trace.OrderGuided, OrderModel, OrderAll).
	Order string
	// CacheTTL is how long the walk's read cache keeps a page (walkCacheTTL).
	CacheTTL time.Duration
	Trace    *trace.Trace
	Lines    []trace.Line
	Graph    *trace.Graph
	Decision trace.Outcome
	Walker   trace.Outcome
	Link     string
	// Context and Contextual echo the request for "trace from here" links.
	Context, Contextual string
	// Grants counts the granting tuples reachable from the root along allowed links.
	Grants int
	// Gaps explains where the walk was cut short; empty means exhaustive.
	Gaps []string
	// Holders are the user types that can hold the relation (for "who else has this?");
	// UserType is the checked user's type, preselected.
	Holders  []string
	UserType string
	// Explanation is the plain-language summary; Text the copyable trace.
	Explanation trace.Explanation
	Text        string
	// Why is the explanation's steps, their evidence tied to the model.
	Why []whyStep
	// WhyDown is the same steps read top-down, from the object to the user.
	WhyDown []whyStep
	// WhyFlat is the top-down reading as a numbered list.
	WhyFlat []whyStep
	// Summary is the one-line form summary; Done the player's closing status.
	Summary string
	Done    doneText
}

// ---- live progress ---------------------------------------------------------

type runState struct {
	mu      sync.Mutex
	calls   int
	current string
	// started is set when a run (not only a progress stream) uses the entry.
	started bool
	done    chan struct{}
	once    sync.Once // closes done
}

func (r *runState) update(p trace.Progress) {
	r.mu.Lock()
	r.calls, r.current = p.Calls, p.Current
	r.mu.Unlock()
}

// progressRegistry pairs a check run with the progress stream the browser
// opens for it (by a random run ID). Either may arrive first. Entries are
// removed a minute after their run finishes (so a late stream still sees
// "done"), or when a stream whose run never started goes away.
type progressRegistry struct {
	mu   sync.Mutex
	runs map[string]*runState
}

func newProgressRegistry() *progressRegistry { return &progressRegistry{runs: map[string]*runState{}} }

func (p *progressRegistry) entry(id string) *runState {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.runs[id]
	if !ok {
		st = &runState{done: make(chan struct{})}
		p.runs[id] = st
	}
	return st
}

// start registers a run under id.
func (p *progressRegistry) start(id string) *runState {
	st := p.entry(id)
	st.mu.Lock()
	st.started = true
	st.mu.Unlock()
	return st
}

// watch returns the entry a progress stream follows; the stream may connect
// before its run starts. Call unwatch when the stream ends.
func (p *progressRegistry) watch(id string) *runState { return p.entry(id) }

// unwatch drops the entry of a stream whose run never started, so streams
// for IDs that no run uses don't accumulate.
func (p *progressRegistry) unwatch(id string, st *runState) {
	st.mu.Lock()
	started := st.started
	st.mu.Unlock()
	if started {
		return
	}
	p.mu.Lock()
	if p.runs[id] == st {
		delete(p.runs, id)
	}
	p.mu.Unlock()
}

// finish marks the run under id done (once, however often it is called) and
// removes its entry a minute later.
func (p *progressRegistry) finish(id string) {
	p.mu.Lock()
	st := p.runs[id]
	p.mu.Unlock()
	if st == nil {
		return
	}
	st.once.Do(func() { close(st.done) })
	time.AfterFunc(time.Minute, func() {
		p.mu.Lock()
		if p.runs[id] == st {
			delete(p.runs, id)
		}
		p.mu.Unlock()
	})
}

func validRunID(id string) bool {
	if len(id) < 8 || len(id) > 40 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// handleCheckProgress streams a walk's progress as server-sent events.
func (s *Server) handleCheckProgress(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !validRunID(id) {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	st := s.progress.watch(id)
	defer s.progress.unwatch(id, st)
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	limit := time.After(3 * time.Minute)
	send := func(event string) {
		st.mu.Lock()
		b, _ := json.Marshal(map[string]any{"calls": st.calls, "current": s.displayObject(st.current)})
		st.mu.Unlock()
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		fl.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-limit:
			return
		case <-st.done:
			send("done")
			return
		case <-tick.C:
			send("progress")
		}
	}
}

func (s *Server) handleCheckRun(w http.ResponseWriter, r *http.Request) {
	v, me, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, err)
		return
	}
	run, err := s.parseCheckRun(r.PostForm, v.StoreID, v.ModelID)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err)
		return
	}

	release, ok := acquire(r.Context(), s.traceSem)
	if !ok {
		s.fail(w, r, http.StatusTooManyRequests, errors.New("too many traces are running; try again shortly"))
		return
	}
	defer release()
	// Live progress for the browser (see handleCheckProgress); the walk stops
	// if the browser cancels the request.
	if id := r.PostForm.Get("run_id"); validRunID(id) {
		st := s.progress.start(id)
		defer s.progress.finish(id)
		run.opt.Progress = st.update
	}
	req := run.req
	tr, err := trace.Walk(r.Context(), s.walkAPI, me.m, me.ev, req, run.opt)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err)
		return
	}
	if r.Context().Err() != nil {
		return // canceled by the browser
	}
	s.log.Info("trace", "identity", v.User.Name, "store", v.StoreID, "user", req.User, "relation", req.Relation, "object", req.Object,
		"decision", tr.Decision(), "calls", tr.Calls, "tuples_read", tr.TuplesRead, "tuples_cached", tr.TuplesCached, "verify_calls", tr.VerifyCalls, "mismatches", tr.Mismatches, "duration", tr.Duration)

	res := s.checkResult(tr, run, me.m, v.Base)
	w.Header().Set("HX-Push-Url", res.Link)
	v.Data = res
	s.render(w, "check-result", v)
}

// checkRun is a parsed check form: the walk to run, and what the result
// echoes back (the context as typed, and the form's settings for the link).
type checkRun struct {
	req trace.Request
	opt trace.Options
	// order is the normalized walk order (orderParam).
	order string
	// context and contextual are the request's context and contextual tuples
	// as typed.
	context, contextual string
	// settings are the form's raw walk settings, for the result's link.
	settings url.Values
}

// checkSettings are the form fields that tune a walk without changing the
// question; the result's link carries them so a shared link reruns the same
// walk.
var checkSettings = []string{"verify", "consistency", "max_calls", "max_fanout", "probe_limit"}

// parseCheckRun reads the check form. Every error is the form's fault.
func (s *Server) parseCheckRun(form url.Values, store, model string) (checkRun, error) {
	run := checkRun{
		req: trace.Request{
			Store: store, Model: model,
			User: s.pickedID(form, "user"), Relation: form.Get("relation"), Object: s.pickedID(form, "object"),
		},
		context:    strings.TrimSpace(form.Get("context")),
		contextual: form.Get("contextual"),
		order:      orderParam(form.Get("order"), form.Get("all_paths")),
		settings:   url.Values{},
	}
	if run.req.User == "" || run.req.Relation == "" || run.req.Object == "" {
		return run, errors.New("user, relation, and object are required")
	}
	var err error
	if run.req.Context, err = trace.ParseContext(run.context); err != nil {
		return run, err
	}
	if run.req.Contextual, err = parseContextual(run.contextual); err != nil {
		return run, err
	}
	for _, k := range checkSettings {
		run.settings.Set(k, form.Get(k))
	}
	if form.Get("consistency") == "higher" {
		run.req.Consistency = openfgav1.ConsistencyPreference_HIGHER_CONSISTENCY
	}

	run.opt = s.cfg.Trace.Options()
	limit := func(field string, dst *int, most int) {
		if n, err := strconv.Atoi(form.Get(field)); err == nil && n > 0 {
			*dst = min(n, most)
		}
	}
	limit("max_calls", &run.opt.MaxCalls, config.MaxTraceCalls)
	limit("max_fanout", &run.opt.MaxFanout, config.MaxTraceFanout)
	limit("probe_limit", &run.opt.ProbeLimit, config.MaxTraceProbes)
	run.opt.SetOrder(run.order)
	switch form.Get("verify") {
	case "none":
		run.opt.Verify = trace.VerifyNone
	case "root":
		run.opt.Verify = trace.VerifyRoot
	default:
		run.opt.Verify = trace.VerifyAll
	}
	return run, nil
}

// pickedID reads a picker field: the full ID when one was chosen, otherwise
// the selected type (or userset) and the typed ID, normalized for the type.
func (s *Server) pickedID(form url.Values, field string) string {
	if val := strings.TrimSpace(form.Get(field)); val != "" {
		return val
	}
	sel, typed := form.Get(field+"_type"), strings.TrimSpace(form.Get(field+"_q"))
	if sel == "" || typed == "" {
		return ""
	}
	typ, suffix := sel, ""
	if i := strings.IndexByte(sel, '#'); i >= 0 {
		typ, suffix = sel[:i], sel[i:]
	}
	typed = strings.TrimPrefix(typed, typ+":")
	return typ + ":" + s.rule(typ).Normalize(typed) + suffix
}

// link is the check page URL that reruns this check with the same settings.
func (run checkRun) link(base string) string {
	q := map[string]string{
		"user": run.req.User, "relation": run.req.Relation, "object": run.req.Object,
		"context": run.context, "contextual": run.contextual, "order": run.order,
	}
	for k := range run.settings {
		q[k] = run.settings.Get(k)
	}
	return base + "/check?" + queryOf(q)
}

// checkResult assembles what the result template shows from a finished walk.
func (s *Server) checkResult(tr *trace.Trace, run checkRun, m *schema.Model, base string) *checkResult {
	req := run.req
	graph := tr.Graph()
	links := traceLinks{base: base, user: req.User, context: run.context, contextual: run.contextual}
	addNodeDetails(tr, graph, links, m)
	gaps := tr.Gaps()
	ex := tr.Explanation(s.displayObject)
	res := &checkResult{
		Order:    run.order,
		CacheTTL: walkCacheTTL,
		Trace:    tr,
		Lines:    tr.Lines(),
		Graph:    graph,
		Decision: tr.Decision(),
		Walker:   tr.Root.Outcome,
		Link:     run.link(base),

		Context:    run.context,
		Contextual: run.contextual,
		Grants:     tr.GrantingTuples(),
		Gaps:       gaps,

		Explanation: ex,
		Text:        tr.Text(),
		Why:         whySteps(ex.Steps, m, links),
		WhyDown:     whyDown(ex.Down(), m, links),
		WhyFlat:     whyFlat(ex.DownFlat(), m, links),

		Summary: checkSummary(req.User, req.Relation, req.Object, run.context),
		Done:    doneTexts(tr.Decision(), run.order, len(gaps) == 0),
	}
	if subj, err := schema.ParseSubject(req.User); err == nil {
		res.UserType = subj.Type
		if subj.Relation != "" {
			res.UserType += "#" + subj.Relation
		}
		if typ, _, ok := schema.SplitObject(req.Object); ok {
			if rel, ok := m.Relation(typ, req.Relation); ok {
				res.Holders = m.SubjectTypes(rel)
			}
		}
	}
	return res
}

// queryOf encodes the non-empty values as a query string (keys sorted, as
// url.Values.Encode does), so equal checks get equal links.
func queryOf(m map[string]string) string {
	q := url.Values{}
	for k, v := range m {
		if v != "" {
			q.Set(k, v)
		}
	}
	return q.Encode()
}

// parseContextual accepts a JSON array of tuple keys, or one tuple per line:
//
//	user:anne viewer document:roadmap
//	user:anne viewer document:roadmap non_expired_grant {"grant_time":"2026-01-01T00:00:00Z","grant_duration":"1h"}
func parseContextual(text string) ([]*openfgav1.TupleKey, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	var out []*openfgav1.TupleKey
	if strings.HasPrefix(text, "[") {
		var raw []json.RawMessage
		if err := json.Unmarshal([]byte(text), &raw); err != nil {
			return nil, fmt.Errorf("contextual tuples: %w", err)
		}
		for i, r := range raw {
			var k openfgav1.TupleKey
			if err := protojson.Unmarshal(r, &k); err != nil {
				return nil, fmt.Errorf("contextual tuple %d: %w", i+1, err)
			}
			out = append(out, &k)
		}
		return out, nil
	}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields, rest := cutFields(line, 4)
		if len(fields) < 3 {
			return nil, fmt.Errorf("contextual tuple line %d: expected \"user relation object [condition {context}]\"", i+1)
		}
		k := &openfgav1.TupleKey{User: fields[0], Relation: fields[1], Object: fields[2]}
		if len(fields) >= 4 {
			k.Condition = &openfgav1.RelationshipCondition{Name: fields[3]}
			if rest != "" {
				var m map[string]any
				if err := json.Unmarshal([]byte(rest), &m); err != nil {
					return nil, fmt.Errorf("contextual tuple line %d: condition context: %w", i+1, err)
				}
				st, err := structpb.NewStruct(m)
				if err != nil {
					return nil, err
				}
				k.Condition.Context = st
			}
		}
		out = append(out, k)
	}
	return out, nil
}

// cutFields splits up to n fields off s, separated by any run of white
// space (spaces or tabs), and returns what follows them, trimmed (a
// condition's JSON context, which may itself contain spaces).
func cutFields(s string, n int) (fields []string, rest string) {
	rest = strings.TrimSpace(s)
	for len(fields) < n && rest != "" {
		i := strings.IndexFunc(rest, unicode.IsSpace)
		if i < 0 {
			return append(fields, rest), ""
		}
		fields, rest = append(fields, rest[:i]), strings.TrimLeftFunc(rest[i:], unicode.IsSpace)
	}
	return fields, rest
}
