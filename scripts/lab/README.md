# Lab script steps

## Overview

Programs shipped with releases for retained pipeline steps. They consume `FLEET_INPUT_DIR` and write artifacts under `FLEET_OUTPUT_DIR`; the coordinator owns scheduling and state.

## Interface

- [character-views.py](character-views.py): `read_inside`, `png_dimensions`, `encode_views`; CLI binds inspected views to source hashes and constructs a provider request in `output.json` using `--request-kind`, `--expected` and `--max-request-bytes`.
- [encode.sh](encode.sh): encodes the first existing PNG/JPEG input under `example/` as `output.json.data_uri`; large inputs use macOS `sips` to resize.
- [image-check.sh](image-check.sh): decodes the first inline image in `gen/output.json`; writes image bytes and decoded `image.json` metadata using `sips`.
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
