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

*   **D1. Script host for Blender: the Mac.** Checked read-only on 2026-11-11: the Mac has Blender 5.2.1 LTS (`/opt/homebrew/bin/blender`, `/Applications/Blender.app`) and an Apple M2 GPU with Metal. The Windows PC has an RTX 4070 Ti, but no Blender in Windows (`C:\Program Files\Blender Foundation` absent) or in either WSL distribution. csgo's Blender route already runs on the Mac. Moving to the PC would mean installing Blender under WSL and a GPU path for Cycles, for no measured gain: measuring and previewing a 3000-triangle prop is seconds of CPU or Eevee work. Revisit when a step needs Cycles renders at volume.
*   **D2. Hugging Face and Google AI usage.** Neither offers a per-key usage endpoint like Tavily's `/usage`. Google AI calls record `connection_cost` in unit `tokens` from `usageMetadata.totalTokenCount` (input plus output, so image output is counted), basis `measured`. Hugging Face calls record basis `unavailable` unless the reply carries a cost; a `credits` or `tokens` budget on `huggingface` therefore counts nothing, and only a `calls` budget bounds it. Reconcile is not built for either; the API keys screen shows "not reconciled" for them, not "matched" (fleet-connections Q2).
*   **D3. UC4 fan-out.** Stays out. UC4 keeps `lab_fit: none` until a pipeline needs fan-out and the owner reopens the Lab decision.

## Technical Plan (Architecture)

```text
 csgo scripts (Mac) ──HTTP + per-tool owner token──┐
 Lab agent step ──MCP──────────────────────────────┼──▶ fleet-gateway (PDS18) ──▶ Meshy / Hugging Face / Google AI
 Lab api step (coordinator) ───────────────────────┘          │ spool ─▶ tool_run + connection_cost
                                                              │
 meshy-prop:  script encode ─▶ api image_to_3d (job) ─▶ script fetch+measure+render (host: Mac) ─▶ evaluators
 image-brief: agent | api google-ai ─▶ script image checks ─▶ evaluators
 evaluators: script:example.checks ─▶ owner rating ─▶ judge with UC rubric (renders) ─▶ agreement view
```

### 1. Keys and providers (S1)

*   **Providers.** The provider catalogue (today `go/coordinator/c_providers.go`) gains `huggingface` and `google-ai`; Meshy gains the multi-image tools. Each tool's `inputSchema` is the provider's request body, passed through.

    | Provider | Tool | Method and path | Unit | Idempotent |
    |---|---|---|---|---|
    | `meshy` | `multi_image_to_3d` | `POST /openapi/v1/multi-image-to-3d` | `credits` | no |
    | `meshy` | `multi_job_status` | `GET /openapi/v1/multi-image-to-3d/{jobId}` | `credits` | yes |
    | `meshy` | `multi_recent_tasks` | `GET /openapi/v1/multi-image-to-3d?sort_by=-created_at` | `credits` | yes |
    | `huggingface` | `inference` | `POST /models/{model}` on the Inference Providers router | `requests` | no |
    | `google-ai` | `generate_content` | `POST /v1beta/models/{model}:generateContent` | `tokens` | no |

    Meshy's `image_to_3d` input schema is widened to list `ai_model`, `topology`, `target_polycount`, `enable_pbr` and `texture_resolution`, so variants can set them. No image tool is idempotent, so none fails over (connections §2). Gemini and Hugging Face calls are `sync`; Meshy stays `job`.
*   **Path fields.** A `{field}` in a catalogue path is taken from the body, removed from the forwarded body, and checked against a pattern the catalogue entry declares (`pathPatterns: {model: "^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$", jobId: "^[0-9a-f-]+$"}`); a value that does not match is refused with `bad-request` before a key is attached. This applies to Meshy's existing `{jobId}` too.
*   **Key move, per key, owner only.** The value never passes through an agent, a terminal transcript, a command line or a repository file.
    1.  The owner reads the Keychain item locally and pipes it to `connections_key_add` for the target connection; the operation reads the value from stdin or a no-echo prompt and refuses it as an argument. It answers the fingerprint only.
    2.  csgo switches. Blender MCP makes its own Meshy calls with `BLENDERMCP_MESHY_API_KEY` and cannot be pointed at a Fleet MCP server, so its Meshy feature is turned off: an agent calls `mcp__meshy__image_to_3d` and imports the GLB through Blender MCP. csgo's Hugging Face and Gemini scripts call `POST /v1/call/<connection>/<tool>` with an owner connection token (`connections_token_create`), one token per tool (`csgo-blender`, `csgo-scripts`) with only the connections it needs, so `tokenName` attributes each call.
    3.  The first call through each connection succeeds and appears as a `tool_run` with `connection_cost`.
    4.  A nightly backup has run after the add, and its `secrets.sops.json` lists that fingerprint. Only then the owner deletes the Keychain item, and a search of csgo for the item name and the old variable returns nothing.
*   **Done when:** a Meshy charge from a csgo call lands as `connection_cost` with its `tokenName`; csgo's Blender work runs with `BLENDERMCP_MESHY_API_KEY` unset; `huggingface` and `google-ai` each show one recorded call.

### 2. Deleting AssetQ (S2, after S1)

One cut, no read-only period (R3.1).

1.  **Seed.** Export the crate image (AssetQ asset `2c887345…`, SHA-256 prefix `fe2077d1`) and its Meshy GLB into `config/lab/examples/fleet-uc2-crate.json` as `fleet:UC2` crate@1, using the binary example file form (§4): `inputs` = the image; `references` = the GLB, its `stats.json` and its four renders, produced by running S4's `measure` and `render` scripts on the GLB. The baseline Meshy arguments are copied from AssetQ's `model.meshy-prop.v1` method row into the seed pull request description.
2.  **Gate.** `lab-examples import` returned an artifact ID for crate@1; the stored GLB's SHA-256 equals the AssetQ object's; `lab-examples check` passes on the seed. Only then step 3.
3.  **Service.** Stop and disable `assetq.service`; remove the unit, the user `assetq`, `/srv/assetq`, the database and role `assetq`.
4.  **Routes.** Remove `/etc/caddy/sites/pipe.caddy`, `pipe.caddy.pre-c4` and `/etc/caddy/private/assetq-proof.caddy`; reload Caddy; remove the Cloudflare DNS record and Access app for `pipe.ufobe.com` and its guard entry on port 9122.
5.  **Leftover.** Remove `/home/fleet-agent/.config/fleet/connections/pipe-read`.
6.  **Tokens.** AssetQ's 4 project tokens die with its database; nothing outside it holds one.
7.  **Fleet.** Registry entry `assetq:datasets` becomes `obsolete`, `maps_to: ["fleet:UC2"]` (§3); fleet-lab.md drops the AssetQ "Not built" line and stops listing assetq as a live project.
8.  **Repository.** Archive `taskqorg/assetq` (connections decision 8).

**Done when:** `curl https://pipe.ufobe.com` fails; `ss -ltn` on PDS18 shows nothing on 9460; the C4 search in fleet-connections returns nothing; `scripts/lab-drift.ts` reports assetq `unavailable` with stored statuses unchanged; a direct `meshy` gateway call still lands as `connection_cost`.

### 3. Registry (S3)

*   **First use-case schema.** Today no schema exists: the loader (`useCases()` in the coordinator Lab area) parses JSON and checks nothing, and the only typed shape is `Source` in `scripts/lab-drift.ts`. S3 adds `contracts/lab-registry.schema.json` (generated types, `contracts/README.md` subset) and validates every entry in `useCases()`; an invalid file fails the registry load with the entry ID and reason.

    ```json
    {"$defs": {
      "UseCaseStatus": {"type": "string", "enum": ["current", "obsolete", "duplicate", "to-design"]},
      "LabFit": {"type": "string", "enum": ["work", "behavior", "none"]},
      "SourceStatus": {"type": "string", "enum": ["current", "changed", "missing"]},
      "UseCaseSource": {"type": "object", "additionalProperties": false,
        "required": ["repo", "path", "anchor", "commit", "sha256"],
        "properties": {"repo": {"type": "string"}, "path": {"type": "string"}, "anchor": {"type": "string"},
                       "commit": {"type": "string"}, "sha256": {"type": "string"}}},
      "UseCaseCheck": {"type": "object", "additionalProperties": false, "required": ["kind", "ref"],
        "properties": {"kind": {"type": "string"}, "ref": {"type": "string"}, "metric": {"type": "string"}}},
      "UseCase": {"type": "object", "additionalProperties": false,
        "required": ["id", "source", "source_status", "actor", "goal", "expected_outcome", "checks", "lab_fit", "tags", "maturity"],
        "properties": {
          "id": {"type": "string"}, "source": {"$ref": "#/$defs/UseCaseSource"},
          "source_status": {"$ref": "#/$defs/SourceStatus"},
          "status": {"$ref": "#/$defs/UseCaseStatus"},
          "maps_to": {"type": "array", "items": {"type": "string"},
            "$comment": "fleet:UCn entries (no #) that this entry belongs to or is covered by"},
          "actor": {"type": "string"}, "goal": {"type": "string"}, "expected_outcome": {"type": "string"},
          "checks": {"type": "array", "items": {"$ref": "#/$defs/UseCaseCheck"}},
          "lab_fit": {"$ref": "#/$defs/LabFit"},
          "tags": {"type": "array", "items": {"type": "string"}}, "maturity": {"type": "string"}}}}}
    ```

    `UseCaseCheck` is the evaluator reference the current files carry (`{kind, ref, metric}`, for example `script`, `example.checks`, `pass_fail`); S3 adds any other field found in them. Loader rules: a `maps_to` target must be an existing `fleet:UCn` ID without `#`; `status` absent means current.
*   **`maps_to`, one meaning:** the Fleet use cases this entry belongs to or is covered by. For an obsolete or duplicate entry it names the successor; for a per-source entry (below) it names its parent.
*   **Marks.** Marking an entry `obsolete`, `duplicate` or `to-design` also sets its `lab_fit` to `none` in the same edit (today 8 taskq entries and `assetq:datasets` are `work`, one dashq entry is `behavior`).

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
*   **New Fleet entries** (sections in [use-cases.md](../use-cases.md), indexed in `config/lab/use-cases/fleet.json`):
    *   **fleet:UC12 Generate a product visual to a brief.** Sources: desiign hero and visual work (generation only), taskq UC-16. `lab_fit: work`. Pipeline `image-brief`.
    *   **fleet:UC13 Telegram channel.** A thread is a PM session with its own focus; the owner sends "look at this and do it"; agents run directly under agreed rules. Source: pilotq L01–L20. Status `to-design`, `lab_fit: none`.
*   **csgo and desiign as sources.** `source` is one object, so each extra source of a Fleet use case is its own entry: `fleet:UC2#csgo-437` (weapon and prop route), `fleet:UC6#csgo-435` (character route), `fleet:UC12#desiign-hero`, each `maps_to` its parent, status `current`. Drift for these runs locally with `FLEET_LAB_REPOS` pointing at the checkouts; in CI csgo and desiign are not checked out and report `unavailable`, which leaves stored statuses alone.
*   **Seeds.** `taskq:UC-07` and `taskq:UC-16` stay valid examples; new versions point `use_case_id` at `fleet:UC8` and `fleet:UC12` (IDs immutable, so a new version, not an edit).
*   **Screen.** `labRegistry` (coordinator Lab area) copies `status` and `maps_to` into its reply; `/lab` shows the status, links `maps_to`, and collapses obsolete and duplicate entries under their source.

### 4. Asset pipelines (S4)

*   **Binary example files.** Lab examples are text only today (`ExampleFile {path, text}` in `scripts/lab-example-format.ts`; inputs staged as `text/plain`). S4 adds a second form `{path, sha256, media_type}`: `lab-examples import` uploads the file next to the seed (`config/lab/examples/<example id>/<path>`) as a Lab artifact and verifies the hash; staging writes the stored bytes with their media type; the "checks pass with no agent" check works the same. fleet-lab's example contract is amended to match.
*   **Image to Meshy.** Meshy fetches `image_url` from its own servers, and every Fleet route outside loopback is behind Cloudflare Access, so no Fleet URL is reachable from Meshy. Meshy accepts a `data:` URI in `image_url`, so a first `script` step `encode` reads the example image from `FLEET_INPUT_DIR/example/` and writes `output.json {"data_uri": "data:image/png;base64,…"}`. No new reference form and no public route.
*   **Variant arguments.** fleet-lab's override list gains `arguments` for `api` steps: `{<step id>: {<argument>: value}}`, merged over the step's `arguments` before references are resolved and hashed into `config_ref` with the other overrides. Pipelines carry no variant references.
*   **Script host.** Blender steps set `host` to the Mac's host ID (the value `hosts_list` shows). This uses the existing pin: no fallback, and a host that cannot run the step fails it with the reason. Blender's path comes from the host's environment (`FLEET_BLENDER`, default `blender` on `PATH`), documented in [configuration.md](../configuration.md). Files move between hosts as step artifacts: each step's outputs are uploaded, and a later step on any host receives them in `FLEET_INPUT_DIR/<step id>/`.
*   **Scripts** in `scripts/lab/` in Fleet:
    *   `encode.sh`: the data URI above.
    *   `blender/run.sh <script> [args]`: runs `"$FLEET_BLENDER" --background --python blender/<script>.py -- [args]`.
    *   `blender/fetch-measure.py`: downloads the GLB from the URL in `FLEET_INPUT_DIR/gen/output.json` (`model_urls.glb`) to `$FLEET_OUTPUT_DIR/model.glb`, imports it and writes `$FLEET_OUTPUT_DIR/stats.json` `{triangles, components, dimensions_m: [x, y, z], uv: bool, pbr: {base_color, normal, roughness, metallic}}`.
    *   `blender/render.py --views <list> --size <px>`: renders `FLEET_INPUT_DIR/measure/model.glb` to `$FLEET_OUTPUT_DIR/renders/<view>.png` with Eevee. Views and size are pipeline arguments; the first pipeline uses `front,side,top,three-quarter` and 1024.
    *   `image-check.sh`: decodes the image and extracts size, alpha and colour profile from the image bytes. Execution receives inputs only; `script:example.checks` compares `image.json` with the example’s explicit requirements at the evaluator boundary. The separate `fleet-uc12-bitmap-pilot` synthetic fixture checks 1024×1024 opaque sRGB PNG output without provider calls; it qualifies the technical image path, not vector artwork or a product rubric.
    *   Output layout: everything a step writes under `$FLEET_OUTPUT_DIR` is a trial output; `script:example.checks` sees each step's files at the top level, later steps winning (fleet-lab, Ratings), so checks read `stats.json`, `model.glb` and `renders/*.png`.
*   **`meshy-prop`** (UC2):

    ```json
    {"version":2,"id":"meshy-prop","steps":[
      {"id":"encode","kind":"script","command":"scripts/lab/encode.sh"},
      {"id":"gen","kind":"api","connection":"meshy","tool":"image_to_3d","mode":"job",
       "arguments":{"image_url":"${steps.encode.output.data_uri}","target_formats":["glb"]}},
      {"id":"measure","kind":"script","host":"<Mac host ID>","command":"scripts/lab/blender/run.sh fetch-measure"},
      {"id":"render","kind":"script","host":"<Mac host ID>","command":"scripts/lab/blender/run.sh render --views front,side,top,three-quarter --size 1024"}]}
    ```

    The baseline variant's `arguments` override for `gen` copies AssetQ's `model.meshy-prop.v1` settings (model, topology, 3000 triangles, PBR, 2K textures) as recorded in the seed pull request; other variants change them. The submit is never retried (connections Q1).
*   **`image-brief`** (UC12): an `agent` step (a profile with an image tool) or an `api` step `google-ai generate_content`, followed by `scripts/lab/image-check.sh`. Two variants of one pipeline.
*   **Example checks** for the crate seed: triangles within the example's target and tolerance (written in the example); one component; dimensions within the reference's ratio; UV and base colour present; four renders exist. On inputs alone none of these files exist, so at least one check fails, as Lab requires.
*   **Retry cost.** "Retry cell" reruns the whole pipeline, so retrying a trial whose Mac step failed submits to Meshy again and is charged again. `infra:` trials are excluded from comparison but their `tool_run` rows count toward `credits` budgets, and the matrix shows their spend. The owner sees this before retrying; step-level resume is not built.
*   **Done when:** a UC2 `ab` experiment (two Meshy settings as `arguments` overrides, owner repeats) completes on the crate seed with script scores and an owner `pick_best`.

### 5. Judges (S5)

*   **Rubrics as prompts.** The generic `judge-pass-fail` and `judge-scale` pipelines stay. Each use case's rubric is an owner-approved file `config/lab/rubrics/<use case>.md` (criteria, anchor text per scale point, pass and fail examples from owner-rated trials), passed to the judge as its `prompt` override. A rubric edit changes the variant's `config_ref`, so scores carry the rubric version and a re-score is a new evaluator run.
*   **Inputs.** The judge gets the trial's files under `trial/`; for UC2 that means `renders/*.png` and `stats.json`, not the GLB.
*   **Order-swapped pairwise judge.** Evaluator `judge:<pipeline>:pick_best` runs the judge twice per group, in the shuffled order and in the reverse order. Agreeing answers give one `pick_best` score; disagreeing answers give no score and the rationale `order-dependent`. Both runs are kept.
*   **Agreement view.** Already defined (fleet-lab, Ratings); S5 shows it per rubric version on `/lab/<experiment>`, plus pairwise agreement (owner `pick_best` vs judge `pick_best`). The owner decides when a judge runs unattended (Q3).
*   **Model.** The judge's profile and model are variants the owner chooses and calibrates.
*   **Done when:** agreement is shown for UC1 and UC2 rubrics on at least one experiment each.

### 6. Loop (S6)

Builds fleet-lab "Iterative improvement" as written there; this design adds nothing to it. **Done when:** a UC9 `loop` experiment stops by `no_gain`.

### 7. Character (S7)

`character-multiview` (UC6), the fixed route csgo #435–#437 found; UC6's conversational refinement stays outside the pipeline, and this pipeline is one tool an operator uses in it:

1.  `api google-ai generate_content`: multi-view sheet from the brief (variant: `api huggingface inference` for angle views).
2.  `script` compose: split and normalise views, write data URIs.
3.  `api meshy multi_image_to_3d`, `mode: job`.
4.  `script` on the Mac: rebuild, rig and conform with Blender scripts in `scripts/lab/blender/`, written for Fleet from csgo's route (csgo stays the source of the route, not of the code).
5.  Owner `scale` rating; a judge only after S5 shows agreement for UC2.

Preparation methods are selected explicitly. `character-multiview` uses Google AI, `character-multiview-hf` uses the Hugging Face angle route, and `character-multiview-meshy-prep` uses Meshy Image to Image (`nano-banana-2`, `generate_multi_view: true`) through the existing connection and project grants. The Meshy preparation method is neither an automatic fallback after a provider failure nor an owner-selected default.

Job steps may set `status_tool` and `recent_tool`; absent fields retain the existing single-image and multi-image status/list defaults. Both read tools must be granted idempotent GET tools before submission. Catalogue `adoptMatch` names the primary submitted identity field; `adoptFields` names additional submitted fields that must be returned unchanged. A lost submit is adopted only from one unowned task created during the original attempt with exact input identity. Meshy preparation requires the reference images, prompt, model and preparation mode; generated `image_urls` do not establish the submitted reference identity. Missing inputs, ambiguous matches or an unreadable listing leave `unknown:submit-unconfirmed`; Fleet never sends a second POST. Known job IDs use their status tool. Retain the provider receipt, every returned view and actual consumed credits; finished-job accounting remains once per job.

The initial Meshy method declares front, left and back as exactly three views; this is a method parameter, not a global character limit. Retain all downloaded bytes and URLs with hashes, dimensions and media types. Missing or extra views fail visibly before charged downstream generation without discarding evidence. Sheet-grid extraction and separate returned images are distinct input modes. A mapping inspection reads actual image bytes against the references and records each source index/hash, semantic view, rationale and operator-verified or agent-inspected basis in `view-map.json`. Provider array order never implies semantic order. Unknown or conflicting mappings stop preparation. The encoder independently verifies hashes, dimensions and the one-to-one expected view names/count before constructing the downstream request. Resizing records its parameters and does not imply original resolution. Mapping annotations are not quality scores or owner labels.

Rebuild, rig and conform remain Fleet Blender implementations of the source route, not imported csgo scripts. Retain topology/part/UV/material statistics, rig and bind evidence, conform measurements and renders, including defects. Successful preparation or generation proves transport only; trial completion does not establish game readiness or aesthetic acceptance. The actual owner scale rating remains separate from a judge result.

**Done when:** one UC6 trial completes end to end.

### Methodology

Examples, splits, repeats, noise, cost and evaluator layers follow [fleet-lab.md](fleet-lab.md) unchanged.

## Alternatives Considered

### Script host on the Windows PC (WSL)

*   **Pros:** RTX 4070 Ti; Cycles renders on CUDA or OptiX are faster.
*   **Cons:** no Blender installed in Windows or WSL; WSL GPU rendering needs its own setup; csgo's Blender route runs on the Mac.
*   **Reason:** rejected by default (D1); measuring and previewing a low-poly prop does not need the faster GPU.

### A signed artifact URL for Meshy

*   **Reason:** rejected by author: Meshy cannot reach Access-guarded routes, and a public route would need its own expiry and replay design; a `data:` URI needs neither.

### Keep AssetQ read-only until S4 works

*   **Reason:** rejected by author: R3.1 forbids staged removal, and AssetQ has no caller; the one asset worth keeping is the seed, gated before deletion.

### A host capability list (`requires: ["blender"]`) instead of a host pin

*   **Pros:** a pipeline names no host.
*   **Cons:** a new readiness probe, a new scheduler rule and a new contract for one host.
*   **Reason:** rejected by author until a second Blender host exists; the pin already fails clearly.

### A Blender agent pipeline for UC2 (`blender-agent-prop`)

*   **Reason:** deferred: agent steps cannot pin a host today, and S4's comparison is between Meshy settings. Add it with a host override for agent steps when UC2 compares routes.

### Per-use-case judge pipelines

*   **Reason:** rejected by author: a copy per use case; a rubric as a `prompt` override is versioned by `config_ref` already.

### Port AssetQ's method registry

*   **Reason:** rejected by author: catalogue, Promote and `recommendation_set` already do it (fleet-connections Non-Goals).

## Risks & Mitigations

*   **Meshy charge on a lost submit.** Connections Q1 path: adoption from recent tasks, else `unknown:submit-unconfirmed`, never resubmitted. S7 extends it to multi-image.
*   **Mac asleep or offline.** Blender steps fail with the host reason, the trial is `infra:` and excluded; its Meshy spend still counts, and a retry is charged again.
*   **Large `data:` URI.** The image travels in the call body once per submit; the spool records arguments without the image body.
*   **Path injection through `{model}`.** Path fields are pattern-checked before a key is attached (§1).
*   **Owner token on the Mac.** One token per csgo tool, fewest connections each; the owner may set a `stop` budget on `meshy`, the scope the gateway enforces today.
*   **Rubric drift.** A rubric edit is a new `config_ref`; old scores keep theirs.
*   **Keychain deleted too early.** Deletion waits for a good call, a backup listing the fingerprint and an empty csgo search (§1).
*   **Seed lost with AssetQ.** Deletion waits for import, hash match and check (§2).

## Detailed Implementation Plan

Files are named as they exist today; wave 1 is renaming the Go files, so a renamed file keeps the responsibility named here.

| Stage | Change | Files (C create, M modify, D delete) | Check | Needs |
|---|---|---|---|---|
| S1 | Providers `huggingface`, `google-ai`; Meshy `multi_*` tools; widened `image_to_3d` schema; `pathPatterns`; `totalTokenCount` usage | M `go/coordinator/c_providers.go`; M `go/gateway` (path field check, usage parsing) | unit test per tool shape and per pattern; one live call each | — |
| S1 | `connections_key_add` reads stdin or prompt only | M `go/coordinator/c_connections.go` | an argument value is refused | — |
| S1 | Key move §1; csgo switch | owner action; csgo repo | §1 done-when | providers |
| S2 | AssetQ deletion §2; seed | C `config/lab/examples/fleet-uc2-crate.json` and `fleet-uc2-crate/*` | §2 gate and done-when | S1, S4 binary examples and scripts |
| S3 | Use-case schema, validation, marks, UC12/UC13, per-source entries, seed re-versions, registry screen | C `contracts/lab-registry.schema.json`; M `go/coordinator/c_lab.go` (`useCases`, `labRegistry`); M `config/lab/use-cases/*.json`; M `docs/use-cases.md`; M `config/lab/examples/taskq-uc07-plan-split.json`, `taskq-uc16-tray-icon.json` (new versions); M `app/src/views/lab.tsx`; M `scripts/lab-drift.ts` (types from schema) | registry loads; a bad `maps_to` is refused; `/lab` shows statuses | — |
| S4 | Binary example files | M `scripts/lab-example-format.ts`, `scripts/lab-examples.ts`; M `go/coordinator/c_pipelines.go` (`inputsFor`) | crate seed imports and passes `check` | — |
| S4 | `arguments` override for `api` steps | M `go/coordinator/c_pipelines.go` (`runStep`, override merge); M `docs/designs/fleet-lab.md` | `config_ref` changes with arguments; test | — |
| S4 | Scripts and pipelines | C `scripts/lab/encode.sh`, `scripts/lab/blender/{run.sh,fetch-measure.py,render.py}`, `scripts/lab/image-check.sh`, `config/pipelines/meshy-prop.json`, `config/pipelines/image-brief.json`; M `docs/configuration.md` (`FLEET_BLENDER`) | UC2 `ab` with owner `pick_best` | S1 |
| S5 | Rubrics, order-swapped pairwise judge, agreement per rubric | C `config/lab/rubrics/fleet-uc1.md`, `fleet-uc2.md`; M `go/coordinator/c_lab.go` (evaluators); M `app/src/views/lab.tsx` | agreement shown for UC1, UC2 | S4 |
| S6 | `loop` per fleet-lab | M `go/coordinator/c_lab.go` | UC9 loop stops by `no_gain` | — |
| S7 | `character-multiview`; `status_tool`, `recent_tool`, `adoptMatch` | C `config/pipelines/character-multiview.json`, character scripts in `scripts/lab/blender/`; M `go/coordinator/pipelines.go`, `providers.go` | one UC6 trial end to end | S4 |

S2 needs the S4 binary example form and the measure and render scripts for the seed's references; it does not need the S4 experiment. S3 and S6 do not depend on S1.

### Verification Plan

*   **Build and tests:** `pnpm run build`; `cd go && go vet ./... && go test ./...`; `tests/go/run.sh`; `scripts/lab-examples.ts check` on every seed including the crate.
*   **Live:** the "Done when" line of each stage.
*   **Secrets:** no key value appears in any commit, log, transcript, artifact, command line or pull request; key moves are described by item and connection name only.

--------------------------------------------------------------------------------

*Drafted with EGM*
