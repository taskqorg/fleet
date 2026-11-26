# Fact count

**Code.** `FactCount`, `FactCounts` in `packages/ui` (`controls/identity.tsx`, styles `controls.css`); the glyph table `factKinds` in `registry.ts`. Board: [screens/projects.bl](../board/screens/projects.bl), frame "Facts · glyphs", option F2 (owner pick, 27 Nov 2026); the remaining screens: [screens/fact-glyphs.bl](../board/screens/fact-glyphs.bl) (approved 27 Nov 2026).

**Purpose.** A row says how many questions, failures, sessions and tasks it has without a word, so a trained operator reads the whole list at a glance and the interface stays light (owner, 27 Nov 2026: "по минимальной информации имел всю картину").

**Anatomy.** A small capsule on its tone's background: the fact's glyph and its count in mono. The glyph table is the one source; every screen uses the same glyph for the same fact:

| Fact | Glyph | Tone | Count |
|---|---|---|---|
| `setup` Needs setup | `sliders-horizontal` | attention | none: the glyph alone |
| `need` Need you | `bell` | attention | yes |
| `questions` Open questions | `message-circle-question` | attention | yes |
| `failed` Failed | `octagon-x` | danger | yes, or the glyph alone |
| `behind` Hosts behind | `clock` | attention | yes |
| `reverting` Reverts in flight | `undo-2` | attention | yes |
| `drift` Drifting | `circle-alert` | attention | yes |
| `working` Sessions working | `activity` | running | yes |
| `installing` Hosts installing | `download` | running | yes |
| `inFlight` Tasks in flight | `list` | neutral | yes |
| `onWay` On their way to the repository | `git-pull-request` | neutral | yes |
| `lab` Lab experiments | `chart-no-axes-column` | neutral | yes |
| `merged` Merged | `check-check` | success | yes |

**Order.** Always as in the table, packed to the right of the row: what needs the person first, then trouble, then work, then what is done. Time stays the rightmost fact ([OR-64](work-facts.md#rules-kept-here)).

**States.** A zero is left out ([OR-29](state-counters.md#rules-kept-here)); a row with no facts shows nothing (no "nothing in flight").

**Accessibility.** Each capsule is `role="img"` named "Open questions: 3", or by the caller's tooltip when it gives one ("3 of 4 slots busy"); the tooltip is the same words. Touch reaches the same words in the item's detail.

**Do.** Add a fact to `factKinds` before a screen shows it. **Don't.** Write the fact as words ("3 questions", "1 in flight", "needs setup"), invent a glyph per screen, or show a zero.

**Used in.** Projects list rows, the Projects Summary, the project head, Conversations rows (open questions), Hosts rows (workers running; a failing poll as `failed`). Library › Changes summary line, Project Overview › Delivery (merged), the rail counters on their default views (glyph only; [nav rail](nav-rail.md)).

## Rules kept here

* **OR-73 Facts are glyphs.** A row's counts are shown as a glyph and a number on the fact's tone, never as words: one glyph per fact everywhere (the table in [fact count](fact-count.md)), zeros hidden, the word in the tooltip. An operator learns the glyphs once and reads every list the same way (owner, 27 Nov 2026).
