#!/usr/bin/env bash
# Orchestrator side of docs/designs/fleet-delivery-pipeline.md#8-workers-merge-and-watch-their-own-pull-requests:
# a worker stops after enabling auto-merge, so nobody else sees a landing fail. This runs scripts/watch-landing.sh for
# every open pull request with auto-merge on or in the merge queue (gh pr merge --auto on a clean pull request enqueues it at once and clears autoMergeRequest), and every one merged in the last <minutes> (default 60), all at once.
# Usage: scripts/watch-landings.sh [minutes] [timeout-minutes, default 45]
# Prints "#<n> <last line of watch-landing.sh>" per pull request as each finishes; exit 1 if any did not end on hosts/site.
set -uo pipefail
cd "$(dirname "$0")/.."
since=$(date -u -v-"${1:-60}"M +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d "-${1:-60} min" +%Y-%m-%dT%H:%M:%SZ)
prs=$( { gh pr list -R taskqorg/fleet --state open --json number,autoMergeRequest -q '.[] | select(.autoMergeRequest) | .number'
         gh api graphql -f query='{repository(owner:"taskqorg",name:"fleet"){mergeQueue(branch:"main"){entries(first:100){nodes{pullRequest{number}}}}}}' \
           -q '.data.repository.mergeQueue.entries.nodes[].pullRequest.number'
         gh pr list -R taskqorg/fleet --state merged --search "merged:>=$since" --json number -q '.[].number'; } | sort -un)
[ -n "$prs" ] || { echo "no pull request to watch"; exit 0; }
out=$(mktemp -d); trap 'rm -rf "$out"' EXIT
for n in $prs; do
  ( scripts/watch-landing.sh "$n" "${2:-45}" 2>/dev/null | tail -1 | sed "s/^/#$n /" | tee "$out/$n" ) &
done
wait
[ -z "$(grep -L -E ' on (hosts|site) ' "$out"/*)" ]
