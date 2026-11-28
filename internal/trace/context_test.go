package trace

import "testing"

func TestParseContext(t *testing.T) {
	for _, blank := range []string{"", "  \n"} {
		if c, err := ParseContext(blank); c != nil || err != nil {
			t.Errorf("ParseContext(%q) = %v, %v; want no context", blank, c, err)
		}
	}
	c, err := ParseContext(` {"user_ip": "10.1.2.3", "n": 2} `)
	if err != nil || c.GetFields()["user_ip"].GetStringValue() != "10.1.2.3" || c.GetFields()["n"].GetNumberValue() != 2 {
		t.Errorf("ParseContext: %v, %v", c, err)
	}
	for _, bad := range []string{`[1, 2]`, `{"a":`, `"text"`} {
		if _, err := ParseContext(bad); err == nil {
			t.Errorf("ParseContext(%q) should fail", bad)
		}
	}
}
