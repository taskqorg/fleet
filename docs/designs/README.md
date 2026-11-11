# docs/designs

Technical designs, briefs and their reviews. A design here is accepted direction, not shipped behaviour; check the code.

* `fleet-data-foundation.md`: Postgres, typed tasks and executions, the artifact store, ingest, isolation, people and hosts, in phases. `fleet-data-foundation-brief.md` is its brief; `fleet-data-foundation-review/phase3/live-findings.md` holds live findings it cites.
* `fleet-operator-app.md`: the operator web app and its live change feed; `reviews/fleet-operator-app/` is its review.
* `fleet-view-urls-and-list-path.md`: view URLs and the list path.
* `fleet-analytics.md`: `analytics_query` over work records and the price table.
* `fleet-lab.md`: the Fleet Lab (experiments, examples, evaluators, Promote) and work analysis.
* `fleet-people-access.md`: people, identity, per-host secrets, holders, subscriptions and logins, isolation layers, take, dispatch, intake, readings and budgets.
* `fleet-definition-editors.md`: definitions (agents, pipelines, catalogue) as store revisions with drafts, conflicts, export to git, and the operations the editors need.
* `fleet-pm-agent.md`: the PM agent: director-scoped agent tools, task tools, Held tasks, focus, watch mode.
* `fleet-connections.md`: API key connections, the Fleet gateway copied from Pipe's read lane, per-run tokens, call methods and deleting Pipe.
* `fleet-conversations.md`: the Conversations screen: one thread per task, paging both ways, typed transcript blocks, copy, question and result cards, the Results page.
* `fleet-asset-pipelines.md`: provider keys from the Keychain into connections, deleting AssetQ, registry statuses and `maps_to`, Meshy and image pipelines with Blender on the Mac, per-use-case judges, `loop`, the character pipeline.
* `fleet-stack.md`: Go for every service and the migration order, delivery order, rule delivery, toolchain, MCP subcommands.
* `fleet-contracts.md`: JSON Schema contract sources and generators (L3).
* `fleet-go-code.md`: Go code target state, third-party modules, coordinator rewrite, executor and gateway.
* `fleet-stack-trial.md`: the language trial slice and measurement protocol (L1).
* `fleet-docs-governance.md`: document kinds and their life, what an agent may add, EGM outputs outside the tree, the warning-only docs report and `docs-order` issue, the proposed specq rule, and the docs rework plan.
* `fleet-executor-delivery.md`: executor release files built in CI and how a host fetches them.
* `fleet-self-update.md`: releases by tag on GitHub Releases with provenance, the `fleet-update` timer on every host, PDS18 order, rollback, `autoUpdate`, versions in `hosts_list`.
* `fleet-delivery-pipeline.md`: merge queue and ruleset, stacked and flaky rules, automatic release per green `main` batch, faster host pickup, smoke in the host health probe.
* `fleet-telemetry.md`: one span per operation across all services in ClickHouse, trace context, process CPU and memory, the rules pass, telemetry operations and the System screen read panels.
* `fleet-system-management.md`: host remove, archive, delete and purge with previews, worktree removal on delivery, problems as needs-you questions, Postgres backups and restore drill, the audit journal and the System screen.
* `legacy-inventory.md`: what the removed legacy pages did and which app section rebuilds each gap.
* `entity-ids-brief.md`: human entity codes such as `T-FLT-42`.
* `web-stack-brief.md`: front-end stack choice.
* `reviews/drift-2026-11-10.md` and `reviews/drift-2026-11-10-web-stack.md`: drift between these designs and the code, cited by the conversations and view-URL designs.
