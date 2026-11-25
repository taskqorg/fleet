# Infinite list

A list loads its next page as its end scrolls near; there are no Show more, Show less, `+K more`, Older or Newer buttons and no page numbers ([OR-65](../owner-rules.md)). A short preview inside a card shows a fixed number of rows and its title opens the full list ([OR-30](../owner-rules.md)). Code: `packages/ui/src/infinite.tsx`; the app's operation pages: `app/src/op-pages.ts`. Board: [infinite-lists.bl](../board/infinite-lists.bl), approved 2026-11-25 with loading row A (three faint skeleton rows).

## Parts

* **Pages** (`createPages`). The first page is the view's live read (`useOp`): change rows reread it, so new rows arrive at the top. Older pages come from the operation's own cursor (`before`/`nextBefore`, `after`/`nextAfter`) and are read once each. A first page reread after older pages loaded merges by key at the top; the first page's version of a row wins. Only one page is in flight. A new filter or closing the list aborts it and drops the older pages.
* **Window** (`createWindow`). Rows already in memory (a JSON log, a board feed, spawned tasks) are built a page at a time the same way, so the DOM never holds rows nobody scrolled to.
* **List end** (`ListEnd`). An invisible sentinel right after the last row, observed by an `IntersectionObserver` against the nearest scrolling ancestor with a 200 px margin (about five rows). It asks for the next page once per arrival; a page that leaves the end in view asks for the next as soon as it lands. While a page loads it shows the quiet loading row; when a page fails, one danger row says what did not load and offers Retry, which asks only for that page; after the last row it shows nothing.
* **Infinite list** (`InfiniteList`). The rows and their end. Rows past the first 25 have `content-visibility: auto` with a remembered intrinsic size, so they skip layout and paint until they scroll near.
* **Virtual list.** A list long enough to need windowing (Tasks, Conversations, Results) keeps `VirtualList` and its `onEnd`, which follows the same rules.

## Preview inside a card

A section that previews a longer list (Latest finished, Waiting for you, a bot's recent runs) shows a fixed number of rows and no toggle (`Preview`). Its title (`DetailSection` with `href`, `onOpen` and `count`) opens the full list with the same filter; the full list loads on scroll. A section that is the page's main list (a project's sessions) is an infinite list itself. Problems and questions only a card answers (Needs a look, Waiting for you on Home) are never cut.

## Phone

The same parts; the loading and error rows are 44 px, and Retry is a full hit target.
