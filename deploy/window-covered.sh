#!/usr/bin/env bash
# Exit 0 when the migration check a speculative build ran (its speculative.json) still covers the current window
# (RECENT, DIGESTS as deploy/release-recent.sh prints them), so the release need not run it again
# (docs/designs/fleet-delivery-pipeline.md#5-compatibility-across-skipped-versions). A version checked then must keep its
# asset digest; a version that entered the window since must ship the same go/coordinator/schema.sql (the whole
# migration history) as the newest checked one, so the database it builds and the schema it serves are the ones checked.
#   RECENT=... DIGESTS=... deploy/window-covered.sh <speculative.json>     (needs the release tags fetched)
set -euo pipefail
checked=$(jq -r '.checkedWith | join(" ")' "$1")
[ -n "$checked" ] && [ -n "$RECENT" ] || exit 1
old=$(jq -r '.digests // ""' "$1")
digest() { printf '%s\n' $2 | awk -F= -v v="$1" '$1 == v { print $2 }'; }
newest=${checked%% *}
for v in $RECENT; do
  case " $checked " in
    *" $v "*) [ "$(digest "$v" "$old")" = "$(digest "$v" "$DIGESTS")" ] || exit 1 ;;
    *) git diff --quiet "v$newest" "v$v" -- go/coordinator/schema.sql || exit 1 ;;
  esac
done
