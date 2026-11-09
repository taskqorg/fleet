# Project settings matrix

2026-11-09. Research only, read from source code of `~/Projects/orca` and `~/Projects/t3code` and the Fleet worktree. Basis: [fleet.md](../fleet.md), [first-version design](first-version-design.md), [architecture breadth plan](architecture-breadth-plan.md), [project setup and access](project-setup-and-access.md). Goal: agents work automatically; projects differ only in settings. No limits or gates are proposed.

## Config files and locations

| Tool | Shared (in repo, committed) | Local (per user/host) |
|---|---|---|
| Orca | `orca.yaml` at worktree root (`src/main/workspace-hooks/hooks.ts` `orcaYamlPath`, parser `src/shared/orca-yaml.ts`). Recognized keys: `scripts` (`setup`, `archive`), `setupAgentStartupPolicy`, `issueCommand`, `defaultTabs`, `environmentRecipes`, `worktree.sharedDirectories`. Read from the *new worktree*, so a branch can change it. Content-hash trust approval before first run (`PersistedTrustedOrcaHooks`, `src/shared/orca-yaml-hook-types.ts`) | `Repo` record in Orca store (`src/shared/repo-types.ts`): `hookSettings`, `worktreeBaseRef`, `worktreeBasePath`, `symlinkPaths`, `gitUsername`, `ghAccount`, `forkSyncMode`, `sourceControlAi`, `connectionId`/`executionHostId`. Global settings `src/shared/default-global-settings.ts` |
| t3code | `t3.json` at project root (`packages/contracts/src/t3ProjectFile.ts`, schema `https://t3.codes/schema/t3.json`): `iconPath`, `defaultThreadEnvMode`, `worktreeSubmodules`, `scripts[]` | `<stateDir>/settings.json` (`apps/server/src/config.ts`) with global settings and `projectSettingsOverrides[projectId]` (`ProjectSettingsOverrides`, `packages/contracts/src/settings.ts`): absent key inherits global, `null` is a value |
| Fleet now | `config/projects/<id>/project.json` (`id`, `name`, `capability`, `recommendedMethod`, `contextFiles`); profiles `config/agents/<id>.json` (`harness`, `model`, `effort`, `instructions`, `mcpServers`, `settings.sandbox/mode`) | Executor env `FLEET_PROJECT_ROOTS` (`crates/executor/src/main.rs` `project_roots`) |

## Matrix

"Need" = yes (V1, needed for automatic agent work), later, no. Copy source = where to take logic from.

| Setting | Orca | t3code | Fleet now | Need | Copy from |
|---|---|---|---|---|---|
| Worktree on/off per project | Always worktree per workspace | `defaultThreadEnvMode` `local`\|`worktree`; built-in default `local`; `t3.json` < project override | Worktree if `workingDirectory` is a git repo under `FLEET_PROJECT_ROOTS` (`worktree()`, main.rs:86) | yes: some projects (docs, research) need no worktree | t3 `defaultThreadEnvMode` |
| Worktree location | global `workspaceDir`, `nestWorkspaces: true`; per repo `worktreeBasePath` (relative to repo) | global `worktreesDir` = `<baseDir>/worktrees/<repo>/<name>` (`GitVcsDriverCore.ts` `createWorktree`) | `<sessionDir>/worktree` | no: session dir is right for Fleet (owned working area) | — |
| Base branch / ref | per repo `worktreeBaseRef`; default = repo default branch (`defaultBaseRef`); `refreshLocalBaseRefOnWorktreeCreate: false` | `newWorktreesStartFromOrigin: true` (start from `origin/<base>` after fetch) | Current `HEAD` of the checkout; no fetch | yes: agents must start from fresh `origin/<default>` | t3 `newWorktreesStartFromOrigin` + Orca `worktreeBaseRef` |
| Branch naming | global `branchPrefix: 'git-username'` (+`branchPrefixCustom`), `autoRenameBranchFromWork: true`; `gitUsername` per repo | `branchNamingMode` `static`\|`semantic`\|`custom` (default `static`), `branchNamePrefix` default `t3`, `branchNameInstructions` | Fixed `fleet/<session>` | later: prefix per project is enough; semantic rename is cosmetic | t3 `branchNamePrefix` |
| Submodules | — | `worktreeSubmodules` `recursive`\|`top-level`\|`none` (built-in `recursive`) | none | later: only when a project has submodules | t3 |
| Setup script | `orca.yaml` `scripts.setup`, runs after worktree create, env `ORCA_ROOT_PATH`, `ORCA_WORKTREE_PATH`, `ORCA_WORKSPACE_NAME` (`setup-hook-env-vars.ts`); timeout 120 s for in-process hooks (`hooks.ts` `HOOK_TIMEOUT`); also local `hookSettings.scripts.setup` | `scripts[].runOnWorktreeCreate: true` (first such script; `packages/shared/src/projectScripts.ts`), runs in worktree with `NO_COLOR=1` (`apps/server/src/project/ProjectSetupScriptRunner.ts`) | none | yes: build/install before the agent works | t3 script entry + Orca env vars |
| Setup run policy | `hookSettings.setupRunPolicy` `ask`\|`run-by-default`\|`skip-by-default` (default `run-by-default`); per create `--setup run\|skip\|inherit` (default `inherit`) (`effective-hook-config.ts` `shouldRunSetupForCreate`) | always runs if defined | — | yes, as per-start override `setup: run\|skip\|inherit`; `ask` not needed (no human in loop) | Orca `SetupDecision` |
| Agent start vs setup | `setupAgentStartupPolicy` `start-immediately`\|`wait-for-setup` (default `start-immediately`) | `scripts[].async` (default `true` = start immediately; `false` = wait) | — | yes: default **wait** for automatic agents is a project choice; keep both values | Orca enum names |
| Shared vs local script source | `commandSourcePolicy` `shared-only`\|`local-only`\|`run-both` (default `shared-only`, or `local-only` if a local script exists) (`hook-command-source-policy.ts`) | `t3.json` scripts imported into project settings (`defaultProjectScripts`) | — | later: Fleet config is in Git already; one source | — |
| Script trust | Content-hash approval per repo (`orca-hook-trust-entries.ts`) | — | Executor git disables hooks | no: Fleet project config is owner-authored in Git; trust = repo access | — |
| Teardown hook | `scripts.archive` before worktree removal; failure blocks removal unless explicit waiver (`runtime-registered-local-worktree-removal.ts`, `worktree-archive-hook-gate.ts`) | `scripts[].runOnSettle` (runs in thread worktree each time it settles), `stopOnSettle` (kill script terminal on settle) | none; worktree never removed | yes: cleanup of ports/containers/build output | Orca `archive` (before remove) + t3 `stopOnSettle` |
| Worktree removal / retention | manual delete; `skipDeleteWorktreeConfirm`, `alwaysForceDeleteWorktrees`, `alwaysForceDeletePreservedBranches` (`worktree-deletion-preferences.ts`) | `worktreeCleanup` `null`\|`{mode:off}`\|`{mode:custom, rules}`; `worktreeKeepWhen` default `uncommitted-changes`, `worktreeAfterDays`, `worktreeSettledAfterDays` | none | yes: remove after delivery; keep if uncommitted changes | t3 `worktreeCleanup` rules |
| Branch cleanup | After worktree removal `git branch -d` (safe), force only on failed creation; also deletes if already merged into target (`worktree-branch-removal.ts`) | `sidebarAutoSettleOnMerge: true` (settles thread on merge) | none | yes | Orca `deleteBranchAfterWorktreeRemoval` |
| Run/dev scripts | `defaultTabs[]` (`title`, `color`, `command`) created once per new worktree | `scripts[]` (`name`, `command`, `icon`, `previewUrl`, `autoOpenPreview`); example `t3code/t3.json` `Dev` | none | later: agents run commands themselves; preview needs `previewUrl` later | t3 `scripts[]` |
| Issue command | `issueCommand` (shared default for linked issues) | — | — | no | — |
| Ports | none as setting (only localhost labels) | none as setting (`previewUrl` is a fixed URL) | none | later: needed only for parallel dev servers; neither tool proves a field | — |
| Env files / copy ignored files | `symlinkPaths[]`: APFS clone on macOS, else symlink, into new worktrees (absent = none); `worktree.sharedDirectories[]` in `orca.yaml` shares rebuildable dirs (e.g. `node_modules`) | none (setup script does it, e.g. `scripts/setup-worktree.ts`) | none | yes: `.env`, caches; but secrets stay per environment, see [project setup](project-setup-and-access.md) | Orca `symlinkPaths` + `sharedDirectories` |
| Sandbox environments | `environmentRecipes[]` (`id`, `name`, `create`, `suspend`, `resume`, `destroy`, `checkoutMode`) | — | environments = hosts/OS users | later | Orca recipes when VMs are needed |
| Merge method | per call `merge`\|`squash`\|`rebase`, default `squash` (`src/main/github/client/merge/merge-pr.ts`); auto-merge via `gh pr merge --auto --<method>`, falls back to enqueue if repo forbids auto-merge (`pr-auto-merge.ts`) | `pullRequestMergeMethod` `merge`\|`squash`\|`rebase`\|`null` (null = repo default) per project | delivery pushes `HEAD:main` directly (`delivery_worker.py:113`) | yes | t3 key, Orca auto-merge logic |
| Integration mode (PR vs direct) | PR via `gh`; no direct-merge setting | PR actions; no direct-merge setting | direct push only | yes: `pr` \| `direct` per repo; neither tool has `direct`, Fleet delivery already proves it | Fleet `delivery_worker.py` + Orca PR flow |
| Conflict handling | UI/agent resolves; no policy key | no policy key | none | later: agent resolves in its worktree; no key proven | — |
| Sync main checkout after merge | `forkSyncMode` (fork default branch sync offer); `refreshLocalBaseRefOnWorktreeCreate` | `defaultAutoPull: false` (auto-pull checkout; `VcsStatusBroadcaster.ts` `VcsAutoPullPolicy`) | none | yes: next worktree must see merged work | t3 `defaultAutoPull` |
| Commit message hygiene | `sourceControlAi` per repo (agent, model, instructions per operation) | `removeAgentCreditsOnMerge: false`, `sourceControlWritingStyle`, `sourceControlWriterModelSelection` | none | later | t3 |
| Default agent/model | global `defaultTuiAgent: null`, model per agent; per repo only `sourceControlAi` | `defaultModelSelection` per project (default `null`) | profile `model`/`effort` (`config/agents/*.json`); fallback `gpt-6.1-sol`/`low` (`agent-tools.ts:28`) | yes: project role → profile id | Fleet profiles (already) |
| Permission / approval mode | global `agentPermissionDefault: 'full-access'`, `agentPermissionOverrides` per agent | `defaultRuntimeMode` `approval-required`\|`auto-accept-edits`\|`auto`\|`full-access`, default `full-access`, per project | profile `settings.sandbox`, `settings.mode` | yes, in profile; project may override | t3 `RuntimeMode` enum |
| Instructions / skills / MCP | global agent args/env; `agentSkillSharingEnabled: false` | `threadTitleInstructions`, `reviewGuideInstructions`; `providerInstances[id].enabled` per project | profile `instructions`, `mcpServers`; project `contextFiles` | yes (exists) | Fleet profile |
| Browser/device access | — | `enableAgentBrowserAccess: true`, `enableAgentDeviceAccess: false` | — | later (computer use) | t3 |
| Git identity | user's git config; `gitUsername` used for branch prefix | user's git config | hard-coded `Fleet delivery <fleet@ufobe.com>`; agent worktrees have none (global config `/dev/null`) | yes: per project `name`/`email` | Fleet executor env `GIT_AUTHOR_*` |
| Forge credentials | `ghAccount` binding `(host, user)`, token resolved at spawn into child env only | host git/SSH, non-interactive | ambient on host | yes: connection binding, see [project setup](project-setup-and-access.md) | Orca `ghAccount` |
| Execution host | `connectionId` / `executionHostId` (`local`, `ssh:*`, `runtime:*`) | per environment | host chosen per start | yes: allowed environments per project | — |
| File watching | `orca.yaml` change re-check (`repo-hooks-check-wake.ts`); worktree status rollups | VCS status broadcaster with auto-pull | none | no: coordinator re-reads Git config per start | — |

## Proposed minimal Fleet project schema

Only fields proven in Orca, t3code or the Fleet prototype. Lives in `config/projects/<id>/project.json` (Git), next to the existing fields. Absent field = built-in default shown.

```json
{
  "id": "hello-world",
  "name": "Hello World",
  "contextFiles": ["rules.md"],
  "roles": { "worker": "claude-worker", "reviewer": "claude-reviewer" },
  "repositories": [{
    "id": "main",
    "remote": "git@github.com:taskqorg/fleet-hello-world.git",
    "connection": "github-taskqorg",
    "envMode": "worktree",
    "baseRef": null,
    "startFromOrigin": true,
    "branchPrefix": "fleet",
    "linkPaths": [],
    "sharedDirectories": [],
    "scripts": {
      "setup": null,
      "setupStart": "wait-for-setup",
      "teardown": null
    },
    "integration": {
      "mode": "pr",
      "mergeMethod": null,
      "autoMerge": false,
      "syncCheckoutAfterMerge": true,
      "deleteBranch": true
    },
    "cleanup": { "keepWhen": "uncommitted-changes" },
    "identity": { "name": "Fleet agent", "email": "fleet@ufobe.com" }
  }]
}
```

Field sources: `envMode` = t3 `defaultThreadEnvMode`; `baseRef` = Orca `worktreeBaseRef` (null = default branch); `startFromOrigin` = t3 `newWorktreesStartFromOrigin`; `branchPrefix` = t3 `branchNamePrefix` (branch `<prefix>/<session>`); `linkPaths` = Orca `symlinkPaths`; `sharedDirectories` = Orca `worktree.sharedDirectories`; `scripts.setup`/`teardown` = Orca `scripts.setup`/`archive` (env `FLEET_ROOT_PATH`, `FLEET_WORKTREE_PATH`, `FLEET_SESSION_ID`, like `ORCA_*`); `setupStart` = Orca `setupAgentStartupPolicy` (default changed to `wait-for-setup` because agents start unattended; owner to confirm); per-start override `setup: run|skip|inherit` = Orca `SetupDecision`; `integration.mode` `pr|direct` = Orca PR flow / Fleet delivery direct push; `mergeMethod` = t3 `pullRequestMergeMethod` (null = repo default); `autoMerge` = Orca `gh pr merge --auto`; `syncCheckoutAfterMerge` = t3 `defaultAutoPull`; `deleteBranch` = Orca safe `git branch -d` after removal; `cleanup.keepWhen` = t3 `worktreeKeepWhen`; `identity` replaces the hard-coded delivery identity. Harness defaults stay in profiles via `roles`; project-level `runtimeMode` override (t3 `defaultRuntimeMode`) only if a project needs one.

Not included (no need yet): ports, dev/run scripts, submodules, issue command, VM recipes, script trust, commit-writing AI, file watching.

## Owner questions

1. `setupStart` default: `wait-for-setup` (safe for unattended agents) or Orca/t3 default `start-immediately`?
2. Default `integration.mode` for new projects: `pr` or `direct`?
3. Who merges a PR in `pr` mode: auto-merge, reviewer agent, or owner?
4. Should the coordinator, not the executor, own `syncCheckoutAfterMerge` (one checkout per environment)?
5. Teardown hook failure: block worktree removal (Orca) or record and remove?
