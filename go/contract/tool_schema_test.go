package contract

import (
	"encoding/json"
	"slices"
	"testing"
)

// Native discovery must expose the same names, required fields and resolved definitions as core validation.
func TestOperationToolSchema(t *testing.T) {
	var s struct {
		Properties map[string]json.RawMessage
		Required   []string
	}
	if err := json.Unmarshal(Operations["agent_effective"].InputSchema, &s); err != nil {
		t.Fatal(err)
	}
	if s.Properties["id"] == nil || s.Properties["agentId"] != nil {
		t.Fatal("effective-agent identity schema drift")
	}
	if !slices.Contains(s.Required, "id") || !slices.Contains(s.Required, "projectId") {
		t.Fatal(s.Required)
	}
	for name, op := range Operations {
		if !json.Valid(op.InputSchema) {
			t.Fatal("missing schema", name)
		}
		var value any
		json.Unmarshal(op.InputSchema, &value)
		checkToolRefs(t, name, value)
	}
}
func checkToolRefs(t *testing.T, name string, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		if x["$ref"] != nil {
			t.Fatal("unresolved discovery reference", name, x)
		}
		for _, v := range x {
			checkToolRefs(t, name, v)
		}
	case []any:
		for _, v := range x {
			checkToolRefs(t, name, v)
		}
	}
}
