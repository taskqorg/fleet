# Stage 7: Critic Review

Target: `docs/designs/fleet-conversations.md`, FeatureRange. Reviewers: `egm-design`, `egm-structural`, `egm-style` (feature-spec standard). Date: 2026-11-10. Duplicates are merged; each row lists the reviewers that raised it.

| # | Severity | Source | Finding | Resolution |
|---|---|---|---|---|
| K1 | Blocker | design | The change-feed horizon works on `change.xid`/`change.seq`; `document` rows have no `xid`, so "thread reads copy that rule" had nothing to compute with. | Fixed: each thread read runs in one snapshot and returns that snapshot's `feedCursor`; the client follows the feed from it and places late rows by key under anchor keeping (operator-app pattern). |
| K2 | Blocker | design, style | One event holds several blocks, so `seq` alone cannot address a block. | Fixed: block key `(seq, index)` used for cursors, search hits, selection and export. |
| K3 | Major | design | Result block had no defined position. | Fixed: coordinator writes an `execution.ended` event on terminal transition; result block sits at its key and updates in place. |
| K4 | Major | design | `total` is a full count. | Fixed: open and `around` only; time reported by the check; listed in Risks. |
| K5 | Major | design | Paging over delta rows undefined; open stream could split. | Fixed: deltas stored as kind `event-delta`; open stream built in full as one entry at the end of the newest page. |
| K6 | Major | design, style | Membership rule contradictory and the two-task case was a drafter rule; stale index name. | Fixed: one rule and a set query; two-task case moved to Open Questions with a default; index `execution_task_seq`. |
| K7 | Major | design | Access control for thread routes not stated. | Fixed: project scope; hidden sessions shown as empty boundary rows. |
| K8 | Major | design | Clipboard write after a round trip loses the user gesture; whole export held in memory. | Fixed: `ClipboardItem` with a promise created in the handler, failure shown with retry; export route streams markdown. |
| K9 | Major | design | Re-projection assumed every raw record exists. | Fixed: records with no raw data or unknown shape get one `tool` block with raw JSON. |
| K10 | Major | structural | No Risks & Mitigations section. | Fixed: added. |
| K11 | Major | structural, style, design | Stages and `pnpm run reproject` undefined in FeatureRange. | Fixed: Terms define stages; the script and executor subcommand are named consistently. |
| K12 | Major | structural | Additional Resources and Future Improvements were H3 under NFR. | Fixed: H2. |
| K13 | Major | style | "never arrives below `last`" ambiguous; "newest first, ascending"; `limit` maximum unclear; quota-handoff sentence. | Fixed. |
| K14 | Nit | design | Text "size limit" had no source (invented limit). | Fixed: text read in `Range` pages, no cap. |
| K15 | Nit | design | Two different answers can race in `tasks.answer`. | Fixed: `UPDATE ... WHERE state = 'open'`; plan item added. |
| K16 | Nit | structural | Table placement and lead-ins; RCA and drift links; hover chips per OR-19/OR-20. | Fixed. |
| K17 | Nit | style, design | Unmeasured claim "slow to open"; "its CSP"; entries vs blocks; rejection wording and attribution. | Fixed. |

Re-audit: not run (cap of one; findings resolved directly). Stage 8 checks the result.
