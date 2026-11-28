# Fleet repository publisher

This package implements the PDS18 GitHub App path for Fleet publishing. The coordinator authorizes the project's managed branch and action. A Linux peer-credential check admits only the coordinator's OS user to the operation endpoint. The executor can read readiness and redeem a single-use ticket to stream Git objects for an authorized draft. The service account loads the systemd credential and keeps installation tokens private.

Provider requests use GitHub's fixed HTTPS origin. Git pushes run in a service-owned bare repository with a fixed remote, a verified commit ID, disabled hooks and no inherited Git configuration. The execution worktree supplies only an object pack. The provider's branch rules and merge queue control delivery.

See the [operator procedure](../../deploy/infrastructure/README.md#fleet-publishing-identity).
