package main

import (
	"context"
	"errors"
	"fmt"
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

func automationDedup(id, key string) string {
	return "automation:" + id + ":" + key
}

func getBotAutomation(q *Request, botID, id string) (db.BotAutomation, error) {
	a, err := q.db().BotAutomationGet(q.ctx(), db.BotAutomationGetParams{BotID: botID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNotFound
	}
	return a, err
}

func botAutomationRecord(a db.BotAutomation, b *botFull, now time.Time) contract.BotAutomationRecord {
	return contract.BotAutomationRecord{
		Id: a.ID, Revision: int64(a.Revision),
		Settings: contract.BotAutomationSettings{
			Name: a.Name, Instructions: a.Instructions, ProjectId: a.ProjectID,
			Kind: contract.BotAutomationKind(a.Kind), ScheduleTime: a.ScheduleTime,
			ScheduleZone: a.ScheduleZone, Event: contract.BotAutomationEvent(a.Event), Enabled: a.Enabled,
		},
		NextRunAt: formatTimePtr(nextAutomationSchedule(a, b, now)),
	}
}

func botAutomations(q *Request, r *contract.BotAutomationsRequest) (contract.BotAutomationsReply, error) {
	out := contract.BotAutomationsReply{Automations: []contract.BotAutomationRecord{}}
	b, err := readBotFull(q, r.BotId)
	if err != nil {
		return out, err
	}
	out.CanManage = botDirects(q, b.BotRecord) == nil
	rows, err := q.db().BotAutomationList(q.ctx(), b.Id)
	if err != nil {
		return out, err
	}
	now := time.Now()
	for _, row := range rows {
		record := botAutomationRecord(row.BotAutomation, &b, now)
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

func botAutomationSave(q *Request, r *contract.BotAutomationSaveRequest) (contract.BotAutomationSaveReply, error) {
	var out contract.BotAutomationSaveReply
	if !botIDPattern.MatchString(r.AutomationId) {
		return out, fail(400, "Expected an automation UUID")
	}
	b, err := readBotFull(q, r.BotId)
	if err != nil {
		return out, err
	}
	if err := botDirects(q, b.BotRecord); err != nil {
		return out, err
	}
	settings := r.Settings
	if err := validateBotAutomation(&b, settings); err != nil {
		return out, err
	}
	now := time.Now()
	a, err := getBotAutomation(q, b.Id, r.AutomationId)
	switch {
	case err == nil:
		current := botAutomationRecord(a, &b, now)
		if current.Settings == settings && (current.Revision == r.BaseRevision || current.Revision-1 == r.BaseRevision) {
			return contract.BotAutomationSaveReply{Automation: current}, nil
		}
		if current.Revision != r.BaseRevision {
			return out, fail(409, "Automation changed. Reload before saving")
		}
	case errors.Is(err, errNotFound) && r.BaseRevision == 0:
	default:
		return out, err
	}

	// Changes to the trigger or enabling it establish a new baseline.
	// Edits to its name and instructions preserve waiting work and today's slot.
	effectiveSince, eventCursor := a.EffectiveSince, a.EventCursor
	if r.BaseRevision == 0 || a.Enabled != settings.Enabled || a.Kind != string(settings.Kind) ||
		a.Event != string(settings.Event) || a.ScheduleTime != settings.ScheduleTime || a.ScheduleZone != settings.ScheduleZone {
		effectiveSince = now
		if settings.Kind == "needs_you" && settings.Enabled {
			eventCursor, err = head(q.ctx(), q.tx)
			if err != nil {
				return out, err
			}
		}
	}
	var changed int64
	if r.BaseRevision == 0 {
		changed, err = q.db().BotAutomationCreate(q.ctx(), db.BotAutomationCreateParams{
			ID: r.AutomationId, BotID: b.Id, ProjectID: b.ProjectId, Name: settings.Name,
			Instructions: settings.Instructions, Kind: string(settings.Kind), ScheduleTime: settings.ScheduleTime,
			ScheduleZone: settings.ScheduleZone, Event: string(settings.Event), Enabled: settings.Enabled,
			EffectiveSince: effectiveSince, EventCursor: eventCursor,
		})
	} else {
		changed, err = q.db().BotAutomationUpdate(q.ctx(), db.BotAutomationUpdateParams{
			ID: a.ID, BotID: b.Id, Revision: a.Revision, Name: settings.Name, Instructions: settings.Instructions,
			Kind: string(settings.Kind), ScheduleTime: settings.ScheduleTime, ScheduleZone: settings.ScheduleZone,
			Event: string(settings.Event), Enabled: settings.Enabled, EffectiveSince: effectiveSince, EventCursor: eventCursor,
		})
	}
	if err != nil {
		return out, err
	}
	if changed != 1 {
		// A UUID already in use outside this bot is indistinguishable from an inaccessible automation.
		return out, errNotFound
	}
	if err := audit(q, "bot-automation:"+r.AutomationId, "revision", r.BaseRevision, r.BaseRevision+1, auditExtra{ProjectID: &b.ProjectId}); err != nil {
		return out, err
	}
	if q.app != nil {
		afterCommit(q, q.app.wakeBots)
	}
	saved, err := getBotAutomation(q, b.Id, r.AutomationId)
	return contract.BotAutomationSaveReply{Automation: botAutomationRecord(saved, &b, now)}, err
}

const automationDate = "2006-01-02"

func automationScheduleSlot(day, clock time.Time) time.Time {
	slot := time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, day.Location())
	if slot.Day() != day.Day() || slot.Hour() != clock.Hour() || slot.Minute() != clock.Minute() {
		// A forward clock change can remove the requested local time.
		return time.Time{}
	}
	return slot
}

func automationSchedule(a db.BotAutomation, now time.Time) (local, clock time.Time, ok bool) {
	loc, err := time.LoadLocation(a.ScheduleZone)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	clock, err = time.Parse("15:04", a.ScheduleTime)
	return now.In(loc), clock, err == nil
}

func dueAutomationSchedule(a db.BotAutomation, now time.Time) (string, bool) {
	local, clock, ok := automationSchedule(a, now)
	if !ok {
		return "", false
	}
	date := local.Format(automationDate)
	slot := automationScheduleSlot(local, clock)
	return date, !slot.IsZero() && !slot.Before(a.EffectiveSince) && !now.Before(slot) && date > a.LastScheduleDate
}

func nextAutomationSchedule(a db.BotAutomation, b *botFull, now time.Time) *time.Time {
	if !a.Enabled || a.Kind != "schedule" || b.Paused {
		return nil
	}
	local, clock, ok := automationSchedule(a, now)
	if !ok {
		return nil
	}
	// Advance a calendar date at noon so transitions at the current clock time
	// cannot move an iteration back into the same date.
	day := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, local.Location())
	if last, err := time.ParseInLocation(automationDate, a.LastScheduleDate, local.Location()); err == nil && last.After(day) {
		day = time.Date(last.Year(), last.Month(), last.Day(), 12, 0, 0, 0, local.Location())
	}
	for ; ; day = day.AddDate(0, 0, 1) {
		slot := automationScheduleSlot(day, clock)
		if !slot.IsZero() && !slot.Before(now) && !slot.Before(a.EffectiveSince) && day.Format(automationDate) > a.LastScheduleDate {
			return &slot
		}
	}
}

func botAutomationTriggerPass(ctx context.Context, app *App, now time.Time) error {
	var due []db.BotAutomation
	err := app.store.Read(ctx, nil, func(q *Request) error {
		rows, err := q.db().BotAutomationTriggers(q.ctx())
		if err != nil || len(rows) == 0 {
			return err
		}
		h, err := head(q.ctx(), q.tx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			switch row.Kind {
			case "schedule":
				if _, ready := dueAutomationSchedule(row, now); ready {
					due = append(due, row)
				}
			case "needs_you":
				if row.EventCursor != h {
					due = append(due, row)
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, row := range due {
		runErr := app.store.Write(ctx, nil, []string{"bot:" + row.BotID}, func(q *Request) error {
			return botAutomationProcess(q, row.BotID, row.ID, now)
		})
		if runErr != nil {
			err = errors.Join(err, fmt.Errorf("automation %s: %w", row.ID, runErr))
		}
	}
	return err
}

func botAutomationProcess(q *Request, botID, id string, now time.Time) error {
	b, err := readBotFull(q, botID)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	a, err := getBotAutomation(q, botID, id)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil || !a.Enabled {
		return err
	}
	switch a.Kind {
	case "schedule":
		date, due := dueAutomationSchedule(a, now)
		if !due {
			return nil
		}
		prompt := fmt.Sprintf("Fleet: scheduled daily wake (%s %s) in project %s.", a.ScheduleTime, a.ScheduleZone, b.ProjectId)
		if _, _, err := admitBotWake(q, &b, botWakeReq{
			Cause: "schedule", DedupKey: automationDedup(a.ID, "schedule:"+date),
			Text: prompt, SentBy: "Fleet", Source: "schedule", CheckPause: true, Automation: &a,
		}); err != nil {
			return err
		}
		return q.db().BotAutomationScheduleAdvance(q.ctx(), db.BotAutomationScheduleAdvanceParams{ID: a.ID, LastScheduleDate: date})
	case "needs_you":
		s, err := ensureBotHomeSession(q, &b)
		if err != nil {
			return err
		}
		return botProcessNeedsYou(q, &b, s, db.BotWakeCursor{NeedsYouChangeSeq: a.EventCursor}, &a)
	default:
		return nil
	}
}
