# Lab script steps

## Overview

Programs shipped with releases for retained pipeline steps. They consume `FLEET_INPUT_DIR` and write artifacts under `FLEET_OUTPUT_DIR`; the coordinator owns scheduling and state.

## Interface

- [character-views.py](character-views.py): `read_inside`, `png_dimensions`, `encode_views`; CLI binds inspected views to source hashes and constructs a provider request in `output.json` using `--request-kind`, `--expected` and `--max-request-bytes`.
- [encode.sh](encode.sh): encodes the first existing PNG/JPEG input under `example/` as `output.json.data_uri`; large inputs use macOS `sips` to resize.
- [image-check.sh](image-check.sh): decodes the first inline image in `gen/output.json`; writes image bytes and decoded `image.json` metadata using `sips`.
- [avatar.py](avatar.py): the `avatar-make` pipeline's steps: `request` (the Gemini image request from the example's description and optional reference), `check` (a 512 px square still `avatar-still.png`, `sips`), `animate` (an animated SVG `avatar.svg`: the still plus a CSS-breathing light at its brightest region) and `result` (the result lineage and `projectResult` {avatar, still, glow} that `bot_avatar_set` takes).
- [video.py](video.py): the `video-make` pipeline's steps: `request` (the Interactions API body for Gemini Omni Flash from the example's `prompt`, optional first-frame `image`, `aspect_ratio` 16:9 or 9:16, `seconds` 3 to 15), `save` (the finished interaction's video as `clip.mp4`) and `result` (the result lineage and `projectResult` {video}). The `video` step between them is a `job` api step: `interaction_create` in the background, polled with `interaction_get`.
- [doc_recognize.py](doc_recognize.py): the `doc-recognize-*` pipelines' steps (DAM model documents, [use case](../../docs/benchmarks/dam-use-cases.md#document-recognition)): `prompt` (the shared prompt for an agent step that reads the example images itself; a PDF is given as one JPEG per page, up to 12, rendered with PDFKit through `swift` on the macOS host), `request` (an Anthropic, OpenAI or Gemini request body with the prompt and the images scaled with `sips` under the gateway's 1 MiB; `--only` keeps identity documents for a second pass) and `finish` (the JSON object out of an agent's output or any provider reply, normalized to the result shape, with `summary`, one line naming the result, such as `KATARZYNA NOWAK · 5 files · 1 unreadable`, or every person's name when the files name several (`A & B · 5 files · 2 people`); an ID's back side carries `side: back` and `front` (its front's file) and takes its front's name and date of birth; identity documents never carry a `site`; documents carry `id_number` (normalized: letters and digits only) and a selfie `same_person_as` (the ID whose photo shows the same face, compared only within the run's own files); a form with an ID's number, or a selfie pointing at an ID, takes that ID's name and date of birth and joins its person; `--merge` joins a two-step run).
- [harness-capability.py](harness-capability.py): reads `example/binding.json`, invokes the release executor's `qualify-mcp` in a temporary fixture, and retains its capability receipt without invoking a model.
- [loop-strategy.py](loop-strategy.py): expands the configured `list` or `grid` snapshot to `strategy.json` and `output.json`.
- [blender/](blender/README.md): source retention, model measurement, rebuild and rendering.

## Invariants

- `character-views.py:encode_views` requires explicit inspection basis/rationale and matching source/view hashes, rejects escaped paths, validates normalized RGB8 PNG bytes, and checks the encoded request size before output. It does not submit the paid request.
- `image-check.sh` fails for missing decoded dimensions, alpha or profile instead of inventing values.
- `harness-capability.py` requires an absolute registry path and asserts the receipt reports no model invocation and a passing symbol witness.
- `loop-strategy.py` supports only list and grid; other generator kinds fail.

## Side Effects

Read retained inputs, create image/JSON artifacts, spawn local tools and temporary fixtures. Capability qualification invokes the declared MCP server through the executor. Blender children may download provider outputs; see their contract. The encoding/image metadata scripts require macOS `sips`.

## Verification

From the repository root: `python3 tests/assets/character-views.test.py`; use the actual focused fixtures listed in [tests](../../tests/README.md) for the step changed. Rendering and MCP qualification need their declared host tools and fixtures; a documentation check does not establish those capabilities.
