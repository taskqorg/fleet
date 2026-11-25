# config

Checked-in configuration the coordinator loads at start.

* `harness-pins.json`: version, vendor URL and digest of every release-managed CLI target. Activation follows [self-update](../docs/designs/fleet-self-update.md#managed-agent-clis).
* `catalogue.json`: the capability catalogue: methods for a capability (for example `web-research`) and the pipeline file each one runs.
* `agents/*.json`: agent profiles (model, effort, instructions, MCP servers).
* `pipelines/*.json`: pipeline definitions run by the coordinator's pipeline runner (`go/coordinator/pipelines.go`).
* `triggers/*.json`: trigger presets: the values `trigger_save` copies into a new trigger ([triggers](../docs/designs/fleet-triggers.md#presets)). Read from the files on each `trigger_list`; changing one does not change triggers made from it.
* `lab/use-cases/<project>.json`: the Lab use-case registry; `lab/examples/*.json`: owner-written example seeds, imported as lab artifacts ([fleet-lab.md](../docs/designs/fleet-lab.md) "Use-case registry and examples").
* `projects/<id>/project.json` and `rules.md`: connected projects, their locations and rules; `reference.json` where a project has a reference site.
* `bugsink.json`: the operator app's Bugsink DSN, built into the bundle; `executor-defaults.json` holds the executor's (`bugsinkDsn`) ([error tracking](../docs/designs/fleet-error-tracking.md)).
* `connections.example.json`: shape of the host-only connections file. Real connection files and keys never live here.

`agents/`, `pipelines/` and `catalogue.json` are the repository revision of definitions the coordinator keeps in its store
(docs/designs/fleet-definition-editors.md): the coordinator imports a file it has not seen, and after that its store wins;
a changed file here shows as `repository-changed` until someone saves it. The files are canonical (keys sorted,
two-space indent, trailing newline); `go/coordinator/definitions_test.go` checks it.
