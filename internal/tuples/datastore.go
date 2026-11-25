package tuples

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/datastore"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// Datastore reads edges from OpenFGA's Postgres tuple table using only the
// indexes OpenFGA creates:
//
//   - Out edges walk tuple_pkey (store, object_type, object_id, relation, _user),
//     so a user prefix is a range seek within one object#relation.
//   - In edges walk idx_user_lookup (store, _user, relation, object_type,
//     object_id COLLATE "C"), whose byte-order collation makes an object ID
//     prefix an exact range.
type Datastore struct {
	db *datastore.DB
	// MaxScan bounds rows examined per filtered request (out edges under a
	// linguistic collation may interleave non-matching IDs).
	MaxScan int
	Batch   int
	Slack   int
}

// NewDatastore returns a datastore source.
func NewDatastore(db *datastore.DB) *Datastore {
	return &Datastore{db: db, MaxScan: 5000, Batch: 250, Slack: 25}
}

func (d *Datastore) Name() string       { return "datastore" }
func (d *Datastore) Label() string      { return "Datastore · ID prefix search" }
func (d *Datastore) FilterMode() string { return "prefix" }

const (
	outCols  = `_user, coalesce(condition_name, ''), condition_context`
	outWhere = `store = $1 AND object_type = $2 AND object_id = $3 AND relation = $4`
	inCols   = `object_id, coalesce(condition_name, ''), condition_context`
	inWhere  = `store = $1 AND _user = $2 AND relation = $3 AND object_type = $4`
)

type cursor struct {
	// I indexes Prefixes; After is the last peer value returned.
	I     int    `json:"i,omitempty"`
	After string `json:"a,omitempty"`
}

func decode(s string) (cursor, error) {
	var c cursor
	if s == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, fmt.Errorf("invalid cursor")
	}
	return c, json.Unmarshal(b, &c)
}

func (c cursor) encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

type row struct {
	peer, cond string
	ctx        []byte
}

func (d *Datastore) rows(ctx context.Context, sql string, args ...any) ([]row, error) {
	rs, err := d.db.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("datastore: %w", err)
	}
	return pgx.CollectRows(rs, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.peer, &x.cond, &x.ctx)
		return x, err
	})
}

func (d *Datastore) tuple(q Query, r row) *openfgav1.TupleKey {
	t := &openfgav1.TupleKey{Relation: q.Relation}
	if q.Dir == In {
		t.User, t.Object = q.User, q.ObjectType+":"+r.peer
	} else {
		t.User, t.Object = r.peer, q.Object
	}
	if r.cond != "" {
		t.Condition = &openfgav1.RelationshipCondition{Name: r.cond}
		if len(r.ctx) > 0 {
			var st structpb.Struct
			if proto.Unmarshal(r.ctx, &st) == nil {
				t.Condition.Context = &st
			}
		}
	}
	return t
}

func (d *Datastore) anchorArgs(q Query) ([]any, error) {
	if q.Dir == In {
		if q.User == "" || q.ObjectType == "" {
			return nil, fmt.Errorf("in-edges need a user and an object type")
		}
		return []any{q.Store, q.User, q.Relation, q.ObjectType}, nil
	}
	typ, id, ok := strings.Cut(q.Object, ":")
	if !ok {
		return nil, fmt.Errorf("invalid object %q", q.Object)
	}
	return []any{q.Store, typ, id, q.Relation}, nil
}

// Edges returns edges in index order. Prefix filters seek straight to the
// matching range; Contains/Match filters are not supported here.
func (d *Datastore) Edges(ctx context.Context, q Query) (*Page, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	if q.Contains != "" || q.Match != nil {
		return nil, fmt.Errorf("the datastore source filters by ID prefix only")
	}
	args, err := d.anchorArgs(q)
	if err != nil {
		return nil, err
	}
	cur, err := decode(q.Cursor)
	if err != nil {
		return nil, err
	}
	if len(q.Prefixes) == 0 {
		return d.unfiltered(ctx, q, args, cur)
	}
	q.Prefixes = minimalPrefixes(q.Prefixes)
	if q.Dir == In {
		return d.inPrefixes(ctx, q, args, cur)
	}
	return d.outPrefixes(ctx, q, args, cur)
}

func (d *Datastore) unfiltered(ctx context.Context, q Query, args []any, cur cursor) (*Page, error) {
	cols, where, order := outCols, outWhere, `_user`
	if q.Dir == In {
		cols, where, order = inCols, inWhere, `object_id COLLATE "C"`
	}
	sql := fmt.Sprintf(`SELECT %s FROM tuple WHERE %s ORDER BY %s LIMIT $5`, cols, where, order)
	a := append(args, q.Limit+1)
	if cur.After != "" {
		sql = fmt.Sprintf(`SELECT %s FROM tuple WHERE %s AND %s > $6 ORDER BY %s LIMIT $5`, cols, where, order, order)
		a = append(a, cur.After)
	}
	rs, err := d.rows(ctx, sql, a...)
	if err != nil {
		return nil, err
	}
	page := &Page{Scanned: len(rs)}
	if len(rs) > q.Limit {
		rs = rs[:q.Limit]
		page.More = true
		page.Cursor = cursor{After: rs[len(rs)-1].peer}.encode()
	}
	for _, r := range rs {
		page.Tuples = append(page.Tuples, d.tuple(q, r))
	}
	return page, nil
}

// inPrefixes uses byte-order ranges on idx_user_lookup's object_id COLLATE "C".
func (d *Datastore) inPrefixes(ctx context.Context, q Query, args []any, cur cursor) (*Page, error) {
	page := &Page{}
	for i := cur.I; i < len(q.Prefixes); i++ {
		p := q.Prefixes[i]
		from, op := p, ">="
		if i == cur.I && cur.After != "" {
			from, op = cur.After, ">"
		}
		need := q.Limit - len(page.Tuples)
		if need <= 0 {
			// The page filled exactly at a prefix boundary: resume at this prefix.
			page.More, page.Cursor = true, cursor{I: i}.encode()
			return page, nil
		}
		sql := fmt.Sprintf(`SELECT %s FROM tuple WHERE %s AND object_id COLLATE "C" %s $5 AND starts_with(object_id, $6)`, inCols, inWhere, op)
		a := append(append([]any{}, args...), from, p)
		if up, ok := upper(p); ok {
			sql += ` AND object_id COLLATE "C" < $8`
			a = append(a, need+1, up)
		} else {
			a = append(a, need+1)
		}
		sql += ` ORDER BY object_id COLLATE "C" LIMIT $7`
		rs, err := d.rows(ctx, sql, a...)
		if err != nil {
			return nil, err
		}
		page.Scanned += len(rs)
		if len(rs) > need {
			rs = rs[:need]
			for _, r := range rs {
				page.Tuples = append(page.Tuples, d.tuple(q, r))
			}
			page.More, page.Cursor = true, cursor{I: i, After: rs[len(rs)-1].peer}.encode()
			return page, nil
		}
		for _, r := range rs {
			page.Tuples = append(page.Tuples, d.tuple(q, r))
		}
	}
	return page, nil
}

// outPrefixes seeks tuple_pkey to each user prefix and walks forward. Under a
// linguistic collation, IDs that differ only in punctuation interleave, so the
// walk continues until it has left the prefix's collation run (or hits MaxScan,
// which is reported).
func (d *Datastore) outPrefixes(ctx context.Context, q Query, args []any, cur cursor) (*Page, error) {
	page := &Page{}
	for i := cur.I; i < len(q.Prefixes); i++ {
		p := q.Prefixes[i]
		from, op := p, ">="
		if i == cur.I && cur.After != "" {
			from, op = cur.After, ">"
		}
		tracker := d.db.NewRunTracker(p, d.Slack)
	walk:
		for {
			if page.Scanned >= d.MaxScan {
				page.More, page.Cursor = true, cursor{I: i, After: from}.encode()
				page.Note = fmt.Sprintf("Stopped early after checking %d edges; more matches may follow.", page.Scanned)
				return page, nil
			}
			sql := fmt.Sprintf(`SELECT %s FROM tuple WHERE %s AND _user %s $5 ORDER BY _user LIMIT $6`, outCols, outWhere, op)
			rs, err := d.rows(ctx, sql, append(append([]any{}, args...), from, d.Batch)...)
			if err != nil {
				return nil, err
			}
			if len(rs) == 0 {
				break
			}
			for _, r := range rs {
				page.Scanned++
				from, op = r.peer, ">"
				match, done := tracker.Next(r.peer)
				if done {
					break walk
				}
				if !match {
					continue
				}
				page.Tuples = append(page.Tuples, d.tuple(q, r))
				if len(page.Tuples) >= q.Limit {
					page.More, page.Cursor = true, cursor{I: i, After: r.peer}.encode()
					return page, nil
				}
			}
			if len(rs) < d.Batch {
				break
			}
		}
	}
	return page, nil
}

// Count counts edges exactly, up to limit.
func (d *Datastore) Count(ctx context.Context, q Query, limit int) (Count, error) {
	args, err := d.anchorArgs(q)
	if err != nil {
		return Count{}, err
	}
	where := outWhere
	if q.Dir == In {
		where = inWhere
	}
	var n int
	sql := fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM tuple WHERE %s LIMIT $5) s`, where)
	if err := d.db.Pool.QueryRow(ctx, sql, append(args, limit+1)...).Scan(&n); err != nil {
		return Count{}, fmt.Errorf("datastore: %w", err)
	}
	if n > limit {
		return Count{N: limit, Exact: false}, nil
	}
	return Count{N: n, Exact: true}, nil
}

// upper returns the smallest string greater than every string with prefix p
// in byte order, or false when there is none (p is empty or all 0xFF).
func upper(p string) (string, bool) {
	b := []byte(p)
	for len(b) > 0 && b[len(b)-1] == 0xFF {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return "", false
	}
	b[len(b)-1]++
	return string(b), true
}

// minimalPrefixes sorts prefixes and drops any covered by a shorter one
// ("CN%3DA" is covered by "CN%3D"), so overlapping filters don't return an
// edge twice. The result is deterministic, which cursors rely on.
func minimalPrefixes(ps []string) []string {
	sorted := append([]string(nil), ps...)
	sort.Strings(sorted)
	var out []string
	for _, p := range sorted {
		if len(out) > 0 && strings.HasPrefix(p, out[len(out)-1]) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Bulk is implemented by sources that can summarize every relation of an
// object in a couple of queries instead of one per relation.
type Bulk interface {
	// OutCounts counts edges on object for each relation, each capped at limit.
	OutCounts(ctx context.Context, store, object string, relations []string, limit int) (map[string]Count, error)
	// OutEdges returns all edges on object for the given (low-cardinality) relations.
	OutEdges(ctx context.Context, store, object string, relations []string) (map[string][]*openfgav1.TupleKey, error)
}

// OutCounts runs one query with a capped count per relation.
func (d *Datastore) OutCounts(ctx context.Context, store, object string, relations []string, limit int) (map[string]Count, error) {
	typ, id, ok := strings.Cut(object, ":")
	if !ok {
		return nil, fmt.Errorf("invalid object %q", object)
	}
	rows, err := d.db.Pool.Query(ctx, `
		SELECT r.rel, (SELECT count(*) FROM (
			SELECT 1 FROM tuple WHERE store = $1 AND object_type = $2 AND object_id = $3 AND relation = r.rel LIMIT $5) s)
		FROM unnest($4::text[]) AS r(rel)`, store, typ, id, relations, limit+1)
	if err != nil {
		return nil, fmt.Errorf("datastore: %w", err)
	}
	defer rows.Close()
	out := map[string]Count{}
	for rows.Next() {
		var rel string
		var n int
		if err := rows.Scan(&rel, &n); err != nil {
			return nil, err
		}
		if n > limit {
			out[rel] = Count{N: limit}
		} else {
			out[rel] = Count{N: n, Exact: true}
		}
	}
	return out, rows.Err()
}

// OutEdges reads every edge of the given relations in one query. Callers pass
// only relations already known to be small.
func (d *Datastore) OutEdges(ctx context.Context, store, object string, relations []string) (map[string][]*openfgav1.TupleKey, error) {
	out := map[string][]*openfgav1.TupleKey{}
	if len(relations) == 0 {
		return out, nil
	}
	typ, id, ok := strings.Cut(object, ":")
	if !ok {
		return nil, fmt.Errorf("invalid object %q", object)
	}
	rows, err := d.db.Pool.Query(ctx, `
		SELECT relation, _user, coalesce(condition_name, ''), condition_context FROM tuple
		WHERE store = $1 AND object_type = $2 AND object_id = $3 AND relation = ANY($4)
		ORDER BY relation, _user LIMIT 2000`, store, typ, id, relations)
	if err != nil {
		return nil, fmt.Errorf("datastore: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rel string
		var r row
		if err := rows.Scan(&rel, &r.peer, &r.cond, &r.ctx); err != nil {
			return nil, err
		}
		out[rel] = append(out[rel], d.tuple(Query{Dir: Out, Object: object, Relation: rel}, r))
	}
	return out, rows.Err()
}
