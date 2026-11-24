# Target Information Architecture, Core Journeys & Agentic Method

This specification establishes the closed top-level navigation map, column contracts, end-to-end user journeys (`J1`–`J5`), and the agentic product-development method for Fleet, building on [fleet.md](../../fleet.md), [owner rules](../design-system/owner-rules.md), and [navigation and columns](../design-system/patterns/navigation-and-columns.md).

---

<a id="1-closed-8-section-information-architecture"></a>

## 1. Closed Information Architecture

Fleet is used by talking to sessions ([owner principles](../design-system/owner-rules.md#principles)). The rail has seven sections; the owner also sees System › People. Home and Live are full-width pages; the other five are catalogue sections with `CatalogueTree` or `ListPagePanel` in the control column (`OR-24`, `OR-46`, `OR-48`). The shell header keeps the focus chip and Spotlight (`OR-32`, `OR-34`, `OR-43`).

| Section | Core question answered | Control column | Work area (idle) | Work area (open item) |
|---|---|---|---|---|
| **Home** | Who works for me, and what do they need from me? | none (full width) | Session cards: Waiting for you, Failed, Working, Queued; asleep bots with Wake; a composer that picks who to talk to; one pulse line (`OR-50`) | The session's conversation (`OR-51`) |
| **Live** | What runs where right now, and what is broken? | none (full width) | Hosts with worker slots and 24 h run strips, projects in flight, delivery, CI, recent events; red only for trouble (`OR-56`); readable on a wall screen | Host, task or pull request detail |
| **Conversations** | Every session, past and present | Session list with saved views and query search | Summary by project and state | The session's conversation (`OR-51`) |
| **Projects** | What are our products, their rules, methods and outputs? | Project list | Cross-project summary | Project with facets: Sessions · Tasks · Delivery · Results · Rules & methods |
| **Lab** | Which model, harness or pipeline is best for each use case right now? | `CatalogueTree` of Use cases and Runs | Leaderboard and the run launcher | Use-case card or experiment detail |
| **Library** | What methods, pipelines, agents, bots and skills are available and recommended? | `CatalogueTree`: Methods, Pipelines, Agents, Bots, Skills, Changes | Library summary and pending sync | Definition editor (`OR-45`) |
| **System** | Are hosts, accounts, connections and spend healthy, and who has access? | `CatalogueTree`: Hosts, Accounts & limits, Connections & keys, Runners, Spend, People | Readiness and spend summary | Host, account, connection, spend breakdown, person |

Tasks, results and delivery are reached from the session that does the work or from its project; they have no rail item. A screen opened from another section highlights the section that owns it (`OR-57`).

Not built yet: the app still shows the earlier rail (Overview, Conversations, Projects, Tasks, Spend, Bots, Lab, Library, Infrastructure, and Humans for the owner); the map below says where each item goes.

### Route consolidation map

Earlier rails had 15, then 9 sections. Legacy routes remain reachable as deep links and through Spotlight (`⌘K`); agents **must not** add a top-level navigation item, a `<nav class="sub-nav">` bar or a duplicate always-open search input (`OR-31`, `OR-32`, `OR-46`) without updating this specification first.

| Earlier section (`registry.ts`) | Destination | Notes |
|---|---|---|
| `Overview` | `Home` (sessions and their questions) and `Live` (the technical picture) | The Needs-you inbox becomes questions on session cards (`OR-40`, `OR-53`). |
| `Conversations`, `Projects`, `Lab` | Unchanged | Conversations open in the one session view (`OR-51`). |
| `Tasks`, `Results`, `Delivery` | Facets of `Projects` and of the session that does the work; Spotlight finds them | Work is reached through who does it. |
| `Bots` | Active bots on `Home`; definitions in `Library › Bots` | A bot is a session you talk to. |
| `Catalogue`, `Agents`, `Pipelines`, `Changes` | `Library` (`CatalogueTree` roots) | Drift shows where the object lives. |
| `Hosts`, `Runners`, `API keys`, `Spend`, `Humans` | `System` | Runners data also feeds `Live`. |

---|---|---|
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
2. **Journey-driven task briefs:** Parallel `/swarm` workers own disjoint slices anchored to `J1`–`J5` and `OR-1`–`OR-49` (not isolated single-file tickets unaware of adjacent workflows).
3. **One core query, two consumers (`R2`/`R7`):** Every aggregation shown in the UI (such as use-case leaderboards or per-account model compatibility) lives in a single shared Go coordinator operation so agents and people see identical facts.
4. **Live Playwright journey verification:** After merge-queue landing on prod, automated checks walk `J1`–`J5` end-to-end against real data, verifying zero console errors, `OR-45 Diff from default` conciseness, and honest error/cost states.
