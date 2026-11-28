package web

import (
	"html"
	"html/template"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	dslKeyword = regexp.MustCompile(`\b(model|schema|type|relations|define|condition|module|extend)\b`)
	dslOp      = regexp.MustCompile(`\b(or|and|but not|from|with)\b`)
	dslComment = regexp.MustCompile(`(?m)#.*$`)
)

// highlightDSL adds minimal syntax highlighting to OpenFGA DSL.
func highlightDSL(src string) template.HTML {
	var b strings.Builder
	for i, line := range strings.Split(src, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		code, comment := line, ""
		if loc := dslComment.FindStringIndex(line); loc != nil && strings.TrimSpace(line[:loc[0]]) == "" {
			code, comment = line[:loc[0]], line[loc[0]:]
		}
		e := html.EscapeString(code)
		e = dslKeyword.ReplaceAllString(e, `<span class="kw">$1</span>`)
		e = dslOp.ReplaceAllString(e, `<span class="op">$1</span>`)
		b.WriteString(e)
		if comment != "" {
			b.WriteString(`<span class="cm">` + html.EscapeString(comment) + `</span>`)
		}
	}
	return template.HTML(b.String())
}

// truncate shortens s to n characters (not bytes, so a multi-byte character
// is never cut), marking the cut with "…".
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
