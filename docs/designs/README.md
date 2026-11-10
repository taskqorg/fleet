# docs/designs

Technical designs, briefs and their reviews. A design here is accepted direction, not shipped behaviour; check the code.

* `fleet-data-foundation.md`: Postgres, typed tasks and executions, the artifact store, ingest, isolation, people and hosts, in phases. `fleet-data-foundation-brief.md` is its brief; `fleet-data-foundation-review/phase3/live-findings.md` holds live findings it cites.
* `fleet-operator-app.md`: the operator web app and its live change feed; `reviews/fleet-operator-app/` is its review.
* `fleet-view-urls-and-list-path.md`: view URLs and the list path.
* `fleet-lab.md`: the Fleet Lab (experiments, examples, evaluators, Promote) and work analysis.
* `fleet-people-access.md`: people, identity, per-host secrets, holders, subscriptions and logins, isolation layers, take, dispatch, intake, readings and budgets.
* `fleet-pm-agent.md`: the PM agent: director-scoped agent tools, task tools, Held tasks, focus, watch mode.
* `fleet-connections.md`: API key connections, the Fleet gateway copied from Pipe's read lane, per-run tokens, call methods and deleting Pipe.
* `fleet-conversations.md`: the Conversations screen: one thread per task, paging both ways, typed transcript blocks, copy, question and result cards, the Results page. `conversations-brief.md` is its brief.
* `fleet-stack.md`: Go for every service and the migration order, delivery order, rule delivery, toolchain, JSON Schema contracts, MCP subcommands, the trial slice and measurement protocol.
* `fleet-docs-governance.md`: document kinds and their life, what an agent may add, EGM outputs outside the tree, the warning-only docs report and `docs-order` issue, the proposed specq rule, and the docs rework plan.
* `fleet-executor-delivery.md`: executor release files built in CI and how a host fetches them.
* `fleet-self-update.md`: releases by tag on GitHub Releases with provenance, the `fleet-update` timer on every host, PDS18 order, rollback, `autoUpdate`, versions in `hosts_list`.
* `legacy-inventory.md`: what the removed legacy pages did and which app section rebuilds each gap.
* `entity-ids-brief.md`: human entity codes such as `T-FLT-42`.
* `web-stack-brief.md`: front-end stack choice.
* `reviews/drift-2026-11-10.md` and `reviews/drift-2026-11-10-web-stack.md`: drift between these designs and the code, cited by the conversations and view-URL designs.
