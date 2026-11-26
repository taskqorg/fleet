// Token specs (docs/designs/fleet-connections.md#13-local-agents): POST /v1/call/-/connections answers the client specs
// of the bearer's connections from the snapshot, so a session's `fleet-executor mcp connection <name>` needs no spec in
// its settings and never holds stale tools. The path is under /v1/call because only that prefix reaches the gateway
// from other hosts; "-" is no connection name.
package main

import (
	"encoding/json"
	"net/http"
)

const tokenSpecsPath = "/v1/call/-/connections"

// connectionSpec: one connection as its client serves it (the coordinator's connSpec: name, instructions, tools, the
// waits).
type connectionSpec struct {
	Name         string          `json:"name"`
	Instructions json.RawMessage `json:"instructions,omitempty"`
	Tools        []toolSpec      `json:"tools"`
	Limits       *specWaits      `json:"limits,omitempty"`
}

type toolSpec struct {
	Name        string          `json:"name"`
	Description json.RawMessage `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	Idempotent  bool            `json:"idempotent"`
	File        any             `json:"file,omitempty"`
}

type specWaits struct {
	StallMs  *int64 `json:"stallMs,omitempty"`
	AnswerMs *int64 `json:"answerMs,omitempty"`
}

func (g *gateway) serveTokenSpecs(w http.ResponseWriter, r *http.Request) {
	b, ok := bearer(r)
	var t *token
	if ok {
		t = g.authenticate(b)
	}
	if t == nil {
		writeJSON(w, 401, map[string]string{"error": "unauthenticated", "reason": "a live Fleet token is required"})
		return
	}
	s := g.snapshot.Load()
	out := []connectionSpec{}
	for _, name := range t.Connections {
		c, known := s.Connections[name]
		p, hasProvider := s.Providers[c.Provider]
		if !known || !hasProvider || c.Intake == "paused" {
			continue
		}
		spec := connectionSpec{Name: name, Instructions: p.Rest["instructions"], Tools: []toolSpec{}}
		for _, tn := range c.Tools {
			tl, ok := p.Tools[tn]
			if !ok {
				continue
			}
			ts := toolSpec{Name: tn, Description: tl.Rest["description"], InputSchema: tl.Rest["inputSchema"], Idempotent: tl.Idempotent}
			if tl.File != nil {
				ts.File = *tl.File
			}
			spec.Tools = append(spec.Tools, ts)
		}
		if l := c.Limits; l != nil && (l.StallMs != nil || l.AnswerMs != nil) {
			spec.Limits = &specWaits{StallMs: l.StallMs, AnswerMs: l.AnswerMs}
		}
		out = append(out, spec)
	}
	writeJSON(w, 200, struct {
		Connections []connectionSpec `json:"connections"`
	}{out})
}
