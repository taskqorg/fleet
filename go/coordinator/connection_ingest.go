// Gateway spool ingestion, call recording, transfer completion, and key accounting.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
	"github.com/jackc/pgx/v5"
)

var ingestMu sync.Mutex

// Spool ingest (§3): every gateway line becomes one tool_run with connection_cost, idempotent by callId. Each
// unrecorded line runs in its own transaction locked only by "usageKey:" + key (or NOLOCK when no usageKey), never
// acquiring GLOBAL. A past day's file is deleted once its lines are committed; a file with a line that could not be
// imported is kept.
func connIngestPass(ctx context.Context, a *App) (added int, err error) {
	if gatewayDir() == "" {
		return 0, nil
	}
	ingestMu.Lock()
	defer ingestMu.Unlock()
	dir := filepath.Join(gatewayDir(), "state", "spool")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, nil
	}
	// A spool pass publishes one change per scope. Tool rows themselves remain silent.
	var changed []string
	defer func() {
		if len(changed) == 0 {
			return
		}
		publishErr := a.store.Write(ctx, nil, nil, func(q *Request) error {
			return q.db().ListSpendWakeGateway(q.ctx(), changed)
		})
		err = errors.Join(err, publishErr)
	}()
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
		path := filepath.Join(dir, f)
		b, err := os.ReadFile(path)
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
			if err := a.store.Read(ctx, nil, func(q *Request) error {
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
				if err := a.store.Write(ctx, nil, nil, func(q *Request) error { return connComplete(q, l) }); err != nil {
					return added, err
				}
				changed = append(changed, "call:"+l.CallId)
				continue
			}
			if recorded {
				continue
			}
			var locks []string
			if l.UsageKey != nil && *l.UsageKey != "" {
				locks = []string{"usageKey:" + *l.UsageKey}
			}
			var ok bool
			if err := a.store.Write(ctx, nil, locks, func(q *Request) error {
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
				changed = append(changed, "call:"+l.CallId)
			}
		}
		if past && unimported > 0 {
			log.Printf("spool %s: past day kept, %d line(s) not imported", f, unimported)
		} else if past {
			// Keep a newly created empty file (e.g. open(O_CREAT) before write) and any file that grew while imported.
			if info, err := os.Stat(path); err == nil && info.Size() == int64(len(b)) && (len(parsed) > 0 || time.Since(info.ModTime()) >= time.Minute) {
				os.Remove(path)
			}
		}
	}
	return added, nil
}

// callData: a call row's data (tool_run) from its gateway line and run token.
type callData struct {
	Tool                    string                 `json:"tool"`
	Connection              string                 `json:"connection"`
	Outcome                 contract.CallOutcome   `json:"outcome"`
	ErrorCode               *string                `json:"errorCode"`
	Status                  int64                  `json:"status"`
	KeyFingerprint          *string                `json:"keyFingerprint"`
	Usage                   *contract.CallUsage    `json:"usage"`
	DirectedBy              *string                `json:"directedBy"`
	StartedAt               int64                  `json:"startedAt"`
	Ms                      int64                  `json:"ms"`
	Transfer                *contract.CallTransfer `json:"transfer,omitempty"`
	Phase                   *contract.CallPhase    `json:"phase,omitempty"`
	Detail                  *string                `json:"detail,omitempty"` // project access: what the call read (audit)
	UploadAccounted         bool                   `json:"uploadAccounted,omitempty"`
	TokenName               *string                `json:"tokenName,omitempty"`
	PipelineRunID           *string                `json:"pipelineRunId,omitempty"`
	DirectorAccessRevokedAt json.RawMessage        `json:"directorAccessRevokedAt,omitempty"`
}

// callExecution: a call row as insertExecution (store.go) takes it.
type callExecution struct {
	ID                string   `json:"id"`
	Kind              string   `json:"kind"`
	State             string   `json:"state"`
	ProjectID         *string  `json:"projectId"`
	ParentExecutionID *string  `json:"parentExecutionId"`
	ExperimentID      *string  `json:"experimentId"`
	TaskID            *string  `json:"taskId"`
	SessionID         *string  `json:"sessionId"`
	DirectedBy        *string  `json:"directedBy"`
	StartedAt         *int64   `json:"startedAt"`
	EndedAt           *int64   `json:"endedAt"`
	PipelineID        *string  `json:"pipelineId"`
	PipelineRevision  *int64   `json:"pipelineRevision"`
	StepID            *string  `json:"stepId"`
	ProfileID         *string  `json:"profileId"`
	ProfileRevision   *int64   `json:"profileRevision"`
	SystemActor       *string  `json:"systemActor"`
	Data              callData `json:"data"`
}

func connRecord(q *Request, l contract.CallLine) (bool, error) {
	if l.CallId == "" || l.TokenSha256 == nil || l.ExecutionId == nil {
		return false, nil
	}
	t, err := runTokenMaybe(q, "run-token:"+*l.TokenSha256)
	if err != nil || t == nil {
		return false, err
	}
	id := "call:" + l.CallId
	state := "failed"
	if l.Outcome == contract.CallOutcomeOk {
		state = "succeeded"
	}
	data := callData{Tool: l.Tool, Connection: l.Connection, Outcome: l.Outcome, ErrorCode: l.ErrorCode, Status: l.Status,
		KeyFingerprint: l.KeyFingerprint, Usage: l.Usage, DirectedBy: t.DirectedBy, StartedAt: l.StartedAt, Ms: l.Ms, Transfer: l.Transfer,
		Phase: l.Phase, TokenName: t.Personal, PipelineRunID: t.PipelineRunID, Detail: l.Detail}
	if l.Phase != nil && *l.Phase == contract.CallPhaseCompleted && isUpload(l) {
		data.UploadAccounted = true
	}
	parent := t.ExecutionID
	if t.Personal != nil {
		parent = nil
	}
	var pr db.ConnCallParentRow
	if pid := deref(parent); pid != "" {
		var err error
		if pr, err = q.db().ConnCallParent(q.ctx(), pid); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}
	var startedAt, endedAt *int64
	if l.StartedAt > 0 {
		start, end := l.StartedAt, l.StartedAt+max(l.Ms, 0)
		startedAt, endedAt = &start, &end
	}
	director := firstString(deref(t.DirectedBy), deref(pr.DirectedBy))
	if deref(t.ProjectID) == "" && pr.SessionID == nil {
		if !isNull(t.DirectorAccessRevokedAt) {
			data.DirectorAccessRevokedAt = t.DirectorAccessRevokedAt
		} else if director != "" {
			active, err := q.db().ConnPersonExists(q.ctx(), director)
			if err != nil {
				return false, err
			}
			if !active {
				data.DirectorAccessRevokedAt = jsonBytes(nowMs())
			}
		}
	}
	var directedBy *string
	if director != "" {
		directedBy = &director
	}
	inserted, err := insertExecution(q, callExecution{ID: id, Kind: "tool_run", State: state, ProjectID: t.ProjectID, ParentExecutionID: parent,
		ExperimentID: t.ExperimentID, TaskID: pr.TaskID, SessionID: pr.SessionID, DirectedBy: directedBy, StartedAt: startedAt, EndedAt: endedAt,
		PipelineID: pr.PipelineID, PipelineRevision: pr.PipelineRevision, StepID: pr.StepID, ProfileID: pr.ProfileID, ProfileRevision: pr.ProfileRevision,
		SystemActor: t.SystemActor, Data: data})
	if err != nil || !inserted {
		return false, err
	}
	if l.Outcome != contract.CallOutcomeOk {
		if err := q.db().ConnCallProblem(q.ctx(), db.ConnCallProblemParams{ID: id, Problem: problemOf(string(l.Outcome), deref(l.ErrorCode))}); err != nil {
			return false, err
		}
		if err := ingestCallGap(q, id, parent, t.ProjectID, startedAt, l); err != nil {
			return false, err
		}
	}
	if err := connAccount(q, id, l, t.Step); err != nil {
		return false, err
	}
	if t.Personal != nil {
		if err := tokenCallNote(q, t.ID, l); err != nil {
			return false, err
		}
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
		// The cost names its amount under its unit as well (a JSON object keyed by the provider's unit).
		cost = map[string]json.RawMessage{"unit": jsonBytes(u.Unit), u.Unit: jsonBytes(amount), "amount": jsonBytes(amount), "connection": jsonBytes(l.Connection)}
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
		k, err := connKeyMaybe(q, "key:"+p.Key)
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
			if k.EnabledAt != nil && int64(*k.EnabledAt) >= at {
				return nil
			}
			k.Problem = &keyProblem{Status: p.Status, Message: msg, At: at}
			if err := connKeyPut(q, *k); err != nil {
				return err
			}
		}
	}
	return nil
}

func isUpload(l contract.CallLine) bool {
	return l.Transfer != nil && l.Transfer.Direction == contract.CallTransferDirectionUpload
}

// transferPatch: what a transfer's completed line writes over its started row's data; an upload also writes its key,
// usage (null when the line has none) and that it is accounted.
type transferPatch struct {
	Outcome         contract.CallOutcome   `json:"outcome"`
	Status          int64                  `json:"status"`
	ErrorCode       *string                `json:"errorCode"`
	Ms              int64                  `json:"ms"`
	Phase           contract.CallPhase     `json:"phase"`
	Transfer        *contract.CallTransfer `json:"transfer,omitempty"`
	KeyFingerprint  json.RawMessage        `json:"keyFingerprint,omitempty"`
	Usage           json.RawMessage        `json:"usage,omitempty"`
	UploadAccounted bool                   `json:"uploadAccounted,omitempty"`
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
	patch := transferPatch{Outcome: l.Outcome, Status: l.Status, ErrorCode: l.ErrorCode, Ms: ms, Phase: contract.CallPhaseCompleted, Transfer: l.Transfer}
	upload := isUpload(l)
	if upload {
		patch.KeyFingerprint, patch.Usage, patch.UploadAccounted = jsonBytes(l.KeyFingerprint), jsonBytes(l.Usage), true
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
	k, err := connKeyMaybe(q, "key:"+fp)
	if err != nil || k == nil || k.State == "retired" || k.State == "disabled" {
		return err
	}
	if k.EnabledAt != nil && attemptedAt > 0 && int64(*k.EnabledAt) > attemptedAt {
		return nil
	}
	st := fmt.Sprint(status)
	reason, now := "provider answered HTTP "+st, nowMs()
	k.State, k.DisabledReason, k.DisabledAt = "disabled", &reason, &now
	if err := connKeyPut(q, *k); err != nil {
		return err
	}
	publish(q)
	return problemRaise(q, "", Problem{Cause: "key:" + fp + ":disabled",
		Text: fmt.Sprintf("API key %s of %v was refused by the provider (HTTP %s) and is disabled. Re-enable it on the API keys screen once it works again, or retire it.", fp, k.ConnectionID, st)})
}
