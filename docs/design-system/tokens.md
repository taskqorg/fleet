# Tokens

Visual values for direction A "Quiet ledger" with monospace figures ([D3](decisions.md)). The machine-readable source is [packages/ui/tokens.json](../../packages/ui/tokens.json); this file explains how to use it. Pages and controls use tokens only, never raw values.

In CSS every token is a custom property named by its path without the group for colour: `--bg`, `--fg-muted`, `--accent`; other groups keep their prefix: `--space-3`, `--radius-md`, `--font-mono`, `--motion-fast`. `packages/ui/scripts/tokens.mjs` generates them; `npm run check:design` reports contrast pairs below 4.5:1.

## Colour

Colour carries meaning only ([principles §1](principles.md#1-density-and-calm)). Each theme defines the same names.

| Token | Use |
|---|---|
| `bg` | Page background behind panes. |
| `surface` | Panes, lists, inputs. |
| `float` | Floating layers: tooltips, context menus (OR-16). |
| `surface-sunken` | Code, transcripts, wells inside a pane. |
| `fg`, `fg-muted`, `fg-faint` | Main text; secondary text (IDs, reasons, column heads); placeholder and disabled. All meet 4.5:1 on `surface` except `fg-faint`, which is used only for placeholders and disabled text. |
| `line`, `line-soft` | Pane and header borders; row separators. |
| `hover`, `selected` | Row hover; selected row (plus a 3 px `accent` bar at the left edge). |
| `accent`, `on-accent` | Primary action, focus ring, links, the new-rows pill. One accent per screen. |
| `attention-*` | Waiting for a person, warnings, problems. `-fg` text, `-bg` badge fill, `-soft` block fill, `-line` block border. |
| `danger-*` | Failed, apply error, destructive confirm. |
| `running-*` | In progress, running, preparing when active. |
| `success-*` | Completed, succeeded, applied, online. |
| `neutral-*` | Queued, paused, cancelled, unknown. |
| `estimated-fg` | Estimated numbers ([number](controls/number.md)). |
| `data-1` … `data-4` | Series in fill bars and charts, in order. More series reuse `data-1..4` with a pattern, never new hues. |

State to colour mapping lives in [state badge](controls/state-badge.md).

## Type

| Token | Value | Use |
|---|---|---|
| `font-sans` | system UI stack | All text. No web fonts: offline and instant. |
| `font-mono` | system monospace stack | Numbers in columns and totals, IDs, durations, sizes, code and transcripts. |
| `font-size-xs` | 11 px | Sub-labels under totals. |
| `font-size-sm` | 12 px | Column heads, reasons, IDs, badges. |
| `font-size-md` | 13 px | Body and rows (default). |
| `font-size-lg` | 15 px | Section titles, total values. |
| `font-size-xl` | 18 px | Page title (one per page). |
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

## Radius and elevation

`radius-sm` 3 px (badges, kbd), `radius-md` 4 px (controls), `radius-lg` 6 px (panes), `radius-pill` (pills, new-rows pill). Panes are flat with a `line` border. Shadows only for floating layers: `elevation-popover` (menus, popovers), `elevation-pill` (new-rows pill).

## Motion

`motion-fast` 150 ms with `motion-ease` for panels and popovers; `motion-highlight` 1200 ms for the changed-row fade. Rules in [principles §9](principles.md#9-motion).

## Themes

Light and dark are defined in `tokens.json`. CSS applies dark under `@media (prefers-color-scheme: dark)` unless `data-theme="light"` is set on the root, and always under `data-theme="dark"`. The person's override is remembered.
