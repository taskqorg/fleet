# Fleet spec-driven design method

* **Author:** Alex Kirs
* **Date:** 2026-11-09
* **Status:** approved by the owner on 2026-11-09 ([D6](decisions.md)). Brainstorm record: seven review lenses, the owner's answers and decisions D4–D6.

## Overview

Fleet's interface is built by agents across many separate sessions. In earlier rounds a fresh session that was told "let's do design" ignored earlier decisions, the owner's preferences and the shared guidance in `specq/design`, even though its instructions said to follow the spec. The work was also not grounded in real data, and basics such as targets, spacing and scrolling were decided again every time.

**Owner principle:** the method guides agents and never becomes a gate that stops work. It aims for 95% of the result with 20% of the effort. We start simple and learn what works.

The method has three parts:

1. **Owner rules every session reads first.** [owner-rules.md](owner-rules.md) is one page in plain words: OR-1 to OR-19. They are instructions, not gates.
2. **Shared parts that make the right way the easy way.**
   * `packages/ui` holds one time and number formatter, one icon registry (the TaskQ Lucide set), tokens, a ready virtual list, and the controls.
   * Consistency comes from these parts being simpler to use than writing a local variant.
3. **Checks that only warn.** `npm run check:design` prints a report.
   * It is not part of `npm test`, and it never blocks a pull request.
   * Reviewers and the owner read the report. Book screenshots are attached to the pull request.

Work follows one loop at every step: update the spec, the owner says "ok" in chat, the worker records it in `decisions.md`, and a fresh session builds from the spec.

## Order of work

This is guidance, not a gate:

1. Foundation specs: entities, system sections, and what the interface shows and accepts. This is [inventory.md](inventory.md).
2. The whole-system map (OR-10).
3. Owner rules, registry and tokens.
4. Everyday controls in `packages/ui`: lists, tables, filters, forms (OR-4). The book page is their live demo.
5. Patterns.
6. Pages, section by section. Orchestrated agents build them and iterate until the owner says "ok".

## Mechanisms

* **Entry point.** The [README](README.md) starts with owner-rules.md and links the rest in order. An `AGENTS.md` line points design and UI sessions to it.
* **`packages/ui`:**
  * Preact + Signals + Wouter, built with esbuild ([D4](decisions.md)).
  * `tokens.json` produces CSS variables.
  * `registry`: icons, state names, labels.
  * `format`: compact time, numbers, bytes, money with basis.
  * `VirtualList`: built on TanStack Virtual core, with anchor keeping.
  * The controls themselves.
* **Book page.** `book/index.html` is the live demo of `packages/ui`, built from the same code into one self-contained file. There is no separate copy.
* **`check:design` report.** The report warns about:
  * raw colours or pixel values outside tokens;
  * inline SVG outside the registry;
  * date formatting outside `format`;
  * `100vh`;
  * low contrast pairs;
  * arrow glyphs beside numbers (OR-7);
  * interactive elements in floating layers (OR-14);
  * bundle size.

  Each warning names the rule and the shared part to use instead.
* **Performance targets.** These are measured targets, not stops: initial JavaScript ≤ 200 KB, and interaction response ≤ 100 ms.
* **Decisions.** Each decision is recorded once in `decisions.md`. A decided question is not asked again unless the owner reopens it.
* **Prototype code.** `packages/coordinator/*ui.ts` is evidence only. New PWA routes replace it page by page.

## Goals and non-goals

* **Goals:**
  * the owner rules;
  * shared parts that carry consistency;
  * the warning report;
  * the decision log;
  * the order of work.
* **Non-goals:**
  * blocking gates;
  * visual regression tests (for now);
  * timing gates;
  * making the method shared through specq (later);
  * native desktop apps.

## Alternatives weighed

* **Blocking checks in `npm test` or CI (the reviewers' recommendation).** Rejected by the owner: the method must guide, not stop work. The checks still run, as a report.
* **A stronger `AGENTS.md` alone.** Not enough on its own, because it failed before. It is kept as a pointer, and the shared parts do the real work.
* **An LLM reviewer as a gate.** Rejected. Its results are not repeatable.
* **Pixel visual regression.** Not now. Screenshots are attached to pull requests instead.
* **Adopting HIG or a component library wholesale.** Rejected. HIG numbers fill gaps that no owner rule covers (OR-5).
* **Storybook, Figma or a token server.** Rejected. Each would be a second source of truth.
* **Copying P1–P16 verbatim into Fleet.** Rejected. owner-rules.md links the specq source instead.

## Decisions

| Question | Choice |
|---|---|
| Front-end stack | Preact + Signals + Wouter, esbuild; virtual scroll from a library ([D4](decisions.md)) |
| Owner rules | Per-rule answers; instructions, not gates ([D5, D6](decisions.md)) |
| Where checks run | Warning report in the pull request, never blocking |
| Performance | Measured targets: initial JavaScript ≤ 200 KB, interaction ≤ 100 ms |
| Approval | Owner says "ok" in chat; the worker records it in `decisions.md` |
| Visual regression | Not now; book screenshots are attached to the pull request |
| Stage-1 specimens | Archived in `explorations/archive/` |
| Book role | Live demo page of `packages/ui`; no separate copy |
