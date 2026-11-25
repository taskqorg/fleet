package main

import (
	"slices"
	"testing"
)

func TestModelNames(t *testing.T) {
	got := modelNames([]any{"opus", M{"value": "sonnet", "displayName": "Sonnet"}, M{"id": "gpt-5.5"}, M{"model": "gpt-5.5"}, "", 3})
	if want := []string{"opus", "sonnet", "gpt-5.5"}; !slices.Equal(got, want) {
		t.Fatalf("modelNames = %v, want %v", got, want)
	}
	if modelNames(nil) != nil || modelNames(M{}) != nil {
		t.Fatal("no list must read as not probed (nil)")
	}
}
