package web

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"testing"
)

// TestPaletteContrast checks the color tokens in app.css against WCAG AA:
// body and outcome colors are used as text, so they need 4.5:1 against both
// the page background and panels, in the light and the dark theme.
func TestPaletteContrast(t *testing.T) {
	css, err := os.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile(`(?s):root \{(.*?)\n\s*\}`).FindAllSubmatch(css, 2)
	if len(blocks) != 2 {
		t.Fatalf("expected light and dark :root blocks, found %d", len(blocks))
	}
	textTokens := []string{"fg", "muted", "accent", "allowed", "denied", "unknown", "error", "skipped", "mismatch", "probe", "ttu", "direct", "derived", "hybrid"}
	for i, theme := range []string{"light", "dark"} {
		vars := map[string]string{}
		for _, m := range regexp.MustCompile(`--([a-z]+):\s*(#[0-9a-fA-F]{6})`).FindAllSubmatch(blocks[i][1], -1) {
			vars[string(m[1])] = string(m[2])
		}
		for _, bg := range []string{"bg", "panel"} {
			for _, tok := range textTokens {
				c := contrast(vars[tok], vars[bg])
				if c < 4.5 {
					t.Errorf("%s theme: --%s %s on --%s %s has contrast %.2f, want ≥ 4.5", theme, tok, vars[tok], bg, vars[bg], c)
				}
			}
		}
		t.Logf("%s theme: lowest outcome contrast on panel = %.2f", theme, minContrast(vars, []string{"allowed", "denied", "unknown", "accent"}, "panel"))
	}
}

func minContrast(vars map[string]string, toks []string, bg string) float64 {
	m := math.Inf(1)
	for _, t := range toks {
		m = math.Min(m, contrast(vars[t], vars[bg]))
	}
	return m
}

func luminance(hex string) float64 {
	ch := func(i int) float64 {
		v, _ := strconv.ParseUint(hex[i:i+2], 16, 8)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(1) + 0.7152*ch(3) + 0.0722*ch(5)
}

func contrast(a, b string) float64 {
	if len(a) != 7 || len(b) != 7 {
		return 0
	}
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
