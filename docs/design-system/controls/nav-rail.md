# Navigation rail

**Code.** `NavRail` in `packages/ui`. Demo: [book](../book/index.html#nav-rail).

**Purpose.** Moves between the seven sections (OR-46): Home, Live, Conversations, Projects, Lab, Library, System. Each person may pin the ones they follow first ([D9](../decisions.md)).

**Anatomy.** A left column of width `size-rail`, from top to bottom:
0. The head: the Fleet mark and name ([D23](../decisions.md#2026-11-11-d23-name-and-mark)) and the "N need you" chip. The collapse button, the search icon and the focus chip are not here: they sit in the shell header beside the rail (OR-42, OR-43, [app shell](../patterns/app-shell.md)).
1. The pinned group, in the order the person set.
2. The seven sections in their fixed order, with no group headings. Every other screen belongs to one of them and highlights it while open (OR-57): Tasks, Results and Delivery are Projects, Bots and the definitions are Library, Hosts, Runners, keys, Spend and People are System.
3. When the person turns it on, the bots zone (**Bots** below), at the bottom above the user block.
4. At the bottom, the current user block ([board](../board/screens/shell.bl)): the update strip when a newer app is ready, then the avatar and name, then the version (**Version** below). A click (or right-click) on the person opens a short menu with Versions and Logout, the Cloudflare Access logout (OR-42). The avatar is the person's initial on the `selected` surface in `fg`: the same in every state, theme and width, with no dot or tone.

Each item has a registry icon (OR-3), a name, an optional counter (**Counters** below) and a pin button.

**Behaviour.**
* The pin button appears on hover and focus, and is always visible on touch. Pinning adds the item to the pinned group and keeps it in its own group, where its pin stays shown as pressed. Unpin from either place.
* Right-click (or the context-menu key, Shift+F10) on any item opens a short menu: Pin or Unpin, and for a pinned item Move up and Move down.
* Drag a pinned item to reorder the pinned group.
* Pins and their order are a person setting (`pins` in `person_settings_get`/`person_settings_set`) and are restored on every device.
* A section opens with its list as the person left it: sort, grouping, filters and search (`lastViews`, a person setting kept per section as its list path; the open item and checked rows are not kept, so a deleted item never traps the section). It is saved when the person leaves the section or the tab hides. Clicking the current section from an open item returns to that list. Spotlight's section jumps do the same.
* The current section has the `selected` background.
* Collapsed and expanded map 1:1 (OR-42): the same rows with the same icons and heights; only names, pin buttons and the brand name go. Every icon (the mark, section icons, the avatar) keeps its exact position: the paddings do not change and the collapsed rail is just its icon column wide. The user block keeps its avatar. The toggle shows `panel-left-close` expanded and `panel-left-open` collapsed.
* The rail is the left [side panel](side-panel.md): its toggle, divider and stored state are that control's. The divider on the rail's right edge resizes it (OR-41): drag, or focus it and press Left and Right; a double-click resets the width. Collapsed, the rail has no divider.
* The collapse button shrinks the rail to icons: pins first and counters as badges on the icons. Names appear in tooltips, shown only while names are hidden (OR-20).
* At phone width (760 px and below) the bottom bar of five replaces the rail ([OR-60](../owner-rules.md), [app shell](../patterns/app-shell.md)): Home, Live, Conversations, Projects and More, whose short menu holds the bots zone when it is on, Lab, Library, System and the user block.

**Counters.** A section's counter counts its *counter view*, a person setting (`navCounters` in `person_settings_get`/`person_settings_set`; agents write it too):

| Section | Default | Choice |
|---|---|---|
| Conversations | Running | any system preset or saved view of the list |
| Projects (its Tasks facet) | Running | any system preset or saved view of the Tasks list |
| System (its Hosts list) | Offline (not seen within the Hosts list's stale limit), attention tone | on or off |

`nav_counts` also counts Delivery (In flight) and Results (Last 24 h); they have no place on the rail until their facets get one.

* Other sections have no counter: Home is answered by the "N need you" chip in the head, which is not a counter; Live, Lab and Library have no amount that changes often enough to watch.
* One read gives every counter: `nav_counts` (agents call the same tool). Each section counts with its list's own filters (the view's states, search and the projects it names), so a counter equals the list it stands for, with one exception: ignored projects are never counted, also when a saved view names one, which its list still shows (OR-34). A saved view that names its projects is that list whatever the focus, so it is one number with no split (`split` false in the reply). Removing a saved view removes its counter choice, and a view the list no longer takes (a state since renamed) counts the default: the section's default counts again.
* Without a focus a counter is one number: `12`. With a focus it splits: the count in the focus in the foreground colour, then `·` and the rest outside the focus (not ignored) in muted colour, `12·30`. With nothing in the focus but some outside it shows `·30` on no fill, so a bare number always means "in focus". Zero in and out shows nothing (OR-29). Hosts have no project and stay one number.
* Collapsed (and below 760 px) only the in-focus number stays, as a badge on the icon's top right; `·30` alone shows no badge. The badge is positioned over the icon, so no icon moves (OR-42, OR-23).
* The accessible name says it in words: "12 running", "12 running in focus, 30 outside", "none running in focus, 30 outside".
* The counter view is chosen in the section's saved views (OR-33): the menu of a view chip (right click, Menu key or Shift+F10) offers **Count in navigation**, and on the counted view **Stop counting in navigation**. Choosing the default stores no choice. A view named `-` cannot be counted: `-` is the setting's off value. Results and Hosts have no saved views, so their counters are turned off through `person_settings_set` (agents included).
* Live: the app reads `nav_counts` again after a change of a counted kind (tasks, sessions, executions, results, delivery, hosts) at most once per 5 s, the last change included (15 s when only executions changed: their token and cost totals change all the time), and every 30 s for host liveness, which moves without a change row. A changed focus, ignored project, counter view or counted saved view reads at once, after its save, also when another device changed it.
* Clicking the item opens the section as the person left it, as without a counter; the counter is not a link of its own.

**Bots.** An optional zone of bots, off by default ([decision](../decisions.md#2026-11-25-bots-in-the-navigation), [board](../board/screens/shell.bl), frame "Shell · rail bots zone").
* The switch **Bots in navigation** is in the user block's menu and in the right-click menu of every rail item. The setting is `railBots` (`{on, recent}`) in `person_settings_get`/`person_settings_set`; agents read and set it too.
* The zone sits at the bottom, above the user block, and grows upward into the rail's free space: no section row or icon moves when it turns on, off, or when bots start and stop (OR-23, OR-42). When the rail is too short, the rail scrolls as a whole, its one scroller (OR-25).
* Its first row is the heading: the bot icon, **Bots** and ›. A click opens Library › Bots, the list of all bots (tooltip "All bots"). Right-click on it opens All bots, **More recent bots (n)**, **Fewer recent bots** (down to 0) and **Hide bots in navigation**, applied at once.
* Then the rows: the bots pinned in Library (`rowPins.library`, ids `bot:<id>`) in their pin order; then the bots that are not pinned and are running or waiting now; then up to `recent` other bots, most recently run first. With `recent` 0 (the default) the zone is the pinned bots plus the bots active now. With nothing to show, only the heading stays.
* A row: the bot's [avatar](avatar.md) (`sm`; `md` on phone) with its activity state (`botAvatarState`: asleep dim, working with a glow, waiting and failed with a dot, paused dimmest), its name and on the right the run's age, "asks" while it waits for an answer, or the age of its last run (OR-2). A click opens the bot's Home conversation; right-click (or the context-menu key, Shift+F10) offers Pin or Unpin and Open in Library.
* Collapsed, the zone keeps its rows: the heading is the bot icon, a row is the avatar, the name moves to a tooltip.
* At phone width the zone is the first part of More: the heading row, then one row of avatars with names under them.
* The rows come from `bot_list` and update live with it.

**Version.** The installed Fleet release (the coordinator's version, `1.261115.6`) in mono after the person's name. Its slot is there from the first paint, so nothing moves when the version arrives. A click shows the overview (an informational layer, OR-14): one row per part with its version, state and a short note, and a head that says when the versions were last read ("checked 12 s ago"; after a failed read "could not check · last checked 6 min ago"). Moments in a row (started, checked, last seen, failing since) are compact ages with the exact time on hover (OR-2, `Ago`), never raw timestamps; while the overview is open, the mark, its states and its ages are recounted from one present every 15 seconds. The rows are this tab, the app the site serves (bundled or a separate UI release), the coordinator (since when, and the release it offers hosts when that differs), the gateway when configured, and each host (polls failing with the reason code and status, current, behind, updating, update failed, offline; auto-update off or held). A host row that needs a look, is offline or fails its polls has **Open host**. The overview stays open while its values change in place; rows keep their place (OR-6). The data is `system_versions`, the same read agents use.

The mark is toned (OR-18), with an icon beside the number (not colour alone), only by a confirmed problem that is true now:

| Tone | When |
|---|---|
| danger | A host update failed (for a release the host does not run yet); a host's poll problem is open (`pollProblem`: its polls have failed long enough for the coordinator to open a needs-you problem); the gateway is unreachable (the coordinator's probe and its immediate re-probe both failed) |
| attention | A host is behind the offered release with auto-update off or held |

Nothing else tones it: this tab not yet checked, a gateway probe older than a minute (the coordinator is repeating it: "checking…"), a failed or late `system_versions` read (the rows keep their last confirmed states), a host that fails a poll before its problem opens, a host offline (the System counter counts it), a host updating or behind while auto-update is on. A warning therefore shows the current state and never flashes for a blip that has already resolved. Summary words say what the tone shows ("1 part needs a look").

**Update.** When the site serves a different app than the tab runs, an update strip shows above the person with the version the site serves. The check compares the hashed entry script and stylesheet of the page this tab loaded with a fresh copy of the page: at load, on focus (at most once a minute) and every 5 minutes, so it gives the same answer every time and goes away once the tab runs the site's app. The tab then updates itself when nobody uses it: the strip says "Updating in 24s" and a thin bar along its foot empties with the seconds; at 0 the tab reloads into the new app. Any pointer move, key, wheel, scroll, touch or focus change puts the count back to 30 s, and a newly offered update on a tab in view starts from the full count. A tab the person is away from (hidden, or its window without focus) never reloads: it waits for the person to come back, then updates at once when their last action was 30 s or more ago, and otherwise counts down the rest; coming back is not activity. With the read mirror ([operator app D3](../../designs/fleet-operator-app.md#indexeddb-mirror)) the reloaded page shows its data at once. **Update** reloads at once. Work is never lost: while a field the person typed into holds input they did not send, the strip says "Waits for your draft" (or "Waits for the open dialog" while a modal dialog is open) and holds the count, with an icon button to update anyway; at 0 a request still in flight finishes first ("Waits for a save" after 2 s). Holding was chosen over restoring drafts after the reload: it cannot lose what a page did not save (composers also keep their drafts across reloads). The reload keeps the route and puts the page's scroll areas back where they were. Reload asks a waiting service worker to take over, then reloads; the strip shows "Reloading into …" until the page unloads (it never turns back in between). If the site is unreachable or the worker does not take over, the strip says "Update failed" with the reason and **Retry**, the tab keeps working, and the next check counts down again. A reload that brings back the same old app (a cache in between) does not repeat by itself: the strip says so with Retry. The overview shows the same strip at its top. There is no separate update banner. Implementation: `app/src/version.ts` (one timestamp written by passive listeners at most every 250 ms, one 1 s timer only while an update is ready).

Collapsed, the strip keeps its row with only its button over the avatar column (the seconds while it counts, pause while held, the spinner while reloading, Retry after a failure; its tooltip says the full state), and the version gives way to a toned icon button that floats above the block when the mark would be toned; it opens the overview. No row or icon moves (OR-42). At phone width the user block, with its strip, is in the More menu.

**Keyboard.** `g` then a letter goes to a section ([principles §5](../principles.md#5-keyboard)). Up and down arrows move through items, `p` pins or unpins the focused item, and Alt+Up and Alt+Down move a focused pinned item.

**Accessibility.** `nav` named "Sections". Each pinned and group list is named. Pin buttons are named "Pin Tasks" or "Unpin Tasks" and expose `aria-pressed`. Counters have accessible names such as "12 running in focus, 30 outside" (**Counters**).

**Do.** Keep the order the person chose. **Don't.** Reorder sections automatically by frequency (specq "stable adaptation"). Don't add a separate Favourites page (OR-12).

## Board

The user block, the update strip, the version overview and their states (expanded, collapsed, phone): [screens/shell.bl](../board/screens/shell.bl). Masters on [blocks.bl](../board/blocks.bl): `Nav rail` (with the "N need you" chip in its head) (the seven sections of [OR-46](../owner-rules.md), built from `Nav item`, whose `current` variant marks the open section), and the phone `Bottom bar` of five `Tab item`s ([OR-60](../owner-rules.md)). A screen sets its section by switching the nested item to `current`.
