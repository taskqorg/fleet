# Fleet self-update

*   **Author:** Alex Kirs
*   **Status:** Proposed
*   **Date:** 2026-11-11
*   **Input:** the self-update brief (in the pull request description), accepted by the owner on 2026-11-11 with its six recommendations and decision 8 ("a host works with the documented default paths only; an update never rewrites host env files")

--------------------------------------------------------------------------------

## Overview

Every Fleet host installs a new release by itself. The owner pushes a tag `v<version>` on a green `main` commit. A release workflow publishes one tarball per target on GitHub Releases with checksums signed by the release key and build provenance. On each host a one-shot `fleet-update` command, started every 5 minutes by a systemd timer or a launchd agent, decides whether a newer release should run there, verifies it, installs it into `releases/<version>/`, switches `current`, restarts that host's Fleet services and rolls back by itself when the new version does not come up healthy.

The design copies the CoreQ update model ([coreq.md](https://github.com/taskqorg/coreq/blob/main/coreq.md), sections Release artifacts, Update, Launcher) into Go; Fleet does not link the Rust crate ([stack](fleet-stack.md)). `COREQ-R<n>` names the CoreQ rule a step copies; [deviations](#deviations-from-coreq) are listed. `R2` and `R5` are principles of the [Fleet contract](../../fleet.md#principles).

### Terms

*   **Host, holder, host record**: a machine running Fleet services, the person who holds it, and its record `host:<id>` ([people access §3](fleet-people-access.md#3-hosts-records-secrets-and-trust)).
*   **Host observation**: document kind `host-observation`, rewritten by the coordinator on every executor poll (`go/coordinator/a_poll.go`).
*   **Host problem**: an entry in the observation's `problems`, shown by `hosts_list`.
*   **`hosts_list`, `hosts_update`**: shared operations (R2), callable by agents, the web app and `POST /api/ops/<name>`.
*   **Needs-you question**: a row in the `question` table addressed to a person, keyed by `cause`; dispatch stall alerts use it (`go/coordinator/a_dispatch.go`).
*   **Setting change**: a `setting-change` document recorded for every host-field change (`hostsUpdate`).

## The Problem

Fleet runs on three hosts: PDS18 (Linux: coordinator, gateway, Access origin guard, executor), the owner's Mac (executor) and Windows/WSL (executor). Hosts held by other people follow. Every install is manual ([deploy runbook](../../deploy/README.md)): download two CI artifacts, repack the Mac tarball, copy files, check SHA-256, take a `pg_dump` as `postgres`, run `install-release.sh`, restart units. Agent sessions must coordinate so installs do not cross. The cost grows per host, and a host held by someone else cannot wait for the owner.

Also wrong today:

*   CI publishes only workflow artifacts. They expire, need an authenticated `gh`, and carry no provenance ([executor delivery](fleet-executor-delivery.md)).
*   Executors report no version.
*   Executor env files carry values every host needs (`FLEET_STORE_ENDPOINT_HOSTS`, the Mac `PATH`). A missing line breaks uploads silently.

## Goals

*   A tag `v*` on a green `main` commit is the only release action; no host needs a human after its first install.
*   An executor never runs a version newer than its coordinator's healthy version.
*   A release that fails verification, selftest or health is never left running.
*   A transient failure (network, GitHub, Sigstore, coordinator restart) never blocks a release; the next run retries.
*   The holder can switch updates off or hold them until a time, on their host only.
*   `hosts_list` shows each host's running version and last update result; a failed update raises one needs-you question per host and version.
*   No install step asks for a hand-edited env line; every default ships in the release.

## Non-Goals

*   Release channels, staged rollouts, signing beyond GitHub build provenance, coordinator push.
*   Automatic database restore.
*   Operator UI buttons "update now", "roll back", release notes (stage 2).
*   Windows native executor.

## Decisions (defaults the owner can overturn)

The brief left four points to the draft.

*   **Executor lag.** The coordinator serves executors of its previous release (N-1) and its own (N). An older executor keeps working and shows host problem `version-behind`; nothing is refused ("help, never block"). Any host protocol change stays readable by an N-1 executor; a removal waits one release (expand, then contract).
*   **Version scheme.** CoreQ's as is: `N.yymmdd.M`, major, UTC date of the tag, sequence on that day from 0. First release `1.261111.0`, tag `v1.261111.0`. Comparison by numeric dot parts.
*   **`autoUpdate`.** Host field: `"on"` (default when absent), `"off"`, or an RFC 3339 time ending in `Z` meaning "hold until then". `hosts_update` refuses other forms with 400; a time in the past is accepted and means on. The host compares against its own UTC clock.
*   **Minimal env.** One line, `FLEET_HOST_ID=<id>`, the host's identity. The coordinator URL `https://fleet.ufobe.com` and every other value ship as release defaults ([section 5](#5-config-defaults-and-env)). Other env lines remain optional overrides.
*   **`version-behind` edge.** Flagged at N-2 or older; N-1 is served without a flag.

### Decision: release signature

Owner rule (2026-11-11): standard library first; sigstore only for a guarantee Ed25519 lacks for Fleet. The guarantees Fleet needs are "built by `release.yml` on a `v*` tag of `taskqorg/fleet`" and "not altered after the build". An Ed25519 key held only by the GitHub environment `release` (deployment rule: tags `v*`) gives both: only a job of a `v*` tag run can read it, and the signature covers `SHA256SUMS`, whose lines name the version. Sigstore adds a public transparency log and no long-lived key; for one repository with one owner neither is needed at update time. So `release.yml` signs `SHA256SUMS` with the private key (secret `FLEET_RELEASE_KEY`, environment `release`) into `SHA256SUMS.sig`, and `fleet-update` checks it with `crypto/ed25519` against the public key compiled in (`go/update/main.go`). Zero new modules; no network service besides GitHub at update time. `actions/attest-build-provenance` still runs, so the release carries public provenance (`provenance.sigstore.jsonl`) anyone can check with `gh attestation verify`. Key rotation: a new key pair, the secret replaced, the new public key in the next release; hosts take that release with the old key's signature.

## Critical User Journeys (CUJs)

### CUJ 1: Owner releases

*   **Given**: commit C on `main`; its `ci` run may still be running. Every host has `autoUpdate: "on"`.
*   **When**: the owner runs `git tag v1.261111.0 C && git push origin v1.261111.0`.
*   **Then**: the release workflow waits for C's `build-test-design` job, builds, publishes. Within 5 minutes PDS18's run installs the coordinator, gateway, guard and the PDS18 executor. Each other executor installs on its next run after the coordinator is healthy. `hosts_list` shows `version: 1.261111.0`, `update.state: ok` for every host.

### CUJ 2: Bad release on PDS18

*   **Given**: release N's coordinator exits at start.
*   **When**: PDS18's `fleet-update` installs N.
*   **Then**: three unhealthy starts in one run; `current` goes back to `previous`; N is refused on PDS18; the coordinator never reported N as healthy, so no executor moves. The owner gets one needs-you question "pds18: update to N rolled back (unhealthy)". Runs skip N until a newer tag appears.

### CUJ 3: Holder holds updates

*   **Given**: Alina holds her Mac and has a demo tomorrow.
*   **When**: she calls `hosts_update {id: "alina-mac", autoUpdate: "2026-11-13T09:00:00Z"}`.
*   **Then**: her executor stays on its version until that time, then installs on the next run. If the coordinator moved two releases meanwhile, her row shows `version-behind`; work still runs.

### CUJ 4: Tampered asset

*   **Given**: a release asset does not match its provenance.
*   **When**: `fleet-update` downloads it.
*   **Then**: verification fails before any file of it runs; nothing changes; that version is refused on the host with reason `provenance`; one needs-you question.

## Technical Plan (Architecture)

### Parts

```text
 owner: git tag v<ver> ──► release.yml: wait for green CI, build 2 tarballs, selftest (linux + macOS),
                           migration check, SHA256SUMS + .sig, latest.json, provenance ──► GitHub Release (public)
                                                                     ▲ fetched by every fleet-update
                                                                     │
 PDS18 root timer ─ fleet-update pds18 ─────────────────────────────┤
   reads latest.json; version + autoUpdate from 127.0.0.1 /api/health│
   stage + verify + selftest in /var/lib/fleet-update (root only)     │
   pg_dump, install /home/fleet/releases, restart coordinator/gw/guard│
   health 30 s, 3 starts → rollback; write releases/<ver>/healthy     │
   then install /opt/fleet-agent/releases, restart PDS18 executor     │
                                                                      │
 other host user timer ─ fleet-update executor ───────────────────────┘
   reads <data>/update/update.json (target, autoUpdate) written by its executor
   verify, selftest, install ~/releases, restart executor, health, write update-state.json

 executor poll ──► coordinator /api/hosts/poll
   body: version, update (from update-state.json)    answer: + target, autoUpdate
   coordinator: host-observation.version/update ─► hosts_list, version-behind, needs-you
   target = coordinator's newest version with a healthy marker
   pds18 row: coordinator also reads /var/lib/fleet-update/pds18-state.json
```

### 1. Release workflow

`.github/workflows/release.yml`, `on: push: tags: ['v*']`. Permissions per job, only what each writes (COREQ-R7).

*   **Job `gate`** (`contents: read`, `actions: read`, checkout with `fetch-depth: 0`):
    *   Refuse a tag whose name is not `v<N>.<yymmdd>.<M>` (strict regex `^v[1-9][0-9]*\.[0-9]{6}\.(0|[1-9][0-9]*)$`) with `yymmdd` the UTC date of the annotated tag's tagger date (lightweight tag: the commit date), or whose commit is not an ancestor of `origin/main`, or whose version is not newer than the current latest release.
    *   Find the `ci` run with `gh run list --workflow ci --commit <sha> --event push --branch main --json databaseId,status`. No run yet: poll every 30 s. Then `gh run watch <id>` until it completes. Both waits end at the gate job's `timeout-minutes` (GitHub's default 360). Proceed only when job `build-test-design` concluded `success`; other jobs do not gate, as on `main` today. A timeout fails the release; the owner re-runs the workflow, no new tag needed.
    *   Output `previous`: the latest earlier `v*` tag (empty for the first release).
*   **Job `build`** (`needs: gate`, `contents: read`): `deploy/build-release.sh <commit> out <version> <previous> linux/amd64 darwin/arm64`, the one build script for releases and local trials. Each tarball `fleet-<ver>-<os>-<arch>.tgz` holds `fleet-coordinator`, `fleet-gateway`, `fleet-executor`, `fleet-update`, `dist/app/`, `config/`, `deploy/` (unit files, `install-release.sh`, `status.sh`) and `release.json` `{version, previous, commit, target, builtAt}`. Runs every linux binary's `selftest`. Uploads the tarballs with `actions/upload-artifact`.
*   **Job `selftest-macos`** (`needs: build`, `runs-on: macos-14`, `contents: read`): downloads the darwin tarball, runs each binary's `selftest` (COREQ-R25).
*   **Job `migrate-check`** (`needs: build`, `contents: read`): on the runner's PostgreSQL, start the new coordinator (migrates), stop it, start the `previous` release's coordinator on that database and require `/api/health` 200. Skipped when `previous` is empty. This enforces "expand in N, contract in N+1".
*   **Job `publish`** (`needs: [selftest-macos, migrate-check]`, environment `release`, `contents: write`, `id-token: write`, `attestations: write`): signs `SHA256SUMS` (written by the build) into `SHA256SUMS.sig` with `openssl pkeyutl -sign -rawin` and the secret `FLEET_RELEASE_KEY`, writes `latest.json` `{"version", "files": {"linux-amd64": "<file>", "darwin-arm64": "<file>"}}`; `actions/attest-build-provenance` over both tarballs, its bundle saved as `provenance.sigstore.jsonl`; `gh release create v<ver> --latest` with the six files.

`ci.yml` drops "Pack the release" and "Upload the release" and the comment that a push to `main` publishes a release; `fleet-update` joins its cross-build loop so pull request trials carry it.

### 2. Versions in binaries

*   Every binary has `var version = "dev"`, set by `-ldflags "-X main.version=<ver>"` (the gateway already has it).
*   `/api/health` adds `version` (and `autoUpdate`, section 4).
*   Each binary gets `selftest` (executor: a new entry of its `subcommands` map in `go/executor/main.go`; coordinator: checked in `main()` before the first `need(...)`), printing `<name> <version>: selftest ok`. Coordinator: parse `config/` defaults and the embedded migrations, no database. Executor: open an in-memory SQLite journal, load the harness table. `fleet-update`: decode the compiled-in release public key. CoreQ's offline selftest env (`<ENV>_SELFTEST_OFFLINE=1`) is not needed: no Fleet selftest uses the network.
*   `fleet-update` never installs `dev`.

### 3. `fleet-update`

New Go command `go/update/` (binary `fleet-update`), in every tarball, standard library only. One run, then exit; the timer repeats. The release repository `taskqorg/fleet` and the release public key are compiled in (COREQ-R8).

Subcommands:

*   `fleet-update pds18`: root on PDS18. Updates the coordinator side, then the PDS18 executor.
*   `fleet-update executor`: the executor service user on every other host.
*   `fleet-update selftest`.

#### 3.1 Layout

Releases root `R`: `~/releases` of the service user; on PDS18 `/home/fleet/releases` (coordinator side) and `/opt/fleet-agent/releases` (PDS18 executor, root-owned, so `fleet-agent` cannot replace its own executor).

*   `R/<ver>/`: unpacked tarball, directories 0755 and files read-only. Prune `chmod`s before removing.
*   `R/current`, `R/previous`: symlinks, replaced atomically (`current.tmp-<pid>`, then rename). Units keep `ExecStart=…/releases/current/<binary>`.
*   `R/<ver>/starts`, `R/<ver>/healthy`: unhealthy start count, healthy marker (COREQ-R16).
*   State directory `S`: executor hosts `<data dir>/update/`; PDS18 `/var/lib/fleet-update/` (root, 0755) with `stage/` (0700). `S/update-state.json` (PDS18: `S/pds18-state.json`, 0644, with keys `coordinator` and `executor`, one `last` each) holds `{refused: {"<ver>": "<reason>"}, last: {version, state, error, at}}`, written durably (COREQ-R22). `S/update.lock` is `flock`ed for the whole run.
*   The running version is read from `R/current/release.json`, never from the link name.
*   Prune at start: leftover `stage/` and `.tmp` entries; every `R/<ver>/` that is neither `current` nor `previous`; on PDS18 every dump in `/backup/fleet/pre-deploy/` except those of `current` and `previous` (the same rule).

#### 3.2 One run

1.  Take `update.lock`; if held, exit 0. Prune.
2.  Read the inputs.
    *   Executor mode: `<data dir>/update/update.json` `{target, autoUpdate, running, pollOk, pollError, at}`, written by the executor from its last poll answer. Missing file: exit (the executor has not polled yet).
    *   `pds18` mode: `version` and `autoUpdate` from `GET 127.0.0.1:9470/api/health` (the coordinator reports `autoUpdate` of its own host record). Coordinator not answering: treat as `"on"`, so an update can repair it. Candidate from `https://github.com/taskqorg/fleet/releases/latest/download/latest.json`.
3.  `autoUpdate` is `"off"` or a future time: record `held`, exit.
4.  Candidate: executor mode `target`; `pds18` mode `latest.json.version`. Stop when not newer than the running version (COREQ-R14), refused in state, or not matching the strict version regex of section 1 (stricter than COREQ-R12 step 2).
5.  Download the tarball for this `<os>-<arch>`, `SHA256SUMS` and `SHA256SUMS.sig` from `https://github.com/taskqorg/fleet/releases/download/v<ver>/` into the stage (`S/stage/` on PDS18; `R/<ver>.tmp/` elsewhere). Streamed to disk; no size cap beyond the checksum.
6.  Verify: the Ed25519 signature of `SHA256SUMS` against the compiled-in key, then the tarball's SHA-256 against its line (COREQ-R11, with a key instead of a certificate identity). `release.json` of the unpacked tree must name the candidate version.
7.  Unpack in the stage, run `selftest` of each binary this host runs (COREQ-R25).
8.  `pds18` mode: `runuser -u postgres -- pg_dump -Fc -d fleet -f /backup/fleet/pre-deploy/fleet-pre-<ver>.dump`.
9.  Install: `pds18` mode copies the staged tree to `/home/fleet/releases/<ver>/`, owner `fleet`; executor mode renames `.tmp` to `R/<ver>/`. Write `previous` = old `current`, then `current` = `<ver>`. Running binaries are never written in place (COREQ-R13).
10. Restart (3.3), then health (3.4).
11. Write `last` in state.

Failure classes:

| Failure | Result |
|---|---|
| Network, HTTP, disk, `pg_dump` | `state: error`, nothing refused, next run retries |
| Digest mismatch against `SHA256SUMS`, signature does not verify | `refused`, reason `checksum` / `provenance` |
| Selftest exits non-zero | `refused`, reason `selftest` |
| Three unhealthy starts | rollback, `refused`, reason `unhealthy` |

A truncated download fails the digest; to keep it transient, a digest mismatch is retried once with a fresh download in the same run before it refuses.

#### 3.3 Restarts

*   `pds18` mode, coordinator side: `systemctl --user -M fleet@ restart fleet fleet-gateway fleet-access-guard` (root drives the `fleet` user manager; needs linger for `fleet`, systemd 248+). After the coordinator side is healthy, the same run installs the same version into `/opt/fleet-agent/releases` and runs `systemctl --user -M fleet-agent@ restart fleet-executor`. That unit is the shared Linux unit plus the drop-in `deploy/infrastructure/fleet-executor-pds18.conf` (`ExecStart=/opt/fleet-agent/releases/current/fleet-executor`). Its `update.json` is at `/home/fleet-agent/.local/state/fleet/executor/update/update.json` (the default data directory of `fleet-agent`); executor health on PDS18 reads it. The PDS18 executor follows in the same run, not a later one.
*   Executor mode: Linux/WSL `systemctl --user restart fleet-executor`; macOS `launchctl kickstart -k gui/<uid>/com.ufobe.fleet-executor`.
*   After a healthy `pds18` install, the run copies the verified `fleet-update` to `/opt/fleet/update/fleet-update` (root-owned, temp file then rename): the root unit runs that path, so root never executes a file `fleet` can write.

#### 3.4 Health and rollback

One run does all three starts (COREQ-R16 counts starts; Fleet has no launcher, so `fleet-update` counts them).

*   **Coordinator side**: within 30 s of a start (the window `install-release.sh` uses today), coordinator `GET 127.0.0.1:9470/api/health` 200 with `database: ok` and `version` = candidate, and gateway `GET 127.0.0.1:8742/healthz` 200.
*   **Executor**: within 30 s, the unit is active and the executor's `update.json` shows `running` = candidate with `pollOk: true` (written after its first poll answered 200). The executor sets `pollError` instead when a poll fails: `network` (connection error, timeout, 3xx from the Access edge) or `http-<status>` for 5xx. Those two do not count as an unhealthy start: the run leaves the new version current, records `state: pending`, and the next run checks again. Unit not active, or a 401/403/4xx answer to the new executor, counts.
*   **Healthy**: write `R/<ver>/healthy` (on PDS18 root writes it into `/home/fleet/releases/<ver>/`, readable by `fleet`).
*   **Unhealthy**: increment `starts`, restart. At the third, `current` back to `previous`, restart, refuse the version. No `previous`: keep `current`, `state: failed`. systemd `Restart=on-failure` restarts inside one start window do not count separately.
*   Migrations run when the coordinator starts. A rollback runs the previous coordinator on the migrated database; the release `migrate-check` job proves that works. The dump is for manual recovery only.

#### 3.5 Units

*   PDS18: `deploy/infrastructure/fleet-update.service` (root, `Type=oneshot`, `ExecStart=/opt/fleet/update/fleet-update pds18`) and `fleet-update.timer` (`OnBootSec=2min`, `OnUnitActiveSec=5min`).
*   Linux/WSL executor: `deploy/hosts/linux/fleet-update.service` and `.timer`, user units, same timing, `ExecStart=%h/releases/current/fleet-update executor`.
*   Mac: `deploy/hosts/macos/fleet-update.plist`, `StartInterval` 300, `RunAtLoad`, log `~/Library/Logs/fleet-update.log`.

### 4. Coordinator and executor changes

*   **Host field `autoUpdate`**: validated and recorded by `hosts_update` like `intake`.
All poll changes are additive: an N-1 executor ignores new answer fields and the coordinator accepts bodies without the new fields (expand, then contract).

*   **Poll body** (`contracts/host-protocol.schema.json` `PollRequest`): optional `version` and `update` (`last` of the host's `update-state.json`).
*   **Poll answer** (`PollReply`): `target` (`healthyTarget`) and `autoUpdate` of the polling host.
*   **`healthyTarget`**: the coordinator's own `version` when `healthy` exists in its release directory (next to its executable), else the version of `releases/previous` when that has `healthy`, else null (no update offered). Read per request; two `stat` calls.
*   **`/api/health`** adds `version` and `autoUpdate` of the coordinator's own host (`FLEET_HOST_ID` of the coordinator, `pds18`); both are not secret.
*   **`poll`** writes `version` and `update` into the observation it already rebuilds, so one writer owns the row. After `hostProblems`, it appends `{kind: "version-behind"}` when the host's version is older than `previous` of the coordinator's `release.json`. `EdgeProblemKind` becomes `ProblemKind` with both values.
*   **Needs-you**: extract `openCauseQuestion(q, cause string, addressedTo, projectID any, text string) error` in `a_dispatch.go` from its lines that call `hookTaskAsk` and then set `cause` and `addressed_to`; dispatch and `poll` both use it. `poll` opens a question when `update.state` is `refused` or `failed` and no question with cause `update:<hostId>:<version>` is open or answered (one lookup by cause, so repeated polls add nothing). Kind `operator_decision`, options `["ok"]`, addressed to the host's holder, text `"<hostId>: update to <version> <rolled back|refused> (<reason>)."`. A poll with `state: ok` withdraws open `update:<hostId>:*` questions (`state='withdrawn', cause=NULL`). Dispatch's held-cause filter does not match `update:`, so dispatch never withdraws them.
*   **PDS18 row**: for its own host the coordinator also reads `/var/lib/fleet-update/pds18-state.json` and merges it with the polled `update`: the worse state wins (`failed` > `refused` > `error` > `pending` > `held` > `ok`), and `update.parts` lists coordinator and executor results.
*   **`hosts_list`** adds `version`, `autoUpdate`, `update` `{version, state, error, at}`.
*   **Executor**: writes `<data dir>/update/update.json` `{target, autoUpdate, running, pollOk, pollError, at}` only when a value changes (durably, temp file then rename); sends `version` and `update` in polls.

### 5. Config defaults and env

| Value | Release default |
|---|---|
| `FLEET_HOST_ID` | none: the one required line |
| `FLEET_COORDINATOR_URL` | `https://fleet.ufobe.com` |
| `FLEET_DATA_DIR` | Linux `~/.local/state/fleet/executor`, macOS `~/Library/Application Support/Fleet/executor` |
| `FLEET_HOST_SECRET_FILE` | `~/.config/fleet/host-secret` |
| `FLEET_STORE_ENDPOINT_HOSTS` | the three store tunnel hosts, in shipped `config/executor-defaults.json` |
| macOS `PATH` | set in the shipped plist's `EnvironmentVariables` (`/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin`) |
| `FLEET_CODEX_BIN` | `codex` on `PATH` |

Env values override defaults; section 5 applies to the executor only (the coordinator keeps its env). The units read `~/.config/fleet/executor.env` only if it exists (`EnvironmentFile=-`; plist `[ -f … ] && .`). PDS18's coordinator env files keep their secret lines; updates never touch env files.

### First install

Once per host: unpack a tarball with its `deploy/install-release.sh` (unpacks into `releases/<ver>/`, selftests every binary, links `current`), write the host secret with `scripts/host-secret.sh` at the default path, write `executor.env` with `FLEET_HOST_ID`, install the executor and `fleet-update` units from `deploy/`. Linux and WSL: `loginctl enable-linger <user>`; WSL also `systemd=true` in `/etc/wsl.conf`. PDS18: also copy `fleet-update` to `/opt/fleet/update/`, enable linger for `fleet` and `fleet-agent`, create `/var/lib/fleet-update`.

The three current hosts do this once: their env files are reduced to `FLEET_HOST_ID`, their data directories move to the default path with the executor stopped, and old `fleet-<sha12>-…` release directories are deleted.

### Deviations from CoreQ

*   `current`/`previous` are symlinks, not version files: existing units use `releases/current/<binary>`.
*   OS names `linux`, `darwin` (Go's), not `macos`.
*   No 64 MiB cap: the tarball holds the web app; checksum and provenance bound it.
*   No launcher: `fleet-update` counts starts and rolls back.
*   An Ed25519 signature of `SHA256SUMS` checked with Go's `crypto/ed25519`, not `gh attestation verify` ([Decision: release signature](#decision-release-signature)).

## Alternatives Considered

### Alternative A: Coordinator pushes updates

*   **Pros**: one decision point.
*   **Cons**: a broken coordinator cannot fix itself; it would drive installs on hosts it does not own.
*   **Reason for Rejection**: Rejected by author (brief, out of scope): pull keeps each host's authority with its holder.

### Alternative B: Link CoreQ's Rust crate

*   **Pros**: no reimplementation.
*   **Cons**: Rust toolchain and FFI in an all-Go repository.
*   **Reason for Rejection**: Rejected by author (brief): copy the contract, not the crate.

### Alternative C: Executors read `latest.json`

*   **Pros**: no coordinator involvement.
*   **Cons**: an executor can run ahead of its coordinator.
*   **Reason for Rejection**: Rejected by author (brief): executors install the coordinator's healthy version.

### Alternative D: `gh attestation verify`, as CoreQ

*   **Pros**: proven in CoreQ; no new Go dependency.
*   **Cons**: every host needs `gh` installed and authenticated.
*   **Reason for Rejection**: Rejected by author (brief).

### Alternative G: sigstore-go in `fleet-update`

*   **Pros**: keyless; checks the GitHub provenance itself, with the transparency log.
*   **Cons**: measured 2026-11-11 (sigstore-go v1.4.0): `go.sum` grows from 38 to 206 modules, a minimal verifier binary from 8.3 MB to 26.9 MB (static, darwin/arm64); every update needs the Sigstore TUF service.
*   **Reason for Rejection**: stack rule "standard library first" ([decision](#decision-release-signature)).

### Alternative E: Separate `/api/hosts/update` route

*   **Pros**: updater independent of the executor.
*   **Cons**: a second host-to-coordinator path and a second writer of the observation row, and the updater needs the host secret.
*   **Reason for Rejection**: the poll already carries host identity; the executor relays.

### Alternative F: Long-running updater (CoreQ launcher)

*   **Pros**: counts real service starts.
*   **Cons**: a process that must replace itself; CoreQ needs re-exec and launcher replacement rules.
*   **Reason for Rejection**: a one-shot run under a timer picks up the new updater from `current`.

--------------------------------------------------------------------------------

## Non-Functional Requirements

*   **Security.** Only a release signed in `release.yml` on a `v*` tag of `taskqorg/fleet` runs; the public key and repository are compiled in and checked before any release file runs. Root on PDS18 stages, verifies and selftests only in `/var/lib/fleet-update` and runs only `/opt/fleet/update/fleet-update`; it never executes a file in `/home/fleet`.
*   **Rollout.** Executors follow within one timer period after the coordinator is healthy.
*   **Rollback.** Automatic after three unhealthy starts; `install-release.sh --rollback` stays as the recovery path when `fleet-update` itself is broken.
*   **Observability (R5).** One log line per step (journal or `fleet-update.log`); results in `hosts_list`; failures as needs-you questions.

## Risks

*   **GitHub down**: transient; runs retry.
*   **Release key leaked**: rotate (see the decision); until then a forged release could install. The key is readable only by `v*` tag jobs of environment `release`.
*   **Disk full**: `pg_dump` or unpack fails as `error`; dumps and releases are pruned to `current` and `previous`.
*   **Coordinator down while an executor updates**: the start does not count; no refusal.
*   **Host clock skew**: only affects when a hold ends.
*   **Migration that breaks the previous coordinator**: stopped by `migrate-check` before publish.
*   **`fleet-update` itself broken in a release**: its selftest stops it before install; if it passes selftest but fails later, the previous `fleet-update` stays in `/opt/fleet/update` on PDS18; elsewhere the holder runs `install-release.sh --rollback`.

--------------------------------------------------------------------------------

### Additional Resources

*   [CoreQ](https://github.com/taskqorg/coreq/blob/main/coreq.md); [deploy runbook](../../deploy/README.md); [executor delivery](fleet-executor-delivery.md); [people access](fleet-people-access.md).

### Future Improvements

*   Stage 2: operator UI "update now" and "roll back" per host through shared operations (R2); release notes view.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

### Target Files to Create/Modify

Six phases, one pull request each; every phase builds and passes `go test ./...` and CI on its own. Phase order keeps the protocol additive: coordinators learn new fields before executors send them, and `ci.yml` stops packing only when `release.yml` exists.

#### Phase 1: versions and selftest

1.  `deploy/build-release.sh` (Modify): arguments `<commit> <out> <version> <previous> [targets]`; `-X main.version=<version>`; build `update` once it exists (phase 4; until then three binaries); ship `deploy/` (units, `install-release.sh`, `status.sh`); `release.json` `{version, previous, commit, target, builtAt}`; file `fleet-<version>-<os>-<arch>.tgz`.
2.  `go/coordinator/main.go` (Modify): `var version = "dev"`; `selftest` checked before the first `need(...)`.
3.  `go/executor/main.go` (Modify): `var version`; `selftest` in the `subcommands` map.
4.  `go/gateway/main.go` (Modify): selftest output unchanged; version now set by the release build.
5.  `go/coordinator/health.go` (Modify): `version`, `autoUpdate` of the coordinator's host.
6.  `deploy/install-release.sh` (Modify): version from `release.json`, unpack into `releases/<version>/`, selftest every binary present (`fleet-coordinator`, `fleet-gateway`, `fleet-executor`, `fleet-update`), check against `SHA256SUMS`; old names not accepted.
*   Tests: `health` returns `version` and `autoUpdate`; each `selftest` exits 0 without env (shell check in `tests/go/run.sh`).

#### Phase 2: protocol expand and coordinator

7.  `contracts/host-protocol.schema.json` (Modify): optional `PollRequest.version`, `PollRequest.update`; `PollReply.target`, `PollReply.autoUpdate`; `EdgeProblemKind` becomes `ProblemKind` with `version-behind`. Regenerate `go/coordinator/protocol_gen.go` (`go generate`).
8.  `go/coordinator/a_hosts.go` (Modify): `autoUpdate` validation and `setting-change` in `hostsUpdate`; `version`, `autoUpdate`, `update` in `hostsList`; PDS18 state merge for the coordinator's own host.
9.  `go/coordinator/a_poll.go` (Modify): store `version`, `update`; `version-behind` after `hostProblems`; answer `target`, `autoUpdate`; needs-you open and withdraw.
10. `go/coordinator/a_dispatch.go` (Modify): extract `openCauseQuestion`; dispatch calls it.
11. `go/coordinator/health.go` (Modify): `healthyTarget`.
*   Tests (`go/coordinator/*_test.go` or `tests/go`): `autoUpdate` accepts `on`, `off`, a future and a past `Z` time, refuses `+02:00` and other strings with 400, writes one `setting-change`; `version-behind` absent at N and N-1, present at N-2; `healthyTarget` returns current with marker, previous with marker, null without; a `refused` poll opens one question, a repeat adds none, an `ok` withdraws it; dispatch does not withdraw `update:` causes; the merge picks the worse state.

#### Phase 3: executor

12. `go/executor/executor.go` (Modify): release defaults of section 5; `version` and `update` in the poll body; `update/update.json` written on change; read `target`, `autoUpdate`.
13. `go/executor/upload.go` (Modify): `FLEET_STORE_ENDPOINT_HOSTS` default from `config/executor-defaults.json` of the release.
14. `config/executor-defaults.json` (Create): `storeEndpointHosts`.
*   Tests (`go/executor/core_test.go`): `update.json` written once per change, not per poll; `pollOk`/`pollError` set for 200, connection error, 302, 503, 401; poll body carries `version` and `update`; defaults apply without env.

#### Phase 4: `fleet-update`

15. `go/update/main.go`, `update.go` (Create): subcommands, section 3; the updater takes the public key as a field, `main` passes the compiled-in one.
16. `go/update/update_test.go` (Create): version compare and strict regex; never-downgrade; refused skip; each failure class of 3.2; digest mismatch retried once; atomic links; three-start rollback; `pending` not counted; prune with read-only directories and dumps; held and off; signature by another key refused.
17. GitHub environment `release` (tags `v*`) with secret `FLEET_RELEASE_KEY`; the public key in `go/update/main.go`.

#### Phase 5: workflows and units

18. `.github/workflows/release.yml` (Create): jobs of section 1.
19. `.github/workflows/ci.yml` (Modify): delete the release pack, upload and their comment; add `update` to the cross-build loop.
20. `deploy/infrastructure/fleet-update.service`, `fleet-update.timer`, `fleet-executor-pds18.conf` (Create).
21. `deploy/hosts/linux/fleet-update.service`, `fleet-update.timer`, `deploy/hosts/macos/fleet-update.plist` (Create).
22. `deploy/hosts/linux/fleet-executor.service`, `deploy/hosts/macos/fleet-executor.plist` (Modify): optional env file; plist `PATH`.
*   Check: the fork dry run of the Verification Plan.

#### Phase 6: docs and the three hosts

23. `deploy/README.md` (Modify): release by tag, first install, check with `hosts_list`, manual rollback.
24. `docs/infrastructure-build.md`, `docs/executor-build.md`, `docs/designs/fleet-executor-delivery.md` (Modify): GitHub Releases with provenance; env defaults; link here.
25. `docs/designs/README.md` (Modify): add this design.
*   Then the one-time first install of PDS18, the Mac and WSL ([first install](#first-install)).

### Verification Plan

*   **Build**: `pnpm run build`; `cd go && go generate ./... && go build ./...`.
*   **Unit tests**: `cd go && go vet ./... && go test ./...`.
*   **Release dry run**: replaced by the first real release (owner's call): tag on `main`, PDS18 and the other hosts update themselves; a rollback test with a deliberately broken selftest on a test tag.
*   **Live**: first tag on `main`; `hosts_list` shows PDS18 then every executor at the new version with `update.state: ok`.

--------------------------------------------------------------------------------

*Drafted with EGM*
