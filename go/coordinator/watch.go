// Watch operations (docs/designs/fleet-pm-agent.md section 5) over triggers (triggers.go, docs/designs/fleet-triggers.md):
// a session's watch is its focus-changes trigger (the kinds) and its conversation-check trigger (the timer). Kept until
// the app and the PM skill use trigger_list and trigger_save; the change reads of a trigger's check live here.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fleet/contract"
	"fleet/coordinator/db"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// watchTriggerIDs: the session's two watch triggers.
func watchTriggerIDs(sid string) (kinds, timer string) {
	return "focus-changes:" + sid, "conversation-check:" + sid
}

// watchTriggers: the session's watch triggers, nil when absent.
func watchTriggers(q *Request, sid string) (kinds, timer *db.Trigger, err error) {
	k, t := watchTriggerIDs(sid)
	get := func(id string) (*db.Trigger, error) {
		row, err := q.db().TriggerGet(q.ctx(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return &row, err
	}
	if kinds, err = get(k); err != nil {
		return nil, nil, err
	}
	timer, err = get(t)
	return kinds, timer, err
}

// watchRecord: the watch record of a session's watch triggers, nil when it has none.
func watchRecord(sid string, kinds, timer *db.Trigger) *contract.WatchRecord {
	if kinds == nil && timer == nil {
		return nil
	}
	notPaused := false
	r := contract.WatchRecord{Id: "watch:" + sid, SessionId: sid, Kinds: []string{}, TimerPaused: &notPaused}
	var last *time.Time
	for _, t := range []*db.Trigger{kinds, timer} {
		if t == nil {
			continue
		}
		r.Wakes += t.Fires
		if t.LastFireAt != nil && (last == nil || t.LastFireAt.After(*last)) {
			last = t.LastFireAt
		}
		if t.ChangeCursor > r.Cursor {
			r.Cursor = t.ChangeCursor
		}
		if t.LastError != nil {
			r.LastError = t.LastError
		}
	}
	if last != nil {
		r.LastWakeAt = jsonBytes(last.UnixMilli())
	}
	if kinds != nil {
		r.Kinds = kinds.IfChanged
	}
	if timer != nil && timer.EveryMs != nil {
		ms, paused := float64(*timer.EveryMs), !timer.Enabled
		r.TimerMs, r.TimerPaused = &ms, &paused
		if timer.Enabled && timer.NextAt != nil {
			at := timer.NextAt.UnixMilli()
			r.NextWakeAt = &at
		}
	}
	return &r
}

// watchTarget: the session sid as a trigger target the caller may manage and write.
func watchTarget(q *Request, sid string) (triggerTarget, error) {
	if sid == "" {
		return triggerTarget{}, fail(400, "Expected sessionId")
	}
	t, err := triggerTargetOf(q, sid, "")
	if err != nil {
		return t, err
	}
	if err := t.session.directs(q); err != nil {
		return t, err
	}
	return t, nil
}

// watchTimerSettings: the conversation-check settings for a timer of interval ms.
func watchTimerSettings(ms int64, enabled bool) *contract.TriggerSettings {
	return &contract.TriggerSettings{Name: "Conversation check", PresetId: ptr("conversation-check"),
		Text: "Fleet: conversation check; review your focus with task_page and questions_list.", When: "every",
		EveryMs: &ms, IfChanged: []string{"task", "question", "execution"}, Enabled: enabled}
}

func revisionOf(t *db.Trigger) int64 {
	if t == nil {
		return 0
	}
	return int64(t.Revision)
}

func watchSet(q *Request, b *contract.WatchSetRequest) (contract.WatchRecord, error) {
	sid := b.SessionId
	target, err := watchTarget(q, sid)
	if err != nil {
		return contract.WatchRecord{}, err
	}
	if err := target.writable(q); err != nil {
		return contract.WatchRecord{}, err
	}
	if len(b.Kinds) == 0 {
		return contract.WatchRecord{}, fail(400, "Expected kinds: task, question, execution, host, limits")
	}
	for _, k := range b.Kinds {
		if !slices.Contains(triggerKinds, k) {
			return contract.WatchRecord{}, fail(400, "Unknown kind "+k+": use task, question, execution, host, limits")
		}
	}
	if t := b.TimerMs; t != nil && *t <= 0 {
		return contract.WatchRecord{}, fail(400, "Expected timerMs > 0")
	}
	kinds, timer, err := watchTriggers(q, sid)
	if err != nil {
		return contract.WatchRecord{}, err
	}
	kid, tid := watchTriggerIDs(sid)
	every := triggerTickMs() // a check each pass, as the watch had
	if _, err := saveTrigger(q, triggerSaveIn{id: kid, baseRevision: revisionOf(kinds), target: target, replace: &contract.TriggerSettings{
		Name: "Focus changes", PresetId: ptr("focus-changes"), When: "every", EveryMs: &every, IfChanged: b.Kinds, Enabled: true}}); err != nil {
		return contract.WatchRecord{}, err
	}
	switch {
	case b.TimerMs != nil:
		ms := int64(math.Max(1, math.Round(*b.TimerMs)))
		if _, err := saveTrigger(q, triggerSaveIn{id: tid, baseRevision: revisionOf(timer), target: target, replace: watchTimerSettings(ms, true)}); err != nil {
			return contract.WatchRecord{}, err
		}
	case timer != nil: // watch_set without a timer sets a watch without one
		if err := q.db().TriggerDeleteIDs(q.ctx(), []string{tid}); err != nil {
			return contract.WatchRecord{}, err
		}
	}
	if kinds, timer, err = watchTriggers(q, sid); err != nil {
		return contract.WatchRecord{}, err
	}
	return *watchRecord(sid, kinds, timer), nil
}

func watchTimerSet(q *Request, b *contract.WatchTimerSetRequest) (contract.WatchRecord, error) {
	var out contract.WatchRecord
	target, err := watchTarget(q, b.SessionId)
	if err != nil {
		return out, err
	}
	if err := target.writable(q); err != nil {
		return out, err
	}
	kinds, timer, err := watchTriggers(q, b.SessionId)
	if err != nil {
		return out, err
	}
	if timer == nil || timer.EveryMs == nil {
		return out, fail(409, "Watch no longer exists. Reload before changing the timer")
	}
	if float64(*timer.EveryMs) != b.BaseTimerMs || !timer.Enabled != b.BasePaused {
		return out, fail(409, "Watch timer changed. Reload before changing it")
	}
	_, tid := watchTriggerIDs(b.SessionId)
	settings := triggerSettingsOf(*timer)
	switch string(b.Action) {
	case "edit":
		if b.TimerMs == nil || *b.TimerMs < 1 || math.Trunc(*b.TimerMs) != *b.TimerMs || *b.TimerMs > float64(8_640_000_000_000_000-time.Now().UnixMilli()) {
			return out, fail(400, "Enter a positive timer interval in whole milliseconds within the supported range")
		}
		ms := int64(*b.TimerMs)
		settings.EveryMs = &ms
	case "pause":
		if b.BasePaused {
			return out, fail(409, "Watch timer is already paused")
		}
		settings.Enabled = false
	case "resume":
		if !b.BasePaused {
			return out, fail(409, "Watch timer is already active")
		}
		settings.Enabled = true
	case "cancel":
		if err := q.db().TriggerDeleteIDs(q.ctx(), []string{tid}); err != nil {
			return out, err
		}
		if r := watchRecord(b.SessionId, kinds, nil); r != nil {
			return *r, nil
		}
		notPaused := false
		return contract.WatchRecord{Id: "watch:" + b.SessionId, SessionId: b.SessionId, Kinds: []string{}, TimerPaused: &notPaused}, nil
	}
	saved, err := saveTrigger(q, triggerSaveIn{id: tid, baseRevision: int64(timer.Revision), target: target, replace: &settings})
	if err != nil {
		return out, err
	}
	return *watchRecord(b.SessionId, kinds, &saved), nil
}

func init() {
	op("watch_set", watchSet)
	op("watch_timer_set", watchTimerSet)
	op("watch_stop", func(q *Request, b *contract.WatchStopRequest) (contract.WatchStopReply, error) {
		if _, err := watchTarget(q, b.SessionId); err != nil {
			return contract.WatchStopReply{}, err
		}
		k, t := watchTriggerIDs(b.SessionId)
		return contract.WatchStopReply{SessionId: b.SessionId, Stopped: true}, q.db().TriggerDeleteIDs(q.ctx(), []string{k, t})
	})
	op("watch_get", func(q *Request, b *contract.WatchGetRequest) (contract.WatchGetReply, error) {
		if b.SessionId == "" {
			return contract.WatchGetReply{}, fail(400, "Expected sessionId")
		}
		s, err := watchSessionOf(q, b.SessionId)
		if err != nil {
			return contract.WatchGetReply{}, err
		}
		if s == nil {
			return contract.WatchGetReply{}, errNotFound
		}
		canManage := s.directs(q) == nil
		if task := s.task(); canManage && task != "" {
			cancelled, err := q.db().TaskCancelled(q.ctx(), task)
			if err != nil {
				return contract.WatchGetReply{}, err
			}
			canManage = !cancelled
		}
		kinds, timer, err := watchTriggers(q, b.SessionId)
		return contract.WatchGetReply{CanManageTimer: &canManage, Watch: watchRecord(b.SessionId, kinds, timer)}, err
	})
}

// watchSession: the session fields a watch reads; deliveryId and focus are payload fields (queries/agent.sql "WatchSession").
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

// taskOrEmpty: task for a session that may be nil.
func (s *watchSession) taskOrEmpty() string {
	if s == nil {
		return ""
	}
	return s.task()
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
