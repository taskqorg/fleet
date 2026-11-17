# Start picker

**Code.** `ProjectPick`, `PresetPicker` and `StartFacts` in `packages/ui` (`data-ui` `project-pick`, `preset-pick`, `start-facts`). Demo: [book](../book/index.html#start-picker).

**Purpose.** Start a conversation ([fleet-conversations.md, Start a conversation](../../designs/fleet-conversations.md#start-a-conversation)): pick the project, pick what we are doing now, and see what the start will use before Send.

**Anatomy.**
* *Project pick:* a row of chips. The caller orders the projects: focused ones (OR-34), then recent ones, then the rest by name; ignored projects are left out. The first six show; "No project" is one more chip, never a separate button. A picked project outside the first six stays shown. With more projects, a search icon (OR-31) finds the rest and `+K` counts what is not shown (OR-9, OR-30).
* *Preset picker:* one card per preset, in a grid that wraps. A card has the preset's icon (system presets only) and name, its description, and a line of what it sets in words: agent (harness, model, effort), "Read only" or "Can edit", "New branch" or "No branch", "Starter instruction". Never a raw profile id alone. A preset that cannot start here stays visible, disabled, and its line says why.
* *Start facts:* chips under the composer: the project, the agent (harness mark and `model · effort`; the tooltip names the profile and whether it is the project's role or a system profile), access (shield) and branch (`<prefix>/new` or "No branch"; the tooltip names the base). Access and branch are buttons: a click flips them for this start.

**Behaviour.** Picking a project loads its presets (`session_presets`); the first available preset is picked. Picking another preset resets the flipped access and branch. Outside projects there is no branch chip.

**Keyboard.** `Tab` to each chip and card; `Enter` or `Space` picks or flips.

**Accessibility.** Each part is a named group ("Project", "Preset", "Start settings"); chips and cards expose `aria-pressed`, a disabled card `aria-disabled` with its reason as tooltip. Touch targets follow the chip and button sizes.

**Used in.** Conversations, `/conversations/new` ([page](../pages/conversations.md)).
