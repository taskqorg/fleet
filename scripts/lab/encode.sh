#!/bin/sh
# Writes output.json {"data_uri": "data:<type>;base64,..."} for the one image in FLEET_INPUT_DIR/example/, so Meshy
# receives the image inline (docs/designs/fleet-asset-pipelines.md §4).
set -eu
set -- "$FLEET_INPUT_DIR"/example/*.png "$FLEET_INPUT_DIR"/example/*.jpg "$FLEET_INPUT_DIR"/example/*.jpeg
for f; do [ -f "$f" ] && break; done
[ -f "$f" ] || { echo "no image in $FLEET_INPUT_DIR/example" >&2; exit 1; }
case $f in *.png) type=image/png ;; *) type=image/jpeg ;; esac
{ printf '{"data_uri":"data:%s;base64,' "$type"; base64 < "$f" | tr -d '\n'; printf '"}\n'; } > "$FLEET_OUTPUT_DIR/output.json"
