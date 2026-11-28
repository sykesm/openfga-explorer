package datastore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
)

// EncodeCursor turns a page position into an opaque cursor for a URL.
func EncodeCursor[T any](c T) string {
	b, _ := json.Marshal(c) // cursors are plain structs: marshaling cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor reads a cursor made by EncodeCursor; the empty cursor is the
// zero position (the first page).
func DecodeCursor[T any](s string) (T, error) {
	var c T
	if s == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, errors.New("invalid cursor")
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, errors.New("invalid cursor")
	}
	return c, nil
}
