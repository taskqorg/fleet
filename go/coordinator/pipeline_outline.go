package main

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"fleet/contract"
)

// stepRefPattern: a reference to an earlier step's output inside a step's arguments, prompt or command.
var stepRefPattern = regexp.MustCompile(`\$\{steps\.([A-Za-z0-9_-]+)\.`)

// pipelineOutline: a pipeline's steps as numbered plain-text lines, the one form people (the pipeline page, an
// experiment, Lab) and agents (definition_get) read. A step's description is its line, followed for a model or agent
// step by what runs (the model or tool and its connection, the agent profile); a step without one names what runs
// (for a script, the program). A step reads the earlier steps its
// arguments, prompt or command reference. Steps that run on a condition are indented under one line naming it;
// consecutive steps with the same condition share that line.
func pipelineOutline(d *pipelineDef) []contract.PipelineOutlineLine {
	out := []contract.PipelineOutlineLine{}
	lastWhen := ""
	for i, s := range d.Steps {
		depth := int64(0)
		if w := conditionText(s.When); w != "" {
			if w != lastWhen {
				out = append(out, contract.PipelineOutlineLine{Text: w, Depth: 0, Condition: ptr(true)})
			}
			lastWhen, depth = w, 1
		} else {
			lastWhen = ""
		}
		text := strings.TrimSpace(s.Description)
		switch {
		case text == "":
			text = stepRuns(s)
		case s.Kind != "script": // a model or an agent is what a reader compares: keep it beside the description
			text += " · " + stepRuns(s)
		}
		if reads := stepReads(s); len(reads) > 0 {
			text += " · reads " + strings.Join(reads, ", ")
		}
		n := int64(i + 1)
		out = append(out, contract.PipelineOutlineLine{N: &n, Step: ptr(s.ID), Text: text, Depth: depth})
	}
	return out
}

// stepRuns: what a step without a description runs, in plain words.
func stepRuns(s pipelineDefStep) string {
	switch s.Kind {
	case "api":
		var args struct {
			Model string `json:"model"`
		}
		json.Unmarshal(s.Arguments, &args)
		via := s.Tool
		if s.Connection != "" {
			via += ", " + s.Connection
		}
		if args.Model != "" {
			return args.Model + " (" + via + ")"
		}
		return via
	case "agent":
		return "agent " + s.Profile
	case "script":
		var cmd string
		json.Unmarshal(s.Command, &cmd)
		return scriptName(cmd)
	}
	return s.Kind
}

// scriptName: a command's program and its first argument when that is a word (`doc_recognize.py finish`), not the
// interpreter, quoting or environment paths.
func scriptName(cmd string) string {
	words := strings.Fields(cmd)
	for i, w := range words {
		w = strings.Trim(w, `"'`)
		base := path.Base(strings.ReplaceAll(w, `"`, ""))
		if strings.Contains(base, ".") && !strings.HasPrefix(base, "-") {
			if i+1 < len(words) && !strings.HasPrefix(words[i+1], "-") {
				return base + " " + strings.Trim(words[i+1], `"'`)
			}
			return base
		}
	}
	if len(words) > 0 {
		return "script " + path.Base(strings.Trim(words[0], `"'`))
	}
	return "script"
}

// stepReads: the earlier steps a step's arguments, prompt or command reference, in order of first mention.
func stepReads(s pipelineDefStep) []string {
	seen, out := map[string]bool{}, []string{}
	for _, raw := range []json.RawMessage{s.Arguments, s.Prompt, s.Command} {
		for _, m := range stepRefPattern.FindAllStringSubmatch(string(raw), -1) {
			if !seen[m[1]] && m[1] != s.ID {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

// conditionText: a step's condition as a line ("if review.verdict is pass"); empty when the step has none.
func conditionText(raw json.RawMessage) string {
	w, err := parsePipelineWhen(rawAny(raw))
	if err != nil || w == nil {
		return ""
	}
	var equals any
	json.Unmarshal(w.Equals, &equals)
	at := strings.Join(append([]string{w.Source.Step}, w.Source.Path...), ".")
	b, _ := json.Marshal(equals)
	return "if " + at + " is " + strings.Trim(string(b), `"`)
}
