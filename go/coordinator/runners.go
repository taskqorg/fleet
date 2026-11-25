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
)

// runnerRow: one execution as the Runners screen reads it (queries/runners.sql), or a group of tool calls.
type runnerRow struct {
	id, kind, state                                                            string
	hostID, harness, taskID, title, sessionTitle, sessionID, parentID, problem *string
	callerRun, model, accountID, billingBasis                                  *string
	started, ended, seen, wallMs, cpuMs, peakMemoryBytes, tokens               *float64
	subject                                                                    string
	connectionCost                                                             []byte
	isAPIConn                                                                  bool
	tokensInput, tokensOutput, tokensCacheRead, tokensCacheWrite               *int64
	at                                                                         time.Time
	calls, failedCalls                                                         *int64
	usages                                                                     []usage // a tool-call group, priced already per call
	grouped                                                                    bool
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

// toolCallGroups: the tool calls of one caller (parent execution, else task, else connection) as one row: calls and
// failed calls, first start, last end, summed wall time, all usages priced together; failed when any call failed,
// running while any runs.
func toolCallGroups(calls []runnerRow) []runnerRow {
	groups, order := map[string]*runnerRow{}, []string{}
	for i := range calls {
		c := &calls[i]
		// Per host (the screen lists runs by host), then the caller: its parent run when that run exists, else its task,
		// else the connection (the coordinator's own reads, e.g. GitHub, carry a fresh session and parent id per call).
		key := deref(c.hostID) + "/" + firstString(deref(c.callerRun), deref(c.taskID), "conn:"+strings.SplitN(c.subject, "/", 2)[0])
		g := groups[key]
		if g == nil {
			zero := int64(0)
			failed := int64(0)
			g = &runnerRow{id: "calls:" + key, kind: "tool_run", state: "succeeded", hostID: c.hostID, sessionID: c.sessionID, taskID: c.taskID, title: c.title,
				parentID: c.parentID, calls: &zero, failedCalls: &failed, usages: []usage{}, grouped: true}
			groups[key], order = g, append(order, key)
		}
		*g.calls++
		switch c.state {
		case "failed", "cancelled":
			*g.failedCalls++
			if g.state != "running" {
				g.state = "failed"
			}
		case "preparing", "running", "waiting":
			g.state = "running"
		}
		if v := c.started; v != nil && (g.started == nil || *v < *g.started) {
			g.started = ptr(*v)
		}
		if v := c.ended; v != nil && (g.ended == nil || *v > *g.ended) {
			g.ended = ptr(*v)
		}
		if v := c.wallMs; v != nil {
			w := *v
			if g.wallMs != nil {
				w += *g.wallMs
			}
			g.wallMs = &w
		}
		if c.problem != nil && g.problem == nil { // calls come newest first: the latest cause
			g.problem = c.problem
		}
		g.usages = append(g.usages, runnerUsages(c)...)
	}
	out := make([]runnerRow, 0, len(order))
	for _, k := range order {
		if g := groups[k]; g.state == "running" {
			g.ended = nil
		}
		out = append(out, *groups[k])
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
		runs = append(runs, runnerRow{id: r.ID, kind: r.Kind, state: r.State, hostID: r.HostID, harness: r.Harness, taskID: r.TaskID, title: r.Title, sessionTitle: nonEmpty(r.SessionTitle),
			sessionID: r.SessionID, parentID: r.ParentExecutionID, problem: r.Problem, started: runnerMs(r.At), ended: runnerMs(r.EndedAt), seen: runnerMs(r.LastSeenAt),
			wallMs: runnerF(r.WallMs), cpuMs: runnerF(r.CpuMs), peakMemoryBytes: runnerF(r.PeakMemoryBytes), tokens: runnerTokens(r.TokensInput, r.TokensOutput, r.TokensCacheRead, r.TokensCacheWrite),
			subject: r.Subject, connectionCost: r.ConnectionCost, billingBasis: r.BillingBasis, model: r.Model, accountID: r.AccountID, isAPIConn: r.IsApiConn,
			tokensInput: r.TokensInput, tokensOutput: r.TokensOutput, tokensCacheRead: r.TokensCacheRead, tokensCacheWrite: r.TokensCacheWrite, at: runnerTime(r.At)})
	}
	// ponytail: the newest 20000 calls of the day; a busier gateway needs the sums in SQL.
	cs, err := q.db().RunnerCalls(q.ctx())
	if err != nil {
		return contract.ExecutionsRunnersReply{}, err
	}
	calls := make([]runnerRow, 0, len(cs))
	for _, r := range cs {
		calls = append(calls, runnerRow{callerRun: r.CallerRun, id: r.ID, kind: r.Kind, state: r.State, hostID: r.HostID, harness: r.Harness, taskID: r.TaskID, title: r.Title,
			sessionID: r.SessionID, parentID: r.ParentExecutionID, problem: r.Problem, started: runnerMs(r.At), ended: runnerMs(r.EndedAt), seen: runnerMs(r.LastSeenAt),
			wallMs: runnerF(r.WallMs), cpuMs: runnerF(r.CpuMs), peakMemoryBytes: runnerF(r.PeakMemoryBytes), tokens: runnerTokens(r.TokensInput, r.TokensOutput, r.TokensCacheRead, r.TokensCacheWrite),
			subject: r.Subject, connectionCost: r.ConnectionCost, billingBasis: r.BillingBasis, model: r.Model, accountID: r.AccountID, isAPIConn: r.IsApiConn,
			tokensInput: r.TokensInput, tokensOutput: r.TokensOutput, tokensCacheRead: r.TokensCacheRead, tokensCacheWrite: r.TokensCacheWrite, at: runnerTime(r.At)})
	}
	out := contract.ExecutionsRunnersReply{ServerNow: time.Now().UnixMilli(), Runs: []contract.RunnerRun{}, Spend: []contract.RunnerSpend{}}
	runs = append(runs, toolCallGroups(calls)...)
	for i := range runs {
		r := &runs[i]
		out.Runs = append(out.Runs, contract.RunnerRun{Id: r.id, Kind: r.kind, State: contract.ExecutionState(r.state), HostId: r.hostID, Harness: r.harness, Model: r.model,
			TaskId: r.taskID, TaskTitle: r.title, SessionTitle: r.sessionTitle, SessionId: r.sessionID, Problem: r.problem, StartedAt: r.started, EndedAt: r.ended, LastSeenAt: r.seen,
			WallMs: r.wallMs, CpuMs: r.cpuMs, PeakMemoryBytes: r.peakMemoryBytes, Tokens: r.tokens, Calls: r.calls, FailedCalls: r.failedCalls,
			ParentExecutionId: r.parentID, Cost: runnerCost(prices, isRunnerSubscription(r), runnerUsages(r))})
	}
	// Spend: every execution this UTC month, by when it ran (end, else start), summed per host, kind and period in Go so
	// costs are priced per usage. ponytail: a full-month scan per call; aggregate closed days in SQL when months grow large.
	rows, err := q.db().RunnerSpendRows(q.ctx())
	if err != nil {
		return contract.ExecutionsRunnersReply{}, err
	}
	type key struct{ host, kind, period string }
	type acc struct {
		runs, measured    int64
		wall, cpu, tokens *float64
		us, subUs         []usage // metered, and on a subscription login
	}
	sums, order := map[key]*acc{}, []key{}
	add := func(p **float64, v *float64) {
		if v != nil {
			if *p == nil {
				*p = new(float64)
			}
			**p += *v
		}
	}
	for _, s := range rows {
		r := &runnerRow{kind: s.Kind, hostID: s.HostID, subject: s.Subject, connectionCost: s.ConnectionCost, billingBasis: s.BillingBasis, model: s.Model, accountID: s.AccountID,
			isAPIConn: s.IsApiConn, tokensInput: s.TokensInput, tokensOutput: s.TokensOutput, tokensCacheRead: s.TokensCacheRead, tokensCacheWrite: s.TokensCacheWrite, at: runnerTime(s.At)}
		periods := []string{"month"}
		if s.Today {
			periods = append(periods, "today")
		}
		for _, p := range periods {
			k := key{deref(s.HostID), s.Kind, p}
			a := sums[k]
			if a == nil {
				a = &acc{}
				sums[k], order = a, append(order, k)
			}
			a.runs++
			if s.Measured {
				a.measured++
			}
			add(&a.wall, runnerF(s.WallMs))
			add(&a.cpu, runnerF(s.CpuMs))
			add(&a.tokens, runnerTokens(s.TokensInput, s.TokensOutput, s.TokensCacheRead, s.TokensCacheWrite))
			if isRunnerSubscription(r) {
				a.subUs = append(a.subUs, runnerUsages(r)...)
			} else {
				a.us = append(a.us, runnerUsages(r)...)
			}
		}
	}
	for _, k := range order {
		a := sums[k]
		out.Spend = append(out.Spend, contract.RunnerSpend{HostId: optStr(k.host), Kind: k.kind, Period: k.period, Runs: a.runs, Measured: a.measured, WallMs: a.wall, CpuMs: a.cpu,
			Tokens: a.tokens, Cost: append(runnerCost(prices, false, a.us), runnerCost(prices, true, a.subUs)...)})
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
