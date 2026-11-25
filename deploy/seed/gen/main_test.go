package main

import (
	"bytes"
	"os"
	"testing"
)

// TestCommitted: the committed sample tuples are exactly what gen writes.
// After changing the generator: go run ./deploy/seed/gen
func TestCommitted(t *testing.T) {
	for name, ts := range map[string][]Tuple{"tuples.json": sample(), "collation-tuples.json": collation()} {
		want, err := encode(ts)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile("../" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("deploy/seed/%s is out of date: run go run ./deploy/seed/gen", name)
		}
	}
}
