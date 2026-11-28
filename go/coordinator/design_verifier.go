package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
	"github.com/jackc/pgx/v5"
)

const designVerifierIdentity = "fleet-design-verifier"
const designVerifierAudit = "design-verifier:taskqorg/fleet"

func designVerifierPerson(p *Person) bool {
	return isViewer(p) && p.Email == designVerifierIdentity &&
		len(p.Grants) == 1 && p.Grants[0] == (personGrant{"project", "fleet"})
}

func designVerifierCaller(q *Request) bool {
	if !designVerifierPerson(q.person) || q.r == nil || q.agent != "" || q.caller != nil {
		return false
	}
	configured := q.peopleSnapshot().person(designVerifierIdentity)
	return configured != nil && configured.AccessCommonName != "" &&
		strings.EqualFold(configured.AccessCommonName, q.r.Header.Get("X-Fleet-Person"))
}

// The evidence reader and observation writer use a dedicated Cloudflare machine identity.
func designVerifierRequest(p *Person, r *http.Request) bool {
	if !designVerifierPerson(p) || r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/api/ops/me", "/api/ops/design_verifier_evidence", "/api/ops/design_verifier_status", "/api/ops/design_verifier_observe":
		return true
	}
	return false
}

func designVerifierObservationField(o contract.DesignVerifierObservation, now int64) string {
	if !forgeSHAPattern.MatchString(o.Revision) {
		return ""
	}
	hasToken := o.KeyFingerprint != nil || o.AuthenticatedAt != nil || o.ExpiresAt != nil
	hasCheck := o.Head != nil || o.ExternalId != nil || o.CheckRunId != nil
	switch o.Kind {
	case "provider":
		fingerprint, prefixed := strings.CutPrefix(deref(o.KeyFingerprint), "SHA256:")
		publicHash, err := base64.StdEncoding.DecodeString(fingerprint)
		if !hasCheck && o.State == "ready" && prefixed && err == nil && len(publicHash) == 32 &&
			deref64(o.AuthenticatedAt) > 0 && deref64(o.AuthenticatedAt) < deref64(o.ExpiresAt) && deref64(o.ExpiresAt) > now {
			return "provider-ready"
		}
		if !hasCheck && !hasToken && o.State == "unknown" {
			return "provider-unknown"
		}
	case "pull_request", "merge_group":
		if !hasToken && forgeSHAPattern.MatchString(deref(o.Head)) && validDesignHash(deref(o.ExternalId)) && deref64(o.CheckRunId) > 0 &&
			(o.State == "success" || o.State == "failure" || o.State == "pending") {
			return o.Kind
		}
	}
	return ""
}

func observeDesignVerifier(q *Request, r *contract.DesignVerifierObserveRequest) (contract.OkReply, error) {
	if !designVerifierCaller(q) {
		return contract.OkReply{}, fail(403, "The protected Fleet verifier identity is required")
	}
	field := designVerifierObservationField(*r, nowMs())
	if field == "" {
		return contract.OkReply{}, fail(400, "Invalid verifier observation")
	}
	if err := audit(q, designVerifierAudit, field, nil, r, auditExtra{ProjectID: ptr("fleet")}); err != nil {
		return contract.OkReply{}, err
	}
	return contract.OkReply{Ok: true}, nil
}

func getDesignVerifierStatus(q *Request, _ *contract.DesignVerifierStatusRequest) (contract.DesignVerifierStatusReply, error) {
	var out contract.DesignVerifierStatusReply
	if !mayAccessProjectID(q.person, "fleet") {
		return out, errNotFound
	}
	configured := q.peopleSnapshot().person(designVerifierIdentity)
	out = contract.DesignVerifierStatusReply{Service: designVerifierIdentity, Repository: fleetDesignRepository,
		RepositoryId: 1450484391, AppId: 5962560, InstallationId: 186718702,
		Permissions:             []string{"checks:write", "contents:read", "pull_requests:read", "metadata:read"},
		FleetIdentityConfigured: designVerifierPerson(configured) && configured.AccessCommonName != "", Provider: "unknown"}
	rows, err := q.db().DesignVerifierLatest(q.ctx())
	if err != nil {
		return out, err
	}
	var latestProviderAt time.Time
	for _, row := range rows {
		record := &contract.DesignVerifierObservationRecord{At: row.At.UnixMilli()}
		if err := json.Unmarshal(row.Value, &record.Observation); err != nil {
			return out, err
		}
		switch row.Field {
		case "provider-ready", "provider-unknown":
			if row.At.After(latestProviderAt) {
				out.LatestProvider = record
				latestProviderAt = row.At
			}
			if row.Field == "provider-ready" {
				out.LastReady = record
			}
		case "pull_request":
			out.LatestPR = record
		case "merge_group":
			out.LatestMergeGroup = record
		}
	}
	if o := out.LatestProvider; out.FleetIdentityConfigured && o != nil &&
		o.Observation.State == "ready" && deref64(o.Observation.ExpiresAt) > nowMs() {
		out.Provider = "ready"
	}
	return out, nil
}

func getDesignVerifierEvidence(q *Request, r *contract.DesignVerifierEvidenceRequest) (contract.DesignVerifierEvidenceReply, error) {
	var out contract.DesignVerifierEvidenceReply
	if !designVerifierCaller(q) && !(q.person != nil && q.person.Owner && !q.person.machine() && q.agent == "" && q.caller == nil) {
		return out, fail(403, "The Fleet verifier or human owner is required")
	}
	if r.Number < 1 || !forgeSHAPattern.MatchString(r.Head) || !validForgeBranch(r.Branch) {
		return out, fail(400, "Provide an exact PR number, head and branch")
	}
	rows, err := q.db().DesignVerifierPublishingSources(q.ctx(), db.DesignVerifierPublishingSourcesParams{
		Number: strconv.FormatInt(r.Number, 10), Head: r.Head, Branch: r.Branch,
	})
	if err != nil {
		return out, err
	}
	if len(rows) == 0 || rows[0].Task == "" {
		return out, errNotFound
	}
	task := rows[0].Task
	for _, row := range rows {
		if row.Task != task {
			return out, fail(409, "The PR head maps to more than one Fleet task")
		}
	}
	return verifyDesignWriter(q, r, task, rows)
}

func verifyDesignWriter(q *Request, r *contract.DesignVerifierEvidenceRequest, task string, sources []db.DesignVerifierPublishingSourcesRow) (contract.DesignVerifierEvidenceReply, error) {
	var out contract.DesignVerifierEvidenceReply
	t, err := taskLoad(q, task)
	if err != nil {
		return out, err
	}
	if !needsDesignSelection(t) || t.State == db.TaskStatePaused || t.State == db.TaskStateCancelled ||
		t.State == db.TaskStateFailed || t.State == db.TaskStateCompleted {
		return out, fail(409, "No active design admission for this Fleet task")
	}
	row, err := q.db().LatestDesignOffer(q.ctx(), &task)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, errNotFound
	}
	if err != nil {
		return out, err
	}
	seal, err := designRecord(row)
	if err != nil {
		return out, err
	}
	if seal == nil || seal.Request.Repository != fleetDesignRepository || row.State != db.QuestionStateAnswered ||
		deref(row.AnsweredBy) == "" || deref(row.AnsweredBy) != deref(row.AddressedTo) || deref(row.Answer) == designRework {
		return out, fail(409, "The Fleet owner has no current selection for this PR")
	}
	writer, err := designWriter(row)
	if err != nil {
		return out, err
	}
	matched := false
	for _, source := range sources {
		matched = matched || source.Execution == writer
	}
	if !matched {
		return out, fail(409, "The exact PR head was not observed for the admitted Fleet writer")
	}
	option := slices.IndexFunc(seal.Request.Options, func(o contract.DesignReviewOption) bool { return o.Key == deref(row.Answer) })
	if option < 0 {
		return out, fail(409, "The selected option is absent")
	}
	selectedRecipes := make(map[string]string)
	for _, c := range seal.Request.Options[option].Cases {
		selectedRecipes[c.RecipeArtifactId] = c.ScopeKey
	}
	recipes := []contract.DesignVerifierRecipe{}
	digest, err := readDesignDigest(q, &seal.Request, "fleet", task, deref(row.ExecutionID), false, true, func(id string, bytes []byte) error {
		if key := selectedRecipes[id]; key != "" {
			r := contract.DesignVerifierRecipe{ScopeKey: key}
			if err := json.Unmarshal(bytes, &r); err != nil {
				return err
			}
			recipes = append(recipes, r)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	if digest != seal.Digest {
		return out, fail(409, "The original design evidence has changed")
	}
	return contract.DesignVerifierEvidenceReply{Number: r.Number, Head: r.Head, Branch: r.Branch, Writer: writer, Review: seal.reply(row), Recipes: recipes}, nil
}

func init() {
	op("design_verifier_status", getDesignVerifierStatus)
	op("design_verifier_observe", observeDesignVerifier)
	op("design_verifier_evidence", getDesignVerifierEvidence)
}
