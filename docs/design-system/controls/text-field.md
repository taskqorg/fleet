# Text field

**Code.** `TextField` in `packages/ui`; [book](../book/index.html#text-field).

**Purpose.** Edit an unsaved form value. It does not imply that a setting was applied; [setting-field](setting-field.md) owns that separate interaction.

**Anatomy.** Visible label, controlled text or number input (multiline for prose), help/error or disabled reason. Use shared fonts, spacing, surfaces and focus tokens.

**States.** Editable, invalid with retained value, disabled with a reason. The caller owns draft persistence, submission and readback; the control sends no requests and invents no bounds.

**Keyboard and accessibility.** Native input keys; label associated with a unique id, help/error through aria-describedby, invalid through aria-invalid. Multiline Enter remains a newline; creating a record is an explicit form action.

**Do.** Preserve edits on failure. **Don't.** Present an unsaved input as an applied host setting or silently submit on restoration.

**Used in.** Lab experiment creation. The book includes prose, error and pending fixtures. CI owns browser/visual verification under the current local no-launch guard.
