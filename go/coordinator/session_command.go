// Composer commands (docs/design-system/controls/composer.md "Commands"): a message that starts with a known /command
// changes a context item of the session instead of being sent. One parser, here on the server, so the composer's preview,
// its apply and an agent's call read a command the same way. Today the only command is /trigger (docs/designs/
// fleet-triggers.md): it edits, pauses, resumes, runs, deletes or creates a trigger of the session.
package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

// triggerChangeKinds: the change kinds a trigger condition may name (fleet-triggers.md "Trigger").
var triggerChangeKinds = []string{"task", "question", "execution", "host", "limits"}

// triggerChangedDefault: "if changed" without kinds, the condition of the conversation-check preset.
var triggerChangedDefault = []string{"task", "question", "execution"}

// triggerCommand: one parsed /trigger command. A nil field is left as it is; IfChanged set to an empty list is "always".
type triggerCommand struct {
	Name      string    // the trigger's name; for "new", the name to give (empty: the preset's)
	Action    string    // edit, pause, resume, run, delete or new
	Preset    string    // new only
	EveryMs   *int64    // every <duration>
	Daily     *string   // daily HH:MM
	Zone      *string   // daily HH:MM <IANA zone>
	IfChanged *[]string // if changed, if <kinds>, always
	Text      *string   // after the first ": "
}

var (
	commandDuration = regexp.MustCompile(`^([1-9][0-9]*)(s|m|h|d)$`)
	commandClock    = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	commandName     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// isComposerCommand: whether text is a command for parseTriggerCommand; anything else is an ordinary message.
func isComposerCommand(text string) bool {
	t := strings.TrimSpace(text)
	return t == "/trigger" || strings.HasPrefix(t, "/trigger ")
}

// parseTriggerCommand reads `/trigger <name> [every <dur> | daily HH:MM [zone]] [if changed | if <kinds> | always] [: <text>]`,
// `/trigger <name> pause|resume|run|delete` and `/trigger new <preset> [as <name>] ...`. The error says what to write.
func parseTriggerCommand(text string) (triggerCommand, error) {
	var c triggerCommand
	t := strings.TrimSpace(text)
	if !isComposerCommand(t) {
		return c, fmt.Errorf("a command starts with /trigger")
	}
	// The message starts after the first colon that ends a word (": " or a final ":"), so "daily 09:00" stays in the head.
	rest := strings.TrimSpace(strings.TrimPrefix(t, "/trigger")) + " "
	head, body, hasText := strings.Cut(rest, ": ")
	if hasText {
		s := strings.TrimSpace(body)
		if s == "" {
			return c, fmt.Errorf("write the message after the colon, or leave the colon out to keep it")
		}
		c.Text = &s
	}
	words := strings.Fields(head)
	if len(words) == 0 {
		return c, fmt.Errorf("name the trigger: /trigger <name> every 5m: <text>")
	}
	c.Action = "edit"
	if words[0] == "new" {
		if len(words) < 2 {
			return c, fmt.Errorf("name the preset: /trigger new conversation-check")
		}
		c.Action, c.Preset, words = "new", words[1], words[2:]
		if len(words) >= 2 && words[0] == "as" {
			c.Name, words = words[1], words[2:]
		}
	} else {
		c.Name, words = words[0], words[1:]
	}
	for _, n := range []string{c.Name, c.Preset} {
		if n != "" && !commandName.MatchString(n) {
			return c, fmt.Errorf("%q is not a trigger or preset name: letters, digits, '.', '_' and '-'", n)
		}
	}
	if c.Action == "edit" && len(words) == 1 && !hasText && slices.Contains([]string{"pause", "resume", "run", "delete"}, words[0]) {
		c.Action = words[0]
		return c, nil
	}
	for i := 0; i < len(words); i++ {
		w := words[i]
		next := func(what string) (string, error) {
			if i+1 >= len(words) {
				return "", fmt.Errorf("%s needs %s", w, what)
			}
			i++
			return words[i], nil
		}
		switch w {
		case "every":
			v, err := next("a duration such as 5m")
			if err != nil {
				return c, err
			}
			m := commandDuration.FindStringSubmatch(v)
			if m == nil {
				return c, fmt.Errorf("%q is not a duration: write 30s, 5m, 2h or 1d", v)
			}
			n, _ := strconv.ParseInt(m[1], 10, 64)
			ms := n * map[string]int64{"s": 1000, "m": 60_000, "h": 3_600_000, "d": 86_400_000}[m[2]]
			c.EveryMs = &ms
		case "daily":
			v, err := next("a time such as 09:00")
			if err != nil {
				return c, err
			}
			if !commandClock.MatchString(v) {
				return c, fmt.Errorf("%q is not a time: write HH:MM, such as 09:00", v)
			}
			c.Daily = &v
			if i+1 < len(words) && (strings.Contains(words[i+1], "/") || words[i+1] == "UTC") {
				i++
				z := words[i]
				c.Zone = &z
			}
		case "if":
			v, err := next("changed, or kinds such as task,question")
			if err != nil {
				return c, err
			}
			kinds := triggerChangedDefault
			if v != "changed" {
				kinds = nil
				for _, k := range strings.Split(v, ",") {
					if !slices.Contains(triggerChangeKinds, k) {
						return c, fmt.Errorf("%q is not a change kind: use %s", k, strings.Join(triggerChangeKinds, ", "))
					}
					if !slices.Contains(kinds, k) {
						kinds = append(kinds, k)
					}
				}
			}
			c.IfChanged = &kinds
		case "always":
			c.IfChanged = &[]string{}
		default:
			return c, fmt.Errorf("%q is not part of a /trigger command: use every, daily, if changed, always, or a colon before the message", w)
		}
	}
	if c.EveryMs != nil && c.Daily != nil {
		return c, fmt.Errorf("a trigger runs every <duration> or daily at a time, not both")
	}
	if c.Action == "edit" && c.EveryMs == nil && c.Daily == nil && c.IfChanged == nil && c.Text == nil {
		return c, fmt.Errorf("say what to change: every 5m, daily 09:00, if changed, always, or a colon and the message")
	}
	return c, nil
}

// formatTriggerCommand: the command that sets a trigger to what it is now, for Edit (the composer is filled with it) and
// for Load current after a conflict. everyMs 0 and daily "" mean the trigger has neither (a manual one).
func formatTriggerCommand(name string, everyMs int64, daily, zone string, ifChanged []string, text string) string {
	var b strings.Builder
	b.WriteString("/trigger " + name)
	switch {
	case everyMs > 0:
		b.WriteString(" every " + formatCommandDuration(everyMs))
	case daily != "":
		b.WriteString(" daily " + daily)
		if zone != "" {
			b.WriteString(" " + zone)
		}
	}
	switch {
	case len(ifChanged) == 0:
		b.WriteString(" always")
	case slices.Equal(ifChanged, triggerChangedDefault):
		b.WriteString(" if changed")
	default:
		b.WriteString(" if " + strings.Join(ifChanged, ","))
	}
	if text != "" {
		b.WriteString(": " + text)
	}
	return b.String()
}

// formatCommandDuration: the largest whole unit of ms (90000 is 90s, 300000 is 5m).
func formatCommandDuration(ms int64) string {
	for _, u := range []struct {
		ms   int64
		unit string
	}{{86_400_000, "d"}, {3_600_000, "h"}, {60_000, "m"}, {1000, "s"}} {
		if ms%u.ms == 0 {
			return strconv.FormatInt(ms/u.ms, 10) + u.unit
		}
	}
	return strconv.FormatInt((ms+999)/1000, 10) + "s"
}

func init() {
	op("session_command", func(q *Request, r *contract.SessionCommandRequest) (contract.SessionCommandReply, error) {
		return sessionCommand(q, contract.SessionCommandPreviewRequest{SessionId: r.SessionId, Text: &r.Text, TriggerId: r.TriggerId, BaseRevision: r.BaseRevision}, true)
	})
	op("session_command_preview", func(q *Request, r *contract.SessionCommandPreviewRequest) (contract.SessionCommandReply, error) {
		return sessionCommand(q, *r, false)
	})
}

// triggerSlug: how a command names a trigger, its name in lower case with spaces and other signs as '-'
// ("Conversation check" is conversation-check).
func triggerSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	return b.String()
}

// commandTrigger: the trigger of rows a command names: by its slug, else by its id. A slug two triggers share asks
// for the id.
func commandTrigger(rows []db.TriggerListRow, name string) (db.Trigger, error) {
	var found []db.Trigger
	for _, r := range rows {
		if triggerSlug(r.Trigger.Name) == strings.ToLower(name) {
			found = append(found, r.Trigger)
		}
	}
	if len(found) > 1 {
		ids := []string{}
		for _, t := range found {
			ids = append(ids, t.ID)
		}
		return db.Trigger{}, fail(400, fmt.Sprintf("%d triggers are named %s: write the id instead, one of %s", len(found), name, strings.Join(ids, ", ")))
	}
	if len(found) == 1 {
		return found[0], nil
	}
	for _, r := range rows {
		if r.Trigger.ID == name {
			return r.Trigger, nil
		}
	}
	return db.Trigger{}, fail(404, "No trigger "+name+" in this session: /trigger new conversation-check creates one")
}

// commandNameOf: the word a command names t by, its slug when no other trigger shares it, else its id.
func commandNameOf(rows []db.TriggerListRow, t db.Trigger) string {
	slug := triggerSlug(t.Name)
	if slug == "" || !commandName.MatchString(slug) {
		return t.ID
	}
	if _, err := commandTrigger(rows, slug); err != nil {
		return t.ID
	}
	return slug
}

func commandFormat(rows []db.TriggerListRow, t db.Trigger) string {
	s := triggerSettingsOf(t)
	return formatTriggerCommand(commandNameOf(rows, t), ms(s.EveryMs), deref(s.DailyTime), deref(s.DailyZone), s.IfChanged, s.Text)
}

func describeWhen(s contract.TriggerSettings) string {
	switch s.When {
	case "every":
		return "every " + formatCommandDuration(ms(s.EveryMs))
	case "daily":
		return strings.TrimSpace("daily " + deref(s.DailyTime) + " " + deref(s.DailyZone))
	}
	return "manual"
}

func describeCondition(kinds []string) string {
	switch {
	case len(kinds) == 0:
		return "always"
	case slices.Equal(kinds, triggerChangedDefault):
		return "if changed (" + strings.Join(kinds, ", ") + ")"
	}
	return "if " + strings.Join(kinds, ", ") + " changed"
}

func describeState(enabled bool) string {
	if enabled {
		return "active"
	}
	return "paused"
}

// commandInput: the trigger_save settings a parsed edit or new command sets over base (the trigger's current
// settings, or the preset's for new).
func commandInput(c triggerCommand, base contract.TriggerSettings) *contract.TriggerSettingsInput {
	in := &contract.TriggerSettingsInput{}
	if c.EveryMs != nil {
		w := contract.TriggerWhen("every")
		in.When, in.EveryMs = &w, c.EveryMs
	}
	if c.Daily != nil {
		w := contract.TriggerWhen("daily")
		zone := deref(base.DailyZone)
		if c.Zone != nil {
			zone = *c.Zone
		}
		if zone == "" {
			zone = "UTC"
		}
		in.When, in.DailyTime, in.DailyZone = &w, c.Daily, &zone
	}
	if c.IfChanged != nil {
		in.IfChanged = *c.IfChanged
	}
	in.Text = c.Text
	if c.Action == "new" && c.Name != "" {
		in.Name = &c.Name
	}
	switch c.Action {
	case "pause", "resume":
		on := c.Action == "resume"
		in.Enabled = &on
	}
	return in
}

// commandChanges: what the command changes, field by field, for the preview.
func commandChanges(before *contract.TriggerSettings, after contract.TriggerSettings) []contract.SessionCommandChange {
	out := []contract.SessionCommandChange{}
	add := func(field, from, to string, hasFrom bool) {
		if hasFrom && from == to {
			return
		}
		c := contract.SessionCommandChange{Field: field, To: to}
		if hasFrom {
			c.From = &from
		}
		out = append(out, c)
	}
	var b contract.TriggerSettings
	if before != nil {
		b = *before
	}
	has := before != nil
	if !has {
		add("Name", "", after.Name, false)
	}
	add("When", describeWhen(b), describeWhen(after), has)
	add("Condition", describeCondition(b.IfChanged), describeCondition(after.IfChanged), has)
	add("Message", b.Text, after.Text, has)
	add("State", describeState(b.Enabled), describeState(after.Enabled), has)
	return out
}

// sessionCommand: one composer command, previewed (session_command_preview, a read) or run (session_command). The
// preview and the run read the command with the same parser, so what the preview shows is what the run does.
func sessionCommand(q *Request, r contract.SessionCommandPreviewRequest, apply bool) (contract.SessionCommandReply, error) {
	out := contract.SessionCommandReply{Changes: []contract.SessionCommandChange{}}
	target, err := triggerTargetOf(q, r.SessionId, "")
	if err != nil {
		return out, err
	}
	rows, err := q.db().TriggerList(q.ctx(), db.TriggerListParams{SessionID: &target.sessionID})
	if err != nil {
		return out, err
	}
	now := time.Now()
	if id := deref(r.Format); id != "" {
		for _, row := range rows {
			if row.Trigger.ID == id {
				rec := triggerRecord(row.Trigger, now)
				out.Action, out.Text, out.Trigger = "format", commandFormat(rows, row.Trigger), &rec
				return out, nil
			}
		}
		return out, errNotFound
	}
	c, err := parseTriggerCommand(deref(r.Text))
	if err != nil {
		return out, fail(400, err.Error())
	}
	out.Text, out.Action = strings.TrimSpace(deref(r.Text)), c.Action

	if c.Action == "new" {
		p, err := triggerPreset(c.Preset)
		if err != nil {
			return out, err
		}
		if p.Target != "session" {
			return out, fail(400, "Preset "+p.Id+" is for a bot: change it on the bot's page")
		}
		in := commandInput(c, contract.TriggerSettings{})
		var after contract.TriggerSettings
		applyTriggerInput(&after, &p.Settings)
		applyTriggerInput(&after, in)
		if err := normalizeTriggerSettings(&after); err != nil {
			return out, err
		}
		title := "Add trigger " + after.Name
		out.Title, out.Changes = &title, commandChanges(nil, after)
		if !apply {
			return out, nil
		}
		saved, err := triggerSaveOp(q, &contract.TriggerSaveRequest{Id: newID(), SessionId: &target.sessionID, PresetId: &p.Id, Settings: in})
		if err != nil {
			return out, err
		}
		out.Applied, out.Trigger = true, &saved.Trigger
		return out, nil
	}

	// The base is of one trigger (the one Edit opened); a command that names another is not checked against it.
	based := r.BaseRevision != nil && r.TriggerId != nil
	cur, err := commandTrigger(rows, c.Name)
	if err != nil {
		var he *HTTPError
		if based && c.Name == *r.TriggerId && errors.As(err, &he) && he.Code == 404 {
			return out, fail(409, "Trigger no longer exists")
		}
		return out, err
	}
	rec := triggerRecord(cur, now)
	out.Trigger = &rec
	if based && cur.ID == *r.TriggerId && *r.BaseRevision != rec.Revision {
		return out, fail(409, "Trigger changed since you opened it")
	}
	before := triggerSettingsOf(cur)
	verb := map[string]string{"edit": "Change", "pause": "Pause", "resume": "Resume", "run": "Run", "delete": "Delete"}[c.Action]
	title := verb + " trigger " + cur.Name
	out.Title = &title
	switch c.Action {
	case "run":
		out.Changes = append(out.Changes, contract.SessionCommandChange{Field: "Run", To: "now, whatever its time and condition say"})
	case "delete":
		state := describeState(before.Enabled)
		out.Changes = append(out.Changes, contract.SessionCommandChange{Field: "State", From: &state, To: "deleted"})
	default:
		after := before
		applyTriggerInput(&after, commandInput(c, before))
		if err := normalizeTriggerSettings(&after); err != nil {
			return out, err
		}
		out.Changes = commandChanges(&before, after)
	}
	if !apply {
		return out, nil
	}
	switch c.Action {
	case "run":
		res, err := triggerRunOp(q, &contract.TriggerRunRequest{Id: cur.ID})
		if err != nil {
			return out, err
		}
		out.Trigger, out.Sent = &res.Trigger, &res.Sent
	case "delete":
		if _, err := triggerDeleteOp(q, &contract.TriggerDeleteRequest{Id: cur.ID, BaseRevision: rec.Revision}); err != nil {
			return out, err
		}
		out.Trigger = nil
	default:
		saved, err := triggerSaveOp(q, &contract.TriggerSaveRequest{Id: cur.ID, BaseRevision: rec.Revision, Settings: commandInput(c, before)})
		if err != nil {
			return out, err
		}
		out.Trigger = &saved.Trigger
	}
	out.Applied = true
	return out, nil
}
