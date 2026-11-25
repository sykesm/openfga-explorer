package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sykesm/openfga-explorer/internal/trace"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/sykesm/openfga-explorer/internal/scenario"
	"github.com/sykesm/openfga-explorer/internal/schema"
)

// Scenarios run in a fresh in-memory engine per request (see package
// scenario): nothing is stored, so any replica can serve any run.

const (
	maxScenarioBytes = 1 << 20
	scenarioTimeout  = 10 * time.Second
)

type scenarioPage struct {
	Template string // a starter document for the viewed model
}

type scenarioResult struct {
	Report *scenario.Report
	Err    string
}

func (s *Server) handleScenarios(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	s.withStoreName(r.Context(), v)
	v.Title, v.Active, v.Data = "Scenarios", "scenarios", &scenarioPage{Template: scenarioTemplate(v.Model)}
	s.render(w, "scenarios", v)
}

func (s *Server) handleScenarioRun(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxScenarioBytes)
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusRequestEntityTooLarge, fmt.Errorf("the scenario is larger than %d KB", maxScenarioBytes>>10))
		return
	}
	res := &scenarioResult{}
	v.Data = res
	doc, err := scenario.Parse([]byte(r.PostForm.Get("doc")))
	if err == nil {
		err = doc.Validate(scenario.DefaultLimits)
	}
	if err != nil {
		res.Err = err.Error()
		s.render(w, "scenario-result", v)
		return
	}
	select {
	case s.sandboxSem <- struct{}{}:
		defer func() { <-s.sandboxSem }()
	case <-time.After(5 * time.Second):
		s.fail(w, r, http.StatusTooManyRequests, errors.New("too many scenario runs; try again shortly"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scenarioTimeout)
	defer cancel()
	report, err := scenario.Run(ctx, doc, v.Model.Proto)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		res.Err = fmt.Sprintf("the run took longer than %v; try fewer tuples or expectations", scenarioTimeout)
	case err != nil:
		res.Err = err.Error()
	default:
		res.Report = report
		s.log.Info("scenario", "identity", v.User.Name, "store", v.StoreID, "tests", len(report.Tests),
			"passed", report.Passed, "failed", report.Failed, "duration", report.Duration)
	}
	s.render(w, "scenario-result", v)
}

// templateRelations are relation names that make a familiar first example,
// with how strongly to prefer each.
var templateRelations = map[string]int{"viewer": 6, "reader": 6, "member": 4, "editor": 4, "writer": 4, "owner": 3}

// templateExample picks the starter scenario's tuple: a person-like user
// type (a type with no relations of its own, ideally "user") holding a
// familiar relation (viewer, member, …) directly on a richer object type.
// Among equals, the earlier type and relation in the model wins. Without
// any plain direct relation it falls back to user / viewer / document.
func templateExample(m *schema.Model) (user, rel, typ string) {
	user, rel, typ = "user", "viewer", "document"
	best := -1
	for _, tn := range m.TypeNames {
		t := m.Types[tn]
		for _, rn := range t.RelationNames {
			for _, ref := range t.Relations[rn].DirectTypes {
				u := ref.GetType()
				if ref.GetRelation() != "" || ref.GetWildcard() != nil || ref.GetCondition() != "" || u == tn {
					continue
				}
				score := templateRelations[rn]*10 + min(len(t.RelationNames), 9)
				if ut, ok := m.Types[u]; ok && len(ut.RelationNames) == 0 {
					score += 200 // a leaf type: people, services
				}
				if u == "user" {
					score += 100
				}
				if score > best {
					best, user, rel, typ = score, u, rn, tn
				}
			}
		}
	}
	return user, rel, typ
}

// scenarioTemplate is a starter document for the model: one tuple from
// templateExample, and checks that its user has the relation and another
// user does not.
func scenarioTemplate(m *schema.Model) string {
	user, rel, typ := templateExample(m)
	var b strings.Builder
	fmt.Fprintf(&b, `# A scenario: tuples, and what should (and shouldn't) be allowed.
# Same format as `+"`fga model test`"+` (.fga.yaml); JSON works too.
# Without a "model:" it runs against the model you are viewing, with ONLY
# these tuples (never the store's data).
name: My scenario
tuples:
  - user: %[1]s:alice
    relation: %[2]s
    object: %[3]s:example
tests:
  - name: direct %[2]s
    check:
      - user: %[1]s:alice
        object: %[3]s:example
        assertions:
          %[2]s: true
      - user: %[1]s:bob
        object: %[3]s:example
        assertions:
          %[2]s: false
`, user, rel, typ)
	return b.String()
}

// captureResponse returns the draft with the captured test added.
type captureResponse struct {
	Draft   string `json:"draft,omitempty"`
	Name    string `json:"name,omitempty"`
	Tuples  int    `json:"tuples"`
	Allowed bool   `json:"allowed"`
	Error   string `json:"error,omitempty"`
}

// handleScenarioCapture turns a check into a scenario test (the expectation
// plus the tuples on its granting path) and adds it to the draft the
// browser sends, optionally renaming every ID. The draft stays the
// browser's: it is sent, merged, and returned; nothing is kept here.
func (s *Server) handleScenarioCapture(w http.ResponseWriter, r *http.Request) {
	v, me, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxScenarioBytes)
	reply := func(code int, resp captureResponse) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}
	if err := r.ParseForm(); err != nil {
		reply(http.StatusRequestEntityTooLarge, captureResponse{Error: "the draft is too large"})
		return
	}
	f := r.PostForm
	req := trace.Request{Store: v.StoreID, Model: v.ModelID, User: f.Get("user"), Relation: f.Get("relation"), Object: f.Get("object")}
	if req.User == "" || req.Relation == "" || req.Object == "" {
		reply(http.StatusBadRequest, captureResponse{Error: "user, relation, and object are required"})
		return
	}
	var ctxMap map[string]any
	if c := strings.TrimSpace(f.Get("context")); c != "" {
		if err := json.Unmarshal([]byte(c), &ctxMap); err != nil {
			reply(http.StatusBadRequest, captureResponse{Error: "the request context isn't a JSON object"})
			return
		}
		if req.Context, err = structpb.NewStruct(ctxMap); err != nil {
			reply(http.StatusBadRequest, captureResponse{Error: err.Error()})
			return
		}
	}
	if req.Contextual, err = parseContextual(f.Get("contextual")); err != nil {
		reply(http.StatusBadRequest, captureResponse{Error: err.Error()})
		return
	}
	select {
	case s.traceSem <- struct{}{}:
		defer func() { <-s.traceSem }()
	case <-time.After(5 * time.Second):
		reply(http.StatusTooManyRequests, captureResponse{Error: "too many traces are running; try again shortly"})
		return
	}
	opt := trace.DefaultOptions()
	opt.Verify = trace.VerifyRoot
	opt.MaxCalls, opt.Timeout = s.cfg.Trace.MaxCalls, s.cfg.Trace.Timeout
	tr, err := trace.Explain(r.Context(), s.walkAPI, me.m, me.ev, req, opt)
	if err != nil {
		reply(http.StatusBadRequest, captureResponse{Error: err.Error()})
		return
	}
	c := scenario.Capture{User: req.User, Relation: req.Relation, Object: req.Object, Allowed: tr.Decision() == trace.Allowed, Context: ctxMap}
	for _, pt := range tr.GrantingPathTuples() {
		tp := scenario.Tuple{User: pt.User, Relation: pt.Relation, Object: pt.Object}
		if pt.Condition != "" {
			tp.Condition = &scenario.Condition{Name: pt.Condition, Context: pt.Context}
		}
		c.Tuples = append(c.Tuples, tp)
	}
	desc := fmt.Sprintf("Captured from a check on %s: %s.", time.Now().UTC().Format("2006-01-02"), tr.Decision())
	if !c.Allowed {
		desc += " Add tuples that should still not grant it."
	}
	if c.Allowed && tr.Root.Outcome != trace.Allowed {
		desc += " The walk did not find the whole granting path within its limits, so some tuples may be missing."
	}
	test := c.Test(desc)
	if f.Get("mask") != "" {
		scenario.Mask(&test)
	}
	draft, err := scenario.AppendTest([]byte(f.Get("draft")), test, "Captured checks")
	if err != nil {
		reply(http.StatusBadRequest, captureResponse{Error: err.Error()})
		return
	}
	reply(http.StatusOK, captureResponse{Draft: string(draft), Name: test.Name, Tuples: len(test.Tuples), Allowed: c.Allowed})
}
