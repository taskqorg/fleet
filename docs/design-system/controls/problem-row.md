# Problem row

**Code.** `ProblemRow` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Shows a signal that needs attention with its cause, evidence and available actions, without losing the work context.

**Anatomy.** Row with a 3 px `attention-fg` left bar (or `danger-fg` for failures) on `attention-soft`: title ("Low space on windows store"), facts line ("18 GB free, warning at 25 GB"), affected work link ("4 tasks waiting"), action buttons (default buttons; the one recommended action may be primary), "Details" link to the cause and evidence.

**States.** Open, acting (button busy), resolved (fades to neutral and shows "Resolved 10:50" until the list refreshes), acknowledged (moved to a lower group, still visible), uncertain ("Action sent, outcome not confirmed").

**Behaviour.** Actions run the system operations listed for the problem; a displayed action does not prove completion, so the row stays until the cause clears. Problems are listed by severity then age.

**Keyboard.** Row is focusable; `Enter` opens details; action shortcuts digits `1`–`3` while focused.

**Accessibility.** `role="group"` named by the title; severity in words ("Warning", "Failure") in the accessible name.

**Do.** Offer the real actions (raise capacity, change locations, add retention rule). **Don't.** Show a problem without a next step or a dismiss-only alert.

**Used in.** Overview waiting list, Hosts, task detail, Projects readiness.

## Board

Master `Problem row` on [blocks.bl](../board/blocks.bl), board *Data*, tones `attention` and `danger` ([OR-56](../owner-rules.md)); up to three action buttons, the recommended one primary, then Details. Repeats aggregate into one row with `×N` ([OR-58](../owner-rules.md)); built: `count` and `onDetails` on `ProblemRow`, demo: [book](../book/index.html#problem-blocks). Text names: `title`, `facts`, `details`; buttons take `label`. A run failure's title is its plain cause; `code` puts the stored cause code in the title's tooltip, never in its text ([formats](../formats.md#values)).
