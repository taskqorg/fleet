# Laboratory deployment evidence

Observed 2026-11-09. The real public baseline/configured pair completed and was inspected. This is bounded configuration-experiment acceptance, not a general isolation or statistical quality claim.

## Installed candidate

- Coordinator: `/home/fleet/releases/candidate-5580d567`, archive SHA256 `5580d567b6173e57fce47f73fa3edccddde4c483fee505a2d0c95b9fd7da6818`, verified remotely. Complete compiled profiles and contract included. Fleet service running, PID 421366, NRestarts 0. Prior candidate retained.
- Mac executor: SHA256 `4dcbf22b006a52cfe739935168384b1199ecf1ad174fc9eca9e18efb15cfebce`, matching qualification. Activated only after zero unfinished journal commands; LaunchAgent running, PID 41298. Previous binary/private config retained outside Git.
- Explicit Fleet-owned native HOME and CODEX_HOME; owner completed dedicated subscription login. Personal configuration and credentials were not copied. Predeployment native provenance and limitations: [runtime evidence](lab-runtime-build.md).
- Existing Access and machine routes unchanged; anonymous `/lab` and `/api/experiments` return 302 to login. Actual owner browser loaded the protected laboratory.

## Public experiment

Experiment `b284cf02-df90-4306-ad95-07034708bcc7` was created through the visible form at https://fleet.ufobe.com/lab. Both profiles received identical task text; baseline runs first through normal executor dispatch.

| Variant | Fleet session | Execution |
|---|---|---|
| Baseline | a82091f1-43f0-4492-b551-ac2313a146d0 | e510f46e-5433-4a63-b162-5a53f38b0fb9 |
| Tool plus instructions | e34dc43f-2f0f-454c-96f8-c1f1afc8c8b1 | a54698c6-16ca-468a-a1c9-d0806d6b8f1f |

## Observed outcomes

| Observation | Baseline | Tool plus instructions |
|---|---|---|
| Terminal state | completed | completed |
| Native thread | 01a1be0a-c60d-74b2-8864-4306b2401380 | 01a1be0c-82b4-7843-8394-f12a1c5dd543 |
| Receipt duration | 112.666 s | 108.988 s |
| Native input tokens | 58060 | 75973 |
| Cached input tokens | 55424 | 66816 |
| Native output tokens | 4565 | 4276 |
| Design-rules MCP | Namespace absent, no MCP calls | get_design_rules completed, arguments {}, actual response retained |
| Result | Self-selected warm palette and studio page | Supplied heading, palette and three-card layout applied |

Token values are raw reported counters from separate fresh sessions, not subscription charges. The configured run used more input tokens in this pair; no stable speed, cost or quality superiority follows.

Both retained native contexts passed model/effort, supplied-instruction and personal Memory pointer checks; baseline lacked the selected MCP namespace. [Baseline provenance](../evidence/lab-live-baseline-provenance.json) and [configured provenance](../evidence/lab-live-configured-provenance.json) contain booleans and rollout hashes rather than private instruction contents. No synthetic conflicting personal fixture was installed; marker absence alone is not a suppression test.

[Public experiment evidence](../evidence/lab-live-experiment.json) retains snapshots, events, results and downloaded artifact sources. [Source checks](../evidence/lab-live-source-checks.json) verify all SHA256 values, the configured heading after parsing HTML line breaks, all three palette values and three feature-card articles. Raw substring heading comparison initially returned false because the heading contains a br element; parsed visible text matches, with a trailing period. Desktop CSS defines a narrow introduction beside the card column and mobile stacking.

The actual public browser rendered both retained outputs with CSS: [desktop](../evidence/lab-live-desktop.png), [wide layout](../evidence/lab-live-wide.png), [mobile](../evidence/lab-live-mobile.png). Screenshots were opened and visually inspected. At 390px the parent laboratory page had no horizontal overflow; the configured preview heading and controls remained readable. Fixed-height frames show a scrollable excerpt, not the full generated page at once.

Both full-session links were opened through the public authenticated browser: baseline assistant history/result rendered; configured history included get_design_rules and its result preview. Comparison, two result frames and selected execution identities reloaded. A note was saved through the visible form, its successful status and server readback confirmed, then reload restored the same text. An earlier save attempt had no confirmed durable result; only the subsequent confirmed save is accepted. No duplicate experiment was created.

## Limits and compatibility

- This release is a working-tree candidate identified by artifact hashes; it is not committed/pushed or a tagged release.
- Configuration independence is qualified for these fresh Codex executions. Built-in harness instructions remain; no filesystem containment is claimed. Claude, Serena and repository-aware stateful tools are deferred.
- Old personal-home native sessions and their results remain readable. Resuming them through the new dedicated home is explicitly refused; no personal transcript migration or personal-config fallback occurred. Changed-configuration native resume remains unqualified.
- CPU/RSS cover the native leader; disk covers the executor data directory. Child-tree attribution, I/O and subscription cost remain unknown.
- Laboratory refresh is manual. Static previews support same-directory assets; dynamic services and general publication are outside this experiment.
- Earlier native stop/abnormal-exit evidence and current fake-process regressions cover unchanged cleanup behavior; these lifecycle cases were not repeated with another paid call after this configuration change.

Serena bootstrap recovery, local runtime qualification, coordinator checks and native deployment evidence remain distinct. No unrelated service or personal harness configuration was changed.

