package main

import (
	"encoding/json"
	"fleet/contract"
	"fmt"
	"github.com/jackc/pgx/v5"
	"maps"
	"slices"
	"strings"
)

func projectWrite(q *Request, create bool) (any, error) {
	b := body(q)
	id := astr(b["projectId"])
	op := "project_update"
	if create {
		id = astr(b["id"])
		op = "project_create"
	}
	if q.person == nil || (!fullAccess(q.person) && !hasRole(q.person, "operator")) {
		return nil, fail(403, "Authenticated operator required")
	}
	if !idPattern.MatchString(id) || strings.TrimSpace(astr(b["requestId"])) == "" {
		return nil, fail(400, "Expected project identity and requestId")
	}
	// GLOBAL also serializes people projection replacement; authenticate again in this transaction.
	var roles []string
	var owner bool
	if err := q.tx.QueryRow(q.ctx(), "SELECT roles,owner FROM person WHERE email=$1", q.person.Email).Scan(&roles, &owner); err != nil {
		return nil, fail(403, "Director is unavailable")
	}
	bundle, err := projectBundle(id, mapOf(b["content"]))
	if err != nil {
		return nil, err
	}
	text, err := canonicalText(bundle)
	if err != nil {
		return nil, err
	}
	payload := sha256Text(js(b))
	var prior []byte
	var digest string
	err = q.tx.QueryRow(q.ctx(), "SELECT payload_sha,result FROM project_write_request WHERE directing_email=$1 AND operation=$2 AND request_id=$3", q.person.Email, op, b["requestId"]).Scan(&digest, &prior)
	if err == nil {
		if !mayAccessProjectID(q.person, id) {
			return nil, fail(404, "Unknown project")
		}
		if digest != payload {
			return nil, fail(409, "Request identity conflict")
		}
		var out doc
		err = json.Unmarshal(prior, &out)
		return out, err
	}
	if err != pgx.ErrNoRows {
		return nil, err
	}
	if !create && !mayAccessProjectID(q.person, id) {
		return nil, fail(404, "Unknown project")
	}
	head, err := headOf(q, "project", id, true)
	if err != nil {
		return nil, err
	}
	if head != nil && head.Retired != nil {
		if create {
			return nil, fail(409, "Project id belongs to an archived project")
		}
		return nil, fail(404, "Unknown project")
	}
	if create {
		if head != nil {
			return nil, fail(409, "Project exists")
		}
		// Creation may publish change rows only for its freshly reserved identity.
		if sc := scopeOf(q.person); sc != nil {
			if _, err := q.tx.Exec(q.ctx(), "SELECT set_config('fleet.project_ids',$1,true)", sc.ProjectIDs+","+id); err != nil {
				return nil, err
			}
		}
		if err := exec(q, "INSERT INTO definition(kind,id,path,head,project_id,created_by,initial_member) VALUES('project',$1,$2,0,$1,$3,$3)", id, "config/projects/"+id+"/project.json", q.person.Email); err != nil {
			return nil, err
		}
	} else {
		if head == nil {
			return nil, fail(404, "Unknown project")
		}
		if head.state() == contract.DefinitionStateRepositoryChanged && (head.RepoSha == nil || astr(b["repoSha"]) != *head.RepoSha) {
			return nil, fail(409, "Project source changed; reload and review before saving")
		}
		if int64(num(b["baseRevision"])) != head.Head {
			return nil, fail(409, "Project revision changed")
		}
	}
	// A project edit cannot turn a foreign host into a newly granted resource.
	previous := doc{}
	if head != nil {
		var old doc
		if err := json.Unmarshal(head.Content, &old); err != nil {
			return nil, err
		}
		previous = mapOf(old["settings"])
	}
	for _, raw := range anys(mapOf(bundle["settings"])["environments"]) {
		environment := mapOf(raw)
		unchanged := false
		for _, prior := range anys(previous["environments"]) {
			if canonical(prior) == canonical(raw) {
				unchanged = true
			}
		}
		if unchanged || fullAccess(q.person) {
			continue
		}
		h, err := maybe(q, "host:"+astr(environment["hostId"]), "host")
		if err != nil {
			return nil, err
		}
		if h == nil || h["holder"] != q.person.Email {
			return nil, fail(403, "Host binding is not granted")
		}
	}
	for _, profile := range mapOf(mapOf(bundle["settings"])["roles"]) {
		if _, err := readTrustedProfile(q, astr(profile)); err != nil {
			return nil, fail(400, "Unknown role configuration")
		}
	}
	for _, profile := range anys(mapOf(mapOf(bundle["settings"])["execution"])["profiles"]) {
		if _, err := readTrustedProfile(q, astr(profile)); err != nil {
			return nil, fail(400, "Unknown execution profile")
		}
	}
	base := int64(0)
	var repo *string
	if head != nil {
		base = head.Head
		repo = head.RepoSha
	}
	rev, err := appendRevision(q, "project", id, base, &text, repo, "editor", "Save project")
	if err != nil {
		return nil, err
	}
	if create && q.agent != "" {
		e, err := get(q, strings.TrimPrefix(q.agent, "execution:"), "execution")
		if err != nil {
			return nil, err
		}
		session, err := get(q, e["sessionId"], "session")
		if err != nil {
			return nil, err
		}
		if mapOf(session["focus"])["projectIds"] != "*" {
			ids := focusIDs(session)
			if !contains(ids, id) {
				ids = append(ids, id)
			}
			session["focus"] = doc{"projectIds": ids}
			if err := put(q, "session", session); err != nil {
				return nil, err
			}
		}
	}
	// Saved: an owner or full-access person naming a connection again keeps its key on the hosts. Anyone else
	// cannot cancel the owner's key removal by naming the connection.
	if fullAccess(q.person) {
		if cs, err := headConnections(jsonBytes(bundle)); err != nil {
			return nil, err
		} else if err := withdrawConnectionRemovals(q, cs); err != nil {
			return nil, err
		}
	}
	out := doc{"projectId": id, "revision": rev.Revision, "contentSha": rev.ContentSha, "configured": true}
	if err := exec(q, "INSERT INTO project_write_request VALUES($1,$2,$3,$4,$5,$6)", q.person.Email, op, b["requestId"], payload, id, jsonBytes(out)); err != nil {
		return nil, err
	}
	return out, nil
}

func projectInitialRevoke(q *Request) (any, error) {
	if q.person == nil || !q.person.Owner {
		return nil, fail(403, "Only the owner administers access")
	}
	b := body(q)
	id := astr(b["projectId"])
	var version int64
	var revoked bool
	if err := q.tx.QueryRow(q.ctx(), "SELECT access_version,initial_member_revoked_at IS NOT NULL FROM definition WHERE kind='project' AND id=$1 FOR UPDATE", id).Scan(&version, &revoked); err != nil {
		return nil, fail(404, "Unknown project")
	}
	if !revoked {
		if int64(num(b["baseAccessVersion"])) != version {
			return nil, fail(409, "Access version changed")
		}
		if err := exec(q, "UPDATE definition SET initial_member_revoked_at=now(),access_version=access_version+1 WHERE kind='project' AND id=$1", id); err != nil {
			return nil, err
		}
		if err := put(q, "setting-change", doc{"id": newID(), "projectId": id, "target": "project:" + id, "field": "initialAccess", "previous": "active", "value": "revoked", "actor": actor(q, "operator"), "at": nowMs()}); err != nil {
			return nil, err
		}
		version++
	}
	return doc{"projectId": id, "accessVersion": version, "revoked": true}, nil
}

// projectArchive: retire a project definition (owner decision to drop a project). Its tasks, sessions and executions
// must be closed first (task_cancel, session_archive, execution_stop), so nothing runs under a project no list shows.
// Step rows count through their run: a finished run leaves unreached steps 'preparing', which is not open work.
// The GLOBAL write lock of every operation serializes this check with task_create and session_start; afterwards
// task_create, project_update and project_create refuse the id.
func projectArchive(q *Request) (any, error) {
	if err := ownerOnly(q, "Only the owner archives projects"); err != nil {
		return nil, err
	}
	id := astr(body(q)["projectId"])
	rows, err := queryRows(q, `SELECT kind,count(*) AS n,min(id) AS example FROM (
	  SELECT 'task' AS kind,id FROM task WHERE project_id=$1 AND state::text=ANY($2)
	  UNION ALL SELECT 'session',id FROM document WHERE project_id=$1 AND kind='session' AND NOT data ? 'archivedAt'
	  UNION ALL SELECT 'execution',id FROM execution WHERE project_id=$1 AND kind::text<>'pipeline_step' AND state IN ('preparing','running','waiting')) o GROUP BY kind ORDER BY kind`, id, slices.Collect(maps.Keys(openTask)))
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		open := []string{}
		for _, r := range rows {
			open = append(open, fmt.Sprintf("%v %s (e.g. %s)", r["n"], astr(r["kind"]), astr(r["example"])))
		}
		return nil, fail(409, "Close first: "+strings.Join(open, ", "))
	}
	// The head stays readable after retirement, so archiving again (idempotent) cleans keys a first run left behind.
	var raw []byte
	err = q.tx.QueryRow(q.ctx(), `SELECT r.content FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND r.revision=d.head
	  WHERE d.kind='project' AND d.id=$1`, id).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fail(404, "Unknown project")
	}
	if err != nil {
		return nil, err
	}
	conns, err := headConnections(raw)
	if err != nil {
		return nil, err
	}
	if err := exec(q, "UPDATE definition SET retired_at=coalesce(retired_at,now()) WHERE kind='project' AND id=$1", id); err != nil {
		return nil, err
	}
	removed, err := removeUnusedConnections(q, conns)
	if err != nil {
		return nil, err
	}
	return doc{"projectId": id, "archived": true, "connectionRemovals": removed}, nil
}

// projectRetired: the id belongs to an archived project (its definition row stays, so the id is never reused).
func projectRetired(q *Request, id string) (bool, error) {
	return has(q, "SELECT 1 FROM definition WHERE kind='project' AND id=$1 AND retired_at IS NOT NULL", id)
}

func init() {
	gatedObjOp("project_archive", false, shaped[contract.ProjectArchiveRequest, contract.ProjectArchiveReply](projectArchive))
	gatedObjOp("project_create", false, shaped[contract.ProjectCreateRequest, contract.ProjectCreateReply](func(q *Request) (any, error) { return projectWrite(q, true) }))
	gatedObjOp("project_update", false, shaped[contract.ProjectUpdateRequest, contract.ProjectUpdateReply](func(q *Request) (any, error) { return projectWrite(q, false) }))
	gatedObjOp("project_initial_access_revoke", false, shaped[contract.ProjectInitialAccessRevokeRequest, contract.ProjectInitialAccessRevokeReply](projectInitialRevoke))
}

func projectContext(q *Request, id string) (doc, error) {
	var raw []byte
	var revision int64
	var hash string
	read := func(tx pgx.Tx) error {
		return tx.QueryRow(q.ctx(), `SELECT r.content,d.head,r.content_sha FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND r.revision=d.head WHERE d.kind='project' AND d.id=$1 AND d.retired_at IS NULL`, id).Scan(&raw, &revision, &hash)
	}
	var err error
	if q.tx != nil {
		err = read(q.tx)
	} else {
		err = q.app.store.Tx(q.ctx(), nil, false, "", read)
	}
	if err == pgx.ErrNoRows {
		return nil, fail(404, "Unknown project")
	}
	if err != nil {
		return nil, err
	}
	var b doc
	if err = json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	if _, err = projectBundle(id, b); err != nil {
		return nil, err
	}
	settings := mapOf(b["settings"])
	parsed, unknown, err := projectSettings(settings)
	if err != nil {
		return nil, err
	}
	out := doc{}
	for k, v := range settings {
		out[k] = v
	}
	out["content"] = b
	out["settings"] = parsed
	out["unknownKeys"] = unknown
	ctx := []doc{}
	for _, f := range anys(b["contextFiles"]) {
		m := mapOf(f)
		ctx = append(ctx, doc{"path": m["path"], "text": m["text"], "sha256": sha256Text(js(m["text"]))})
	}
	out["context"] = ctx
	if q.tx != nil {
		head, err := headOf(q, "project", id, false)
		if err != nil {
			return nil, err
		}
		out["repoSha"] = head.RepoSha
		out["sourceState"] = head.state()
	}
	if q.person != nil && q.person.Owner && q.tx != nil {
		var member *string
		var revoked bool
		var accessVersion int64
		if err := q.tx.QueryRow(q.ctx(), "SELECT initial_member,initial_member_revoked_at IS NOT NULL,access_version FROM definition WHERE kind='project' AND id=$1", id).Scan(&member, &revoked, &accessVersion); err != nil {
			return nil, err
		}
		out["initialAccess"] = doc{"member": member, "revoked": revoked, "version": accessVersion}
	}
	out["revision"] = revision
	out["contentSha"] = hash
	return out, nil
}
func projectIDs(q *Request) ([]string, error) {
	rows, err := headTexts(q, "project")
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, r := range rows {
		if q.person == nil || mayAccessProjectID(q.person, r.id) {
			out = append(out, r.id)
		}
	}
	return out, nil
}

// headConnections: the distinct git connections a project head (bundle JSON) names in settings.repos.
func headConnections(raw []byte) ([]string, error) {
	var b struct {
		Settings struct {
			Repos []struct {
				Connection string `json:"connection"`
			} `json:"repos"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	out := []string{}
	for _, r := range b.Settings.Repos {
		if r.Connection != "" && !slices.Contains(out, r.Connection) {
			out = append(out, r.Connection)
		}
	}
	return out, nil
}

// removeUnusedConnections: a git connection no live project names any more leaves every host that holds it
// (connection-remove deletes the deploy key directory). Every live head counts, whoever asks; a head that does not
// parse stops the removal (fail closed). A removal already pending is not queued twice; project_create and
// project_update withdraw pending removals of a connection they name again. Returns "host/connection" queued.
func removeUnusedConnections(q *Request, conns []string) ([]string, error) {
	out := []string{}
	if len(conns) == 0 {
		return out, nil
	}
	heads, err := queryRows(q, `SELECT d.id,r.content FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND r.revision=d.head
	  WHERE d.kind='project' AND d.retired_at IS NULL`)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for _, h := range heads {
		raw, isText := h["content"].(string)
		if !isText {
			raw = js(h["content"])
		}
		cs, err := headConnections([]byte(raw))
		if err != nil {
			return nil, fail(409, "Project "+astr(h["id"])+" does not parse; no deploy key is removed")
		}
		for _, c := range cs {
			used[c] = true
		}
	}
	obs, err := all(q, "host-observation", nil)
	if err != nil {
		return nil, err
	}
	for _, c := range conns {
		if used[c] {
			continue
		}
		for _, o := range obs {
			host := astr(o["id"])
			if !slices.Contains(strs(o["gitConnections"]), c) {
				continue
			}
			if dup, err := has(q, "SELECT 1 FROM document WHERE kind='command' AND data->>'hostId'=$1 AND data->>'kind'='connection-remove' AND data->>'status'='pending' AND data->'payload'->>'connection'=$2", host, c); err != nil || dup {
				if err != nil {
					return nil, err
				}
				continue
			}
			sid, xid := newID(), newID()
			if err := put(q, "session", doc{"id": sid, "createdAt": nowMs(), "actor": "program-worker"}); err != nil {
				return nil, err
			}
			if err := put(q, "execution", doc{"id": xid, "executionId": xid, "sessionId": sid, "hostId": host, "state": "preparing", "createdAt": nowMs(), "actor": "program-worker"}); err != nil {
				return nil, err
			}
			if err := put(q, "command", doc{"id": newID(), "executionId": xid, "sessionId": sid, "hostId": host, "kind": string(contract.CommandKindConnectionRemove), "status": "pending",
				"payload": contract.ConnectionRemovePayload{Connection: c}}); err != nil {
				return nil, err
			}
			out = append(out, host+"/"+c)
		}
	}
	return out, nil
}

// withdrawConnectionRemovals: a project names connections again, so their pending removals must not run; their
// executions end cancelled. ponytail: a host that already fetched the command still runs it (commands stay pending
// until the receipt); the key then has to be put back by hand. A host-side use check would close that.
func withdrawConnectionRemovals(q *Request, conns []string) error {
	if len(conns) == 0 {
		return nil
	}
	cmds, err := queryRows(q, `SELECT data FROM document WHERE kind='command' AND data->>'kind'='connection-remove' AND data->>'status'='pending' AND data->'payload'->>'connection'=ANY($1)`, conns)
	if err != nil {
		return err
	}
	for _, r := range cmds {
		c := mapOf(r["data"])
		c["status"], c["stopDetail"] = "stopped", "connection in use again"
		if err := put(q, "command", c); err != nil {
			return err
		}
		e, err := get(q, c["executionId"], "execution")
		if err != nil {
			return err
		}
		if !terminal(e) {
			e["state"], e["stopDetail"], e["finishedAt"] = "cancelled", "connection in use again", nowMs()
			if err := put(q, "execution", e); err != nil {
				return err
			}
		}
	}
	return nil
}
