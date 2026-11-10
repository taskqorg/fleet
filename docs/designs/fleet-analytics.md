# Fleet analytics: the work query and the price table

Fleet's analytics is not a subsystem. Experiments, their records, statistics and the comparison screen belong to [Lab](fleet-lab.md#experiment-record); spans belong to [telemetry](fleet-telemetry.md#5-query-surface). This file owns two things only: `analytics_query`, one typed query over work history, and the price table that turns usage into estimated money.

Status: accepted design, not built. Order: the first decided Lab experiment comes first; this query comes after it ([Lab staging](fleet-lab.md#analytics-staging)).

## Terms

| Term | Meaning |
|---|---|
| Work record | An `execution` row with its `execution_totals` row; provider calls are `tool_run` child rows. |
| Grouping columns | Typed columns written once when an `execution` row is created: `pipeline_id`, `pipeline_revision`, `step_id`, `profile_revision`. |
| Basis | How a figure was obtained: `measured`, `estimated`, `subscription` or `unavailable`. |
| Price entry | One row of the price table: what one unit of one model or connection tool costs, from one source, on one date. |

## `analytics_query`

One operation under the [data foundation §6](fleet-data-foundation.md#6-operations-for-people-and-agents) permission model, for people, the PM agent and the Lab screen. No raw SQL.

**Request.** `{window: {from, to}, dimensions[], measures[], filters{}}`. The window is required; there is no default window and no cap on its length.

| Dimensions (fixed list) | Measures (fixed list) |
|---|---|
| `project`, `kind` (execution kind), `parent`, `pipeline`, `pipeline_revision`, `step`, `profile_revision`, `harness`, `model`, `host`, `connection`, `experiment`, `variant`, `state` (final execution state), `day` (UTC) | `count`, `failed` (count in state `failed`), `wall_ms` (sum, p50, p95), `active_ms` (sum), `tokens_input`, `tokens_output`, `tokens_cache_read`, `tokens_cache_write`, `calls`, `credits`, `cost` |

**Reply.** One row per group: dimension values, each measure as `{value, basis}`, plus, once per reply, `hidden` (rows in the window the caller may not see) and `unknown` (rows with no value for a requested dimension, grouped as `unknown`, never dropped). A measure that mixes bases reports the weakest one (`unavailable` < `subscription` < `estimated` < `measured`); `subscription` rows add no money. `cost` is summed per currency; `currency` is part of each cost value, and two currencies give two values. A missing price gives `cost: {value: null, basis: unavailable}`, never 0.

**Rules.**

- It runs under the caller's row-level security (`fleet.project_ids`) and never sets the project setting to `'*'`. A test proves that project A sees nothing from project B, including through `score` joins and `tool_run` children.
- Grouping uses the typed columns only, never JSON extraction from `execution.data`. The columns are added to `execution` in the [coordinator rewrite](fleet-stack.md#coordinator-rewrite) and filled at row creation; rows without them read as `unknown`.
- Nested usage is not counted again: each measure is summed from the row that recorded it (a `tool_run` for calls and credits, the run for tokens), never from a parent's rolled-up totals. Grouping by `parent` attributes children to their parent.
- A new index `execution(project_id, started_at)` serves the window; rows with null `started_at` fall outside every window.
- Sources: `calls` and `connection` from `tool_run` rows, `credits` and money from `execution_totals.connection_cost`, tokens and times from `execution_totals`.
- `usage_summary` is not an implemented operation; cost totals come from this query.

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

Not built: the table, the snapshot pipeline, the grouping columns, the operation.
