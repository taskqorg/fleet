# Goldfish Review Report (Stage 7, Critic Review)

**Target:** `/Users/kirs/orca/workspaces/fleet/shad/docs/designs/fleet-data-foundation.md` (`L1-583`)
**Standard:** `feature-spec-standard.md` (egm-design, egm-structural)
**Context:** `fleet-data-foundation-brief.md`, `fleet-data-foundation-goldfish-review.md`

Reviewers were told about these owner constraints, and no finding conflicts with them: no invented caps, harness quirks are facts and never blockers, the owner fully controls all hosts, existing products are preferred, and PDS18 infrastructure follows tight.md.

## Reviewer Status

| Reviewer | Status |
| --- | --- |
| `egm-design` | completed |
| `egm-structural` | completed |
| `egm-style` | completed |

**Totals after removing duplicates:** 1 🔴 Blocker, 17 🟡 Major, 22 🟢 Nit.

## Check of earlier finding M8: partially resolved

M8 said delivery is already task-like and that there are two question systems, `project-question` and `question`.

**Resolved:**
- Delivery is now an execution kind (L246, L366).
- "Production" is defined as having no `experiment_id` (L254).
- Lab trials are kept out of tasks (L246).

**Still open:**
- The document does not say where a delivery's own data goes: its task text, question, account and method.
- Both question systems are kept, and decisions add a third link point (L267). The `task_id` link is stored only in `document.data`, so it cannot be a real foreign key.
- Task state is stored twice (L262 versus G2 L57).
- It does not define how a result is accepted when no task exists.

The fixes are in M1-M3 below.

## Prioritized Findings

### 🔴 B1. One stored object shared by several artifact rows can be deleted while still in use
* **Lens:** `egm-design`
* **Scope:** L278, L289-296, L299-307
* **Problem:**
  - Objects are deduplicated within a project by `<project>/<lab|prod>/<sha256>`, so many artifact rows can point to the same object.
  - Deletes and tombstones run per artifact row. Deleting one row can therefore remove bytes that another live row still uses, for example two lab experiments that store the same file.
  - There is no forced purge for an object found to contain a secret, which the earlier review asked for in M4.
* **Recommendation:**
  - Count references per (store, key), either in `artifact_copy` or in a separate object table.
  - Delete the stored object only when no live row points to it.
  - Define a forced purge that tombstones every row pointing to that object.

### 🟡 M1. Task state is stored twice and has no acceptance (M8)
* **Lens:** `egm-design`
* **Scope:** L258-268, L57, L27
* **Problem:** `queued`, `in_progress` and `waiting` repeat what executions and open questions already show, and nothing says who changes them. There is also no `accepted` state and no acceptance record.
* **Recommendation:**
  - Store only intent and outcome: open, paused, cancelled, accepted, failed.
  - Link each acceptance to a specific execution or artifact.
  - Derive the other states in the view.

### 🟡 M2. A migrated delivery's own data has no target (M8)
* **Lens:** `egm-design`
* **Scope:** L246, L366
* **Problem:** The document does not say where a delivery's task text, question, account and method go.
* **Recommendation:** Each migrated delivery creates or links a `task` row that carries this data. Its steps become child executions.

### 🟡 M3. Two question systems remain (M8)
* **Lens:** `egm-design`
* **Scope:** L267 (and L779 for the link)
* **Problem:** The two systems are not merged, and the link is stored only in jsonb.
* **Recommendation:** Merge them into one question table with typed `task_id`, `execution_id` and `project_id` columns, or justify keeping them separate.

### 🟡 M4. ClickHouse deduplication does not guarantee "nothing counted twice"
* **Lens:** `egm-design`
* **Scope:** L326
* **Problem:**
  - `ReplacingMergeTree` removes duplicates by the ORDER BY key, here (`project_id`, `execution_id`, `ts`), not by `seq`.
  - Duplicates disappear only when background merges run, so totals can count a retried batch twice in the meantime.
  - The scope of `seq` is not stated. Today it is per host, and a collision is on record.
* **Recommendation:**
  - End the ordering key with (`execution_id`, `seq`), and make `seq` unique per execution.
  - State how queries handle rows that have not merged yet, for example `FINAL` or aggregation.

### 🟡 M5. Totals written once miss late events
* **Lens:** `egm-design`
* **Scope:** L256
* **Problem:** Totals are written when an execution ends. Outbox batches from an offline host can arrive later, and the stored totals then stay too low.
* **Recommendation:** Recompute totals when late events arrive. Show "pending ingest" until the host's outbox has drained.

### 🟡 M6. Row-level security does not apply to the table owner
* **Lens:** `egm-design`
* **Scope:** L359
* **Problem:** The coordinator connects as `fleet`, which owns the tables. Postgres skips row-level security for the owner, so the "second guard" does nothing.
* **Recommendation:** Set `FORCE ROW LEVEL SECURITY`, or connect as a non-owner role. Add a test that a query with the wrong `fleet.project_ids` returns no rows.

### 🟡 M7. Redaction covers less than the document claims
* **Lens:** `egm-design`, `egm-style`
* **Scope:** L18, L65, L185, L341-343, L569
* **Problem:**
  - The first pass removes only secrets the executor injected. Harness OAuth tokens in the account user's `HOME` and secrets an agent reads from disk are not covered.
  - The second pass only logs a pattern match, so the leaked value is still stored.
  - L18 and L185 ("secrets removed", "never sees credentials") and L569 ("redacted twice") therefore overstate the protection.
* **Recommendation:**
  - Mask pattern matches before storing them, not only log them.
  - Add the harness login tokens to the exact-value list.
  - Narrow the claims to "known credentials".

### 🟡 M8. Stores on loopback cannot serve presigned links to other machines
* **Lens:** `egm-design`
* **Scope:** L194, L276, L298
* **Problem:**
  - The stores listen on loopback, but CUJ 5 and `artifacts.get` give operators presigned links. A presigned URL to a loopback address does not work from another machine.
  - Placement relies on server-side copy, but `CopyObject` does not work between independent Garage nodes. The probe at L124 tested copies only within one node.
* **Recommendation:**
  - State the network path: an executor proxy, a tailnet bind, or a reverse proxy.
  - State that every copy between hosts goes through executors.

### 🟡 M9. Running agents as other OS users on PDS18 has no named privileged step
* **Lens:** `egm-design`
* **Scope:** L212-213, L399-412
* **Problem:** A per-execution `systemd-run --scope` as another OS user conflicts with tight's rules of no sudo and user units only. The document does not say how account users get linger and cgroup delegation.
* **Recommendation:**
  - Name the one-time root setup: a system template unit with `Delegate=yes`, or a small helper.
  - Record it in tight, as Q8 or a new question.

### 🟡 M10. The rollback window contradicts G7
* **Lens:** `egm-design`
* **Scope:** L82 versus L582 (and the reverse script at L738)
* **Problem:**
  - G7 promises rollback until Phase 7. The NFR says only until Phase 2.
  - The reverse script exports only `document` rows, so typed rows and Garage objects have no way back.
* **Recommendation:** Either align G7 with the NFR or extend the reverse tool. List which phases cannot be rolled back.

### 🟡 M11. No Risks & Mitigations section
* **Lens:** `egm-design`, `egm-structural`
* **Scope:** Whole document. Risks are currently scattered across L413-419 and L564-582.
* **Problem:** Several risks have no stated mitigation:
  - PDS18 is a single point of failure.
  - The outbox backlog grows while PDS18 is down.
  - ClickHouse and Garage disks fill up.
  - A store that is offline may hold a project's only copy.
  - Redaction can miss secrets.
  - The multipart checksum setting (L127) can be missed.
  - The Windows store choice (Q2) is still open.
* **Recommendation:**
  - Add an H2 section with a table: Risk | Impact | Mitigation | Residual.
  - Move the §9 known gaps into it.
  - Do not add caps.

### 🟡 M12. Some rejection reasons are not concrete trade-offs
* **Lens:** `egm-structural`, `egm-style`
* **Scope:** L446, L452, L485, L509, L530
* **Problem:**
  - A7 says only "do not reinvent the wheel".
  - A8d gives no concrete reason.
  - A2 and A11 restate the decision instead of naming its cost.
* **Recommendation:** Give one concrete trade-off per entry, for example "A8d: stores data only on PDS18 and cannot pin a project to Windows".

### 🟡 M13. Open Questions is out of order and mostly answered
* **Lens:** `egm-structural`, `egm-style`, `egm-design`
* **Scope:** L117-151
* **Problem:**
  - The order runs Q1, Q1b, Q2, Q9, Q8, Q4, Q5, Q6, Q7, and Q3 is missing.
  - Q1, Q1b, Q4, Q5 and Q6 are already answered.
  - Q7 is already decided at L577-578.
* **Recommendation:**
  - Renumber the questions in order.
  - Move the answered ones to "Resolved questions / probe results".
  - Leave Q2, Q8, Q9 and the open part of Q4. Close Q7.

### 🟡 M14. Reference "see item 35" points to nothing
* **Lens:** all three
* **Scope:** L146
* **Recommendation:** Replace it with a real anchor (L30 or §5 L334), or delete it.

### 🟡 M15. §9 says the same things twice, and the repeats disagree
* **Lens:** `egm-structural`
* **Scope:** L397-435 ("Inner layer" at L409 and L435; "Measurement" at L410-412 and L430-433)
* **Problem:** The two Measurement passages disagree: one measures CPU per account user, the other uses per-user process accounting. Bold paragraphs stand in for headings.
* **Recommendation:** Split the section into `#### Mechanism` and `#### Requirements`, and state each topic once.

### 🟡 M16. Q5 and Q6 state facts without evidence
* **Lens:** `egm-style`
* **Scope:** L137-149
* **Problem:** Field names, the 30-day default and the sweep-skip behaviour are stated as fact without saying how they were checked. The Codex values are called "measured" without a source.
* **Recommendation:** Add one evidence line and the version checked to each item, as Q4 does.

### 🟡 M17. The performance claim goes beyond the evidence
* **Lens:** `egm-style`
* **Scope:** L575
* **Problem:** "10 parallel sessions, proven live" covers only ten parallel Codex turns on the current outbox (brief L36).
* **Recommendation:** Narrow the claim to what was tested. State the target for the new event volume separately.

### 🟢 Nits
* **N1** (L314, `egm-design`): the upload retry has no defined end. Make the window an operator setting, or show `waiting` with a reason.
* **N2** (`egm-design`): there is no success metric for the foundation itself, for example "CUJ 1-6 pass live, with coverage labels".
* **N3** (L136, L109, `egm-structural`): D4 is cited before it is defined. "§3" is used, but headings have no § sign.
* **N4** (L128, L487-509, `egm-structural`): letter suffixes (A8b-d, Q1b) mix with plain numbers. Renumber, or explain the suffixes.
* **N5** (L209, L231, L368, `egm-structural`): "central store" contradicts D2, which says each host has its own store. Use one term.
* **N6** (L83, L357, `egm-structural`): a stray blank line after G7, and a missing blank line before the list at L357.
* **N7** (L7, L562, `egm-structural`): horizontal rules are used unevenly.
* **N8** (L119, L128, `egm-structural`): answered items show the "answered" date inconsistently.
* **N9** (L11, `egm-style`): "the best one" should be "the variant that scored highest".
* **N10** (L36, `egm-style`): "does not scale" has no threshold. Lead with the concrete problem.
* **N11** (L38, `egm-style`): "honestly" should be "on the same measures".
* **N12** (L127, `egm-style`): "gotcha" is informal. The last sentence states the cause backwards; move it to D2.
* **N13** (L248, `egm-style`): "waited forever" should give the date and duration.
* **N14** (L286, `egm-style`): "does not slow agents" has not been tested. Use "to reduce contention".
* **N15** (L339, `egm-style`): drop "not invented", or cite the source.
* **N16** (L136, L215, L539, `egm-style`): the native OTel point appears three times. Keep it in D4.
* **N17** (L446 onward, `egm-style`): "Rejected by author:" opens all 18 entries. Say it once before A1.
* **N18** (L452, `egm-style`): drop "simple".
* **N19** (L485, `egm-style`): replace the "reinvent the wheel" idiom.
* **N20** (L490, L529, `egm-style`): "future is unclear" needs a source or an "unverified" mark. "Heavy stack" should name the components.
* **N21** (L551, `egm-style`): "the remaining 5%" is not measured. Name the requirements left uncovered.
* **N22** (L189-194, `egm-style`): CUJ 5 has one "When" event but two "Then" outcomes. Split it.
