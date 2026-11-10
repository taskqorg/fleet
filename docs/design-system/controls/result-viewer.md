# Result viewer shell

**Code.** `ResultViewer` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** One frame that opens any result in the right viewer: text and reports, code diffs, images with zoom, video with a poster frame, 3D models later, and links to a running preview.

**Anatomy.**
* Header: result name, type, size ([number](number.md)), producing execution link, copy state per location ([state badge](state-badge.md): stored, placed, pending, unavailable), icon-only actions (compare, download, share; OR-1).
* Body: the viewer for the type, on `surface-sunken`.
* Side strip (optional): checks for this result, comments and annotations.

**As built (Conversations Stage 4).** `ArtifactView` is the body: image (fit, fixed 4:3 frame so nothing jumps), video (16:9 frame, native controls), text read in 64 KB `Range` pages as the reader scrolls (no cap), HTML and SVG as source with a "Preview (no scripts)" toggle that shows them in a `sandbox=""` frame, and download only for other types. Zoom, diff, versions and 3D are not built yet.

**Viewers.** Text and markdown (rendered, with raw toggle); diff (unified and split, file list on the left); image (fit, 100 %, zoom with wheel and `+`/`-`, pan by drag); video (poster frame, native controls); preview link (URL, last check, "Open preview"); 3D (placeholder until defined); unknown type (metadata and download only).

**States.** Loading (header shown at once, body skeleton), ready, unavailable ("No holding store is online. Last copy on windows, seen 2h ago"; never "lost"), too large to preview (download only), error.

**Behaviour.** Compare opens two versions side by side with synchronised zoom and scroll, or a diff for text. Opening a result keeps the task context; `Esc` returns to it.

**Keyboard.** `Esc` close; `[`/`]` previous and next result; `c` compare; `+`/`-`/`0` zoom.

**Accessibility.** Images need alt text from the result description; diffs are readable as text with added and removed lines marked in words, not only colour.

**Do.** Show copy state. **Don't.** Download a large file just to show metadata.

**Used in.** Task detail, Results, Sessions, Laboratory comparison.
