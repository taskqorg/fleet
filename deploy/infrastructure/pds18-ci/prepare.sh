#!/usr/bin/env bash
# Run as root on PDS18 after reviewing capacity and the dedicated runner group.
set -euo pipefail
[ "$(id -u)" = 0 ]
ip -4 address show | grep -q '155\.103\.4\.2/'
[ -c /dev/kvm ]
[ ! -e /var/lib/fleet-ci-vm ] || { echo 'Existing guest state: inspect, never overwrite.' >&2; exit 1; }
# The VM's complete 10 GiB host envelope must fit current available memory.
[ "$(awk '/MemAvailable/ {print $2}' /proc/meminfo)" -gt 10485760 ]
[ "$(df --output=avail -k /var/lib | tail -1)" -gt 67108864 ]
base=$(cd -- "$(dirname -- "$0")" && pwd)
proof=$(mktemp -d)
trap 'rm -rf "$proof"' EXIT
url=https://cloud-images.ubuntu.com/releases/noble/release-20261017
curl --fail --silent --show-error "$url/SHA256SUMS" -o "$proof/SHA256SUMS"
curl --fail --silent --show-error "$url/SHA256SUMS.gpg" -o "$proof/SHA256SUMS.gpg"
gpgv --keyring /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg "$proof/SHA256SUMS.gpg" "$proof/SHA256SUMS"
image=ubuntu-24.04-server-cloudimg-amd64.img
expected=04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94
grep -Fx "$expected *$image" "$proof/SHA256SUMS"
# A read-only signed base image is shared with exactly one writable guest overlay.
install -d -m 0755 /var/lib/fleet-ci-image
curl --fail --silent --show-error --limit-rate 5M "$url/$image" -o "$proof/$image"
echo "$expected  $proof/$image" | sha256sum --check
install -m 0444 "$proof/$image" /var/lib/fleet-ci-image/noble.img
# No upgrades, production restart or broad VM platform installation.
packages=(qemu-system-x86=1:10.2.1+ds-1ubuntu3.3 qemu-utils=1:10.2.1+ds-1ubuntu3.3 cloud-image-utils=0.33-1build1)
LC_ALL=C apt-get --simulate --no-install-recommends install "${packages[@]}" > "$proof/apt-plan"
grep -E '^0 upgraded, [0-9]+ newly installed, 0 to remove' "$proof/apt-plan"
NEEDRESTART_MODE=l apt-get install --yes --no-install-recommends "${packages[@]}"
useradd --system --user-group --home-dir /var/lib/fleet-ci-vm --shell /usr/sbin/nologin fleet-ci-vm
install -d -o fleet-ci-vm -g fleet-ci-vm -m 0700 /var/lib/fleet-ci-vm
install -d -m 0700 /var/lib/fleet-ci-admin
ssh-keygen -q -t ed25519 -N '' -C fleet-ci-guest-management -f /var/lib/fleet-ci-admin/guest-key
pub=$(cat /var/lib/fleet-ci-admin/guest-key.pub)
sed "s|GUEST_MANAGEMENT_PUBLIC_KEY|$pub|" "$base/cloud-init.yaml" > "$proof/user-data"
echo 'instance-id: fleet-ci-pds18-1' > "$proof/meta-data"
cloud-localds --network-config="$base/network-config.yaml" /var/lib/fleet-ci-vm/seed.img "$proof/user-data" "$proof/meta-data"
qemu-img create -f qcow2 -F qcow2 -b /var/lib/fleet-ci-image/noble.img /var/lib/fleet-ci-vm/guest.qcow2 64G
chown fleet-ci-vm:fleet-ci-vm /var/lib/fleet-ci-vm/*
uid=$(id -u fleet-ci-vm)
# A separate output table affects only the new service UID; existing tables remain intact.
cat > /etc/fleet-ci-network.nft <<RULES
table inet fleet_ci {
  chain output {
    type filter hook output priority -10; policy accept;
    meta skuid $uid ct state established,related accept
    meta skuid $uid meta nfproto ipv6 counter reject
    meta skuid $uid ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.0.0.0/24, 192.0.2.0/24, 192.168.0.0/16, 198.18.0.0/15, 198.51.100.0/24, 203.0.113.0/24, 224.0.0.0/4, 240.0.0.0/4, 155.103.4.0/30, 23.239.109.234 } counter reject
  }
}
RULES
nft --check --file /etc/fleet-ci-network.nft
install -m 0644 "$base/fleet-ci-network.service" "$base/fleet-ci-vm.service" /etc/systemd/system/
systemctl daemon-reload
echo 'Prepared, not started. Review firewall and units, then start fleet-ci-vm.service.'
