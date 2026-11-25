package main

import (
	"strconv"
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: access_status shows a subscription's limit as subscription_limits' periods would: the latest reading per
// period of the default window whose period has not reset, the period with the lowest percent used. Risk: the
// Access screen's limit column disagrees with Accounts & limits after access_status stopped reading the history.
func TestAccessStatusLimitFromLatestReadings(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO managed_account (id, name, harness, payload) VALUES ('acct-l', 'acct-l', 'claude', '{}')`)
	exec(`INSERT INTO managed_account (id, name, harness, payload) VALUES ('acct-o', 'acct-o', 'claude', '{}')`)
	now := time.Now()
	read := func(id string, ago time.Duration, readings string) {
		at := now.Add(-ago)
		account := "acct-l"
		if id == "ao-1" {
			account = "acct-o"
		}
		exec(`INSERT INTO execution (id, kind, state, host_id, account_id, started_at, data, observation, config_ref)
		 VALUES ($1, 'host_job', 'succeeded', 'pds18', $3, $2, '{}', 'confirmed', '{}')`, id, at, account)
		exec(`INSERT INTO event (id, execution_id, type, payload, updated_at) VALUES ($1, $2, 'limit_reading', jsonb_build_object('data', jsonb_build_object('readings', $3::jsonb)), $4)`,
			"ev-"+id, id, readings, at)
	}
	future, past := now.Add(3*time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli()
	read("al-1", 2*time.Hour, `[{"period": "5h", "pct": 10, "resetsAt": `+strconv.FormatInt(future, 10)+`}]`)
	read("al-2", time.Hour, `[{"period": "5h", "pct": 40, "resetsAt": `+strconv.FormatInt(future, 10)+`}, {"period": "week", "pct": 70, "resetsAt": `+strconv.FormatInt(future, 10)+`}]`)
	read("al-3", 30*time.Minute, `[{"period": "month", "pct": 5, "resetsAt": `+strconv.FormatInt(past, 10)+`}]`) // reset: unknown, not shown
	read("ao-1", 8*24*time.Hour, `[{"period": "5h", "pct": 20}]`)                                                // older than the window: unknown
	var st contract.AccessStatusReply
	e.opInto(t, "access_status", doc{}, e.ownerHeaders(), &st)
	limits := map[string]*contract.AccessLimit{}
	for _, it := range st.Items {
		limits[it.Id] = it.Limit
	}
	if lim := limits["account:acct-l"]; lim == nil || lim.Text != "5h 60% left" {
		t.Fatalf("limit %+v, want 5h 60%% left (latest 5h reading 40%%, the lowest percent of the periods not reset)", lim)
	}
	if lim := limits["account:acct-o"]; lim == nil || lim.Text != "unknown" {
		t.Fatalf("acct-o limit %+v, want unknown (its only reading is older than the window)", lim)
	}
	var sl contract.SubscriptionLimitsReply
	e.opInto(t, "subscription_limits", doc{}, e.ownerHeaders(), &sl)
	found := false
	for _, s := range sl.Subscriptions {
		for _, p := range s.Periods {
			if s.Id == "acct-l" && p.Period == "5h" {
				found = true
				if p.Pct == nil || *p.Pct != 40 {
					t.Fatalf("subscription_limits 5h %v, want 40", p.Pct)
				}
			}
		}
	}
	if !found {
		t.Fatal("subscription_limits has no 5h period for acct-l")
	}
}
