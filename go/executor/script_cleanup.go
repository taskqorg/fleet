package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"fleet/contract"
)

// Only a sessionless host job owns its entire staging directory. Older commands
// without project data and commands sharing a session keep their previous behavior.
func personalScriptJob(c contract.Command) bool {
	return c.Script != nil && str0(c.Script.SessionId) == "" && c.Script.Payload != nil && (isDesignCaptureJob(c) || projectPersonal(c.Script.Payload.Project))
}

type scriptOutputs struct {
	Names            []string
	CollectionFailed bool
	Reported         bool
}

type scriptQueueError struct{ error }

func scriptExitUnconfirmed(ex *workerExit) bool {
	return ex.Status == "unknown" || ex.CleanupUnknown || ex.ProcessesUnresolved
}

func (e *executor) holdScriptStaging(commandID string, held bool) error {
	_, err := e.ex.Exec("UPDATE commands SET script_staging_held=? WHERE id=?", held, commandID)
	return err
}

func (e *executor) finishUnconfirmedScript(c contract.Command) error {
	id, err := idOf(c, "id")
	if err != nil {
		return err
	}
	return e.inTx(func() error {
		if err := e.holdScriptStaging(id, true); err != nil {
			return err
		}
		cleanup := contract.ScriptPersonalCleanup{Unconfirmed: new(true)}
		if err := e.event(c, uint64(nowMs()), contract.FleetEventTypeScriptPersonalCleanup, cleanup); err != nil {
			return err
		}
		return e.finish(c, "unknown", "")
	})
}

// Snapshot the outputs before retiring staging. Journal the close event,
// cleanup evidence and terminal receipt together. The output summary survives
// a restart between removing staging and writing those notices.
func (e *executor) finishPersonalScriptRun(execution string, l *live, ex *workerExit, closed contract.ProcessClosed) error {
	c := l.command
	dir := e.sessionDir(execution)
	result, captured, err := e.readScriptOutputs(execution)
	if err != nil || result.Reported {
		return err
	}
	status := ex.Status
	cleanup := contract.ScriptPersonalCleanup{}
	var trial *contract.TrialRemoved
	if isDesignCaptureJob(c) && (scriptExitUnconfirmed(ex) || status != "completed") {
		// A stopped/unconfirmed guest must never publish partially returned outputs.
		if scriptExitUnconfirmed(ex) {
			cleanup.Unconfirmed = new(true)
		} else {
			cleanup.Removed = e.removeScriptStaging(dir)
		}
	} else if scriptExitUnconfirmed(ex) {
		// Retain the existing best-effort upload of diagnostics and results.
		// The launch fence keeps the staging available while ownership is unknown.
		cleanup.Unconfirmed = new(true)
		result.CollectionFailed = !e.insideDirs(dir) || e.artifacts(dir, []string{"output"}, execution, &result.Names) != nil
	} else {
		if !captured {
			if isDesignCaptureJob(c) {
				// The worker exits successfully only after both private streams
				// were uploaded and verified using the existing artifact store.
				result.Names = []string{"receipt.json", "capture.tar"}
			} else {
				result, err = e.queuePersonalScriptOutputs(dir, execution)
				if err != nil {
					return err
				}
			}
			if err := saveScriptOutputs(e.db, execution, result); err != nil {
				return err
			}
		}
		if result.CollectionFailed {
			status = "failed"
		} else {
			cleanup.Removed = e.removeScriptStaging(dir)
			if isDesignCaptureJob(c) && !cleanup.Removed {
				status = "failed"
			}
		}
		if l.trial != nil {
			// A replay may find the trial already removed.
			_, err := os.Lstat(l.trial[1])
			trial = &contract.TrialRemoved{Removed: os.IsNotExist(err) || removeTrial(l.trial[0], l.trial[1])}
		}
	}
	if result.CollectionFailed {
		cleanup.CollectionFailed = new(true)
	}
	e.workerDiagnostics(dir, execution, c, ex.Seq+1)
	return e.inTx(func() error {
		if err := e.event(c, ex.Seq, contract.FleetEventTypeProcessClosed, closed); err != nil {
			return err
		}
		if trial != nil {
			if err := e.event(c, ex.Seq+2, contract.FleetEventTypeTrialRemoved, *trial); err != nil {
				return err
			}
		}
		if err := e.event(c, ex.Seq+3, contract.FleetEventTypeScriptPersonalCleanup, cleanup); err != nil {
			return err
		}
		result.Reported = true
		if err := saveScriptOutputs(e.ex, execution, result); err != nil {
			return err
		}
		if err := e.holdScriptStaging(l.commandID(), scriptExitUnconfirmed(ex) || result.CollectionFailed); err != nil {
			return err
		}
		e.declared = result.Names
		return e.finish(c, status, l.native)
	})
}

func (e *executor) readScriptOutputs(execution string) (scriptOutputs, bool, error) {
	var result scriptOutputs
	var saved sql.NullString
	if err := e.db.QueryRow("SELECT script_outputs FROM launches WHERE execution=?", execution).Scan(&saved); err != nil {
		return result, false, err
	}
	if !saved.Valid {
		return result, false, nil
	}
	err := json.Unmarshal([]byte(saved.String), &result)
	return result, true, err
}

func saveScriptOutputs(db execer, execution string, result scriptOutputs) error {
	_, err := db.Exec("UPDATE launches SET script_outputs=? WHERE execution=?", enc(result), execution)
	return err
}

func (e *executor) queuePersonalScriptOutputs(dir, execution string) (scriptOutputs, error) {
	var result scriptOutputs
	if !e.insideDirs(dir) {
		result.CollectionFailed = true
		return result, nil
	}
	rows, err := e.db.Query(`SELECT json_extract(body,'$.name') FROM transfers
		WHERE kind='upload' AND json_extract(body,'$.scriptOutput')=1 AND json_extract(body,'$.executionId')=?
		ORDER BY rowid`, execution)
	if err != nil {
		return result, err
	}
	queued := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return result, err
		}
		queued[name] = true
		result.Names = append(result.Names, name)
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	err = e.collectArtifacts(dir, []string{"output"}, execution, &result.Names, queued)
	var queueErr scriptQueueError
	if errors.As(err, &queueErr) {
		// Keep staging if the journal could not accept an upload. On restart
		// the same confirmed exit resumes collection from the durable jobs.
		return result, queueErr
	}
	result.CollectionFailed = err != nil
	return result, nil
}

// Persist the spool file and its directory entry before a transfer can claim it.
func syncScriptOutput(out *os.File) error {
	if err := out.Sync(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(out.Name()))
	if err != nil {
		return err
	}
	defer dir.Close()
	return unix.Fsync(int(dir.Fd()))
}

func (e *executor) removeScriptStaging(dir string) bool {
	if !e.insideDirs(dir) {
		return false
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false
	}
	defer root.Close()
	removed := true
	for _, name := range []string{"input", "output"} {
		if root.RemoveAll(name) == nil {
			continue
		}
		// WalkDir stats its root, which would follow a link. Chmod only
		// real staging directories when retrying read-only cleanup.
		info, err := root.Lstat(name)
		if err != nil || !info.IsDir() {
			removed = false
			continue
		}
		_ = fs.WalkDir(root.FS(), name, func(path string, entry fs.DirEntry, err error) error {
			if entry != nil && entry.IsDir() {
				_ = root.Chmod(path, 0o700)
			}
			return nil
		})
		if root.RemoveAll(name) != nil {
			removed = false
		}
	}
	return removed
}
