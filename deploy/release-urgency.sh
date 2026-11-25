#!/bin/sh
# Classifies a release batch for host pacing (docs/designs/fleet-host-updates.md section 7):
#   deploy/release-urgency.sh <previous release commit> <release commit>
# Prints "immediate <reason>" or "normal". Immediate when a pull request merged in the batch has the label release:now or
# security, or a host protocol schema changed. Needs GH_TOKEN with pull-requests read for the labels.
set -eu
from=$1 to=$2
changed=$(git diff --name-only "$from" "$to" -- contracts/host-protocol.schema.json contracts/host-files.schema.json)
if [ -n "$changed" ]; then echo "immediate host protocol: $(echo $changed)"; exit 0; fi
# Merge queue merges ("Merge pull request #N from ...") and squash merges ("... (#N)") on the first-parent line.
prs=$(git log --first-parent --format=%s "$from..$to" | sed -nE 's/^Merge pull request #([0-9]+) .*/\1/p; s/.*\(#([0-9]+)\)$/\1/p' | sort -un)
for n in $prs; do
  labels=$(gh pr view "$n" --json labels -q '.labels[].name') || { echo "release-urgency: labels of #$n unreadable" >&2; continue; }
  for l in $labels; do
    case $l in release:now|security) echo "immediate #$n $l"; exit 0 ;; esac
  done
done
echo normal
