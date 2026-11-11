# State ownership

Fleet owns accepted tasks, dependencies, questions, decisions, execution links and their work history. No issue, repository or external board is required to create or execute work, including laboratory assignments.

Keep a readable chronological history of discussion, decisions, execution changes, results and their authors or sources. Link the underlying sessions and evidence; this preserves the useful history previously provided by issue threads without making the board authoritative.

GitHub, GitLab or other project systems may link issues and receive or supply configured updates through integrations. External changes enter Fleet through its operations and authority rules. A linked issue and a Fleet task are not independently authoritative copies of the same work state. Exact synchronization and conflict behavior remain integration decisions.

Harnesses retain their native session semantics; Fleet retains available execution history and associations under the runtime and observation contracts. Project systems remain authoritative for their own source code, assets, CI and publication facts. Record those facts without equating an external operation's success with completion of the whole task.

Instructions, skills, agent configurations and pipeline definitions remain canonical repository files. State ownership does not require copying external materials or choosing a particular database or event-storage architecture.

This is the Fleet target. Existing TaskQ board-backed work remains unchanged until a separately designed migration preserves its history and operational continuity.

## Project revisions

The project definition owner holds current settings and owned context text in one immutable revision bundle. Release files are seeds. Website and PM use the same project operations and optimistic revision check. Initial creator membership is trusted, revocable metadata, independent of immutable creator audit and existing explicit person grants; it is not a second project configuration store.
