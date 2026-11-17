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

The setting needs at least one bridged source: Fleet tools (`fleetTools` or `fleetOperations`) or a granted connection. Without one, the launch fails with `codeMode needs Fleet tools or a connection`. A named MCP grant cannot use the id `code`.

Native harness tools stay native. Fleet does not replace the file, edit and shell tools inside Codex, Claude or Antigravity.

## Tools

### `code`

Input: `{source: string, detachAfterSeconds?: number}`.

- `source` is the body of an async function. It may use `await`, `return` and `Promise.all`.
- The runtime keeps one global object for the whole execution. `state` is a persistent object for values that later calls read, for example `state.issues = await ...`. Variables declared with `let`, `const` or `var` live only inside one call.
- `print(...values)` and `console.log` append text to the call's output.
- The reply is `{jobId, status, output, value, calls}`. `status` is `done`, `failed` or `running`. `value` is the JSON form of the returned value, `null` for none. `output` holds the printed text. `calls` lists the bridged calls.
- A thrown error returns `isError: true`, `status: "failed"` and `error: {message, stack}`, with the output printed before it.
- The runtime does not cut `output` or `value`. The agent controls their size in its code.

`detachAfterSeconds` defaults to 50. When the code is still running at that time, `code` replies with `status: "running"` and the output so far. The code keeps running. The default stays below Codex's default MCP tool timeout of 60 seconds, so long work survives in every harness. The value is a reply time, not a limit on how long the code runs.

### `poll`

Input: `{jobId: string, waitSeconds?: number}`.

- Replies with the job's final result, in the `code` reply shape, once the job ends.
- Replies `status: "running"` with the text printed so far when `waitSeconds` (default 50) pass first.
- `poll` without `jobId` lists the jobs of this runtime with their status.

## Bridge functions

- `fleet.<tool>(args)` calls each tool of the `fleet` server for this execution: `session_start`, `session_fork`, `message_send`, `ask`, `execution_status`, `artifact_save`, the project operations, the director's operations and the others ([tool list](../../go/executor/mcp-tools.json)). The code server runs the same handler as the `fleet` server, with the same context and journal.
- `conn["<connection>"].<tool>(args)` calls a granted connection's tool through the gateway with this run's token ([call methods](fleet-connections.md#5-call-methods)).
- `fleet.tools()` and `conn["<connection>"].tools()` return the names, descriptions and input schemas. The agent looks up a tool there instead of loading every schema into its context.
- A bridge function returns the tool's result, parsed from JSON when the result is JSON. A tool error rejects the promise with `FleetCallError {tool, outcome, code, output}`.
- Calls may run in parallel. The code server serves each bridge call on its own goroutine.

## Authority

Code mode adds no authority. The code server bridges only the tools that the execution already has.

- The runtime runs with Node's permission model: `node --permission`. It reads the worktree and the execution input directory. It writes nothing, starts no child process, and loads no native addon or worker. Writes and commands go through the native harness tools under their own sandbox.
- A Claude profile whose `tools` list has none of `Read`, `Grep`, `Glob` or `Bash` gets no file reads in the runtime.
- The runtime environment has no run token, gateway URL or Access file. The code server keeps them, as the connection servers do ([per-run tokens](fleet-connections.md#4-per-run-tokens)).
- The runtime has no network. Bridged calls go through the code server, so the runtime needs none, and a harness without network (Codex without `gitNetwork`) gets none through code. Node 22's permission model has no network rule, so a module hook refuses `net`, `http`, `https`, `http2`, `tls`, `dgram`, `dns` and `inspector` for both `require` and `import`, and the runtime deletes `fetch`, `WebSocket` and `EventSource`. `--permission` already refuses `process.binding`. This is a runtime rule, not an OS boundary.
- File reads follow symlinks inside the read roots. That adds no authority: the same files are readable through the harness's own read tools, and the run file is readable by the same OS user anyway ([threat model](fleet-connections.md#4-per-run-tokens)).
- The host needs Node 22.15 or later for that module hook. Without it, the launch fails with `code-mode-unready: node ...`. Not built: Node on PDS18, which has none today.

## Unknown outcomes

Bridge calls follow the existing rule: an uncertain effect stays unknown and is never repeated automatically.

- A Fleet mutation without an answer returns `{status: "pending", requestKey}`, as it does outside code. The code reads it later with `fleet.agent_tool_result({requestKey})`. The bridge never retries it.
- A connection call with outcome `unknown` rejects with `FleetCallError` with `outcome: "unknown"`.
- When the runtime process dies, every running job ends with `runtime-exited`, and `state` is lost. The next `code` call starts a new runtime, and its reply says so: `restarted: true`.

## Observability

- Each `code` and `poll` call is a harness tool call. It appears in the transcript blocks and tool metrics like any MCP tool ([harness blocks](../../go/executor/harness_blocks.go)).
- Each bridged Fleet call is a journal request, and each connection call is a gateway `CallLine` and a `tool_run` row, the same as a direct call.
- The `code` and `poll` replies carry `calls: [{tool, ms, outcome}]` for the job's bridged calls. A connection call shows as `<connection>.<tool>`.
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

The tests start a real code server with a real Node runtime, a fake gateway and an empty journal. They check persistent `state`, parallel bridged calls, ungranted calls, detach and `poll`, a tool error as `FleetCallError`, blocked writes and commands, refused network modules, a restarted runtime, and the launch servers for `add` and `only`. The runtime tests skip when the host has no Node 22.15 or later.
