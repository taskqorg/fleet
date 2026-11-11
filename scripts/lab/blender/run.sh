#!/bin/sh
# Runs one Lab Blender script headless (docs/designs/fleet-asset-pipelines.md §4): run.sh <script> [args].
# Blender is FLEET_BLENDER, else blender on PATH.
set -eu
script=$1; shift
exec "${FLEET_BLENDER:-blender}" --background --factory-startup --python "$(dirname "$0")/$script.py" -- "$@"
