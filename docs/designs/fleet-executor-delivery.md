# Executor delivery

How executor files are built in CI, published and fetched by a host. Fetching is manual and changes no deployment. Language and port order: [stack](fleet-stack.md).

## Build

- The CI job `go` cross-builds `fleet-executor-<os>-<arch>` for linux/amd64 and darwin/arm64 from one Linux runner with `CGO_ENABLED=0` and `-trimpath`, each with a `.sha256` file, into the workflow artifact `fleet-go-<commit sha>` ([stack](fleet-stack.md#executor-and-gateway)). The Linux binary is static, so there is no glibc requirement. The gateway (`fleet-gateway-<os>-<arch>`) is built the same way.
- The job runs on pull requests too, so a downloaded Mac binary can be checked before merge. A host uses only a successful run of a push to `main`.
- The main release pack (`fleet-release-<sha>`) also carries a linux/amd64 `fleet-executor` and `fleet-gateway`.
- PDS18 is amd64 ([PDS Codex environment](../pds-codex-environment.md)); the Mac is arm64. WSL uses the Linux file. Windows without WSL is not a target; a Windows executor is not built.

## Publication and trust

- Files are workflow artifacts with the default Actions retention, like the coordinator archive. After expiry that run cannot supply that revision; an already downloaded file stays usable. No GitHub Releases. Not built: lasting publication, an update source and who starts an update.
- SHA-256 detects corruption, not a publisher. GitHub access and the selected revision are the trust boundary.
- macOS files are not Developer ID signed or notarized. No signing identity and no automatic quarantine removal are added. Not built: signing.

## Fetch a CI release manually

With an authenticated GitHub CLI, choose a successful main CI run for the exact full commit SHA, then download and verify in an empty directory:

```sh
revision=<full-commit-sha>
run=<successful-main-ci-run-id>
target=darwin-arm64 # Linux/WSL: linux-amd64
gh run view "$run" --repo taskqorg/fleet --json headSha,event,headBranch,conclusion
# Require headSha == revision, event == push, headBranch == main, conclusion == success.
gh run download "$run" --repo taskqorg/fleet \
  --name "fleet-go-$revision" --dir executor-download
(cd executor-download && shasum -a 256 -c "fleet-executor-$target.sha256") && \
  chmod +x "executor-download/fleet-executor-$target"
```

Stop on any mismatch or failed command. Use the file only after verification; activating or replacing an installed executor is separate operator work. Keep a verified local copy if the revision must outlive retention.

CLI downloads normally carry no quarantine attribute. If macOS refuses to run the file, inspect it with `xattr -l <file>` and record the refusal before any operator-approved signing or quarantine change.
