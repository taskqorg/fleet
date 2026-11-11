# Code editor

**Code.** `CodeEditor` and `DiffText` in `packages/ui`.

**Purpose.** Edit a definition's text (an agent, a pipeline, the catalogue) in place, and read a draft's difference from its base.

**Anatomy.** `CodeEditor`: a monospace text area as tall as its text (8 to 40 lines, then it scrolls by its own handle), and the validation problems under it, each with its JSON path. `DiffText`: a unified diff in mono, added lines on the success tint and removed lines on the danger tint.

**Behaviour.**
* The caller keeps the text as a draft (`definition_draft_put`); the editor never saves.
* Problems come from the coordinator's validator and do not block typing; Save stays refused while there are problems.
* An empty diff says "No differences".

**Accessibility.** The text area is labelled by the definition's path; problems are `role="alert"`; the diff is labelled.

**Used in.** Agents, Pipelines, Catalogue (the [definition editor](../patterns/definition-editor.md)).
