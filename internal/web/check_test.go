package web

import "testing"

func TestParseContextual(t *testing.T) {
	ks, err := parseContextual("user:anne viewer doc:1\n# comment\n\nuser:bob viewer doc:2 grant {\"grant_duration\":\"1h\"}")
	if err != nil {
		t.Fatal(err)
	}
	if len(ks) != 2 || ks[1].GetCondition().GetName() != "grant" || ks[1].GetCondition().GetContext().GetFields()["grant_duration"].GetStringValue() != "1h" {
		t.Fatalf("got %v", ks)
	}
	ks, err = parseContextual(`[{"user":"user:anne","relation":"viewer","object":"doc:1"}]`)
	if err != nil || len(ks) != 1 || ks[0].GetUser() != "user:anne" {
		t.Fatalf("json form: %v %v", ks, err)
	}
	if _, err := parseContextual("only two"); err == nil {
		t.Fatal("expected error")
	}
}

func TestTrimVerdict(t *testing.T) {
	for in, want := range map[string]string{
		"Allowed: user:eng-002 has can_view on document:doc-0007.":    "user:eng-002 has can_view on document:doc-0007.",
		"Denied: group:eng#member does not have viewer on doc:1 — x.": "group:eng#member does not have viewer on doc:1 — x.",
		"Undecided: the walk could not settle whether …":              "The walk could not settle whether …",
		"": "",
	} {
		if got := trimVerdict(in); got != want {
			t.Errorf("trimVerdict(%q) = %q, want %q", in, got, want)
		}
	}
}
