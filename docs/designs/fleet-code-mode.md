# Fleet code mode

Code mode gives an agent one tool that runs JavaScript. Inside that code, every Fleet tool and every granted connection is a function. The agent chains, loops over, filters and parallelizes calls in code, and only the value the code returns enters its context. The tool is an executor MCP server, so it works the same way in every harness Fleet runs: `codex`, `claude`, `antigravity` and `acp`.

## Purpose

- **Less context, fewer turns.** With separate tools, each call result passes through the model's context, and the model rereads it on every later turn. In code, intermediate results stay in variables.
- **Orchestration in code.** A coordinator starts workers, waits for them, compares their results and returns a short summary. Its context holds the summary, not every worker's transcript.
- **One mechanism for every harness.** Claude Code has its own Code Mode (`CLAUDE_CODE_EXPERIMENTAL_CODE_MODE`). Claude Code 2.1.319 refuses it outside the interactive terminal ("for now it only works in the interactive terminal"), and Fleet runs Claude headless, so Fleet cannot use it. The other harnesses have no equivalent.

Background: Cloudflare's "Code Mode" article and server-side `search` and `execute` tools; Claude Code's client-side `REPL` and `poll` tools; recursive language models (RLM), where the REPL persists, starts sub-agents and holds large inputs as variables.

## Terms

- **Code server:** `fleet-executor mcp code`, the MCP server that serves the `code` and `poll` tools for one execution.
- **Runtime:** the Node process that the code server starts for the execution. It keeps its global state between `code` calls.
- **Bridge function:** a function in the runtime that sends one tool call to the code server and returns its result.

## Profile setting

An agent profile selects code mode with `codeMode` ([profile schema](../../contracts/profile.schema.json)):

| Value | Effect |
|---|---|
| absent | No code server. |
| `add` | The code server is added. The `fleet` server and the connection servers stay, so the agent can call each tool directly or through code. |
| `only` | The code server replaces the `fleet` server and the connection servers. Fleet tools and connections are reachable only through `code`. Native harness tools (read, edit, shell) stay. |

The setting needs at least one bridged source: Fleet tools (`fleetTools`, on by default) or a granted connection. `ParseProfile` refuses `codeMode` with `fleetTools: false` and no connections. When access filtering leaves no connection at launch, the launch fails with `codeMode needs Fleet tools or a connection`. A named MCP grant and a connection cannot use the name `code`.

The executor passes the code server's spec in a 0600 file beside the run file (`runs/<execution>.code.json`), not in argv, because Fleet operations and connection schemas can outgrow one argument. The file is removed with the run file.

Native harness tools stay native. Fleet does not replace the file, edit and shell tools inside Codex, Claude or Antigravity.

## Tools

### `code`

Input: `{source: string, detachAfterSeconds?: number}`.

- `source` is the body of an async function. It may use `await`, `return` and `Promise.all`.
- The runtime keeps one global object for the whole execution. `state` is a persistent object for values that later calls read, for example `state.issues = await ...`. Variables declared with `let`, `const` or `var` live only inside one call.
- `print(...values)` and `console.log` append a line to the call's output; `process.stdout.write` appends its text as is.
- The reply is `{jobId, status, output, value, calls}`. `status` is `done`, `failed` or `running`. `value` is the JSON form of the returned value, `null` for none. `output` holds the printed text. `calls` lists the bridged calls.
- A thrown error returns `isError: true`, `status: "failed"` and `error: {message, stack}`, with the output printed before it.
- The runtime does not cut `output` or `value`. The agent controls their size in its code.

`detachAfterSeconds` defaults to 25. When the code is still running at that time, `code` replies with `status: "running"` and the output so far. The code keeps running. The executor's MCP filter forwards one request at a time, so a second call waits for the first reply; with the 25 s default, two calls in a row still answer within Codex's default MCP tool timeout of 60 seconds; a third parallel call can miss it. A value above 50 counts as 50: a longer reply wait would lose the reply in Codex and hold every later call, `poll` reset included. The value is a reply time, not a limit on how long the code runs.

### `poll`

Input: `{jobId: string, waitSeconds?: number}`.

- Replies with the job's final result, in the `code` reply shape, once the job ends.
- Replies `status: "running"` with the text printed so far when `waitSeconds` (default 25, at most 50; 0 answers at once) pass first.
- Each reply for a job carries only the output and `calls` added since that job's previous reply.
- A final result is returned once. The code server then drops the job, so it keeps no delivered output or value, and a later `poll` of that `jobId` answers `unknown jobId`. An undelivered job keeps its output until it is polled or the execution ends.
- `poll` without `jobId` lists the unfinished and undelivered jobs with their status.
- `poll {reset: true}` stops the runtime, for code that never yields (`while (true) {}`). Every running job fails with `runtime-exited`, and `state` is lost.

## Bridge functions

- `fleet.<tool>(args)` calls each tool of the `fleet` server for this execution: `session_start`, `session_fork`, `message_send`, `ask`, `execution_status`, `artifact_save`, the project operations, the director's operations and the others ([tool list](../../go/executor/mcp-tools.json)). The code server runs the same handler as the `fleet` server, with the same context and journal.
- `conn["<connection>"].<tool>(args)` calls a granted connection's tool through the gateway with this run's token ([call methods](fleet-connections.md#5-call-methods)). The bridge offers the same tools as the direct connection server: names of letters, digits and `_`.
- With `only`, the code server's MCP `instructions` carry each granted connection's instructions, which the removed connection servers gave. With `add` the direct servers still give them, so they are not repeated.
- `fleet.$tools()` and `conn["<connection>"].$tools()` return the names, descriptions and input schemas. The agent looks up a tool there instead of loading every schema into its context.
- A bridge function returns the tool's result, parsed from JSON when the result is JSON. A tool error rejects the promise with `FleetCallError {tool, outcome, code, output}`.
- Calls may run in parallel. The code server serves each bridge call on its own goroutine.
- `fleet` and `conn` hold one function per granted name, sent by the code server when the runtime starts. A name that is not granted, a typo or a probe by a built-in (`await` reads `then`, `JSON.stringify` reads `toJSON`) is `undefined` and sends no call. The code server refuses ungranted names as well.

## Authority

Code mode adds no authority. The code server bridges only the tools that the execution already has.

- The runtime runs with Node's permission model: `node --permission`. It reads the worktree and the execution input directory. It writes nothing, starts no child process, and loads no native addon or worker. Writes and commands go through the native harness tools under their own sandbox.
- A Claude profile whose `tools` list has none of `Read`, `Grep`, `Glob` or `Bash` gets no file reads in the runtime, and the `code` tool description says so.
- The runtime environment has no run token, gateway URL or Access file. The code server keeps them, as the connection servers do ([per-run tokens](fleet-connections.md#4-per-run-tokens)).
- The runtime has no network. Bridged calls go through the code server, so the runtime needs none, and a harness without network (Codex without `gitNetwork`) gets none through code. Node 22's permission model has no network rule, so the runtime runs inside an OS sandbox. Its stdin and stdout pipes to the code server still work.
  - macOS: `sandbox-exec` with `(deny network*)`, which also refuses Unix sockets, plus `(deny file-write*)` except `/dev/null`, `(deny signal)` except to itself, and `(deny process-exec)` except Node itself and `(deny process-fork)`. Mach services stay reachable.
  - Linux: `bwrap --unshare-net --unshare-ipc --unshare-pid`. The sandbox sees only the system directories, the Node binary itself and the read roots, all read-only, with its own `/tmp`, `/proc` and `/dev`. A Node installed under the home directory therefore exposes no other file of it. Host Unix sockets outside the read roots are unreachable; a socket file inside a read root stays reachable, as it is for the harness's own tools there. Other processes are invisible to signals.
- The OS sandbox, not only Node's permission model, refuses writes and signals: Node documents its model as a seat belt against mistakes, not a boundary against hostile code.
- File reads follow symlinks inside the read roots. That adds no authority: the same files are readable through the harness's own read tools, and the run file is readable by the same OS user anyway ([threat model](fleet-connections.md#4-per-run-tokens)).
- The host needs a Node with `--permission` (22.13 or later in 22, 23.5 or later in 23, any later major) and a sandbox that starts. The executor runs the sandbox to check it, with a 30 s bound. It keeps a ready result while its files exist and a failure for one minute, so a host made ready later, or a Node moved, needs no executor restart. Without them, the launch fails with `code-mode-unready: <reason>`, for example on Linux without user namespaces. Hosts without such a sandbox, such as Windows, cannot use code mode.
- Not built: Node on PDS18, which has none today; `bwrap --unshare-net` works there for the `fleet-agent` user.
- Not built: code mode readiness as a host capability for placement. Today a host that is not ready fails the launch with `code-mode-unready` instead of being skipped.

## Unknown outcomes

Bridge calls follow the existing rule: an uncertain effect stays unknown and is never repeated automatically.

- A Fleet mutation without an answer rejects with `FleetCallError` with `outcome: "unknown"`, `code: "pending"` and `output: {status: "pending", requestKey}`. A `request_child` without a host answer (`status: "pending-or-rejected"` and no `response`) rejects the same way; a retry with the same `requestId` is the same request. A host refusal rejects with `outcome: "error"`. The code reads it later with `fleet.agent_tool_result({requestKey})`. The bridge never retries it.
- A bridged call started before the code returns is part of its job, awaited or not: the final reply waits for it and lists its outcome in `calls`.
- A bridged call made after the code returned (a timer) is refused with `code: "job-ended"` and never sent: its effect would reach no reply.
- Arguments that do not serialize (a `BigInt`, a cycle) reject with `code: "bad-arguments"`.
- A connection call with outcome `unknown` rejects with `FleetCallError` with `outcome: "unknown"`.
- A journal failure on a Fleet call rejects with `outcome: "unknown"` and `code: "journal-error"`: the request may already be journaled and sent. A stored reply that does not decode is such a failure.
- An unawaited rejection or a throw inside a timer is printed into the output of the job that made the failed call, not fatal: the runtime, its other jobs and `state` survive.
- The runtime talks to the code server over its own pipe (fd 3). A raw write to fd 1 cannot corrupt that protocol; it goes to the code server's log, not to a job's output.
- A reply from a call that started a runtime carries `newRuntime: true`: its `state` is empty. That is the first call of each code server, so a resumed session in a new execution sees that earlier `state` is gone, and the first call after a crash or reset.
- When the runtime process dies, every running job ends with `runtime-exited`, and `state` is lost. A job with bridged calls still in flight ends when they do, with their outcomes in `calls`, so no effect goes unreported.
- When the harness closes the code server, the runtime stops and calls still in flight end with the process. A journal write is one SQLite transaction, so it is recorded or not, and the agent sees no reply, as for any lost reply. The next `code` call starts a new runtime, and its reply says so with `newRuntime: true`.

## Observability

- Each `code` and `poll` call is a harness tool call. It appears in the transcript blocks and tool metrics like any MCP tool ([harness blocks](../../go/executor/harness_blocks.go)).
- Each bridged Fleet call is a journal request, and each connection call is a gateway `CallLine` and a `tool_run` row, the same as a direct call.
- The `code` and `poll` replies carry `calls: [{tool, ms, outcome}]` for the job's bridged calls. A connection call shows as `<connection>.<tool>`.
- The launch's observed harness record carries `codeMode` and `codeTools`. With `only`, its MCP list has no removed server: Codex's `fleetMcpTools` is empty.
- Not built: one span per `code` call with child spans per bridged call.

## Coordination through code

A coordinator is an ordinary role ([orchestration](../orchestration.md)): a profile with `codeMode`, Fleet tools and instructions. No new entity or agent type exists for it.

Example (invented) of one `code` call by a coordinator:

```js
const tries = await Promise.all([1, 2, 3].map(i =>
  fleet.session_start({ requestId: `speed-${i}`, role: "worker",
    text: `Try speed-up approach ${i}; report the benchmark number.` })))
state.tries = tries
return tries
```

A later call reads each child with `fleet.execution_status({sessionId})`, compares the numbers and returns only the best one. The completion wake still queues each finished child's turn as the coordinator's next message ([test results](../test-results-T1-T16.md)).

Not built: a large prompt or conversation history as a runtime variable (RLM style). Large inputs reach the runtime today as files in the input directory or as artifacts.

## Measurement

The Lab compares variants on the same examples ([Lab](fleet-lab.md)):

1. One profile per harness without `codeMode`, with `add`, and with `only`.
2. Examples that chain several tool calls: a connection search with filtering, and a coordinator task with several workers.
3. Compared per run: input and cached tokens, cost, turn count, wall time and evaluator score.

Not built: the Lab examples and variants for this comparison.

Each harness is qualified separately. A Claude result does not qualify Codex or Antigravity.

## Verification

Run from `go/`:

```sh
go test ./executor -run CodeMode
```

The tests start a real code server with a real Node runtime, a fake gateway and an empty journal. They check persistent `state`, parallel bridged calls, ungranted calls, detach and `poll`, a tool error as `FleetCallError`, blocked writes, commands, signals to another process and network (`fetch` and the internal `_http_client` reach no server), a timer throw that keeps `state`, a 70 MB value, `poll` reset of a spinning job, a result returned once, probes that send no call, a restarted runtime, a journal error as `unknown`, and the launch servers for `add` and `only`. The runtime tests skip when the host has no such Node or no sandbox that starts. CI's hosted unit job installs `bubblewrap`, lifts Ubuntu 24.04's user-namespace restriction and sets `FLEET_REQUIRE_CODE_MODE`, so there a missing sandbox fails the tests instead of skipping them.
