# PDS18 second native environment (Codex + Antigravity)

2026-11-09. Infra-only provisioning on `root@pds18.ufobe.com` (Ubuntu 26.04.1, x86_64), `fleet` user (uid 1005, linger on). No repo code was changed, the coordinator was not redeployed or restarted, no model turn ran. Claude is not installed and never runs on PDS: the Linux executor build refuses Claude accounts (`crates/executor/src/harness.rs`, `cfg!(target_os = "macos")`).

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

## Blocked

1. **Human login (Codex).** `codex login --device-auth` is waiting under the dedicated CODEX_HOME (log `/home/fleet/build/codex-login.log`). Open https://auth.openai.com/codex/device and enter the one-time code from the report. The code expires 15 minutes after it was issued. To start it again: `su - fleet -c 'env -i HOME=/home/fleet/accounts/codex-pds/home CODEX_HOME=/home/fleet/accounts/codex-pds/codex PATH=/usr/bin:/bin /home/fleet/runtime/codex-0.162.0/codex login --device-auth'`.
2. **Human login (Antigravity).** `agy acp --login` is waiting, with stdin on the FIFO `/home/fleet/build/agy-login/in` (the FIFO is kept open for 30 minutes) and output in `/home/fleet/build/agy-login/out.log`. Open the Google URL from that log, then paste back the code or the landing URL: `su - fleet -c 'echo "<code-or-url>" > /home/fleet/build/agy-login/in'`. To start it again, run `/home/fleet/build/agylogin.sh` as fleet.
3. **Coordinator change (owned by the code agent).** A poll as `pds18-native` returns `403 Host rejected`. `owned()` in `packages/coordinator/app.ts` admits only `hostId`, `integrationHostId` and `publicationHostId`. Every native start/lab execution is pinned to `config.hostId`, and `accounts.ts hosts()` lists only Mac and publication. Needed:
   - an env list of extra native hosts (e.g. `FLEET_NATIVE_HOST_IDS=pds18-native`) accepted by `owned()`;
   - per-session/account host selection (account binding → hostId) instead of `config.hostId`;
   - `hosts()`/onboarding to show `pds18-native` (linux) as eligible for codex/antigravity, with Claude still rejected on any linux host;
   - managed accounts `fleet-codex-pds` and `fleet-antigravity-pds` bound to host `pds18-native`.
   Do not reuse `pds18-program`: the program worker polls that id.

## After login and the coordinator change

1. Status check: `codex login status` (dedicated env) and `agy models` (dedicated HOME). Then set `readiness: ready` in the registry.
2. `systemctl --user enable --now fleet-executor` as fleet (`XDG_RUNTIME_DIR=/run/user/1005`).
3. One minimal Codex turn on account `fleet-codex-pds` through Fleet. Then, if cheap, a Codex → Antigravity handoff on PDS through the existing `session_transition` tool.

Not done yet: the Fleet turn and the handoff.

## Rollback

```
su - fleet -c 'XDG_RUNTIME_DIR=/run/user/1005 systemctl --user disable --now fleet-executor; pkill -u fleet -f "codex login|agy acp --login|sleep 1800"'
rm -rf /home/fleet/runtime/{executor-b9e50a75df4d,current-executor,codex-0.162.0,agy-1.5.3} /home/fleet/accounts /home/fleet/data/executor-pds18 /home/fleet/build ~fleet/.cargo ~fleet/.rustup \
  /home/fleet/.config/fleet/{executor-pds18.env,native-accounts.pds18.json} /home/fleet/.config/systemd/user/fleet-executor.service
```

The coordinator, program worker, SQLite and Mac executor were not touched.
