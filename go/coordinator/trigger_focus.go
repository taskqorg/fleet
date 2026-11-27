// The focus of a session trigger (docs/designs/fleet-triggers.md): the session fields a trigger's change check and
// fire read (its task, delivery and focus), and how a change is classified and worded for its message. Triggers
// replaced the watch operations (watch_set, watch_timer_set, watch_stop, watch_get).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fleet/contract"
	"fleet/coordinator/db"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

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

// classify: one scoped change; "" drops it (§5 table).
func classify(q *Request, c change, target triggerTarget) (string, error) {
	switch c.Kind {
	case "task":
		t, err := q.db().TaskWatchState(q.ctx(), db.TaskWatchStateParams{Seq: c.Seq, ID: c.Id})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !t.StateChanged {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		line := "task " + c.Id + " now " + t.State
		if t.Legacy {
			sessionID, botID := target.ref()
			delivered, err := q.db().LegacyTaskWakeDelivered(q.ctx(), db.LegacyTaskWakeDeliveredParams{
				SessionID: sessionID, BotID: botID, SignalLine: line, StateAt: t.StateAt,
			})
			if err != nil || delivered {
				return "", err
			}
		}
		return line, nil
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
		s, err := q.db().ExecutionWatchState(q.ctx(), db.ExecutionWatchStateParams{ID: c.Id, WatchSignal: c.ExecutionWatchSignal})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		line := "execution " + c.Id + " " + s
		if c.ExecutionWatchSignal == nil {
			sessionID, botID := target.ref()
			delivered, err := q.db().LegacyExecutionWakeDelivered(q.ctx(), db.LegacyExecutionWakeDeliveredParams{
				SessionID: sessionID, BotID: botID, SignalLine: line,
			})
			if err != nil || delivered {
				return "", err
			}
		}
		return line, nil
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
