# Fleet design verifier

Install this service independently of candidate CI, on a protected Linux systemd host with Git, OpenSSL 3, and Node.js 22 or newer at `/usr/bin/node`. The installer requires the operator's administrative account. The verifier uses a private systemd state directory and a dynamic service identity. It polls GitHub for open PRs targeting `main` and temporary merge queue branches, reads the exact Git objects in a bare repository, and runs only released code.

The service reports provider readiness independently of isolated renderer readiness. Leave the repository's existing rules in effect while qualifying the visual path. See the [design approval contract](../../docs/designs/fleet-delivery-pipeline.md#design-approval) for cutover requirements.

## Install a signed Fleet release

1. On PDS18 the unit loads `LoadCredentialEncrypted=github-app.pem:/etc/credstore.encrypted/fleet-design-verifier.github-app.pem`. Use the operator-provisioned encrypted credential. Systemd sets `FLEET_DESIGN_GITHUB_APP_KEY_FILE` from `%d/github-app.pem` at service start, resolving to `/run/credentials/fleet-design-verifier.service/github-app.pem`.
2. Run the following in a private directory on a Linux amd64 host, using the version tag published by Fleet after this change lands. The verification key is the public Ed25519 key pinned in the [native updater](../../go/update/main.go). Run the installation commands as root.

   ```sh
   set -eu
   tag=vREPLACE_WITH_PUBLISHED_FLEET_TAG
   asset="fleet-${tag#v}-linux-amd64.tgz"
   url="https://github.com/taskqorg/fleet/releases/download/$tag"
   curl -fsSLO "$url/$asset" -O "$url/SHA256SUMS" -O "$url/SHA256SUMS.sig"
   cat >fleet-release-public.pem <<'PEM'
   -----BEGIN PUBLIC KEY-----
   MCowBQYDK2VwAyEAeT+0MVUmjUvvItJGhR3rUqW5PBzvE8QbD9Uq/wjAHlQ=
   -----END PUBLIC KEY-----
   PEM
   openssl pkeyutl -verify -pubin -inkey fleet-release-public.pem -rawin -in SHA256SUMS -sigfile SHA256SUMS.sig
   awk -v asset="$asset" '$2 == asset { print }' SHA256SUMS | sha256sum --strict -c -
   release="/opt/fleet-design-verifier/releases/$tag"
   install -d -m 0755 /opt/fleet-design-verifier/releases
   mkdir -m 0755 "$release"
   tar -xzf "$asset" -C "$release" --no-same-owner
   sh "$release/deploy/design-verifier/install.sh"
   ```

3. Read the service and its latest provider observation. These commands display no credential values.

   ```sh
   systemctl is-active fleet-design-verifier.service
   journalctl -u fleet-design-verifier.service -n 5 -o cat --no-pager
   cat /var/lib/fleet-design-verifier/status.json
   ```

   A fresh status with `provider: "ready"` confirms a short-lived installation token scoped to App `5962560`, installation `186718702` and repository `1450484391`. `authentication` shows the public key fingerprint and the token acquisition and expiry times. It includes the verifier release's exact `revision`. Read `results` for separate PR and merge-group observations. Each confirmed result includes its `head`, `appId`, `checkRunId`, `externalId` and `conclusion`. `null` means in progress. The service writes `provider: "unknown"` when the provider probe fails. Treat an absent or stale status as unknown as well.

## Check receipts

The service retains check inputs before posting and readbacks after posting, under `/var/lib/fleet-design-verifier/receipts/`. Each file is indexed by the GitHub check's `external_id`. Merge-group receipts include the verified merge method, every merge commit through current `main`, its original PR head, confirmed against the matching GitHub merge queue entry, and the corresponding protected PR receipt.

The issuer uses its existing Metadata read access to confirm exactly one effective `merge_queue` rule with `merge_method: MERGE` at `GET /repos/taskqorg/fleet/rules/branches/main`. The same App reads all live membership pages through GraphQL. It checks the effective rule again before publishing. A missing, inaccessible, ambiguous or unsupported rule leaves the check pending. GitHub may discard and regenerate queue refs. The service waits for fresh provider facts when a ref, base or original head moves.

## Fleet identity and retained status

Provision a dedicated Cloudflare Access service token for the issuer. The GitHub App key and Fleet Access credential have separate authority. Fleet scopes this machine to its `fleet` project and the four operations `me`, `design_verifier_status`, `design_verifier_evidence` and `design_verifier_observe`. The Fleet identity can read the original evidence revalidated by the coordinator and submit observations, including the issuer's exact check-run readbacks. It cannot publish branches, decide designs or change repository rules.

The operator can provision it without an interactive daemon login:

1. Use the operator's Cloudflare API credential to call `POST /accounts/{account_id}/access/service_tokens` for `fleet-design-verifier` with a bounded duration. Save the response to a private file on the protected host. Add a Service Auth policy on the existing Fleet Access application using only this token's ID. The Cloudflare policy uses `decision: "non_identity"` and a `service_token` include. Keep the existing Fleet people and agent policies in place. [Service token API](https://developers.cloudflare.com/api/resources/zero_trust/subresources/access/subresources/service_tokens/methods/create/) · [Service Auth policy](https://developers.cloudflare.com/cloudflare-one/access-controls/service-credentials/service-tokens/)
2. Add the row below to the private roster at the coordinator's configured `FLEET_PEOPLE_FILE`. Use the new token's `result.client_id` as `accessCommonName` and preserve the roster's owner and other entries. Save it atomically with the existing owner and file mode.

   ```json
   {"email":"fleet-design-verifier","name":"Fleet design verifier","roles":["viewer"],"accessCommonName":"<Cloudflare client ID>","grants":[{"kind":"project","id":"fleet"}]}
   ```

3. On PDS18, pipe the one-time Cloudflare API response from the operator's private file into the encrypted store. Create the drop-in and restart the service.

   ```sh
   set -eu
   umask 077
   fleet_credential=$(mktemp)
   trap 'rm -f "$fleet_credential"' EXIT
   jq -e '{clientId:.result.client_id,clientSecret:.result.client_secret}
     | select([.clientId, .clientSecret] | all(type == "string" and length > 0))' "$CF_TOKEN_RESPONSE_FILE" >"$fleet_credential"
   systemd-creds encrypt --with-key=host --name=fleet-access.json "$fleet_credential" \
     /etc/credstore.encrypted/fleet-design-verifier.fleet-access.json.next
   mv -T /etc/credstore.encrypted/fleet-design-verifier.fleet-access.json.next \
     /etc/credstore.encrypted/fleet-design-verifier.fleet-access.json
   install -d /etc/systemd/system/fleet-design-verifier.service.d
   cat >/etc/systemd/system/fleet-design-verifier.service.d/fleet-access.conf <<'UNIT'
   [Service]
   LoadCredentialEncrypted=fleet-access.json:/etc/credstore.encrypted/fleet-design-verifier.fleet-access.json
   UNIT
   systemctl daemon-reload
   systemctl restart fleet-design-verifier.service
   ```

4. A fresh host report with `fleetEvidence: "ready"` confirms the scoped actor and a successful Fleet observation write. From the operator's Fleet session, call `design_verifier_status {}` or use this in the authenticated Fleet page's browser console.

   ```js
   const status = await fetch('/api/ops/design_verifier_status', {
     method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}',
   });
   console.log(await status.json());
   ```

   `fleetIdentityConfigured` reports roster registration. `lastReady` retains the last successful provider authentication even after a later failure. `latestPR` and `latestMergeGroup` each carry the exact commit, App check run and retained receipt when observed. An absent entry is unknown. Fleet marks provider readiness unknown when the last daemon observation is older than two minutes.

## Provision the isolated renderer

The release includes `install-renderer.sh`, two bundled controller entry points, the pinned build input manifest, the release's frozen npm inputs, and the static PNG comparator on Linux amd64. Use the same signed release for the coordinator's evidence operation and the protected issuer. The current verifier's Fleet identity and GitHub App credentials are sufficient.

1. Provision `bubblewrap` on the protected Linux host. Enable unprivileged user namespaces for this runtime and Chromium's sandbox according to the host's local security policy. The renderer refuses to start if either namespace boundary cannot be established.
2. Preload an approved Linux amd64 OCI base by digest into the root Podman store. It must be a clean OS image with GNU tar, glibc, fontconfig, the Chromium shared libraries and the font inventory of the designer's renderer. Image provenance and fonts are operator inputs. The installer does not start an OCI container. It exports its filesystem, installs Node, Playwright and Chromium from the signed Fleet pins, and populates a frozen npm dependency tree with lifecycle scripts disabled. Host tools used in this preparation are Podman, jq, curl, GNU tar, unzip, corepack and runuser.
3. Run from the newly verified release directory as root. Replace the image reference with the exact approved one already in the root store. On upgrades with the same runtime inputs reuse the existing rootfs. To replace it, stop the service and move the previous rootfs to a private rollback location first.

   ```sh
   systemctl stop fleet-design-verifier.service
   sh deploy/design-verifier/install-renderer.sh 'REGISTRY/IMAGE@sha256:BASE_IMAGE_DIGEST'
   systemd-run --quiet --wait --pipe -p DynamicUser=yes -p NoNewPrivileges=yes \
     /usr/bin/node "$(pwd -P)/deploy/design-verifier/renderer-probe.mjs"
   sh deploy/design-verifier/install.sh
   ```

The installer prepares the empty fixed `/trusted` directory and `/trusted-compare` file inside the renderer rootfs before it becomes read-only. For a rootfs installed by an earlier release, stop the verifier first and run the signed release's `deploy/design-verifier/prepare-renderer-mounts.sh /opt/fleet-design-renderer/rootfs` as the operator, then repeat the stock probe. The helper refuses symlinks and incompatible existing targets; it does not replace the image or alter any credentials.

The existing local bwrap renderer remains the default. The optional [protected VM backend](VM-PROTOCOL.md) requires a separately qualified root-managed broker socket, immutable launch manifest and protected configuration. A missing configuration uses the existing local backend; a present but invalid configuration fails closed. Installing the configuration, granting the socket and enabling visual work require the independent VM integration and exact-head qualification; a signed package or protocol fixture test alone does not qualify them. The optional release files `fleet-design-verifier-vm.conf` and `fleet-design-vm-tmpfiles.conf` provide the scoped verifier group and a traversable `root:fleet-design-vm-client` socket directory. An operator installs them only after creating the dedicated static group with no account members and after qualifying the broker, manifest and guest; do not install either for the default local renderer. The socket's `DirectoryMode` alone does not set its parent group. Verify the group, directory and socket ownership/modes and denial for unrelated processes, including after verifier restart, before treating the binding as authorized.

The signed Linux bundle contains `fleet-design-vm.socket`, `fleet-design-vm.service`, `vm-broker.py`, `vm-preflight.py`, `fleet-design-vm-guest.service`, `vm-guest.py`, `vm-guest-worker.py`, the bundled `vm-guest-worker.mjs`, and `fleet-design-vm-worker.socket` and `fleet-design-vm-worker.service`. Install the signed `vm-capture.py` next to the host broker and guest supervisor. For a separately approved installation, the operator stages the broker and preflight scripts as root-owned files in `/opt/fleet-render-vm/bin`, installs the socket and service under `/etc/systemd/system`, and supplies the separately reviewed golden, root-owned `/opt/fleet-render-vm/bin/launch.json`, bounded state filesystem and public-digest `/etc/fleet-render-vm/image.env`. The broker's launch manifest fields are `version`, `goldenSha256`, `biosSha256`, `releaseSha256`, `guestWorkerSha256`, `runtimeSha256` and `releaseCommit`. The operator must establish that the approved golden actually contains the verified signed release, guest worker and runtime named by that manifest; the broker verifies the golden and firmware bytes but does not inspect a guest filesystem. The verifier's protected VM config pins the exact manifest bytes and signed release/runtime; the guest probe reports its root-owned worker and runtime digests for comparison. This is a consistency check, not independent guest attestation. Install the three guest units together inside the reviewed golden. Enable only the supervisor. Its boot dependencies create a root-only Unix socket and start the fixed worker directly as `fleet-render-worker`. The supervisor reports ready after the worker handshake. It sends one validated protected operation and payload or selects the fixed capture staging paths. PID 1 powers off the guest when the supervisor finishes. The three units and their signed entrypoints must be qualified together. These operator steps are not performed by the ordinary verifier installer and must not be automated or enabled before qualification.

After startup, read `/var/lib/fleet-design-verifier/status.json`. Require `provider: "ready"`, `fleetEvidence: "ready"`, `visualParity: "ready"` and the expected release revision. The probe launches sandboxed Chromium and loads a page on the new network namespace's loopback. This is runtime readiness. A candidate qualifies only when its own App check and retained receipt confirm parity.

Each job starts without inherited environment or directory access. It gets a read-only rootfs and protected helpers, a fresh user, mount, PID and network namespace, and bounded scratch mounts under a watchdog and the service cgroup. Git archives enter the build guest over standard input after file types, paths and export attributes have been checked. Build and browser guests have separate writable filesystems. The browser gets only the normal `app/build.mjs` outputs and synthetic operation fixtures. The comparator runs in a third guest with only the two PNGs. Production data, tokens, cookies, verifier state and control sockets stay outside all three guests. The installer and protected service users never share a writable toolchain with a candidate.

## Capture app originals in a designer execution

The `pack` command runs in the Fleet Design preset's existing sandbox. Run `capture-job`, `capture`, `replay` and `compare` only in a separate credential-free Linux amd64 VM with no designer shell, no other execution under the importer uid and no designer access to its private storage. That VM must already have the approved renderer rootfs, bubblewrap, Chromium user namespaces and a bounded execution allocation. T-FLT-186 tracks integration proof of this separation; the CLI cannot establish it merely by creating a private directory. The caller must already run inside a unified cgroup with finite memory, PID and CPU limits; the CLI checks its actual cgroup before starting the renderer. Capture and replay also require `TMPDIR` to be a private, per-job tmpfs mount owned by the caller, mode 0700 and at most 256 MiB; the CLI verifies its mount and capacity before importing the Git bundle. Provision the mount outside the CLI and destroy it after the job. Use the `designer.mjs` from the signed Fleet release installed there. The same pinned rootfs supplies the app build, font inventory and browser used by protected rendering. Run one capture at a time. The command refuses other platforms.

Capture uses an explicit list of edited files and a self-contained base bundle from the project at the exact signed release commit. A project operator or trusted producer retains that bundle as a Fleet result; generating and delivering it automatically is not yet built. The designer receives it as an input artifact. Use the authorized job boundary below to qualify the return path before offering new originals. Do not give the VM execution credentials or use another execution to retain the originals. The producer can create a bounded depth-one bundle from a trusted project checkout using `git init --bare`, then `git fetch --depth=1 --no-tags` of the signed commit into `refs/heads/release`, and `git bundle create ... refs/heads/release`. Verify the commit and its tree against the signed release before retaining the bundle. The importer declares that commit as the sole shallow boundary, verifies all included objects and signed build pins, and binds the base tree and bundle digest in the replay manifest. The base bundle may be at most 60 MiB; the complete retained source must fit the existing 64 MiB source limit. The pack step reads only the named allowed files and never invokes Git in the designer checkout. In the separate VM, the importer creates a private Git repository, checks the signed release commit and build pins, and does not access the designer checkout. Place private fixtures and outputs under `.egm/` and use invented data.

```sh
node "$FLEET_DESIGN_RELEASE/deploy/design-verifier/designer.mjs" pack \
  --checkout . --base "$SIGNED_RELEASE_COMMIT" \
  --paths .egm/design-paths.json --out .egm/design-overlay.json
```

`design-paths.json` is a JSON array of repository-relative files to include, including deleted files. The capture rejects symlinks, unsupported or protected paths, oversized input, and a base different from the signed release. Existing files preserve their Git executable mode. New files have mode `100644`. Only the named files are captured; the author must list every intended change.

Set `FLEET_DESIGN_RELEASE` to the installed signed release directory. With no `--fixture`, the command uses the typed invented Conversation and Task Details cases in [app-originals-fixture.ts](../../scripts/app-originals-fixture.ts). The task case opens the normal project Tasks detail URL and selects the real **Details** tab. For a different synthetic state, write the output of `designer.mjs fixture` to a private JSON file, edit the exact request/reply pairs using the generated `Ops` contract, and pass its path as `--fixture`. A required read, unmatched request, wrong route or missing landmark blocks the whole capture. The isolated origin serves the regular app assets and the synthetic replies. Each frame uses a fresh sandboxed Chromium context, a frozen clock, UTC, en-US, reduced motion and DPR 1.

For Image viewer originals, use a version 2 synthetic fixture with an `images` map and eight cases. Each case keeps the normal version 1 route scenario and adds a `journey` with `flow`, `entry`, `resultCode`, `resultTitle`, `image`, and optional `thumbnail`. Use the `open`, `fit`, `zoom` and `returned` states under `pages/conversations/image-viewer` and `pages/tasks/image-viewer`. The cases use the same fixed gestures as the protected image driver. Every frame retains the observed focus, and returned frames also retain the observed displaced source scroll. For Conversation, use the landed `session_view.includeQuestionArtifactIds: true` request and return the selected artifact in the question. Use the same exact result, preview and operation replies when mapping selected cases in the version 2 protected policy.

### Host job contract

The current Fleet Design execution calls `design_capture` with a stable `requestId`:

1. Call `status` to read the configured release commit before packing. Save the packed `overlay.json` and exact `fixture.json` with the execution's existing artifact tool. Wait for those artifacts to be stored.
2. Call `start` with `overlayArtifactId` and `fixtureArtifactId`. Fleet resolves the current project role, session, task and running execution. The originating native Linux executor must advertise `design-capture-stage-linux-v1` on its authenticated poll before Fleet creates the job or hands out its permit. The capability requires the signed replay validator, Node and anonymous file support on the execution volume. It chooses the approved base bundle and fixed `pds18-native` host. The coordinator rechecks the originating designer immediately before signing the handoff. Expired pending captures are settled even while the broker is unavailable. A repeated request reads its existing job. A different request or changed input in that execution returns a conflict.
3. Use `status` while the job runs and `stage` after success. Stage verifies the terminal receipt, the exact archive, complete PNGs and the producer's replay grammar. It returns the archive ID, digest and verified file inventory. Retry with the same request ID or use `agent_tool_result` for a pending coordinator request. The same durable transfer retries interrupted reads.
4. In the originating designer execution, call `artifact_get` for the verified archive ID. The coordinator checks current authority and orders the read grant against cancellation, child execution stop and initial project membership revocation. A granted download may finish. A revocation committed first denies the read. The executor publishes the complete verified bytes atomically under a fresh input filename. Check the downloaded archive digest against the stage result, then extract and inspect the originals and save the source, recipes and original PNGs through that execution's ordinary artifact tool. The verified receipt and raw archive have no direct browser or project-wide download. Regular scene capture uses its existing tools.
5. Use `cancel` to stop the capture or revoke its pending return. Cancelling or ending the originating execution also fences return. A later turn has a different execution ID. No capture result by itself creates an owner offer.

Fleet reuses the durable script job envelope with a fixed capture discriminator. The executor selects the compiled host client directly. The job does not use the ordinary shell runner or MCP. The client stages only immutable artifact IDs owned by the designer or selected by the operator, verifies input sizes and SHA-256s, and sends bytes over the local broker socket. The broker starts the signed entry in the credential-free guest:

```sh
node "$FLEET_DESIGN_RELEASE/deploy/design-verifier/designer.mjs" capture-job \
  --job-input "$CAPTURE_PRIVATE_INPUT" --out "$CAPTURE_PRIVATE_OUTPUT"
```

The guest reads `request.json`, `base.bundle`, `overlay.json` and `fixture.json`. The request records the project, task, session, designer execution, separate host job, nonce, signed base and exact artifact hashes. The guest reconstructs the packaged source against the overlay before returning `capture.tar` and `receipt.json`. The outer archive starts with the handoff and includes the source archive, recipes and PNGs. The receipt records the request, release, runtime, reconstructed source, and archive digests. Fleet releases stage only after an authenticated terminal host receipt and stored outputs. The host closes or withholds partial outputs on failure or cancellation. The host client holds the output in bounded anonymous memory until the complete response and EOF. It follows the normal artifact verification continuation for a remote store without starting another multipart upload. The executor verifies the uploaded bytes as a stream without publishing a local copy. The first designer-readable archive is the download authorized by the coordinator.

### PDS18 broker binding

The operator selects the approved base artifact, signed producer, runtime and exact `launch.json` SHA-256 in the coordinator's `FLEET_DESIGN_CAPTURE_INSTALLATION` JSON. The fields are `host` (only `pds18-native`), `baseArtifact`, `producer`, `runtime` and `launchSha256`. Configure the private Ed25519 seed through `FLEET_DESIGN_CAPTURE_SIGNING_KEY_FILE` and install its public key in the broker. The seed file holds 32 bytes encoded as hex with no group or other permissions. The coordinator checks current designer authority immediately before signing. The permit signs the fixed purpose, host, job, request digest, installation digest and expiry. It travels only through authenticated host transport and the fixed broker client.

The executor advertises `design-capture-broker-v2` when the root-owned socket `/run/fleet-design-capture/broker.sock` is present. It checks the Linux server peer UID. The broker checks the pinned executor UID and the Ed25519 permit. It marks the job spent on the bounded volume before launch. That record survives restart and prevents replay after a wall-clock rollback.

An idle broker acknowledges capture with `FDCA`. A busy broker replies `FDB1` before receiving the permit. The client retries explicit busy every two seconds with the same job and unspent permit until admission, cancellation or the existing job or permit deadline. After admission a transport failure is terminal because the permit may have been spent. After `FDCA`, the client sends `FDC1` and five frames with big-endian 32-bit lengths. The host-only permit is at most 1 KiB. The four required guest files in order are `request.json` at most 8 KiB, `base.bundle` at most 60 MiB, `overlay.json` at most 3 MiB, and `fixture.json` at most 256 KiB. The request pins the other three files. The broker verifies the signed producer, runtime and launch-manifest binding before spending the permit or allocating a guest. No host paths cross the socket.

The broker verifies the approved golden and reuses T199's private virtio framing. The guest receives only the four files and runs the fixed signed capture. A disconnected client cancels the guest. Fleet's execution stop fences artifact return even when a stop races with a completed broker result.

The broker returns `FDR1` followed by the 32-bit receipt and archive lengths, exactly those bytes and EOF. The limits are 8 KiB and 100 MiB. A successful return requires matching release and runtime, guest shutdown, clean QEMU exit, virtio EOF and confirmation that the shared cgroup, scratch and durable fence were cleaned up.

#### Install from a verified release

1. Use one normal signed Fleet Linux amd64 release for host broker, executor, guest and coordinator. Verify the signed checksums using the existing Fleet release procedure before staging files. Keep the existing T199 bounded volume, KVM and cgroup checks. Both sockets share one broker and one VM allocation. Each keeps its own filesystem group and protocol.
2. In a fresh offline golden build, install the root-owned signed release and all three guest units from the protected VM procedure. Provision the unprivileged `fleet-render-worker` user and group with UID and GID 990 for the capture tmpfs. Install `fleet-design-vm-guest-capture.conf` as the supervisor's `capture.conf` drop-in, `fleet-design-vm-worker-capture.conf` as the worker's `capture.conf` drop-in, and `fleet-design-vm-worker-capture-tmpfiles.conf` under `/etc/tmpfiles.d/`. Enable only the supervisor. The static worker starts with a private network namespace, an empty capability bounding set, a finite cgroup and a fresh 256 MiB tmpfs. The guest permits `AF_NETLINK` for bubblewrap's loopback setup and has no NIC. The host broker keeps its Unix-only policy. Qualify the protected stock probe and capture in this same signed golden.
3. Power off and measure the golden. Establish that it contains the signed release, both worker entries, guest units and pinned runtime. Record the SHA-256s of the release archive, bootstrap worker, designer bundle, runtime and golden. Bind the golden, BIOS, release, worker and runtime with the existing T199 `launch.json` and image procedure. Approve the matching protected verifier launch identity separately before cutting over the shared golden.
4. With the broker drained and stopped, install `vm-broker.py`, `vm-preflight.py`, `vm-capture.py` and `vm-capture-host.py` as root-owned files in `/opt/fleet-render-vm/bin/`. Install `fleet-design-capture.socket` under `/etc/systemd/system/`, `fleet-design-vm-capture.conf` as `/etc/systemd/system/fleet-design-vm.service.d/capture.conf`, and `fleet-design-capture-tmpfiles.conf` under `/etc/tmpfiles.d/`. Create `fleet-design-capture-client` as a dedicated system group with only the PDS18 executor account as member. Refresh the executor's group membership during the approved cutover. The directory uses `root:fleet-design-capture-client` mode 0710 and the socket mode 0660. The protected verifier group and project-browser grants are separate.
5. Install root-owned `/opt/fleet-render-vm/bin/capture.json` with these fields. Use `version` (1), `host` (`pds18-native`), `clientUid` (executor UID), `publicKey` (the coordinator's 32-byte Ed25519 public key in lowercase hex), `launchSha256` (exact `launch.json` bytes), `producer` (signed `commit`, `version` and `target`), and `runtime` (`imageSha256`, `fontSha256`, `lockSha256`, `node`, `chromium` and `playwright`). Use the same producer, runtime and launch digest in the coordinator, together with its approved base artifact. Its private seed stays on the coordinator.
6. Run `systemd-analyze verify`, provision the directory with `systemd-tmpfiles --create /etc/tmpfiles.d/fleet-design-capture-tmpfiles.conf`, reload the manager and start the two socket units. Read back socket permissions, the effective shared broker cgroup and the live stock T199 probe under its protected identity. Confirm denial for an unrelated local account and rejection of an invalid permit from the executor account. A present socket reports capability. A signed capture round trip establishes readiness.

#### Qualify and roll back

Run `node --test tests/ui/vm-transport.test.mjs` from the installed release. Qualify one ordinary Fleet designer execution with invented fixtures. Check valid dispatch and same-execution stage and save. Reject permit replay. Cancel one owned test job and confirm QEMU, sockets and scratch disappear. Capture and replay the selected normal-route cells with the same signed release and inspect actual focus and scroll on return. Retain the signed release, terminal receipt, hashes and protected stock probe. Unconfirmed teardown retains the existing `run.fence.*` marker and blocks the next start. Use T199's operator clearance before restarting.

For rollback, stop capture dispatch and drain the job. Disable and stop the capture socket, remove its broker drop-in and tmpfiles definition, and reload systemd. If the shared golden changed, restore the matching signed protected broker, golden and launch manifest together. Keep the spent-permit ledger and cleanup fences. Read back the protected socket and stock probe.

`source.tar` contains four regular files: `base.bundle`, `overlay.json`, `overlay.patch` and `replay.json`. The replay manifest retains the frozen fixture and clock, base and reconstructed trees, renderer and release identities and ordered original hashes. The recipe records the effective product and installed dependency input hashes. Verify the retained archive digest before unpacking its four named files into a fresh private directory. Replay uses the same signed renderer and exact base:

```sh
node "$FLEET_DESIGN_RELEASE/deploy/design-verifier/designer.mjs" replay \
  --base-bundle "$FLEET_INPUT_DIR/retained-source/base.bundle" --replay "$FLEET_INPUT_DIR/retained-source"
```

The admitted writer can apply the reviewed `overlay.patch` with `git apply --index --binary` on the recorded base. Carry the product overlay into delivery. The frozen fixture, clock and other replay data travel with the retained evidence. Fixtures and rendered application inputs must be deterministic; replay checks the resulting hashes and there is no random seed. Use `designer.mjs compare --before <capture-dir> --after <capture-dir>` to compare two complete captures of the same fixture on the same runtime. It verifies the PNG hashes and reports differing pixel counts and bounds using the existing comparator. Exit status 2 means a confirmed difference.

The protected issuer still requires a separately reviewed operator route and impact mapping for the exact selection. The designer-only Details click supplies originals. The protected verifier's own route and gesture policy determines which cases it can qualify.

Keep the full dependency pin list in each recipe. The released issuer and its matching build worker can carry identical pins once and encode a larger inventory for the existing 96 KiB transport. The worker checks each decoded file hash before the build. Install the matching signed controllers and worker together.

## Register a selected route and impact mapping

The operator owns `/etc/fleet-design-verifier/parity.json`. Install it atomically as a root-owned regular file with mode `0644` in a root-owned directory with mode `0755`. Keep the source, fixture and impact review trace with the operator's task or release evidence. Start from the actual selected cases and original scene data. Translate that synthetic data into responses at the normal coordinator operation boundary. Do not take the policy, route scripts or fixture replies from the candidate checkout. The verifier never runs the book to establish production parity.

- Set `version` to `1` and `cases` to an object keyed by the exact selected `scopeKey`. Each value binds `recipe` to the selected recipe digest and `sourceSha256` to the selected source artifact's reported hash. Use `start` and `target` for the literal app paths and `landmark` for a unique visible selector that confirms the target is loaded. The policy has no script field.
- Choose `flow` as `open` for an ordinary app URL, `direct` for a conversation deep link, `standalone` for direct entry to a conversation independent of the bot page, `bot` for the **Talk to it** click on a real bot detail URL, or `back` for that click followed by browser Back. For `back`, add `backLandmark` for the restored bot page. The bot journey needs selected `bot`, `back` and `standalone` cases. The standalone case opens a direct link in a fresh browser context and can assert a redirect to the canonical URL.
- Supply `responses` as a list of `{op, request, reply, required}` entries with synthetic data only. A request must match the method, operation and JSON values exactly. Property order is immaterial. Mark the reads that prove the scenario with `required: true`; use `forbiddenOps` when the accepted behavior forbids a read such as `sessions_list`. Unmatched operations, missing required reads, forbidden reads, popups, page exceptions, or a wrong URL or landmark block the capture.
- Set `paths` to reviewed `{path, before, after, cases, reason}` entries. A fingerprint is the Git mode, a colon and the SHA-256 of the actual blob. Use JSON `null` for an absent side. Review every unclassified path against the PR merge base and against the queue base and final tree. App, shared UI and design paths can map to selected cases. The fixture responds at the operation boundary. Visual changes in the coordinator or other code outside the executed app require a verifier that runs that behavior and stay blocked here. An empty cases list asserts a reviewed nonvisual impact and needs its reason. Paths without an exact mapping stay blocked. The combined tree must cover every selected case of every visual member without overlapping selections.

Use the installer-shipped diagnostic to print fingerprints once the daemon has fetched the actual provider objects. Run against the protected daemon's local bare repository as the operator. The output is a mapping input and does not issue a check.

Provider fetches write to a private slot on the same filesystem as the retained bare repository. Git can read the existing object cache as an alternate. After the process group exits and Git confirms the exact commits, the verifier links complete packs and their indexes into the cache. Interrupted transfers stay in their slots. On restart the single verifier unit reaps its old slots before admitting new fetches.

```sh
/usr/bin/node /opt/fleet-design-verifier/current/deploy/design-verifier/renderer-probe.mjs \
  --impact /var/lib/fleet-design-verifier pull_request BASE_SHA HEAD_SHA
```

Replace the final three arguments for the genuine merge group. Use the reported object fingerprints to populate the corresponding reviewed entries. The release pins the executed build files and package lock. A changed pin needs a newly signed and provisioned verifier environment before the candidate can qualify. Changes to selected scene inputs or installed dependency bytes require current matching owner evidence.

### Question image journeys

Keep the last compatible version-1 policy at `/etc/fleet-design-verifier/parity.json`. Install a complete version-2 policy at `/etc/fleet-design-verifier/parity-v2.json` with the same file ownership, mode and atomic rename procedure. Carry forward the reviewed legacy cases and impact mappings into version 2 before adding image cases. The new verifier selects version 2 when present and uses version 1 when the version-2 file is absent. An invalid version-2 file blocks verification.

Use a separate selected case for each state of each surface. Set `flow` to `image-open`, `image-fit`, `image-zoom` or `image-returned`. Keep the existing recipe and source pins. Use the same literal route for `start` and `target`. For `surface: "task"`, use the normal task detail route such as `/projects/TST/tasks/T-TST-1`. The runner selects Details. For `surface: "conversation"`, use the normal conversation route such as `/conversations/C-TST-1`.

Set `resultCode` and `resultTitle` to the synthetic result resolved by the fixture. Set `entry` to `thumbnail` for the question's Open button or `entity` for its result code and the entity card's image link. Supply the exact operation requests from the normal shell. Require `task_get` or `session_view` as appropriate and `entity_refs` in both. Require `entity_card` for entity entry. Image journeys have fixed controls and landmarks.

Add a top-level `images` map. Each key identifies one synthetic PNG with `path` set to its literal `/api/results/<id>` route, `png` to canonical base64 and `sha256` to the hash of the PNG bytes. Set the case's `image` to the original's key and its optional `thumbnail` to a separate key when the shell uses a distinct thumbnail. With two assets, the opener must load the thumbnail before activation and the original must first be requested after the trusted browser click reaches the document. With one asset, the same bytes can load before or after activation. The viewer must decode the declared original and the stage must reach all four viewport edges. The opening image bounds must cover the stage's pixel centers at DPR 1.

For returned cases, use a long enough synthetic question to exercise a nonzero source scroll offset at each viewport. The worker opens and closes the viewer through the app controls and observes the same opener and ancestor scroll offsets after Back. Task cases also confirm Details remains selected after return. Receipts bind the synthetic image hashes and retain the per-frame state observations.

The signed Linux archive contains the focused regression test at `tests/ui/design-image-parity.test.mjs`. Run it with Node's test runner to check fixture validation, loopback transport and return assertions. A live journey requires the qualified renderer and matching owner originals for the normal app.

## Render and check the live qualification

The renderer uses the fixed clock, UTC timezone and en-US locale of the recipe, the release-pinned headless Chromium, DPR 1, reduced motion, and a fresh browser context for each frame. It waits for the expected app route, landmark, fonts and two animation frames, then captures the whole viewport with animations disabled and the caret hidden. It compares decoded PNG color and alpha samples at 1440×900 and 390×844 in dark and light. There are no masks or pixel tolerances. A receipt records both PNG hashes, the rectangle enclosing the different pixels and the number of different pixels. Any differing pixel blocks. A browser or runtime change that changes pixels requires a confirmed matching runtime or new owner evidence.

For the separate pilot, compare the real PR result with the genuine merge-group result using managed publishing and the Fleet issuer readbacks. Verify the issuer App, check run, exact head, receipt external ID, selected owner decision, member PR receipts, archive and policy hashes, and all actual and original image hashes. Inspect `receipts/<externalId>.json` in the verifier state directory. The protected service rechecks current selection and provider identity before publishing success. A change while the PR or group remains active is re-evaluated on the next check. The GitHub report is a point-in-time observation.

Inside the receipt, `evidenceState: "absent"` means there was no current exact-head selection or admission. `"blocked"` means a verified input was stale or incompatible, impact was unmapped, a route assertion failed, or pixels differed. These both publish failure. `"unknown"` leaves the check pending when a provider fact or a worker result could not be confirmed. Confirm an actual mismatch and a missing or stale selection on different exact candidates before treating the positive path as qualified. The operator controls rule activation after qualification.

## Restart and rotation

Restart `fleet-design-verifier.service` and check that a fresh host report advances `authentication.authenticatedAt` and `expiresAt`. Once its Fleet identity is configured, the new authentication also appears in `lastReady`. On clean stop the service revokes its current installation token. Superseded tokens expire on GitHub's schedule.

To rotate the App key, the operator prepares the replacement key for App `5962560`, then encrypts it into an adjacent file and switches the encrypted credential atomically. Set `FLEET_DESIGN_NEW_APP_KEY_FILE` to the operator's private input file on PDS18.

```sh
set -eu
umask 077
systemd-creds encrypt --with-key=host --name=github-app.pem "$FLEET_DESIGN_NEW_APP_KEY_FILE" \
  /etc/credstore.encrypted/fleet-design-verifier.github-app.pem.next
mv -T /etc/credstore.encrypted/fleet-design-verifier.github-app.pem.next \
  /etc/credstore.encrypted/fleet-design-verifier.github-app.pem
systemctl restart fleet-design-verifier.service
```

Verify the new public fingerprint and a fresh acquisition in host status and Fleet `lastReady`, then revoke the superseded App key at GitHub. Compare genuine PR and merge-group runs against the App identity, exact head and corresponding retained receipt. Repeat the check after a new PR head. Keep release enforcement pending until the protected visual path is qualified.

## Managed publishing readiness

The Fleet worker runs `project-publishing/capabilities` and `project-publishing/status` through the execution's project grant and its host mediator. Capability shows the repository, branch, provider, merge grant and available actions. Status confirms the exact PR head and provider state or confirms `UNPUBLISHED`. Operators can also read `publishing_capabilities` and `publishing_status` for project `fleet`. These reads expose no credentials. The [delivery order](../../docs/designs/fleet-delivery-pipeline.md#8-workers-merge-and-watch-their-own-pull-requests) describes the publish, exact-head `code-review`, readiness and merge lifecycle.

For a rollback, stop the service or rerun the installer from the previous verified release which contains it. Keep the App credential and state directory in place. Upgrade one instance at a time.

Pair activation and rollback with their compatible policy files. Before activating the image-aware release, retain the last working `parity.json` in place and verify its hash. Register `parity-v2.json` alongside it. After activation, read the new release revision and verify a known legacy flow on its exact candidate with the version-2 policy hash in the retained receipt. When rolling back to a release with version-1 support, the installer's atomic `current` switch and restart select the retained `parity.json` automatically. Verify its hash, read back the restored release revision and verify the legacy flow again with the version-1 policy hash. If registration accidentally replaced `parity.json`, stop the service and atomically restore the saved version-1 bytes before activating the older release. A deployment or a fresh provider probe does not establish that any candidate check succeeded.
