---
name: design-first-ui
description: Design visible Fleet UI changes in isolated scene previews before production implementation.
---

# Design-first UI

Use this skill for Fleet when the project places the work in the [Design-First workflow](../../../docs/design-system/README.md#design-first). The project contract records the rollout state. Run `node scripts/ui-context.ts <page>` and read the owner principles first.

1. In managed Chromium, walk the live section and navigation neighbours with real data in dark and light at 1440×900 and 390×844. Record deployed version, routes, observed cases, and code, data and missing-field differences. Call out unobserved cells.
2. In task-private source, build one to three meaningfully different options from Fleet's actual UI package. Keep fixture data, shell and review states comparable. A restoration still needs an explicit rendered option and selection. Use a private copy of the pinned project source with canonical scene paths. Keep proposed production behavior outside this stage.
3. Use the [capture command](../../../packages/ui/book/README.md#capture) for each affected state. Open all original PNGs at their actual size. Look at first scan target, reading order, grouping, spacing, alignment, truncation, action and state visibility, focus and touch. Compare against the baseline and neighbours. Fix findings and recapture. Retain a critique for each frame with the rule or trade-off it illustrates.
4. Submit the exact recipe, four stored originals per state, live observations and critique for Fleet to seal. The addressed owner choice selects one option or requests rework. A task acceptance, chat answer or an unsealed artifact is insufficient.
5. After Fleet admits the production stage, transfer the selected scene, implement the approved behavior, and verify the normal app routes and states against the four-frame references. Material visual differences return to design. Check the exact PR and queue candidates and repeat the real-data walk after deployment.
