// Watch mode (docs/designs/fleet-pm-agent.md §5): a session subscribes to the change log of its focus; a single-flight
// ticker turns foreign changes and a stored timer into ordinary queued messages to that session. No broker, no caps.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var watchKinds = []string{"task", "question", "execution", "host"}

func watchSet(q *Request, b doc) (doc, error) {
	sid, err := strArg(b, "sessionId", "sessionId", 0)
	if err != nil {
		return nil, err
	}
	if _, err := get(q, sid, "session"); err != nil {
		return nil, err
	}
	kinds := strList(b["kinds"])
	if xs, _ := b["kinds"].([]any); len(kinds) == 0 || len(kinds) != len(xs) {
		return nil, fail(400, "Expected kinds: task, question, execution, host")
	}
	for _, k := range kinds {
		if !slices.Contains(watchKinds, k) {
			return nil, fail(400, "Unknown kind "+k+": use task, question, execution, host")
		}
	}
	old, err := maybe(q, "watch:"+sid, "watch")
	if err != nil {
		return nil, err
	}
	w := doc{"id": "watch:" + sid, "sessionId": sid, "kinds": kinds, "wakes": 0}
	if old != nil {
		w["wakes"], w["lastWakeAt"], w["cursor"] = old["wakes"], old["lastWakeAt"], old["cursor"]
	}
	if w["cursor"] == nil { // a new watch starts from now
		if w["cursor"], err = head(q.ctx(), q.tx); err != nil {
			return nil, err
		}
	}
	if t, ok := b["timerMs"]; ok && t != nil {
		ms := num(t)
		if ms <= 0 {
			return nil, fail(400, "Expected timerMs > 0")
		}
		w["timerMs"], w["nextWakeAt"] = ms, time.Now().UnixMilli()+int64(ms)
	}
	return w, putDoc(q, "watch", w)
}

func init() {
	agentOp("watch_set", false, watchSet)
	agentOp("watch_stop", false, func(q *Request, b doc) (doc, error) {
		sid, err := strArg(b, "sessionId", "sessionId", 0)
		if err != nil {
			return nil, err
		}
		return doc{"sessionId": sid, "stopped": true}, exec(q, "DELETE FROM document WHERE kind='watch' AND id=$1", "watch:"+sid)
	})
	agentOp("watch_get", true, func(q *Request, b doc) (doc, error) {
		sid, err := strArg(b, "sessionId", "sessionId", 0)
		if err != nil {
			return nil, err
		}
		w, err := maybe(q, "watch:"+sid, "watch")
		return doc{"watch": w}, err
	})
	agentDescriptions["watch_set"] = "Watch your focus: kinds (task, question, execution, host) and optional timerMs; Fleet sends you one message per batch of foreign changes and on the timer"
	agentDescriptions["watch_stop"] = "Stop watching"
	agentDescriptions["watch_get"] = "Your watch: kinds, timer, wakes, last wake, last error"
	onStart(func(ctx context.Context, a *App) {
		every(ctx, cEnvMs("FLEET_WATCH_TICK_MS", 15000), func() { watchPass(ctx, a) })
	})
}

// watchPass: one pass over every watch, each in its own write transaction (§5 steps 1-9).
func watchPass(ctx context.Context, a *App) {
	var ids []string
	if err := a.store.Tx(ctx, nil, false, "", func(tx pgx.Tx) error {
		rs, err := tx.Query(ctx, "SELECT id FROM document WHERE kind='watch' ORDER BY seq")
		if err == nil {
			ids, err = pgx.CollectRows(rs, pgx.RowTo[string])
		}
		return err
	}); err != nil {
		log.Printf("watch: %v", err)
		return
	}
	for _, id := range ids {
		err := a.store.Tx(ctx, nil, true, "", func(tx pgx.Tx) error {
			return watchOne(&Request{app: a, tx: tx, r: (&http.Request{}).WithContext(ctx)}, id)
		})
		if err != nil {
			a.store.Tx(ctx, nil, true, "", func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "UPDATE document SET data=data||jsonb_build_object('lastError',$2::text) WHERE kind='watch' AND id=$1", id, err.Error())
				return err
			})
		}
	}
}

func watchOne(q *Request, wid string) error {
	w, err := maybe(q, wid, "watch")
	if err != nil || w == nil {
		return err
	}
	id := gs(w, "sessionId")
	s, err := maybe(q, id, "session")
	if err != nil {
		return err
	}
	if s == nil {
		return exec(q, "DELETE FROM document WHERE kind='watch' AND id=$1", wid)
	}
	// PM busy: a wake still queued covers everything; keep the cursor (step 6).
	if busy, err := has(q, "SELECT 1 FROM document WHERE kind='queued-message' AND data->>'sessionId'=$1 AND data->>'state'='queued' AND (data->>'requestId' LIKE 'watch:%' OR data->>'requestId' LIKE 'timer:%')", id); err != nil || busy {
		return err
	}
	dir := q.app.people[firstString(s["directedBy"], ownerEmail(q))]
	if dir == nil {
		return fail(403, "Director of the watching session is not a person of this Fleet")
	}
	own := map[string]bool{}
	es, err := rowsOf(q, "SELECT id FROM execution WHERE session_id=$1", id)
	if err != nil {
		return err
	}
	for _, e := range es {
		own["execution:"+gs(e, "id")] = true
	}
	children := map[string]bool{}
	cs, err := rowsOf(q, "SELECT id FROM document WHERE kind='session' AND data->>'createdBySessionId'=$1", id)
	if err != nil {
		return err
	}
	for _, c := range cs {
		children[gs(c, "id")] = true
	}
	kinds, focus := strList(w["kinds"]), focusIDs(s)
	cursor := int64(num(w["cursor"]))
	start := cursor
	parts, seen := []string{}, map[string]bool{}
	for {
		page, next, resync, _, err := since(q.ctx(), q.tx, cursor, 500)
		if err != nil {
			return err
		}
		if resync {
			cursor = next
			parts = append(parts, "change history after your last wake was pruned; re-read your focus with task_page")
			break
		}
		for _, c := range page {
			p := ""
			if c.ProjectID != nil {
				p = *c.ProjectID
			}
			if !slices.Contains(kinds, c.Kind) || seen[c.Kind+c.ID] ||
				(c.Actor != nil && own[*c.Actor]) || (c.SessionID != nil && (children[*c.SessionID] || *c.SessionID == id)) ||
				(focus != nil && !slices.Contains(focus, p)) || (!dir.Owner && !mayAccessProjectID(dir, p)) {
				continue
			}
			seen[c.Kind+c.ID] = true
			if t, err := classify(q, c); err != nil {
				return err
			} else if t != "" {
				parts = append(parts, t)
			}
		}
		cursor = next
		if len(page) < 500 {
			break
		}
	}
	w["cursor"] = cursor
	nowMs := time.Now().UnixMilli()
	send := func(rid, text string) error {
		if _, err := submit(q, doc{"sessionId": id, "requestId": rid, "text": text}); err != nil {
			return err
		}
		w["wakes"], w["lastWakeAt"] = num(w["wakes"])+1, nowMs
		return nil
	}
	if len(parts) > 0 {
		if err := send(fmt.Sprintf("watch:%s:%d", id, start), fmt.Sprintf("Fleet: %d changes in your focus: %s", len(parts), strings.Join(parts, "; "))); err != nil {
			return err
		}
	}
	if at := int64(num(w["nextWakeAt"])); w["timerMs"] != nil && at <= nowMs {
		if err := send(fmt.Sprintf("timer:%s:%d", id, at), "Fleet: watch timer; review your focus with task_page and questions_list"); err != nil {
			return err
		}
		w["nextWakeAt"] = nowMs + int64(num(w["timerMs"]))
	}
	delete(w, "lastError")
	return putDoc(q, "watch", w)
}

// classify: one change by its current row; "" drops it (§5 table).
func classify(q *Request, c change) (string, error) {
	switch c.Kind {
	case "task":
		t, err := first(q, "SELECT state::text AS state,pause_reason FROM task WHERE id=$1", c.ID)
		if err != nil || t == nil {
			return "", err
		}
		st := gs(t, "state")
		if gs(t, "pause_reason") == "held" {
			st = "held"
		}
		return "task " + c.ID + " now " + st, nil
	case "question":
		if c.Op != "I" {
			return "", nil
		}
		r, err := first(q, "SELECT task_id FROM question WHERE id=$1 AND state='open'", c.ID)
		if err != nil || r == nil {
			return "", err
		}
		return "question " + c.ID + " on task " + firstString(r["task_id"], "none"), nil
	case "execution":
		r, err := first(q, "SELECT 1 AS x FROM execution WHERE id=$1 AND state='failed'", c.ID)
		if err != nil || r == nil {
			return "", err
		}
		return "execution " + c.ID + " failed", nil
	case "host":
		return "host " + c.ID + " changed", nil
	}
	return "", nil
}
