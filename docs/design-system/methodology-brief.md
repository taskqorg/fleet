# Fleet spec-driven design method

* **Author:** Alex Kirs
* **Date:** 2026-11-09, revised 2026-11-10 after the [independent review](reviews/2026-11-10/SUMMARY.md) ([D14](decisions.md#2026-11-10-d14-review-fixes-and-automatic-checks))
* **Status:** approved by the owner ([D6](decisions.md#2026-11-09-d6-method-owner-rules-table-and-decisions-38)); later decisions D7–D14 are in [decisions.md](decisions.md).

**Terms.**
* *Owner:* the person who approves design choices.
* *Worker:* the agent session that does the work.
* *Book:* the live demo page of `packages/ui`.
* *Mockups:* pages built from the book.
* *specq:* the organisation's shared rules repository; its P1–P16 are the owner's verbatim preferences.
* *TaskQ:* an earlier product whose icon set Fleet reuses.

## Problem

Fleet's interface is built by agents in many separate sessions. In earlier rounds, a fresh session ignored earlier decisions and the owner's preferences, even though its instructions said to follow the spec. Basics such as touch targets, spacing and scrolling were decided again each time.

**Owner principle:** the method guides and never becomes a gate that stops work. It aims for most of the result with little effort, starts simple, and learns what works.

## Method

1. **Owner rules first.** Every design or UI session reads [owner-rules.md](owner-rules.md) first. They are instructions, not gates. The file is the only list of the rules, so other documents link to it and do not copy rule numbers or ranges.
2. **Shared parts make the right way the easy way.** `packages/ui` holds the tokens, the icon registry, the formatter, the virtual list, the controls and the patterns. Pages compose them and add no styling of their own.
3. **Checks only warn.** `npm run check:design` prints a report. It never fails the build. Its checks are listed in one place: the `RULES` table and the steps in [`check-design.mjs`](../../packages/ui/scripts/check-design.mjs).

## Work loop

1. Update the spec, meaning the inventory, owner rules, controls, patterns and pages, together with a `decisions.md` entry.
2. Build from the spec in `packages/ui`. Run `npm run book` and `npm run check:design`.
3. Open a pull request.
   * CI ([`ci.yml`](../../.github/workflows/ci.yml)) runs the build, the tests and the design report.
   * CI posts the report summary as a pull-request comment and uploads the report and screenshots.
   * If CI is not available, paste the report summary into the pull request by hand.
4. The owner reviews the self-contained book and mockups and says "ok". The worker records the "ok" in `decisions.md`.

Order of work (guidance, not a gate):
1. Inventory.
2. System map.
3. Owner rules, registry and tokens.
4. Everyday controls.
5. Patterns.
6. Pages.

## Performance targets

These are measured targets, not stops. The report prints each value next to its target.
* **JavaScript size:** the minified size of `packages/ui` and of each built page's inline script, raw and gzip, against 200 KB raw.
* **Interaction latency:** the time from clicking a task row to its detail heading being visible in the mockups, measured in headless Chromium, against 100 ms.

## Risks

* *Warnings get ignored.* Because the report never blocks, nobody may act on it. Mitigation: CI posts its summary on every pull request, and reviewers check that summary first.
* *Heuristic checks make mistakes.* Pattern and DOM checks can raise false alarms or miss real problems. Each check was shown once to catch its defect (D14), and review still looks at the screenshots.
* *A check fails silently.* A check that cannot run reports "not run" with its reason. Read those lines rather than assuming everything passed.

## Alternatives weighed

* **Blocking checks in CI.** Rejected by the owner. Only build and test failures fail the job; design warnings never do.
* **An LLM reviewer as a gate.** Rejected, because its results are not repeatable. Advisory reviews, like the 2026-11-10 one, are welcome.
* **Pixel visual regression.** Not now. Screenshots go into the pull request instead.
* **Adopting a component library or HIG wholesale.** Rejected. HIG numbers fill only the gaps that no owner rule covers.
* **Storybook, Figma or a token server.** Rejected, because each would be a second source of truth.
