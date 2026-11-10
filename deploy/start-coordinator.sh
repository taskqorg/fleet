#!/bin/sh
# Starts the coordinator from a release (or a checkout): the Go front door on PORT with the TypeScript coordinator behind it
# on FLEET_BACKEND_PORT (default PORT+1, loopback), sharing the environment (docs/designs/fleet-stack.md#coordinator-migration-order).
#   FLEET_FRONT_DOOR=typescript   fallback: the TypeScript coordinator alone on PORT, as before slice 1.
#   FLEET_NODE                    node binary; default `node`.
#   FLEET_TS_SERVER, FLEET_GO_SERVER  commands, default the release paths below.
# If either process exits, the other is stopped and the script exits non-zero, so the service manager restarts both.
set -eu
cd "$(dirname "$0")/.."
port=${PORT:-8787}
ts=${FLEET_TS_SERVER:-"${FLEET_NODE:-node} --enable-source-maps dist/packages/coordinator/server.js"}
if [ "${FLEET_FRONT_DOOR:-go}" = typescript ]; then eval "exec $ts"; fi
os=$(uname -s | tr A-Z a-z); arch=$(uname -m); [ "$arch" = x86_64 ] && arch=amd64; [ "$arch" = aarch64 ] && arch=arm64
go=${FLEET_GO_SERVER:-"dist/go/fleet-coordinator-$os-$arch"}
back=${FLEET_BACKEND_PORT:-$((port + 1))}
(export PORT=$back; eval "exec $ts") & tsp=$!
(export FLEET_BACKEND_URL=http://127.0.0.1:$back; eval "exec $go") & gop=$!
stopping=0
trap 'stopping=1; kill -TERM $tsp $gop 2>/dev/null || true' TERM INT
while [ $stopping = 0 ] && kill -0 $tsp 2>/dev/null && kill -0 $gop 2>/dev/null; do sleep 1 & wait $! || true; done
kill -TERM $tsp $gop 2>/dev/null || true
wait $tsp || true; wait $gop || true
[ $stopping = 1 ]
