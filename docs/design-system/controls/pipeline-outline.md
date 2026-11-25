# Pipeline outline

**Code.** `PipelineOutline` (`packages/ui/src/controls/pipeline-outline.tsx`).

**Purpose.** Shows how a pipeline works in plain words, so a person reads it like a short Markdown list instead of a step diagram or JSON ([OR-38](../owner-rules.md)).

**Anatomy.** An ordered list in a bordered block. Each line: the step number `n.` (muted mono), the step id (mono, medium weight) and what the step does (normal text, wraps). A nested step (`depth` 1) is indented one step; a condition line has no number and is in the attention tone.

**Behaviour.** The data is the server's `outline` (`PipelineOutlineLine[]`: `n`, `step`, `text`, `depth`, `condition`). The same text at 1440 and 390 px: lines wrap and never scroll sideways ([OR-66](../owner-rules.md)).

**Used in.** A project's pipeline page: Overview ("How it works") and Definition ([Projects](../pages/projects.md)).

## Board

[screens/projects.bl](../board/screens/projects.bl), Approved P-A5 and P-A7.
