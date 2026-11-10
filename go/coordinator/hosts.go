package main

// hosts and the host operations (docs/designs/fleet-people-access.md). A host row is the 'host' document
// 'host:<id>' {hostId, name, holder, platform, roles, secretHash, intake, harnesses, createdAt}; only the secret's hash is kept.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

const hostStaleMs = 30000

var (
	hostID    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)
	hexHash   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hostRoles = regexp.MustCompile(`^(publication|inspector-for:[a-z][a-z0-9-]{0,79})$`)
)

func init() {
	operation("hosts_register", false, hostsRegister)
	operation("hosts_update", false, hostsUpdate)
	operation("hosts_rotate_secret", false, hostsRotateSecret)
	operation("hosts_list", true, hostsList)
	operation("route_decisions", true, routeDecisions)
}

// hostList: every host with its bare id.
func hostList(q *Request) ([]doc, error) {
	rows, err := all(q, "host", nil)
	for _, h := range rows {
		h["id"] = h["hostId"]
		delete(h, "hostId")
	}
	return rows, err
}

func hostByID(q *Request, id any) (doc, error) {
	hs, err := hostList(q)
	for _, h := range hs {
		if h["id"] == id {
			return h, err
		}
	}
	return nil, err
}

func saveHost(q *Request, h doc) error {
	row := doc{}
	for k, v := range h {
		row[k] = v
	}
	row["id"], row["hostId"] = "host:"+astr(h["id"]), h["id"]
	return put(q, "host", row)
}

func hostView(h doc) doc {
	v := doc{}
	for k, x := range h {
		if k != "secretHash" {
			v[k] = x
		}
	}
	return v
}

func hostIDs(q *Request) ([]string, error) {
	hs, err := hostList(q)
	out := []string{}
	for _, h := range hs {
		out = append(out, astr(h["id"]))
	}
	return out, err
}

// executionHostIDs: hosts that run agent work (no program-worker role).
func executionHostIDs(q *Request) ([]string, error) {
	hs, err := hostList(q)
	out := []string{}
	for _, h := range hs {
		if len(anys(h["roles"])) == 0 {
			out = append(out, astr(h["id"]))
		}
	}
	return out, err
}

func hostWithRole(q *Request, role string) (string, error) {
	hs, err := hostList(q)
	for _, h := range hs {
		if inList(strs(h["roles"]), role) {
			return astr(h["id"]), err
		}
	}
	return "", err
}

// hostMaySee: a host held by the owner sees every project; another holder's host only the projects of the holder's grants.
func (a *App) hostMaySee(h doc, projectID any) bool {
	p := a.people[astr(h["holder"])]
	if p == nil {
		return false
	}
	if p.Owner {
		return true
	}
	id, _ := projectID.(string)
	for _, g := range p.Grants {
		if g.Kind == "project" && g.ID == id && id != "" {
			return true
		}
	}
	return false
}

func mintSecret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// notifyHolder: the owner changed a setting of a row someone else holds; tell the holder with a question addressed to them.
func notifyHolder(q *Request, holder, text string) error {
	if q.person == nil || !q.person.Owner || q.person.Email == holder {
		return nil
	}
	id, err := askQuestion(q, doc{"kind": "operator_decision", "text": text, "options": []any{"ok"}, "askedBy": q.person.Email, "data": doc{"notice": "owner-override"}})
	if err != nil {
		return err
	}
	return exec(q, "UPDATE question SET addressed_to=$2 WHERE id=$1", id, holder)
}

func validHarnesses(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for _, x := range m {
		h, _ := x.(map[string]any)
		if _, ok := h["allowed"].(bool); !ok {
			return false
		}
	}
	return true
}

func (q *Request) obj() doc {
	if q.body == nil {
		return doc{}
	}
	var b doc
	if decode(q.body, &b) != nil || b == nil {
		return doc{}
	}
	return b
}

func ownerOnly(q *Request, msg string) error {
	if q.person == nil || !q.person.Owner {
		return fail(403, msg)
	}
	return nil
}

// secretOf: the hash to store, and the minted secret when the host did not bring its own hash.
func secretOf(q *Request, b doc, self string) (hash, secret string, err error) {
	if v, ok := b["secretSha256"]; ok && v != nil {
		s, _ := v.(string)
		if !hexHash.MatchString(s) {
			return "", "", fail(400, "secretSha256 must be 64 hex characters")
		}
		hash = s
	} else {
		secret = mintSecret()
		hash = secretHash(secret)
	}
	if hash == secretHash(q.app.browserSecret) {
		return "", "", fail(400, "A host secret must differ from the browser secret")
	}
	hs, err := hostList(q)
	for _, h := range hs {
		if h["id"] != self && h["secretHash"] == hash {
			return "", "", fail(409, "Secret already used by another host")
		}
	}
	return hash, secret, err
}

func hostsRegister(q *Request) (any, error) {
	if err := ownerOnly(q, "Only the owner manages hosts"); err != nil {
		return nil, err
	}
	b := q.obj()
	id := astr(b["id"])
	if !hostID.MatchString(id) {
		return nil, fail(400, "Expected host id")
	}
	if h, err := hostByID(q, id); err != nil || h != nil {
		if err == nil {
			err = fail(409, "Host exists")
		}
		return nil, err
	}
	holder := q.person.Email
	if s, ok := b["holder"].(string); ok {
		holder = strings.ToLower(s)
	}
	if q.app.people[holder] == nil {
		return nil, fail(400, "Holder is not a person")
	}
	if v, ok := b["secretSha256"]; ok && v != nil && !hexHash.MatchString(astr(v)) {
		return nil, fail(400, "secretSha256 must be 64 hex characters")
	}
	roles := []any{}
	if b["roles"] != nil {
		rs, ok := b["roles"].([]any)
		for _, r := range rs {
			if !hostRoles.MatchString(astr(r)) {
				ok = false
			}
		}
		if !ok {
			return nil, fail(400, "roles: publication or inspector-for:<hostId>")
		}
		roles = rs
	}
	var harnesses any = doc{}
	if b["harnesses"] != nil {
		if !validHarnesses(b["harnesses"]) {
			return nil, fail(400, "harnesses: {<name>: {allowed}}")
		}
		harnesses = b["harnesses"]
	}
	hash, secret, err := secretOf(q, b, "")
	if err != nil {
		return nil, err
	}
	name, platform := id, "unknown"
	if s, ok := b["name"].(string); ok {
		name = s
	}
	if s, ok := b["platform"].(string); ok {
		platform = s
	}
	h := doc{"id": id, "name": name, "holder": holder, "platform": platform, "roles": roles, "secretHash": hash, "intake": "all", "harnesses": harnesses, "createdAt": nowMs()}
	if err := saveHost(q, h); err != nil {
		return nil, err
	}
	out := doc{"host": hostView(h)}
	if secret != "" {
		out["secret"] = secret
	}
	return out, nil
}

func hostsUpdate(q *Request) (any, error) {
	b := q.obj()
	h, err := hostByID(q, b["id"])
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, fail(404, "Unknown host")
	}
	if q.person == nil || (!q.person.Owner && q.person.Email != h["holder"]) {
		return nil, fail(403, "Only the holder or the owner changes this host")
	}
	next := doc{}
	if v, ok := b["intake"]; ok && v != nil {
		if v != "all" && v != "paused" {
			return nil, fail(400, "intake: all or paused")
		}
		next["intake"] = v
	}
	if v, ok := b["autoUpdate"]; ok && v != nil {
		if !validAutoUpdate(v) {
			return nil, fail(400, "autoUpdate: on, off or an RFC 3339 time ending in Z")
		}
		next["autoUpdate"] = v
	}
	if v, ok := b["harnesses"]; ok && v != nil {
		if !validHarnesses(v) {
			return nil, fail(400, "harnesses: {<name>: {allowed}}")
		}
		m := doc{}
		if old, ok := h["harnesses"].(map[string]any); ok {
			for k, x := range old {
				m[k] = x
			}
		}
		for k, x := range v.(map[string]any) {
			m[k] = x
		}
		next["harnesses"] = m
	}
	changed := []string{}
	for _, f := range []string{"intake", "autoUpdate", "harnesses"} {
		v, ok := next[f]
		if !ok || js(h[f]) == js(v) {
			continue
		}
		changed = append(changed, f)
		if err := put(q, "setting-change", doc{"id": newID(), "target": "host:" + astr(h["id"]), "holder": h["holder"], "field": f, "previous": h[f], "value": v, "actor": q.person.Email, "at": nowMs()}); err != nil {
			return nil, err
		}
	}
	if len(changed) > 0 {
		if err := notifyHolder(q, astr(h["holder"]), fmt.Sprintf("The owner changed %s of your host %s.", strings.Join(changed, ", "), h["id"])); err != nil {
			return nil, err
		}
	}
	for k, v := range next {
		h[k] = v
	}
	if err := saveHost(q, h); err != nil {
		return nil, err
	}
	return doc{"host": hostView(h)}, nil
}

func hostsRotateSecret(q *Request) (any, error) {
	if err := ownerOnly(q, "Only the owner manages hosts"); err != nil {
		return nil, err
	}
	b := q.obj()
	h, err := hostByID(q, b["id"])
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, fail(404, "Unknown host")
	}
	hash, secret, err := secretOf(q, b, astr(h["id"]))
	if err != nil {
		return nil, err
	}
	h["secretHash"] = hash
	if err := saveHost(q, h); err != nil {
		return nil, err
	}
	out := doc{"host": hostView(h)}
	if secret != "" {
		out["secret"] = secret
	}
	return out, nil
}

func hostsList(q *Request) (any, error) {
	configured, err := hostIDs(q)
	if err != nil {
		return nil, err
	}
	observed, err := all(q, "host-observation", nil)
	if err != nil {
		return nil, err
	}
	ids := append([]string{}, configured...)
	for _, o := range observed {
		if !inList(ids, astr(o["id"])) {
			ids = append(ids, astr(o["id"]))
		}
	}
	running, err := queryRows(q, "SELECT host_id,harness,count(*)::int AS n FROM execution WHERE state IN ('preparing','running') AND host_id IS NOT NULL GROUP BY 1,2")
	if err != nil {
		return nil, err
	}
	queued, err := queryRows(q, "SELECT data->>'hostId' AS host_id,count(*)::int AS n FROM document WHERE kind='command' AND data->>'status'='pending' GROUP BY 1")
	if err != nil {
		return nil, err
	}
	list, err := hostList(q)
	if err != nil {
		return nil, err
	}
	records := map[any]doc{}
	for _, r := range list {
		records[r["id"]] = r
	}
	now := nowMs()
	out := []doc{}
	for _, id := range ids {
		var o doc
		for _, x := range observed {
			if x["id"] == id {
				o = x
			}
		}
		by := doc{}
		total := 0
		for _, r := range running {
			if r["host_id"] == id {
				h := astr(r["harness"])
				if h == "" {
					h = "unknown"
				}
				n := int(r["n"].(int32))
				by[h], total = n, total+n
			}
		}
		n := 0
		for _, r := range queued {
			if r["host_id"] == id {
				n = int(r["n"].(int32))
			}
		}
		h := doc{"id": id, "configured": inList(configured, id), "platform": nil, "lastSeenAt": nil, "capabilities": []any{}, "problems": []any{},
			"runningByHarness": by, "running": total, "slots": nil, "queued": n, "stores": "unavailable",
			"version": nil, "autoUpdate": "on", "update": nil}
		if r := records[id]; r != nil {
			h["autoUpdate"] = autoUpdateOf(r)
		}
		if id == pds18Host() {
			h["update"] = mergeUpdate(nil)
		}
		if o != nil {
			h["platform"], h["lastSeenAt"], h["version"] = o["platform"], o["lastSeenAt"], o["version"]
			if id == pds18Host() {
				h["update"] = mergeUpdate(o["update"])
			} else {
				h["update"] = o["update"]
			}
			if o["capabilities"] != nil {
				h["capabilities"] = o["capabilities"]
			}
			// A host that stopped polling shows no problems: its staleness is the signal.
			if seen, ok := o["lastSeenAt"].(float64); ok && float64(now)-seen < hostStaleMs && o["problems"] != nil {
				h["problems"] = o["problems"]
			}
		}
		out = append(out, h)
	}
	return doc{"staleAfterMs": hostStaleMs, "serverNow": now, "hosts": out}, nil
}

func routeDecisions(q *Request) (any, error) {
	id, ok := q.obj()["executionId"].(string)
	if !ok || id == "" || len(id) > 200 {
		return nil, fail(400, "Expected executionId")
	}
	rs, err := queryRows(q, "SELECT execution_id,at,host_id,candidates,winner FROM route_decision WHERE execution_id=$1 ORDER BY at", id)
	out := []doc{}
	for _, r := range rs {
		out = append(out, doc{"executionId": r["execution_id"], "at": r["at"], "hostId": r["host_id"], "candidates": r["candidates"], "winner": r["winner"]})
	}
	return doc{"decisions": out}, err
}
