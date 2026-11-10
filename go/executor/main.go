// Command executor is the Fleet host executor: it polls the coordinator, runs harness sessions and lab scripts,
// keeps its journal in SQLite and delivers receipts and events through a durable outbox.
// Behaviour reference: crates/executor (Rust) at origin/main.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Session is one native protocol conversation (codex app-server, ACP). The executor writes Initialize() first, then feeds
// every stdout frame to Receive: it returns frames to write back, an optional event to journal, and done when the turn
// reached a terminal outcome. An error means the outcome is unconfirmed; the executor never replays.
type Session interface {
	Initialize() json.RawMessage
	Receive(frame json.RawMessage) (requests []json.RawMessage, event json.RawMessage, done bool, err error)
}

// Harness builds the native command for one agent harness (claude, codex, antigravity, ...). Part E2 implements it.
type Harness interface {
	// Name is the harness id from the payload (harness::name).
	Name() string
	// Command returns the process to start in dir, resuming native when non-empty, and bytes to write to its stdin
	// (nil: stdin is not used for the prompt).
	Command(dir, native string, payload json.RawMessage) (*exec.Cmd, []byte, error)
	// Session returns the stdin/stdout protocol for this run, or nil when the harness streams plain JSON lines.
	// servers are the MCP servers the executor wires (fleet delegation, connections).
	Session(root, native string, payload, servers json.RawMessage) Session
	// Project maps one native event to the coordinator's block projection (harness::Blocks::project).
	Project(state *BlockState, event json.RawMessage, at uint64) json.RawMessage
}

// BlockState carries projection state per execution; E2 owns its fields.
type BlockState struct{ Data map[string]any }

// Harnesses is filled by part E2 (init functions in its files) keyed by Name().
var Harnesses = map[string]Harness{}

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
}

var runMCP = func([]string) error { return fmt.Errorf("mcp: not built") }

func reproject() error { return fmt.Errorf("reproject: not yet ported") }

func runExecutor() error {
	root := os.Getenv("FLEET_DATA_DIR")
	if root == "" {
		return fmt.Errorf("FLEET_DATA_DIR required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	db, err := openJournal(filepath.Join(root, "journal.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	return fmt.Errorf("executor loop: not yet ported")
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
