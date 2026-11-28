package datastore

import "testing"

func TestCursor(t *testing.T) {
	type pos struct {
		I     int    `json:"i,omitempty"`
		After string `json:"a,omitempty"`
	}
	in := pos{I: 2, After: "doc:7"}
	out, err := DecodeCursor[pos](EncodeCursor(in))
	if err != nil || out != in {
		t.Errorf("round trip: %+v, %v", out, err)
	}
	if c, err := DecodeCursor[pos](""); err != nil || c != (pos{}) {
		t.Errorf("empty cursor: %+v, %v", c, err)
	}
	for _, bad := range []string{"!!!", "bm90IGpzb24"} { // not base64; base64 of "not json"
		if _, err := DecodeCursor[pos](bad); err == nil {
			t.Errorf("DecodeCursor(%q) should fail", bad)
		}
	}
}
