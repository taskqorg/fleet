// System activity (docs/designs/fleet-analytics.md, System activity): the earlier rows of the coordinator's own reads,
// marked legacySystemRead by migration 69, are deleted once no gateway spool line names them. Ingest takes a spool line
// whose row is missing as new and would replay it, key side effects included (a refused key disabled again), so a row
// whose line is still in the spool stays, marked, until a later pass (hourly) finds the line gone.
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
		every(ctx, "system-reads-cleanup", envMs("FLEET_SYSTEM_READS_CLEANUP_MS", 3_600_000), func(ctx context.Context) error {
			n, err := systemReadsCleanup(ctx, a)
			if err == nil && n > 0 {
				log.Printf("system reads cleanup: %d legacy rows deleted", n)
			}
			return err
		})
	})
}

// spoolCallIDs: the execution ids (call:<callId>) every spool file still names; ok false when the spool cannot be seen
// (no gateway directory, no spool directory): any row may then be named by a line a later start ingests.
func spoolCallIDs() (ids []string, ok bool, err error) {
	if gatewayDir() == "" {
		return nil, false, nil
	}
	dir := filepath.Join(gatewayDir(), "state", "spool")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	ids = []string{}
	for _, e := range entries {
		if !spoolFile.MatchString(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if os.IsNotExist(err) {
			continue // ingest removed a fully imported past day meanwhile
		}
		if err != nil {
			return nil, false, err // an unreadable file may name any row: delete nothing this time
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
	return ids, true, nil
}

// systemReadsCleanup deletes the legacy rows no spool line names, 1000 per transaction, totals first (foreign key).
func systemReadsCleanup(ctx context.Context, a *App) (int, error) {
	var left bool
	if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		if err := showDeleted(q); err != nil {
			return err
		}
		var err error
		left, err = q.db().SystemReadsLegacyLeft(q.ctx())
		return err
	}); err != nil || !left {
		return 0, err
	}
	keep, ok, err := spoolCallIDs()
	if err != nil || !ok {
		return 0, err // the spool unseen: delete nothing this time
	}
	deleted := 0
	for {
		var n int
		if err := runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error {
			if err := showDeleted(q); err != nil {
				return err
			}
			gone, err := q.db().SystemReadsLegacyBatch(q.ctx(), keep)
			if err != nil {
				return err
			}
			if n = len(gone); n == 0 {
				return nil
			}
			if err := q.db().SystemReadsTotalsDelete(q.ctx(), gone); err != nil {
				return err
			}
			return q.db().SystemReadsDelete(q.ctx(), gone)
		}); err != nil {
			return deleted, err
		}
		deleted += n
		if n == 0 || ctx.Err() != nil {
			return deleted, ctx.Err()
		}
	}
}
