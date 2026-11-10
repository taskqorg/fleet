# Executor delivery (L2)

Scope: build executor files in CI; fetching is manual and changes no deployment.

Use native runners: Ubuntu 24.04 for x86_64-unknown-linux-gnu and macOS 15
for aarch64-apple-darwin. Native macOS avoids a cross-compilation toolchain
for bundled SQLite and kernel calls. No new dependencies. Linux requires
glibc 2.39 or newer; PDS18 documents Ubuntu 26.04. WSL compatibility depends
on its distribution and remains unqualified until run there.

Publish Actions artifacts with the coordinator's existing default retention,
as confirmed by the coordinator for this phase. Lasting publication remains
stack-design question 7. After expiry a host cannot fetch that revision from
that run; an already downloaded file remains usable. No GitHub Releases.

PR runs also upload executor candidates so the downloaded Mac binary can be
qualified before merge. A host selects a successful main run for normal use.
Each target has a binary and adjacent SHA-256 file, named with the full target
triple. Checksums detect corruption; GitHub access and selected revision are
the trust boundary. No signing identity or automatic quarantine removal is added.
