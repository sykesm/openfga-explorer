package web

import (
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strings"
)

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

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
