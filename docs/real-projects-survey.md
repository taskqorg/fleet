# Real projects survey

2026-11-09. Read-only survey of the owner's repositories under `~/Projects` and `~/orca/workspaces`. It grounds the [project settings matrix](project-settings-matrix.md) and the [decisions](project-settings-decisions.md) in real workflows. Secret files are only noted as present. Sources: repository listings, `AGENTS.md`, `taskq.json`, `.github/workflows`, `.gitlab-ci.yml`, `package.json`, `.claude/`, `.mcp*.json`, and recent `git log`.

## Per-project table

| Project | Stack | Checks / dev | Setup needs | Git flow and CI | Deploy | Agent files, harness, MCP |
|---|---|---|---|---|---|---|
| agentq | Rust (Cargo) + Python scripts | `cargo test --locked`, `cargo run --locked -- selftest`, specq `contract_lint.py agentq.md` | none beyond toolchain; pinned specq commit in CI | GitHub `taskqorg/agentq`; branch `alexkirs/*` + PR squash (`(#358)`); workflows `test`, `release`, `runtime-tag`, `pin-update` (bot PRs, `--auto` merge) | GitHub release, runtime tag | `AGENTS.md` (links specq), `.claude/settings.local.json`, `.worktrees/`, `taskq.json` (`publish: pr`, limits claude/codex) |
| assetq (pipe) | Rust + web + Python gate | `python3 scripts/check.py` | toolchain; systemd units in `deploy/read-lane` | GitHub PR squash; `tests`, `pin-update` | systemd service on server (`/opt/pipe-read`) | `AGENTS.md`, `pipe.md`, `.specq/` copy, `taskq.json` `publish: pr` |
| coreq | Rust, `build.rs`, instrument JS | `cargo test`, contract lint | toolchain | GitHub PR squash; `tests`, `tag` | tagged library release | `AGENTS.md`, `coreq.md`, `taskq.json` `publish: pr` |
| dashq | Rust | `cargo test --locked`, `cargo run -- selftest`, contract lint; affected named tests during dev | fixtures in repo | GitHub PR squash; `tests`, `release`, `pin-update`, `taskq-assets` | release artifacts | `AGENTS.md` (testing run policy), `taskq.json` `publish: pr` |
| pilotq | Rust | `cargo test`, contract lint | **git submodule** `specq` | GitHub PR squash; `tests`, `release`, `pin-update` | release artifacts | `AGENTS.md`, `taskq.json` `publish: pr` |
| serverq | Rust + Python checks | `serverq.md § 10`; `cargo test --locked`, contract lint | `.specq/` copy | GitHub PR, some merge commits; `check`, `release` | release; consumed by servers | `AGENTS.md` ("communicate in Russian"), `taskq.json` `publish: pr` |
| specq | Markdown rules + Python lint | `check.yml` lint | none | GitHub PR squash | none (consumed by pin) | `AGENTS.md`; docs-only, no build |
| taskq | Rust + Node web build (`npm run build`, prettier) | `tests.yml`, `shots.yml` (screenshots) | `node_modules`, `.local/` state | GitHub PR squash, many `alexkirs/direct-*` branches; `release-build`, `pages`, `cache` | GitHub Pages, packaged releases | `AGENTS.md`, `CLAUDE.md`, `.codex/`, `.claude/` |
| cs (cyprus.bz) | React Router + Vite + Bun/Node, Supabase, Docker, Playwright | `npm run dev` via `scripts/worktree-launch.sh` (per-worktree env and **port check**), `smoke:*`, Playwright configs | `.env`, `.env.local` (secret, present), `.env.*.example`, `.toolchain.env`, `.certs`, `.auth`, `node_modules`, Supabase, docker-compose services | **GitLab** `gradus/cyprusbz`; `taskq.json` `publish: direct`; `main` = DEV, `live` = production; hotfix backmerge commits; `.gitlab-ci.yml` single pipeline with environment guards | DEV on push to `main`; LIVE only via `npm run release` on explicit owner request; nginx + systemd (`infra/`) | `AGENTS.md`, `CLAUDE.md`, rich `.claude/` (agents `researcher`/`reviewer`/`tester`, skills `deploy-watch`, `fix-and-ship`, `db-migration`, `edge-deploy`, ..., hooks, commands), MCP `context7`, `jcodemunch`, `playwright`; Docker harness wrappers `claude-docker.sh`, `claude-local.sh` |
| pah | PHP Yii2, Composer, Docker, Python AI trainer | `./yii migrate`, `./yii fixture "*"`, docker compose | `vendor/`, DB, DEV Basic Auth identity (credential file on host) | GitLab, branch `fix/*`/`feature/*` + MR merge commits into `master` | `.gitlab-ci.yml` `deploy_dev` with `resource_group` | `AGENTS.md`, `.claude/`, `.hermes/`, `claude-project.sh`/`codex-project.sh` |
| abuse-bot | Node/TS, Vite frontend, Drizzle, MySQL, pm2 | `npm run dev` (ports 3001 + vite), `vitest run`, `npm run build` | `.env` (secret, present), `.env.example`, MySQL, two `npm install` (postinstall frontend) | GitHub, direct commits to `main` | server pull: `deploy.sh` fetches `origin/main`, `reset --hard`, `deploy-after.sh` | `AGENTS.md`, `.claude/`, `.hermes/`, `opencode.json`, MCP `context7`, `mysql`; Docker wrappers for claude/codex/opencode |
| kirs.online | static HTML/CSS | `tests/` | none | GitHub, direct to `main` | GitHub Pages (`CNAME`) | `taskq.toml`, `.claude/` |
| lbg | legacy PHP site | none | `config.php` from sample | direct commits to `master`, no CI | manual server | `.claude/` only |
| orca, t3code | TS monorepos (pnpm) | many CI workflows; `orca.yaml` `scripts.setup` = `pnpm install`; `t3.json` scripts | `node_modules`, `.env.example` (t3) | upstream PR flow | desktop/mobile releases | reference sources, not owner products |

`~/orca/workspaces/<project>/<name>` are Orca worktrees of the same repos (for example `fleet/goblin`, `taskq/albatross`, `agentq/glaucus`); every repo also keeps `.worktrees/` and `.claude/worktrees/` for harness worktrees.

## Common needs

1. **Shared rules context.** Every taskqorg repo binds specq from `AGENTS.md`, and the decision document `<project>.md` is read first. Pinned specq comes as a commit pin, `.specq/` copy, or submodule.
2. **Worktree per task**, branch `alexkirs/<topic>`, from fresh `origin/main`.
3. **Setup:** Cargo toolchain only (taskqorg Rust repos); `npm install` twice plus env files and local services (cs, abuse-bot); `composer` plus DB plus Docker (pah).
4. **Secrets per environment:** `.env`, `.env.local`, `.certs`, `.auth`, and host credential files. Repos ship `*.example` templates.
5. **Ports:** cs checks ports per worktree; abuse-bot hardcodes 3001. Parallel dev servers need distinct ports.
6. **Checks:** one canonical gate per repo (`cargo test --locked` + `selftest` + contract lint, `scripts/check.py`, `vitest run`, smoke scripts). Dev runs affected tests; the full gate runs on the frozen candidate.
7. **Integration:** two real modes. `pr` + squash on GitHub (all taskqorg); `direct` push (cs to GitLab `main`, abuse-bot, kirs.online, lbg). GitLab MR with merge commits (pah).
8. **Bot dependency PRs:** `pin-update.yml` opens PRs and auto-merges them after checks (agentq); "manual pin updates pending review" in dashq/pilotq.
9. **Deploy:** varies by project: release tags and artifacts (Rust), push-to-branch environment (cs `main` DEV, `live` PROD), server pull + reset (abuse-bot), CI deploy job with resource group (pah), GitHub Pages.
10. **Production gate:** cs LIVE needs explicit owner request in the current conversation.
11. **Harnesses:** Claude Code everywhere; Codex (taskq.json limits, `.codex/`, `codex-*.sh`); OpenCode and Hermes in abuse-bot and pah; Docker-wrapped harness runs in cs and abuse-bot.
12. **Project skills, subagents, MCP:** cs has the full set (skills, agents, hooks, commands, 3 MCP servers); abuse-bot has `context7` + `mysql`.
13. **Board:** `taskq.json` names a board (`github` or `gitlab`), repo, `publish` mode and per-harness limits.
14. **Docs-only repos** (specq) need no build and no services.

## Recurring scenarios

| Scenario | Seen in | Needs |
|---|---|---|
| Interactive dev session | cs, abuse-bot, pah | worktree, setup, env files, dev server with free port, browser/Playwright, preview URL |
| Automatic pipeline (task to merged PR) | taskqorg repos via taskq | worktree from origin, canonical gate, PR, squash, auto-merge on green checks |
| Bot maintenance PR | `pin-update.yml` in 5 repos | scheduled trigger, PR, auto-merge or "pending review" per repo |
| Lab / qualification experiment | assetq `.qualification-*`, `qualification-4-pm-*` dirs, Fleet lab docs | isolated copies, evidence retained, no integration |
| Review | cs `reviewer` agent, specq rules | reviewer role on the PR or diff |
| Deploy to DEV | cs `main`, pah `deploy_dev`, abuse-bot pull | trigger after integration; watch result (`deploy-watch` skill) |
| Release to production | cs `npm run release`, Rust `release.yml`, tags | explicit human request; separate from integration |
| Hotfix + backmerge | cs `live` to `main` | branch from `live`, merge back to `main` |
| Docs / rules change | specq, Fleet docs | no setup, PR only |
| Research | Fleet, `~/Projects/research`, md notes | no worktree, no integration |

## Mapping to project settings and attention items

| Need | Schema field ([matrix](project-settings-matrix.md)) | Attention item |
|---|---|---|
| specq + `<project>.md` context | `contextFiles`; profile `instructions` | — |
| worktree from fresh main | `envMode: worktree`, `startFromOrigin: true`, `baseRef` | fetch failed |
| no worktree for docs/research | `envMode: local` | — |
| branch `alexkirs/*` | `branchPrefix` | — |
| `npm install`, composer, `cargo fetch` | `scripts.setup`, `setupStart: wait` | setup failed |
| `.env`, `.certs`, `.auth` | `linkPaths` (per environment, never in Git) | missing link source, expired credential |
| `node_modules`, `vendor`, `target` | `sharedDirectories` | — |
| submodule `specq` (pilotq) | not in schema (dropped as "later") | — |
| PR + squash vs direct | `integration.mode`, `mergeMethod` | merge conflict, checks failed |
| auto-merge after checks | `integration.autoMerge` | checks failed or timed out |
| GitHub vs GitLab | `remote`, `connection` | auth expired |
| sync host checkout | executor fast-forward on *base updated* | checkout dirty |
| DB/Docker cleanup | `scripts.teardown` | teardown failed |
| harness role, MCP, skills | `roles` to profiles (`mcpServers`, `instructions`) | — |
| commit identity | `identity` | — |

## Gaps in the schema

1. **Submodules.** pilotq uses one. Add t3 `worktreeSubmodules` now, not later.
2. **Ports.** cs and abuse-bot run dev servers per worktree. Need a port allocation or `FLEET_PORT_BASE` env for setup and dev scripts, like cs `worktree-runtime.mjs`.
3. **Check command.** Each repo has one canonical gate; the schema has no `checks` field. Add `scripts.check` (gate for the frozen candidate) so pipelines and reviewers run the same command.
4. **Deploy and environments.** cs maps branch to environment (`main` DEV, `live` PROD), pah deploys from CI, abuse-bot pulls. Add `delivery.environments[]` with `branch` or `command`, and `requiresHumanRequest` for production. The decision "accept and publish separately" in [fleet.md](../fleet.md) needs this field.
5. **Forge type.** GitLab MR (cs, pah) versus GitHub PR. `connection` must carry forge kind; `integration.mode: pr` must work for GitLab MRs.
6. **Merge method `merge`.** pah uses merge commits; already allowed by `mergeMethod`, but default "repo default" must be read from GitLab too.
7. **Hotfix base.** cs hotfixes branch from `live`. Per-start `baseRef` override is needed, not only a project default.
8. **Harness wrapper / container.** cs and abuse-bot run harnesses in Docker (`claude-docker.sh`). Maps to Orca `environmentRecipes`; keep as later, but record as a real need.
9. **Board binding.** `taskq.json` `board`/`repo` and per-harness `limits` already exist per project; Fleet should import `board` and `publish` as `connection` and `integration.mode`. Per-harness limits belong to resources, not project settings.
10. **Bot PR policy.** Auto-merge differs per PR source (bot pin updates versus agent work). `autoMerge` may need a per-source value.

## Representative test projects

1. **dashq** (Rust, GitHub, PR + squash + auto-merge, pin bot). Typical taskqorg repo: toolchain-only setup, one gate, releases. Tests the automatic pipeline end to end with low setup risk.
2. **cs** (cyprus.bz: TS web app, GitLab, `direct`, env files, ports, services, DEV/LIVE deploy, rich skills and MCP). Hardest real case; tests setup, `linkPaths`, ports, GitLab forge, deploy environments and the production gate.
3. **specq** (docs-only, PR). Tests `envMode: local` or minimal worktree with no setup, review role, and a downstream pin bump in consumers.

pilotq is the fallback for submodule coverage; abuse-bot for the server-pull deploy and MySQL MCP.

## Recommended defaults

**Setup**
- `envMode: worktree`, `startFromOrigin: true`, `branchPrefix` from the owner's git username (`alexkirs`), matching every repo.
- `setupStart: wait`; `scripts.setup: null` (Rust repos need nothing); setup failure raises an attention item.
- `linkPaths` empty by default; per environment for repos that ship `.env*.example`.
- `scripts.check` required for automatic pipelines; run the full gate on the candidate, affected tests during work.

**Integration**
- Default `mode: pr` with `mergeMethod: squash`. 9 of 13 owner repos already do this.
- `mode: direct` per project when `taskq.json` says `publish: direct` (cs) or history shows direct commits (abuse-bot, kirs.online, lbg).
- `deleteBranch: true`; host checkout fast-forward on *base updated*.

**Merge and deploy**
- `autoMerge: true` after required checks, matching `pin-update.yml` practice; owner can disable per project (dashq/pilotq keep manual pin review).
- Deploy follows integration only where the repo already does that (cs `main` to DEV). Production deploy (cs `live`, releases, tags) stays a separate action that requires an explicit owner request; never auto.
