package main

import (
	"slices"
	"testing"
)

func TestAcpModelList(t *testing.T) {
	r := typed[acpResult](j(`{"sessionId":"s","models":{"availableModels":[{"modelId":"gemini-3.5-pro"},{"modelId":"gemini-3.5-flash"}]},` +
		`"configOptions":[{"id":"mode","category":"mode","type":"select","currentValue":"plan","options":[{"value":"plan"}]},` +
		`{"id":"model","category":"model","type":"select","currentValue":"gemini-3.5-pro","options":[{"value":"gemini-3.5-pro"},{"options":[{"value":"claude-sonnet-5"}]}]}]}`))
	got := modelNames(acpModelList(r))
	want := []string{"gemini-3.5-pro", "gemini-3.5-flash", "claude-sonnet-5"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if modelNames(acpModelList(typed[acpResult](j(`{"sessionId":"s"}`)))) != nil {
		t.Fatal("a session/new answer without models must read not probed")
	}
}
