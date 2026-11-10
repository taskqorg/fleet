# docs/designs

Technical designs, briefs and their reviews. A design here is accepted direction, not shipped behaviour; check the code.

* `fleet-data-foundation.md`: Postgres, typed tasks and executions, the artifact store, ingest, isolation, people and hosts, in phases. `fleet-data-foundation-brief.md` and `-goldfish-review.md` and the `fleet-data-foundation-review/` folder are its inputs and reviews.
* `fleet-operator-app.md`: the operator web app and its live change feed.
* `fleet-view-urls-and-list-path.md`: view URLs and the list path.
* `fleet-lab.md`: the Fleet Lab (experiments, examples, evaluators, Promote) in stages; `lab-brief.md` is its brief and `reviews/fleet-lab/` its review.
* `fleet-people-access.md`: people, identity, per-host secrets, holders, subscriptions, take, dispatch, intake, readings and budgets. `people-access-brief.md` is its brief; `reviews/people-access/` holds the terms research and the review.
* `fleet-pm-agent.md`: the PM agent: director-scoped agent tools, task tools, Held tasks, focus, watch mode. `pm-agent-brief.md` is its brief; `reviews/pm-agent/` its review.
* `fleet-connections.md`: API key connections, the Fleet gateway copied from Pipe's read lane, per-run tokens, call methods and deleting Pipe.
* `fleet-conversations.md`: the Conversations screen: one thread per task, paging both ways, typed transcript blocks, copy, question and result cards, the Results page. `conversations-brief.md` is its brief and `reviews/conversations/` its review.
* `fleet-stack.md`: stack plan: rule delivery (Step 0), executor delivery, JSON Schema contracts and Python MCP servers as executor subcommands on the current stack, then a measured Go and Rust trial. `fleet-stack-brief.md` is its brief and `reviews/fleet-stack/` its review.
* `legacy-inventory.md`: what the removed legacy pages did and which app section rebuilds each gap.
* `entity-ids-brief.md`: human entity codes such as `T-FLT-42`.
* `web-stack-brief.md`: front-end stack choice.
* `reviews/`: dated reviews, including drift reports between these designs and the code.
