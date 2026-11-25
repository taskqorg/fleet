package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("publishing_capabilities", publishingCapabilities)
	op("publishing_status", publishingStatus)
	op("publishing_publish_draft", publishingPublishDraft)
	op("publishing_record_code_review", publishingRecordCodeReview)
	op("publishing_mark_ready", publishingMarkReady)
	op("publishing_request_merge", publishingRequestMerge)
	op("publishing_cancel_merge_request", publishingCancelMergeRequest)
	hostRoute(contract.RoutePublishingRecord, publishingRecordRoute)
}

const publishingCommandTimeout = 45 * time.Second

type publishingProjectSettings struct {
	runSettings
	Worktree *struct {
		BranchPrefix string `json:"branchPrefix"`
	} `json:"worktree"`
	Integration *struct {
		Mode        string `json:"mode"`
		AutoMerge   bool   `json:"autoMerge"`
		AutoAccept  *bool  `json:"autoAccept"`
		MergeMethod string `json:"mergeMethod"`
	} `json:"integration"`
}

type publishingCmdOutcome struct {
	outcome string
	errMsg  string
	result  json.RawMessage
}

var (
	pubMu          sync.Mutex
	pubRecords     = map[string]contract.PublishingRecord{}
	pubCmdOutcomes = map[string]publishingCmdOutcome{}
	pubCmdWaiters  = map[string]chan struct{}{}
	pubIdempotent  = map[string]json.RawMessage{}
)

func publishingRecordKey(projectID string, r contract.PublishingRecord) string {
	return fmt.Sprintf("%s|%s|%s|%s#%d", projectID, r.Provider, strings.ToLower(r.Host), strings.ToLower(r.Repo), r.Number)
}

func publishingIdempotencyKey(projectID, action, requestID string) string {
	return projectID + "|" + action + "|" + requestID
}

func publishingRegisterWaiter(cmdID string) (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	pubMu.Lock()
	if _, done := pubCmdOutcomes[cmdID]; done {
		select {
		case ch <- struct{}{}:
		default:
		}
	} else {
		pubCmdWaiters[cmdID] = ch
	}
	pubMu.Unlock()
	return ch, func() {
		pubMu.Lock()
		if cur, ok := pubCmdWaiters[cmdID]; ok && cur == ch {
			delete(pubCmdWaiters, cmdID)
		}
		pubMu.Unlock()
	}
}

func publishingWakeCommand(cmdID string) {
	if cmdID == "" {
		return
	}
	pubMu.Lock()
	ch := pubCmdWaiters[cmdID]
	delete(pubCmdWaiters, cmdID)
	pubMu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func publishingNotifyCommand(cmdID, status, errMsg string) {
	if cmdID == "" {
		return
	}
	pubMu.Lock()
	if _, exists := pubCmdOutcomes[cmdID]; !exists {
		msg := errMsg
		if msg == "" && status != "completed" {
			msg = "publishing command " + status
		}
		pubCmdOutcomes[cmdID] = publishingCmdOutcome{outcome: status, errMsg: msg}
	}
	ch := pubCmdWaiters[cmdID]
	delete(pubCmdWaiters, cmdID)
	pubMu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func publishingAwaitCommand(ctx context.Context, cmdID string, timeout time.Duration) (publishingCmdOutcome, error) {
	ch, cleanup := publishingRegisterWaiter(cmdID)
	defer cleanup()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-ch:
		pubMu.Lock()
		out, ok := pubCmdOutcomes[cmdID]
		pubMu.Unlock()
		if !ok {
			return publishingCmdOutcome{}, fail(502, "Publishing command finished without outcome")
		}
		return out, nil
	case <-waitCtx.Done():
		if ctx.Err() != nil {
			return publishingCmdOutcome{}, fail(499, "Request cancelled waiting for host publishing result")
		}
		return publishingCmdOutcome{}, fail(504, "Timed out waiting for host publishing result")
	}
}

func hostCapsHaveRepo(caps []string, ref contract.RepoRef) bool {
	if contract.HasCapability(caps, contract.PublishingHostCapability(ref.Provider, ref.Host, ref.Slug)) {
		return true
	}
	if contract.HasCapability(caps, contract.ForgeCapability(ref.Slug)) {
		return true
	}
	if contract.HasCapability(caps, contract.ForgeCapability(ref.Host+"/"+ref.Slug)) {
		return true
	}
	if ref.Provider == "gitlab" {
		for _, g := range contract.SlugGroupGrants(ref.Slug) {
			if contract.HasCapability(caps, contract.PublishingHostCapability(ref.Provider, ref.Host, g)) {
				return true
			}
		}
	}
	return false
}

func hostHasProjectPublishingCapability(caps []string, settings runSettings, hostID string) bool {
	if len(settings.Repos) == 0 {
		return false
	}
	var envRepo *string
	for _, co := range settings.Environments {
		if co.HostID == hostID {
			envRepo = co.Repo
			break
		}
	}
	for _, r := range settings.Repos {
		if envRepo == nil || r.ID == *envRepo {
			ref, ok := contract.ParseRepoRemote(r.Remote, r.Forge, r.Host)
			if !ok {
				legacy := strings.TrimSuffix(strings.TrimPrefix(r.Remote, "https://github.com/"), ".git")
				return legacy != "" && contract.HasCapability(caps, contract.ForgeCapability(legacy))
			}
			return hostCapsHaveRepo(caps, ref)
		}
	}
	return false
}

type publishingHostView struct {
	id     string
	caps   []string
	holder string
}

func publishingOnlineHosts(q *Request) ([]publishingHostView, error) {
	rows, err := q.db().HostObservations(q.ctx())
	if err != nil {
		return nil, err
	}
	out := make([]publishingHostView, 0, len(rows))
	now := time.Now()
	for _, r := range rows {
		hr, herr := hostRecordByID(q, r.ID)
		if herr != nil {
			return nil, herr
		}
		if hr != nil && hr.removed() {
			continue
		}
		seenAt := timeMs(r.LastSeenAt)
		if q.app != nil {
			seenAt = q.app.effectiveHostSeen(r.ID, seenAt)
		}
		if seenAt <= 0 || now.Sub(time.UnixMilli(int64(seenAt))) > time.Duration(hostStaleMs)*time.Millisecond {
			continue
		}
		var obs hostPollObservationPayload
		if err := json.Unmarshal(r.Payload, &obs); err != nil || obs.Capabilities == nil {
			continue
		}
		holder := deref(r.Holder)
		if hr != nil && deref(hr.Holder) != "" {
			holder = *hr.Holder
		}
		out = append(out, publishingHostView{id: r.ID, caps: *obs.Capabilities, holder: holder})
	}
	return out, nil
}

func hostHolderAllowsProject(q *Request, holder, projectID string) bool {
	if holder == "" || projectID == "" {
		return true
	}
	p := q.peopleSnapshot().person(holder)
	if p == nil {
		return false
	}
	return fullAccess(p) || mayAccessProjectID(p, projectID)
}

func loadPublishingSettings(q *Request, projectID string) (publishingProjectSettings, string, error) {
	var s publishingProjectSettings
	raw := projectSettingsOf(q, projectID)
	if raw == nil {
		return s, "fleet", fail(404, "Unknown project")
	}
	if err := lenientDecode(jsonBytes(raw), &s); err != nil {
		return s, "fleet", err
	}
	prefix := "fleet"
	if s.Worktree != nil && s.Worktree.BranchPrefix != "" {
		prefix = s.Worktree.BranchPrefix
	}
	return s, prefix, nil
}

func buildProjectPublishingCapabilities(q *Request, projectID, repoFilter string, online []publishingHostView) ([]contract.PublishingCapability, error) {
	s, _, err := loadPublishingSettings(q, projectID)
	if err != nil {
		return nil, err
	}
	forgeEnabled := s.Execution != nil && s.Execution.Forge
	autoMerge := s.Integration != nil && s.Integration.Mode == "pr" && s.Integration.AutoMerge
	mergeMethod := "squash"
	if s.Integration != nil && s.Integration.MergeMethod != "" {
		mergeMethod = s.Integration.MergeMethod
	}
	envHosts := map[string]map[string]bool{}
	anyEnv := map[string]bool{}
	for _, co := range s.Environments {
		anyEnv[co.HostID] = true
		if co.Repo != nil {
			if envHosts[*co.Repo] == nil {
				envHosts[*co.Repo] = map[string]bool{}
			}
			envHosts[*co.Repo][co.HostID] = true
		}
	}
	out := make([]contract.PublishingCapability, 0, len(s.Repos))
	for _, r := range s.Repos {
		if repoFilter != "" && r.ID != repoFilter && !strings.EqualFold(r.Remote, repoFilter) {
			continue
		}
		ref, rok := contract.ParseRepoRemote(r.Remote, r.Forge, r.Host)
		if !rok {
			continue
		}
		if repoFilter != "" && r.ID != repoFilter && !strings.EqualFold(ref.Slug, repoFilter) && !strings.EqualFold(r.Remote, repoFilter) {
			continue
		}
		owner := strings.Split(ref.Slug, "/")[0]
		var factsReady bool
		if ref.Provider == "gitlab" {
			_, _, factsReady, err = gitlabConnectionOf(q, ref.Host, owner, r.Connection)
		} else {
			_, _, factsReady, err = githubConnectionOf(q, owner)
		}
		if err != nil {
			return nil, err
		}
		eligible := []string{}
		restricted := envHosts[r.ID]
		for _, h := range online {
			if len(restricted) > 0 && !restricted[h.id] {
				continue
			}
			if !hostHolderAllowsProject(q, h.holder, projectID) {
				continue
			}
			if hostCapsHaveRepo(h.caps, ref) {
				eligible = append(eligible, h.id)
			}
		}
		supported := forgeEnabled && len(eligible) > 0
		actions := []string{"capabilities", "status"}
		var reason *string
		if supported {
			actions = append(actions, "publish_draft", "record_code_review", "mark_ready")
			if autoMerge {
				actions = append(actions, "request_merge", "cancel_merge_request")
			}
		} else if !forgeEnabled {
			reason = ptr("project execution.forge is disabled")
		} else {
			reason = ptr("no online eligible host reports publishing capability for " + ref.Host + "/" + ref.Slug)
		}
		defBranch := r.DefaultBranch
		if defBranch == "" {
			defBranch = "main"
		}
		out = append(out, contract.PublishingCapability{
			ProjectId:               projectID,
			RepoId:                  r.ID,
			Provider:                contract.PublishingProvider(ref.Provider),
			Host:                    ref.Host,
			Repo:                    ref.Slug,
			Remote:                  optStr(r.Remote),
			DefaultBranch:           defBranch,
			GitConnection:           optStr(r.Connection),
			ForgeEnabled:            forgeEnabled,
			AutoMergeGranted:        autoMerge,
			MergeMethod:             mergeMethod,
			ProviderFactsConfigured: factsReady,
			PublicationSupported:    supported,
			EligibleHosts:           eligible,
			SupportedActions:        actions,
			Reason:                  reason,
		})
	}
	return out, nil
}

func publishingProjectFilter(q *Request, projectID *string, projectIDs, exclude []string) ([]string, map[string]bool, error) {
	if deref(projectID) != "" {
		if !mayAccessProjectID(q.person, *projectID) {
			return nil, nil, fail(404, "Unknown project")
		}
		if slices.Contains(exclude, *projectID) {
			return []string{}, map[string]bool{}, nil
		}
		return []string{*projectID}, map[string]bool{*projectID: true}, nil
	}
	all, err := projectIDsList(q)
	if err != nil {
		return nil, nil, err
	}
	want := map[string]bool{}
	if len(projectIDs) > 0 {
		for _, id := range projectIDs {
			want[id] = true
		}
	}
	out := make([]string, 0, len(all))
	allowed := map[string]bool{}
	for _, id := range all {
		if !fullAccess(q.person) && !mayAccessProjectID(q.person, id) {
			continue
		}
		if len(want) > 0 && !want[id] {
			continue
		}
		if slices.Contains(exclude, id) {
			continue
		}
		out = append(out, id)
		allowed[id] = true
	}
	return out, allowed, nil
}

func projectIDsList(q *Request) ([]string, error) {
	return projectIDs(q)
}

func publishingCapabilities(q *Request, r *contract.PublishingCapabilitiesRequest) (contract.PublishingCapabilitiesReply, error) {
	var none contract.PublishingCapabilitiesReply
	projID := r.ProjectId
	if deref(projID) == "" && deref(r.SessionId) != "" {
		s, err := sessionByID(q, *r.SessionId)
		if err != nil {
			return none, err
		}
		if s.ProjectID != "" {
			projID = ptr(s.ProjectID)
		}
	}
	if deref(projID) == "" && deref(r.ExecutionId) != "" {
		e, err := getExecution(q, *r.ExecutionId)
		if err != nil {
			return none, err
		}
		if e.ProjectID != "" {
			projID = ptr(e.ProjectID)
		}
	}
	pids, _, err := publishingProjectFilter(q, projID, nil, nil)
	if err != nil {
		return none, err
	}
	online, err := publishingOnlineHosts(q)
	if err != nil {
		return none, err
	}
	repos := []contract.PublishingCapability{}
	for _, pid := range pids {
		caps, err := buildProjectPublishingCapabilities(q, pid, deref(r.RepoId), online)
		if err != nil {
			return none, err
		}
		repos = append(repos, caps...)
	}
	return contract.PublishingCapabilitiesReply{ProjectId: projID, Repos: repos}, nil
}

func publishingStatus(q *Request, r *contract.PublishingStatusRequest) (contract.PublishingStatusReply, error) {
	var none contract.PublishingStatusReply
	projID := r.ProjectId
	branchFilter := deref(r.Branch)
	if deref(projID) == "" && deref(r.ExecutionId) != "" {
		e, err := getExecution(q, *r.ExecutionId)
		if err != nil {
			return none, err
		}
		if e.ProjectID != "" {
			projID = ptr(e.ProjectID)
		}
	}
	if deref(projID) == "" && deref(r.SessionId) != "" {
		s, err := sessionByID(q, *r.SessionId)
		if err != nil {
			return none, err
		}
		if s.ProjectID != "" {
			projID = ptr(s.ProjectID)
		}
	}
	pids, allowed, err := publishingProjectFilter(q, projID, r.ProjectIds, r.ExcludeProjectIds)
	if err != nil {
		return none, err
	}
	online, err := publishingOnlineHosts(q)
	if err != nil {
		return none, err
	}
	caps := []contract.PublishingCapability{}
	repoByProjAndSlug := map[string]contract.PublishingCapability{}
	for _, pid := range pids {
		pc, err := buildProjectPublishingCapabilities(q, pid, deref(r.RepoId), online)
		if err != nil {
			return none, err
		}
		for _, c := range pc {
			caps = append(caps, c)
			repoByProjAndSlug[pid+"|"+strings.ToLower(c.Repo)] = c
		}
	}
	deliveryMu.Lock()
	last := deliveryLast
	deliveryMu.Unlock()
	base := deliveryReply(last, func(p string) bool { return allowed[p] })

	pubMu.Lock()
	recs := make([]contract.PublishingRecord, 0, len(pubRecords))
	for _, rec := range pubRecords {
		if !allowed[rec.ProjectId] {
			continue
		}
		if deref(r.RepoId) != "" && deref(rec.RepoId) != *r.RepoId && !strings.EqualFold(rec.Repo, *r.RepoId) {
			continue
		}
		if branchFilter != "" && deref(rec.Branch) != branchFilter {
			continue
		}
		recs = append(recs, rec)
	}
	pubMu.Unlock()

	items := make([]contract.DeliveryItem, 0, len(base.Items)+len(recs))
	indexByKey := map[string]int{}
	for _, it := range base.Items {
		if !allowed[it.ProjectId] {
			continue
		}
		if branchFilter != "" && deref(it.Branch) != branchFilter {
			continue
		}
		if deref(r.RepoId) != "" {
			if c, ok := repoByProjAndSlug[it.ProjectId+"|"+strings.ToLower(it.Repo)]; !ok || (c.RepoId != *r.RepoId && !strings.EqualFold(c.Repo, *r.RepoId)) {
				continue
			}
		}
		key := fmt.Sprintf("%s|%s#%d", it.ProjectId, strings.ToLower(it.Repo), it.Number)
		indexByKey[key] = len(items)
		items = append(items, it)
	}
	for _, rec := range recs {
		key := fmt.Sprintf("%s|%s#%d", rec.ProjectId, strings.ToLower(rec.Repo), rec.Number)
		stage := contract.DeliveryStage(rec.Stage)
		if !stage.Valid() {
			stage = contract.DeliveryStagePrOpen
		}
		prov := string(rec.Provider)
		kind := string(rec.Kind)
		host := rec.Host
		mergedItem := contract.DeliveryItem{
			ProjectId:        rec.ProjectId,
			Repo:             rec.Repo,
			Number:           rec.Number,
			Title:            rec.Title,
			Url:              rec.Url,
			Stage:            stage,
			Since:            rec.Since,
			Opened:           rec.Opened,
			Merged:           rec.Merged,
			Release:          rec.Release,
			Provider:         &prov,
			Host:             &host,
			Kind:             &kind,
			Branch:           rec.Branch,
			BaseBranch:       rec.BaseBranch,
			HeadSha:          rec.HeadSha,
			IsDraft:          rec.IsDraft,
			AutoMergeEnabled: rec.AutoMergeEnabled,
			MergeMethod:      rec.MergeMethod,
		}
		if idx, ok := indexByKey[key]; ok {
			existing := items[idx]
			if mergedItem.Title == "" {
				mergedItem.Title = existing.Title
			}
			if mergedItem.Url == "" {
				mergedItem.Url = existing.Url
			}
			if mergedItem.Opened == nil {
				mergedItem.Opened = existing.Opened
			}
			if mergedItem.Merged == nil {
				mergedItem.Merged = existing.Merged
			}
			if mergedItem.HeadSha == nil {
				mergedItem.HeadSha = existing.HeadSha
			}
			if mergedItem.Branch == nil {
				mergedItem.Branch = existing.Branch
			}
			if mergedItem.BaseBranch == nil {
				mergedItem.BaseBranch = existing.BaseBranch
			}
			if mergedItem.IsDraft == nil {
				mergedItem.IsDraft = existing.IsDraft
			}
			if mergedItem.AutoMergeEnabled == nil {
				mergedItem.AutoMergeEnabled = existing.AutoMergeEnabled
			}
			items[idx] = mergedItem
		} else {
			indexByKey[key] = len(items)
			items = append(items, mergedItem)
		}
	}

	if err := linkDeliveryItems(q, items); err != nil {
		return none, err
	}
	// A project-forge record names its source execution: that links a pull request whose branch names no session.
	srcByKey, srcs := map[string]string{}, []string{}
	for _, rec := range recs {
		if src := deref(rec.SourceExecutionId); src != "" {
			srcByKey[fmt.Sprintf("%s|%s#%d", rec.ProjectId, strings.ToLower(rec.Repo), rec.Number)] = src
			srcs = append(srcs, src)
		}
	}
	bySource, err := sourceTasks(q, srcs)
	if err != nil {
		return none, err
	}
	for i, it := range items {
		if st, ok := bySource[srcByKey[fmt.Sprintf("%s|%s#%d", it.ProjectId, strings.ToLower(it.Repo), it.Number)]]; ok && it.TaskId == nil {
			items[i].TaskId = ptr(st[1])
			if st[0] != "" {
				items[i].SessionId = ptr(st[0])
			}
		}
	}
	if task := deref(r.TaskId); task != "" {
		kept := map[string]bool{}
		items = slices.DeleteFunc(items, func(it contract.DeliveryItem) bool {
			mine := deref(it.TaskId) == task
			kept[fmt.Sprintf("%s|%s#%d", it.ProjectId, strings.ToLower(it.Repo), it.Number)] = mine
			return !mine
		})
		recs = slices.DeleteFunc(recs, func(rec contract.PublishingRecord) bool {
			return !kept[fmt.Sprintf("%s|%s#%d", rec.ProjectId, strings.ToLower(rec.Repo), rec.Number)]
		})
	}
	return contract.PublishingStatusReply{
		Capabilities: caps,
		Error:        base.Error,
		Items:        items,
		Landing:      base.Landing,
		Records:      recs,
		RefreshedAt:  base.RefreshedAt,
		Samples:      base.Samples,
		Source:       base.Source,
	}, nil
}

func publishingRecordToContract(b *contract.PublishingHostRecord, now string) contract.PublishingRecord {
	stage := string(contract.DeliveryStagePrOpen)
	switch strings.ToUpper(b.State) {
	case "MERGED":
		stage = string(contract.DeliveryStageMerged)
	case "OPEN":
		if b.AutoMergeEnabled != nil && *b.AutoMergeEnabled {
			stage = string(contract.DeliveryStageMqWait)
		} else if cs := strings.ToLower(deref(b.ChecksState)); cs == "failed" || cs == "failure" {
			stage = string(contract.DeliveryStagePrFailed)
		} else if cs == "running" || cs == "pending" {
			stage = string(contract.DeliveryStagePrCi)
		}
	}
	return contract.PublishingRecord{
		ProjectId:         deref(b.ProjectId),
		RepoId:            b.RepoId,
		Provider:          contract.PublishingProvider(b.Provider),
		Host:              b.Host,
		Repo:              b.Repo,
		Kind:              contract.PublishingKind(b.Kind),
		Number:            b.Number,
		Title:             b.Title,
		Url:               b.Url,
		Stage:             stage,
		Since:             now,
		Branch:            optStr(b.Branch),
		BaseBranch:        b.BaseBranch,
		HeadSha:           optStr(b.HeadSha),
		IsDraft:           ptr(b.IsDraft),
		State:             optStr(b.State),
		AutoMergeEnabled:  b.AutoMergeEnabled,
		MergeMethod:       b.MergeMethod,
		Mergeable:         b.Mergeable,
		ChecksState:       b.ChecksState,
		ReviewState:       b.ReviewState,
		SourceExecutionId: b.SourceExecutionId,
	}
}

// threadDeliveryStage maps retained provider facts. A missing check observation never becomes a checks stage.
func threadDeliveryStage(raw []byte) (*contract.ThreadDeliveryStage, error) {
	var b contract.PublishingHostRecord
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	stage := contract.ThreadDeliveryStageUnknown
	switch strings.ToLower(b.State) {
	case "merged", "published":
		stage = contract.ThreadDeliveryStageMerged
	case "integrating", "merging":
		stage = contract.ThreadDeliveryStageMerging
	case "open", "opened", "pr-open":
		stage = openPublishingStage(b)
	}
	return &stage, nil
}

func openPublishingStage(b contract.PublishingHostRecord) contract.ThreadDeliveryStage {
	var result struct {
		MergeQueueEntry   *struct{ State string }                      `json:"mergeQueueEntry"`
		HeadPipeline      *struct{ SHA, Status string }                `json:"headPipeline"`
		StatusCheckRollup []struct{ Status, Conclusion, State string } `json:"statusCheckRollup"`
	}
	// Older or partial receipts may carry no provider detail.
	if json.Unmarshal(b.Result, &result) != nil {
		result.MergeQueueEntry, result.HeadPipeline, result.StatusCheckRollup = nil, nil, nil
	}
	state := deref(b.ChecksState)
	if state == "" && b.Provider == "gitlab" && result.HeadPipeline != nil && b.HeadSha != "" && result.HeadPipeline.SHA == b.HeadSha {
		state = result.HeadPipeline.Status
	}
	checks := observedPublishingCheck(state)
	if state == "" && b.Provider == "github" {
		for _, check := range result.StatusCheckRollup {
			observed := observedPublishingCheck(firstString(check.Conclusion, check.State, check.Status))
			if observed == contract.ThreadDeliveryStageChecksFailed {
				checks = observed
				break
			}
			if observed == contract.ThreadDeliveryStageChecks {
				checks = observed
			}
		}
	}
	// Requested auto-merge is permission to merge later. Use the provider's observed queue state.
	if result.MergeQueueEntry != nil && checks != contract.ThreadDeliveryStageChecksFailed {
		switch result.MergeQueueEntry.State {
		case "AWAITING_CHECKS", "LOCKED", "MERGEABLE", "QUEUED":
			return contract.ThreadDeliveryStageMerging
		}
	}
	return checks
}

func observedPublishingCheck(state string) contract.ThreadDeliveryStage {
	switch strings.ToLower(state) {
	case "failed", "failure", "error", "timed_out", "startup_failure", "action_required":
		return contract.ThreadDeliveryStageChecksFailed
	case "running", "pending", "queued", "in_progress":
		return contract.ThreadDeliveryStageChecks
	default:
		return contract.ThreadDeliveryStagePrOpen
	}
}

func publishingRecordRoute(q *Request, b *contract.PublishingHostRecord) (contract.OkReply, error) {
	var none contract.OkReply
	if b.HostId != q.hostID() {
		return none, fail(403, "Host rejected")
	}
	cmdID := deref(b.CommandId)
	execID := deref(b.ExecutionId)
	action := deref(b.Action)
	now := time.Now().UTC().Format(time.RFC3339)
	rec := publishingRecordToContract(b, now)
	outcome := "completed"
	if deref(b.Error) != "" {
		outcome = "failed"
	}
	err := q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(wq *Request) error {
		if !hostHolderAllowsProject(wq, wq.host.Holder, rec.ProjectId) {
			return fail(403, "Publishing host project rejected")
		}
		sourceID := execID
		if cmdID != "" {
			cmd, err := commandOf(wq, b.HostId, execID, cmdID, "Publishing record rejected")
			if err != nil {
				return err
			}
			if cmd.Kind == string(contract.CommandKindPublishing) {
				var payload contract.PublishingCommandPayload
				if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
					return err
				}
				sourceID = deref(payload.SourceExecutionId)
			}
		} else if execID != "" {
			if err := ownedBody(wq, b.HostId, execID); err != nil {
				return err
			}
		}
		if source := deref(b.SourceExecutionId); source != "" && source != sourceID {
			return fail(403, "Publishing record source execution rejected")
		}
		if execID != "" {
			execution, err := wq.db().ExecutionByID(wq.ctx(), execID)
			if err != nil {
				return err
			}
			if rec.ProjectId != "" && deref(execution.ProjectID) != rec.ProjectId {
				return fail(403, "Publishing record project rejected")
			}
		}
		if err := audit(wq, "publishing:"+b.Repo, action, nil, b, auditExtra{
			ProjectID: b.ProjectId,
			Holder:    wq.host.Holder,
		}); err != nil {
			return err
		}
		if rec.ProjectId != "" {
			if err := wq.db().DeliveryStatePut(wq.ctx(), db.DeliveryStatePutParams{
				ID:          "delivery-state:" + rec.ProjectId,
				ProjectID:   rec.ProjectId,
				RefreshedAt: &now,
			}); err != nil {
				return err
			}
		}
		afterCommit(wq, func() {
			pubMu.Lock()
			if outcome == "completed" && rec.Number > 0 {
				pubRecords[publishingRecordKey(rec.ProjectId, rec)] = rec
			}
			if cmdID != "" {
				pubCmdOutcomes[cmdID] = publishingCmdOutcome{
					outcome: outcome,
					errMsg:  deref(b.Error),
					result:  b.Result,
				}
			}
			if outcome == "completed" && b.RequestId != nil && *b.RequestId != "" && len(b.Result) > 0 {
				pubIdempotent[publishingIdempotencyKey(rec.ProjectId, action, *b.RequestId)] = b.Result
			}
			pubMu.Unlock()
			publishingWakeCommand(cmdID)
		})
		return nil
	})
	if err != nil {
		return none, err
	}
	return contract.OkReply{Ok: true}, nil
}

type publishingMutationParams struct {
	action            string
	requestID         string
	projectID         *string
	repoID            *string
	repo              *string
	branch            *string
	number            *int64
	expectedHeadSha   *string
	title             *string
	body              *string
	artifactID        *string
	description       *string
	mergeMethod       *string
	sourceExecutionID *string
	requireLocalHost  bool
	requireAutoMerge  bool
}

func resolvePublishingTarget(q *Request, p publishingMutationParams) (projectID, targetHost, cmdID string, err error) {
	if deref(p.projectID) == "" && deref(p.sourceExecutionID) != "" {
		se, eerr := getExecution(q, *p.sourceExecutionID)
		if eerr != nil {
			return "", "", "", eerr
		}
		if se.ProjectID != "" {
			p.projectID = ptr(se.ProjectID)
		}
	}
	if deref(p.projectID) == "" {
		return "", "", "", fail(400, "Expected projectId")
	}
	projectID = *p.projectID
	if !mayAccessProjectID(q.person, projectID) {
		return "", "", "", fail(404, "Unknown project")
	}
	settings, prefix, err := loadPublishingSettings(q, projectID)
	if err != nil {
		return "", "", "", err
	}
	if settings.Execution == nil || !settings.Execution.Forge {
		return "", "", "", fail(403, "Project does not grant forge execution")
	}
	autoMerge := settings.Integration != nil && settings.Integration.Mode == "pr" && settings.Integration.AutoMerge
	if p.requireAutoMerge && !autoMerge {
		return "", "", "", fail(403, "Project does not grant auto-merge")
	}

	var sourceExec *agentExecution
	var sourceSessionBranch string
	var preferredRepoID *string
	if deref(p.sourceExecutionID) != "" {
		se, eerr := getExecution(q, *p.sourceExecutionID)
		if eerr != nil {
			return "", "", "", eerr
		}
		if se.ProjectID != "" && se.ProjectID != projectID {
			return "", "", "", fail(403, "Source execution belongs to another project")
		}
		s, serr := sessionByID(q, se.SessionID)
		if serr != nil {
			return "", "", "", serr
		}
		if p.requireLocalHost {
			if s.Access == "read" {
				return "", "", "", fail(403, "Read-only session cannot publish")
			}
			if s.Branch != nil && !*s.Branch {
				return "", "", "", fail(403, "Branchless session cannot publish")
			}
		}
		prof, _ := profileOf(se.AgentConfig)
		if len(settings.Execution.Profiles) > 0 && prof.ID != "" && !slices.Contains(settings.Execution.Profiles, prof.ID) {
			return "", "", "", fail(403, "Execution profile is not granted forge access")
		}
		sourceExec = &se
		sourceSessionBranch = prefix + "/" + se.SessionID
		for _, co := range settings.Environments {
			if co.HostID == deref(se.HostID) && co.Repo != nil {
				preferredRepoID = co.Repo
				break
			}
		}
	}

	var chosen *projectRepo
	var chosenRef contract.RepoRef
	for i := range settings.Repos {
		r := settings.Repos[i]
		ref, rok := contract.ParseRepoRemote(r.Remote, r.Forge, r.Host)
		if !rok {
			continue
		}
		if deref(p.repoID) != "" {
			if r.ID == *p.repoID {
				chosen, chosenRef = &r, ref
				break
			}
			continue
		}
		if deref(p.repo) != "" {
			if strings.EqualFold(ref.Slug, *p.repo) || strings.EqualFold(ref.Host+"/"+ref.Slug, *p.repo) {
				chosen, chosenRef = &r, ref
				break
			}
			continue
		}
		if preferredRepoID != nil {
			if r.ID == *preferredRepoID {
				chosen, chosenRef = &r, ref
				break
			}
			continue
		}
		chosen, chosenRef = &r, ref
		break
	}
	if chosen == nil {
		return "", "", "", fail(404, "Repository is not granted by project")
	}

	branch := deref(p.branch)
	if branch == "" && sourceSessionBranch != "" {
		branch = sourceSessionBranch
	}
	if branch == "" && p.number != nil && *p.number > 0 {
		pubMu.Lock()
		for _, rec := range pubRecords {
			if rec.ProjectId == projectID && strings.EqualFold(rec.Repo, chosenRef.Slug) && rec.Number == *p.number && deref(rec.Branch) != "" {
				branch = *rec.Branch
				break
			}
		}
		pubMu.Unlock()
		if branch == "" {
			deliveryMu.Lock()
			for _, dr := range deliveryLast.repos {
				for _, it := range dr.items {
					if it.ProjectId == projectID && strings.EqualFold(it.Repo, chosenRef.Slug) && it.Number == *p.number && deref(it.Branch) != "" {
						branch = *it.Branch
						break
					}
				}
				if branch != "" {
					break
				}
			}
			deliveryMu.Unlock()
		}
	}
	if p.requireLocalHost && !strings.HasPrefix(branch, prefix+"/") {
		return "", "", "", fail(403, "Branch is outside project branchPrefix")
	}

	online, err := publishingOnlineHosts(q)
	if err != nil {
		return "", "", "", err
	}
	envHosts := map[string]bool{}
	for _, co := range settings.Environments {
		if co.Repo == nil || *co.Repo == chosen.ID {
			envHosts[co.HostID] = true
		}
	}
	requireHost := ""
	if p.requireLocalHost && sourceExec != nil {
		requireHost = deref(sourceExec.HostID)
	}
	for _, h := range online {
		if requireHost != "" && h.id != requireHost {
			continue
		}
		if len(envHosts) > 0 && !envHosts[h.id] {
			continue
		}
		if !hostHolderAllowsProject(q, h.holder, projectID) {
			continue
		}
		if hostCapsHaveRepo(h.caps, chosenRef) {
			targetHost = h.id
			break
		}
	}
	if targetHost == "" {
		if requireHost != "" {
			return "", "", "", fail(409, "Source execution host lacks publishing capability for "+chosenRef.Slug)
		}
		return "", "", "", fail(409, "No online eligible host with publishing capability for "+chosenRef.Slug)
	}

	defBranch := chosen.DefaultBranch
	if defBranch == "" {
		defBranch = "main"
	}
	mergeMethod := deref(p.mergeMethod)
	if mergeMethod == "" && settings.Integration != nil && settings.Integration.MergeMethod != "" {
		mergeMethod = settings.Integration.MergeMethod
	}
	if mergeMethod == "" {
		mergeMethod = "squash"
	}
	payload := contract.PublishingCommandPayload{
		Action:            p.action,
		RequestId:         p.requestID,
		ProjectId:         projectID,
		RepoId:            optStr(chosen.ID),
		Repo:              chosenRef.Slug,
		Host:              chosenRef.Host,
		Provider:          chosenRef.Provider,
		Remote:            optStr(chosen.Remote),
		Prefix:            optStr(prefix),
		Branch:            optStr(branch),
		BaseBranch:        defBranch,
		Number:            p.number,
		Title:             p.title,
		Body:              p.body,
		ExpectedHeadSha:   p.expectedHeadSha,
		ArtifactId:        p.artifactID,
		Description:       p.description,
		MergeMethod:       mergeMethod,
		SourceExecutionId: p.sourceExecutionID,
		AutoMerge:         autoMerge,
	}

	sid, eid := newID(), newID()
	cmdID = newID()
	if err := sessionPut(q, &agentSession{ID: sid, CreatedAt: nowMs(), Actor: "program-worker", ProjectID: projectID}); err != nil {
		return "", "", "", err
	}
	if err := executionWrite(q, struct {
		ID          string `json:"id"`
		ExecutionID string `json:"executionId"`
		SessionID   string `json:"sessionId"`
		HostID      string `json:"hostId"`
		ProjectID   string `json:"projectId"`
		State       string `json:"state"`
		CreatedAt   int64  `json:"createdAt"`
		Actor       string `json:"actor"`
	}{ID: eid, ExecutionID: eid, SessionID: sid, HostID: targetHost, ProjectID: projectID, State: "preparing", CreatedAt: nowMs(), Actor: "program-worker"}); err != nil {
		return "", "", "", err
	}
	if err := commandPut(q, &agentCommand{
		ID:          cmdID,
		ExecutionID: eid,
		SessionID:   sid,
		HostID:      targetHost,
		ProjectID:   projectID,
		Kind:        string(contract.CommandKindPublishing),
		Status:      "pending",
		Payload:     jsonBytes(payload),
	}); err != nil {
		return "", "", "", err
	}
	return projectID, targetHost, cmdID, nil
}

func runPublishingMutation[Rep any](q *Request, p publishingMutationParams) (Rep, error) {
	var none Rep
	if strings.TrimSpace(p.requestID) == "" {
		return none, fail(400, "Expected requestId")
	}
	if deref(p.projectID) != "" {
		pubMu.Lock()
		cached, ok := pubIdempotent[publishingIdempotencyKey(*p.projectID, p.action, p.requestID)]
		pubMu.Unlock()
		if ok && len(cached) > 0 {
			if err := json.Unmarshal(cached, &none); err == nil {
				return none, nil
			}
		}
	}
	var projectID, targetHost, cmdID string
	var cachedReply json.RawMessage
	runWrite := func(wq *Request) error {
		pid := deref(p.projectID)
		if pid == "" && deref(p.sourceExecutionID) != "" {
			if se, eerr := getExecution(wq, *p.sourceExecutionID); eerr == nil && se.ProjectID != "" {
				pid = se.ProjectID
			}
		}
		if pid != "" {
			pubMu.Lock()
			cached, ok := pubIdempotent[publishingIdempotencyKey(pid, p.action, p.requestID)]
			pubMu.Unlock()
			if ok && len(cached) > 0 {
				cachedReply = cached
				return nil
			}
		}
		var err error
		projectID, targetHost, cmdID, err = resolvePublishingTarget(wq, p)
		return err
	}
	if q.tx == nil && q.app != nil {
		if err := q.app.store.Write(q.ctx(), q, []string{GLOBAL}, runWrite); err != nil {
			return none, err
		}
	} else {
		if err := runWrite(q); err != nil {
			return none, err
		}
	}
	if len(cachedReply) > 0 {
		if err := json.Unmarshal(cachedReply, &none); err == nil {
			return none, nil
		}
	}
	if q.app != nil && targetHost != "" {
		q.app.wakeHost(targetHost)
	}
	out, err := publishingAwaitCommand(q.ctx(), cmdID, publishingCommandTimeout)
	if err != nil {
		return none, err
	}
	if out.outcome != "completed" {
		msg := out.errMsg
		if msg == "" {
			msg = "Publishing action failed"
		}
		if strings.Contains(msg, "head changed") || strings.Contains(msg, "draft") || strings.Contains(msg, "not open") {
			return none, fail(409, msg)
		}
		return none, fail(502, msg)
	}
	if len(out.result) == 0 {
		return none, fail(502, "Host completed publishing action without result payload")
	}
	if err := json.Unmarshal(out.result, &none); err != nil {
		return none, fail(502, "Invalid publishing result payload: "+err.Error())
	}
	if projectID != "" {
		pubMu.Lock()
		pubIdempotent[publishingIdempotencyKey(projectID, p.action, p.requestID)] = out.result
		pubMu.Unlock()
	}
	return none, nil
}

func publishingPublishDraft(q *Request, r *contract.PublishingPublishDraftRequest) (contract.PublishingPublishDraftReply, error) {
	return runPublishingMutation[contract.PublishingPublishDraftReply](q, publishingMutationParams{
		action:            "publish_draft",
		requestID:         r.RequestId,
		projectID:         r.ProjectId,
		repoID:            r.RepoId,
		expectedHeadSha:   r.ExpectedHeadSha,
		title:             &r.Title,
		body:              &r.Body,
		sourceExecutionID: r.SourceExecutionId,
		requireLocalHost:  true,
	})
}

func publishingRecordCodeReview(q *Request, r *contract.PublishingRecordCodeReviewRequest) (contract.PublishingRecordCodeReviewReply, error) {
	return runPublishingMutation[contract.PublishingRecordCodeReviewReply](q, publishingMutationParams{
		action:            "record_code_review",
		requestID:         r.RequestId,
		projectID:         r.ProjectId,
		repoID:            r.RepoId,
		expectedHeadSha:   &r.ExpectedHeadSha,
		artifactID:        &r.ArtifactId,
		description:       &r.Description,
		sourceExecutionID: r.SourceExecutionId,
		requireLocalHost:  true,
	})
}

func publishingMarkReady(q *Request, r *contract.PublishingMarkReadyRequest) (contract.PublishingMarkReadyReply, error) {
	return runPublishingMutation[contract.PublishingMarkReadyReply](q, publishingMutationParams{
		action:            "mark_ready",
		requestID:         r.RequestId,
		projectID:         r.ProjectId,
		repoID:            r.RepoId,
		repo:              r.Repo,
		branch:            r.Branch,
		number:            r.Number,
		expectedHeadSha:   r.ExpectedHeadSha,
		sourceExecutionID: r.SourceExecutionId,
	})
}

func publishingRequestMerge(q *Request, r *contract.PublishingRequestMergeRequest) (contract.PublishingRequestMergeReply, error) {
	var mm *string
	if r.MergeMethod != nil {
		s := string(*r.MergeMethod)
		mm = &s
	}
	return runPublishingMutation[contract.PublishingRequestMergeReply](q, publishingMutationParams{
		action:            "request_merge",
		requestID:         r.RequestId,
		projectID:         r.ProjectId,
		repoID:            r.RepoId,
		repo:              r.Repo,
		branch:            r.Branch,
		number:            r.Number,
		expectedHeadSha:   r.ExpectedHeadSha,
		mergeMethod:       mm,
		sourceExecutionID: r.SourceExecutionId,
		requireAutoMerge:  true,
	})
}

func publishingCancelMergeRequest(q *Request, r *contract.PublishingCancelMergeRequestRequest) (contract.PublishingCancelMergeRequestReply, error) {
	return runPublishingMutation[contract.PublishingCancelMergeRequestReply](q, publishingMutationParams{
		action:            "cancel_merge_request",
		requestID:         r.RequestId,
		projectID:         r.ProjectId,
		repoID:            r.RepoId,
		repo:              r.Repo,
		branch:            r.Branch,
		number:            r.Number,
		expectedHeadSha:   r.ExpectedHeadSha,
		sourceExecutionID: r.SourceExecutionId,
	})
}

// Keep unused pgx reference satisfied cleanly across build tags if needed.
var _ = pgx.ErrNoRows
