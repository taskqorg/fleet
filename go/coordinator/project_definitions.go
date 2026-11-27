package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

// projectWriteArgs: a decoded project_create or project_update; request is recorded under its JSON for idempotent replay.
type projectWriteArgs struct {
	id, requestID string
	content       json.RawMessage
	baseRevision  int64
	repoSha       *string
	request       any
}

// projectWriteResult: a project write's reply, recorded for its replay.
type projectWriteResult struct {
	ProjectID  string `json:"projectId"`
	Revision   int64  `json:"revision"`
	ContentSha string `json:"contentSha"`
	Configured bool   `json:"configured"`
}

// projectSettingRefs: the settings of a project bundle that name hosts and profiles.
type projectSettingRefs struct {
	Environments []json.RawMessage          `json:"environments"`
	Roles        map[string]json.RawMessage `json:"roles"`
	Presets      json.RawMessage            `json:"presets"`
	Execution    struct {
		Profiles []json.RawMessage `json:"profiles"`
	} `json:"execution"`
}

// rawText: a JSON string's value, "" for any other value.
func rawText(raw json.RawMessage) string {
	var s string
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func projectCIRerunSetting(settings json.RawMessage) json.RawMessage {
	return recOf(recOf(settings)["integration"])["ciRerun"]
}

// projectWrite: a project_create or project_update; its reply JSON (a retry replays the recorded one).
func projectWrite(q *Request, create bool, w projectWriteArgs) ([]byte, error) {
	id := w.id
	op := "project_update"
	if create {
		op = "project_create"
	}
	if q.person == nil || (!fullAccess(q.person) && !hasRole(q.person, "operator")) {
		return nil, fail(403, "Authenticated operator required")
	}
	if !idPattern.MatchString(id) || strings.TrimSpace(w.requestID) == "" {
		return nil, fail(400, "Expected project identity and requestId")
	}
	// GLOBAL also serializes people projection replacement; authenticate again in this transaction.
	if _, err := q.db().DefPersonRoles(q.ctx(), q.person.Email); err != nil {
		return nil, fail(403, "Director is unavailable")
	}
	bundle, err := projectBundle(id, w.content)
	if err != nil {
		return nil, err
	}
	text, err := canonicalText(bundle)
	if err != nil {
		return nil, err
	}
	payload := sha256Text(js(body(q))) // the body as sent, keys sorted: a retry matches its stored request
	prior, err := q.db().DefWriteRequest(q.ctx(), db.DefWriteRequestParams{DirectingEmail: q.person.Email, Operation: op, RequestID: w.requestID})
	if err == nil {
		if !mayAccessProjectID(q.person, id) {
			return nil, fail(404, "Unknown project")
		}
		if prior.PayloadSha != payload {
			return nil, fail(409, "Request identity conflict")
		}
		return prior.Result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err := projectHeadText(recOf(bundle["settings"])); err != nil { // new writes only: a retry gets its stored result
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
	if q.agent != "" || !fullAccess(q.person) {
		var previous json.RawMessage
		if head != nil {
			previous = projectCIRerunSetting(recOf(head.Content)["settings"])
		}
		next := projectCIRerunSetting(bundle["settings"])
		if js(jsonValue(previous)) != js(jsonValue(next)) {
			return nil, fail(403, "Only an owner-authorized person may change the CI rerun grant")
		}
	}
	if create {
		if head != nil {
			return nil, fail(409, "Project exists")
		}
		// Creation may publish change rows only for its freshly reserved identity.
		if sc := scopeOf(q.person); sc != nil && sc.ProjectIDs != "*" {
			if err := q.db().DefSetProjectScope(q.ctx(), sc.ProjectIDs+","+id); err != nil {
				return nil, err
			}
		}
		if err := q.db().DefProjectCreate(q.ctx(), db.DefProjectCreateParams{ID: id, Path: "config/projects/" + id + "/project.json", CreatedBy: &q.person.Email}); err != nil {
			return nil, err
		}
	} else {
		if head == nil {
			return nil, fail(404, "Unknown project")
		}
		if head.state() == contract.DefinitionStateRepositoryChanged && (head.RepoSha == nil || deref(w.repoSha) != *head.RepoSha) {
			return nil, fail(409, "Project source changed; reload and review before saving")
		}
		if w.baseRevision != head.Head {
			return nil, fail(409, "Project revision changed")
		}
	}
	var settings projectSettingRefs
	json.Unmarshal(jsonBytes(bundle["settings"]), &settings)
	// A project edit cannot turn a foreign host into a newly granted resource.
	var previous struct {
		Settings struct {
			Environments []json.RawMessage `json:"environments"`
		} `json:"settings"`
	}
	if head != nil {
		if err := json.Unmarshal(head.Content, &previous); err != nil {
			return nil, err
		}
	}
	for _, raw := range settings.Environments {
		now, _ := canonicalText([]byte(raw))
		unchanged := false
		for _, prior := range previous.Settings.Environments {
			if was, _ := canonicalText([]byte(prior)); was == now {
				unchanged = true
			}
		}
		if unchanged || fullAccess(q.person) {
			continue
		}
		var environment struct {
			HostID any `json:"hostId"`
		}
		json.Unmarshal(raw, &environment)
		hostID, _ := environment.HostID.(string)
		var h *hostRecord
		if hostID != "" {
			var err error
			h, err = hostRecordByID(q, hostID)
			if err != nil {
				return nil, err
			}
		}
		if h == nil || h.holder() != q.person.Email {
			return nil, fail(403, "Host binding is not granted")
		}
	}
	for _, profile := range settings.Roles {
		if _, err := readTrustedProfile(q, rawText(profile)); err != nil {
			return nil, fail(400, "Unknown role configuration")
		}
	}
	// A preset starts with its role's profile or its own: both must be trusted profiles, so a saved preset can start.
	var presetsValue any
	json.Unmarshal(settings.Presets, &presetsValue)
	if presetsValue == nil {
		presetsValue = []any{}
	}
	presets, err := parsePresets(presetsValue)
	if err != nil {
		return nil, err
	}
	for _, p := range presets {
		if p.Profile != "" {
			if _, err := readTrustedProfile(q, p.Profile); err != nil {
				return nil, fail(400, "Unknown preset profile "+p.Profile)
			}
		} else if rawText(settings.Roles[p.Role]) == "" {
			return nil, fail(400, "Preset "+p.ID+" names role "+p.Role+", which the project does not map, and no profile")
		}
	}
	for _, profile := range settings.Execution.Profiles {
		if _, err := readTrustedProfile(q, rawText(profile)); err != nil {
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
		e, err := getExecution(q, strings.TrimPrefix(q.agent, "execution:"))
		if err != nil {
			return nil, err
		}
		session, err := sessionByID(q, e.SessionID)
		if err != nil {
			return nil, err
		}
		if session.Focus == nil || string(session.Focus.ProjectIDs) != `"*"` {
			ids := focusIDsOf(session.Focus)
			if !contains(ids, id) {
				ids = append(ids, id)
			}
			session.Focus = &agentFocus{ProjectIDs: jsonBytes(ids)}
			if err := sessionPut(q, &session); err != nil {
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
	out := jsonBytes(projectWriteResult{ProjectID: id, Revision: rev.Revision, ContentSha: rev.ContentSha, Configured: true})
	if err := q.db().DefWriteRequestInsert(q.ctx(), db.DefWriteRequestInsertParams{DirectingEmail: q.person.Email, Operation: op, RequestID: w.requestID,
		PayloadSha: payload, ProjectID: id, Result: out}); err != nil {
		return nil, err
	}
	return out, nil
}

// projectReply: a project write's reply JSON as its open contract reply.
func projectReply(out []byte, err error) (map[string]json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	var r map[string]json.RawMessage
	return r, json.Unmarshal(out, &r)
}

func projectInitialRevoke(q *Request, r *contract.ProjectInitialAccessRevokeRequest) (contract.ProjectInitialAccessRevokeReply, error) {
	if !q.person.Owner { // the owner only, stricter than the declared owner rule (full access)
		return contract.ProjectInitialAccessRevokeReply{}, fail(403, "Only the owner administers access")
	}
	out, err := projectInitialRevokeOf(q, r.ProjectId, r.BaseAccessVersion)
	if err != nil {
		return nil, err
	}
	return projectReply(jsonBytes(out), nil)
}

type projectRevokeResult struct {
	ProjectID     string `json:"projectId"`
	AccessVersion int64  `json:"accessVersion"`
	Revoked       bool   `json:"revoked"`
}

func projectInitialRevokeOf(q *Request, id string, base int64) (projectRevokeResult, error) {
	a, err := q.db().DefProjectAccessLock(q.ctx(), id)
	if err != nil {
		return projectRevokeResult{}, fail(404, "Unknown project")
	}
	version := a.AccessVersion
	if !a.Revoked {
		if base != version {
			return projectRevokeResult{}, fail(409, "Access version changed")
		}
		if err := q.db().DefProjectRevoke(q.ctx(), id); err != nil {
			return projectRevokeResult{}, err
		}
		if err := auditDefinition(q, definitionChange{ProjectID: id, Target: "project:" + id, Field: "initialAccess", Previous: "active", Value: "revoked"}); err != nil {
			return projectRevokeResult{}, err
		}
		version++
	}
	return projectRevokeResult{ProjectID: id, AccessVersion: version, Revoked: true}, nil
}

// projectArchive: retire a project definition (owner decision to drop a project). Its tasks, sessions and executions
// must be closed first (task_cancel, session_archive, execution_stop), so nothing runs under a project no list shows.
// Step rows count through their run: a finished run leaves unreached steps 'preparing', which is not open work.
// The GLOBAL write lock of every operation serializes this check with task_create and session_start; afterwards
// task_create, project_update and project_create refuse the id.
func projectArchive(q *Request, r *contract.ProjectArchiveRequest) (contract.ProjectArchiveReply, error) {
	return projectArchiveOf(q, r.ProjectId)
}

func projectArchiveOf(q *Request, id string) (contract.ProjectArchiveReply, error) {
	var out contract.ProjectArchiveReply
	rows, err := q.db().DefProjectOpenWork(q.ctx(), db.DefProjectOpenWorkParams{ProjectID: id, TaskStates: slices.Collect(maps.Keys(openTask))})
	if err != nil {
		return out, err
	}
	if len(rows) > 0 {
		open := []string{}
		for _, r := range rows {
			open = append(open, fmt.Sprintf("%v %s (e.g. %s)", r.N, r.Kind, r.Example))
		}
		return out, fail(409, "Close first: "+strings.Join(open, ", "))
	}
	// The head stays readable after retirement, so archiving again (idempotent) cleans keys a first run left behind.
	heads, err := definitionsOf(q.ctx(), q.db(), definitionFilter{kind: "project", id: id})
	if err != nil {
		return out, err
	}
	if len(heads) == 0 {
		return out, fail(404, "Unknown project")
	}
	conns, err := headConnections(heads[0].Content)
	if err != nil {
		return out, err
	}
	if err := q.db().DefProjectRetire(q.ctx(), id); err != nil {
		return out, err
	}
	removed, err := removeUnusedConnections(q, conns)
	if err != nil {
		return out, err
	}
	invalidateEnvCacheOnCommit(q)
	return contract.ProjectArchiveReply{ProjectId: id, Archived: true, ConnectionRemovals: removed}, nil
}

// projectRetired: the id belongs to an archived project (its definition row stays, so the id is never reused).
func projectRetired(q *Request, id string) (bool, error) {
	return q.db().DefProjectRetired(q.ctx(), id)
}

func init() {
	op("project_archive", projectArchive)
	op("project_create", func(q *Request, r *contract.ProjectCreateRequest) (contract.ProjectCreateReply, error) {
		return projectReply(projectWrite(q, true, projectWriteArgs{id: r.Id, requestID: r.RequestId, content: recOf(q.body)["content"], request: r}))
	})
	op("project_update", func(q *Request, r *contract.ProjectUpdateRequest) (contract.ProjectUpdateReply, error) {
		return projectReply(projectWrite(q, false, projectWriteArgs{id: r.ProjectId, requestID: r.RequestId, content: recOf(q.body)["content"],
			baseRevision: r.BaseRevision, repoSha: r.RepoSha, request: r}))
	})
	op("project_initial_access_revoke", projectInitialRevoke)
}

// projectContextFile: one owned context file of a project as its context shows it.
type projectContextFile struct {
	Path   any    `json:"path"`
	Text   any    `json:"text"`
	Sha256 string `json:"sha256"`
}

// projectContext: a live project's head as the record its callers read (dispatch, agents, projects.go): the bundle's
// settings at the top, with content (the bundle), the parsed settings, unknownKeys, context, revision and contentSha;
// in a transaction also its repoSha and sourceState, and for the owner its initialAccess.
func projectContext(q *Request, id string) (rawObject, error) {
	var heads []definitionRow
	err := q.app.store.Read(q.ctx(), q, func(q *Request) error {
		var err error
		heads, err = definitionsOf(q.ctx(), q.db(), definitionFilter{kind: "project", id: id, current: true})
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(heads) == 0 {
		return nil, fail(404, "Unknown project")
	}
	h := heads[0]
	b, err := openObject(h.Content) // the open bundle, as projectBundle and projectSettings (projects.go) read it
	if err != nil {
		return nil, err
	}
	if _, err = projectBundle(id, b); err != nil {
		return nil, err
	}
	var parts struct {
		Settings     json.RawMessage `json:"settings"`
		ContextFiles []struct {
			Path any `json:"path"`
			Text any `json:"text"`
		} `json:"contextFiles"`
	}
	if err = json.Unmarshal(jsonBytes(b), &parts); err != nil {
		return nil, err
	}
	var settings rawObject // a value of b: already in openObject's form
	if err := json.Unmarshal(orNull(parts.Settings), &settings); err != nil {
		return nil, err
	}
	parsed, unknown, err := projectSettings(settings)
	if err != nil {
		return nil, err
	}
	out := make(rawObject, len(settings)+10)
	maps.Copy(out, settings)
	out["content"] = jsonBytes(b)
	out["settings"] = jsonBytes(parsed)
	out["unknownKeys"] = jsonBytes(unknown)
	ctx := []projectContextFile{}
	for _, f := range parts.ContextFiles {
		ctx = append(ctx, projectContextFile{Path: f.Path, Text: f.Text, Sha256: sha256Text(js(f.Text))})
	}
	out["context"] = openJSON(ctx)
	if q.tx != nil {
		head, err := headOf(q, "project", id, false)
		if err != nil {
			return nil, err
		}
		out["repoSha"] = jsonBytes(head.RepoSha)
		out["sourceState"] = jsonBytes(head.state())
	}
	if q.person != nil && q.person.Owner && q.tx != nil {
		a, err := q.db().DefProjectAccess(q.ctx(), id)
		if err != nil {
			return nil, err
		}
		out["initialAccess"] = openJSON(struct {
			Member  *string `json:"member"`
			Revoked bool    `json:"revoked"`
			Version int64   `json:"version"`
		}{a.InitialMember, a.Revoked, a.AccessVersion})
	}
	out["revision"] = jsonBytes(h.Head)
	out["contentSha"] = jsonBytes(h.ContentSha)
	return out, nil
}

// projectHeadSettings: a project's settings as projectContext gives them ("settings": projectSettings of the head's
// settings), from the head's content already read, without its context files, their hashes, its source state or its
// access (a list of every project's roles reads only this). An invalid bundle has none, as projectSettingsOf answers.
func projectHeadSettings(id string, content []byte) rawObject {
	b, err := openObject(content)
	if err != nil {
		return nil
	}
	if _, err = projectBundle(id, b); err != nil {
		return nil
	}
	var parts struct {
		Settings json.RawMessage `json:"settings"`
	}
	if json.Unmarshal(jsonBytes(b), &parts) != nil {
		return nil
	}
	var settings rawObject
	if json.Unmarshal(orNull(parts.Settings), &settings) != nil {
		return nil
	}
	parsed, _, err := projectSettings(settings)
	if err != nil {
		return nil
	}
	return parsed
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
	heads, err := headTexts(q, "project")
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for _, h := range heads {
		cs, err := headConnections(h.text)
		if err != nil {
			return nil, fail(409, "Project "+h.id+" does not parse; no deploy key is removed")
		}
		for _, c := range cs {
			used[c] = true
		}
	}
	obs, err := q.db().HostObservations(q.ctx())
	if err != nil {
		return nil, err
	}
	removed, err := q.db().RemovedHostIDs(q.ctx())
	if err != nil {
		return nil, err
	}
	type hostConnections struct {
		id          string
		connections []string
	}
	live := []hostConnections{} // a removed host never fetches a removal
	for _, o := range obs {
		if !slices.Contains(removed, o.ID) {
			var p struct {
				GitConnections json.RawMessage `json:"gitConnections"`
			}
			_ = lenientDecode(o.Payload, &p)
			live = append(live, hostConnections{o.ID, jsonStrings(p.GitConnections)})
		}
	}
	for _, c := range conns {
		if used[c] {
			continue
		}
		for _, o := range live {
			host := o.id
			if !slices.Contains(o.connections, c) {
				continue
			}
			if dup, err := q.db().DefRemovalPending(q.ctx(), db.DefRemovalPendingParams{HostID: host, Connection: c}); err != nil || dup {
				if err != nil {
					return nil, err
				}
				continue
			}
			xid := newID() // a host job: no session
			if err := executionWrite(q, struct {
				ID          string `json:"id"`
				ExecutionID string `json:"executionId"`
				HostID      string `json:"hostId"`
				State       string `json:"state"`
				CreatedAt   int64  `json:"createdAt"`
				Actor       string `json:"actor"`
			}{xid, xid, host, "preparing", nowMs(), "program-worker"}); err != nil {
				return nil, err
			}
			if err := commandPut(q, &agentCommand{ID: newID(), ExecutionID: xid, HostID: host, Kind: string(contract.CommandKindConnectionRemove), Status: "pending",
				Payload: jsonBytes(contract.ConnectionRemovePayload{Connection: c})}); err != nil {
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
	ids, err := q.db().DefRemovalsPending(q.ctx(), conns)
	if err != nil {
		return err
	}
	for _, id := range ids {
		c, err := commandGet(q, id)
		if err != nil {
			return err
		}
		c.Status, c.StopDetail = "stopped", "connection in use again"
		if err := commandPut(q, &c); err != nil {
			return err
		}
		e, err := getExecution(q, c.ExecutionID)
		if err != nil {
			return err
		}
		if !e.terminal() {
			e.State, e.StopDetail, e.FinishedAt = "cancelled", "connection in use again", jsonBytes(nowMs())
			if err := executionPut(q, &e); err != nil {
				return err
			}
		}
	}
	return nil
}
