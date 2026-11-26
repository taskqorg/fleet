# Fact count

**Code.** `FactCount`, `FactCounts` in `packages/ui` (`controls/identity.tsx`, styles `controls.css`); the glyph table `factKinds` in `registry.ts`. Board: [screens/projects.bl](../board/screens/projects.bl), frame "Facts · glyphs", option F2 (owner pick, 27 Nov 2026).

**Purpose.** A row says how many questions, failures, sessions and tasks it has without a word, so a trained operator reads the whole list at a glance and the interface stays light (owner, 27 Nov 2026: "по минимальной информации имел всю картину").

**Anatomy.** A small capsule on its tone's background: the fact's glyph and its count in mono. The glyph table is the one source; every screen uses the same glyph for the same fact:

| Fact | Glyph | Tone | Count |
|---|---|---|---|
| `setup` Needs setup | `sliders-horizontal` | attention | none: the glyph alone |
| `questions` Open questions | `message-circle-question` | attention | yes |
| `failed` Failed | `octagon-x` | danger | yes |
| `working` Sessions working | `activity` | running | yes |
| `inFlight` Tasks in flight | `list` | neutral | yes |

**Order.** Always as in the table, packed to the right of the row: what needs the person first, then trouble, then work. Time stays the rightmost fact ([OR-64](work-facts.md#rules-kept-here)).

**States.** A zero is left out ([OR-29](state-counters.md#rules-kept-here)); a row with no facts shows nothing (no "nothing in flight").

**Accessibility.** Each capsule is `role="img"` named "Open questions: 3"; the tooltip is the same word or a caller's reason (the readiness reason for `setup`). Touch reaches the same words in the item's detail.

**Do.** Add a fact to `factKinds` before a screen shows it. **Don't.** Write the fact as words ("3 questions", "1 in flight", "needs setup"), invent a glyph per screen, or show a zero.

**Used in.** Projects list rows, the Projects Summary, the project head, Conversations rows (open questions), Hosts rows (workers running; a failing poll as `failed`). *Follow-ups:* Changes (reverts in flight), Project Overview delivery, the rail's Projects badge.

## Rules kept here

* **OR-73 Facts are glyphs.** A row's counts are shown as a glyph and a number on the fact's tone, never as words: one glyph per fact everywhere (the table in [fact count](fact-count.md)), zeros hidden, the word in the tooltip. An operator learns the glyphs once and reads every list the same way (owner, 27 Nov 2026).
