package trace

import "testing"

func TestEvidenceLine(t *testing.T) {
	for _, c := range []struct {
		ev   Evidence
		want string
	}{
		{Evidence{Kind: EvidenceRule, Ref: Ref{"doc:1", "viewer", "viewer from parent"}}, "rule   doc#viewer via viewer from parent"},
		{Evidence{Kind: EvidenceTuple, Tuple: Tuple{User: "user:anne", Relation: "viewer", Object: "doc:1"}, Ref: Ref{"doc:1", "viewer", "user"}},
			"tuple  doc:1#viewer@user:anne  (doc#viewer via user)"},
		// Both readings show a tuple's condition and whether it was stored.
		{Evidence{Kind: EvidenceTuple, Tuple: Tuple{User: "user:erin", Relation: "viewer", Object: "doc:2", Condition: "in_office", Contextual: true}, Ref: Ref{"doc:2", "viewer", "user with in_office"}},
			"tuple  doc:2#viewer@user:erin with in_office (contextual, not stored)  (doc#viewer via user with in_office)"},
	} {
		if got := evidenceLine(c.ev); got != c.want {
			t.Errorf("evidenceLine = %q\n          want %q", got, c.want)
		}
	}
	if got := stepRefs("from", []int{2}); got != "from step 2" {
		t.Errorf("stepRefs one = %q", got)
	}
	if got := stepRefs("see", []int{1, 3, 4}); got != "see steps 1, 3 and 4" {
		t.Errorf("stepRefs many = %q", got)
	}
}
