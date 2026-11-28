#!/bin/sh
# Run from a verified release. The base must be an operator-selected Linux amd64
# OCI image, pinned by digest, with GNU tar, glibc, and the scene host's font and
# Chromium runtime libraries. No candidate checkout is used during provisioning.
set -eu
umask 077
cd "$(dirname "$0")/../.."
release=$(pwd -P)
base=/opt/fleet-design-renderer
image=${1:-}
case "$release" in
  /opt/fleet-design-verifier/releases/v*) ;;
  *) echo 'Use a verified Fleet release directory' >&2; exit 1 ;;
esac
[ "$(id -u)" = 0 ] && [ "$#" = 1 ] && [ -f deploy/design-verifier/build-inputs.json ] || exit 1
printf '%s\n' "$image" | grep -Eq '^[A-Za-z0-9./:_-]+@sha256:[a-f0-9]{64}$' || {
  echo 'Provide an approved OCI image by digest' >&2; exit 1
}
[ ! -e "$base/rootfs" ] || { echo 'Move the previous renderer rootfs aside while the verifier is stopped' >&2; exit 1; }
install -d -m 0755 "$base"
stage=$(mktemp -d "$base/.stage.XXXXXXXX")
container=
cleanup() {
  if [ -n "$container" ]; then podman rm "$container" >/dev/null; fi
  rm -rf "$stage"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
install -d "$stage/rootfs"
container=$(podman create --pull=never --platform linux/amd64 --network none "$image")
podman export --output "$stage/rootfs.tar" "$container"
tar -xf "$stage/rootfs.tar" -C "$stage/rootfs" --no-same-owner --same-permissions
# The new namespace mounts its own proc, dev, tmp, and run. Reject special
# files anywhere in the base and stage only the downloaded, hash-checked tools.
if find "$stage/rootfs" -xdev \( -type s -o -type b -o -type c -o -type p \) -print -quit | grep -q .; then
  echo 'The renderer base contains a special file' >&2; exit 1
fi
mkdir -p "$stage/rootfs/work"
for dir in opt usr proc dev run tmp work; do
  [ ! -L "$stage/rootfs/$dir" ] && [ -d "$stage/rootfs/$dir" ] || exit 1
done
runtime="$stage/rootfs/opt/fleet-design-runtime"
mkdir "$runtime"
install -d "$runtime/node" "$runtime/chromium" "$runtime/playwright"
pins="$release/config/tool-pins.json"
tool() { jq -er --arg tool "$1" --arg key "$2" '.targets["linux-amd64"][$tool][$key]' "$pins"; }
download() {
  curl --proto '=https' --tlsv1.2 -fsSL --max-time 180 "$(tool "$1" url)" -o "$stage/$1"
  printf '%s  %s\n' "$(tool "$1" "$2")" "$stage/$1" | "${2}sum" --strict -c -
}
download node sha256
tar -xzf "$stage/node" -C "$runtime/node" --strip-components=1 --no-same-owner
download chromium sha256
unzip -q "$stage/chromium" -d "$runtime/chromium"
download playwright sha512
tar -xzf "$stage/playwright" -C "$runtime/playwright" --strip-components=1 --no-same-owner

# Fetch the release's integrity-locked npm dependencies with an empty
# environment, an unprivileged user and a disposable home and store.
mkdir "$stage/deps"
cp "$release"/deploy/design-verifier/runtime-lock/* "$stage/deps/"
chmod 0711 "$stage"
chown -R nobody "$stage/deps"
# Corepack and pnpm install only the signed release manifest and never run package lifecycle scripts.
(
  cd "$stage/deps"
  runuser -u nobody -- env -i PATH=/usr/bin:/bin HOME="$stage/deps" COREPACK_HOME="$stage/deps/corepack" \
    corepack pnpm install --frozen-lockfile --ignore-scripts --package-import-method=copy --store-dir "$stage/deps/store"
)
mv "$stage/deps/node_modules" "$runtime/"
chown -hR root:root "$runtime"
lock=$(sha256sum "$stage/deps/pnpm-lock.yaml" | cut -d ' ' -f 1)
# Font inventory is an evidence identifier. Use the same distro/font set as the designer host.
fonts=$(
  cd "$stage/rootfs"
  find usr/share/fonts -type f -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum | sha256sum | cut -d ' ' -f 1
)
jq -n --arg image "${image##*@sha256:}" --arg fonts "$fonts" --arg lock "$lock" \
  --arg node "$(tool node version)" --arg chromium "$(tool chromium version)" --arg playwright "$(tool playwright version)" \
  '{imageSha256:$image,fontSha256:$fonts,lockSha256:$lock,node:$node,chromium:$chromium,playwright:$playwright}' > "$runtime/runtime.json"
# No installed file is writable to the service or candidate. Leave privileges on
# host paths alone and do not bind any host runtime socket into either guest.
find "$stage/rootfs" -xdev -type f -perm /6000 -exec chmod a-s {} +
chmod -R a+rX,go-w "$runtime"
chmod 0755 "$stage/rootfs"
mv -T "$stage/rootfs" "$base/rootfs"
