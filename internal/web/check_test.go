package web

import (
	"net/url"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/sykesm/openfga-explorer/internal/config"
	"github.com/sykesm/openfga-explorer/internal/ids"
	"github.com/sykesm/openfga-explorer/internal/trace"
)

func TestParseCheckRun(t *testing.T) {
	cfg := config.Default()
	cfg.Types = ids.Rules{"user": {Case: "lower"}}
	s := &Server{cfg: cfg}
	run, err := s.parseCheckRun(url.Values{
		// A typed ID with its type chosen in the picker is normalized for the type.
		"user_type": {"user"}, "user_q": {"user:ANNE"},
		"relation": {"viewer"}, "object": {"document:1"},
		"context":     {` {"x": 1} `},
		"consistency": {"higher"}, "verify": {"root"}, "order": {trace.OrderModel},
		"max_calls": {"999999"}, "max_fanout": {"7"}, "probe_limit": {"-3"},
	}, "store", "model")
	if err != nil {
		t.Fatal(err)
	}
	if run.req.User != "user:anne" || run.req.Store != "store" || run.req.Model != "model" {
		t.Errorf("request: %+v", run.req)
	}
	if run.req.Consistency != openfgav1.ConsistencyPreference_HIGHER_CONSISTENCY || run.opt.Verify != trace.VerifyRoot {
		t.Errorf("consistency %v, verify %v", run.req.Consistency, run.opt.Verify)
	}
	def := cfg.Trace.Options()
	if run.opt.MaxCalls != config.MaxTraceCalls || run.opt.MaxFanout != 7 || run.opt.ProbeLimit != def.ProbeLimit {
		t.Errorf("limits: calls %d, fanout %d, probes %d (default %d)", run.opt.MaxCalls, run.opt.MaxFanout, run.opt.ProbeLimit, def.ProbeLimit)
	}
	// The link reruns the same check: the context as typed (trimmed), the
	// normalized order, and the raw settings; empty fields are left out.
	want := "/s/check?consistency=higher&context=%7B%22x%22%3A+1%7D&max_calls=999999&max_fanout=7&object=document%3A1&order=" +
		trace.OrderModel + "&probe_limit=-3&relation=viewer&user=user%3Aanne&verify=root"
	if got := run.link("/s"); got != want {
		t.Errorf("link\n got %s\nwant %s", got, want)
	}

	for name, form := range map[string]url.Values{
		"missing object":  {"user": {"user:anne"}, "relation": {"viewer"}},
		"bad context":     {"user": {"user:anne"}, "relation": {"viewer"}, "object": {"document:1"}, "context": {"{"}},
		"bad contextual":  {"user": {"user:anne"}, "relation": {"viewer"}, "object": {"document:1"}, "contextual": {"only two"}},
		"type, no typing": {"user_type": {"user"}, "relation": {"viewer"}, "object": {"document:1"}},
	} {
		if _, err := s.parseCheckRun(form, "store", "model"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

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
	// Any run of spaces or tabs separates fields; the context after the
	// condition may contain spaces.
	ks, err = parseContextual("user:anne  viewer\tdoc:1   grant  {\"grant_duration\": \"1h\", \"x\": 1}")
	if err != nil {
		t.Fatal(err)
	}
	k := ks[0]
	if k.GetUser() != "user:anne" || k.GetRelation() != "viewer" || k.GetObject() != "doc:1" || k.GetCondition().GetName() != "grant" ||
		k.GetCondition().GetContext().GetFields()["grant_duration"].GetStringValue() != "1h" {
		t.Errorf("repeated spaces and tabs: %v", k)
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
