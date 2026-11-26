# App shell

**Code.** `AppShell`.

**Job.** Gives every page the same three columns (OR-24) under one shell header (OR-43), so the person always knows where they are, what they watch and where the attention is.

**Composition, left to right.**
* *Navigation:* the [navigation rail](../controls/nav-rail.md) of eight sections (OR-46). Its head holds the product name and the "N need you" chip, which links to Home. Pinned sections come first; the current user block ends it. At phone width the bottom bar replaces it (below).
* *Shell header* (OR-43): one row over everything right of the navigation, described below.
* *Control column,* `size-ctl` wide: the catalogue of the current section. See [list and detail](list-detail.md).
* *Work area:* the opened item, or the whole page for a section with no catalogue (Home, Live, History).
* *[TooltipLayer](../owner-rules.md):* mounted once.

No bar spans the whole window (OR-24).

**Shell header (OR-43).** One header (`data-ui="shell-header"`) spans the control column and the work area, whatever page is below; the navigation sits beside it, not under it. Left to right:
* the collapse toggle (OR-42), its first item: the navigation's [side-panel](../controls/side-panel.md) toggle;
* the section title (`h1`, the current section's label; `AppShell.title` overrides it): the page's only top heading, so a control column or page has no heading of its own, only its tools (`page-h` without `h1`);
* right after the title, the section's own actions (`ShellActions`), as icon buttons with tooltips (OR-1): Conversations puts New conversation (`plus`) here, so the control column needs no head row for it; the slot is empty and takes no space on other sections;
* after them, the page's status (`ShellStatus`: its offline marker, a data-age chip), so a page needs no head row for it and nothing moves when it shows (OR-23);
* on the right, the [focus chip](../controls/focus-control.md) (OR-34) with its "N need you outside focus" indicator;
* the search icon (`search`, tooltip "Search ⌘K", `aria-keyshortcuts` Meta+K and Control+K), which opens the [Spotlight palette](../controls/spotlight.md) (OR-31, OR-32) and puts the focus in its field;
* last, after search, the toggles of right [side panels](../controls/side-panel.md) (the session's Context). The header keeps that place on every page, so search never moves when a page brings a toggle.

At phone width (760 px and below) the toggle hides, the section actions, the focus chip and the indicator keep their icons (the indicator its count), and the title gives way first: nothing scrolls sideways.

**Wall mode.** `AppShell.wall` (`data-wall`) hides the navigation, the bottom bar and the shell header, so the page fills the screen: [Live](../pages/live.md) on a TV at `/live?wall`. A viewer (Wall viewer in [people access](../../designs/fleet-people-access.md#wall-viewer)) gets a navigation of Live alone, no focus chip and no search.

**Bottom bar (OR-60).** At phone width a bottom bar of five replaces the rail under the work area (`BottomBar`, `data-ui="bottom-bar"`): Home, Live, Conversations and Projects as tabs (icon over name, the in-focus counter as a badge), then More (owner, 2026-11-26: as OR-60 says; the tab ids are `barTabs` in the registry). More opens a short menu above it (OR-14) with the Pinned zone when the person turned it on ([nav rail](../controls/nav-rail.md), **Pinned**), History, Lab, Library and System and, under a line, the user block (Logout, versions). The tab of the current section, or More when the current one is in its menu, is marked. The rail's head is not shown there: the need-you count is Home's.

**Outside focus.** The indicator counts the [Needs you](needs-you.md) items (open questions and Needs-you problems) whose project is outside the focus and not ignored (ignore is full silence); with All projects it shows nothing. A click opens `/home?all`, Home without the focus (ignored projects stay hidden); its head says so, and the indicator hides there. It is never kept as the Overview's last view: the navigation, Back or its head's focus icon return to the focused Overview.

**Dividers (OR-41).** The navigation and the control column each have a drag divider on their right edge (`role=separator`, Left and Right arrows by 16 px, double-click resets). Widths are the `--size-rail` and `--size-ctl` variables on the shell. Limits: navigation 160 to 400 px, control column 280 to 720 px. Until the first settings read answers (at most 1.5 s) the shell is laid out but hidden, so a stored width or a collapsed rail never jumps in (OR-23). The divider's hit zone is 8 px with a pointer, over the rail for the navigation and over the work area's padding for the list (the list ends in buttons). On touch it is 44 px centred on the line, and every control under it (rail items and pins, list rows and their buttons, the collapse toggle) takes the tap first; the line stays 4 px. Widths are person settings (`widths.nav`, `widths.control`; `0` resets), clamped to these limits when read; a drag saves once, at its end, and an arrow key on release; the upper limit also follows the window (a quarter for the navigation, half the split for the list).

**Rules.**
* A "Skip to content" link comes first. The "N need you" chip is hidden on Home, where the needs-you panel is already shown (OR-17, D14).
* The shell fills the viewport and does not scroll. Each column has at most one vertical scroller, never nested (OR-25): the rail, the catalogue's virtual list (a short hand-built catalogue scrolls with its column instead), and the work area (or the transcript inside it).
* The shell is the only rule that uses the viewport height. Nothing else uses `100vh` or `100dvh`.
* The navigation collapses to icons by its button; the choice is the person setting `navCollapsed`, so it follows the person to every device. Below 760 px the bottom bar takes its place. It never disappears.
* A click anywhere on a list row opens it (OR-26). The shell handles this once for every row that has a row link, in virtual lists and in key-value lists.
* Routes are stable URLs (`#/tasks/9989` in mockups, real paths in the app; specq F7): the target routes of the [route consolidation map](../../designs/fleet-ia-and-journeys.md#route-consolidation-map), to which every earlier route redirects. A screen opened from another section highlights the section that owns it (OR-57). An unknown path shows the app's not-found page inside the shell. Back restores the list position and selection.
* Every page renders `AppShell`. The traceability check in `pnpm run check:design` reports any page that does not.

**Private transient state.** Access invalidation removes Spotlight results as well as routed/focus data. `AppShell` keeps its non-private geometry, but closes private search content while validation is pending and keys Spotlight by access generation. The palette's kept query, results, highlighted row and recent items ([Spotlight](../controls/spotlight.md)) belong to one access generation: a new generation starts them empty. The caller also clears its shared search-result cache before identity validation.

## Board

The shell header is the `Shell header` master on [blocks.bl](../board/blocks.bl): collapse toggle, section title, focus chip and search icon, the Home and Live form of [OR-43](#rules-kept-here).

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-24 Three columns.** Catalogue sections (Conversations, Projects, Lab, Library, System) have three columns: navigation (collapses to icons), the control column (the section's catalogue with search, filters and counters) and the work area (the opened item, or the section's page). Home and Live are full-width pages right of the navigation; an open session is the conversation with an optional context panel (OR-51). No bar spans the whole window: the product name and the "need you" count sit in the navigation, and the area right of it has one shell header (OR-43). *Target* for Home and Live; until then the Overview's catalogue is the Needs-you inbox, and Runners and Spend have no control column.
* **OR-25 One scroll per column.** A column has at most one vertical scroller, and it is a plain browser scroller. A scroller never sits inside another one, and the page itself does not scroll. Nothing scrolls sideways (OR-66).
* **OR-26 The whole row opens.** A click anywhere on a list row opens the item. Controls inside the row (pin, checkbox, menu) act on their own. Never make the person aim at the title.
* **OR-43 One shell header.** One header spans the area right of the navigation, above the control column and the work area, whatever page is below. Left to right: the collapse toggle (OR-42) and the section title, the page's only top heading (a column has no heading of its own), then the section's own actions as icons (Conversations: New conversation); on the right the focus chip (OR-34) and the search icon (OR-31). A right-panel toggle goes after search once a right panel exists. At phone width it keeps icons only and never scrolls sideways. Its height contains the full hit targets for the active input mode. On Home and Live the header holds only the section title, the focus chip and the search icon.
* **OR-60 Phone first-class.** At phone width the rail becomes a bottom bar of five: Home, Live, Conversations, Projects and More. *Target:* a row title keeps at least 60% of the row width; service fields wrap below it or fold.
* **UR-49.** (merged with OR-24) Desktop layouts serve 1280 to 1920 px: catalogue sections in three columns ([OR-24](#rules-kept-here)), Home and Live full width right of the navigation. *Check:* Browser: at 1280 and 1920 px, expect the section's columns and no page scroll sideways.
* **UR-71.** (merged with OR-43) The shell header names where the person is with one title ([OR-43](#rules-kept-here)); the title matches the highlighted rail item or names the opened item inside it. *Check:* Browser: compare the `h1` with the rail item that has `aria-current`.
* **UR-86.** (merged with OR-60) At 390 px the rail is a bottom bar of five: Home, Live, Conversations, Projects, More ([OR-60](#rules-kept-here)). *Check:* Browser: at 390 × 844, expect five items in the bottom bar.
* **UR-89.** (merged with OR-60) A row title keeps at least 60 % of the row width; service fields wrap below or fold ([OR-60](#rules-kept-here)). *Check:* Browser: measure the title's share of the row width.
