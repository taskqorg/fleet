// API key connections (docs/designs/fleet-connections.md). Key values live only in
// <FLEET_SECRET_DIR>/connections/<connection>/<fingerprint> (0600); rows hold the fingerprint and state. Every change
// republishes <FLEET_GATEWAY_DIR>/snapshot.json (no key values) once the change committed.
package main

import (
	"fleet/obs"

	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"fleet/contract"
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

func connOwner(q *Request) error {
	if q.person != nil && !fullAccess(q.person) {
		return fail(403, "Only the owner handles key values")
	}
	return nil
}

func connHolderOrOwner(q *Request, c doc) error {
	if q.person != nil && !fullAccess(q.person) && q.person.Email != c["holder"] {
		return fail(403, "Only the holder or the owner changes this API key")
	}
	return nil
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
	return rows(q, "SELECT data FROM document WHERE kind='connection-key' AND data->>'connectionId'=$1 ORDER BY (data->>'addedAt')::bigint, seq", name)
}

// Snapshot (§3). Built in its own read transaction, so it shows only committed rows.
func connSnapshot(q *Request) (contract.GatewaySnapshot, error) {
	snap := contract.GatewaySnapshot{Schema: 2, GeneratedMs: nowMs(), Providers: gatewayProviders, Connections: map[string]contract.GatewayConnection{},
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
		snap.Connections[name] = contract.GatewayConnection{Provider: astr(c["provider"]), Tools: strs(c["tools"]), Intake: astr(c["intake"]), Keys: keys, Blocked: bl}
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
			ProjectId: astr(t["projectId"]), ExperimentId: astr(t["experimentId"]), Connections: strs(t["connections"])})
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
var publishPending sync.Mutex
var publishQueued bool

func publish(q *Request) {
	if q.tx == nil {
		schedulePublish()
		return
	}
	onEnd(q.tx, func(committed bool) {
		if committed {
			schedulePublish()
		}
	})
}

func schedulePublish() {
	publishPending.Lock()
	defer publishPending.Unlock()
	if publishQueued || runningApp == nil {
		return
	}
	publishQueued = true
	go func() {
		publishPending.Lock()
		publishQueued = false
		publishPending.Unlock()
		if err := writeSnapshot(context.Background(), runningApp); err != nil {
			log.Printf("snapshot: %v", err)
		}
	}()
}

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
	gatedOp("connections_list", true, shaped[contract.ConnectionsListRequest, contract.ConnectionsListReply](func(q *Request) (any, error) {
		keys, err := all(q, "connection-key", nil)
		if err != nil {
			return nil, err
		}
		sort.SliceStable(keys, func(i, j int) bool {
			a, _ := numOK(keys[i]["addedAt"])
			b, _ := numOK(keys[j]["addedAt"])
			return a < b
		})
		hist, err := rows(q, "SELECT data FROM document WHERE kind='setting-change' AND data->>'target' LIKE 'connection:%' ORDER BY seq")
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
				if h["target"] == "connection:"+astr(c["name"]) {
					hs = append(hs, h)
				}
			}
			c["keys"], c["spend"], c["history"] = ks, doc{"basis": "unavailable"}, hs
			out = append(out, c)
		}
		return doc{"providers": providers, "connections": out}, nil
	}))
	gatedOp("connections_create", false, checkFirst(func(q *Request) error {
		if !keysOnly(q.obj(), "name", "provider", "holder", "comment") {
			return fail(400, "Expected name and a known provider")
		}
		return nil
	}, shaped[contract.ConnectionsCreateRequest, contract.ConnectionView](func(q *Request) (any, error) {
		b := body(q)
		name, provider := astr(b["name"]), astr(b["provider"])
		if !connName.MatchString(name) || providers[provider] == nil || !keysOnly(b, "name", "provider", "holder", "comment") {
			return nil, fail(400, "Expected name and a known provider")
		}
		if old, err := maybe(q, "connection:"+name, "api-connection"); err != nil {
			return nil, err
		} else if old != nil {
			return nil, fail(409, "API key name exists")
		}
		holder := directorOf(q)
		if h, ok := b["holder"].(string); ok {
			holder = strings.ToLower(h)
		}
		if q.person != nil && !fullAccess(q.person) && holder != q.person.Email {
			return nil, fail(403, "Only the owner creates an API key for another person")
		}
		comment := astr(b["comment"])
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
	})))
	// Owner only. Writes the value file and the row; answers with the fingerprint only.
	gatedOp("connections_key_add", false, checkFirst(connOwner, shaped[contract.ConnectionsKeyAddRequest, contract.ConnectionsKeyAddReply](func(q *Request) (any, error) {
		if err := connOwner(q); err != nil {
			return nil, err
		}
		b := body(q)
		c, err := connByName(q, b["connection"])
		if err != nil {
			return nil, err
		}
		value, ok := b["value"].(string)
		if !ok || len(value) < 8 || len(value) > 4096 || !keyValue.MatchString(value) {
			return nil, fail(400, "Expected one key value without spaces")
		}
		qid := astr(b["questionId"])
		if qid != "" {
			if _, err := keyRequestOf(q, qid, astr(c["name"])); err != nil {
				return nil, err
			}
		}
		fp := fingerprint(value)
		if old, err := maybe(q, "key:"+fp, "connection-key"); err != nil {
			return nil, err
		} else if old != nil {
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
	})))
	// An agent (or a person) asks the owner for a key value (docs/designs/fleet-connections.md §1, key requests): a
	// secret question the owner answers through connections_key_add with questionId. No argument carries a value.
	projectOp("connections_key_request", false, shaped[contract.ConnectionsKeyRequestRequest, contract.ConnectionsKeyRequestReply](func(q *Request) (any, error) {
		b := body(q)
		for k := range b {
			if !inList([]string{"projectId", "requestId", "connection", "reason"}, k) {
				return nil, fail(400, "Unexpected argument "+k+"; a key value goes only into the owner's field")
			}
		}
		if _, err := projectContext(q, astr(b["projectId"])); err != nil {
			return nil, err
		}
		c, err := connByName(q, b["connection"])
		if err != nil {
			return nil, err
		}
		rid, reason := astr(b["requestId"]), astr(b["reason"])
		if rid == "" || len(rid) > 200 || len([]rune(reason)) > 500 {
			return nil, fail(400, "Expected requestId and a reason of at most 500 characters")
		}
		text := fmt.Sprintf("Add a %s key for connection %s", c["provider"], c["name"])
		if reason != "" {
			text += ": " + reason
		}
		d, err := first(q, "SELECT "+projectQuestionJSON+" AS v FROM question WHERE "+projectQuestion+" AND project_id=$1 AND request_id=$2", b["projectId"], rid)
		if err != nil {
			return nil, err
		}
		if d != nil {
			if d = mapOf(d["v"]); astr(d["text"]) != text {
				return nil, fail(409, "Question identity conflict")
			}
			return d, nil
		}
		v := doc{"id": newID(), "projectId": b["projectId"], "requestId": rid, "text": text, "state": "waiting", "createdAt": nowMs(), "actor": actor(q, "operator")}
		ask := doc{"id": v["id"], "kind": "secret", "text": text, "projectId": b["projectId"], "askedBy": v["actor"], "requestId": rid, "askedAt": v["createdAt"],
			"options": doc{"connection": c["name"]}}
		if strings.HasPrefix(q.agent, "execution:") { // a host agent: the question shows in its conversation
			ask["executionId"] = strings.TrimPrefix(q.agent, "execution:")
		}
		_, err = askQuestion(q, ask)
		return v, err
	}))
	// Rotation (§2): active <-> draining, any -> retired (the value file is deleted; the row stays); disabled -> active is the owner's.
	gatedOp("connections_key_state", false, shaped[contract.ConnectionsKeyStateRequest, contract.ConnectionsKeyStateReply](func(q *Request) (any, error) {
		b := body(q)
		id := astr(b["keyId"])
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
		if err := connHolderOrOwner(q, c); err != nil {
			return nil, err
		}
		to := astr(b["state"])
		if to != "active" && to != "draining" && to != "retired" {
			return nil, fail(400, "state: active, draining or retired")
		}
		if k["state"] == "retired" {
			return nil, fail(409, "A retired key stays retired")
		}
		if k["state"] == "disabled" && to != "retired" {
			if err := connOwner(q); err != nil {
				return nil, err
			}
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
	}))

	gatedOp("connections_set_tools", false, checkFirst(connOwner, shaped[contract.ConnectionsSetToolsRequest, contract.ConnectionView](func(q *Request) (any, error) {
		b := body(q)
		if !keysOnly(b, "connection", "tools") {
			return nil, fail(400, "Expected connection and tools")
		}
		if err := exec(q, "SELECT 1 FROM document WHERE id=$1 AND kind='api-connection' FOR UPDATE", "connection:"+astr(b["connection"])); err != nil {
			return nil, err
		}
		c, err := connByName(q, b["connection"])
		if err != nil {
			return nil, err
		}
		tools, ok := b["tools"].([]any)
		if !ok {
			return nil, fail(400, "tools must be an explicit array")
		}
		seen := map[string]bool{}
		for _, value := range tools {
			name, ok := value.(string)
			if !ok || name == "" || seen[name] || providerTools(astr(c["provider"]))[name] == nil {
				return nil, fail(400, "tools must be unique recognized provider tool names")
			}
			seen[name] = true
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
	})))
	gatedOp("connections_set_access", false, shaped[contract.ConnectionsSetAccessRequest, contract.ConnectionView](func(q *Request) (any, error) {
		b := body(q)
		c, err := connByName(q, b["connection"])
		if err != nil {
			return nil, err
		}
		if err := connHolderOrOwner(q, c); err != nil {
			return nil, err
		}
		a := mapOf(b["access"])
		if a == nil {
			a = doc{}
		}
		if !keysOnly(a, "people", "projects", "experiments") {
			return nil, fail(400, "access: {people, projects, experiments}")
		}
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
	}))
	gatedOp("connections_set_intake", false, shaped[contract.ConnectionsSetIntakeRequest, contract.ConnectionView](func(q *Request) (any, error) {
		b := body(q)
		c, err := connByName(q, b["connection"])
		if err != nil {
			return nil, err
		}
		if err := connHolderOrOwner(q, c); err != nil {
			return nil, err
		}
		in := astr(b["intake"])
		if in != "all" && in != "paused" {
			return nil, fail(400, "intake: all or paused")
		}
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
	}))
	// Owner connection tokens (§4): a long-lived bearer for the owner's interactive sessions; the value is answered once.
	gatedOp("connections_token_create", false, checkFirst(connOwner, shaped[contract.ConnectionsTokenCreateRequest, contract.ConnectionsTokenCreateReply](func(q *Request) (any, error) {
		// Full access (owner, grant "*" such as site-agents) mints during development; directedBy records the real actor.
		if err := connOwner(q); err != nil {
			return nil, err
		}
		b := body(q)
		name, ok := b["name"].(string)
		if !ok || !connName.MatchString(name) {
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
		requested := []string{}
		for _, x := range anys(b["connections"]) {
			if s, ok := x.(string); ok {
				requested = append(requested, s)
			}
		}
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
	})))
	gatedOp("connections_token_list", true, shaped[contract.ConnectionsTokenListRequest, contract.ConnectionsTokenListReply](func(q *Request) (any, error) {
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
	}))
	gatedOp("connections_token_revoke", false, checkFirst(connOwner, shaped[contract.ConnectionsTokenRevokeRequest, contract.ConnectionsTokenRevokeReply](func(q *Request) (any, error) {
		if err := connOwner(q); err != nil {
			return nil, err
		}
		b := body(q)
		toks, err := all(q, "run-token", nil)
		if err != nil {
			return nil, err
		}
		n := 0
		for _, t := range toks {
			if t["personal"] == b["name"] && b["name"] != nil && t["revokedAt"] == nil {
				t["revokedAt"] = nowMs()
				if err := put(q, "run-token", t); err != nil {
					return nil, err
				}
				n++
			}
		}
		if n == 0 {
			return nil, fail(404, "Unknown token")
		}
		publish(q)
		return doc{"name": b["name"], "revoked": true}, nil
	})))

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
			err := runTx(ctx, a, nil, true, "", connTick)
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

// every runs fn on a ticker until ctx ends, each run as the pass span pass.<name> in fn's ctx; single flight (a slow
// pass never overlaps the next); d <= 0 turns it off.
func every(ctx context.Context, name string, d time.Duration, fn func(context.Context) error) {
	if d <= 0 {
		return
	}
	passIntervals.Store(name, d)
	go func() {
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				obs.Pass(ctx, name, func(ctx context.Context) (int, error) { return 0, fn(ctx) })
			}
		}
	}()
}

// Per-run tokens (§4): granted are the requested connections that exist, take intake and whose access names the run.
func connAllowed(c doc, run doc) bool {
	a := mapOf(c["access"])
	has := func(list string, v any) bool {
		for _, x := range anys(a[list]) {
			if x == v {
				return true
			}
		}
		return false
	}
	if len(anys(a["people"])) == 0 && len(anys(a["projects"])) == 0 && len(anys(a["experiments"])) == 0 {
		return run["directedBy"] != nil && run["directedBy"] == c["holder"]
	}
	return (run["directedBy"] != nil && has("people", run["directedBy"])) || has("projects", "*") ||
		(run["projectId"] != nil && has("projects", run["projectId"])) || (run["experimentId"] != nil && has("experiments", run["experimentId"]))
}

// mint: {token, refused, connections}; token is nil when nothing was granted.
func mint(q *Request, run doc, requested []string) (doc, error) {
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
		if c != nil && c["intake"] == "all" && connAllowed(c, run) {
			granted = append(granted, c)
		}
	}
	if len(granted) == 0 {
		return doc{"token": nil, "connections": []any{}, "refused": uniq}, nil
	}
	token := randomToken()
	sha := sha256hex([]byte(token))
	names, specs, refused := []any{}, []any{}, []string{}
	for _, c := range granted {
		names = append(names, c["name"])
		p := mapOf(providers[astr(c["provider"])])
		tools := []any{}
		for _, t := range anys(c["tools"]) {
			if spec := mapOf(mapOf(p["tools"])[astr(t)]); spec != nil {
				tools = append(tools, doc{"name": t, "description": spec["description"], "inputSchema": spec["inputSchema"]})
			}
		}
		specs = append(specs, doc{"name": c["name"], "instructions": p["instructions"], "tools": tools})
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
	row := doc{"id": "run-token:" + sha, "sha256": sha, "executionId": run["executionId"], "directedBy": run["directedBy"], "projectId": run["projectId"],
		"experimentId": run["experimentId"], "pipelineRunId": run["pipelineRunId"], "connections": names, "createdAt": nowMs()}
	if run["step"] == true {
		row["step"] = true
	}
	if run["personal"] != nil {
		row["personal"] = run["personal"]
	}
	err := put(q, "run-token", row)
	if err != nil {
		return nil, err
	}
	publish(q)
	return doc{"token": token, "refused": refused, "connections": specs}, nil
}

func revokeTokens(q *Request, executionID string) error {
	ts, err := rows(q, "SELECT data FROM document WHERE kind='run-token' AND data->>'executionId'=$1 AND NOT data ? 'revokedAt'", executionID)
	if err != nil {
		return err
	}
	for _, t := range ts {
		t["revokedAt"] = nowMs()
		if err := exec(q, "UPDATE document SET data=$2 WHERE id=$1", t["id"], jsonBytes(t)); err != nil {
			return err
		}
	}
	if len(ts) > 0 {
		publish(q)
	}
	return nil
}

// At start no api step call of an earlier process is in flight: their tokens are revoked.
func revokeSteps(q *Request) error {
	ts, err := rows(q, "SELECT data FROM document WHERE kind='run-token' AND data->>'step'='true' AND NOT data ? 'revokedAt'")
	if err != nil {
		return err
	}
	for _, t := range ts {
		if c, _ := numOK(t["createdAt"]); int64(c) < processStarted {
			t["revokedAt"] = nowMs()
			if err := exec(q, "UPDATE document SET data=$2 WHERE id=$1", t["id"], jsonBytes(t)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Spool ingest (§3): every gateway line becomes one tool_run with connection_cost, idempotent by callId. A past day's
// file is deleted once its lines are committed; a file with a line that could not be imported is kept. A line naming a disabled key disables its row and asks the owner once.
func connIngest(q *Request) (int, error) {
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
			ok, err := connRecord(q, l)
			if err != nil {
				return added, err
			}
			if ok {
				added++
			}
		}
		if added > 0 {
			publish(q)
		}
		if past && unimported > 0 {
			log.Printf("spool %s: past day kept, %d line(s) not imported", f, unimported)
		} else if past {
			// Retired only once the imported lines are committed; a re-read after a lost commit is deduplicated by callId.
			path := filepath.Join(dir, f)
			onEnd(q.tx, func(committed bool) {
				if committed {
					os.Remove(path)
				}
			})
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
	inserted, err := insertExecution(q, doc{"id": id, "kind": "tool_run", "state": state, "projectId": t["projectId"], "parentExecutionId": parent, "experimentId": t["experimentId"], "data": data})
	if err != nil || !inserted {
		return false, err
	}
	if l.Outcome != contract.CallOutcomeOk {
		if err := exec(q, "UPDATE execution SET problem=$2 WHERE id=$1 AND problem IS NULL", id, problemOf(string(l.Outcome), deref(l.ErrorCode))); err != nil {
			return false, err
		}
	}
	var cost any
	if u := l.Usage; u != nil {
		amount := u.Amount
		if l.UsageKey != nil {
			// A finished job's charge (Meshy consumed_credits) counts once per (connection, job); later polls cost 0. A
			// pipeline step's job is counted by recordJob instead.
			// Concurrent ingests of two polls of one job serialize on the key.
			if err := exec(q, "SELECT pg_advisory_xact_lock(hashtext($1))", "usageKey:"+*l.UsageKey); err != nil {
				return false, err
			}
			var n int
			if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM execution WHERE kind='tool_run' AND data->>'usageKey'=$1 AND id<>$2", *l.UsageKey, id).Scan(&n); err != nil {
				return false, err
			}
			if n > 0 || t["step"] == true {
				amount = 0
			} else if err := exec(q, "UPDATE execution SET data=jsonb_set(data,'{usageKey}',to_jsonb($2::text)) WHERE id=$1", id, *l.UsageKey); err != nil {
				return false, err
			}
		}
		cost = doc{"unit": u.Unit, u.Unit: amount, "amount": amount, "connection": l.Connection}
	}
	if err := writeConnectionCost(q, id, t["projectId"], cost); err != nil {
		return false, err
	}
	if d := l.Disabled; d != nil && d.Key != "" {
		if err := connDisable(q, d.Key, d.Status); err != nil {
			return false, err
		}
	}
	if p := l.KeyProblem; p != nil && p.Key != "" {
		k, err := maybe(q, "key:"+p.Key, "connection-key")
		if err != nil {
			return false, err
		}
		if k != nil {
			msg := p.Message
			if len(msg) > 500 {
				msg = msg[:500]
			}
			at := l.StartedAt
			if at == 0 {
				at = nowMs()
			}
			k["problem"] = doc{"status": p.Status, "message": msg, "at": at}
			if err := put(q, "connection-key", k); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

func connDisable(q *Request, fp string, status int64) error {
	k, err := maybe(q, "key:"+fp, "connection-key")
	if err != nil || k == nil || k["state"] == "retired" || k["state"] == "disabled" {
		return err
	}
	st := fmt.Sprint(status)
	k["state"], k["disabledReason"], k["disabledAt"] = "disabled", "provider answered HTTP "+st, nowMs()
	if err := put(q, "connection-key", k); err != nil {
		return err
	}
	publish(q)
	cause := "key:" + fp + ":disabled"
	var n int
	// Fix: TS stored the cause only in data, so the dedupe never matched; the cause column (and its open-unique index) holds it now.
	if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM question WHERE cause=$1 AND state<>'answered'", cause).Scan(&n); err != nil || n > 0 {
		return err
	}
	id, err := askRow(q, "operator_decision", fmt.Sprintf("API key %s of %v was refused by the provider (HTTP %s) and is disabled. Re-enable it on the API keys screen once it works again, or retire it.", fp, k["connectionId"], st),
		nil, []any{"seen"}, "fleet", doc{"cause": cause})
	if err != nil {
		return err
	}
	return exec(q, "UPDATE question SET cause=$2 WHERE id=$1", id, cause)
}

var lastBlocked string

// One pass: ingest, republish when the blocked set changed, revoke tokens of ended or unknown runs.
func connTick(q *Request) error {
	if _, err := connIngest(q); err != nil {
		return err
	}
	b, err := blockedConnections(q)
	if err != nil {
		return err
	}
	s := string(jsonBytes(sortedKeysOf(func() map[string]any {
		m := map[string]any{}
		for k, v := range b {
			if v {
				m[k] = true
			}
		}
		return m
	}())))
	if s != lastBlocked {
		onEnd(q.tx, func(committed bool) { // a rolled-back pass leaves lastBlocked so the next pass republishes
			if committed {
				lastBlocked = s
				schedulePublish()
			}
		})
	}
	open, err := rows(q, "SELECT data FROM document WHERE kind='run-token' AND NOT data ? 'revokedAt' AND NOT data ? 'step' AND NOT data ? 'personal'")
	if err != nil {
		return err
	}
	for _, t := range open {
		id := astr(t["executionId"])
		var state *string
		err := q.tx.QueryRow(q.ctx(), "SELECT coalesce((SELECT state::text FROM execution WHERE id=$1 AND "+execKinds+"),'')", id).Scan(&state)
		if err != nil {
			return err
		}
		if state == nil || *state == "" || terminalTyped[*state] {
			if err := revokeTokens(q, id); err != nil {
				return err
			}
		}
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
				var recorded float64
				if err := q.tx.QueryRow(q.ctx(), `SELECT coalesce(sum((t.connection_cost->>'amount')::numeric),0)::float8 FROM execution e JOIN execution_totals t ON t.execution_id=e.id
				 WHERE e.kind='tool_run' AND e.data->>'keyFingerprint'=$1 AND (e.data->>'startedAt')::bigint>=$2`, k["fingerprint"], int64(at)).Scan(&recorded); err != nil {
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

// gatewayCall: one in-process call through fleet-gateway (contract.GatewayCall).
func gatewayCall(ctx context.Context, token, connection, tool string, args any) contract.CallLine {
	var header map[string]string
	if strings.HasPrefix(gatewayURL(), "https://") {
		header = cfAccessHeaders()
	}
	return contract.GatewayCall(ctx, gatewayURL(), token, connection, tool, jsonBytes(args), header)
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

// keyRequestOf: the open key request qid for connection.
func keyRequestOf(q *Request, qid, connection string) (doc, error) {
	qn, err := first(q, "SELECT * FROM question WHERE id=$1 AND kind='secret'", qid)
	if err != nil {
		return nil, err
	}
	if qn == nil {
		return nil, fail(404, "Unknown key request")
	}
	if astr(qn["state"]) != "open" {
		return nil, fail(409, "Key request already settled")
	}
	if astr(mapOf(qn["options"])["connection"]) != connection {
		return nil, fail(400, "The key request names another connection")
	}
	return qn, nil
}

// keyAnswerCheck: the answer hook of a key request (tasks area calls it). Only the owner settles it, with decline or
// with the fingerprint answer of a key added to its connection, so no answer path carries a value.
func keyAnswerCheck(q *Request, qn doc, answer string) error {
	if qn["kind"] != "secret" {
		return nil
	}
	if q.person != nil && !fullAccess(q.person) {
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
	if k == nil || k["connectionId"] != conn {
		return fail(400, "Answer decline, or add the key in its field")
	}
	return nil
}

func init() {
	agentDescriptions["connections_key_request"] = "Ask the owner to add an API key: projectId, requestId, connection, reason. Never pass a key value; the owner pastes it into Fleet and the answer (question_history) is only {connection, fingerprint, state} or decline"
}
