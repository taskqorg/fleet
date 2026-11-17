package main

import (
	"errors"
	"strconv"
	"strings"

	"fleet/contract"
)

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
		if deref(a.ProjectID) != astr(s["projectId"]) {
			return nil, fail(400, "Attachment "+id+" belongs to another project")
		}
		if a.Role == "attachment" {
			// A person's upload joins the conversation it is sent in; an agent sends only uploads already in its session.
			cond := "(session_id IS NULL OR session_id=$2)"
			if q.agent != "" {
				cond = "session_id=$2"
			}
			tag, err := q.tx.Exec(q.ctx(), "UPDATE artifact SET session_id=$2 WHERE id=$1 AND "+cond, a.ID, s["id"])
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, fail(400, "Attachment "+id+" belongs to another conversation")
			}
		}
		if seen[a.ID] { // an id and its legacy id name one file
			continue
		}
		seen[a.ID] = true
		out = append(out, doc{"artifactId": a.ID, "name": attachmentName(a.Name), "mediaType": a.MediaType, "size": a.Size})
	}
	return out, nil
}

// attachmentPayload: the enqueue payload that carries a message's attachments to its execution's start command, with
// the ids as requested (the request identity).
func attachmentPayload(attachments []any, requested []string) doc {
	if len(attachments) == 0 {
		return nil
	}
	return doc{"attachments": attachments, "attachmentRequest": requested}
}

// startAttachments: the inputs and attachment list of a start command. Each file goes to
// attachments/<execution>/<base name> under FLEET_INPUT_DIR: a new folder per turn, since the input folder belongs to
// the session; a repeated name in one message gets its position as a prefix.
func startAttachments(executionID string, list []any) ([]contract.InputArtifact, []contract.StartAttachment) {
	var inputs []contract.InputArtifact
	var out []contract.StartAttachment
	used := map[string]bool{}
	for i, x := range list {
		m := mapOf(x)
		name := attachmentName(astr(m["name"]))
		// Unique in the folder whatever the file system's case rule; a prefix keeps the name under 255 bytes.
		for k := i + 1; used[strings.ToLower(name)]; k++ {
			base := attachmentName(astr(m["name"]))
			if len(base) > 240 {
				base = strings.ToValidUTF8(base[:240], "")
			}
			name = strconv.Itoa(k) + "-" + base
		}
		used[strings.ToLower(name)] = true
		rel := "attachments/" + executionID + "/" + name
		inputs = append(inputs, contract.InputArtifact{ArtifactId: astr(m["artifactId"]), Path: rel})
		out = append(out, contract.StartAttachment{ArtifactId: astr(m["artifactId"]), Path: rel, Name: name, MediaType: astr(m["mediaType"]), Size: int64(num(m["size"]))})
	}
	return inputs, out
}
