# Fleet design verifier

Install this service independently of candidate CI, on a protected Linux systemd host with Git, OpenSSL 3, and Node.js 22 or newer at `/usr/bin/node`. The installer requires the operator's administrative account. The verifier uses a private systemd state directory and a dynamic service identity. It polls GitHub for open PRs targeting `main` and temporary merge queue branches, reads the exact Git objects in a bare repository, and runs only released code.

The initial service qualifies the nonvisual and fail-closed paths. It reports provider readiness independently of visual readiness. Leave the repository's existing rules in effect while this service is being qualified. See the [design approval contract](../../docs/designs/fleet-delivery-pipeline.md#design-approval) for cutover requirements.

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

The service retains check inputs before posting and readbacks after posting, under `/var/lib/fleet-design-verifier/receipts/`. Each file is indexed by the GitHub check's `external_id`. Merge-group receipts include every merge commit through current `main`, its original PR head, confirmed against the matching GitHub merge queue entry, and the corresponding protected PR receipt. This stage assumes the repository's existing merge-commit queue mode. Changes to that mode require requalification. GitHub may discard and regenerate queue refs. The service waits for fresh provider facts when a ref, base or original head moves.

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

   `fleetIdentityConfigured` reports roster registration. `lastReady` retains the last successful provider authentication even after a later failure. `latestPR` and `latestMergeGroup` each carry the exact commit, App check run and retained receipt when observed. An absent entry is unknown. Fleet marks provider readiness unknown when the last daemon observation is older than two minutes. The current service also reads and hashes selected original PNGs and recipe bytes through the Fleet admission operation. The results include tracked recipe-input comparisons. Normal production-route and isolated rendered parity are unavailable in this stage, so missing evidence, unknown impact and visual changes stay blocked.

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

For a rollback, stop the service or rerun the installer from the previous verified release which contains it. Leave the App credential and state directory in place. Upgrade one instance at a time. A deployment or a fresh provider probe does not establish that any candidate check succeeded.
