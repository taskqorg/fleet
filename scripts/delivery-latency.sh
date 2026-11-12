#!/usr/bin/env bash
# Delivery latency of the last N merged pull requests (docs/designs/fleet-delivery-pipeline.md): per pull request and as P50/P90,
# minutes from "opened" to each stage: pull_request CI done, merge_group CI done, merged, release published, installed on PDS18.
# Usage: scripts/delivery-latency.sh [N, default 20]
# Sources: gh (pull requests, workflow runs, releases matched by target commit); hosts_list through the deployed app as
# site-agents (AGENTS.md, update.at of a healthy "ok" update, only the release each host runs now); journalctl -u fleet-update
# on PDS18 ("coordinator: X -> Y" then "Y ok:"; a ui-*-v* release logs only its "Y ok:", read locally on pds18 or over
# `ssh pds18`) for earlier releases. A stage without data prints "-".
set -euo pipefail
n=${1:-20}; [ "$n" -gt 0 ] 2>/dev/null || { echo "usage: delivery-latency.sh [N>0]" >&2; exit 64; }
repo=taskqorg/fleet
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
# gh orders by creation (search cannot sort by merge time; "updated" moves with labels): take 3n and keep the n most recently
# merged. ponytail: a branch older than 3n newer pull requests is missed; page further if that ever matters.
gh pr list -R "$repo" -s merged -B main -L $(( n * 3 )) --json number,createdAt,mergedAt,headRefOid,mergeCommit \
  | jq --argjson n "$n" 'sort_by(.mergedAt) | .[-$n:]' >"$tmp/prs.json"
[ "$(jq length "$tmp/prs.json")" -gt 0 ] || { echo "no merged pull request"; exit 0; }
gh api --paginate "repos/$repo/releases?per_page=100" -q '.[]|[.tag_name,.published_at]|@tsv' >"$tmp/releases.tsv"
# A release carries every merge since the previous one: match by the first release tag containing the merge commit (as watch-landing.sh).
git fetch -q origin main --tags 2>/dev/null || true
for sha in $(jq -r '.[].mergeCommit.oid // empty' "$tmp/prs.json"); do
  printf '%s\t%s\n' "$sha" "$(git tag --contains "$sha" 'v1.*' 'ui-*-v1.*' | awk '{ n=$0; sub(/^.*v/,"",n); print n, $0 }' | sort -k1,1V | cut -d' ' -f2- | head -1)"
done >"$tmp/tags.tsv"
# Workflow runs of the ci workflow since the oldest pull request was opened: one query, matched by commit and event below.
since=$(jq -r 'map(.createdAt) | min' "$tmp/prs.json")
gh run list -R "$repo" --workflow ci --created ">=$since" -L 2000 --json headSha,event,updatedAt,status,conclusion >"$tmp/runs.json"
# Host evidence. hosts_list: every host's current update time. Journal: every coordinator update on PDS18 with its time.
env_file=~/.config/ufobe-agents/site-access.env
# The token reaches curl on stdin (-H @-): never a file, never an argument.
if [ -f "$env_file" ]; then
  ( . "$env_file"; printf 'CF-Access-Client-Id: %s\nCF-Access-Client-Secret: %s\n' "$CF_ACCESS_CLIENT_ID" "$CF_ACCESS_CLIENT_SECRET" ) \
    | curl -sf --max-time 10 -H @- -H 'Content-Type: application/json' -H 'Origin: https://fleet.ufobe.com' \
    https://fleet.ufobe.com/api/ops/hosts_list -d '{}' >"$tmp/hosts.json" || echo '{"hosts":[]}' >"$tmp/hosts.json"
else echo '{"hosts":[]}' >"$tmp/hosts.json"; fi
journal="journalctl -u fleet-update --no-pager -o short-iso --since '60 days ago'"
if [ "$(hostname -s 2>/dev/null)" = pds18 ]; then sh -c "$journal" >"$tmp/journal.txt" 2>/dev/null || echo "journal unavailable: on PDS18 column from hosts_list only" >&2
else ssh -o BatchMode=yes -o ConnectTimeout=5 pds18 "$journal" >"$tmp/journal.txt" 2>/dev/null || echo "journal unavailable (ssh pds18): on PDS18 column from hosts_list only" >&2; fi

D="$tmp" python3 -I - <<'PY'
import json, os, re
from datetime import datetime
d = os.environ["D"]
load = lambda f: json.load(open(os.path.join(d, f)))
ts = lambda s: datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp()
prs, runs = load("prs.json"), load("runs.json")
try: hosts = load("hosts.json")["hosts"]
except (ValueError, KeyError, TypeError): hosts = []  # an Access redirect or a changed shape: no host column, not no report
tsv = lambda f: [l.split("\t") for l in open(os.path.join(d, f)).read().splitlines() if l]
published = dict(tsv("releases.tsv")); tag_of = dict(tsv("tags.tsv"))
# journalctl short-iso prints +0000 on older systemd; fromisoformat before 3.11 wants +00:00.
jts = lambda s: datetime.fromisoformat(re.sub(r"([+-]\d\d)(\d\d)$", r"\1:\2", s)).timestamp()
# First successful ci run of an event on a commit (a red run adds nothing; an "edited" rerun after green adds nothing either).
# ponytail: a re-run attempt of an already green run moves its updatedAt; rare, accepted.
def ci(sha, event):
    done = [ts(r["updatedAt"]) for r in runs if r["headSha"] == sha and r["event"] == event and r["conclusion"] == "success"]
    return min(done) if done else None
# PDS18 install time per (part, version): hosts_list for the version the coordinator's host runs now (a healthy "ok" update
# only), else the journal's "Y ok:" line (the first after the "<part>: X -> Y" start line, whichever run logs it; a refused,
# errored or rolled-back candidate never gets its "ok"). The UI part logs no arrow line: an "ok" of a version no arrow named
# is a UI install.
installed = {}
for h in hosts:
    # The coordinator's own host is the one row whose update carries the parts; its coordinator part is the install, whatever
    # the executor or the merged row says.
    u = ((h.get("update") or {}).get("parts") or {}).get("coordinator") or {}
    if u.get("state") == "ok" and u.get("version") and u.get("at"): installed["coordinator", u["version"]] = ts(u["at"])
part = {}  # version -> part named by its arrow line
for line in open(os.path.join(d, "journal.txt")):
    m = re.match(r"(\S+) \S+ fleet-update\[\d+\]: ((\S+): \S* -> (\S+)|(\S+) ok:)", line)
    if not m: continue
    if m.group(3): part.setdefault(m.group(4), m.group(3))
    else: installed.setdefault((part.get(m.group(5), "ui"), m.group(5)), jts(m.group(1)))
stages = ["pr_ci", "mq_ci", "merged", "released", "on_host"]
rows = []
for p in prs:
    t0 = ts(p["createdAt"]); merge = (p["mergeCommit"] or {}).get("oid")
    tag = tag_of.get(merge) or None; pub = published.get(tag) if tag else None
    t = {"pr_ci": ci(p["headRefOid"], "pull_request"), "mq_ci": ci(merge, "merge_group"), "merged": ts(p["mergedAt"]),
         "released": ts(pub) if pub else None, "on_host": installed.get(("ui" if tag.startswith("ui-") else "coordinator", tag.rsplit("-v", 1)[-1].removeprefix("v"))) if tag else None}
    rows.append((p["number"], tag or "-", {k: (v - t0) / 60 if v else None for k, v in t.items()}))
fmt = lambda v: "-" if v is None else f"{v:.1f}"
print("| PR | release | PR CI done | MQ CI done | merged | release published | on PDS18 |")
print("|---|---|---|---|---|---|---|")
for num, tag, m in rows: print(f"| #{num} | {tag} | " + " | ".join(fmt(m[s]) for s in stages) + " |")
def pct(vals, q):
    v = sorted(x for x in vals if x is not None)
    return v[min(len(v) - 1, int(q * (len(v) - 1) + 0.5))] if v else None  # nearest rank, half up
for name, q in (("P50", .5), ("P90", .9)): print(f"| {name} | | " + " | ".join(fmt(pct([m[s] for _, _, m in rows], q)) for s in stages) + " |")
print("\nminutes from pull request opened; P50/P90 over pull requests with data for that stage")
PY
