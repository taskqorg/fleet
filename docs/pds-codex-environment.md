# PDS18 second native environment (Codex + Antigravity)

> Historical record. It was written against the TypeScript coordinator and the Rust and Python executor, which the Go port deleted; file names in it are not current code. Current code: `go/coordinator`, `go/executor`, `go/gateway` ([stack](designs/fleet-stack.md)).

2026-11-09. Infra-only provisioning on `root@pds18.ufobe.com` (Ubuntu 26.04.1, x86_64), `fleet` user (uid 1005, linger on). No repo code was changed and the coordinator was not redeployed. The live turns are listed below. Claude is not installed and never runs on PDS: the executor's readiness refuses Claude where `FLEET_CLAUDE_PLACEMENT` does not match the platform (`claudePlacement`, `go/executor/readiness.go`).

## Installed

| Item | Path | Identity |
|---|---|---|
| Native executor, built on PDS from `crates/executor` at `b9e50a75df4d` with the root `Cargo.lock` (`cargo build --release --locked`, rustup minimal under `~fleet/.cargo`) | `/home/fleet/runtime/executor-b9e50a75df4d/fleet-executor`, symlink `/home/fleet/runtime/current-executor` | SHA256 `c2d1b4f2f006ba031f29240d8e299a32c98d1d5a5e12bafca284e9e8af30fff2` |
| Codex CLI 0.162.0, reused: the binary already on PDS (`/home/hermes/.local/bin/codex`) was copied. No download, no hermes credentials | `/home/fleet/runtime/codex-0.162.0/codex` | SHA256 `50ed828f357c655a3c82054d346cab8434f24901f14ec19b8267571bdd008b38` |
| Antigravity `agy` 1.5.3 (same version as the Mac), from the official manifest `linux_amd64.json` with its sha512 checked. `agy install` was not run, so the shell config is untouched | `/home/fleet/runtime/agy-1.5.3/agy` | SHA256 `7ca212748ca0dfda718efbc39a776a104472e7e9344d4b6ea147c543684f96a3` |

## Accounts and data (all 0700, owned by fleet)

- Codex `fleet-codex-pds`: HOME `/home/fleet/accounts/codex-pds/home`, CODEX_HOME `/home/fleet/accounts/codex-pds/codex`.
- Antigravity `fleet-antigravity-pds`: HOME `/home/fleet/accounts/antigravity-pds/home`, behaviour root (`--gemini-dir`) `/home/fleet/accounts/antigravity-pds/gemini`, `--app-data-dir=antigravity-cli`, `--include-default-customizations=false`. Credentials are private-HOME, not native-current.
- Registry `/home/fleet/.config/fleet/native-accounts.pds18.json`: both accounts `readiness: unprovisioned` until a login is done and status is checked.
- Executor data dir `/home/fleet/data/executor-pds18` (journal/outbox, `native-home`). This is separate from the coordinator SQLite.
- Env `/home/fleet/.config/fleet/executor-pds18.env`: host id `pds18-native`, coordinator `http://127.0.0.1:<PORT>`. The bearer is the existing machine secret, read from `coordinator.env` on the host and never printed.
- systemd user unit `/home/fleet/.config/systemd/user/fleet-executor.service`, installed but **not enabled or started**.

No Mac credentials were copied.

## Live results (2026-11-09)

Both human logins were done by the owner (Codex device auth under the dedicated CODEX_HOME; Antigravity `acp --login` into the dedicated `--gemini-dir`). The FIFO holder `sleep 1800` was killed. No re-login, no credential copy.

- Status: `codex login status` = `Logged in using ChatGPT`. `agy models` does not accept `--gemini-dir` (acp-only flags) and reads `~/.gemini`, so with the dedicated HOME it says "Please sign in". The Antigravity credentials live in the behaviour root (`gemini/antigravity-cli`). Antigravity readiness was proven by the live ACP turn below.
- Executor rebuilt on PDS from `5876af182736` (includes the `ea74c4c`/`66f678a` native-id fixes): `/home/fleet/runtime/executor-5876af182736/fleet-executor`, SHA256 `fad87e68eb4b182fcff3e4ea0b3a355ba98ffdbb13cb8c151de0152f0a034049`, `current-executor` now points to it (previous `executor-b9e50a75df4d` kept). The cargo tests need `config/agents` and ran on the Mac with the same source: 20 pass.
- Registry readiness was set to `ready` (backup `native-accounts.pds18.json.pre-ready`). `POST /api/hosts/accounts` (`reason: ready`) returned 200 for both accounts, so the coordinator now shows the bindings `fleet-codex-pds` and `fleet-antigravity-pds` on `pds18-native` as `ready`.
- `fleet-executor` user unit is enabled and active. It polls as `pds18-native` (heartbeat <1s, capabilities `steer`, `native-fork`) and gets no 403.
- Coordinator stays at `candidate-66f678a9de83`. No code change was needed, so there was no redeploy.

| Check | Session | Result |
|---|---|---|
| Codex turn on PDS (`fleet-codex-pds`, gpt-6.1-sol low) | `8031278a-372c-4fdb-9500-b0dbc7ee2c85` | completed on `pds18-native`, thread `01a1bff3-e10f-…`, reply `TEAL PDS` |
| Handoff Codex(PDS) → Antigravity(PDS) | same | `create_with_handoff`, `full_thread_summary` delivered, ACP session `c4adc074-…`, reply `TEAL PDS GEMINI` |
| Handoff Antigravity(PDS) → Codex(PDS) | same | `create_with_handoff`, `delta_since_target_last_seen` delivered, resumed the same thread `01a1bff3-…`, reply `TEAL PDS GEMINI` |
| Cross-environment Codex Mac (`fleet-primary`, `owner-mac`) → Codex PDS (`fleet-codex-pds`) | `27315ef6-efb8-4844-911c-5eb3111686df` | Mac turns `CORAL MAC`, then host switched by binding to `pds18-native`, handoff `full_thread_summary` delivered, reply `CORAL MAC LINUX` |

Claude was not used on PDS. At the end there were 0 unfinished executions.

Gaps: the executor poll does not send `platform` (the coordinator uses the binding's `linux`); there is no dedicated-root Antigravity status command, so the proof is the turn itself; Antigravity native fork/steer is not implemented; the PDS executor tests cannot run from a crates-only archive.

## Rollback

Executor only: `ln -sfn /home/fleet/runtime/executor-b9e50a75df4d /home/fleet/runtime/current-executor` (when idle). Readiness: restore `native-accounts.pds18.json.pre-ready` and post `readiness: unavailable`. Full removal:

```
su - fleet -c 'XDG_RUNTIME_DIR=/run/user/1005 systemctl --user disable --now fleet-executor; pkill -u fleet -f "codex login|agy acp --login|sleep 1800"'
rm -rf /home/fleet/runtime/{executor-b9e50a75df4d,executor-5876af182736,current-executor,codex-0.162.0,agy-1.5.3} /home/fleet/accounts /home/fleet/data/executor-pds18 /home/fleet/build ~fleet/.cargo ~fleet/.rustup \
  /home/fleet/.config/fleet/{executor-pds18.env,native-accounts.pds18.json} /home/fleet/.config/systemd/user/fleet-executor.service
```

The coordinator, program worker, SQLite and Mac executor were not touched.
