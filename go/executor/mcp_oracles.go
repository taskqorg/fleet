package main

import (
	"errors"
)

// mcpToolResult: an MCP tools/call result as qualification reads it.
type mcpToolResult struct {
	IsError bool `json:"isError"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// mcpResultText: the text content of a decoded tools/call result.
func mcpResultText(result any) (string, error) {
	r := typed[mcpToolResult](result)
	if r.IsError {
		return "", errors.New("MCP reported tool failure")
	}
	text := ""
	for _, c := range r.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	if text == "" {
		return "", errors.New("MCP returned no textual witness")
	}
	return text, nil
}

func serenaSymbolWitness(text string) error {
	v, err := dec(text)
	if err != nil {
		return errors.New("Serena symbol result is not structured navigation evidence (check language-server readiness)")
	}
	for _, names := range typed[map[string][]string](v) { // symbol kind -> names
		for _, name := range names {
			if name == "CapabilityWitness" {
				return nil
			}
		}
	}
	return errors.New("Serena did not observe the known fixture symbol")
}
