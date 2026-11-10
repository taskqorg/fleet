# Executor delivery

How executor files are built in CI, published and fetched by a host. Fetching is manual and changes no deployment. Language and port order: [stack](fleet-stack.md).

## Build

- The CI job `executor-release` builds `cargo build --locked --release -p fleet-executor` on native runners: Ubuntu 24.04 for `x86_64-unknown-linux-gnu`, macOS 15 for `aarch64-apple-darwin`. Native macOS avoids a cross toolchain for bundled SQLite and kernel calls. No new dependencies.
- Each target gets `fleet-executor-<triple>` and `fleet-executor-<triple>.sha256`, uploaded as the workflow artifact `fleet-executor-<triple>-<commit sha>`. The job summary records build seconds and bytes.
- The job runs on pull requests too, so a downloaded Mac binary can be checked before merge. A host uses only a successful run of a push to `main`.
- Linux needs glibc 2.39 or newer (Ubuntu 24.04 build). PDS18 is x86_64 ([PDS Codex environment](../pds-codex-environment.md)); the Mac is arm64. WSL uses the Linux file; its distribution must meet the glibc requirement. Windows without WSL is not a target.
- The gateway is Go: the CI job `go` cross-builds `fleet-gateway-<os>-<arch>` for linux/amd64 and darwin/arm64 with `CGO_ENABLED=0` into the artifact `fleet-go-<sha>`, and the main release pack carries `fleet-gateway`.

## Publication and trust

- Files are workflow artifacts with the default Actions retention, like the coordinator archive. After expiry that run cannot supply that revision; an already downloaded file stays usable. No GitHub Releases. Not built: lasting publication, an update source and who starts an update.
- SHA-256 detects corruption, not a publisher. GitHub access and the selected revision are the trust boundary.
- macOS files are not Developer ID signed or notarized. No signing identity and no automatic quarantine removal are added. Not built: signing.

## Fetch a CI release manually

With an authenticated GitHub CLI, choose a successful main CI run for the exact full commit SHA, then download and verify in an empty directory:

```sh
revision=<full-commit-sha>
run=<successful-main-ci-run-id>
target=aarch64-apple-darwin # Linux/WSL: x86_64-unknown-linux-gnu
gh run view "$run" --repo taskqorg/fleet --json headSha,event,headBranch,conclusion
# Require headSha == revision, event == push, headBranch == main, conclusion == success.
gh run download "$run" --repo taskqorg/fleet \
  --name "fleet-executor-$target-$revision" --dir executor-download
(cd executor-download && shasum -a 256 -c "fleet-executor-$target.sha256") && \
  chmod +x "executor-download/fleet-executor-$target"
```

Stop on any mismatch or failed command. Use the file only after verification; activating or replacing an installed executor is separate operator work. Keep a verified local copy if the revision must outlive retention.

CLI downloads normally carry no quarantine attribute. If macOS refuses to run the file, inspect it with `xattr -l <file>` and record the refusal before any operator-approved signing or quarantine change.

## After the executor moves to Go

The Go executor builds with `CGO_ENABLED=0` for every target from one Linux job, like the coordinator in the CI job `go` ([stack](fleet-stack.md#executor-and-gateway)). The native macOS runner and the glibc requirement then fall away: the Linux binary is static. Signing and quarantine are unchanged. Not built: the Go executor.
