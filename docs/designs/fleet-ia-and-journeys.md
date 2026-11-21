# Target Information Architecture, Core Journeys & Agentic Method

Status: **Shipped (8-section rail + Library / Infrastructure sub-tabs)** — the navigation rail in [registry.ts](../../packages/ui/src/registry.ts) and [main.tsx](../../app/src/main.tsx) now consolidates into the 8 top-level sections (`Work`: Overview, Conversations, Projects, Tasks, Spend; `System`: Lab, Library, Infrastructure) with top sub-navigation tabs for `Library` (`/agents`, `/pipelines`, `/catalogue`, `/changes`) and `Infrastructure` (`/hosts`, `/runners`, `/keys`), while preserving all deep links, manifests, and Spotlight (`⌘K`) access.

This specification establishes the closed top-level navigation map, column contracts, end-to-end user journeys (`J1`–`J5`), and the agentic product-development method for Fleet, building on [fleet.md](../../fleet.md), [owner rules](../design-system/owner-rules.md), and [navigation and columns](../design-system/patterns/navigation-and-columns.md).

---

## 1. Closed 8-Section Information Architecture

The current 15-section rail splits tightly coupled operator and architect workflows across thin single-file screens (`Catalogue`, `Changes`, `Runners`, `API keys`, `Results`). Fleet consolidates into **8 top-level sections** in two role groups (`WORK` and `SYSTEM`), leaving global navigation/filtering to the shell header (`Focus` + `Spotlight ⌘K`, `OR-32`, `OR-34`):

| Group | Section | Core Question Answered | Control Column (`size-ctl`) | Work Area (idle) | Work Area (open item) |
|---|---|---|---|---|---|
| **Work** | **Overview** | What needs me right now and what is currently running? | `Needs you` inbox (questions, failed steps, undecided Lab series) | Active executions pulse, delivery watchlist, system health alerts | Opens inline action or navigates to the linked task/conversation/run |
| **Work** | **Conversations** | How do I discuss, plan and delegate work with a PM? | Filterable PM session list | Start a new PM conversation (`StartPicker`) | Live streaming transcript + composer |
| **Work** | **Projects** | What are our real products, rules, recommended methods and outputs? | Project list (`Projects`) | Cross-project active work & health summary | Project detail with tabs: **Work & Rules** · **Methods** (recommendations) · **Delivery** (PRs/releases) · **Results** (artifacts) |
| **Work** | **Tasks** | What real tasks are queued, running, waiting or finished? | Virtualized task list (`OR-33` saved views + query search) | Summary strip & backlog flow by project | Task detail (`ExecutionTree`, transcript, linked **Results** & **Delivery** stages) |
| **Work** | **Spend** | Where do time, tokens and money go across projects and models? | Optional window/grouping controls | Latency/cost/token breakdown by project, purpose, model, subscription vs API (`spend_summary`) | Drill-down to supporting executions |
| **System** | **Lab** | Which model, harness or pipeline is best for each use case right now? | `CatalogueTree` of **Use Cases** (`LLM Code Benchmarks`, `Real Work`, `Fleet & Projects`, folded `Archived`) & **Runs** | **Leaderboard / Suite Matrix** (Model × Pipeline pass rate/metric, median time, cost/tokens) + **2-click Run launcher** | **Use-case card** (examples + run history + quick A/B launch) OR **Experiment detail** (live step progress, metric matrix, Promote) |
| **System** | **Library** | What methods, pipelines, agents and skills are available and recommended? | `CatalogueTree` grouped by role/domain (`Methods`, `Pipelines`, `Agents`, `Skills`; Lab variants folded by default) | Library summary (`Totals` by harness/status, pending Git sync banner if drifted) + Skills rollout | Editor/Detail obeying `OR-45 Diff from default` (`Methods` with status badge, `Pipelines` DAG, `Agents` overrides) |
| **System** | **Infrastructure** | Are hosts, accounts and connections healthy and compatible with our models? | Section tree (`Hosts`, `Accounts`, `Connections & API keys`, `Runners`) | Live readiness matrix (hosts × harnesses × probed models + runner queue health) | Host detail (capabilities, logs, updates), Account detail (probed supported models & rate limits), Connection/Key config |

### Route Consolidation Map (15 → 8)

Agents **must not** add a new top-level navigation item or a duplicate always-open search input (`OR-31`, `OR-32`) without updating this specification first.

| Current Section (`registry.ts`) | Target Destination | Transition Notes |
|---|---|---|
| `Overview`, `Tasks`, `Conversations`, `Projects`, `Spend` | Unchanged (5 top-level sections) | `Projects` and `Tasks` absorb project-scoped results and delivery tabs. |
| `Results` | Embedded inside `Tasks` and `Projects` + reachable via `Spotlight (⌘K)` | Unlinked raw artifacts do not justify a standalone top-level rail item. |
| `Delivery` | Embedded inside `Projects` (Delivery tab), `Tasks` (`StageChip`), and `Overview` pulse | Keeps release pipeline visibility alongside the work that produced it. |
| `Lab` | `Lab` (redesigned around Use-case suites, Leaderboard, and fast launcher) | `Use-case registry` promotes from a buried idle tab to the main navigator. |
| `Catalogue`, `Agents`, `Pipelines` | Unified inside `Library` (`CatalogueTree` roots) | Methods, pipelines, agents and skills share one workflow and promotion target. |
| `Changes` | Surfaced inside `Library` (repository/store sync banner) and `Infrastructure` (host/pin drift) | Drift is an operational signal where the object lives, not a separate destination. |
| `Hosts`, `Runners`, `API keys` | Unified inside `Infrastructure` | Hosts, per-account probed model lists, connections/keys and CI runners live together. |

---

## 2. Lab-First Evaluation Model

1. **Aggregate comparison (`Leaderboard`) before raw experiment rows.**
   When an architect selects a collection folder in `CatalogueTree` (such as `LLM Code Benchmarks` or `Real Work`), the Work Area answers the primary question first: across all eligible runs on the same suite/evaluator version, what is each variant's **metric score (`0.00–1.00`)**, **median wall duration**, **token / list-price cost (including subscription usage)**, and **consistency spread** across repeats.
2. **Use case as a first-class navigable entity.**
   Selecting a use case opens its card: goal & expected outcome, attached approved `examples`, historic runs/scores per model/pipeline, and an inline **Run in Lab** trigger.
3. **2-click fast launch without manual JSON.**
   - Selecting models, agents, pipelines or examples uses [`EntityPicker`](../design-system/controls/entity-picker.md) (checkboxes over [`CatalogueTree`](../design-system/controls/catalogue-tree.md)) rather than raw JSON overrides for standard A/B comparisons.
   - `Goal`, `Hypothesis`, and `Decision rule` default cleanly from the selected use case and variants (editable in an expandable section) instead of blocking quick runs.
4. **Live step visibility & honest cost accounting.**
   - Running multi-step trials display current step progress (`step 2/3 · review`), active host and elapsed time in place (`OR-6`).
   - Subscription executions expose input/output/cache tokens and `list-price equivalent` beside measured API credits so model efficiency can always be compared.
5. **Continuous `metric` scoring by default.**
   Multi-check coding and real-work benchmarks score `passed / total` (`script:example.checks:metric`) so partial improvements (e.g., 3-step `implement → review → fix` climbing from `0.00` to `0.50`) remain measurable.

---

## 3. Core User Journeys (`J1`–`J5`)

Every feature PR affecting operator or architect workflows validates the affected journey via shared core operations (`fleet.md` R2/R7) and live browser checks:

- **J1 — Best model/method at a glance (`< 5 s`):** Architect opens `Lab`, picks a collection (`LLM Code Benchmarks` or `Real Work`), and reads a clean comparison matrix (`Model × Pipeline` → metric score, median wall time, tokens/cost, failure rate) with zero raw commit hashes or UUIDs (`OR-38`).
- **J2 — Fast A/B or multi-step Lab run (`<= 3` clicks):** From any use case or collection, architect chooses `Run in Lab`, toggles 2+ models/pipelines via `EntityPicker`, and clicks `Launch` without writing JSON. Multi-step trials report live per-step state (`implement`, `review`, `fix`) and specific `problem` causes on failures.
- **J3 — Promote winner to Recommended (`1` action + scope):** From an experiment or use-case comparison, architect promotes a winning variant to `Library → Methods` with lifecycle status (`experimental` → `recommended`) and project/system scope. `Library` and `Projects` reflect updated measured quality/cost evidence immediately without requiring manual `config/catalogue.json` edits.
- **J4 — Real project execution with full attribution:** Operator (or PM session) creates a project task; system allocates a compatible host/account, executes using the project's recommended method (or explicit override), retains artifacts and delivery stage on the task/project, and attributes all tokens/cost/time to `Spend`.
- **J5 — Proactive environment & account compatibility:** `Infrastructure` probes actual supported models per bound host account during inspection/startup and marks incompatible profiles before dispatch; router (`route_decisions`) skips accounts known to reject a requested model (`modelNotFound`) while keeping manual inspection visible.

---

## 4. Agentic Product-Development Method

To prevent fragmented screens and misaligned contracts across parallel agent PRs (both in Fleet and in downstream products built through Fleet):

1. **Closed IA Map before UI code:** Every section and new control has an agreed entry in this map and [navigation and columns](../design-system/patterns/navigation-and-columns.md) before implementation starts.
2. **Journey-driven task briefs:** Parallel `/swarm` workers own disjoint slices anchored to `J1`–`J5` and `OR-1`–`OR-45` (not isolated single-file tickets unaware of adjacent workflows).
3. **One core query, two consumers (`R2`/`R7`):** Every aggregation shown in the UI (such as use-case leaderboards or per-account model compatibility) lives in a single shared Go coordinator operation so agents and people see identical facts.
4. **Live Playwright journey verification:** After merge-queue landing on prod, automated checks walk `J1`–`J5` end-to-end against real data, verifying zero console errors, `OR-45 Diff from default` conciseness, and honest error/cost states.
