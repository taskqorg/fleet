# Stage 6: Comprehension Test (Phase 3 artifact scope)

Reviewer: egm-comprehension. Ranges: lines 368-418, 492-518 (FeatureRange) and 1039-1140 (DetailedRange).

**What this proposes**

Fleet gets its own artifact store, replacing today's base64 upload path and its size cap. Each host (PDS18, the Mac and Windows) runs its own S3 store, reachable through Cloudflare Tunnel behind Cloudflare Access. Each project lists the hosts that must hold its artifacts. Executors upload files straight to a store in 32 MiB multipart parts that can resume, and the coordinator then checks the size and hash. A placement worker in the coordinator asks the executors on the target hosts to copy objects until every listed host holds a verified copy. It also runs a daily re-hash audit and applies optional retention rules (move or delete) once the operator has confirmed them. Executors build image and video previews with `ffmpeg`. An execution cannot succeed while a required artifact is not stored. Agents get two ways to store files: an output folder (`FLEET_OUTPUT_DIR`) that the executor collects for them, and explicit MCP tools for artifacts, history, transcripts, tasks and questions.

**How the existing system works, per this document**

Today executors send artifacts to `POST /api/hosts/artifacts` as base64. Both the coordinator and the executor cap the size with `FLEET_MAX_ARTIFACT_BYTES` (16 MiB by default in the executor). A run can succeed even when its artifact upload got a 4xx. `delivery.ts` reads `delivery-result.json` by decoding `contentBase64`. Executors already collect worktree output (commit `23c5418`) and send rows to the coordinator in batches through a local outbox. The tool schemas on `/api/tools` are stubs (`{type:'object'}`).

No Blockers.

**Findings**

1. Major: retention example target `nas` is not a defined host or store.
2. Major: single-part vs multipart rule stated inconsistently (§3 and `reserve` say always multipart; item 24 single-part PUT; the tool returns `url | parts[]`). Fix: one rule.
3. Major: how the whole-object SHA-256 is verified for multipart uploads and for copies (the coordinator does not download) is not stated.
4. Major: meaning of `artifact.store_id` next to `artifact_copy`, and when copy rows are created, are unclear.
5. Major: success rule while retrying: who retries after the agent exits, what the UI shows, how discard interacts.
6. Major: `artifact_get` "local path" has no defined meaning for objects in Garage.
7. Nit: what sets store state `full` and how writers fall back.
8. Nit: purge vs stores offline at the time; placement must never copy a deleted hash.
9. Nit: role `web` defined but never produced.
10. Nit: "raw" vs "redacted raw" transcript.
11. Nit: item 26b has two Why lines.
12. Nit: "readiness" and "host's last observation" used without a pointer.
