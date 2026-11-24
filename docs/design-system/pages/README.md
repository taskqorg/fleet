# Pages

Page specs map [inventory](../inventory.md) items to patterns and controls. The mockups are one self-contained file built from `packages/ui` only, with synthetic data: [mockups.html](mockups.html), built by `npm run book`. Routes: `#/overview`, `#/tasks`, `#/tasks/9989`, `#/results`, `#/hosts`, `#/laboratory`.

The target interface is one end-to-end variant over the seven rail sections ([information architecture](../../designs/fleet-ia-and-journeys.md#1-closed-information-architecture)), drawn on the [design board](../README.md#design-board) and tied together by [screens/flow.bl](../board/screens/flow.bl): the journeys J1–J5, answering a question from Home and waking a bot, as click paths across the screens.

| Rail section | Target spec | Board | Earlier specs it absorbs |
|---|---|---|---|
| Home | [home.md](home.md) | [home](../board/screens/home.bl) | [overview.md](overview.md) (with Live) |
| Live | [live.md](live.md) | [live](../board/screens/live.bl) | [overview.md](overview.md), parts of [hosts.md](hosts.md), [runners.md](runners.md), [delivery.md](delivery.md) |
| (every section) | [session.md](session.md): the one session view | [session](../board/screens/session.bl) | the conversation page of [conversations.md](conversations.md) |
| Conversations | [conversations.md](conversations.md#target) | [conversations](../board/screens/conversations.bl) | |
| Projects | [projects.md](projects.md#target) | [projects](../board/screens/projects.bl) | facets [tasks.md](tasks.md), [delivery.md](delivery.md), [results.md](results.md) |
| Lab | [laboratory.md](laboratory.md#target) | [lab](../board/screens/lab.bl) | |
| Library | [library.md](library.md) | [library](../board/screens/library.bl) | roots [catalogue.md](catalogue.md), [agents.md](agents.md), [pipelines.md](pipelines.md), [changes.md](changes.md) |
| System | [system.md](system.md) | [system](../board/screens/system.bl) | roots [hosts.md](hosts.md), [runners.md](runners.md), [spend.md](spend.md) |

The earlier specs keep the content of each screen as it runs today; each names its target home in its first lines.

| Page today | Workspace | Spec |
|---|---|---|
| Overview | Operator | [overview.md](overview.md) (until Home and Live are built) |
| Tasks (list and detail) | Operator | [tasks.md](tasks.md) |
| Results | Operator | [results.md](results.md) |
| Hosts, environments and stores | Architect | [hosts.md](hosts.md) |
| Runners | Architect | [runners.md](runners.md) (app only) |
| Conversations | Operator | [conversations.md](conversations.md) (app only) |
| Delivery | Operator | [delivery.md](delivery.md) (app only) |
| Laboratory | Architect | [laboratory.md](laboratory.md) |
| Agents | Architect | [agents.md](agents.md) (mockup only) |
| Pipelines | Architect | [pipelines.md](pipelines.md) (mockup only) |
| Catalogue | Architect | [catalogue.md](catalogue.md) (mockup only) |
| Changes | Architect | [changes.md](changes.md) (design canvas only) |
| Spend | Architect | [spend.md](spend.md) (design canvas only) |

The operator app (`app/`, [design](../../designs/fleet-operator-app.md)) builds the Overview, Tasks, Conversations and Hosts pages for real. `pnpm run check:design` runs `tests/ui/app.check.ts` on it.
