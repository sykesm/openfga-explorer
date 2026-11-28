// Package trace explains OpenFGA check decisions. It re-evaluates a check by
// walking the authorization model's rewrite rules, reading the tuples behind
// each edge through the OpenFGA API, and evaluating conditions locally. Every
// relation-level step can be verified with a real Check call so divergence
// from the server is visible rather than silent.
//
// Walks are bounded by a call budget and a deadline. High-cardinality edges
// are sampled, and ListObjects is used to find (or rule out) the specific
// userset or parent edges that matter, so most traces stay small even on
// stores with billions of tuples.
package trace

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/cond"
	"github.com/sykesm/openfga-explorer/internal/fga"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"google.golang.org/protobuf/types/known/structpb"
)

// Outcome is the result of evaluating a node or link.
type Outcome string

const (
	Allowed Outcome = "allowed"
	Denied  Outcome = "denied"
	// Unknown means the walker could not decide within its budget.
	Unknown Outcome = "unknown"
	Errored Outcome = "error"
	// Skipped branches were not evaluated because the result was already decided.
	Skipped Outcome = "skipped"
)

// Kind identifies the role of a node in the trace graph.
type Kind string

const (
	KindRelation     Kind = "relation"     // object#relation
	KindUnion        Kind = "union"        // nested "or"
	KindIntersection Kind = "intersection" // nested "and"
	KindExclusion    Kind = "exclusion"    // nested "but not"
	KindDirect       Kind = "direct"       // type restrictions (tuples)
	KindTTU          Kind = "ttu"          // "X from Y"
	KindSubject      Kind = "subject"      // the user being checked (or a wildcard)
	KindBlocked      Kind = "blocked"      // a tuple whose condition failed
	KindCycle        Kind = "cycle"
	KindSkipped      Kind = "skipped"
	KindMore         Kind = "more" // truncated edges
)

// Symbol is an operator kind's set symbol ("∪", "∩", "∖"); empty for others.
func (k Kind) Symbol() string {
	switch k {
	case KindUnion:
		return "∪"
	case KindIntersection:
		return "∩"
	case KindExclusion:
		return "∖"
	}
	return ""
}

// Word is an operator kind as the model writes it ("or", "and", "but not").
func (k Kind) Word() string {
	switch k {
	case KindUnion:
		return "or"
	case KindIntersection:
		return "and"
	case KindExclusion:
		return "but not"
	}
	return ""
}

// Node is a step in the evaluation.
type Node struct {
	ID       int
	Kind     Kind
	Object   string
	Relation string
	Label    string
	// Expr is the rewrite expression evaluated at this node.
	Expr string
	// Op is set on relation nodes whose rewrite is a union, intersection, or exclusion.
	Op       Kind
	Outcome  Outcome
	Reason   string
	Notes    []string
	Children []*Link
	Server   *ServerVerdict
	// Truncated is set when not every edge was examined.
	Truncated *Truncation
	// Incomplete explains why edges here may have been missed: some edges were
	// never examined and no probe proved they cannot grant access.
	Incomplete string
	// Hint is what the server's Check said about a skipped branch (Allowed or
	// Denied), when guided evaluation asked; empty when not known.
	Hint Outcome
	// Checked lists the server Checks behind Hint (one per object for
	// "X from Y" branches), so a skipped branch can be traced on demand.
	Checked []BranchCheck
}

// BranchCheck is a server Check made to guide the walk.
type BranchCheck struct {
	Object, Relation string
	Known, Allowed   bool
}

// Link connects a node to one of its children.
type Link struct {
	Label string
	// Tuple is the tuple traversed by this link, if any.
	Tuple *Tuple
	Cond  *cond.Result
	// Role is "base" or "subtract" for exclusions.
	Role    string
	Outcome Outcome
	// Blocking marks the link that decided a denial (failed intersection branch,
	// matched exclusion, failed condition).
	Blocking bool
	// Probe is set when the edge was found through a targeted ListObjects probe.
	Probe bool
	Node  *Node
}

// Tuple is a tuple traversed during the walk.
type Tuple struct {
	User, Relation, Object string
	Condition              string
	Contextual             bool
}

// Key is the tuple's identity, object#relation (its condition is part of
// the tuple, not of its key).
func (t *Tuple) Key() string { return t.Object + "#" + t.Relation + "@" + t.User }

func (t *Tuple) String() string {
	s := t.Key()
	if t.Condition != "" {
		s += " [" + t.Condition + "]"
	}
	return s
}

// Truncation records unexamined edges.
type Truncation struct {
	// Examined is the number of edges evaluated.
	Examined int
	// Found is the number of relevant edges among the tuples read.
	Found int
	// Read is the number of tuples read; More is set when further pages exist.
	Read     int
	More     bool
	What     string // "userset" or the tupleset relation
	Object   string
	Relation string
}

// Summary describes the truncation, e.g. "examined 10 of 100+ userset edges".
func (t *Truncation) Summary() string {
	what := t.What + " edges"
	if t.What == "userset" {
		what = "userset (group-style) edges"
	}
	switch {
	case !t.More:
		return fmt.Sprintf("examined %d of %d %s", t.Examined, t.Found, what)
	case t.Found == 0:
		return fmt.Sprintf("no %s among the first %d tuples read; the rest were not read", what, t.Read)
	case t.Found < t.Read:
		return fmt.Sprintf("examined %d of the %d %s among the first %d tuples read; the rest were not read", t.Examined, t.Found, what, t.Read)
	}
	return fmt.Sprintf("examined %d of the first %d %s read; more were not read", t.Examined, t.Found, what)
}

// ServerVerdict is the result of a verification Check call.
type ServerVerdict struct {
	Allowed  bool
	Err      string
	Mismatch bool
}

// Phase of a step in the animation timeline.
type Phase string

const (
	Enter    Phase = "enter"
	Exit     Phase = "exit"
	Revisit  Phase = "revisit"
	Traverse Phase = "link"
)

// Step is an entry in the evaluation timeline, used for animation.
type Step struct {
	Phase   Phase
	Node    int
	From    int // for links
	Outcome Outcome
}

// Request is a check to explain.
type Request struct {
	Store, Model           string
	User, Relation, Object string
	Context                *structpb.Struct
	Contextual             []*openfgav1.TupleKey
	Consistency            openfgav1.ConsistencyPreference
}

// VerifyMode selects which relation nodes are verified with a server Check.
type VerifyMode string

const (
	VerifyNone VerifyMode = "none"
	// VerifyRoot verifies the root plus every undecided node.
	VerifyRoot VerifyMode = "root"
	VerifyAll  VerifyMode = "all"
)

// Options bounds a walk.
type Options struct {
	MaxCalls int
	MaxDepth int
	// MaxFanout is the number of edges evaluated per direct/tuple-to-userset node.
	MaxFanout int
	// MaxEdges is the number of edges read per node (in pages of 100).
	MaxEdges int
	// ProbeLimit is the number of ListObjects candidates looked up per probe.
	ProbeLimit int
	Timeout    time.Duration
	// AllPaths evaluates every branch instead of stopping at the first decision.
	AllPaths    bool
	Verify      VerifyMode
	VerifyLimit int
	// Parallel bounds concurrent lookahead reads. 0 uses the default; 1
	// disables lookahead (strictly sequential reads).
	Parallel int
	// Progress, if set, is called as the walk enters each relation.
	Progress func(Progress)
	// Unguided disables guided evaluation. Normally, when not evaluating every
	// path, the walker asks the server (one BatchCheck per "or"/"and") which
	// branches hold, evaluates a deciding branch first, and skips the rest.
	Unguided bool
}

// Evaluation orders, see Options.Order.
const (
	OrderGuided = "guided" // shortest explanation: the server picks the deciding branch
	OrderModel  = "model"  // branches in the order the model lists them
	OrderAll    = "all"    // every branch
)

// Order names how branches are evaluated.
func (o Options) Order() string {
	switch {
	case o.AllPaths:
		return OrderAll
	case o.Unguided:
		return OrderModel
	}
	return OrderGuided
}

// SetOrder sets AllPaths and Unguided from an evaluation order name.
func (o *Options) SetOrder(order string) {
	o.AllPaths = order == OrderAll
	o.Unguided = order == OrderModel
}

// Progress reports a walk in flight.
type Progress struct {
	Calls   int
	Current string // object#relation being evaluated
}

// DefaultOptions are the defaults used by the UI.
func DefaultOptions() Options {
	return Options{
		MaxCalls:    250,
		MaxDepth:    25,
		MaxFanout:   25,
		MaxEdges:    100,
		ProbeLimit:  25,
		Timeout:     15 * time.Second,
		Verify:      VerifyAll,
		VerifyLimit: 150,
		Parallel:    16,
	}
}

// Trace is the explained result of a check.
type Trace struct {
	Request     Request
	Options     Options
	Root        *Node
	Steps       []Step
	Calls       int
	VerifyCalls int
	Duration    time.Duration
	// Exhausted is set when the call budget or deadline stopped the walk.
	Exhausted  bool
	Mismatches int
	Nodes      int
	// GuidedSkips counts branches skipped because the server's Check had
	// already shown which branch decides (guided evaluation).
	GuidedSkips int
	// GroupsRuledOut counts userset edges (e.g. groups with access) not
	// walked because the server's Check says the user does not hold them.
	GroupsRuledOut int
	// The walk's calls by kind (ReadCalls + CheckCalls + ListCalls = Calls),
	// and TuplesRead, the tuples its Read calls returned. Tuples the server
	// reads inside its own Check and ListObjects calls are not known.
	ReadCalls, CheckCalls, ListCalls int
	TuplesRead                       int
	// CachedReads of the ReadCalls, and TuplesCached of the TuplesRead, were
	// answered by a read cache in front of the API rather than by OpenFGA.
	CachedReads, TuplesCached int
	// LookaheadUnused counts reads made ahead of the walk (to save time) that
	// it turned out not to need. They reached the server (or its cache) but
	// are not among the walk's calls.
	LookaheadUnused int
}

// Decision is the overall answer: the server verdict when available, else the walker's.
func (t *Trace) Decision() Outcome {
	if v := t.Root.Server; v != nil && v.Err == "" {
		if v.Allowed {
			return Allowed
		}
		return Denied
	}
	return t.Root.Outcome
}

var errBudget = errors.New("budget exhausted")

type walker struct {
	api   fga.API
	model *schema.Model
	cond  *cond.Evaluator
	req   Request
	opt   Options
	subj  schema.Subject

	mu    sync.Mutex // guards rcache
	calls atomic.Int64
	// Calls by kind (they add up to calls), and the tuples the reads returned.
	readCalls  atomic.Int64
	checkCalls atomic.Int64
	listCalls  atomic.Int64
	tuplesRead atomic.Int64
	// Reads (and their tuples) answered by a read cache in front of the API
	// (fga.Cache), not by OpenFGA.
	cachedReads  atomic.Int64
	tuplesCached atomic.Int64
	// lookahead counts reads issued ahead of the walk (see prefetch).
	lookahead      atomic.Int64
	guidedSkips    atomic.Int64
	groupsRuledOut atomic.Int64
	deadline       time.Time
	exhaust        atomic.Bool
	rcache         map[readKey]*readEntry
	known          map[string]bool // server verdicts for object#relation, from guidance
	sem            chan struct{}   // bounds concurrent lookahead reads
	nextID         int
	memo           map[string]*Node
	subjects       map[string]*Node
	stack          map[string]bool
	steps          []Step
	relNodes       []*Node
}

// Walk evaluates a check step by step (reading tuples, following the model's
// rewrite rules) and returns its trace; Trace.Explanation puts it in words.
func Walk(ctx context.Context, api fga.API, model *schema.Model, ev *cond.Evaluator, req Request, opt Options) (*Trace, error) {
	subj, err := schema.ParseSubject(req.User)
	if err != nil {
		return nil, err
	}
	typ, _, ok := schema.SplitObject(req.Object)
	if !ok {
		return nil, fmt.Errorf("invalid object %q: expected type:id", req.Object)
	}
	if _, ok := model.Relation(typ, req.Relation); !ok {
		return nil, fmt.Errorf("type %q has no relation %q", typ, req.Relation)
	}
	if _, ok := model.Types[subj.Type]; !ok {
		return nil, fmt.Errorf("user type %q is not defined in the model", subj.Type)
	}
	def := DefaultOptions()
	if opt.MaxCalls <= 0 {
		opt.MaxCalls = def.MaxCalls
	}
	if opt.MaxDepth <= 0 {
		opt.MaxDepth = def.MaxDepth
	}
	if opt.MaxFanout <= 0 {
		opt.MaxFanout = def.MaxFanout
	}
	if opt.MaxEdges <= 0 {
		opt.MaxEdges = def.MaxEdges
	}
	if opt.ProbeLimit <= 0 {
		opt.ProbeLimit = def.ProbeLimit
	}
	if opt.Timeout <= 0 {
		opt.Timeout = def.Timeout
	}
	if opt.Verify == "" {
		opt.Verify = def.Verify
	}
	if opt.VerifyLimit <= 0 {
		opt.VerifyLimit = def.VerifyLimit
	}
	if opt.Parallel <= 0 {
		opt.Parallel = def.Parallel
	}
	if ev == nil {
		ev = cond.New(model.Conditions)
	}
	start := time.Now()
	w := &walker{
		api: api, model: model, cond: ev, req: req, opt: opt, subj: subj,
		deadline: start.Add(opt.Timeout),
		memo:     map[string]*Node{},
		subjects: map[string]*Node{},
		stack:    map[string]bool{},
		rcache:   map[readKey]*readEntry{},
		known:    map[string]bool{},
		sem:      make(chan struct{}, opt.Parallel),
	}
	ctx, cancel := context.WithDeadline(ctx, w.deadline.Add(5*time.Second))
	defer cancel()

	root := w.checkRelation(ctx, req.Object, req.Relation, 0)
	tr := &Trace{Request: req, Options: opt, Root: root}
	tr.Calls = int(w.calls.Load())
	tr.ReadCalls, tr.CheckCalls, tr.ListCalls = int(w.readCalls.Load()), int(w.checkCalls.Load()), int(w.listCalls.Load())
	tr.TuplesRead = int(w.tuplesRead.Load())
	tr.CachedReads, tr.TuplesCached = int(w.cachedReads.Load()), int(w.tuplesCached.Load())
	tr.Exhausted = w.exhaust.Load()
	tr.LookaheadUnused = w.lookaheadUnused()
	tr.VerifyCalls = w.verify(ctx, root)
	tr.Steps = w.steps
	tr.Nodes = w.nextID
	tr.GuidedSkips = int(w.guidedSkips.Load())
	tr.GroupsRuledOut = int(w.groupsRuledOut.Load())
	for _, n := range w.relNodes {
		if n.Server != nil && n.Server.Mismatch {
			tr.Mismatches++
		}
	}
	tr.Duration = time.Since(start)
	return tr, nil
}

func (w *walker) node(kind Kind, label string) *Node {
	w.nextID++
	return &Node{ID: w.nextID, Kind: kind, Label: label}
}

func (w *walker) step(s Step) { w.steps = append(w.steps, s) }

func (w *walker) budget() error {
	if int(w.calls.Load()) >= w.opt.MaxCalls || time.Now().After(w.deadline) {
		w.exhaust.Store(true)
		return errBudget
	}
	return nil
}

// record is a tuple returned from a read, with its origin.
type record struct {
	key        *openfgav1.TupleKey
	contextual bool
}

// readKey identifies one Read API page.
type readKey struct{ object, relation, user, token string }

// readEntry is a Read result, shared between the walk and lookahead reads.
type readEntry struct {
	done   chan struct{}
	tuples []*openfgav1.TupleKey
	next   string
	err    error
	cached bool // answered by the read cache in front of the API
	// charged is set (under walker.mu) once the walk uses the entry: only
	// then does it count as one of the walk's calls.
	charged bool
}

// fetch returns the Read result for k, issuing the call at most once per walk.
// A read lookahead already issued (or finished) is shared, not repeated. The
// walk is charged one call the first time it uses an entry, whoever issued
// it, so its calls, budget and decisions are the same with or without
// lookahead.
func (w *walker) fetch(ctx context.Context, k readKey) *readEntry {
	w.mu.Lock()
	e, ok := w.rcache[k]
	charge := !ok || !e.charged
	if charge {
		if err := w.budget(); err != nil {
			w.mu.Unlock() // budget failures are not cached: a later call may still be allowed
			e := &readEntry{done: make(chan struct{}), err: err}
			close(e.done)
			return e
		}
		w.calls.Add(1)
		w.readCalls.Add(1)
	}
	if !ok {
		e = &readEntry{done: make(chan struct{})}
		w.rcache[k] = e
	}
	e.charged = true
	w.mu.Unlock()
	if !ok {
		w.issue(ctx, k, e)
	}
	<-e.done
	if charge && e.err == nil {
		w.tuplesRead.Add(int64(len(e.tuples)))
		if e.cached {
			w.cachedReads.Add(1)
			w.tuplesCached.Add(int64(len(e.tuples)))
		}
	}
	return e
}

// issue makes the Read call for k and completes e.
func (w *walker) issue(ctx context.Context, k readKey, e *readEntry) {
	defer close(e.done)
	rctx, cached := fga.WithCacheReport(ctx)
	resp, err := fga.ReadPage(rctx, w.api, w.req.Store, &openfgav1.ReadRequestTupleKey{Object: k.object, Relation: k.relation, User: k.user}, 100, k.token, w.req.Consistency)
	if err != nil {
		e.err = err
		return
	}
	for _, t := range resp.GetTuples() {
		e.tuples = append(e.tuples, t.GetKey())
	}
	e.next, e.cached = resp.GetContinuationToken(), *cached
}

// read reads one page of tuples matching the filter, merging contextual tuples on the first page.
func (w *walker) read(ctx context.Context, object, relation, user, token string) ([]record, string, error) {
	e := w.fetch(ctx, readKey{object, relation, user, token})
	if e.err != nil {
		return nil, "", e.err
	}
	var out []record
	if token == "" {
		for _, ct := range w.req.Contextual {
			if ct.GetObject() == object && ct.GetRelation() == relation && (user == "" || ct.GetUser() == user) {
				out = append(out, record{key: ct, contextual: true})
			}
		}
	}
	for _, t := range e.tuples {
		out = append(out, record{key: t})
	}
	return out, e.next, nil
}

// prefetch issues reads concurrently ahead of the (sequential) walk. The walk
// later finds them in the cache and is charged for them only then (see
// fetch), so its decisions and step order are the same as without lookahead;
// only latency changes. Lookahead has its own allowance (up to MaxCalls
// reads, while the walk has budget left) and never marks the walk as out of
// budget. Reads the walk never uses are reported as LookaheadUnused.
func (w *walker) prefetch(ctx context.Context, keys []readKey) {
	if w.opt.Parallel <= 1 {
		return
	}
	for _, k := range keys {
		select {
		case w.sem <- struct{}{}:
		default:
			return // lookahead is saturated; the walk will read on demand
		}
		w.mu.Lock()
		_, seen := w.rcache[k]
		allowed := !seen && int(w.calls.Load()) < w.opt.MaxCalls && int(w.lookahead.Load()) < w.opt.MaxCalls && time.Now().Before(w.deadline)
		var e *readEntry
		if allowed {
			e = &readEntry{done: make(chan struct{})}
			w.rcache[k] = e
			w.lookahead.Add(1)
		}
		w.mu.Unlock()
		if !allowed {
			<-w.sem
			continue
		}
		go func() {
			defer func() { <-w.sem }()
			w.issue(ctx, k, e)
		}()
	}
}

// lookaheadUnused counts the lookahead reads the walk never used.
func (w *walker) lookaheadUnused() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, e := range w.rcache {
		if !e.charged {
			n++
		}
	}
	return n
}

// plannedReads lists the first-page reads evaluating object#relation will
// need on that object (following computed relations on the same object).
func (w *walker) plannedReads(object, relation string) []readKey {
	var out []readKey
	seen := map[string]bool{}
	var visit func(rel string)
	visit = func(rel string) {
		if seen[rel] {
			return
		}
		seen[rel] = true
		typ, _, _ := schema.SplitObject(object)
		r, ok := w.model.Relation(typ, rel)
		if !ok {
			return
		}
		// Both sides of a "but not" are evaluated, so both need their reads.
		for u := range schema.Leaves(r.Rewrite) {
			switch v := u.GetUserset().(type) {
			case *openfgav1.Userset_This:
				usersets := false
				for _, ref := range r.DirectTypes {
					switch {
					case refAcceptsSubject(ref, w.subj):
						out = append(out, readKey{object, rel, w.subj.String(), ""})
					case ref.GetWildcard() != nil && ref.GetType() == w.subj.Type && w.subj.Relation == "":
						out = append(out, readKey{object, rel, w.subj.Type + ":*", ""})
					case ref.GetRelation() != "":
						usersets = true
					}
				}
				if usersets {
					out = append(out, readKey{object, rel, "", ""})
				}
			case *openfgav1.Userset_ComputedUserset:
				visit(v.ComputedUserset.GetRelation())
			case *openfgav1.Userset_TupleToUserset:
				out = append(out, readKey{object, v.TupleToUserset.GetTupleset().GetRelation(), "", ""})
			}
		}
	}
	visit(relation)
	return out
}

// readAll reads up to MaxEdges tuples. more reports whether further pages exist.
func (w *walker) readAll(ctx context.Context, object, relation string) (recs []record, more bool, err error) {
	token := ""
	for {
		page, next, err := w.read(ctx, object, relation, "", token)
		recs = append(recs, page...)
		if err != nil {
			return recs, next != "", err
		}
		if next == "" {
			return recs, false, nil
		}
		if len(recs) >= w.opt.MaxEdges {
			return recs, true, nil
		}
		token = next
	}
}

func (w *walker) checkRelation(ctx context.Context, object, relation string, depth int) *Node {
	key := object + "#" + relation
	if w.stack[key] {
		n := w.node(KindCycle, key)
		n.Object, n.Relation = object, relation
		n.Outcome = Denied
		n.Reason = "cycle: " + key + " is already being evaluated on this path, so this branch cannot grant access"
		w.step(Step{Phase: Enter, Node: n.ID})
		w.step(Step{Phase: Exit, Node: n.ID, Outcome: n.Outcome})
		return n
	}
	if n, ok := w.memo[key]; ok {
		w.step(Step{Phase: Revisit, Node: n.ID, Outcome: n.Outcome})
		return n
	}
	n := w.node(KindRelation, key)
	n.Object, n.Relation = object, relation
	w.memo[key] = n
	if w.opt.Progress != nil {
		w.opt.Progress(Progress{Calls: int(w.calls.Load()), Current: key})
	}
	w.prefetch(ctx, w.plannedReads(object, relation))
	w.relNodes = append(w.relNodes, n)
	w.step(Step{Phase: Enter, Node: n.ID})
	defer func() { w.step(Step{Phase: Exit, Node: n.ID, Outcome: n.Outcome}) }()

	typ, _, _ := schema.SplitObject(object)
	rel, ok := w.model.Relation(typ, relation)
	if !ok {
		n.Outcome, n.Reason = Errored, fmt.Sprintf("type %s has no relation %s", typ, relation)
		return n
	}
	n.Expr = rel.Expr
	if w.subj.Relation != "" && w.subj.Type+":"+w.subj.ID == object && w.subj.Relation == relation {
		n.Outcome, n.Reason = Allowed, "the user is this userset"
		return n
	}
	if depth >= w.opt.MaxDepth {
		n.Outcome, n.Reason = Unknown, fmt.Sprintf("maximum resolution depth (%d) reached", w.opt.MaxDepth)
		return n
	}
	if w.budget() != nil {
		n.Outcome, n.Reason = Unknown, "walk budget exhausted before this relation was evaluated"
		return n
	}
	w.stack[key] = true
	defer delete(w.stack, key)

	switch v := rel.Rewrite.GetUserset().(type) {
	case *openfgav1.Userset_Union:
		n.Op = KindUnion
		n.Outcome, n.Reason = w.union(ctx, n, object, rel, v.Union.GetChild(), depth)
	case *openfgav1.Userset_Intersection:
		n.Op = KindIntersection
		n.Outcome, n.Reason = w.intersection(ctx, n, object, rel, v.Intersection.GetChild(), depth)
	case *openfgav1.Userset_Difference:
		n.Op = KindExclusion
		n.Outcome, n.Reason = w.difference(ctx, n, object, rel, v.Difference, depth)
	default:
		child, label := w.eval(ctx, object, rel, rel.Rewrite, depth)
		if child.Kind == KindDirect {
			// A relation defined only by type restrictions: fold the direct node
			// into the relation so its tuples hang off the relation itself.
			n.Children, n.Notes, n.Truncated = child.Children, child.Notes, child.Truncated
			n.Outcome, n.Reason = child.Outcome, child.Reason
			for i := range w.steps {
				if w.steps[i].Node == child.ID && w.steps[i].Phase != Traverse {
					w.steps[i].Node = n.ID
				}
				if w.steps[i].From == child.ID {
					w.steps[i].From = n.ID
				}
			}
			return n
		}
		w.link(n, &Link{Label: label, Node: child, Outcome: child.Outcome})
		n.Outcome, n.Reason = child.Outcome, child.Reason
	}
	return n
}

func (w *walker) link(parent *Node, l *Link) {
	parent.Children = append(parent.Children, l)
	if l.Outcome != Skipped && l.Node != nil {
		w.step(Step{Phase: Traverse, From: parent.ID, Node: l.Node.ID, Outcome: l.Outcome})
	}
}

// eval evaluates a rewrite node and returns the child node plus a link label.
func (w *walker) eval(ctx context.Context, object string, rel *schema.Relation, u *openfgav1.Userset, depth int) (*Node, string) {
	switch v := u.GetUserset().(type) {
	case *openfgav1.Userset_This:
		return w.direct(ctx, object, rel, depth), "direct"
	case *openfgav1.Userset_ComputedUserset:
		return w.checkRelation(ctx, object, v.ComputedUserset.GetRelation(), depth+1), "computed"
	case *openfgav1.Userset_TupleToUserset:
		return w.ttu(ctx, object, rel, v.TupleToUserset, depth), "from " + v.TupleToUserset.GetTupleset().GetRelation()
	case *openfgav1.Userset_Union:
		n := w.opNode(KindUnion, object, rel, u)
		n.Outcome, n.Reason = w.union(ctx, n, object, rel, v.Union.GetChild(), depth)
		w.step(Step{Phase: Exit, Node: n.ID, Outcome: n.Outcome})
		return n, "or"
	case *openfgav1.Userset_Intersection:
		n := w.opNode(KindIntersection, object, rel, u)
		n.Outcome, n.Reason = w.intersection(ctx, n, object, rel, v.Intersection.GetChild(), depth)
		w.step(Step{Phase: Exit, Node: n.ID, Outcome: n.Outcome})
		return n, "and"
	case *openfgav1.Userset_Difference:
		n := w.opNode(KindExclusion, object, rel, u)
		n.Outcome, n.Reason = w.difference(ctx, n, object, rel, v.Difference, depth)
		w.step(Step{Phase: Exit, Node: n.ID, Outcome: n.Outcome})
		return n, "but not"
	}
	n := w.node(KindRelation, "?")
	n.Outcome, n.Reason = Errored, "unsupported rewrite"
	return n, "?"
}

func (w *walker) opNode(kind Kind, object string, rel *schema.Relation, u *openfgav1.Userset) *Node {
	n := w.node(kind, kind.Symbol()+" "+kind.Word())
	n.Object, n.Relation = object, rel.Name
	n.Expr = schema.RenderRewrite(u, rel.DirectTypes)
	w.step(Step{Phase: Enter, Node: n.ID})
	return n
}

// skipped records a branch that was not evaluated. g, when set, is what the
// server's Check said about it during guided evaluation.
func (w *walker) skipped(object string, rel *schema.Relation, u *openfgav1.Userset, why string, g *guess) *Link {
	n := w.node(KindSkipped, schema.RenderRewrite(u, rel.DirectTypes))
	n.Object, n.Relation = object, rel.Name
	n.Outcome, n.Reason = Skipped, why
	if g != nil {
		n.Checked = g.checks
		if g.known {
			if g.allowed {
				n.Hint = Allowed
				n.Reason += " · the server's Check says this branch would also grant access"
			} else {
				n.Hint = Denied
				n.Reason += " · the server's Check says this branch does not hold"
			}
		}
	}
	return &Link{Label: "not evaluated", Node: n, Outcome: Skipped}
}

func guessAt(gs []guess, i int) *guess {
	if i < len(gs) {
		return &gs[i]
	}
	return nil
}

func branchName(u *openfgav1.Userset, rel *schema.Relation) string {
	if _, ok := u.GetUserset().(*openfgav1.Userset_This); ok {
		return "direct tuples"
	}
	return schema.RenderRewrite(u, rel.DirectTypes)
}

func combineUndecided(outs []Outcome) Outcome {
	if slices.Contains(outs, Unknown) {
		return Unknown
	}
	if slices.Contains(outs, Errored) {
		return Errored
	}
	return Denied
}

func (w *walker) union(ctx context.Context, n *Node, object string, rel *schema.Relation, children []*openfgav1.Userset, depth int) (Outcome, string) {
	links := make([]*Link, len(children))
	outs := make([]Outcome, len(children))
	var granted []string
	eval := func(i int) Outcome {
		child, label := w.eval(ctx, object, rel, children[i], depth)
		links[i] = &Link{Label: label, Node: child, Outcome: child.Outcome}
		w.traverse(n, links[i])
		outs[i] = child.Outcome
		if child.Outcome == Allowed {
			granted = append(granted, branchName(children[i], rel))
		}
		return child.Outcome
	}
	// Guided: evaluate a branch the server says grants access first; if it
	// does, the others are not needed (an "or" needs only one).
	first := -1
	var gs []guess
	if !w.opt.AllPaths && !w.opt.Unguided && len(children) > 1 {
		gs = w.guide(ctx, object, children)
		for i, g := range gs {
			if g.known && g.allowed {
				first = i
				break
			}
		}
	}
	decided := false
	if first >= 0 && eval(first) == Allowed {
		decided = true
		why := fmt.Sprintf("not needed: %s grants access (the server's Check confirmed which branch grants)", branchName(children[first], rel))
		for i, c := range children {
			if i != first {
				links[i] = w.skipped(object, rel, c, why, guessAt(gs, i))
				w.guidedSkips.Add(1)
			}
		}
	} else {
		for i, c := range children {
			if i == first {
				continue
			}
			if decided && !w.opt.AllPaths {
				links[i] = w.skipped(object, rel, c, "an earlier branch already granted access", guessAt(gs, i))
				continue
			}
			if eval(i) == Allowed {
				decided = true
			}
		}
	}
	n.Children = append(n.Children, links...)
	if decided {
		return Allowed, "granted by " + strings.Join(granted, ", ")
	}
	var evaluated []Outcome
	for i, l := range links {
		if l.Outcome != Skipped {
			evaluated = append(evaluated, outs[i])
		}
	}
	o := combineUndecided(evaluated)
	if o == Denied {
		return Denied, "no branch of the union grants access"
	}
	return o, "at least one branch could not be decided"
}

// traverse records the animation step for a link without attaching it
// (union and intersection attach links in model order after evaluating).
func (w *walker) traverse(parent *Node, l *Link) {
	if l.Outcome != Skipped && l.Node != nil {
		w.step(Step{Phase: Traverse, From: parent.ID, Node: l.Node.ID, Outcome: l.Outcome})
	}
}

func (w *walker) intersection(ctx context.Context, n *Node, object string, rel *schema.Relation, children []*openfgav1.Userset, depth int) (Outcome, string) {
	links := make([]*Link, len(children))
	outs := make([]Outcome, len(children))
	var blocked []string
	eval := func(i int) Outcome {
		child, label := w.eval(ctx, object, rel, children[i], depth)
		l := &Link{Label: label, Node: child, Outcome: child.Outcome}
		if child.Outcome == Denied {
			l.Blocking = true
			blocked = append(blocked, branchName(children[i], rel))
		}
		links[i] = l
		w.traverse(n, l)
		outs[i] = child.Outcome
		return child.Outcome
	}
	// Guided: evaluate a branch the server says fails first; if it does, the
	// others are not needed (an "and" fails if any branch fails).
	first := -1
	var gs []guess
	if !w.opt.AllPaths && !w.opt.Unguided && len(children) > 1 {
		gs = w.guide(ctx, object, children)
		for i, g := range gs {
			if g.known && !g.allowed {
				first = i
				break
			}
		}
	}
	if first >= 0 && eval(first) == Denied {
		why := fmt.Sprintf("not needed: %s does not hold, so the intersection cannot (the server's Check confirmed it)", branchName(children[first], rel))
		for i, c := range children {
			if i != first {
				links[i] = w.skipped(object, rel, c, why, guessAt(gs, i))
				w.guidedSkips.Add(1)
			}
		}
	} else {
		for i, c := range children {
			if i == first {
				continue
			}
			if len(blocked) > 0 && !w.opt.AllPaths {
				links[i] = w.skipped(object, rel, c, "an earlier branch of the intersection already failed", guessAt(gs, i))
				continue
			}
			eval(i)
		}
	}
	n.Children = append(n.Children, links...)
	if len(blocked) > 0 {
		return Denied, "blocked: intersection requires " + strings.Join(blocked, ", ")
	}
	var evaluated []Outcome
	for i, l := range links {
		if l.Outcome != Skipped {
			evaluated = append(evaluated, outs[i])
		}
	}
	for _, o := range evaluated {
		if o != Allowed {
			return combineUndecided(evaluated), "at least one branch could not be decided"
		}
	}
	return Allowed, "every branch of the intersection is satisfied"
}

// guess is a server-decided verdict for one rewrite branch.
type guess struct {
	known, allowed bool
	checks         []BranchCheck
}

// guide asks the server which branches of an "or"/"and" hold, with one
// BatchCheck. Computed relations map to one check; "X from Y" maps to X on
// each Y object (read from the tuples, usually already cached). Direct and
// nested branches stay unknown.
func (w *walker) guide(ctx context.Context, object string, children []*openfgav1.Userset) []guess {
	out := make([]guess, len(children))
	subs := make([][]string, len(children)) // object#relation keys per branch
	partial := make([]bool, len(children))  // TTU edges beyond the fanout limit
	var all []string
	for i, c := range children {
		switch v := c.GetUserset().(type) {
		case *openfgav1.Userset_ComputedUserset:
			subs[i] = []string{object + "#" + v.ComputedUserset.GetRelation()}
		case *openfgav1.Userset_TupleToUserset:
			target := v.TupleToUserset.GetComputedUserset().GetRelation()
			recs, next, err := w.read(ctx, object, v.TupleToUserset.GetTupleset().GetRelation(), "", "")
			if err != nil {
				continue
			}
			for _, r := range recs {
				if len(subs[i]) >= w.opt.MaxFanout {
					partial[i] = true
					break
				}
				typ, _, _ := schema.SplitObject(r.key.GetUser())
				if _, ok := w.model.Relation(typ, target); ok && r.key.GetCondition() == nil {
					subs[i] = append(subs[i], r.key.GetUser()+"#"+target)
				} else if r.key.GetCondition() != nil {
					partial[i] = true // conditional parent edges: let the walk decide
				}
			}
			if next != "" {
				partial[i] = true
			}
			if len(subs[i]) == 0 && !partial[i] {
				out[i] = guess{known: true, allowed: false} // no parents at all
			}
		}
		all = append(all, subs[i]...)
	}
	verdicts := w.serverChecks(ctx, all)
	for i := range children {
		for _, k := range subs[i] {
			v, ok := verdicts[k]
			j := strings.LastIndexByte(k, '#')
			out[i].checks = append(out[i].checks, BranchCheck{Object: k[:j], Relation: k[j+1:], Known: ok, Allowed: v})
		}
		if len(subs[i]) == 0 {
			continue
		}
		known, granted := true, false
		for _, k := range subs[i] {
			v, ok := verdicts[k]
			if !ok {
				known = false
				continue
			}
			granted = granted || v
		}
		switch {
		case granted:
			out[i].known, out[i].allowed = true, true
		case known && !partial[i]:
			out[i].known, out[i].allowed = true, false
		}
	}
	return out
}

// serverChecks runs Check for object#relation keys (BatchCheck when the
// server supports it), remembering results for verification.
func (w *walker) serverChecks(ctx context.Context, keys []string) map[string]bool {
	out := map[string]bool{}
	var todo []string
	seen := map[string]bool{}
	w.mu.Lock()
	for _, k := range keys {
		if v, ok := w.known[k]; ok {
			out[k] = v
		} else if !seen[k] {
			seen[k] = true
			todo = append(todo, k)
		}
	}
	w.mu.Unlock()
	if len(todo) == 0 || w.budget() != nil {
		return out
	}
	ctuples := w.contextual()
	tk := func(k string) *openfgav1.CheckRequestTupleKey {
		i := strings.LastIndexByte(k, '#')
		return w.checkKey(k[:i], k[i+1:])
	}
	record := func(k string, v bool) {
		w.mu.Lock()
		w.known[k] = v
		w.mu.Unlock()
		out[k] = v
	}
	if bc, ok := w.api.(fga.BatchChecker); ok && len(todo) <= batchSize {
		w.calls.Add(1)
		w.checkCalls.Add(1)
		req := &openfgav1.BatchCheckRequest{StoreId: w.req.Store, AuthorizationModelId: w.req.Model, Consistency: w.req.Consistency}
		for i, k := range todo {
			req.Checks = append(req.Checks, &openfgav1.BatchCheckItem{TupleKey: tk(k), ContextualTuples: ctuples, Context: w.req.Context, CorrelationId: strconv.Itoa(i)})
		}
		if resp, err := bc.BatchCheck(ctx, req); err == nil {
			for i, k := range todo {
				if r := resp.GetResult()[strconv.Itoa(i)]; r != nil && r.GetError() == nil {
					record(k, r.GetAllowed())
				}
			}
			return out
		}
	}
	for _, k := range todo {
		if w.budget() != nil {
			break
		}
		w.calls.Add(1)
		w.checkCalls.Add(1)
		req := &openfgav1.CheckRequest{StoreId: w.req.Store, AuthorizationModelId: w.req.Model, TupleKey: tk(k), Context: w.req.Context, Consistency: w.req.Consistency, ContextualTuples: ctuples}
		if resp, err := w.api.Check(ctx, req); err == nil {
			record(k, resp.GetAllowed())
		}
	}
	return out
}

func (w *walker) difference(ctx context.Context, n *Node, object string, rel *schema.Relation, d *openfgav1.Difference, depth int) (Outcome, string) {
	base, label := w.eval(ctx, object, rel, d.GetBase(), depth)
	bl := &Link{Label: label, Role: "base", Node: base, Outcome: base.Outcome}
	w.link(n, bl)
	if base.Outcome == Denied && !w.opt.AllPaths {
		bl.Blocking = true
		l := w.skipped(object, rel, d.GetSubtract(), "the base of the exclusion is not satisfied", nil)
		l.Role = "subtract"
		w.link(n, l)
		return Denied, "blocked: base " + schema.RenderRewrite(d.GetBase(), rel.DirectTypes) + " is not satisfied"
	}
	sub, slabel := w.eval(ctx, object, rel, d.GetSubtract(), depth)
	sl := &Link{Label: "but not " + slabel, Role: "subtract", Node: sub, Outcome: sub.Outcome}
	w.link(n, sl)
	switch {
	case base.Outcome == Denied:
		bl.Blocking = true
		return Denied, "blocked: base " + schema.RenderRewrite(d.GetBase(), rel.DirectTypes) + " is not satisfied"
	case sub.Outcome == Allowed:
		sl.Blocking = true
		return Denied, "blocked: excluded by \"but not " + schema.RenderRewrite(d.GetSubtract(), rel.DirectTypes) + "\""
	case base.Outcome == Allowed && sub.Outcome == Denied:
		return Allowed, "base is satisfied and the exclusion does not apply"
	}
	return combineUndecided([]Outcome{base.Outcome, sub.Outcome}), "base or exclusion could not be decided"
}

func (w *walker) subjectNode(user string) *Node {
	if n, ok := w.subjects[user]; ok {
		return n
	}
	n := w.node(KindSubject, user)
	n.Outcome = Allowed
	w.subjects[user] = n
	return n
}

// tupleLink builds a link for a tuple, evaluating its condition. ok reports
// whether the tuple can be traversed.
func (w *walker) tupleLink(r record, label string) (*Link, bool) {
	k := r.key
	l := &Link{Label: label, Tuple: &Tuple{User: k.GetUser(), Relation: k.GetRelation(), Object: k.GetObject(), Contextual: r.contextual}}
	if c := k.GetCondition(); c != nil && c.GetName() != "" {
		l.Tuple.Condition = c.GetName()
		l.Cond = w.cond.Evaluate(c.GetName(), c.GetContext(), w.req.Context)
		switch l.Cond.Status {
		case cond.Met:
		case cond.NotMet:
			l.Outcome, l.Blocking = Denied, true
			return l, false
		default:
			l.Outcome, l.Blocking = Errored, true
			return l, false
		}
	}
	return l, true
}

func (w *walker) blockedNode(l *Link) *Node {
	n := w.node(KindBlocked, l.Tuple.User)
	n.Outcome = l.Outcome
	if l.Cond != nil {
		n.Reason = "condition " + l.Cond.Summary()
	}
	return n
}

func refAcceptsSubject(ref *openfgav1.RelationReference, s schema.Subject) bool {
	if ref.GetType() != s.Type {
		return false
	}
	switch {
	case s.Relation != "":
		return ref.GetRelation() == s.Relation
	case s.IsWildcard():
		return ref.GetWildcard() != nil
	default:
		return ref.GetRelation() == "" && ref.GetWildcard() == nil
	}
}

func (w *walker) direct(ctx context.Context, object string, rel *schema.Relation, depth int) *Node {
	n := w.node(KindDirect, "direct "+schema.RenderRefs(rel.DirectTypes))
	n.Object, n.Relation = object, rel.Name
	w.step(Step{Phase: Enter, Node: n.ID})
	defer func() { w.step(Step{Phase: Exit, Node: n.ID, Outcome: n.Outcome}) }()

	found, undecided := false, false
	fail := func(err error) {
		undecided = true
		n.Incomplete = "some edges could not be read: " + err.Error()
		if errors.Is(err, errBudget) {
			n.Notes = append(n.Notes, "budget exhausted")
		} else {
			n.Notes = append(n.Notes, "read failed: "+err.Error())
		}
	}
	user := w.subj.String()

	// 1. A tuple naming the user exactly.
	exact := false
	for _, ref := range rel.DirectTypes {
		if refAcceptsSubject(ref, w.subj) {
			exact = true
		}
	}
	if exact {
		recs, _, err := w.read(ctx, object, rel.Name, user, "")
		if err != nil {
			fail(err)
		}
		for _, r := range recs {
			l, ok := w.tupleLink(r, rel.Name)
			if ok {
				l.Node, l.Outcome = w.subjectNode(user), Allowed
				found = true
			} else {
				l.Node = w.blockedNode(l)
			}
			w.link(n, l)
		}
		if err == nil && len(recs) == 0 {
			n.Notes = append(n.Notes, fmt.Sprintf("no tuple %s#%s@%s", object, rel.Name, user))
		}
	} else {
		n.Notes = append(n.Notes, fmt.Sprintf("type restrictions do not allow %s directly", w.subjectTypeLabel()))
	}

	// 2. A public wildcard tuple.
	if (!found || w.opt.AllPaths) && w.subj.Relation == "" && !w.subj.IsWildcard() {
		for _, ref := range rel.DirectTypes {
			if ref.GetType() != w.subj.Type || ref.GetWildcard() == nil {
				continue
			}
			wc := w.subj.Type + ":*"
			recs, _, err := w.read(ctx, object, rel.Name, wc, "")
			if err != nil {
				fail(err)
				break
			}
			for _, r := range recs {
				l, ok := w.tupleLink(r, rel.Name+" (public)")
				if ok {
					l.Node, l.Outcome = w.subjectNode(wc), Allowed
					found = true
				} else {
					l.Node = w.blockedNode(l)
				}
				w.link(n, l)
			}
			if len(recs) == 0 {
				n.Notes = append(n.Notes, "no wildcard tuple "+wc)
			}
			break
		}
	}

	// 3. Userset edges (type#relation), which may contain the user.
	var usersets []*openfgav1.RelationReference
	seen := map[string]bool{}
	for _, ref := range rel.DirectTypes {
		k := ref.GetType() + "#" + ref.GetRelation()
		if ref.GetRelation() != "" && !seen[k] {
			seen[k] = true
			usersets = append(usersets, ref)
		}
	}
	if len(usersets) > 0 && (!found || w.opt.AllPaths) {
		recs, more, err := w.readAll(ctx, object, rel.Name)
		if err != nil {
			fail(err)
		}
		accepted := map[string]bool{}
		for _, ref := range usersets {
			accepted[ref.GetType()+"#"+ref.GetRelation()] = true
		}
		var edges []record
		for _, r := range recs {
			u := r.key.GetUser()
			s, perr := schema.ParseSubject(u)
			if perr != nil || s.Relation == "" || !accepted[s.Type+"#"+s.Relation] || u == user {
				continue
			}
			edges = append(edges, r)
		}
		// Guided: one BatchCheck asks which userset edges contain the user
		// (which groups the user is a member of, however deeply nested), so
		// only those are walked; the rest are shown as ruled out by the
		// server. Without this, proving membership in a large group tree
		// walks the whole tree. Conditional edges are always walked.
		ruledOut := map[string]bool{}
		if !w.opt.AllPaths && !w.opt.Unguided && len(edges) > 1 {
			var keys []string
			for i, r := range edges {
				if i >= w.opt.MaxFanout {
					break
				}
				if r.key.GetCondition() == nil {
					keys = append(keys, r.key.GetUser()) // "group:eng#member" is object#relation
				}
			}
			holds := w.serverChecks(ctx, keys)
			for _, k := range keys {
				if v, ok := holds[k]; ok && !v {
					ruledOut[k] = true
				}
			}
			// Walk the edges the server says hold first.
			first := func(r record) int {
				if holds[r.key.GetUser()] {
					return 0
				}
				return 1
			}
			slices.SortStableFunc(edges, func(a, b record) int { return cmp.Compare(first(a), first(b)) })
		}
		// Look ahead: start the reads each userset edge will need, in parallel.
		var ahead []readKey
		for i, r := range edges {
			if i >= w.opt.MaxFanout {
				break
			}
			if ruledOut[r.key.GetUser()] {
				continue
			}
			if sub, err := schema.ParseSubject(r.key.GetUser()); err == nil {
				ahead = append(ahead, w.plannedReads(sub.Type+":"+sub.ID, sub.Relation)...)
			}
		}
		w.prefetch(ctx, ahead)
		examined := map[string]bool{}
		evaluated, ruled := 0, 0
		for _, r := range edges {
			if found && !w.opt.AllPaths {
				break
			}
			if evaluated >= w.opt.MaxFanout {
				break
			}
			evaluated++
			examined[r.key.GetUser()] = true
			if ruledOut[r.key.GetUser()] {
				w.link(n, w.ruledOutEdge(r))
				ruled++
				continue
			}
			if w.evalUsersetEdge(ctx, n, r, false, depth) {
				found = true
			}
		}
		if ruled > 0 {
			w.groupsRuledOut.Add(int64(ruled))
			n.Notes = append(n.Notes, fmt.Sprintf("%d group-style edge(s) not walked: the server's Check says the user is not in them", ruled))
		}
		truncated := more || evaluated < len(edges)
		settled := found && !w.opt.AllPaths // a grant ends a short-circuiting walk
		if truncated && !settled {
			n.Truncated = &Truncation{Examined: evaluated, Found: len(edges), Read: len(recs), More: more, What: "userset", Object: object, Relation: rel.Name}
			resolved, ok := w.probeUsersets(ctx, n, object, rel, usersets, examined, depth)
			if ok {
				found = true
			}
			if !resolved {
				n.Incomplete = n.Truncated.Summary() + "; the remaining edges could not all be ruled out"
				if !found {
					undecided = true
				}
			}
		}
		if len(edges) == 0 && err == nil && !more {
			n.Notes = append(n.Notes, "no userset tuples")
		}
	}

	for _, l := range n.Children {
		if l.Outcome == Unknown || l.Outcome == Errored {
			undecided = true
		}
	}
	switch {
	case found:
		n.Outcome, n.Reason = Allowed, "a direct tuple grants access"
	case undecided:
		n.Outcome, n.Reason = Unknown, "not every direct edge could be examined"
		for _, l := range n.Children {
			if l.Outcome == Errored && l.Cond != nil {
				n.Outcome, n.Reason = Errored, "condition could not be evaluated: "+l.Cond.Summary()
			}
		}
	default:
		n.Outcome, n.Reason = Denied, "no direct tuple grants access ("+strings.Join(n.Notes, "; ")+")"
	}
	return n
}

func (w *walker) subjectTypeLabel() string {
	if w.subj.Relation != "" {
		return w.subj.Type + "#" + w.subj.Relation
	}
	return w.subj.Type
}

func usersetLabel(r record) string {
	label := r.key.GetRelation()
	if s, err := schema.ParseSubject(r.key.GetUser()); err == nil && s.Relation != "" {
		label += " via #" + s.Relation
	}
	return label
}

// ruledOutEdge records a userset edge that guided evaluation did not walk
// because the server's Check says the user does not hold the userset
// (e.g. is not a member of that group).
func (w *walker) ruledOutEdge(r record) *Link {
	l, _ := w.tupleLink(r, usersetLabel(r)) // unconditional: always ok
	s, _ := schema.ParseSubject(r.key.GetUser())
	obj := s.Type + ":" + s.ID
	n := w.node(KindSkipped, r.key.GetUser())
	n.Object, n.Relation = obj, s.Relation
	n.Outcome, n.Hint = Skipped, Denied
	n.Reason = fmt.Sprintf("not evaluated: the server's Check says %s is not %s of %s", w.req.User, s.Relation, obj)
	n.Checked = []BranchCheck{{Object: obj, Relation: s.Relation, Known: true, Allowed: false}}
	l.Label, l.Node, l.Outcome = "not evaluated", n, Skipped
	return l
}

func (w *walker) evalUsersetEdge(ctx context.Context, n *Node, r record, probe bool, depth int) bool {
	l, ok := w.tupleLink(r, usersetLabel(r))
	l.Probe = probe
	if !ok {
		l.Node = w.blockedNode(l)
		w.link(n, l)
		return false
	}
	s, _ := schema.ParseSubject(r.key.GetUser())
	child := w.checkRelation(ctx, s.Type+":"+s.ID, s.Relation, depth+1)
	l.Node, l.Outcome = child, child.Outcome
	w.link(n, l)
	return child.Outcome == Allowed
}

// probeUsersets finds the userset edges that can matter when there are too many
// to examine: ListObjects returns the usersets (e.g. groups) the user belongs
// to, and each candidate is looked up with an exact read. resolved reports
// whether the probe proved that no unexamined edge grants access.
func (w *walker) probeUsersets(ctx context.Context, n *Node, object string, rel *schema.Relation, refs []*openfgav1.RelationReference, examined map[string]bool, depth int) (resolved, found bool) {
	resolved = true
	for _, ref := range refs {
		cands, complete, err := w.listObjects(ctx, ref.GetType(), ref.GetRelation())
		if err != nil {
			n.Notes = append(n.Notes, fmt.Sprintf("probe %s#%s failed: %v", ref.GetType(), ref.GetRelation(), err))
			resolved = false
			continue
		}
		var ahead []readKey
		for i, c := range cands {
			if i >= w.opt.ProbeLimit {
				break
			}
			if u := c + "#" + ref.GetRelation(); !examined[u] {
				ahead = append(ahead, readKey{object, rel.Name, u, ""})
			}
		}
		w.prefetch(ctx, ahead)
		looked := 0
		for _, c := range cands {
			u := c + "#" + ref.GetRelation()
			if examined[u] {
				continue
			}
			if looked >= w.opt.ProbeLimit {
				resolved = false
				break
			}
			looked++
			recs, _, err := w.read(ctx, object, rel.Name, u, "")
			if err != nil {
				resolved = false
				break
			}
			for _, r := range recs {
				examined[u] = true
				if w.evalUsersetEdge(ctx, n, r, true, depth) {
					found = true
				}
			}
			if found && !w.opt.AllPaths {
				break
			}
		}
		if !complete {
			resolved = false
		}
		n.Notes = append(n.Notes, fmt.Sprintf("probe: user is %s of %d %s object(s); %d looked up", ref.GetRelation(), len(cands), ref.GetType(), looked))
		if found && !w.opt.AllPaths {
			return true, true
		}
	}
	return resolved, found
}

// listObjects lists objects of a type the user has a relation with. complete
// is false when the result may have been capped by the server.
func (w *walker) listObjects(ctx context.Context, typ, relation string) ([]string, bool, error) {
	if err := w.budget(); err != nil {
		return nil, false, err
	}
	w.calls.Add(1)
	w.listCalls.Add(1)
	req := &openfgav1.ListObjectsRequest{
		StoreId: w.req.Store, AuthorizationModelId: w.req.Model,
		Type: typ, Relation: relation, User: w.req.User,
		Context: w.req.Context, Consistency: w.req.Consistency,
	}
	req.ContextualTuples = w.contextual()
	resp, err := w.api.ListObjects(ctx, req)
	if err != nil {
		return nil, false, err
	}
	objs := resp.GetObjects()
	return objs, len(objs) < fga.ListMax, nil
}

func (w *walker) ttu(ctx context.Context, object string, rel *schema.Relation, t *openfgav1.TupleToUserset, depth int) *Node {
	tupleset := t.GetTupleset().GetRelation()
	target := t.GetComputedUserset().GetRelation()
	n := w.node(KindTTU, target+" from "+tupleset)
	n.Object, n.Relation = object, rel.Name
	w.step(Step{Phase: Enter, Node: n.ID})
	defer func() { w.step(Step{Phase: Exit, Node: n.ID, Outcome: n.Outcome}) }()

	recs, more, err := w.readAll(ctx, object, tupleset)
	found, undecided := false, false
	var ahead []readKey
	for i, r := range recs {
		if i >= w.opt.MaxFanout {
			break
		}
		ahead = append(ahead, w.plannedReads(r.key.GetUser(), target)...)
	}
	w.prefetch(ctx, ahead)
	if err != nil {
		undecided = true
		n.Notes = append(n.Notes, "read failed: "+err.Error())
	}
	evaluated := 0
	examined := map[string]bool{}
	var targetTypes []string
	for _, r := range recs {
		if found && !w.opt.AllPaths {
			break
		}
		u := r.key.GetUser()
		typ, _, _ := schema.SplitObject(u)
		if _, ok := w.model.Relation(typ, target); !ok {
			continue
		}
		if evaluated >= w.opt.MaxFanout {
			more = true
			break
		}
		evaluated++
		examined[u] = true
		if w.evalTTUEdge(ctx, n, r, target, false, depth) {
			found = true
		}
	}
	settled := found && !w.opt.AllPaths // a grant ends a short-circuiting walk
	if (more || evaluated < len(recs)) && !settled {
		n.Truncated = &Truncation{Examined: evaluated, Found: len(recs), Read: len(recs), More: more, What: tupleset, Object: object, Relation: tupleset}
		if tr, ok := w.model.Relation(rel.Type, tupleset); ok {
			for _, ref := range tr.DirectTypes {
				if _, has := w.model.Relation(ref.GetType(), target); has {
					targetTypes = append(targetTypes, ref.GetType())
				}
			}
		}
		resolved := true
		for _, tt := range targetTypes {
			cands, complete, err := w.listObjects(ctx, tt, target)
			if err != nil || !complete {
				resolved = false
			}
			var ahead []readKey
			for i, c := range cands {
				if i < w.opt.ProbeLimit && !examined[c] {
					ahead = append(ahead, readKey{object, tupleset, c, ""})
				}
			}
			w.prefetch(ctx, ahead)
			looked := 0
			for _, c := range cands {
				if examined[c] || (found && !w.opt.AllPaths) {
					continue
				}
				if looked >= w.opt.ProbeLimit {
					resolved = false
					break
				}
				looked++
				prs, _, err := w.read(ctx, object, tupleset, c, "")
				if err != nil {
					resolved = false
					break
				}
				for _, r := range prs {
					if w.evalTTUEdge(ctx, n, r, target, true, depth) {
						found = true
					}
				}
			}
			n.Notes = append(n.Notes, fmt.Sprintf("probe: user is %s of %d %s object(s); %d looked up", target, len(cands), tt, looked))
		}
		if !resolved {
			n.Incomplete = n.Truncated.Summary() + "; the remaining edges could not all be ruled out"
			if !found {
				undecided = true
			}
		}
	}
	for _, l := range n.Children {
		if l.Outcome == Unknown || l.Outcome == Errored {
			undecided = true
		}
	}
	switch {
	case found:
		n.Outcome, n.Reason = Allowed, fmt.Sprintf("granted through %s", tupleset)
	case undecided:
		n.Outcome, n.Reason = Unknown, "not every "+tupleset+" edge could be examined"
	case len(recs) == 0 && err == nil:
		n.Outcome, n.Reason = Denied, fmt.Sprintf("no %s tuples on %s", tupleset, object)
	default:
		n.Outcome, n.Reason = Denied, fmt.Sprintf("the user is not %s of any %s of %s", target, tupleset, object)
	}
	return n
}

func (w *walker) evalTTUEdge(ctx context.Context, n *Node, r record, target string, probe bool, depth int) bool {
	l, ok := w.tupleLink(r, r.key.GetRelation())
	l.Probe = probe
	if !ok {
		l.Node = w.blockedNode(l)
		w.link(n, l)
		return false
	}
	child := w.checkRelation(ctx, r.key.GetUser(), target, depth+1)
	l.Node, l.Outcome = child, child.Outcome
	w.link(n, l)
	return child.Outcome == Allowed
}

// verify runs server Checks for relation nodes and records mismatches.
func (w *walker) verify(ctx context.Context, root *Node) int {
	if w.opt.Verify == VerifyNone {
		return 0
	}
	var targets []*Node
	for _, n := range w.relNodes {
		switch {
		case n == root:
			targets = append(targets, n)
		case w.opt.Verify == VerifyAll, n.Outcome == Unknown || n.Outcome == Errored:
			targets = append(targets, n)
		}
		if len(targets) >= w.opt.VerifyLimit {
			break
		}
	}
	// Relations the guidance already checked need no second call.
	var remaining []*Node
	w.mu.Lock()
	for _, n := range targets {
		if v, ok := w.known[n.Object+"#"+n.Relation]; ok {
			w.setVerdict(n, v, nil)
		} else {
			remaining = append(remaining, n)
		}
	}
	w.mu.Unlock()
	targets = remaining
	if bc, ok := w.api.(fga.BatchChecker); ok && w.verifyBatch(ctx, bc, targets) {
		return len(targets)
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, n := range targets {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			req := &openfgav1.CheckRequest{
				StoreId: w.req.Store, AuthorizationModelId: w.req.Model,
				TupleKey: w.checkKey(n.Object, n.Relation), ContextualTuples: w.contextual(),
				Context: w.req.Context, Consistency: w.req.Consistency,
			}
			resp, err := w.api.Check(ctx, req)
			w.setVerdict(n, resp.GetAllowed(), err)
		})
	}
	wg.Wait()
	return len(targets)
}

// batchSize matches OpenFGA's default maximum checks per BatchCheck request.
const batchSize = 50

// verifyBatch verifies nodes with BatchCheck, a few requests instead of one
// per node. It reports false (so the caller falls back to individual Checks)
// when the server does not support BatchCheck.
func (w *walker) verifyBatch(ctx context.Context, bc fga.BatchChecker, targets []*Node) bool {
	ctuples := w.contextual()
	type chunk struct{ nodes []*Node }
	var chunks []chunk
	for i := 0; i < len(targets); i += batchSize {
		chunks = append(chunks, chunk{targets[i:min(i+batchSize, len(targets))]})
	}
	var wg sync.WaitGroup
	var unsupported atomic.Bool
	for _, c := range chunks {
		wg.Go(func() {
			req := &openfgav1.BatchCheckRequest{StoreId: w.req.Store, AuthorizationModelId: w.req.Model, Consistency: w.req.Consistency}
			for i, n := range c.nodes {
				req.Checks = append(req.Checks, &openfgav1.BatchCheckItem{
					TupleKey:         w.checkKey(n.Object, n.Relation),
					ContextualTuples: ctuples, Context: w.req.Context, CorrelationId: strconv.Itoa(i),
				})
			}
			resp, err := bc.BatchCheck(ctx, req)
			var fe *fga.Error
			if errors.As(err, &fe) && (fe.Status == 404 || fe.Status == 501 || fe.Code == "unimplemented") {
				unsupported.Store(true)
				return
			}
			for i, n := range c.nodes {
				if err != nil {
					w.setVerdict(n, false, err)
					continue
				}
				r := resp.GetResult()[strconv.Itoa(i)]
				var rerr error
				if e := r.GetError(); e != nil {
					rerr = errors.New(e.GetMessage())
				}
				w.setVerdict(n, r.GetAllowed(), rerr)
			}
		})
	}
	wg.Wait()
	return !unsupported.Load()
}

func (w *walker) setVerdict(n *Node, allowed bool, err error) {
	v := &ServerVerdict{}
	if err != nil {
		v.Err = err.Error()
	} else {
		v.Allowed = allowed
		v.Mismatch = (n.Outcome == Allowed && !allowed) || (n.Outcome == Denied && allowed)
	}
	n.Server = v
}

// Gaps lists the reasons the walk may have missed granting paths: the call
// budget or deadline ran out, edge lists were cut short without a probe
// proving the rest irrelevant, or branches stayed undecided. An empty result
// means every branch the walk visited was examined completely.
func (t *Trace) Gaps() []string {
	var out []string
	if t.Exhausted {
		out = append(out, fmt.Sprintf("the call budget (%d calls) or time limit ran out", t.Options.MaxCalls))
	}
	seen := map[int]bool{}
	undecided := 0
	var walk func(n *Node)
	walk = func(n *Node) {
		if seen[n.ID] {
			return
		}
		seen[n.ID] = true
		if n.Incomplete != "" {
			where := n.Object + "#" + n.Relation
			out = append(out, where+": "+n.Incomplete)
		}
		if (n.Kind == KindRelation) && (n.Outcome == Unknown || n.Outcome == Errored) {
			undecided++
		}
		for _, l := range n.Children {
			if l.Node != nil {
				walk(l.Node)
			}
		}
	}
	walk(t.Root)
	if undecided > 0 {
		out = append(out, fmt.Sprintf("%d relation(s) stayed undecided (amber)", undecided))
	}
	return out
}

// contextual is the request's contextual tuples as the API takes them (nil
// when there are none).
func (w *walker) contextual() *openfgav1.ContextualTupleKeys {
	if len(w.req.Contextual) == 0 {
		return nil
	}
	return &openfgav1.ContextualTupleKeys{TupleKeys: w.req.Contextual}
}

// checkKey is the tuple key for checking the request's user against
// object#relation.
func (w *walker) checkKey(object, relation string) *openfgav1.CheckRequestTupleKey {
	return &openfgav1.CheckRequestTupleKey{User: w.req.User, Relation: relation, Object: object}
}
