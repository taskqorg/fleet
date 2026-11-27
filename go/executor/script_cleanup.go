package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"fleet/contract"
)

// Only a sessionless host job owns its entire staging directory. Older commands
// without project data and commands sharing a session keep their previous behavior.
func personalScriptJob(c contract.Command) bool {
	return c.Script != nil && str0(c.Script.SessionId) == "" && c.Script.Payload != nil && projectPersonal(c.Script.Payload.Project)
}

type scriptOutputs struct {
	Names            []string
	CollectionFailed bool
}

type scriptQueueError struct{ error }

// For a confirmed exit, snapshot the outputs before removing staging. Journal
// the close event, cleanup evidence and terminal receipt together so a restart
// can retry the cleanup without repeating notices or dropping declared outputs.
func (e *executor) finishPersonalScriptRun(execution string, l *live, ex *workerExit, closed contract.ProcessClosed) error {
	c := l.command
	var terminal bool
	if err := e.db.QueryRow("SELECT terminal FROM commands WHERE id=?", l.commandID()).Scan(&terminal); err != nil {
		return err
	}
	if terminal {
		return nil
	}
	dir := e.sessionDir(execution)
	status := ex.Status
	var result scriptOutputs
	var cleanup *contract.ScriptPersonalCleanup
	var trial *contract.TrialRemoved
	if status != "unknown" && !ex.CleanupUnknown && !ex.ProcessesUnresolved {
		var err error
		result, err = e.scriptOutputSummary(execution, dir)
		if err != nil {
			return err
		}
		cleanup = e.scriptCleanup(dir, result.CollectionFailed)
		if result.CollectionFailed {
			status = "failed"
		}
		if l.trial != nil {
			trial = &contract.TrialRemoved{Removed: removeTrial(l.trial[0], l.trial[1])}
		}
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
		if cleanup != nil {
			if err := e.event(c, ex.Seq+3, contract.FleetEventTypeScriptPersonalCleanup, *cleanup); err != nil {
				return err
			}
		}
		e.declared = result.Names
		return e.finish(c, status, l.native)
	})
}

// A reattached exit can rebuild its terminal receipt after either directory has
// gone. Jobs already queued during an interrupted collection are reused.
func (e *executor) scriptOutputSummary(execution, dir string) (scriptOutputs, error) {
	var result scriptOutputs
	var saved sql.NullString
	if err := e.db.QueryRow("SELECT script_outputs FROM launches WHERE execution=?", execution).Scan(&saved); err != nil {
		return result, err
	}
	if saved.Valid {
		err := json.Unmarshal([]byte(saved.String), &result)
		return result, err
	}
	result, err := e.queuePersonalScriptOutputs(dir, execution)
	if err != nil {
		return result, err
	}
	_, err = e.db.Exec("UPDATE launches SET script_outputs=? WHERE execution=?", enc(result), execution)
	return result, err
}

func (e *executor) queuePersonalScriptOutputs(dir, execution string) (scriptOutputs, error) {
	var result scriptOutputs
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
	if len(result.Names) > 0 {
		spool, err := os.Open(filepath.Join(e.root, "spool"))
		if err != nil {
			return result, err
		}
		defer spool.Close()
		if err := spool.Sync(); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (e *executor) scriptCleanup(dir string, collectionFailed bool) *contract.ScriptPersonalCleanup {
	data := &contract.ScriptPersonalCleanup{Removed: e.removeScriptStaging(dir)}
	if collectionFailed {
		data.CollectionFailed = &collectionFailed
	}
	return data
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
		// Match the host cleanup policy for read-only subdirectories while
		// keeping all filesystem operations inside this job's directory.
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
