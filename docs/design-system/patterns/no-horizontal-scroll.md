# No horizontal scroll

Nothing in Fleet scrolls sideways ([OR-66](../owner-rules.md)): on a Mac trackpad a sideways swipe over a scroller turns into browser Back or Forward. Content reflows to the width it has, at every window width and in every column. Board: [no-hscroll.bl](../board/no-hscroll.bl), approved 2026-11-26. Code: `packages/ui`; check: `tests/ui/hscroll.ts` (the built app in `tests/ui/app.check.ts`, the deployed site in `scripts/hscroll.ts`).

## Patterns

One pattern for each kind of wide content; a screen picks the pattern and adds no overflow of its own.

| Content | Wide | Narrow | Code |
|---|---|---|---|
| Table | A table with a sticky header while its columns fit the frame. | Stacked rows: the row header on top, every other cell as label and value, the label from its column header. | `DataTable` measures its table on resize and on new rows and sets `.stacked`; no fixed breakpoint. |
| Key-value grid | Label column and value; the value wraps. | Under a 28rem container the label goes above the value. | `.kvt-row`, `.kv` |
| Long value, ID, path, URL | In a row: one line cut with an ellipsis; the full text stays in the DOM, its tooltip and the entity card. | In a detail, card or message: the value wraps in full (`overflow-wrap: anywhere`). | `.kv-line`, base `overflow-wrap` |
| Code, log, diff, JSON | Long lines soft-wrap (`white-space: pre-wrap`), keeping indentation. | Same; the line-number gutter stays, one number per logical line. | base `pre`, `.rd-lines` |
| Chip row, flow, tabs | Chips and flow steps wrap onto the next line. | Same. In a fixed-height list row the chips that do not fit fold into `+K` (OR-30); tabs that do not fit go into More. | `.flow`, `.pinned-marks`, `TabBar` |
| Wide matrix (Lab Model × Pipeline) | The matrix while all its columns fit. | A ranked list of model and pipeline pairs, best score first; a pipeline filter gives the pivot. | `lab-matrix.tsx` |

## Safety net

`html` and `body` set `overscroll-behavior-x: none`, so a stray sideways overscroll is not navigation. It is a net, not the fix: the fix is that nothing overflows.

## Check

`hscrollScan` lists every element that overflows sideways: the page itself, a box with `overflow-x: auto` or `scroll` whose content is wider, and content that sticks out past the viewport. The app check runs it on every route at 1440, 1100, 768 and 390 px and puts each case in the design report, so a new overflow shows on the pull request. `scripts/hscroll.ts` walks the deployed site at the same widths, every section route and the first details it links to.
