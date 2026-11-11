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

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

var watchKinds = []string{"task", "question", "execution", "host"}

func watchSet(q *Request, b doc) (doc, error) {
	sid, err := strArg(b, "sessionId", "sessionId", 0)
	if err != nil {
		return nil, err
	}
	if s, err := get(q, sid, "session"); err != nil {
		return nil, err
	} else if err := directs(q, s); err != nil {
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
	return w, put(q, "watch", w)
}

func init() {
	agentTypedOp("watch_set", false, func(q *Request, b *contract.WatchSetRequest) (contract.WatchRecord, error) {
		w, err := watchSet(q, docOf(b))
		if err != nil {
			return contract.WatchRecord{}, err
		}
		return asT[contract.WatchRecord](w)
	})
	agentTypedOp("watch_stop", false, func(q *Request, b *contract.WatchStopRequest) (contract.WatchStopReply, error) {
		sid := b.SessionId
		if sid == "" {
			return contract.WatchStopReply{}, fail(400, "Expected sessionId")
		}
		if s, err := get(q, sid, "session"); err != nil {
			return contract.WatchStopReply{}, err
		} else if err := directs(q, s); err != nil {
			return contract.WatchStopReply{}, err
		}
		return contract.WatchStopReply{SessionId: sid, Stopped: true}, exec(q, "DELETE FROM document WHERE kind='watch' AND id=$1", "watch:"+sid)
	})
	agentTypedOp("watch_get", true, func(q *Request, b *contract.WatchGetRequest) (contract.WatchGetReply, error) {
		if b.SessionId == "" {
			return contract.WatchGetReply{}, fail(400, "Expected sessionId")
		}
		w, err := maybe(q, "watch:"+b.SessionId, "watch")
		if err != nil || w == nil {
			return contract.WatchGetReply{}, err
		}
		x, err := asT[contract.WatchRecord](w)
		return contract.WatchGetReply{Watch: &x}, err
	})
	agentDescriptions["watch_set"] = "Watch your focus: kinds (task, question, execution, host) and optional timerMs; Fleet sends you one message per batch of foreign changes and on the timer"
	agentDescriptions["watch_stop"] = "Stop watching"
	agentDescriptions["watch_get"] = "Your watch: kinds, timer, wakes, last wake, last error"
	onStart(func(ctx context.Context, a *App) {
		every(ctx, envMs("FLEET_WATCH_TICK_MS", 15000), func() { watchPass(ctx, a) })
	})
}

// watchPass: one pass over every watch, each in its own write transaction (§5 steps 1-9).
// ponytail: each watch takes the GLOBAL write lock briefly every tick; a read-only pre-check first if many watches show.
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
	id := astr(w["sessionId"])
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
		own["execution:"+astr(e["id"])] = true
	}
	children := map[string]bool{}
	cs, err := rowsOf(q, "SELECT id FROM document WHERE kind='session' AND data->>'createdBySessionId'=$1", id)
	if err != nil {
		return err
	}
	for _, c := range cs {
		children[astr(c["id"])] = true
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
			if !slices.Contains(kinds, string(c.Kind)) || seen[string(c.Kind)+c.Id] ||
				(c.Actor != nil && own[*c.Actor]) || (c.SessionId != nil && (children[*c.SessionId] || *c.SessionId == id)) ||
				(focus != nil && c.Kind != "host" && !slices.Contains(focus, p)) || (!fullAccess(dir) && !mayAccessProjectID(dir, p)) {
				continue
			}
			seen[string(c.Kind)+c.Id] = true
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
	nowMs := time.Now().UnixMilli()
	timer := w["timerMs"] != nil && int64(num(w["nextWakeAt"])) <= nowMs
	if len(parts) == 0 && !timer && cursor == start {
		return nil // nothing to send or move: no write
	}
	w["cursor"] = cursor
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
	if timer && len(parts) > 0 { // the change wake also serves as the timer wake
		w["nextWakeAt"] = nowMs + int64(num(w["timerMs"]))
	} else if at := int64(num(w["nextWakeAt"])); timer {
		if err := send(fmt.Sprintf("timer:%s:%d", id, at), "Fleet: watch timer; review your focus with task_page and questions_list"); err != nil {
			return err
		}
		w["nextWakeAt"] = nowMs + int64(num(w["timerMs"]))
	}
	delete(w, "lastError")
	return put(q, "watch", w)
}

// classify: one change by its current row; "" drops it (§5 table).
func classify(q *Request, c change) (string, error) {
	switch c.Kind {
	case "task":
		t, err := first(q, "SELECT state::text AS state,pause_reason FROM task WHERE id=$1", c.Id)
		if err != nil || t == nil {
			return "", err
		}
		st := astr(t["state"])
		if astr(t["pause_reason"]) == "held" {
			st = "held"
		}
		return "task " + c.Id + " now " + st, nil
	case "question":
		if c.Op != "I" {
			return "", nil
		}
		r, err := first(q, "SELECT task_id FROM question WHERE id=$1 AND state='open'", c.Id)
		if err != nil || r == nil {
			return "", err
		}
		return "question " + c.Id + " on task " + firstString(r["task_id"], "none"), nil
	case "execution":
		r, err := first(q, "SELECT CASE WHEN state='failed' THEN 'failed' ELSE 'unknown' END AS s FROM execution WHERE id=$1 AND (state='failed' OR observation='unconfirmed')", c.Id)
		if err != nil || r == nil {
			return "", err
		}
		return "execution " + c.Id + " " + astr(r["s"]), nil
	case "host": // only reported problems (§5 table)
		r, err := first(q, "SELECT 1 AS x FROM document WHERE kind='host-observation' AND id=$1 AND jsonb_array_length(coalesce(data->'problems','[]'))>0", c.Id)
		if err != nil || r == nil {
			return "", err
		}
		return "host " + c.Id + " reports problems", nil
	}
	return "", nil
}
