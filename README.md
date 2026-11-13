# Fleet Blueprint

## System Overview

Fleet coordinates agent work across projects and hosts. The [system specification](fleet.md) owns product rules; [AGENTS.md](AGENTS.md) names the pinned shared rules and task-entry requirements. These directory contracts map implemented boundaries and side effects; they do not replace product specifications.

The Preact app calls coordinator operations over HTTP and consumes its change feed. The coordinator owns shared work state in PostgreSQL, queries ClickHouse telemetry, coordinates object transfers and dispatches commands to host executors. Executors run harnesses and host jobs and send durable receipts. The gateway handles provider calls and the Access origin guard; the updater verifies and installs published releases. See each child contract for the actual interfaces and limits.

## Subdirectories & System Boundaries

| Directory | Boundary |
|---|---|
| [app](app/README.md) | Browser app, routes, shared operation client and live/offline state. |
| [go](go/README.md) | Coordinator, executor, gateway, updater, contract generation and telemetry library. |
| [contracts](contracts/README.md) | Shared JSON schemas and TypeScript generation. |
| [packages](packages/README.md) | UI controls, component book and shared view URL grammar. |
| [scripts](scripts/README.md) | CI selection, documentation reports, Lab imports, key administration and delivery observation. |
| [deploy](deploy/README.md) | Release construction, installation and host infrastructure inputs. |
| [config](config/README.md) | Shipped profiles, project context, catalogue and pipeline definitions. |
| [docs](docs/README.md) | Current product and architecture topic index, including the design system. |
| [tests](tests/README.md) | Verification harnesses and outside/browser/native checks. |

## Global Invariants

- Shared operations, data and permissions serve people and agents; interfaces do not add independent work-state mutations ([fleet.md](fleet.md), R2).
- [contracts](contracts/README.md) owns wire shapes; Go generation is committed and TypeScript generation is build output. Changes must reach every consumer.
- Task, execution and session lifecycles remain distinct. Agent completion alone does not establish task acceptance; source implementations are mapped in [coordinator](go/coordinator/README.md).
- Access values remain host-private. Installed state and live data completeness require observations; checked-in service files and a documented metric do not prove either.
- Maintain the affected directory contracts when file responsibilities, interfaces, invariants or side effects change, then reconcile parent boundary summaries. Existing design rules keep their canonical homes under the [docs index](docs/README.md).

## Global Side Effects

Running Fleet can change PostgreSQL work state, emit ClickHouse spans, transfer objects, create local work areas, launch and stop agent/tool processes, call external providers and publish project outputs within granted authority. Release and host administration tools can replace executables and restart services. Reading this blueprint or generating documentation performs none of those operations.

Working briefs and review reports stay in ignored `.egm/`; they are never committed. Persistent rules and package contracts stay in their owning files ([documentation governance](docs/designs/fleet-docs-governance.md)).

## System Verification

For documentation changes: `git diff --check` and `node scripts/docs-report.ts --base origin/main`, plus link/source review of changed contracts. The docs report warns and does not fail the build.

[package.json](package.json) exposes `pnpm run build`, `pnpm test`, `pnpm run check:design` and focused build commands. [fleet.md](fleet.md) defines the exact merge gate and local-test restriction: the full gate runs in CI, never on the owner's Mac. A documentation bootstrap does not qualify production services, native harnesses or deployment.
