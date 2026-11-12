// Command executor is the Fleet host executor: it polls the coordinator, runs harness sessions and lab scripts,
// keeps its journal in SQLite and delivers receipts and events through a durable outbox.
// Behaviour reference: crates/executor (Rust) at origin/main.
package main

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

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

//go:embed journal.sql
var journalSQL string

// openJournal opens the journal with the Rust executor's file format and pragmas and applies the journal.sql sections
// above its user_version. A behind journal is migrated under BEGIN IMMEDIATE, the version read again inside the lock, so
// two processes opening one journal never run a section twice.
func openJournal(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrateJournal(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// journalSections: journal.sql split at its "-- version N" lines, numbers strictly increasing from 1; a header written any
// other way is an error, never silently part of the previous section.
func journalSections() ([]string, error) {
	var out []string
	for _, line := range strings.Split(journalSQL, "\n") {
		if n, ok := strings.CutPrefix(line, "-- version "); ok {
			if v, err := strconv.Atoi(n); err != nil || v != len(out)+1 {
				return nil, fmt.Errorf("journal.sql: header %q, want version %d", line, len(out)+1)
			}
			out = append(out, "")
			continue
		}
		if l := strings.ToLower(strings.TrimSpace(line)); strings.HasPrefix(l, "--") && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(l, "--")), "version") {
			return nil, fmt.Errorf("journal.sql: malformed header %q", line)
		}
		if len(out) > 0 {
			out[len(out)-1] += line + "\n"
		}
	}
	return out, nil
}

func migrateJournal(db *sql.DB) (err error) {
	sections, err := journalSections()
	if err != nil {
		return err
	}
	var have int
	if err := db.QueryRow("PRAGMA user_version").Scan(&have); err != nil {
		return err
	}
	if have >= len(sections) {
		return nil // current: no write lock
	}
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			db.Exec("ROLLBACK")
		}
	}()
	if err := db.QueryRow("PRAGMA user_version").Scan(&have); err != nil {
		return err
	}
	for i := have; i < len(sections); i++ {
		if _, err := db.Exec(sections[i]); err != nil {
			return fmt.Errorf("journal version %d: %w", i+1, err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			return err
		}
	}
	_, err = db.Exec("COMMIT")
	return err
}

// subcommands: argv[1] -> handler. No argument runs the executor loop.
var subcommands = map[string]func(args []string) error{
	"mcp":          func(args []string) error { return runMCP(args) }, // part E2
	"qualify-mcp":  qualifyMCP,
	"mcp-guardian": mcpGuardian,
	"reproject":    func([]string) error { return reproject() },
	"selftest":     selftest,
}

func main() {
	run := runExecutor
	if mcpDependencyInvocation() {
		run = mcpOwnedCommand
	} else if len(os.Args) > 1 {
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

// Disposable delivery timing path control; never merge.
