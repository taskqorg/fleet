# Infinite list

A list loads its next page as its end scrolls near; there are no Show more, Show less, `+K more`, Older or Newer buttons and no page numbers ([OR-65](#rules-kept-here)). A short preview inside a card shows a fixed number of rows and its title opens the full list ([OR-30](#rules-kept-here)). Code: `packages/ui/src/infinite.tsx`; the app's operation pages: `app/src/op-pages.ts`. Board: [infinite-lists.bl](../board/infinite-lists.bl), approved 2026-11-25 with loading row A (three faint skeleton rows).

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

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-30 Latest N, then the full list.** A block inside a card shows its N latest items and nothing else; its title, with the count, opens the full list (OR-65). A block never grows past the page; it never pushes the rest of the column out of view. A row of chips that does not fit folds the rest into a `+K` count, which expands nothing.
* **OR-65 Lists load on scroll.** No list has Show more, Show less, `+K more`, Older and Newer buttons, page numbers or a fixed row cap with no way to the rest. A list loads its next page as its end scrolls within a few rows of view, through one shared primitive ([infinite list](../patterns/infinite-list.md)): a quiet loading row while a page is in flight, one danger row with Retry when it fails, nothing after the last row. It stays efficient: cursor pages from the operation, one request in flight, an abort when the list closes, loaded pages never read again, new rows merged at the top, and rows past the first screen skip layout until they scroll near. A short preview inside a card follows OR-30.
* **UR-15.** Older rows load as the person nears the end, and a loading row shows only after 300 ms. *Check:* Browser: scroll to the end with a slowed network.
