#!/usr/bin/env bash
# Watch one pull request from the merge queue to the hosts (docs/designs/fleet-delivery-pipeline.md#8-workers-merge-and-watch-their-own-pull-requests).
# Usage: scripts/watch-landing.sh <pr> [timeout-minutes, default 45]
# Prints one line per stage: merged <sha>, released v<version>, on hosts. Exit 0 when every polling host with auto-update on
# runs that release or a newer one; exit 1 when the pull request leaves the queue unmerged; exit 2 on timeout.
# Hosts are read through the deployed app as site-agents (AGENTS.md); without ~/.config/ufobe-agents/site-access.env it stops after the release.
set -euo pipefail
pr=$1
deadline=$(( $(date +%s) + ${2:-45} * 60 ))
repo=taskqorg/fleet
wait_step() { [ "$(date +%s)" -lt "$deadline" ] || { echo "timeout at: $1"; exit 2; }; sleep 30; }

q='query($n:Int!){repository(owner:"taskqorg",name:"fleet"){pullRequest(number:$n){state mergeCommit{oid} autoMergeRequest{enabledAt} mergeQueueEntry{state}
  timelineItems(last:1,itemTypes:[REMOVED_FROM_MERGE_QUEUE_EVENT]){nodes{... on RemovedFromMergeQueueEvent{reason}}}}}}'
while :; do
  # A failed gh call (network, rate limit) is retried, never read as "not merged".
  line=$(gh api graphql -f query="$q" -F n="$pr" -q '.data.repository.pullRequest | [.state, (.mergeCommit.oid // "-"),
    (if .autoMergeRequest or .mergeQueueEntry then "waiting" else "-" end), (.timelineItems.nodes[0].reason // "auto-merge off")] | join(" ")') \
    || { wait_step "queue (gh failed)"; continue; }
  read -r state sha waiting reason <<<"$line"
  [ "$state" = MERGED ] && break
  # Out of the queue with auto-merge off: GitHub disables auto-merge when it removes an entry, so the last removal names why.
  [ "$state" = OPEN ] && [ "$waiting" = waiting ] || { echo "not merged: $state, $reason"; exit 1; }
  wait_step "queue"
done
echo "merged $sha"

# The first release whose tagged commit contains the merge commit; a batch release may carry several merges.
git fetch -q origin main
while :; do
  git fetch -q --tags origin
  tag=$(git tag --contains "$sha" --sort=v:refname 'v1.*' | head -1)
  [ -n "$tag" ] && break
  wait_step "release"
done
echo "released $tag"

env_file=~/.config/ufobe-agents/site-access.env
[ -f "$env_file" ] || { echo "no site access: check hosts_list yourself"; exit 0; }
want=${tag#v}
while :; do
  behind=$( . "$env_file"; curl -sf -X POST -H 'Content-Type: application/json' -H 'Origin: https://fleet.ufobe.com' \
    -H "CF-Access-Client-Id: $CF_ACCESS_CLIENT_ID" -H "CF-Access-Client-Secret: $CF_ACCESS_CLIENT_SECRET" \
    https://fleet.ufobe.com/api/ops/hosts_list -d '{}' | WANT="$want" python3 -I -c '
import json, os, sys
d = json.load(sys.stdin)
v = lambda s: tuple(int(x) for x in s.split("."))
want = v(os.environ["WANT"])
# Only hosts that poll now, run a release build (1.x.y, not a dev 0.0.0-<sha>) and have auto-update on are expected
# to move; an off or held host is the owner'"'"'s choice, a silent one is not this pull request'"'"'s problem.
live = lambda h: h["lastSeenAt"] and d["serverNow"] - h["lastSeenAt"] < d["staleAfterMs"]
rel = lambda s: bool(s) and s.count(".") == 2 and s.replace(".", "").isdigit()
print(" ".join(h["id"] + "@" + h["version"] for h in d["hosts"]
  if live(h) and rel(h["version"]) and h["autoUpdate"] == "on" and v(h["version"]) < want))') || behind="(hosts_list failed)"
  [ -z "$behind" ] && break
  wait_step "hosts, behind: $behind"
done
echo "on hosts $tag"
