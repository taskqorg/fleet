package main

import (
	"testing"

	"fleet/contract"
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

// Conversations grouped by room: one group per chat and topic, labelled with the group's title (a topic as
// "<group> › <topic>", one whose name Fleet has not seen as "<group> › topic <n>"), a private chat as such, and the
// conversations without a room under a null label ("No room"). Risk: two rooms of one group share a label.
func TestSessionsRoomGroups(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO telegram_room (id, chat_id, thread_id, kind, title, chat_title) VALUES
	 ('-100:0', -100, 0, 'supergroup', 'PilotQ TEST', 'PilotQ TEST'), ('-100:3', -100, 3, 'supergroup', 'Billing', 'PilotQ TEST'),
	 ('-100:7', -100, 7, 'supergroup', '', ''), ('42:0', 42, 0, 'private', '', '')`)
	for _, s := range []struct{ id, room string }{{"rg-general", "-100:0"}, {"rg-topic", "-100:3"}, {"rg-unnamed", "-100:7"}, {"rg-dm", "42:0"}, {"rg-none", ""}} {
		exec(`INSERT INTO session (id, project_id, created_at) VALUES ($1, 'p1', clock_timestamp())`, s.id)
		if s.room != "" {
			exec(`INSERT INTO telegram_work (id, room_id, session_id, title, created_by) VALUES ($1, $2, $3, '', 'ann@example.com')`, "w-"+s.id, s.room, s.id)
		}
	}
	var out contract.SessionsListReply
	e.opInto(t, "sessions_list", map[string]any{"group": "room"}, e.ownerHeaders(), &out)
	got := map[string]string{}
	for _, g := range out.Groups {
		got[g.Key] = deref(g.Label)
	}
	want := map[string]string{"r:-100:0": "Telegram · PilotQ TEST", "r:-100:3": "Telegram · PilotQ TEST › Billing",
		"r:-100:7": "Telegram · PilotQ TEST › topic 7", "r:42:0": "Telegram · private chat", "~": ""}
	for k, v := range want {
		if l, ok := got[k]; !ok || l != v {
			t.Errorf("group %s: label %q, want %q (all: %v)", k, l, v, got)
		}
	}
}
