# config

Checked-in configuration the coordinator loads at start.

* `catalogue.json`: the capability catalogue: methods for a capability (for example `web-research`) and the pipeline file each one runs.
* `agents/*.json`: agent profiles (model, effort, instructions, MCP servers).
* `pipelines/*.json`: pipeline definitions run by `packages/coordinator/projects.ts`.
* `projects/<id>/project.json` and `rules.md`: connected projects, their locations and rules; `reference.json` where a project has a reference site.
* `connections.example.json`: shape of the host-only connections file. Real connection files and keys never live here.
