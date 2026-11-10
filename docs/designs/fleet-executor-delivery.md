# Executor delivery

How executor files are built in CI, published and fetched by a host. Language and port order: [stack](fleet-stack.md).

## Build

- The CI job `go` cross-builds `fleet-executor-<os>-<arch>` for linux/amd64 and darwin/arm64 from one Linux runner with `CGO_ENABLED=0` and `-trimpath`, each with a `.sha256` file, into the workflow artifact `fleet-go-<commit sha>` ([stack](fleet-stack.md#executor-and-gateway)). The Linux binary is static, so there is no glibc requirement. The gateway (`fleet-gateway-<os>-<arch>`) is built the same way.
- The job runs on pull requests too, so a downloaded Mac binary can be checked before merge. A host uses only a successful run of a push to `main`.
- Releases: the `go` job's files are for pull request trials. A host runs a release from GitHub Releases, published by `.github/workflows/release.yml` on a `v*` tag; each tarball carries the executor, the gateway, the coordinator and `fleet-update` ([self-update](fleet-self-update.md)).
- PDS18 is amd64 ([PDS Codex environment](../pds-codex-environment.md)); the Mac is arm64. WSL uses the Linux file. Windows without WSL is not a target; a Windows executor is not built.

## Publication and trust

- GitHub Releases are lasting and public. `SHA256SUMS` is signed with the release key (Ed25519); `fleet-update` checks the signature against its compiled-in public key, then each tarball's SHA-256. The release also carries GitHub build provenance (`provenance.sigstore.jsonl`, `gh attestation verify`).
- macOS files are not Developer ID signed or notarized. No signing identity and no automatic quarantine removal are added. Not built: signing.

## Fetch and update

Every host updates itself with `fleet-update` ([self-update](fleet-self-update.md)); the first install downloads a release by hand ([deploy runbook](../../deploy/README.md#first-install-pds18-the-owners-mac-wsl)).
