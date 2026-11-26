# Focus chip

**Code.** `FocusControl` in `packages/ui`; the app wires it in `app/src/main.tsx` (`Focus`). Demo: [book](../book/index.html#focus-control).

**Purpose.** Shows and sets which projects the person watches (OR-34): the focus, saved presets and the projects they ignore, plus what needs them outside the focus. It sits on the right of the [shell header](../patterns/app-shell.md) (OR-43), before the search icon.

**Anatomy.**
* The chip: the `focus` icon, the focused project names (two, then `+N`) or "All projects", and a chevron. With a focus set it is filled with the accent, so it never reads as All projects.
* Before the chip, when above 0: "N need you outside focus" in attention tone with the `bell` icon.
* The panel, under the chip, right-aligned:
  1. Presets: the system "All projects", the person's saved presets (each with a remove cross), and Save focus preset (icon) while a focus is set.
  2. Find project (a search field) when more than 8 projects are offered.
  3. The projects: a checkbox per project and an Ignore action (`circle-slash`, tooltip "Ignore: hide everywhere"; off for the only focused project, which would widen the lists to every project; the server refuses it too unless the same change clears the focus). Ignored projects are not listed. "No project" (`-`) is listed when the focus or a preset holds it and cannot be ignored here.
  4. When any project is ignored, a folded "Ignored (n)" group; open, each row has Restore (`undo-2`).

**Behaviour.**
* A click on the chip opens or closes the panel. Escape, a click outside or Tab away closes it; Escape returns the focus to the chip (`useDismiss`). The panel floats and moves nothing (an OR-14 exception, like Spotlight); it stays an informational surface of its own colour, border and shadow (OR-16).
* A checkbox adds or removes a project at once; a preset sets the focus in one click (ignored projects in it are left out; a preset whose projects are all ignored cannot be picked); "All projects" clears the focus.
* Ignore hides the project for this person everywhere (the empty texts still speak of "the focused projects" only while a focus is set): lists, counts and `search`, also with All projects. It leaves the focus. A view whose URL names the project still shows it. Restore brings it back.
* The outside count is the [Needs you](../patterns/needs-you.md) items (open questions and Needs-you problems) of projects outside the focus that are not ignored (ignore is full silence); a click closes the panel and opens `/overview?all`, where the count hides. Nothing shows at 0, and nothing is counted with All projects. A new question counts at once, a new problem within a minute.
* Focus, presets and ignored projects are person settings (`focus`, `focusPresets`, `ignored` in `person_settings_*`); agents read and write them. The app applies them to every list and feed that names no project (`prefs.ts`: `scopeIds`, `withFocus`): the focus as its project filter (`projectIds`, `filter.projectId`); with no focus, the ignored projects as the server-side exclusion (`excludeProjectIds`, `filter.excludeProjectId`), so work outside projects and in a project missing from the project list stays visible. A view whose URL names a project gets neither. Agents' list reads leave nothing out unless they pass the exclusion.

**Phone.** At 760 px and below the chip and the indicator keep their icons (the indicator its count); the chip's tooltip names the focus. The panel spans the window under the header.

**Accessibility.** The chip is a button named "Focus: Alpha, Bravo" (or "Focus: All projects") with `aria-expanded`. The panel is a group named "Focus projects"; project checkboxes are named by the project; Ignore and Restore buttons are named "Ignore Alpha", "Restore Alpha". The indicator is named "2 need you outside focus: show every project". Touch targets are 44 px on touch devices.

**Do.** Keep the chip in the header on every page. **Don't.** Hide an ignored project from the Ignored group, or show the outside indicator at 0.

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-34 Focus.** One global project multi-select applies across the whole app and changes in one or two clicks. It is the focus chip in the shell header (OR-43): it names the focused projects (two, then `+N`) and is filled with the accent while a focus is set, so it never reads as "All projects". Its panel floats under the chip and moves nothing (an exception to OR-14): the system preset "All projects" and saved presets, and a project search for long lists. A project is in focus, normal or ignored. An ignored project is hidden everywhere for that person, also with All projects (lists, counts, search), by a server-side exclusion, so work outside projects and in projects missing from the project list stays; it is not offered in the picker and is restored from the panel's folded "Ignored (n)" section; a view whose URL names a project still shows it. When items that need the person (Needs you) belong to projects outside the focus that are not ignored, the chip shows "N need you outside focus" in attention tone; a click opens the Overview without the focus. Ignore is full silence: an ignored project is never counted. With All projects, or at zero, nothing shows. Navigation counters split the same way: in focus bright, outside dim ([nav rail](../controls/nav-rail.md)). Focus, presets and ignored projects are stored on the server per person; agents read and write them.
