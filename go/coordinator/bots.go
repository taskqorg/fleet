// Persistent bot settings, scoped memory, and runtime operations.
package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

var botIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func init() {
	op("bot_list", botList)
	op("bot_get", botGet)
	op("bot_settings_save", botSettingsSave)
	op("bot_memory_save", botMemorySave)
	op("bot_run", botRunOp)
	op("bot_pause", botPauseOp)
	op("bot_stop", botStopOp)
	op("bot_runs", botRunsOp)
	op("bot_tasks", botTasksOp)
	op("bot_task_link", botTaskLinkOp)
	onStart(func(ctx context.Context, a *App) {
		a.ensureRuntime()
		everyWake(ctx, "bots", envMs("FLEET_BOT_TICK_MS", 15000), a.botWakeCh, true, func(ctx context.Context, _ bool) error {
			return botTriggerPass(ctx, a, time.Now())
		})
	})
}

type botFull struct {
	contract.BotRecord
	RevisionCreatedAt time.Time
	PausedBy          *string
	ResumedAt         *time.Time
}

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

func botList(q *Request, r *contract.BotListRequest) (contract.BotListReply, error) {
	out := contract.BotListReply{Bots: []contract.BotListItem{}}
	if r.ProjectId != nil && projectSettingsOf(q, *r.ProjectId) == nil {
		return out, errNotFound
	}
	rows, err := q.db().BotList(q.ctx(), db.BotListParams{ProjectID: r.ProjectId, ProjectIds: r.ProjectIds, ExcludeProjectIds: r.ExcludeProjectIds})
	for _, row := range rows {
		out.Bots = append(out.Bots, contract.BotListItem{
			Id:            row.ID,
			ProjectId:     row.ProjectID,
			Name:          row.Name,
			Purpose:       row.Purpose,
			Revision:      int64(row.Revision),
			Paused:        row.Paused,
			HomeSessionId: row.HomeSessionID,
			ActiveRunId:   row.ActiveRunID,
			LastRunState:  row.LastRunState,
			LastRunAt:     formatTimePtr(row.LastRunAt),
		})
	}
	return out, err
}

func readBotFull(q *Request, id string) (botFull, error) {
	row, err := q.db().BotGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return botFull{}, errNotFound
	}
	if err != nil {
		return botFull{}, err
	}
	rec := contract.BotRecord{
		Id:                row.ID,
		ProjectId:         row.ProjectID,
		Director:          row.Director,
		Revision:          int64(row.Revision),
		Memory:            row.Memory,
		MemoryRevision:    int64(row.MemoryRevision),
		HomeSessionId:     row.HomeSessionID,
		Paused:            row.Paused,
		PausedAt:          formatTimePtr(row.PausedAt),
		ActiveRunId:       row.ActiveRunID,
		ActiveExecutionId: row.ActiveExecutionID,
		LastRunAt:         formatTimePtr(row.LastRunAt),
		LastRunState:      row.LastRunState,
		Settings: contract.BotSettings{
			Name:           row.Name,
			Purpose:        row.Purpose,
			ProfileId:      row.ProfileID,
			Instructions:   row.Instructions,
			Soul:           row.Soul,
			ScheduleTime:   row.ScheduleTime,
			ScheduleZone:   row.ScheduleZone,
			WakeOnNeedsYou: row.WakeOnNeedsYou,
		},
	}
	return botFull{
		BotRecord:         rec,
		RevisionCreatedAt: row.RevisionCreatedAt,
		PausedBy:          row.PausedBy,
		ResumedAt:         row.ResumedAt,
	}, nil
}

func readBot(q *Request, id string) (contract.BotRecord, error) {
	b, err := readBotFull(q, id)
	return b.BotRecord, err
}

func botGet(q *Request, r *contract.BotGetRequest) (contract.BotGetReply, error) {
	bot, err := readBot(q, r.BotId)
	return contract.BotGetReply{Bot: bot}, err
}

func botDirects(q *Request, bot contract.BotRecord) error {
	if q.person != nil && (q.person.Email == bot.Director || fullAccess(q.person)) {
		return nil
	}
	return fail(403, "Bot is directed by someone else")
}

func validateBotSettings(q *Request, settings contract.BotSettings) error {
	if strings.TrimSpace(settings.Name) == "" {
		return fail(400, "Enter a bot name")
	}
	if err := validateBotSchedule(settings.ScheduleTime, settings.ScheduleZone); err != nil {
		return err
	}
	_, err := readTrustedProfile(q, settings.ProfileId)
	return err
}

func validateBotSchedule(scheduleTime, scheduleZone string) error {
	if scheduleTime != "" {
		t, err := time.Parse("15:04", scheduleTime)
		if err != nil || t.Format("15:04") != scheduleTime {
			return fail(400, "Enter the local schedule time as HH:MM")
		}
		if scheduleZone == "" || scheduleZone == "Local" {
			return fail(400, "Enter a schedule time zone")
		}
		if _, err := time.LoadLocation(scheduleZone); err != nil {
			return fail(400, "Unknown schedule time zone")
		}
	} else if scheduleZone != "" {
		return fail(400, "Enter a schedule time with its time zone")
	}
	return nil
}

func botSettingsSave(q *Request, r *contract.BotSettingsSaveRequest) (contract.BotSettingsSaveReply, error) {
	var out contract.BotSettingsSaveReply
	if !botIDPattern.MatchString(r.BotId) || r.BaseRevision < 0 {
		return out, fail(400, "Expected a bot UUID and a nonnegative revision")
	}
	if projectSettingsOf(q, r.ProjectId) == nil {
		return out, errNotFound
	}
	var prevWakeOnNeedsYou bool
	var prevName string
	bot, err := readBot(q, r.BotId)
	switch {
	case err == nil:
		if bot.ProjectId != r.ProjectId {
			return out, errNotFound
		}
		if err := botDirects(q, bot); err != nil {
			return out, err
		}
		if (bot.Revision == r.BaseRevision || bot.Revision-1 == r.BaseRevision) && bot.Settings == r.Settings {
			return contract.BotSettingsSaveReply{Bot: bot}, nil
		}
		if bot.Revision != r.BaseRevision {
			return out, fail(409, "Bot settings changed. Reload before saving")
		}
		prevWakeOnNeedsYou = bot.Settings.WakeOnNeedsYou
		prevName = bot.Settings.Name
	case errors.Is(err, errNotFound):
		if r.BaseRevision != 0 {
			return out, err
		}
	default:
		return out, err
	}
	if err := validateBotSettings(q, r.Settings); err != nil {
		return out, err
	}
	var changed int64
	if r.BaseRevision == 0 {
		changed, err = q.db().BotCreate(q.ctx(), db.BotCreateParams{ID: r.BotId, ProjectID: r.ProjectId, Director: q.person.Email})
	} else {
		changed, err = q.db().BotSettingsAdvance(q.ctx(), db.BotSettingsAdvanceParams{ID: r.BotId, Revision: int32(r.BaseRevision)})
	}
	if err != nil {
		return out, err
	}
	if changed != 1 {
		if r.BaseRevision == 0 {
			return out, errNotFound
		}
		return out, fail(409, "Bot settings changed. Reload before saving")
	}
	settings := r.Settings
	revision := r.BaseRevision + 1
	err = q.db().BotSettingsRevisionInsert(q.ctx(), db.BotSettingsRevisionInsertParams{
		BotID: r.BotId, Revision: int32(revision), Name: settings.Name, Purpose: settings.Purpose,
		ProfileID: settings.ProfileId, Instructions: settings.Instructions, Soul: settings.Soul,
		ScheduleTime: settings.ScheduleTime, ScheduleZone: settings.ScheduleZone, WakeOnNeedsYou: settings.WakeOnNeedsYou,
	})
	if err != nil {
		return out, err
	}
	if err := audit(q, "bot:"+r.BotId, "settingsRevision", r.BaseRevision, revision, auditExtra{ProjectID: &r.ProjectId}); err != nil {
		return out, err
	}
	saved, err := readBotFull(q, r.BotId)
	if err != nil {
		return out, err
	}
	if _, err := ensureBotHomeSession(q, &saved); err != nil {
		return out, err
	}
	if r.BaseRevision > 0 && settings.Name != prevName && saved.HomeSessionId != nil {
		if row, err := q.db().SessionByID(q.ctx(), *saved.HomeSessionId); err == nil && deref(row.TitleBy) == "bot" {
			if _, err := q.db().TitleRename(q.ctx(), db.TitleRenameParams{ID: row.ID, Title: settings.Name, TitleBy: "bot"}); err != nil {
				return out, err
			}
		}
	}
	if r.BaseRevision == 0 || (!prevWakeOnNeedsYou && settings.WakeOnNeedsYou) {
		if err := initBotWakeCursor(q, saved.Id, saved.ProjectId); err != nil {
			return out, err
		}
	}
	if q.app != nil {
		afterCommit(q, func() { q.app.wakeBots() })
	}
	bot, err = readBot(q, r.BotId)
	return contract.BotSettingsSaveReply{Bot: bot}, err
}

func botMemorySave(q *Request, r *contract.BotMemorySaveRequest) (contract.BotMemorySaveReply, error) {
	var out contract.BotMemorySaveReply
	if r.BaseRevision < 0 {
		return out, fail(400, "Expected a nonnegative revision")
	}
	bot, err := readBot(q, r.BotId)
	if err != nil {
		return out, err
	}
	if err := botDirects(q, bot); err != nil {
		return out, err
	}
	if (bot.MemoryRevision == r.BaseRevision || bot.MemoryRevision-1 == r.BaseRevision) && bot.Memory == r.Memory {
		return contract.BotMemorySaveReply{Bot: bot}, nil
	}
	if bot.MemoryRevision != r.BaseRevision {
		return out, fail(409, "Bot memory changed. Reload before saving")
	}
	changed, err := q.db().BotMemoryAdvance(q.ctx(), db.BotMemoryAdvanceParams{ID: r.BotId, MemoryRevision: int32(r.BaseRevision)})
	if err != nil {
		return out, err
	}
	if changed != 1 {
		return out, fail(409, "Bot memory changed. Reload before saving")
	}
	revision := r.BaseRevision + 1
	if err := q.db().BotMemoryRevisionInsert(q.ctx(), db.BotMemoryRevisionInsertParams{BotID: r.BotId, Revision: int32(revision), Memory: r.Memory}); err != nil {
		return out, err
	}
	if err := audit(q, "bot:"+r.BotId, "memoryRevision", r.BaseRevision, revision, auditExtra{ProjectID: &bot.ProjectId}); err != nil {
		return out, err
	}
	bot, err = readBot(q, r.BotId)
	return contract.BotMemorySaveReply{Bot: bot}, err
}
