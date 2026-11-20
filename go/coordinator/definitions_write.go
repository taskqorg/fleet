// Definition writes and their reads (docs/designs/fleet-definition-editors.md, Save and conflicts): per-person drafts,
// diff, save with base and repository checks, create, history and export.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

// conflict: a 409 whose body carries DefinitionConflict next to the message.
func conflict(reason contract.ConflictReason, head int64, repoSha *string, msg string) error {
	return &HTTPError{Code: 409, Msg: msg, Extra: map[string]any{"reason": reason, "head": head, "repoSha": repoSha}}
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
	if _, err := validDefinition(kind, id, []byte(text)); err != nil {
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
	text := func(v any) *string { t, _ := canonicalText(jsonBytes(v)); t = strings.TrimSuffix(t, "\n"); return &t }
	var walk func(path string, x, y any, hasX, hasY bool)
	walk = func(path string, x, y any, hasX, hasY bool) {
		switch {
		case !hasX:
			out = append(out, contract.FieldChange{Path: path, Op: contract.FieldOpAdd, ToText: text(y)})
			return
		case !hasY:
			out = append(out, contract.FieldChange{Path: path, Op: contract.FieldOpRemove, FromText: text(x)})
			return
		}
		mx, okx := x.(map[string]any)
		my, oky := y.(map[string]any)
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
				walk(path+"/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"), vx, vy, hx, hy)
			}
			return
		}
		ax, okx := x.([]any)
		ay, oky := y.([]any)
		if okx && oky {
			byID := func(xs []any) map[string]any {
				m := map[string]any{}
				for _, e := range xs {
					id := astr(mapOf(e)["id"])
					if _, dup := m[id]; id == "" || dup {
						return nil // no id or a repeated one: compare by index
					}
					m[id] = e
				}
				return m
			}
			if ix, iy := byID(ax), byID(ay); ix != nil && iy != nil {
				ids := []string{}
				for _, e := range ax {
					ids = append(ids, astr(mapOf(e)["id"]))
				}
				for _, e := range ay {
					if _, ok := ix[astr(mapOf(e)["id"])]; !ok {
						ids = append(ids, astr(mapOf(e)["id"]))
					}
				}
				for _, id := range ids {
					vx, hx := ix[id]
					vy, hy := iy[id]
					walk(path+"/"+strings.ReplaceAll(strings.ReplaceAll(id, "~", "~0"), "/", "~1"), vx, vy, hx, hy)
				}
				return
			}
			for i := 0; i < max(len(ax), len(ay)); i++ {
				var vx, vy any
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
		if !reflect.DeepEqual(x, y) {
			out = append(out, contract.FieldChange{Path: path, Op: contract.FieldOpReplace, FromText: text(x), ToText: text(y)})
		}
	}
	walk("", decodeNumbers(a), decodeNumbers(b), a != "", b != "")
	return out
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
	var d draftRow
	err := q.tx.QueryRow(q.ctx(), "SELECT base,content,updated_at FROM definition_draft WHERE kind=$1 AND id=$2 AND person=$3", kind, id, draftOwner(q)).Scan(&d.Base, &d.Content, &d.At)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &d, err
}

// headOf: the definition's head row, nil when it does not exist or was deleted (definition_delete).
func headOf(q *Request, kind, id string, lock bool) (*definitionRow, error) {
	where := "d.kind=$1 AND d.id=$2 AND d.deleted_at IS NULL"
	if lock {
		if _, err := q.tx.Exec(q.ctx(), "SELECT 1 FROM definition WHERE kind=$1 AND id=$2 FOR UPDATE", kind, id); err != nil {
			return nil, err
		}
	}
	xs, err := definitionRows(q, where, 0, kind, id)
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
	if err := exec(q, `INSERT INTO definition_draft(kind,id,person,base,content) VALUES($1,$2,$3,$4,$5)
	  ON CONFLICT(kind,id,person) DO UPDATE SET base=excluded.base,content=excluded.content,updated_at=now()`, r.Kind, r.Id, draftOwner(q), r.BaseRevision, text); err != nil {
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
	tag, err := q.tx.Exec(q.ctx(), "DELETE FROM definition_draft WHERE kind=$1 AND id=$2 AND person=$3", r.Kind, r.Id, draftOwner(q))
	if err != nil {
		return contract.DefinitionDraftDiscardReply{}, err
	}
	return contract.DefinitionDraftDiscardReply{Discarded: tag.RowsAffected() > 0}, nil
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
		xs, err := definitionRows(q, "d.kind=$1 AND d.id=$2", *s.Revision, kind, id)
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
	var t *string
	if err := q.tx.QueryRow(q.ctx(), "SELECT repo_content FROM definition WHERE kind=$1 AND id=$2", kind, id).Scan(&t); err != nil && err != pgx.ErrNoRows {
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
	actorKind, byEmail, byExec := "person", any(director(q)), any(nil)
	if q.agent != "" {
		actorKind, byEmail, byExec = "agent", nil, strings.TrimPrefix(q.agent, "execution:")
		if source == "editor" {
			source = "agent"
		}
	}
	var parent any
	if head > 0 {
		parent = head
	}
	n := head + 1
	err := exec(q, `INSERT INTO definition_revision(kind,id,revision,parent,content,content_sha,repo_base,source,actor_kind,by_email,by_execution,directed_by,message)
	  VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, kind, id, n, parent, *text, sha256Text(*text), repoBase, source, actorKind, byEmail, byExec, director(q), message)
	if pgCode(err) == "23505" {
		return contract.Revision{}, conflict(contract.ConflictReasonBaseMoved, head, nil, "Another save moved the head")
	}
	if err != nil {
		return contract.Revision{}, err
	}
	// A save decides a definition whose file is gone: it is the store's own from now (store-only, exportable), and a
	// retired one is back.
	if err := exec(q, "UPDATE definition SET head=$3,retired_at=CASE WHEN deleted_at IS NULL THEN NULL ELSE retired_at END,ever_shipped=ever_shipped AND repo_sha IS NOT NULL WHERE kind=$1 AND id=$2", kind, id, n); err != nil {
		return contract.Revision{}, err
	}
	if source == "lab" { // a Lab promote is not the saver's draft: leave their draft as it is
	} else if err := exec(q, "DELETE FROM definition_draft WHERE kind=$1 AND id=$2 AND person=$3", kind, id, draftOwner(q)); err != nil {
		return contract.Revision{}, err
	}
	change := doc{"id": newID(), "target": "definition:" + kind + "/" + id, "field": "revision", "previous": head, "value": n, "actor": actor(q, "operator"), "at": nowMs()}
	if kind == "project" {
		change["projectId"] = id
	}
	if err := put(q, "setting-change", change); err != nil {
		return contract.Revision{}, err
	}
	invalidateEnvCacheOnCommit(q)
	xs, err := definitionRows(q, "d.kind=$1 AND d.id=$2", n, kind, id)
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
		return out, &HTTPError{Code: 409, Msg: "Invalid content: " + ps[0].Message, Extra: map[string]any{"reason": contract.ConflictReasonInvalidDraft, "head": head.Head, "repoSha": head.RepoSha}}
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
		return out, &HTTPError{Code: 409, Msg: "Invalid content: " + ps[0].Message, Extra: map[string]any{"reason": contract.ConflictReasonInvalidDraft, "head": 0, "repoSha": nil}}
	}
	if strings.TrimSpace(r.Message) == "" {
		return out, fail(400, "Expected message")
	}
	tag, err := q.tx.Exec(q.ctx(), "INSERT INTO definition(kind,id,path,head) VALUES($1,$2,$3,0) ON CONFLICT DO NOTHING", r.Kind, r.Id, definitionPath(string(r.Kind), r.Id))
	if err != nil {
		return out, err
	}
	if tag.RowsAffected() == 0 {
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
	if err := q.tx.QueryRow(q.ctx(), `SELECT count(*) FROM execution WHERE state IN ('preparing','running','waiting')
	  AND (definitions @> jsonb_build_array(jsonb_build_object('kind',$1::text,'id',$2::text))
	   OR ($1='pipeline' AND (pipeline_id=$2 OR data->>'pipelineId'=$2)) OR ($1='agent' AND (profile_id=$2 OR config_ref->>'profileId'=$2)))`, r.Kind, r.Id).Scan(&out.Running); err != nil {
		return out, err
	}
	// A live experiment names it as its pipeline, or anywhere in its variants (a method's pipelineId, a profile
	// override) or evaluators: a JSON string equal to the id.
	rs, err := queryRows(q, `SELECT id FROM experiment WHERE state IN ('draft','running','paused') AND tombstoned_at IS NULL AND (pipeline_id=$1
	  OR jsonb_path_exists(variants, '$.** ? (@ == $id)', jsonb_build_object('id',$1::text))
	  OR jsonb_path_exists(evaluators, '$.** ? (@ == $id)', jsonb_build_object('id',$1::text))) ORDER BY id`, r.Id)
	if err != nil {
		return out, err
	}
	for _, x := range rs {
		out.Experiments = append(out.Experiments, astr(x["id"]))
	}
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
		if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM session WHERE payload->'agentConfig'->>'id'=$1 AND archived_at IS NULL AND tombstoned_at IS NULL", r.Id).Scan(&sessions); err != nil {
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
	if err := exec(q, "UPDATE definition SET deleted_at=now(),retired_at=coalesce(retired_at,now()) WHERE kind=$1 AND id=$2", r.Kind, r.Id); err != nil {
		return out, err
	}
	if err := exec(q, "DELETE FROM definition_draft WHERE kind=$1 AND id=$2", r.Kind, r.Id); err != nil {
		return out, err
	}
	if err := put(q, "setting-change", doc{"id": newID(), "target": "definition:" + string(r.Kind) + "/" + r.Id, "field": "deletedAt", "previous": nil, "value": nowMs(),
		"actor": actor(q, "operator"), "at": nowMs(), "note": js(doc{"state": out.State, "head": head.Head})}); err != nil {
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
	rs, err := q.tx.Query(q.ctx(), "SELECT "+definitionCols+` FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id
	  WHERE d.kind=$1 AND d.id=$2 AND r.revision<$3 ORDER BY r.revision DESC LIMIT $4`, string(r.Kind), r.Id, before, limit)
	if err != nil {
		return out, err
	}
	xs, err := pgx.CollectRows(rs, scanDefinition)
	for _, x := range xs {
		out.Revisions = append(out.Revisions, x.revisionView(q))
	}
	return out, err
}

// definitionExport: one patch against the running release's config/ for every ahead or store-only definition (or the
// named ones). A repository-changed or repository-deleted one is never exported, so an export cannot revert a release.
func definitionExport(q *Request, r *contract.DefinitionExportRequest) (contract.DefinitionExportReply, error) {
	out := contract.DefinitionExportReply{Files: []contract.ExportFile{}, Covers: []contract.DefinitionRef{}}
	xs, err := definitionRows(q, "d.retired_at IS NULL", 0)
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
