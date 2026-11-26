// Triggers (docs/designs/fleet-triggers.md): one mechanism for timed and change wakes. A trigger puts its text into a
// session, or wakes a bot in its home session, when its time comes (every, daily, manual); with a change condition
// (ifChanged) it fires only when something in its focus changed since it last fired, and otherwise only counts a quiet
// check, so no model turn runs. One single-flight pass every FLEET_TRIGGER_TICK_MS and on every change wake. No caps.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

// triggerTickMs: the pass interval. A trigger every this often or less checks each pass, as the watch did.
func triggerTickMs() int64 { return envMs("FLEET_TRIGGER_TICK_MS", 15000).Milliseconds() }

// maxEveryMs: the longest interval a time.Duration holds (about 292 years); a longer one would wrap negative.
const maxEveryMs = int64(math.MaxInt64 / int64(time.Millisecond))

// triggerKinds: the change kinds a condition may name (the watch table in fleet-pm-agent.md section 5).
var triggerKinds = []string{"task", "question", "execution", "host", "limits", "problem"}

func init() {
	op("trigger_list", triggerList)
	op("trigger_save", triggerSaveOp)
	op("trigger_delete", triggerDeleteOp)
	op("trigger_run", triggerRunOp)
	onStart(func(ctx context.Context, a *App) {
		a.ensureRuntime()
		everyWake(ctx, "triggers", time.Duration(triggerTickMs())*time.Millisecond, a.botWakeCh, true, func(ctx context.Context, _ bool) error {
			return triggerPass(ctx, a, time.Now())
		})
	})
}

// triggerTarget: what a trigger wakes, read for a save, a check or a fire.
type triggerTarget struct {
	sessionID, botID string
	projectID        *string
	session          *watchSession // session target
	bot              *botFull      // bot target
}

func (t triggerTarget) ref() (*string, *string) {
	if t.bot != nil {
		return nil, &t.botID
	}
	return &t.sessionID, nil
}

// triggerTargetOf: the session or bot named, errNotFound when not visible.
func triggerTargetOf(q *Request, sessionID, botID string) (triggerTarget, error) {
	if (sessionID == "") == (botID == "") {
		return triggerTarget{}, fail(400, "Name the target: sessionId or botId")
	}
	if botID != "" {
		b, err := readBotFull(q, botID)
		if err != nil {
			return triggerTarget{}, err
		}
		return triggerTarget{botID: botID, projectID: &b.ProjectId, bot: &b}, nil
	}
	s, err := watchSessionOf(q, sessionID)
	if err == nil && s == nil {
		err = errNotFound
	}
	if err != nil {
		return triggerTarget{}, err
	}
	return triggerTarget{sessionID: sessionID, projectID: s.ProjectID, session: s}, nil
}

func triggerTargetOfRow(q *Request, t db.Trigger) (triggerTarget, error) {
	return triggerTargetOf(q, deref(t.SessionID), deref(t.BotID))
}

// manages: the caller may create, change, run and delete triggers of the target: the person who directs the session
// or the bot, a person with full access, and an agent for its own session or one its director directs.
func (t triggerTarget) manages(q *Request) error {
	if t.bot != nil {
		return botDirects(q, t.bot.BotRecord)
	}
	if q.person == nil || fullAccess(q.person) || deref(t.session.DirectedBy) == q.person.Email {
		return nil
	}
	if q.agent != "" {
		if e, err := getExecution(q, strings.TrimPrefix(q.agent, "execution:")); err == nil && e.SessionID == t.sessionID {
			return nil
		}
	}
	return fail(403, "Session is directed by someone else")
}

// writable: a session target whose task is cancelled takes no new triggers (taskAllowsWork).
func (t triggerTarget) writable(q *Request) error {
	if t.session != nil {
		return taskAllowsWork(q, t.session.task())
	}
	return nil
}

func formatTimeOpt(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

func triggerSettingsOf(t db.Trigger) contract.TriggerSettings {
	s := contract.TriggerSettings{Name: t.Name, Text: t.Text, When: contract.TriggerWhen(t.WhenKind), IfChanged: t.IfChanged,
		Enabled: t.Enabled, EveryMs: t.EveryMs, PresetId: optStr(t.PresetID)}
	if s.IfChanged == nil {
		s.IfChanged = []string{}
	}
	if t.DailyTime != "" {
		s.DailyTime, s.DailyZone = &t.DailyTime, &t.DailyZone
	}
	return s
}

func triggerRecord(t db.Trigger, now time.Time) contract.TriggerRecord {
	r := contract.TriggerRecord{Id: t.ID, SessionId: t.SessionID, BotId: t.BotID, ProjectId: t.ProjectID, Revision: int64(t.Revision),
		Settings: triggerSettingsOf(t), Fires: t.Fires, LastFireAt: formatTimeOpt(t.LastFireAt), QuietChecks: t.QuietChecks,
		QuietSince: formatTimeOpt(t.QuietSince), LastError: t.LastError}
	r.NextAt = formatTimeOpt(triggerNext(t, now))
	return r
}

// triggerNext: when the trigger next checks or fires; nil when paused or manual.
func triggerNext(t db.Trigger, now time.Time) *time.Time {
	if !t.Enabled {
		return nil
	}
	switch t.WhenKind {
	case "every":
		if t.NextAt == nil {
			return &now
		}
		return t.NextAt
	case "daily":
		return nextDaily(t, now)
	}
	return nil
}

// triggerDue: a timed trigger whose time has come; for daily also the local date it serves.
func triggerDue(t db.Trigger, now time.Time) (slot string, due bool) {
	if !t.Enabled {
		return "", false
	}
	switch t.WhenKind {
	case "every":
		if t.NextAt == nil {
			return fmt.Sprint(now.UnixMilli()), true
		}
		// Due within half a pass: a time just after this pass would otherwise wait a whole extra pass, so a trigger
		// every pass checked every second pass (30 s at the 15 s default).
		early := time.Duration(triggerTickMs()/2) * time.Millisecond
		return fmt.Sprint(t.NextAt.UnixMilli()), !now.Add(early).Before(*t.NextAt)
	case "daily":
		return dueDaily(t, now)
	}
	return "", false
}

const dailyDate = "2006-01-02"

func dailySlot(day, clock time.Time) time.Time {
	slot := time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, day.Location())
	if slot.Day() != day.Day() || slot.Hour() != clock.Hour() || slot.Minute() != clock.Minute() {
		// A forward clock change can remove the requested local time.
		return time.Time{}
	}
	return slot
}

func dailyClock(t db.Trigger, now time.Time) (local, clock time.Time, ok bool) {
	loc, err := time.LoadLocation(t.DailyZone)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	clock, err = time.Parse("15:04", t.DailyTime)
	return now.In(loc), clock, err == nil
}

func dueDaily(t db.Trigger, now time.Time) (string, bool) {
	local, clock, ok := dailyClock(t, now)
	if !ok {
		return "", false
	}
	date := local.Format(dailyDate)
	slot := dailySlot(local, clock)
	return date, !slot.IsZero() && !slot.Before(t.EffectiveSince) && !now.Before(slot) && date > t.LastSlot
}

func nextDaily(t db.Trigger, now time.Time) *time.Time {
	local, clock, ok := dailyClock(t, now)
	if !ok {
		return nil
	}
	// Advance a calendar date at noon so transitions at the current clock time cannot move an iteration back into
	// the same date.
	day := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, local.Location())
	if last, err := time.ParseInLocation(dailyDate, t.LastSlot, local.Location()); err == nil && last.After(day) {
		day = time.Date(last.Year(), last.Month(), last.Day(), 12, 0, 0, 0, local.Location())
	}
	for i := 0; i < 400; i++ {
		slot := dailySlot(day, clock)
		if !slot.IsZero() && !slot.Before(now) && !slot.Before(t.EffectiveSince) && day.Format(dailyDate) > t.LastSlot {
			return &slot
		}
		day = day.AddDate(0, 0, 1)
	}
	return nil
}

// Presets (config/triggers/*.json).

func triggerPresets() ([]contract.TriggerPreset, error) {
	dir := filepath.Join(configDir(), "triggers")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	out := []contract.TriggerPreset{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var p contract.TriggerPreset
		if err := json.Unmarshal(raw, &p); err != nil || p.Id != strings.TrimSuffix(filepath.Base(f), ".json") {
			log.Printf("trigger preset %s skipped: id %q, %v", f, p.Id, err) // one bad file never hides the others
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func triggerPreset(id string) (*contract.TriggerPreset, error) {
	ps, err := triggerPresets()
	if err != nil {
		return nil, err
	}
	for i := range ps {
		if ps[i].Id == id {
			return &ps[i], nil
		}
	}
	return nil, fail(400, "Unknown trigger preset "+id)
}

// applyTriggerInput: the input's fields over s.
func applyTriggerInput(s *contract.TriggerSettings, in *contract.TriggerSettingsInput) {
	if in == nil {
		return
	}
	if in.Name != nil {
		s.Name = *in.Name
	}
	if in.Text != nil {
		s.Text = *in.Text
	}
	if in.When != nil {
		s.When = *in.When
	}
	if in.EveryMs != nil {
		s.EveryMs = in.EveryMs
	}
	if in.DailyTime != nil {
		s.DailyTime = in.DailyTime
	}
	if in.DailyZone != nil {
		s.DailyZone = in.DailyZone
	}
	if in.IfChanged != nil { // [] clears the condition; absent keeps it
		s.IfChanged = in.IfChanged
	}
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
}

// normalizeTriggerSettings: one settings value per meaning (fields the kind does not use cleared), or the 400 a
// person can act on.
func normalizeTriggerSettings(s *contract.TriggerSettings) error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return fail(400, "Enter a trigger name")
	}
	if s.IfChanged == nil {
		s.IfChanged = []string{}
	}
	kinds := []string{}
	for _, k := range s.IfChanged {
		if !slices.Contains(triggerKinds, k) {
			return fail(400, "Unknown change kind "+k+": use task, question, execution, host, limits, problem")
		}
		if !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}
	s.IfChanged = kinds
	switch s.When {
	case "every":
		if s.EveryMs == nil || *s.EveryMs < 1 || *s.EveryMs > maxEveryMs {
			return fail(400, "Enter everyMs, a positive interval in milliseconds of at most 292 years")
		}
		s.DailyTime, s.DailyZone = nil, nil
	case "daily":
		if deref(s.DailyTime) == "" {
			return fail(400, "Enter a daily wake time")
		}
		if deref(s.DailyZone) == "" {
			return fail(400, "Choose a time zone for the daily wake")
		}
		if err := validateBotSchedule(*s.DailyTime, *s.DailyZone); err != nil {
			return err
		}
		s.EveryMs = nil
	case "manual":
		s.EveryMs, s.DailyTime, s.DailyZone = nil, nil, nil
	default:
		return fail(400, "Expected when: every, daily or manual")
	}
	return nil
}

// triggerSaveIn: one save, from trigger_save or session_command.
type triggerSaveIn struct {
	id           string
	baseRevision int64
	target       triggerTarget
	presetID     string // create: the preset its absent fields come from
	input        *contract.TriggerSettingsInput
}

// saveTrigger: create (baseRevision 0) or change one trigger. A change of when, the condition or enabling starts its
// schedule and change history from now; a change of name or text keeps them.
func saveTrigger(q *Request, in triggerSaveIn) (db.Trigger, error) {
	var none db.Trigger
	current, err := q.db().TriggerGet(q.ctx(), in.id)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return none, err
	}
	if exists {
		if deref(current.SessionID) != in.target.sessionID || deref(current.BotID) != in.target.botID {
			return none, errNotFound // the id belongs to another target
		}
	}
	var s contract.TriggerSettings
	switch {
	case exists:
		s = triggerSettingsOf(current)
	case in.presetID != "":
		p, err := triggerPreset(in.presetID)
		if err != nil {
			return none, err
		}
		if (p.Target == "bot") != (in.target.bot != nil) {
			return none, fail(400, "Preset "+p.Id+" is for a "+string(p.Target)+" target")
		}
		applyTriggerInput(&s, &p.Settings)
		s.PresetId = &p.Id
	}
	applyTriggerInput(&s, in.input)
	if err := normalizeTriggerSettings(&s); err != nil {
		return none, err
	}
	if in.target.bot == nil && slices.Contains(s.IfChanged, "problem") {
		return none, fail(400, "Change kind problem wakes a bot only: a problem goes to the bot that owns it")
	}
	if exists {
		same := triggerSettingsOf(current)
		normalizeTriggerSettings(&same)
		if triggerSettingsEqual(same, s) && (int64(current.Revision) == in.baseRevision || int64(current.Revision)-1 == in.baseRevision) {
			return current, nil // an unchanged retry
		}
		if int64(current.Revision) != in.baseRevision {
			return none, fail(409, "Trigger changed. Reload before saving")
		}
	} else if in.baseRevision != 0 {
		return none, fail(409, "Trigger no longer exists. Reload before saving")
	}
	now := time.Now()
	effectiveSince, cursor, nextAt := current.EffectiveSince, current.ChangeCursor, current.NextAt
	if !exists || current.Enabled != s.Enabled || current.WhenKind != string(s.When) || ms(current.EveryMs) != ms(s.EveryMs) ||
		current.DailyTime != deref(s.DailyTime) || current.DailyZone != deref(s.DailyZone) || !slices.Equal(current.IfChanged, s.IfChanged) {
		effectiveSince, nextAt = now, nil
		if s.EveryMs != nil {
			next := now.Add(time.Duration(*s.EveryMs) * time.Millisecond)
			nextAt = &next
		}
		if !exists || (s.Enabled && !current.Enabled) || !slices.Equal(current.IfChanged, s.IfChanged) {
			if cursor, err = head(q.ctx(), q.tx); err != nil {
				return none, err
			}
		}
	}
	sessionID, botID := in.target.ref()
	var changed int64
	if !exists {
		changed, err = q.db().TriggerCreate(q.ctx(), db.TriggerCreateParams{ID: in.id, SessionID: sessionID, BotID: botID,
			ProjectID: in.target.projectID, Name: s.Name, PresetID: deref(s.PresetId), Text: s.Text, WhenKind: string(s.When),
			EveryMs: s.EveryMs, DailyTime: deref(s.DailyTime), DailyZone: deref(s.DailyZone), IfChanged: s.IfChanged,
			Enabled: s.Enabled, EffectiveSince: effectiveSince, ChangeCursor: cursor, NextAt: nextAt})
	} else {
		changed, err = q.db().TriggerUpdate(q.ctx(), db.TriggerUpdateParams{ID: in.id, Revision: current.Revision, Name: s.Name,
			PresetID: deref(s.PresetId), Text: s.Text, WhenKind: string(s.When), EveryMs: s.EveryMs, DailyTime: deref(s.DailyTime),
			DailyZone: deref(s.DailyZone), IfChanged: s.IfChanged, Enabled: s.Enabled, EffectiveSince: effectiveSince,
			ChangeCursor: cursor, NextAt: nextAt})
	}
	if err != nil {
		return none, err
	}
	if changed != 1 {
		return none, fail(409, "Trigger changed. Reload before saving")
	}
	if err := audit(q, "trigger:"+in.id, "revision", in.baseRevision, in.baseRevision+1, auditExtra{ProjectID: in.target.projectID}); err != nil {
		return none, err
	}
	if q.app != nil {
		afterCommit(q, q.app.wakeBots)
	}
	return q.db().TriggerGet(q.ctx(), in.id)
}

// ms: an optional interval, 0 for none.
func ms(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func triggerSettingsEqual(a, b contract.TriggerSettings) bool {
	return a.Name == b.Name && a.Text == b.Text && a.When == b.When && ms(a.EveryMs) == ms(b.EveryMs) &&
		deref(a.DailyTime) == deref(b.DailyTime) && deref(a.DailyZone) == deref(b.DailyZone) &&
		slices.Equal(a.IfChanged, b.IfChanged) && a.Enabled == b.Enabled && deref(a.PresetId) == deref(b.PresetId)
}

// Operations.

func triggerList(q *Request, r *contract.TriggerListRequest) (contract.TriggerListReply, error) {
	out := contract.TriggerListReply{Triggers: []contract.TriggerRecord{}, PassMs: triggerTickMs()}
	t, err := triggerTargetOf(q, deref(r.SessionId), deref(r.BotId))
	if err != nil {
		return out, err
	}
	out.CanManage = t.manages(q) == nil
	sessionID, botID := t.ref()
	rows, err := q.db().TriggerList(q.ctx(), db.TriggerListParams{SessionID: sessionID, BotID: botID})
	if err != nil {
		return out, err
	}
	now := time.Now()
	for _, row := range rows {
		rec := triggerRecord(row.Trigger, now)
		if row.LastState != "" { // a bot trigger that caused a run
			rec.LastRun = &contract.TriggerLastRun{State: contract.BotRunState(row.LastState), Observation: contract.BotRunObservation(row.LastObservation),
				Reason: optStr(row.LastReason), At: row.LastRunAt.Time.UTC().Format(time.RFC3339Nano)}
		}
		out.Triggers = append(out.Triggers, rec)
	}
	if out.Presets, err = triggerPresets(); err != nil {
		return out, err
	}
	return out, nil
}

func triggerSaveOp(q *Request, r *contract.TriggerSaveRequest) (contract.TriggerSaveReply, error) {
	var out contract.TriggerSaveReply
	if r.BaseRevision == 0 && !botIDPattern.MatchString(r.Id) { // migrated triggers keep their older ids
		return out, fail(400, "Expected a trigger UUID")
	}
	sessionID, botID := deref(r.SessionId), deref(r.BotId)
	if existing, err := q.db().TriggerGet(q.ctx(), r.Id); err == nil && sessionID == "" && botID == "" {
		sessionID, botID = deref(existing.SessionID), deref(existing.BotID)
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	t, err := triggerTargetOf(q, sessionID, botID)
	if err != nil {
		return out, err
	}
	if err := t.manages(q); err != nil {
		return out, err
	}
	if err := t.writable(q); err != nil {
		return out, err
	}
	saved, err := saveTrigger(q, triggerSaveIn{id: r.Id, baseRevision: r.BaseRevision, target: t, presetID: deref(r.PresetId), input: r.Settings})
	if err != nil {
		return out, err
	}
	return contract.TriggerSaveReply{Trigger: triggerRecord(saved, time.Now())}, nil
}

// triggerManaged: the trigger id and its target, when the caller may manage it.
func triggerManaged(q *Request, id string) (db.Trigger, triggerTarget, error) {
	t, err := q.db().TriggerGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, triggerTarget{}, errNotFound
	}
	if err != nil {
		return t, triggerTarget{}, err
	}
	target, err := triggerTargetOfRow(q, t)
	if err != nil {
		return t, target, err
	}
	return t, target, target.manages(q)
}

func triggerDeleteOp(q *Request, r *contract.TriggerDeleteRequest) (contract.TriggerDeleteReply, error) {
	out := contract.TriggerDeleteReply{Id: r.Id}
	t, target, err := triggerManaged(q, r.Id)
	if err != nil {
		return out, err
	}
	n, err := q.db().TriggerDelete(q.ctx(), db.TriggerDeleteParams{ID: t.ID, Revision: int32(r.BaseRevision)})
	if err != nil {
		return out, err
	}
	if n != 1 {
		return out, fail(409, "Trigger changed. Reload before deleting")
	}
	out.Deleted = true
	return out, audit(q, "trigger:"+t.ID, "deleted", r.BaseRevision, nil, auditExtra{ProjectID: target.projectID})
}

func triggerRunOp(q *Request, r *contract.TriggerRunRequest) (contract.TriggerRunReply, error) {
	var out contract.TriggerRunReply
	t, target, err := triggerManaged(q, r.Id)
	if err != nil {
		return out, err
	}
	if err := target.writable(q); err != nil {
		return out, err
	}
	sent, err := triggerCheck(q, t, target, time.Now(), true)
	if err != nil {
		return out, err
	}
	fresh, err := q.db().TriggerGet(q.ctx(), t.ID)
	return contract.TriggerRunReply{Trigger: triggerRecord(fresh, time.Now()), Sent: sent}, err
}

// Pass.

// triggerPass: every enabled timed trigger whose time has come, each in its own write (§Pass in the design).
func triggerPass(ctx context.Context, a *App, now time.Time) error {
	var due []db.Trigger
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		rows, err := q.db().TriggerDue(q.ctx())
		for _, t := range rows {
			if _, ok := triggerDue(t, now); ok {
				due = append(due, t)
			}
		}
		return err
	}); err != nil {
		log.Printf("triggers: %v", err)
		return err
	}
	var errs error
	for _, t := range due {
		lock := GLOBAL
		if t.BotID != nil {
			lock = "bot:" + *t.BotID
		}
		err := a.store.Write(ctx, nil, []string{lock}, func(q *Request) error { return triggerOne(q, t.ID, now) })
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("trigger %s: %w", t.ID, err))
			a.store.Write(ctx, nil, []string{lock}, func(q *Request) error {
				return q.db().TriggerSetError(q.ctx(), db.TriggerSetErrorParams{Error: err.Error(), ID: t.ID})
			})
		}
	}
	return errs
}

func triggerOne(q *Request, id string, now time.Time) error {
	t, err := q.db().TriggerGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, due := triggerDue(t, now); !due {
		return nil
	}
	target, err := triggerTargetOfRow(q, t)
	if errors.Is(err, errNotFound) {
		return nil // the target went; its rows go with it
	}
	if err != nil {
		return err
	}
	if target.session != nil {
		if task := target.session.task(); task != "" {
			if cancelled, err := q.db().TaskCancelled(q.ctx(), task); err != nil || cancelled {
				if err == nil {
					err = q.db().TriggerDeleteIDs(q.ctx(), []string{t.ID})
				}
				return err
			}
		}
	}
	_, err = triggerCheck(q, t, target, now, false)
	return err
}

// triggerBusy: the session still has a trigger message queued, or the bot is running a turn: the next pass
// covers everything, so the state stays.
func triggerBusy(q *Request, t db.Trigger, target triggerTarget) (bool, error) {
	if target.bot == nil {
		return q.db().TriggerWakeQueued(q.ctx(), target.sessionID)
	}
	// A bot's conversations run in parallel: only this trigger's own message still queued holds it.
	return q.db().BotTriggerWakeQueued(q.ctx(), db.BotTriggerWakeQueuedParams{BotID: &target.botID, Column2: t.ID})
}

// triggerCheck: one check of t; manual fires whatever the time and the condition say. Reports whether it sent.
func triggerCheck(q *Request, t db.Trigger, target triggerTarget, now time.Time, manual bool) (bool, error) {
	if !manual {
		if busy, err := triggerBusy(q, t, target); err != nil || busy {
			return false, err
		}
	}
	slot, _ := triggerDue(t, now)
	if manual {
		slot = "run:" + fmt.Sprint(now.UnixMilli())
	}
	cursor, limits := t.ChangeCursor, t.LimitsSignaled
	var parts []threadPart
	if len(t.IfChanged) > 0 {
		var err error
		if parts, cursor, limits, err = triggerChanges(q, t, target); err != nil {
			return false, err
		}
	}
	st := db.TriggerStateParams{ID: t.ID, ChangeCursor: cursor, NextAt: t.NextAt, LastSlot: t.LastSlot, Fires: t.Fires,
		LastFireAt: t.LastFireAt, QuietChecks: t.QuietChecks, QuietSince: t.QuietSince, LimitsSignaled: limits}
	if limits == nil {
		st.LimitsSignaled = []string{}
	}
	if !manual {
		if t.WhenKind == "daily" {
			st.LastSlot = slot
		} else if t.EveryMs != nil {
			next := now.Add(time.Duration(*t.EveryMs) * time.Millisecond)
			st.NextAt = &next
		}
	}
	if len(t.IfChanged) > 0 && len(parts) == 0 && !manual {
		if ms(t.EveryMs) > triggerTickMs() { // a trigger checking each pass counts no quiet checks: they mean nothing
			st.QuietChecks++
			if st.QuietSince == nil {
				st.QuietSince = &now
			}
		}
		return false, q.db().TriggerState(q.ctx(), st)
	}
	if len(t.IfChanged) > 0 {
		// The message lists every change the target's change triggers watch, from the earliest of their cursors, so
		// they all start after it: no change is reported twice or lost (fleet-triggers.md, Pass step 4).
		all, err := triggerTargetChanges(q, t, target)
		if err != nil {
			return false, err
		}
		if all.IfChanged != nil {
			if parts, cursor, st.LimitsSignaled, err = triggerChanges(q, all, target); err != nil {
				return false, err
			}
			if st.LimitsSignaled == nil {
				st.LimitsSignaled = []string{}
			}
			st.ChangeCursor = cursor
		}
	}
	sent, err := triggerSend(q, t, target, slot, parts, manual)
	if err != nil {
		return false, err
	}
	if sent { // a paused bot's skipped wake is in the trigger's log (trigger_log), not a fire
		st.Fires, st.LastFireAt, st.QuietChecks, st.QuietSince = st.Fires+1, &now, 0, nil
	}
	if err := q.db().TriggerState(q.ctx(), st); err != nil {
		return false, err
	}
	if len(t.IfChanged) > 0 {
		sessionID, botID := target.ref()
		err = q.db().TriggerShareCursor(q.ctx(), db.TriggerShareCursorParams{ChangeCursor: cursor, ID: t.ID, SessionID: sessionID, BotID: botID})
	}
	return sent, err
}

// triggerTargetChanges: t widened to every enabled change trigger of its target: the union of their kinds from the
// earliest of their cursors. IfChanged stays nil when t is the target's only change trigger.
func triggerTargetChanges(q *Request, t db.Trigger, target triggerTarget) (db.Trigger, error) {
	sessionID, botID := target.ref()
	rows, err := q.db().TriggerList(q.ctx(), db.TriggerListParams{SessionID: sessionID, BotID: botID})
	if err != nil {
		return t, err
	}
	all, kinds, others := t, slices.Clone(t.IfChanged), false
	for _, r := range rows {
		o := r.Trigger
		if o.ID == t.ID || !o.Enabled || len(o.IfChanged) == 0 {
			continue
		}
		others = true
		for _, k := range o.IfChanged {
			// Limits signals are not change rows: each trigger keeps its own sent keys and signals them itself.
			if k != "limits" && !slices.Contains(kinds, k) {
				kinds = append(kinds, k)
			}
		}
		all.ChangeCursor = min(all.ChangeCursor, o.ChangeCursor)
	}
	if !others {
		all.IfChanged = nil
		return all, nil
	}
	all.IfChanged = kinds
	return all, nil
}

// triggerSend: the trigger's message to its target.
// Changes go one message per subject (threads_bot.go changeThread), in the order the subjects first changed. A session
// target gets them in its threads; a bot target in its conversation for each subject (fleet-conversations.md#rules). A
// check without changes (a timed wake, Run now) is one message: in the session, or in the bot's trigger conversation.
func triggerSend(q *Request, t db.Trigger, target triggerTarget, slot string, all []threadPart, manual bool) (bool, error) {
	if len(all) == 0 {
		return triggerSendOne(q, t, target, slot, nil, "", manual)
	}
	keys, lines := threadGroups(all)
	if target.bot != nil {
		// Each subject's conversation takes its bot-subject lock before its session lock (botConversation), so one pass
		// waking several subjects takes all of them first, in lock order; otherwise the second subject's lock would
		// come after the first subject's session lock.
		locks := make([]string, 0, len(keys))
		for _, k := range keys {
			locks = append(locks, botSubjectLock(target.bot.Id, firstString(k, "trigger:"+t.ID)))
		}
		if err := acquireTxLock(q, locks...); err != nil {
			return false, err
		}
	}
	sent := false
	for _, k := range keys {
		var ok bool
		var err error
		// A bot target: each subject's changes go to the bot's conversation for that subject, each its own turn.
		ok, err = triggerSendOne(q, t, target, slot, lines[k], k, manual)
		if err != nil {
			return sent, err
		}
		sent = sent || ok
	}
	return sent, nil
}

// triggerSendOne: the trigger's message to its target for one thread ("" the main thread).
func triggerSendOne(q *Request, t db.Trigger, target triggerTarget, slot string, parts []string, thread string, manual bool) (bool, error) {
	changes := ""
	if len(parts) > 0 {
		changes = fmt.Sprintf("Fleet: %d changes in your focus: %s", len(parts), strings.Join(parts, "; "))
	}
	rid := "trigger:" + t.ID + ":" + slot
	if thread != "" {
		rid += ":" + thread
	}
	if target.bot == nil {
		text := strings.TrimSpace(strings.Join(slices.DeleteFunc([]string{t.Text, changes}, func(s string) bool { return s == "" }), "\n"))
		if text == "" {
			text = "Fleet: trigger " + t.Name
		}
		_, err := agentSubmitFrom(q, &contract.MessageSendRequest{SessionId: &target.sessionID, RequestId: &rid, Text: &text}, "trigger", t.ID)
		return err == nil, err
	}
	b := target.bot
	cause, head := "schedule", fmt.Sprintf("Fleet: trigger wake in project %s.", b.ProjectId)
	switch {
	case manual:
		cause, head = "manual", fmt.Sprintf("Fleet: manual run requested for %s (%s). Inspect your project state and handle any open work.", b.Settings.Name, b.ProjectId)
	case len(parts) > 0:
		cause, head = "needs_you", fmt.Sprintf("Fleet: %d changes in project %s: %s", len(parts), b.ProjectId, strings.Join(parts, "; "))
	case t.WhenKind == "daily":
		head = fmt.Sprintf("Fleet: scheduled daily wake (%s %s) in project %s.", t.DailyTime, t.DailyZone, b.ProjectId)
	}
	subject := thread
	if subject == "" {
		subject = "trigger:" + t.ID // a timed wake or Run now: the trigger's own conversation
	}
	_, _, sent, err := admitBotWake(q, b, botWakeReq{Cause: cause, DedupKey: rid, Text: head, SentBy: "Fleet", Source: "trigger",
		CheckPause: !manual, Trigger: &t, Thread: subject})
	return sent, err
}

// triggerChanges: the changes after the trigger's cursor that its condition names, classified by their current rows
// (watch.go classify), and the cursor and sent limits keys after them.
func triggerChanges(q *Request, t db.Trigger, target triggerTarget) ([]threadPart, int64, []string, error) {
	var focus []string
	var directedBy, self string
	if target.bot != nil {
		focus, directedBy = []string{target.bot.ProjectId}, target.bot.Director
	} else {
		focus, directedBy, self = target.session.focus(), deref(target.session.DirectedBy), target.sessionID
	}
	dir := q.peopleSnapshot().person(firstString(directedBy, ownerEmail(q)))
	if dir == nil {
		return nil, t.ChangeCursor, t.LimitsSignaled, fail(403, "Director of the trigger's target is not a person of this Fleet")
	}
	own, children := map[string]bool{}, map[string]bool{}
	selves := []string{}
	if self != "" {
		selves = append(selves, self)
	}
	if target.bot != nil { // every conversation of the bot is its own
		ids, err := q.db().BotSessionIDs(q.ctx(), &target.bot.Id)
		if err != nil {
			return nil, t.ChangeCursor, t.LimitsSignaled, err
		}
		for _, id := range ids {
			if id != self {
				selves = append(selves, id)
			}
		}
	}
	for _, sid := range selves {
		es, err := q.db().BotOwnExecutions(q.ctx(), &sid) // the session's and its children's executions
		if err != nil {
			return nil, t.ChangeCursor, t.LimitsSignaled, err
		}
		for _, e := range es {
			own["execution:"+e] = true
		}
		cs, err := q.db().ChildSessionIDs(q.ctx(), &sid)
		if err != nil {
			return nil, t.ChangeCursor, t.LimitsSignaled, err
		}
		for _, c := range cs {
			children[c] = true
		}
		children[sid] = true
	}
	cursor := t.ChangeCursor
	parts, seen := []threadPart{}, map[string]bool{}
	for {
		page, next, resync, _, err := since(q.ctx(), q.tx, cursor, 500)
		if err != nil {
			return nil, t.ChangeCursor, t.LimitsSignaled, err
		}
		if resync {
			cursor = next
			parts = append(parts, threadPart{"fleet", "change history after the last check was pruned; re-read your focus with task_page"})
			break
		}
		for _, c := range page {
			p := deref(c.ProjectID)
			if c.Kind == "problem" {
				// A problem wakes only its owner bot, whatever its project (problems without one included); the bot's
				// own updates are its own (actor) and wake nobody.
				if target.bot == nil || !slices.Contains(t.IfChanged, "problem") || seen["problem"+c.Id] || (c.Actor != nil && own[*c.Actor]) {
					continue
				}
				seen["problem"+c.Id] = true
				if part, ok, err := problemChangePart(q, c.Id, target.bot.Id); err != nil {
					return nil, t.ChangeCursor, t.LimitsSignaled, err
				} else if ok {
					parts = append(parts, part)
				}
				continue
			}
			if !slices.Contains(t.IfChanged, string(c.Kind)) || seen[string(c.Kind)+c.Id] ||
				(c.Actor != nil && own[*c.Actor]) || (c.SessionId != nil && (children[*c.SessionId] || *c.SessionId == self)) ||
				(focus != nil && c.Kind != "host" && !slices.Contains(focus, p)) ||
				// A bot's wake in its own project checks its director's authority when admitted and records a refusal
				// as a failed run; anything else (hosts, other projects) the director must see.
				(!fullAccess(dir) && !mayAccessProjectID(dir, p) && (target.bot == nil || p != target.bot.ProjectId)) {
				continue
			}
			// A task edit that kept its state classifies empty and leaves the task unseen: a later change of the same task
			// in this pass may still change its state.
			s, err := classify(q, c)
			if err != nil {
				return nil, t.ChangeCursor, t.LimitsSignaled, err
			}
			if s != "" || c.Kind != "task" {
				seen[string(c.Kind)+c.Id] = true
			}
			if s != "" {
				parts = append(parts, threadPart{changeThread(c.Change), s})
			}
		}
		cursor = next
		if len(page) < 500 {
			break
		}
	}
	keys := t.LimitsSignaled
	if slices.Contains(t.IfChanged, "limits") {
		signals, sent, err := limitSignals(q, dir, t.LimitsSignaled)
		if err != nil {
			log.Printf("trigger %s: limits: %v", t.ID, err) // a limits read problem never blocks the other changes
		} else if len(signals) > 0 {
			for _, s := range signals {
				parts = append(parts, threadPart{"fleet:limits", s})
			}
			keys = sent
		}
	}
	return parts, cursor, keys, nil
}
