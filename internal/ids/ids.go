// Package ids applies per-type identifier rules: turning what a person types
// (e.g. "cn=alice smith") into the stored object ID (e.g. "CN%3DALICE%20SMITH")
// and rendering stored IDs readably.
package ids

import (
	"fmt"
	"net/url"
	"strings"
)

// Rule describes how IDs of one type are normalized.
type Rule struct {
	// Trim removes surrounding whitespace.
	Trim bool `yaml:"trim"`
	// Case is "upper", "lower", or "" (unchanged).
	Case string `yaml:"case"`
	// Encode lists characters that are percent-encoded in stored IDs, e.g. ":#% ".
	// "%" is always encoded when any characters are listed, so encoding is reversible.
	Encode string `yaml:"encode"`
	// Display is "decoded" to show percent-decoded IDs, or "raw".
	Display string `yaml:"display"`
	// Hint is shown as the picker placeholder, e.g. "CN=Name or account number".
	Hint string `yaml:"hint"`
}

// Validate checks the rule.
func (r Rule) Validate() error {
	switch r.Case {
	case "", "upper", "lower":
	default:
		return fmt.Errorf("case must be upper, lower, or empty; got %q", r.Case)
	}
	switch r.Display {
	case "", "raw", "decoded":
	default:
		return fmt.Errorf("display must be raw or decoded; got %q", r.Display)
	}
	return nil
}

// Rules maps type names to rules.
type Rules map[string]Rule

// For returns the rule for a type (the zero rule when none is configured).
func (rs Rules) For(typ string) Rule { return rs[typ] }

// Normalize converts user input into the stored ID form. It is idempotent:
// input that is already encoded is decoded first, so "CN%3DALICE" and
// "CN=ALICE" both normalize to "CN%3DALICE". It is safe to apply to a prefix.
func (r Rule) Normalize(input string) string {
	s := input
	if r.Trim {
		s = strings.TrimSpace(s)
	}
	if r.Encode != "" {
		s = decodeComplete(s)
	}
	switch r.Case {
	case "upper":
		s = strings.ToUpper(s)
	case "lower":
		s = strings.ToLower(s)
	}
	if r.Encode != "" {
		s = encode(s, r.Encode+"%")
	}
	return s
}

// Display renders a stored ID for people.
func (r Rule) DisplayID(id string) string {
	if r.Display == "decoded" {
		if d, err := url.PathUnescape(id); err == nil {
			return d
		}
	}
	return id
}

func encode(s, chars string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if strings.IndexByte(chars, c) >= 0 {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// decodeComplete decodes valid %XX escapes and leaves anything else (including
// a trailing partial escape typed so far) untouched.
func decodeComplete(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHex(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c >= 'a':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}
