#!/bin/sh
# The executor protocol scripts CI runs (from the repository root, after cargo build -p fleet-executor). Not listed:
# native_probe.py (opt-in real subscription probe) and lab_provenance.py (inspects one retained run, needs arguments).
set -eu
for t in antigravity_acp claude_mcp codex_wire delegation_approval delegation_protocol fake_integration \
         lab_protocol project_protocol; do
  echo "== $t"
  out=$(python3 "tests/executor/$t.py" 2>&1) || { echo "$out"; echo "FAILED: $t"; exit 1; }
  echo "$out" | tail -c 400; echo
done
# The executor's MCP adapter from Node: replayed stdio exchanges and artifact path confinement.
node --test tests/executor/mcp-replay.test.ts tests/executor/artifact-tools.test.ts
