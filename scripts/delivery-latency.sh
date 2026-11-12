#!/usr/bin/env bash
# Delivery latency of the last N merged pull requests (docs/designs/fleet-delivery-pipeline.md): per pull request and as P50/P90,
# minutes from "opened" to each stage: pull_request CI done, merge_group CI done, merged, release published, installed on PDS18.
# Usage: scripts/delivery-latency.sh [N, default 20]
# Sources: gh (pull requests, workflow runs, releases matched by target commit); hosts_list through the deployed app as
# site-agents (AGENTS.md, update.at per host, only the release each host runs now); journalctl -u fleet-update on PDS18
# ("coordinator: X -> Y" lines, read locally on pds18 or over `ssh pds18`) for earlier releases. A stage without data prints "-".
set -euo pipefail
n=${1:-20}
repo=taskqorg/fleet
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
gh pr list -R "$repo" -s merged -L "$n" --json number,createdAt,mergedAt,headRefOid,mergeCommit >"$tmp/prs.json"
gh api --paginate "repos/$repo/releases?per_page=100" -q '.[]|[.tag_name,.target_commitish,.published_at]|@tsv' >"$tmp/releases.tsv"
# Workflow runs of the ci workflow: one query, matched by commit and event below.
gh run list -R "$repo" --workflow ci -L 300 --json headSha,event,updatedAt,status,conclusion >"$tmp/runs.json"
# Host evidence. hosts_list: every host's current update time. Journal: every coordinator update on PDS18 with its time.
env_file=~/.config/ufobe-agents/site-access.env
if [ -f "$env_file" ]; then
  hdr="$tmp/hdr"; ( umask 077; . "$env_file"; printf 'CF-Access-Client-Id: %s\nCF-Access-Client-Secret: %s\n' "$CF_ACCESS_CLIENT_ID" "$CF_ACCESS_CLIENT_SECRET" >"$hdr" )
  curl -sf --max-time 10 -H @"$hdr" -H 'Content-Type: application/json' -H 'Origin: https://fleet.ufobe.com' \
    https://fleet.ufobe.com/api/ops/hosts_list -d '{}' >"$tmp/hosts.json" || echo '{"hosts":[]}' >"$tmp/hosts.json"
else echo '{"hosts":[]}' >"$tmp/hosts.json"; fi
journal="journalctl -u fleet-update --no-pager -o short-iso --since '60 days ago'"
if [ "$(hostname -s 2>/dev/null)" = pds18 ]; then eval "$journal" >"$tmp/journal.txt" 2>/dev/null || :
else ssh -o BatchMode=yes -o ConnectTimeout=5 pds18 "$journal" >"$tmp/journal.txt" 2>/dev/null || :; fi
touch "$tmp/journal.txt"

D="$tmp" python3 -I - <<'PY'
import json, os, re
from datetime import datetime
d = os.environ["D"]
load = lambda f: json.load(open(os.path.join(d, f)))
ts = lambda s: datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp()
prs, runs, hosts = load("prs.json"), load("runs.json"), load("hosts.json")["hosts"]
releases = {t.split("\t")[1]: (t.split("\t")[0], t.split("\t")[2]) for t in open(os.path.join(d, "releases.tsv")).read().splitlines() if t}
# Last successful ci run of an event on a commit (a rerun after a red check counts from its own end).
def ci(sha, event):
    done = [ts(r["updatedAt"]) for r in runs if r["headSha"] == sha and r["event"] == event and r["conclusion"] == "success"]
    return max(done) if done else None
# PDS18 install time per version: the journal line "coordinator: X -> Y", then hosts_list for the version a host runs now.
installed = {}
for line in open(os.path.join(d, "journal.txt")):
    m = re.match(r"(\S+) .*coordinator: \S+ -> (\S+)", line)
    if m and m.group(2) not in installed: installed[m.group(2)] = datetime.fromisoformat(m.group(1)).timestamp()
for h in hosts:
    u = h.get("update") or {}
    if h["id"].startswith("pds18") and u.get("version") and u.get("at"): installed.setdefault(u["version"], ts(u["at"]))
stages = ["pr_ci", "mq_ci", "merged", "released", "on_host"]
rows = []
for p in sorted(prs, key=lambda p: p["mergedAt"]):
    t0 = ts(p["createdAt"]); merge = p["mergeCommit"]["oid"]
    tag, pub = releases.get(merge, (None, None))
    t = {"pr_ci": ci(p["headRefOid"], "pull_request"), "mq_ci": ci(merge, "merge_group"), "merged": ts(p["mergedAt"]),
         "released": ts(pub) if pub else None, "on_host": installed.get(tag[1:]) if tag else None}
    rows.append((p["number"], tag or "-", {k: (v - t0) / 60 if v else None for k, v in t.items()}))
fmt = lambda v: "-" if v is None else f"{v:.1f}"
print("| PR | release | PR CI done | MQ CI done | merged | release published | on PDS18 |")
print("|---|---|---|---|---|---|---|")
for num, tag, m in rows: print(f"| #{num} | {tag} | " + " | ".join(fmt(m[s]) for s in stages) + " |")
def pct(vals, q):
    v = sorted(x for x in vals if x is not None)
    return v[min(len(v) - 1, int(round(q * (len(v) - 1))))] if v else None
for name, q in (("P50", .5), ("P90", .9)): print(f"| {name} | | " + " | ".join(fmt(pct([m[s] for _, _, m in rows], q)) for s in stages) + " |")
print("\nminutes from pull request opened; P50/P90 over pull requests with data for that stage")
PY
