# Real-Work Use Cases (Hermes History)

Recurring real operational and product engineering task classes observed in Hermes execution history, indexed for [Fleet Lab](../designs/fleet-lab.md) in [`config/lab/use-cases/real-work.json`](../../config/lab/use-cases/real-work.json). Each class carries a stable `<a id="...">` anchor so Lab examples and comparisons stay traceable to real project demand.

<a id="ui-unify-and-mockups"></a>

## UI Unification and Mockups

Source: this section; Lab registry entry `real:ui-unify-and-mockups`.

- **Task shape:** Align disparate views, navigation chrome or component styles with an approved design system, control book or HTML mockup specification.
- **Expected outcome:** Unified layout and component usage using shared UI primitives, preserving existing routes and data bindings without visual drift or redundant controls.
- **Checks:** Design-system checks, build/type checks and DOM/layout assertions.

<a id="feature-with-qa"></a>

## Feature Implementation with QA

Source: this section; Lab registry entry `real:feature-with-qa`.

- **Task shape:** Deliver a scoped user-facing feature (for example role-gated controls, interactive moderation actions or form flows) while keeping non-target views and layouts intact.
- **Expected outcome:** Working feature implementation accompanied by verifiable QA checks covering both privileged and standard user paths (`config/lab/examples/real-ui-hover-tags.json`).
- **Checks:** Deterministic DOM/behavior checks verifying role isolation, interaction prompts and layout preservation.

<a id="prod-alert-triage"></a>

## Production Alert Triage

Source: this section; Lab registry entry `real:prod-alert-triage`.

- **Task shape:** Investigate a live production alert (502 gateway spikes, aggressive scraper/crawler bursts, queue saturation or error surges) from logs, metrics and sitemap/route snapshots.
- **Expected outcome:** A structured triage artifact identifying root cause, affected endpoints, offending clients/patterns, customer impact and concrete mitigation steps (`config/lab/examples/real-alert-triage.json`).
- **Checks:** Deterministic schema and evidence checks verifying accurate classification, attribution and remediation fields.

<a id="live-vs-dev-audit"></a>

## Live vs. Dev Audit

Source: this section; Lab registry entry `real:live-vs-dev-audit`.

- **Task shape:** Compare deployed production behavior, configuration, schema state or rendered UI against local development builds and repository specifications.
- **Expected outcome:** A precise diff of verified discrepancies (missing migrations, stale configs, broken routes or UI regressions) backed by concrete observations without unverified guesses.
- **Checks:** Deterministic comparison and report-structure checks against supplied live and dev snapshots.

<a id="ops-automation-and-pr"></a>

## Operations Automation and Pull Requests

Source: this section; Lab registry entry `real:ops-automation-and-pr`.

- **Task shape:** Automate recurring repository maintenance, CI/release tooling, config updates or scripted migrations and deliver a clean, reviewed pull request.
- **Expected outcome:** Idempotent scripts or configuration updates, passing targeted build/verification checks and a complete PR ready for merge queue.
- **Checks:** Script execution, canonical formatting checks and build/type verification.

<a id="autonomous-error-fixing"></a>

## Autonomous Error Fixing

Source: this section; Lab registry entry `real:autonomous-error-fixing`.

- **Task shape:** Pull recent runtime or CI error reports (Bugsink/Sentry events, failing test traces or host logs), isolate the root cause, implement a minimal fix and verify the guard.
- **Expected outcome:** Confirmed root-cause explanation, minimal patch with zero unrelated churn and a passing verification check confirming the error is resolved.
- **Checks:** Deterministic reproduction test that fails before the fix and passes after it.
