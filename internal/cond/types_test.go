package cond

import (
	"strings"
	"testing"

	"github.com/openfga/language/pkg/go/transformer"
)

// typed has one condition per parameter type, so conversions and CEL types
// are exercised for each.
const typed = `model
  schema 1.1
type user
type doc
  relations
    define viewer: [user with b, user with s, user with i, user with u, user with d, user with dur, user with ts, user with ip, user with m]
condition b(x: bool) { x }
condition s(x: string) { x == "yes" }
condition i(x: int) { x > 2 }
condition u(x: uint) { x > uint(2) }
condition d(x: double) { x > 0.5 }
condition dur(x: duration) { x > duration("1h") }
condition ts(x: timestamp) { x > timestamp("2026-01-01T00:00:00Z") }
condition ip(x: ipaddress) { x.in_cidr("192.168.0.0/16") }
condition m(x: map<int>) { x["k"] > 1 }
`

func TestParameterTypes(t *testing.T) {
	pm, err := transformer.TransformDSLToProto(typed)
	if err != nil {
		t.Fatal(err)
	}
	ev := New(pm.GetConditions())
	cases := []struct {
		cond string
		x    any
		want Status
	}{
		{"b", true, Met}, {"b", false, NotMet}, {"b", "true", Failed},
		{"s", "yes", Met}, {"s", "no", NotMet}, {"s", 1.0, Failed},
		{"i", 3.0, Met}, {"i", 1.0, NotMet}, {"i", 2.5, Failed}, {"i", "3", Failed},
		{"u", 3.0, Met}, {"u", -1.0, Failed},
		{"d", 0.75, Met}, {"d", 0.25, NotMet}, {"d", "x", Failed},
		{"dur", "2h", Met}, {"dur", "30m", NotMet}, {"dur", "soon", Failed}, {"dur", 5.0, Failed},
		{"ts", "2026-06-01T00:00:00Z", Met}, {"ts", "2025-06-01T00:00:00Z", NotMet}, {"ts", "yesterday", Failed}, {"ts", true, Failed},
		{"ip", "192.168.1.1", Met}, {"ip", "10.0.0.1", NotMet}, {"ip", 7.0, Failed},
		{"m", map[string]any{"k": 2.0}, Met}, {"m", map[string]any{"k": 1.0}, NotMet}, {"m", "not a map", Failed},
	}
	for _, c := range cases {
		r := ev.Evaluate(c.cond, st(t, map[string]any{"x": c.x}), nil)
		if r.Status != c.want {
			t.Errorf("%s(%v) = %s (%s), want %s", c.cond, c.x, r.Status, r.Err, c.want)
		}
		if r.Summary() == "" || !strings.HasPrefix(r.Summary(), c.cond) {
			t.Errorf("%s summary = %q", c.cond, r.Summary())
		}
		if r.OK() != (r.Status == Met) {
			t.Errorf("%s OK() = %v for %s", c.cond, r.OK(), r.Status)
		}
	}
	// Missing parameters are named, and the summary says so.
	r := ev.Evaluate("i", nil, nil)
	if r.Status != Missing || len(r.Missing) != 1 || r.Missing[0] != "x" || !strings.Contains(r.Summary(), "x") {
		t.Errorf("missing: %+v %q", r, r.Summary())
	}
	// Parameters record where their values came from.
	r = ev.Evaluate("i", nil, st(t, map[string]any{"x": 3.0}))
	if len(r.Params) != 1 || r.Params[0].Source != "request" || r.Params[0].Type != "int" {
		t.Errorf("params: %+v", r.Params)
	}
	var nilResult *Result
	if nilResult.OK() {
		t.Error("nil result is OK")
	}
}

func TestTypeName(t *testing.T) {
	pm, err := transformer.TransformDSLToProto(typed)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"m": "map<int>", "ip": "ipaddress", "dur": "duration", "ts": "timestamp"}
	for name, w := range want {
		if got := TypeName(pm.GetConditions()[name].GetParameters()["x"]); got != w {
			t.Errorf("TypeName(%s) = %q, want %q", name, got, w)
		}
	}
}
