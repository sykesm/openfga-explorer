package datastore

import "testing"

func TestFold(t *testing.T) {
	for in, want := range map[string]string{
		"CN%3DALICE":  "cn3dalice",
		"CN-3D-ALEX":  "cn3dalex",
		"principal:X": "principalx",
		"":            "",
	} {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunTracker(t *testing.T) {
	db := &DB{}
	tr := db.NewRunTracker("CN%3DA", 2)
	steps := []struct {
		v           string
		match, done bool
	}{
		{"CN3DA0001", false, false},  // in the collation run (folds to cn3da…), not a match
		{"CN%3DALICE", true, false},  // match
		{"CN-3D-ALEX", false, false}, // still in the run
		{"CN%3DBOB", false, false},   // folds to cn3dbob: first value outside the run
		{"DZ", false, true},          // second outside value: slack exhausted
	}
	for _, s := range steps {
		m, d := tr.Next(s.v)
		if m != s.match || d != s.done {
			t.Errorf("Next(%q) = %v,%v want %v,%v", s.v, m, d, s.match, s.done)
		}
	}
	bo := (&DB{ByteOrder: true}).NewRunTracker("ab", 5)
	if m, d := bo.Next("ac"); m || !d {
		t.Errorf("byte order: first non-match must end the walk")
	}
}
