# Result viewer shell

**Code.** `ResultViewer` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** One frame that opens any result in the right viewer: text and reports, code diffs, images with zoom, video with a poster frame, 3D models later, and links to a running preview.

**Anatomy.**
* Header: result name, type, size ([number](number.md)), producing execution link, copy state per location ([state badge](state-badge.md): stored, placed, pending, unavailable), icon-only actions (compare, download, share; OR-1).
* Body: the viewer for the type, on `surface-sunken`.
* Side strip (optional): checks for this result, comments and annotations.

**As built.** `ResultViewer` heads the result with its name as people read it (`fileLabel`: no UUID and no long hash from the writer's file name, [OR-38](../owner-rules.md)), its code (`R-FLT-12`, the UUID only as the code's copy tooltip), the kind in words (`formatLabel`: JSON lines, Markdown, CSV table, Diff), size and the producing execution. The stored file name stays the download name. `ArtifactView` is the body: image (fit, fixed 4:3 frame so nothing jumps), video (16:9 frame, native controls), text read in 64 KB `Range` pages as the reader scrolls (no cap), and download only for other types. Zoom, split diff, versions and 3D are not built yet. Canvas: [screens/projects.bl](../board/screens/projects.bl), "Result viewer".

**Rendered first (owner).** Every format with a renderer opens rendered; a **Preview** · **Source** choice above the body (Unified · Source for a diff) switches to the source and back. The choice is remembered per format for the browser session. Copy and download always take the source.

| Format | Preview | Source |
|---|---|---|
| HTML | Runs in a sandboxed frame: scripts on an opaque origin, no network, no access to the app | Text |
| SVG | Image (`<img>`, so its scripts never run) | Text |
| Markdown (`text/markdown`, `.md`) | Rendered as nodes, never as HTML | Text |
| JSON, JSON lines (`application/x-ndjson`, `.jsonl`) | Key/value: keys on the left, values on the right, a nested object or array folded under its key, `null` as "none"; JSON lines one record each ("Record 2 of 9"), only whole lines while pages load. Not JSON after all: the text | Text as stored |
| Diff (`text/x-diff`, `text/x-patch`, `.diff`, `.patch`) | Unified: file list on the left with added and removed counts, hunks with old and new line numbers, a sign and the words "added"/"removed" for screen readers. A narrow viewer steps through files and drops line numbers | Text |
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
