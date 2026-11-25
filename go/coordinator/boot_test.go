package main

import (
	"testing"

	"fleet/contract"
)

// Requirement: boot answers the app's start in one request (who, settings, the stream's cursor), and a person Access
// signed in but Fleet does not know gets person unknown, not a 403 the app reads as an expired session; every other
// request of theirs stays refused. Risk: an unknown person reads data, or a new person's first visit looks signed out.
func TestBoot(t *testing.T) {
	e := openTestCoordinator(t, false)
	var known contract.BootReply
	e.opInto(t, "boot", map[string]any{}, e.ownerHeaders(), &known)
	if known.Person != contract.BootPersonKnown || known.Me.Email == nil || *known.Me.Email != testOwnerEmail || known.Settings == nil || known.Cursor == nil {
		t.Fatalf("known person: %+v", known)
	}

	stranger := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "New.Person@example.com"}
	var unknown contract.BootReply
	e.opInto(t, "boot", map[string]any{}, stranger, &unknown)
	if unknown.Person != contract.BootPersonUnknown || unknown.Me.Email == nil || *unknown.Me.Email != "new.person@example.com" ||
		unknown.Settings != nil || unknown.Cursor != nil || len(unknown.Me.Roles) != 0 {
		t.Fatalf("unknown person: %+v", unknown)
	}
	for _, op := range []string{"me", "person_settings_get", "home_view", "sessions_list"} {
		if code, _ := e.callJSON(t, "POST", "/api/ops/"+op, map[string]any{}, stranger); code != 403 {
			t.Fatalf("%s for an unknown person: %d, want 403", op, code)
		}
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/boot", map[string]any{}, map[string]string{"Authorization": "Bearer wrong", "X-Fleet-Person": "x@example.com"}); code != 401 {
		t.Fatalf("boot without the browser secret: %d, want 401", code)
	}
}
