// Package datastore opens an optional, read-only connection to OpenFGA's
// Postgres datastore. It is shared by instance search and the datastore
// tuple source, and only ever uses the indexes OpenFGA itself creates.
package datastore

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is a read-only pool plus what we learned about the tuple table.
type DB struct {
	Pool *pgxpool.Pool
	// Collation of tuple.object_id / tuple._user.
	Collation string
	// ByteOrder is set for C/POSIX collations, where IDs sharing a prefix are contiguous.
	ByteOrder bool
}

// Open connects with a forced read-only session and a statement timeout.
func Open(ctx context.Context, uri string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(uri)
	if err != nil {
		return nil, fmt.Errorf("datastore URI: %w", err)
	}
	cfg.MaxConns = 8
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	cfg.ConnConfig.RuntimeParams["application_name"] = "openfga-explorer"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	db := &DB{Pool: pool}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = pool.QueryRow(cctx, `
		SELECT COALESCE(
			(SELECT collation_name FROM information_schema.columns
			 WHERE table_schema = current_schema() AND table_name = 'tuple' AND column_name = 'object_id'),
			(SELECT datcollate FROM pg_database WHERE datname = current_database()))`).Scan(&db.Collation)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("inspecting tuple table (is this OpenFGA's datastore?): %w", err)
	}
	switch strings.ToLower(db.Collation) {
	case "c", "posix", "ucs_basic", "c.utf8", "c.utf-8":
		db.ByteOrder = true
	}
	return db, nil
}

// Close releases the pool.
func (db *DB) Close() { db.Pool.Close() }

// Query runs a query and collects the first column of every row as strings.
func (db *DB) Strings(ctx context.Context, sql string, args ...any) ([]string, error) {
	rows, err := db.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Fold approximates the primary collation key used by glibc/ICU locales such
// as en_US: case-insensitive, ignoring punctuation and whitespace. Strings
// whose folded form starts with Fold(prefix) form one contiguous run in
// collation order, and every byte-prefix match lies inside that run.
func Fold(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// RunTracker decides when a walk in collation order has left the run of
// strings that can still match a byte prefix.
type RunTracker struct {
	prefix, run string
	byteOrder   bool
	slack       int
	outside     int
}

// NewRunTracker tracks a walk that started at prefix. slack is the number of
// consecutive out-of-run values tolerated before the run is considered over.
func (db *DB) NewRunTracker(prefix string, slack int) *RunTracker {
	return &RunTracker{prefix: prefix, run: Fold(prefix), byteOrder: db.ByteOrder, slack: slack}
}

// Next reports whether v matches the prefix, and whether the walk is done
// (every later value is guaranteed not to match).
func (t *RunTracker) Next(v string) (match, done bool) {
	if strings.HasPrefix(v, t.prefix) {
		t.outside = 0
		return true, false
	}
	if t.byteOrder {
		return false, true
	}
	if strings.HasPrefix(Fold(v), t.run) {
		// Inside the collation run but not a byte-prefix match (e.g. "CN3DA…"
		// interleaved with "CN%3DA…"): keep walking.
		t.outside = 0
		return false, false
	}
	t.outside++
	return false, t.outside >= t.slack
}
