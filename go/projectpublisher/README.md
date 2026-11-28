# Fleet repository publisher

This package implements the PDS18 GitHub App path for Fleet publishing. The coordinator authorizes the project's managed branch and action. A Linux peer-credential check admits only the coordinator's OS user to the operation endpoint. The executor can read readiness and redeem a single-use ticket to stream Git objects for an authorized draft. The service account loads the systemd credential and keeps installation tokens private.

Provider authentication verifies the signed App identity and owner, then verifies GitHub’s exact, unauthenticated first-party private-App landing banner, structurally separate from the App description. Missing or ambiguous evidence blocks publication. The visibility read requests revalidation and is point-in-time: the existing token cache rechecks after ten minutes or an authentication rejection; restarting the service also forces a fresh read. A settings change is not detected instantly. It also verifies the exact installation, permission set and single-repository inventory before issuing a repository-scoped token.

Provider requests use GitHub's fixed HTTPS origin. Git pushes run in a service-owned bare repository with a fixed remote, a verified commit ID, disabled hooks and no inherited Git configuration. The execution worktree supplies only an object pack. The provider's branch rules and merge queue control delivery.

See the [operator procedure](../../deploy/infrastructure/README.md#fleet-publishing-identity).
