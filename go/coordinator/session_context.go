package main

// Session context (docs/design-system/pages/session.md "Context panel", FLT-30): what one session plans and keeps
// running, in one read over the state that already holds it (its timers and its bot's automations are triggers: the
// panel reads trigger_list). No table of its own:
//   - plan: the work_plan of the session, else of its task (board.go), one step per plan task;
//   - swarm: the other members of that board (work_board_get's roster) with their state;
//   - tasks: the session's own task, the plan's tasks, the tasks it spawned and its bot's tasks, as entity cards.

import (
	"errors"
	"strings"

	"fleet/contract"
)

func init() {
	op("session_context", sessionContext)
}

func sessionContext(q *Request, r *contract.SessionContextRequest) (contract.SessionContextReply, error) {
	out := contract.SessionContextReply{SessionId: r.SessionId, Swarm: []contract.SessionSwarmMember{}, Tasks: []contract.SessionTask{}}
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
	var botTasks []contract.BotTaskRecord
	if s.BotID != "" {
		out.BotId = &s.BotID
		bt, err := botTasksOp(q, &contract.BotTasksRequest{BotId: s.BotID, Limit: ptr(int64(sessionContextTasks))})
		if err != nil && !errors.Is(err, errNotFound) {
			return out, err
		}
		botTasks = bt.Tasks
	}
	out.Tasks, err = sessionTasks(q, s, out.Plan, botTasks)
	return out, err
}

// sessionContextTasks: the most related tasks the panel lists; the bot's own page lists the rest.
const sessionContextTasks = 20

// sessionTasks: the session's own task, then the plan's tasks, the tasks it spawned and its bot's tasks, each once and
// only when the caller may see it (an entity card the caller cannot read is left out).
func sessionTasks(q *Request, s agentSession, plan *contract.SessionPlan, bot []contract.BotTaskRecord) ([]contract.SessionTask, error) {
	out := []contract.SessionTask{}
	seen := map[string]bool{}
	add := func(t contract.SessionTask) {
		if t.Id != "" && !seen[t.Id] && len(out) < sessionContextTasks {
			seen[t.Id] = true
			out = append(out, t)
		}
	}
	card := func(id string) error {
		if id == "" || seen[id] {
			return nil
		}
		c, err := cardTask(q, id)
		if errors.Is(err, errNotFound) || (err == nil && c == nil) {
			return nil
		}
		if err != nil {
			return err
		}
		add(contract.SessionTask{Id: id, Code: c.Code, Title: c.Title, State: deref(c.State)})
		return nil
	}
	if err := card(s.TaskID); err != nil {
		return out, err
	}
	if plan != nil {
		for _, st := range plan.Steps {
			if err := card(deref(st.TaskId)); err != nil {
				return out, err
			}
		}
	}
	roll, err := sessionResourceRollupQuery(q, s.ID)
	if err != nil {
		return out, err
	}
	for _, t := range roll.SpawnedTasks {
		add(contract.SessionTask{Id: t.TaskId, Code: t.Code, Title: t.Title, State: t.State})
	}
	for _, t := range bot {
		add(contract.SessionTask{Id: t.TaskId, Code: t.TaskCode, Title: t.Title, State: t.State})
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
			out.Steps = append(out.Steps, contract.SessionPlanStep{Id: t.Id, Title: t.Title, Status: st, Done: done, Owner: t.OwnerMember, TaskId: t.TaskId})
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
