// Package search finds object instances by ID prefix for the instance picker.
//
// The OpenFGA API cannot enumerate the objects of a type, so the primary
// implementation reads OpenFGA's Postgres tuple table directly (read-only).
// A future trigram implementation can satisfy the same Searcher interface.
package search

import "context"

// Result is a matching instance.
type Result struct {
	// ID is the stored object ID (without the type prefix).
	ID string
	// Side is "object" when found as a tuple object, "user" when found as a tuple user.
	Side string
}

// Page is one page of results.
type Page struct {
	Results []Result
	// Complete is true when every matching ID at or after the cursor was returned.
	Complete bool
	// Cursor continues the search when Complete is false.
	Cursor string
	// Scanned is the number of distinct IDs examined.
	Scanned int
	// Note explains an incomplete result.
	Note string
}

// Query is a search request.
type Query struct {
	Store  string
	Type   string
	Prefix string // normalized, stored form
	Limit  int
	Cursor string
	// Sides restricts which tuple columns are searched: "object", "user", or both when empty.
	Sides []string
}

// Searcher finds instances by prefix.
type Searcher interface {
	Search(ctx context.Context, q Query) (*Page, error)
	// Name describes the implementation for the UI.
	Name() string
}
