# Avatar

**Code.** `Avatar` in `packages/ui` (`controls/avatar.tsx`, styles `home.css`). Demo: [book](../book/index.html#bot-avatar). Board: [blocks.bl](../board/blocks.bl), `Blocks · Bot avatar` (master `Bot avatar`, activity states); the bot page with it: [library.bl](../board/screens/library.bl), section 4.

**Purpose.** Who a row, tile, card or page is about. A bot keeps its letter until someone gives it a picture (owner pick, 25 Nov 2026).

**Anatomy.** A circle in one of four sizes: `sm` 16 (list rows, swarm members), `md` 24 (tiles, cards, rail, composer targets), `lg` 40 (`size-avatar-lg`, a page header), `xl` 96 (`size-avatar-xl`, the bot's Overview and the avatar picker). It shows, in order of precedence:
* a **picture**: any still image (PNG, JPEG, WebP, AVIF, SVG), an animated image (animated WebP, AVIF, GIF, APNG, animated SVG) or a muted video loop (WebM, MP4), cropped to the circle; with its optional still frame;
* the bot's **harness mark** on `surface-sunken`;
* a **letter** on the bot's identity colour ([OR-72](project-tile.md#rules-kept-here); `color` of the bot, else its stored automatic `autoColor`), else on the tone (`P` for a project PM).

A picture keeps its own look inside a 2 px ring in the bot's identity colour.

**Motion.** A picture plays at `lg` and `xl`, on hover, and while the bot works. Elsewhere it shows its still frame, so a list of many bots costs one small image per row and no animation. Under reduced motion every avatar shows its still frame and nothing breathes. Lottie and TGS are not shown (they need a player); the avatar pipeline turns sources into animated SVG or a video loop.

**States (activity).** The avatar's own light says what the bot is doing; no ring:
* *Asleep:* dim (55%).
* *Working:* full, with a glow in the picture's glow colour (else the tone) that breathes over 4 s.
* *Waiting for you:* full, steady, an amber `attention-fg` dot at the lower right.
* *Failed:* the picture dim, a red `danger-fg` dot.
* *Paused:* dimmest (35%), no glow.

The state comes from `bot_list`/`bot_get`: `paused`, the active run, `needsYou` and the last run's state. The row's words say the same thing; the dot is the only colour signal. The dot's ring is `--avatar-ring` (default `surface`): a surface sets it to its own colour (`rail`, `float`). `tip` gives an avatar without its name (the collapsed rail) a tooltip.

**Accessibility.** Decorative by default (the name next to it carries the meaning); with `label` it is an image with that name. The dot is decorative: the state word is in the row.

**Setting it.** Bot › Settings & Memory › Avatar: Letter, Upload file, or Make with pipeline (`avatar-make`: description and optional reference, optionally animated; pick one of the results). Agents: `bot_avatar_set` (an image or video artifact of the bot's project, an optional still frame, a glow colour) and `bot_avatar_clear`. **Colour:** Bot › Settings & Memory › Colour: Auto (the least-used colour stored when the bot was made) or one of the eight, solid or hatched, saved at once; agents `bot_identity_set {botId, color, pattern}` ('' is automatic).

**Used in.** [Library](../pages/library.md) bot rows, the bot header and Overview, [Home](../pages/home.md) tiles and cards, the composer target picker.
