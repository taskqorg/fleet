# Fleet analytics: the work query and the price table

Fleet's analytics is not a subsystem. Experiments, their records, statistics and the comparison screen belong to [Lab](fleet-lab.md#experiment-record); spans belong to [telemetry](fleet-telemetry.md#5-query-surface). This file owns two things only: `analytics_query`, one typed query over work history, and the price table that turns usage into estimated money.

Status: accepted design; schema, coordinator `analytics_query`, entity resource rollups, and price table built (UI views tracked in [design system](../design-system/README.md)).

## Terms

| Term | Meaning |
|---|---|
| Work record | A leaf `execution` row (`agent_run`, `script`, `tool_run`, or measured `child_agent`) with its `execution_totals` row; container kinds (`pipeline_run`, `pipeline_step`) and placeholder `child_agent` rows without totals are excluded from primary sums. |
| Grouping columns | Typed columns written once when an `execution` row is created: `pipeline_id`, `pipeline_revision`, `step_id`, `profile_id`, `profile_revision`, alongside `project_id`, `kind`, `parent_execution_id`, `harness`, `model`, `host_id`, `experiment_id`, `variant`, `state`, and `started_at`. |
| Basis | How a figure was obtained: `measured`, `estimated`, `subscription` or `unavailable`. |
| Time spans | `elapsed_ms = MAX(ended_at) - MIN(started_at)` across participating leaf executions (turnaround wall span); `execution_wall_ms = SUM(wall_ms)` (parallel effort sum); `cpu_ms = SUM(cpu_ms)` (measured OS CPU time). |
| Price entry | One row of the price table: what one unit of one model or connection tool costs, from one source, on one date. |

## `analytics_query`

One operation under the [data foundation §6](fleet-data-foundation.md#6-operations-for-people-and-agents) permission model, for people, the PM agent and the Lab screen (`contracts/ops-analytics.schema.json`). No raw SQL.

**Request.** `{window: {from, to}, dimensions[], measures[], filters{}}`. The window `[from, to)` (timestamps as epoch milliseconds, RFC 3339, or `YYYY-MM-DD`) is required; there is no default window and no cap on its length.

| Dimensions (fixed list) | Measures (fixed list) |
|---|---|
| `day`, `hour`, `week` (UTC), `project`, `kind` (execution kind), `parent`, `pipeline`, `pipeline_revision`, `step`, `profile_id`, `profile_revision`, `harness`, `model`, `provider`, `account`, `host`, `connection`, `experiment`, `variant`, `state` (final execution state), `outcome`, `person` | `executions` (alias `count`), `count`, `failed` (count in state `failed`), `tokens_input`, `tokens_output`, `tokens_cache_read`, `tokens_cache_write`, `tokens_reasoning`, `wall_ms` (sum), `wall_ms_p50`, `wall_ms_p95`, `active_ms`, `api_ms`, `cpu_ms`, `peak_memory_bytes`, `calls`, `credits`, `cost` |

**Reply.** `{rows, totals, hidden, unknown}`. Each bucket row carries `dimensions` (map from requested dimension name to non-null string, encoding SQL `NULL` / absent dimension values as `"unknown"`), numeric/integer measure structs `{value, basis}` (plus optional `note` / `reason` / `partial` when relevant), and `cost: CostFigure[]` (`{value, currency, basis, prices: [{id, observedAt}], note?, reason?, usages?}`). Reply-level `totals` gives the ungrouped `{measures, cost}` across visible matching rows; `hidden` counts leaf rows inside the unfiltered `[from, to)` window excluded by the caller's row-level security scope (never leaking inaccessible project names, IDs, or spend); `unknown` counts visible matching leaf rows where any requested dimension resolved to `"unknown"` (never dropped).

- A numeric measure that mixes bases reports the weakest one (`unavailable` < `subscription` < `estimated` < `measured`), with `value: null` when completely unmeasured; `peak_memory_bytes` uses `MAX`.
- Cost figures group strictly by `(currency, basis)`: metered charges (`measured` provider cost and `estimated` token tariffs) sum per currency and never combine with `subscription` figures or downgrade metered `basis`; subscription runs keep their own `(currency, "subscription")` list-price equivalent entry (`note: "subscription (list-price equivalent)"`); missing usage or missing prices produce `{value: null, currency: null, basis: "unavailable"}` (never `0`).

**Rules.**

- It runs inside a normal `read: true` transaction under the caller's row-level security (`fleet.project_ids`, `fleet.person`) and never sets `fleet.project_ids` to `'*'`. Projectless `directed_totals` access also enforces parent session non-revocation (`NOT (data ? 'directorAccessRevokedAt')`). Agent callers omitting an explicit project filter remain restricted to their bound project(s). A test proves project A sees nothing from project B, including through `score` joins and `tool_run` children.
- Grouping uses the typed columns on `execution` and indexed joins (`project`, `session`, `task`, `account` / `connection`, `price_entry`), never ad-hoc JSON extraction on `execution.data` for grouping columns.
- Nested usage is not counted twice: only leaf executions (`kind NOT IN ('pipeline_run', 'pipeline_step')` and measured `child_agent` rows carrying `execution_totals`) contribute tokens, durations, compute, tools, calls, and cost. Pre-attachment session turns resolve to a task deterministically via `session.data->>'taskId'`. Entity detail views (`task_get`, `session_get`) partition attributed leaf executions into non-overlapping `own`, `delegated`, and `spawned` scopes.
- Per-leaf `LATERAL` price lookup (`FROM price_entry_subject WHERE subject = ... AND unit = ... AND effective_from <= e.started_at ORDER BY effective_from DESC, seq DESC LIMIT 1`) runs inside the leaf CTE **before** outer `GROUP BY` and percentiles, preserving mid-bucket `effective_from` rate changes and exact `(id, observedAt)` provenance.
- `execution(project_id, started_at)` serves the window; rows with null `started_at` fall outside every window.
- Sources: `calls`, `connection`, `credits`, and money `measured` spend from `tool_run` / `execution_totals.connection_cost`; tokens, durations (`wall_ms`, `active_ms`, `api_ms`, `cpu_ms`), `peak_memory_bytes`, and gateway/harness metrics from `execution_totals`.
- `usage_summary` is not an implemented operation; cost totals come from `analytics_query` and shared `CostFigure[]` rollups.

**Lab use.** The Lab comparison reads trials through the same contract with `experiment` and `variant` as dimensions; the statistics on top are Lab's ([statistics](fleet-lab.md#statistics)).

## Price table

Money is estimated from usage and a price entry. Fleet has no money budget unit; budgets stay `trials | wall_ms | calls | credits`.

**Entry.** `{id, subject, unit, price, currency, source, source_ref, observed_at, effective_from}`.

- `subject`: a resolved model ID (never an alias such as `*-latest`) or `<connection>/<tool>`.
- `unit`: `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, or the connection tool's catalogue unit (the same spelling).
- `source`: `vendor` (the vendor's price page, the source of truth), `snapshot` (a fetched public price list, one optional input) or `owner` (hand-entered).
- Entries are append-only; a price change is a new entry. A figure uses the latest entry with `effective_from <= started_at`; none gives `unavailable`. `observed_at` is when the price was read; `effective_from` defaults to it and may be set earlier, so a price entered today can cover past work.

The table belongs to connections and data, beside the provider catalogue in [connections](fleet-connections.md#1-model). A connection with a fixed vendor price per unit gets its entry when its catalogue entry is added.

**Every cost figure** carries its basis, the price entry `id` and its `observed_at`.

| Basis | When |
|---|---|
| `measured` | The provider returned the amount in its own unit (credits or money). |
| `estimated` | Usage × a price entry. |
| `subscription` | The run used a subscription login; shown as "subscription", never as money. |
| `unavailable` | No usage or no price entry. |

The first experiment may use one `owner` entry with basis `estimated`; nothing else is needed before it.

Built: `price_entry`, `price_add`, `prices_list`, typed `CostFigure[]` / `ResourceSummary` / `ScopedResourceRollup` contracts, `analytics_query` with per-leaf `LATERAL` `price_entry_subject` pricing and RLS `hidden`/`unknown` counters, entity rollups in `task_get` / `session_get` / `definition_usage` / `executions_runners` / `lab_experiment_get`. Remaining: public vendor snapshot fetch pipeline and Analytics UI views.
