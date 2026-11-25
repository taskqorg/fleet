// Bot automations: the bot page's view of a bot's triggers (triggers.go, docs/designs/fleet-triggers.md). An automation
// is a bot trigger: schedule is daily, needs_you is a change trigger on questions and failed runs, manual runs only on
// request. Kept until the bot page reads trigger_list and writes trigger_save.
package main

import (
	"errors"
	"slices"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

func init() {
	op("bot_automations", botAutomations)
	op("bot_automation_save", botAutomationSave)
}

// getBotTrigger: the trigger id of the bot, errNotFound when it is another target's or absent.
func getBotTrigger(q *Request, botID, id string) (db.Trigger, error) {
	t, err := q.db().TriggerGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && deref(t.BotID) != botID) {
		return t, errNotFound
	}
	return t, err
}

// automationOf: the automation settings of a bot trigger; false for a trigger the automation kinds cannot show
// (an unconditional repeating wake, a condition on other kinds).
func automationOf(t db.Trigger, projectID string) (contract.BotAutomationSettings, bool) {
	s := contract.BotAutomationSettings{Name: t.Name, Instructions: t.Text, ProjectId: projectID, Enabled: t.Enabled, Event: "none"}
	switch t.WhenKind {
	case "daily":
		s.Kind, s.ScheduleTime, s.ScheduleZone = "schedule", t.DailyTime, t.DailyZone
	case "manual":
		s.Kind = "manual"
	default:
		s.Kind = "needs_you"
		switch k := slices.Sorted(slices.Values(t.IfChanged)); {
		case slices.Equal(k, []string{"execution", "question"}):
			s.Event = "all"
		case slices.Equal(k, []string{"question"}):
			s.Event = "question"
		case slices.Equal(k, []string{"execution"}):
			s.Event = "execution"
		default:
			return s, false
		}
	}
	return s, true
}

func botAutomationRecord(t db.Trigger, b *botFull, now time.Time) (contract.BotAutomationRecord, bool) {
	s, ok := automationOf(t, b.ProjectId)
	r := contract.BotAutomationRecord{Id: t.ID, Revision: int64(t.Revision), Settings: s}
	if t.WhenKind == "daily" && !b.Paused {
		r.NextRunAt = formatTimePtr(triggerNext(t, now))
	}
	return r, ok
}

func botAutomations(q *Request, r *contract.BotAutomationsRequest) (contract.BotAutomationsReply, error) {
	out := contract.BotAutomationsReply{Automations: []contract.BotAutomationRecord{}}
	b, err := readBotFull(q, r.BotId)
	if err != nil {
		return out, err
	}
	out.CanManage = botDirects(q, b.BotRecord) == nil
	rows, err := q.db().TriggerList(q.ctx(), db.TriggerListParams{BotID: &b.Id})
	if err != nil {
		return out, err
	}
	now := time.Now()
	for _, row := range rows {
		record, ok := botAutomationRecord(row.Trigger, &b, now)
		if !ok {
			continue
		}
		if row.LastState != "" {
			state := contract.BotRunState(row.LastState)
			record.LastRunState = &state
			observation := contract.BotRunObservation(row.LastObservation)
			record.LastRunObservation = &observation
			record.LastRunReason = optStr(row.LastReason)
			record.LastRunAt = formatTimePtr(&row.LastRunAt)
		}
		out.Automations = append(out.Automations, record)
	}
	return out, nil
}

func validateBotAutomation(b *botFull, s contract.BotAutomationSettings) error {
	if strings.TrimSpace(s.Name) == "" {
		return fail(400, "Enter an automation name")
	}
	if s.ProjectId != b.ProjectId {
		return fail(403, "Automation project must match the bot project")
	}
	if s.Kind == "schedule" {
		if s.ScheduleTime == "" {
			return fail(400, "Enter a daily wake time")
		}
		if err := validateBotSchedule(s.ScheduleTime, s.ScheduleZone); err != nil {
			return err
		}
	} else if s.ScheduleTime != "" || s.ScheduleZone != "" {
		return fail(400, "Schedule time and zone require a daily trigger")
	}
	if (s.Kind == "needs_you") == (s.Event == "none") {
		return fail(400, "Choose an event filter for a Needs you trigger")
	}
	return nil
}

// triggerOfAutomation: the trigger settings an automation's settings mean.
func triggerOfAutomation(s contract.BotAutomationSettings) contract.TriggerSettings {
	t := contract.TriggerSettings{Name: s.Name, Text: s.Instructions, Enabled: s.Enabled, IfChanged: []string{}}
	switch s.Kind {
	case "schedule":
		t.When, t.DailyTime, t.DailyZone = "daily", &s.ScheduleTime, &s.ScheduleZone
	case "manual":
		t.When = "manual"
	default:
		every := triggerTickMs() // a check each pass, as the bot pass had
		t.When, t.EveryMs = "every", &every
		switch s.Event {
		case "question":
			t.IfChanged = []string{"question"}
		case "execution":
			t.IfChanged = []string{"execution"}
		default:
			t.IfChanged = []string{"question", "execution"}
		}
	}
	return t
}

func botAutomationSave(q *Request, r *contract.BotAutomationSaveRequest) (contract.BotAutomationSaveReply, error) {
	var out contract.BotAutomationSaveReply
	if r.BaseRevision == 0 && !botIDPattern.MatchString(r.AutomationId) {
		return out, fail(400, "Expected an automation UUID")
	}
	b, err := readBotFull(q, r.BotId)
	if err != nil {
		return out, err
	}
	if err := botDirects(q, b.BotRecord); err != nil {
		return out, err
	}
	if err := validateBotAutomation(&b, r.Settings); err != nil {
		return out, err
	}
	settings := triggerOfAutomation(r.Settings)
	if current, err := getBotTrigger(q, b.Id, r.AutomationId); err == nil {
		settings.PresetId = optStr(current.PresetID)
	}
	saved, err := saveTrigger(q, triggerSaveIn{id: r.AutomationId, baseRevision: r.BaseRevision,
		target: triggerTarget{botID: b.Id, projectID: &b.ProjectId, bot: &b}, replace: &settings})
	if err != nil {
		var f *HTTPError
		if errors.As(err, &f) && f.Code == 409 {
			return out, fail(409, "Automation changed. Reload before saving")
		}
		return out, err
	}
	record, _ := botAutomationRecord(saved, &b, time.Now())
	return contract.BotAutomationSaveReply{Automation: record}, nil
}
