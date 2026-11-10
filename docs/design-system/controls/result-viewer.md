# Result viewer shell

**Code.** `ResultViewer` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** One frame that opens any result in the right viewer: text and reports, code diffs, images with zoom, video with a poster frame, 3D models later, and links to a running preview.

**Anatomy.**
* Header: result name, type, size ([number](number.md)), producing execution link, copy state per location ([state badge](state-badge.md): stored, placed, pending, unavailable), icon-only actions (compare, download, share; OR-1).
* Body: the viewer for the type, on `surface-sunken`.
* Side strip (optional): checks for this result, comments and annotations.

**As built.** `ArtifactView` is the body: image (fit, fixed 4:3 frame so nothing jumps), video (16:9 frame, native controls), text read in 64 KB `Range` pages as the reader scrolls (no cap), and download only for other types. Zoom, diff, versions and 3D are not built yet.

**Rendered first (owner).** Every format with a renderer opens rendered; one **Source** / **Preview** button above the body switches to the source and back. The choice is remembered per format for the browser session. Copy and download always take the source.

| Format | Preview | Source |
|---|---|---|
| HTML | Runs in a sandboxed frame: scripts on an opaque origin, no network, no access to the app | Text |
| SVG | Image (`<img>`, so its scripts never run) | Text |
| Markdown (`text/markdown`, `.md`) | Rendered as nodes, never as HTML | Text |
| JSON | Indented, two spaces | Text as stored |
| CSV | Table, first row as the header | Text |
| Image, video | As above; no switch | — |
| Anything else textual (plain text, YAML, logs, code) | No renderer: the text shows directly | — |

YAML has no renderer: its source is already its readable form.

**Viewers.** Text and markdown (rendered, with raw toggle); diff (unified and split, file list on the left); image (fit, 100 %, zoom with wheel and `+`/`-`, pan by drag); video (poster frame, native controls); preview link (URL, last check, "Open preview"); 3D (placeholder until defined); unknown type (metadata and download only).

**States.** Loading (header shown at once, body skeleton), ready, unavailable ("No holding store is online. Last copy on windows, seen 2h ago"; never "lost"), too large to preview (download only), error.

**Behaviour.** Compare opens two versions side by side with synchronised zoom and scroll, or a diff for text. Opening a result keeps the task context; `Esc` returns to it.

**Keyboard.** `Esc` close; `[`/`]` previous and next result; `c` compare; `+`/`-`/`0` zoom.

**Accessibility.** Images need alt text from the result description; diffs are readable as text with added and removed lines marked in words, not only colour.

**Do.** Show copy state. **Don't.** Download a large file just to show metadata.

**Used in.** Task detail, Results, Sessions, Laboratory comparison.
