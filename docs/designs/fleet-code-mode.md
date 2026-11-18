# Fleet code mode

Code mode gives an agent one tool that runs JavaScript. Inside that code, every Fleet tool, every granted connection and the agent's own file and shell tools are functions. The agent chains, loops over, filters and parallelizes calls in code, and only the value the code returns enters its context. The tool is an executor MCP server, so it works the same way in every harness Fleet runs: `codex`, `claude`, `antigravity` and `acp`.

## Purpose

- **Less context, fewer turns.** With separate tools, each call result passes through the model's context, and the model rereads it on every later turn. In code, intermediate results stay in variables.
- **Whole turns in one call.** A turn's reads, searches, commands, edits and tool calls run in one script. The agent pays one round trip instead of one per step, and the native tools it no longer needs are hidden.
- **Orchestration in code.** A coordinator starts workers, waits for them, compares their results and returns a short summary. Its context holds the summary, not every worker's transcript.
- **One mechanism for every harness.** Claude Code has its own Code Mode (`CLAUDE_CODE_EXPERIMENTAL_CODE_MODE`). Claude Code 2.1.319 refuses it outside the interactive terminal ("for now it only works in the interactive terminal"), and Fleet runs Claude headless, so Fleet cannot use it. The other harnesses have no equivalent.

Background: Cloudflare's "Code Mode" article and server-side `search` and `execute` tools; Claude Code's client-side `REPL` and `poll` tools, where every native tool (`Read`, `Write`, `Edit`, `Glob`, `Grep`, a shell) is a function in the script and the direct tools are hidden; recursive language models (RLM), where the REPL persists, starts sub-agents and holds large inputs as variables.

Measured before the host functions existed ([#642](https://github.com/taskqorg/fleet/issues/642)): Claude research runs with `only` cost 24–40% less; Claude worker runs, which also read and write files and run commands, cost 4–52% more. Their traces alternate `code` and native `Bash`, because the runtime could neither write files nor run commands.

## Terms

- **Code server:** `fleet-executor mcp code`, the MCP server that serves the `code` and `poll` tools for one execution.
- **Runtime:** the Node process that the code server starts for the execution. It keeps its global state between `code` calls.
- **Bridge function:** a function in the runtime that sends one tool call to the code server and returns its result.
- **Host functions:** `read`, `glob`, `grep`, `write`, `edit` and `sh`: the agent's file and shell tools inside the runtime.

## Profile setting

An agent profile selects code mode with `codeMode` ([profile schema](../../contracts/profile.schema.json)):

| Value | Effect |
|---|---|
| absent | No code server. |
| `add` | The code server is added. The `fleet` server and the connection servers stay, so the agent can call each tool directly or through code. |
| `only` | The code server replaces the `fleet` server and the connection servers, and the native tools that host functions cover are hidden where the harness allows it ([hidden native tools](#hidden-native-tools)). Fleet tools and connections are reachable only through `code`. |

The setting needs at least one bridged source: Fleet tools (`fleetTools`, on by default) or a granted connection. `ParseProfile` refuses `codeMode` with `fleetTools: false` and no connections. When access filtering leaves no connection at launch, the launch fails with `codeMode needs Fleet tools or a connection`. A named MCP grant and a connection cannot use the name `code`.

The executor passes the code server's spec in a 0600 file beside the run file (`runs/<execution>.code.json`), not in argv, because Fleet operations and connection schemas can outgrow one argument. The file is removed with the run file.

### Hidden native tools

With `only`, the harness offers no native tool that a host function covers:

| Harness | Hidden | Stays |
|---|---|---|
| `claude` | `Bash`, `Write`, `Edit`, `Glob` and `Grep` leave `--tools` and `--allowedTools`. | `Read`, the only way to see an attached image or PDF; `Task`, `TaskStop`, `Skill`, web tools. |
| `codex` | The shell: `--disable shell_tool --disable unified_exec` (checked with Codex 0.167). | `apply_patch`: Codex has no switch for it. |
| `antigravity`, `acp` | Nothing: the ACP launch has no tool switch. | Every native tool. |

With `add`, every native tool stays.

## Tools

### `code`

Input: `{source: string, detachAfterSeconds?: number}`.

- `source` is the body of an async function. It may use `await`, `return` and `Promise.all`.
- Without an explicit `return`, the last expression statement is the result. The runtime returns the shortest tail of the source, after a newline or `;`, that compiles as one expression after a prefix that compiles on its own. A bare `return` across a line break keeps JavaScript's usual behavior. A line that starts with a binary operator or a dot continues the line before it, so it is never split off; `!` alone starts a new one. A `;` inside a trailing line comment is no split point, a trailing function or class declaration is never the result, so it stays hoisted, and a trailing assignment (`state.x = ...`) is not either, so a value kept for later calls stays out of the reply. A last line that starts with `(`, `[` or a template is the result, unlike plain JavaScript, which would call or index the line before it: `({a, b})` on the last line returns the object.
- The runtime keeps one global object for the whole execution. `state` is a persistent object for values that later calls read, for example `state.issues = await ...`. Variables declared with `let`, `const` or `var` live only inside one call.
- `print(...values)` and `console.log` append a line to the call's output; `process.stdout.write` appends its text as is.
- The runtime does not cut the output or the value. The agent controls their size in its code.

The tool description follows Claude Code's own rules: few, full scripts; batch all operations of a step into one call; `Promise.all` for parallel work; the last expression is the result; `fleet.$tools()` only to look up a name or schema. Its example reads several files, greps, calls a connection and returns a compact object. It names only the host functions this execution has.

#### Reply

The reply is MCP text parts, so the agent reads the value itself, not an escaped JSON string:

1. The printed output, when there is any.
2. For a finished job, the value: a nonempty string as is, the empty string as `""`, any other value as compact JSON, and `null` for none.
3. A part that starts with `[code] ` and a JSON object, only when it carries something: `status` and `jobId` while the job runs; `status: "failed"` and `error: {message, stack}` for a failed job; `newRuntime: true`; `calls` when a bridged call did not end `ok` or the job failed. A successful final reply omits a batch of all-successful calls. A running reply includes completed call outcomes so a later failure has no hidden effects. Running and failed job replies also carry metadata as MCP `structuredContent`. A completed job delivers its value and any metadata as text parts so a client that prefers `structuredContent` still sees its value. Programmatic clients can identify completed-job metadata under `_meta["fleet/code"]`.

A failed job replies `isError: true`. Replies that are not a job's result (the `poll` listing, `reset`, an unknown `jobId`, a missing `source`) are one `[code] ` part.

`detachAfterSeconds` defaults to 25. When the code is still running at that time, `code` replies with `status: "running"` and the output so far. The code keeps running. The executor's MCP filter forwards one request at a time, so a second call waits for the first reply; with the 25 s default, two calls in a row still answer within Codex's default MCP tool timeout of 60 seconds; a third parallel call can miss it. A value above 50 counts as 50: a longer reply wait would lose the reply in Codex and hold every later call, `poll` reset included. The value is a reply time, not a limit on how long the code runs.

### `poll`

Input: `{jobId: string, waitSeconds?: number}`.

- Replies with the job's final result, in the [`code` reply](#reply) shape, once the job ends.
- Replies `status: "running"` with the text printed so far when `waitSeconds` (default 25, at most 50; 0 answers at once) pass first.
- Each reply for a job carries only the output and `calls` added since that job's previous reply.
- A final result is returned once. The code server then drops the job, so it keeps no delivered output or value, and a later `poll` of that `jobId` answers `unknown jobId`. An undelivered job keeps its output until it is polled or the execution ends.
- `poll` without `jobId` lists the unfinished and undelivered jobs with their status.
- `poll {reset: true}` stops the runtime, for code that never yields (`while (true) {}`), and kills running `sh` commands. Every running job fails with `runtime-exited` once its bridged calls in flight end, and `state` is lost.

## Bridge functions

- `fleet.<tool>(args)` calls each tool of the `fleet` server for this execution: `session_start`, `session_fork`, `message_send`, `ask`, `execution_status`, `artifact_save`, the project operations, the director's operations and the others ([tool list](../../go/executor/mcp-tools.json)). The code server runs the same handler as the `fleet` server, with the same context and journal.
- `conn["<connection>"].<tool>(args)` calls a granted connection's tool through the gateway with this run's token ([call methods](fleet-connections.md#5-call-methods)). The bridge offers the same tools as the direct connection server: names of letters, digits and `_`.
- With `only`, the code server's MCP `instructions` carry each granted connection's instructions, which the removed connection servers gave. With `add` the direct servers still give them, so they are not repeated.
- `fleet.$tools()` and `conn["<connection>"].$tools()` return the names, descriptions and input schemas. The agent looks up a tool there instead of loading every schema into its context.
- A bridge function returns the tool's result, parsed from JSON when the result is JSON. A tool error rejects the promise with `FleetCallError {tool, outcome, code, output}`.
- Calls may run in parallel. The code server serves each bridge call on its own goroutine.
- `fleet` and `conn` hold one function per granted name, sent by the code server when the runtime starts. A name that is not granted, a typo or a probe by a built-in (`await` reads `then`, `JSON.stringify` reads `toJSON`) is `undefined` and sends no call. The code server refuses ungranted names as well.

## Host functions

The runtime offers the agent's own file and shell tools as functions, with exactly the authority those tools have in this execution, never more. A function the execution lacks is `undefined`. Relative paths start at the worktree.

| Function | Result | Runs in |
|---|---|---|
| `read(path)` | The file's text. | The runtime, within its read roots. |
| `glob(pattern, {path})` | Matching paths, for example `glob("**/*.go")`. | The runtime. |
| `grep(pattern, {path, glob, ignoreCase})` | `[{file, line, text}]` for each matching line. `pattern` is a regular expression or its source. | The runtime. |
| `write(path, text)` | `{path, bytes}`. Missing directories are created. | The code server, inside the worktree. |
| `edit(path, old, new, {all})` | `{path, replaced}`. `old` must occur once, or `all: true` replaces every occurrence. | The code server, inside the worktree. |
| `` sh`cmd` `` or `sh(cmd, {cwd, timeoutMs, args})` | `{code, stdout, stderr}`, with `.text()`, `.json()` and `.lines()` over `stdout`. `signal` is set when a signal ended the command, `timedOut: true` when `timeoutMs` passed. | The code server, in the shell sandbox. |

- `glob` and `grep` walk the tree asynchronously without following symlinks and skip `.git` and `node_modules`; they do not read `.gitignore`. An explicit starting path can itself be a symlink within the read roots. A `grep` glob without a slash matches file names at any depth, as ripgrep's; globs match dot files too. An unreadable directory is skipped. `grep` reads no further in a file with a NUL byte in its first 8 KiB. Returned paths are relative to the worktree when they are inside it.
- `` sh`...` `` accepts a literal command. Pass dynamic values as an array of strings in `args` and reference them in the command as quoted positional parameters such as `"$1"`. A non-zero exit does not throw: the script reads `code`.
- `sh` runs `bash -c` (`/bin/sh` without bash) in the worktree, or in `cwd`, with stdin from `/dev/null` and the code server's environment without `FLEET_RUN_TOKEN`, `FLEET_GATEWAY_URL`, `FLEET_CF_ACCESS_FILE` and `FLEET_MCP_*`, plus `FLEET_INPUT_DIR`, `FLEET_OUTPUT_DIR`, `USER` and `LOGNAME` as the harness has them. The approved execution Git settings reach the shell. It has no default timeout. A guardian kills the shell group if the code server loses its lifetime pipe. `poll {reset: true}` and the end of the code server kill running commands, and a reset or dead runtime starts no new one. When `waitid` observes exit before reap, the server kills the process group while the leader still reserves its id. If that observation is unavailable, it manages the leader only.
- `sh` returns captured stdout and stderr to the script. An output pipe that stays open for a second after the command exits is closed. Only what the script returns or prints reaches the reply.
- `write` and `edit` run one at a time, so parallel edits of one file all land. They follow symlinks inside the worktree, as the native `Write` tool does, then walk the path one directory handle at a time with Go's `os.Root`, check the publication path from the worktree again before rename, refuse a component that became a symlink meanwhile, and compare each directory and the file with the worktree's `.git` by file identity. The new contents go to a temporary file renamed over the name: no reader sees a half-written file, a failed write keeps the old contents and the mode, and a hard link keeps its contents. A path outside the worktree, through a symlink out of it, or at or under any `.git` (a nested repository's too, in any case, through a symlink or a concurrent rename) fails with `FleetCallError` `code: "outside-worktree"`. `edit` fails with `not-found` when `old` does not occur and with `not-unique` when it occurs more than once without `all`.
- `sh`, `write` and `edit` are bridged calls: they belong to their job like any other call, and `calls` names them `sh`, `write` and `edit`. `read`, `glob` and `grep` send no call.

### Authority per harness

The host functions mirror what the harness's own tools may do in this execution:

| Harness | `read`, `glob`, `grep` | `write`, `edit` | `sh` |
|---|---|---|---|
| `codex` | Read roots. | Yes. | Sandboxed. Writes: the worktree, the profile's writable roots (with `gitNetwork`, the checkout's Git directories) and the temporary directories. Network only with `gitNetwork`. |
| `claude` | With `Read`, `Grep`, `Glob` or `Bash` in `tools`. | `write` with `Write`, `edit` with `Edit`. | With `Bash`, without a sandbox, because Fleet runs Claude's own `Bash` without one (`--permission-mode dontAsk`). |
| `antigravity`, `acp` | Read roots. | Unavailable. | Unavailable. |

A read-only start (`readOnly`, [conversations](fleet-conversations.md)) narrows the grants: Codex gets only the read functions and sandboxed `sh` with no writes or network. Claude keeps `read`, `glob` and `grep` within its read roots. Antigravity and other ACP agents use the same read functions in both modes, consistent with Fleet's denial of ACP client file and terminal operations. Antigravity may request one-time permission for the execution's injected Code Mode MCP tools during its active turn.

The shell sandbox reads files as Codex's sandbox does and reaches no host Unix socket:

- macOS: `sandbox-exec` with a deny-default profile, as Codex's seatbelt policy. It allows processes, file reads, writes under the write roots and to `/dev/null`, `/dev/zero`, `/dev/tty*` and `/dev/fd/*`, signals inside the same sandbox, and only the Mach services ordinary commands need: user and group lookup, notifications, logging, `sysmond`, `dirhelper` and `securityd`. No other service is reachable, so no launchd job (`launchctl submit`) or app runs outside the sandbox for a command. The executor's data directory (run files, MCP sockets) is unreadable, except the read and write roots, a harness `HOME` inside it and the worktree's Git directories. The executor finds those when it prepares the launch, from the checkout it made; what `.git` files say later grants nothing, since the sandboxed commands can rewrite them. With network it adds IP traffic, the DNS socket (`/private/var/run/mDNSResponder`) and the DNS, network configuration and certificate services; no other Unix socket.
- Linux: `bwrap --ro-bind / /` with its own `/dev` and `/proc`; empty tmpfs over `/run`, `/var/run`, `XDG_RUNTIME_DIR` and the executor's data directory; a seccomp filter blocks Unix socket creation across all paths and blocks io_uring; the read roots, the worktree's Git directories and the write roots bound back from paths pinned at preparation; the private harness home stays readable; `--unshare-pid --unshare-ipc --die-with-parent --new-session`; and `--unshare-net` without network. With network, a resolver file under `/run` is bound back read-only.
- A write root's `.git` stays read-only unless another write root lies inside it, as in Codex. On macOS the rule matches `.git` in any case, since the file system ignores case.
- The temporary directories are `TMPDIR` and, on macOS, `/private/tmp`. On Linux `/tmp` is a private directory of the code server, and `TMPDIR` is `/tmp`, the same for every command of the execution and removed with the code server; a read-only start gets an empty read-only `/tmp`.

## Authority

Code mode adds no authority. The code server bridges only the tools that the execution already has.

- The runtime runs with Node's permission model: `node --permission`. It reads the worktree and the execution input directory. It writes nothing, starts no child process, and loads no native addon or worker. `write`, `edit` and `sh` are bridged to the code server, which checks each one against the execution's grant ([host functions](#host-functions)).
- A Claude profile whose `tools` list has none of `Read`, `Grep`, `Glob` or `Bash` gets no file reads in the runtime, and the `code` tool description says so.
- The runtime environment has no run token, gateway URL or Access file. The code server keeps them, as the connection servers do ([per-run tokens](fleet-connections.md#4-per-run-tokens)). `sh` commands get the code server's environment without them, and the sandboxed shell cannot read the run files in the executor's data directory.
- The runtime has no network. Bridged calls go through the code server, so the runtime needs none, and a harness without network (Codex without `gitNetwork`) gets none through code. Node 22's permission model has no network rule, so the runtime runs inside an OS sandbox. Its stdin and its protocol pipe (fd 3) to the code server still work.
  - macOS: `sandbox-exec` with `(deny network*)`, which also refuses Unix sockets, plus `(deny file-write*)` except `/dev/null`, `(deny signal)` except to itself, and `(deny process-exec)` except Node itself and `(deny process-fork)`. Mach services stay reachable.
  - Linux: `bwrap --unshare-net --unshare-ipc --unshare-pid`. The sandbox sees only the system directories, the Node binary itself and the read roots, all read-only, with its own `/tmp`, `/proc` and `/dev`. A Node installed under the home directory therefore exposes no other file of it. Host Unix sockets outside the read roots are unreachable; a socket file inside a read root stays reachable, as it is for the harness's own tools there. Other processes are invisible to signals.
- The OS sandbox, not only Node's permission model, refuses writes and signals: Node documents its model as a seat belt against mistakes, not a boundary against hostile code.
- File reads follow symlinks inside the read roots. That adds no authority: the same files are readable through the harness's own read tools, and the run file is readable by the same OS user anyway ([threat model](fleet-connections.md#4-per-run-tokens)).
- The host needs a Node with `--permission` (22.13 or later in 22, 23.5 or later in 23, any later major) and a sandbox that starts. The executor runs the sandbox to check it, with a 30 s bound. It keeps a ready result while its files exist and a failure for one minute; after that a launch starts a new probe in the background and still gets the old failure, so a host made ready later works from the next launch, without an executor restart. Without them, the launch fails with `code-mode-unready: <reason>`, for example on Linux without user namespaces. Hosts without such a sandbox, such as Windows, cannot use code mode.
- Not built: Node on PDS18, which has none today; `bwrap --unshare-net` works there for the `fleet-agent` user.
- Not built: code mode readiness as a host capability for placement. Today a host that is not ready fails the launch with `code-mode-unready` instead of being skipped.

## Unknown outcomes

Bridge calls follow the existing rule: an uncertain effect stays unknown and is never repeated automatically.

- A Fleet mutation without an answer rejects with `FleetCallError` with `outcome: "unknown"`, `code: "pending"` and `output: {status: "pending", requestKey}`. A `request_child` without a host answer (`status: "pending-or-rejected"` and no `response`) rejects the same way; a retry with the same `requestId` is the same request. A host refusal rejects with `outcome: "error"`. The code reads it later with `fleet.agent_tool_result({requestKey})`. The bridge never retries it.
- A bridged call started before the code returns is part of its job, awaited or not: the final reply waits for it and lists its outcome in `calls`. So is a call chained off one of them (`fleet.a().then(fleet.b)`): a call counts as in flight until the runtime has run the callbacks of its reply.
- A bridged call made after the code returned and its calls ended (a timer) is refused with `code: "job-ended"` and never sent: its effect would reach no reply.
- Arguments that do not serialize (a `BigInt`, a cycle) reject with `code: "bad-arguments"`.
- A connection call with outcome `unknown` rejects with `FleetCallError` with `outcome: "unknown"`.
- A journal failure on a Fleet call rejects with `outcome: "unknown"` and `code: "journal-error"`: the request may already be journaled and sent. A stored reply that does not decode is such a failure.
- An unawaited rejection or a throw inside a timer is not fatal: the runtime, its other jobs and `state` survive. The text goes into the output of the job that made the failed call while that job runs, and is dropped after; the call's outcome is always in `calls`.
- The runtime's own code is one closure: user code sees only `fleet`, `conn`, `state`, `print`, `require`, `FleetCallError` and the granted host functions, so ordinary code cannot reuse call ids by mistake. All jobs of one runtime are one agent's code in one trust domain: code that writes to the protocol pipe (fd 3) or stdin can disturb only its own jobs, never another execution or the bridged calls' authority. A protocol line that no longer decodes is reported in every running job's output.
- The runtime talks to the code server over its own pipe (fd 3). A raw write to fd 1 cannot corrupt that protocol; it is dropped, not added to a job's output.
- A reply from a call that started a runtime carries `newRuntime: true`: its `state` is empty. That is the first call of each code server, so a resumed session in a new execution sees that earlier `state` is gone, and the first call after a crash or reset.
- When the runtime process dies, every running job ends with `runtime-exited`, with `stderrTail`, the last 4 KiB of the runtime's stderr (for example a sandbox that cannot start), and `state` is lost. A job with bridged calls still in flight ends when they do, with their outcomes in `calls`, so no effect goes unreported. The next `code` call starts a new runtime.
- When the harness closes the code server, the runtime stops, every job ends at once, and calls still in flight end with the process. A journal write is one SQLite transaction, so it is recorded or not, and the agent sees no reply, as for any lost reply.

## Observability

- Each `code` and `poll` call is a harness tool call. It appears in the transcript blocks and tool metrics like any MCP tool ([harness blocks](../../go/executor/harness_blocks.go)).
- Each bridged Fleet call is a journal request, and each connection call is a gateway `CallLine` and a `tool_run` row, the same as a direct call.
- The `code` and `poll` replies carry `calls: [{tool, ms, outcome}]` for the job's bridged calls when one of them did not end `ok` or the job failed. A connection call shows as `<connection>.<tool>`.
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

The tests start a real code server with a real Node runtime, a fake gateway and an empty journal. They check the reply parts and the last expression as the result; `sh` in the worktree, refused writes outside its write roots and no network without `gitNetwork`; `write` and `edit` inside and outside the worktree; no `sh`, `write` or `edit` for a Claude profile without `Bash`, `Write` or `Edit`; the hidden native tools for `only`; persistent `state`, parallel bridged calls, ungranted calls, detach and `poll`, a tool error as `FleetCallError`, reads inside a read root and refused reads outside it, blocked writes, commands, signals to another process and network (`fetch` and the internal `_http_client` reach no server), a timer throw that keeps `state`, a 70 MB value, `poll` reset of a spinning job, a result returned once, probes that send no call, a restarted runtime, a journal error as `unknown`, and the launch servers for `add` and `only`. The runtime tests skip when the host has no such Node or no sandbox that starts. CI's hosted unit job installs `bubblewrap`, lifts Ubuntu 24.04's user-namespace restriction and sets `FLEET_REQUIRE_CODE_MODE`, so there a missing sandbox fails the tests instead of skipping them.
