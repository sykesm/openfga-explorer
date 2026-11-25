package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// The impact of a model change on real people: for one relation, sample
// objects of its type from the store, list who holds the relation on each
// under the old and the new model (ListUsers takes a model ID), and report
// who loses or gains it. Admin-only and bounded (see livequery.go).

type impactResult struct {
	Type, Relation string
	BaseID, NewID  string
	Objects        []objectImpact
	Sampled        int
	Lost, Gained   int // people (and usersets) across the sampled objects
	Truncated      bool
	Err            string
	Allowed        bool
	Calls          int
	Elapsed        time.Duration
	Note           string // how the sample was taken
}

type objectImpact struct {
	Object       string
	Lost, Gained []string // the first impactShown of each
	// LostMore and GainedMore count the rest, which are not listed.
	LostMore, GainedMore int
	Errors               []string
}

// impactShown bounds the people listed per object and direction, so a change
// that affects thousands stays a readable (and small) page.
const impactShown = 20

// shown returns the first n of a sorted list and how many were left out.
func shown(list []string, n int) ([]string, int) {
	if len(list) <= n {
		return list, 0
	}
	return list[:n], len(list) - n
}

// changeLogScanPages bounds the change-log pages read to find objects.
const changeLogScanPages = 5

func (s *Server) handleImpact(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	q := r.URL.Query()
	res := &impactResult{Type: q.Get("type"), Relation: q.Get("relation"), BaseID: q.Get("base"), NewID: v.ModelID, Allowed: s.liveAllowed(r)}
	v.Data = res
	if !res.Allowed {
		res.Err = errNotAllowed.Error()
		s.render(w, "impact", v)
		return
	}
	base, err := s.model(r.Context(), v.StoreID, res.BaseID)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, fmt.Errorf("base model %s: %w", res.BaseID, err))
		return
	}
	// The kinds of user to list: any that can hold the relation in either version.
	kinds := map[string]bool{}
	for _, k := range append(base.m.Holders(res.Type, res.Relation), v.Model.Holders(res.Type, res.Relation)...) {
		kinds[k] = true
	}
	if len(kinds) == 0 {
		res.Err = fmt.Sprintf("no kind of user can hold %s#%s in either version", res.Type, res.Relation)
		s.render(w, "impact", v)
		return
	}
	var reqCtx *structpb.Struct
	if c := q.Get("context"); c != "" {
		if reqCtx, err = parseContext(c); err != nil {
			res.Err = "request context: " + err.Error()
			s.render(w, "impact", v)
			return
		}
	}
	id := identity(r)
	done, err := s.live.start(id.Name, s.cfg.LiveQueries.RunsPerHour, time.Now())
	if err != nil {
		res.Err = err.Error()
		s.render(w, "impact", v)
		return
	}
	defer done()
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.LiveQueries.Timeout)
	defer cancel()
	b := &budget{max: int64(s.cfg.LiveQueries.MaxChecks)}
	start := time.Now()
	// Record the run's cost, then render: the page shows the calls and time.
	defer func() {
		res.Calls, res.Elapsed = int(b.used.Load()), time.Since(start)
		s.log.Info("live query", "kind", "impact", "identity", id.Name, "store", v.StoreID, "relation", res.Type+"#"+res.Relation,
			"base", res.BaseID, "objects", res.Sampled, "lost", res.Lost, "gained", res.Gained, "calls", res.Calls, "duration", res.Elapsed)
		s.render(w, "impact", v)
	}()

	objects, err := s.sampleObjects(ctx, b, v.StoreID, res.Type, s.cfg.LiveQueries.MaxObjects)
	if err != nil {
		res.Err = "sampling objects: " + err.Error()
		return
	}
	res.Sampled = len(objects)
	res.Note = fmt.Sprintf("%d %s object(s) taken across those in the store's change log (up to %d pages of changes)", len(objects), res.Type, changeLogScanPages)
	var kindList []string
	for k := range kinds {
		kindList = append(kindList, k)
	}
	sort.Strings(kindList)
	for _, obj := range objects {
		oi := objectImpact{Object: obj}
		old, oldErr := s.holders(ctx, b, v.StoreID, res.BaseID, obj, res.Relation, kindList, reqCtx, &res.Truncated)
		now, newErr := s.holders(ctx, b, v.StoreID, res.NewID, obj, res.Relation, kindList, reqCtx, &res.Truncated)
		oi.Errors = append(oldErr, newErr...)
		for u := range old {
			if !now[u] {
				oi.Lost = append(oi.Lost, u)
			}
		}
		for u := range now {
			if !old[u] {
				oi.Gained = append(oi.Gained, u)
			}
		}
		sort.Strings(oi.Lost)
		sort.Strings(oi.Gained)
		res.Lost += len(oi.Lost)
		res.Gained += len(oi.Gained)
		oi.Lost, oi.LostMore = shown(oi.Lost, impactShown)
		oi.Gained, oi.GainedMore = shown(oi.Gained, impactShown)
		res.Objects = append(res.Objects, oi)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			res.Err = fmt.Sprintf("stopped after %d object(s): the run's %v time limit was reached", len(res.Objects), s.cfg.LiveQueries.Timeout)
			break
		}
		if b.used.Load() >= b.max {
			res.Err = fmt.Sprintf("stopped after %d object(s): the run used its %d-call budget", len(res.Objects), b.max)
			break
		}
	}
}

// sampleObjects picks up to n distinct objects of a type, spread across
// those appearing in the first pages of the store's change log.
func (s *Server) sampleObjects(ctx context.Context, b *budget, store, typ string, n int) ([]string, error) {
	seen := map[string]bool{}
	var all []string
	token := ""
	for page := 0; page < changeLogScanPages; page++ {
		if err := b.spend(1); err != nil {
			break
		}
		resp, err := s.fga.ReadChanges(ctx, &openfgav1.ReadChangesRequest{StoreId: store, Type: typ, PageSize: wrapperspb.Int32(100), ContinuationToken: token})
		if err != nil {
			return nil, err
		}
		for _, c := range resp.GetChanges() {
			if o := c.GetTupleKey().GetObject(); !seen[o] {
				seen[o] = true
				all = append(all, o)
			}
		}
		token = resp.GetContinuationToken()
		if len(resp.GetChanges()) < 100 || token == "" {
			break
		}
	}
	if len(all) <= n {
		return all, nil
	}
	// Spread the picks evenly over what was found.
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, all[i*len(all)/n])
	}
	return out, nil
}

// holders lists who holds a relation on an object under a model, one
// ListUsers call per kind of user ("user", "group#member").
func (s *Server) holders(ctx context.Context, b *budget, store, model, object, rel string, kinds []string, reqCtx *structpb.Struct, truncated *bool) (map[string]bool, []string) {
	out := map[string]bool{}
	var errs []string
	typ, id, _ := schema.SplitObject(object)
	for _, k := range kinds {
		if err := b.spend(1); err != nil {
			errs = append(errs, err.Error())
			break
		}
		ft, fr, _ := strings.Cut(k, "#")
		resp, err := s.fga.ListUsers(ctx, &openfgav1.ListUsersRequest{StoreId: store, AuthorizationModelId: model,
			Object: &openfgav1.Object{Type: typ, Id: id}, Relation: rel, Context: reqCtx,
			UserFilters: []*openfgav1.UserTypeFilter{{Type: ft, Relation: fr}}})
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s (%s, model %s): %v", k, object, short(model), err))
			continue
		}
		if len(resp.GetUsers()) >= listObjectsMax {
			*truncated = true
		}
		for _, u := range resp.GetUsers() {
			out[userString(u)] = true
		}
	}
	return out, errs
}

func short(s string) string {
	if len(s) > 10 {
		return s[:10] + "…"
	}
	return s
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
