# Work allocation and balancing

Balancing is the one place that decides where new work runs and on what: which host, which subscription (account) and which harness and model. Automatic placement and operator steering use the same operations from interfaces, agent sessions and HAL0, the owner's PM bot (fleet.md R2, R7). The owner chose the shape on 2026-11-25 ([decision](design-system/decisions.md#2026-11-25-balancing-lives-on-live-as-items)).

## Requirements

What people must be able to do, in the owner's words where possible. Each line names what already exists.

| # | Requirement | Today |
|---|---|---|
| B1 | **Take a resource out.** "Nobody touches the Mac now": a host or a subscription takes no new work while the others keep working. Running work finishes where it is. It comes back on command or at a time given with the command. | Host and subscription `intake: paused` (`hosts_update`, `account_update`); host `intake: spare` takes only work no other host can. Not built: a return time, a reason. |
| B2 | **Pin a project.** "This project runs only here, only on this account": a project lists the hosts and subscriptions it may use; nothing else runs it. | Not built. The project's `subscriptions[]` only orders candidates; `environments[]` names checkouts, not a restriction. |
| B3 | **Personal subscriptions stay personal.** A person who connects their own subscription uses only it, and nobody else's work runs on it unless that person lists the project in its take rule. | Built: the holder rule and `take.projects` ([people and access](designs/fleet-people-access.md#6-dispatch)). Not built: showing it as one line. |
| B4 | **See quota that will go unused.** A subscription with quota left that resets before current use would spend it is visible, with how much and when, together with the work it could take. | Readings, burn, pace and projection exist (`subscription_limits`). Not built: the expected-unused figure and its place on Live. |
| B5 | **Prefer or avoid an agent kind.** "Antigravity wherever possible", "keep Claude for what needs it": a preference for a harness, a model or a subscription reorders the candidates; it never breaks a requirement. | Not built. A profile names one harness and one model. `order: soonest_reset` per director exists. |
| B6 | **A profile names a kind of model, not one model.** A profile says what kind of model its work needs ("a model that can code"); the candidates for that kind are listed once and shared. | Not built. |
| B7 | **Limit how much runs on a host.** At most N at once on a host. | Built: `maxConcurrent` (`hosts_update`). A pipeline host job counts once it runs; a script step handed to its host and queued in the executor does not, so a host with queued steps still takes agent runs and shows its true running count. |
| B8 | **See the balance one item at a time.** Each host, subscription, model class and pinned project shows its effective state and why, on Live. | Not built. Hosts show on Live without balance state. |
| B9 | **Change it by voice.** HAL0 applies every change the screen can make, says what it moved, and can undo it. | Not built. Agents can call `hosts_update` and `account_update`. |
| B10 | **Explain.** Every queued run shows why it waits; every started run shows why it went where it went. | Built: `route_decision`, `waitReason` and `waitDetail`. |
| B11 | **Accounts are understandable.** "Only 2 real Codex accounts, yet Fleet shows several": one screen says which real accounts exist, on which hosts each is logged in and whether that login works, the rules in plain words for which account a task takes, and the picks as they happen, with controls. | Not built. Each `managed_account` record has one host binding and no field naming the real provider identity: prod has five codex records for two real accounts, so picking a subscription picks a host, readings split per record, and the "two personal subscriptions of one holder" warning fires on every decision. |
| B12 | **A conversation stays where it lives.** A conversation (bot or session) continues on the host and login that hold its native session. It moves only when forced: the host is gone or updating past the hold, the login is not ready, or the account is out or exhausted. Every move is visible with its reason. | Not built. `rankCandidates` loops subscriptions first and hosts second, so a momentary skip of the first subscription moves the turn to another host: HAL0 conversation C-FLT-592 moved host 45 times in 14 hours on 2026-11-25, each move a new native thread plus a growing handoff. |

Constraints that hold for all of them:

* **Changes govern new work.** A change never moves, stops or switches a running process. Moving running work is an explicit interruption with continuation or handoff ([execution continuity](execution-continuity.md#continuation-and-handoff)).
* **A preference never relaxes a requirement.** A profile, task or project that requires a harness, host or subscription keeps it; if that resource is out, the work waits with the reason visible instead of silently moving.
* **No invented limits.** Fleet adds no caps, thresholds or quotas of its own. Every number in balancing is one a person set or one Fleet measured.
* **Fleet suggests, a person decides.** Fleet never switches subscriptions by itself to use up quota (owner, 2026-11-25). It shows quota that will go unused, and HAL0 offers the matching preference; a person confirms.
* **No rule engine.** Balance state is fields on the items it concerns, not a separate list of rules ([people and access](designs/fleet-people-access.md) keeps the same line). The words *out*, *only*, *prefer*, *avoid* and *cap* name those fields.

## Registered execution resources

Register a host's identity and connection, available environments, OS, software, harnesses and configured account references. Observe availability, last contact, active work, CPU, memory, disk and other relevant capacity. Separate configured capabilities, observed readiness and unknown state.

A host may initially expose one default environment. Additional environments on the same host share its physical capacity; registering them does not create additional resources. Account quotas may likewise be shared across several hosts.

## Balance state

Five kinds of field, each on the item it concerns. Each carries `until` (a time, or none), the person who set it, the time and a short reason; the audit journal keeps the previous value (`setting-change`), so undo restores it.

| Field | On | Values | Effect on new work |
|---|---|---|---|
| **intake** (B1) | host, account | `in`, `out`; a host also `spare` | `out`: no new start on it. `spare`: only work no other host can take now. Existing column `intake` (`all` reads as `in`, `paused` as `out`). |
| **only** (B2) | project settings `placement` | `{hosts?: [host], subscriptions?: [subscription]}` | The project's work starts only on listed hosts and subscriptions. An empty or absent list leaves that side open. |
| **preference** (B5) | subscription; harness and model in the director's person settings | `prefer`, `default`, `avoid` | Reorders candidates that pass every requirement (see below). `avoid` is used only when no `prefer` or `default` candidate can start now. |
| **cap** (B7) | host | positive integer or none | Existing `maxConcurrent`. |
| **model class** (B6) | agent profile | class id | The profile's harness and model come from the class's candidates. |

Who may set them follows existing authority: a host's or subscription's holder (or the owner) sets its intake, cap and preference; a project operator sets the project's `only`; each director sets the harness and model preference for the work they direct (the owner's covers the owner's work, which includes HAL0 and agents acting for the owner); architects edit model classes.

`until` is either a time or "until reset", which resolves to the subscription's next `resetsAt` when set. When it passes, a background pass writes the previous value back with its own audit row.

### Accounts and logins

Two things that today share one record (B11):

* **Account:** one real provider identity (a ChatGPT, Claude or Google login), with its holder, harness, intake, preference, take rule, `switchPct` and readings. Provider limits belong to the account, so readings are kept per account, not per host.
* **Login:** that account authorized on one host, with a state: `ready`, `not logged in` or `expired` (from the existing binding readiness checks).

Placement picks an account and one of its ready logins. The existing `managed_account` records of one real identity become one account with several logins; the provider's own account id or email, read at login, identifies the real identity (the record carries no such field today). The holder rule and take rules apply to the account, so "two personal subscriptions of one holder" stops firing for one person's one account. The "Account" in this document is the "subscription" of [people and access](designs/fleet-people-access.md).

### Model classes

A model class is a named, ordered list of candidates, each `{harness, model, effort?}`: for example `coding`: Antigravity `gemini-3.8-flash-low`, Codex `gpt-6.2-sol`, Claude `opus`. It is a definition kind next to agent profiles, stored and edited the same way ([definition editors](designs/fleet-definition-editors.md)), file `config/model-classes/<id>.json`.

* A profile names `modelClass` instead of `harness` and `model`. A profile that still names one harness and model is a class of one; nothing changes for it.
* A profile may still require a harness (`harness` with `required: true`); then only that harness's candidates count.
* The order in the class is the architects' default. The director's harness and model preference reorders it per run; the class file does not change.
* A candidate must meet the profile's requirements on the chosen host (tools, Code Mode, skills, sandbox). The existing capability filters in dispatch apply unchanged.
* The chosen harness and model are fixed on the session at its first start. A continuation keeps them, as it keeps its native session; another harness is a handoff.

### Expected unused quota

For each subscription period with a reading, `expectedUnused = left − projected use until resetsAt`, from the readings and burn rate `subscription_limits` already derives. It is a measurement, never a threshold: no reading means unknown, never zero. A subscription with expected unused quota and queued work it could run is a *suggestion*: Live marks it and HAL0 offers "prefer `<subscription>` until reset". Nothing changes until a person confirms.

## Placement and selection

1. Resolve task requirements, explicit choices, project rules, access and balance state.
   **A continuation goes home first (B12).** For a run that continues a conversation, the candidate (the session's harness and model, its last login, that login's host) is tried before any other. It loses only when forced: the host is removed, offline past the hold or updating past `continuationHostWait`, the login is not ready, or the account is out, rejected or over `switchPct`. A momentary skip such as a full or overloaded host makes the run wait for its home within the same hold, not move. Preferences, `spare` and ordering apply to new conversations and to forced moves only. A move records `route_decision` with the forcing reason and shows in the conversation as one line ("moved from owner-mac to pds18: owner-mac updating").
2. Find eligible candidates `(harness, model, subscription, host)`. Requirements filter: trust and grants, the project's `only` lists, intake `out`, host harness `allowed`, readiness, capabilities, `switchPct`, online, overload, cap and budgets (the existing order in `rankCandidates`, `go/coordinator/dispatch.go`). An alternate harness must come from the profile's model class and meet its requirements, not merely be installed.
3. Order the eligible candidates:
   1. Harness and model: `prefer` first, then `default`, then `avoid`; ties keep the model class order.
   2. Subscription: `prefer` first, then `default`, then `avoid`; ties keep today's order (the director's own, take-rule ones, the project's `subscriptions[]`, `soonest_reset` when the director set it).
   3. Host: today's order (explicit, the session's last host, the coordinator, creation order).
4. Start the first candidate that can start now. Reevaluate queued work when availability, load or balance state changes. Record the candidates, skips and winner in `route_decision`; with no candidate, the execution waits with the first skip as its reason.

A task may permit several harnesses or require a specific one. Changing a preference does not relax a requirement. If the only required resource is unavailable or out, the work waits with the reason visible rather than moving.

## Operations

Agents get every capability first (fleet.md R7); the web screen uses the same operations.

| Operation | Does | Authority |
|---|---|---|
| `balance_view {project?}` | Every item with its effective state: hosts (intake, cap, running of slots), accounts (intake, preference, reading, `expectedUnused`, who may use it, and each login with its host and state), model classes (effective candidate order for the caller), projects with `only` lists; each with `until`, set by, reason and `queuedAffected`. Plus the open suggestions, the placement order in plain words ("New coding work: Antigravity first, then Codex; accounts: fleet-primary, then fleet-secondary; a conversation stays on its host and login unless forced"), and the latest picks and moves with their reasons. | Person; rows filtered by existing visibility |
| `balance_set {item, field, value, until?, reason?, dryRun?}` | Writes one field above, with its audit row; wakes dispatch. With `dryRun`, writes nothing and returns what would change: queued runs that would move or wait, running work left as is. Without it, returns the same report and the change id. | As in [Balance state](#balance-state) |
| `balance_undo {change}` | Writes back the value the change replaced. | Same as the change |
| `route_decisions {execution}` | Why a run waits or went where it went. | Existing |
| `definition_*` with kind `model-class` | Read and edit model classes. | Existing definition authority |

`hosts_update` and `account_update` keep their other fields; their `intake`, `maxConcurrent` and preference fields write through the same code as `balance_set`, so there is one write path per field.

**HAL0.** HAL0 maps a request to `balance_set`, runs it with `dryRun` first, then applies it and answers in one line what moved: "Mac out until 09:00: 5 queued runs move to pds, 2 running finish on the Mac." "Antigravity wherever possible" is `balance_set {item: harness antigravity, field: preference, value: prefer, until: reset}`. "Put it back" is `balance_undo` of its last change. These are ordinary settings, not radical actions, so no confirmation card is needed (OR-61). HAL0's daily brief names open suggestions.

## Interface

Balancing lives on [Live](design-system/pages/live.md) as a **Balance** block: one line per item, each with its state word and why ("owner-mac · Out until 09:00 · HAL0, 14:02 · 2 running finish, 5 queued moved"; "antigravity-owner · Preferred · 82% left, resets 2d"; "alina-claude · Only Alina"; "Coding · Antigravity first, then Codex, then Claude"; "desiign · owner-mac, claude-owner"). Each account line lists its logins as host and state ("owner-mac ready · pds18 ready · windows expired"). Under the lines, the placement order in plain words and the latest picks: which conversation or task took which account and host, and every move with its reason. There is no separate list of rules. A line's row menu (OR-37) offers In or Out, Prefer, Default or Avoid, Until and Undo; the same changes come by voice through HAL0. Host bands show an out host dimmed with its state word. Wall mode shows the block read-only. System keeps host and subscription configuration (registration, logins, `switchPct`, take rules) and links to Live for balance. The options drawn and the owner's pick are in the [decision](design-system/decisions.md#2026-11-25-balancing-lives-on-live-as-items).

Not built: all of [Operations](#operations), the Balance block, `until`, `only`, preferences, model classes, accounts with logins and continuation-first placement.

## Running work and disconnection

Routing changes govern new assignments, including new steps and trials. They do not imply that active processes moved, stopped or changed harness. Moving active work requires an explicit supported interruption and continuation or handoff; keep the outcome visible.

Use connected eligible resources for new work. An offline host's current execution remains unconfirmed until reconciled, not automatically eligible for duplicate execution elsewhere. Follow [disconnection rules](deployment.md#disconnection-behavior).

Reserve and account for work already admitted across shared host capacity and account quota pools, including laboratory children. Apply configured quota and capacity policies to automatic balancing; estimated or missing measurements stay explicit. Newly available capacity does not authorize unlimited work.

## Explainability

Show eligible resources, active balance state, load, quota information and placement reasons. Human and agent queries expose the same state. Balancing uses understandable ordering rules; a learning optimizer is not required. Ordering can be tuned later from retained work evidence and Lab experiments.

## Open

* **Subscription cap.** A per-subscription "at most N at once" was not requested; add it as a `cap` on subscriptions when someone asks.
* **Out with exceptions.** "Keep general work off the Mac but allow work that names it": today `out` stops every new start, and an explicit host choice waits. Add an exception list only if this is asked for again.
