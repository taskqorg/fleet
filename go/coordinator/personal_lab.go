package main

import (
	"errors"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

func init() {
	opGates["lab_promote"] = labPromotionAuthority
	opGates["lab_personal_promote"] = labPromotionAuthority
	op("lab_personal_list", personalLabList)
	op("lab_personal_save", personalLabSave)
	op("lab_personal_withdraw", personalLabWithdraw)
	op("lab_personal_promote", personalLabPromote)
}

func labPromotionAuthority(q *Request) error {
	if !hasRole(q.person, "architect") {
		return fail(403, "Architect role required to promote into shared Fleet")
	}
	return nil
}

func personalLabAuthority(p *Person) error {
	if !hasPersonalLab(p) {
		return fail(403, "Personal Lab access required")
	}
	return nil
}

func setPersonalLabScope(q *Request, review bool) error {
	author := ""
	if hasPersonalLab(q.person) {
		author = q.person.Email
	}
	return q.db().PersonalLabDraftScope(q.ctx(), db.PersonalLabDraftScopeParams{Author: author, Review: review})
}

func personalLabRow(r db.PersonalLabDraft) contract.LabPersonalDraft {
	return contract.LabPersonalDraft{Id: r.ID, Author: r.Author, Project: r.Project, Pipeline: r.Pipeline,
		Variant: r.Variant, PipelineRevision: r.PipelineRevision, PromotedMethod: r.PromotedMethod}
}

func personalLabList(q *Request, _ *contract.LabPersonalListRequest) (contract.LabPersonalListReply, error) {
	out := contract.LabPersonalListReply{Drafts: []contract.LabPersonalDraft{}}
	review := hasRole(q.person, "architect")
	if !review && !hasPersonalLab(q.person) {
		return out, fail(403, "Personal Lab access required")
	}
	if err := setPersonalLabScope(q, review); err != nil {
		return out, err
	}
	rows, err := q.db().PersonalLabDrafts(q.ctx())
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		out.Drafts = append(out.Drafts, personalLabRow(r))
	}
	return out, nil
}

func personalLabSave(q *Request, r *contract.LabPersonalSaveRequest) (contract.LabPersonalSaveReply, error) {
	var out contract.LabPersonalSaveReply
	if err := labIDArg(r.Id, "draft id"); err != nil {
		return out, err
	}
	if err := personalLabAuthority(q.person); err != nil {
		return out, err
	}
	if err := setPersonalLabScope(q, false); err != nil {
		return out, err
	}
	variant := strings.TrimSpace(r.Variant)
	if variant == "" {
		return out, fail(400, "Enter a variant name")
	}
	p := db.PersonalLabDraftSaveParams{ID: r.Id, Author: q.person.Email, Project: strings.TrimSpace(r.Project),
		Pipeline: strings.TrimSpace(r.Pipeline), Variant: variant}
	if valueOr(r.Submit) {
		if p.Project == "" || !mayAccessProjectID(q.person, p.Project) || !projectKnown(q, p.Project) {
			return out, fail(404, "Choose a shared project you can access")
		}
		if st := strings.Trim(methodSlug.ReplaceAllString(strings.ToLower(variant), "-"), "-"); st == "" {
			return out, fail(400, "A submitted variant name must include a letter or digit")
		}
		head, err := sharedLabPipeline(q, p.Pipeline)
		if err != nil {
			return out, err
		}
		p.PipelineRevision = &head.Head
	}
	row, err := q.db().PersonalLabDraftSave(q.ctx(), p)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, fail(409, "This draft is locked or unavailable")
	}
	return personalLabRow(row), err
}

func sharedLabPipeline(q *Request, id string) (*definitionRow, error) {
	if err := labIDArg(id, "shared pipeline ID"); err != nil {
		return nil, err
	}
	head, err := unscoped(q, func() (*definitionRow, error) { return headOf(q, "pipeline", id, true) })
	if err != nil {
		return nil, err
	}
	if head == nil {
		return nil, fail(404, "Choose an existing shared pipeline")
	}
	if state := head.state(); state == contract.DefinitionStateRepositoryChanged || state == contract.DefinitionStateRepositoryDeleted {
		return nil, fail(409, "Resolve the shared pipeline's repository changes before submitting or promoting")
	}
	return head, nil
}

func personalLabWithdraw(q *Request, r *contract.LabPersonalWithdrawRequest) (contract.LabPersonalWithdrawReply, error) {
	var out contract.LabPersonalWithdrawReply
	if err := labIDArg(r.Id, "draft id"); err != nil {
		return out, err
	}
	if err := personalLabAuthority(q.person); err != nil {
		return out, err
	}
	if err := setPersonalLabScope(q, false); err != nil {
		return out, err
	}
	n, err := q.db().PersonalLabDraftWithdraw(q.ctx(), db.PersonalLabDraftWithdrawParams{ID: r.Id, Author: q.person.Email})
	if err == nil && n == 0 {
		return out, fail(404, "No unpublished draft found")
	}
	return contract.LabPersonalWithdrawReply{Id: r.Id}, err
}

func personalLabPromote(q *Request, r *contract.LabPersonalPromoteRequest) (contract.LabPersonalPromoteReply, error) {
	var out contract.LabPersonalPromoteReply
	if err := labPromotionAuthority(q); err != nil {
		return out, err
	}
	if err := labIDArg(r.Id, "draft id"); err != nil {
		return out, err
	}
	if !r.Confirm {
		return out, fail(400, "Confirm publication into shared Fleet")
	}
	if err := setPersonalLabScope(q, true); err != nil {
		return out, err
	}
	row, err := q.db().PersonalLabDraftReview(q.ctx(), r.Id)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, fail(404, "Unknown submitted draft")
	}
	if err != nil {
		return out, err
	}
	if !mayAccessProjectID(q.person, row.Project) || !projectKnown(q, row.Project) {
		return out, fail(404, "Unknown shared project")
	}
	if r.PipelineRevision != *row.PipelineRevision {
		return out, fail(409, "Review the submitted pipeline revision before promoting")
	}
	if row.PromotedMethod != nil {
		return contract.LabPersonalPromoteReply{Method: *row.PromotedMethod}, nil
	}
	author, err := unscoped(q, func() (*Person, error) { return resolveAccessTx(q.ctx(), q.tx, row.Author) })
	if err != nil {
		return out, err
	}
	if !hasPersonalLab(author) || !mayAccessProjectID(author, row.Project) {
		return out, fail(409, "The author no longer has access to this draft's destination")
	}
	head, err := sharedLabPipeline(q, row.Pipeline)
	if err != nil {
		return out, err
	}
	if head.Head != *row.PipelineRevision {
		return out, fail(409, "The shared pipeline changed. Ask the author to resubmit the draft")
	}
	exp := &labExperiment{ProjectID: row.Project, PipelineID: row.Pipeline,
		Variants: []labVariant{{Label: row.Variant, ConfigRef: labConfigDigest(row.Pipeline, nil)}}}
	reply, err := promoteVariant(q, exp, row.Variant, "")
	if err != nil {
		return out, err
	}
	out.Method = deref(reply.Method.Id)
	if err := q.db().PersonalLabDraftPromoted(q.ctx(), db.PersonalLabDraftPromotedParams{ID: row.ID, PromotedMethod: &out.Method}); err != nil {
		return out, err
	}
	return out, nil
}
