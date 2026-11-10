# Lab

The Fleet Lab runs experiments: pipelines over example sets, compared across variants and scored by evaluators, with results promoted into project recommendations. The design, its decisions and its build stages are in [designs/fleet-lab.md](designs/fleet-lab.md). The old lab (two-profile comparisons at `/legacy/lab`) was removed in Stage 0; `/lab` shows "Lab is being rebuilt" until Stage 3.

Stage 1 (registry and examples): use cases are indexed in `config/lab/use-cases/` (`pnpm exec tsx scripts/lab-drift.ts` reports changed sources); examples are lab artifacts imported through `POST /api/lab/examples` from the seeds in `config/lab/examples/` (`pnpm exec tsx scripts/lab-examples.ts check|import`) and from assetq datasets (`scripts/lab-import-assetq.ts`).
