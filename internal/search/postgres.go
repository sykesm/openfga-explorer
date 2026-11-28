package search

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/sykesm/openfga-explorer/internal/datastore"
)

// Postgres searches OpenFGA's Postgres tuple table by prefix using only the
// indexes OpenFGA creates: the primary key (store, object_type, object_id, …)
// for objects and idx_user_lookup (store, _user, …) for users.
//
// Because LIKE 'abc%' cannot use those indexes under a linguistic collation
// such as en_US, the search walks distinct IDs in index order starting at the
// prefix (a recursive "skip scan"), checks each one byte-wise, and stops once
// it has left the run of IDs that collate like the prefix. Under en_US,
// punctuation is ignored at the primary level, so IDs such as "CN%3DALICE"
// and "CN3DA0001" interleave; the walk tolerates that and reports an
// incomplete result (with a cursor) if it hits its scan budget first.
type Postgres struct {
	db *datastore.DB
	// MaxScan bounds the distinct IDs examined per side per request.
	MaxScan int
	// Batch is the number of IDs fetched per query.
	Batch int
	// Slack is the number of consecutive IDs outside the prefix run required
	// before the run is considered finished (guards against collation quirks).
	Slack int
}

// NewPostgres searches through an open datastore connection.
func NewPostgres(db *datastore.DB) *Postgres {
	return &Postgres{db: db, MaxScan: 5000, Batch: 500, Slack: 25}
}

// Name describes the searcher.
func (p *Postgres) Name() string {
	return fmt.Sprintf("Postgres prefix search (collation %s)", p.db.Collation)
}

const objectScan = `
WITH RECURSIVE s(id, n) AS (
	(SELECT object_id, 1 FROM tuple
	 WHERE store = $1 AND object_type = $2 AND object_id %s $3
	 ORDER BY object_id LIMIT 1)
	UNION ALL
	SELECT (SELECT t.object_id FROM tuple t
	        WHERE t.store = $1 AND t.object_type = $2 AND t.object_id > s.id
	        ORDER BY t.object_id LIMIT 1), s.n + 1
	FROM s WHERE s.id IS NOT NULL AND s.n < $4
)
SELECT id FROM s WHERE id IS NOT NULL`

const userScan = `
WITH RECURSIVE s(id, n) AS (
	(SELECT _user, 1 FROM tuple
	 WHERE store = $1 AND _user %s $2
	 ORDER BY _user LIMIT 1)
	UNION ALL
	SELECT (SELECT t._user FROM tuple t
	        WHERE t.store = $1 AND t._user > s.id
	        ORDER BY t._user LIMIT 1), s.n + 1
	FROM s WHERE s.id IS NOT NULL AND s.n < $3
)
SELECT id FROM s WHERE id IS NOT NULL`

type cursor struct {
	Object *string `json:"o,omitempty"`
	User   *string `json:"u,omitempty"`
	// Done sides need no further scanning.
	ObjectDone bool `json:"od,omitempty"`
	UserDone   bool `json:"ud,omitempty"`
}

func (c cursor) encode() string { return datastore.EncodeCursor(c) }

type sideResult struct {
	ids      []string
	complete bool
	last     string
	scanned  int
	note     string
}

// Search implements Searcher.
func (p *Postgres) Search(ctx context.Context, q Query) (*Page, error) {
	if q.Limit <= 0 {
		q.Limit = 25
	}
	cur, err := datastore.DecodeCursor[cursor](q.Cursor)
	if err != nil {
		return nil, err
	}
	sides := q.Sides
	if len(sides) == 0 {
		sides = []string{"object", "user"}
	}
	page := &Page{Complete: true}
	next := cursor{ObjectDone: true, UserDone: true}
	seen := map[string]bool{}
	var notes []string
	for _, side := range sides {
		var after *string
		switch side {
		case "object":
			if cur.ObjectDone {
				continue
			}
			after = cur.Object
		case "user":
			if cur.UserDone {
				continue
			}
			after = cur.User
		default:
			return nil, fmt.Errorf("unknown side %q", side)
		}
		r, err := p.scan(ctx, side, q, after)
		if err != nil {
			return nil, err
		}
		page.Scanned += r.scanned
		for _, id := range r.ids {
			if !seen[id] {
				seen[id] = true
				page.Results = append(page.Results, Result{ID: id, Side: side})
			}
		}
		if !r.complete {
			page.Complete = false
			last := r.last
			if side == "object" {
				next.Object, next.ObjectDone = &last, false
			} else {
				next.User, next.UserDone = &last, false
			}
			if r.note != "" {
				notes = append(notes, r.note)
			}
		}
	}
	slices.SortFunc(page.Results, func(a, b Result) int { return cmp.Compare(a.ID, b.ID) })
	if !page.Complete {
		page.Cursor = next.encode()
		page.Note = strings.Join(notes, "; ")
	}
	return page, nil
}

func (p *Postgres) scan(ctx context.Context, side string, q Query, after *string) (sideResult, error) {
	var res sideResult
	start := q.Prefix
	if side == "user" {
		start = q.Type + ":" + q.Prefix
	}
	from, op := start, ">="
	if after != nil {
		from, op = *after, ">"
	}
	tracker := p.db.NewRunTracker(start, p.Slack)
	found := map[string]bool{}
	for res.scanned < p.MaxScan {
		n := min(p.Batch, p.MaxScan-res.scanned)
		var batch []string
		var err error
		if side == "object" {
			batch, err = p.collect(ctx, fmt.Sprintf(objectScan, op), q.Store, q.Type, from, n)
		} else {
			batch, err = p.collect(ctx, fmt.Sprintf(userScan, op), q.Store, from, n)
		}
		if err != nil {
			return res, err
		}
		if len(batch) == 0 {
			res.complete = true
			return res, nil
		}
		for _, v := range batch {
			res.scanned++
			res.last = v
			match, done := tracker.Next(v)
			if done {
				res.complete = true
				return res, nil
			}
			if !match {
				continue
			}
			id := v
			if side == "user" {
				id = strings.TrimPrefix(v, q.Type+":")
				if i := strings.IndexByte(id, '#'); i >= 0 {
					id = id[:i]
				}
			}
			// "type:*" is a public wildcard, not an instance.
			if id != "*" && !found[id] {
				found[id] = true
				res.ids = append(res.ids, id)
				if len(res.ids) >= q.Limit {
					res.note = "more matches may exist"
					return res, nil
				}
			}
		}
		from, op = res.last, ">"
		if len(batch) < n {
			res.complete = true
			return res, nil
		}
	}
	res.note = fmt.Sprintf("stopped after scanning %d IDs that sort near the prefix; results may be incomplete", res.scanned)
	return res, nil
}

func (p *Postgres) collect(ctx context.Context, sql string, args ...any) ([]string, error) {
	out, err := p.db.Strings(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("prefix search: %w", err)
	}
	return out, nil
}
