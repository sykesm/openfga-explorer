package cond

import (
	"testing"

	"github.com/openfga/language/pkg/go/transformer"
	"google.golang.org/protobuf/types/known/structpb"
)

const model = `model
  schema 1.1
type user
type doc
  relations
    define viewer: [user with grant, user with net, user with tags]
condition grant(current_time: timestamp, grant_time: timestamp, grant_duration: duration) {
  current_time < grant_time + grant_duration
}
condition net(user_ip: ipaddress, cidr: string) {
  user_ip.in_cidr(cidr)
}
condition tags(required: list<string>, have: list<string>) {
  required.all(t, t in have)
}
`

func st(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEvaluate(t *testing.T) {
	m, err := transformer.TransformDSLToProto(model)
	if err != nil {
		t.Fatal(err)
	}
	ev := New(m.GetConditions())
	cases := []struct {
		name       string
		cond       string
		tuple, req map[string]any
		want       Status
	}{
		{"grant valid", "grant", map[string]any{"grant_time": "2026-11-01T00:00:00Z", "grant_duration": "720h"}, map[string]any{"current_time": "2026-11-24T00:00:00Z"}, Met},
		{"grant expired", "grant", map[string]any{"grant_time": "2026-11-01T00:00:00Z", "grant_duration": "24h"}, map[string]any{"current_time": "2026-11-24T00:00:00Z"}, NotMet},
		{"grant missing time", "grant", map[string]any{"grant_time": "2026-11-01T00:00:00Z", "grant_duration": "24h"}, nil, Missing},
		{"tuple context wins", "grant", map[string]any{"grant_time": "2026-11-01T00:00:00Z", "grant_duration": "24h", "current_time": "2026-11-01T01:00:00Z"}, map[string]any{"current_time": "2030-01-01T00:00:00Z"}, Met},
		{"ip in cidr", "net", map[string]any{"cidr": "10.0.0.0/8"}, map[string]any{"user_ip": "10.1.2.3"}, Met},
		{"ip outside cidr", "net", map[string]any{"cidr": "10.0.0.0/8"}, map[string]any{"user_ip": "192.168.0.1"}, NotMet},
		{"bad ip", "net", map[string]any{"cidr": "10.0.0.0/8"}, map[string]any{"user_ip": "nope"}, Failed},
		{"list all", "tags", map[string]any{"required": []any{"a", "b"}}, map[string]any{"have": []any{"a", "b", "c"}}, Met},
		{"list missing tag", "tags", map[string]any{"required": []any{"a", "z"}}, map[string]any{"have": []any{"a"}}, NotMet},
		{"undefined condition", "nope", nil, nil, Failed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var tc, rc *structpb.Struct
			if c.tuple != nil {
				tc = st(t, c.tuple)
			}
			if c.req != nil {
				rc = st(t, c.req)
			}
			r := ev.Evaluate(c.cond, tc, rc)
			if r.Status != c.want {
				t.Fatalf("status = %s (%s), want %s", r.Status, r.Summary(), c.want)
			}
		})
	}
}
