# Principles

Usability rules that every Fleet screen follows. Controls and pages link here instead of restating a rule. A page that breaks a rule is a defect. A rule that does not fit a real need is changed here first, with a [decisions](decisions.md) entry.

The [owner rules](owner-rules.md) come first and win on conflict. This file does not restate them: it says how a screen meets them and how a reviewer or a browser check proves it. The three owner principles set the order of work: glance, work through conversation, details on demand ([owner principles](owner-rules.md#principles-card)). Sections 12 to 14 carry them; sections 1 to 11 and 15 to 19 apply to every screen.

People work by talking to sessions: bots and project PMs. They come back many times a day, often from a phone, to see what happens and to answer what waits for them. Architects also compare Lab runs and keep hosts healthy. Screens must be quick to read, calm and predictable.

## How to read a rule

* Each rule has a stable id, written `**UR-<n>.**` as its bold lead, one sentence that can be tested, and its check. Ids never change or get reused; page specs and agent context cite them.
* Checks help and show; they never block a merge. A browser check reports where a screen breaks a rule.
* **Browser:** a Playwright step on the built app, at 1440 × 900 unless the check names another size. It can live in `tests/ui/` once the rule gets an automated check.
* **Review:** a reviewer checks it by reading the screen or the page spec.
* A rule that depends on a *Target* owner rule applies once that screen is built. Until then the current screen keeps its current behaviour (owner rules, *Target*).
* A rule never weakens the two fixed exceptions: touch targets of at least 44 px, and WCAG contrast ([owner rules](owner-rules.md)).

## 1. Density and calm

| Id | Rule | Check |
|---|---|---|
| **UR-1.** | A list row is one line, about 32 px tall with a fine pointer and 44 px on touch, and a desktop list shows at least 15 rows without scrolling. | Browser: open a list at 1440 × 900 and count visible `[role=row]` elements; measure one row's height. |
| **UR-2.** | Cards are used only where a page spec names them (Home session cards, Live tiles); lists, catalogues and detail pages have no decorative cards, hero areas or large empty margins. | Review: compare the screen with its page spec. |
| **UR-3.** | Colour carries meaning only (state, attention, trouble), and at most one accent colour appears per screen, for the primary action and the focus ring. | Review on a screenshot in both themes; red follows [OR-56](owner-rules.md). |
| **UR-4.** | Labels use words from the [inventory](inventory.md) and the state vocabulary ([OR-55](owner-rules.md)); an internal id is never the main label ([OR-38](owner-rules.md)). | Review; Browser: no visible text matches a UUID pattern. |
| **UR-5.** | Numbers in columns use tabular figures and right alignment. | Browser: computed `font-variant-numeric` of numeric cells is `tabular-nums`. |

Tooltips follow [OR-20](owner-rules.md).

## 2. Measurement basis

| Id | Rule | Check |
|---|---|---|
| **UR-6.** | Every number keeps its basis: measured, estimated or unavailable; an unknown value is never shown as `0`. | Review the data source of each number; Browser: an unavailable value shows `—` with "unavailable" in its accessible name. |
| **UR-7.** | The main view shows the rounded value, and the basis marks (`≈`, `≥`, "partial", the subscription mark) sit in the tooltip and in Details ([OR-59](owner-rules.md)); until OR-59 is built, an estimate keeps its `≈` prefix and dotted underline. | Review; Browser: the accessible name of an estimated value contains "estimated". |
| **UR-8.** | A partial total says how much is measured ("2 of 3 runs measured") wherever its basis is shown. | Review. |
| **UR-9.** | The three bases differ in more than colour (prefix, style or text). | Review a greyscale screenshot. |

## 3. Lists and scrolling

| Id | Rule | Check |
|---|---|---|
| **UR-10.** | A list that can be longer than a screen renders only visible rows plus a buffer ([OR-9](owner-rules.md)). | Browser: with 500 rows loaded, the DOM holds fewer than 100 row elements. |
| **UR-11.** | Rows inserted above the viewport keep the first visible row at the same screen position ([OR-6](owner-rules.md), [OR-23](owner-rules.md)). | Browser: note the first visible row's `top`, insert rows through the feed, compare. |
| **UR-12.** | At the top of a newest-first list, or at the bottom of a live transcript, new rows appear in place and the view follows; scrolled away, the view stays still and a pill says `12 new`. | Browser: scroll away, wait for a new row, expect the pill; activate it, expect the newest row visible. |
| **UR-13.** | A row whose state changes updates its cells in place; a move caused by the sort waits until the pointer leaves the list or the person scrolls. | Browser: hover a row, change its state, expect the same row under the pointer. |
| **UR-14.** | Selection, scroll position and filters survive updates, Back and a reload. | Browser: select a row, scroll, open an item, go Back, compare; reload, compare. |
| **UR-15.** | Older rows load as the person nears the end, and a loading row shows only after 300 ms. | Browser: scroll to the end with a slowed network. |

## 4. Filters and search

| Id | Rule | Check |
|---|---|---|
| **UR-16.** | Filtering loaded data reacts within 100 ms; a server query keeps the old rows, dimmed, until the new ones arrive, and the list never empties while loading. | Browser: type a query and time the first change; with a slowed network the row count never reaches 0 during the load. |
| **UR-17.** | Active filters show as removable terms with a count ("84 of 1,240") and one clear action ([OR-11](owner-rules.md), [OR-36](owner-rules.md)). | Browser: apply a filter, expect the count and the clear action. |
| **UR-18.** | The full filter, grouping and sort live in the URL, so a copied URL reproduces the view. | Browser: set a filter, open the URL in a new context, compare rows. |
| **UR-19.** | Search matches titles, codes and key text, highlights matches, and stays in the current list unless the person picks "everywhere" (the palette, [OR-32](owner-rules.md)). | Browser: search a known code, expect it first and highlighted. |
| **UR-93.** | The last filter set per list is remembered per person on the server, and saved views are one-click choices ([OR-33](owner-rules.md)). | Browser: set a filter, open another section, come back without the URL query, expect the filter. |

Grouping and sort follow [OR-35](owner-rules.md).

## 5. Keyboard

| Id | Rule | Check |
|---|---|---|
| **UR-20.** | Every action can be done without a mouse. | Review: walk the journey with the keyboard only. |
| **UR-21.** | `/` focuses list search and `Esc` clears it, then leaves it; `⌘K` opens the palette ([OR-32](owner-rules.md)). | Browser: press the keys, check `document.activeElement`. |
| **UR-22.** | `j` / `k` or the arrow keys move the row selection, `Enter` opens, and `Esc` closes the opened item and returns focus to its row. | Browser: open a row with `Enter`, press `Esc`, expect focus on that row. |
| **UR-23.** | `g` and one letter open each rail section, and `?` lists every shortcut; today `g o` Overview, `g t` Tasks, `g r` Results, `g h` Hosts, `g l` Laboratory, and the seven-section letters are set when that rail is built. | Browser: press `?`, expect one `g` shortcut per rail section. |
| **UR-24.** | Decision shortcuts (for example `a` accept, `r` request rework) work only while the decision has focus and show in the button. | Browser: focus a decision, press its letter, expect the action; press it elsewhere, expect none. |
| **UR-92.** | No shortcut fires while typing in a field. | Browser: type `j`, `g o` and `/` in the composer, expect only the text. |
| **UR-25.** | Focus is always visible (2 px ring in the accent colour, 2 px offset) and trapped only in a modal dialog. | Browser: tab through the page and check each focused element's outline. |

## 6. Decisions inline

| Id | Rule | Check |
|---|---|---|
| **UR-26.** | A question, acceptance or approval shows in the conversation that asks it, on that session's Home card and in Telegram, as views of one record ([OR-53](owner-rules.md), [OR-40](owner-rules.md)); answering in one place updates the others within one update. | Browser: answer on Home, open the session, expect the answered card. |
| **UR-27.** | A decision shows who it is addressed to, why the work waits and how long it has waited. | Review. |
| **UR-28.** | Picking an offered answer takes one click or one key; a free-text answer is always offered. | Browser: answer a question with one click. |
| **UR-29.** | After an answer the card says "Answered by …" and whether delivery is done or pending; a shown action is not proof that it completed. | Browser: answer, expect the delivery state on the card. |

Confirmation of risky actions is [section 17](#17-error-prevention-and-recovery).

## 7. States

| Id | Rule | Check |
|---|---|---|
| **UR-30.** | A state shows its word and icon, not only a colour, from the one vocabulary ([OR-55](owner-rules.md)), and its reason on the same row or on hover. | Browser: every state badge has text or an accessible name. |
| **UR-31.** | A wait shows its release condition. | Review. |
| **UR-32.** | Observation state is separate from execution state: a lost host shows "running · unconfirmed, last seen 4m", never "failed". | Review on a host or run whose host stopped reporting. |
| **UR-33.** | A setting shows "applied 10:42", "pending" or "apply error: …" next to its value. | Browser: change a host setting, expect "pending", then "applied". |

## 8. Empty, loading and error

| Id | Rule | Check |
|---|---|---|
| **UR-34.** | The first load keeps the layout and shows skeletons in the real row or card size; later loads keep the old data visible. | Browser: with a slowed network, layout shift on load is below 0.1. |
| **UR-35.** | An empty area says why it is empty and what to do, and an idle work area shows a useful summary, never "Pick a row" ([list-detail](patterns/list-detail.md)). | Browser: open each section with nothing selected, expect no "Pick" or "Select" placeholder text. |
| **UR-36.** | An error says what failed, keeps the data already shown, offers retry, and marks only the affected area. | Browser: block one read with a route rule, expect a retry in that area and the rest intact. |
| **UR-37.** | Data older than its refresh interval shows its age ([offline marker](controls/offline-marker.md)). | Browser: go offline, wait one interval, expect the age. |
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
| **UR-42.** | Text contrast is at least 4.5:1, and large text and UI parts at least 3:1, in both themes (WCAG 2.1 AA). | Browser: axe contrast rules in light and dark. |
| **UR-43.** | Every control is reachable by keyboard and has an accessible name. | Browser: axe `button-name`, `link-name` and `label` rules. |
| **UR-44.** | Lists use table or grid semantics, and virtualised rows keep `aria-rowcount` and `aria-rowindex`. | Browser: read the attributes on a long list. |
| **UR-45.** | Live updates are announced politely and in batches ("3 tasks changed"), never row by row. | Browser: count `aria-live` announcements during a burst of updates. |
| **UR-46.** | Hit targets are at least 24 × 24 px with a fine pointer and 44 × 44 px with a coarse pointer, on any width (D14). | Browser: on touch emulation, measure every interactive element's box. |
| **UR-47.** | Text zoom to 200 % keeps all content and functions. | Browser: set the root font size to 200 %, expect no clipped control and no page scroll sideways. |

## 11. Themes and devices

| Id | Rule | Check |
|---|---|---|
| **UR-48.** | Light and dark are equal; the default follows the system, the person's choice is stored, and a screen is reviewed in dark first. | Browser: emulate each colour scheme and compare screenshots. |
| **UR-49.** | Desktop layouts serve 1280 to 1920 px: catalogue sections in three columns ([OR-24](owner-rules.md)), Home and Live full width right of the navigation. | Browser: at 1280 and 1920 px, expect the section's columns and no page scroll sideways. |
| **UR-50.** | No page scroll in either direction, and nothing scrolls sideways inside it: code and tables wrap or stack ([OR-66](owner-rules.md)). | Browser: `scrollWidth` equals `clientWidth` on the document at 1440 and 390 px. |

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
| **UR-53.** | Trouble is the loudest thing on a screen: red only for failures, dead hosts and blocked work ([OR-56](owner-rules.md)), and repeats collapse to one item with `×N` ([OR-58](owner-rules.md)). | Browser: count danger-tone elements per cause; expect one per cause. |
| **UR-54.** | A glance screen shows no ids, model badges, tokens or cost unless cost is the screen's question ([OR-54](owner-rules.md)). | Browser: on Home, no visible text matches a model name, a token count or a currency amount. |
| **UR-55.** | A glance figure updates in place within one feed update and never needs a reload. | Browser: change a session's state through the API, expect the count to change without navigation. |

## 13. Conversation as the main mode

| Id | Rule | Check |
|---|---|---|
| **UR-56.** | Every bot, PM and task run is reached as a conversation in the one session view, from any section and from a link ([OR-51](owner-rules.md)). | Browser: open the same session from Home, Conversations, a project and a task; expect the same view and URL. |
| **UR-57.** | Consecutive tool calls and system events show as one folded line with what was done and how long it took; heartbeats and repeated attachments never show ([OR-52](owner-rules.md)). | Browser: in a transcript with tool calls, no two adjacent tool rows are expanded by default. |
| **UR-58.** | A question is answered in place on its card; the answer never needs another page ([OR-53](owner-rules.md)). | Browser: answer a question; the URL stays the same. |
| **UR-59.** | The composer is always reachable in an open session and sends with `Enter`; a message to a busy agent is queued or steers it, and says which. | Browser: send while the agent runs, expect "queued" or "sent" on the message. |
| **UR-60.** | Starting a conversation with a project PM, a bot or a preset takes at most two actions from Home or Conversations. | Browser: from Home, pick a PM and send; count actions. |
| **UR-61.** | Agent text reads as prose: entity codes become entity cards ([OR-44](owner-rules.md)), long outputs fold, and no UUID or raw JSON shows unless opened. | Browser: no visible UUID in a transcript; a code opens its card. |

## 14. Details on demand

| Id | Rule | Check |
|---|---|---|
| **UR-62.** | Technical detail (ids, metrics, tool calls, cost, qualifiers) folds by default and opens with one click in place ([OR-19](owner-rules.md)). | Browser: count clicks from the main view to a run's tokens; expect one. |
| **UR-63.** | From any problem, total or state, the cause is at most one click away: the failing run, its error and the action to fix it. | Browser: from a red item on Home or Live, one click shows the error text. |
| **UR-64.** | Opened detail keeps the context it came from: the list, card or transcript stays in view or returns on Back. | Browser: open detail, go Back, expect the same scroll position and selection. |
| **UR-65.** | Configuration and comparison views show only what differs from the default ([OR-45](owner-rules.md)). | Review. |

## 15. Wayfinding

| Id | Rule | Check |
|---|---|---|
| **UR-66.** | Every screen belongs to exactly one rail section, which stays highlighted while the screen is open ([OR-57](owner-rules.md)); the [product map](product-map.md) names it. | Browser: for every route in the product map, exactly one rail item has `aria-current`. |
| **UR-67.** | Browser Back always returns to the previous screen with its state; the app never adds a history entry for a passing redirect or a background update. | Browser: navigate A, B, C; Back twice returns to A with its scroll and selection. |
| **UR-68.** | A deep link restores the full view: the section, the opened item, filters, the tab and the scroll target. | Browser: copy the URL of an opened item with a filter, open it in a fresh context, compare. |
| **UR-69.** | A URL is section, kind and human code, never a UUID ([OR-38](owner-rules.md), [OR-57](owner-rules.md)); old URLs keep working through a redirect. | Browser: every `href` on a screen matches no UUID pattern; old routes in the product map redirect. |
| **UR-70.** | An unknown route says so and offers the way home; it is never a blank page. | Browser: open `/no-such-page`, expect the message and a link to Home. |
| **UR-71.** | The shell header names where the person is with one title ([OR-43](owner-rules.md)); the title matches the highlighted rail item or names the opened item inside it. | Browser: compare the `h1` with the rail item that has `aria-current`. |

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
| **UR-76.** | A radical action starts only from a confirmation card that names its scope and the way back ([OR-61](owner-rules.md)). | Review against the OR-61 list; Browser: the action's first click shows the card, not the result. |
| **UR-77.** | Other destructive actions (cancel, stop, revoke, archive) confirm in place and name what will happen; ordinary actions do not ask. | Browser: click Stop on a run, expect an inline confirmation naming the run. |
| **UR-78.** | Where an action can be undone (archive, pin, a setting, a dismissed item), it offers Undo in place for at least 10 s instead of a confirmation. | Browser: archive a conversation, expect Undo, use it, expect the row back. |
| **UR-79.** | Forms keep what was typed after an error and mark the field that failed. | Browser: submit a form that the server rejects, expect the values kept and the field marked. |
| **UR-80.** | Controls offer only valid choices: an action that cannot run now is disabled with its reason, never hidden. | Review; Browser: a disabled control has a reason in its accessible description. |
| **UR-81.** | Leaving with an unsent message or an unsaved draft keeps it, and returning restores it. | Browser: type in the composer, navigate away and back, expect the text. |

## 18. Consistency

| Id | Rule | Check |
|---|---|---|
| **UR-82.** | One concept has one name and one icon on every screen: the registry and the state vocabulary ([OR-3](owner-rules.md), [OR-55](owner-rules.md)). | Review against `packages/ui/src/registry.ts`; Browser: a state word outside the vocabulary fails. |
| **UR-83.** | One job has one control on every screen ([OR-49](owner-rules.md), [OR-13](owner-rules.md)): a session card is one block, a question one block, an entity reference one `EntityCode`. | `pnpm run check:design`; Review: the screen uses the block named for the job. |
| **UR-84.** | One action has the same label, icon and place in every list and detail where it appears (row menu, detail header). | Review across the screens in the product map. |
| **UR-85.** | One kind of value has one format everywhere ([OR-28](owner-rules.md), [formats](formats.md)). | Review. |

## 19. Phone

| Id | Rule | Check |
|---|---|---|
| **UR-86.** | At 390 px the rail is a bottom bar of five: Home, Live, Conversations, Projects, More ([OR-60](owner-rules.md)). | Browser: at 390 × 844, expect five items in the bottom bar. |
| **UR-87.** | Every action available on desktop is reachable on a phone, with 44 px targets. | Browser: run the journey checks at 390 × 844 with touch emulation. |
| **UR-88.** | A list and its opened item are separate screens, and Back returns to the list at the same place. | Browser: open an item at 390 px, go Back, compare scroll position. |
| **UR-89.** | A row title keeps at least 60 % of the row width; service fields wrap below or fold ([OR-60](owner-rules.md)). | Browser: measure the title's share of the row width. |
| **UR-90.** | The composer stays visible above the on-screen keyboard and a question answers in place. | Browser: focus the composer at 390 px, expect it inside the visual viewport. |
| **UR-91.** | The context panel and long details open as a sheet, never as a floating panel over the conversation ([OR-51](owner-rules.md), [OR-27](owner-rules.md)). | Review. |
