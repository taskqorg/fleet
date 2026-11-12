#!/usr/bin/env bash
# Run as root inside the guest: the warm runner pool (README, "Runner pool"). Idempotent; registers only missing slots.
#   pool.sh <slots>            e.g. pool.sh 4: slot 1 is user ci (fleet-pds18-kvm-1), slot n is user ci<n>
# A missing slot needs a registration token: feed one on stdin (gh api -X POST orgs/taskqorg/actions/runners/registration-token -q .token).
# Each slot is its own user, so one job's pkill, /dev/shm cleanup, HOME and ports-by-socket stay its own; the warm caches
# are one group-writable tree all slots share. Toolchains stay per slot (actions/tool-cache replaces a version's
# directory without a lock). Only new slots start; restart an existing idle slot's service to apply a changed .env.
set -euo pipefail
slots=${1:?slots}
cache=/srv/ci-cache
version=2.338.2 sha=baebf204307bac6193480852732f0aba2ae97b6c5597c244f4d5558390147c40
[ "$(busctl get-property org.freedesktop.login1 /org/freedesktop/login1 org.freedesktop.login1.Manager RemoveIPC)" = "b false" ]
getent group cipool >/dev/null || groupadd cipool
# The hosted image's Git (git-core PPA): Ubuntu's 2.43 lacks --no-lazy-fetch, which the executor passes.
git -C / --no-lazy-fetch version >/dev/null 2>&1 || { add-apt-repository -y ppa:git-core/ppa && NEEDRESTART_MODE=l apt-get install -y -q git; }
install -d -o root -g cipool -m 2775 "$cache" "$cache"/{go-build,gomod,pnpm-store,ms-playwright}
# Group write through a default ACL, not the runners' umask: jobs keep umask 022, since the executor's tests (and its
# hardening) refuse group-writable files and directories of their own.
command -v setfacl >/dev/null || NEEDRESTART_MODE=l apt-get install -y -q acl
setfacl -R -m g:cipool:rwX -m d:g:cipool:rwX -m d:m::rwx "$cache"
token= new=
for n in $(seq 1 "$slots"); do
  user=ci; [ "$n" = 1 ] || user=ci$n
  id "$user" >/dev/null 2>&1 || useradd --create-home --shell /bin/bash "$user"
  usermod -aG cipool,docker "$user"
  home=$(getent passwd "$user" | cut -d: -f6) dir=$home/actions-runner name=fleet-pds18-kvm-$n
  if [ ! -f "$dir/.runner" ]; then
    [ -n "$token" ] || read -r token
    [ -n "$token" ]
    if [ ! -x "$dir/config.sh" ]; then
      tgz=/var/cache/actions-runner-linux-x64-$version.tar.gz
      [ -f "$tgz" ] || curl -fsSL -o "$tgz" "https://github.com/actions/runner/releases/download/v$version/actions-runner-linux-x64-$version.tar.gz"
      echo "$sha  $tgz" | sha256sum --check --quiet
      install -d -o "$user" -g "$user" "$dir"
      sudo -u "$user" tar -xzf "$tgz" -C "$dir"
    fi
    (cd "$dir" && ACTIONS_RUNNER_INPUT_TOKEN=$token sudo -u "$user" --preserve-env=ACTIONS_RUNNER_INPUT_TOKEN ./config.sh --unattended \
      --url https://github.com/taskqorg --runnergroup fleet-pds18-trusted-main --name "$name" --labels fleet-pds18 --work _work)
  fi
  unit=actions.runner.taskqorg.$name.service
  rm -f "/etc/systemd/system/$unit.d/pool.conf"
  # A slot whose service install did not complete (config.sh wrote .runner first) is installed here too.
  if [ ! -f "/etc/systemd/system/$unit" ]; then (cd "$dir" && ./svc.sh install "$user"); new="$new $name"; fi
  # The shared caches.
  cat > "$dir/.env" <<ENV
LANG=C.UTF-8
GOCACHE=$cache/go-build
GOMODCACHE=$cache/gomod
npm_config_store_dir=$cache/pnpm-store
pnpm_config_store_dir=$cache/pnpm-store
PLAYWRIGHT_BROWSERS_PATH=$cache/ms-playwright
ENV
  chown "$user:$user" "$dir/.env"
done
systemctl daemon-reload
for name in $new; do systemctl restart "actions.runner.taskqorg.$name.service"; done
