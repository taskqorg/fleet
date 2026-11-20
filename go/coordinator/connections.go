// API key connections (docs/designs/fleet-connections.md). Key values live only in
// <FLEET_SECRET_DIR>/connections/<connection>/<fingerprint> (0600); rows hold the fingerprint and state. Every change
// republishes <FLEET_GATEWAY_DIR>/snapshot.json (no key values) once the change committed.
package main

import (
	"fleet/coordinator/db"

	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"github.com/jackc/pgx/v5"
)

var (
	connName       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	keyValue       = regexp.MustCompile(`^\S+$`) // with 8..4096 bytes, checked beside it
	keyFile        = regexp.MustCompile(`^[0-9a-f]{12}$`)
	stagedKey      = regexp.MustCompile(`^\.([0-9a-f]{12})\.add$`)
	spoolFile      = regexp.MustCompile(`^\d{4}-\d\d-\d\d\.jsonl$`)
	usageToken     = randomToken()
	processStarted = nowMs()
)

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func fingerprint(v string) string { return sha256hex([]byte(v))[:12] }

func secretDir() string  { return os.Getenv("FLEET_SECRET_DIR") }
func gatewayDir() string { return os.Getenv("FLEET_GATEWAY_DIR") }
func gatewayURL() string { return strings.TrimRight(os.Getenv("FLEET_GATEWAY_URL"), "/") }

func connDir(name string) (string, error) {
	if secretDir() == "" {
		return "", fail(409, "FLEET_SECRET_DIR is not set on the coordinator")
	}
	return filepath.Join(secretDir(), "connections", name), nil
}

func connByName(q *Request, name any) (doc, error) {
	n, ok := name.(string)
	if !ok || !connName.MatchString(n) {
		return nil, fail(400, "Expected connection name")
	}
	c, err := maybe(q, "connection:"+n, "api-connection")
	if err == nil && c == nil {
		err = fail(404, "Unknown API key "+n)
	}
	return c, err
}

func connChange(q *Request, c doc, field string, previous, value any) error {
	return put(q, "setting-change", doc{"id": newID(), "target": "connection:" + astr(c["name"]), "holder": c["holder"], "field": field,
		"previous": previous, "value": value, "actor": actor(q, "operator"), "at": nowMs()})
}

func connKeys(q *Request, name string) ([]doc, error) {
	return all(q, "connection-key", doc{"connectionId": name})
}

// Snapshot (§3). Built in its own read transaction, so it shows only committed rows.
func connSnapshot(q *Request) (contract.GatewaySnapshot, error) {
	snap := contract.GatewaySnapshot{Schema: 2, GeneratedMs: nowMs(), Providers: snapshotProviders(), Connections: map[string]contract.GatewayConnection{},
		Tokens: []contract.GatewayRunToken{}, UsageTokenSha256: sha256hex([]byte(usageToken))}
	blocked, err := blockedConnections(q)
	if err != nil {
		return snap, err
	}
	connections, err := all(q, "api-connection", nil)
	if err != nil {
		return snap, err
	}
	for _, c := range connections {
		name := astr(c["name"])
		ks, err := connKeys(q, name)
		if err != nil {
			return snap, err
		}
		keys := []contract.GatewayKey{}
		for _, k := range ks {
			if k["state"] == "retired" {
				continue
			}
			e := contract.GatewayKey{Id: strings.TrimPrefix(astr(k["id"]), "key:"), State: astr(k["state"])}
			if at, ok := numOK(k["enabledAt"]); ok {
				ms := int64(at)
				e.EnabledAt = &ms
			}
			keys = append(keys, e)
		}
		bl := []string{}
		if blocked[name] {
			bl = append(bl, "connection")
		}
		gc := contract.GatewayConnection{Provider: astr(c["provider"]), Tools: strs(c["tools"]), Intake: astr(c["intake"]), Keys: keys, Blocked: bl}
		if l, ok := c["limits"].(map[string]any); ok && len(l) > 0 {
			// A value that does not decode leaves this connection at its defaults; it never blocks every other one.
			if limits, err := asT[contract.GatewayLimits](l); err != nil {
				log.Printf("connection %s: limits not published: %v", name, err)
			} else {
				gc.Limits = &limits
			}
		}
		if gc.Provider == "youtrack" {
			// Keep issue_get URLs for connections and existing tokens without the replacement download tool.
			const legacyProvider = "youtrack-legacy-attachments"
			if _, ok := snap.Providers[legacyProvider]; !ok {
				p := snap.Providers[gc.Provider]
				p.Tools = maps.Clone(p.Tools)
				issue := p.Tools["issue_get"]
				issue.Path = strings.Replace(issue.Path, "attachments(id,name,size,mimeType,comment(id))", "attachments(name,url,size)", 1)
				p.Tools["issue_get"] = issue
				snap.Providers = maps.Clone(snap.Providers)
				snap.Providers[legacyProvider] = p
			}
			if !slices.Contains(gc.Tools, "attachment_get") {
				gc.Provider = legacyProvider
			}
		}
		snap.Connections[name] = gc
	}
	toks, err := all(q, "run-token", nil)
	if err != nil {
		return snap, err
	}
	for _, t := range toks {
		if t["revokedAt"] != nil {
			continue
		}
		snap.Tokens = append(snap.Tokens, contract.GatewayRunToken{Sha256: astr(t["sha256"]), ExecutionId: astr(t["executionId"]), DirectedBy: astr(t["directedBy"]),
			ProjectId: astr(t["projectId"]), ExperimentId: astr(t["experimentId"]), Connections: strs(t["connections"]), FileDownloads: strs(t["fileDownloads"])})
	}
	return snap, nil
}

var snapshotMu sync.Mutex

func writeSnapshot(ctx context.Context, a *App) error {
	if gatewayDir() == "" {
		return nil
	}
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	var s contract.GatewaySnapshot
	if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) { s, err = connSnapshot(q); return }); err != nil {
		return err
	}
	if err := os.MkdirAll(gatewayDir(), 0o750); err != nil {
		return err
	}
	file := filepath.Join(gatewayDir(), "snapshot.json")
	tmp := fmt.Sprintf("%s.%d.tmp", file, os.Getpid())
	if err := os.WriteFile(tmp, jsonBytes(s), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// publish: the snapshot is rewritten after the caller's transaction commits (coalesced). A rewrite scheduled before the
// commit would read the old rows, and the tick does not republish on every change.
func publish(q *Request) {
	if q.tx == nil {
		schedulePublish()
		return
	}
	afterCommit(q, schedulePublish)
}

// schedulePublish rewrites the snapshot once per burst of calls (background.go); before the app runs there is none to
// write.
func schedulePublish() {
	if a := runningApp; a != nil {
		publishSnapshot(a)
	}
}

var publishSnapshot = coalesced("snapshot", writeSnapshot)

// connSync: key files without a row are registered; a directory without a connection becomes one (owner-held, every project).
func connSync(q *Request) error {
	if secretDir() == "" {
		return nil
	}
	root := filepath.Join(secretDir(), "connections")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	owner := ownerEmailOf(q.app)
	for _, ent := range entries {
		name := ent.Name()
		if !connName.MatchString(name) || !ent.IsDir() {
			continue
		}
		c, err := maybe(q, "connection:"+name, "api-connection")
		if err != nil {
			return err
		}
		if c != nil && c["holder"] != owner {
			log.Printf("secret directory %s: connection is held by %v, files not registered", name, c["holder"])
			continue
		}
		if c == nil {
			if providers[name] == nil {
				continue
			}
			c = doc{"id": "connection:" + name, "name": name, "provider": name, "holder": owner, "access": doc{"people": []any{}, "projects": []any{"*"}, "experiments": []any{}},
				"intake": "all", "tools": toolNames(name), "comment": "", "createdAt": nowMs()}
			if err := put(q, "api-connection", c); err != nil {
				return err
			}
		}
		files, _ := os.ReadDir(filepath.Join(root, name))
		for _, f := range files {
			if m := stagedKey.FindStringSubmatch(f.Name()); m != nil {
				// An add a crash interrupted: its bytes count only when the row committed.
				k, err := maybe(q, "key:"+m[1], "connection-key")
				if err != nil {
					return err
				}
				staged := filepath.Join(root, name, f.Name())
				if k != nil && k["state"] != "retired" && k["connectionId"] == name {
					os.Rename(staged, filepath.Join(root, name, m[1]))
				} else {
					os.Remove(staged)
				}
				continue
			}
			if !keyFile.MatchString(f.Name()) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, name, f.Name()))
			if err != nil {
				log.Printf("secret file %s %s: %v", name, f.Name(), err)
				continue
			}
			if fingerprint(strings.TrimSpace(string(b))) != f.Name() {
				continue // a renamed or edited file never registers under a wrong fingerprint
			}
			if k, err := maybe(q, "key:"+f.Name(), "connection-key"); err != nil {
				return err
			} else if k != nil && k["state"] == "retired" {
				os.Remove(filepath.Join(root, name, f.Name())) // a retirement committed before a crash removed the value
			} else if k == nil {
				if err := put(q, "connection-key", doc{"id": "key:" + f.Name(), "connectionId": name, "fingerprint": f.Name(), "state": "active", "addedAt": nowMs(), "addedBy": "import"}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func toolNames(provider string) []any {
	out := []any{}
	for _, k := range sortedKeysOf(providerTools(provider)) {
		out = append(out, k)
	}
	return out
}

func idList(v any, field string) ([]any, error) {
	if v == nil {
		return []any{}, nil
	}
	l, ok := v.([]any)
	if !ok {
		return nil, fail(400, field+": list of ids")
	}
	seen, out := map[string]bool{}, []any{}
	for _, x := range l {
		s, ok := x.(string)
		if !ok || len(s) > 320 {
			return nil, fail(400, field+": list of ids")
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

func init() {
	// Agent dispatch mints the run token for the profile's connections (dispatch.go).
	op("connections_list", func(q *Request, _ *contract.ConnectionsListRequest) (contract.ConnectionsListReply, error) {
		out, err := connectionsList(q)
		return replyAs[contract.ConnectionsListReply](out, err)
	})
	op("connections_create", func(q *Request, r *contract.ConnectionsCreateRequest) (contract.ConnectionsCreateReply, error) {
		out, err := connectionsCreate(q, r)
		return replyAs[contract.ConnectionsCreateReply](out, err)
	})
	op("connections_key_add", func(q *Request, r *contract.ConnectionsKeyAddRequest) (contract.ConnectionsKeyAddReply, error) {
		out, err := connectionsKeyAdd(q, r)
		return replyAs[contract.ConnectionsKeyAddReply](out, err)
	})
	op("connections_key_request", func(q *Request, r *contract.ConnectionsKeyRequestRequest) (contract.ConnectionsKeyRequestReply, error) {
		out, err := connectionsKeyRequest(q, r)
		return replyAs[contract.ConnectionsKeyRequestReply](out, err)
	})
	op("connections_delete", connectionsDelete)
	op("connections_key_state", func(q *Request, r *contract.ConnectionsKeyStateRequest) (contract.ConnectionsKeyStateReply, error) {
		out, err := connectionsKeyState(q, r)
		return replyAs[contract.ConnectionsKeyStateReply](out, err)
	})
	op("connections_set_tools", func(q *Request, r *contract.ConnectionsSetToolsRequest) (contract.ConnectionsSetToolsReply, error) {
		out, err := connectionsSetTools(q, r)
		return replyAs[contract.ConnectionsSetToolsReply](out, err)
	})
	op("connections_set_access", func(q *Request, r *contract.ConnectionsSetAccessRequest) (contract.ConnectionsSetAccessReply, error) {
		out, err := connectionsSetAccess(q, r)
		return replyAs[contract.ConnectionsSetAccessReply](out, err)
	})
	op("connections_set_intake", func(q *Request, r *contract.ConnectionsSetIntakeRequest) (contract.ConnectionsSetIntakeReply, error) {
		out, err := connectionsSetIntake(q, r)
		return replyAs[contract.ConnectionsSetIntakeReply](out, err)
	})
	op("connections_set_limits", func(q *Request, r *contract.ConnectionsSetLimitsRequest) (contract.ConnectionsSetLimitsReply, error) {
		out, err := connectionsSetLimits(q, r)
		return replyAs[contract.ConnectionsSetLimitsReply](out, err)
	})
	op("connections_token_create", func(q *Request, r *contract.ConnectionsTokenCreateRequest) (contract.ConnectionsTokenCreateReply, error) {
		out, err := connectionsTokenCreate(q, r)
		return replyAs[contract.ConnectionsTokenCreateReply](out, err)
	})
	op("connections_token_list", func(q *Request, _ *contract.ConnectionsTokenListRequest) (contract.ConnectionsTokenListReply, error) {
		out, err := connectionsTokenList(q)
		return replyAs[contract.ConnectionsTokenListReply](out, err)
	})
	op("connections_token_revoke", connectionsTokenRevoke)
}

func connectionsList(q *Request) (any, error) {
	keys, err := all(q, "connection-key", nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(keys, func(i, j int) bool {
		a, _ := numOK(keys[i]["addedAt"])
		b, _ := numOK(keys[j]["addedAt"])
		return a < b
	})
	hist, err := q.db().ConnHistory(q.ctx())
	if err != nil {
		return nil, err
	}
	connections, err := all(q, "api-connection", nil)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, c := range connections {
		ks, hs := []any{}, []any{}
		for _, k := range keys {
			if k["connectionId"] == c["name"] {
				ks = append(ks, k)
			}
		}
		for _, h := range hist {
			if h.Target == "connection:"+astr(c["name"]) {
				hs = append(hs, json.RawMessage(h.Data))
			}
		}
		c["keys"], c["spend"], c["history"] = ks, doc{"basis": "unavailable"}, hs
		out = append(out, c)
	}
	return doc{"providers": providers, "connections": out}, nil
}

func connectionsCreate(q *Request, r *contract.ConnectionsCreateRequest) (any, error) {
	name, provider := r.Name, r.Provider
	if !connName.MatchString(name) || providers[provider] == nil {
		return nil, fail(400, "Expected name and a known provider")
	}
	if slices.Contains(contract.ExecutorMCPServers, name) {
		return nil, fail(400, "Connection name "+name+" is an executor MCP server name")
	}
	if old, err := maybe(q, "connection:"+name, "api-connection"); err != nil {
		return nil, err
	} else if old != nil {
		return nil, fail(409, "API key name exists")
	}
	holder := directorOf(q)
	if r.Holder != nil {
		holder = strings.ToLower(*r.Holder)
	}
	if q.person != nil && !fullAccess(q.person) && holder != q.person.Email {
		return nil, fail(403, "Only the owner creates an API key for another person")
	}
	comment := deref(r.Comment)
	if len(comment) > 2000 {
		comment = comment[:2000]
	}
	c := doc{"id": "connection:" + name, "name": name, "provider": provider, "holder": holder, "access": doc{"people": []any{}, "projects": []any{}, "experiments": []any{}},
		"intake": "all", "tools": toolNames(provider), "comment": comment, "createdAt": nowMs()}
	if err := put(q, "api-connection", c); err != nil {
		return nil, err
	}
	publish(q)
	return c, nil
}

// connectionsKeyAdd (owner): writes the value file and the row; answers with the fingerprint only.
func connectionsKeyAdd(q *Request, r *contract.ConnectionsKeyAddRequest) (any, error) {
	c, err := connByName(q, r.Connection)
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
		if err := keyRequestOf(q, qid, astr(c["name"]), keyAnswer(astr(c["name"]), fp, "active")); err != nil {
			return nil, err
		}
	}
	if old, err := maybe(q, "key:"+fp, "connection-key"); err != nil {
		return nil, err
	} else if old != nil {
		if qid != "" && old["connectionId"] == c["name"] && old["state"] == "active" { // Fleet already holds it: answer with it
			if _, err := answerQuestion(q, qid, keyAnswer(astr(c["name"]), fp, "active"), actor(q, "operator"), nil); err != nil {
				return nil, err
			}
			return old, nil
		}
		return nil, fail(409, "Key already added")
	}
	dir, err := connDir(astr(c["name"]))
	if err != nil {
		return nil, err
	}
	k := doc{"id": "key:" + fp, "connectionId": c["name"], "fingerprint": fp, "state": "active", "addedAt": nowMs(), "addedBy": actor(q, "operator")}
	if err := put(q, "connection-key", k); err != nil {
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
	onEnd(q.tx, func(committed bool) {
		if !committed {
			os.Remove(staged)
		} else if err := os.Rename(staged, final); err != nil {
			log.Printf("key %s: %v", fp, err)
		}
	})
	if qid != "" { // the agent's question gets the fingerprint only, never the value
		if _, err := answerQuestion(q, qid, keyAnswer(astr(c["name"]), fp, "active"), actor(q, "operator"), nil); err != nil {
			return nil, err
		}
	}
	publish(q)
	return k, nil
}

// connectionsKeyRequest: an agent (or a person) asks the owner for a key value (docs/designs/fleet-connections.md §1,
// key requests): a secret question the owner answers through connections_key_add with questionId. No argument carries a
// value (the request is closed: an extra key is 400).
func connectionsKeyRequest(q *Request, r *contract.ConnectionsKeyRequestRequest) (any, error) {
	if _, err := projectContext(q, r.ProjectId); err != nil {
		return nil, err
	}
	c, err := connByName(q, r.Connection)
	if err != nil {
		return nil, err
	}
	rid, reason := r.RequestId, deref(r.Reason)
	if rid == "" || len(rid) > 200 || len([]rune(reason)) > 500 {
		return nil, fail(400, "Expected requestId and a reason of at most 500 characters")
	}
	text := fmt.Sprintf("Add a %s key for connection %s", c["provider"], c["name"])
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
	v := doc{"id": newID(), "projectId": r.ProjectId, "requestId": rid, "text": text, "state": "waiting", "createdAt": nowMs(), "actor": actor(q, "operator")}
	ask := doc{"id": v["id"], "kind": "secret", "text": text, "projectId": r.ProjectId, "askedBy": v["actor"], "requestId": rid, "askedAt": v["createdAt"],
		"options": doc{"connection": c["name"]}}
	if strings.HasPrefix(q.agent, "execution:") { // a host agent: the question shows in its conversation
		eid := strings.TrimPrefix(q.agent, "execution:")
		sid, err := q.db().ConnExecutionSession(q.ctx(), eid)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		ask["executionId"] = eid
		if err == nil {
			ask["sessionId"] = nil
			if sid != nil {
				ask["sessionId"] = *sid
			}
		}
	}
	_, err = askQuestion(q, ask)
	return v, err
}

// connectionsDelete (§1, owner) once every key is retired (no value file left), so a test connection can go. Run tokens
// drop the name (a new connection with it is not granted to them), open key requests are withdrawn, the history moves to
// an audit row that a new connection of the same name does not inherit.
func connectionsDelete(q *Request, r *contract.ConnectionsDeleteRequest) (contract.ConnectionsDeleteReply, error) {
	var no contract.ConnectionsDeleteReply
	c, err := connByName(q, r.Connection)
	if err != nil {
		return no, err
	}
	name := astr(c["name"])
	if _, err := getForUpdate(q, astr(c["id"]), "api-connection"); err != nil {
		return no, err
	}
	keys, err := connKeys(q, name)
	if err != nil {
		return no, err
	}
	for _, k := range keys {
		if k["state"] != "retired" {
			return no, fail(409, "Retire every key of "+name+" first")
		}
	}
	toks, err := all(q, "run-token", nil)
	if err != nil {
		return no, err
	}
	for _, t := range toks {
		if names := strs(t["connections"]); slices.Contains(names, name) {
			t["connections"] = slices.DeleteFunc(names, func(n string) bool { return n == name })
			if err := put(q, "run-token", t); err != nil {
				return no, err
			}
		}
	}
	if err := q.db().DeleteAPIConnection(q.ctx(), astr(c["id"])); err != nil {
		return no, err
	}
	if astr(c["id"]) != "" {
		secrets, err := q.db().ConnOpenKeyRequests(q.ctx(), name)
		if err != nil {
			return no, err
		}
		if err := questionsWithdraw(q, secrets); err != nil {
			return no, err
		}
	}
	if err := put(q, "setting-change", doc{"id": newID(), "target": "deleted-connection:" + name, "field": "connection", "value": "deleted", "actor": actor(q, "operator"), "at": nowMs()}); err != nil {
		return no, err
	}
	if dir, err := connDir(name); err == nil {
		onEnd(q.tx, func(committed bool) {
			if committed { // every key is retired: only stale stages can remain, and connSync must not re-register them
				if err := os.RemoveAll(dir); err != nil {
					log.Printf("connection %s: %v", name, err)
				}
			}
		})
	}
	publish(q)
	return contract.ConnectionsDeleteReply{Name: name, Deleted: true}, nil
}

// connectionsKeyState: rotation (§2): active <-> draining, any -> retired (the value file is deleted; the row stays);
// disabled -> active is the owner's.
func connectionsKeyState(q *Request, r *contract.ConnectionsKeyStateRequest) (any, error) {
	id := r.KeyId
	if id != "" && !strings.HasPrefix(id, "key:") {
		id = "key:" + id
	}
	k, err := maybe(q, id, "connection-key")
	if err != nil {
		return nil, err
	}
	if k == nil {
		return nil, fail(404, "Unknown key")
	}
	c, err := connByName(q, k["connectionId"])
	if err != nil {
		return nil, err
	}
	// The request names the key, not the connection: the holder rule is checked on the key's connection here.
	if q.person != nil && !fullAccess(q.person) && q.person.Email != c["holder"] {
		return nil, fail(403, "Only the holder or the owner changes this API key")
	}
	to := string(r.State)
	if k["state"] == "retired" {
		return nil, fail(409, "A retired key stays retired")
	}
	if k["state"] == "disabled" && to != "retired" && q.person != nil && !fullAccess(q.person) {
		return nil, fail(403, "Only the owner handles key values")
	}
	if to == k["state"] {
		return k, nil
	}
	if err := connChange(q, c, "key:"+astr(k["fingerprint"]), k["state"], to); err != nil {
		return nil, err
	}
	k["state"], k["updatedAt"] = to, nowMs()
	if to == "active" {
		delete(k, "disabledReason")
		delete(k, "disabledAt")
		delete(k, "problem")
		k["enabledAt"] = nowMs()
	}
	if err := put(q, "connection-key", k); err != nil {
		return nil, err
	}
	// The key left disabled (enabled again or retired): its refused-key question no longer needs the owner.
	if err := problemClose(q, "key:"+astr(k["fingerprint"])+":disabled"); err != nil {
		return nil, err
	}
	if to == "retired" {
		if dir, err := connDir(astr(c["name"])); err == nil {
			// The value goes only once the retirement committed; connSync removes it if a crash came in between.
			path := filepath.Join(dir, astr(k["fingerprint"]))
			onEnd(q.tx, func(committed bool) {
				if committed {
					os.Remove(path)
				}
			})
		}
	}
	publish(q)
	return k, nil
}

// connectionsSetTools (owner): the explicit provider tool grant.
func connectionsSetTools(q *Request, r *contract.ConnectionsSetToolsRequest) (any, error) {
	if _, err := getForUpdate(q, "connection:"+r.Connection, "api-connection"); err != nil && !errors.Is(err, errNotFound) {
		return nil, err
	}
	c, err := connByName(q, r.Connection)
	if err != nil {
		return nil, err
	}
	seen, tools := map[string]bool{}, []any{}
	for _, name := range r.Tools {
		if name == "" || seen[name] || providerTools(astr(c["provider"]))[name] == nil {
			return nil, fail(400, "tools must be unique recognized provider tool names")
		}
		seen[name] = true
		tools = append(tools, name)
	}
	if canonical(tools) != canonical(c["tools"]) {
		if err := connChange(q, c, "tools", c["tools"], tools); err != nil {
			return nil, err
		}
		c["tools"], c["updatedAt"] = tools, nowMs()
		if err := put(q, "api-connection", c); err != nil {
			return nil, err
		}
		publish(q)
	}
	return c, nil
}

func connectionsSetAccess(q *Request, r *contract.ConnectionsSetAccessRequest) (any, error) {
	c, err := connByName(q, r.Connection)
	if err != nil {
		return nil, err
	}
	a := docOf(r.Access)
	people, err := idList(a["people"], "people")
	if err != nil {
		return nil, err
	}
	for i, p := range people {
		people[i] = strings.ToLower(p.(string))
	}
	projects, err := idList(a["projects"], "projects")
	if err != nil {
		return nil, err
	}
	exps, err := idList(a["experiments"], "experiments")
	if err != nil {
		return nil, err
	}
	next := doc{"people": people, "projects": projects, "experiments": exps}
	if canonical(next) != canonical(c["access"]) {
		if err := connChange(q, c, "access", c["access"], next); err != nil {
			return nil, err
		}
		c["access"], c["updatedAt"] = next, nowMs()
		if err := put(q, "api-connection", c); err != nil {
			return nil, err
		}
		publish(q)
	}
	return c, nil
}

func connectionsSetIntake(q *Request, r *contract.ConnectionsSetIntakeRequest) (any, error) {
	c, err := connByName(q, r.Connection)
	if err != nil {
		return nil, err
	}
	in := string(r.Intake)
	if c["intake"] != in {
		if err := connChange(q, c, "intake", c["intake"], in); err != nil {
			return nil, err
		}
		c["intake"], c["updatedAt"] = in, nowMs()
		if err := put(q, "api-connection", c); err != nil {
			return nil, err
		}
		publish(q)
	}
	return c, nil
}

// connectionsSetLimits: file tool limits (§9); the stored object is replaced as a whole; an omitted field takes the
// gateway's default.
func connectionsSetLimits(q *Request, r *contract.ConnectionsSetLimitsRequest) (any, error) {
	c, err := connByName(q, r.Connection)
	if err != nil {
		return nil, err
	}
	next := doc{}
	for k, v := range map[string]*int64{"fileBytes": r.Limits.FileBytes, "replyBytes": r.Limits.ReplyBytes, "stallMs": r.Limits.StallMs, "answerMs": r.Limits.AnswerMs} {
		if v == nil {
			continue
		}
		if *v < 1 || *v > 1<<53 {
			return nil, fail(400, "limits."+k+" must be a positive integer")
		}
		next[k] = float64(*v)
	}
	previous, _ := c["limits"].(map[string]any)
	if previous == nil {
		previous = doc{}
	}
	if canonical(next) != canonical(previous) {
		if err := connChange(q, c, "limits", previous, next); err != nil {
			return nil, err
		}
		c["limits"], c["updatedAt"] = next, nowMs()
		if len(next) == 0 {
			delete(c, "limits")
		}
		if err := put(q, "api-connection", c); err != nil {
			return nil, err
		}
		publish(q)
	}
	return c, nil
}

// connectionsTokenCreate (owner, §4): a long-lived bearer for the owner's interactive sessions; the value is answered
// once. Full access (owner, grant "*" such as site-agents) mints during development; directedBy records the real actor.
func connectionsTokenCreate(q *Request, r *contract.ConnectionsTokenCreateRequest) (any, error) {
	name := r.Name
	if !connName.MatchString(name) {
		return nil, fail(400, "Expected token name")
	}
	toks, err := all(q, "run-token", nil)
	if err != nil {
		return nil, err
	}
	for _, t := range toks {
		if t["personal"] == name && t["revokedAt"] == nil {
			return nil, fail(409, "Token name in use")
		}
	}
	requested := append([]string{}, r.Connections...)
	m, err := mint(q, doc{"executionId": "token:" + name, "directedBy": directorOf(q), "personal": name}, requested)
	if err != nil {
		return nil, err
	}
	if m["token"] == nil {
		r := []string{}
		for _, x := range m["refused"].([]string) {
			r = append(r, x)
		}
		return nil, fail(409, "No usable connection: "+strings.Join(r, ", "))
	}
	m["name"] = name
	return m, nil
}

func connectionsTokenList(q *Request) (any, error) {
	toks, err := all(q, "run-token", nil)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, t := range toks {
		if t["personal"] != nil {
			out = append(out, doc{"name": t["personal"], "connections": t["connections"], "createdAt": t["createdAt"], "revokedAt": t["revokedAt"], "directedBy": t["directedBy"]})
		}
	}
	return doc{"tokens": out}, nil
}

func connectionsTokenRevoke(q *Request, r *contract.ConnectionsTokenRevokeRequest) (contract.ConnectionsTokenRevokeReply, error) {
	var no contract.ConnectionsTokenRevokeReply
	toks, err := all(q, "run-token", nil)
	if err != nil {
		return no, err
	}
	n := 0
	for _, t := range toks {
		if t["personal"] == r.Name && t["revokedAt"] == nil {
			t["revokedAt"] = nowMs()
			if err := put(q, "run-token", t); err != nil {
				return no, err
			}
			n++
		}
	}
	if n == 0 {
		return no, fail(404, "Unknown token")
	}
	publish(q)
	return contract.ConnectionsTokenRevokeReply{Name: r.Name, Revoked: true}, nil
}

func init() {

	onStart(func(ctx context.Context, a *App) {
		if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
			if err := revokeSteps(q); err != nil {
				return err
			}
			return connSync(q)
		}); err != nil {
			log.Printf("connections sync: %v", err)
		}
		if err := writeSnapshot(ctx, a); err != nil {
			log.Printf("snapshot: %v", err)
		}
		// Spool ingest and token revocation (FLEET_CONNECTION_TICK_MS: test seam, 0 turns it off); usage reconcile every 10 min.
		every(ctx, "connections", envMs("FLEET_CONNECTION_TICK_MS", 3000), func(ctx context.Context) error {
			err := connTickPass(ctx, a)
			if err != nil {
				log.Printf("connections tick: %v", err)
			}
			return err
		})
		every(ctx, "reconcile", envMs("FLEET_RECONCILE_MS", 600000), func(ctx context.Context) error { connReconcile(ctx, a); return nil })
	})
}

func envMs(k string, d int) time.Duration {
	var n int
	if _, err := fmt.Sscan(envOr(k, fmt.Sprint(d)), &n); err != nil {
		n = d
	}
	return time.Duration(n) * time.Millisecond
}

// mintRun: what a run token is minted for: its execution, director, project, experiment and pipeline run, and its
// marks (a pipeline step's, a person's named token, Fleet's own reads).
type mintRun struct {
	ExecutionID, DirectedBy, ProjectID, ExperimentID, PipelineRunID, Personal, SystemActor *string
	Step                                                                                   bool
}

// mintRunOf: a run record's fields; a field that is not a string is absent.
func mintRunOf(run doc) mintRun {
	str := func(k string) *string {
		if s, ok := run[k].(string); ok {
			return &s
		}
		return nil
	}
	return mintRun{ExecutionID: str("executionId"), DirectedBy: str("directedBy"), ProjectID: str("projectId"), ExperimentID: str("experimentId"),
		PipelineRunID: str("pipelineRunId"), Personal: str("personal"), SystemActor: str("systemActor"), Step: run["step"] == true}
}

// orNil: the string, or an untyped nil (a record field left out of its column).
func orNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// Per-run tokens (§4): granted are the requested connections that exist, take intake and whose access names the run.
func connAllowed(c doc, run doc) bool { return connAllowedFor(c, mintRunOf(run)) }

func connAllowedFor(c doc, r mintRun) bool {
	a := mapOf(c["access"])
	has := func(list string, v *string) bool {
		for _, x := range anys(a[list]) {
			if v != nil && x == *v {
				return true
			}
		}
		return false
	}
	if len(anys(a["people"])) == 0 && len(anys(a["projects"])) == 0 && len(anys(a["experiments"])) == 0 {
		return r.DirectedBy != nil && *r.DirectedBy == c["holder"]
	}
	all := "*"
	return has("people", r.DirectedBy) || has("projects", &all) || has("projects", r.ProjectID) || has("experiments", r.ExperimentID)
}

// mint: {token, refused, connections}; token is nil when nothing was granted.
func mint(q *Request, run doc, requested []string) (doc, error) {
	return mintFor(q, mintRunOf(run), requested)
}

func mintFor(q *Request, run mintRun, requested []string) (doc, error) {
	seen, uniq := map[string]bool{}, []string{}
	for _, r := range requested {
		if !seen[r] {
			seen[r] = true
			uniq = append(uniq, r)
		}
	}
	granted := []doc{}
	for _, name := range uniq {
		c, err := maybe(q, "connection:"+name, "api-connection")
		if err != nil {
			return nil, err
		}
		if c != nil && c["intake"] == "all" && connAllowedFor(c, run) {
			granted = append(granted, c)
		}
	}
	if len(granted) == 0 {
		return doc{"token": nil, "connections": []any{}, "refused": uniq}, nil
	}
	token := randomToken()
	sha := sha256hex([]byte(token))
	names, specs, refused := []any{}, []any{}, []string{}
	var fileDownloads []string
	for _, c := range granted {
		names = append(names, c["name"])
		p := mapOf(providers[astr(c["provider"])])
		tools := []any{}
		for _, t := range anys(c["tools"]) {
			if spec := mapOf(mapOf(p["tools"])[astr(t)]); spec != nil {
				// file and idempotent tell the client the transfer mode and how an edge error reads; its MCP server
				// keeps them from the model.
				entry := doc{"name": t, "description": spec["description"], "inputSchema": spec["inputSchema"], "idempotent": spec["idempotent"] == true}
				if spec["file"] != nil {
					entry["file"] = spec["file"]
				}
				if spec["file"] == "download" && !slices.Contains(fileDownloads, astr(c["name"])) {
					fileDownloads = append(fileDownloads, astr(c["name"]))
				}
				tools = append(tools, entry)
			}
		}
		spec := doc{"name": c["name"], "instructions": p["instructions"], "tools": tools}
		// The configured waits let the client outwait the gateway (§9).
		waits := doc{}
		for _, k := range []string{"stallMs", "answerMs"} {
			if v := mapOf(c["limits"])[k]; v != nil {
				waits[k] = v
			}
		}
		if len(waits) > 0 {
			spec["limits"] = waits
		}
		specs = append(specs, spec)
	}
	for _, n := range uniq {
		ok := false
		for _, g := range names {
			ok = ok || g == n
		}
		if !ok {
			refused = append(refused, n)
		}
	}
	row := doc{"id": "run-token:" + sha, "sha256": sha, "executionId": orNil(run.ExecutionID), "directedBy": orNil(run.DirectedBy), "projectId": projectOrNil(orNil(run.ProjectID)),
		"experimentId": orNil(run.ExperimentID), "pipelineRunId": orNil(run.PipelineRunID), "connections": names, "createdAt": nowMs()}
	if len(fileDownloads) > 0 {
		row["fileDownloads"] = fileDownloads
	}
	if run.Step {
		row["step"] = true
	}
	if run.Personal != nil {
		row["personal"] = *run.Personal
	}
	if run.SystemActor != nil {
		row["systemActor"] = *run.SystemActor // its calls are Fleet's own activity (connRecord)
	}
	err := put(q, "run-token", row)
	if err != nil {
		return nil, err
	}
	publish(q)
	return doc{"token": token, "refused": refused, "connections": specs}, nil
}

func revokeTokens(q *Request, executionID string) error {
	n, err := q.db().RevokeExecutionTokens(q.ctx(), db.RevokeExecutionTokensParams{At: float64(nowMs()), ExecutionID: executionID})
	if err != nil {
		return err
	}
	if n > 0 {
		publish(q)
	}
	return nil
}

// At start no api step call of an earlier process is in flight: their tokens are revoked.
func revokeSteps(q *Request) error {
	return q.db().RevokeStepTokensBefore(q.ctx(), db.RevokeStepTokensBeforeParams{At: float64(nowMs()), Started: float64(processStarted)})
}

// Spool ingest (§3): every gateway line becomes one tool_run with connection_cost, idempotent by callId. Each
// unrecorded line runs in its own transaction locked only by "usageKey:" + key (or NOLOCK when no usageKey), never
// acquiring GLOBAL. A past day's file is deleted once its lines are committed; a file with a line that could not be
// imported is kept.
func connIngestPass(ctx context.Context, a *App) (int, error) {
	if gatewayDir() == "" {
		return 0, nil
	}
	dir := filepath.Join(gatewayDir(), "state", "spool")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, nil
	}
	added := 0
	yesterday := time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02")
	names := []string{}
	for _, e := range entries {
		if spoolFile.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, f := range names {
		// ponytail: the whole file is read each pass (one day of lines); keep a read offset if a day's file gets large.
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		// Only complete lines while the gateway may still write the day (today, yesterday); a past day is read whole, so
		// a last line without its newline is imported or keeps the file.
		past := f[:10] < yesterday
		if !past {
			b = b[:bytes.LastIndexByte(b, '\n')+1]
		}
		// Whole lines of any length (no Scanner token cap, W3-2); a line not imported keeps the file.
		unimported := 0
		var parsed []contract.CallLine
		var callIDs []string
		for _, line := range bytes.Split(b, []byte{'\n'}) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var l contract.CallLine
			if err := json.Unmarshal(line, &l); err != nil {
				// Kept in the file; a line here means the gateway and the contract disagree.
				log.Printf("spool %s: line not imported: %v", f, err)
				unimported++
				continue
			}
			parsed = append(parsed, l)
			if l.CallId != "" && l.TokenSha256 != nil && l.ExecutionId != nil {
				callIDs = append(callIDs, "call:"+l.CallId)
			}
		}
		// Rows already recorded, with their transfer phase ("" for a one-line call).
		existing := map[string]string{}
		if len(callIDs) > 0 {
			if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
				rs, err := q.db().ConnCallPhases(q.ctx(), callIDs)
				if err != nil {
					return err
				}
				for _, r := range rs {
					existing[r.ID] = r.Phase
				}
				return nil
			}); err != nil {
				return added, err
			}
		}
		for _, l := range parsed {
			if l.CallId == "" || l.TokenSha256 == nil || l.ExecutionId == nil {
				continue
			}
			phase, recorded := existing["call:"+l.CallId]
			if l.Phase != nil && *l.Phase == contract.CallPhaseCompleted {
				// A transfer's completion line updates its started row, also one this pass just inserted from the same file.
				if phase == string(contract.CallPhaseCompleted) {
					continue
				}
				if err := runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error { return connComplete(q, l) }); err != nil {
					return added, err
				}
				continue
			}
			if recorded {
				continue
			}
			lock := NOLOCK
			if l.UsageKey != nil && *l.UsageKey != "" {
				lock = "usageKey:" + *l.UsageKey
			}
			var ok bool
			if err := runTx(ctx, a, nil, true, lock, func(q *Request) error {
				var err error
				ok, err = connRecord(q, l)
				if err != nil {
					return err
				}
				if ok {
					publish(q)
				}
				return nil
			}); err != nil {
				return added, err
			}
			if ok {
				added++
			}
		}
		if past && unimported > 0 {
			log.Printf("spool %s: past day kept, %d line(s) not imported", f, unimported)
		} else if past {
			os.Remove(filepath.Join(dir, f))
		}
	}
	return added, nil
}

func connRecord(q *Request, l contract.CallLine) (bool, error) {
	if l.CallId == "" || l.TokenSha256 == nil || l.ExecutionId == nil {
		return false, nil
	}
	t, err := maybe(q, "run-token:"+*l.TokenSha256, "run-token")
	if err != nil || t == nil {
		return false, err
	}
	id := "call:" + l.CallId
	state := "failed"
	if l.Outcome == contract.CallOutcomeOk {
		state = "succeeded"
	}
	data := doc{"tool": l.Tool, "connection": l.Connection, "outcome": l.Outcome, "errorCode": l.ErrorCode, "status": l.Status,
		"keyFingerprint": l.KeyFingerprint, "usage": l.Usage, "directedBy": t["directedBy"], "startedAt": l.StartedAt, "ms": l.Ms}
	if l.Transfer != nil {
		data["transfer"] = l.Transfer
	}
	if l.Phase != nil {
		data["phase"] = *l.Phase
		if *l.Phase == contract.CallPhaseCompleted && isUpload(l) {
			data["uploadAccounted"] = true
		}
	}
	if t["personal"] != nil {
		data["tokenName"] = t["personal"]
	}
	if t["pipelineRunId"] != nil {
		data["pipelineRunId"] = t["pipelineRunId"]
	}
	var parent any = t["executionId"]
	if t["personal"] != nil {
		parent = nil
	}
	var pr db.ConnCallParentRow
	if pid := astr(parent); pid != "" {
		var err error
		if pr, err = q.db().ConnCallParent(q.ctx(), pid); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}
	pTask, pSession, pDir, pPipe, pStep, pProf, pPipeRev, pProfRev := pr.TaskID, pr.SessionID, pr.DirectedBy, pr.PipelineID, pr.StepID, pr.ProfileID, pr.PipelineRevision, pr.ProfileRevision
	var startedAt, endedAt any
	if l.StartedAt > 0 {
		dur := l.Ms
		if dur < 0 {
			dur = 0
		}
		startedAt, endedAt = l.StartedAt, l.StartedAt+dur
	}
	if astr(t["projectId"]) == "" && pSession == nil {
		if t["directorAccessRevokedAt"] != nil {
			data["directorAccessRevokedAt"] = t["directorAccessRevokedAt"]
		} else if dir := firstString(t["directedBy"], deref(pDir)); dir != "" {
			active, err := q.db().ConnPersonExists(q.ctx(), dir)
			if err != nil {
				return false, err
			}
			if !active {
				data["directorAccessRevokedAt"] = nowMs()
			}
		}
	}
	inserted, err := insertExecution(q, doc{
		"id": id, "kind": "tool_run", "state": state, "projectId": t["projectId"], "parentExecutionId": parent,
		"experimentId": t["experimentId"], "taskId": pTask, "sessionId": pSession,
		"directedBy": nilIf(firstString(t["directedBy"], deref(pDir))), "startedAt": startedAt, "endedAt": endedAt,
		"pipelineId": pPipe, "pipelineRevision": pPipeRev, "stepId": pStep, "profileId": pProf, "profileRevision": pProfRev,
		"systemActor": t["systemActor"], "data": data,
	})
	if err != nil || !inserted {
		return false, err
	}
	if l.Outcome != contract.CallOutcomeOk {
		if err := q.db().ConnCallProblem(q.ctx(), db.ConnCallProblemParams{ID: id, Problem: problemOf(string(l.Outcome), deref(l.ErrorCode))}); err != nil {
			return false, err
		}
	}
	if err := connAccount(q, id, l, t["step"] == true); err != nil {
		return false, err
	}
	return true, nil
}

// connAccount applies provider usage and key consequences after a call or upload completion is recorded.
func connAccount(q *Request, id string, l contract.CallLine, step bool) error {
	var cost any
	if u := l.Usage; u != nil {
		amount := u.Amount
		if l.UsageKey != nil {
			// A finished job's charge (Meshy consumed_credits) counts once per (connection, job); later polls cost 0. A
			// pipeline step's job is counted by recordJob instead.
			// Concurrent ingests of two polls of one job serialize on the key.
			if err := acquireTxLock(q, "usageKey:"+*l.UsageKey); err != nil {
				return err
			}
			n, err := q.db().ConnUsageKeyCount(q.ctx(), db.ConnUsageKeyCountParams{UsageKey: *l.UsageKey, ID: id})
			if err != nil {
				return err
			}
			if n > 0 || step {
				amount = 0
			} else if err := q.db().ConnSetUsageKey(q.ctx(), db.ConnSetUsageKeyParams{UsageKey: *l.UsageKey, ID: id}); err != nil {
				return err
			}
		}
		cost = doc{"unit": u.Unit, u.Unit: amount, "amount": amount, "connection": l.Connection}
	}
	if err := writeConnectionCost(q, id, cost); err != nil {
		return err
	}
	at := l.StartedAt
	if l.ProviderAttemptAt != nil {
		at = *l.ProviderAttemptAt
	}
	if d := l.Disabled; d != nil && d.Key != "" {
		if err := connDisable(q, d.Key, d.Status, at); err != nil {
			return err
		}
	}
	if p := l.KeyProblem; p != nil && p.Key != "" {
		k, err := maybe(q, "key:"+p.Key, "connection-key")
		if err != nil {
			return err
		}
		if k != nil {
			msg := p.Message
			if len(msg) > 500 {
				msg = msg[:500]
			}
			if at == 0 {
				at = nowMs()
			}
			if enabled, ok := numOK(k["enabledAt"]); ok && int64(enabled) >= at {
				return nil
			}
			k["problem"] = doc{"status": p.Status, "message": msg, "at": at}
			if err := put(q, "connection-key", k); err != nil {
				return err
			}
		}
	}
	return nil
}

func isUpload(l contract.CallLine) bool {
	return l.Transfer != nil && l.Transfer.Direction == contract.CallTransferDirectionUpload
}

// connComplete applies a file transfer's completed line (§9) to its started row. A repeated pass leaves the
// completed row unchanged. If the started line was lost, the completed line is recorded alone.
func connComplete(q *Request, l contract.CallLine) error {
	id := "call:" + l.CallId
	p := problemOf(string(l.Outcome), deref(l.ErrorCode))
	state, problem := "failed", &p
	if l.Outcome == contract.CallOutcomeOk {
		state, problem = "succeeded", nil
	}
	ms := max(l.Ms, 0)
	patch := doc{"outcome": l.Outcome, "status": l.Status, "errorCode": l.ErrorCode, "ms": ms, "phase": contract.CallPhaseCompleted}
	if l.Transfer != nil {
		patch["transfer"] = l.Transfer
	}
	upload := isUpload(l)
	if upload {
		patch["keyFingerprint"], patch["usage"], patch["uploadAccounted"] = l.KeyFingerprint, l.Usage, true
	}
	n, err := q.db().ConnTransferComplete(q.ctx(), db.ConnTransferCompleteParams{ID: id, Ms: ms, Problem: problem, Patch: jsonBytes(patch), Upload: upload})
	if err != nil {
		return err
	}
	if n == 1 {
		if _, err := executionTransition(q, id, executionEvent{To: state, KeepEnd: true, Correct: true}); err != nil {
			return err
		}
		if upload {
			// A file upload has no usageOnce. Its usage and key effects are known only after the POST.
			if err := connAccount(q, id, l, false); err != nil {
				return err
			}
		}
		if err := q.db().ConnTransferTotals(q.ctx(), db.ConnTransferTotalsParams{ID: id, Ms: ms}); err != nil {
			return err
		}
		publish(q)
		return nil
	}
	if exists, err := q.db().ConnExecutionExists(q.ctx(), id); err != nil || exists {
		return err
	}
	ok, err := connRecord(q, l)
	if ok {
		publish(q)
	}
	return err
}

func connDisable(q *Request, fp string, status, attemptedAt int64) error {
	k, err := maybe(q, "key:"+fp, "connection-key")
	if err != nil || k == nil || k["state"] == "retired" || k["state"] == "disabled" {
		return err
	}
	if enabled, ok := numOK(k["enabledAt"]); ok && attemptedAt > 0 && int64(enabled) > attemptedAt {
		return nil
	}
	st := fmt.Sprint(status)
	k["state"], k["disabledReason"], k["disabledAt"] = "disabled", "provider answered HTTP "+st, nowMs()
	if err := put(q, "connection-key", k); err != nil {
		return err
	}
	publish(q)
	return problemRaise(q, "", Problem{Cause: "key:" + fp + ":disabled", Options: []any{"seen"},
		Text: fmt.Sprintf("API key %s of %v was refused by the provider (HTTP %s) and is disabled. Re-enable it on the API keys screen once it works again, or retire it.", fp, k["connectionId"], st)})
}

var (
	lastBlockedMu sync.Mutex
	lastBlocked   string
)

// One pass: ingest without GLOBAL, republish when the blocked set changed, revoke tokens of ended or unknown runs.
func connTickPass(ctx context.Context, a *App) error {
	if _, err := connIngestPass(ctx, a); err != nil {
		return err
	}
	var blockedJSON string
	var toRevoke []string
	if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		b, err := blockedConnections(q)
		if err != nil {
			return err
		}
		m := map[string]any{}
		for k, v := range b {
			if v {
				m[k] = true
			}
		}
		blockedJSON = string(jsonBytes(sortedKeysOf(m)))
		open, err := q.db().OpenRunTokenExecutions(q.ctx())
		if err != nil {
			return err
		}
		for _, id := range open {
			state, err := q.db().ConnRunState(q.ctx(), id)
			if err != nil {
				return err
			}
			if state == "" || executionEnds(state) {
				toRevoke = append(toRevoke, id)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if len(toRevoke) > 0 {
		if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
			for _, id := range toRevoke {
				if err := revokeTokens(q, id); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	lastBlockedMu.Lock()
	changed := blockedJSON != lastBlocked
	if changed {
		lastBlocked = blockedJSON
	}
	lastBlockedMu.Unlock()
	if changed {
		schedulePublish()
	}
	return nil
}

// Reconcile (§2): the provider's per-key usage since the first observation against the credits recorded through the gateway.
func connReconcile(ctx context.Context, a *App) {
	if gatewayURL() == "" {
		return
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", gatewayURL()+"/v1/usage", nil)
	req.Header.Set("Authorization", "Bearer "+usageToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer res.Body.Close()
	var report doc
	if res.StatusCode/100 != 2 || json.NewDecoder(res.Body).Decode(&report) != nil {
		return
	}
	err = runTx(ctx, a, nil, true, "", func(q *Request) error {
		for _, r := range anys(report["keys"]) {
			row := mapOf(r)
			k, err := maybe(q, "key:"+astr(row["key"]), "connection-key")
			if err != nil {
				return err
			}
			if k == nil {
				continue
			}
			usage, ok := numOK(row["usage"])
			if !ok {
				rc := mapOf(k["reconcile"])
				if rc == nil {
					rc = doc{}
				}
				unknown := "no usage reported"
				if row["unknown"] != nil {
					unknown = fmt.Sprint(row["unknown"])
				}
				rc["at"], rc["unknown"] = report["at"], unknown
				k["reconcile"] = rc
			} else {
				base := mapOf(k["usageBaseline"])
				if base == nil {
					base = doc{"usage": usage, "at": report["at"]}
					k["usageBaseline"] = base
				}
				at, _ := numOK(base["at"])
				fp, _ := k["fingerprint"].(string)
				recorded, err := q.db().ConnKeyRecordedSince(q.ctx(), db.ConnKeyRecordedSinceParams{Fingerprint: fp, Since: int64(at)})
				if err != nil {
					return err
				}
				bu, _ := numOK(base["usage"])
				delta := usage - bu
				k["reconcile"] = doc{"at": report["at"], "providerDelta": delta, "recorded": recorded, "difference": delta - recorded}
			}
			if err := put(q, "connection-key", k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("usage reconcile: %v", err)
	}
}

// gatewayCall: one in-process call through fleet-gateway (contract.GatewayCall). idempotent is the catalogue tool's
// flag: an edge 5xx (no gateway call id) on a tool that may not be repeated reads unknown.
func gatewayCall(ctx context.Context, token, connection, tool string, args any, idempotent bool) contract.CallLine {
	t := contract.GatewayTarget{URL: gatewayURL(), Token: token, Idempotent: idempotent}
	if strings.HasPrefix(t.URL, "https://") {
		t.Header = cfAccessHeaders()
	}
	return contract.GatewayCall(ctx, t, connection, tool, jsonBytes(args))
}

func cfAccessHeaders() map[string]string {
	p := os.Getenv("FLEET_CF_ACCESS_FILE")
	if p == "" {
		h, _ := os.UserHomeDir()
		p = filepath.Join(h, ".config/fleet/cf-access-machine.env")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	vals := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(l, "export "))
		if k, v, ok := strings.Cut(l, "="); ok {
			vals[k] = strings.Trim(v, `'"`)
		}
	}
	out := map[string]string{}
	if v, ok := vals["CF_ACCESS_CLIENT_ID"]; ok {
		out["CF-Access-Client-Id"] = v
	}
	if v, ok := vals["CF_ACCESS_CLIENT_SECRET"]; ok {
		out["CF-Access-Client-Secret"] = v
	}
	return out
}

// keyAnswer: the only answer a key request records besides decline: the JSON text {connection, fingerprint, state}.
func keyAnswer(connection, fp, state string) string {
	return js(doc{"connection": connection, "fingerprint": fp, "state": state})
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

// keyAnswerCheck: the answer hook of a key request (tasks area calls it). Only the owner settles it, with decline or
// with the fingerprint answer of a key added to its connection, so no answer path carries a value.
func keyAnswerCheck(q *Request, qn doc, answer string) error {
	if qn["kind"] != "secret" {
		return nil
	}
	if q.agent != "" || q.person == nil || !fullAccess(q.person) { // never the asking agent, even with the owner's rights
		return fail(403, "Only the owner answers a key request")
	}
	if answer == "decline" {
		return nil
	}
	var a struct{ Connection, Fingerprint, State string }
	conn := astr(mapOf(qn["options"])["connection"])
	if json.Unmarshal([]byte(answer), &a) != nil || a.Connection != conn || keyAnswer(a.Connection, a.Fingerprint, a.State) != answer {
		return fail(400, "Answer decline, or add the key in its field")
	}
	k, err := maybe(q, "key:"+a.Fingerprint, "connection-key")
	if err != nil {
		return err
	}
	if k == nil || k["connectionId"] != conn || a.State != "active" || k["state"] != "active" {
		return fail(400, "Answer decline, or add the key in its field")
	}
	return nil
}

func init() {
}
