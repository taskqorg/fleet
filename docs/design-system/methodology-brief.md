# Fleet spec-driven design method

* **Author:** Alex Kirs
* **Date:** 2026-11-09
* **Status:** brainstorm brief for owner review. Not adopted. Stage 2 of the design work is paused until this brief is approved.

## Overview

Fleet's interface is built by agents in many separate sessions. In earlier rounds a fresh session told "let's do design" ignored earlier decisions, owner preferences and the shared guidance in `specq/design`, even though the instructions said to follow the spec. Work was not grounded in real data, and basics such as targets, spacing and scroll behaviour were decided again each time.

This brief proposes a spec-driven design method for Fleet. Its central idea: **a session must not be able to ignore a prior decision without a check failing.** Documents explain the rules; shared code and tests enforce them. The easiest way to draw any Fleet screen is to import the shared components, and anything else fails `npm test`.

The work runs in three steps, each as a spec-driven loop (update the spec, owner approves, a fresh session implements only from the approved spec):

1. **Foundation specs:** entities, system sections, capabilities and what the interface must show and accept (the existing [inventory](inventory.md), refined).
2. **One design book** for the whole system, built from the owner's named rules and the consistency registry.
3. **Section builds:** orchestrated agents build each section of the app from the book and iterate until the owner approves.

## Analysis

### What a fresh session sees today

* There is no `AGENTS.md` or `CLAUDE.md` in this repository, no CI workflow and no hooks.
* `npm test` runs only `tests/coordinator/*.test.ts`. The Playwright browser tests (`tests/coordinator/browser.ts`, `lab-browser.ts`) never run, because their file names don't match that pattern.
* `book/build.mjs` checks contrast and file size, but nothing runs it.
* The [README](README.md) lists folders that don't exist, leaves out folders that do, and never mentions owner preferences.
* The approved direction's specimens already break owner preferences:
  * P7 (no added direction arrow): the "12 new ↑" pill.
  * P14 (no working popups): the filter popover.
  * The compact-time preference: "4 min ago" instead of "4m".

A new session copies what it finds. Today that means the old prototype strings or the specimens, and their violations come back with them.

### Main objections (ranked)

1. **Nothing is enforced.** Every planned safeguard assumes the agent reads an entry file first, and skipping it is the failure we already saw. Only a failing test reliably stops a session.
2. **Owner preferences are not rules.** P1–P16 sit in `specq/design/owner-preferences.md`, marked "personal, not rules". An agent reads that as optional.
3. **No consistency registry as data.** Icons, symbols, labels, time and number formats and states are prose scattered across control files, so nothing can check them and they already contradict each other.
4. **The book and the app would be two copies.** The book is a standalone HTML file, while the prototype app is server-rendered TypeScript strings. Two copies drift.
5. **No plan and no recorded approval.** "Owner approved" lives in chat, where no check can see it, and nothing lists the work units with their inputs and done conditions.
6. **The front-end stack is undecided,** which blocks shared components.
7. **The owner's top priority has no test.** Smooth scrolling and rows added without jump or flicker are checked nowhere.
8. **Too many document layers.** Each layer is one more file to skip. Patterns are currently mixed into `controls/`, and decisions have no status or stable ids.

### Ideal final result

* Importing `@fleet/ui` components is both the easiest way and the only allowed way to render Fleet UI.
* Labels, icons, formats and colours come only from the registry and the tokens:
  * a component given an unknown id does not compile;
  * code that bypasses the registry or tokens fails lint, or a test named after the owner rule it breaks.
* The book and every specimen are outputs of the same code, so they cannot drift.
* A fresh session needs no memory of earlier sessions: `npm test` fails the first time it ignores a decision, and the error message names the rule and the right component.

### The inventive move

The trade-off people usually accept is "agents are free to write UI, and reviewers catch drift later". Instead, remove the freedom where it is not needed: one shared package owns markup, tokens, registry and formatters. Pages compose those parts and add no styling of their own. Review then looks at composition and data, not at pixels.

## Proposed mechanisms

### Enforcement (cheap and robust; do first)

1. **One gate, `npm run check:design`, inside `npm test`.** It:
   * rebuilds the book and fails if the committed copy differs;
   * runs the lints;
   * runs the owner-rule tests;
   * runs the browser behaviour tests.
2. **A shared UI package (`packages/ui`).** It exports:
   * components;
   * CSS variables generated from `tokens.json`;
   * the registry;
   * one formatter module for time, numbers, sizes and money.

   The book renders by importing these components, and so do app pages. Specimens shown to the owner are bundled from the same code.
3. **`registry.json`, the single catalogue of:**
   * icons and symbols (reusing the TaskQ icon set, per P8);
   * labels;
   * state names with their colour role;
   * compact time and number formats.

   Components take only semantic props, for example `<Age t>` or `<State value="waiting">`. TypeScript literal types make an unknown id a compile error.
4. **Lints on the rendered HTML and the source.** They ban:
   * raw colour, pixel or millisecond values outside tokens;
   * inline SVG outside the icon set;
   * literal UI strings outside the registry;
   * `Date` or `Intl` formatting outside the formatter module;
   * `100vh`.

   Each message names the rule and the replacement, for example "use `registry.labels.age` (OR-2 compact time)".
5. **Owner rules as named, tested rules.** The verbatim quotes stay once in `specq`.
   * Fleet's `owner-rules.md` lists each adopted rule as `OR-n`. Each entry links its `P-n`, its scope and its check type: lint, test or review-only.
   * Each checkable rule has a test named by its id. For example, OR-7 forbids direction arrows next to numbers, and OR-14 forbids interactive controls inside popups.
   * These tests would have failed the current specimens.
6. **Book completeness:** every registry entry and every control appears in the book, and every one is used somewhere. Contrast pairs come from the control specs, not from a hand-kept list.
7. **Accessibility:** axe-core on the book page in headless Chromium.
8. **Approval recorded in the repository:**
   * each spec file carries front matter with `id`, `status: draft | approved` and `approved-at: <commit>`;
   * the check fails when code cites a draft spec;
   * the check also fails when an approved spec changed but the code that implements it did not.

### Medium cost

9. **Behaviour tests in Playwright on fixture data built from real Fleet records:**
   * insert rows above and below the viewport, then assert zero layout shift and an unchanged first visible row;
   * assert no flash of empty content on refresh;
   * assert touch targets and spacing come from tokens.

   These test the owner's top priority directly and give the same result on every run.
10. **Deterministic budgets:** JavaScript and CSS bytes per route, request count, layout shift, and the PWA installability check. No millisecond ceilings in ordinary pull requests; timing is observed, not gated.
11. **`plan.md`:** a table of work units. Each row has an id, inputs (spec ids and versions), outputs, a done command and an approval status. The orchestrator launches only approved rows, and every UI change cites its work-unit id.

### Brittle (defer or limit)

* **Pixel regression:**
  * per component only, on one pinned host, with a tolerance;
  * only after tokens are frozen;
  * changing a baseline needs a decision id.

  DOM and accessibility-tree snapshots come first.
* **`AGENTS.md`:** three lines pointing to `docs/design-system/START.md` and `npm run check:design`. It is a pointer, not the control.

### Process

* **Order of work:**
  1. Test harness and gate.
  2. Foundation specs (inventory).
  3. Owner rules and their tests.
  4. Registry and tokens.
  5. Controls as code, with the book generated from them.
  6. Patterns.
  7. Pages, section by section.
* **Deciding once:**
  * Each open choice goes to the owner as one inline HTML file with up to three rendered options, each with a one-line difference.
  * The answer becomes a decision file `decisions/NNNN-<slug>.md` with a status (open, adopted or superseded) and the ids it affects.
  * Reopening needs a new decision that supersedes the old one, with a reason. Agents never re-ask an adopted question.
* **Change propagation:**
  * A token or registry change regenerates the package and the book, and the same pull request shows every affected screen.
  * Ids are deprecated, not renamed in place.
* **Locked and open layers:**
  * Owner rules, the registry, tokens and controls change only through an approved decision.
  * Only the page being designed is open to the session.
* **Prototype code:** `packages/coordinator/*ui.ts` is evidence only. It gets no new UI, and new PWA routes replace it page by page; the old route remains until the new one reaches parity.
* **Baseline system:** take Apple HIG numbers (targets, spacing, motion, scroll behaviour) as the base layer. Owner rules override it, and every override is listed.

### Proposed layout of `docs/design-system/`

| File | Holds |
|---|---|
| `START.md` | Entry point: what to read, in order, and the one command that checks the work. |
| `owner-rules.md` | OR-n rules linked to specq P-n, with scope and check type. |
| `registry.json` (+ generated `registry.md`) | Icons, symbols, labels, states, formats. |
| `principles.md` | Usability rules with stable PR-n ids; each one testable or marked review-only. |
| `tokens.json` (+ generated `tokens.md`) | Visual values. |
| `controls/`, `patterns/`, `pages/` | Specs with front-matter status. |
| `decisions/` | One file per decision. |
| `plan.md` | Work units and approval status. |
| `book/` | Generated book page. |

The code lives in `packages/ui`, and the checks live in `tests/design/`.

## Goals vs. Non-Goals

* **In-scope goals:**
  * the method;
  * the entry point;
  * owner rules as named, tested rules;
  * the consistency registry;
  * the shared UI package;
  * the design gate and its checks;
  * decision and approval records;
  * the plan format;
  * the order of work;
  * the treatment of the prototype code.
* **Out-of-scope non-goals:**
  * the interface content itself (resumes in stage 2 after approval);
  * making the method shared across products through specq (later);
  * native desktop apps;
  * an LLM reviewer as a blocking gate;
  * millisecond timing gates.

## Alternatives weighed

* **A stronger `AGENTS.md` or a "proof of reading" step.** Rejected: an instruction alone already failed, and a reading proof is easy to fake.
* **An LLM design reviewer as the gate.** Rejected as a gate because its results are not repeatable. Kept as advisory review.
* **Full-page pixel diffs with auto-update.** Rejected: flaky across hosts, and baselines get rubber-stamped.
* **Adopting HIG, Material, Fluent or a component library wholesale.** Rejected: it fights the owner's preferences. HIG numbers are used as the base layer only.
* **Storybook, Figma or a token server.** Rejected: a second source of truth and extra weight at Fleet's scale.
* **Registry as a Markdown table, or one large design document.** Rejected: neither can be imported or checked.
* **Copying P1–P16 into Fleet.** Rejected: two copies drift. Fleet links the specq ids.
* **Hand-copying book markup into pages.** Rejected: this is the drift the method exists to stop.
* **specq's 10-screen review-set cap.** Not adopted, because it is an invented limit. Review sets show one item per open decision.

## Key decisions and staging for next steps

Open decisions for the owner (each up to three options):

1. **Front-end stack: decided ([D4](decisions.md)).** Preact + Signals + Wouter, built with esbuild; Fastify serves JSON and the static bundle. Virtual scroll uses an existing library (candidate TanStack Virtual via `preact/compat`) that must pass the scroll tests.
2. **Status of P1–P16 in Fleet:**
   * (a) all become binding OR rules;
   * (b) only the checkable ones are binding, the rest review-only;
   * (c) decide rule by rule.

   Same question for conflicts with common practice, such as icon plus hover versus a text label.
3. **Where the gate runs:**
   * (a) `npm test` only;
   * (b) GitHub Actions blocking merge;
   * (c) both, with browser tests in a separate job.
4. **Budgets:**
   * (a) behaviour checks only;
   * (b) plus byte budgets per route;
   * (c) plus timing ceilings.
5. **Approval act:**
   * (a) the owner edits the spec status;
   * (b) the owner merges a pull request labelled design-approved;
   * (c) the owner answers in chat and the orchestrator writes the decision file.
6. **Visual regression:**
   * (a) layout and DOM assertions only;
   * (b) per-component screenshots on one pinned host;
   * (c) human-reviewed screenshots that do not block.
7. **The stage-1 specimens:**
   * (a) delete them;
   * (b) archive them as "not a source";
   * (c) rebuild them from components.
8. **Role of the book:**
   * (a) the book's code is the production component source;
   * (b) the book is rendered from app components.

   Both mean one shared package.

Staging after approval:

* Write `START.md`, `owner-rules.md`, `registry.json`, the gate and `plan.md`.
* Restructure stage-2 drafts (`tokens.json`, `controls/*.md`, the book) into the new layout.
* Then resume stage 2 through the plan.
