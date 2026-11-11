# Fleet definition editors: coordinator operations for Agents, Pipelines and Catalogue

*   **Author:** Alex Kirs
*   **Status:** Proposed; storage decided by the owner
*   **Owner decision (2026-11-11):** the coordinator store is the single source of truth, with append-only revisions. A git mirror is only a later, optional stage. The six rules in "Failure modes and their rules" are binding.
*   **Date:** 2026-11-11 (main `23cc517`, redesign #153 merged)
*   **Depends on:** [fleet-contracts.md](fleet-contracts.md) and the quality round's wave 1 (one router, files by domain, `go/contract`, committed `schema.sql`) and wave 2 lane F (the `ops` contract and typed `op<'name'>` in the app). [fleet-people-access.md](fleet-people-access.md) roles, director and `people.json`. [fleet-lab.md](fleet-lab.md) experiments and Promote.
*   **Screens:** [D20](../design-system/decisions.md), the [definition editor](../design-system/patterns/definition-editor.md) pattern, page specs [agents](../design-system/pages/agents.md), [pipelines](../design-system/pages/pipelines.md), [catalogue](../design-system/pages/catalogue.md), mockup routes `#/agents`, `#/pipelines`, `#/pipelines/render-props`, `#/catalogue` in `docs/design-system/pages/mockups.html`, and "what each screen answers" in [formats.md](../design-system/formats.md). UI choices are decided there and are not reopened here.

--------------------------------------------------------------------------------

## Overview

The Agents, Pipelines and Catalogue screens edit **definitions**: an agent profile (`config/agents/<id>.json`), a pipeline (`config/pipelines/<id>.json`) and a capability's catalogue entry (`config/catalogue.json`). Today the coordinator only reads these files, from the `config/` directory that ships inside each release tarball (`deploy/build-release.sh` copies `config/`). Nothing reads or writes a definition through an operation, holds a draft, or keeps revisions.

This design adds those operations. Saved definitions live in the coordinator's database as append-only **revisions**, and take effect at once. The store is the one source of truth; git never is. `config/` in the release is a **seed**: a definition is imported from it only when the store has nothing for it, and after that the store wins (a changed shipped file only shows a "repository changed" note). Executions pin the revision they started with. Definitions live in Postgres, so the database backups cover them. An export to a patch for the code repository is the git path in stage 1. A one-way **mirror** to a private git repository (`taskqorg/fleet-config`) is a later, optional stage 2, designed here so stage 1 does not block it. The design also covers the operations the screens need around definitions: draft test runs, reruns, effective layers, compatibility, models, usage, per-method assessments, comparing methods in Lab, the Lab baseline delta and person names.

## The Problem

* **No write path.** The owner, the PM agent and Lab want to change agents and pipelines and have the change apply to the next execution. Today a change needs a commit, a merge, a release and a host update.
* **Lab Promote is a dead end.** `expPromote` (`go/coordinator/lab.go`) returns a JSON patch for `config/catalogue.json` that the owner must apply by hand; until a method with the variant's `configRef` is shipped, the recommendation cannot be set.
* **Partial trace of what ran.** A pipeline run already stores the parsed definition, `definitionSha256` and `pipelineId` (`pipelineStart`, `go/coordinator/pipelines.go`). It does not record a revision, nor which agent profile content each agent step resolved, and agent sessions record no profile content at all.
* **The screens need data no operation returns:** draft change counts, diffs, history, effective layers, compatibility, usage and per-method quality. The review in PR #153 lists them.

## Goals

1. Every Agents, Pipelines and Catalogue screen element in the mockups is backed by one operation in the `ops` contract, with a `<Name>Request` and `<Name>Reply` `$defs` entry written in this document.
2. Saving a definition affects the next execution that resolves it, with no release.
3. Every save is a revision with a person, a time and a message; any two revisions and a draft can be compared.
4. Two people saving from the same base cannot silently overwrite each other: the second save gets 409 and a diff.
5. A release that changes a shipped file is never lost under a newer store revision: the definition shows "repository changed" and the next save must resolve it.
6. Each definition shows whether the store is ahead of the code repository's `config/`; one click exports those as a patch. (Stage 2: every save also appears as one commit in the private config repository.)
9. A running execution never changes because someone saved: it keeps the revision it started with.
10. History says who or what made each revision: a person, an agent (with its execution), Lab (with its experiment) or the release seed.
7. Every execution records the kind, id, revision and content hash of each definition it resolved (for a pipeline run: the pipeline and the profile of every agent step).
8. A draft can run (pipeline) or open a test session (agent) without being saved; the run records the draft content and its diff from the base.

## Non-Goals

* Files in `config/agents/` that are not profiles (today `design-rules-data.json`, profile data read by `design-rules`). They stay shipped files.
* The skills editor (D20: not designed). The `kind` enum leaves room; no operation handles skills now. A skill is a directory, not one JSON file, so when it becomes a definition its revision holds rows of `(revision, path, content)` (one per file, text stored as text, not as a JSON blob), and the diff is per file in its own format. The `definition_revision` row then carries no `content` for skills; a `definition_file` table carries the files.
* Opening a pull request on the code repository. The mirror repository carries history and review; export to the code repository stays a patch.
* The mirror in stage 1. It is stage 2, optional.
* Reading anything back from the mirror repository. A commit made there by hand is never applied (one direction only; see "Mirror to the config repository").
* Three-way merge of JSON. A conflict shows both sides; the person chooses or edits.
* Branches or named variants of a definition. Lab variants (`overrides`) already cover "try another version".
* Editing project config (`config/projects/*`) or Lab use-cases through these operations.
* Field-level rights. Two roles, as in fleet-people-access.
* Writing tracked files from the coordinator. The coordinator writes its database only; git changes go through export.

## Critical User Journeys (CUJs)

### CUJ 1: Change an agent and test it

*   **Given**: the owner (architect) opens `claude-worker` on Agents; head is revision 3, synced with the repository.
*   **When**: they change the model; the app calls `definition_draft_put` on each edit; they press Test, which calls `agent_test` with a prompt; then Diff (`definition_diff` draft against 3) and Save with a message (`definition_save` base 3).
*   **Then**: revision 4 exists, by the owner, with the message; the next execution resolving `claude-worker` uses revision 4 and records `{kind: agent, id: claude-worker, revision: 4, contentSha}`; the row shows "ahead of repository"; the draft is gone.

### CUJ 2: Concurrent save

*   **Given**: the owner and the PM agent both drafted `hello-search` from revision 2.
*   **When**: the PM agent saves first (revision 3); the owner saves with base 2.
*   **Then**: the owner gets 409 `base-moved` with `head: 3`; the app shows `definition_diff` 2 to 3 and the owner's draft; the owner calls `definition_draft_put` with base 3 and saves again.

### CUJ 3: Release changes a file under a store revision

*   **Given**: `hello-search` has store revision 3 (editor), based on repository file A; a release ships file B.
*   **When**: the coordinator starts and reconciles definitions.
*   **Then**: the definition state is `repository-changed` (the same when nobody had edited it: the store wins after first boot); executions keep using revision 3; the Draft bar offers the diff 3 against B; saving requires `repoSha: B` in the request, either with B's content ("take repository") or another content ("keep mine"); export leaves the definition out until that save. No save is lost and no release is lost.

### CUJ 4: Fix a failed pipeline step and rerun

*   **Given**: run R of `judge-scale` (revision 5) failed at step `judge`; steps `fetch` and `draft` succeeded.
*   **When**: the owner edits the `judge` step, saves revision 6, presses "Rerun failed" (`pipeline_rerun` mode `failed`).
*   **Then**: a new run R2 uses revision 6, reuses outputs of `fetch` and `draft` (their step content is the same in 5 and 6), and runs `judge` and every step after it; R2 records `rerunOf: R` and the reused step ids.

### CUJ 5: Promote a Lab winner

*   **Given**: experiment E compared two methods for `web-research`; variant `guided` wins.
*   **When**: the owner presses Promote.
*   **Then**: Lab saves a catalogue revision (source `lab`, message names E) that adds or updates the method, and sets the project recommendation in the same transaction; Catalogue shows the method's assessment from E and "ahead of repository".

### CUJ 6: Mirror and export to git

*   **Given**: three saves happened today; the private config repository has one commit for each, authored by the people who saved. The three definitions are ahead of the code repository's `config/`.
*   **When**: a developer wants them in the code repository's seed, and the owner presses Export.
*   **Then**: `definition_export` returns one unified diff against the shipped files and the list of definitions it covers; the owner downloads or copies it and runs `git apply -3` (the patch carries blob `index` lines, so a three-way apply works after main has moved). After the next release ships those files, reconcile marks each definition `synced` without a new revision.

## Technical Plan (Architecture)

### Where definitions live

```
release tarball                       coordinator database (Postgres)
config/agents/*.json  ─┐   start      definition            one row per (kind, id): repo file sha and content,
config/pipelines/*.json├─ reconcile ─▶                      head revision
config/catalogue.json ─┘              definition_revision   append-only (kind, id, revision): content, parent,
                                                            repo base sha, by, director, source, message, at
                                      definition_draft      one per (kind, id, person): base revision, content

readers: profile resolve, loadPipeline, catalogue  ──▶  head revision content (never the file)
writers: definition_save, Lab Promote, PM agent tools ──▶ new revision (one transaction, base check)
git: definition_export  ──▶  unified diff of every ahead definition against the shipped file
```

* **Identity.** `kind` is `agent`, `pipeline` or `catalogue`. `id` is the file stem for agents and pipelines (`idPattern`). A catalogue method's `pipeline` value is a file name (`hello-search.json`); resolve strips `.json` to get the pipeline id. The catalogue is **one definition**, id `catalogue`, path `config/catalogue.json`, whatever capabilities it holds: one file, one revision line, one `repoSha`. Split it only when the file is split.
* **Paths.** `path` is relative to the repository root. Reconcile reads the directories the coordinator reads today: `FLEET_PROFILES_DIRECTORY` (default `config/agents`), `FLEET_PROJECT_CONFIG_DIRECTORY` (default `config`, for `catalogue.json`) and the pipeline directory under `projectConfigDir()`. With an override, `path` is still written as the default repository path, so export patches the repository.
* **Content** is the parsed JSON, validated by the kind's existing validator before it is stored: `validatePipeline` for pipelines, `contract.ParseProfile` (`go/contract/profile.go`, schema `contracts/profile.schema.json`, already taking content) for agents, the checks in `catalogue()` for the catalogue. The validators move to take content instead of a path. Content is stored canonical (keys sorted, two-space indent, trailing newline) so the diff and `contentSha` are stable and match a file written by export. The build commits the shipped files once in canonical form, and a Go test fails when a file under `config/agents`, `config/pipelines` or `config/catalogue.json` is not canonical, so the first export does not reformat whole files.
* **Classification at reconcile.** A file in the profile directory that fails `readTrustedProfile` (`contract.ParseProfile` plus the file-name check) is not a definition: it is skipped, as `listProfiles` skips it today, and listed in a start log line. A pipeline or catalogue file that fails validation keeps the last valid revision (or, with none, no definition) and raises one needs-you question with the problem, as a malformed `people.json` does.
* **Revision numbers** are integers per definition, starting at 1. The repository's file is a revision with `source: repo`. The revision id shown in the head is `<kind>/<id>@<n>`; `contentSha` (SHA-256 of the canonical content) is shown in History next to the git blob sha when the revision came from the repository.

### Mirror to the config repository

**Stage 2, optional; not built in stage 1.** A second opinion recommended keeping git out of stage 1, and the owner put the mirror after it. Stage 1 keeps only what the mirror needs later: `mirror_commit` is not added until stage 2, and export works without it.

**Decision (owner, refines the storage choice).** The store stays the source of truth: immediate effect, rights and per-project visibility under row-level security, one transaction per save, and saves keep working when GitHub is down. Every revision is copied to a private repository, `taskqorg/fleet-config`, so off-site history, diff, blame and review come from git without Fleet building them.

**Layout.** The repository mirrors `config/`: `agents/<id>.json`, `pipelines/<id>.json`, `catalogue.json`, and `skills/` when skills become definitions. Files are the canonical text of each head, so a file in the mirror equals `definition_get.text`.

**Mechanism.**

1. `definition_revision` gains `mirror_commit text` (null until pushed). A save only inserts the revision; it never touches git.
2. One mirror loop in the coordinator (an `onStart` hook, woken by the save's `NOTIFY` and every 60 s) takes unmirrored revisions in `(at, kind, id, revision)` order. It holds a working clone at `FLEET_CONFIG_MIRROR_DIRECTORY` (default `/var/lib/fleet/config-mirror`, 0700).
3. Per revision: write the file (or the file of every definition in one reconcile batch), `git commit --author "<name> <email>" -m "<message>"` with a trailer `Fleet-Revision: <kind>/<id>@<n>` (system revisions: author `Fleet <fleet@<host>>`, message `Repository <release commit>`; agent revisions: the director as author and `Fleet-Execution: <id>`), then `git push`. On success, store the commit sha in `mirror_commit`.
4. git runs with a cleared environment and no hooks, as session worktrees do (fleet-people-access §4), and `GIT_SSH_COMMAND` names the deploy key file.
5. A push failure keeps the commit locally and retries with backoff (1 min doubling to 30 min). After 15 minutes unpushed, the coordinator raises one needs-you question to the owner ("Config mirror behind by N revisions: <error>"), resolved by the next successful push.
6. A rejected non-fast-forward push means someone wrote to the mirror. The loop stops, raises the same question with "mirror repository diverged", and never merges or force-pushes on its own. The owner either force-resets the mirror from the store (`definition_mirror_reset`, architect, owner only: rebuilds the repository branch from the store's history and force-pushes it) or reverts the outside commit.
7. A fresh mirror (empty repository) is filled by replaying every revision in order, so the mirror history is the store's history.

**Credentials.** One deploy key with write access to `taskqorg/fleet-config` only, generated on PDS18 directly into a 0600 file (`FLEET_CONFIG_MIRROR_KEY_FILE`, default `~/.config/fleet/config-mirror-key`); only the public half leaves the host, added in the repository's deploy keys by the owner. No token, no user key. Without the key file the mirror is off and `definition_list` says so once per list (`mirror: off`).

**State on screen.** `DefinitionListItem` and `DefinitionGetReply` carry `mirror` (`MirrorState`: `mirrored`, `pending`, `failed`, `off`) for the head revision; History shows each revision's mirror commit sha with a link.

**Trade-offs.**

* Two copies, one master. The mirror can lag behind the store by minutes, or by as long as GitHub is down. It is never ahead and never read, so it cannot conflict; a lag is shown, not hidden.
* The mirror has no row-level security. Anyone with read access to `taskqorg/fleet-config` sees every project's definitions. Keep it private and give read access to architects only. Definitions never carry credentials (pattern rule), so the exposure is instructions and flows, not keys.
* A hand edit in the mirror is lost unless someone saves it through Fleet. Divergence stops the mirror loudly rather than merging.
* A second deploy key on PDS18 to rotate. One key, one repository, write only there.
* Git on PDS18 becomes a coordinator dependency for the mirror only. Saves and executions do not depend on it.
* Compared with exporting by hand, this costs one loop and one table column, and removes the step people forget.

### Failure modes and their rules

1. **Upgrades never clobber edits.** `config/` in the release is a seed. A definition is imported only when the store has no row for it. After first boot the store wins: a release that changes a shipped file only updates the row's repository fields, and the definition shows `repository-changed` (or `synced` if the content is equal). Taking the shipped version is an explicit save ("take repository"). This holds even when nobody edited the definition, so a release never changes behaviour without a save.
2. **Pinned revisions.** An execution resolves each definition once, when it is created, records `{kind, id, revision, contentSha}`, and every later use reads that revision by id, never the head: the start command built at claim, each turn of the session's execution, every agent step of a pipeline run and every delegation child that names a profile. A save during a run affects only executions created after it. A new turn in an existing session is a new execution and resolves the head at its creation; the session shows when its profile revision changed between turns.
3. **Skills as files.** See Non-Goals: `(revision, path, content)` rows when skills become definitions.
4. **Readable history.** `source` (`repo`, `editor`, `lab`, `agent`) is shown on each History row with its link: Lab revisions name the experiment and variant, agent revisions the execution and its director. A filter hides Lab and agent revisions.
5. **Backups.** Definitions, revisions and drafts are Postgres tables, so the coordinator database backups and restore (the recovery operations) cover them with no extra step. A restore to an older backup rolls definitions back with everything else; the seed then fills only definitions the restored store lacks.
6. **Lost database.** A fresh database imports the seed, which is the code repository's `config/` (or, with stage 2, the owner can seed from the mirror by pointing `FLEET_PROJECT_CONFIG_DIRECTORY` at a checkout of it).

### Reconcile at start

Reconcile runs once at start, in an `onStart` hook registered before the hooks that resume work (`pipelineTick`, `labReconcileRunning`), inside one `writeTxAs` under the `GLOBAL` lock. Terms: `repoSha` is the git blob SHA-1 of the file bytes (matches `git hash-object`); `repoContentSha` is the SHA-256 of the file's canonical content; the `definition` row keeps the last seen `repo_sha`, `repo_content` and `repo_content_sha`. A revision's `repo_base` is the `repo_sha` that was current when it was written.

For each definition file:

1. No `definition` row: insert it and revision 1 (`source: repo`, actor `system`, message `Repository <release commit>`, `repo_base` = `repoSha`).
2. `repoSha` equals the row's `repo_sha`: nothing.
3. Otherwise, let *ahead* = head `content_sha` differs from the row's old `repo_content_sha` (the store held something the repository did not; the source of the head does not matter). Update the row's `repo_sha`, `repo_content`, `repo_content_sha` first, then:
   * new content equals head content: nothing more (an export came back); state `synced`.
   * otherwise: head stays; state becomes `repository-changed` (failure mode 1: the store wins after first boot, whether or not it is *ahead*). *ahead* is shown in the note so the person knows whether the store held edits.

A shipped file that no longer exists sets the row's repository fields to null. If the head equals the last repository content, the definition is retired (`retired_at`; `definition_list` hides it unless asked; new executions get 404 for it). Otherwise state `repository-deleted`. A retired definition whose file returns is un-retired and shows `repository-changed` unless the content equals head. A definition created in the store has no file: state `store-only`.

**State**, computed on read, never stored. The first matching line wins:

1. `store-only`: `ever_shipped` is false and the definition is not retired.
2. `repository-deleted`: the repository fields are null and the definition is not retired.
3. `synced`: head `content_sha` equals `repo_content_sha`.
4. `repository-changed`: head `repo_base` differs from the row's `repo_sha`.
5. `ahead`: otherwise.

The person also sees whether *they* have a draft (`draft` with its change count) as a separate field, because a draft is per person.

### Save and conflicts

`definition_save {kind, id, baseRevision, content, message, repoSha?}` runs in one transaction that first locks the row (`SELECT … FROM definition WHERE kind = $1 AND id = $2 FOR UPDATE`); it does not take the `GLOBAL` lock, and reconcile cannot run concurrently because it runs before the server accepts requests. A unique violation on the revision key is mapped to 409 `base-moved` as a backstop.

* `baseRevision` must equal the head: otherwise 409 `base-moved` with the head revision. No silent overwrite.
* Every new revision stores `repo_base` = the row's current `repo_sha`, read under the lock. When that differs from the head's `repo_base` (state `repository-changed`), the request must carry `repoSha` equal to the row's `repo_sha`: otherwise 409 `repository-changed`. Passing it records that the person saw the repository version.
* Content equal to head: 409 `no-change`.
* Valid content: append revision `head+1`, move head, delete the person's draft for this definition, write an audit entry, emit a `Change` of kind `definition`.

A draft is per person: `definition_draft_put` replaces it, with its `baseRevision`. `baseRevision: 0` drafts a definition that does not exist yet (the screen's "New"); its first save is `definition_create`. A draft whose base is not the head is shown as `stale` with the head revision; the editor offers the diff. Drafts are never merged automatically.

### Resolve: what an execution uses

Profile resolution (the code behind `profiles_list` and agent session start), `loadPipeline` and `catalogue` read the head revision's content from the store instead of the file. Each creates a **definition reference** `{kind, id, revision, contentSha}` and the execution stores the list in a new `execution.definitions` column (JSON array, typed by the `DefinitionRef` contract). A pipeline run resolves the pipeline and every agent step's profile once at start and records them in `execution.definitions`; each step entry in the run's `data.steps` also records the `DefinitionRef` of its profile. The run keeps `definitionSha256` (the sha of the parsed pipeline, as today); `contentSha` of the pipeline ref is the canonical sha and is used for comparison from now on.

A run or test from a draft records `{kind, id, revision: <base>, contentSha, draft: true}` and stores the draft content in `execution.draft_content`, a map keyed `<kind>/<id>` (a run may resolve a draft pipeline and, later, draft profiles). The diff from the base is computed on read from the base revision and that content. A draft with `problems` cannot run: 400.

Lab variants keep `config_ref` (the hash of pipeline and overrides, `lab.go`) and the existing `execution.config_ref` column, unchanged in meaning. Their trial executions get `definitions` like any run, so an assessment points at exact revisions.

### Rights

Two roles, as in [fleet-people-access.md](fleet-people-access.md) "Operations per role". `architectOps` (`go/coordinator/people.go`) gains `definition_`, `agent_effective`, `agent_compatibility`, `agent_test` and `harness_models` (exact names, not an `agent_` prefix). So every definition read and write is architect only, as `profiles_list` is today; `agent_test` is architect only too, because it runs unsaved or new profile content. Exceptions:

* `pipeline_rerun` and the pipeline run entry points stay project operations, under the same grant check as today; either one with `draft: true` is architect only (it carries unsaved content).
* `catalogue` stays a project operation (operators read the recommendation); its new assessment fields are visible to the same callers.
* `lab_compare_methods` is architect (it is `lab_`).

**Agent saves stay inside the director's projects** (owner decision, 2026-11-11). A person with the architect role saves shared definitions: the role is global. An agent acts with its director's grants: `definition_save` from an agent is 409 `used-outside-grants` when the definition is used by a project outside the director's grants, and the body names those `projects`; the agent saves a copy under a new id (`definition_create`) instead. Use is read from project roles (profiles), catalogue methods (`agentProfile`, `pipeline`, by `scope`: a project or `shared` for all) the agent steps of pipelines those methods run, the sessions that run a profile and the runs of a pipeline (read across projects); the catalogue is used by every project.

**Who saved.** A revision stores `by` (the person's email, or the execution id for an agent) and `directedBy` (the person whose work it is; equal to `by` for a person). An agent saves only through an agent tool whose director has the architect role: the agent-tool gate (`go/coordinator/agent.go`, the check that applies `architectOps` to agent calls) checks the director's role for these names, so the PM agent of an architect passes and others get 403. The PM agent's tool set ([fleet-pm-agent.md](fleet-pm-agent.md)) gains `definition_get`, `definition_draft_put` and `definition_save` behind that check; its revisions have `source: agent`.

**Names.** `people.json` already holds `name` (`person.name`, NOT NULL). Every operation here returns people as `Person {email, name}`; the name comes from the `person` table at read time, so a renamed person shows the new name on old revisions. The `person` row policy lets a caller read only their own row unless the scope is `'*'`, so the lookup runs as the coordinator with scope `'*'` and returns only `email` and `name`. This also closes the redesign review's open point "people have no names": the same `Person` type is used by thread blocks (`by`) in lane F.

### Operations

The `ops` contract (`contracts/ops.schema.json`) has the `$defs` below; the new operations are in its `planned` list until they are built, and each build moves its names to `operations`. `definition_mirror_reset` is stage 2 and not planned yet. `catalogue` is built: its reply keeps today's shape in the contract until this build changes `methods` to `CatalogueMethodView`. Handlers decode the request into the generated struct with unknown fields refused; the app calls `op<'definition_get'>` and receives `DefinitionGetReply`. The schemas use only the generator's supported subset (`contracts/README.md`): no `oneOf`, so a definition's content is three optional typed properties of which exactly one is present (the handler checks it and returns 400 otherwise).

Errors use the existing envelope: 400 invalid request, 403 role, 404 unknown definition, run or revision, 409 with a `reason` from `ConflictReason`.

| Operation | Screen element | Change on existing code |
|---|---|---|
| `definition_list` | control column of each editor, "Changed, not saved" group | new |
| `definition_get` | head, Configuration tab | new |
| `definition_draft_put`, `definition_draft_get`, `definition_draft_discard` | Draft bar | new |
| `definition_diff` | Diff tab, conflict view | new |
| `definition_save` | "Save as commit" (label kept; it saves a revision) | new |
| `definition_history` | History tab | new |
| `definition_export` | Export to the code repository (one click) | new |
| `definition_mirror_reset` | the mirror problem's action | new, owner only |
| `definition_usage` | Usage tab, row run count and success | new |
| `agent_effective` | Effective tab | new |
| `agent_compatibility` | Compatibility tab, row state | new |
| `agent_test` | Test on an agent | new; starts a session like `session_create` |
| `harness_models` | model and effort choice | new |
| `pipeline_run_create` | Run, Test on a pipeline | the route `POST /api/pipelines/{id}/runs` (`pipelineRunCreate`) as an operation, plus `draft`. The capability op `pipeline_run` (`query`, `methodId`, through the catalogue) is unchanged except that it records definition references |
| `pipeline_rerun` | "Rerun failed", "Repeat all steps" | new |
| `catalogue` | Methods table | adds `assessment` per method |
| `lab_compare_methods` | "Compare methods in Lab" | new; creates an experiment |
| `lab_experiment_get` | Lab matrix | adds `baseline` and per-variant `vsBaseline` |
| `lab_experiment_promote` | Promote | saves a catalogue revision instead of returning a patch |

### Contract `$defs`

Shared types:

```json
{
  "DefinitionKind": {"type": "string", "enum": ["agent", "pipeline", "catalogue"]},
  "DefinitionState": {"type": "string", "enum": ["synced", "ahead", "repository-changed", "store-only", "repository-deleted"]},
  "RevisionSource": {"type": "string", "enum": ["repo", "editor", "lab", "agent"]},
  "ConflictReason": {"type": "string", "enum": ["base-moved", "repository-changed", "no-change", "wrong-state", "invalid-draft"]},
  "ActorKind": {"type": "string", "enum": ["person", "agent", "system"]},
  "FieldOp": {"type": "string", "enum": ["add", "remove", "replace"]},
  "EffectiveLayerName": {"type": "string", "enum": ["shared-rules", "role", "definition", "project"]},
  "ModelSource": {"type": "string", "enum": ["shipped", "reported"]},
  "RerunMode": {"type": "string", "enum": ["failed", "all"]},
  "AssessmentBasis": {"type": "string", "enum": ["lab", "usage", "none"]},
  "Person": {"type": "object", "required": ["email", "name"], "properties": {"email": {"type": "string"}, "name": {"type": "string"}}},
  "Actor": {"type": "object", "required": ["kind"], "properties": {
    "kind": {"$ref": "#/$defs/ActorKind"}, "person": {"$ref": "#/$defs/Person"}, "executionId": {"type": "string"},
    "directedBy": {"$ref": "#/$defs/Person"}},
    "$comment": "person: the person (kind person); executionId: the agent execution (kind agent); system: reconcile. directedBy is set for person and agent."},
  "DefinitionKey": {"type": "object", "required": ["kind", "id"], "properties": {"kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}}},
  "DefinitionRef": {"type": "object", "required": ["kind", "id", "revision", "contentSha"], "properties": {
    "kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}, "revision": {"type": "integer", "minimum": 0},
    "contentSha": {"type": "string"}, "draft": {"type": "boolean"}},
    "$comment": "With draft true, revision is the base revision (0 for a new definition) and contentSha is the draft's."},
  "DefinitionContent": {"type": "object", "properties": {
    "agent": {"$ref": "#/$defs/AgentDefinition"}, "pipeline": {"$ref": "#/$defs/PipelineDefinition"}, "catalogue": {"$ref": "#/$defs/CatalogueDefinition"}},
    "$comment": "Exactly one property, matching the kind."},
  "AgentDefinition": {"$ref": "profile.schema.json#/$defs/Profile", "$comment": "The agent profile (contracts/profile.schema.json); go/contract.ParseProfile is its validator."},
  "PipelineDefinition": {"type": "object", "required": ["version", "id", "steps"], "properties": {
    "version": {"type": "integer"}, "id": {"type": "string"}, "steps": {"type": "array", "items": {"$ref": "#/$defs/PipelineStep"}}},
    "additionalProperties": true},
  "PipelineStep": {"type": "object", "required": ["id", "kind"], "properties": {
    "id": {"type": "string"}, "kind": {"type": "string"}, "connection": {"type": "string"}, "tool": {"type": "string"}, "arguments": {},
    "mode": {"type": "string"}, "output": {}, "profile": {"type": "string"}, "prompt": {"type": "string"}, "command": {"type": "string"},
    "timeout_ms": {"type": "integer"}, "host": {"type": "string"}},
    "$comment": "kind api (connection, tool, arguments, mode, output), agent (profile, prompt) or script (command, timeout_ms, host); closed, as validatePipeline allows only these keys."},
  "CatalogueDefinition": {"type": "object", "required": ["capability", "methods"], "$comment": "The whole config/catalogue.json as it is today (one capability).", "properties": {
    "capability": {"type": "string"}, "sharedDefault": {"type": "string"},
    "methods": {"type": "array", "items": {"$ref": "#/$defs/CatalogueMethod"}}}},
  "CatalogueMethod": {"type": "object", "required": ["id", "name", "scope", "pipeline"], "properties": {
    "id": {"type": "string"}, "name": {"type": "string"}, "scope": {"type": "string"}, "pipeline": {"type": "string"},
    "agentProfile": {"type": "string"}, "overrides": {"type": "object", "additionalProperties": true},
    "configRef": {"type": "string"}, "sourceExperiment": {"type": "string"}}},
  "Revision": {"type": "object", "required": ["revision", "contentSha", "source", "actor", "message", "at"], "properties": {
    "revision": {"type": "integer", "minimum": 1}, "parent": {"type": ["integer", "null"]}, "contentSha": {"type": "string"},
    "repoBase": {"type": ["string", "null"]}, "source": {"$ref": "#/$defs/RevisionSource"}, "actor": {"$ref": "#/$defs/Actor"},
    "message": {"type": "string"}, "at": {"type": "string"}, "mirrorCommit": {"type": ["string", "null"]}}, "$comment": "mirrorCommit: stage 2 only."},
  "MirrorState": {"type": "string", "enum": ["mirrored", "pending", "failed", "off"], "$comment": "Stage 2 only; the mirror and definition_mirror_reset fields and operation are added with it."},
  "DraftInfo": {"type": "object", "required": ["baseRevision", "changes", "stale", "updatedAt"], "properties": {
    "baseRevision": {"type": "integer"}, "changes": {"type": "integer", "minimum": 0}, "stale": {"type": "boolean"}, "updatedAt": {"type": "string"}}},
  "RunSummary": {"type": "object", "required": ["state", "at"], "properties": {"executionId": {"type": "string"}, "state": {"type": "string"}, "at": {"type": "string"}}},
  "Window": {"type": "object", "required": ["runs", "succeeded", "wallMsMedian", "tokens"], "properties": {
    "runs": {"type": "integer"}, "succeeded": {"type": "integer"}, "wallMsMedian": {"type": ["integer", "null"]}, "tokens": {"type": "integer"},
    "costUsd": {"type": ["number", "null"]}, "costBasis": {"type": "string"}}}
}
```

`changes` counts changed leaf values between the draft and its base (the same walk as `definition_diff`'s field list). `state` in `RunSummary` uses the execution state vocabulary of the quality round's record work (lane A); until then the coordinator's current execution states. `costUsd` is null when spend has no money basis; `costBasis` says what it is (`tokens`, `wall_ms`, `measured`), as Lab's spend does.

Operations:

```json
{
  "DefinitionListRequest": {"type": "object", "required": ["kind"], "properties": {
    "kind": {"$ref": "#/$defs/DefinitionKind"}, "projectId": {"type": "string"}, "includeRetired": {"type": "boolean"}}},
  "DefinitionListReply": {"type": "object", "required": ["items"], "properties": {"items": {"type": "array", "items": {"$ref": "#/$defs/DefinitionListItem"}}}},
  "DefinitionListItem": {"type": "object", "required": ["kind", "id", "name", "scope", "path", "revision", "state", "last30"], "properties": {
    "kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}, "name": {"type": "string"}, "scope": {"type": "string"},
    "path": {"type": "string"}, "revision": {"type": "integer"}, "state": {"$ref": "#/$defs/DefinitionState"},
    "draft": {"$ref": "#/$defs/DraftInfo"}, "mirror": {"$ref": "#/$defs/MirrorState"}, "lastRun": {"$ref": "#/$defs/RunSummary"}, "last30": {"$ref": "#/$defs/Window"},
    "agent": {"$ref": "#/$defs/AgentListFacts"}, "compatibility": {"$ref": "#/$defs/CompatibilityState"}}},
  "AgentListFacts": {"type": "object", "required": ["harness", "model", "roles"], "properties": {
    "harness": {"type": "string"}, "model": {"type": "string"}, "effort": {"type": ["string", "null"]},
    "roles": {"type": "array", "items": {"type": "string"}}},
    "$comment": "harness defaults as at session start; roles: the project roles (project.json roles) that name this profile."},

  "DefinitionGetRequest": {"type": "object", "required": ["kind", "id"], "properties": {
    "kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}, "revision": {"type": "integer", "minimum": 1}}},
  "DefinitionGetReply": {"type": "object", "required": ["kind", "id", "path", "scope", "revision", "head", "state", "text", "content", "meta"], "properties": {
    "kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}, "path": {"type": "string"}, "scope": {"type": "string"},
    "revision": {"type": "integer"}, "head": {"type": "integer"}, "state": {"$ref": "#/$defs/DefinitionState"},
    "repoSha": {"type": ["string", "null"]}, "text": {"type": "string"}, "content": {"$ref": "#/$defs/DefinitionContent"},
    "meta": {"$ref": "#/$defs/Revision"}, "draft": {"$ref": "#/$defs/DraftInfo"}, "mirror": {"$ref": "#/$defs/MirrorState"}}},
  "DefinitionMirrorResetRequest": {"type": "object", "required": ["confirm"], "properties": {"confirm": {"type": "string"}},
    "$comment": "confirm must be the repository name; owner only. Rebuilds the mirror branch from store history and force-pushes."},
  "DefinitionMirrorResetReply": {"type": "object", "required": ["commits"], "properties": {"commits": {"type": "integer"}}},

  "DefinitionDraftPutRequest": {"type": "object", "required": ["kind", "id", "baseRevision", "content"], "properties": {
    "kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}, "baseRevision": {"type": "integer", "minimum": 0},
    "content": {"$ref": "#/$defs/DefinitionContent"}}},
  "DefinitionDraftPutReply": {"type": "object", "required": ["draft", "problems"], "properties": {
    "draft": {"$ref": "#/$defs/DraftInfo"}, "problems": {"type": "array", "items": {"$ref": "#/$defs/Problem"}}}},
  "Problem": {"type": "object", "required": ["path", "message"], "properties": {"path": {"type": "string"}, "message": {"type": "string"}},
    "$comment": "Validation finding; path is a JSON pointer into the content. A draft is stored even when invalid; save refuses it."},
  "DefinitionDraftGetRequest": {"type": "object", "required": ["kind", "id"], "properties": {"kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}}},
  "DefinitionDraftGetReply": {"type": "object", "required": ["draft"], "properties": {
    "draft": {"$ref": "#/$defs/DraftInfo"}, "content": {"$ref": "#/$defs/DefinitionContent"}, "problems": {"type": "array", "items": {"$ref": "#/$defs/Problem"}}},
    "$comment": "No draft: 404."},
  "DefinitionDraftDiscardRequest": {"type": "object", "required": ["kind", "id"], "properties": {"kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}}},
  "DefinitionDraftDiscardReply": {"type": "object", "required": ["discarded"], "properties": {"discarded": {"type": "boolean"}}},

  "DiffSide": {"type": "object", "properties": {"revision": {"type": "integer", "minimum": 1}, "draft": {"type": "boolean"}, "repository": {"type": "boolean"}},
    "$comment": "Exactly one of revision, draft (the caller's draft) or repository (the shipped file)."},
  "DefinitionDiffRequest": {"type": "object", "required": ["kind", "id", "from", "to"], "properties": {
    "kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}, "from": {"$ref": "#/$defs/DiffSide"}, "to": {"$ref": "#/$defs/DiffSide"}}},
  "DefinitionDiffReply": {"type": "object", "required": ["unified", "fields"], "properties": {
    "unified": {"type": "string"}, "fields": {"type": "array", "items": {"$ref": "#/$defs/FieldChange"}}}},
  "FieldChange": {"type": "object", "required": ["path", "op"], "properties": {
    "path": {"type": "string"}, "op": {"$ref": "#/$defs/FieldOp"}, "fromText": {"type": "string"}, "toText": {"type": "string"}},
    "$comment": "fromText and toText are the canonical JSON text of the old and new values; the editor uses path to place the attention tick."},

  "DefinitionSaveRequest": {"type": "object", "required": ["kind", "id", "baseRevision", "content", "message"], "properties": {
    "kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}, "baseRevision": {"type": "integer", "minimum": 1},
    "content": {"$ref": "#/$defs/DefinitionContent"}, "message": {"type": "string"}, "repoSha": {"type": "string"}}},
  "DefinitionSaveReply": {"type": "object", "required": ["revision", "state"], "properties": {
    "revision": {"$ref": "#/$defs/Revision"}, "state": {"$ref": "#/$defs/DefinitionState"}}},
  "DefinitionConflict": {"type": "object", "required": ["reason", "head"], "properties": {
    "reason": {"$ref": "#/$defs/ConflictReason"}, "head": {"type": "integer"}, "repoSha": {"type": ["string", "null"]}},
    "$comment": "Body of a 409 from definition_save and definition_create."},

  "DefinitionCreateRequest": {"type": "object", "required": ["kind", "id", "content", "message"], "properties": {
    "kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}, "content": {"$ref": "#/$defs/DefinitionContent"}, "message": {"type": "string"}}},
  "DefinitionCreateReply": {"type": "object", "required": ["revision"], "properties": {"revision": {"$ref": "#/$defs/Revision"}}},

  "DefinitionHistoryRequest": {"type": "object", "required": ["kind", "id"], "properties": {
    "kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}, "before": {"type": "integer"}, "limit": {"type": "integer", "minimum": 1}}},
  "DefinitionHistoryReply": {"type": "object", "required": ["revisions", "repoSha"], "properties": {
    "revisions": {"type": "array", "items": {"$ref": "#/$defs/Revision"}}, "repoSha": {"type": ["string", "null"]}}},

  "DefinitionExportRequest": {"type": "object", "properties": {"keys": {"type": "array", "items": {"$ref": "#/$defs/DefinitionKey"}}},
    "$comment": "No keys: every definition in state ahead or store-only. repository-changed and repository-deleted are never exported (named keys in those states: 409 repository-changed) until a save resolves them, so an export cannot revert a release."},
  "DefinitionExportReply": {"type": "object", "required": ["patch", "files", "covers"], "properties": {
    "patch": {"type": "string"}, "files": {"type": "array", "items": {"$ref": "#/$defs/ExportFile"}},
    "covers": {"type": "array", "items": {"$ref": "#/$defs/DefinitionRef"}}}},
  "ExportFile": {"type": "object", "required": ["path", "text", "baseSha"], "properties": {
    "path": {"type": "string"}, "text": {"type": "string"}, "baseSha": {"type": ["string", "null"]}}},

  "DefinitionUsageRequest": {"type": "object", "required": ["kind", "id"], "properties": {
    "kind": {"$ref": "#/$defs/DefinitionKind"}, "id": {"type": "string"}, "days": {"type": "integer", "minimum": 1}}},
  "DefinitionUsageReply": {"type": "object", "required": ["usedBy", "total", "byRevision"], "properties": {
    "usedBy": {"type": "array", "items": {"$ref": "#/$defs/DefinitionKey"}}, "total": {"$ref": "#/$defs/Window"},
    "byRevision": {"type": "array", "items": {"$ref": "#/$defs/RevisionWindow"}}}},
  "RevisionWindow": {"type": "object", "required": ["revision", "window"], "properties": {"revision": {"type": "integer"}, "window": {"$ref": "#/$defs/Window"}}},

  "AgentEffectiveRequest": {"type": "object", "required": ["id", "projectId"], "properties": {"id": {"type": "string"}, "projectId": {"type": "string"}, "draft": {"type": "boolean"}}},
  "AgentEffectiveReply": {"type": "object", "required": ["layers", "result"], "properties": {
    "layers": {"type": "array", "items": {"$ref": "#/$defs/EffectiveLayer"}}, "result": {"$ref": "#/$defs/AgentDefinition"}}},
  "EffectiveLayer": {"type": "object", "required": ["name", "source", "fields"], "properties": {
    "name": {"$ref": "#/$defs/EffectiveLayerName"}, "source": {"type": "string"},
    "revision": {"type": ["string", "null"]}, "text": {"type": "string"}, "fields": {"type": "array", "items": {"type": "string"}}},
    "$comment": "source is a path or a URL; revision is the store revision, the specq commit or the project file sha; fields lists the setting paths this layer decides."},

  "CompatibilityState": {"type": "string", "enum": ["checked", "failed", "unchecked"]},
  "AgentCompatibilityRequest": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}}},
  "AgentCompatibilityReply": {"type": "object", "required": ["rows"], "properties": {"rows": {"type": "array", "items": {"$ref": "#/$defs/CompatibilityRow"}}}},
  "CompatibilityRow": {"type": "object", "required": ["harness", "harnessVersion", "model", "state"], "properties": {
    "harness": {"type": "string"}, "harnessVersion": {"type": ["string", "null"]}, "model": {"type": "string"}, "effort": {"type": ["string", "null"]},
    "state": {"$ref": "#/$defs/CompatibilityState"}, "revision": {"type": ["integer", "null"]}, "evidence": {"$ref": "#/$defs/RunSummary"}, "error": {"type": "string"}}},

  "HarnessModelsRequest": {"type": "object", "required": ["harness"], "properties": {"harness": {"type": "string"}}},
  "HarnessModelsReply": {"type": "object", "required": ["models"], "properties": {"models": {"type": "array", "items": {"$ref": "#/$defs/HarnessModel"}}}},
  "HarnessModel": {"type": "object", "required": ["id", "efforts", "source"], "properties": {
    "id": {"type": "string"}, "label": {"type": "string"}, "efforts": {"type": "array", "items": {"type": "string"}},
    "source": {"$ref": "#/$defs/ModelSource"}, "hosts": {"type": "array", "items": {"type": "string"}}}},

  "AgentTestRequest": {"type": "object", "required": ["id", "projectId", "prompt"], "properties": {
    "id": {"type": "string"}, "projectId": {"type": "string"}, "prompt": {"type": "string"}, "draft": {"type": "boolean"}, "hostId": {"type": "string"}},
    "$comment": "draft true uses the caller's draft; false the head revision."},
  "AgentTestReply": {"type": "object", "required": ["sessionId", "executionId", "definition"], "properties": {
    "sessionId": {"type": "string"}, "executionId": {"type": "string"}, "definition": {"$ref": "#/$defs/DefinitionRef"}}},

  "PipelineRunCreateRequest": {"type": "object", "required": ["pipelineId", "projectId", "requestId"], "properties": {
    "pipelineId": {"type": "string"}, "projectId": {"type": "string"}, "requestId": {"type": "string"}, "draft": {"type": "boolean"},
    "exampleArtifactId": {"type": "string"}, "example": {"type": "object", "additionalProperties": true},
    "overrides": {"type": "object", "additionalProperties": true}},
    "$comment": "Body of POST /api/pipelines/{id}/runs (pipelineRunCreate) as an operation pipeline_run_create; pipelineId was the URL segment. draft true runs the caller's draft. The request signature includes draft and the content sha."},
  "PipelineRunCreateReply": {"type": "object", "required": ["runId", "definitions"], "properties": {
    "runId": {"type": "string"}, "definitions": {"type": "array", "items": {"$ref": "#/$defs/DefinitionRef"}}}},
  "PipelineRerunRequest": {"type": "object", "required": ["runId", "mode"], "properties": {
    "runId": {"type": "string"}, "mode": {"$ref": "#/$defs/RerunMode"}, "draft": {"type": "boolean"}, "requestId": {"type": "string"}}},
  "PipelineRerunReply": {"type": "object", "required": ["runId", "reused", "definitions"], "properties": {
    "runId": {"type": "string"}, "reused": {"type": "array", "items": {"type": "string"}}, "definitions": {"type": "array", "items": {"$ref": "#/$defs/DefinitionRef"}}}},

  "CatalogueRequest": {"type": "object", "required": ["projectId"], "properties": {"projectId": {"type": "string"}, "capability": {"type": "string"}}},
  "CatalogueReply": {"type": "object", "required": ["capability", "methods", "sharedDefault", "recommendedMethod", "recommendationSource"], "properties": {
    "capability": {"type": "string"}, "sharedDefault": {"type": ["string", "null"]}, "recommendedMethod": {"type": ["string", "null"]},
    "recommendationSource": {"type": ["string", "null"]}, "revision": {"type": "integer"},
    "methods": {"type": "array", "items": {"$ref": "#/$defs/CatalogueMethodView"}}}},
  "CatalogueMethodView": {"type": "object", "required": ["method", "assessment"], "properties": {
    "method": {"$ref": "#/$defs/CatalogueMethod"}, "recommendedFor": {"type": "array", "items": {"type": "string"}},
    "assessment": {"$ref": "#/$defs/MethodAssessment"}}},
  "MethodAssessment": {"type": "object", "required": ["basis"], "properties": {
    "basis": {"$ref": "#/$defs/AssessmentBasis"}, "experimentId": {"type": "string"}, "examples": {"type": "integer"},
    "score": {"type": ["number", "null"]}, "scoreForm": {"type": "string"}, "wallMsMedian": {"type": ["integer", "null"]},
    "costUsd": {"type": ["number", "null"]}, "costBasis": {"type": "string"}, "at": {"type": "string"}}},

  "LabCompareMethodsRequest": {"type": "object", "required": ["projectId", "capability", "methods"], "properties": {
    "projectId": {"type": "string"}, "capability": {"type": "string"}, "methods": {"type": "array", "items": {"type": "string"}},
    "useCase": {"type": "string"}, "baseline": {"type": "string"}}},
  "LabCompareMethodsReply": {"type": "object", "required": ["experimentId"], "properties": {"experimentId": {"type": "string"}}},

  "VariantVsBaseline": {"type": "object", "required": ["variant", "examples", "scoreDelta"], "properties": {
    "variant": {"type": "string"}, "examples": {"type": "integer"}, "scoreDelta": {"type": ["number", "null"]},
    "wins": {"type": "integer"}, "losses": {"type": "integer"}, "ties": {"type": "integer"},
    "wallMsDelta": {"type": ["integer", "null"]}, "costUsdDelta": {"type": ["number", "null"]}},
    "$comment": "Added to lab_experiment_get: one per non-baseline variant, over examples where both have a final score."},

  "LabExperimentPromoteReply": {"type": "object", "required": ["method", "catalogue", "recommendation"], "properties": {
    "method": {"$ref": "#/$defs/CatalogueMethod"}, "catalogue": {"$ref": "#/$defs/DefinitionRef"}, "recommendation": {"type": ["string", "null"]}}}
}
```

`lab_experiment_get`'s reply (typed by lane F from the existing route) gains `baseline: string` (the variant label) and `vsBaseline: VariantVsBaseline[]`. Its other fields are not restated here.

### Behaviour per operation

* **`definition_list`.** One query over `definition` joined with the head revision and the caller's draft, plus one grouped query for `lastRun` and `last30`: `jsonb_array_elements(execution.definitions)` over executions of the last 30 days, grouped by kind and id. `definition_usage` for one key uses the GIN index (`@> '[{"kind":…,"id":…}]'`). For agents, `compatibility` is the head row's worst state from `agent_compatibility` (`failed` over `unchecked` over `checked`). `projectId` filters to definitions whose `scope` is `shared` or the project. `path` is the repository path even for store-only definitions (where export would write).
* **`definition_get`.** `text` is the canonical JSON text of the chosen revision (the Configuration tab's raw view and the Diff tab's source).
* **Drafts.** `definition_draft_put` validates and stores even an invalid draft, returning `problems`; it emits a `Change` only to the caller. Drafts are kept until saved or discarded; no expiry.
* **`definition_diff`.** Both sides are rendered as canonical text and diffed line by line (unified, three lines of context; Go has no stdlib diff, so a small Myers diff in the coordinator, about 80 lines, with its test). `fields` comes from a recursive walk of the two JSON values; arrays of objects with an `id` (steps, methods) are matched by `id`, other arrays by index.
* **`definition_save`.** As in "Save and conflicts". Lab Promote and the PM agent tool call the same function with their `source`.
* **`definition_create`.** New definitions from the screen's "New" action: 409 `wrong-state` if the key exists in any state. State `store-only`. Deleting a definition is not an operation; export of a `repository-deleted` definition is refused, and the owner restores or removes the file in git.
* **`definition_export`.** For each covered definition (states `ahead` and `store-only` only), write the head content as canonical text, then a unified diff against the shipped file (`/dev/null` side for a new file). `patch` is `git apply`-ready with `a/` and `b/` prefixes and an `index <old blob>..<new blob>` line per file, so `git apply -3` merges when main has moved. Export changes nothing in the store; after a release ships the files, reconcile rule 3 sees the content equal to head: state `synced`, no new revision.

  The export reads the shipped files from the running release's `config/`, so the patch applies to the commit the coordinator was built from (`release.json` names it). The reply's `ExportFile.baseSha` lets the owner see when the repository has moved past that commit.
* **`definition_usage`.** From executions in the window, grouped by revision. `usedBy`: pipelines whose head steps name the agent, catalogue methods naming the pipeline or agent profile.
* **`agent_effective`.** Layers in order: `shared-rules` (the specq core named by the project's rule delivery, [fleet-stack.md](fleet-stack.md) rule delivery: path and commit), `role` (the project's role entry in `config/projects/<id>/project.json` `roles`), `definition` (head or the caller's draft), `project` (the project's `rules.md` and profile overrides). `result` is what a session start would send, computed by the same function the session start uses, so the tab cannot drift from the real start. `fields` names which settings each layer decided (last writer wins).
* **`agent_compatibility`.** Rows are the distinct (harness, `execution.harness_version`, model, effort) of this agent's executions in 30 days (the version is already set from `EventData.harnessVersion`), plus the head's own combination if not seen. A row is `checked` when its latest execution at the head revision reached the harness and ended without a harness or start error, `failed` when it ended with one (with the `error`), `unchecked` otherwise. Unchecked is never shown as incompatible (pattern rule).
* **`harness_models`.** A shipped list `config/harness-models.json` (`{harness: [{id, label, efforts}]}`, a release default, not a definition) merged with models executors report (`availableModels` from ACP harnesses at session open, `go/executor/harness_acp.go`, stored per host in its observation). `source` tells them apart; `hosts` lists where a reported model was seen.
* **`agent_test`.** Architect only. Creates a session with the project's working directory as `session_create` does, `directedBy` the caller, profile from the head or the caller's draft (stored in `draft_content`), and sends `prompt` as the first message. Dispatch and budgets as any session.
* **`pipeline_run_create`.** `pipelineId` resolves the head; `draft: true` uses the caller's draft (architect only). The request signature that detects a repeated `requestId` (`pipelineRunCreate`) adds `draft` and the pipeline `contentSha`. The reply lists every definition the run resolved.
* **`pipeline_rerun`.** Loads finished run R (409 `wrong-state` otherwise) and the definition to use: head of R's pipeline, or the caller's draft with `draft: true` (architect only). Mode `all` starts a fresh run with R's `example`, `exampleArtifactId` and `overrides`. Mode `failed`: walk the new definition's steps in order. A step is reused when all hold: R's `data.steps` entry with the same `id` succeeded; its canonical step content is equal; for an agent step, the profile `DefinitionRef` it would resolve now has the same `contentSha` as the one recorded on R's step; and every step it reads through `${steps.<id>.output…}` (parsed by `resolveRefs`, `pipelines.go`) was reused. The first step that is not reused, and every step after it, run. Reused steps copy R's step entry (`state`, `output`, `usage`, and artifact ids by reference, not copied blobs) into the new run with `reusedFrom: R`. R2 records `rerunOf` and `reused`.
* **`catalogue`.** Reads the catalogue head inside the caller's transaction; the reply keeps today's `sharedDefault`, `recommendedMethod` and `recommendationSource` (the `project-recommendation` document, then `project.json` `recommendedMethod`, then `sharedDefault`). A method's **config ref** is its `configRef` when set, else the same hash Lab computes for a variant (`sha256(canon{pipeline, overrides})`, `lab.go`). `assessment` per method: the latest finished Lab experiment with a variant of that config ref: basis `lab`, its score form, mean final score over approved examples, median wall time, spend with basis. Without one, basis `usage` from `definition_usage` of the method's pipeline (no score), else `none`. `recommendedFor` lists the projects (granted to the caller) whose resolved recommendation, by the same three sources, is the method.
* **`lab_compare_methods`.** Methods of one capability often run different pipelines, but `experiment.pipeline_id` is one column and `expCreate` takes one `pipelineId`. Variants gain an optional `pipelineId`; `experiment.pipeline_id` stays as the default for variants without one, and the trial start (`insertExecution` and `pipelineStart` calls in `lab.go`) uses the variant's pipeline. The variant's `config_ref` hash already covers the pipeline. The operation creates an experiment through `expCreate` with one variant per method (`label` = method id, pipeline and overrides from the method), examples from the project's use-case registry entry for the capability (or `useCase`), and `baseline` (default: the project's recommended method). Returns the id; the owner launches it on the Lab screen as today.
* **Lab baseline.** `lab_experiment_get` marks `baseline` (stored at experiment creation; for existing experiments, the first variant) and computes `vsBaseline` per variant over examples where both have a final score: mean of the score difference, win, loss and tie counts, and median wall time and cost differences.
* **Promote.** In one `writeTxAs`, `expPromote` calls the save function for the catalogue (source `lab`, actor the person, message `Promote <variant> from <experiment>`), adding the method or updating the one with the same config ref (`pipeline` written as `<pipeline id>.json`), then sets the recommendation by a direct call; `catalogue()` reads the new head inside that transaction, so `recommendation_set`'s "Method unavailable" check passes. The reply returns the method and the catalogue revision instead of `cataloguePatch`.

### Live updates

The SSE `Change` type (lane F) gains kind `definition` with `{kind, id, revision, state}`, from a `definition_change` trigger on the `definition` table using the existing `fleet_change()` function, as `experiment` and `score` do. Draft changes are not broadcast; the editor that wrote the draft has the reply. The screens refetch `definition_list` rows by key, as other screens do.

### Data model

Migration 28 in the coordinator's `schema.sql` (`go/coordinator/schema.sql`) has the three tables (built). As built, `content` and `repo_content` are `text` holding the canonical text, not `jsonb` (which reorders keys and drops the formatting the diff, `contentSha` and export rely on), and `definition` has a `project_id` column that stays null so the change trigger can write its row. The `execution` columns come with the run operations. The plan as designed:

```sql
CREATE TABLE definition (
  kind text NOT NULL, id text NOT NULL,
  path text NOT NULL,                -- repository path, also for store-only rows
  head int NOT NULL,
  repo_sha text, repo_content jsonb, repo_content_sha text, -- null: no shipped file
  ever_shipped boolean NOT NULL DEFAULT false,
  retired_at timestamptz,
  PRIMARY KEY (kind, id));
CREATE TABLE definition_revision (
  kind text NOT NULL, id text NOT NULL, revision int NOT NULL, parent int,
  content jsonb NOT NULL, content_sha text NOT NULL, repo_base text,
  source text NOT NULL, actor_kind text NOT NULL,          -- person | agent | system
  by_email text, by_execution text, directed_by text,      -- null for system
  message text NOT NULL, at timestamptz NOT NULL DEFAULT now(),
  mirror_commit text,                                       -- stage 2 only: sha in taskqorg/fleet-config
  PRIMARY KEY (kind, id, revision),
  FOREIGN KEY (kind, id) REFERENCES definition);
CREATE TABLE definition_draft (
  kind text NOT NULL, id text NOT NULL, person text NOT NULL,
  base int NOT NULL, content jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (kind, id, person));                        -- no foreign key: base 0 drafts a new definition
ALTER TABLE execution ADD COLUMN definitions jsonb NOT NULL DEFAULT '[]', ADD COLUMN draft_content jsonb,
  ADD COLUMN rerun_of text, ADD COLUMN reused_steps jsonb;
CREATE INDEX execution_definitions ON execution USING gin (definitions jsonb_path_ops);
CREATE TRIGGER definition_change AFTER INSERT OR UPDATE OR DELETE ON definition FOR EACH ROW EXECUTE FUNCTION fleet_change();
```

Revisions are never updated or deleted. Row-level security: the three tables are readable and writable by the coordinator role only; the operations enforce roles (architect), as `profiles_list` does today. Drafts store the person's email; a person sees only their own draft.

### Migration and order

1. Wave 1 and lane F land first (this design names their target files).
2. Migration and reconcile; readers switch to the store; executions record definition references. Behaviour is unchanged for a coordinator with no editor revisions, because every head is the shipped file.
3. Read operations, then drafts, diff, save, history, export.
4. Run operations (`pipeline_run_create`, references in `pipeline_run`, `pipeline_rerun`, `agent_test`), then agent tabs, usage, harness models.
5. Catalogue assessment, `lab_compare_methods`, baseline, Promote.
6. The app screens (separate work, from `packages/ui`).

## Alternatives Considered

### Save as a git commit through a worker

The coordinator asks a program worker on a host holding the Fleet repository to commit the change on a branch and open a pull request; the change applies after merge, release and host update.

Rejected by author: the owner wants editors, the PM agent and Lab to change agents and pipelines with immediate effect. The path takes minutes to hours, needs a host with the repository online for every save, and gives the editor no revision to test against while the PR is open. History and rights would live in GitHub, outside Fleet's people and roles.

### Save as a patch only

Keep today's Promote principle: every save returns a patch the owner commits by hand. No store revisions.

Rejected by author: there is no history in Fleet, the PM agent and Lab cannot change anything, and a save does not apply until a release. It keeps one source, but the screens' History, conflicts and usage per revision have nothing to read.

### Coordinator writes the shipped `config/` files

Write the edited file into the release's `config/` directory on the coordinator host.

Rejected: the next release overwrites it, nothing records who changed what, and the coordinator would edit files that belong to the release (the principle that the coordinator never writes tracked config).

### Two-way sync with the config repository

Read the mirror repository back, so a commit made there by hand applies to the store.

Rejected by author: two masters. Every conflict would need a merge rule, and a push to GitHub would bypass Fleet's rights and per-project visibility.

### Store overlay without reconcile

Keep files as the source and store only overrides; a release always wins.

Rejected: a release would silently undo a saved change, which breaks goal 5 and makes executions change without a save.

## Detailed Implementation

Paths are the quality plan's target state after wave 1 (coordinator files named by domain in one `package main`, no area prefixes, `go/contract` generated from `contracts/*.schema.json`). Where wave 1 renames a file, the current name is given in brackets.

### Contracts

* `contracts/ops.schema.json` (Modify): the `$defs` above are in; move each built operation from `planned` to `operations`. Why: contract first; Go handler structs and the app's `op<'name'>` types are generated from it.
* `contracts/README.md` (Modify): list the definition operations under `ops`.
* `go/contract/ops_gen.go` (Generated, committed): regenerated by `go generate`; CI fails on drift.
* `app/src/generated/ops.ts` or lane F's generated path (Generated): the app types.

### Coordinator

* `go/coordinator/schema.sql` (Modify): the migration above.
* `go/coordinator/definitions.go` (Create): kinds and paths, canonical JSON and `contentSha`, git blob sha, reconcile at start, state, revision append, drafts, save with conflict checks, create, history, export, and the field walk. Handlers for `definition_list`, `definition_get`, `definition_draft_put|get|discard`, `definition_diff`, `definition_save`, `definition_create`, `definition_history`, `definition_export`. Why: one domain file, one writer of the three tables.
Stage 2 only (the mirror):

* `go/coordinator/mirror.go` (Create): the mirror loop, git runner with cleared env, backoff, the needs-you question, `definition_mirror_reset`. Why: the only code that runs git for definitions.
* `deploy/` coordinator unit and env defaults (Modify): `FLEET_CONFIG_MIRROR_DIRECTORY`, `FLEET_CONFIG_MIRROR_KEY_FILE`, `FLEET_CONFIG_MIRROR_REMOTE` (default `git@github.com:taskqorg/fleet-config.git`); `git` present on PDS18.
* `scripts/config-mirror-key.sh` (Create): generates the ed25519 deploy key on PDS18 straight into the 0600 file and prints only the public key, like `scripts/host-secret.sh`.
Stage 1:

* `go/coordinator/textdiff.go` (Create): line diff (Myers) and unified output. Why: no stdlib diff; about 80 lines, no new dependency.
* `go/coordinator/profiles.go` (Modify; today `listProfiles`, `readTrustedProfile` and `profilesDir` in `projects.go`, `profilesDir` in `store.go`): profile checks already take content (`contract.ParseProfile`); profiles are read from definition heads and return a `DefinitionRef`. The directory readers remain only in reconcile.
* `go/coordinator/pipelines.go` (Modify; today `pipelines.go`): `loadPipeline` reads the head or a draft; `pipelineRunCreate` becomes the operation `pipeline_run_create` (the route stays) with `draft` and the signature change; `pipelineStart` resolves agent step profiles and records refs per step; `pipeline_rerun` with the reuse walk; record `definitions`, `draft_content`, `rerun_of`, `reused_steps`.
* `go/coordinator/pipelines.go`, capability op (Modify; today in `pipelines.go`, moved by wave 1): `pipeline_run` records definition references; the request signature adds the catalogue revision.
* `go/coordinator/catalogue.go` (Modify; today `catalogue` in `projects.go`): read the catalogue head; method assessments; `recommendedFor`.
* `go/coordinator/lab.go` (Modify; today `lab.go`): `lab_compare_methods`; optional per-variant `pipelineId` in `expCreate` and the trial start; baseline stored at creation and `vsBaseline` in `lab_experiment_get`; variants record definition references; `expPromote` saves a catalogue revision and sets the recommendation in one transaction (removes `cataloguePatch` and the `cRecommend` hook variable, per wave 1's direct calls).
* `go/coordinator/agents.go` (Create): `agent_effective` (calls the session start's own profile assembly), `agent_compatibility`, `agent_test`, `harness_models`, `definition_usage`. Why: the Agents screen's reads in one domain file.
* `go/coordinator/sessions.go` (Modify; today `sessions.go`): expose the profile assembly used at start as one function shared with `agent_effective`; accept a draft profile for `agent_test`.
* `go/coordinator/people.go` (Modify; today `people.go`): `architectOps` gains `definition_`, `agent_effective`, `agent_compatibility`, `agent_test`, `harness_models`; a `person(email) Person` lookup under scope `'*'` used by every reply.
* `go/coordinator/agent_tools.go` (Modify; today the gate in `agent.go` that applies `architectOps` to agent calls): for definition operations, allow when the director is an architect.
* `go/coordinator/stream.go` (Modify): `Change` kind `definition`; drafts only to their owner.
* `go/coordinator/pm.go` or the agent tool file of [fleet-pm-agent.md](fleet-pm-agent.md) (Modify): PM tools `definition_get`, `definition_draft_put`, `definition_save`, allowed when the director is an architect; `source: agent`.
* `go/coordinator/hosts.go` (Modify; today `hosts.go` and the event handler in `host_events.go`): store reported models per host from the new event field, for `harness_models`.
* `go/executor/harness_acp.go` (Modify): send `availableModels` in the session-open event, next to `harnessVersion`. Why: the `reported` source of `harness_models`.
* `contracts/host-protocol.schema.json` (Modify): `EventData.availableModels` (array of model ids).
* `config/harness-models.json` (Create): shipped model list per harness with efforts.
* `config/agents/*.json`, `config/pipelines/*.json`, `config/catalogue.json` (Modify, once): rewritten in canonical form, no content change.
* `config/README.md` (Modify): say `agents/`, `pipelines/` and `catalogue.json` are the repository revision of definitions the coordinator keeps in its store; every save is mirrored to `taskqorg/fleet-config`; `config/` is the seed and the path for shipped definitions.

### Docs

* `docs/design-system/patterns/definition-editor.md` (Modify, in this change): the file is the repository revision, saves are store revisions, export brings them to git.
* `docs/designs/README.md` (Modify, in this change): list this design.
* `docs/designs/fleet-lab.md` (Modify, at build): Promote saves a catalogue revision; baseline and `vsBaseline`; `lab_compare_methods`.
* `docs/designs/fleet-people-access.md` (Modify, at build): the architect operation list.
* `fleet.md` (Modify, at build): knowledge and rules: definitions live in the store, the repository is a revision, export.

### Tests

Go unit tests, next to the code (`go test -race`):

* `go/coordinator/definitions_test.go`: canonical JSON is stable and round-trips; every shipped definition file is canonical; git blob sha equals `git hash-object` for a fixture; the state decision list for each of the five states; field walk matches arrays by `id`; reconcile rule 3's three branches, retire and un-retire, and a non-profile file skipped.
* `go/coordinator/textdiff_test.go`: unified diff applies with `git apply --check` to the fixture (skipped when git is absent).
* `go/coordinator/pipelines_test.go`: rerun reuse walk: unchanged steps reused, a changed step and every step after it rerun, a step reading a rerun step reruns, an agent step whose profile was saved in between reruns.

Outside tests against a real coordinator and Postgres (the existing `tests/go` runner, named by domain):

* `tests/go/definitions.test.ts`:
  * save from the head creates revision n+1 and the next `pipeline_run_create` records it (goal 2, 7);
  * two saves from the same base: second is 409 `base-moved` with the head (goal 4);
  * restart with a changed shipped file under an editor revision: state `repository-changed`, executions keep the head, save without `repoSha` is 409, with it succeeds (goal 5);
  * restart with a changed file and no editor revision: head stays, state `repository-changed`; a new shipped file is imported; a restart on a fresh database imports every seed file;
  * a save while a pipeline run and an agent session are running: both keep their revision (the session's next turn resolves the new head);
  * two concurrent saves from one base: one revision, one 409, no 500;
  * export leaves out a `repository-changed` definition, and a named key in that state is 409;
  * export after two saves: `git apply --check` on a checkout of the release commit succeeds; after shipping the exported files, state `synced` with no new revision (goal 6);
  * draft run records `draft_content` and its diff, and the draft is not visible to another person (goal 8);
  * operator calls `definition_save`, `agent_test`, or `pipeline_rerun` with `draft`: 403; operator calls `catalogue`: 200 with assessments and the three recommendation fields;
  * the PM agent of an architect saves a revision (`actor.kind` agent); of an operator: 403;
  * `agent_effective.result` equals the profile the session start sends for the same project.
* `tests/go/lab.test.ts` (Modify): Promote saves a catalogue revision and sets the recommendation for a new method; `vsBaseline` over a fixture experiment; `lab_compare_methods` over two methods with different pipelines runs each trial on its own pipeline.
* Non-owner scope file (quality plan S7): add every new operation to the role matrix.

Mirror, stage 2 (outside test with a local bare repository as the remote):

* `tests/go/definitions.test.ts`: a save produces one commit with the person as author, the message and the `Fleet-Revision` trailer, and the file equals `definition_get.text`; with the remote unreachable the save succeeds, `mirror` is `pending`, and after the remote returns the commit arrives in order; a commit pushed to the remote from outside stops the loop with the divergence question and no force-push; `definition_mirror_reset` by a non-owner is 403.

### Done when

All operations above are in the `ops` contract with generated Go and TypeScript types and no untyped handler body; the tests above pass; `go generate` shows no drift; a coordinator started on a fresh database behaves as today until a definition is saved.

## Open Questions

* **Catalogue file shape with more than one capability.** The catalogue is one definition for one file. When a second capability is added, the file shape changes (an array, or one file per capability), and so does `CatalogueDefinition`. Decide then.
* **When to build the mirror (stage 2).** After stage 1 has run for a while; the owner decides.
