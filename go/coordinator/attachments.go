package main

import (
	"errors"
	"strconv"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

// messageAttachment: one file a message carries, as the message and its execution's dispatch keep it.
type messageAttachment struct {
	ArtifactID string `json:"artifactId"`
	Name       string `json:"name"`
	MediaType  string `json:"mediaType"`
	Size       int64  `json:"size"`
}

// Message attachments (docs/designs/fleet-conversations.md, Attachments): message_send and session_start take artifact
// ids; the message and its execution's dispatch keep {artifactId, name, mediaType, size}; the start command stages each
// file under FLEET_INPUT_DIR/attachments/<execution>/<name> (startAttachments) and the executor lists them in the turn.

// attachmentArg: the artifact ids of a message, in order, without repeats; checked before any read.
func attachmentArg(b doc) ([]string, error) {
	raw, given := b["attachments"]
	if !given || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fail(400, "Expected attachments: a list of artifact ids")
	}
	ids, seen := []string{}, map[string]bool{}
	for _, x := range list {
		id, ok := x.(string)
		if !ok || id == "" || len(id) > 200 {
			return nil, fail(400, "Expected attachments: a list of artifact ids")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// messageAttachments: the stored artifacts a message carries. Each must be readable in the caller's scope (the project
// grant, RLS) and belong to the session's project; an operator upload must be unattached or this session's, and an
// unattached one joins this session. Names are cleaned (attachmentName) before they reach a path or a turn.
func messageAttachments(q *Request, ids []string, s doc) ([]any, error) {
	out, seen := []any{}, map[string]bool{}
	for _, id := range ids {
		a, err := artifactRow(withTx(q), q.app, id)
		var he *HTTPError
		if errors.As(err, &he) && he.Code == 404 {
			return nil, fail(400, "Unknown attachment "+id)
		}
		if err != nil {
			return nil, err
		}
		if a.State != "stored" {
			return nil, fail(409, "Attachment "+id+" is "+a.State)
		}
		if a.Role != "attachment" && a.Role != "original" {
			return nil, fail(400, "Attachment "+id+" is a "+a.Role)
		}
		sessionProject, _ := s["projectId"].(string)
		sessionID, _ := s["id"].(string)
		if deref(a.ProjectID) != sessionProject {
			return nil, fail(400, "Attachment "+id+" belongs to another project")
		}
		if a.Role == "attachment" {
			// A person's upload joins the conversation it is sent in; an agent sends only uploads already in its session.
			n, err := q.db().DispAttachmentJoin(q.ctx(), db.DispAttachmentJoinParams{ID: a.ID, SessionID: &sessionID, Agent: q.agent != ""})
			if err != nil {
				return nil, err
			}
			if n == 0 {
				return nil, fail(400, "Attachment "+id+" belongs to another conversation")
			}
		}
		if seen[a.ID] { // an id and its legacy id name one file
			continue
		}
		seen[a.ID] = true
		out = append(out, messageAttachment{ArtifactID: a.ID, Name: attachmentName(a.Name), MediaType: a.MediaType, Size: a.Size})
	}
	return out, nil
}

// attachmentPayload: the enqueue payload that carries a message's attachments to its execution's start command, with
// the ids as requested (the request identity).
func attachmentPayload(attachments []any, requested []string) doc {
	if len(attachments) == 0 {
		return nil
	}
	return docOf(struct {
		Attachments       []any    `json:"attachments"`
		AttachmentRequest []string `json:"attachmentRequest"`
	}{attachments, requested})
}

// startAttachments: the inputs and attachment list of a start command. Each file goes to
// attachments/<execution>/<base name> under FLEET_INPUT_DIR: a new folder per turn, since the input folder belongs to
// the session; a repeated name in one message gets its position as a prefix.
func startAttachments(executionID string, list []any) ([]contract.InputArtifact, []contract.StartAttachment) {
	var inputs []contract.InputArtifact
	var out []contract.StartAttachment
	used := map[string]bool{}
	for i, x := range list {
		m, _ := asT[messageAttachment](x) // a malformed entry stages as an unnamed file, as before
		name := attachmentName(m.Name)
		// Unique in the folder whatever the file system's case rule; a prefix keeps the name under 255 bytes.
		for k := i + 1; used[strings.ToLower(name)]; k++ {
			base := attachmentName(m.Name)
			if len(base) > 240 {
				base = strings.ToValidUTF8(base[:240], "")
			}
			name = strconv.Itoa(k) + "-" + base
		}
		used[strings.ToLower(name)] = true
		rel := "attachments/" + executionID + "/" + name
		inputs = append(inputs, contract.InputArtifact{ArtifactId: m.ArtifactID, Path: rel})
		out = append(out, contract.StartAttachment{ArtifactId: m.ArtifactID, Path: rel, Name: name, MediaType: m.MediaType, Size: m.Size})
	}
	return inputs, out
}
