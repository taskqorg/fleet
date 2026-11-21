// API connection keys and secret key requests (connections_key_*).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
	"github.com/jackc/pgx/v5"
)

// connectionsKeyAdd (owner): writes the value file and the row; answers with the fingerprint only.
func connectionsKeyAdd(q *Request, r *contract.ConnectionsKeyAddRequest) (any, error) {
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	value := r.Value
	if len(value) < 8 || len(value) > 4096 || !keyValue.MatchString(value) {
		return nil, fail(400, "Expected one key value without spaces")
	}
	qid, fp := deref(r.QuestionId), fingerprint(value)
	if qid != "" {
		// A retry of an add that already answered this request (its reply was lost) passes here and replays below.
		if err := keyRequestOf(q, qid, c.Name, keyAnswer(c.Name, fp, "active")); err != nil {
			return nil, err
		}
	}
	if old, err := connKeyMaybe(q, "key:"+fp); err != nil {
		return nil, err
	} else if old != nil {
		if qid != "" && old.ConnectionID == c.Name && old.State == "active" { // Fleet already holds it: answer with it
			if _, err := answerQuestion(q, qid, keyAnswer(c.Name, fp, "active"), actor(q, "operator"), nil); err != nil {
				return nil, err
			}
			return *old, nil
		}
		return nil, fail(409, "Key already added")
	}
	dir, err := connDir(c.Name)
	if err != nil {
		return nil, err
	}
	by := actor(q, "operator")
	k := connKey{ID: "key:" + fp, ConnectionID: c.Name, Fingerprint: fp, State: "active", AddedAt: float64(nowMs()), AddedBy: &by}
	if err := connKeyPut(q, k); err != nil {
		return nil, err
	}
	// The value is staged before commit and takes its key name only after the row committed; a rolled-back add removes
	// it, and connSync settles a stage a crash left behind against the committed row.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	staged, final := filepath.Join(dir, "."+fp+".add"), filepath.Join(dir, fp)
	if err := os.WriteFile(staged, []byte(value+"\n"), 0o600); err != nil {
		return nil, err
	}
	os.Chmod(staged, 0o600)
	afterCommit(q, func() {
		if err := os.Rename(staged, final); err != nil {
			log.Printf("key %s: %v", fp, err)
		}
	})
	afterRollback(q, func() {
		os.Remove(staged)
	})
	if qid != "" { // the agent's question gets the fingerprint only, never the value
		if _, err := answerQuestion(q, qid, keyAnswer(c.Name, fp, "active"), actor(q, "operator"), nil); err != nil {
			return nil, err
		}
	}
	publish(q)
	return k, nil
}

// keyRequestReply: a new key request as the project's questions show it.
type keyRequestReply struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	RequestID string `json:"requestId"`
	Text      string `json:"text"`
	State     string `json:"state"`
	CreatedAt int64  `json:"createdAt"`
	Actor     string `json:"actor"`
}

// connectionsKeyRequest: an agent (or a person) asks the owner for a key value (docs/designs/fleet-connections.md §1,
// key requests): a secret question the owner answers through connections_key_add with questionId. No argument carries a
// value (the request is closed: an extra key is 400).
func connectionsKeyRequest(q *Request, r *contract.ConnectionsKeyRequestRequest) (any, error) {
	if _, err := projectContext(q, r.ProjectId); err != nil {
		return nil, err
	}
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	rid, reason := r.RequestId, deref(r.Reason)
	if rid == "" || len(rid) > 200 || len([]rune(reason)) > 500 {
		return nil, fail(400, "Expected requestId and a reason of at most 500 characters")
	}
	text := fmt.Sprintf("Add a %s key for connection %s", c.Provider, c.Name)
	if reason != "" {
		text += ": " + reason
	}
	d, err := q.db().ConnKeyRequestByRequest(q.ctx(), db.ConnKeyRequestByRequestParams{ProjectID: r.ProjectId, RequestID: rid})
	if err == nil {
		if d.Text != text {
			return nil, fail(409, "Question identity conflict")
		}
		return json.RawMessage(d.Question), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	now := nowMs()
	v := keyRequestReply{ID: newID(), ProjectID: r.ProjectId, RequestID: rid, Text: text, State: "waiting", CreatedAt: now, Actor: actor(q, "operator")}
	asked := time.UnixMilli(now).UTC()
	ask := questionAsk{ID: v.ID, Kind: "secret", Text: text, ProjectID: r.ProjectId, AskedBy: &v.Actor, RequestID: &rid, AskedAt: &asked,
		Options: jsonBytes(struct {
			Connection string `json:"connection"`
		}{c.Name})}
	if strings.HasPrefix(q.agent, "execution:") { // a host agent: the question shows in its conversation
		eid := strings.TrimPrefix(q.agent, "execution:")
		sid, err := q.db().ConnExecutionSession(q.ctx(), eid)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		ask.ExecutionID = &eid
		if err == nil && sid != nil && *sid != "" {
			ask.SessionID = sid
		}
	}
	_, err = askQuestionAs(q, ask)
	return v, err
}

// connectionsKeyState: rotation (§2): active <-> draining, any -> retired (the value file is deleted; the row stays);
// disabled -> active is the owner's.
func connectionsKeyState(q *Request, r *contract.ConnectionsKeyStateRequest) (any, error) {
	id := r.KeyId
	if id != "" && !strings.HasPrefix(id, "key:") {
		id = "key:" + id
	}
	kp, err := connKeyMaybe(q, id)
	if err != nil {
		return nil, err
	}
	if kp == nil {
		return nil, fail(404, "Unknown key")
	}
	k := *kp
	c, err := connNamed(q, k.ConnectionID)
	if err != nil {
		return nil, err
	}
	// The request names the key, not the connection: the holder rule is checked on the key's connection here.
	if q.person != nil && !fullAccess(q.person) && q.person.Email != c.Holder {
		return nil, fail(403, "Only the holder or the owner changes this API key")
	}
	to := string(r.State)
	if k.State == "retired" {
		return nil, fail(409, "A retired key stays retired")
	}
	if k.State == "disabled" && to != "retired" && q.person != nil && !fullAccess(q.person) {
		return nil, fail(403, "Only the owner handles key values")
	}
	if to == k.State {
		return k, nil
	}
	if err := connChange(q, c, "key:"+k.Fingerprint, k.State, to); err != nil {
		return nil, err
	}
	now := nowMs()
	k.State, k.UpdatedAt = to, &now
	if to == "active" {
		enabled := float64(now)
		k.DisabledReason, k.DisabledAt, k.Problem, k.EnabledAt = nil, nil, nil, &enabled
	}
	if err := connKeyPut(q, k); err != nil {
		return nil, err
	}
	// The key left disabled (enabled again or retired): its refused-key question no longer needs the owner.
	if err := problemClose(q, "key:"+k.Fingerprint+":disabled"); err != nil {
		return nil, err
	}
	if to == "retired" {
		if dir, err := connDir(c.Name); err == nil {
			// The value goes only once the retirement committed; connSync removes it if a crash came in between.
			path := filepath.Join(dir, k.Fingerprint)
			afterCommit(q, func() {
				os.Remove(path)
			})
		}
	}
	publish(q)
	return k, nil
}

// keyAnswer: the only answer a key request records besides decline: the JSON text {connection, fingerprint, state}.
func keyAnswer(connection, fp, state string) string {
	return js(struct {
		Connection  string `json:"connection"`
		Fingerprint string `json:"fingerprint"`
		State       string `json:"state"`
	}{connection, fp, state})
}

// keyRequestOf: the open key request qid for connection, or one already answered with replay (a retried add).
func keyRequestOf(q *Request, qid, connection, replay string) error {
	qn, err := q.db().ConnKeyRequest(q.ctx(), qid)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, "Unknown key request")
	}
	if err != nil {
		return err
	}
	if qn.State != "open" && (replay == "" || qn.State != "answered" || qn.Answer != replay) {
		return fail(409, "Key request already settled")
	}
	if qn.Connection != connection {
		return fail(400, "The key request names another connection")
	}
	return nil
}

// keyAnswerCheck: the answer hook of a key request (tasks area calls it with the question's row). Only the owner
// settles it, with decline or with the fingerprint answer of a key added to its connection, so no answer path carries a
// value.
func keyAnswerCheck(q *Request, question json.RawMessage, answer string) error {
	qn, _ := asT[struct {
		Kind    string `json:"kind"`
		Options struct {
			Connection json.RawMessage `json:"connection"`
		} `json:"options"`
	}](question)
	if qn.Kind != "secret" {
		return nil
	}
	if q.agent != "" || q.person == nil || !fullAccess(q.person) { // never the asking agent, even with the owner's rights
		return fail(403, "Only the owner answers a key request")
	}
	if answer == "decline" {
		return nil
	}
	var a struct{ Connection, Fingerprint, State string }
	conn, _ := jsonAs[string](qn.Options.Connection)
	if json.Unmarshal([]byte(answer), &a) != nil || a.Connection != conn || keyAnswer(a.Connection, a.Fingerprint, a.State) != answer {
		return fail(400, "Answer decline, or add the key in its field")
	}
	k, err := connKeyMaybe(q, "key:"+a.Fingerprint)
	if err != nil {
		return err
	}
	if k == nil || k.ConnectionID != conn || a.State != "active" || k.State != "active" {
		return fail(400, "Answer decline, or add the key in its field")
	}
	return nil
}
