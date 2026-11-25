package main

// Tool call summaries (docs/designs/fleet-conversations.md, tool calls): a thread's tool block carries what its chip
// shows — callId, toolKind, a short target and the first error line — and, unless thread_entries asks full: false, the
// harness's input and output. tool_output loads one call's input and output when it is opened.

import (
	"encoding/json"
	"regexp"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("tool_output", toolOutput)
}

var (
	kindContext = regexp.MustCompile(`(?i)^context`)
	kindWeb     = regexp.MustCompile(`(?i)^(web|fetch|browse)`)
	kindShell   = regexp.MustCompile(`(?i)^(bash|shell|exec|run|command)`)
	kindEdits   = regexp.MustCompile(`(?i)^(edit|write|multiedit|apply_patch|patch|create|filechange)`)
	kindFiles   = regexp.MustCompile(`(?i)^(read|view|cat|open)`)
	kindSearch  = regexp.MustCompile(`(?i)^(grep|glob|search|find|ls)$|(?i)^(grep|glob)`)
	kindFleet   = regexp.MustCompile(`^[a-z]+(_[a-z]+)+$`)
)

// toolNameOf: the tool's own name without its MCP server prefix (mcp__<server>__<tool>), as tool-fold.ts toolName.
func toolNameOf(raw string) string {
	if strings.HasPrefix(raw, "mcp__") {
		if i := strings.LastIndex(raw, "__"); i > 4 {
			return raw[i+2:]
		}
	}
	return raw
}

// toolKindOf classifies a call with the rules of packages/ui/src/controls/tool-fold.ts toolKind(); both must agree.
func toolKindOf(raw string) contract.ToolKind {
	name := toolNameOf(raw)
	switch {
	case kindContext.MatchString(name):
		return "context"
	case strings.HasPrefix(name, "artifact_"):
		return "results"
	case kindWeb.MatchString(name):
		return "web"
	case kindShell.MatchString(name):
		return "shell"
	case kindEdits.MatchString(name):
		return "edits"
	case kindFiles.MatchString(name):
		return "files"
	case kindSearch.MatchString(name):
		return "search"
	case strings.HasPrefix(raw, "mcp__fleet__") || kindFleet.MatchString(name):
		return "fleet"
	}
	return "other"
}

var worktreePrefix = regexp.MustCompile(`^.*/(?:worktrees|workspaces)/[^/]+/`)

// toolTarget: what the call works on, short: a command's first line, a path relative to its workspace, a pattern, a URL
// or an entity named by code. Empty when the input names none.
func toolTarget(input json.RawMessage) string {
	var in struct {
		Command  any    `json:"command"`
		Cmd      any    `json:"cmd"`
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Pattern  string `json:"pattern"`
		URL      string `json:"url"`
		Query    string `json:"query"`
		Code     string `json:"code"`
		Name     string `json:"name"`
	}
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return ""
	}
	cmd := func(v any) string {
		switch c := v.(type) {
		case string:
			return c
		case []any:
			parts := make([]string, 0, len(c))
			for _, p := range c {
				if s, ok := p.(string); ok {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, " ")
		}
		return ""
	}
	t := firstString(cmd(in.Command), cmd(in.Cmd))
	if t == "" {
		if p := firstString(in.FilePath, in.Path); p != "" {
			t = worktreePrefix.ReplaceAllString(p, "")
		} else {
			t = firstString(in.Pattern, in.URL, in.Query, in.Code, in.Name)
		}
	}
	t, _, _ = strings.Cut(strings.TrimSpace(t), "\n")
	if r := []rune(t); len(r) > 160 {
		t = string(r[:160]) + "…"
	}
	return t
}

// toolErrorLine: the first non-empty line of a failed call's output.
func toolErrorLine(output json.RawMessage) string {
	var s string
	if json.Unmarshal(output, &s) != nil {
		var o struct {
			Error   string `json:"error"`
			Message string `json:"message"`
			Stderr  string `json:"stderr"`
			Text    string `json:"text"`
		}
		if json.Unmarshal(output, &o) != nil {
			return ""
		}
		s = firstString(o.Error, o.Message, o.Stderr, o.Text)
	}
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if r := []rune(line); len(r) > 240 {
				line = string(r[:240]) + "…"
			}
			return line
		}
	}
	return ""
}

// summarizeTool sets a tool block's summary fields and, unless full, drops its input and output.
func summarizeTool(b threadBlock, full bool) {
	if b.text("kind") != "tool" {
		return
	}
	in, outRaw := b["input"], b["output"]
	b["callId"] = jsonBytes(b.text("executionId") + "/" + b.text("key"))
	b["toolKind"] = jsonBytes(toolKindOf(b.text("name")))
	if t := toolTarget(in); t != "" {
		b["target"] = jsonBytes(t)
	}
	if st := b.text("state"); st == "failed" || st == "error" {
		if l := toolErrorLine(outRaw); l != "" {
			b["errorLine"] = jsonBytes(l)
		}
	}
	if !full {
		delete(b, "input")
		delete(b, "output")
	}
}

// toolOutput: one call's input and output by its callId (<executionId>/<event seq>:<index>), visible as its thread is.
func toolOutput(q *Request, r *contract.ToolOutputRequest) (contract.ToolOutputReply, error) {
	out := contract.ToolOutputReply{CallId: r.CallId, Input: json.RawMessage("null"), Output: json.RawMessage("null")}
	eid, key, ok := strings.Cut(r.CallId, "/")
	k, err := parseKey(key)
	if !ok || eid == "" || err != nil {
		return out, fail(400, "Expected callId <executionId>/<seq>:<index>")
	}
	raw, err := q.db().ToolCallBlocks(q.ctx(), db.ToolCallBlocksParams{ExecutionID: &eid, Seq: k.seq})
	if err != nil || raw == nil {
		return out, errNotFound
	}
	var blocks []struct {
		Kind   string          `json:"kind"`
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
	}
	if json.Unmarshal(raw, &blocks) != nil || k.index < 0 || k.index >= int64(len(blocks)) || blocks[k.index].Kind != "tool" {
		return out, errNotFound
	}
	if b := blocks[k.index]; len(b.Input) > 0 {
		out.Input = b.Input
	}
	if b := blocks[k.index]; len(b.Output) > 0 {
		out.Output = b.Output
	}
	return out, nil
}
