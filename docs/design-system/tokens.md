# Tokens

Visual values for direction B "Instrument" ([D19](decisions.md)): neutral greys with a barely cool cast and no hue of their own, an accent that is the ink itself (near-white in dark, near-black in light), hairlines, 2 px corners, and monospace for figures, IDs and small labels. The machine-readable source is [packages/ui/tokens.json](../../packages/ui/tokens.json); this file explains how to use it. Pages and controls use tokens only, never raw values.

In CSS every token is a custom property named by its path without the group for colour: `--bg`, `--fg-muted`, `--accent`; other groups keep their prefix: `--space-3`, `--radius-md`, `--font-mono`, `--motion-fast`. `packages/ui/scripts/tokens.mjs` generates them; `npm run check:design` reports contrast pairs below 4.5:1.

## Colour

Colour carries meaning only ([principles §1](principles.md#1-density-and-calm)). Each theme defines the same names.

| Token | Use |
|---|---|
| `rail`, `bg`, `surface` | The three columns, in order: navigation, control column, work area ([app shell](patterns/app-shell.md)). Each step is lighter than the one before, in both themes, so the work area reads as the top layer. |
| `float` | Floating layers: tooltips, context menus (OR-16). |
| `surface-sunken` | Code, transcripts, wells inside a pane. |
| `fg`, `fg-muted`, `fg-faint` | Main text; secondary text (IDs, reasons, column heads); placeholder and disabled. All meet 4.5:1 on `surface` except `fg-faint`, which is used only for placeholders and disabled text. |
| `line`, `line-soft` | Pane and header borders; row separators. |
| `hover`, `selected` | Row hover; selected row (plus a 3 px `accent` bar at the left edge). |
| `accent`, `on-accent` | Primary action, focus ring, links, the current-item tick, the new-rows pill. The accent has no hue (D19): the only colours on a screen are the state tones below. |
| `attention-*` | Waiting for a person, warnings, problems. `-fg` text, `-bg` badge fill, `-soft` block fill, `-line` block border. |
| `danger-*` | Failed, apply error, destructive confirm. |
| `running-*` | In progress, running, preparing when active. |
| `success-*` | Completed, succeeded, applied, online. |
| `neutral-*` | Queued, paused, cancelled, unknown. |
| `estimated-fg` | Estimated numbers ([number](controls/number.md)). |
| `data-1` … `data-4` | Series in fill bars and charts, in order. More series reuse `data-1..4` with a pattern, never new hues. |
| `data-1-ink` … `data-4-ink` | Text set inside a `data-N` fill (a [share bar](controls/share-bar.md) segment): near-white or near-black by the fill's luminance, at least 4.5:1. |

State to colour mapping lives in [state badge](controls/state-badge.md).

## Type

| Token | Value | Use |
|---|---|---|
| `font-sans` | The native UI stack: `system-ui`, then San Francisco, Segoe UI, Roboto, Helvetica Neue, Arial, then colour emoji | All text. No web fonts: the text renders in its final font on the first paint ([D22](decisions.md#2026-11-11-d22-system-fonts)). Every listed face carries Cyrillic. |
| `font-mono` | The native monospace stack: `ui-monospace`, SF Mono, Cascadia Mono, JetBrains Mono, Menlo, Consolas, Liberation Mono | Numbers in columns and totals, IDs, durations, sizes, code and transcripts. |
| `font-size-xs` | 11 px | Sub-labels under totals. |
| `font-size-sm` | 12 px | Column heads, reasons, IDs, badges. |
| `font-size-md` | 13 px | Body and rows (default). |
| `font-size-lg` | 15 px | Section titles, total values. |
| `font-size-xl` | 18 px | Page title (one per page). |
| `font-size-2xl` | 22 px | Title of the item opened in the work area. |
| `font-line` | 1.4 | Line height for text blocks. Rows are sized by `size-row`, not line height. |
| `font-weight-*` | 400, 500, 600 | Regular text; row titles and labels; page and section titles. |

Numbers in mono also use `font-variant-numeric: tabular-nums`.

## Space and size

Spacing scale `space-1..7` = 2, 4, 8, 12, 16, 24, 32 px. Use the scale; no in-between values.

| Token | Value | Use |
|---|---|---|
| `size-row` | 32 px | Default list row on desktop. |
| `size-row-compact` | 28 px | Optional compact density, per person. |
| `size-row-phone` | 44 px | Rows and targets below 640 px. |
| `size-control` | 28 px | Buttons, inputs, chips on desktop. |
| `size-focus-ring` | 2 px | Focus outline width and offset. |
| `size-icon`, `size-icon-sm` | 16, 14 px | Registry icons; small inside badges and chips. |
| `size-badge`, `size-chip` | 20, 24 px | Badge height (and compact badge width); chip height. |
| `size-fill`, `size-search`, `size-list`, `size-total`, `size-clock`, `size-label`, `size-input` | 160, 220, 320, 120, 64, 160, 120 px | Component widths and default list height. |
| `size-thumb` | 72 px | A message image thumbnail (square, fixed so a loading image moves nothing). |
| `size-ctl`, `size-row-2` | 520, 44 px | Control column width ([list and detail](patterns/list-detail.md)); `size-row-2` stays for two-line rows outside the control column. |

## Radius and elevation

`radius-sm` 2 px (badges, kbd), `radius-md` 2 px (controls), `radius-lg` 3 px (panes), `radius-pill` (pills, new-rows pill). Panes are flat with a `line` border. Shadows only for floating layers: `elevation-popover` (menus, popovers), `elevation-pill` (new-rows pill).

## Motion

`motion-fast` 150 ms with `motion-ease` for panels and popovers; `motion-highlight` 1200 ms for the changed-row fade. Rules in [principles §9](principles.md#9-motion).

## Themes

Light and dark are defined in `tokens.json`. CSS applies dark under `@media (prefers-color-scheme: dark)` unless `data-theme="light"` is set on the root, and always under `data-theme="dark"`. The person's override is remembered.

## Design board

The [design board](README.md#design-board) draws with these tokens. [Styles/default.ds](board/Styles/default.ds) is generated from `tokens.json` by `pnpm run board:tokens`; edit `tokens.json`, run it, and commit both. `tests/ui/board-tokens.test.ts` fails when the two differ.

* **Colours.** Each colour is `fleet.<name>` with its light and dark value (`$fleet.bg` is `var(--bg)`), and the board's semantic roles map onto them: surface, container, text, outline, primary, success, error, warning, info. Dark is the first theme.
* **Type, space, radius, size.** `$font.size.xs` to `$font.size.2xl`, `$spacing.xs` to `$spacing.3xl`, `$fleet.radius.*` and `$fleet.size.*` carry the same values.
* **Font.** The board draws with Inter, the closest bundled face to the platform font that `packages/ui` uses.

[styles.bl](board/styles.bl) shows every colour in both themes and the type scale, drawn from the tokens themselves.

