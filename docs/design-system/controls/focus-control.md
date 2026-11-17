# Focus chip

**Code.** `FocusControl` in `packages/ui`; the app wires it in `app/src/main.tsx` (`Focus`). Demo: [book](../book/index.html#focus-control).

**Purpose.** Shows and sets which projects the person watches (OR-34): the focus, saved presets and the projects they ignore, plus what needs them outside the focus. It sits on the right of the [shell header](../patterns/app-shell.md) (OR-43), before the search icon.

**Anatomy.**
* The chip: the `focus` icon, the focused project names (two, then `+N`) or "All projects", and a chevron. With a focus set it is filled with the accent, so it never reads as All projects.
* Before the chip, when above 0: "N need you outside focus" in attention tone with the `bell` icon.
* The panel, under the chip, right-aligned:
  1. Presets: the system "All projects", the person's saved presets (each with a remove cross), and Save focus preset (icon) while a focus is set.
  2. Find project (a search field) when more than 8 projects are offered.
  3. The projects: a checkbox per project and an Ignore action (`circle-slash`, tooltip "Ignore: hide everywhere"). Ignored projects are not listed. "No project" (`-`) is listed when the focus or a preset holds it and cannot be ignored here.
  4. When any project is ignored, a folded "Ignored (n)" group; open, each row has Restore (`undo-2`).

**Behaviour.**
* A click on the chip opens or closes the panel. Escape, a click outside or Tab away closes it and returns the focus to the chip (`useDismiss`). The panel floats and moves nothing (an OR-14 exception, like Spotlight); it stays an informational surface of its own colour, border and shadow (OR-16).
* A checkbox adds or removes a project at once; a preset sets the focus in one click (ignored projects in it are left out); "All projects" clears the focus.
* Ignore hides the project for this person everywhere: lists, counts and `search`, also with All projects. It leaves the focus. A view whose URL names the project still shows it. Restore brings it back.
* The outside count is the [Needs you](../patterns/needs-you.md) items (open questions and Needs-you problems) of projects outside what lists show; a click opens `/overview?all`. Nothing shows at 0, and nothing is counted while every project is shown.
* Focus, presets and ignored projects are person settings (`focus`, `focusPresets`, `ignored` in `person_settings_*`); agents read and write them. The app applies them as the project filter of every list that names no project (`prefs.ts`: `focusIds`, `withFocus`).

**Phone.** At 760 px and below the chip and the indicator keep their icons (the indicator its count); the chip's tooltip names the focus. The panel spans the window under the header.

**Accessibility.** The chip is a button named "Focus: Alpha, Bravo" (or "Focus: All projects") with `aria-expanded`. The panel is a group named "Focus projects"; project checkboxes are named by the project; Ignore and Restore buttons are named "Ignore Alpha", "Restore Alpha". The indicator is named "2 need you outside focus: show every project". Touch targets are 44 px on touch devices.

**Do.** Keep the chip in the header on every page. **Don't.** Hide an ignored project from the Ignored group, or show the outside indicator at 0.
