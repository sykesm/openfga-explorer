package scenario

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
	"github.com/openfga/openfga/pkg/server"
	"github.com/openfga/openfga/pkg/storage/memory"
	"github.com/sykesm/openfga-explorer/internal/cond"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

// Report is the outcome of a run.
type Report struct {
	Name string
	// ModelFrom says which model ran: "the document" or "the viewed model".
	ModelFrom      string
	Tests          []TestReport
	Passed, Failed int
	Duration       time.Duration
}

// TestReport is one test's outcome.
type TestReport struct {
	Name, Description string
	// Tuples are the tuples the test ran with: the document's, then its own.
	Tuples      []TupleReport
	Checks      []CheckResult
	ListObjects []SetResult
	ListUsers   []SetResult
	// Err is set when the test could not run (e.g. a tuple the model rejects).
	Err            string
	Passed, Failed int
}

// TupleReport is a tuple and the checks it helped grant.
type TupleReport struct {
	Tuple
	Shared bool  // from the document, not the test
	UsedBy []int // indices into Checks
}

// CheckResult is one user/relation/object expectation.
type CheckResult struct {
	User, Relation, Object string
	Expected               bool
	Got                    bool
	Pass                   bool
	Err                    string
	// Because explains the result in a sentence (from the explorer's walk).
	Because string
	// GrantedBy are indices into the test's Tuples on the granting path.
	GrantedBy []int
}

// SetResult compares an expected list (ListObjects or ListUsers) with the result.
type SetResult struct {
	Subject, Relation string // the user (ListObjects) or object (ListUsers), and the relation
	Expected, Got     []string
	Missing, Extra    []string
	Pass              bool
	Err               string
}

// Run executes the document's tests. base is the model to use when the
// document has none (the viewed model).
func Run(ctx context.Context, d *Document, base *openfgav1.AuthorizationModel) (*Report, error) {
	start := time.Now()
	r := &Report{Name: d.Name, ModelFrom: "the viewed model"}
	model := base
	if strings.TrimSpace(d.Model) != "" {
		pm, err := transformer.TransformDSLToProto(d.Model)
		if err != nil {
			return nil, fmt.Errorf("%w: model: %w", ErrInvalid, err)
		}
		model, r.ModelFrom = viaJSON(pm), "the document"
	}
	if model == nil {
		return nil, fmt.Errorf("%w: the document has no model and none is being viewed", ErrInvalid)
	}
	sm, err := schema.Load(model)
	if err != nil {
		return nil, err
	}
	// One engine per run, one store per test; all of it is discarded on return.
	engine, err := server.NewServerWithOpts(server.WithDatastore(memory.New()))
	if err != nil {
		return nil, err
	}
	defer engine.Close()
	ev := cond.New(sm.Conditions)
	for _, t := range d.Tests {
		tr := runTest(ctx, engine, model, sm, ev, d.Tuples, t)
		r.Passed += tr.Passed
		r.Failed += tr.Failed
		r.Tests = append(r.Tests, tr)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	r.Duration = time.Since(start)
	return r, nil
}

func runTest(ctx context.Context, engine *server.Server, model *openfgav1.AuthorizationModel, sm *schema.Model, ev *cond.Evaluator, shared []Tuple, t Test) TestReport {
	tr := TestReport{Name: t.Name, Description: t.Description}
	for _, tp := range shared {
		tr.Tuples = append(tr.Tuples, TupleReport{Tuple: tp, Shared: true})
	}
	for _, tp := range t.Tuples {
		tr.Tuples = append(tr.Tuples, TupleReport{Tuple: tp})
	}
	fail := func(err error) TestReport {
		tr.Err = err.Error()
		tr.Failed = 1
		return tr
	}
	st, err := engine.CreateStore(ctx, &openfgav1.CreateStoreRequest{Name: "scenario"})
	if err != nil {
		return fail(err)
	}
	wm, err := engine.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{StoreId: st.GetId(),
		SchemaVersion: model.GetSchemaVersion(), TypeDefinitions: model.GetTypeDefinitions(), Conditions: model.GetConditions()})
	if err != nil {
		return fail(fmt.Errorf("the model: %w", err))
	}
	keys := make([]*openfgav1.TupleKey, 0, len(tr.Tuples))
	for _, tp := range tr.Tuples {
		k, err := tp.key()
		if err != nil {
			return fail(err)
		}
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i += 100 {
		if _, err := engine.Write(ctx, &openfgav1.WriteRequest{StoreId: st.GetId(), AuthorizationModelId: wm.GetAuthorizationModelId(),
			Writes: &openfgav1.WriteRequestWrites{TupleKeys: keys[i:min(i+100, len(keys))]}}); err != nil {
			return fail(fmt.Errorf("tuples %d–%d: %w", i+1, min(i+100, len(keys)), err))
		}
	}
	store, modelID := st.GetId(), wm.GetAuthorizationModelId()

	for _, c := range t.Check {
		users, objects := c.Users, c.Objects
		if c.User != "" {
			users = []string{c.User}
		}
		if c.Object != "" {
			objects = []string{c.Object}
		}
		reqCtx, err := toStruct(c.Context)
		for _, u := range users {
			for _, o := range objects {
				for _, rel := range sortedKeys(c.Assertions) {
					res := CheckResult{User: u, Relation: rel, Object: o, Expected: c.Assertions[rel]}
					if err != nil {
						res.Err = "context: " + err.Error()
					} else {
						explain(ctx, engine, sm, ev, store, modelID, reqCtx, &res, &tr)
					}
					tr.Checks = append(tr.Checks, res)
				}
			}
		}
	}
	for _, lo := range t.ListObjects {
		reqCtx, err := toStruct(lo.Context)
		for _, rel := range sortedKeys(lo.Assertions) {
			res := SetResult{Subject: lo.User, Relation: rel, Expected: sorted(lo.Assertions[rel])}
			if err == nil {
				var resp *openfgav1.ListObjectsResponse
				resp, err = engine.ListObjects(ctx, &openfgav1.ListObjectsRequest{StoreId: store, AuthorizationModelId: modelID,
					User: lo.User, Relation: rel, Type: lo.Type, Context: reqCtx})
				if err == nil {
					res.Got = sorted(resp.GetObjects())
				}
			}
			tr.ListObjects = append(tr.ListObjects, compare(res, err))
		}
	}
	for _, lu := range t.ListUsers {
		reqCtx, err := toStruct(lu.Context)
		typ, id, _ := schema.SplitObject(lu.Object)
		var filters []*openfgav1.UserTypeFilter
		for _, f := range lu.UserFilter {
			filters = append(filters, &openfgav1.UserTypeFilter{Type: f.Type, Relation: f.Relation})
		}
		for _, rel := range sortedKeys(lu.Assertions) {
			res := SetResult{Subject: lu.Object, Relation: rel, Expected: sorted(lu.Assertions[rel].Users)}
			if err == nil {
				var resp *openfgav1.ListUsersResponse
				resp, err = engine.ListUsers(ctx, &openfgav1.ListUsersRequest{StoreId: store, AuthorizationModelId: modelID,
					Object: &openfgav1.Object{Type: typ, Id: id}, Relation: rel, UserFilters: filters, Context: reqCtx})
				if err == nil {
					for _, u := range resp.GetUsers() {
						res.Got = append(res.Got, userString(u))
					}
					res.Got = sorted(res.Got)
				}
			}
			tr.ListUsers = append(tr.ListUsers, compare(res, err))
		}
	}
	for _, c := range tr.Checks {
		count(&tr, c.Pass)
	}
	for _, s := range append(append([]SetResult{}, tr.ListObjects...), tr.ListUsers...) {
		count(&tr, s.Pass)
	}
	return tr
}

func count(tr *TestReport, pass bool) {
	if pass {
		tr.Passed++
	} else {
		tr.Failed++
	}
}

// explain runs the check on the engine and walks it with the explorer's
// walker, recording the explanation and which test tuples granted access.
func explain(ctx context.Context, engine *server.Server, sm *schema.Model, ev *cond.Evaluator, store, modelID string, reqCtx *structpb.Struct, res *CheckResult, tr *TestReport) {
	resp, err := engine.Check(ctx, &openfgav1.CheckRequest{StoreId: store, AuthorizationModelId: modelID, Context: reqCtx,
		TupleKey: &openfgav1.CheckRequestTupleKey{User: res.User, Relation: res.Relation, Object: res.Object}})
	if err != nil {
		res.Err = err.Error()
		return
	}
	res.Got = resp.GetAllowed()
	res.Pass = res.Got == res.Expected
	opt := trace.DefaultOptions()
	opt.Verify = trace.VerifyNone
	opt.AllPaths = false
	walk, err := trace.Walk(ctx, engine, sm, ev, trace.Request{Store: store, Model: modelID, User: res.User, Relation: res.Relation, Object: res.Object, Context: reqCtx}, opt)
	if err != nil {
		return
	}
	res.Because = walk.Explanation(nil).Headline
	if !res.Got {
		return
	}
	used := map[string]bool{}
	for _, k := range walk.PathTuples() {
		used[k] = true
	}
	for i := range tr.Tuples {
		tp := tr.Tuples[i].Tuple
		if used[tp.Object+"#"+tp.Relation+"@"+tp.User] {
			res.GrantedBy = append(res.GrantedBy, i)
			tr.Tuples[i].UsedBy = append(tr.Tuples[i].UsedBy, len(tr.Checks))
		}
	}
}

func compare(res SetResult, err error) SetResult {
	if err != nil {
		res.Err = err.Error()
		return res
	}
	in := func(l []string, s string) bool { // l is sorted
		_, found := slices.BinarySearch(l, s)
		return found
	}
	for _, s := range res.Expected {
		if !in(res.Got, s) {
			res.Missing = append(res.Missing, s)
		}
	}
	for _, s := range res.Got {
		if !in(res.Expected, s) {
			res.Extra = append(res.Extra, s)
		}
	}
	res.Pass = len(res.Missing)+len(res.Extra) == 0
	return res
}

func (t Tuple) key() (*openfgav1.TupleKey, error) {
	k := &openfgav1.TupleKey{User: t.User, Relation: t.Relation, Object: t.Object}
	if t.Condition != nil {
		c, err := toStruct(t.Condition.Context)
		if err != nil {
			return nil, fmt.Errorf("tuple %s: condition context: %w", t, err)
		}
		k.Condition = &openfgav1.RelationshipCondition{Name: t.Condition.Name, Context: c}
	}
	return k, nil
}

func toStruct(m map[string]any) (*structpb.Struct, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return structpb.NewStruct(m)
}

func userString(u *openfgav1.User) string {
	switch {
	case u.GetObject() != nil:
		return u.GetObject().GetType() + ":" + u.GetObject().GetId()
	case u.GetUserset() != nil:
		us := u.GetUserset()
		return us.GetType() + ":" + us.GetId() + "#" + us.GetRelation()
	case u.GetWildcard() != nil:
		return u.GetWildcard().GetType() + ":*"
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func sorted(l []string) []string {
	out := append([]string(nil), l...)
	slices.Sort(out)
	return out
}

// viaJSON normalizes a model built from DSL the way the API would (see
// schema tests: the DSL transformer can leave empty oneof values).
func viaJSON(pm *openfgav1.AuthorizationModel) *openfgav1.AuthorizationModel {
	b, err := protojson.Marshal(pm)
	if err != nil {
		return pm
	}
	out := &openfgav1.AuthorizationModel{}
	if protojson.Unmarshal(b, out) != nil {
		return pm
	}
	return out
}
