#!/bin/sh
# Prints the migration check's window (docs/designs/fleet-delivery-pipeline.md#5-compatibility-across-skipped-versions) as
# GitHub outputs: recent=<the 5 newest published v1.* releases, newest first> and digests=<version>=sha256:<hex> of each
# one's linux tarball, so the check verifies and caches them without more lookups. Needs GH_TOKEN and GH_REPO.
set -eu
releases=$(gh api --paginate "repos/$GH_REPO/releases?per_page=100" --jq '.[] | select(.draft == false and .prerelease == false) | .tag_name as $t | "\($t) \([.assets[] | select(.name == "fleet-\($t | ltrimstr("v"))-linux-amd64.tgz") | .digest // empty] | first // "")"' | awk '$1 ~ /^v1\.[0-9][0-9][0-9][0-9][0-9][0-9]\.[0-9]+$/ && n++ < 5 { print substr($1,2), $2 }')
printf '%s\n' "$releases" | awk 'NF { printf "%s%s", sep, $1; sep=" " } END { print "" }' | sed "s/^/recent='/; s/$/'/"
printf '%s\n' "$releases" | awk 'NF { printf "%s%s=%s", sep, $1, $2; sep=" " } END { print "" }' | sed "s/^/digests='/; s/$/'/"
