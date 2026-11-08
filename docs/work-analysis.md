# Work analysis

Human interfaces and agents query the same retained work history within their permissions. Support project, task, execution, step, method, agent, environment, connection and time-range views, with drill-down from aggregates to source records.

## Coverage

Account for the full work cycle: queueing, preparation, agent and tool execution, questions and waits, retries, review, merge, CI, publication and cleanup. Include failed and cancelled work and infrastructure operations without an active agent.

Expose elapsed and active time, waiting reasons, invocation and retry counts, tokens, monetary cost, CPU, memory, disk and other available resource measurements. Identify the method and actual instructions, skills and configuration used so differences can be investigated.

Distinguish elapsed task duration from summed execution time across parallel work. Attribute nested usage without counting it again in parent totals. State aggregation semantics and measurement coverage; estimates and unavailable data are not exact values or zero.

## Improvement workflow

An operator or architect may discuss project history with an agent, identify repeated work or expensive stages, and inspect the evidence. Findings may suggest a skill, instruction, tool, agent configuration, project rule or pipeline change; analysis does not require a predefined optimization category.

Use selected work as input to the [experiment mechanism](experiments.md), compare the change and apply it under existing project and recommendation rules. Production analytics and laboratory measurements remain connected to their actual executions; neither creates a separate history store requirement.
