package projectpublisher

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const repoPath = "/repos/" + Repo

func validSHA(s string) bool {
	if len(s) != 40 || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

type github struct {
	*installation
	token token
}

func (g github) rest(ctx context.Context, method, path string, payload, out any) error {
	err := g.installation.api(ctx, g.token.value, method, path, payload, out)
	if err != nil && err.Error() == "publisher-provider-http-401" {
		g.invalidate(g.token.value)
	}
	return err
}

func (g github) graph(ctx context.Context, query string, vars map[string]any, out any) error {
	var reply struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := g.rest(ctx, http.MethodPost, "/graphql", map[string]any{"query": query, "variables": vars}, &reply); err != nil {
		return err
	}
	if len(reply.Errors) != 0 || len(reply.Data) == 0 || string(reply.Data) == "null" ||
		(out != nil && json.Unmarshal(reply.Data, out) != nil) {
		return errors.New("publisher-provider-graphql-unconfirmed")
	}
	return nil
}

type pull struct {
	ID     string `json:"node_id"`
	Number int64  `json:"number"`
	URL    string `json:"html_url"`
	Title  string `json:"title"`
	Draft  bool   `json:"draft"`
	State  string `json:"state"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA  string     `json:"sha"`
		Ref  string     `json:"ref"`
		Repo repository `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string     `json:"ref"`
		Repo repository `json:"repo"`
	} `json:"base"`
}

func (p pull) matches(op Operation) bool {
	return p.ID != "" && p.Number > 0 && p.URL == fmt.Sprintf("https://github.com/%s/pull/%d", Repo, p.Number) &&
		(op.Branch == "" || p.Head.Ref == op.Branch) && validSHA(p.Head.SHA) &&
		(p.Head.Repo.matches() || op.Action == "cancel_merge_request") &&
		p.Base.Ref == op.Base && p.Base.Repo.matches() &&
		(op.Number == 0 || p.Number == op.Number)
}

func (g github) lookup(ctx context.Context, op Operation, state string) (pull, error) {
	var pr pull
	if op.Number > 0 {
		err := g.rest(ctx, http.MethodGet, repoPath+"/pulls/"+strconv.FormatInt(op.Number, 10), nil, &pr)
		if err != nil {
			return pr, err
		}
		if !pr.matches(op) {
			return pull{}, errors.New("publisher-pull-request-binding-mismatch")
		}
		return pr, nil
	}
	var listed []pull
	endpoint := repoPath + "/pulls?state=" + state + "&head=" + url.QueryEscape("taskqorg:"+op.Branch)
	if err := g.rest(ctx, http.MethodGet, endpoint, nil, &listed); err != nil {
		return pr, err
	}
	if listed == nil {
		return pull{}, errors.New("publisher-pull-request-list-invalid")
	}
	for _, candidate := range listed {
		if !candidate.matches(op) {
			return pull{}, errors.New("publisher-pull-request-binding-mismatch")
		}
		if pr.Number == 0 || candidate.State == "open" {
			pr = candidate
		}
		if pr.State == "open" {
			break
		}
	}
	return pr, nil
}

type queueEntry struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Position int    `json:"position"`
}

type pullStatus struct {
	ID                  string
	State               string
	HeadRefOid          string
	IsDraft             bool
	Mergeable           string
	MergeStateStatus    string
	IsMergeQueueEnabled *bool
	AutoMergeRequest    json.RawMessage
	MergeQueueEntry     *queueEntry
	Commits             struct {
		Nodes []struct {
			Commit struct {
				OID               string
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes    []json.RawMessage
						PageInfo struct {
							HasNextPage bool
							EndCursor   string
						}
					}
				}
			}
		}
	}
}

const prQuery = `query($number:Int!,$after:String){repository(owner:"taskqorg",name:"fleet"){
	pullRequest(number:$number){id state headRefOid isDraft mergeable mergeStateStatus isMergeQueueEnabled
	autoMergeRequest{enabledAt}
	mergeQueueEntry{id state position}
	commits(last:1){nodes{commit{oid statusCheckRollup{contexts(first:100,after:$after){
	pageInfo{hasNextPage endCursor} nodes{
	__typename ... on CheckRun{name status conclusion detailsUrl startedAt completedAt }
	... on StatusContext{context state targetUrl createdAt}
	}}}}}}}}}`

func (g github) status(ctx context.Context, pr pull, withChecks bool) (pullStatus, []json.RawMessage, error) {
	var outcome pullStatus
	checks := []json.RawMessage{}
	var after *string
	for {
		var reply struct {
			Repository struct{ PullRequest pullStatus }
		}
		query := prQuery
		vars := map[string]any{"number": pr.Number, "after": after}
		if !withChecks {
			query = `query($number:Int!){repository(owner:"taskqorg",name:"fleet"){pullRequest(number:$number){
				id state headRefOid isDraft mergeable mergeStateStatus isMergeQueueEnabled
				autoMergeRequest{enabledAt} mergeQueueEntry{id state position}}}}`
			delete(vars, "after")
		}
		if err := g.graph(ctx, query, vars, &reply); err != nil {
			return outcome, nil, err
		}
		outcome = reply.Repository.PullRequest
		if outcome.ID != pr.ID || outcome.HeadRefOid != pr.Head.SHA || outcome.State == "" {
			return outcome, nil, errors.New("pull request head changed; read the current head before continuing")
		}
		if !withChecks || len(outcome.Commits.Nodes) == 0 {
			return outcome, checks, nil
		}
		commit := outcome.Commits.Nodes[0].Commit
		if commit.OID != pr.Head.SHA {
			return outcome, nil, errors.New("publisher-check-rollup-head-mismatch")
		}
		if commit.StatusCheckRollup == nil {
			return outcome, checks, nil
		}
		contexts := commit.StatusCheckRollup.Contexts
		checks = append(checks, contexts.Nodes...)
		if !contexts.PageInfo.HasNextPage {
			return outcome, checks, nil
		}
		next := contexts.PageInfo.EndCursor
		if next == "" || (after != nil && *after == next) {
			return outcome, nil, errors.New("publisher-check-rollup-page-invalid")
		}
		after = &next
	}
}

func (s pullStatus) autoMerge() bool {
	return len(s.AutoMergeRequest) != 0 && string(s.AutoMergeRequest) != "null"
}

func (g github) publication(ctx context.Context, op Operation) (map[string]any, error) {
	state := "open"
	if op.Action == "status" {
		state = "all"
	}
	pr, err := g.lookup(ctx, op, state)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"provider": "github", "host": Host, "repo": Repo, "kind": "pull_request", "branch": op.Branch,
		"providerActor": Actor, "publicationIdentity": Mode,
	}
	if op.Action == "publish_draft" {
		out["existing"] = pr.Number != 0
		if pr.Number == 0 {
			body := map[string]any{"head": op.Branch, "base": op.Base, "title": op.Title, "body": op.Body, "draft": true}
			if err := g.rest(ctx, http.MethodPost, repoPath+"/pulls", body, &pr); err != nil {
				return nil, err
			}
			if !pr.matches(op) || pr.User.Login != Actor || !pr.Draft || pr.State != "open" {
				return nil, errors.New("publisher-draft-creation-unconfirmed")
			}
		}
	}
	if pr.Number == 0 {
		if op.Action != "status" {
			return nil, errors.New("pull request is not open")
		}
		out["state"], out["published"] = "UNPUBLISHED", false
		if op.Head != "" {
			out["expectedHeadSha"], out["headMatchesExpected"] = op.Head, false
		}
		return out, nil
	}
	out["branch"] = pr.Head.Ref
	out["number"], out["url"], out["title"] = pr.Number, pr.URL, pr.Title
	out["headSha"], out["headRefOid"], out["headRefName"] = pr.Head.SHA, pr.Head.SHA, pr.Head.Ref
	out["baseBranch"], out["baseRefName"], out["isDraft"], out["pullRequestAuthor"] = pr.Base.Ref, pr.Base.Ref, pr.Draft, pr.User.Login
	if op.Action != "status" && (pr.State != "open" || (op.Head != "" && pr.Head.SHA != op.Head)) {
		return nil, errors.New("pull request is not open on the reviewed head")
	}
	if op.Action == "publish_draft" {
		return out, nil
	}
	s, checks, err := g.status(ctx, pr, op.Action == "status")
	if err != nil {
		return nil, err
	}
	if op.Action == "status" {
		out["state"], out["published"], out["isDraft"] = s.State, true, s.IsDraft
		out["mergeable"], out["mergeStateStatus"], out["statusCheckRollup"] = s.Mergeable, s.MergeStateStatus, checks
		out["autoMergeRequest"], out["autoMergeEnabled"] = s.AutoMergeRequest, s.autoMerge() || s.MergeQueueEntry != nil
		if s.MergeQueueEntry != nil {
			out["mergeQueueEntry"] = s.MergeQueueEntry
		}
		if op.Head != "" {
			out["expectedHeadSha"], out["headMatchesExpected"] = op.Head, pr.Head.SHA == op.Head
		}
		return out, nil
	}
	if s.State != "OPEN" {
		return nil, errors.New("pull request is not open")
	}
	out["action"], out["prNumber"], out["prUrl"] = op.Action, pr.Number, pr.URL
	switch op.Action {
	case "record_code_review":
		return out, g.review(ctx, op, out)
	case "mark_ready":
		if s.IsDraft {
			err = g.graph(ctx, `mutation($id:ID!){markPullRequestReadyForReview(input:{pullRequestId:$id}){pullRequest{id}}}`, map[string]any{"id": pr.ID}, nil)
		}
		out["requested"], out["alreadyReady"], out["isDraft"] = true, !s.IsDraft, false
	case "request_merge":
		if s.IsDraft {
			return nil, errors.New("pull request is still a draft")
		}
		if s.MergeQueueEntry == nil && !s.autoMerge() {
			op.Head = pr.Head.SHA
			err = g.requestMerge(ctx, op, s)
		}
		out["requested"], out["autoMergeEnabled"], out["mergeMethod"] = true, true, op.MergeMethod
	case "cancel_merge_request":
		err = g.cancelMerge(ctx, s)
		active := s.autoMerge() || s.MergeQueueEntry != nil
		out["alreadyInactive"], out["cancelled"], out["autoMergeEnabled"] = !active, active, false
	}
	return out, err
}

func (g github) review(ctx context.Context, op Operation, out map[string]any) error {
	base := map[string]any{"state": "success", "context": "code-review", "description": op.Description}
	if op.TargetURL != "" {
		base["target_url"] = op.TargetURL
	}
	if err := g.rest(ctx, http.MethodPost, repoPath+"/statuses/"+op.Head, base, nil); err != nil {
		return err
	}
	var statuses []struct {
		Context     string `json:"context"`
		State       string `json:"state"`
		Description string `json:"description"`
		Target      string `json:"target_url"`
		Updated     string `json:"updated_at"`
		Creator     struct {
			Login string `json:"login"`
		} `json:"creator"`
	}
	// The list is ordered newest first and supplies creator identity, which the combined status omits.
	if err := g.rest(ctx, http.MethodGet, repoPath+"/commits/"+op.Head+"/statuses?per_page=100", nil, &statuses); err != nil {
		return err
	}
	for _, v := range statuses {
		if v.Context != "code-review" {
			continue
		}
		if v.State != "success" || v.Description != op.Description || v.Target != op.TargetURL || v.Creator.Login != Actor {
			break
		}
		out["action"], out["context"], out["state"] = "code_review", "code-review", "success"
		out["description"], out["targetUrl"] = op.Description, op.TargetURL
		out["readbackVerified"], out["statusCreator"], out["updatedAt"] = true, v.Creator.Login, v.Updated
		return nil
	}
	return errors.New("publisher-code-review-readback-unconfirmed")
}

func (g github) requestMerge(ctx context.Context, op Operation, s pullStatus) error {
	if s.IsMergeQueueEnabled == nil {
		return errors.New("publisher-merge-policy-unverified")
	}
	args := map[string]any{"id": s.ID, "head": op.Head}
	mergeable := s.MergeStateStatus == "CLEAN" || s.MergeStateStatus == "HAS_HOOKS" || s.MergeStateStatus == "UNSTABLE"
	if *s.IsMergeQueueEnabled && mergeable {
		return g.graph(ctx, `mutation($id:ID!,$head:GitObjectID!){enqueuePullRequest(input:{pullRequestId:$id,expectedHeadOid:$head}){mergeQueueEntry{id}}}`, args, nil)
	}
	if *s.IsMergeQueueEnabled {
		return g.graph(ctx, `mutation($id:ID!,$head:GitObjectID!){enablePullRequestAutoMerge(input:{pullRequestId:$id,expectedHeadOid:$head}){pullRequest{id}}}`, args, nil)
	}
	args["method"] = strings.ToUpper(op.MergeMethod)
	if mergeable {
		return g.graph(ctx, `mutation($id:ID!,$head:GitObjectID!,$method:PullRequestMergeMethod!){mergePullRequest(input:{pullRequestId:$id,expectedHeadOid:$head,mergeMethod:$method}){pullRequest{id}}}`, args, nil)
	}
	return g.graph(ctx, `mutation($id:ID!,$head:GitObjectID!,$method:PullRequestMergeMethod!){enablePullRequestAutoMerge(input:{pullRequestId:$id,expectedHeadOid:$head,mergeMethod:$method}){pullRequest{id}}}`, args, nil)
}

func (g github) cancelMerge(ctx context.Context, s pullStatus) error {
	if s.MergeQueueEntry != nil {
		if err := g.graph(ctx, `mutation($id:ID!){dequeuePullRequest(input:{id:$id}){mergeQueueEntry{id}}}`, map[string]any{"id": s.ID}, nil); err != nil {
			return err
		}
	}
	if s.autoMerge() {
		return g.graph(ctx, `mutation($id:ID!){disablePullRequestAutoMerge(input:{pullRequestId:$id}){pullRequest{id}}}`, map[string]any{"id": s.ID}, nil)
	}
	return nil
}
