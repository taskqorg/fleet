// System activity (docs/designs/fleet-analytics.md, System activity): the earlier rows of the coordinator's own reads,
// marked legacySystemRead by migration 69, are deleted once no gateway spool line names them. Ingest takes a spool line
// whose row is missing as new and would replay it, key side effects included (a refused key disabled again), so a row
// whose line is still in the spool stays, marked, until a later start finds the line gone.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

func init() {
	onStart(func(ctx context.Context, a *App) {
		go func() {
			if n, err := systemReadsCleanup(ctx, a); err != nil {
				log.Printf("system reads cleanup: %v", err)
			} else if n > 0 {
				log.Printf("system reads cleanup: %d legacy rows deleted", n)
			}
		}()
	})
}

// spoolCallIDs: the execution ids (call:<callId>) every spool file still names.
func spoolCallIDs() ([]string, error) {
	if gatewayDir() == "" {
		return nil, nil
	}
	dir := filepath.Join(gatewayDir(), "state", "spool")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, e := range entries {
		if !spoolFile.MatchString(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if os.IsNotExist(err) {
			continue // ingest removed a fully imported past day meanwhile
		}
		if err != nil {
			return nil, err // an unreadable file may name any row: delete nothing this time
		}
		for _, line := range bytes.Split(b, []byte{'\n'}) {
			var l struct {
				CallId string `json:"callId"`
			}
			if json.Unmarshal(line, &l) == nil && l.CallId != "" {
				ids = append(ids, "call:"+l.CallId)
			}
		}
	}
	return ids, nil
}

// systemReadsCleanup deletes the legacy rows no spool line names, 1000 per transaction, totals first (foreign key).
func systemReadsCleanup(ctx context.Context, a *App) (int, error) {
	var left bool
	if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		if err := showDeleted(q); err != nil {
			return err
		}
		var err error
		left, err = has(q, `SELECT 1 FROM execution WHERE system_actor IS NOT NULL AND data @> '{"legacySystemRead": true}'`)
		return err
	}); err != nil || !left {
		return 0, err
	}
	keep, err := spoolCallIDs()
	if err != nil {
		return 0, err
	}
	if keep == nil {
		keep = []string{} // = ANY(NULL) would match nothing
	}
	deleted := 0
	for {
		var n int
		if err := runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error {
			if err := showDeleted(q); err != nil {
				return err
			}
			ids, err := rows(q, `SELECT jsonb_build_object('id', id) AS data FROM execution
			 WHERE system_actor IS NOT NULL AND data @> '{"legacySystemRead": true}' AND NOT id = ANY($1) LIMIT 1000`, keep)
			if err != nil {
				return err
			}
			gone := make([]string, len(ids))
			for i, r := range ids {
				gone[i] = astr(r["id"])
			}
			if n = len(gone); n == 0 {
				return nil
			}
			if err := exec(q, "DELETE FROM execution_totals WHERE execution_id = ANY($1)", gone); err != nil {
				return err
			}
			return exec(q, "DELETE FROM execution WHERE id = ANY($1)", gone)
		}); err != nil {
			return deleted, err
		}
		deleted += n
		if n == 0 || ctx.Err() != nil {
			return deleted, ctx.Err()
		}
	}
}
