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

// TestStepsByID: steps refer to each other by ID, so two different facts
// worded the same stay apart; only the same fact reached twice (same
// sentence, same evidence) is dropped, and what built on it builds on the
// step kept.
func TestStepsByID(t *testing.T) {
	tuple := func(user string) Evidence {
		return Evidence{Kind: EvidenceTuple, Tuple: Tuple{User: user, Relation: "member", Object: "group:eng"}}
	}
	var x explainer
	a := x.step("anne is a member", tuple("user:anne"))
	b := x.step("anne is a member", tuple("user:Anne")) // same words, another tuple
	again := x.step("anne is a member", tuple("user:anne"))
	onA := x.add(WhyStep{Text: "so anne has viewer", from: []int{a.ID}})
	onAgain := x.add(WhyStep{Text: "so anne has editor", from: []int{again.ID}})
	steps := cite(dedupe([]WhyStep{a, b, again, onA, onAgain}))
	if len(steps) != 4 {
		t.Fatalf("got %d steps, want 4 (only the repeat dropped): %v", len(steps), steps)
	}
	if got := steps[3].from; len(got) != 1 || got[0] != a.ID {
		t.Errorf("the step on the dropped repeat builds on %v, want [%d]", got, a.ID)
	}
	// Both steps after the first two build on step 1, not on step 2 with
	// the same wording.
	if steps[2].Text != "so anne has viewer (from step 1)" || steps[3].Text != "so anne has editor (from step 1)" {
		t.Errorf("citations: %q, %q", steps[2].Text, steps[3].Text)
	}
	for _, d := range (Explanation{Steps: steps}).Down() {
		for _, u := range d.Under {
			if u.ID != a.ID {
				t.Errorf("%q is under %q (step %d), want step %d", d.Sentence, u.Sentence, u.ID, a.ID)
			}
		}
	}
}
