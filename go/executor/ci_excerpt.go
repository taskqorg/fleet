package main

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"fleet/contract"
)

const (
	ciTailLimit    = 4096
	ciWindowLimit  = 2048
	ciWindowCount  = 12
	ciContextLines = 2
)

var (
	// Strip terminal escape sequences and invalid presentation controls before matching credentials.
	ciEscape        = regexp.MustCompile("\\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\[[0-?]*[ -/]*$|\\][^\\x07\\x1b]*(?:\\x07|\\x1b\\\\|$)|[ -/]*[@-Z\\\\-_])")
	ciSectionCR     = regexp.MustCompile(`(section_(?:start|end):[0-9]+:[^\r\n]+)\r`)
	ciSensitive     = regexp.MustCompile(`(?im)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)|\b(?:gh[pousr]_|github_pat_|glpat-|glrt-|gldt-|glptt-|AKIA|ASIA|xox[baprs]-)[a-z0-9_-]+|\b(?:https?|ssh)://[^\s/@]+:[^\s/@]+@|\b(?:authorization|private[-_]token|job[-_]token|[a-z0-9_]*(?:token|password|secret|api_key|access_key|credentials))\s*[:=][^\r\n]*|\bbearer[ \t]+[^\r\n]+`)
	ciCompilerError = regexp.MustCompile(`(?i)(?:\.[a-z][a-z0-9]*:[0-9]+(?::[0-9]+)?:[ \t]+|\berror(?:\[[[:alnum:]]+\])?:[ \t]+)`)
	ciSectionLine   = regexp.MustCompile(`(?:^|T[^ ]+ )section_(start|end):[0-9]+:([[:alnum:]_.-]+)(?:\[[^\]]*\])?$`)
)

func sanitizeCIText(text, token string, scanCut bool) string {
	text = strings.ToValidUTF8(text, "")
	text = ciEscape.ReplaceAllString(text, "")
	text = ciSectionCR.ReplaceAllString(text, "${1}\n")
	text = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			return -1
		}
		return r
	}, text)
	text = redactCIExact(text, token, scanCut)
	return ciSensitive.ReplaceAllString(text, "[REDACTED]")
}

// Match before slicing any lines or windows. Preserve one replacement across
// overlapping matches and tolerate a credential split by terminal line wrapping.
func redactCIExact(text, token string, scanCut bool) string {
	if token == "" {
		return text
	}
	var out strings.Builder
	end := 0
	for from := 0; from < len(text); {
		next := strings.IndexByte(text[from:], token[0])
		if next < 0 {
			break
		}
		start := from + next
		from = start + 1
		pos, n := from, 1
		for pos < len(text) && n < len(token) {
			switch text[pos] {
			case '\n', '\t':
				// The forge credential itself cannot contain whitespace.
			default:
				if text[pos] != token[n] {
					goto matched
				}
				n++
			}
			pos++
		}
	matched:
		if n == len(token) || (scanCut && pos == len(text)) {
			if start >= end {
				out.WriteString(text[end:start])
				out.WriteString("[REDACTED]")
			}
			end = max(end, pos)
		}
	}
	if end == 0 {
		return text
	}
	out.WriteString(text[end:])
	return out.String()
}

func ciMetadataText(text, token string) *string {
	text = ciClipPrefix(strings.TrimSpace(sanitizeCIText(text, token, false)), 160)
	if text == "" {
		return nil
	}
	return &text
}

func ciClipPrefix(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

type ciWindow struct {
	contract.PublishingCIWindow
	text     string
	priority int
}

type ciTraceLine struct {
	text    string
	partial bool
}

func ciMarker(line string) (offset, priority int) {
	for _, marker := range []string{"--- FAIL:", "panic:", "fatal error:", "FAIL\t"} {
		if i := strings.Index(line, marker); i >= 0 {
			return i, 3
		}
	}
	for _, marker := range []string{"##[error]", "::error", "ERROR: Job failed", "Process completed with exit code"} {
		if i := strings.Index(line, marker); i >= 0 {
			return i, 1
		}
	}
	if loc := ciCompilerError.FindStringIndex(line); loc != nil {
		return loc[0], 3
	}
	return 0, 0
}

func hasCIPrimaryWindow(windows []ciWindow) bool {
	return slices.ContainsFunc(windows, func(w ciWindow) bool { return w.priority >= 3 })
}

func selectCITrace(text string, gitlab bool) ([]ciWindow, int64, ciWindow) {
	var windows []ciWindow
	var active ciWindow
	var recent [ciContextLines]ciTraceLine
	var lineNo, lastMarker, omitted int64
	var sections []string
	flush := func() {
		if active.StartLine == nil {
			return
		}
		// Keep concrete failures ahead of wrapper summaries. Preserve source order
		// at equal priority, even when a later failure displaces a wrapper.
		if len(windows) < ciWindowCount {
			windows = append(windows, active)
		} else {
			weakest := len(windows) - 1
			for i := weakest; i >= 0; i-- {
				if windows[i].priority < windows[weakest].priority {
					weakest = i
				}
			}
			if active.priority > windows[weakest].priority {
				windows = append(slices.Delete(windows, weakest, weakest+1), active)
			}
			omitted++
		}
		active = ciWindow{}
	}
	appendLine := func(line ciTraceLine, number int64) {
		if active.EndLine != nil && len(active.text) < ciWindowLimit {
			active.text += "\n"
		}
		shown := ciClipPrefix(line.text, ciWindowLimit-len(active.text))
		if line.partial || len(shown) != len(line.text) {
			active.Partial = &yes
		}
		if len(active.text) < ciWindowLimit || shown != "" {
			active.text += shown
			active.EndLine = &number
		}
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(text, "\n"), "\n") {
		lineNo++
		var section *string
		if gitlab {
			if marker := ciSectionLine.FindStringSubmatch(line); marker != nil {
				if marker[1] == "start" {
					sections = append(sections, marker[2])
				} else if len(sections) > 0 && sections[len(sections)-1] == marker[2] {
					sections = sections[:len(sections)-1]
				}
			} else if len(sections) > 0 {
				name := ciClipPrefix(sections[len(sections)-1], 160)
				section = &name
			}
		}
		markerAt, priority := ciMarker(line)
		// An oversized line can contain an error well after its opening bytes.
		start := max(0, markerAt-128)
		for start > 0 && !utf8.RuneStart(line[start]) {
			start--
		}
		shown := ciClipPrefix(line[start:], 768)
		current := ciTraceLine{shown, len(shown) != len(line)}
		if active.StartLine != nil && (lineNo > lastMarker+ciContextLines ||
			(priority > active.priority) || (priority > 0 && len(active.text)+len(shown)+1 > ciWindowLimit)) {
			flush()
		}
		if priority > 0 {
			if active.StartLine == nil {
				first := max(int64(1), lineNo-ciContextLines)
				active = ciWindow{PublishingCIWindow: contract.PublishingCIWindow{
					Source: contract.PublishingCIWindowSourceLog, StartLine: &first, Section: section,
				}}
				for n := first; n < lineNo; n++ {
					appendLine(recent[(n-1)%ciContextLines], n)
				}
			}
			lastMarker, active.priority = lineNo, max(active.priority, priority)
		}
		if active.StartLine != nil {
			appendLine(current, lineNo)
		}
		recent[(lineNo-1)%ciContextLines] = current
	}
	flush()

	tail := ciWindow{PublishingCIWindow: contract.PublishingCIWindow{Source: contract.PublishingCIWindowSourceLog}}
	if text != "" {
		start := max(0, len(text)-ciTailLimit)
		for !utf8.RuneStart(text[start]) {
			start++
		}
		tail.text = text[start:]
		first := lineNo - int64(strings.Count(strings.TrimSuffix(tail.text, "\n"), "\n"))
		tail.StartLine, tail.EndLine = &first, &lineNo
		if start > 0 && text[start-1] != '\n' {
			tail.Partial = &yes
		}
	}
	return windows, omitted, tail
}

func fillCIExcerpt(reply *contract.PublishingCILogsReply, windows []ciWindow, omitted int64, tail ciWindow, fullTail bool) {
	reply.OmittedWindows = omitted
	reply.Truncated = !fullTail || reply.ScanStatus != contract.PublishingCIScanStatusComplete ||
		omitted > 0 || reply.OmittedFailedSteps != nil ||
		(reply.AnnotationStatus != nil && *reply.AnnotationStatus == contract.PublishingCIAnnotationStatusPartial)

	render := func(selected []ciWindow) contract.PublishingCILogsReply {
		r := *reply
		r.Windows = make([]contract.PublishingCIWindow, 0, len(selected))
		// Present log windows in trace order, followed by annotations and tail.
		ordered := slices.Clone(selected)
		slices.SortStableFunc(ordered, func(a, b ciWindow) int {
			if a.Source != b.Source {
				return strings.Compare(string(b.Source), string(a.Source))
			}
			if a.Source == contract.PublishingCIWindowSourceLog {
				return int(*a.StartLine - *b.StartLine)
			}
			return 0
		})
		for _, window := range ordered {
			r.Windows = append(r.Windows, appendCIExcerpt(&r, window))
			r.Truncated = r.Truncated || window.Partial != nil
		}
		if tail.text != "" {
			window := appendCIExcerpt(&r, tail)
			r.Tail = &window
		}
		return r
	}
	// Reserve the tail before any error windows, using the actual JSON size.
	for len(enc(render(nil))) > ciLogLimit && len(tail.text) > 0 {
		start := max(1, len(tail.text)/2)
		for start < len(tail.text) && !utf8.RuneStart(tail.text[start]) {
			start++
		}
		first := *tail.StartLine + int64(strings.Count(tail.text[:start], "\n"))
		tail.StartLine, tail.Partial, tail.text = &first, &yes, tail.text[start:]
		reply.Truncated = true
	}

	slices.SortStableFunc(windows, func(a, b ciWindow) int { return b.priority - a.priority })
	var selected []ciWindow
	// Budget for the final omission count even while selection is in progress.
	budget := ciLogLimit - len(strconv.FormatInt(omitted+int64(len(windows)), 10))
	for _, window := range windows {
		next := append(selected, window)
		if len(enc(render(next))) > budget {
			reply.OmittedWindows++
			reply.Truncated = true
			continue
		}
		selected = next
	}
	*reply = render(selected)
}

func appendCIExcerpt(reply *contract.PublishingCILogsReply, window ciWindow) contract.PublishingCIWindow {
	if reply.Excerpt != "" {
		reply.Excerpt += "\n\n"
	}
	window.StartByte = int64(len(reply.Excerpt))
	reply.Excerpt += window.text
	window.EndByte = int64(len(reply.Excerpt))
	return window.PublishingCIWindow
}
