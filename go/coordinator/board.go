// Swarm work board (docs/designs/fleet-swarm.md): durable work_plan and board_message operations
// (work_plan_set, board_post, board_read, work_board_get) shared uniformly by agents and the operator UI.
package main

import (
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

func init() {
	op("work_plan_set", workPlanSet)
	op("board_post", boardPost)
	op("board_read", boardRead)
	op("work_board_get", workBoardGet)
}

var boardMentionRe = regexp.MustCompile(`(?:^|[^\w.@/])@([A-Za-z0-9][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)*)`)

type workPlanPayload struct {
	Threads []contract.WorkPlanThread `json:"threads"`
}

func boardNormalizeScope(kind contract.BoardScopeKind, scopeID string) (string, error) {
	if !kind.Valid() {
		return "", fail(400, "Invalid scopeKind: use task, pipeline_run, or session")
	}
	id := strings.TrimSpace(scopeID)
	if id == "" {
		return "", fail(400, "Expected scopeId")
	}
	return id, nil
}

// boardScopeEntityProject returns the project_id of the underlying task, session, or pipeline_run under current scope.
func boardScopeEntityProject(q *Request, kind contract.BoardScopeKind, id string) (*string, bool, error) {
	switch kind {
	case contract.BoardScopeKindTask:
		p, err := q.db().BoardTaskProject(q.ctx(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return p, err == nil, err
	case contract.BoardScopeKindSession:
		p, err := q.db().BoardSessionProject(q.ctx(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return p, err == nil, err
	case contract.BoardScopeKindPipelineRun:
		p, err := q.db().BoardPipelineRunProject(q.ctx(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return p, err == nil, err
	default:
		return nil, false, fail(400, "Invalid scopeKind")
	}
}

// boardScopeCheckProject verifies the project exists and the scope (existing plan or underlying entity) does not belong to another project.
func boardScopeCheckProject(q *Request, projectID string, kind contract.BoardScopeKind, scopeID string) error {
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return fail(400, "Expected projectId")
	}
	ok, err := q.db().AgentProjectLive(q.ctx(), pid)
	if err != nil {
		return err
	}
	if !ok {
		return fail(404, "Unknown project")
	}
	return unscopedNoVal(q, func() error {
		wpProj, err := q.db().BoardWorkPlanScopeProject(q.ctx(), db.BoardWorkPlanScopeProjectParams{
			ScopeKind: string(kind),
			ScopeID:   scopeID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && wpProj != pid {
			return fail(404, "Unknown "+string(kind))
		}
		entProj, found, err := boardScopeEntityProject(q, kind, scopeID)
		if err != nil {
			return err
		}
		if found && deref(entProj) != pid {
			return fail(404, "Unknown "+string(kind))
		}
		return nil
	})
}

func unscopedNoVal(q *Request, fn func() error) error {
	_, err := unscoped(q, func() (struct{}, error) { return struct{}{}, fn() })
	return err
}

// boardScopeResolveVisible checks caller visibility under RLS across work_plan, board_message, and the underlying scope entity.
func boardScopeResolveVisible(q *Request, kind contract.BoardScopeKind, scopeID string) (*string, *db.WorkPlan, error) {
	var projectID *string
	var wpRow *db.WorkPlan
	wp, err := q.db().WorkPlanGet(q.ctx(), db.WorkPlanGetParams{
		ScopeKind: string(kind),
		ScopeID:   scopeID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	if err == nil {
		wpRow = &wp
		p := wp.ProjectID
		projectID = &p
	}
	if projectID == nil {
		mp, err := q.db().BoardAnyMessageProject(q.ctx(), db.BoardAnyMessageProjectParams{
			ScopeKind: string(kind),
			ScopeID:   scopeID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, err
		}
		if err == nil {
			projectID = &mp
		}
	}
	if projectID == nil {
		entProj, found, err := boardScopeEntityProject(q, kind, scopeID)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			return nil, nil, errNotFound
		}
		projectID = entProj
	}
	return projectID, wpRow, nil
}

func workPlanValidateAndNormalize(threads []contract.WorkPlanThread) ([]contract.WorkPlanThread, error) {
	if threads == nil {
		return nil, fail(400, "Expected threads")
	}
	seenThreads := map[string]bool{}
	seenTasks := map[string]bool{}
	out := make([]contract.WorkPlanThread, 0, len(threads))
	for _, th := range threads {
		tid := strings.TrimSpace(th.Id)
		ttitle := strings.TrimSpace(th.Title)
		if tid == "" {
			return nil, fail(400, "Each thread requires a non-empty id")
		}
		if ttitle == "" {
			return nil, fail(400, "Each thread requires a non-empty title")
		}
		if seenThreads[tid] {
			return nil, fail(400, "Duplicate thread id: "+tid)
		}
		seenThreads[tid] = true
		status := contract.WorkPlanThreadStatusPlanned
		if th.Status != nil {
			if !th.Status.Valid() {
				return nil, fail(400, "Invalid thread status: "+string(*th.Status))
			}
			status = *th.Status
		}
		if th.Tasks == nil {
			return nil, fail(400, "Thread "+tid+" requires tasks array")
		}
		tasks := make([]contract.WorkPlanTask, 0, len(th.Tasks))
		for _, tk := range th.Tasks {
			kid := strings.TrimSpace(tk.Id)
			ktitle := strings.TrimSpace(tk.Title)
			if kid == "" {
				return nil, fail(400, "Each task requires a non-empty id")
			}
			if ktitle == "" {
				return nil, fail(400, "Each task requires a non-empty title")
			}
			if seenTasks[kid] {
				return nil, fail(400, "Duplicate task id: "+kid)
			}
			seenTasks[kid] = true
			tStatus := contract.WorkPlanTaskStatusPending
			if tk.Status != nil {
				if !tk.Status.Valid() {
					return nil, fail(400, "Invalid task status: "+string(*tk.Status))
				}
				tStatus = *tk.Status
			}
			if tk.WorkspaceMode != nil && !tk.WorkspaceMode.Valid() {
				return nil, fail(400, "Invalid workspaceMode: "+string(*tk.WorkspaceMode))
			}
			tk.Id = kid
			tk.Title = ktitle
			tk.Status = &tStatus
			tasks = append(tasks, tk)
		}
		out = append(out, contract.WorkPlanThread{
			Id:     tid,
			Title:  ttitle,
			Goal:   th.Goal,
			Status: &status,
			Tasks:  tasks,
		})
	}
	for _, th := range out {
		for _, tk := range th.Tasks {
			for _, dep := range tk.DependsOn {
				d := strings.TrimSpace(dep)
				if d == "" || d == tk.Id || !seenTasks[d] {
					return nil, fail(400, "Invalid task dependency "+dep+" in task "+tk.Id)
				}
			}
		}
	}
	return out, nil
}

func workPlanToContract(row db.WorkPlan) (contract.WorkPlan, error) {
	var p workPlanPayload
	if len(row.Payload) > 0 {
		if err := json.Unmarshal(row.Payload, &p); err != nil {
			return contract.WorkPlan{}, err
		}
	}
	if p.Threads == nil {
		p.Threads = []contract.WorkPlanThread{}
	}
	return contract.WorkPlan{
		Id:        row.ID,
		ProjectId: row.ProjectID,
		ScopeKind: contract.BoardScopeKind(row.ScopeKind),
		ScopeId:   row.ScopeID,
		Version:   int64(row.Version),
		Title:     row.Title,
		Goal:      row.Goal,
		Strategy:  row.Strategy,
		Threads:   p.Threads,
		CreatedAt: row.CreatedAt.UnixMilli(),
		UpdatedAt: row.UpdatedAt.UnixMilli(),
	}, nil
}

func workPlanSet(q *Request, r *contract.WorkPlanSetRequest) (contract.WorkPlanSetReply, error) {
	var out contract.WorkPlanSetReply
	scopeID, err := boardNormalizeScope(r.ScopeKind, r.ScopeId)
	if err != nil {
		return out, err
	}
	title := strings.TrimSpace(r.Title)
	if title == "" {
		return out, fail(400, "Expected title")
	}
	if err := boardScopeCheckProject(q, r.ProjectId, r.ScopeKind, scopeID); err != nil {
		return out, err
	}
	threads, err := workPlanValidateAndNormalize(r.Threads)
	if err != nil {
		return out, err
	}
	if r.ExpectedVersion != nil {
		cur, err := q.db().WorkPlanGet(q.ctx(), db.WorkPlanGetParams{
			ScopeKind: string(r.ScopeKind),
			ScopeID:   scopeID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return out, fail(409, "Work plan does not exist yet")
		}
		if int64(cur.Version) != *r.ExpectedVersion {
			return out, fail(409, "Work plan version conflict")
		}
	}
	payload := jsonBytes(workPlanPayload{Threads: threads})
	row, err := q.db().WorkPlanUpsert(q.ctx(), db.WorkPlanUpsertParams{
		ID:        newID(),
		ProjectID: strings.TrimSpace(r.ProjectId),
		ScopeKind: string(r.ScopeKind),
		ScopeID:   scopeID,
		Title:     title,
		Goal:      strings.TrimSpace(deref(r.Goal)),
		Strategy:  strings.TrimSpace(deref(r.Strategy)),
		Payload:   payload,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, fail(409, "Work plan belongs to another project or is deleted")
	}
	if err != nil {
		return out, err
	}
	plan, err := workPlanToContract(row)
	if err != nil {
		return out, err
	}
	out.Plan = plan
	return out, nil
}

func boardParseMentions(text string, explicit []string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(m string) {
		m = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m), "@"))
		if m == "" || seen[m] {
			return
		}
		seen[m] = true
		out = append(out, m)
	}
	for _, match := range boardMentionRe.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			add(match[1])
		}
	}
	for _, m := range explicit {
		add(m)
	}
	return out
}

func boardMessageToContract(row db.BoardMessage) contract.BoardMessage {
	mentions := row.Mentions
	if mentions == nil {
		mentions = []string{}
	}
	payload := map[string]json.RawMessage{}
	if len(row.Payload) > 0 {
		_ = json.Unmarshal(row.Payload, &payload)
	}
	if payload == nil {
		payload = map[string]json.RawMessage{}
	}
	return contract.BoardMessage{
		Id:                row.ID,
		ProjectId:         row.ProjectID,
		ScopeKind:         contract.BoardScopeKind(row.ScopeKind),
		ScopeId:           row.ScopeID,
		Seq:               row.Seq,
		ThreadId:          row.ThreadID,
		AuthorMember:      row.AuthorMember,
		AuthorRole:        row.AuthorRole,
		AuthorSessionId:   row.AuthorSessionID,
		AuthorExecutionId: row.AuthorExecutionID,
		Kind:              contract.BoardMessageKind(row.Kind),
		Mentions:          mentions,
		Text:              row.Text,
		ArtifactId:        row.ArtifactID,
		Payload:           payload,
		CreatedAt:         row.CreatedAt.UnixMilli(),
	}
}

type boardMemberRef struct {
	member    string
	sessionID string
	role      string
	threadID  string
}

func boardResolveWakeTargets(q *Request, scopeKind, scopeID, authorMember, authorSessionID string, mentions []string) ([]contract.BoardWakeTarget, error) {
	if len(mentions) == 0 {
		return []contract.BoardWakeTarget{}, nil
	}
	directory := map[string]boardMemberRef{}
	order := []string{}
	register := func(ref boardMemberRef) {
		if ref.member == "" || ref.sessionID == "" {
			return
		}
		if cur, ok := directory[ref.member]; ok {
			if cur.sessionID == "" {
				cur.sessionID = ref.sessionID
			}
			if cur.role == "" {
				cur.role = ref.role
			}
			if cur.threadID == "" {
				cur.threadID = ref.threadID
			}
			directory[ref.member] = cur
			return
		}
		directory[ref.member] = ref
		order = append(order, ref.member)
	}
	wp, err := q.db().WorkPlanGet(q.ctx(), db.WorkPlanGetParams{
		ScopeKind: scopeKind,
		ScopeID:   scopeID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		plan, err := workPlanToContract(wp)
		if err != nil {
			return nil, err
		}
		for _, th := range plan.Threads {
			for _, tk := range th.Tasks {
				m := strings.TrimSpace(deref(tk.OwnerMember))
				sid := strings.TrimSpace(deref(tk.SessionId))
				if m != "" && sid != "" {
					register(boardMemberRef{
						member:    m,
						sessionID: sid,
						role:      firstString(strings.TrimSpace(deref(tk.Role)), "worker"),
						threadID:  th.Id,
					})
				}
			}
		}
	}
	posts, err := q.db().BoardMemberPosts(q.ctx(), db.BoardMemberPostsParams{
		ScopeKind: scopeKind,
		ScopeID:   scopeID,
	})
	if err != nil {
		return nil, err
	}
	for _, p := range posts {
		if p.AuthorMember != "" && p.AuthorSessionID != "" {
			register(boardMemberRef{
				member:    p.AuthorMember,
				sessionID: p.AuthorSessionID,
				role:      p.AuthorRole,
				threadID:  p.ThreadID,
			})
		}
	}
	targets := []contract.BoardWakeTarget{}
	seenSessions := map[string]bool{}
	addTarget := func(ref boardMemberRef) {
		if ref.sessionID == "" || seenSessions[ref.sessionID] {
			return
		}
		seenSessions[ref.sessionID] = true
		targets = append(targets, contract.BoardWakeTarget{
			Member:    ref.member,
			SessionId: ref.sessionID,
			Role:      nonEmptyPtr(ref.role),
			ThreadId:  nonEmptyPtr(ref.threadID),
		})
	}
	if slices.Contains(mentions, "all") {
		for _, name := range order {
			ref := directory[name]
			if ref.member == authorMember || (authorSessionID != "" && ref.sessionID == authorSessionID) {
				continue
			}
			addTarget(ref)
		}
	}
	for _, m := range mentions {
		if m == "all" {
			continue
		}
		if ref, ok := directory[m]; ok {
			addTarget(ref)
		}
	}
	return targets, nil
}

func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func boardPost(q *Request, r *contract.BoardPostRequest) (contract.BoardPostReply, error) {
	var out contract.BoardPostReply
	scopeID, err := boardNormalizeScope(r.ScopeKind, r.ScopeId)
	if err != nil {
		return out, err
	}
	authorMember := strings.TrimSpace(r.AuthorMember)
	if authorMember == "" {
		return out, fail(400, "Expected authorMember")
	}
	if strings.TrimSpace(r.Text) == "" {
		return out, fail(400, "Expected text")
	}
	if err := boardScopeCheckProject(q, r.ProjectId, r.ScopeKind, scopeID); err != nil {
		return out, err
	}
	kind := contract.BoardMessageKindMessage
	if r.Kind != nil {
		if !r.Kind.Valid() {
			return out, fail(400, "Invalid message kind")
		}
		kind = *r.Kind
	}
	authorRole := firstString(strings.TrimSpace(deref(r.AuthorRole)), "worker")
	var authorExecID, authorSessID *string
	if r.AuthorExecutionId != nil && strings.TrimSpace(*r.AuthorExecutionId) != "" {
		eid := strings.TrimSpace(*r.AuthorExecutionId)
		info, err := q.db().BoardExecutionInfo(q.ctx(), eid)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, fail(404, "Unknown authorExecutionId")
		}
		if err != nil {
			return out, err
		}
		authorExecID = &info.ID
		if info.SessionID != nil && *info.SessionID != "" {
			authorSessID = info.SessionID
		}
	} else if strings.HasPrefix(q.agent, "execution:") {
		eid := strings.TrimPrefix(q.agent, "execution:")
		if eid != "" {
			info, err := q.db().BoardExecutionInfo(q.ctx(), eid)
			if err == nil {
				authorExecID = &info.ID
				if info.SessionID != nil && *info.SessionID != "" {
					authorSessID = info.SessionID
				}
			}
		}
	}
	if r.AuthorSessionId != nil && strings.TrimSpace(*r.AuthorSessionId) != "" {
		sid := strings.TrimSpace(*r.AuthorSessionId)
		ok, err := q.db().BoardSessionExists(q.ctx(), sid)
		if err != nil {
			return out, err
		}
		if !ok {
			return out, fail(404, "Unknown authorSessionId")
		}
		authorSessID = &sid
	}
	var artifactID *string
	if r.ArtifactId != nil && strings.TrimSpace(*r.ArtifactId) != "" {
		aid := strings.TrimSpace(*r.ArtifactId)
		ap, err := q.db().BoardArtifactProject(q.ctx(), aid)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, fail(404, "Unknown artifactId")
		}
		if err != nil {
			return out, err
		}
		if deref(ap) != strings.TrimSpace(r.ProjectId) {
			return out, fail(409, "Artifact belongs to another project")
		}
		artifactID = &aid
	}
	mentions := boardParseMentions(r.Text, r.Mentions)
	payloadBytes := []byte("{}")
	if r.Payload != nil {
		payloadBytes = jsonBytes(r.Payload)
	}
	nextSeq, err := q.db().BoardNextSeq(q.ctx(), db.BoardNextSeqParams{
		ScopeKind: string(r.ScopeKind),
		ScopeID:   scopeID,
	})
	if err != nil {
		return out, err
	}
	row, err := q.db().BoardMessageInsert(q.ctx(), db.BoardMessageInsertParams{
		ID:                newID(),
		ProjectID:         strings.TrimSpace(r.ProjectId),
		ScopeKind:         string(r.ScopeKind),
		ScopeID:           scopeID,
		Seq:               nextSeq,
		ThreadID:          strings.TrimSpace(deref(r.ThreadId)),
		AuthorMember:      authorMember,
		AuthorRole:        authorRole,
		AuthorSessionID:   authorSessID,
		AuthorExecutionID: authorExecID,
		Kind:              string(kind),
		Mentions:          mentions,
		Text:              r.Text,
		ArtifactID:        artifactID,
		Payload:           payloadBytes,
	})
	if err != nil {
		return out, err
	}
	wakeTargets, err := boardResolveWakeTargets(q, string(r.ScopeKind), scopeID, authorMember, deref(authorSessID), mentions)
	if err != nil {
		return out, err
	}
	out.Message = boardMessageToContract(row)
	out.WakeTargets = wakeTargets
	return out, nil
}

func boardRead(q *Request, r *contract.BoardReadRequest) (contract.BoardReadReply, error) {
	var out contract.BoardReadReply
	scopeID, err := boardNormalizeScope(r.ScopeKind, r.ScopeId)
	if err != nil {
		return out, err
	}
	if _, _, err := boardScopeResolveVisible(q, r.ScopeKind, scopeID); err != nil {
		return out, err
	}
	var since int64
	if r.Since != nil {
		if *r.Since < 0 {
			return out, fail(400, "Invalid since")
		}
		since = *r.Since
	}
	limit := 100
	if r.Limit != nil {
		limit = pageLimit(float64(*r.Limit), 500)
	}
	var threadID, kind, mention, authorMember *string
	if r.ThreadId != nil && strings.TrimSpace(*r.ThreadId) != "" {
		threadID = ptr(strings.TrimSpace(*r.ThreadId))
	}
	if r.Kind != nil {
		if !r.Kind.Valid() {
			return out, fail(400, "Invalid message kind")
		}
		kind = ptr(string(*r.Kind))
	}
	if r.Mention != nil {
		m := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(*r.Mention), "@"))
		if m != "" {
			mention = &m
		}
	}
	if r.AuthorMember != nil && strings.TrimSpace(*r.AuthorMember) != "" {
		authorMember = ptr(strings.TrimSpace(*r.AuthorMember))
	}
	rows, err := q.db().BoardMessagesRead(q.ctx(), db.BoardMessagesReadParams{
		ScopeKind:    string(r.ScopeKind),
		ScopeID:      scopeID,
		Since:        since,
		ThreadID:     threadID,
		Kind:         kind,
		Mention:      mention,
		AuthorMember: authorMember,
		Lim:          int32(limit + 1),
	})
	if err != nil {
		return out, err
	}
	headSeq, err := q.db().BoardHeadSeq(q.ctx(), db.BoardHeadSeqParams{
		ScopeKind: string(r.ScopeKind),
		ScopeID:   scopeID,
	})
	if err != nil {
		return out, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	msgs := make([]contract.BoardMessage, 0, len(rows))
	for _, row := range rows {
		msgs = append(msgs, boardMessageToContract(row))
	}
	out.ScopeKind = r.ScopeKind
	out.ScopeId = scopeID
	out.Messages = msgs
	out.HeadSeq = headSeq
	out.HasMore = hasMore
	return out, nil
}

type boardExecInfo struct {
	id           string
	sessionID    *string
	state        string
	harness      *string
	model        *string
	hostID       *string
	startedAt    *time.Time
	endedAt      *time.Time
	wallMs       *int64
	tokensInput  *int64
	tokensOutput *int64
	hasSpend     bool
	costCurrency *string
	costBasis    *string
	costValue    *float64
	costUnpriced int64
	spendPayload []byte
}

func boardExecFromSessionRow(r db.BoardRosterSessionExecutionsRow) boardExecInfo {
	return boardExecInfo{
		id:           r.ID,
		sessionID:    r.SessionID,
		state:        r.State,
		harness:      r.Harness,
		model:        r.Model,
		hostID:       r.HostID,
		startedAt:    r.StartedAt,
		endedAt:      r.EndedAt,
		wallMs:       r.WallMs,
		tokensInput:  r.TokensInput,
		tokensOutput: r.TokensOutput,
		hasSpend:     r.HasSpend,
		costCurrency: r.CostCurrency,
		costBasis:    r.CostBasis,
		costValue:    r.CostValue,
		costUnpriced: r.CostUnpriced,
		spendPayload: r.SpendPayload,
	}
}

func boardExecFromIDRow(r db.BoardRosterExecutionsByIDsRow) boardExecInfo {
	return boardExecInfo{
		id:           r.ID,
		sessionID:    r.SessionID,
		state:        r.State,
		harness:      r.Harness,
		model:        r.Model,
		hostID:       r.HostID,
		startedAt:    r.StartedAt,
		endedAt:      r.EndedAt,
		wallMs:       r.WallMs,
		tokensInput:  r.TokensInput,
		tokensOutput: r.TokensOutput,
		hasSpend:     r.HasSpend,
		costCurrency: r.CostCurrency,
		costBasis:    r.CostBasis,
		costValue:    r.CostValue,
		costUnpriced: r.CostUnpriced,
		spendPayload: r.SpendPayload,
	}
}

func boardAggregateCosts(execs []boardExecInfo) ([]contract.CostFigure, error) {
	type costGroupKey struct {
		currency string
		basis    contract.ResourceBasis
	}
	type costGroupAcc struct {
		value  float64
		prices map[string]contract.CostPriceRef
	}
	groups := map[costGroupKey]*costGroupAcc{}
	var unpricedTotal int64
	addFigure := func(cf contract.CostFigure) {
		if cf.Basis == contract.ResourceBasisUnavailable && cf.Currency == nil {
			if cf.Usages != nil {
				unpricedTotal += *cf.Usages
			} else {
				unpricedTotal++
			}
			return
		}
		if cf.Currency == nil || cf.Value == nil {
			return
		}
		k := costGroupKey{currency: *cf.Currency, basis: cf.Basis}
		acc := groups[k]
		if acc == nil {
			acc = &costGroupAcc{prices: map[string]contract.CostPriceRef{}}
			groups[k] = acc
		}
		acc.value += *cf.Value
		for _, pr := range cf.Prices {
			acc.prices[pr.Id] = pr
		}
	}
	for _, ex := range execs {
		if !ex.hasSpend {
			continue
		}
		if len(ex.spendPayload) > 0 {
			figs, err := decodeCostFigures(ex.spendPayload)
			if err != nil {
				return nil, err
			}
			for _, cf := range figs {
				addFigure(cf)
			}
			continue
		}
		if ex.costUnpriced > 0 {
			unpricedTotal += ex.costUnpriced
		}
		if ex.costCurrency != nil && ex.costBasis != nil && ex.costValue != nil {
			addFigure(contract.CostFigure{
				Currency: ex.costCurrency,
				Basis:    contract.ResourceBasis(*ex.costBasis),
				Value:    ex.costValue,
			})
		}
	}
	out := make([]contract.CostFigure, 0, len(groups)+1)
	for k, acc := range groups {
		v := acc.value
		curr := k.currency
		cf := contract.CostFigure{
			Value:    &v,
			Currency: &curr,
			Basis:    k.basis,
		}
		if len(acc.prices) > 0 {
			prices := make([]contract.CostPriceRef, 0, len(acc.prices))
			for _, pr := range acc.prices {
				prices = append(prices, pr)
			}
			cf.Prices = prices
		}
		if k.basis == contract.ResourceBasisSubscription {
			cf.Note = ptr(subscriptionCostNote)
		}
		out = append(out, cf)
	}
	if unpricedTotal > 0 {
		u := unpricedTotal
		out = append(out, contract.CostFigure{
			Value:  nil,
			Basis:  contract.ResourceBasisUnavailable,
			Reason: ptr(unavailableCostReason),
			Usages: &u,
		})
	}
	sortCostFigures(out)
	return out, nil
}

type boardRosterDraft struct {
	memberName     string
	role           string
	threadID       string
	sessionID      string
	executionID    string
	workspaceMode  string
	plannedHarness string
	plannedModel   string
	lastPostSeq    *int64
}

func workBoardGet(q *Request, r *contract.WorkBoardGetRequest) (contract.WorkBoardGetReply, error) {
	var out contract.WorkBoardGetReply
	scopeID, err := boardNormalizeScope(r.ScopeKind, r.ScopeId)
	if err != nil {
		return out, err
	}
	projectID, wpRow, err := boardScopeResolveVisible(q, r.ScopeKind, scopeID)
	if err != nil {
		return out, err
	}
	var since int64
	if r.Since != nil {
		if *r.Since < 0 {
			return out, fail(400, "Invalid since")
		}
		since = *r.Since
	}
	limit := 200
	if r.Limit != nil {
		limit = pageLimit(float64(*r.Limit), 500)
	}

	var plan *contract.WorkPlan
	if wpRow != nil {
		p, err := workPlanToContract(*wpRow)
		if err != nil {
			return out, err
		}
		plan = &p
	}

	msgRows, err := q.db().BoardMessagesRead(q.ctx(), db.BoardMessagesReadParams{
		ScopeKind: string(r.ScopeKind),
		ScopeID:   scopeID,
		Since:     since,
		Lim:       int32(limit),
	})
	if err != nil {
		return out, err
	}
	messages := make([]contract.BoardMessage, 0, len(msgRows))
	for _, row := range msgRows {
		messages = append(messages, boardMessageToContract(row))
	}

	counts, err := q.db().BoardScopeSummaryCounts(q.ctx(), db.BoardScopeSummaryCountsParams{
		ScopeKind: string(r.ScopeKind),
		ScopeID:   scopeID,
	})
	if err != nil {
		return out, err
	}
	memberPosts, err := q.db().BoardMemberPosts(q.ctx(), db.BoardMemberPostsParams{
		ScopeKind: string(r.ScopeKind),
		ScopeID:   scopeID,
	})
	if err != nil {
		return out, err
	}

	drafts := map[string]*boardRosterDraft{}
	order := []string{}
	ensureDraft := func(name string) *boardRosterDraft {
		d := drafts[name]
		if d == nil {
			d = &boardRosterDraft{memberName: name, role: "worker"}
			drafts[name] = d
			order = append(order, name)
		}
		return d
	}

	threadsByStatus := map[string]int64{}
	tasksByStatus := map[string]int64{}
	var tasksTotal int64
	if plan != nil {
		for _, th := range plan.Threads {
			st := string(contract.WorkPlanThreadStatusPlanned)
			if th.Status != nil {
				st = string(*th.Status)
			}
			threadsByStatus[st]++
			for _, tk := range th.Tasks {
				tasksTotal++
				tst := string(contract.WorkPlanTaskStatusPending)
				if tk.Status != nil {
					tst = string(*tk.Status)
				}
				tasksByStatus[tst]++
				m := strings.TrimSpace(deref(tk.OwnerMember))
				if m == "" {
					continue
				}
				d := ensureDraft(m)
				if d.threadID == "" {
					d.threadID = th.Id
				}
				if tk.Role != nil && strings.TrimSpace(*tk.Role) != "" {
					d.role = strings.TrimSpace(*tk.Role)
				}
				if d.sessionID == "" && tk.SessionId != nil {
					d.sessionID = strings.TrimSpace(*tk.SessionId)
				}
				if d.executionID == "" && tk.ExecutionId != nil {
					d.executionID = strings.TrimSpace(*tk.ExecutionId)
				}
				if d.workspaceMode == "" && tk.WorkspaceMode != nil {
					d.workspaceMode = string(*tk.WorkspaceMode)
				}
				if d.plannedHarness == "" && tk.Harness != nil {
					d.plannedHarness = strings.TrimSpace(*tk.Harness)
				}
				if d.plannedModel == "" && tk.Model != nil {
					d.plannedModel = strings.TrimSpace(*tk.Model)
				}
			}
		}
	}

	for _, mp := range memberPosts {
		if mp.AuthorMember == "" {
			continue
		}
		d := ensureDraft(mp.AuthorMember)
		seq := mp.LastPostSeq
		d.lastPostSeq = &seq
		if d.role == "worker" && mp.AuthorRole != "" {
			d.role = mp.AuthorRole
		}
		if d.threadID == "" && mp.ThreadID != "" {
			d.threadID = mp.ThreadID
		}
		if d.sessionID == "" && mp.AuthorSessionID != "" {
			d.sessionID = mp.AuthorSessionID
		}
		if d.executionID == "" && mp.AuthorExecutionID != "" {
			d.executionID = mp.AuthorExecutionID
		}
	}

	sessionIDSet := map[string]bool{}
	execIDSet := map[string]bool{}
	for _, name := range order {
		d := drafts[name]
		if d.sessionID != "" {
			sessionIDSet[d.sessionID] = true
		}
		if d.executionID != "" {
			execIDSet[d.executionID] = true
		}
	}

	sessionByID := map[string]db.BoardRosterSessionsRow{}
	execsBySession := map[string][]boardExecInfo{}
	if len(sessionIDSet) > 0 {
		sids := slices.Collect(maps.Keys(sessionIDSet))
		sRows, err := q.db().BoardRosterSessions(q.ctx(), sids)
		if err != nil {
			return out, err
		}
		for _, sr := range sRows {
			sessionByID[sr.ID] = sr
		}
		eRows, err := q.db().BoardRosterSessionExecutions(q.ctx(), sids)
		if err != nil {
			return out, err
		}
		for _, er := range eRows {
			if er.SessionID != nil {
				execsBySession[*er.SessionID] = append(execsBySession[*er.SessionID], boardExecFromSessionRow(er))
			}
		}
	}

	execByID := map[string]boardExecInfo{}
	if len(execIDSet) > 0 {
		eids := slices.Collect(maps.Keys(execIDSet))
		eRows, err := q.db().BoardRosterExecutionsByIDs(q.ctx(), eids)
		if err != nil {
			return out, err
		}
		for _, er := range eRows {
			execByID[er.ID] = boardExecFromIDRow(er)
		}
	}

	roster := make([]contract.WorkBoardRosterEntry, 0, len(order))
	membersByState := map[string]int64{}
	harnessCounts := map[string]int64{}
	now := time.Now().UTC()

	for _, name := range order {
		d := drafts[name]
		var sessRow *db.BoardRosterSessionsRow
		if sr, ok := sessionByID[d.sessionID]; ok {
			sessRow = &sr
		}
		var memberExecs []boardExecInfo
		if d.sessionID != "" && len(execsBySession[d.sessionID]) > 0 {
			memberExecs = execsBySession[d.sessionID]
		} else if ex, ok := execByID[d.executionID]; ok {
			memberExecs = []boardExecInfo{ex}
		}
		var latestExec *boardExecInfo
		if len(memberExecs) > 0 {
			latestExec = &memberExecs[0]
		} else if ex, ok := execByID[d.executionID]; ok {
			latestExec = &ex
		}

		var sessionCode, hostID, state, harness, model, executionID *string
		var startedAt, endedAt, durationMs, tokensIn, tokensOut *int64

		if d.executionID != "" {
			executionID = nonEmptyPtr(d.executionID)
		}
		if sessRow != nil {
			sessionCode = sessRow.Code
			hostID = sessRow.HostID
			var sp sessionPayload
			if len(sessRow.Payload) > 0 && json.Unmarshal(sessRow.Payload, &sp) == nil && len(sp.AgentConfig) > 0 {
				var cfg struct {
					Harness string `json:"harness"`
					Model   string `json:"model"`
				}
				if json.Unmarshal(sp.AgentConfig, &cfg) == nil {
					if cfg.Harness != "" {
						harness = &cfg.Harness
					} else if cfg.Model != "" {
						harness = ptr("codex")
					}
					if cfg.Model != "" {
						model = &cfg.Model
					}
				}
			}
			state = ptr("idle")
		}
		if latestExec != nil {
			executionID = nonEmptyPtr(latestExec.id)
			state = nonEmptyPtr(latestExec.state)
			if latestExec.harness != nil && *latestExec.harness != "" {
				harness = latestExec.harness
			}
			if latestExec.model != nil && *latestExec.model != "" {
				model = latestExec.model
			}
			if latestExec.hostID != nil && *latestExec.hostID != "" {
				hostID = latestExec.hostID
			}
			if latestExec.startedAt != nil {
				ms := latestExec.startedAt.UnixMilli()
				startedAt = &ms
			}
			if latestExec.endedAt != nil {
				ms := latestExec.endedAt.UnixMilli()
				endedAt = &ms
			}
			if latestExec.wallMs != nil {
				durationMs = latestExec.wallMs
			} else if latestExec.startedAt != nil {
				if latestExec.endedAt != nil {
					ms := max(0, latestExec.endedAt.Sub(*latestExec.startedAt).Milliseconds())
					durationMs = &ms
				} else if latestExec.state == "preparing" || latestExec.state == "running" || latestExec.state == "waiting" {
					ms := max(0, now.Sub(*latestExec.startedAt).Milliseconds())
					durationMs = &ms
				}
			}
		}
		if harness == nil && d.plannedHarness != "" {
			harness = nonEmptyPtr(d.plannedHarness)
		}
		if model == nil && d.plannedModel != "" {
			model = nonEmptyPtr(d.plannedModel)
		}

		var inSum, outSum int64
		var hasIn, hasOut bool
		for _, ex := range memberExecs {
			if ex.tokensInput != nil {
				inSum += *ex.tokensInput
				hasIn = true
			}
			if ex.tokensOutput != nil {
				outSum += *ex.tokensOutput
				hasOut = true
			}
		}
		if hasIn {
			tokensIn = &inSum
		}
		if hasOut {
			tokensOut = &outSum
		}
		costs, err := boardAggregateCosts(memberExecs)
		if err != nil {
			return out, err
		}

		stateKey := "unassigned"
		if state != nil && *state != "" {
			stateKey = *state
		}
		membersByState[stateKey]++
		if harness != nil && *harness != "" {
			harnessCounts[*harness]++
		}

		roster = append(roster, contract.WorkBoardRosterEntry{
			MemberName:    d.memberName,
			Role:          d.role,
			ThreadId:      d.threadID,
			SessionId:     nonEmptyPtr(d.sessionID),
			SessionCode:   sessionCode,
			ExecutionId:   executionID,
			State:         state,
			Harness:       harness,
			Model:         model,
			HostId:        hostID,
			WorkspaceMode: nonEmptyPtr(d.workspaceMode),
			StartedAt:     startedAt,
			EndedAt:       endedAt,
			DurationMs:    durationMs,
			TokensInput:   tokensIn,
			TokensOutput:  tokensOut,
			Cost:          costs,
			LastPostSeq:   d.lastPostSeq,
		})
	}

	var threadsTotal int64
	if plan != nil {
		threadsTotal = int64(len(plan.Threads))
	}

	out = contract.WorkBoardGetReply{
		ScopeKind: r.ScopeKind,
		ScopeId:   scopeID,
		ProjectId: projectID,
		Plan:      plan,
		Roster:    roster,
		Messages:  messages,
		HeadSeq:   counts.HeadSeq,
		Summary: contract.WorkBoardSummary{
			ThreadsTotal:     threadsTotal,
			ThreadsByStatus:  threadsByStatus,
			TasksTotal:       tasksTotal,
			TasksByStatus:    tasksByStatus,
			MembersTotal:     int64(len(roster)),
			MembersByState:   membersByState,
			MessagesTotal:    counts.MessagesTotal,
			BlockersCount:    counts.BlockersCount,
			ScreenshotsCount: counts.ScreenshotsCount,
			HarnessCounts:    harnessCounts,
		},
	}
	return out, nil
}
