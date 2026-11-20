package main

import (
	"context"
	"errors"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"fleet/coordinator/db"
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
		q := db.New(tx)
		intents, e := q.WakeClaim(ctx, wakeBatchSize)
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
			channels, payloads := make([]string, len(hints)), make([]string, len(hints))
			for i, h := range hints {
				channels[i], payloads[i] = h.Channel, h.Payload
			}
			if e = q.WakeNotify(ctx, db.WakeNotifyParams{Channels: channels, Payloads: payloads}); e != nil {
				return e
			}
			if e = q.WakeDelete(ctx, ids); e != nil {
				return e
			}
			span.Set("fleet.wake.hints", strconv.Itoa(len(hints)))
		}
		oldest, e := q.WakeOldest(ctx)
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
