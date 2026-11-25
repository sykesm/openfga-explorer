package ids

import "testing"

func TestNormalize(t *testing.T) {
	principal := Rule{Trim: true, Case: "upper", Encode: ":#= ", Display: "decoded"}
	cases := []struct {
		rule     Rule
		in, want string
	}{
		{principal, " cn=alice smith ", "CN%3DALICE%20SMITH"},
		{principal, "CN%3DALICE%20SMITH", "CN%3DALICE%20SMITH"}, // already encoded: idempotent
		{principal, "svc:build#ci", "SVC%3ABUILD%23CI"},
		{principal, "cn%3", "CN%253"}, // partial escape is literal text
		{principal, "100%", "100%25"}, // '%' always encoded
		{Rule{}, "Doc-1", "Doc-1"},
		{Rule{Case: "lower"}, "ACME", "acme"},
	}
	for _, c := range cases {
		if got := c.rule.Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := principal.DisplayID("CN%3DALICE%20SMITH"); got != "CN=ALICE SMITH" {
		t.Errorf("DisplayID = %q", got)
	}
	if got := (Rule{}).DisplayID("CN%3DX"); got != "CN%3DX" {
		t.Errorf("raw DisplayID = %q", got)
	}
}
