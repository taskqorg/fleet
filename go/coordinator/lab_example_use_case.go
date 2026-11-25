// An example artifact's Lab use case on its row (migration 102, FLT-41): labExamplesAdd records it when it stores the
// example; examples stored before that get it once from their content at start (labExampleUseCaseBackfill). Lab lists
// and leaderboards read the row, so a restart no longer reads every example from the object store.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fleet/coordinator/db"
)

func init() {
	onStart(func(ctx context.Context, a *App) {
		untilDone(ctx, "lab-example-use-cases", time.Minute, func(ctx context.Context) error { return labExampleUseCaseBackfill(ctx, a) })
	})
}

// labExampleUseCaseBackfill records the use case of every stored example that has none recorded, a page at a time:
// the reads of a page run side by side outside any transaction, then one write records what they found ('' for an
// example without one). An example that cannot be read stays unrecorded; the step goes on and then fails, to be tried
// again.
func labExampleUseCaseBackfill(ctx context.Context, a *App) error {
	const page = 100
	after, failed := "", 0
	var firstErr error
	for ctx.Err() == nil {
		var ids []string
		if err := a.store.Read(ctx, nil, func(q *Request) (err error) {
			ids, err = q.db().LabExamplesUseCaseUnread(q.ctx(), db.LabExamplesUseCaseUnreadParams{After: after, Lim: page})
			return err
		}); err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		after = ids[len(ids)-1]
		found, errs := parallel(len(ids), 8, func(i int) (string, error) {
			stored, err := labExampleContent(ctx, a, ids[i])
			return stored.UseCaseID, err
		})
		for _, err := range errs {
			if err != nil {
				failed++
				firstErr = cmp.Or(firstErr, err)
			}
		}
		if err := a.store.Write(ctx, nil, nil, func(q *Request) error {
			for i, id := range ids {
				if errs[i] != nil {
					continue
				}
				if err := q.db().LabExampleUseCaseSet(q.ctx(), db.LabExampleUseCaseSetParams{ID: id, UseCaseID: found[i]}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if len(ids) < page {
			break
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d examples unreadable, first: %w", failed, firstErr)
	}
	return ctx.Err()
}

// labExampleContent: a stored example's content, read outside any transaction.
func labExampleContent(ctx context.Context, a *App, id string) (labExample, error) {
	var ex labExample
	b, err := artifactBytes(ctx, a, id, 8<<20)
	if err != nil {
		return ex, err
	}
	if _, ok := labRawObject(b); !ok || json.Unmarshal(b, &ex) != nil {
		return ex, fail(500, "Invalid example artifact")
	}
	return ex, nil
}

// labExampleUseCases: the recorded use case of each of ids that has one recorded (an id missing from the map was not
// read yet).
func labExampleUseCases(q *Request, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.db().LabExampleUseCases(q.ctx(), ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.UseCaseID
	}
	return out, nil
}
