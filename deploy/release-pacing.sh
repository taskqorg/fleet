#!/bin/sh
# Prints the pacing fields of a native release as JSON, {"parts":{...},"executor":{"changedIn","urgentIn"}}
# (docs/designs/fleet-host-updates.md section 7):
#   deploy/release-pacing.sh <commit> <version> <previous version or ""> <immediate|normal>
# Run in a checkout of the commit. The previous release's fields come from its latest.json asset (GH_TOKEN, GH_REPO); a
# previous release whose latest.json has no parts counts as a changed host part. A failed download fails the run: a
# silent reset would drop urgentIn.
set -eu
commit=$1 version=$2 previous=$3 class=$4
parts=$("$(dirname "$0")/release-parts.sh" "$commit")
prev='{}'
if [ -n "$previous" ]; then
  prev=$(gh release download "v$previous" -p latest.json -O -) || { echo "release-pacing: latest.json of v$previous unreadable" >&2; exit 1; }
fi
jq -nc --argjson parts "$parts" --argjson prev "$prev" --arg v "$version" --arg class "$class" '
  def host(p): [p.executor, p.cliPins, p.toolPins];
  ($prev.parts // null) as $pp
  | ($pp == null or host($pp) != host($parts)) as $changed
  | {parts: $parts, executor: {
      changedIn: (if $changed or ($prev.executor.changedIn // "") == "" then $v else $prev.executor.changedIn end),
      urgentIn: (if $changed and $class == "immediate" then $v else ($prev.executor.urgentIn // "") end)}}'
