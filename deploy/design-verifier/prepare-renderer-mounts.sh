#!/bin/sh
# Staging only: make the fixed bwrap mount targets before rootfs is made read-only.
set -eu
root=$1
[ ! -L "$root" ] && [ -d "$root" ] || { echo 'Invalid renderer root' >&2; exit 1; }
if [ ! -e "$root/trusted" ] && [ ! -L "$root/trusted" ]; then mkdir "$root/trusted"; fi
[ ! -L "$root/trusted" ] && [ -d "$root/trusted" ] || { echo 'Invalid worker mount target' >&2; exit 1; }
chmod 0755 "$root/trusted"
if [ ! -e "$root/trusted-compare" ] && [ ! -L "$root/trusted-compare" ]; then
  ( set -C; : > "$root/trusted-compare" )
fi
[ ! -L "$root/trusted-compare" ] && [ -f "$root/trusted-compare" ] && [ ! -s "$root/trusted-compare" ] || {
  echo 'Invalid comparator mount target' >&2; exit 1;
}
chmod 0644 "$root/trusted-compare"
