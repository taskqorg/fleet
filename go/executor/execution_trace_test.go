package main

import (
	"encoding/json"
	"fleet/obs"
	"testing"
)

// Requirement: native/program terminal telemetry is a child of durable coordinator context.
// Risk: retry/restart loses attribution or success/failure/cancellation fabricate detached roots.
func TestDurableExecutionTrace(t *testing.T) {
	const tp = "00-12345678901234567890123456789012-1234567890123456-01"
	for _, tc := range []struct{ name, command, want string }{{"success", "true", obs.OK}, {"failure", "exit 7", obs.Error}, {"cancel", "sleep 10", obs.Cancelled}} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := scriptExecutor(t)
			var spans []*obs.Span
			obs.SetSink(func(s *obs.Span) {
				if s.Name == "execution" {
					spans = append(spans, s)
				}
			})
			defer obs.SetSink(nil)
			c := M{"id": "trace-command", "executionId": "trace-execution", "sessionId": "trace-session", "kind": "script", "traceparent": tp, "run": M{"projectId": "project", "taskId": "task", "executionId": "trace-execution", "actor": "operator"}, "payload": M{"command": tc.command}}
			// A serialized/reloaded command is the same carrier used by the journal and retry path.
			raw, _ := json.Marshal(c)
			c = decM(string(raw))
			if err := e.run(c); err != nil {
				t.Fatal(err)
			}
			if tc.name == "cancel" {
				if err := e.run(M{"id": "stop", "executionId": "trace-execution", "sessionId": "trace-session", "kind": "stop"}); err != nil {
					t.Fatal(err)
				}
			}
			settle(t, e, "trace-execution")
			if len(spans) != 1 {
				t.Fatalf("terminal spans=%d", len(spans))
			}
			s := spans[0]
			if s.TraceID != "12345678901234567890123456789012" || s.ParentSpanID != "1234567890123456" || s.SpanID == s.ParentSpanID || s.Outcome != tc.want || s.ProjectID == nil || *s.ProjectID != "project" || s.TaskID == nil || *s.TaskID != "task" || s.Actor != "operator" {
				t.Fatalf("bad span %+v", s)
			}
			if err := e.run(c); err != nil {
				t.Fatal(err)
			}
			if len(spans) != 1 {
				t.Fatal("completed command retry emitted terminal span twice")
			}
		})
	}
}
