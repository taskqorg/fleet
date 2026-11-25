// Runners screen (docs/design-system/pages/runners.md): what executes work per host, with its time and resources, and
// spend today and this month per host and execution kind. CI jobs come from the GitHub source (separate query).
package main

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5/pgtype"
)

// runnerRow: one execution as the Runners screen reads it (queries/runners.sql), or a group of tool calls.
type runnerRow struct {
	id, kind, state                                                            string
	hostID, harness, taskID, title, sessionTitle, sessionID, parentID, problem *string
	callerRun, model, accountID, billingBasis                                  *string
	started, ended, seen, wallMs, cpuMs, peakMemoryBytes, tokens               *float64
	subject                                                                    string
	attempt                                                                    *int64 // the run's place among its task's executions (work facts)
	connectionCost                                                             []byte
	isAPIConn                                                                  bool
	tokensInput, tokensOutput, tokensCacheRead, tokensCacheWrite               *int64
	at                                                                         time.Time
	calls, failedCalls                                                         *int64
	usages                                                                     []usage // a tool-call group, priced already per call
	grouped                                                                    bool
	cost                                                                       []contract.CostFigure // set: priced already (a call group)
}

// runnerMs: a time as Unix milliseconds, rounded as PostgreSQL rounds (extract(epoch)*1000)::bigint.
func runnerMs(t *time.Time) *float64 {
	if t == nil {
		return nil
	}
	v := math.Round(float64(t.UnixMicro()) / 1000)
	return &v
}

func runnerF(v *int64) *float64 {
	if v == nil {
		return nil
	}
	f := float64(*v)
	return &f
}

// runnerTokens: input + output + cache; nil when none of them was measured (unknown, never 0).
func runnerTokens(in, out, read, write *int64) *float64 {
	if in == nil && out == nil && read == nil && write == nil {
		return nil
	}
	var t float64
	for _, v := range []*int64{in, out, read, write} {
		if v != nil {
			t += float64(*v)
		}
	}
	return &t
}

// runnerCostField: a field of a gateway call's connection cost as text (->>), nil when absent or JSON null.
func runnerCostField(cost []byte, key string) *string {
	var o map[string]json.RawMessage
	if json.Unmarshal(cost, &o) != nil {
		return nil
	}
	raw, ok := o[key]
	if !ok || string(raw) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return &s
	}
	t := string(raw)
	return &t
}

func runnerUnit(r *runnerRow) *string { return runnerCostField(r.connectionCost, "unit") }

// runnerAmount: the cost amount, else the figure named by its unit.
func runnerAmount(r *runnerRow) *float64 {
	a := runnerCostField(r.connectionCost, "amount")
	if a == nil {
		if u := runnerUnit(r); u != nil {
			a = runnerCostField(r.connectionCost, *u)
		}
	}
	if a == nil {
		return nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(*a), 64)
	if err != nil {
		return nil
	}
	return &v
}

func isRunnerSubscription(r *runnerRow) bool {
	switch deref(r.billingBasis) {
	case string(contract.ResourceBasisSubscription):
		return true
	case "metered":
		return false
	}
	return r.accountID != nil && !r.isAPIConn
}

// runnerCost: the cost of one run's usages. A run on a subscription login (account_id / subscription billing_basis) costs no metered money: its
// figures say basis "subscription", the list-price equivalent of what it used (fleet-analytics.md bases).
func runnerCost(prices []db.PriceEntry, subscription bool, us []usage) []contract.CostFigure {
	out := estimateCost(prices, us)
	if subscription {
		note := subscriptionCostNote
		for i := range out { // unpriced too: a subscription run adds nothing to the metered side
			out[i].Basis = contract.ResourceBasisSubscription
			out[i].Note = &note
		}
	}
	return out
}

// runCost: the run's cost figures, priced here unless its group came priced from SQL.
func (r *runnerRow) runCost(prices []db.PriceEntry) []contract.CostFigure {
	if r.cost != nil {
		return r.cost
	}
	return runnerCost(prices, isRunnerSubscription(r), runnerUsages(r))
}

// callGroupCost: a call group's cost figures from its sums per currency and basis (RunnerCallGroups costs): one
// figure per currency and basis, and the unpriced calls as one unavailable figure.
func callGroupCost(raw []byte) []contract.CostFigure {
	var sums []struct {
		Currency *string  `json:"currency"`
		Basis    *string  `json:"basis"`
		Value    *float64 `json:"value"`
		Unpriced int64    `json:"unpriced"`
	}
	out := []contract.CostFigure{}
	if json.Unmarshal(raw, &sums) != nil {
		return out
	}
	unpriced := int64(0)
	for _, z := range sums {
		unpriced += z.Unpriced
		if z.Currency == nil || z.Value == nil {
			continue
		}
		basis := contract.ResourceBasisEstimated
		if z.Basis != nil && *z.Basis != "" {
			basis = contract.ResourceBasis(*z.Basis)
		}
		cur, val := *z.Currency, *z.Value
		out = append(out, contract.CostFigure{Value: &val, Currency: &cur, Basis: basis, Prices: []contract.CostPriceRef{}})
	}
	if unpriced > 0 {
		reason := unavailableCostReason
		out = append(out, contract.CostFigure{Basis: contract.ResourceBasisUnavailable, Reason: &reason, Usages: &unpriced})
	}
	sortCostFigures(out)
	return out
}

func runnerUsages(r *runnerRow) []usage {
	if r.grouped {
		return r.usages
	}
	if r.kind == "tool_run" {
		unit := runnerUnit(r)
		if unit == nil {
			return []usage{{r.subject, "", nil, r.at}}
		}
		return []usage{{r.subject, *unit, runnerAmount(r), r.at}}
	}
	out := []usage{}
	for _, u := range []struct {
		unit string
		v    *int64
	}{{"input_tokens", r.tokensInput}, {"output_tokens", r.tokensOutput}, {"cache_read_tokens", r.tokensCacheRead}, {"cache_write_tokens", r.tokensCacheWrite}} {
		if u.v != nil && *u.v > 0 {
			v := float64(*u.v)
			out = append(out, usage{deref(r.model), u.unit, &v, r.at})
		}
	}
	return out
}

func executionsRunners(q *Request, _ *contract.ExecutionsRunnersRequest) (contract.ExecutionsRunnersReply, error) {
	prices, err := priceEntries(q)
	if err != nil {
		return contract.ExecutionsRunnersReply{}, err
	}
	// Active runs first, so a long run is never cut off by newer ended ones. ponytail: newest 500; page by host when busier.
	// Tool calls (gateway calls, thousands a day) are not rows of their own: they are summed per caller below.
	rs, err := q.db().RunnerRuns(q.ctx())
	if err != nil {
		return contract.ExecutionsRunnersReply{}, err
	}
	runs := make([]runnerRow, 0, len(rs))
	for _, r := range rs {
		var attempt *int64
		if r.Attempt > 0 {
			attempt = ptr(r.Attempt)
		}
		runs = append(runs, runnerRow{attempt: attempt, id: r.ID, kind: r.Kind, state: r.State, hostID: r.HostID, harness: r.Harness, taskID: r.TaskID, title: r.Title, sessionTitle: nonEmpty(r.SessionTitle),
			sessionID: r.SessionID, parentID: r.ParentExecutionID, problem: r.Problem, started: runnerMs(r.At), ended: runnerMs(r.EndedAt), seen: runnerMs(r.LastSeenAt),
			wallMs: runnerF(r.WallMs), cpuMs: runnerF(r.CpuMs), peakMemoryBytes: runnerF(r.PeakMemoryBytes), tokens: runnerTokens(r.TokensInput, r.TokensOutput, r.TokensCacheRead, r.TokensCacheWrite),
			subject: r.Subject, connectionCost: r.ConnectionCost, billingBasis: r.BillingBasis, model: r.Model, accountID: r.AccountID, isAPIConn: r.IsApiConn,
			tokensInput: r.TokensInput, tokensOutput: r.TokensOutput, tokensCacheRead: r.TokensCacheRead, tokensCacheWrite: r.TokensCacheWrite, at: runnerTime(r.At)})
	}
	// Tool calls (gateway calls, tens of thousands a day) are summed per host and caller in SQL, priced from
	// execution_spend (queries/runners.sql RunnerCallGroups).
	gs, err := q.db().RunnerCallGroups(q.ctx())
	if err != nil {
		return contract.ExecutionsRunnersReply{}, err
	}
	groups := make([]runnerRow, 0, len(gs))
	for _, g := range gs {
		calls, failed := g.Calls, g.Failed
		state := "succeeded"
		if g.Running {
			state = "running"
		} else if failed > 0 {
			state = "failed"
		}
		r := runnerRow{id: "calls:" + g.Host + "/" + g.Caller, kind: "tool_run", state: state, hostID: optStr(g.Host), sessionID: optStr(g.SessionID),
			taskID: optStr(g.TaskID), title: optStr(g.Title), parentID: optStr(g.ParentExecutionID), problem: optStr(g.Problem),
			calls: &calls, failedCalls: &failed, grouped: true, usages: []usage{}, cost: callGroupCost(g.Costs)}
		if g.StartedAt.Valid {
			r.started = runnerMs(&g.StartedAt.Time)
		}
		if g.EndedAt.Valid && !g.Running {
			r.ended = runnerMs(&g.EndedAt.Time)
		}
		if g.WallMs >= 0 {
			r.wallMs = runnerF(&g.WallMs)
		}
		groups = append(groups, r)
	}
	out := contract.ExecutionsRunnersReply{ServerNow: time.Now().UnixMilli(), Runs: []contract.RunnerRun{}, Spend: []contract.RunnerSpend{}}
	runs = append(runs, groups...)
	for i := range runs {
		r := &runs[i]
		out.Runs = append(out.Runs, contract.RunnerRun{Id: r.id, Kind: r.kind, State: contract.ExecutionState(r.state), HostId: r.hostID, Harness: r.harness, Model: r.model,
			TaskId: r.taskID, TaskTitle: r.title, SessionTitle: r.sessionTitle, SessionId: r.sessionID, Problem: r.problem, StartedAt: r.started, EndedAt: r.ended, LastSeenAt: r.seen,
			WallMs: r.wallMs, CpuMs: r.cpuMs, PeakMemoryBytes: r.peakMemoryBytes, Tokens: r.tokens, Calls: r.calls, FailedCalls: r.failedCalls,
			ParentExecutionId: r.parentID, Attempt: r.attempt, Cost: r.runCost(prices)})
	}
	// Spend today and this month per host, from execution_spend (spend_summary by host: the cost priced once per
	// execution, read index-only), as rows of kind "all"; today's runs per host and kind come as rows of their kind
	// without tokens or cost. Wall and CPU time per period are not summed here (pages/runners.md shows them per run).
	now := time.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := day.Add(24 * time.Hour)
	for _, p := range []struct {
		name     string
		from, to time.Time
	}{{"today", day, end}, {"month", month, end}} {
		include := contract.SystemActivityInclude // the coordinator's own work runs on hosts too
		sp, err := spendSummary(q, &contract.SpendSummaryRequest{By: contract.SpendGroupByHost, System: &include,
			Window: contract.AnalyticsWindow{From: jsonBytes(p.from.Format(time.RFC3339)), To: jsonBytes(p.to.Format(time.RFC3339))}})
		if err != nil {
			return contract.ExecutionsRunnersReply{}, err
		}
		for _, r := range sp.Rows {
			host := r.Key
			if host == "unknown" {
				host = ""
			}
			tokens := float64(r.Tokens)
			out.Spend = append(out.Spend, contract.RunnerSpend{HostId: optStr(host), Kind: "all", Period: p.name, Runs: r.Runs, Tokens: &tokens, Cost: r.Cost})
		}
	}
	kinds, err := q.db().RunnerRunsByKind(q.ctx(), pgtype.Timestamptz{Time: day, Valid: true})
	if err != nil {
		return contract.ExecutionsRunnersReply{}, err
	}
	for _, k := range kinds {
		out.Spend = append(out.Spend, contract.RunnerSpend{HostId: optStr(k.HostID), Kind: k.Kind, Period: "today", Runs: k.Runs, Cost: []contract.CostFigure{}})
	}
	return out, nil
}

func init() {
	op("executions_runners", executionsRunners)
}

func runnerTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
