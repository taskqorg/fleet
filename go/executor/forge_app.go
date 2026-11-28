package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"fleet/contract"
	"fleet/executor/supervise"
	"fleet/projectpublisher"
)

func appProbe(wait time.Duration) projectpublisher.Readiness {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return projectpublisher.Probe(ctx)
}

func appPublishing(ctx forgeContext, ref contract.RepoRef, action string, args publishingArgs) (M, error) {
	if ctx.ProjectId != "fleet" || ctx.HostId != projectpublisher.Executor ||
		!contract.ForgeRepoGranted(ctx.Repositories, ref) {
		return nil, errors.New("repository App publication authority unavailable")
	}
	if action == "capabilities" {
		out := publishingCapabilitiesReply(ctx, ref, githubAdapter{host: ref.Host})
		out["publicationIdentity"], out["providerActor"] = projectpublisher.Mode, projectpublisher.Actor
		ready := appProbe(3 * time.Second)
		out["appReadiness"] = ready
		if !ready.Ready {
			out["actions"] = []string{"capabilities", "ci_logs"}
		}
		return out, nil
	}
	if ctx.ReadOnly && action != "status" {
		return nil, errors.New("project-forge-read-only")
	}
	sourceContext := ctx
	if ctx.requestAppPublishing != nil {
		sourceContext.ExecutionId = ""
	}
	head, source, err := checkExpectedHeadAndSource(sourceContext, args.ExpectedHeadSha, args.SourceExecutionId)
	if err != nil {
		return nil, err
	}
	if !ctx.SkipWorktreeHead {
		if ctx.Branch == "" || (!ctx.ReportPublishing && action == "publish_draft" && ctx.Branch == ctx.Base) ||
			(ctx.ReportPublishing && ctx.Branch != ctx.SessionBranch) {
			return nil, errors.New("execution branch changed")
		}
		if err := checkAppLocalHead(ctx, head, action == "publish_draft" || action == "record_code_review"); err != nil {
			return nil, err
		}
	}
	if action == "publish_draft" {
		if ctx.SkipWorktreeHead || args.Title == nil || args.Body == nil || strings.TrimSpace(*args.Title) == "" {
			return nil, errors.New("invalid App draft publication")
		}
		if head == "" {
			b, err := boundedOutput(appLocalGit(ctx, "rev-parse", "--verify", "HEAD^{commit}"), probeWait)
			head = strings.TrimSpace(string(b))
			if err != nil || !validForgeHex40(head) {
				return nil, errors.New("execution head unavailable")
			}
		}
	}
	req := contract.PublishingAppRequest{HostId: ctx.HostId, ExecutionId: ctx.ExecutionId, CommandId: ctx.CommandId,
		AgentToolKey: ctx.CommandId + ":publishing-app:" + newID(), Action: action, RequestId: args.RequestId, ExpectedHeadSha: optStr(head),
		Title: args.Title, Body: args.Body, Lease: optStr(args.leaseSHA), MergeMethod: args.MergeMethod,
		ArtifactId: args.ArtifactId, Description: args.Description}
	request := ctx.requestAppPublishing
	if request == nil {
		request = func(r contract.PublishingAppRequest) (contract.PublishingAppReply, error) {
			return appJournalRequest(ctx, r)
		}
	}
	reply, err := request(req)
	if err != nil {
		return nil, err
	}
	if reply.Error != nil {
		return nil, errors.New(*reply.Error)
	}
	if action == "publish_draft" {
		if reply.Ticket == nil || *reply.Ticket == "" {
			return nil, errors.New("repository App push ticket missing")
		}
		pushed, err := appPush(ctx, head, *reply.Ticket)
		if err != nil {
			return nil, err
		}
		reply.Result = pushed.Result
	}
	value, err := dec(string(reply.Result))
	out, ok := value.(M)
	if err != nil || !ok {
		return nil, errors.New("repository publisher returned an invalid result")
	}
	if args.RequestId != nil {
		out["requestId"] = *args.RequestId
	}
	if source != "" && out["sourceExecutionId"] == nil {
		out["sourceExecutionId"] = source
	}
	pr := typed[forgePRView](out)
	adapter := githubAdapter{host: ref.Host}
	view := pubView{Number: pr.Number, URL: pr.URL, Title: pr.Title, IsDraft: pr.IsDraft, HeadRefOid: pr.HeadRefOid,
		HeadRefName: pr.HeadRefName, BaseRefName: pr.BaseRefName, State: pr.State, AutoMergeEnabled: typed[bool](out["autoMergeEnabled"])}
	if action != "status" {
		view.State = "OPEN"
	}
	recordPublishingToJournal(ctx, ref, adapter, action, view, str0(args.RequestId), out)
	return out, nil
}

func appJournalRequest(ctx forgeContext, req contract.PublishingAppRequest) (contract.PublishingAppReply, error) {
	var reply contract.PublishingAppReply
	if !isFile(ctx.Journal) || ctx.CommandId == "" || ctx.ExecutionId == "" {
		return reply, errors.New("repository publisher execution context unavailable")
	}
	db, err := openJournal(ctx.Journal)
	if err != nil {
		return reply, err
	}
	defer db.Close()
	if err := journalOnce(db, req.AgentToolKey, enc(req), contract.RoutePublishingApp.Path, ctx.ExecutionId); err != nil {
		return reply, err
	}
	r, err := wait(db, "delegation_requests", req.AgentToolKey, 35, false)
	if err != nil {
		return reply, err
	}
	if r == nil {
		return reply, errors.New("repository App operation unconfirmed; inspect live status before retry")
	}
	if r.Keys.Error.Set {
		v := typed[struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}](r.Value)
		return reply, errors.New(firstStr(v.Message, v.Error))
	}
	return contractOf[contract.PublishingAppReply](r.Value)
}

func appLocalGit(ctx forgeContext, args ...string) *exec.Cmd {
	cmd := cleanCmd("git", append([]string{"--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-C", ctx.Workdir}, args...)...)
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1")
	return cmd
}

func checkAppLocalHead(ctx forgeContext, expected string, fence bool) error {
	out, err := boundedOutput(appLocalGit(ctx, "symbolic-ref", "--short", "HEAD"), probeWait)
	if err != nil || strings.TrimSpace(string(out)) != ctx.Branch {
		return errors.New("execution branch changed")
	}
	if fence && expected != "" {
		out, err := boundedOutput(appLocalGit(ctx, "rev-parse", "--verify", "HEAD^{commit}"), probeWait)
		if err != nil || strings.TrimSpace(string(out)) != expected {
			return errors.New("local branch head does not match reviewed head_sha")
		}
	}
	return nil
}

func appPush(ctx forgeContext, head, ticket string) (projectpublisher.Reply, error) {
	var none projectpublisher.Reply
	if err := checkAppLocalHead(ctx, head, true); err != nil {
		return none, err
	}
	pack, err := os.CreateTemp("", "fleet-publish-pack-")
	if err != nil {
		return none, errors.New("cannot stage execution objects")
	}
	defer pack.Close()
	defer os.Remove(pack.Name())
	cmd := appLocalGit(ctx, "pack-objects", "--stdout", "--revs", "--compression=2")
	cmd.Stdin, cmd.Stdout = strings.NewReader(head+"\n"), pack
	job, err := supervise.Spawn(supervise.Spec{Cmd: cmd, Deadline: 45 * time.Second, KillLeftovers: true})
	if err != nil || job.Wait() != nil || job.TimedOut() {
		return none, errors.New("cannot pack execution objects")
	}
	if _, err := pack.Seek(0, 0); err != nil {
		return none, errors.New("cannot read execution objects")
	}
	c, cancel := context.WithTimeout(context.Background(), 95*time.Second)
	defer cancel()
	return projectpublisher.Push(c, ticket, pack)
}
