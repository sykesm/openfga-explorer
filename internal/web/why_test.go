package web

import "testing"

func TestFindPart(t *testing.T) {
	def := "[user, user:*, group#member, user with grant] or editor or member from org or member"
	for _, c := range []struct {
		part        string
		restriction bool
		want        int
	}{
		{"user", true, 1},
		{"user:*", true, 7},
		{"group#member", true, 15},
		{"user with grant", true, 29},
		{"editor", false, 49},
		{"member from org", false, 59},
		{"member", false, 78}, // not the "member" of "member from org"
		{"editor", true, -1},  // not a type restriction
		{"viewer", false, -1},
		{"", false, -1},
	} {
		if got := findPart(def, c.part, c.restriction); got != c.want {
			t.Errorf("findPart(%q, %v) = %d, want %d", c.part, c.restriction, got, c.want)
		}
	}
	if got := findPart("(viewer and member from org) but not blocked from org", "but not blocked from org", false); got != 29 {
		t.Errorf("exclusion found at %d, want 29", got)
	}
}
