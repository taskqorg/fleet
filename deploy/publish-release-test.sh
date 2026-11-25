#!/usr/bin/env bash
# Checks deploy/publish-release.sh against a fake `gh api` that keeps releases and assets in a JSON file and fails chosen
# requests once (a fault: "<method> <path regex> <code> [landed]", landed = the request took effect before the error).
# Run by hand: deploy/publish-release-test.sh. Needs bash and jq. Not part of CI.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
cat > "$work/bin/gh" << 'FAKE'
#!/usr/bin/env bash
set -euo pipefail
s=$FAKE_STATE
[ "$1" = api ] && shift
method=GET input= ep= jqf=
while [ $# -gt 0 ]; do
  case $1 in
    --method) method=$2; shift 2 ;;
    -H) shift 2 ;;
    --input) input=$2; shift 2 ;;
    --jq) jqf=$2; shift 2 ;;
    *) ep=$1; shift ;;
  esac
done
ep=${ep#https://uploads.github.com/}
path=${ep%%\?*} query=
[ "$path" = "$ep" ] || query=${ep#*\?}
path=${path#repos/o/r}
echo "$method $path" >> "$s/log"
if [[ $path == /compare/* ]]; then cat "$s/moved"; exit 0; fi
fault= landed=
if [ -s "$s/faults" ]; then
  n=0
  while read -r fm fp fc fl; do
    n=$((n + 1))
    if [ "$fm" = "$method" ] && [[ $path =~ $fp ]]; then fault=$fc landed=${fl:-}; sed -i.bak "${n}d" "$s/faults"; break; fi
  done < "$s/faults"
fi
fail() {
  case $1 in
    net) echo "Post \"https://api.github.com$path\": read tcp: connection reset by peer" >&2 ;;
    403) echo "gh: Resource not accessible by integration (HTTP 403)" >&2 ;;
    404) echo '{"message":"Not Found"}'; echo "gh: Not Found (HTTP 404)" >&2 ;;
    422) echo '{"message":"Validation Failed"}'; echo "gh: Validation Failed (HTTP 422)" >&2 ;;
    *) echo "gh: HTTP $1 (HTTP $1)" >&2 ;;
  esac
  exit 1
}
[ -z "$fault" ] || [ "$landed" = landed ] || fail "$fault"
db=$s/releases.json
put() { jq "$@" "$db" > "$db.new" && mv "$db.new" "$db"; }
next() { local n; n=$(($(cat "$s/id") + 1)); echo "$n" > "$s/id"; echo "$n"; }
res=
case "$method $path" in
  "GET /releases") res=$(jq -c . "$db") ;;
  "GET /releases/tags/"*)
    t=${path#/releases/tags/}
    res=$(jq -c --arg t "$t" 'map(select(.tag_name == $t and (.draft | not))) | .[0] // empty' "$db")
    [ -n "$res" ] || fail 404 ;;
  "POST /releases")
    id=$(next)
    put --argjson id "$id" --slurpfile b "$input" '[$b[0] + {id: $id, assets: []}] + .'
    res=$(jq -c --argjson id "$id" '.[] | select(.id == $id)' "$db") ;;
  "GET /releases/"*/assets)
    id=${path#/releases/}; id=${id%/assets}
    res=$(jq -c --argjson id "$id" '.[] | select(.id == $id) | .assets' "$db")
    [ -n "$res" ] || fail 404 ;;
  "POST /releases/"*/assets)
    id=${path#/releases/}; id=${id%/assets}; name=${query#name=}
    [ -n "$(jq --argjson id "$id" '.[] | select(.id == $id) | .id' "$db")" ] || fail 404
    [ -z "$(jq -r --argjson id "$id" --arg n "$name" '.[] | select(.id == $id) | .assets[] | select(.name == $n) | .name' "$db")" ] || fail 422
    aid=$(next)
    d=sha256:$({ sha256sum "$input" 2> /dev/null || shasum -a 256 "$input"; } | cut -d' ' -f1)
    put --argjson id "$id" --argjson a "$aid" --arg n "$name" --arg d "$d" \
      'map(if .id == $id then .assets += [{id: $a, name: $n, state: "uploaded", digest: $d}] else . end)'
    res='{}' ;;
  "DELETE /releases/assets/"*)
    a=${path#/releases/assets/}
    [ -n "$(jq --argjson a "$a" '.[].assets[] | select(.id == $a) | .id' "$db")" ] || fail 404
    put --argjson a "$a" 'map(.assets |= map(select(.id != $a)))' ;;
  "GET /releases/"*)
    id=${path#/releases/}
    res=$(jq -c --argjson id "$id" '.[] | select(.id == $id)' "$db")
    [ -n "$res" ] || fail 404 ;;
  "PATCH /releases/"*)
    id=${path#/releases/}
    [ -n "$(jq --argjson id "$id" '.[] | select(.id == $id) | .id' "$db")" ] || fail 404
    put --argjson id "$id" --slurpfile b "$input" 'map(if .id == $id then . + ($b[0] | del(.make_latest)) else . end)'
    res=$(jq -c --argjson id "$id" '.[] | select(.id == $id)' "$db") ;;
  "DELETE /releases/"*)
    id=${path#/releases/}
    [ -n "$(jq --argjson id "$id" '.[] | select(.id == $id) | .id' "$db")" ] || fail 404
    put --argjson id "$id" 'map(select(.id != $id))' ;;
  *) echo "fake gh: no route for $method $path" >&2; exit 2 ;;
esac
[ -z "$fault" ] || fail "$fault"
[ -z "$res" ] || { if [ -n "$jqf" ]; then jq -r "$jqf" <<< "$res"; else echo "$res"; fi; }
FAKE
chmod +x "$work/bin/gh"

files=$work/files
mkdir -p "$files"
for f in fleet-1.0.0-linux-amd64.tgz SHA256SUMS provenance.sigstore.jsonl; do echo "content of $f" > "$files/$f"; done
digest() { echo "sha256:$({ sha256sum "$files/$1" 2> /dev/null || shasum -a 256 "$files/$1"; } | cut -d' ' -f1)"; }
A=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
B=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
failed=0

# run <name> <releases json> <faults> [env...]: a fresh state, then the script; sets $code and $s.
run() {
  name=$1
  s=$work/$name
  mkdir -p "$s"
  echo "$2" > "$s/releases.json"
  printf '%s\n' "$3" > "$s/faults"
  echo 100 > "$s/id"
  echo "${MOVED:-0}" > "$s/moved"
  : > "$s/log"
  shift 3
  code=0
  env PATH="$work/bin:$PATH" FAKE_STATE="$s" GH_REPO=o/r SHA=$A RELEASE_SLEEP=true RELEASE_TRIES=4 GITHUB_STEP_SUMMARY= "$@" \
    "$here/publish-release.sh" v1.0.0 true "v1.0.0" "Fleet 1.0.0" "$files"/* > "$s/output" 2>&1 || code=$?
}
check() {
  if eval "$2"; then echo "ok   $name: $1"; else
    echo "FAIL $name: $1"
    sed 's/^/     | /' "$s/output"
    failed=1
  fi
}
db() { jq -r "$@" "$s/releases.json"; }
# One published v1.0.0 for commit A whose assets are exactly the files, with their digests.
published_right() {
  [ "$(db '[.[] | select(.tag_name == "v1.0.0")] | length')" = 1 ] &&
    [ "$(db '.[] | select(.tag_name == "v1.0.0") | "\(.draft) \(.target_commitish)"')" = "false $A" ] &&
    [ "$(db '.[] | select(.tag_name == "v1.0.0") | .assets | map("\(.name) \(.digest)") | sort | join(",")')" = \
      "$(for f in "$files"/*; do n=$(basename "$f"); echo "$n $(digest "$n")"; done | sort | paste -sd, -)" ]
}

run fresh '[]' ''
check "creates, uploads and publishes" '[ $code = 0 ] && published_right'

draft="[{\"id\":7,\"tag_name\":\"v1.0.0\",\"target_commitish\":\"$A\",\"draft\":true,\"assets\":[
  {\"id\":71,\"name\":\"SHA256SUMS\",\"state\":\"uploaded\",\"digest\":\"$(digest SHA256SUMS)\"},
  {\"id\":72,\"name\":\"fleet-1.0.0-linux-amd64.tgz\",\"state\":\"uploaded\",\"digest\":\"sha256:old\"},
  {\"id\":73,\"name\":\"provenance.sigstore.jsonl\",\"state\":\"starter\",\"digest\":null},
  {\"id\":74,\"name\":\"latest.json\",\"state\":\"uploaded\",\"digest\":\"sha256:x\"}]}]"
run same-commit-draft "$draft" ''
check "reuses the draft, keeps the matching asset, replaces the rest, drops the stray one" \
  '[ $code = 0 ] && published_right && [ "$(db ".[0].id")" = 7 ] && ! grep -q "POST /releases$" "$s/log" &&
   [ "$(db ".[0].assets[] | select(.name == \"SHA256SUMS\") | .id")" = 71 ] && grep -q "deleting asset latest.json" "$s/output"'

run other-commit-draft "[{\"id\":8,\"tag_name\":\"v1.0.0\",\"target_commitish\":\"$B\",\"draft\":true,\"assets\":[]}]" ''
check "deletes another commit's draft and starts fresh" \
  '[ $code = 0 ] && published_right && grep -q "DELETE /releases/8" "$s/log" && grep -q "made for commit $B" "$s/output"'

run create-500-landed '[]' 'POST ^/releases$ 500 landed'
check "a 500 on create that made the draft: reused, no second draft" \
  '[ $code = 0 ] && published_right && [ "$(grep -c "POST /releases$" "$s/log")" = 1 ] && [ "$(db length)" = 1 ]'

run create-500 '[]' 'POST ^/releases$ 500
POST ^/releases$ 502'
check "a 500 and a 502 on create: retried" '[ $code = 0 ] && published_right && [ "$(grep -c "POST /releases$" "$s/log")" = 3 ]'

run upload-422 '[]' 'POST /assets$ 422 landed'
check "a 422 on upload whose asset landed: kept after reading the assets" \
  '[ $code = 0 ] && published_right && grep -q "kept .*checksum matches" "$s/output"'

run upload-500-landed '[]' 'POST /assets$ 500 landed
POST /assets$ net'
check "a 500 (landed) and a network error on upload: retried" '[ $code = 0 ] && published_right'

run publish-500-landed '[]' 'PATCH ^/releases/ 500 landed'
check "a 500 on publish that landed: done" '[ $code = 0 ] && published_right && [ "$(grep -c "PATCH" "$s/log")" = 1 ]'

run delete-500 "[{\"id\":8,\"tag_name\":\"v1.0.0\",\"target_commitish\":\"$B\",\"draft\":true,\"assets\":[]}]" 'DELETE ^/releases/8 500
DELETE ^/releases/8 429'
check "a 500 and a 429 on cleanup: retried" '[ $code = 0 ] && published_right'

run create-400 '[]' 'POST ^/releases$ 400'
check "a 400 is not retried" '[ $code != 0 ] && [ "$(grep -c "POST /releases$" "$s/log")" = 1 ]'

run outage '[]' 'POST ^/releases$ 500
POST ^/releases$ 500
POST ^/releases$ 500
POST ^/releases$ 500'
check "gives up after RELEASE_TRIES" '[ $code != 0 ] && [ "$(grep -c "POST /releases$" "$s/log")" = 4 ]'

pub="[{\"id\":9,\"tag_name\":\"v1.0.0\",\"target_commitish\":\"$A\",\"draft\":false,\"assets\":[]},
  {\"id\":10,\"tag_name\":\"v1.0.0\",\"target_commitish\":\"$A\",\"draft\":true,\"assets\":[]}]"
run already-published "$pub" ''
check "already published for this commit: leftover draft deleted, nothing else" \
  '[ $code = 0 ] && [ "$(db length)" = 1 ] && [ "$(db ".[0].id")" = 9 ] && ! grep -Eq "^(POST|PATCH)" "$s/log"'

run published-other "[{\"id\":9,\"tag_name\":\"v1.0.0\",\"target_commitish\":\"$B\",\"draft\":false,\"assets\":[]}]" ''
check "published for another commit: fails" '[ $code != 0 ] && grep -q "already published for $B" "$s/output"'

MOVED=1 run superseded '[]' 'PATCH ^/releases/ 403' GITHUB_EVENT_NAME=push
check "a 403 on publish after main moved workflows: draft deleted, green" '[ $code = 0 ] && [ "$(db length)" = 0 ]'
MOVED=1 run superseded-at-create '[]' 'POST ^/releases$ 403' GITHUB_EVENT_NAME=push
check "a 403 on create after main moved workflows: green, nothing left" '[ $code = 0 ] && [ "$(db length)" = 0 ]'
run forbidden '[]' 'PATCH ^/releases/ 403' GITHUB_EVENT_NAME=push
check "a 403 when workflows did not move: fails" '[ $code != 0 ]'

exit "$failed"
