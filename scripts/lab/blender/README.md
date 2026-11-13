# Blender pipeline programs

## Overview

Headless Blender steps process retained model and image artifacts. The scripts provide technical outputs; they do not establish owner aesthetic acceptance or game acceptance.

## Interface

- [run.sh](run.sh): `run.sh <script> [args]` selects `FLEET_BLENDER` or `blender`, starts factory settings and turns Python exceptions into failure.
- [compose-views.py](compose-views.py): `--mode fixture-sheet|gemini-sheet|url-images` retains raw inputs, normalizes declared cells/images, and emits provenance and candidates. `--reference-map` accepts only the explicitly inspected canonical fixture mapping.
- [fetch-measure.py](fetch-measure.py): downloads `gen/output.json.model_urls.glb`, or accepts `--glb`; writes `model.glb` and `stats.json` with triangles, connected surface components, dimensions, UV and material-input observations.
- [render.py](render.py): `render_set` renders declared views to PNG; `--glb`, `--blend`, `--views`, `--size`, `--front-axis` and `--poses` select inputs and views. Pose rendering requires the retained rig and guide.
- [character-build.py](character-build.py): `validate_guide`, `matrix`, `axis_point`, `zone`, `main`; validates the CT94 guide, retains provider source/rig provenance, rebuilds eight surface parts, bakes materials unless explicitly skipped, conforms against sampled capsule distances and writes `character.blend`, `character.glb` and stats. `--validate-guide-only` and `--fetch-source` are separate early-return modes.

## Invariants

- `compose-views.py:retain` preserves provenance, including failure. Candidate order alone does not establish semantic view names; the encoder in the parent directory validates a separate inspected mapping.
- `character-build.py:validate_guide` checks the 94-bone skeleton, parent order, transforms, source mapping, pose coverage and eight-part profile. Missing original poses remain unavailable.
- `character-build.py:main` requires an actual source rig/material pair and validates retained request/hash bindings for pipeline inputs. Sampled capsule/surface distances are explicitly not the original seven-pose fit oracle.
- `render.py:render_set` requires visible geometry and nonzero evaluated bounds; poses must match the supplied bone names and matrix count.

## Side Effects

These scripts replace Blender scene contents, read and write local artifacts, render images and may download HTTPS/provider model/image outputs. They can consume substantial CPU, memory and disk. `--skip-bake` leaves material qualification incomplete. Importing the CLI scripts inside Blender can execute their top-level processing.

## Verification

Use the retained example inputs and declared Blender environment for the specific pipeline step. `run.sh character-build --validate-guide-only` checks a guide when `FLEET_INPUT_DIR` and `FLEET_OUTPUT_DIR` are set. It does not qualify geometry, materials or pose acceptance. Documentation-only edits use `git diff --check`; no paid generation or rendering is needed for this bootstrap.
