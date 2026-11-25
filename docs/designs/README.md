# docs/designs

Technical designs, briefs and their reviews. A design here is accepted direction, not shipped behaviour; check the code.

* `fleet-data-foundation.md`: Postgres, typed tasks and executions, the artifact store, ingest, isolation, people and hosts, in phases. `fleet-data-foundation-review/phase3/live-findings.md` holds live findings it cites.
* `fleet-operator-app.md`: operator and architect workflows, the operator web app, live change feed and surviving retirement requirements.
* `fleet-ia-and-journeys.md`: top-level information architecture, Lab-first evaluation model, core user journeys `J1`–`J5`, and agentic product-development method.
* `fleet-view-urls-and-list-path.md`: view URLs and the list path.
* `fleet-data-layer.md`: instant screens by default: the read mirror, one view operation per screen, the change stream as the only reread, server queries that indexes serve under RLS, and what keeps it from drifting.
* `fleet-history.md`: one history mechanism: activity entries written by one trigger function, `history_view` with scope filters, folding and cursors, and the History screens (rail page, section chip and empty pane, object tab).
* `fleet-analytics.md`: `analytics_query` over work records and the price table.
* `fleet-lab.md`: the Fleet Lab (experiments, examples, evaluators, Promote) and work analysis.
* `fleet-people-access.md`: people, identity, per-host secrets, holders, subscriptions and logins, isolation layers, take, dispatch, intake, readings and budgets.
* `fleet-definition-editors.md`: definitions (agents, pipelines, catalogue) as store revisions with drafts, conflicts, export to git, and the operations the editors need.
* `fleet-pm-agent.md`: the PM agent: director-scoped agent tools, task tools, Held tasks, focus, watch mode.
* `fleet-triggers.md`: triggers: one mechanism for timed and change wakes of a conversation or a bot (every, daily, manual; only if changed), presets in `config/triggers/`, replacing watch, bot schedule and bot automations.
* `fleet-swarm.md`: universal cross-harness Swarm coordination, configurable coordinator strategies, durable shared board, visual Swarm GUI and Lab evaluation.
* `fleet-code-mode.md`: the `code` and `poll` executor MCP tools that run JavaScript with Fleet tools and granted connections as functions, in every harness.
* `fleet-connections.md`: API key connections, the Fleet gateway copied from Pipe's read lane, per-run tokens, call methods, file transfer and deleting Pipe.
* `fleet-project-access.md`: project resource access: access servers, grants, the owner's approval card, the provisioner, `fleet-provision` recipes and probes.
* `fleet-conversations.md`: the conversation model (one conversation per line of work, grouped by bot, PM, room and task) and the Conversations screen: task history, paging both ways, typed transcript blocks, copy, question and result cards, the Results page.
* `fleet-asset-pipelines.md`: provider keys from the Keychain into connections, deleting AssetQ, registry statuses and `maps_to`, Meshy and image pipelines with Blender on the Mac, per-use-case judges, `loop`, the character pipeline.
* `fleet-stack.md`: Go for every service and the migration order, delivery order, rule delivery, toolchain, MCP subcommands.
* `fleet-contracts.md`: JSON Schema contract sources and generators (L3).
* `fleet-go-code.md`: Go code target state, third-party modules, coordinator rewrite, executor and gateway.
* `fleet-stack-trial.md`: the language trial slice and measurement protocol (L1).
* `fleet-docs-governance.md`: document kinds and their life, what an agent may add, EGM outputs outside the tree, the warning-only docs report and `docs-order` issue, the proposed specq rule, and the docs rework plan.
* `fleet-executor-delivery.md`: executor release files built in CI and how a host fetches them.
* `fleet-self-update.md`: releases by tag on GitHub Releases with provenance, the `fleet-update` timer on every host, PDS18 order, rollback, `autoUpdate`, versions in `hosts_list`.
* `fleet-delivery-pipeline.md`: merge queue and ruleset, stacked and flaky rules, automatic release per green `main` batch, faster host pickup, smoke in the host health probe.
* `fleet-host-updates.md`: one update procedure for every component a host runs, and release pacing: hosts install only changed parts, by urgency class, idle or in a daily window.
* `fleet-host-updates.md`: one procedure for every updatable thing on a host (Fleet release, agent CLIs, host tools, project environments, OS packages): discover, propose, qualify offline and on hosts, merge, roll out, verify, watch, recover.
* `fleet-telemetry.md`: one span per operation across all services in ClickHouse, trace context, process CPU and memory, the rules pass, telemetry operations and the System screen read panels.
* `fleet-host-cleanup.md`: the run marker and run temporary directory that let the executor remove everything a run leaves, the hourly sweep of session directories and `/tmp`, release retention, `mcp connection` lifetime, swap and top-consumer readings in `host-saturation`, and one test PostgreSQL per machine.
* `fleet-error-tracking.md`: Bugsink as the error tracker: the browser tunnel, server errors through sentry-go, the `bugsink` connection, alerts into Needs you, source maps, onboarding.
* `fleet-system-management.md`: host remove, archive, delete and purge with previews, worktree removal on delivery, problems as needs-you questions, Postgres backups and restore drill, the audit journal and the System screen.
