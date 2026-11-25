#!/usr/bin/env bash
# Publishes the GitHub release <tag> of commit $SHA with <file>... (release.yml, job publish). Safe to run again for the
# same commit: it reuses this commit's draft of <tag>, keeps an asset whose sha256 digest matches the file and replaces
# any other asset of that draft, then publishes. A draft of <tag> made for another commit is deleted first. A published
# release of <tag> for $SHA is left as it is; one for another commit fails the run.
# GitHub API errors 5xx, 429 and network failures are retried with backoff (2 s doubling to 60 s, RELEASE_TRIES attempts,
# default 8); after a failed create, upload or publish the script reads the state again before the next attempt, since
# the request may have landed. Any other 4xx fails at once.
# The workflow token cannot tag a commit whose .github/workflows differ from main's head (HTTP 403 "Resource not
# accessible by integration", docs/designs/fleet-delivery-pipeline.md#4-automatic-release): on a push run whose commit
# main has moved past with workflow changes, the draft is deleted and the run ends green; the next release run carries it.
# usage: publish-release.sh <tag> <latest: true|false> <title> <notes> <file>...
# Needs GH_TOKEN, GH_REPO, SHA; GITHUB_EVENT_NAME and GITHUB_STEP_SUMMARY when run by the workflow.
set -euo pipefail
[ $# -ge 5 ] || { echo "usage: publish-release.sh <tag> <latest> <title> <notes> <file>..." >&2; exit 64; }
tag=$1 latest=$2 title=$3 notes=$4
shift 4
: "${GH_REPO:?}" "${SHA:?}"
tries=${RELEASE_TRIES:-8}
pause=${RELEASE_SLEEP:-sleep}
here=$(cd "$(dirname "$0")" && pwd)
api=repos/$GH_REPO
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
out=$tmp/out err=$tmp/err

note() { echo "$*"; [ -z "${GITHUB_STEP_SUMMARY:-}" ] || echo "$*" >> "$GITHUB_STEP_SUMMARY"; }

# once <method> <endpoint> [gh api args]: one request; prints the response. Returns 0, 2 for a 4xx other than 429 (its
# message stays in $err), 3 for anything worth retrying (5xx, 429, network).
once() {
  if gh api --method "$1" "$2" "${@:3}" > "$out" 2> "$err"; then cat "$out"; return 0; fi
  grep -q "HTTP 404" "$err" || cat "$err" >&2
  if grep -Eq 'HTTP 4[0-9][0-9]' "$err" && ! grep -q 'HTTP 429' "$err"; then return 2; fi
  return 3
}
# backoff <attempt>: waits before attempt+1, or fails the run when the attempts are used up.
backoff() {
  [ "$1" -lt "$tries" ] || { echo "::error::GitHub API still failing after $1 attempts" >&2; exit 1; }
  local d=$((2 ** $1))
  [ "$d" -le 60 ] || d=60
  echo "retry $(($1 + 1))/$tries in ${d}s" >&2
  "$pause" "$d"
}
# call: once, retried while transient; for reads and deletes, which are safe to repeat.
call() {
  local i=1 rc
  while :; do
    once "$@" && return 0 || rc=$?
    [ "$rc" = 3 ] || return "$rc"
    backoff "$i"
    i=$((i + 1))
  done
}
# gone <method> <endpoint>: a delete; already gone (404) is done.
gone() {
  call "$@" > /dev/null && return 0 || rc=$?
  [ "$rc" = 2 ] && grep -q 'HTTP 404' "$err" && return 0
  exit 1
}
sha256() { { sha256sum "$1" 2> /dev/null || shasum -a 256 "$1"; } | cut -d' ' -f1; }
# The workflow token's 403 on a tag when main moved on with workflow changes (push runs only).
superseded() {
  grep -q 'HTTP 403' "$err" && grep -q 'Resource not accessible by integration' "$err" && [ "${GITHUB_EVENT_NAME:-}" = push ] || return 1
  local n
  n=$("$here/workflows-moved.sh" "$SHA") && [ -n "$n" ] && [ "$n" != 0 ]
}

# A published release of this tag.
if rel=$(call GET "$api/releases/tags/$tag"); then
  commit=$(jq -r .target_commitish <<< "$rel")
  [ "$commit" = "$SHA" ] || { echo "::error::$tag is already published for $commit, not $SHA"; exit 1; }
  published=true
else
  rc=$?
  { [ "$rc" = 2 ] && grep -q 'HTTP 404' "$err"; } || exit 1
  published=false
fi

# draft: this commit's newest draft of the tag. Drafts of the tag for other commits and older duplicates are deleted
# (all of them once the tag is published).
find_draft() {
  local list id commit
  list=$(call GET "$api/releases?per_page=100") || exit 1
  draft=
  while read -r id commit; do
    if [ "$published" = true ]; then
      note "deleting draft $id of $tag: $tag is already published"
    elif [ "$commit" != "$SHA" ]; then
      note "deleting draft $id of $tag: made for commit $commit, this run releases $SHA"
    elif [ -z "$draft" ]; then
      draft=$id
      continue
    else
      note "deleting draft $id of $tag: a second draft for $SHA"
    fi
    gone DELETE "$api/releases/$id"
  done < <(jq -r --arg t "$tag" '.[] | select(.draft and .tag_name == $t) | "\(.id) \(.target_commitish)"' <<< "$list")
}
find_draft
if [ "$published" = true ]; then
  note "$tag is already published for $SHA; nothing to do"
  exit 0
fi

# Create the draft. A failed create may still have made one: look again before the next attempt.
jq -nc --arg t "$tag" --arg c "$SHA" --arg n "$title" --arg b "$notes" \
  '{tag_name:$t, target_commitish:$c, name:$n, body:$b, draft:true}' > "$tmp/create.json"
if [ -n "$draft" ]; then
  note "reusing draft $draft of $tag for $SHA"
else
  i=1
  while :; do
    if rel=$(once POST "$api/releases" --input "$tmp/create.json"); then
      draft=$(jq -r .id <<< "$rel")
      echo "created draft $draft of $tag"
      break
    else
      rc=$?
    fi
    [ "$rc" = 3 ] || exit 1
    backoff "$i"
    i=$((i + 1))
    find_draft
    [ -z "$draft" ] || { note "reusing draft $draft of $tag for $SHA, made by the failed attempt"; break; }
  done
fi

# Assets by name: a matching sha256 is kept, anything else is deleted and uploaded again, and assets not in <file>...
# are deleted, so SHA256SUMS, its signature and the provenance always describe the tarballs next to them.
assets() { call GET "$api/releases/$draft/assets?per_page=100" || exit 1; }
list=$(assets)
names=()
for f in "$@"; do names+=("$(basename "$f")"); done
while read -r id name; do
  printf '%s\n' "${names[@]}" | grep -Fxq "$name" && continue
  echo "deleting asset $name: not part of this release"
  gone DELETE "$api/releases/assets/$id"
done < <(jq -r '.[] | "\(.id) \(.name)"' <<< "$list")
for f in "$@"; do
  name=$(basename "$f")
  want=sha256:$(sha256 "$f")
  i=1
  while :; do
    read -r id state digest < <(jq -r --arg n "$name" '.[] | select(.name == $n) | "\(.id) \(.state) \(.digest // "none")"' <<< "$list") || id=
    if [ -n "$id" ]; then
      if [ "$state" = uploaded ] && [ "$digest" = "$want" ]; then
        echo "kept $name: checksum matches"
        break
      fi
      echo "replacing $name ($state, $digest)"
      gone DELETE "$api/releases/assets/$id"
    fi
    if once POST "https://uploads.github.com/$api/releases/$draft/assets?name=$name" \
      -H 'Content-Type: application/octet-stream' --input "$f" > /dev/null; then
      echo "uploaded $name"
      break
    else
      rc=$?
    fi
    # 422: the name exists, an earlier attempt landed after all; read the assets again and judge it like any other.
    [ "$rc" = 3 ] || { [ "$rc" = 2 ] && grep -q 'HTTP 422' "$err"; } || exit 1
    backoff "$i"
    i=$((i + 1))
    list=$(assets)
  done
done

# Publish: GitHub tags the commit now. A failed edit may have landed: read the release before the next attempt.
jq -nc --arg t "$tag" --arg c "$SHA" --arg n "$title" --arg b "$notes" --arg l "$latest" \
  '{tag_name:$t, target_commitish:$c, name:$n, body:$b, draft:false, make_latest:$l}' > "$tmp/publish.json"
i=1
while :; do
  once PATCH "$api/releases/$draft" --input "$tmp/publish.json" > /dev/null && break || rc=$?
  if [ "$rc" = 2 ] && superseded; then
    gone DELETE "$api/releases/$draft"
    note "superseded: main changed workflows since $SHA; the next release run publishes it"
    exit 0
  fi
  [ "$rc" = 3 ] || exit 1
  backoff "$i"
  i=$((i + 1))
  rel=$(call GET "$api/releases/$draft") || exit 1
  [ "$(jq -r .draft <<< "$rel")" = false ] && break
done
note "published $tag for $SHA"
