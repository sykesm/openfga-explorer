package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"github.com/sykesm/openfga-explorer/internal/trace"
	"google.golang.org/protobuf/types/known/structpb"
)

// "What can this user access?": ListObjects over the store's real tuples for
// a subject (an instance, or one of its usersets), one call per relation.
// Admin-only and bounded (see livequery.go).

type accessPanel struct {
	Object   string
	Subjects []string // the instance and its usersets that can be users
	Subject  string
	Types    []accessType
	Type     string
	Rels     []string // selected relations
	Context  string
	Ran      bool
	Results  []accessResult
	Err      string
	Calls    int
	Elapsed  time.Duration
	Allowed  bool
}

// accessType is an object type and the relations the subject could hold on it.
type accessType struct {
	Name string
	Rels []string
}

type accessResult struct {
	Relation  string
	Objects   []string
	Truncated bool // the server returned its maximum; there may be more
	Err       string
}

// listObjectsMax is OpenFGA's default ListObjects result limit; a result
// that size may be cut off.
const listObjectsMax = 1000

// accessSubjects lists the subjects an instance can be checked as.
func accessSubjects(m *schema.Model, obj string) []string {
	typ, _, _ := schema.SplitObject(obj)
	var out []string
	for _, ut := range m.UserTypes() {
		t, rel, _ := strings.Cut(ut, "#")
		if t != typ {
			continue
		}
		if rel == "" {
			out = append(out, obj)
		} else {
			out = append(out, obj+"#"+rel)
		}
	}
	return out
}

func (s *Server) handleAccess(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	q := r.URL.Query()
	p := &accessPanel{Object: q.Get("object"), Subject: q.Get("subject"), Type: q.Get("type"), Rels: q["rel"], Context: q.Get("context"),
		Allowed: s.liveAllowed(r)}
	v.Data = p
	if !p.Allowed {
		p.Err = errNotAllowed.Error()
		s.render(w, "access", v)
		return
	}
	p.Subjects = accessSubjects(v.Model, p.Object)
	if len(p.Subjects) == 0 {
		p.Err = fmt.Sprintf("%s can't be the user of any relation in this model", p.Object)
		s.render(w, "access", v)
		return
	}
	if !slices.Contains(p.Subjects, p.Subject) {
		p.Subject = p.Subjects[0]
	}
	subj, err := schema.ParseSubject(p.Subject)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err)
		return
	}
	ut := subj.Type
	if subj.Relation != "" {
		ut += "#" + subj.Relation
	}
	for _, tn := range v.Model.TypeNames {
		reach := v.Model.Reachable(tn, ut)
		var rels []string
		for _, rn := range v.Model.Types[tn].RelationNames {
			if reach[rn] {
				rels = append(rels, rn)
			}
		}
		if len(rels) > 0 {
			p.Types = append(p.Types, accessType{Name: tn, Rels: rels})
		}
	}
	var chosen *accessType
	for i := range p.Types {
		if p.Types[i].Name == p.Type {
			chosen = &p.Types[i]
		}
	}
	if chosen == nil && len(p.Types) > 0 {
		chosen = &p.Types[0]
		p.Type = chosen.Name
	}
	var rels []string
	for _, rel := range p.Rels {
		if chosen != nil && slices.Contains(chosen.Rels, rel) {
			rels = append(rels, rel)
		}
	}
	p.Rels = rels
	if q.Get("run") == "" || chosen == nil {
		s.render(w, "access", v)
		return
	}
	if len(p.Rels) == 0 {
		p.Err = "choose at least one relation"
		s.render(w, "access", v)
		return
	}
	var reqCtx *structpb.Struct
	if p.Context != "" {
		if reqCtx, err = trace.ParseContext(p.Context); err != nil {
			p.Err = "request context: " + err.Error()
			s.render(w, "access", v)
			return
		}
	}
	id := identity(r)
	done, err := s.live.start(id.Name, s.cfg.LiveQueries.RunsPerHour, time.Now())
	if err != nil {
		p.Err = err.Error()
		s.render(w, "access", v)
		return
	}
	defer done()
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.LiveQueries.Timeout)
	defer cancel()
	b := &budget{max: int64(s.cfg.LiveQueries.MaxChecks)}
	start := time.Now()
	p.Ran = true
	for _, rel := range p.Rels {
		res := accessResult{Relation: rel}
		if err := b.spend(1); err != nil {
			res.Err = err.Error()
			p.Results = append(p.Results, res)
			break
		}
		resp, err := s.fga.ListObjects(ctx, &openfgav1.ListObjectsRequest{StoreId: v.StoreID, AuthorizationModelId: v.ModelID,
			User: p.Subject, Relation: rel, Type: p.Type, Context: reqCtx})
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			res.Err = fmt.Sprintf("stopped: the run's %v time limit was reached", s.cfg.LiveQueries.Timeout)
		case err != nil:
			res.Err = err.Error()
		default:
			res.Objects = resp.GetObjects()
			sort.Strings(res.Objects)
			res.Truncated = len(res.Objects) >= listObjectsMax
		}
		p.Results = append(p.Results, res)
		if ctx.Err() != nil {
			break
		}
	}
	p.Calls, p.Elapsed = int(b.used.Load()), time.Since(start)
	s.log.Info("live query", "kind", "access", "identity", id.Name, "store", v.StoreID, "subject", p.Subject, "type", p.Type,
		"relations", len(p.Rels), "calls", p.Calls, "duration", p.Elapsed)
	s.render(w, "access", v)
}
