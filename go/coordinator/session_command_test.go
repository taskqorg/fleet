package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseTriggerCommand(t *testing.T) {
	ms := func(n int64) *int64 { return &n }
	str := func(s string) *string { return &s }
	kinds := func(k ...string) *[]string {
		if k == nil {
			k = []string{}
		}
		return &k
	}
	for _, tc := range []struct {
		in   string
		want triggerCommand
		err  string
	}{
		{in: "/trigger conversation-check every 10m if changed: Check open questions first, then task_page",
			want: triggerCommand{Name: "conversation-check", Action: "edit", EveryMs: ms(600_000), IfChanged: kinds("task", "question", "execution"), Text: str("Check open questions first, then task_page")}},
		{in: "/trigger conversation-check every 10m", want: triggerCommand{Name: "conversation-check", Action: "edit", EveryMs: ms(600_000)}},
		{in: "/trigger check: Look at the time: 12:00", want: triggerCommand{Name: "check", Action: "edit", Text: str("Look at the time: 12:00")}},
		{in: "/trigger check always", want: triggerCommand{Name: "check", Action: "edit", IfChanged: kinds()}},
		{in: "/trigger check if task,host,task", want: triggerCommand{Name: "check", Action: "edit", IfChanged: kinds("task", "host")}},
		{in: "/trigger nightly daily 09:00 Europe/Berlin", want: triggerCommand{Name: "nightly", Action: "edit", Daily: str("09:00"), Zone: str("Europe/Berlin")}},
		{in: "  /trigger check pause ", want: triggerCommand{Name: "check", Action: "pause"}},
		{in: "/trigger check delete", want: triggerCommand{Name: "check", Action: "delete"}},
		{in: "/trigger new conversation-check as watch-ci every 2h: CI?", want: triggerCommand{Name: "watch-ci", Action: "new", Preset: "conversation-check", EveryMs: ms(7_200_000), Text: str("CI?")}},
		{in: "/trigger new daily-reconciliation", want: triggerCommand{Action: "new", Preset: "daily-reconciliation"}},
		{in: "hello", err: "starts with /trigger"},
		{in: "/triggers x", err: "starts with /trigger"},
		{in: "/trigger", err: "name the trigger"},
		{in: "/trigger check", err: "say what to change"},
		{in: "/trigger check every 5", err: "not a duration"},
		{in: "/trigger check every 0m", err: "not a duration"},
		{in: "/trigger check every", err: "every needs"},
		{in: "/trigger check daily 25:00", err: "not a time"},
		{in: "/trigger check every 5m daily 09:00", err: "not both"},
		{in: "/trigger check if tasks", err: "not a change kind"},
		{in: "/trigger check soon", err: "not part of"},
		{in: "/trigger check:   ", err: "after the colon"},
		{in: "/trigger ch@ck every 5m", err: "not a trigger or preset name"},
	} {
		got, err := parseTriggerCommand(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: error %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q:\n got %+v\nwant %+v", tc.in, got, tc.want)
		}
	}
}

// A trigger's own command reads back to the same trigger: Edit and Load current put it in the box unchanged.
func TestFormatTriggerCommandRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		everyMs   int64
		daily     string
		zone      string
		ifChanged []string
		text      string
		want      string
	}{
		{300_000, "", "", []string{"task", "question", "execution"}, "Review your focus with task_page and questions_list",
			"/trigger conversation-check every 5m if changed: Review your focus with task_page and questions_list"},
		{90_000, "", "", nil, "", "/trigger conversation-check every 90s always"},
		{0, "09:00", "UTC", []string{"question"}, "Reconcile", "/trigger conversation-check daily 09:00 UTC if question: Reconcile"},
	} {
		s := formatTriggerCommand("conversation-check", tc.everyMs, tc.daily, tc.zone, tc.ifChanged, tc.text)
		if s != tc.want {
			t.Errorf("format: %q, want %q", s, tc.want)
		}
		c, err := parseTriggerCommand(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if (tc.everyMs > 0) != (c.EveryMs != nil) || (c.EveryMs != nil && *c.EveryMs != tc.everyMs) || (tc.text != "" && *c.Text != tc.text) {
			t.Errorf("%q read back as %+v", s, c)
		}
	}
}
