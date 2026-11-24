package main

// Session context (docs/design-system/pages/session.md "Context panel", FLT-30): what one session plans and keeps
// running, in one read over the state that already holds it. No table of its own:
//   - plan: the work_plan of the session, else of its task (board.go), one step per plan task;
//   - timers: the session's watch timer (watch.go);
//   - automations: the automations of the session's bot (bot_automations.go);
//   - swarm: the other members of that board (work_board_get's roster) with their state.

import (
	"errors"
	"strings"

	"fleet/contract"
)

func init() {
	op("session_context", sessionContext)
}

func sessionContext(q *Request, r *contract.SessionContextRequest) (contract.SessionContextReply, error) {
	out := contract.SessionContextReply{SessionId: r.SessionId, Timers: []contract.SessionTimer{},
		Automations: []contract.BotAutomationRecord{}, Swarm: []contract.SessionSwarmMember{}}
	sid, err := sessionArg(r.SessionId)
	if err != nil {
		return out, err
	}
	s, err := sessionByID(q, sid)
	if err != nil {
		return out, err
	}
	board, err := sessionBoard(q, s)
	if err != nil {
		return out, err
	}
	if board != nil {
		out.Plan = sessionPlan(board)
		out.Swarm = sessionSwarm(board, sid)
	}
	w, err := watchOf(q, "watch:"+sid)
	if err != nil {
		return out, err
	}
	if w != nil && w.TimerMs != nil {
		out.Timers = append(out.Timers, contract.SessionTimer{Id: w.ID, EveryMs: *w.TimerMs, NextAt: w.NextWakeAt})
	}
	if s.BotID != "" {
		a, err := botAutomations(q, &contract.BotAutomationsRequest{BotId: s.BotID})
		if err != nil && !errors.Is(err, errNotFound) { // a bot the caller cannot see has no automations here
			return out, err
		}
		out.Automations = a.Automations
	}
	return out, nil
}

// sessionBoard: the board of the session, else the board of its task when the session's has neither plan nor members;
// nil when neither has anything.
func sessionBoard(q *Request, s agentSession) (*contract.WorkBoardGetReply, error) {
	scopes := []struct {
		kind contract.BoardScopeKind
		id   string
	}{{contract.BoardScopeKindSession, s.ID}}
	if s.TaskID != "" {
		scopes = append(scopes, struct {
			kind contract.BoardScopeKind
			id   string
		}{contract.BoardScopeKindTask, s.TaskID})
	}
	for _, sc := range scopes {
		b, err := workBoardGet(q, &contract.WorkBoardGetRequest{ScopeKind: sc.kind, ScopeId: sc.id, Limit: ptr(int64(1))})
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if b.Plan != nil || len(b.Roster) > 0 {
			return &b, nil
		}
	}
	return nil, nil
}

// sessionPlan: the board's plan as steps, threads in order and each thread's tasks in order; nil without a plan.
func sessionPlan(b *contract.WorkBoardGetReply) *contract.SessionPlan {
	p := b.Plan
	if p == nil {
		return nil
	}
	out := &contract.SessionPlan{ScopeKind: p.ScopeKind, ScopeId: p.ScopeId, Title: p.Title, Version: p.Version, Steps: []contract.SessionPlanStep{}}
	for _, th := range p.Threads {
		for _, t := range th.Tasks {
			st := contract.WorkPlanTaskStatusPending
			if t.Status != nil {
				st = *t.Status
			}
			done := st == contract.WorkPlanTaskStatusDone
			out.Steps = append(out.Steps, contract.SessionPlanStep{Id: t.Id, Title: t.Title, Status: st, Done: done})
			if done {
				out.Done++
			}
		}
	}
	out.Total = int64(len(out.Steps))
	return out
}

// sessionSwarm: the board's members other than the session itself: the people of a swarm it started or joined.
func sessionSwarm(b *contract.WorkBoardGetReply, sid string) []contract.SessionSwarmMember {
	out := []contract.SessionSwarmMember{}
	for _, m := range b.Roster {
		if m.SessionId != nil && strings.TrimSpace(*m.SessionId) == sid {
			continue
		}
		out = append(out, contract.SessionSwarmMember{MemberName: m.MemberName, Role: m.Role, SessionId: m.SessionId, SessionCode: m.SessionCode, State: m.State})
	}
	return out
}
