// Forge code-review verification (T-FLT-27): verifies project/host/actor grants and auditable stored review
// evidence referencing the exact 40-character commit SHA before recording an audit row and authorizing project-forge
// to post the GitHub `code-review` commit status.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

var (
	forgeSHAPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	forgeArtifactPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

func init() {
	hostRoute(contract.RouteForgeReview, forgeReviewRoute)
}

type forgeAuditEntry struct {
	ExecutionID       string `json:"executionId"`
	SessionID         string `json:"sessionId"`
	TaskID            string `json:"taskId,omitempty"`
	Provider          string `json:"provider,omitempty"`
	Host              string `json:"host,omitempty"`
	Repo              string `json:"repo"`
	Branch            string `json:"branch"`
	HeadSha           string `json:"headSha"`
	ArtifactID        string `json:"artifactId"`
	ArtifactSha256    string `json:"artifactSha256"`
	ReviewExecutionID string `json:"reviewExecutionId"`
	ReviewSessionID   string `json:"reviewSessionId"`
	SourceExecutionID string `json:"sourceExecutionId,omitempty"`
	RequestID         string `json:"requestId,omitempty"`
	GithubActor       string `json:"githubActor"`
	ProviderActor     string `json:"providerActor,omitempty"`
	Description       string `json:"description"`
	Independent       bool   `json:"independent"`
}

type forgeWorktreeSettings struct {
	Worktree *struct {
		BranchPrefix string `json:"branchPrefix"`
	} `json:"worktree"`
}

func validForgeRepoSlug(repo string) bool {
	return validForgeRepoSlugForProvider(repo, "")
}

func validForgeRepoSlugForProvider(repo, provider string) bool {
	parts := strings.Split(repo, "/")
	if provider == "gitlab" {
		if len(parts) < 2 {
			return false
		}
	} else if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return false
		}
		for i := 0; i < len(p); i++ {
			c := p[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
				return false
			}
		}
	}
	return true
}

func validForgeBranch(branch string) bool {
	if branch == "" || len(branch) > 255 || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "//") || strings.Contains(branch, "..") {
		return false
	}
	if strings.ContainsAny(branch, " \t\r\n:~^?*[\\") {
		return false
	}
	for i := 0; i < len(branch); i++ {
		if branch[i] < 0x20 || branch[i] == 0x7f {
			return false
		}
	}
	return true
}

func validForgeDescription(desc string) bool {
	trimmed := strings.TrimSpace(desc)
	if trimmed == "" || len(desc) > 140 {
		return false
	}
	for i := 0; i < len(desc); i++ {
		if desc[i] < 0x20 || desc[i] == 0x7f {
			return false
		}
	}
	return true
}

func validForgeGithubActor(actor string) bool {
	if actor == "" || len(actor) > 100 || strings.ContainsAny(actor, " \t\r\n/") {
		return false
	}
	for i := 0; i < len(actor); i++ {
		if actor[i] < 0x20 || actor[i] == 0x7f {
			return false
		}
	}
	return true
}

func forgeProjectSettings(q *Request, e agentExecution, projectID string) (runSettings, string, error) {
	var settings runSettings
	var wt forgeWorktreeSettings
	var snapSettings json.RawMessage
	if jsonGiven(e.ProjectSnapshot) {
		var snap projectSnapshot
		_ = lenientDecode(e.ProjectSnapshot, &snap)
		snapSettings = snap.Settings
	}
	raw := snapSettings
	if !jsonIsObject(raw) {
		raw = jsonBytes(projectSettingsOf(q, projectID))
	}
	if err := lenientDecode(raw, &settings); err != nil {
		return settings, "", err
	}
	_ = lenientDecode(raw, &wt)
	prefix := "fleet"
	if wt.Worktree != nil && wt.Worktree.BranchPrefix != "" {
		prefix = wt.Worktree.BranchPrefix
	}
	return settings, prefix, nil
}

func forgeSelectedRepoRef(settings runSettings, hostID string) (contract.RepoRef, bool) {
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
				return contract.RepoRef{}, false
			}
			if !validForgeRepoSlugForProvider(ref.Slug, ref.Provider) {
				return contract.RepoRef{}, false
			}
			return ref, true
		}
	}
	return contract.RepoRef{}, false
}

func forgeSelectedRepoSlug(settings runSettings, hostID string) string {
	ref, ok := forgeSelectedRepoRef(settings, hostID)
	if !ok {
		return ""
	}
	return ref.Slug
}

func forgeResolveActor(q *Request, e agentExecution, projectID, holder string) (*Person, error) {
	d := firstString(e.DirectedBy, holder)
	person := q.peopleSnapshot().person(d)
	if person == nil {
		return nil, fail(403, "Director "+d+" is not a person of this Fleet")
	}
	if hp := q.peopleSnapshot().person(holder); d != holder && (hp == nil || !hp.Owner) {
		return nil, fail(403, "Host "+deref(e.HostID)+" is held by "+holder+", not the director "+d)
	}
	if e.TakenFrom != "" {
		if person = q.peopleSnapshot().person(e.TakenFrom); person == nil {
			return nil, fail(403, "Requester "+e.TakenFrom+" is not a person of this Fleet")
		}
	}
	if !mayAccessProjectID(person, projectID) {
		return nil, fail(403, "Project outside actor grants")
	}
	return person, nil
}

func forgeReviewRoute(q *Request, b *contract.ForgeReviewRequest) (contract.ForgeReviewReply, error) {
	var none contract.ForgeReviewReply
	if b.HostId != q.hostID() {
		return none, fail(403, "Host rejected")
	}
	if !forgeSHAPattern.MatchString(b.HeadSha) {
		return none, fail(400, "Expected full 40-character lowercase headSha")
	}
	if !forgeArtifactPattern.MatchString(b.ArtifactId) || len(b.ArtifactId) > 200 || strings.Contains(b.ArtifactId, "..") {
		return none, fail(400, "Invalid artifactId")
	}
	if !validForgeRepoSlugForProvider(b.Repo, deref(b.Provider)) {
		return none, fail(400, "Invalid repo")
	}
	if !validForgeBranch(b.Branch) {
		return none, fail(400, "Invalid branch")
	}
	if !validForgeDescription(b.Description) {
		return none, fail(400, "Invalid description")
	}
	providerActor := b.GithubActor
	if deref(b.ProviderActor) != "" {
		providerActor = *b.ProviderActor
	}
	if !validForgeGithubActor(providerActor) {
		return none, fail(400, "Invalid providerActor")
	}
	if b.AgentToolKey == "" {
		return none, fail(400, "Expected agentToolKey")
	}

	var (
		e           agentExecution
		projectID   string
		taskID      string
		person      *Person
		holder      string
		art         *Artifact
		revExec     agentExecution
		independent bool
		targetURL   string
		repoRef     contract.RepoRef
	)
	err := q.app.store.Read(q.ctx(), q, func(rq *Request) error {
		c, err := commandOf(rq, b.HostId, b.ExecutionId, b.CommandId, "Command rejected")
		if err != nil {
			return err
		}
		if c.Status != "accepted" && c.Status != "started" {
			return fail(409, "Command is not active")
		}
		e, err = getExecution(rq, b.ExecutionId)
		if err != nil {
			return err
		}
		if e.State != "running" {
			return fail(409, "Execution is not running")
		}
		s, err := sessionByID(rq, e.SessionID)
		if err != nil {
			return err
		}
		link, err := rq.db().AgentExecutionLink(rq.ctx(), b.ExecutionId)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		projectID = firstString(deref(link.ProjectID), e.ProjectID, s.ProjectID)
		taskID = firstString(deref(link.TaskID), e.TaskID, s.TaskID)
		if projectID == "" {
			return fail(403, "Execution has no project")
		}
		if s.Access == "read" {
			return fail(403, "Read-only session cannot attest code review")
		}
		if s.Branch != nil && !*s.Branch {
			return fail(403, "Branchless session cannot attest code review")
		}
		holder = rq.host.Holder
		person, err = forgeResolveActor(rq, e, projectID, holder)
		if err != nil {
			return err
		}
		obsRow, err := rq.db().HostObservationOf(rq.ctx(), b.HostId)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(403, "Host lacks forge capability for "+b.Repo)
		}
		if err != nil {
			return err
		}
		var obs hostPollObservationPayload
		if err := json.Unmarshal(obsRow.Payload, &obs); err != nil || obs.Capabilities == nil {
			return fail(403, "Host lacks forge capability for "+b.Repo)
		}
		settings, prefix, err := forgeProjectSettings(rq, e, projectID)
		if err != nil {
			return err
		}
		if settings.Execution == nil || !settings.Execution.Forge {
			return fail(403, "Project does not grant forge execution")
		}
		prof, _ := profileOf(e.AgentConfig)
		if len(settings.Execution.Profiles) > 0 && !slices.Contains(settings.Execution.Profiles, prof.ID) {
			return fail(403, "Execution profile is not granted forge access")
		}
		ref, ok := forgeSelectedRepoRef(settings, b.HostId)
		if !ok || !strings.EqualFold(ref.Slug, b.Repo) {
			return fail(403, "Repository is not granted by project")
		}
		if b.Provider != nil && string(*b.Provider) != ref.Provider {
			return fail(403, "Provider does not match project repository")
		}
		if deref(b.Host) != "" && !strings.EqualFold(*b.Host, ref.Host) {
			return fail(403, "Repository host does not match project repository")
		}
		if !hostCapsHaveRepo(*obs.Capabilities, ref) {
			return fail(403, "Host lacks forge capability for "+b.Repo)
		}
		repoRef = ref
		if !strings.HasPrefix(b.Branch, prefix+"/") {
			return fail(403, "Branch is outside project branchPrefix")
		}
		art, err = artifactRow(rq.ctx(), rq.app, b.ArtifactId)
		if err != nil {
			return err
		}
		if art.State != "stored" {
			return fail(409, "Review artifact is not stored")
		}
		if art.Role == "example" {
			return fail(403, "Example artifact cannot attest code review")
		}
		if art.ProjectID == nil || *art.ProjectID != projectID {
			return fail(403, "Review artifact belongs to another project")
		}
		if art.TaskID != nil && taskID != "" && *art.TaskID != taskID {
			return fail(403, "Review artifact belongs to another task")
		}
		if art.ExecutionID == nil || *art.ExecutionID == "" {
			return fail(403, "Review artifact lacks execution provenance")
		}
		revExec, err = getExecution(rq, *art.ExecutionID)
		if err != nil {
			return fail(403, "Review execution unavailable")
		}
		independent = revExec.SessionID != e.SessionID
		return nil
	})
	if err != nil {
		return none, err
	}

	content, err := artifactBytes(q.ctx(), q.app, art.ID, 1<<20)
	if err != nil {
		return none, err
	}
	if len(bytes.TrimSpace(content)) == 0 || bytes.Contains(content, []byte{0}) || !utf8.Valid(content) {
		return none, fail(400, "Review artifact must contain non-empty UTF-8 text evidence")
	}
	if !strings.Contains(strings.ToLower(string(content)), b.HeadSha) {
		return none, fail(409, "Review artifact does not reference exact commit "+b.HeadSha)
	}

	var recordedActor string
	err = q.app.store.Write(q.ctx(), q, []string{"execution:" + b.ExecutionId}, func(wq *Request) error {
		c, err := commandOf(wq, b.HostId, b.ExecutionId, b.CommandId, "Command rejected")
		if err != nil {
			return err
		}
		if c.Status != "accepted" && c.Status != "started" {
			return fail(409, "Command is not active")
		}
		cur, err := getExecution(wq, b.ExecutionId)
		if err != nil {
			return err
		}
		if cur.State != "running" {
			return fail(409, "Execution is not running")
		}
		wq.agent = "execution:" + e.ID
		wq.person = person
		recordedActor = actor(wq, wq.agent)
		entry := forgeAuditEntry{
			ExecutionID:       e.ID,
			SessionID:         e.SessionID,
			TaskID:            taskID,
			Provider:          repoRef.Provider,
			Host:              repoRef.Host,
			Repo:              b.Repo,
			Branch:            b.Branch,
			HeadSha:           b.HeadSha,
			ArtifactID:        art.ID,
			ArtifactSha256:    art.ContentHash,
			ReviewExecutionID: revExec.ID,
			ReviewSessionID:   revExec.SessionID,
			SourceExecutionID: deref(b.SourceExecutionId),
			RequestID:         deref(b.RequestId),
			GithubActor:       providerActor,
			ProviderActor:     providerActor,
			Description:       b.Description,
			Independent:       independent,
		}
		return audit(wq, "forge:"+b.Repo+":"+b.HeadSha, "code-review", nil, entry, auditExtra{
			ProjectID: &projectID,
			Holder:    holder,
			Note:      b.Description,
		})
	})
	if err != nil {
		return none, err
	}

	if strings.HasPrefix(browserOrigin, "https://") {
		targetURL = strings.TrimRight(browserOrigin, "/") + "/results/" + art.ID
	}
	return contract.ForgeReviewReply{
		Verified:          true,
		ArtifactId:        art.ID,
		ArtifactSha256:    art.ContentHash,
		ExecutionId:       e.ID,
		SessionId:         e.SessionID,
		ReviewExecutionId: ptr(revExec.ID),
		ReviewSessionId:   ptr(revExec.SessionID),
		Actor:             recordedActor,
		OnBehalfOf:        ptr(person.Email),
		Independent:       independent,
		TargetUrl:         optStr(targetURL),
		Provider:          optStr(repoRef.Provider),
		Host:              optStr(repoRef.Host),
		ProviderActor:     optStr(providerActor),
	}, nil
}
