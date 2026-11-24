// Persistent bot settings and scoped memory. Fleet sessions and executions own runtime state.
package main

import (
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
}

func botList(q *Request, r *contract.BotListRequest) (contract.BotListReply, error) {
	out := contract.BotListReply{Bots: []contract.BotListItem{}}
	if r.ProjectId != nil && projectSettingsOf(q, *r.ProjectId) == nil {
		return out, errNotFound
	}
	rows, err := q.db().BotList(q.ctx(), db.BotListParams{ProjectID: r.ProjectId, ProjectIds: r.ProjectIds, ExcludeProjectIds: r.ExcludeProjectIds})
	for _, row := range rows {
		out.Bots = append(out.Bots, contract.BotListItem{
			Id: row.ID, ProjectId: row.ProjectID, Name: row.Name, Purpose: row.Purpose, Revision: int64(row.Revision),
		})
	}
	return out, err
}

func readBot(q *Request, id string) (contract.BotRecord, error) {
	row, err := q.db().BotGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.BotRecord{}, errNotFound
	}
	if err != nil {
		return contract.BotRecord{}, err
	}
	return contract.BotRecord{
		Id: row.ID, ProjectId: row.ProjectID, Director: row.Director,
		Revision: int64(row.Revision), Memory: row.Memory, MemoryRevision: int64(row.MemoryRevision),
		Settings: contract.BotSettings{
			Name: row.Name, Purpose: row.Purpose, ProfileId: row.ProfileID, Instructions: row.Instructions,
			Soul: row.Soul, ScheduleTime: row.ScheduleTime, ScheduleZone: row.ScheduleZone, WakeOnNeedsYou: row.WakeOnNeedsYou,
		},
	}, nil
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
	if settings.ScheduleTime != "" {
		t, err := time.Parse("15:04", settings.ScheduleTime)
		if err != nil || t.Format("15:04") != settings.ScheduleTime {
			return fail(400, "Enter the local schedule time as HH:MM")
		}
		if settings.ScheduleZone == "" || settings.ScheduleZone == "Local" {
			return fail(400, "Enter a schedule time zone")
		}
		if _, err := time.LoadLocation(settings.ScheduleZone); err != nil {
			return fail(400, "Unknown schedule time zone")
		}
	} else if settings.ScheduleZone != "" {
		return fail(400, "Enter a schedule time with its time zone")
	}
	_, err := readTrustedProfile(q, settings.ProfileId)
	return err
}

func botSettingsSave(q *Request, r *contract.BotSettingsSaveRequest) (contract.BotSettingsSaveReply, error) {
	var out contract.BotSettingsSaveReply
	if !botIDPattern.MatchString(r.BotId) || r.BaseRevision < 0 {
		return out, fail(400, "Expected a bot UUID and a nonnegative revision")
	}
	if projectSettingsOf(q, r.ProjectId) == nil {
		return out, errNotFound
	}
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
