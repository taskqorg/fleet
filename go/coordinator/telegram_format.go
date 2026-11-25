// Agent replies are Markdown; Telegram shows a subset of HTML (docs/designs/fleet-telegram.md, Replies). tgHTML turns
// the Markdown an agent writes into that subset: bold, italic, inline code, code blocks, links, headings as bold lines and
// list bullets. Everything else is escaped text, so a reply never shows raw asterisks or backticks, and a refused
// formatting falls back to the plain text (telegramSent).
package main

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
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

// tgLine converts one line outside code blocks. Code spans stay literal: they are set aside as placeholders, the whole
// line gets inline formatting (so bold may wrap a code span), then they come back as <code>.
func tgLine(line string) string {
	if m := mdHeading.FindStringSubmatch(line); m != nil {
		line = "**" + m[1] + "**"
	}
	line = mdBullet.ReplaceAllString(line, "${1}• ")
	var spans []string
	var kept strings.Builder
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			break
		}
		end := strings.IndexByte(line[start+1:], '`')
		if end < 0 {
			break
		}
		kept.WriteString(line[:start])
		kept.WriteString("\x00" + strconv.Itoa(len(spans)) + "\x00")
		spans = append(spans, line[start+1:start+1+end])
		line = line[start+end+2:]
	}
	kept.WriteString(line)
	out := tgInline(kept.String())
	for i, sp := range spans {
		out = strings.Replace(out, "\x00"+strconv.Itoa(i)+"\x00", "<code>"+html.EscapeString(sp)+"</code>", 1)
	}
	return out
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

// tgPlain: the text without its Markdown marks, for the plain resend after Telegram refused the HTML (a reply never
// shows raw asterisks, owner-tester 2026-11-25).
func tgPlain(md string) string {
	s := mdLink.ReplaceAllString(md, "$1 ($2)")
	s = mdBold.ReplaceAllString(s, "$1$2")
	s = strings.ReplaceAll(s, "```", "")
	return strings.ReplaceAll(s, "`", "")
}

// --- rich messages (Bot API 10: sendRichMessage, InputRichMessage.markdown) ---
//
// Rich Markdown is GitHub Flavored Markdown with up to 32768 characters per message: tables, headings, lists, code,
// <details> and images render as the agent wrote them, so a long answer needs no splitting at 4096 and no
// conversion (PilotQ sent every message this way; Fleet adopts it, docs/designs/fleet-telegram.md, Replies). tgRich
// only fixes what Telegram reads differently from a Markdown viewer:
//   - Rich Markdown also accepts HTML: '<' and '&' outside code are escaped, so model text never becomes markup; only
//     <details> and <summary> (a folded block) stay tags.
//   - A single line break is a soft break, shown as a space: a line followed by more paragraph text ends in a hard break.
//   - "#89 stays open" at a line start would be a heading: a '#' not followed by a space is escaped.
//   - Rich Markdown reads $...$ as a formula: '$' outside code is escaped, since money ($2,52) is far more common in
//     answers than math (owner-tester F23).
//   - Telegram links bare names like fleet.md as domains: file names and paths go into code spans.
//   - "13:47 UTC" and ISO times become date-time entities, so every reader sees their own local time.

// telegramRichLimit: the rich message limit is 32768 UTF-8 characters; read as bytes, 16000 runes of Cyrillic (two bytes
// each) still fit with the marks tgRich adds, and a longer reply goes out in parts.
const telegramRichLimit = 16000

var (
	richISOTime = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})(?::(\d{2}))?(?:\.\d+)?(?:Z| ?UTC)`)
	richUTCTime = regexp.MustCompile(`\b([01]?\d|2[0-3]):([0-5]\d)(?::([0-5]\d))? ?UTC\b`)
	richTimes   = regexp.MustCompile(richISOTime.String() + "|" + richUTCTime.String())
	// richTags: the HTML an agent may use on purpose (a folded block); every other tag stays text.
	richTags = strings.NewReplacer("&lt;details>", "<details>", "&lt;details open>", "<details open>", "&lt;/details>", "</details>",
		"&lt;summary>", "<summary>", "&lt;/summary>", "</summary>")
	richFileExt = map[string]bool{"md": true, "go": true, "ts": true, "tsx": true, "js": true, "mjs": true, "json": true, "sh": true,
		"py": true, "rs": true, "sql": true, "yaml": true, "yml": true, "toml": true, "txt": true, "html": true, "css": true,
		"svg": true, "png": true, "jpg": true, "jpeg": true, "log": true, "env": true, "lock": true, "mod": true, "sum": true, "csv": true}
)

// tgRich converts agent Markdown into rich Markdown for Telegram; now places a time without a date on its nearest day.
func tgRich(md string, now time.Time) string {
	lines := strings.Split(md, "\n")
	var out strings.Builder
	fence := ""
	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		t := strings.TrimLeft(line, " ")
		if fence != "" {
			out.WriteString(line)
			if strings.HasPrefix(t, fence) && strings.TrimSpace(strings.TrimLeft(t, fence[:1])) == "" {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = t[:3]
			out.WriteString(line)
			continue
		}
		if strings.HasPrefix(t, "#") && len(t) > 1 && t[1] != ' ' && t[1] != '#' {
			line = strings.Repeat(" ", len(line)-len(t)) + `\` + t
		}
		out.WriteString(richLine(line, now))
		if i+1 < len(lines) && softBreak(line, lines[i+1]) {
			out.WriteString("  ")
		}
	}
	return out.String()
}

// softBreak: the line and the next one are text of one paragraph, so the break between them must be a hard one.
func softBreak(line, next string) bool {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "|") || strings.HasSuffix(line, "  ") || strings.HasSuffix(t, `\`) ||
		strings.HasPrefix(t, "<") {
		return false
	}
	n := strings.TrimSpace(next)
	if n == "" || mdBullet.MatchString(next) || mdOrdered.MatchString(n) {
		return false
	}
	for _, mark := range []string{"#", ">", "|", "```", "~~~", "<", "---", "***"} {
		if strings.HasPrefix(n, mark) {
			return false
		}
	}
	return true
}

var mdOrdered = regexp.MustCompile(`^\d+[.)]\s`)

// richLine: one line outside code blocks; code spans stay as written.
func richLine(line string, now time.Time) string {
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
		out.WriteString(richText(line[:start], now))
		out.WriteString(line[start : start+end+2])
		line = line[start+end+2:]
	}
	out.WriteString(richText(line, now))
	return out.String()
}

// richText: text outside code: escaped HTML, local times, file names in code spans.
func richText(s string, now time.Time) string {
	s = richTags.Replace(strings.NewReplacer("&", "&amp;", "<", "&lt;", `\$`, `\$`, "$", `\$`).Replace(s)) // an escaped \$ stays one
	words := strings.Split(s, " ")
	for i, w := range words {
		words[i] = richWord(w)
	}
	s = strings.Join(words, " ")
	// One pass for both forms, so the date-time entity of an ISO time is never matched again by its "13:47 UTC".
	return richTimes.ReplaceAllStringFunc(s, func(m string) string {
		if p := richISOTime.FindStringSubmatch(m); p != nil && p[0] == m {
			at, err := time.Parse("2006-01-02 15:04:05", fmt.Sprintf("%s-%s-%s %s:%s:%s", p[1], p[2], p[3], p[4], p[5], or(p[6], "00")))
			if err != nil {
				return m
			}
			return richTime(m, at, "dt")
		}
		p := richUTCTime.FindStringSubmatch(m)
		if p == nil {
			return m
		}
		h, _ := strconv.Atoi(p[1])
		mi, _ := strconv.Atoi(p[2])
		sec, _ := strconv.Atoi(or(p[3], "0"))
		u := now.UTC()
		at := time.Date(u.Year(), u.Month(), u.Day(), h, mi, sec, 0, time.UTC)
		// The nearest such moment: "23:50 UTC" read just after midnight is yesterday's.
		if d := at.Sub(u); d > 12*time.Hour {
			at = at.Add(-24 * time.Hour)
		} else if d < -12*time.Hour {
			at = at.Add(24 * time.Hour)
		}
		return richTime(m, at, "t")
	})
}

// richTime: a date-time entity; the written text stays its alternative where an app shows no local time.
func richTime(text string, at time.Time, format string) string {
	return fmt.Sprintf("![%s](tg://time?unix=%d&format=%s)", text, at.Unix(), format)
}

// richWord puts a bare file name or path into a code span: Telegram would link fleet.md or go/coordinator as domains.
// Links, commands (/help), URLs, versions, domains and directories stay as written.
func richWord(w string) string {
	core := strings.TrimLeft(w, `("*_`)
	head := w[:len(w)-len(core)]
	core = strings.TrimRight(core, `.,;:!?)"*_`)
	tail := w[len(head)+len(core):]
	if core == "" || strings.Contains(core, "://") || strings.ContainsAny(core, "[]()<>&@`") {
		return w
	}
	for _, c := range core {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_-./", c)) {
			return w
		}
	}
	file := core[strings.LastIndexByte(core, '/')+1:]
	if dot := strings.LastIndexByte(file, '.'); dot <= 0 || !richFileExt[strings.ToLower(file[dot+1:])] {
		return w // a path without a file name is no domain to Telegram
	}
	return head + "`" + core + "`" + tail
}

func or(s, otherwise string) string {
	if s == "" {
		return otherwise
	}
	return s
}
