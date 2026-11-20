package main

// budgets (docs/designs/fleet-people-access.md §9) and provider limit readings, which dispatch applies.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("budget_set", budgetSet)
	op("budget_ask", budgetAsk)
	op("budget_delete", budgetDelete)
	op("budgets_list", budgetsList)
}

var (
	budgetScopes  = []string{"person", "project", "experiment", "connection"}
	budgetActions = []string{"warn", "pause", "stop"}
	budgetPeriods = []string{"day", "week", "month"}
)

// budgetLock: the advisory lock of one budget's row.
func budgetLock(id string) string { return "budget:" + id }

func budgetView(r db.Budget) contract.Budget {
	return contract.Budget{Id: r.ID, Scope: r.Scope, ScopeId: r.ScopeID, Unit: r.Unit, Limit: r.Limit, Period: r.Period,
		Action: r.Action, SetBy: r.SetBy, SetAt: r.SetAt.Format(time.RFC3339Nano)}
}

// spend: measured spend of a budget, or nil when Fleet does not measure its unit. A person or project budget counts their
// work, not Fleet's own calls; a connection budget counts every call.
func spend(q *Request, b db.Budget) (*float64, string, error) {
	period := b.Period
	if period != nil && *period == "" {
		period = nil
	}
	var v float64
	var err error
	switch {
	case b.Scope == "connection" && b.Unit == "calls":
		v, err = q.db().BudgetSpendConnectionCalls(q.ctx(), db.BudgetSpendConnectionCallsParams{Connection: b.ScopeID, Period: period})
	case b.Scope == "connection":
		v, err = q.db().BudgetSpendConnectionAmount(q.ctx(), db.BudgetSpendConnectionAmountParams{Unit: b.Unit, Connection: b.ScopeID, Period: period})
	case b.Unit != "wall_ms" && b.Unit != "tokens":
		return nil, "unverifiable", nil
	case b.Scope == "person":
		var r db.BudgetSpendPersonRow
		r, err = q.db().BudgetSpendPerson(q.ctx(), db.BudgetSpendPersonParams{Person: b.ScopeID, Period: period})
		v = r.WallMs
		if b.Unit == "tokens" {
			v = r.Tokens
		}
	case b.Scope == "project":
		var r db.BudgetSpendProjectRow
		r, err = q.db().BudgetSpendProject(q.ctx(), db.BudgetSpendProjectParams{ProjectID: b.ScopeID, Period: period})
		v = r.WallMs
		if b.Unit == "tokens" {
			v = r.Tokens
		}
	default:
		return nil, "unverifiable", nil
	}
	if err != nil {
		return nil, "", err
	}
	return &v, "measured", nil
}

func budgetSet(q *Request, b *contract.BudgetSetRequest) (contract.Budget, error) {
	var none contract.Budget
	if !inList(budgetScopes, b.Scope) || b.ScopeId == "" || b.Unit == "" || b.Limit < 0 {
		return none, fail(400, "Expected scope, scopeId, unit, limit")
	}
	action := "warn"
	if b.Action != nil {
		action = *b.Action
	}
	if !inList(budgetActions, action) || (b.Period != nil && !inList(budgetPeriods, *b.Period)) {
		return none, fail(400, "action warn|pause|stop; period day|week|month")
	}
	id := deref(b.Id)
	if id == "" {
		id = newID()
	}
	var r db.Budget
	err := q.app.store.Write(q.ctx(), q, []string{budgetLock(id)}, func(q *Request) (err error) {
		r, err = q.db().BudgetUpsert(q.ctx(), db.BudgetUpsertParams{ID: id, Scope: b.Scope, ScopeID: b.ScopeId, Unit: b.Unit, Limit: b.Limit,
			Period: b.Period, Action: action, SetBy: q.person.Email})
		return err
	})
	if err != nil {
		return none, err
	}
	return budgetView(r), nil
}

// budgetAskData: data.budgetAsk of the question that asks to raise a budget.
type budgetAskData struct {
	BudgetId string  `json:"budgetId"`
	Limit    float64 `json:"limit"`
	AskedBy  *string `json:"askedBy"`
}

func budgetAsk(q *Request, b *contract.BudgetAskRequest) (contract.BudgetAskReply, error) {
	var none contract.BudgetAskReply
	asker, askedBy := "fleet", (*string)(nil)
	if q.person != nil {
		asker, askedBy = q.person.Email, &q.person.Email
	}
	reason := ""
	if s := deref(b.Reason); s != "" {
		if len(s) > 500 {
			s = s[:500]
		}
		reason = ": " + s
	}
	id := newID()
	err := q.app.store.Write(q.ctx(), q, nil, func(q *Request) error {
		r, err := q.db().BudgetGet(q.ctx(), b.BudgetId)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "Unknown budget")
		}
		if err != nil {
			return err
		}
		if b.Limit <= r.Limit {
			return fail(400, "Expected a limit above the current one")
		}
		var project *string
		if r.Scope == "project" {
			project = &r.ScopeID
		}
		data, _ := json.Marshal(struct {
			BudgetAsk budgetAskData `json:"budgetAsk"`
			Cause     string        `json:"cause"`
		}{budgetAskData{r.ID, b.Limit, askedBy}, "budget:" + r.ID + ":ask"})
		return q.db().BudgetAskInsert(q.ctx(), db.BudgetAskInsertParams{ID: id, ProjectID: project,
			Text:    fmt.Sprintf("%s asks to raise the %s %s budget from %v to %v %s%s.", asker, r.Scope, r.ScopeID, jsNum(r.Limit), jsNum(b.Limit), r.Unit, reason),
			Options: []byte(`["approve","decline"]`), AskedBy: &asker, Data: data})
	})
	if err != nil {
		return none, err
	}
	return contract.BudgetAskReply{QuestionId: id}, nil
}

// jsNum prints a number as JavaScript does for the integers and short decimals budgets use.
func jsNum(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return fmt.Sprintf("%.0f", f)
	}
	return fmt.Sprint(f)
}

func budgetDelete(q *Request, b *contract.BudgetDeleteRequest) (contract.BudgetDeleteReply, error) {
	if b.Id == "" {
		return contract.BudgetDeleteReply{}, fail(404, "Unknown budget")
	}
	var n int64
	err := q.app.store.Write(q.ctx(), q, []string{budgetLock(b.Id)}, func(q *Request) (err error) {
		n, err = q.db().BudgetDelete(q.ctx(), b.Id)
		return err
	})
	if err != nil {
		return contract.BudgetDeleteReply{}, err
	}
	if n == 0 {
		return contract.BudgetDeleteReply{}, fail(404, "Unknown budget")
	}
	return contract.BudgetDeleteReply{Id: b.Id, Deleted: true}, nil
}

func budgetsList(q *Request, _ *contract.BudgetsListRequest) (contract.BudgetsListReply, error) {
	out := contract.BudgetsListReply{Budgets: []contract.Budget{}}
	err := q.app.store.Read(q.ctx(), q, func(q *Request) error {
		rs, err := q.db().BudgetsList(q.ctx())
		if err != nil {
			return err
		}
		for _, r := range rs {
			v, basis, err := spend(q, r)
			if err != nil {
				return err
			}
			x := budgetView(r)
			x.Spend = &contract.BudgetSpend{Basis: basis, Value: v}
			out.Budgets = append(out.Budgets, x)
		}
		return nil
	})
	return out, err
}

type reachedBudget struct{ id, action, reason string }

// budgetsReached: the budgets covering an execution (its project and director) that measured spend has reached.
func budgetsReached(q *Request, project, directedBy string) ([]reachedBudget, error) {
	out := []reachedBudget{}
	err := q.app.store.Read(q.ctx(), q, func(q *Request) error {
		rs, err := q.db().BudgetsCovering(q.ctx(), db.BudgetsCoveringParams{ProjectID: project, Person: directedBy})
		if err != nil {
			return err
		}
		for _, r := range rs {
			v, _, err := spend(q, r)
			if err != nil {
				return err
			}
			if v != nil && *v >= r.Limit {
				out = append(out, reachedBudget{r.ID, r.Action, fmt.Sprintf("%s %s budget reached: %s %s of %s", r.Scope, r.ScopeID, r.Unit, jsNum(*v), jsNum(r.Limit))})
			}
		}
		return nil
	})
	return out, err
}

// blockedConnections: connections whose stop budget is reached; the gateway refuses their calls.
func blockedConnections(q *Request) (map[string]bool, error) {
	out := map[string]bool{}
	err := q.app.store.Read(q.ctx(), q, func(q *Request) error {
		rs, err := q.db().BudgetsConnectionStop(q.ctx())
		if err != nil {
			return err
		}
		for _, r := range rs {
			v, _, err := spend(q, r)
			if err != nil {
				return err
			}
			if v != nil && *v >= r.Limit {
				out[r.ScopeID] = true
			}
		}
		return nil
	})
	return out, err
}

// budgetAskOf: data.budgetAsk of a question, nil for every other question.
func budgetAskOf(raw json.RawMessage) (*budgetAskData, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var a budgetAskData
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// budgetAnswerCheck and budgetAnswerAfter: the answer hooks of a budget ask (tasks area calls them, in its transaction,
// with the question's data.budgetAsk; after, with its recorded answer and who gave it).
func budgetAnswerCheck(q *Request, ask json.RawMessage, answer string) error {
	a, err := budgetAskOf(ask)
	if err != nil || a == nil {
		return err
	}
	if answer != "approve" && answer != "decline" {
		return fail(400, "Answer approve or decline")
	}
	if answer == "approve" && q.person != nil && !fullAccess(q.person) {
		return fail(403, "Only the owner raises a budget")
	}
	return nil
}

func budgetAnswerAfter(q *Request, ask json.RawMessage, answer string, answeredBy *string) error {
	a, err := budgetAskOf(ask)
	if err != nil || a == nil || answer != "approve" {
		return err
	}
	by := "owner"
	if answeredBy != nil {
		by = *answeredBy
	}
	var locks []string
	if a.BudgetId != "" {
		locks = []string{budgetLock(a.BudgetId)}
	}
	return q.app.store.Write(q.ctx(), q, locks, func(q *Request) error {
		if err := q.db().BudgetRaise(q.ctx(), db.BudgetRaiseParams{ID: a.BudgetId, Limit: a.Limit, SetBy: by}); err != nil {
			return err
		}
		change := struct {
			Id         string  `json:"id"`
			Target     string  `json:"target"`
			Field      string  `json:"field"`
			Value      float64 `json:"value"`
			AskedBy    *string `json:"askedBy"`
			Actor      *string `json:"actor"`
			OnBehalfOf string  `json:"onBehalfOf,omitempty"`
			At         int64   `json:"at"`
		}{Id: newID(), Target: "budget:" + a.BudgetId, Field: "limit", Value: a.Limit, AskedBy: a.AskedBy, Actor: answeredBy, At: nowMs()}
		if q.agent != "" && q.person != nil { // an agent's change: the execution acts for its director (fleet-pm-agent.md §1)
			change.Actor, change.OnBehalfOf = &q.agent, q.person.Email
		}
		data, _ := json.Marshal(change)
		return q.db().SettingChangeInsert(q.ctx(), db.SettingChangeInsertParams{ID: change.Id, Data: data})
	})
}
