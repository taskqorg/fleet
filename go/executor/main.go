// Command executor is the Fleet host executor: it polls the coordinator, runs harness sessions and lab scripts,
// keeps its journal in SQLite and delivers receipts and events through a durable outbox.
// Behaviour reference: crates/executor (Rust) at origin/main.
package main

//go:generate go run ../gen ../../contracts/host-protocol.schema.json main protocol_gen.go

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"

	_ "modernc.org/sqlite"
)

// JSON values are decoded with encoding/json into any (map[string]any, []any, string, float64, bool, nil), the Go form of
// serde_json::Value in the Rust executor.

// Session is one native stdin/stdout protocol conversation (codex app-server, ACP). The executor writes Initialize()
// first (through writeFrame, which journals the bytes before the pipe write), then feeds every stdout line to Receive: it
// returns frames to write back, an optional event to journal, and done when the turn reached a terminal outcome (ACP).
// An error means the outcome is unconfirmed; the executor never replays.
type Session interface {
	Initialize() map[string]any
	Receive(frame map[string]any) (requests []map[string]any, event map[string]any, done bool, err error)
	// Steer returns the frame for a `steer` command, or nil when this session cannot steer now (codex: turn/steer with
	// expectedTurnId; other harnesses return nil). The answer arrives later as an event {type: "steer.result", commandId, ok}.
	Steer(text, commandID string) map[string]any
}

// Harness is one agent harness (codex, claude, antigravity, acp). Part E2
// implements it and registers each in Harnesses from init(). Order of calls in run():
//
//	cmd, stdin := h.Command(dir, native, p)   // codex without account: the exec form
//	... the executor appends env, cwd, profile flags, codex -c/--disable flags ...
//	h.WireMCP(cmd, p, servers, extra)         // claude: --mcp-config/--allowedTools; codex: -c mcp_servers.* flags; ACP: no-op
//	cmd, s := h.Protocol(cmd, root, native, p, servers) // codex: codex::command (app-server, keeps -c/--enable/--disable) + Session
//	                                          // (fork from p.fork.nativeSessionId when native == ""); ACP: cmd + Session; claude: cmd, nil
//	every stdout event: h.Normalize(event)    // claude rate_limit_event -> limit_reading; others: unchanged
type Harness interface {
	Command(dir, native string, p map[string]any) (cmd *exec.Cmd, stdin []byte, err error)
	// servers: name -> {"command": string, "args": []any, "tools": []string (codex enabled_tools; nil = all), "env": []string
	// (codex env_vars)}. "fleet" is the executor's own delegation server; others are profile and connection servers.
	WireMCP(cmd *exec.Cmd, p map[string]any, servers map[string]any, extraTools []string)
	Protocol(cmd *exec.Cmd, root, native string, p map[string]any, servers map[string]any) (*exec.Cmd, Session)
	Normalize(event map[string]any) map[string]any
}

// Blocks is the transcript block state of one execution (harness::Blocks): Project returns the normalized projection
// (session, completed, blocks, ...) of one event; at is the record time in ms.
type Blocks interface {
	Project(harness string, event map[string]any, at uint64) map[string]any
}

// Filled by part E2: Harnesses keyed by harness name ("codex", "claude", "antigravity", "acp"), NewBlocks, and runMCP
// (the `mcp` subcommand).
var (
	Harnesses = map[string]Harness{}
	NewBlocks func() Blocks
	runMCP    = func([]string) error { return fmt.Errorf("mcp: not built") }
)

// openJournal opens the journal with the Rust executor's file format and pragmas.
func openJournal(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS commands(id TEXT PRIMARY KEY,body TEXT NOT NULL,terminal INTEGER DEFAULT 0);
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,native TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS delegation_requests(id TEXT PRIMARY KEY,body TEXT NOT NULL,response TEXT);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// subcommands: argv[1] -> handler. No argument runs the executor loop.
var subcommands = map[string]func(args []string) error{
	"mcp":       func(args []string) error { return runMCP(args) }, // part E2
	"reproject": func([]string) error { return reproject() },
	"selftest":  selftest,
}

func main() {
	run := runExecutor
	if len(os.Args) > 1 {
		h, ok := subcommands[os.Args[1]]
		if !ok {
			fmt.Fprintf(os.Stderr, "executor: unknown subcommand %q\n", os.Args[1])
			os.Exit(2)
		}
		run = func() error { return h(os.Args[2:]) }
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "executor:", err)
		os.Exit(1)
	}
}
