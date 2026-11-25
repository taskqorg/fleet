package main

import (
	"testing"

	"fleet/coordinator/db"
)

// Telegram rows follow the reader's scope (policies.sql): a room by its project hint, a link by its person; the owner
// and the coordinator's own work see all.
func TestTelegramRowsFollowScope(t *testing.T) {
	e := openTestCoordinator(t, false)
	if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		for _, r := range []db.TelegramRoomUpsertParams{{ID: "room:p1", ChatID: -1, Kind: "group", Title: "p1"}, {ID: "room:none", ChatID: -2, Kind: "group", Title: "none"}} {
			if _, err := q.db().TelegramRoomUpsert(q.ctx(), r); err != nil {
				return err
			}
		}
		if _, err := q.db().TelegramRoomSetHint(q.ctx(), db.TelegramRoomSetHintParams{ID: "room:p1", ProjectHint: "p1"}); err != nil {
			return err
		}
		for _, l := range []db.TelegramLinkAddParams{{Code: "c-ann", Email: "ann@example.com"}, {Code: "c-bob", Email: "bob@example.com"}} {
			if err := q.db().TelegramLinkAdd(q.ctx(), l); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ann := &Person{Email: "ann@example.com", Roles: []string{"operator"}}
	ann.Grants = append(ann.Grants, struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}{"project", "p1"})
	var rooms []db.TelegramRoom
	var taken string
	if err := e.app.store.Write(e.ctx, scopedRequest(e.ctx, e.app, ann), nil, func(q *Request) (err error) {
		if rooms, err = q.db().TelegramRoomList(q.ctx()); err != nil {
			return err
		}
		if _, err := q.db().TelegramLinkTake(q.ctx(), "c-bob"); err == nil {
			t.Error("ann took bob's link code")
		}
		taken, err = q.db().TelegramLinkTake(q.ctx(), "c-ann")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0].ID != "room:p1" {
		t.Fatalf("ann sees rooms %v, want only room:p1", rooms)
	}
	if taken != "ann@example.com" {
		t.Fatalf("ann's code: %q", taken)
	}
	var all []db.TelegramRoom
	if err := e.app.store.Read(e.ctx, nil, func(q *Request) (err error) { all, err = q.db().TelegramRoomList(q.ctx()); return err }); err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("the coordinator sees %d rooms, want 2", len(all))
	}
}
