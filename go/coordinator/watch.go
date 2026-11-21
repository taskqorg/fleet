// Watch mode (docs/designs/fleet-pm-agent.md §5): a session subscribes to the change log of its focus; a single-flight
// ticker turns foreign changes and a stored timer into ordinary queued messages to that session. No broker, no caps.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fleet/contract"
	"fleet/coordinator/db"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var watchKinds = []string{"task", "question", "execution", "host", "limits"}

// watchPayload: a watch row's payload, the record fields that are not columns (session and project are).
type watchPayload struct {
	Kinds          []string        `json:"kinds"`
	Wakes          int64           `json:"wakes"`
	LastWakeAt     json.RawMessage `json:"lastWakeAt,omitempty"`
	Cursor         int64           `json:"cursor"`
	TimerMs        *float64        `json:"timerMs,omitempty"`
	NextWakeAt     *int64          `json:"nextWakeAt,omitempty"`
	LimitsSignaled []string        `json:"limitsSignaled,omitempty"`
	LastError      *string         `json:"lastError,omitempty"`
}

// watchRow: one watch, its columns and payload.
type watchRow struct {
	ID, SessionID string
	ProjectID     *string
	watchPayload
}

func (w watchRow) record() contract.WatchRecord {
	return contract.WatchRecord{Id: w.ID, SessionId: w.SessionID, Kinds: w.Kinds, Wakes: w.Wakes, LastWakeAt: w.LastWakeAt, Cursor: w.Cursor,
		TimerMs: w.TimerMs, NextWakeAt: w.NextWakeAt, LastError: w.LastError}
}

// watchOf: the watch of id, nil when there is none.
func watchOf(q *Request, id string) (*watchRow, error) {
	r, err := q.db().WatchRecordOf(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w := &watchRow{ID: id, SessionID: r.SessionID, ProjectID: r.ProjectID}
	if err := json.Unmarshal(r.Payload, &w.watchPayload); err != nil {
		return nil, err
	}
	return w, nil
}

func watchPut(q *Request, w *watchRow) error {
	return q.db().WatchPut(q.ctx(), db.WatchPutParams{ID: w.ID, SessionID: w.SessionID, ProjectID: w.ProjectID, Payload: jsonBytes(w.watchPayload)})
}

// watchSession: the session fields a watch reads; deliveryId and focus are payload fields (queries/records.sql "session").
type watchSession struct {
	ProjectID, TaskID, DirectedBy *string         `json:"-"`
	DeliveryID                    any             `json:"deliveryId"`
	Focus                         json.RawMessage `json:"focus"`
}

// watchSessionOf: the session sid, nil when it is not visible.
func watchSessionOf(q *Request, sid string) (*watchSession, error) {
	r, err := q.db().WatchSession(q.ctx(), sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := &watchSession{}
	if err := json.Unmarshal(r.Payload, s); err != nil {
		return nil, err
	}
	s.ProjectID, s.TaskID, s.DirectedBy = r.ProjectID, r.TaskID, r.DirectedBy
	return s, nil
}

// task: the task the session works on (its delivery, else its task), "" for none.
func (s *watchSession) task() string {
	if s.TaskID != nil {
		return firstString(s.DeliveryID, *s.TaskID)
	}
	return firstString(s.DeliveryID)
}

// directs: as directs (agent.go) for a session record: the caller may act on the session.
func (s *watchSession) directs(q *Request) error {
	if q.agent != "" || q.person == nil || fullAccess(q.person) || deref(s.DirectedBy) == q.person.Email {
		return nil
	}
	return fail(403, "Session is directed by someone else")
}

// focus: the projects of the session's focus, nil for every project (no focus, or projectIds "*"), as focusIDs (agent.go).
func (s *watchSession) focus() []string {
	raw := bytes.TrimSpace(s.Focus)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var f struct {
		ProjectIDs json.RawMessage `json:"projectIds"`
	}
	json.Unmarshal(raw, &f)
	var star string
	if json.Unmarshal(f.ProjectIDs, &star) == nil && star == "*" {
		return nil
	}
	out := []string{}
	var xs []json.RawMessage
	json.Unmarshal(f.ProjectIDs, &xs)
	for _, x := range xs {
		var id string
		if string(x) != "null" && json.Unmarshal(x, &id) == nil {
			out = append(out, id)
		}
	}
	return out
}

func watchSet(q *Request, b *contract.WatchSetRequest) (contract.WatchRecord, error) {
	sid := b.SessionId
	if sid == "" {
		return contract.WatchRecord{}, fail(400, "Expected sessionId")
	}
	s, err := watchSessionOf(q, sid)
	if err == nil && s == nil {
		err = errNotFound
	}
	if err != nil {
		return contract.WatchRecord{}, err
	} else if err := s.directs(q); err != nil {
		return contract.WatchRecord{}, err
	}
	if err := taskAllowsWork(q, s.task()); err != nil {
		return contract.WatchRecord{}, err
	}
	if len(b.Kinds) == 0 {
		return contract.WatchRecord{}, fail(400, "Expected kinds: task, question, execution, host, limits")
	}
	for _, k := range b.Kinds {
		if !slices.Contains(watchKinds, k) {
			return contract.WatchRecord{}, fail(400, "Unknown kind "+k+": use task, question, execution, host, limits")
		}
	}
	old, err := watchOf(q, "watch:"+sid)
	if err != nil {
		return contract.WatchRecord{}, err
	}
	w := &watchRow{ID: "watch:" + sid, SessionID: sid, ProjectID: s.ProjectID, watchPayload: watchPayload{Kinds: b.Kinds}}
	if old != nil {
		w.Wakes, w.LastWakeAt, w.Cursor = old.Wakes, old.LastWakeAt, old.Cursor
		if w.LastWakeAt == nil { // the record carried the old watch's missing last wake as null
			w.LastWakeAt = json.RawMessage("null")
		}
	} else if w.Cursor, err = head(q.ctx(), q.tx); err != nil { // a new watch starts from now
		return contract.WatchRecord{}, err
	}
	if t := b.TimerMs; t != nil {
		if *t <= 0 {
			return contract.WatchRecord{}, fail(400, "Expected timerMs > 0")
		}
		next := time.Now().UnixMilli() + int64(*t)
		w.TimerMs, w.NextWakeAt = t, &next
	}
	return w.record(), watchPut(q, w)
}

func init() {
	op("watch_set", watchSet)
	op("watch_stop", func(q *Request, b *contract.WatchStopRequest) (contract.WatchStopReply, error) {
		sid := b.SessionId
		if sid == "" {
			return contract.WatchStopReply{}, fail(400, "Expected sessionId")
		}
		if s, err := watchSessionOf(q, sid); err != nil {
			return contract.WatchStopReply{}, err
		} else if s == nil {
			return contract.WatchStopReply{}, errNotFound
		} else if err := s.directs(q); err != nil {
			return contract.WatchStopReply{}, err
		}
		return contract.WatchStopReply{SessionId: sid, Stopped: true}, q.db().DeleteWatch(q.ctx(), "watch:"+sid)
	})
	op("watch_get", func(q *Request, b *contract.WatchGetRequest) (contract.WatchGetReply, error) {
		if b.SessionId == "" {
			return contract.WatchGetReply{}, fail(400, "Expected sessionId")
		}
		w, err := watchOf(q, "watch:"+b.SessionId)
		if err != nil || w == nil {
			return contract.WatchGetReply{}, err
		}
		x := w.record()
		return contract.WatchGetReply{Watch: &x}, nil
	})
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "watch", envMs("FLEET_WATCH_TICK_MS", 15000), func(ctx context.Context) error { watchPass(ctx, a); return nil })
	})
}

// watchPass: one pass over every watch, each in its own write transaction (§5 steps 1-9).
// ponytail: each watch takes the GLOBAL write lock briefly every tick; a read-only pre-check first if many watches show.
func watchPass(ctx context.Context, a *App) {
	var ids []string
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		var err error
		ids, err = q.db().WatchIDs(ctx)
		return err
	}); err != nil {
		log.Printf("watch: %v", err)
		return
	}
	for _, id := range ids {
		err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
			return watchOne(q, id)
		})
		if err != nil {
			a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
				return q.db().WatchSetError(ctx, db.WatchSetErrorParams{Error: err.Error(), ID: id})
			})
		}
	}
}

func watchOne(q *Request, wid string) error {
	w, err := watchOf(q, wid)
	if err != nil || w == nil {
		return err
	}
	id := w.SessionID
	s, err := watchSessionOf(q, id)
	if err != nil {
		return err
	}
	cancelled := false
	if s != nil {
		if task := s.task(); task != "" {
			if cancelled, err = q.db().TaskCancelled(q.ctx(), task); err != nil {
				return err
			}
		}
	}
	if s == nil || cancelled {
		return q.db().DeleteWatch(q.ctx(), wid)
	}
	// PM busy: a wake still queued covers everything; keep the cursor (step 6).
	if busy, err := q.db().WatchWakeQueued(q.ctx(), id); err != nil || busy {
		return err
	}
	dir := q.peopleSnapshot().person(firstString(deref(s.DirectedBy), ownerEmail(q)))
	if dir == nil {
		return fail(403, "Director of the watching session is not a person of this Fleet")
	}
	own := map[string]bool{}
	es, err := q.db().SessionExecutionIDs(q.ctx(), &id)
	if err != nil {
		return err
	}
	for _, e := range es {
		own["execution:"+e] = true
	}
	children := map[string]bool{}
	cs, err := q.db().ChildSessionIDs(q.ctx(), &id)
	if err != nil {
		return err
	}
	for _, c := range cs {
		children[c] = true
	}
	kinds, focus := w.Kinds, s.focus()
	cursor := w.Cursor
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
	var limits []string
	if slices.Contains(kinds, "limits") {
		signals, keys, err := limitSignals(q, dir, w.LimitsSignaled)
		if err != nil {
			log.Printf("watch %s: limits: %v", wid, err) // a limits read problem never blocks the other wakes
		} else if len(signals) > 0 {
			limits = signals
			w.LimitsSignaled = keys
		}
	}
	nowMs := time.Now().UnixMilli()
	nextWakeAt := int64(0)
	if w.NextWakeAt != nil {
		nextWakeAt = *w.NextWakeAt
	}
	timer := w.TimerMs != nil && nextWakeAt <= nowMs
	if len(parts) == 0 && len(limits) == 0 && !timer && cursor == start {
		return nil // nothing to send or move: no write
	}
	w.Cursor = cursor
	send := func(rid, text string) error {
		if _, err := agentSubmit(q, &contract.MessageSendRequest{SessionId: &id, RequestId: &rid, Text: &text}, "watch"); err != nil {
			return err
		}
		w.Wakes, w.LastWakeAt = w.Wakes+1, jsonBytes(nowMs)
		return nil
	}
	rewake := func() {
		next := nowMs + int64(*w.TimerMs)
		w.NextWakeAt = &next
	}
	if len(parts) > 0 {
		if err := send(fmt.Sprintf("watch:%s:%d", id, start), fmt.Sprintf("Fleet: %d changes in your focus: %s", len(parts), strings.Join(parts, "; "))); err != nil {
			return err
		}
	}
	if len(limits) > 0 { // its own message: a limits signal is not a change in the focus
		ks := w.LimitsSignaled
		if err := send(fmt.Sprintf("limits:%s:%s", id, ks[len(ks)-1]), "Fleet: limits: "+strings.Join(limits, "; ")); err != nil {
			return err
		}
	}
	if timer && len(parts) > 0 { // the change wake also serves as the timer wake
		rewake()
	} else if timer {
		if err := send(fmt.Sprintf("timer:%s:%d", id, nextWakeAt), "Fleet: watch timer; review your focus with task_page and questions_list"); err != nil {
			return err
		}
		rewake()
	}
	w.LastError = nil
	return watchPut(q, w)
}

// classify: one change by its current row; "" drops it (§5 table).
func classify(q *Request, c change) (string, error) {
	switch c.Kind {
	case "task":
		t, err := q.db().TaskWatchState(q.ctx(), c.Id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		st := t.State
		if deref(t.PauseReason) == "held" {
			st = "held"
		}
		return "task " + c.Id + " now " + st, nil
	case "question":
		if c.Op != "I" {
			return "", nil
		}
		task, err := q.db().OpenQuestionTask(q.ctx(), c.Id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return "question " + c.Id + " on task " + firstString(task, "none"), nil
	case "execution":
		s, err := q.db().ExecutionWatchState(q.ctx(), c.Id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return "execution " + c.Id + " " + s, nil
	case "host": // only reported problems (§5 table)
		if ok, err := q.db().ObservationHasProblems(q.ctx(), c.Id); err != nil || !ok {
			return "", err
		}
		return "host " + c.Id + " reports problems", nil
	}
	return "", nil
}

// limitsCache: one limits reply per watch pass for every watch (the reply reads all readings).
// ponytail: a 10 s reuse window shared by all watches; key it by pass if passes get more frequent.
var limitsCache struct {
	at    int64
	reply contract.SubscriptionLimitsReply
}

// limitSignals (fleet-people-access.md section 8 "PM agents"): a subscription held by the director with a period
// projected empty before its own next reset. Each (subscription, period, reset) is sent once; the agent may propose a
// refresh or order soonest_reset to the director, never set it.
func limitSignals(q *Request, dir *Person, sent []string) ([]string, []string, error) {
	now := time.Now().UnixMilli()
	if now-limitsCache.at > 10_000 {
		r, err := limitsReply(q, limitsDefaults(now))
		if err != nil {
			return nil, nil, err
		}
		limitsCache.at, limitsCache.reply = now, r
	}
	parts, keys := limitSignalsOf(limitsCache.reply, dir, sent)
	return parts, keys, nil
}

func limitSignalsOf(r contract.SubscriptionLimitsReply, dir *Person, sent []string) ([]string, []string) {
	var parts []string
	keys := slices.Clone(sent)
	for _, s := range r.Subscriptions {
		var holder string
		json.Unmarshal(s.Holder, &holder)
		if holder != dir.Email {
			continue
		}
		for _, p := range s.Periods {
			if p.FirstEmpty.State != contract.LimitEmptyStateAt {
				continue
			}
			empty := time.UnixMilli(*p.FirstEmpty.At).UTC().Format(time.RFC3339)
			reset := "unknown"
			if p.ResetsAt != nil {
				reset = time.UnixMilli(*p.ResetsAt).UTC().Format(time.RFC3339)
			}
			key := s.Id + ":" + p.Period + ":" + reset
			if slices.Contains(keys, key) {
				continue
			}
			keys = append(keys, key)
			parts = append(parts, fmt.Sprintf("subscription %s %s %s runs empty at %s, before its next reset (%s); you may propose subscription_limits_refresh or order soonest_reset to the director, never set it",
				s.Id, s.Harness, p.Period, empty, reset))
		}
	}
	// ponytail: the sent list keeps the last 100 keys; older resets are long past.
	if len(keys) > 100 {
		keys = keys[len(keys)-100:]
	}
	return parts, keys
}
