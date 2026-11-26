package main

// Who decides a Lab experiment (docs/designs/fleet-lab.md, Experiment record): a gate decides itself at close; an
// experiment an agent created is that agent's to decide; only an experiment a person created and that offers a choice
// between variants waits for that person (Lab's Waiting for you). Agents' decisions and failed gates show passively
// with their owner (OR-71), never as the person's question.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

// requestAgentRun: the agent run behind the request (q.agent execution:<id>), nil for a person's own call.
func requestAgentRun(q *Request) *string {
	if id, ok := strings.CutPrefix(q.agent, "execution:"); ok && id != "" {
		return &id
	}
	return nil
}

// decider: rule for a gate; execution:<id> for an experiment an agent run created (its bot or session decides);
// agent:<identity> for a machine identity's (site-agents); person:<email> for a person's.
func (e *labExperiment) decider() string {
	switch {
	case e.Gate:
		return "rule"
	case e.RequestedByExec != nil && *e.RequestedByExec != "":
		return "execution:" + *e.RequestedByExec
	case e.RequestedBy != nil && *e.RequestedBy != "" && !personEmail.MatchString(*e.RequestedBy):
		return "agent:" + *e.RequestedBy
	case e.RequestedBy != nil && *e.RequestedBy != "":
		return "person:" + *e.RequestedBy
	}
	return "agent:unknown" // legacy provenance (migration 37): nobody to ask
}

// labGateCause: the problem a failed gate raises, owned by the duty bot.
func labGateCause(id string) string { return "lab-gate:" + id }

// labGateDecide: a gate that stopped or finished writes its own decision: passed when every variant's last trial
// succeeded and passed its pass/fail check, failed otherwise; a failure is a problem for the duty bot (the agent CLI
// pin qualification raises its own, pins:cli:<sha>). A decision already made stays.
func labGateDecide(q *Request, id string) error {
	row, err := expLockedRow(q, id)
	if err != nil {
		return err
	}
	if !row.Gate || row.State != "done" && row.State != "stopped" || row.Decision != nil && *row.Decision != "pending" {
		return nil
	}
	e, err := expOf(row)
	if err != nil {
		return err
	}
	runs, err := expRuns(q, id)
	if err != nil {
		return err
	}
	scores, err := scoresCurrent(q, id)
	if err != nil {
		return err
	}
	labels := []string{}
	for _, v := range e.Variants {
		labels = append(labels, v.Label)
	}
	st := pinsTrialOutcome(labels, expTrials(runs), scores)
	decision, reason := "passed", fmt.Sprintf("%d of %d passed", st.Ok, st.Trials)
	if !st.Passed {
		decision = "failed"
		if !st.Done {
			reason += "; stopped before every variant ran"
		}
		if len(st.Failures) > 0 {
			reason += ": " + strings.Join(st.Failures[:min(len(st.Failures), 3)], "; ")
		}
	}
	if err := q.db().LabExperimentDecide(q.ctx(), db.LabExperimentDecideParams{ID: id, Decision: &decision, DecisionReason: &reason, DecidedBy: ptr("lab:gate")}); err != nil {
		return err
	}
	if row.PipelineID == pinsQualifyPipeline {
		return nil
	}
	if decision == "passed" {
		return problemClose(q, labGateCause(id))
	}
	return problemRaise(q, "lab", Problem{Cause: labGateCause(id), ProjectID: row.ProjectID,
		Text: labTitle(e) + " failed: " + reason + "."})
}

// labTitle: the experiment's human name: its title, else its Lab group, else its goal, else its pipeline.
func labTitle(e *labExperiment) string {
	for _, s := range []*string{e.Title, e.LabGroup, e.Goal} {
		if s != nil && strings.TrimSpace(*s) != "" {
			return strings.TrimSpace(*s)
		}
	}
	return e.PipelineID
}

// labViewDecisions: of the focus's experiments (newest first), the finished ones waiting for this person's choice
// between variants, and those whose open decision or failed gate is an agent's, with that agent's name.
func labViewDecisions(q *Request, exps []contract.LabExperimentRecord) (waiting []string, bots []contract.LabWithBot, err error) {
	waiting, bots = []string{}, []contract.LabWithBot{}
	me := ""
	if q.person != nil {
		me = "person:" + q.person.Email
	}
	names := map[string]string{}
	for _, x := range exps {
		pending := (x.State == "done" || x.State == "stopped") && deref(x.Decision) == "pending"
		failedGate := x.Gate && deref(x.Decision) == "failed"
		switch {
		case pending && x.Decider == me && len(x.Variants) >= 2:
			waiting = append(waiting, x.Id)
		case failedGate || pending && strings.HasPrefix(x.Decider, "execution:") || pending && strings.HasPrefix(x.Decider, "agent:"):
			owner, ok := names[x.Decider]
			if !ok {
				if owner, err = labDeciderName(q, x.Decider, failedGate); err != nil {
					return nil, nil, err
				}
				names[x.Decider] = owner
			}
			bots = append(bots, contract.LabWithBot{ExperimentId: x.Id, Owner: owner, Failed: failedGate})
		}
	}
	return waiting, bots, nil
}

// labDeciderName: the name the Lab shows for an agent decider: the bot of the run, else its session's title; a machine
// identity by its name; a failed gate's problem by the duty bot that owns it.
func labDeciderName(q *Request, decider string, gate bool) (string, error) {
	if gate {
		owner, err := problemOwner(q, "")
		if err != nil || owner == nil {
			return "duty bot", err
		}
		return botName(q, *owner)
	}
	if id, ok := strings.CutPrefix(decider, "agent:"); ok {
		return id, nil
	}
	id := strings.TrimPrefix(decider, "execution:")
	ex, err := q.db().ExecutionByID(q.ctx(), id)
	if err != nil {
		return "agent", nil // the run is gone: its decision stays an agent's
	}
	if ex.BotID != nil && *ex.BotID != "" {
		return botName(q, *ex.BotID)
	}
	if ex.SessionID != nil && *ex.SessionID != "" {
		return "session " + *ex.SessionID, nil
	}
	return "agent", nil
}

// botName: a bot's name (its code when it has no name, its id when neither).
func botName(q *Request, id string) (string, error) {
	b, err := q.db().BotGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, nil
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(b.Name) != "" {
		return b.Name, nil
	}
	if b.Code != nil && *b.Code != "" {
		return *b.Code, nil
	}
	return id, nil
}

// labTitleArg: a given title, trimmed; nil when blank.
func labTitleArg(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	return ptr(strings.TrimSpace(*s))
}
