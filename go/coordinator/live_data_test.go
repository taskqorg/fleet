package main

import (
	"encoding/json"
	"testing"
)

// opInto: POST /api/ops/<op> as headers say, decoded into out; any status but 200 fails the test.
func (e *testCoordEnv) opInto(t *testing.T, op string, body any, headers map[string]string, out any) {
	t.Helper()
	code, got := e.callJSON(t, "POST", "/api/ops/"+op, body, headers)
	if code != 200 {
		t.Fatalf("%s = %d %v", op, code, got)
	}
	raw, _ := json.Marshal(got)
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s reply: %v", op, err)
	}
}
