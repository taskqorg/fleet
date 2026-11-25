package main

import "testing"

// Risk: the executor's MCP lifecycle receipts become results of every turn (HAL0 2026-11-25: 726 of 1212 results a day).
func TestDiagnosticFile(t *testing.T) {
	for name, want := range map[string]bool{
		"mcp-lifecycle-5eb2ce29-16e7c862.jsonl": true,
		"x.mcp/mcp-lifecycle-5eb2ce29.jsonl":    true,
		"output/mcp-lifecycle.md":               false,
		"report-mcp-lifecycle-5eb2.jsonl":       false,
		"blue-square.png":                       false,
	} {
		if diagnosticFile(name) != want {
			t.Errorf("%s: want %v", name, want)
		}
	}
}
