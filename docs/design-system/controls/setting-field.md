# Setting field

**Code.** `SettingField` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Edits a value that stays until changed and is applied elsewhere, for example store capacity or service memory, and shows whether it was applied.

**Anatomy.** Label; current value (input with unit); apply status after the value as a compact setting badge: applied with its age, pending, or apply error with the error text and an icon-only Retry; help text with what the setting does; "changed by Alex, 10:40" in `fg-muted`.

**States.** Viewing; editing (input, "Save" and "Cancel"); invalid (message under the field, Save disabled); saving; pending (saved, not yet applied by the host); applied; apply error; read only (person lacks the right; shows who can change it).

**Behaviour.** Edit is explicit: click the value or press `Enter` on the row. Save writes the setting through its operation; the status shows pending until the host reports `applied_at` or `apply_error`. No built-in caps: validation checks only type and unit. Agents' proposed changes show as a [decision block](decision-block.md) next to the field.

**Keyboard.** `Enter` edit and save; `Esc` cancel.

**Accessibility.** Label tied to the input; apply status in `aria-describedby` and announced when it changes.

**Do.** Show the applied status next to the value. **Don't.** Show "Saved" as if it were applied.

**Used in.** Hosts and stores, Projects, Connections.

## Board

Master `Setting field` on [blocks.bl](../board/blocks.bl), board *Sections*, variants `applied`, `editing`, `pending` and `error`; the apply status takes the dot-and-word form with the tones of the setting family in the [state badge](state-badge.md) mapping (Applied, Pending, Apply error), not the session words. Text names `label`, `value`, `help`.

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **UR-33.** A setting shows "applied 10:42", "pending" or "apply error: …" next to its value. *Check:* Browser: change a host setting, expect "pending", then "applied".
