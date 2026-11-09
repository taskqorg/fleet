# Transcript viewer

**Code.** `Transcript` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Reads a live or finished execution transcript: messages, tool calls, results and questions, with paging and search.

**Anatomy.** Toolbar (search within transcript, filter by kind: messages, tool calls, errors; "Follow" toggle; jump to start/end); body on `surface-sunken`; entries with time in `font-mono` `fg-muted`, author or tool name, and content. Tool calls are collapsed to one line ("Call: file_change · completed · 2s") and expand on click. Code and command output use `font-mono`.

**States.** Loading (skeleton entries), following (live, pinned to the bottom), paused-follow (pill "24 new"), searching (matches highlighted, "3 of 17" with next/previous), finished, history gap ("Events 120–180 not received yet" in `attention-fg`), error.

**Implementation.** `VirtualList` with measured rows and `follow="bottom"`.

**Behaviour.**
* Virtualised with measured, cached entry heights; older pages load when scrolling up, keeping the anchor entry in place.
* Follow mode: at the bottom, new entries stick to the bottom. Scrolling up turns follow off; the pill turns it back on.
* Expanding an entry above the viewport does not move the viewport.
* Masked secrets show as `[masked]` in `fg-muted`.
* Links to results and files open in the [result viewer](result-viewer.md) beside the transcript.

**Keyboard.** `/` search in transcript; `n`/`N` next/previous match; `End` jump to newest and follow; `e` expand or collapse the focused entry.

**Accessibility.** `role="log"` with polite announcements batched to at most one per 5 s while following; each entry is a focusable article with time and author in its name.

**Do.** Keep native harness detail (images, tool output). **Don't.** Auto-scroll when the person has scrolled up.

**Used in.** Task detail, Sessions, Laboratory trials, History.
