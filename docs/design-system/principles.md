# Principles

Usability rules that every Fleet screen follows. Controls and pages link here instead of restating a rule. A page that breaks a rule is a defect. A rule that does not fit a real need is changed here first, with a [decisions](decisions.md) entry.

The [owner rules](owner-rules.md) come first and win on conflict. This file does not restate them: it says how a screen meets them and how a reviewer or a browser check proves it. The three owner principles set the order of work: glance, work through conversation, details on demand ([owner principles](owner-rules.md#principles-card)). Sections 12 to 14 carry them; sections 1 to 11 and 15 to 19 apply to every screen.

People work by talking to sessions: bots and project PMs. They come back many times a day, often from a phone, to see what happens and to answer what waits for them. Architects also compare Lab runs and keep hosts healthy. Screens must be quick to read, calm and predictable.

## How to read a rule

* Each rule has a stable id, written `**UR-<n>.**` as its bold lead, one sentence that can be tested, and its check. Ids never change or get reused; page specs and agent context cite them.
* Each rule has one home. The rules here need judgment. A rule that a control or pattern spec carries lives in that spec's "Rules kept here", a rule a script enforces is named with its check, and a rule that repeats an owner rule is merged into it; [rules kept elsewhere](#rules-kept-elsewhere) lists each by id.
* Checks help and show; they never block a merge. A browser check reports where a screen breaks a rule.
* **Browser:** a Playwright step on the built app, at 1440 × 900 unless the check names another size. It can live in `tests/ui/` once the rule gets an automated check.
* **Review:** a reviewer checks it by reading the screen or the page spec.
* A rule that depends on a *Target* owner rule applies once that screen is built. Until then the current screen keeps its current behaviour (owner rules, *Target*).
* A rule never weakens the two fixed exceptions: touch targets of at least 44 px, and WCAG contrast ([owner rules](owner-rules.md)).

## 1. Density and calm

| Id | Rule | Check |
|---|---|---|
| **UR-2.** | Cards are used only where a page spec names them (Home session cards, Live tiles); lists, catalogues and detail pages have no decorative cards, hero areas or large empty margins. | Review: compare the screen with its page spec. |
| **UR-3.** | Colour carries meaning only (state, attention, trouble), and at most one accent colour appears per screen, for the primary action and the focus ring. | Review on a screenshot in both themes; red follows [OR-56](owner-rules.md). |
| **UR-4.** | Labels use words from the [inventory](inventory.md) and the state vocabulary ([OR-55](controls/state-badge.md#rules-kept-here)); an internal id is never the main label ([OR-38](owner-rules.md)). | Review; Browser: no visible text matches a UUID pattern. |

Tooltips follow [OR-20](owner-rules.md).

## 2. Measurement basis

Kept elsewhere: UR-6, UR-7, UR-8, UR-9 in [number](controls/number.md#rules-kept-here).

## 3. Lists and scrolling

| Id | Rule | Check |
|---|---|---|
| **UR-14.** | Selection, scroll position and filters survive updates, Back and a reload. | Browser: select a row, scroll, open an item, go Back, compare; reload, compare. |

## 4. Filters and search

Kept elsewhere: UR-16, UR-19 in [search](controls/search.md#rules-kept-here); UR-17 in [filter bar](controls/filter-bar.md#rules-kept-here); UR-18 in [list view](patterns/list-view.md#rules-kept-here); UR-93 in [nav rail](controls/nav-rail.md#rules-kept-here).

Grouping and sort follow [OR-35](patterns/list-view.md#rules-kept-here).

## 5. Keyboard

| Id | Rule | Check |
|---|---|---|
| **UR-20.** | Every action can be done without a mouse. | Review: walk the journey with the keyboard only. |
| **UR-92.** | No shortcut fires while typing in a field. | Browser: type `j`, `g o` and `/` in the composer, expect only the text. |
| **UR-25.** | Focus is always visible (2 px ring in the accent colour, 2 px offset) and trapped only in a modal dialog. | Browser: tab through the page and check each focused element's outline. |

## 6. Decisions inline

Kept elsewhere: UR-26, UR-27, UR-28, UR-29 in [decision block](controls/decision-block.md#rules-kept-here).

Confirmation of risky actions is [section 17](#17-error-prevention-and-recovery).

## 7. States

| Id | Rule | Check |
|---|---|---|
| **UR-31.** | A wait shows its release condition. | Review. |
| **UR-32.** | Observation state is separate from execution state: a lost host shows "running · unconfirmed, last seen 4m", never "failed". | Review on a host or run whose host stopped reporting. |

## 8. Empty, loading and error

| Id | Rule | Check |
|---|---|---|
| **UR-38.** | A screen loads with no console errors and no failed requests to the coordinator. | Browser: record `console` errors and responses with status 400 or above for each route; expect none. |

## 9. Motion

| Id | Rule | Check |
|---|---|---|
| **UR-39.** | Motion only explains a change: a row or card highlight on update (fade over 1.2 s), a panel opening (150 ms), the new-rows pill. | Review. |
| **UR-40.** | Nothing moves on scroll, bounces or counts up. | Review. |
| **UR-41.** | With `prefers-reduced-motion`, only opacity fades under 150 ms remain. | Browser: emulate reduced motion, check computed transition durations. |

## 10. Accessibility

| Id | Rule | Check |
|---|---|---|
| **UR-43.** | Every control is reachable by keyboard and has an accessible name. | Browser: axe `button-name`, `link-name` and `label` rules. |
| **UR-46.** | Hit targets are at least 24 × 24 px with a fine pointer and 44 × 44 px with a coarse pointer, on any width (D14). | Browser: on touch emulation, measure every interactive element's box. |
| **UR-47.** | Text zoom to 200 % keeps all content and functions. | Browser: set the root font size to 200 %, expect no clipped control and no page scroll sideways. |

## 11. Themes and devices

| Id | Rule | Check |
|---|---|---|
| **UR-48.** | Light and dark are equal; the default follows the system, the person's choice is stored, and a screen is reviewed in dark first. | Browser: emulate each colour scheme and compare screenshots. |

Phone rules are [section 19](#19-phone).

### Narrow work areas

* Fit the available work-area width first. For detail panes up to 500 CSS px, use one 8 px outer gutter per side. Align the heading, transcript and composer to it. Keep panel padding where it separates a border from content.
* Size controls by input mode. Compact pointer controls keep their desktop dimensions when the window narrows. Icon-only controls are square. On touch, keep the full 44 × 44 px target inside its row and give the shell header enough height. Keep the standard icon and text sizes.
* Wrap related controls together when they need room. Keep search-result stepping controls with active search on narrow conversation headers.
* Follow [OR-20](owner-rules.md) for hints. At a viewport edge, move the hint as a unit. Under zoom, size it to the visible viewport, wrap the message at that width and keep its anchor intact. Make essential explanations available through a visible label or a tappable control on touch.

Review at 390 CSS px with a mouse and with touch, at 320 px on touch, and in a narrow detail pane beside a catalogue. Check the heading, buttons, longest practical label and hint, code, scrolling and composer.

## 12. Glance

| Id | Rule | Check |
|---|---|---|
| **UR-51.** | Home and Live answer "what happens now" within 5 s of opening: who works, how much is in flight, what is stuck and what waits for the person, with no click and no scroll at 1440 × 900. | Browser: open the page cold; within 5 s the pulse line (Home) or status bar (Live) and the first group are visible above the fold; Review: a new reader states the answer from one screenshot. |
| **UR-52.** | Every screen leads with the answer to its question ([what each screen answers](formats.md#what-each-screen-answers)); totals and attention come before lists and settings. | Review against the page spec's purpose line. |
| **UR-55.** | A glance figure updates in place within one feed update and never needs a reload. | Browser: change a session's state through the API, expect the count to change without navigation. |

## 13. Conversation as the main mode

| Id | Rule | Check |
|---|---|---|
| **UR-60.** | Starting a conversation with a project PM, a bot or a preset takes at most two actions from Home or Conversations. | Browser: from Home, pick a PM and send; count actions. |
| **UR-61.** | Agent text reads as prose: entity codes become entity cards ([OR-44](controls/entity-card.md#rules-kept-here)), long outputs fold, and no UUID or raw JSON shows unless opened. | Browser: no visible UUID in a transcript; a code opens its card. |

## 14. Details on demand

| Id | Rule | Check |
|---|---|---|
| **UR-63.** | From any problem, total or state, the cause is at most one click away: the failing run, its error and the action to fix it. | Browser: from a red item on Home or Live, one click shows the error text. |
| **UR-64.** | Opened detail keeps the context it came from: the list, card or transcript stays in view or returns on Back. | Browser: open detail, go Back, expect the same scroll position and selection. |

## 15. Wayfinding

| Id | Rule | Check |
|---|---|---|
| **UR-67.** | Browser Back always returns to the previous screen with its state; the app never adds a history entry for a passing redirect or a background update. | Browser: navigate A, B, C; Back twice returns to A with its scroll and selection. |
| **UR-68.** | A deep link restores the full view: the section, the opened item, filters, the tab and the scroll target. | Browser: copy the URL of an opened item with a filter, open it in a fresh context, compare. |
| **UR-69.** | A URL is section, kind and human code, never a UUID ([OR-38](owner-rules.md), [OR-57](owner-rules.md)); old URLs keep working through a redirect. | Browser: every `href` on a screen matches no UUID pattern; old routes in the product map redirect. |
| **UR-70.** | An unknown route says so and offers the way home; it is never a blank page. | Browser: open `/no-such-page`, expect the message and a link to Home. |

## 16. Feedback

| Id | Rule | Check |
|---|---|---|
| **UR-72.** | Every action shows its result in place within one update: the changed state, "queued", or an error with retry. | Browser: after each action, within 3 s, the acted-on element shows a new state or an error. |
| **UR-73.** | An action that takes longer than 300 ms shows progress on the control that started it, and the control cannot be triggered twice. | Browser: with a slowed network, click twice; expect one request. |
| **UR-74.** | An outcome that arrives later (delivery, a run, an install) is shown where the action was started, not only in a toast. | Review. |
| **UR-75.** | Success is quiet: a changed state or a short "Saved" next to the control, no modal and no full-screen message. | Review. |

## 17. Error prevention and recovery

| Id | Rule | Check |
|---|---|---|
| **UR-77.** | Other destructive actions (cancel, stop, revoke, archive) confirm in place and name what will happen; ordinary actions do not ask. | Browser: click Stop on a run, expect an inline confirmation naming the run. |
| **UR-78.** | Where an action can be undone (archive, pin, a setting, a dismissed item), it offers Undo in place for at least 10 s instead of a confirmation. | Browser: archive a conversation, expect Undo, use it, expect the row back. |
| **UR-79.** | Forms keep what was typed after an error and mark the field that failed. | Browser: submit a form that the server rejects, expect the values kept and the field marked. |
| **UR-80.** | Controls offer only valid choices: an action that cannot run now is disabled with its reason, never hidden. | Review; Browser: a disabled control has a reason in its accessible description. |
| **UR-81.** | Leaving with an unsent message or an unsaved draft keeps it, and returning restores it. | Browser: type in the composer, navigate away and back, expect the text. |

## 18. Consistency

| Id | Rule | Check |
|---|---|---|
| **UR-82.** | One concept has one name and one icon on every screen: the registry and the state vocabulary ([OR-3](owner-rules.md), [OR-55](controls/state-badge.md#rules-kept-here)). | Review against `packages/ui/src/registry.ts`; Browser: a state word outside the vocabulary fails. |
| **UR-84.** | One action has the same label, icon and place in every list and detail where it appears (row menu, detail header). | Review across the screens in the product map. |

## 19. Phone

| Id | Rule | Check |
|---|---|---|
| **UR-87.** | Every action available on desktop is reachable on a phone, with 44 px targets. | Browser: run the journey checks at 390 × 844 with touch emulation. |

## Rules kept elsewhere

These rules left the sections above on 2026-11-26 (owner, one pass). Their ids stay. A rule merged into an owner rule says what that rule says; a spec marks a rule that moved with an owner rule as merged with it.

| Rules | Fate | Where it lives now |
|---|---|---|
| UR-1, UR-10, UR-12, UR-13, UR-22, UR-44, UR-45 | component | [controls/data-list.md](controls/data-list.md#rules-kept-here) |
| UR-5, UR-6, UR-7, UR-8, UR-9 | component | [controls/number.md](controls/number.md#rules-kept-here) |
| UR-11 | merged | into [OR-6](owner-rules.md#rules) |
| UR-15 | component | [patterns/infinite-list.md](patterns/infinite-list.md#rules-kept-here) |
| UR-16, UR-19, UR-21 | component | [controls/search.md](controls/search.md#rules-kept-here) |
| UR-17 | component | [controls/filter-bar.md](controls/filter-bar.md#rules-kept-here) |
| UR-18 | component | [patterns/list-view.md](patterns/list-view.md#rules-kept-here) |
| UR-23, UR-93 | component | [controls/nav-rail.md](controls/nav-rail.md#rules-kept-here) |
| UR-24, UR-26, UR-27, UR-28, UR-29, UR-58 | component | [controls/decision-block.md](controls/decision-block.md#rules-kept-here) |
| UR-30 | component | [controls/state-badge.md](controls/state-badge.md#rules-kept-here) |
| UR-33 | component | [controls/setting-field.md](controls/setting-field.md#rules-kept-here) |
| UR-34, UR-36 | component | [controls/status-states.md](controls/status-states.md#rules-kept-here) |
| UR-35, UR-88 | component | [patterns/list-detail.md](patterns/list-detail.md#rules-kept-here) |
| UR-37 | component | [controls/offline-marker.md](controls/offline-marker.md#rules-kept-here) |
| UR-42 | check | `check-design.mjs` contrast: token pairs in `tokens.json` below 4.5:1 |
| UR-49, UR-71, UR-86, UR-89 | component | [patterns/app-shell.md](patterns/app-shell.md#rules-kept-here) |
| UR-50 | component | [patterns/no-horizontal-scroll.md](patterns/no-horizontal-scroll.md#rules-kept-here) |
| UR-53 | merged | into [OR-56](owner-rules.md#rules) |
| UR-54 | merged | into [OR-54](owner-rules.md#rules) |
| UR-56 | merged | into [OR-51](owner-rules.md#rules) |
| UR-57 | component | [controls/tool-line.md](controls/tool-line.md#rules-kept-here) |
| UR-59, UR-90 | component | [controls/composer.md](controls/composer.md#rules-kept-here) |
| UR-62 | merged | into [OR-19](owner-rules.md#rules) |
| UR-65 | component | [patterns/definition-editor.md](patterns/definition-editor.md#rules-kept-here) |
| UR-66 | merged | into [OR-57](owner-rules.md#rules) |
| UR-76 | merged | into [OR-61](owner-rules.md#rules) |
| UR-83 | merged | into [OR-49](owner-rules.md#rules) |
| UR-85 | merged | into [OR-28](owner-rules.md#rules) |
| UR-91 | component | [controls/context-panel.md](controls/context-panel.md#rules-kept-here) |
