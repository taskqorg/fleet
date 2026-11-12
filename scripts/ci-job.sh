#!/usr/bin/env bash
# The commands of ci.yml's app, browser check and release-build jobs, one place for the hosted job and its PDS18 twin
# (.github/workflows/pds18.yml); the Go parts and the browser build are scripts/ci-part.sh. Setup (checkout,
# toolchains, caches, artifacts) stays in the workflows. Run from the checkout root:
#   scripts/ci-job.sh app | browser-<shard> | release
set -eo pipefail
pg=$(ls -d /usr/lib/postgresql/*/bin 2>/dev/null | tail -1)
[ -z "$pg" ] || PATH=$pg:$PATH
case $1 in
  app)
    pnpm install --frozen-lockfile
    pnpm run app:build
    node --test tests/ui/*.test.ts ;;
  # Filter path (fleet-view-urls-and-list-path.md §4), Conversations (fleet-conversations.md "Browser check"), Lab-create.
  browser-0)
    pnpm exec playwright install chromium
    node tests/ui/filter.check.ts --assert --sizes 20000 ;;
  browser-1)
    pnpm exec playwright install chromium
    node tests/ui/filter.check.ts --assert --sizes 3000
    node tests/ui/conversations.check.ts
    node tests/ui/lab-create.check.ts ;;
  # Speculative release build (docs/designs/fleet-delivery-pipeline.md#4-automatic-release); needs GH_TOKEN, GH_REPO.
  release)
    version=$(deploy/release-version.sh "$GITHUB_SHA")
    previous=$(gh release view --json tagName -q .tagName 2>/dev/null || true)
    deploy/release-recent.sh > "$RUNNER_TEMP/window"
    . "$RUNNER_TEMP/window"
    mkdir out
    deploy/build-release.sh "$GITHUB_SHA" out "$version" "${previous#v}" linux/amd64 darwin/arm64
    jq -nc --arg v "$version" --arg c "$GITHUB_SHA" --arg d "$digests" '{version:$v,commit:$c,digests:$d,checkedWith:$ARGS.positional}' --args $recent > out/speculative.json
    echo "built $version, checked with: ${recent:-none}" | tee -a "$GITHUB_STEP_SUMMARY"
    # The gate takes the artifact only with these SHA256SUMS: no other job can write this job's annotations.
    echo "::notice title=release SHA256SUMS::$(sha256sum < out/SHA256SUMS | cut -d' ' -f1)"
    # Linux selftests, every binary of both targets static.
    deploy/verify-release.sh out new
    # New coordinator migrates, each recent one still serves.
    [ -z "$recent" ] || VERSION=$version RECENT=$recent DIGESTS=$digests deploy/migrate-check.sh new out ;;
  *) echo "unknown job $1" >&2; exit 2 ;;
esac
