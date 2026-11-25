# Bot tile

**Code.** `BotTile` in `packages/ui`. Demo: [book](../book/index.html#bot-tile). Board: [blocks.bl](../board/blocks.bl), master `Bot tile`.

**Purpose.** A bot with no active run on [Home](../pages/home.md): what it is for, when it was last used, and a way to wake it ([OR-50](../owner-rules.md)).

**Anatomy.** One row tile with a dashed `line` border and `radius-lg`, height `size-row-2`: the avatar ([agent mark](transcript-viewer.md)), the name, under it the purpose and the last use in `fg-muted` (`delivery, rollouts · 6h`, [OR-2](../owner-rules.md)), and a Wake [button](button.md) on the right.

**States.**
* *Asleep:* as above.
* *Waking:* Wake is busy; the tile stays until the session's card appears in Working, then leaves the row.
* *Wake failed:* the tile keeps its place, its border turns `danger-fg`, and the last-use text gives the reason with Retry ([OR-56](../owner-rules.md)).

**Behaviour.**
* A click on the tile opens the bot's session view; Wake starts a run and opens nothing.
* Typing to the bot in the [composer](composer.md#target-picker) also wakes it with its memory.
* Tiles share one row, as many as fit, then `All N bots ›`, which opens Bots ([OR-30](../owner-rules.md)).

**Keyboard.** One focus stop per tile; `Enter` opens; `Tab` moves to Wake.

**Accessibility.** The tile is named "<bot>, asleep, last used 6 hours ago"; Wake is named "Wake <bot>".

**Do.** Say what the bot is for in three or four words. **Don't.** Show a state dot: the dashed border already says asleep, and the word stays in the accessible name.

**Used in.** [Home](../pages/home.md) Asleep row.
