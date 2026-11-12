# PDS18 warm runner pool

One persistent Ubuntu 24.04 KVM guest, 12 vCPU / 16 GiB / 160 GiB disk, four GitHub runners sharing one warm cache tree. It runs the heavy jobs of merge queue groups of `main` and pushes to `main` (`ci.yml` go, app, browser and release-build jobs, `release.yml` build) when it has a free slot; everything else, pull requests included, stays GitHub-hosted. This is **not** an ephemeral or fork-safe runner. No Fleet production mounts, credentials, sockets or host root access enter the guest.

## Trust

The pool runs code that reached a merge queue group of `main`: a reviewed pull request with the `code-review` status, enqueued by a collaborator, or a push to `main`. Pull request runs, a fork's included, never reach it. The slots share the warm caches (Go build and module caches, Playwright browsers) and the docker daemon, so one admitted job can leave entries a later job, a release build included, reads. That is the accepted boundary: the same queue code already builds the speculative release (`ci.yml` release-build) that `release.yml` signs. Per-slot users isolate jobs from each other's processes, ports-by-socket, `/dev/shm` and `HOME`; they are not a security boundary.

## Admission

Organization runner group `fleet-pds18-trusted-main` (id 6) admits only repository `taskqorg/fleet` and jobs defined in the selected workflows at `refs/heads/main`. The API takes no wildcard ref (`ci.yml@refs/heads/gh-readonly-queue/main/*` is refused with HTTP 400, ref not found), so a job defined in `ci.yml` of a merge queue group can never be admitted. Merge queue jobs reach the pool through the reusable `.github/workflows/pds18.yml`, which `ci.yml` calls at `@main`: a selected reusable workflow admits the jobs defined in it, whatever the caller. Its job goes to the pool only when the caller's event is `merge_group` on `refs/heads/gh-readonly-queue/main/*` or `push` to `refs/heads/main`, which no caller can change; any other caller gets a hosted job that fails at once (never a skipped one, which would pass).

Read back on 2026-11-12 (`gh api orgs/taskqorg/actions/runner-groups/6`): `restricted_to_workflows=true`, `visibility=selected`, `allows_public_repositories=true`, selected repository `taskqorg/fleet` only, `selected_workflows`:

* `taskqorg/fleet/.github/workflows/ci.yml@refs/heads/main`
* `taskqorg/fleet/.github/workflows/pds18.yml@refs/heads/main`
* `taskqorg/fleet/.github/workflows/pds18-heartbeat.yml@refs/heads/main`
* `taskqorg/fleet/.github/workflows/release.yml@refs/heads/main`

Labels and `if` conditions are routing, not the security boundary. Never enable public access on Default or reuse another provider's group.

## Routing and fallback

The `selection` job of `ci.yml` runs `scripts/pds18-route.ts`. Free slots are the pool size (4) less the pool's jobs queued or running in the repository. release-build takes a free slot always (warm: 69-106 s on the pool against 68-125 s hosted, its recent coordinators stay extracted); the others only when 14 or more of the repository's jobs are queued or running on hosted runners (of 20 per organization), app, go vet and the browser shards first, go outside last. PDS18's CPU is a 2014 Xeon E5-2620 v3: warm on the pool, setup is about 3 s against 20-40 s hosted, but go outside took 275-290 s against 184 s and browser shard 0 106 s against 49-80 s (2026-11-12), so the pool pays only where its caches win or hosted would make the job wait. Go unit stays hosted: two executor MCP owned-dependency tests fail on the guest only. The pool counts as down or full, and everything runs hosted, when the newest successful `pds18-heartbeat.yml` run (every 5 minutes on the pool) is older than 40 minutes (GitHub delays scheduled runs), or when any pool job, a heartbeat included, has waited for a runner for more than 2 minutes; a dead pool shows within one beat. Any API error routes hosted. Jobs already routed have no hosted fallback: the `pds18-watch` job cancels its run when one has waited 5 minutes while no pool job runs, so the queue drops the group at once instead of at its timeout, and the next run routes hosted. Each heavy family has a hosted job and a PDS18 twin; the `app` qualification merges the two results, and `release.yml` reads whichever release-build ran. The job commands live in `scripts/ci-job.sh`, shared by both.

## Provision

Review `prepare.sh`, the two units and cloud-init first. Run `prepare.sh` as root on PDS18 only after checking current capacity/pressure and apt simulation. It verifies the signed pinned official Ubuntu image and its actual bytes, installs the three pinned distro packages, creates a dedicated VM user and guest-management key, and prepares a 64 GiB maximum guest disk. It refuses to overwrite existing guest state. No production service restart, existing firewall edit, VM start or runner registration is performed by preparation. An interrupted preparation requires inspection before resuming, never deletion/re-run blindly.

The VM runs in its own top-level `fleet-ci.slice` (CPU and IO weight 10; production runs in `system.slice` and `user.slice` at weight 100, so a low weight on the VM unit inside `system.slice` alone would still win half the host from `user.slice`), bounded to 1200% CPU (12 of 24 cores), 18 GiB host memory with no host swap and 100 MB/s read/write. The host disk scheduler is mq-deadline, which ignores IO weights; the bandwidth cap is the IO bound. Guest RAM is 16 GiB. Review these host isolation limits before boot; availability is an observation, not a reservation. The dedicated nft output table affects only the VM UID. It rejects private/link-local/metadata, host subnet, VPN peer and IPv6 destinations while preserving public DNS/TLS. Established connections allow replies for the loopback-only guest-management SSH forward. QEMU user networking exposes no inbound public port. Existing production firewall tables are untouched.

After reviewing `/etc/fleet-ci-network.nft` and `systemctl cat fleet-ci-vm`, start `fleet-ci-vm.service` without enabling boot startup yet. Wait for guest cloud-init using the dedicated root-owned `/var/lib/fleet-ci-admin/guest-key` through `ci@127.0.0.1:22222`; keep the guest host key pinned in a dedicated known_hosts file. Do not disable SSH host-key checking. Inspect resource/pressure/site observations before further load. Complete guest security upgrades and reboot the guest if `/var/run/reboot-required` exists before registering online; this does not restart any production service.

Run `check-network.py` inside the guest before registration. Check the host nft reject counter increased for actual probes to the known listening host SSH port; connection failure alone could mean an unused port. Verify public GitHub/artifact HTTPS and DNS. Also check the VM process UID, cgroup limits and absence of guest production mounts/host sockets. No browser launch is part of provisioning.

Before registering, read back `busctl get-property org.freedesktop.login1 /org/freedesktop/login1 org.freedesktop.login1.Manager RemoveIPC`: it must print `b false` ([logins on the guest](#logins-on-the-guest)).

## Runner pool

Download official `actions/runner` Linux x64 v2.338.2 and verify SHA256 `baebf204307bac6193480852732f0aba2ae97b6c5597c244f4d5558390147c40` (the release asset digest the GitHub API returns); `pool.sh` does both. Run `pool.sh <slots>` as root inside the guest after the group readback and network checks above, with one short-lived registration token on stdin (`gh api -X POST orgs/taskqorg/actions/runners/registration-token -q .token`); no token in argv, repo, seed image or shell history. It is idempotent and registers only missing slots:

* slot 1 is user `ci` (`fleet-pds18-kvm-1`), slot n user `ci<n>` (`fleet-pds18-kvm-<n>`); group `fleet-pds18-trusted-main`, label `fleet-pds18`, work directory `_work`, service via the official `svc.sh`;
* every slot's runner `.env` points `GOCACHE`, `GOMODCACHE` and `PLAYWRIGHT_BROWSERS_PATH` into `/srv/ci-cache`, setgid group `cipool`, group-writable through a default ACL (jobs keep umask 022: the executor's tests refuse group-writable paths of their own, and a `UMask=0002` runner hung them); toolchains (`actions/setup-*`) stay in each slot's `_work/_tool`, since the tool cache replaces a version's directory without a lock, and so does the pnpm store (pnpm hardlinks store files and then chmods the bins, which only their owner may: EPERM across slots);
* only new slots are started; restart an idle existing slot's service to apply a changed `.env`;
* each job checks out into its slot's clean `_work`, starts its PostgreSQL in `$RUNNER_TEMP/pg` (stopped at job end, `FLEET_TRIAL_PGDATA`) and its ClickHouse as a service container on a per-job host port; the runner removes the containers and kills leftover processes at job end.

Git comes from `ppa:git-core/ppa`, as on the hosted image (Ubuntu's 2.43 lacks `--no-lazy-fetch`, which the executor uses). Chromium's system packages are installed once as root (`npx playwright@<version> install-deps chromium`); jobs only fetch the browser into the shared path when its version changes. Read back four runners online in the group (`gh api orgs/taskqorg/actions/runners`). Do not create a second registration of a slot on uncertain completion.

## Logins on the guest

logind keeps users' IPC: `RemoveIPC=no` in `/etc/systemd/logind.conf.d/50-fleet-keep-ipc.conf`, written by cloud-init. With the default, a runner service, which is no login session, loses its shared memory when the last SSH session of its user ends, and a job's PostgreSQL fails. It is host-wide rather than linger per user because the guest exists only for the pool. On an existing guest, write the same drop-in and run `systemctl restart systemd-logind`. Every `pds18.yml` job and the release `build` job fail first thing when the readback is not `b false`.

## Guest state the release job keeps

`~/fleet-releases/v/<version>` under each slot's user holds the last 5 releases' `fleet-coordinator`, `config/` and `release.json` for the migration check ([delivery pipeline, section 5](../../../docs/designs/fleet-delivery-pipeline.md#5-compatibility-across-skipped-versions)). The release job fills and prunes it; deleting it is safe, the next run downloads again.

## Rollback

To take the pool out of routing without a pull request, stop the runner services (`systemctl stop 'actions.runner.*'` in the guest): the heartbeat goes stale and `ci.yml` routes hosted within 20 minutes, or at once for any run whose pool job waited 2 minutes. Drain before stopping; do not interrupt uncertain work. To remove it, restore hosted-only jobs through the normal reviewed PR, remove only the verified registrations with the supported GitHub API, stop/disable `fleet-ci-vm.service`, and remove only `table inet fleet_ci` after the VM is stopped. Leave existing firewall tables and production services alone. Keep guest disk/evidence for review; deletion is a separate explicit action.
