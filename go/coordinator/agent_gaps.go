package main

// Agent gaps (docs/designs/fleet-telemetry.md#10-agent-gaps): what agents lack. A failed tool call of an agent run (a failed tool
// block in the run's stored events, fleetNormalized.blocks) and a failed gateway call (a tool_run row of spool ingest)
// get a gap class from the rule table config/agent-gaps.json at ingest; agent_gap keeps one row per classified failure.
// agent_gaps reads a window grouped by class with daily counts and the setting that controls each class;
// agent_gaps_reclassify, and the agent-gaps pass when the rules change, classify the retained window again.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5/pgtype"
)

func init() {
	onStart(func(ctx context.Context, a *App) {
		everyWake(ctx, "agent-gaps", time.Hour, nil, true, func(ctx context.Context, _ bool) error { return agentGapsCatchUp(ctx, a) })
	})
	op("agent_gaps", agentGaps)
	op("agent_gaps_reclassify", agentGapsReclassify)
}

const (
	gapLineMax      = 200 // runes of a decisive line
	gapDefaultDays  = 7
	gapMaxDays      = 30
	gapReclassDays  = 30 // the window the agent-gaps pass classifies again when the rules change
	gapReclassMax   = 90
	gapExamples     = 5
	gapBreakdownMax = 12
	gapReadMax      = 50000 // rows one read counts (AgentGapRows reads one more to tell)
)

// gapRules: config/agent-gaps.json, compiled.
type gapRules struct {
	Families   map[string]gapFamily `json:"families"`
	Finding    gapFindingRules      `json:"finding"`
	ShellTools string               `json:"shellTools"`
	Rules      []gapRule            `json:"rules"`
	shell      *regexp.Regexp
	digest     string
}

type gapFamily struct {
	Label   string                   `json:"label"`
	Title   string                   `json:"title"`
	Setting contract.AgentGapSetting `json:"setting"`
}

type gapRule struct {
	Source string `json:"source"`
	Shell  *bool  `json:"shell"`
	Match  string `json:"match"`
	Class  string `json:"class"`
	Why    string `json:"why"`
	re     *regexp.Regexp
}

var (
	gapRulesMu     sync.Mutex
	gapRulesLoaded *gapRules
	gapRulesErr    error
)

func gapRulesPath() string { return filepath.Join(configDir(), "agent-gaps.json") }

// loadGapRules reads and compiles the rule table once per process (a release ships its config; a change takes a
// restart, which then reclassifies).
func loadGapRules() (*gapRules, error) {
	gapRulesMu.Lock()
	defer gapRulesMu.Unlock()
	if gapRulesLoaded == nil && gapRulesErr == nil {
		raw, err := os.ReadFile(gapRulesPath())
		if err == nil {
			gapRulesLoaded, gapRulesErr = parseGapRules(raw)
		} else {
			gapRulesErr = err
		}
	}
	return gapRulesLoaded, gapRulesErr
}

var gapClassTemplate = regexp.MustCompile(`\{(\d+|tool|connection)\}`)

func parseGapRules(raw []byte) (*gapRules, error) {
	var r gapRules
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("agent-gaps.json: %w", err)
	}
	var err error
	if r.shell, err = regexp.Compile(r.ShellTools); err != nil {
		return nil, fmt.Errorf("agent-gaps.json shellTools: %w", err)
	}
	if err := r.Finding.valid(); err != nil {
		return nil, err
	}
	if _, ok := r.Families["other"]; !ok {
		return nil, fmt.Errorf("agent-gaps.json: family other is required")
	}
	for i := range r.Rules {
		x := &r.Rules[i]
		if x.Source != "agent" && x.Source != "gateway" {
			return nil, fmt.Errorf("agent-gaps.json rule %d: source must be agent or gateway", i)
		}
		if x.re, err = regexp.Compile(x.Match); err != nil {
			return nil, fmt.Errorf("agent-gaps.json rule %d: %w", i, err)
		}
		if x.Class == "skip" {
			continue
		}
		if _, ok := r.Families[gapFamilyOf(x.Class)]; !ok {
			return nil, fmt.Errorf("agent-gaps.json rule %d: class %s has no family", i, x.Class)
		}
		for _, m := range gapClassTemplate.FindAllStringSubmatch(x.Class, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil && n > x.re.NumSubexp() {
				return nil, fmt.Errorf("agent-gaps.json rule %d: class names group %d of %d", i, n, x.re.NumSubexp())
			}
		}
	}
	h := sha256.Sum256(raw)
	r.digest = hex.EncodeToString(h[:6])
	return &r, nil
}

func gapFamilyOf(class string) string { f, _, _ := strings.Cut(class, ":"); return f }

// gapFailure: one failed call as the rules see it. Text is the agent tool's output or error, or the gateway call's
// error code.
type gapFailure struct {
	Source, Tool, Connection, Text string
}

// classify answers the failure's class and decisive line; ok is false when it is no gap (a skip rule, or a shell
// command no rule names: its exit status is the program's answer, not something the agent lacks).
func (r *gapRules) classify(f gapFailure) (class, line string, ok bool) {
	shell := f.Source == "agent" && r.shell.MatchString(f.Tool)
	for _, x := range r.Rules {
		if x.Source != f.Source || (x.Shell != nil && *x.Shell != shell) {
			continue
		}
		m := x.re.FindStringSubmatchIndex(f.Text)
		if m == nil {
			continue
		}
		if x.Class == "skip" {
			return "", "", false
		}
		class = gapClassTemplate.ReplaceAllStringFunc(x.Class, func(t string) string {
			switch k := t[1 : len(t)-1]; k {
			case "tool":
				return gapKey(f.Tool)
			case "connection":
				return gapKey(f.Connection)
			default:
				n, _ := strconv.Atoi(k)
				if 2*n+1 < len(m) && m[2*n] >= 0 {
					return gapKey(f.Text[m[2*n]:m[2*n+1]])
				}
				return "-"
			}
		})
		return cutRunes(class, 120), lineAt(f.Text, m[0]), true
	}
	if shell || harnessFailure(f.Tool) {
		return "", "", false
	}
	return "other", decisiveLine(f.Text), true
}

// harnessFailure: a failed block that is the run's own lifecycle (blocks.own in the executor: unparsed, *.failed, a
// Claude error result), not a tool call; the run's failure cause covers it unless a rule names what it lacked.
func harnessFailure(tool string) bool {
	return tool == "unparsed" || tool == "result" || strings.HasSuffix(tool, ".failed")
}

func gapKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// lineAt: the line of text holding offset at, trimmed and cut to gapLineMax runes around at.
func lineAt(text string, at int) string {
	start := strings.LastIndexByte(text[:at], '\n') + 1
	end := len(text)
	if i := strings.IndexByte(text[at:], '\n'); i >= 0 {
		end = at + i
	}
	line := strings.TrimSpace(text[start:end])
	if utf8.RuneCountInString(line) <= gapLineMax {
		return line
	}
	from := max(at-start-gapLineMax/4, 0)
	for from > 0 && !utf8.RuneStart(text[start+from]) {
		from--
	}
	return cutRunes(strings.TrimSpace(text[start+from:end]), gapLineMax)
}

var decisiveWords = regexp.MustCompile(`(?i)error|fail|denied|refused|not found|invalid|unknown|cannot|can't|unable|rejected|missing`)

// decisiveLine: the first line naming an error, else the first non-empty line.
func decisiveLine(text string) string {
	first := ""
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if first == "" {
			first = l
		}
		if decisiveWords.MatchString(l) {
			return cutRunes(l, gapLineMax)
		}
	}
	return cutRunes(first, gapLineMax)
}

// ---- reading failures ----

// gapBlock: a tool block of fleetNormalized.blocks (contract.TranscriptTool as stored).
type gapBlock struct {
	Kind   string          `json:"kind"`
	Name   string          `json:"name"`
	State  string          `json:"state"`
	Output json.RawMessage `json:"output"`
}

// eventFailures: the failed tool calls of one stored event's data, with their block index. The tool's server is the
// MCP name prefix (mcp__<server>__<tool>) or, for Codex, the item's server; an empty output falls back to the
// item's error message (Codex keeps an MCP tool error there).
func eventFailures(data rawObject) []indexedFailure {
	var blocks []gapBlock
	json.Unmarshal(data.object("fleetNormalized")["blocks"], &blocks)
	var out []indexedFailure
	for i, b := range blocks {
		if b.Kind != "tool" || b.State != "failed" {
			continue
		}
		tool, server := b.Name, ""
		if srv, t, ok := strings.Cut(strings.TrimPrefix(tool, "mcp__"), "__"); ok && strings.HasPrefix(tool, "mcp__") {
			server, tool = srv, t
		}
		item := data.object("item")
		if s := item.text("server"); s != "" && server == "" {
			server = s
		}
		text := outputText(b.Output)
		if text == "" {
			text = errorText(item["error"])
		}
		if text == "" {
			text = errorText(data["error"])
		}
		out = append(out, indexedFailure{i, gapFailure{Source: "agent", Tool: tool, Connection: server, Text: text}})
	}
	return out
}

type indexedFailure struct {
	index int
	gapFailure
}

// outputText: a tool output as text: a string, the text parts of MCP content, an error's message, else its JSON.
func outputText(raw json.RawMessage) string {
	if isNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []rawObject
	if json.Unmarshal(raw, &parts) == nil {
		return mcpContentText(parts)
	}
	var o rawObject
	if json.Unmarshal(raw, &o) == nil {
		var content []rawObject
		if json.Unmarshal(o["content"], &content) == nil && len(content) > 0 {
			return mcpContentText(content)
		}
		if t := errorText(o["error"]); t != "" {
			return t
		}
		for _, k := range []string{"output", "stderr", "message", "text"} {
			if t := o.text(k); t != "" {
				return t
			}
		}
	}
	return string(raw)
}

func mcpContentText(parts []rawObject) string {
	var ts []string
	for _, p := range parts {
		if t := p.text("text"); t != "" {
			ts = append(ts, t)
		}
	}
	return strings.Join(ts, "\n")
}

// errorText: an error as a string or an object with a message.
func errorText(raw json.RawMessage) string {
	if isNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o rawObject
	if json.Unmarshal(raw, &o) == nil {
		return o.text("message")
	}
	return ""
}

func gapTime(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// recordEventGaps writes the classified failures of one stored event (id eventID, data its data) of an agent run.
func recordEventGaps(q *Request, r *gapRules, executionID string, projectID *string, eventID string, at time.Time, data rawObject) (int, int, error) {
	fs := eventFailures(data)
	n := 0
	for _, f := range fs {
		class, line, ok := r.classify(f.gapFailure)
		if !ok {
			continue
		}
		if err := q.db().AgentGapPut(q.ctx(), db.AgentGapPutParams{ID: eventID + "#" + strconv.Itoa(f.index), ExecutionID: executionID, ProjectID: projectID,
			Source: "agent", At: gapTime(at), Class: class, Family: gapFamilyOf(class), Tool: gapToolName(f.gapFailure), Line: line, Rules: r.digest}); err != nil {
			return len(fs), n, err
		}
		n++
	}
	return len(fs), n, nil
}

func gapToolName(f gapFailure) string {
	if f.Connection != "" {
		return f.Connection + "/" + f.Tool
	}
	return f.Tool
}

// ingestGaps: the gaps of a host's event batch (host_events.go ingestIn), for an agent run. A broken rule table is
// logged once by the pass, never refuses a batch.
func ingestGaps(q *Request, execution db.Execution, prepared []preparedEvent) error {
	if execution.Kind != db.ExecutionKindAgentRun {
		return nil
	}
	r, err := loadGapRules()
	if err != nil {
		return nil
	}
	for _, p := range prepared {
		v := p.value
		c := eventColumnsOf(v)
		if c.Delta {
			continue
		}
		at := time.Now()
		if c.At != nil {
			at = time.UnixMilli(int64(*c.At))
		}
		if _, _, err := recordEventGaps(q, r, execution.ID, execution.ProjectID, v.text("id"), at, v.object("data")); err != nil {
			return err
		}
	}
	return nil
}

// recordCallGap writes the gap of one failed gateway call (id its tool_run row): on the agent run that made it
// (parent), else on the call's own row.
func recordCallGap(q *Request, r *gapRules, id string, parent *string, projectID *string, at time.Time, tool, connection, code string) (bool, error) {
	class, line, ok := r.classify(gapFailure{Source: "gateway", Tool: tool, Connection: connection, Text: code})
	if !ok {
		return false, nil
	}
	owner := id
	if p := deref(parent); p != "" {
		exists, err := q.db().AgentGapRunExists(q.ctx(), p)
		if err != nil {
			return false, err
		}
		if exists {
			owner = p
		}
	}
	if line == "" {
		line = code
	}
	err := q.db().AgentGapPut(q.ctx(), db.AgentGapPutParams{ID: id, ExecutionID: owner, ProjectID: projectID, Source: "gateway", At: gapTime(at),
		Class: class, Family: gapFamilyOf(class), Tool: connection + "/" + tool, Line: line, Rules: r.digest})
	return err == nil, err
}

// ingestCallGap: the gap of a gateway call spool ingest just recorded (connection_ingest.go connRecord).
func ingestCallGap(q *Request, id string, parent, projectID *string, startedAt *int64, l contract.CallLine) error {
	r, err := loadGapRules()
	if err != nil {
		return nil
	}
	at := time.Now()
	if startedAt != nil {
		at = time.UnixMilli(*startedAt)
	}
	code := firstString(deref(l.ErrorCode), string(l.Outcome))
	_, err = recordCallGap(q, r, id, parent, projectID, at, l.Tool, l.Connection, code)
	return err
}

// ---- reclassification ----

const gapPage = 50

// reclassifyGaps classifies the failures of the last days again with the current rules: agent runs a page at a
// time (each page one transaction: its rows cleared, its failed events read and classified), then the failed gateway
// calls of the window. It records the pass so the agent-gaps pass does not repeat it.
func reclassifyGaps(ctx context.Context, a *App, q *Request, days int) (contract.AgentGapsReclassifyReply, error) {
	r, err := loadGapRules()
	if err != nil {
		return contract.AgentGapsReclassifyReply{}, fail(400, "config/agent-gaps.json: "+err.Error())
	}
	out := contract.AgentGapsReclassifyReply{Rules: r.digest}
	since := gapTime(time.Now().Add(-time.Duration(days) * 24 * time.Hour))
	after := ""
	for {
		var runs []db.AgentGapRunsRow
		if err := a.store.Read(ctx, q, func(q *Request) (err error) {
			runs, err = q.db().AgentGapRuns(q.ctx(), db.AgentGapRunsParams{Since: since, After: after, Lim: gapPage})
			return err
		}); err != nil {
			return out, err
		}
		if len(runs) == 0 {
			break
		}
		// The events are read without locks (a scan of every event of 50 runs); each run's rows are then written in a
		// short transaction locked on that run only, so ingest of a live run waits at most for its own write.
		type stored struct {
			id   string
			at   time.Time
			data rawObject
		}
		byRun := make([][]stored, len(runs))
		if err := a.store.Read(ctx, q, func(q *Request) error {
			for i, x := range runs {
				evs, err := q.db().AgentGapFailedEvents(q.ctx(), x.ID)
				if err != nil {
					return err
				}
				for _, e := range evs {
					var payload rawObject
					json.Unmarshal(e.Payload, &payload)
					at := time.Now()
					if e.At != nil {
						at = *e.At
					}
					byRun[i] = append(byRun[i], stored{e.ID, at, payload.object("data")})
				}
			}
			return nil
		}); err != nil {
			return out, err
		}
		for i, x := range runs {
			if err := a.store.Write(ctx, q, []string{"execution:" + x.ID}, func(q *Request) error {
				if _, err := q.db().AgentGapsClear(q.ctx(), []string{x.ID}); err != nil {
					return err
				}
				for _, e := range byRun[i] {
					read, wrote, err := recordEventGaps(q, r, x.ID, x.ProjectID, e.id, e.at, e.data)
					if err != nil {
						return err
					}
					out.Failures += int64(read)
					out.Gaps += int64(wrote)
				}
				return nil
			}); err != nil {
				return out, err
			}
		}
		out.Executions += int64(len(runs))
		after = runs[len(runs)-1].ID
	}
	err = a.store.Write(ctx, q, []string{"agent-gaps"}, func(q *Request) error {
		if _, err := q.db().AgentGapsClearGateway(q.ctx(), since); err != nil {
			return err
		}
		calls, err := q.db().AgentGapFailedCalls(q.ctx(), since)
		if err != nil {
			return err
		}
		for _, c := range calls {
			at := time.Now()
			if c.StartedAt != nil {
				at = *c.StartedAt
			}
			wrote, err := recordCallGap(q, r, c.ID, c.ParentExecutionID, c.ProjectID, at, c.Tool, c.Connection, firstString(c.ErrorCode, c.Outcome))
			if err != nil {
				return err
			}
			out.Failures++
			if wrote {
				out.Gaps++
			}
		}
		return q.db().AgentGapPassPut(q.ctx(), db.AgentGapPassPutParams{Rules: r.digest, Days: int32(days)})
	})
	return out, err
}

// agentGapsCatchUp: the agent-gaps pass. When no classification with the current rules covered gapReclassDays (a
// first start, or a changed rule table), it classifies that window again; then it moves the findings
// (agent_gap_findings.go).
func agentGapsCatchUp(ctx context.Context, a *App) error {
	r, err := loadGapRules()
	if err != nil {
		return err
	}
	var done bool
	if err := a.store.Read(ctx, nil, func(q *Request) (err error) {
		done, err = q.db().AgentGapPassDone(q.ctx(), db.AgentGapPassDoneParams{Rules: r.digest, Days: gapReclassDays})
		return err
	}); err != nil {
		return err
	}
	if !done {
		if _, err := reclassifyGaps(ctx, a, nil, gapReclassDays); err != nil {
			return err
		}
	}
	return a.store.Write(ctx, nil, []string{"agent-gaps"}, func(q *Request) error { return agentGapFindingsPass(q, r, time.Now().UTC()) })
}

func agentGapsReclassify(q *Request, r *contract.AgentGapsReclassifyRequest) (contract.AgentGapsReclassifyReply, error) {
	days := gapReclassDays
	if r.Days != nil {
		days = int(*r.Days)
	}
	if days < 1 || days > gapReclassMax {
		return contract.AgentGapsReclassifyReply{}, fail(400, fmt.Sprintf("days: 1 to %d", gapReclassMax))
	}
	return reclassifyGaps(q.ctx(), q.app, q, days)
}

// ---- the read ----

func agentGaps(q *Request, r *contract.AgentGapsRequest) (contract.AgentGapsReply, error) {
	rules, err := loadGapRules()
	if err != nil {
		return contract.AgentGapsReply{}, fail(400, "config/agent-gaps.json: "+err.Error())
	}
	days := int64(gapDefaultDays)
	if r.Days != nil {
		days = *r.Days
	}
	if days < 1 || days > gapMaxDays {
		return contract.AgentGapsReply{}, fail(400, fmt.Sprintf("days: 1 to %d", gapMaxDays))
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := today.AddDate(0, 0, -int(days-1))
	prevFrom := from.AddDate(0, 0, -int(days))
	rows, err := q.db().AgentGapRows(q.ctx(), db.AgentGapRowsParams{Since: gapTime(prevFrom), Until: gapTime(now.Add(time.Minute))})
	if err != nil {
		return contract.AgentGapsReply{}, err
	}
	truncated := len(rows) > gapReadMax
	if truncated {
		rows = rows[:gapReadMax]
	}
	stored, err := q.db().AgentGapFindings(q.ctx())
	if err != nil {
		return contract.AgentGapsReply{}, err
	}
	findings := map[string]gapFinding{}
	for _, f := range stored {
		findings[f.Class] = gapFinding(f)
	}
	out := agentGapsReplyOf(rules, rows, r, findings, from, now, int(days))
	if truncated {
		out.Truncated = ptr(true)
	}
	return out, nil
}

type gapAgg struct {
	out        contract.AgentGapClass
	executions map[string]bool
	exampled   map[string]bool
	counts     [5]map[string]int64
	daily      map[string]int64
}

// agentGapsReplyOf groups rows (newest first, the window and the one before it) by class; findings (nil: none) join
// their classes, and a finding's class with no failure in the window still shows, with its zero days.
func agentGapsReplyOf(rules *gapRules, rows []db.AgentGapRowsRow, r *contract.AgentGapsRequest, findings map[string]gapFinding, from, now time.Time, days int) contract.AgentGapsReply {
	out := contract.AgentGapsReply{From: from.Format(time.RFC3339), To: now.Format(time.RFC3339), Days: int64(days), Rules: rules.digest, Classes: []contract.AgentGapClass{}}
	want := func(x db.AgentGapRowsRow) bool {
		if c := deref(r.Class); c != "" && x.Class != c && x.Family != c {
			return false
		}
		return (deref(r.HostId) == "" || deref(x.HostID) == deref(r.HostId)) && (deref(r.ProfileId) == "" || x.ProfileID == deref(r.ProfileId)) &&
			(deref(r.ProjectId) == "" || deref(x.ProjectID) == deref(r.ProjectId))
	}
	by := map[string]*gapAgg{}
	agg := func(class string) *gapAgg {
		g := by[class]
		if g == nil {
			family := gapFamilyOf(class)
			fam, ok := rules.Families[family]
			if !ok {
				fam = rules.Families["other"]
			}
			label := fam.Label
			if _, arg, ok := strings.Cut(class, ":"); ok {
				label = strings.ReplaceAll(label, "{arg}", arg)
			}
			src := "agent"
			if strings.HasPrefix(family, "gateway-") || family == "mcp-refused" {
				src = "gateway"
			}
			g = &gapAgg{out: contract.AgentGapClass{Class: class, Family: family, Label: firstString(label, class), Source: src, Title: fam.Title, Setting: fam.Setting, Examples: []contract.AgentGapExample{}},
				executions: map[string]bool{}, exampled: map[string]bool{}, daily: map[string]int64{}}
			for i := range g.counts {
				g.counts[i] = map[string]int64{}
			}
			by[class] = g
		}
		return g
	}
	digests := map[string]bool{}
	runs := map[string]bool{}
	for _, x := range rows {
		if !want(x) {
			continue
		}
		g, fresh := by[x.Class], by[x.Class] == nil
		g = agg(x.Class)
		if fresh {
			g.out.Source = x.Source // rows come newest first
		}
		if x.At.Before(from) {
			g.out.PreviousCount++
			continue
		}
		digests[x.Rules] = true
		out.Total++
		if x.Source == "gateway" {
			out.Calls++
		} else {
			runs[x.ExecutionID] = true
		}
		if x.Class == "other" {
			out.Other++
		}
		g.out.Count++
		g.executions[x.ExecutionID] = true
		at := x.At.UTC().Format(time.RFC3339)
		if g.out.LastAt == "" {
			g.out.LastAt = at // rows come newest first
		}
		g.out.FirstAt = at
		g.daily[x.At.UTC().Format("2006-01-02")]++
		for i, k := range []string{deref(x.HostID), deref(x.Harness), x.ProfileID, x.Tool, deref(x.ProjectID)} {
			g.counts[i][gapKey(k)]++
		}
		if len(g.out.Examples) < gapExamples && !g.exampled[x.ExecutionID] {
			g.exampled[x.ExecutionID] = true
			g.out.Examples = append(g.out.Examples, contract.AgentGapExample{ExecutionId: x.ExecutionID, At: at, Source: x.Source, Tool: x.Tool, Line: x.Line,
				HostId: optStr(deref(x.HostID)), Harness: optStr(deref(x.Harness)), ProfileId: optStr(x.ProfileID), ProjectId: optStr(deref(x.ProjectID)),
				Path: gapPath(x)})
		}
	}
	classWanted := func(class string) bool {
		c := deref(r.Class)
		return c == "" || c == class || c == gapFamilyOf(class)
	}
	for class, f := range findings {
		// A fixed finding stays on its class while the class has failures in the window or its verdict falls in it.
		if f.State == "fixed" && (f.VerdictAt == nil || f.VerdictAt.Before(from)) {
			if g := by[class]; g == nil || g.out.Count == 0 {
				continue
			}
		}
		if classWanted(class) && deref(r.HostId) == "" && deref(r.ProfileId) == "" && deref(r.ProjectId) == "" {
			agg(class).out.Finding = ptr(findingOut(f))
		}
	}
	for _, g := range by {
		if g.out.Count == 0 && g.out.Finding == nil {
			continue
		}
		if f, ok := findings[g.out.Class]; ok && g.out.Finding == nil {
			g.out.Finding = ptr(findingOut(f))
		}
		g.out.Executions = int64(len(g.executions))
		fr := rules.Finding
		if days == fr.Days { // the threshold is defined over the finding window only
			g.out.AboveThreshold = ptr(g.out.Class != "other" && g.out.Count >= fr.MinCount && g.out.Executions >= fr.MinExecutions)
		}
		for d := 0; d < days; d++ {
			day := from.AddDate(0, 0, d).Format("2006-01-02")
			g.out.Daily = append(g.out.Daily, contract.AgentGapDay{Day: day, Count: g.daily[day]})
		}
		lists := []*[]contract.AgentGapCount{&g.out.Hosts, &g.out.Harnesses, &g.out.Profiles, &g.out.Tools, &g.out.Projects}
		for i, l := range lists {
			*l = topCounts(g.counts[i])
		}
		out.Classes = append(out.Classes, g.out)
	}
	sort.Slice(out.Classes, func(i, j int) bool {
		a, b := out.Classes[i], out.Classes[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Class < b.Class
	})
	out.Runs = int64(len(runs))
	fr := rules.Finding
	out.Threshold = contract.AgentGapThreshold{Days: int64(fr.Days), MinCount: fr.MinCount, MinExecutions: fr.MinExecutions, WatchDays: int64(fr.WatchDays), FixedRatio: fr.FixedRatio}
	for d := range digests {
		out.ClassifiedWith = append(out.ClassifiedWith, d)
	}
	sort.Strings(out.ClassifiedWith)
	return out
}

func topCounts(m map[string]int64) []contract.AgentGapCount {
	out := make([]contract.AgentGapCount, 0, len(m))
	for k, n := range m {
		out = append(out, contract.AgentGapCount{Key: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > gapBreakdownMax {
		out = out[:gapBreakdownMax]
	}
	return out
}

// gapPath: the app path that shows the run, as entity_card links an execution.
func gapPath(x db.AgentGapRowsRow) *string {
	switch {
	case x.SessionID != nil:
		return itemPath("conversations", *x.SessionID)
	case x.TaskID != nil:
		return itemPath("tasks", *x.TaskID)
	case x.ExperimentID != nil:
		return itemPath("lab", *x.ExperimentID)
	}
	return nil
}
