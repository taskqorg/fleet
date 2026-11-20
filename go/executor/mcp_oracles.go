package main

import (
	"encoding/json"
	"errors"
)

// mcpToolResult: an MCP tools/call result as qualification reads it.
type mcpToolResult struct {
	IsError lax[bool] `json:"isError"`
	Content lax[[]lax[struct {
		Type lax[string] `json:"type"`
		Text lax[string] `json:"text"`
	}]] `json:"content"`
}

// mcpResultText: the text content of a tools/call result (its JSON, or the decoded object).
func mcpResultText(result any) (string, error) {
	var r mcpToolResult
	viewOf(result, &r)
	if r.IsError.V {
		return "", errors.New("MCP reported tool failure")
	}
	text := ""
	for _, c := range r.Content.V {
		if c.V.Type.V == "text" {
			text += c.V.Text.V
		}
	}
	if text == "" {
		return "", errors.New("MCP returned no textual witness")
	}
	return text, nil
}

func serenaSymbolWitness(text string) error {
	var value lax[map[string]lax[[]lax[string]]] // symbol kind -> names
	if json.Unmarshal([]byte(text), &value) != nil {
		return errors.New("Serena symbol result is not structured navigation evidence (check language-server readiness)")
	}
	for _, names := range value.V {
		for _, name := range names.V {
			if name.V == "CapabilityWitness" {
				return nil
			}
		}
	}
	return errors.New("Serena did not observe the known fixture symbol")
}
