// Approval cards for radical actions (FLT-35, docs/designs/fleet-telegram.md#approvals-approvalsgo). An agent calls approval_ask
// before an action; Fleet rates it by the project's risk rules. A radical action becomes a card: an agent question
// with Approve / Not now whose data.approval holds the exact scope. Only an approver answers it (approvalMayAnswer,
// checked in agentReplyTo for every path: the web app, Telegram, the reply tool); the answer reaches the asking session
// as any reply does, and the agent reads the state with the same requestId.
package main

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("approval_ask", approvalAsk)
}

// approvalRiskDefault: the permissive defaults; a project's settings.risks rates any action differently.
var approvalRiskDefault = map[string]string{
	"read": "free", "analyze": "free", "mockup": "free", "task": "free", "dev-branch": "free",
	"dev-deploy": "reversible", "test-data": "reversible",
	"live-deploy": "radical", "delete-accounts": "radical", "ban-accounts": "radical", "user-messages": "radical",
	"keys": "radical", "access": "radical", "app": "radical", "money": "radical", "data-drop": "radical", "other-radical": "radical",
}

// approvalOwnerOnly: actions only the owner approves unless settings.approvals names others.
var approvalOwnerOnly = []string{"keys", "access", "app", "money"}

const approvalYes, approvalNo = "Approve", "Not now"

// approvalScope: what the card shows and what an answer approves; a different scope is a different card.
type approvalScope struct {
	RequestID string `json:"requestId"`
	Action    string `json:"action"`
	Risk      string `json:"risk"`
	What      string `json:"what"`
	Where     string `json:"where"`
	Version   string `json:"version,omitempty"`
	Rollback  string `json:"rollback,omitempty"`
}

func (a approvalScope) same(b approvalScope) bool {
	return a.Action == b.Action && a.What == b.What && a.Where == b.Where && a.Version == b.Version && a.Rollback == b.Rollback
}

// approvalSettings: settings.risks {action: free|reversible|radical} and settings.approvals {action: [emails]}.
type approvalSettings struct {
	Risks     map[string]string   `json:"risks"`
	Approvals map[string][]string `json:"approvals"`
}

func approvalSettingsOf(q *Request, projectID string) approvalSettings {
	var s approvalSettings
	if projectID != "" {
		if raw := projectSettingsOf(q, projectID); raw != nil {
			lenientDecode(jsonBytes(raw), &s)
		}
	}
	return s
}

func approvalRisk(s approvalSettings, action string) string {
	if r := s.Risks[action]; r == "free" || r == "reversible" || r == "radical" {
		return r
	}
	if r, ok := approvalRiskDefault[action]; ok {
		return r
	}
	return "radical"
}

// approvers: who may approve an action of a project: the owner; the people settings.approvals names for it; else, for
// all but keys and money, every person with a grant to the project. An agent identity (a {kind: "*"} grant without
// the owner flag, such as site-agents) approves nothing unless named: an approval is a person's decision.
func approvers(q *Request, projectID, action string) []*Person {
	s := approvalSettingsOf(q, projectID)
	named, hasNamed := s.Approvals[action]
	out := []*Person{}
	for _, p := range q.app.peopleSnapshot().list() {
		switch {
		case p.Owner:
			out = append(out, p)
		case action == serviceGrantApproval:
		case hasNamed && slices.Contains(named, p.Email):
			out = append(out, p)
		case hasNamed || slices.Contains(approvalOwnerOnly, action) || fullAccess(p):
		case mayAccessProjectID(p, projectID):
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b *Person) int {
		if a.Owner != b.Owner {
			if a.Owner {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Email, b.Email)
	})
	return out
}

func personName(p *Person) string {
	if p.Name != "" {
		return p.Name
	}
	return p.Email
}

func approverNames(ps []*Person) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, personName(p))
	}
	return out
}

// approvalText: the card: the exact scope and who it waits for.
func approvalText(a approvalScope, waiting []string) string {
	var b strings.Builder
	b.WriteString("Approval needed: " + a.What + "\nWhere: " + a.Where)
	if a.Version != "" {
		b.WriteString("\nVersion: " + a.Version)
	}
	if a.Rollback != "" {
		b.WriteString("\nRollback: " + a.Rollback)
	}
	b.WriteString("\nWaiting for: " + strings.Join(waiting, ", "))
	return b.String()
}

func approvalAsk(q *Request, r *contract.ApprovalAskRequest) (contract.ApprovalAskReply, error) {
	out := contract.ApprovalAskReply{WaitingFor: []string{}}
	if r.RequestId == "" || strings.TrimSpace(r.What) == "" || strings.TrimSpace(r.Where) == "" {
		return out, fail(400, "Expected requestId, what and where")
	}
	e, err := getExecution(q, r.ExecutionId)
	if err != nil {
		return out, err
	}
	s, err := sessionByID(q, e.SessionID)
	if err != nil {
		return out, err
	}
	project := e.ProjectID
	if project == "" {
		project = s.ProjectID
	}
	scope := approvalScope{RequestID: r.RequestId, Action: string(r.Action), What: strings.TrimSpace(r.What), Where: strings.TrimSpace(r.Where),
		Version: strings.TrimSpace(deref(r.Version)), Rollback: strings.TrimSpace(deref(r.Rollback))}
	scope.Risk = approvalRisk(approvalSettingsOf(q, project), scope.Action)
	out.Risk = contract.ApprovalRisk(scope.Risk)
	if scope.Risk != "radical" {
		out.State = contract.ApprovalStateNotNeeded
		return out, nil
	}
	ps := approvers(q, project, scope.Action)
	out.WaitingFor = approverNames(ps)
	prev, err := q.db().ApprovalByRequest(q.ctx(), db.ApprovalByRequestParams{SessionID: &s.ID, RequestID: r.RequestId})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if err == nil {
		var old approvalScope
		_ = json.Unmarshal(prev.Approval, &old)
		if old.same(scope) {
			out.QuestionId = &prev.ID
			switch {
			case prev.State == "open":
				out.State = contract.ApprovalStateOpen
			case prev.State == "answered" && prev.Answer == approvalYes:
				out.State, out.AnsweredBy = contract.ApprovalStateApproved, &prev.AnsweredBy
			case prev.State == "answered":
				out.State, out.AnsweredBy = contract.ApprovalStateDeclined, &prev.AnsweredBy
			default:
				out.State = contract.ApprovalStateWithdrawn
			}
			return out, nil
		}
		// A changed scope withdraws the old card: an approval covers exactly what it named.
		if prev.State == "open" {
			if err := questionsWithdraw(q, []string{prev.ID}); err != nil {
				return out, err
			}
		}
	}
	id := newID()
	asked := "execution:" + e.ID
	if _, err := askQuestionAs(q, questionAsk{ID: id, Kind: "agent_question", Text: approvalText(scope, out.WaitingFor), ProjectID: project,
		TaskID: e.TaskID, ExecutionID: &e.ID, SessionID: &e.SessionID, AskedBy: &asked,
		Options: jsonBytes([]string{approvalYes, approvalNo}), Data: jsonBytes(map[string]approvalScope{"approval": scope})}); err != nil {
		return out, err
	}
	out.QuestionId, out.State = &id, contract.ApprovalStateOpen
	return out, nil
}

// approvalMayAnswer: nil unless the question is an approval card the caller may not answer: agents never, people only
// when they are approvers of its action.
func approvalMayAnswer(q *Request, questionID string) error {
	a, err := q.db().ApprovalOf(q.ctx(), questionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var scope approvalScope
	_ = json.Unmarshal(a.Approval, &scope)
	ps := approvers(q, a.ProjectID, scope.Action)
	if q.agent == "" && q.person != nil && slices.ContainsFunc(ps, func(p *Person) bool { return p.Email == q.person.Email }) {
		return nil
	}
	return fail(403, "This approval waits for "+strings.Join(approverNames(ps), ", "))
}
