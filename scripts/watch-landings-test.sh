#!/usr/bin/env bash
# Self-check of scripts/watch-landings.sh with stub gh and watch-landing.sh: the exit is 0 only when every line succeeds.
set -u
t=$(mktemp -d); trap 'rm -rf "$t"' EXIT
mkdir -p "$t/scripts" "$t/bin"; cp "$(dirname "$0")/watch-landings.sh" "$t/scripts/"
printf '#!/bin/sh\ncase $1 in 1) echo "on hosts v1.2.3";; 2) echo "merged ab12, nothing to ship";; 4) echo "on site ui-1.2.3-v1.2.4";; *) echo "timeout at: UI site";; esac\n' >"$t/scripts/watch-landing.sh"
run() { printf '#!/bin/sh\ncase "$*" in *open*) echo 1; echo 2;; *graphql*) echo 4;; *) %s;; esac\n' "$1" >"$t/bin/gh"; chmod +x "$t"/bin/gh "$t"/scripts/*; PATH="$t/bin:$PATH" "$t/scripts/watch-landings.sh" >/dev/null; }
run true; [ $? = 0 ] || { echo "FAIL: all successes must exit 0"; exit 1; }
run 'echo 3'; [ $? = 1 ] || { echo "FAIL: a timeout must exit 1"; exit 1; }
echo ok
