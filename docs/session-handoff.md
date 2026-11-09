# Fleet design session handoff

Continue the architecture discussion with the owner. This is session context, not a product contract. Accepted behavior lives in [fleet.md](../fleet.md); scenarios live in [use-cases.md](use-cases.md).

## Ownership and scope

The owner requested a full handoff to Claude Opus 5.5 at Medium effort, with the intermediate specification committed and pushed. Continue in the same checkout and branch, `alexkirs/chore-org-repo-inventory`. The outgoing agent stops after delivering this handoff.

This is product and architecture design, not implementation. Do not launch Fleet workloads, change infrastructure, deploy, or merge this branch. The repository contains a specification, not a working system. An organization-wide repository inventory is not established by these documents.

Follow [specq](https://github.com/taskqorg/specq), available locally at `~/Projects/specq`. For relevant host or agent tooling, use the private entrypoint supplied by the environment; do not scan secret stores.

## How to conduct the discussion

- Speak concise Russian. Persist professional English Markdown.
- Work top-down: agree on responsibilities and relationships before choosing implementation details.
- Discuss coherent batches of several related decisions. The owner explicitly rejected one small confirmation per turn as too slow.
- Recommend the simplest complete model for a small team, mostly one active owner. Challenge unnecessary restrictions and contradictions; do not agree mechanically.
- After the owner accepts a batch, update its canonical sections and verify the result. Do not keep accepted decisions only in chat.
- Keep proposals distinct from accepted decisions. A qualification or uncertainty is not approval of a specific technology.
- Avoid repeated summaries and rules. Link existing definitions instead of copying them.
- Ponytail full remains active: avoid speculative complexity; close replies with a brief statement of relevant unchosen or unchecked details.

## Decisions that must survive the handoff

These are navigation notes; the linked specification is authoritative.

- The system performs arbitrary work across projects, not only software development.
- Operator and architect interfaces share a core with the agent-facing interface. Operator desktop targets Windows, macOS and Linux.
- A PM is an ordinary selected-agent session with role instructions and tools. It may discuss multiple projects; it is not a permanent project channel. There is no separate request entity.
- Projects override default instructions and workflow rules. Instructions never grant permissions.
- Task, execution and session are distinct. Agent completion does not prove task acceptance. Tasks can continue without an open PM session.
- Merge, publication and deployment are project-specific operations, not universal task statuses. GitHub, GitLab and custom services must fit the same model.
- Agents normally use dedicated environments. This does not require a separate machine per task. A personal desktop can be explicitly connected; shared desktop collaboration is not the primary scenario.
- People and agents use one catalogue. Agents discover relevant descriptions and instructions on demand. Explicit method choices win; otherwise project recommendations take precedence over system recommendations.
- Laboratory evidence includes actual conditions and failed runs. Comments and conclusions do not automatically change recommendations; promotion is an architect action.
- Git files are canonical for skills, instructions, agent configurations and pipeline definitions. The catalogue does not own a second editable copy.
- Use current versions unless explicitly pinned. Record revisions actually used. The owner rejected default reuse of an old configuration for retries: traceability does not require freezing work to old versions.
- Operator and architect must be able to inspect skill and agent configuration diffs using existing tools.
- Losing contact does not prove failure or cancellation. An uncertain external effect does not authorize repeating the action. Pause and cancellation do not undo completed effects.

## Most recent accepted packages

Execution organization is recorded: environment requirements versus concrete local or remote environments; conflict isolation; automatic allocation with operator priorities and explicit choices; separate referenced service connections; secrets outside versioned files; project-selected result storage; preservation before releasing environments; observation of external operations without an active agent.

Pipeline architecture is recorded: structured text definitions in Git describe steps, dependencies, executors, inputs and outputs. Definitions, step instructions and run state are separate. The system coordinates steps and result transfer. The UI combines the definition with run state, without maintaining another workflow definition.

YAML was shown only as an illustrative format. The owner's last clarification retained uncertainty about the concrete choice. The outgoing agent explicitly left YAML as a candidate, not a mandatory format. No schema or execution engine has been selected.

## Pending proposals and open detail

Do not treat this list as accepted requirements:

- The preceding pipeline/laboratory batch proposed independent-step parallelism, selective continuation after failures, and using the same execution machinery for experiments and ordinary work. The owner focused approval on textual pipeline definitions and their architectural separation; review the canonical coverage before adopting remaining proposals.
- Exact pipeline format, schema, engine, branching, loops, editor and restart semantics remain open. Avoid inventing a general programming language.
- Dirty files, revision retention and the moment updated instructions are loaded remain unspecified. A repository edit does not rewrite instructions already read into an agent's context.
- Physical service boundaries, storage technology and mapping responsibilities to existing organization repositories remain unselected.
- Spec-driven development as an agent skill and a more structured specification layout were raised as future research, not decided.

## Suggested next move

Read the contract and use cases, then check whether all major blocks have clear responsibilities and relationships. Offer one substantial next batch addressing actual gaps. A useful candidate is the overall component map: operator and architect clients, shared coordination, execution hosts, repository-backed definitions, runtime state and external connections. Keep this a logical architecture, not a premature microservice or repository split.

Begin the new conversation with a brief Russian acknowledgement that you have taken over, then present the next batch. Do not ask the owner to repeat context, reconfirm accepted decisions, or approve merely reading the files. Do not silently implement new proposals.

## Validation and document budget

The root contract is 210 lines, the working target of its default 300-line cap. Follow specq's density and canonical-source rules before adding material. Move coherent detail to linked documents when justified; do not delete requirements or increase the budget merely to fit more text.

Run from the repository root after specification edits:

```sh
python3 /Users/kirs/Projects/specq/engineering/scripts/contract_lint.py fleet.md
git diff --check
```

Read back changed passages and check affected links. These checks validate documentation, not runtime behavior. No implementation or end-to-end execution has been verified.
