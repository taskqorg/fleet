package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

const projectSiteProblemSource = "agent-site-access"

func (a *App) reconcileProjectSiteProblems(ctx context.Context) error {
	return a.store.Write(ctx, nil, []string{"problems:" + projectSiteProblemSource}, func(q *Request) error {
		heads, err := headTexts(q, "project")
		if err != nil {
			return err
		}
		held, err := q.db().ProblemsOpenOfSource(q.ctx(), projectSiteProblemSource)
		if err != nil {
			return err
		}
		active := map[string]bool{}
		loadedHosts := false
		var hosts []projectSetupHost
		now := nowMs()
		for _, head := range heads {
			project := agentView[siteAccessProject](projectHeadSettings(head.id, head.text))
			if project.Config == nil || project.Config.Mode != contract.AgentSiteAccessModeWeb {
				continue
			}
			if !loadedHosts {
				loadedHosts = true
				hosts, err = projectSetupHosts(q, now)
				if err != nil {
					return err
				}
			}
			check, access := projectSiteSetup(q, head.id, project, hosts, now)
			if len(access.Targets) == 0 {
				access.Targets = []contract.ProjectSiteAccessTarget{{TargetId: "-", State: check.State, ReasonCode: check.ReasonCode, NextAction: check.NextAction}}
			}
			for _, target := range access.Targets {
				if target.State == contract.ProjectSetupCheckStateReady {
					continue
				}
				cause := "problem:agent-site-access:" + head.id + ":" + target.TargetId
				active[cause] = true
				if target.State != contract.ProjectSetupCheckStateMissing && target.State != contract.ProjectSetupCheckStateBlocked {
					continue // An unresolved check does not open or clear a finding.
				}
				text := projectSiteProblemText(target)
				open, err := q.db().ProblemOpenGet(q.ctx(), cause)
				if errors.Is(err, pgx.ErrNoRows) {
					p := Problem{Cause: cause, ProjectID: head.id, Rule: "R-PROJECT-SITE-ACCESS", Text: text}
					err = problemRaise(q, projectSiteProblemSource, p)
				} else if err == nil && open.Text != text {
					err = q.db().ProblemPresent(q.ctx(), db.ProblemPresentParams{ID: open.ID, Text: &text})
				}
				if err != nil {
					return err
				}
			}
		}
		var closed []string
		for _, p := range held {
			if !active[p.Cause] {
				closed = append(closed, p.ID)
			}
		}
		return problemsCloseIDs(q, closed, "cleared")
	})
}

func projectSiteProblemText(target contract.ProjectSiteAccessTarget) string {
	if target.TargetId == "-" {
		return "Agent Site Access needs a live site target."
	}
	subject := fmt.Sprintf("Agent Site Access for %s", target.Origin)
	switch target.ReasonCode {
	case "credential_expired":
		return subject + " needs credential rotation. The credential has expired."
	case "credential_unavailable":
		return subject + " needs a credential in the private store."
	case "grant_missing", "grant_invalid", "grant_does_not_match":
		return subject + " needs an owner-approved grant for this target."
	case "browser_environment_missing":
		return subject + " needs a browser-enabled environment."
	case "auth_rejected":
		return subject + " was refused by the site."
	case "identity_mismatch":
		return subject + " returned a different application identity."
	default:
		return subject + " needs another readiness check."
	}
}
