package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

// The delivery pass's last state survives a restart (migration delivery_snapshot): the pass stores it every time,
// and a coordinator that starts takes it as deliveryLast before it serves, so publishing_status, delivery_status, Home
// and Changes answer the last known pull requests (with their age, refreshedAt) instead of none until its first pass.

type deliverySnapshot struct {
	RefreshedAt *string                     `json:"refreshedAt"`
	Source      contract.DeliverySource     `json:"source"`
	Repos       map[string]deliverySnapRepo `json:"repos"`
	Samples     []contract.DeliverySample   `json:"samples"`
}

type deliverySnapRepo struct {
	Projects  []string                `json:"projects"`
	Items     []contract.DeliveryItem `json:"items"`
	Landing   int64                   `json:"landing"`
	Problem   string                  `json:"problem"`
	Delivered []deliverySnapPR        `json:"delivered"`
}

// deliverySnapPR: a merged pull request with the fields deliveryMergedPR leaves out of its JSON.
type deliverySnapPR struct {
	deliveryMergedPR
	SnapBody      string `json:"body"`
	SnapDelivered bool   `json:"delivered"`
}

func snapshotOf(s deliveryState) deliverySnapshot {
	out := deliverySnapshot{RefreshedAt: s.refreshedAt, Source: s.source, Repos: map[string]deliverySnapRepo{}, Samples: s.samples}
	for key, r := range s.repos {
		prs := []deliverySnapPR{}
		for _, pr := range r.delivered {
			prs = append(prs, deliverySnapPR{deliveryMergedPR: pr, SnapBody: pr.Body, SnapDelivered: pr.Delivered})
		}
		out.Repos[key] = deliverySnapRepo{Projects: r.projects, Items: r.items, Landing: r.landing, Problem: r.problem, Delivered: prs}
	}
	return out
}

func (s deliverySnapshot) state() deliveryState {
	out := deliveryState{refreshedAt: s.RefreshedAt, source: s.Source, repos: map[string]*deliveryRepo{}, samples: s.Samples}
	for key, r := range s.Repos {
		prs := []deliveryMergedPR{}
		for _, p := range r.Delivered {
			pr := p.deliveryMergedPR
			pr.Body, pr.Delivered = p.SnapBody, p.SnapDelivered
			prs = append(prs, pr)
		}
		out.repos[key] = &deliveryRepo{projects: r.Projects, items: r.Items, landing: r.Landing, problem: r.Problem, delivered: prs}
	}
	return out
}

// deliveryRestore: the stored state becomes deliveryLast when no pass has run in this process yet.
func deliveryRestore(ctx context.Context, a *App) {
	var raw []byte
	err := a.store.Read(ctx, nil, func(q *Request) error {
		var err error
		raw, err = q.db().DeliverySnapshotGet(q.ctx())
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		log.Printf("delivery snapshot: %v", err)
		return
	}
	var s deliverySnapshot
	if err := json.Unmarshal(raw, &s); err != nil || s.Source == "" {
		log.Printf("delivery snapshot: unreadable: %v", err)
		return
	}
	deliveryMu.Lock()
	if deliveryLast.source == contract.DeliverySourceNone {
		deliveryLast = s.state()
	}
	deliveryMu.Unlock()
}

// deliverySave: the pass stores its state after every pass.
func deliverySave(q *Request, s deliveryState) error {
	return q.db().DeliverySnapshotPut(q.ctx(), jsonBytes(snapshotOf(s)))
}
