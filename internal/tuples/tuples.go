// Package tuples reads the edges around an object, the tuples where it is the
// object ("out") or the user ("in"), through interchangeable sources:
//
//   - API: the OpenFGA Read API. Always available; filters by substring over
//     the pages it reads, and cannot count beyond one page.
//   - Datastore: OpenFGA's Postgres tuple table, read-only. Finds edges by ID
//     prefix through OpenFGA's own indexes and counts them exactly.
package tuples

import (
	"context"
	"strconv"
	"strings"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
)

// Dir selects which side of the tuple the anchor object is on.
type Dir string

const (
	// Out edges are tuples with the anchor as the object: object#relation@user.
	Out Dir = "out"
	// In edges are tuples with the anchor (or its userset) as the user.
	In Dir = "in"
)

// Query selects edges around one object.
type Query struct {
	Store    string
	Dir      Dir
	Relation string
	// Object is the anchor for Out queries ("type:id").
	Object string
	// User and ObjectType select In queries: tuples whose user is User
	// ("type:id" or "type:id#rel") on objects of ObjectType.
	User, ObjectType string
	// Contains is a case-insensitive substring filter (API source). Match, if
	// set, replaces it and receives the peer ("type:id[#rel]").
	Contains string
	Match    func(peer string) bool
	// Prefixes are byte prefixes of the peer string (datastore source), e.g.
	// "principal:CN%3DAL" for Out edges or "CN%3DAL" (object ID) for In edges.
	Prefixes []string
	Cursor   string
	Limit    int
}

// Filtered reports whether the query filters edges.
func (q Query) Filtered() bool { return q.Contains != "" || q.Match != nil || len(q.Prefixes) > 0 }

// Page is a batch of edges.
type Page struct {
	Tuples []*openfgav1.TupleKey
	// Cursor continues the query when More is set.
	Cursor string
	More   bool
	// Scanned is how many tuples were examined to produce this page.
	Scanned int
	// Note explains a partial result (e.g. a scan budget was reached).
	Note string
}

// Count is a possibly capped edge count.
type Count struct {
	N int
	// Exact is false when N is a lower bound ("N+").
	Exact bool
}

func (c Count) String() string {
	s := commas(c.N)
	if !c.Exact {
		s += "+"
	}
	return s
}

// Source reads edges.
// FilterMode is how a source matches an edge filter.
type FilterMode string

// Filter modes: the API matches anywhere in the peer, the datastore by prefix.
const (
	FilterContains FilterMode = "contains"
	FilterPrefix   FilterMode = "prefix"
)

type Source interface {
	// Name is "api" or "datastore".
	Name() string
	// Label describes the source and its filter semantics for the UI.
	Label() string
	// FilterMode is how the source matches an edge filter.
	FilterMode() FilterMode
	Edges(ctx context.Context, q Query) (*Page, error)
	// Count counts edges, stopping at limit.
	Count(ctx context.Context, q Query, limit int) (Count, error)
}

// Peer returns the other end of an edge relative to the query's anchor.
func Peer(q Query, t *openfgav1.TupleKey) string {
	if q.Dir == In {
		return t.GetObject()
	}
	return t.GetUser()
}

func commas(n int) string {
	d := strconv.Itoa(n)
	var b strings.Builder
	for i, c := range d {
		if i > 0 && (len(d)-i)%3 == 0 && d[i-1] != '-' {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
