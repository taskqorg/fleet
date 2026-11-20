package main

// Replies between processes (docs/designs/fleet-standard.md S7): an MCP server that journaled a request (a delegation,
// agent tool or project request, an artifact transfer) waits for the executor to settle it. The waiter makes a FIFO
// under <journal dir>/wake named by the request key before it reads the journal; the process that writes the reply
// signals every FIFO of that key. The waiter reads the journal again on each signal and once at its deadline: no
// polling, and a lost signal costs only latency, never the reply.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"fleet/errtrack"
)

// journalDir: the folder of db's main file; "" for an in-memory journal.
func journalDir(db *sql.DB) string {
	var file string
	if db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&file) != nil || file == "" {
		return ""
	}
	return filepath.Dir(file)
}

func wakeName(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:10])
}

var wakeSeq atomic.Int64

// waiter: one FIFO a process waits on. A nil waiter (no journal folder, no FIFO) waits until the deadline. A reader
// goroutine turns each signal into sig: a FIFO is not pollable on macOS, so its read blocks a thread instead of taking
// a deadline; close unblocks it by writing to the FIFO itself.
type waiter struct {
	f      *os.File
	path   string
	sig    chan struct{}
	closed atomic.Bool
}

// listen makes the FIFO of key under a temporary name and renames it into place once it is open: a signal never finds
// it unopened. It is opened read-write, so a read blocks until a signal instead of seeing the end of a FIFO without
// writers. FIFOs of processes that are gone are removed.
func listen(db *sql.DB, key string) *waiter {
	dir := journalDir(db)
	if dir == "" {
		return nil
	}
	dir = filepath.Join(dir, "wake")
	if os.MkdirAll(dir, 0o700) != nil {
		return nil
	}
	sweepWake(dir)
	name := fmt.Sprintf("%s.%d.%d", wakeName(key), os.Getpid(), wakeSeq.Add(1))
	tmp, path := filepath.Join(dir, "tmp-"+name), filepath.Join(dir, name)
	if syscall.Mkfifo(tmp, 0o600) != nil {
		return nil
	}
	f, err := os.OpenFile(tmp, os.O_RDWR, 0)
	if err != nil {
		os.Remove(tmp)
		return nil
	}
	if os.Rename(tmp, path) != nil {
		f.Close()
		os.Remove(tmp)
		return nil
	}
	w := &waiter{f: f, path: path, sig: make(chan struct{}, 1)}
	errtrack.Go("wake.read", func() {
		defer f.Close()
		var b [64]byte
		for {
			if _, err := f.Read(b[:]); err != nil || w.closed.Load() {
				return
			}
			select {
			case w.sig <- struct{}{}:
			default:
			}
		}
	})
	return w
}

// sweepWake removes the FIFOs of processes that no longer run (a waiter killed before its close).
func sweepWake(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		var hash string
		var pid, seq int
		n := strings.TrimPrefix(e.Name(), "tmp-")
		if _, err := fmt.Sscanf(strings.ReplaceAll(n, ".", " "), "%s %d %d", &hash, &pid, &seq); err == nil && pid > 0 &&
			errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// wait blocks until a signal (true) or until (false).
func (w *waiter) wait(until time.Time) bool {
	if w == nil {
		time.Sleep(time.Until(until))
		return false
	}
	t := time.NewTimer(time.Until(until))
	defer t.Stop()
	select {
	case <-w.sig:
		return true
	case <-t.C:
		return false
	}
}

func (w *waiter) close() {
	if w != nil && !w.closed.Swap(true) {
		os.Remove(w.path)
		w.f.Write([]byte{0}) // the reader returns and closes the FIFO
	}
}

// wake signals every process waiting for key's reply. A FIFO nobody holds open (its waiter died) is removed.
func wake(db *sql.DB, key string) {
	dir := journalDir(db)
	if dir == "" {
		return
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "wake", wakeName(key)+".*"))
	for _, p := range paths {
		f, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			if err.(*os.PathError).Err == syscall.ENXIO {
				os.Remove(p)
			}
			continue
		}
		f.Write([]byte{1})
		f.Close()
	}
}
