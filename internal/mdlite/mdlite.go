// Package mdlite renders the small subset of Markdown found in notes and
// changelogs: **bold**, `code`, [text](http(s) url), and [[wikilinks]] and
// [text](relative.md) links (both shown as plain text). Everything else is
// escaped. Input is untrusted.
package mdlite

import (
	"html"
	"html/template"
	"regexp"
	"strconv"
	"strings"
)

var (
	linkRe = regexp.MustCompile(`\[([^\[\]]+)\]\((https?://[^\s()\x00]+)\)`)
	// relRe is a link with no scheme (a note's [title](other_note.md)): the
	// target means nothing here, so only the text is kept.
	relRe  = regexp.MustCompile(`\[([^\[\]]+)\]\(([^\s():\x00]+)\)`)
	wikiRe = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)
	boldRe = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	codeRe = regexp.MustCompile("\x00([0-9]+)\x00")
)

// Render returns safe HTML for s. Code spans become placeholders first, so
// bold and links work across them ("**run `x` now**").
func Render(s string) template.HTML {
	s = strings.ReplaceAll(s, "\x00", "")
	var b strings.Builder
	var code []string
	parts := strings.Split(s, "`")
	// An odd count of backticks leaves the last segment unclosed: treat it as text.
	closed := len(parts)%2 == 1
	for i, p := range parts {
		if i%2 == 1 && (closed || i < len(parts)-1) {
			b.WriteString("\x00" + strconv.Itoa(len(code)) + "\x00")
			code = append(code, "<code>"+html.EscapeString(p)+"</code>")
			continue
		}
		if i%2 == 1 {
			b.WriteString("`")
		}
		b.WriteString(p)
	}
	out := codeRe.ReplaceAllStringFunc(inline(b.String()), func(m string) string {
		n, err := strconv.Atoi(m[1 : len(m)-1])
		if err != nil || n >= len(code) {
			return ""
		}
		return code[n]
	})
	return template.HTML(out)
}

// Plain strips the markup and returns text (for titles and search).
func Plain(s string) string {
	s = linkRe.ReplaceAllString(s, "$1")
	s = relRe.ReplaceAllString(s, "$1")
	s = wikiRe.ReplaceAllString(s, "$1")
	s = boldRe.ReplaceAllString(s, "$1")
	return strings.ReplaceAll(s, "`", "")
}

func inline(s string) string {
	e := html.EscapeString(s)
	e = wikiRe.ReplaceAllString(e, "$1")
	e = linkRe.ReplaceAllString(e, `<a href="$2" rel="noopener noreferrer">$1</a>`)
	e = relRe.ReplaceAllString(e, "$1")
	e = boldRe.ReplaceAllString(e, "<strong>$1</strong>")
	return e
}
