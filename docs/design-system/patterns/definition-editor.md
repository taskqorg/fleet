# Definition editor

**Code.** `DefinitionEditor`, with the [draft bar](../controls/draft-bar.md), [choice](../controls/choice.md) and [step list](../controls/step-list.md). Built in the book and the mockups, and in the app as Agents, Pipelines and Catalogue (`app/src/views/definitions.tsx`): the definition text in a [code editor](../controls/code-editor.md), kept as a draft while typing, the draft bar, Diff and History.

**Job.** Read and change a definition that lives as a file in git: an agent configuration, a pipeline, a skill, a catalogue entry ([fleet.md, knowledge and rules](../../../fleet.md#knowledge-and-rules)). Saved definitions are revisions in the coordinator's store and apply to the next execution; every revision is mirrored to a private config repository; the file in the code repository is the seed ([fleet-definition-editors.md](../../designs/fleet-definition-editors.md)).

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
* Saving creates a revision (the button keeps "Save as commit"); the person sees the diff first. Nothing is saved silently.
* A draft can be tested without saving. The run records the draft's content and its difference from the base.
* Links open the same object, not a copy: the agent named in a pipeline step opens that agent's editor.
* Tools grant capability; credentials are never shown or entered here. They stay in [connections](../../designs/fleet-connections.md).
* State comes first in the catalogue row (OR-22): draft, ready, not checked, incompatible for agents; the last run's state for pipelines.

## Diff row

How a configuration shows only what differs from its base ([OR-45](#rules-kept-here)). Built: `DiffRow` and `InheritedRows` in `packages/ui` (`src/controls/diff-row.tsx`); demo: [book](../book/index.html#diff-row). Master `Diff row` on [blocks.bl](../board/blocks.bl), board *Data*:
* `changed`: the field in mono and its value; the base default is in the tooltip only, never as a second value or an arrow on the row ([OR-7](../owner-rules.md), [OR-20](../owner-rules.md)).
* `inherited`: the folded "Inherited defaults (N)" row, which opens the unchanged fields in place.

Text names: `field`, `value`, `label`. Used in the Library definition editor, a project's Setup and a Lab variant.

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-45 Diff from default.** Configuration and comparison views (agents, projects, pipelines, Lab variants) show identity plus **only** fields that differ from their base default (harness default, `variantOf` base profile, shared project defaults, or Lab baseline). Unchanged inherited defaults stay collapsed in a single expandable `Inherited defaults (N)` row so differences stand out at a glance. No `override`, `overridden`, or `custom` badges or words, and no `t-attention` tone on normal configuration values; show the base default only in a tooltip if useful (OR-20).
* **UR-65.** (merged with OR-45) Configuration and comparison views show only what differs from the default ([OR-45](#rules-kept-here)). *Check:* Review.
