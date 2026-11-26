package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/schema"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// The change log (OpenFGA ReadChanges) is read forward only, oldest first,
// in pages; a page's continuation token reads on from it. The explorer keeps
// nothing: each page is a URL carrying the token, and "live" polls with it.

const changesPageSize = 50

// Filtering by user type reads on, a log page at a time, until a page of
// matches is found or changesScanMax changes have been read. A log page is
// never split (its token resumes after it), so a filtered page can hold up
// to 2*changesPageSize-1 rows.
const changesScanMax = 2000

// changeRanges are the preset windows, ending now.
var changeRanges = map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

type changesPage struct {
	// The form, echoed back.
	Range, From, To, Type, UserType string
	TZ                              int      // the browser's offset from UTC, in minutes west (Date.getTimezoneOffset)
	Types                           []string // object types: those with relations
	UserTypes                       []string // types that can be a tuple's user
	// Results.
	Ran        bool
	Rows       []changeRow
	Start, End time.Time
	Next       string // continuation token for the next page, if any
	Done       bool   // reached the end of the window or of the log
	Live       bool
	Poll       string // live: token to poll with
	CatchUp    bool   // live: the page was full, poll again at once
	Scanned    int    // changes read in the window (more than Rows when filtering)
	Limited    bool   // the scan budget ran out before a page of matches
	Err        string
	PageSize   int
	ScanMax    int
}

type changeRow struct {
	Time                         string
	Op                           string // "write" or "delete"
	User, Relation, Object, Cond string
	// ObjectType and ObjectID link to the object in Explore.
	ObjectType, ObjectID string
}

// changeWindow turns the form into a time window. Custom times are in the
// browser's timezone (tz minutes west of UTC).
func changeWindow(rng, from, to string, tz int, now time.Time) (start, end time.Time, err error) {
	loc := time.FixedZone("", -tz*60)
	if d, ok := changeRanges[rng]; ok {
		return now.Add(-d), now, nil
	}
	switch rng {
	case "live":
		return now.Add(-5 * time.Minute), time.Time{}, nil
	case "custom":
		const layout = "2006-01-02T15:04"
		if start, err = time.ParseInLocation(layout, from, loc); err != nil {
			return start, end, fmt.Errorf("choose a start time")
		}
		end = now
		if to != "" {
			if end, err = time.ParseInLocation(layout, to, loc); err != nil {
				return start, end, fmt.Errorf("the end time is not valid")
			}
		}
		if !end.After(start) {
			return start, end, fmt.Errorf("the end time must be after the start time")
		}
		return start, end, nil
	}
	return start, end, fmt.Errorf("unknown range %q", rng)
}

func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	v, _, err := s.resolve(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, err)
		return
	}
	s.withStoreName(r.Context(), v)
	q := r.URL.Query()
	p := &changesPage{Range: q.Get("range"), From: q.Get("from"), To: q.Get("to"), Type: q.Get("type"), UserType: q.Get("user_type"), PageSize: changesPageSize, ScanMax: changesScanMax}
	p.TZ, _ = strconv.Atoi(q.Get("tz"))
	for _, tn := range v.Model.TypeNames {
		if len(v.Model.Types[tn].RelationNames) > 0 {
			p.Types = append(p.Types, tn)
		}
		if len(v.Model.IncomingFor(tn)) > 0 {
			p.UserTypes = append(p.UserTypes, tn)
		}
	}
	if p.Range == "" {
		p.Range = "1h"
	}
	v.Title, v.Active, v.Data = "Changes", "changes", p
	if q.Has("range") || q.Has("token") {
		s.readChanges(r, v.StoreID, p, q.Get("token"))
	}
	switch {
	case q.Get("rows") != "": // live polling: just the new rows and the next poller
		s.render(w, "change-rows", v)
	case r.Header.Get("HX-Request") != "":
		s.render(w, "changes-result", v)
	default:
		s.render(w, "changes", v)
	}
}

func (s *Server) readChanges(r *http.Request, store string, p *changesPage, token string) {
	p.Ran = true
	p.Live = p.Range == "live"
	start, end, err := changeWindow(p.Range, p.From, p.To, p.TZ, time.Now())
	if err != nil {
		p.Err = err.Error()
		return
	}
	p.Start, p.End = start, end
	// ReadChanges filters by object type only; a user type is filtered here,
	// reading on until a page of matches is found or the scan budget is spent.
	filter := p.UserType != ""
	size := changesPageSize
	loc := time.FixedZone("", -p.TZ*60)
	for {
		req := &openfgav1.ReadChangesRequest{StoreId: store, Type: p.Type, PageSize: wrapperspb.Int32(int32(size)), ContinuationToken: token}
		if token == "" {
			req.StartTime = timestamppb.New(start)
		}
		resp, err := s.fga.ReadChanges(r.Context(), req)
		if err != nil {
			p.Err = err.Error()
			return
		}
		changes := resp.GetChanges()
		for _, c := range changes {
			ts := c.GetTimestamp().AsTime()
			if !end.IsZero() && ts.After(end) {
				p.Done = true
				break
			}
			p.Scanned++
			k := c.GetTupleKey()
			if filter && userType(k.GetUser()) != p.UserType {
				continue
			}
			op := "write"
			if c.GetOperation() == openfgav1.TupleOperation_TUPLE_OPERATION_DELETE {
				op = "delete"
			}
			typ, id, _ := schema.SplitObject(k.GetObject())
			p.Rows = append(p.Rows, changeRow{Time: ts.In(loc).Format("2006-01-02 15:04:05"), Op: op,
				User: k.GetUser(), Relation: k.GetRelation(), Object: k.GetObject(), Cond: k.GetCondition().GetName(), ObjectType: typ, ObjectID: id})
		}
		next := resp.GetContinuationToken()
		if p.Done || len(changes) < size || next == "" {
			// The end of the window, or of the log for now.
			if p.Live {
				p.Poll = next
			} else {
				p.Done = true
			}
			return
		}
		token = next
		if !filter || len(p.Rows) >= changesPageSize || p.Scanned >= changesScanMax {
			break
		}
	}
	// More of the log remains.
	if p.Live {
		p.Poll, p.CatchUp = token, true
	} else {
		p.Next = token
		p.Limited = filter && len(p.Rows) < changesPageSize
	}
}

// userType is the type of a tuple's user: "group" for group:eng#member.
func userType(user string) string {
	t, _, _ := strings.Cut(user, ":")
	return t
}
