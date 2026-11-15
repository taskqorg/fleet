package main

import (
	"context"
	"errors"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"fleet/obs"
	"github.com/jackc/pgx/v5"
)

const wakeBatchSize = 1000
const wakeRecoveryInterval = 100 * time.Millisecond

func init() { onStart(startWakeOutbox) }

// Always enabled: resource rollout flags cannot disable legacy or prepared notifications.
func startWakeOutbox(ctx context.Context, a *App) {
	go func() {
		for ctx.Err() == nil {
			n, err := wakeOutboxPass(ctx, a)
			if err != nil && ctx.Err() == nil {
				log.Printf("wake dispatcher: %v", err)
			}
			if err == nil && n == wakeBatchSize {
				continue
			}
			timer := time.NewTimer(wakeRecoveryInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

// Claims, NOTIFY and claim deletion form one transaction. A producer's original XID
// stays in its durable change/work rows; dispatcher XIDs are never visibility cursors.
func wakeOutboxPass(ctx context.Context, a *App) (claimed int, err error) {
	ctx, span := obs.Start(ctx, "wake.dispatch", "internal")
	defer func() { span.End(err) }()
	// This queue cannot change authorization. Use the maintenance pool transaction
	// directly so an empty recovery check does not invalidate Store's access caches.
	err = pgx.BeginTxFunc(ctx, a.store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT wake_id::text,channel,payload FROM wake_outbox ORDER BY created_at,wake_id LIMIT $1 FOR UPDATE SKIP LOCKED`, wakeBatchSize)
		if e != nil {
			return e
		}
		type intent struct{ ID, Channel, Payload string }
		intents, e := pgx.CollectRows(rows, pgx.RowToStructByPos[intent])
		if e != nil {
			return e
		}
		claimed = len(intents)
		span.Set("fleet.wake.claimed", strconv.Itoa(claimed))
		if claimed > 0 {
			type hint struct{ Channel, Payload string }
			seen := map[hint]bool{}
			ids := make([]string, 0, claimed)
			hints := []hint{}
			for _, v := range intents {
				ids = append(ids, v.ID)
				h := hint{v.Channel, v.Payload}
				if !seen[h] {
					seen[h] = true
					hints = append(hints, h)
				}
			}
			slices.SortFunc(hints, func(a, b hint) int {
				if n := strings.Compare(a.Channel, b.Channel); n != 0 {
					return n
				}
				return strings.Compare(a.Payload, b.Payload)
			})
			batch := &pgx.Batch{}
			for _, h := range hints {
				batch.Queue(`SELECT pg_notify($1,$2)`, h.Channel, h.Payload)
			}
			batch.Queue(`DELETE FROM wake_outbox WHERE wake_id=ANY($1::uuid[])`, ids)
			if e = tx.SendBatch(ctx, batch).Close(); e != nil {
				return e
			}
			span.Set("fleet.wake.hints", strconv.Itoa(len(hints)))
		}
		var oldest time.Time
		e = tx.QueryRow(ctx, `SELECT created_at FROM wake_outbox ORDER BY created_at,wake_id LIMIT 1`).Scan(&oldest)
		if errors.Is(e, pgx.ErrNoRows) {
			span.Set("fleet.wake.pending", "false")
			return nil
		}
		if e != nil {
			return e
		}
		span.Set("fleet.wake.pending", "true")
		span.Set("fleet.wake.oldest_pending_ms", strconv.FormatInt(time.Since(oldest).Milliseconds(), 10))
		return nil
	})
	return claimed, err
}
