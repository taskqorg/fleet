package main

import (
	"net/http"
	"reflect"
	"testing"

	"fleet/contract"
)

// A legacy session_view must omit artifactIds: the existing acceptance card renders any IDs it receives.
// Opt-in must project the stored IDs only from questions the caller could already read.
func TestSessionViewQuestionArtifactIdsOptIn(t *testing.T) {
	e := openTestCoordinator(t, false)
	lifecycleSQL(t, e, `INSERT INTO session (id, project_id, directed_by) VALUES ('attachments-session', 'desiign', 'member@trial.test')`)
	lifecycleSQL(t, e, `INSERT INTO artifact (id, project_id, scope, content_hash, size, media_type, name, state)
		VALUES ('question-attachment', 'desiign', 'prod', repeat('ab',32), 1, 'image/png', 'result.png', 'stored')`)
	lifecycleSQL(t, e, `INSERT INTO question (id, kind, session_id, project_id, state, text, data) VALUES
		('with-attachment', 'acceptance', 'attachments-session', 'desiign', 'open', 'Accept the result?', '{"artifactIds":["question-attachment"]}'),
		('missing-attachments', 'agent_question', 'attachments-session', 'desiign', 'open', 'Legacy question', '{}'),
		('null-attachments', 'agent_question', 'attachments-session', 'desiign', 'open', 'Null fixture', '{"artifactIds":null}'),
		('malformed-attachments', 'agent_question', 'attachments-session', 'desiign', 'open', 'Malformed fixture', '{"artifactIds":["question-attachment",1]}'),
		('null-attachment-id', 'agent_question', 'attachments-session', 'desiign', 'open', 'Null element', '{"artifactIds":["question-attachment",null]}'),
		('empty-attachment-id', 'agent_question', 'attachments-session', 'desiign', 'open', 'Empty element', '{"artifactIds":["question-attachment",""]}'),
		('closed-question', 'acceptance', 'attachments-session', 'desiign', 'answered', 'Already answered', '{"artifactIds":["question-attachment"]}'),
		('hidden-question', 'agent_question', 'attachments-session', 'hello-world', 'open', 'Outside the member grant', '{"artifactIds":["hidden-attachment"]}')`)
	headers := e.ownerHeaders()
	headers["X-Fleet-Person"] = "member@trial.test"
	var legacy doc
	for _, tc := range []struct {
		name string
		flag *bool
	}{
		{"omitted", nil},
		{"false", ptr(false)},
		{"true", ptr(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := contract.SessionViewRequest{SessionId: "attachments-session", IncludeQuestionArtifactIds: tc.flag}
			var reply doc
			e.opInto(t, "session_view", req, headers, &reply)
			questions, ok := reply["questions"].([]any)
			if !ok || len(questions) != 6 {
				t.Fatalf("expected six visible open questions, got %v", reply["questions"])
			}
			found := false
			for _, item := range questions {
				question := item.(map[string]any)
				id := question["id"]
				switch id {
				case "with-attachment":
					found = true
				case "missing-attachments", "null-attachments", "malformed-attachments", "null-attachment-id", "empty-attachment-id":
				default:
					t.Fatalf("unexpected question %v", id)
				}
				ids, present := question["artifactIds"]
				if tc.flag != nil && *tc.flag && id == "with-attachment" {
					if !reflect.DeepEqual(ids, []any{"question-attachment"}) {
						t.Errorf("attached IDs = %v", ids)
					}
				} else if present {
					t.Errorf("%v included artifactIds = %v", id, ids)
				}
				delete(question, "artifactIds")
			}
			if !found {
				t.Fatal("acceptance question is missing")
			}
			if tc.flag == nil {
				legacy = reply
			} else if !reflect.DeepEqual(reply, legacy) {
				t.Errorf("session_view changed beyond the opted-in field: %v", reply)
			}
		})
	}
	headers["X-Fleet-Person"] = "outsider@trial.test"
	if code, reply := e.callJSON(t, http.MethodPost, "/api/ops/session_view",
		contract.SessionViewRequest{SessionId: "attachments-session", IncludeQuestionArtifactIds: ptr(true)}, headers); code != http.StatusNotFound {
		t.Fatalf("out-of-scope session_view = %d %v", code, reply)
	}
}
