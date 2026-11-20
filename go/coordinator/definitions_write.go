// Definition writes and their reads (docs/designs/fleet-definition-editors.md, Save and conflicts): per-person drafts,
// diff, save with base and repository checks, create, history and export.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

// conflict: a 409 whose body carries DefinitionConflict next to the message.
func conflict(reason contract.ConflictReason, head int64, repoSha *string, msg string) error {
	return &HTTPError{Code: 409, Msg: msg, Extra: conflictExtra(reason, head, repoSha)}
}

// conflictExtra: DefinitionConflict as the error body's extra fields (repoSha null when unknown).
func conflictExtra(reason contract.ConflictReason, head int64, repoSha *string) doc {
	return docOf(struct {
		Reason  contract.ConflictReason `json:"reason"`
		Head    int64                   `json:"head"`
		RepoSha *string                 `json:"repoSha"`
	}{reason, head, repoSha})
}

// definitionChange: the setting-change audit record of a definition (a project's carries its project).
type definitionChange struct {
	ID         string `json:"id"`
	ProjectID  string `json:"projectId,omitempty"`
	Target     string `json:"target"`
	Field      string `json:"field"`
	Previous   any    `json:"previous"`
	Value      any    `json:"value"`
	Actor      string `json:"actor"`
	OnBehalfOf string `json:"onBehalfOf,omitempty"`
	At         int64  `json:"at"`
	Note       string `json:"note,omitempty"`
}

// auditDefinition records c; an agent's change is the execution acting for its director (fleet-pm-agent.md §1).
func auditDefinition(q *Request, c definitionChange) error {
	c.ID, c.Actor, c.At = newID(), actor(q, "operator"), nowMs()
	if q.agent != "" && q.person != nil {
		c.Actor, c.OnBehalfOf = q.agent, q.person.Email
	}
	return q.db().DefSettingChange(q.ctx(), db.DefSettingChangeParams{ID: c.ID, Data: jsonBytes(c), ProjectID: defText(c.ProjectID)})
}

// contentText: the request's content of the definition's kind as canonical text, from the bytes sent (the typed struct
// would drop a misspelled key the kind's strict validator must see), or 400 when the wrong property is set.
func contentText(q *Request, kind contract.DefinitionKind, c contract.DefinitionContent) (string, error) {
	n := 0
	for _, set := range []bool{c.Agent != nil, c.Pipeline != nil, c.Catalogue != nil} {
		if set {
			n++
		}
	}
	if n != 1 || (kind == "agent") != (c.Agent != nil) || (kind == "pipeline") != (c.Pipeline != nil) || (kind == "catalogue") != (c.Catalogue != nil) {
		return "", fail(400, "content: exactly the property of the kind ("+string(kind)+")")
	}
	var b struct {
		Content map[string]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(q.body, &b); err != nil || b.Content[string(kind)] == nil {
		return "", fail(400, "content: exactly the property of the kind ("+string(kind)+")")
	}
	return canonicalText([]byte(b.Content[string(kind)]))
}

// problems: the kind's validator on content; a draft is stored even when it has some, a save refuses it.
func problems(kind, id, text string) []contract.Problem {
	if err := validDefinition(kind, id, []byte(text)); err != nil {
		return []contract.Problem{{Path: "", Message: err.Error()}}
	}
	return []contract.Problem{}
}

func decodeNumbers(text string) any {
	var v any
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	dec.Decode(&v)
	return v
}

// fieldChanges: changed leaves between two contents as JSON pointers; arrays of objects with an id match by id.
func fieldChanges(a, b string) []contract.FieldChange {
	out := []contract.FieldChange{}
	text := func(v json.RawMessage) *string {
		t, _ := canonicalText([]byte(v))
		t = strings.TrimSuffix(t, "\n")
		return &t
	}
	pointer := func(path, k string) string {
		return path + "/" + strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
	}
	var walk func(path string, x, y json.RawMessage, hasX, hasY bool)
	walk = func(path string, x, y json.RawMessage, hasX, hasY bool) {
		switch {
		case !hasX:
			out = append(out, contract.FieldChange{Path: path, Op: contract.FieldOpAdd, ToText: text(y)})
			return
		case !hasY:
			out = append(out, contract.FieldChange{Path: path, Op: contract.FieldOpRemove, FromText: text(x)})
			return
		}
		mx, okx := jsonObject(x)
		my, oky := jsonObject(y)
		if okx && oky {
			keys := map[string]bool{}
			for k := range mx {
				keys[k] = true
			}
			for k := range my {
				keys[k] = true
			}
			ks := make([]string, 0, len(keys))
			for k := range keys {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				vx, hx := mx[k]
				vy, hy := my[k]
				walk(pointer(path, k), vx, vy, hx, hy)
			}
			return
		}
		ax, okx := jsonArray(x)
		ay, oky := jsonArray(y)
		if okx && oky {
			if ix, iy := elementsByID(ax), elementsByID(ay); ix != nil && iy != nil {
				ids := []string{}
				for _, e := range ax {
					ids = append(ids, elementID(e))
				}
				for _, e := range ay {
					if _, ok := ix[elementID(e)]; !ok {
						ids = append(ids, elementID(e))
					}
				}
				for _, id := range ids {
					vx, hx := ix[id]
					vy, hy := iy[id]
					walk(pointer(path, id), vx, vy, hx, hy)
				}
				return
			}
			for i := 0; i < max(len(ax), len(ay)); i++ {
				var vx, vy json.RawMessage
				if i < len(ax) {
					vx = ax[i]
				}
				if i < len(ay) {
					vy = ay[i]
				}
				walk(fmt.Sprintf("%s/%d", path, i), vx, vy, i < len(ax), i < len(ay))
			}
			return
		}
		if !reflect.DeepEqual(decodeNumbers(string(x)), decodeNumbers(string(y))) {
			out = append(out, contract.FieldChange{Path: path, Op: contract.FieldOpReplace, FromText: text(x), ToText: text(y)})
		}
	}
	walk("", json.RawMessage(a), json.RawMessage(b), a != "", b != "")
	return out
}

// jsonObject: the members of a JSON object, false when raw is no object.
func jsonObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '{' {
		return nil, false
	}
	var m map[string]json.RawMessage
	return m, json.Unmarshal(raw, &m) == nil
}

// jsonArray: the elements of a JSON array, false when raw is no array.
func jsonArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '[' {
		return nil, false
	}
	var xs []json.RawMessage
	return xs, json.Unmarshal(raw, &xs) == nil
}

// elementID: an array element's string id, "" when it is no object or has none.
func elementID(raw json.RawMessage) string {
	if _, ok := jsonObject(raw); !ok {
		return ""
	}
	var e struct {
		ID any `json:"id"`
	}
	json.Unmarshal(raw, &e)
	id, _ := e.ID.(string)
	return id
}

// elementsByID: array elements by id; nil when one has no id or an id repeats (compare by index).
func elementsByID(xs []json.RawMessage) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for _, e := range xs {
		id := elementID(e)
		if _, dup := m[id]; id == "" || dup {
			return nil
		}
		m[id] = e
	}
	return m
}

type draftRow struct {
	Base    int64
	Content string
	At      time.Time
}

// draftOwner: drafts are per person; an agent has its own (execution:<id>), never its director's.
func draftOwner(q *Request) string {
	if q.agent != "" {
		return q.agent
	}
	return director(q)
}

func draftOf(q *Request, kind, id string) (*draftRow, error) {
	r, err := q.db().DefDraft(q.ctx(), db.DefDraftParams{Kind: kind, ID: id, Person: draftOwner(q)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &draftRow{Base: r.Base, Content: r.Content, At: r.UpdatedAt}, err
}

// headOf: the definition's head row, nil when it does not exist or was deleted (definition_delete).
func headOf(q *Request, kind, id string, lock bool) (*definitionRow, error) {
	if lock {
		if err := q.db().DefLock(q.ctx(), db.DefLockParams{Kind: kind, ID: id}); err != nil {
			return nil, err
		}
	}
	xs, err := definitionsOf(q.ctx(), q.db(), definitionFilter{kind: kind, id: id, live: true})
	if err != nil || len(xs) == 0 {
		return nil, err
	}
	return &xs[0], nil
}

func draftInfo(d *draftRow, head *definitionRow) *contract.DraftInfo {
	base := ""
	if head != nil && d.Base == head.Head {
		base = string(head.Content)
	}
	info := contract.DraftInfo{BaseRevision: d.Base, Stale: head == nil && d.Base != 0 || head != nil && d.Base != head.Head, UpdatedAt: d.At.UTC().Format(time.RFC3339Nano)}
	if base != "" || d.Base == 0 {
		info.Changes = int64(len(fieldChanges(base, d.Content)))
	}
	return &info
}

func definitionDraftPut(q *Request, r *contract.DefinitionDraftPutRequest) (contract.DefinitionDraftPutReply, error) {
	var out contract.DefinitionDraftPutReply
	if err := definitionKind(r.Kind); err != nil {
		return out, err
	}
	if !idPattern.MatchString(r.Id) && r.Id != "catalogue" {
		return out, fail(400, "id: a file stem (lower case, digits, -)")
	}
	text, err := contentText(q, r.Kind, r.Content)
	if err != nil {
		return out, err
	}
	head, err := headOf(q, string(r.Kind), r.Id, false)
	if err != nil {
		return out, err
	}
	if head == nil && r.BaseRevision != 0 || head != nil && r.BaseRevision < 1 {
		return out, fail(400, "baseRevision: 0 for a new definition, else a revision of it")
	}
	if err := q.db().DefDraftPut(q.ctx(), db.DefDraftPutParams{Kind: string(r.Kind), ID: r.Id, Person: draftOwner(q), Base: int32(r.BaseRevision), Content: text}); err != nil {
		return out, err
	}
	d, err := draftOf(q, string(r.Kind), r.Id)
	if err != nil {
		return out, err
	}
	return contract.DefinitionDraftPutReply{Draft: *draftInfo(d, head), Problems: problems(string(r.Kind), r.Id, text)}, nil
}

func definitionDraftGet(q *Request, r *contract.DefinitionDraftGetRequest) (contract.DefinitionDraftGetReply, error) {
	var out contract.DefinitionDraftGetReply
	d, err := draftOf(q, string(r.Kind), r.Id)
	if err != nil || d == nil {
		if err == nil {
			err = fail(404, "No draft")
		}
		return out, err
	}
	head, err := headOf(q, string(r.Kind), r.Id, false)
	if err != nil {
		return out, err
	}
	c, err := definitionContent(string(r.Kind), []byte(d.Content))
	return contract.DefinitionDraftGetReply{Draft: *draftInfo(d, head), Content: &c, Text: d.Content, Problems: problems(string(r.Kind), r.Id, d.Content)}, err
}

func definitionDraftDiscard(q *Request, r *contract.DefinitionDraftDiscardRequest) (contract.DefinitionDraftDiscardReply, error) {
	n, err := q.db().DefDraftDelete(q.ctx(), db.DefDraftDeleteParams{Kind: string(r.Kind), ID: r.Id, Person: defText(draftOwner(q))})
	if err != nil {
		return contract.DefinitionDraftDiscardReply{}, err
	}
	return contract.DefinitionDraftDiscardReply{Discarded: n > 0}, nil
}

// sideText: one side of a diff: a revision, the caller's draft or the shipped file ("" and false when absent).
func sideText(q *Request, kind, id string, s contract.DiffSide) (string, bool, error) {
	n := 0
	for _, b := range []bool{s.Revision != nil, s.Draft != nil && *s.Draft, s.Repository != nil && *s.Repository} {
		if b {
			n++
		}
	}
	if n != 1 {
		return "", false, fail(400, "Each side: exactly one of revision, draft or repository")
	}
	switch {
	case s.Revision != nil:
		if *s.Revision < 1 {
			return "", false, fail(400, "revision: 1 or more")
		}
		xs, err := definitionsOf(q.ctx(), q.db(), definitionFilter{kind: kind, id: id, revision: *s.Revision})
		if err != nil || len(xs) == 0 {
			if err == nil {
				err = fail(404, "Unknown definition or revision")
			}
			return "", false, err
		}
		return string(xs[0].Content), true, nil
	case s.Draft != nil && *s.Draft:
		d, err := draftOf(q, kind, id)
		if err != nil || d == nil {
			if err == nil {
				err = fail(404, "No draft")
			}
			return "", false, err
		}
		return d.Content, true, nil
	}
	t, err := q.db().DefRepoContent(q.ctx(), db.DefRepoContentParams{Kind: kind, ID: id})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	if t == nil {
		return "", false, nil
	}
	return *t, true, nil
}

func definitionDiff(q *Request, r *contract.DefinitionDiffRequest) (contract.DefinitionDiffReply, error) {
	var out contract.DefinitionDiffReply
	if err := definitionKind(r.Kind); err != nil {
		return out, err
	}
	a, ha, err := sideText(q, string(r.Kind), r.Id, r.From)
	if err != nil {
		return out, err
	}
	b, hb, err := sideText(q, string(r.Kind), r.Id, r.To)
	if err != nil {
		return out, err
	}
	path := definitionPath(string(r.Kind), r.Id)
	return contract.DefinitionDiffReply{Unified: unifiedDiff(path, a, b, ha, hb), Fields: fieldChanges(a, b)}, nil
}

// shippedPath: where the running release keeps the definition's file (the directories reconcile reads).
func shippedPath(kind, id string) string {
	switch kind {
	case "agent":
		return filepath.Join(profilesDir(), id+".json")
	case "pipeline":
		return filepath.Join(configDir(), "pipelines", id+".json")
	}
	return filepath.Join(configDir(), "catalogue.json")
}

func definitionPath(kind, id string) string {
	switch kind {
	case "agent":
		return "config/agents/" + id + ".json"
	case "pipeline":
		return "config/pipelines/" + id + ".json"
	}
	return "config/catalogue.json"
}

// appendRevision: the next revision of a definition whose row is locked; moves the head and drops the saver's draft.
func appendRevision(q *Request, kind, id string, head int64, text, repoBase *string, source, message string) (contract.Revision, error) {
	actorKind, byEmail, byExec := "person", ptr(director(q)), (*string)(nil)
	if q.agent != "" {
		actorKind, byEmail, byExec = "agent", nil, ptr(strings.TrimPrefix(q.agent, "execution:"))
		if source == "editor" {
			source = "agent"
		}
	}
	var parent *int32
	if head > 0 {
		parent = ptr(int32(head))
	}
	n := head + 1
	err := q.db().DefRevisionInsert(q.ctx(), db.DefRevisionInsertParams{Kind: kind, ID: id, Revision: int32(n), Parent: parent, Content: *text, ContentSha: sha256Text(*text),
		RepoBase: repoBase, Source: source, ActorKind: actorKind, ByEmail: byEmail, ByExecution: byExec, DirectedBy: ptr(director(q)), Message: message})
	if pgCode(err) == "23505" {
		return contract.Revision{}, conflict(contract.ConflictReasonBaseMoved, head, nil, "Another save moved the head")
	}
	if err != nil {
		return contract.Revision{}, err
	}
	// A save decides a definition whose file is gone: it is the store's own from now (store-only, exportable), and a
	// retired one is back.
	if err := q.db().DefMoveHead(q.ctx(), db.DefMoveHeadParams{Kind: kind, ID: id, Head: int32(n)}); err != nil {
		return contract.Revision{}, err
	}
	if source == "lab" { // a Lab promote is not the saver's draft: leave their draft as it is
	} else if _, err := q.db().DefDraftDelete(q.ctx(), db.DefDraftDeleteParams{Kind: kind, ID: id, Person: defText(draftOwner(q))}); err != nil {
		return contract.Revision{}, err
	}
	change := definitionChange{Target: "definition:" + kind + "/" + id, Field: "revision", Previous: head, Value: n}
	if kind == "project" {
		change.ProjectID = id
	}
	if err := auditDefinition(q, change); err != nil {
		return contract.Revision{}, err
	}
	invalidateEnvCacheOnCommit(q)
	xs, err := definitionsOf(q.ctx(), q.db(), definitionFilter{kind: kind, id: id, revision: n})
	if err != nil || len(xs) == 0 {
		return contract.Revision{}, err
	}
	return xs[0].revisionView(q), nil
}

func definitionSave(q *Request, r *contract.DefinitionSaveRequest) (contract.DefinitionSaveReply, error) {
	var out contract.DefinitionSaveReply
	if err := definitionKind(r.Kind); err != nil {
		return out, err
	}
	text, err := contentText(q, r.Kind, r.Content)
	if err != nil {
		return out, err
	}
	head, err := headOf(q, string(r.Kind), r.Id, true)
	if err != nil {
		return out, err
	}
	if head == nil {
		return out, fail(404, "Unknown definition: create it first")
	}
	if r.BaseRevision != head.Head {
		return out, conflict(contract.ConflictReasonBaseMoved, head.Head, head.RepoSha, fmt.Sprintf("The head is revision %d", head.Head))
	}
	if head.state() == contract.DefinitionStateRepositoryChanged && (r.RepoSha == nil || head.RepoSha == nil || *r.RepoSha != *head.RepoSha) {
		return out, conflict(contract.ConflictReasonRepositoryChanged, head.Head, head.RepoSha, "The repository file changed: look at it and save with its repoSha")
	}
	if text == string(head.Content) {
		return out, conflict(contract.ConflictReasonNoChange, head.Head, head.RepoSha, "No change from the head")
	}
	if ps := problems(string(r.Kind), r.Id, text); len(ps) > 0 {
		return out, &HTTPError{Code: 409, Msg: "Invalid content: " + ps[0].Message, Extra: conflictExtra(contract.ConflictReasonInvalidDraft, head.Head, head.RepoSha)}
	}
	if strings.TrimSpace(r.Message) == "" {
		return out, fail(400, "Expected message")
	}
	rev, err := appendRevision(q, string(r.Kind), r.Id, head.Head, &text, head.RepoSha, "editor", r.Message)
	if err != nil {
		return out, err
	}
	now, err := headOf(q, string(r.Kind), r.Id, false)
	if err != nil {
		return out, err
	}
	return contract.DefinitionSaveReply{Revision: rev, State: now.state()}, nil
}

func definitionCreate(q *Request, r *contract.DefinitionCreateRequest) (contract.DefinitionCreateReply, error) {
	var out contract.DefinitionCreateReply
	if err := definitionKind(r.Kind); err != nil {
		return out, err
	}
	if r.Kind == "catalogue" || !idPattern.MatchString(r.Id) {
		return out, fail(400, "id: a file stem (lower case, digits, -); the catalogue exists once")
	}
	text, err := contentText(q, r.Kind, r.Content)
	if err != nil {
		return out, err
	}
	if ps := problems(string(r.Kind), r.Id, text); len(ps) > 0 {
		return out, &HTTPError{Code: 409, Msg: "Invalid content: " + ps[0].Message, Extra: conflictExtra(contract.ConflictReasonInvalidDraft, 0, nil)}
	}
	if strings.TrimSpace(r.Message) == "" {
		return out, fail(400, "Expected message")
	}
	n, err := q.db().DefCreate(q.ctx(), db.DefCreateParams{Kind: string(r.Kind), ID: r.Id, Path: definitionPath(string(r.Kind), r.Id)})
	if err != nil {
		return out, err
	}
	if n == 0 {
		return out, conflict(contract.ConflictReasonWrongState, 0, nil, "The definition exists")
	}
	rev, err := appendRevision(q, string(r.Kind), r.Id, 0, &text, nil, "editor", r.Message)
	return contract.DefinitionCreateReply{Revision: rev}, err
}

// definitionDelete (fleet-definition-editors.md, Delete): an agent or pipeline nothing uses goes from every list, read and
// run. The row and its revisions stay (deleted_at, retired_at): the id is never reused (definition_create refuses it) and
// executions that pinned a revision keep their history. A shipped definition is refused: the repository brings it back.
func definitionDelete(q *Request, r *contract.DefinitionDeleteRequest) (contract.DefinitionDeleteReply, error) {
	out := contract.DefinitionDeleteReply{Kind: r.Kind, Id: r.Id, UsedBy: []contract.DefinitionKey{}, Experiments: []string{}}
	if err := definitionKind(r.Kind); err != nil {
		return out, err
	}
	if r.Kind != contract.DefinitionKindAgent && r.Kind != contract.DefinitionKindPipeline {
		return out, fail(400, "kind: agent or pipeline (a project goes with object_delete; the catalogue exists once)")
	}
	head, err := headOf(q, string(r.Kind), r.Id, !r.Preview)
	if err != nil || head == nil {
		if err == nil {
			err = fail(404, "Unknown definition")
		}
		return out, err
	}
	out.State = head.state()
	if out.UsedBy, err = usedByDefinitions(q, string(r.Kind), r.Id); err != nil {
		return out, err
	}
	// Every pin, not only the first one of its kind (execution.pipeline_id and profile_id keep the first).
	// Older runs name it only in data (pipelineId) or config_ref (profileId).
	if out.Running, err = q.db().DefRunning(q.ctx(), db.DefRunningParams{Kind: string(r.Kind), ID: r.Id}); err != nil {
		return out, err
	}
	// A live experiment names it as its pipeline, or anywhere in its variants (a method's pipelineId, a profile
	// override) or evaluators: a JSON string equal to the id.
	exps, err := q.db().DefExperimentsUsing(q.ctx(), r.Id)
	if err != nil {
		return out, err
	}
	out.Experiments = append(out.Experiments, exps...)
	// A project's roles and execution profiles name agents.
	roles := []string{}
	if r.Kind == contract.DefinitionKindAgent {
		byProfile, err := projectRoleProfiles(q, true)
		if err != nil {
			return out, err
		}
		roles = byProfile[r.Id]
		sort.Strings(roles)
	}
	// An open session whose profile it is would take no further turn.
	var sessions int64
	if r.Kind == contract.DefinitionKindAgent {
		if sessions, err = q.db().DefSessionsUsing(q.ctx(), r.Id); err != nil {
			return out, err
		}
	}
	refused := ""
	switch {
	case out.State != contract.DefinitionStateStoreOnly && out.State != contract.DefinitionStateRepositoryDeleted:
		refused = "shipped by the repository (" + head.Path + "): the next release would bring it back; delete the file instead"
	case len(roles) > 0:
		refused = "project roles use it: " + strings.Join(roles, ", ")
	case len(out.UsedBy) > 0:
		names := []string{}
		for _, k := range out.UsedBy {
			names = append(names, string(k.Kind)+"/"+k.Id)
		}
		refused = "used by " + strings.Join(names, ", ")
	case sessions > 0:
		refused = fmt.Sprintf("%d open sessions use it as their profile", sessions)
	case out.Running > 0:
		refused = fmt.Sprintf("%d unfinished executions run it", out.Running)
	case len(out.Experiments) > 0:
		refused = "live Lab experiments run it: " + strings.Join(out.Experiments, ", ")
	}
	if refused != "" {
		out.Refused = &refused
	}
	if r.Preview {
		return out, nil
	}
	if refused != "" {
		return out, fail(409, "Not deleted: "+refused)
	}
	if err := q.db().DefDelete(q.ctx(), db.DefDeleteParams{Kind: string(r.Kind), ID: r.Id}); err != nil {
		return out, err
	}
	if _, err := q.db().DefDraftDelete(q.ctx(), db.DefDraftDeleteParams{Kind: string(r.Kind), ID: r.Id}); err != nil {
		return out, err
	}
	note := js(struct {
		State contract.DefinitionState `json:"state"`
		Head  int64                    `json:"head"`
	}{out.State, head.Head})
	if err := auditDefinition(q, definitionChange{Target: "definition:" + string(r.Kind) + "/" + r.Id, Field: "deletedAt", Value: nowMs(), Note: note}); err != nil {
		return out, err
	}
	out.Deleted = true
	return out, nil
}

func definitionHistory(q *Request, r *contract.DefinitionHistoryRequest) (contract.DefinitionHistoryReply, error) {
	out := contract.DefinitionHistoryReply{Revisions: []contract.Revision{}}
	head, err := headOf(q, string(r.Kind), r.Id, false)
	if err != nil || head == nil {
		if err == nil {
			err = fail(404, "Unknown definition")
		}
		return out, err
	}
	out.RepoSha = head.RepoSha
	limit := int64(50)
	if r.Limit != nil {
		limit = max(1, min(*r.Limit, 500))
	}
	before := head.Head + 1
	if r.Before != nil {
		before = min(*r.Before, head.Head+1)
	}
	xs, err := q.db().DefHistory(q.ctx(), db.DefHistoryParams{Kind: string(r.Kind), ID: r.Id, Before: int32(before), Lim: int32(limit)})
	for _, x := range xs {
		out.Revisions = append(out.Revisions, definitionRowOf(db.DefRowsRow(x)).revisionView(q))
	}
	return out, err
}

// definitionExport: one patch against the running release's config/ for every ahead or store-only definition (or the
// named ones). A repository-changed or repository-deleted one is never exported, so an export cannot revert a release.
func definitionExport(q *Request, r *contract.DefinitionExportRequest) (contract.DefinitionExportReply, error) {
	out := contract.DefinitionExportReply{Files: []contract.ExportFile{}, Covers: []contract.DefinitionRef{}}
	xs, err := definitionsOf(q.ctx(), q.db(), definitionFilter{current: true})
	if err != nil {
		return out, err
	}
	want := map[string]bool{}
	for _, k := range r.Keys {
		want[string(k.Kind)+"/"+k.Id] = true
	}
	var patch bytes.Buffer
	for _, x := range xs {
		if x.Kind == "project" {
			if len(r.Keys) == 0 {
				continue
			}
			for _, key := range r.Keys {
				if key.Kind == "project" && key.Id == x.ID {
					return out, fail(400, "Project export is deferred")
				}
			}
			continue
		}

		key := x.Kind + "/" + x.ID
		st := x.state()
		if len(want) > 0 && !want[key] {
			continue
		}
		if st == contract.DefinitionStateRepositoryChanged || st == contract.DefinitionStateRepositoryDeleted {
			if want[key] {
				return out, conflict(contract.ConflictReasonRepositoryChanged, x.Head, x.RepoSha, key+" is "+string(st)+": save it to resolve first")
			}
			continue
		}
		if st != contract.DefinitionStateAhead && st != contract.DefinitionStateStoreOnly {
			continue
		}
		var shipped string
		exists := false
		if b, err := os.ReadFile(shippedPath(x.Kind, x.ID)); err == nil {
			shipped, exists = string(b), true
		}
		patch.WriteString(unifiedDiff(x.Path, shipped, string(x.Content), exists, true))
		out.Files = append(out.Files, contract.ExportFile{Path: x.Path, Text: string(x.Content), BaseSha: x.RepoSha})
		out.Covers = append(out.Covers, contract.DefinitionRef{Kind: contract.DefinitionKind(x.Kind), Id: x.ID, Revision: x.Head, ContentSha: x.ContentSha})
	}
	out.Patch = patch.String()
	return out, nil
}

func init() {
	op("definition_draft_put", definitionDraftPut)
	op("definition_draft_get", definitionDraftGet)
	op("definition_draft_discard", definitionDraftDiscard)
	op("definition_diff", definitionDiff)
	op("definition_save", definitionSave)
	op("definition_create", definitionCreate)
	op("definition_delete", definitionDelete)
	op("definition_history", definitionHistory)
	op("definition_export", definitionExport)
}
