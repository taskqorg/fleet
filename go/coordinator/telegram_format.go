// Agent replies are Markdown; Telegram shows a subset of HTML (docs/designs/fleet-telegram.md, Replies). tgHTML turns
// the Markdown an agent writes into that subset: bold, italic, inline code, code blocks, links, headings as bold lines and
// list bullets. Everything else is escaped text, so a reply never shows raw asterisks or backticks, and a refused
// formatting falls back to the plain text (telegramSent).
package main

import (
	"html"
	"regexp"
	"strings"
)

var (
	mdLink    = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	mdBold    = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	mdItalic  = regexp.MustCompile(`(^|[\s(])[*_]([^*_\s][^*_\n]*?)[*_]([\s).,:;!?]|$)`)
	mdHeading = regexp.MustCompile(`^#{1,6}\s+(.+)$`)
	mdBullet  = regexp.MustCompile(`^(\s*)[-*+]\s+`)
)

// tgHTML converts Markdown to Telegram HTML. Code spans and blocks are cut out first, so their text is only escaped.
func tgHTML(md string) string {
	var out strings.Builder
	lines := strings.Split(md, "\n")
	inBlock := false
	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inBlock {
				out.WriteString("</pre>")
			} else {
				out.WriteString("<pre>")
			}
			inBlock = !inBlock
			continue
		}
		if inBlock {
			out.WriteString(html.EscapeString(line))
			continue
		}
		out.WriteString(tgLine(line))
	}
	if inBlock {
		out.WriteString("</pre>") // a block a chunk cut off still closes
	}
	return out.String()
}

// tgLine converts one line outside code blocks: code spans stay literal, the rest gets inline formatting.
func tgLine(line string) string {
	if m := mdHeading.FindStringSubmatch(line); m != nil {
		return "<b>" + tgInline(m[1]) + "</b>"
	}
	line = mdBullet.ReplaceAllString(line, "${1}• ")
	var out strings.Builder
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			break
		}
		end := strings.IndexByte(line[start+1:], '`')
		if end < 0 {
			break
		}
		out.WriteString(tgInline(line[:start]))
		out.WriteString("<code>" + html.EscapeString(line[start+1:start+1+end]) + "</code>")
		line = line[start+end+2:]
	}
	out.WriteString(tgInline(line))
	return out.String()
}

// tgInline: escaped text with links, bold and italic.
func tgInline(s string) string {
	s = html.EscapeString(s)
	s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
		p := mdLink.FindStringSubmatch(m)
		return `<a href="` + p[2] + `">` + p[1] + "</a>"
	})
	s = mdBold.ReplaceAllStringFunc(s, func(m string) string {
		p := mdBold.FindStringSubmatch(m)
		return "<b>" + p[1] + p[2] + "</b>"
	})
	return mdItalic.ReplaceAllString(s, "${1}<i>${2}</i>${3}")
}
