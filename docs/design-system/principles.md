# Principles

Usability rules that every Fleet screen follows. Controls and pages link here instead of restating a rule. A page that breaks a rule is a defect; a rule that does not fit a real need is changed here first, with a `decisions.md` entry.

The main user is an operator who handles dozens of tasks a day, and an architect who compares runs and keeps hosts healthy. Both come back to the same screens many times a day. The screens must be dense, calm and predictable.

## 1. Density and calm

* **Dense by default.** A list row is one line, about 32 px tall on desktop. A desktop screen shows at least 20 task rows without scrolling. No decorative cards, hero areas or large empty margins.
* **Calm by default.** Colour carries meaning only: state, attention, measurement basis. Everything else is neutral. At most one accent colour on a screen, used for the primary action and the focus ring.
* **One attention level per signal.** Problems and waiting decisions use the attention colour; normal running work does not. A screen where everything is highlighted is a defect.
* **Numbers are tabular.** Numbers use tabular figures and right alignment in columns, so they line up and do not shift while they update.
* **Plain words.** Labels are short nouns and verbs from the inventory (task, execution, store, accept, retry). No internal identifiers as the main label; IDs appear secondary and copyable.

## 2. Measurement basis

Every number shows its basis (inventory, "Measurement basis"):

* **Measured**: shown as is.
* **Estimated**: shown with a `≈` prefix and a lighter, dotted-underline style. The tooltip and accessible name say "estimated" and the source of the estimate.
* **Unavailable**: shown as `—` with the word "unavailable" on hover and in the accessible name, plus the reason when known. Never shown as `0`.
* **Partial totals** say so: "≥ 12.4k tokens, 2 of 3 executions measured".

The three must differ in more than colour (prefix, style, text), so they survive greyscale and colour-blind viewing.

## 3. Lists and scrolling

Lists of tasks, executions, events, transcript lines and results grow without limit.

* **Virtualised.** Long lists render only visible rows plus a buffer. Row heights are fixed per list density, so the scroll bar is honest and scrolling stays at 60 frames per second. Variable-height content (transcripts) measures rows once and caches the height.
* **No jumps.** New or changed rows never move what the person is looking at:
  * When rows are inserted above the viewport, the list keeps the first visible row at the same screen position (anchor keeping). It never relies only on browser scroll anchoring.
  * When the person is at the top of a newest-first list, or at the bottom of a live transcript ("following"), new rows appear in place and the view follows.
  * When the person has scrolled away, the view stays still and a pill shows "12 new ↑" (or ↓). Activating the pill jumps to the newest rows and resumes following.
* **Updates in place.** A row whose state changes updates its cells without re-sorting under the pointer. If the sort order would move the row, the move waits until the pointer leaves the list or the person scrolls, and a short highlight marks the changed row.
* **Stable selection.** The selected row stays selected across updates, filters that still include it, and reloads.
* **Position is kept.** Returning to a list (back button or tab switch) restores its scroll position, selection and filters.
* **Load more silently.** Older rows load as the person approaches the end; a thin loading row appears only if loading takes longer than 300 ms.

## 4. Filters and search

* **Fast.** Filtering and search react within 100 ms for loaded data. Server queries are debounced at 150 ms and show the old results, dimmed, until new ones arrive; the list never empties while loading.
* **Visible.** Active filters show as removable chips in one filter bar above the list. A count shows "84 of 1,240".
* **Remembered.** The last filter set per list is remembered per person. Saved filters have names and appear as one-click choices in the filter bar.
* **Shareable.** The full filter and sort state lives in the URL. Copying the URL reproduces the view.
* **Search** matches titles, IDs and key text, highlights matches, and scopes to the current list unless the person picks "everywhere".
* **Clear is one action.** "Clear filters" is always present when any filter is active.

## 5. Keyboard

The operator can work without the mouse.

* `/` focuses search; `Esc` clears it, then leaves it.
* `j` / `k` or arrow keys move the row selection; `Enter` opens; `Esc` closes detail and returns focus to the row.
* `g` then a letter goes to a workspace (`g o` Overview, `g t` Tasks, `g r` Results, `g h` Hosts, `g l` Laboratory).
* Decision shortcuts work only while the decision block has focus (for example `a` accept, `r` request rework), and are shown in the button.
* `?` shows all shortcuts. Shortcuts never fire while typing in a field.
* Focus is always visible (2 px ring in the accent colour, 2 px offset) and never trapped, except in a modal dialog.

## 6. Decisions inline

* A question, acceptance or approval appears in the work it belongs to and in the Overview list of everything waiting. Both are views of one record; answering in one place updates the other.
* The decision block shows who it is addressed to, why the work waits, and how long it has waited.
* Answers can be picked from the offered options or typed. Picking an option takes one click or one key.
* After answering, the block shows "Answered by …, delivered" or "delivery pending". A displayed action is not proof that it completed.
* Destructive or irreversible actions (cancel, revoke, delete retention) ask for confirmation in place, naming what will happen. Ordinary actions do not ask.

## 7. States

* Every state badge shows the state word, not only a colour, and its reason on the same row or on hover. A wait shows its release condition.
* Observation state is separate from execution state: a lost host shows "running · unconfirmed, last seen 4 min ago", never "failed".
* Settings show "applied 10:42" or "apply error: …" next to the value. A changed but not yet applied value shows "pending".

## 8. Empty, loading and error

* **Loading:** keep the layout; show skeleton rows in the real row height for the first load only. Later loads keep old data visible.
* **Empty:** say why it is empty and what to do: "No tasks match these filters. Clear filters" or "No results yet. Results appear when an execution stores one."
* **Error:** say what failed, keep any data already shown, and offer retry. Partial failure marks only the affected area ("Host list: last update failed 2 min ago. Retry").
* **Stale data** shows its age once it is older than the expected refresh interval.

## 9. Motion

* Motion only explains change: a row highlight on update (fade over 1.2 s), a panel opening (150 ms), the new-rows pill.
* No motion on scroll, no bouncing, no animated counters.
* `prefers-reduced-motion` removes all transitions except opacity fades under 150 ms.

## 10. Accessibility

* WCAG 2.1 AA: text contrast at least 4.5:1, large text and UI components at least 3:1, in both themes.
* Every control is reachable and usable by keyboard and has an accessible name.
* Lists use proper table or grid semantics; virtualised rows keep `aria-rowcount` and `aria-rowindex`.
* Live updates are announced politely and in batches ("3 tasks changed"), never row by row.
* Hit targets are at least 24 × 24 px on desktop and 44 × 44 px on phone.
* Text zoom to 200 % keeps all content and functions.

## 11. Themes and devices

* Light and dark are equal citizens. The default follows the system setting; the person can override it, and the choice is remembered.
* Desktop first: layouts are designed for 1280 to 1920 px wide, with list and detail side by side from 1100 px.
* Phone usable: at 390 px, list and detail become stacked screens, tables collapse into two-line rows, and every action stays reachable. No horizontal page scroll; wide tables scroll inside their own frame.
