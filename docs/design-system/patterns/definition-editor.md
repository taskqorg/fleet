# Definition editor

**Code.** `DefinitionEditor`, with the [draft bar](../controls/draft-bar.md), [choice](../controls/choice.md) and [step list](../controls/step-list.md). Built in the book and the mockups; not in the app.

**Job.** Read and change a definition that lives as a file in git: an agent configuration, a pipeline, a skill, a catalogue entry ([fleet.md, knowledge and rules](../../../fleet.md#knowledge-and-rules)). The file stays the one source; the editor never keeps a copy of its own.

**Where.** The work area of [list and detail](list-detail.md). The control column lists the definitions of one kind. Its first group is "Changed, not saved".

**Composition, from top to bottom.**
1. **Head:** scope, file path and revision in mono; the name; tabs.
2. **Draft bar,** only while the definition differs from its saved revision: how many changes against which revision, then "Test" (or "Run"), "Diff" and "Save as commit". The bar is titled "Draft", so its actions do not repeat the word (OR-17). New executions use the saved revision until the draft is saved.
3. **Sections** with small mono headings, each a group of [setting fields](../controls/setting-field.md) or rows. A changed field carries an attention tick on its left edge.

**Tabs, the same order for every kind.**
* *Configuration* or *Definition*: the editable sections.
* *Effective* (agents): what an execution really receives, as ordered layers with the source and revision of each: shared rules, role, this definition, project override.
* *Diff*: the draft against the saved revision, in the file's own text, with "Compare in Lab" to run both on the same recorded work.
* *Compatibility* (agents): checked combinations with their state. Unchecked is shown as unchecked, never as incompatible.
* *Runs* (pipelines), *Usage* or *Used by*: where the definition is used and what it cost.
* *History*: saved revisions.

**Rules.**
* Saving is a commit; the person sees the diff first. Nothing is saved silently.
* A draft can be tested without saving. The run records the draft's content and its difference from the base.
* Links open the same object, not a copy: the agent named in a pipeline step opens that agent's editor.
* Tools grant capability; credentials are never shown or entered here. They stay in [connections](../../connections.md).
* State comes first in the catalogue row (OR-22): draft, ready, not checked, incompatible for agents; the last run's state for pipelines.
