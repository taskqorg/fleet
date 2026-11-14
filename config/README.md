# config

Checked-in configuration the coordinator loads at start.

* `catalogue.json`: the capability catalogue: methods for a capability (for example `web-research`) and the pipeline file each one runs.
* `agents/*.json`: agent profiles (model, effort, instructions, MCP servers).
* `pipelines/*.json`: pipeline definitions run by the coordinator's pipeline runner (`go/coordinator/pipelines.go`).
* `lab/use-cases/<project>.json`: the Lab use-case registry; `lab/examples/*.json`: owner-written example seeds, imported as lab artifacts ([fleet-lab.md](../docs/designs/fleet-lab.md) "Use-case registry and examples").
* `projects/<id>/project.json` and `rules.md`: connected projects, their locations and rules; `reference.json` where a project has a reference site.
* `connections.example.json`: shape of the host-only connections file. Real connection files and keys never live here.

`agents/`, `pipelines/` and `catalogue.json` are the repository revision of definitions the coordinator keeps in its store
(docs/designs/fleet-definition-editors.md): the coordinator imports a file it has not seen, and after that its store wins;
a changed file here shows as `repository-changed` until someone saves it. The files are canonical (keys sorted,
two-space indent, trailing newline); `go/coordinator/definitions_test.go` checks it.

`pipelines/publish-static.json` exposes the bounded publication program through the ordinary `pipeline_run_create` tool. Supply an example input named `index.html`; the destination comes from the run project and existing host-only publication binding, never from example fields. The resulting output records the protected URL, immutable version, source hashes and readback verification. A stored artifact alone is not a publication.
