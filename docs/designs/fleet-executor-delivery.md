# Executor delivery

How executor files are built in CI, published and fetched by a host. Language and port order: [stack](fleet-stack.md).

## Build

- `release.yml` job `build` packs the release tarballs after the merge for linux/amd64 and darwin/arm64 with `deploy/build-release.sh` (`CGO_ENABLED=0`, `-trimpath`, each binary checked static) into the workflow artifact `release` with `SHA256SUMS` ([checks per change](fleet-delivery-pipeline.md#checks-per-change)); those are the bytes the release signs. The Linux binaries are static, so there is no glibc requirement.
- Pull requests and the merge queue build no tarballs.
- Releases: A host runs a release from GitHub Releases, published by `.github/workflows/release.yml` on a `v*` tag; each tarball carries the executor, the gateway, the coordinator and `fleet-update` ([self-update](fleet-self-update.md)).
- PDS18 is amd64 ([PDS Codex environment](../pds-codex-environment.md)); the Mac is arm64. WSL uses the Linux file. Windows without WSL is not a target; a Windows executor is not built.

## Publication and trust

- GitHub Releases are lasting and public. `SHA256SUMS` is signed with the release key (Ed25519); `fleet-update` checks the signature against its compiled-in public key, then each tarball's SHA-256. The release also carries GitHub build provenance (`provenance.sigstore.jsonl`, `gh attestation verify`).
- macOS files are not Developer ID signed or notarized. No signing identity and no automatic quarantine removal are added. Not built: signing.

## Fetch and update

Every host updates itself with `fleet-update` ([self-update](fleet-self-update.md)); the first install downloads a release by hand ([deploy runbook](../../deploy/README.md#first-install-pds18-the-owners-mac-wsl)).
