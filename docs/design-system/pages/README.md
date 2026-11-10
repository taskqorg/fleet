# Pages

Page specs map [inventory](../inventory.md) items to patterns and controls. The mockups are one self-contained file built from `packages/ui` only, with synthetic data: [mockups.html](mockups.html), built by `npm run book`. Routes: `#/overview`, `#/tasks`, `#/tasks/9989`, `#/results`, `#/hosts`, `#/laboratory`.

| Page | Workspace | Spec |
|---|---|---|
| Overview | Operator | [overview.md](overview.md) |
| Tasks (list and detail) | Operator | [tasks.md](tasks.md) |
| Results | Operator | [results.md](results.md) |
| Hosts, environments and stores | Architect | [hosts.md](hosts.md) |
| Conversations | Operator | [conversations.md](conversations.md) (app only) |
| Laboratory | Architect | [laboratory.md](laboratory.md) |
| Agents | Architect | [agents.md](agents.md) (mockup only) |
| Pipelines | Architect | [pipelines.md](pipelines.md) (mockup only) |
| Catalogue | Architect | [catalogue.md](catalogue.md) (mockup only) |

The operator app (`app/`, [design](../../designs/fleet-operator-app.md)) builds the Overview, Tasks, Conversations and Hosts pages for real. `pnpm run check:design` runs `tests/ui/app.check.ts` on it.
