package main

// budgets (docs/designs/fleet-people-access.md §9) and provider limit readings, which dispatch applies.

import (
	"fmt"
	"math"

	"fleet/contract"
)

func init() {
	operation("budget_set", false, ownerFirst("Only the owner sets budgets", typedOp(budgetSet)))
	operation("budget_ask", false, typedOp(budgetAsk))
	operation("budget_delete", false, ownerFirst("Only the owner deletes budgets", typedOp(budgetDelete)))
	operation("budgets_list", true, typedOp(budgetsList))
}

var (
	budgetScopes  = []string{"person", "project", "experiment", "connection"}
	budgetActions = []string{"warn", "pause", "stop"}
	budgetPeriods = []string{"day", "week", "month"}
	measuredUnit  = map[string]string{"wall_ms": "coalesce(t.wall_ms,0)", "tokens": "coalesce(t.tokens_input,0)+coalesce(t.tokens_output,0)"}
)

func budgetView(r map[string]any) contract.Budget {
	return contract.Budget{Id: astr(r["id"]), Scope: astr(r["scope"]), ScopeId: astr(r["scope_id"]), Unit: astr(r["unit"]), Limit: num(r["limit"]), Period: strPtr(r["period"]),
		Action: astr(r["action"]), SetBy: astr(r["set_by"]), SetAt: timeText(r["set_at"])}
}

// spend: measured spend of a budget, or nil when Fleet does not measure its unit.
func spend(q *Request, b map[string]any) (any, string, error) {
	period, _ := b["period"].(string)
	var v float64
	if b["scope"] == "connection" {
		since := ""
		if period != "" {
			since = "AND (e.data->>'startedAt')::bigint >= (extract(epoch from now() - interval '1 " + period + "')*1000)::bigint"
		}
		var err error
		if b["unit"] == "calls" {
			err = q.tx.QueryRow(q.ctx(), "SELECT count(*)::float8 FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.kind='tool_run' AND e.data->>'connection'=$1 "+since, b["scope_id"]).Scan(&v)
		} else {
			err = q.tx.QueryRow(q.ctx(), "SELECT coalesce(sum((t.connection_cost->>'amount')::numeric) FILTER (WHERE t.connection_cost->>'unit'=$2),0)::float8 FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.kind='tool_run' AND e.data->>'connection'=$1 "+since, b["scope_id"], b["unit"]).Scan(&v)
		}
		return v, "measured", err
	}
	expr := measuredUnit[astr(b["unit"])]
	if expr == "" || (b["scope"] != "person" && b["scope"] != "project") {
		return nil, "unverifiable", nil
	}
	since := ""
	if period != "" {
		since = "AND e.started_at >= now() - interval '1 " + period + "'"
	}
	col := "e.directed_by"
	if b["scope"] == "project" {
		col = "e.project_id"
	}
	err := q.tx.QueryRow(q.ctx(), "SELECT coalesce(sum("+expr+"),0)::float8 FROM execution e JOIN execution_totals t ON t.execution_id=e.id WHERE "+col+"=$1 "+since, b["scope_id"]).Scan(&v)
	return v, "measured", err
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
	if err := exec(q, `INSERT INTO budget(id,scope,scope_id,unit,"limit",period,action,set_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
    ON CONFLICT(id) DO UPDATE SET scope=excluded.scope,scope_id=excluded.scope_id,unit=excluded.unit,"limit"=excluded."limit",period=excluded.period,action=excluded.action,set_by=excluded.set_by,set_at=now()`,
		id, b.Scope, b.ScopeId, b.Unit, b.Limit, b.Period, action, q.person.Email); err != nil {
		return none, err
	}
	rs, err := queryRows(q, `SELECT id,scope,scope_id,unit,"limit"::float8 AS limit,period,action,set_by,set_at FROM budget WHERE id=$1`, id)
	if err != nil {
		return none, err
	}
	return budgetView(rs[0]), nil
}

func budgetAsk(q *Request, b *contract.BudgetAskRequest) (contract.BudgetAskReply, error) {
	var none contract.BudgetAskReply
	rs, err := queryRows(q, `SELECT id,scope,scope_id,unit,"limit"::float8 AS limit FROM budget WHERE id=$1`, b.BudgetId)
	if err != nil {
		return none, err
	}
	if len(rs) == 0 {
		return none, fail(404, "Unknown budget")
	}
	r := rs[0]
	limit := b.Limit
	if limit <= num(r["limit"]) {
		return none, fail(400, "Expected a limit above the current one")
	}
	asker, askedBy := "fleet", any(nil)
	if q.person != nil {
		asker, askedBy = q.person.Email, q.person.Email
	}
	reason := ""
	if s := deref(b.Reason); s != "" {
		if len(s) > 500 {
			s = s[:500]
		}
		reason = ": " + s
	}
	var project any
	if r["scope"] == "project" {
		project = r["scope_id"]
	}
	id, err := askQuestion(q, doc{"kind": "operator_decision", "projectId": project,
		"text":    fmt.Sprintf("%s asks to raise the %s %s budget from %v to %v %s%s.", asker, r["scope"], r["scope_id"], jsNum(num(r["limit"])), jsNum(limit), r["unit"], reason),
		"options": []any{"approve", "decline"}, "askedBy": asker,
		"data": doc{"budgetAsk": doc{"budgetId": r["id"], "limit": limit, "askedBy": askedBy}, "cause": fmt.Sprint("budget:", r["id"], ":ask")}})
	return contract.BudgetAskReply{QuestionId: id}, err
}

// jsNum prints a number as JavaScript does for the integers and short decimals budgets use.
func jsNum(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return fmt.Sprintf("%.0f", f)
	}
	return fmt.Sprint(f)
}

func budgetDelete(q *Request, b *contract.BudgetDeleteRequest) (contract.BudgetDeleteReply, error) {
	tag, err := q.tx.Exec(q.ctx(), "DELETE FROM budget WHERE id=$1", b.Id)
	if err != nil {
		return contract.BudgetDeleteReply{}, err
	}
	if tag.RowsAffected() == 0 {
		return contract.BudgetDeleteReply{}, fail(404, "Unknown budget")
	}
	return contract.BudgetDeleteReply{Id: b.Id, Deleted: true}, nil
}

func budgetsList(q *Request, _ *contract.BudgetsListRequest) (contract.BudgetsListReply, error) {
	out := contract.BudgetsListReply{Budgets: []contract.Budget{}}
	rs, err := queryRows(q, `SELECT id,scope,scope_id,unit,"limit"::float8 AS limit,period,action,set_by,set_at FROM budget ORDER BY set_at`)
	if err != nil {
		return out, err
	}
	for _, r := range rs {
		v, basis, err := spend(q, r)
		if err != nil {
			return out, err
		}
		x := budgetView(r)
		sp := contract.BudgetSpend{Basis: basis}
		if f, ok := v.(float64); ok {
			sp.Value = &f
		}
		x.Spend = &sp
		out.Budgets = append(out.Budgets, x)
	}
	return out, nil
}

type reachedBudget struct{ id, action, reason string }

// budgetsReached: the budgets covering an execution (its project and director) that measured spend has reached.
func budgetsReached(q *Request, e doc) ([]reachedBudget, error) {
	d, _ := e["dispatch"].(map[string]any)
	project := astr(d["projectId"])
	rs, err := queryRows(q, `SELECT id,scope,scope_id,unit,"limit"::float8 AS limit,period,action FROM budget WHERE (scope='project' AND scope_id=$1) OR (scope='person' AND scope_id=$2)`, project, astr(e["directedBy"]))
	if err != nil {
		return nil, err
	}
	out := []reachedBudget{}
	for _, r := range rs {
		v, _, err := spend(q, r)
		if err != nil {
			return nil, err
		}
		if f, ok := v.(float64); ok && f >= num(r["limit"]) {
			out = append(out, reachedBudget{astr(r["id"]), astr(r["action"]), fmt.Sprintf("%s %s budget reached: %s %s of %s", r["scope"], r["scope_id"], r["unit"], jsNum(f), jsNum(num(r["limit"])))})
		}
	}
	return out, nil
}

// budgetAnswerCheck and budgetAnswerAfter: the answer hooks of a budget ask (tasks area calls them).
func budgetAnswerCheck(qn doc, answer string, p *Person) error {
	data, _ := qn["data"].(map[string]any)
	if data == nil || data["budgetAsk"] == nil {
		return nil
	}
	if answer != "approve" && answer != "decline" {
		return fail(400, "Answer approve or decline")
	}
	if answer == "approve" && p != nil && !fullAccess(p) {
		return fail(403, "Only the owner raises a budget")
	}
	return nil
}

func budgetAnswerAfter(q *Request, qn doc) error {
	data, _ := qn["data"].(map[string]any)
	a, _ := data["budgetAsk"].(map[string]any)
	if a == nil || qn["answer"] != "approve" {
		return nil
	}
	by := nonNil(qn["answered_by"], "owner")
	if err := exec(q, `UPDATE budget SET "limit"=$2,set_by=$3,set_at=now() WHERE id=$1`, a["budgetId"], a["limit"], by); err != nil {
		return err
	}
	return put(q, "setting-change", doc{"id": newID(), "target": fmt.Sprint("budget:", a["budgetId"]), "field": "limit", "value": a["limit"], "askedBy": a["askedBy"], "actor": qn["answered_by"], "at": nowMs()})
}
