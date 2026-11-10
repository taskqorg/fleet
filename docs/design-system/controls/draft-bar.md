# Draft bar

**Code.** `DraftBar` in `packages/ui`. Demo: [book](../book/index.html#draft-bar).

**Purpose.** Says that a definition differs from its saved revision, and offers what can be done with the difference ([definition editor](../patterns/definition-editor.md)).

**Anatomy.** The shape of a [problem row](problem-row.md) with the attention bar: an icon, "Draft", the number of changes and the revision they are against, one sentence on what differs, then the actions. The primary action is "Save as commit".

**Behaviour.** Shown only while there is a difference. It never appears for a saved definition.

**Accessibility.** A group named "Draft".

**Used in.** Agents, Pipelines.
