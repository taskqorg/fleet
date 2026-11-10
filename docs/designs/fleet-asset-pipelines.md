# Fleet asset pipelines: keys, deleting AssetQ, the registry, asset pipelines, judges, the loop

*   **Author:** Alex Kirs
*   **Status:** Proposed
*   **Date:** 2026-11-11
*   **Builds on:** [fleet-lab.md](fleet-lab.md) (experiments, examples, evaluators, pipeline format 2), [fleet-connections.md](fleet-connections.md) (connections, gateway, Meshy job mode), [use-cases.md](../use-cases.md) (Fleet use cases UC1–UC11).

--------------------------------------------------------------------------------

## Overview

Fleet replaces every prototype: agentq, pilotq, serverq, coreq, dashq, taskq and assetq. Pipe's read lane is already gone (connections C4). What remains of AssetQ is a running service with one owner-accepted Meshy job and no callers; three provider keys still sit in the owner's Mac Keychain and are read by csgo tools directly. Lab can run pipelines but has no asset pipeline, no host that runs Blender, no judges per use case and no `loop`.

This design does seven things, in stages S1–S7:

1.  **S1 Keys.** Move the Keychain keys for Meshy, Hugging Face and Google AI into Fleet connections; csgo tools call the gateway.
2.  **S2 Delete AssetQ.** One cut, after S1 (R3.1). The accepted crate image and its GLB become the first UC2 example.
3.  **S3 Registry.** Prototype entries get a status and a `maps_to` link to the Fleet use case that covers them; csgo and desiign become sources; UC12 and UC13 are added.
4.  **S4 Asset pipelines.** `meshy-prop` and `image-brief`, with Blender script steps on one named host.
5.  **S5 Judges.** A rubric per use case, an order-swapped pairwise judge, agreement on screen.
6.  **S6 Loop.** Experiment kind `loop` with strategies and the `propose` generator.
7.  **S7 Character.** `character-multiview` for UC6.

### Glossary

Lab terms (pipeline, example, experiment, variant, trial, evaluator, Promote) are in [fleet-lab.md](fleet-lab.md#glossary); connection terms (connection, key, gateway, per-run token, catalogue provider) are in [fleet-connections.md](fleet-connections.md#glossary).

*   **AssetQ:** the asset service in the assetq repository (the rest of Pipe after its read lane was deleted): `assetq.service` on PDS18, its Postgres database `assetq`, objects under `/srv/assetq`, the route `pipe.ufobe.com`.
*   **Script host:** the executor host that runs a pipeline's Blender script steps.
*   **Headless Blender:** `blender --background --python <script>`, no window.
*   **csgo, desiign:** the owner's game project and a client design project. csgo's research (`docs/knowledge/research/*`, `docs/research/43x.md`) holds the weapon, prop and character routes; desiign holds hero and product-visual work.
*   **Registry source:** a project whose documents a use-case entry points at (`source.repo`).
*   **Rubric:** a judge pipeline's prompt section: criteria, an anchor text per scale point, and pass and fail examples taken from owner-rated trials.

## The Problem

*   **Keys outside Fleet.** `csgo.meshy.api`, `csgo.hf.token` and `gemini` are Mac Keychain items. csgo's Blender MCP reads the Meshy key from `BLENDERMCP_MESHY_API_KEY`. Fleet cannot list, budget or account for these calls; a Meshy charge from csgo is invisible.
*   **A dead service still runs.** AssetQ listens on `127.0.0.1:9460` behind `pipe.ufobe.com`, holds 4 project tokens and 20 MB of objects, and has no caller (checked on PDS18 2026-11-11). A leftover `connections/pipe-read` file is a C4 miss. fleet-lab.md still lists "removal of Pipe's method evaluation" as not built.
*   **The registry treats replaced prototypes as live.** `config/lab/use-cases/` indexes agentq, dashq, pilotq, taskq and assetq as current projects; nothing says which Fleet use case replaces each entry. csgo and desiign, where the asset work happens, are not sources.
*   **No asset pipeline.** UC2 (compare 3D methods) and UC6 (character) have no pipeline, no example and no step that can measure a mesh. Script steps run on the coordinator's default host (PDS18), which has no Blender.
*   **Evaluation stops at generic judges.** `judge-pass-fail` and `judge-scale` have no per-use-case rubric; the order-swapped `pick_best` judge is not built; `loop` is refused with 400.

## Goals

*   **G1.** Every provider key Fleet's work uses is a Fleet connection key; no Keychain copy remains after its first good gateway call.
*   **G2.** AssetQ is gone from PDS18, Caddy, Cloudflare, Fleet's registry and docs; the assetq repository is archived. Only the crate seed survives.
*   **G3.** Every registry entry is either current or marked with what replaced it; `/lab` shows the status.
*   **G4.** A UC2 `ab` experiment runs Meshy image-to-3D on the crate seed, measures each mesh with headless Blender, and ends with an owner `pick_best`.
*   **G5.** Judges for UC1 and UC2 have rubrics and show agreement with the owner.
*   **G6.** A UC9 `loop` experiment stops by `no_gain`.
*   **G7.** One UC6 character trial runs end to end.

## Non-Goals

*   An asset library or asset browser. Outputs are Lab-scope and task artifacts (fleet-connections Non-Goals).
*   Porting AssetQ's method registry, recommendations or worker. Catalogue, Promote and `experiment`/`score` replace them.
*   Branching or fan-out inside a pipeline (fleet-lab, Not adopted). UC4 stays `lab_fit: none`.
*   Real client briefs from desiign. Examples use invented briefs only.
*   Designing the pilotq Telegram channel. UC13 is registered as `to-design`; its design is separate.
*   Coded thresholds: example counts, repeats, judge agreement, budgets and `no_gain` k are owner settings per experiment (Q3, no invented limits).

## Owner decisions

| # | Decision |
|---|---|
| A1 | Import only the owner-accepted crate image and its Meshy GLB, as seed `fleet:UC2` crate@1. Everything else in AssetQ is dropped with its database. |
| A2 | Keychain keys `csgo.meshy.api`, `csgo.hf.token` and `gemini` move into connections `meshy`, `huggingface` and `google-ai`. csgo tools switch to the gateway. Each Keychain item is deleted after the first good call on its connection. |
| A3 | csgo and desiign (generation scenarios only) are registry sources. |
| A4 | Every prototype, taskq included, is replaced by Fleet. Its entries keep their IDs and are marked obsolete or `maps_to` a Fleet use case. |
| A5 | Lab effort goes to fleet, csgo, desiign and asset generation. |
| A6 | pilotq becomes a Fleet Telegram channel: UC13, status `to-design`. |

## Settled with defaults (owner can overturn)

*   **D1. Script host for Blender: the Mac.** Checked read-only on 2026-11-11: the Mac has Blender 5.2.1 LTS (`/opt/homebrew/bin/blender`, `/Applications/Blender.app`) and an Apple M2 GPU with Metal. The Windows PC has an RTX 4070 Ti, but no Blender in Windows (`C:\Program Files\Blender Foundation` absent) or in either WSL distribution. csgo's Blender MCP route already runs on the Mac. Moving to the PC would mean installing Blender under WSL and a GPU path for Cycles, for no measured gain: the measure and preview-render steps are seconds of CPU or Eevee work on a 3000-triangle prop. Revisit when a step needs Cycles renders at volume.
*   **D2. Hugging Face and Google AI usage.** Neither offers a per-key usage endpoint like Tavily's `/usage` (checked against their public API references; the Gemini API reports `usageMetadata` token counts per reply, Hugging Face Inference Providers bills per request in the account). So: Google AI calls record `connection_cost` in unit `input_tokens` from `usageMetadata` (output tokens kept in the call's `usage`), basis `measured`; Hugging Face calls record basis `unavailable` unless the reply carries a cost. Reconcile is not built for either; the API keys screen shows "not reconciled" for them, not "matched". This is the same rule as fleet-connections Q2.
*   **D3. UC4 fan-out.** Stays out. UC4 keeps `lab_fit: none` until a pipeline needs fan-out and the owner reopens the Lab decision.

## Technical Plan (Architecture)

```text
 csgo tools (Mac) ──MCP / HTTP + owner token──┐
 Lab agent step ──MCP──────────────────────────┼──▶ fleet-gateway (PDS18) ──▶ Meshy / Hugging Face / Google AI
 Lab api step (coordinator) ───────────────────┘          │ spool ─▶ tool_run + connection_cost
                                                          │
 pipeline  meshy-prop:  api image_to_3d (job) ──▶ script measure+render (host: mac, Blender) ──▶ evaluators
 pipeline  image-brief: agent | api google-ai ──▶ script image checks ──▶ evaluators
 evaluators: script:example.checks ─▶ owner rating ─▶ judge:<uc>-judge (renders) ─▶ agreement view
```

### 1. Keys and providers (S1)

*   **Providers.** The catalogue (`provider` entries in the coordinator's connections area) gains `huggingface` and `google-ai`, and Meshy gains `multi_image_to_3d` with its own `job_status` path. Tool shapes are below; each tool's `inputSchema` is the provider's request body, passed through.

    | Provider | Tool | Method and path | Unit | Idempotent |
    |---|---|---|---|---|
    | `meshy` | `multi_image_to_3d` | `POST /openapi/v1/multi-image-to-3d` | `credits` | no |
    | `meshy` | `multi_job_status` | `GET /openapi/v1/multi-image-to-3d/{jobId}` | `credits` | yes |
    | `huggingface` | `inference` | `POST /models/{model}` on the Inference Providers router; `model` is a body field taken into the path | `requests` | no |
    | `google-ai` | `generate_content` | `POST /v1beta/models/{model}:generateContent` | `input_tokens` (output tokens kept in the call's `usage`) | no |

    Image generation is charged per call, so no tool here is idempotent and none fails over (connections §2). Gemini and Hugging Face calls are `sync`; Meshy stays `job`.
*   **Key move, per key, owner only.** The value never passes through an agent, a terminal transcript or a file in a repository.
    1.  The owner reads the Keychain item locally and gives it to `connections_key_add` for the target connection (API keys screen or the owner CLI). The coordinator answers the fingerprint only.
    2.  csgo switches: its Blender MCP runbook drops `BLENDERMCP_MESHY_API_KEY` and uses the `meshy` MCP connection; its Hugging Face and Gemini scripts call `POST /v1/call/<connection>/<tool>` with the owner's connection token (`connections_token_create`, file named by `FLEET_TOKEN_FILE`).
    3.  The first call through each connection succeeds and appears as a `tool_run` with `connection_cost`.
    4.  The owner deletes the Keychain item, and a search of csgo for the item name and the old variable returns nothing.
*   **Done when:** a Meshy charge from a csgo call lands as `connection_cost` (connections C3 check, now from csgo), and `huggingface` and `google-ai` each show one recorded call.

### 2. Deleting AssetQ (S2, after S1)

One cut, no read-only period (R3.1).

1.  **Seed.** Export the crate image (AssetQ asset `2c887345…`, SHA-256 prefix `fe2077d1`) and its Meshy GLB into `config/lab/examples/fleet-uc2-crate.json` as `fleet:UC2` crate@1: `inputs` = the image; `references` = the GLB plus its measured stats (`stats.json`: triangles, components, dimensions, UV and PBR maps present). Binary files are stored as Lab artifacts by `lab-examples import`; the seed names them by path and hash. The owner merges the seed pull request.
2.  **Service.** Stop and disable `assetq.service`; remove the unit, the user `assetq`, `/srv/assetq`, the database and role `assetq`.
3.  **Routes.** Remove `/etc/caddy/sites/pipe.caddy`, `pipe.caddy.pre-c4` and `/etc/caddy/private/assetq-proof.caddy`; reload Caddy; remove the Cloudflare DNS record and Access app for `pipe.ufobe.com` and its guard entry on port 9122.
4.  **Leftover.** Remove `/home/fleet-agent/.config/fleet/connections/pipe-read`.
5.  **Tokens.** Revoke AssetQ's 4 project tokens (they die with the database; listed so nothing outside it keeps one).
6.  **Fleet.** Registry entry `assetq:datasets` becomes `obsolete`, `maps_to: fleet:UC2` (§3); fleet-lab.md drops the "removal of Pipe's method evaluation" line and stops listing assetq as a live project.
7.  **Repository.** Archive `taskqorg/assetq` (connections decision 8).

**Done when:** `curl https://pipe.ufobe.com` fails; `ss -ltn` on PDS18 shows nothing on 9460; the C4 search in fleet-connections returns nothing; `scripts/lab-drift.ts` reports assetq as `unavailable` with stored statuses unchanged; a direct `meshy` gateway call still lands as `connection_cost`.

### 3. Registry (S3)

*   **Contract change.** The use-case entry gains two fields; absent means current.

    ```json
    {"$defs": {
      "UseCaseStatus": {"type": "string", "enum": ["current", "obsolete", "duplicate", "to-design"]},
      "UseCase": {"type": "object", "additionalProperties": false,
        "required": ["id", "source", "source_status", "actor", "goal", "expected_outcome", "checks", "lab_fit", "tags", "maturity"],
        "properties": {
          "id": {"type": "string"}, "source": {"$ref": "#/$defs/UseCaseSource"},
          "source_status": {"type": "string", "enum": ["current", "changed", "missing"]},
          "status": {"$ref": "#/$defs/UseCaseStatus"},
          "maps_to": {"type": "array", "items": {"type": "string"},
            "$comment": "Fleet use case IDs that cover this entry; empty for obsolete with no successor"},
          "actor": {"type": "string"}, "goal": {"type": "string"}, "expected_outcome": {"type": "string"},
          "checks": {"type": "array", "items": {"$ref": "#/$defs/UseCaseCheck"}},
          "lab_fit": {"type": "string", "enum": ["work", "behavior", "none"]},
          "tags": {"type": "array", "items": {"type": "string"}}, "maturity": {"type": "string"}}}}}
    ```

    `UseCaseSource` and `UseCaseCheck` keep today's shapes. The schema goes into `contracts/lab-config.json`; the loader refuses a `maps_to` ID that is not a `fleet:` entry, and a `duplicate` or `obsolete` entry whose `lab_fit` is not `none`.
*   **Marks.**

    | Source | Entries | Status | `maps_to` |
    |---|---|---|---|
    | assetq | `assetq:datasets` | obsolete | `fleet:UC2` |
    | agentq | A1–A5 | obsolete | `fleet:UC5` |
    | agentq | A6, A7 (runtime failures) | duplicate | `fleet:UC3` |
    | dashq | DQ-01..05, DQ-07..10 | duplicate | `fleet:UC10` |
    | dashq | DQ-06 | duplicate | `fleet:UC8` |
    | pilotq | L01–L20 | to-design | `fleet:UC13` |
    | taskq | UC-01, 02, 03, 05, 07, 17 | duplicate | `fleet:UC8` |
    | taskq | UC-06 | duplicate | `fleet:UC7` |
    | taskq | UC-14 | duplicate | `fleet:UC10` |
    | taskq | UC-16 | duplicate | `fleet:UC12` |
    | taskq | UC-04, 12, 13 | duplicate | `fleet:UC3`, `fleet:UC11` |
    | taskq | UC-08, 09, 10, 11, 15 | obsolete | none (Fleet core behaviour, no Lab) |

    serverq and coreq have no registry files and get none.
*   **New Fleet entries** (written as sections in [use-cases.md](../use-cases.md), indexed in `config/lab/use-cases/fleet.json`):
    *   **fleet:UC12 Generate a product visual to a brief.** Sources: desiign hero and visual work (generation only), taskq UC-16. `lab_fit: work`. Pipeline `image-brief`.
    *   **fleet:UC13 Telegram channel.** A thread is a PM session with its own focus; the owner sends "look at this and do it"; agents run directly under agreed rules. Source: pilotq L01–L20. Status `to-design`, `lab_fit: none`.
*   **csgo as a source.** UC2 gains csgo's weapon and prop route (#437) and UC6 its character route (#435–#437) as extra `source` anchors. Because `source` is one object today, a use case with several sources gets one registry entry per source, ID `fleet:UC2#csgo-437`, with `maps_to: ["fleet:UC2"]` and status `current`; drift then tracks each source on its own.
*   **Seeds.** `taskq:UC-07` and `taskq:UC-16` stay valid; new versions point `use_case_id` at `fleet:UC8` and `fleet:UC12` (IDs immutable, so a new version, not an edit).
*   **Screen.** `/lab` registry shows the status and links `maps_to`; obsolete and duplicate entries are collapsed under their source.

### 4. Asset pipelines (S4)

*   **Script host.** Blender steps set `host: "<Mac host name>"` in the pipeline. This uses the existing pin: no fallback, and a host that cannot run the step fails it with the reason (fleet-lab, `script` step). No new capability mechanism; a host name in a pipeline is a definition the owner edits. Blender's path comes from the host's environment (`FLEET_BLENDER`, default `blender` on `PATH`), set in [configuration.md](../configuration.md).
*   **Scripts** live in `scripts/lab/blender/` in Fleet, run as `"$FLEET_BLENDER" --background --python <script> -- <args>`:
    *   `measure.py`: imports the GLB, writes `stats.json` `{triangles, components, dimensions_m: [x, y, z], uv: bool, pbr: {base_color, normal, roughness, metallic}}`.
    *   `render.py`: four fixed views (front, side, top, three-quarter) at 1024 px with Eevee, plus a turntable sheet; writes `renders/*.png`.
*   **`meshy-prop`** (UC2):

    ```json
    {"version":2,"id":"meshy-prop","steps":[
      {"id":"gen","kind":"api","connection":"meshy","tool":"image_to_3d","mode":"job",
       "arguments":{"image_url":"${example.inputs.image.url}","ai_model":"${variant.ai_model}","topology":"triangle",
                    "target_polycount":3000,"should_texture":true,"enable_pbr":true,"target_formats":["glb"]}},
      {"id":"fetch","kind":"script","command":"scripts/lab/fetch-model.sh ${steps.gen.output.model_urls.glb}"},
      {"id":"measure","kind":"script","host":"<mac>","command":"scripts/lab/blender/run.sh measure"},
      {"id":"render","kind":"script","host":"<mac>","command":"scripts/lab/blender/run.sh render"}]}
    ```

    The baseline copies AssetQ's `model.meshy-prop.v1` settings (model `meshy-t2`, smart topology, 3000 triangles, 2K PBR textures). Provider parameters (`ai_model`, `target_polycount`, texture settings) are variant overrides (`arguments` gains `${variant.<name>}` references, resolved like `${example.*}`), and S1 widens the `image_to_3d` input schema to list them, so an `ab` or `grid` experiment can vary them. The submit is never retried (connections Q1).
*   **`blender-agent-prop`** (UC2 alternative): an `agent` step whose profile grants the Blender MCP and `meshy`, pinned to the Mac, followed by the same `measure` and `render` steps.
*   **`image-brief`** (UC12): an `agent` step (a profile with an image tool) or an `api` step `google-ai generate_content`, followed by `scripts/lab/image-check.sh` (decodes, size, alpha, colour profile). Two variants of one pipeline, not two pipelines.
*   **Example checks** for the crate seed (`checks[]`, each a shell command over the trial's output; "checks pass with no agent" holds because the inputs alone have no `stats.json`): triangles within the example's target ± the tolerance written in the example; one component; dimensions within the reference's ratio; UV and base colour present; four renders exist.
*   **Pipeline `image_url`.** Meshy needs a URL. The coordinator resolves `${example.inputs.image.url}` to a short-lived signed artifact URL for that step only. This is a new reference form: `${example.inputs.<name>.url}`.
*   **Done when:** a UC2 `ab` experiment (two Meshy settings, owner repeats) completes on the crate seed with script scores and an owner `pick_best`.

### 5. Judges (S5)

*   **Per-use-case judges.** `judge-uc1` and `judge-uc2`, copies of `judge-scale` and `judge-pass-fail` whose prompt carries the use case's rubric. The rubric starts as an owner-approved text in the pipeline file; a rubric edit is a new pipeline version, so scores carry the judge version hash and a re-score is a new evaluator run.
*   **Inputs.** The judge gets the trial's files under `trial/`; for UC2 that means `renders/*.png` and `stats.json`, never the GLB.
*   **Order-swapped pairwise judge.** Evaluator `judge:<pipeline>:pick_best` runs the judge twice per group, in the shuffled order and in the reverse order. Agreeing answers give one `pick_best` score; disagreeing answers give no score and the rationale `order-dependent`. Both runs are kept.
*   **Agreement view.** Already defined (fleet-lab, Ratings); S5 shows it per judge version on `/lab/<experiment>`, plus pairwise agreement (owner `pick_best` vs judge `pick_best`). The owner decides when a judge runs unattended (Q3).
*   **Model.** The judge's profile and model are variants the owner chooses and calibrates; no model is fixed here.
*   **Done when:** agreement is shown for UC1 and UC2 judges on at least one experiment each.

### 6. Loop (S6)

Builds fleet-lab "Iterative improvement" as written there: kind `loop`, strategy step, generators `list`, `grid`, `propose` (with `keep_best`), allocator `full`, stop rules `max_iterations` and `no_gain {k}`. Order of use per use case: `ab`, then `grid` over two or three axes, then `loop` with `propose`. Finalists always run on held-out examples; only held-out winners are promoted.

The `propose` generator is also the "recommending judge": run once after an `ab` or `grid` experiment, its overrides are shown to the owner as proposals and never applied as a config edit.

**Done when:** a UC9 `loop` experiment stops by `no_gain`.

### 7. Character (S7)

`character-multiview` (UC6):

1.  `api google-ai generate_content`: multi-view sheet from the brief (variant: `api huggingface inference` for angle views).
2.  `script` compose: split and normalise views.
3.  `api meshy multi_image_to_3d`, `mode: job`, with `job_status` tool `multi_job_status`.
4.  `script` on the Mac: rebuild, rig and conform with csgo's Blender scripts copied into `scripts/lab/blender/` (input only; csgo stays the source of the route, not of the code).
5.  Owner `scale` rating; a judge only after S5 shows agreement for UC2.

The `api` step's `job` mode today polls the tool named `job_status`; S7 adds an optional `status_tool` field to the step so Meshy's two endpoints can be used.

**Done when:** one UC6 trial completes end to end.

### Methodology (applies to S4–S7)

*   **Examples.** Per use case a tune split and a held-out split, drafted by `draft-examples` and approved by the owner (R3.6). Counts are set per experiment.
*   **Script checks first.** Deterministic, run on every trial, each its own score.
*   **Repeats and noise.** Stochastic providers need repeats; baseline repeat spread is the "no difference" band used by `no_gain`. `infra:` trials are excluded; `unknown` is shown, never averaged.
*   **Cost.** Every trial shows `connection_cost` and wall time next to quality; never one combined score.
*   **Production feedback.** Tasks using a promoted method feed work analysis; a regression becomes a new example with `origin: task:<id>`.

## Alternatives Considered

### Script host on the Windows PC (WSL)

*   **Pros:** RTX 4070 Ti; Cycles renders on CUDA or OptiX are faster.
*   **Cons:** no Blender installed in Windows or WSL; WSL GPU rendering needs its own setup; csgo's Blender route runs on the Mac.
*   **Reason:** rejected by default (D1); measuring and previewing a low-poly prop does not need the faster GPU.

### Keep AssetQ read-only until S4 works

*   **Reason:** rejected by author: R3.1 forbids staged removal, and AssetQ has no caller; the one asset worth keeping is the seed.

### A host capability list (`requires: ["blender"]`) instead of a host pin

*   **Pros:** a pipeline names no host.
*   **Cons:** a new readiness probe, a new scheduler rule and a new contract for one host.
*   **Reason:** rejected by author until a second Blender host exists; the pin already fails clearly.

### A second `source` array in each registry entry

*   **Reason:** rejected by author: drift hashes one section per entry; one entry per source keeps `lab-drift.ts` unchanged.

### Port AssetQ's method registry

*   **Reason:** rejected by author: catalogue, Promote and `recommendation_set` already do it (fleet-connections Non-Goals).

## Risks & Mitigations

*   **Meshy charge on a lost submit.** Unchanged connections Q1 path: `recent_tasks` adoption, else `unknown:submit-unconfirmed`, never resubmitted.
*   **Mac asleep or offline.** Blender steps fail with the host reason, the trial is `infra:` and excluded; the owner retries the cell.
*   **Signed artifact URL leaks.** Valid for one step, read-only, one artifact.
*   **Judge drift after a rubric edit.** A new judge version is a new evaluator; old scores keep their version hash.
*   **Keychain deleted before csgo switched.** The deletion step requires the search in §1 step 4 to be empty first.

## Detailed Implementation Plan

Areas are named by package and responsibility, not by file, because wave 1 is renaming the Go files.

| Stage | Change | Area | Check | Needs |
|---|---|---|---|---|
| S1 | Providers `huggingface`, `google-ai`; Meshy `multi_image_to_3d`, `multi_job_status`; usage mapping for `usageMetadata` | coordinator, provider catalogue; gateway usage parsing | unit test per tool shape; one live call each | — |
| S1 | Key move per §1; csgo runbook and scripts switch | owner action; csgo repo | Meshy charge from csgo as `connection_cost`; csgo search empty | providers |
| S2 | AssetQ deletion §2; seed `fleet-uc2-crate.json` | PDS18, Cloudflare, `config/lab/examples` | §2 checks | S1 |
| S3 | `UseCase` schema with `status`, `maps_to`; loader rules; marks; UC12, UC13; csgo and desiign source entries; seed re-versions; registry screen | `contracts/lab-config.json`; coordinator Lab registry loader; `config/lab/use-cases/*`; `docs/use-cases.md`; app Lab view | `lab-drift.ts` clean; loader test refuses bad `maps_to`; `/lab` shows statuses | — |
| S4 | `scripts/lab/blender/` (`run.sh`, `measure.py`, `render.py`), `fetch-model.sh`, `image-check.sh`; pipelines `meshy-prop`, `blender-agent-prop`, `image-brief`; `${example.inputs.<name>.url}`; `FLEET_BLENDER` setting | scripts; `config/pipelines`; coordinator pipeline runner; executor host settings | UC2 `ab` with owner `pick_best` | S1, S2 |
| S5 | `judge-uc1`, `judge-uc2`; `judge:<pipeline>:pick_best` with order swap; pairwise agreement | `config/pipelines`; coordinator Lab evaluators; app Lab view | agreement shown for UC1, UC2 | S4 |
| S6 | `loop`, strategies, generators, allocator `full`, stop rules | coordinator Lab reconcile | UC9 loop stops by `no_gain` | — |
| S7 | `character-multiview`; `status_tool` on `api` job steps; character Blender scripts | `config/pipelines`; coordinator pipeline runner; scripts | one UC6 trial end to end | S4 |

S3 and S6 do not depend on S1. Docs: [fleet-lab.md](fleet-lab.md) and [fleet-connections.md](fleet-connections.md) carry the amendments this design needs; [use-cases.md](../use-cases.md) gets UC12 and UC13 in S3.

### Verification Plan

*   **Build and tests:** `pnpm run build`; `cd go && go vet ./... && go test ./...`; `tests/go/run.sh`; `scripts/lab-examples.ts check` on every seed including the crate.
*   **Live:** the "Done when" line of each stage.
*   **Secrets:** no key value appears in any commit, log, transcript, artifact or pull request; key moves are described by item and connection name only.

--------------------------------------------------------------------------------

*Drafted with EGM*
