// Runners screen (docs/design-system/pages/runners.md): what executes work per host, with its time and resources, and
// spend today and this month per host and execution kind. CI jobs come from the GitHub source (separate query).
package main

import (
	"time"

	"fleet/contract"
)

// runnerAt: when an execution ran (end, else start, else the start a gateway call carries in data, else last seen).
const runnerAt = `coalesce(e.ended_at,e.started_at,CASE WHEN e.data->>'startedAt' ~ '^[0-9]{1,15}$' THEN to_timestamp((e.data->>'startedAt')::bigint/1000.0) END,e.last_seen_at)`

// runnerTokens: input + output + cache; null when none of them was measured (unknown, never 0).
const runnerTokens = `CASE WHEN coalesce(x.tokens_input,x.tokens_output,x.tokens_cache_read,x.tokens_cache_write) IS NULL THEN NULL
 ELSE coalesce(x.tokens_input,0)+coalesce(x.tokens_output,0)+coalesce(x.tokens_cache_read,0)+coalesce(x.tokens_cache_write,0) END`

// runnerUsage: the priced usages of one execution: a gateway call's connection cost, an agent run's tokens per model,
// priced at the run's start (fleet-analytics.md, price table). A run on a subscription login (account_id) adds no money.
const runnerUsage = `coalesce(e.data->>'connection','')||'/'||coalesce(e.data->>'tool','') AS subject,x.connection_cost->>'unit' AS unit,
 coalesce(x.connection_cost->>'amount',x.connection_cost->>(x.connection_cost->>'unit'))::float8 AS amount,
 e.model,e.account_id,x.tokens_input,x.tokens_output,x.tokens_cache_read,x.tokens_cache_write,coalesce(e.started_at,` + runnerAt + `) AS at`

func runnerUsages(r doc) []usage {
	at, _ := r["at"].(time.Time)
	if r["kind"] == "tool_run" {
		if r["unit"] == nil {
			return []usage{{astr(r["subject"]), "", nil, at}}
		}
		var a *float64
		if v, ok := numOK(r["amount"]); ok {
			a = &v
		}
		return []usage{{astr(r["subject"]), astr(r["unit"]), a, at}}
	}
	out := []usage{}
	if r["account_id"] != nil {
		return out // subscription: shown as tokens, never as money
	}
	for _, u := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"} {
		col := map[string]string{"input_tokens": "tokens_input", "output_tokens": "tokens_output", "cache_read_tokens": "tokens_cache_read", "cache_write_tokens": "tokens_cache_write"}[u]
		if v, ok := numOK(r[col]); ok && v > 0 {
			out = append(out, usage{astr(r["model"]), u, &v, at})
		}
	}
	return out
}

func executionsRunners(q *Request, _ *contract.ExecutionsRunnersRequest) (contract.ExecutionsRunnersReply, error) {
	prices, err := priceEntries(q)
	if err != nil {
		return contract.ExecutionsRunnersReply{}, err
	}
	ms := func(col string) string { return `(extract(epoch FROM ` + col + `)*1000)::bigint` }
	// Active runs first, so a long run is never cut off by newer ended ones. ponytail: newest 500; page by host when busier.
	runs, err := queryRows(q, `SELECT e.id,e.kind::text AS kind,e.state::text AS state,e.host_id,e.harness,e.task_id,t.title,e.session_id,e.problem,
	 `+ms("coalesce(e.started_at,"+runnerAt+")")+` AS started,`+ms("e.ended_at")+` AS ended,`+ms("e.last_seen_at")+` AS seen,
	 x.wall_ms,x.cpu_ms,x.peak_memory_bytes,`+runnerTokens+` AS tokens,`+runnerUsage+`
	 FROM execution e LEFT JOIN execution_totals x ON x.execution_id=e.id LEFT JOIN task t ON t.id=e.task_id
	 WHERE e.kind NOT IN ('pipeline_run','pipeline_step') AND (e.state IN ('preparing','running','waiting') OR `+runnerAt+` > now()-interval '24 hours')
	 ORDER BY (e.state IN ('preparing','running','waiting')) DESC,e.seq DESC LIMIT 500`)
	if err != nil {
		return contract.ExecutionsRunnersReply{}, err
	}
	out := doc{"serverNow": time.Now().UnixMilli(), "runs": []any{}, "spend": []any{}}
	for _, r := range runs {
		out["runs"] = append(out["runs"].([]any), doc{"id": r["id"], "kind": r["kind"], "state": r["state"], "hostId": r["host_id"], "harness": r["harness"], "model": r["model"],
			"taskId": r["task_id"], "taskTitle": r["title"], "sessionId": r["session_id"], "problem": r["problem"], "startedAt": r["started"], "endedAt": r["ended"], "lastSeenAt": r["seen"],
			"wallMs": r["wall_ms"], "cpuMs": r["cpu_ms"], "peakMemoryBytes": r["peak_memory_bytes"], "tokens": r["tokens"], "cost": estimateCost(prices, runnerUsages(r))})
	}
	// Spend: every execution this UTC month, by when it ran (end, else start), summed per host, kind and period in Go so
	// costs are priced per usage. ponytail: a full-month scan per call; aggregate closed days in SQL when months grow large.
	rows, err := queryRows(q, `SELECT e.host_id,e.kind::text AS kind,(`+runnerAt+` >= date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') AS today,
	 x.execution_id IS NOT NULL AS measured,x.wall_ms,x.cpu_ms,`+runnerTokens+` AS tokens,`+runnerUsage+`
	 FROM execution e LEFT JOIN execution_totals x ON x.execution_id=e.id
	 WHERE e.kind NOT IN ('pipeline_run','pipeline_step') AND `+runnerAt+` >= date_trunc('month',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`)
	if err != nil {
		return contract.ExecutionsRunnersReply{}, err
	}
	type key struct{ host, kind, period string }
	type acc struct {
		runs, measured    int
		wall, cpu, tokens *float64
		us                []usage
	}
	sums, order := map[key]*acc{}, []key{}
	add := func(p **float64, v any) {
		if f, ok := numOK(v); ok {
			if *p == nil {
				*p = new(float64)
			}
			**p += f
		}
	}
	for _, r := range rows {
		periods := []string{"month"}
		if r["today"] == true {
			periods = append(periods, "today")
		}
		for _, p := range periods {
			k := key{astr(r["host_id"]), astr(r["kind"]), p}
			a := sums[k]
			if a == nil {
				a = &acc{}
				sums[k], order = a, append(order, k)
			}
			a.runs++
			if r["measured"] == true {
				a.measured++
			}
			add(&a.wall, r["wall_ms"])
			add(&a.cpu, r["cpu_ms"])
			add(&a.tokens, r["tokens"])
			a.us = append(a.us, runnerUsages(r)...)
		}
	}
	for _, k := range order {
		a := sums[k]
		var host any
		if k.host != "" {
			host = k.host
		}
		out["spend"] = append(out["spend"].([]any), doc{"hostId": host, "kind": k.kind, "period": k.period, "runs": a.runs, "measured": a.measured, "wallMs": a.wall, "cpuMs": a.cpu, "tokens": a.tokens, "cost": estimateCost(prices, a.us)})
	}
	return replyAs[contract.ExecutionsRunnersReply](out, nil)
}

func init() {
	operation("executions_runners", true, typedOp(executionsRunners))
}
