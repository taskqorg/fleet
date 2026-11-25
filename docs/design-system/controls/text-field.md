# Text field

**Code.** `TextField` in `packages/ui`; [book](../book/index.html#text-field).

**Purpose.** Edit an unsaved form value. It does not imply that a setting was applied; [setting-field](setting-field.md) owns that separate interaction.

**Anatomy.** Visible label, controlled text or number input (multiline for prose), help/error or disabled reason. Use shared fonts, spacing, surfaces and focus tokens.

**States.** Editable, invalid with retained value, disabled with a reason. The caller owns submission and readback; the control sends no requests and invents no bounds.

**Drafts.** A form keeps each typed value in the draft store, `createDraft(key, initial)` from `packages/ui` (`drafts.ts`), keyed by the form and field (`system-add:host:id`), and clears it after a save. A remount, a route change or a reload brings the text back; a live update never erases it. A write-only value (`type="password"`, a key) is never kept. Design rule `UI-INPUT` keeps raw `<input>`/`<textarea>` out of app screens.

**Keyboard and accessibility.** Native input keys; label associated with a unique id, help/error through aria-describedby, invalid through aria-invalid. Multiline Enter remains a newline; creating a record is an explicit form action.

**Do.** Preserve edits on failure. **Don't.** Present an unsaved input as an applied host setting or silently submit on restoration.

**Used in.** Lab experiment creation. The book includes prose, error and pending fixtures. CI owns browser/visual verification under the current local no-launch guard.

## Board

Master `Text field` on [blocks.bl](../board/blocks.bl), board *Sections*, variants `editable`, `invalid` and `disabled`; text names `label`, `value`, `help`. A form opens in the work area or inline, never floating ([OR-14](../owner-rules.md)).
