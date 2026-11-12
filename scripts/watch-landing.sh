#!/usr/bin/env bash
# Watch one pull request from the merge queue to the hosts (docs/designs/fleet-delivery-pipeline.md#8-workers-merge-and-watch-their-own-pull-requests).
# Usage: scripts/watch-landing.sh <pr> [timeout-minutes, default 45]
# Prints one line per stage: merged <sha>, released v<version>, on hosts. Exit 0 when every polling host with auto-update on
# runs that release or a newer one; exit 1 when the pull request leaves the queue unmerged; exit 2 on timeout; exit 3 after
# the release when there is no site access.
# Hosts are read through the deployed app as site-agents (AGENTS.md); without ~/.config/ufobe-agents/site-access.env it stops after the release.
set -euo pipefail
pr=$1
deadline=$(( $(date +%s) + ${2:-45} * 60 ))
repo=taskqorg/fleet
# Observations are poll times, not the instant a host/site transitioned. Keep stdout's stage contract unchanged.
observed() { printf 'observed_at=%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }
observed "watch_start pr=$pr"
wait_step() { [ "$(date +%s)" -lt "$deadline" ] || { echo "timeout at: $1"; exit 2; }; sleep 30; }

q='query($n:Int!){repository(owner:"taskqorg",name:"fleet"){pullRequest(number:$n){state mergeStateStatus mergeCommit{oid} autoMergeRequest{enabledAt} mergeQueueEntry{state}
  commits(last:1){nodes{commit{statusCheckRollup{contexts(last:100){nodes{... on CheckRun{name conclusion at:startedAt isRequired(pullRequestNumber:$n)} ... on StatusContext{name:context state at:createdAt isRequired(pullRequestNumber:$n)}}}}}}}
  timelineItems(last:1,itemTypes:[REMOVED_FROM_MERGE_QUEUE_EVENT,AUTO_MERGE_DISABLED_EVENT]){nodes{__typename ... on RemovedFromMergeQueueEvent{reason}}}}}}'
dirty=0
while :; do
  # A failed gh call (network, rate limit) is retried, never read as "not merged".
  line=$(gh api graphql -f query="$q" -F n="$pr" -q '.data.repository.pullRequest | [.state, .mergeStateStatus, (.mergeCommit.oid // "-"),
    (if .autoMergeRequest or .mergeQueueEntry then "waiting" else "-" end), (.mergeQueueEntry.state // "-"), (if [.commits.nodes[0].commit.statusCheckRollup.contexts.nodes[]? | select(.isRequired)] | group_by(.name) | map(max_by(.at // (if .conclusion == null then "~" else "" end)) | (.conclusion // .state)) | any(. == "FAILURE" or . == "ERROR" or . == "TIMED_OUT" or . == "STARTUP_FAILURE" or . == "CANCELLED" or . == "STALE" or . == "ACTION_REQUIRED") then "red" else "-" end),
    (if .timelineItems.nodes[0].__typename == "RemovedFromMergeQueueEvent" then (.timelineItems.nodes[0].reason // "unknown") else "auto-merge off" end)] | join(" ")') \
    || { dirty=0; wait_step "queue (gh failed)"; continue; }
  read -r state merge_state sha waiting queued checks reason <<<"$line"
  [ "$state" = MERGED ] && break
  # A conflict with main before the queue takes the entry: auto-merge stays on and nothing is removed, so name it here. It
  # counts when DIRTY on two polls in a row: right after a push GitHub may still report the old head's DIRTY. Inside the queue
  # the queue decides (PR #451: DIRTY one second after joining the queue, merged five minutes later); a real conflict there
  # ends in a removal with reason merge_conflict, named below.
  if [ "$state" = OPEN ] && [ "$merge_state" = DIRTY ] && [ "$waiting" = waiting ] && [ "$queued" = "-" ]; then
    [ "$dirty" = 1 ] && { echo "not merged: $state, merge_conflict"; exit 1; }
    dirty=1; wait_step "queue"; continue
  fi
  dirty=0
  # Out of the queue: the newest of "removed from the queue" (its reason) and "auto-merge disabled" (a veto) names why.
  [ "$state" = OPEN ] && [ "$waiting" = waiting ] || { echo "not merged: $state, $reason"; exit 1; }
  # A required check red on the head while auto-merge waits for the queue: GitHub never creates the entry and names nothing.
  # Only the newest run of each required check counts, as GitHub judges it: a rerun after a red run starts green (queued, no
  # start time yet, sorts newest; a run that ended without starting sorts oldest). A cancelled run does not pass either:
  # naming it beats a silent 45-minute wait.
  # Inside the queue the queue's own run decides (a removal above); a late red pull_request rerun does not.
  [ "$checks" = red ] && [ "$queued" = "-" ] && { echo "not merged: $state, failed_checks (head)"; exit 1; }
  wait_step "queue"
done
observed "merged sha=$sha"
echo "merged $sha"
# A pull request with no change (an empty diff against main before it) ships nothing: no release or host waits for it.
git fetch -q origin main 2>/dev/null || true
git diff --quiet "$sha^1" "$sha" 2>/dev/null && { echo "merged $sha, nothing to ship"; exit 0; }

# The first release whose tagged commit contains the merge commit; a batch release may carry several merges.
while :; do
  # A failed fetch (network) is retried like a missing tag.
  git fetch -q origin main --tags 2>/dev/null || true
  tag=$(git tag --contains "$sha" 'v1.*' 'ui-*-v1.*' | awk '{ n=$0; sub(/^.*v/,"",n); print n, $0 }' | sort -k1,1V | cut -d' ' -f2- 2>/dev/null | head -1 || true)
  [ -n "$tag" ] && break
  wait_step "release"
done
observed "release_tag tag=$tag"
echo "released $tag"
published=$(gh release view "$tag" -R "$repo" --json publishedAt -q '.publishedAt // empty' 2>/dev/null || true)
printf 'published_at=%s source=github_release tag=%s\n' "${published:-unavailable}" "$tag" >&2

env_file=~/.config/ufobe-agents/site-access.env
[ -f "$env_file" ] || { echo "no site access: check hosts_list yourself"; exit 3; }
# The token goes into a 0600 header file read by curl (-H @file): never in ps, never printed (agent-access-smoke.sh).
hdr=$(mktemp); chmod 600 "$hdr"; trap 'rm -f "$hdr"' EXIT
( . "$env_file"; printf 'CF-Access-Client-Id: %s\nCF-Access-Client-Secret: %s\n' "$CF_ACCESS_CLIENT_ID" "$CF_ACCESS_CLIENT_SECRET" ) >"$hdr"
if [[ "$tag" == ui-*-v* ]]; then
  backend=${tag#ui-}; backend=${backend%-v*}; want=${tag##*-v}
  commit=$(git rev-parse "$tag^{commit}")
  while :; do
    if curl -sf --max-time 3 -H @"$hdr" https://fleet.ufobe.com/ui-release.json | WANT="$want" BACKEND="$backend" COMMIT="$commit" python3 -I -c '
import json,os,sys
try: m=json.load(sys.stdin)
except ValueError: sys.exit(1)
sys.exit(0 if m.get("kind")=="ui" and m.get("version")==os.environ["WANT"] and m.get("backendVersion")==os.environ["BACKEND"] and m.get("commit")==os.environ["COMMIT"] else 1)
'; then
      if curl -sf --max-time 3 -H @"$hdr" https://fleet.ufobe.com/api/health | BACKEND="$backend" python3 -I -c '
import json,os,sys
try: h=json.load(sys.stdin)
except ValueError: sys.exit(1)
sys.exit(0 if h.get("database")=="ok" and h.get("version")==os.environ["BACKEND"] else 1)
'; then observed "ui_ready version=$want backend=$backend commit=$commit"; echo "on site $tag"; exit 0; fi
    fi
    # A later native release that contains the merge supersedes the UI tag: its bundled UI is what ships.
    native=$(git tag --contains "$sha" 'v1.*' | sort -V | head -1)
    [ -n "$native" ] && { observed "ui_superseded native=$native"; tag=$native; break; }
    wait_step "UI site"
    git fetch -q origin main --tags 2>/dev/null || true
  done
fi
want=${tag#v}
site_seen=false
while :; do
  # Evidence only, bounded by the existing deploy health probe's 3s request budget; never a new host gate.
  if [ "$site_seen" = false ]; then
    site=$(curl -sf --max-time 3 -H @"$hdr" https://fleet.ufobe.com/api/health | WANT="$want" python3 -I -c '
import json, os, sys
try: h = json.load(sys.stdin)
except ValueError: sys.exit(1)
s = h.get("version", "")
release = lambda v: len(v.split(".")) == 3 and all(x.isdigit() for x in v.split("."))
if h.get("database") == "ok" and release(s) and tuple(map(int, s.split("."))) >= tuple(map(int, os.environ["WANT"].split("."))): print(s)
') || site=""
    if [ -n "$site" ]; then observed "site_healthy version=$site"; site_seen=true; fi
  fi
  behind=$(curl -sf -H @"$hdr" -H 'Content-Type: application/json' -H 'Origin: https://fleet.ufobe.com' \
    https://fleet.ufobe.com/api/ops/hosts_list -d '{}' | WANT="$want" python3 -I -c '
import json, os, sys, time
from datetime import datetime
d = json.load(sys.stdin)
v = lambda s: tuple(int(x) for x in s.split("."))
want = v(os.environ["WANT"])
# Only hosts that poll now, run a release build (1.x.y, not a dev 0.0.0-<sha>) and have auto-update on are expected
# to move; an off or held host is the owner'"'"'s choice, a silent one is not this pull request'"'"'s problem.
live = lambda h: h["lastSeenAt"] and d["serverNow"] - h["lastSeenAt"] < d["staleAfterMs"]
rel = lambda s: bool(s) and s.count(".") == 2 and s.replace(".", "").isdigit()
# autoUpdate is "on", "off" or a hold until an RFC 3339 time; a hold that has passed updates again.
def on(a):
  if a == "on": return True
  try: return datetime.fromisoformat(a.replace("Z", "+00:00")).timestamp() < time.time()
  except (AttributeError, ValueError): return False
hosts = [h for h in d["hosts"] if live(h) and rel(h["version"]) and on(h["autoUpdate"])]
# No live host to check is not "on hosts": keep waiting and name it.
print(" ".join(h["id"] + "@" + h["version"] for h in hosts if v(h["version"]) < want) if hosts else "no live auto-update host")') || behind="(hosts_list failed)"
  [ -z "$behind" ] && break
  wait_step "hosts, behind: $behind"
done
[ "$site_seen" = true ] || observed "site_health unavailable"
observed "hosts_ready target=$tag"
echo "on hosts $tag"
