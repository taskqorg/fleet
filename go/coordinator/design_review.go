// A design decision is an addressed question about retained evidence. It does not complete a task or publish a check.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"slices"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
	"github.com/jackc/pgx/v5"
)

const designRework = "Request changes"
const designProfileID = "codex-designer"
const fleetDesignRepository = "github.com/taskqorg/fleet"

func fleetDesigner(project, profile string) bool {
	return project == "fleet" && profile == designProfileID
}

type designSeal struct {
	Revision  int                         `json:"revision"`
	RequestID string                      `json:"requestId"`
	Request   contract.DesignOfferRequest `json:"request"`
	Digest    string                      `json:"digest"`
}
type designData struct {
	Offer *designSeal `json:"designOffer,omitempty"`
}

type designOriginal struct {
	ID, MIME, SHA string
	Bytes         int64
}
type designCell struct {
	viewport, theme string
	width, height   int
}

var designMatrix = []designCell{{"desktop", "dark", 1440, 900}, {"mobile", "dark", 390, 844}, {"desktop", "light", 1440, 900}, {"mobile", "light", 390, 844}}

func designRecord(row db.Question) (*designSeal, error) {
	if len(row.Data) == 0 {
		return nil, nil
	}
	var d designData
	if err := json.Unmarshal(row.Data, &d); err != nil {
		return nil, err
	}
	return d.Offer, nil
}

func (s *designSeal) reply(row db.Question) contract.DesignReviewGetReply {
	state := string(row.State)
	var selected *string
	if row.State == db.QuestionStateAnswered {
		state = "rework"
		if deref(row.Answer) != designRework {
			state = "selected"
			selected = row.Answer
		}
	}
	return contract.DesignReviewGetReply{QuestionId: row.ID, ProjectId: deref(row.ProjectID), TaskId: deref(row.TaskID), ExecutionId: deref(row.ExecutionID), Repository: s.Request.Repository, Options: s.Request.Options, WalkArtifactId: s.Request.WalkArtifactId, Digest: s.Digest, State: state, Owner: row.AnsweredBy, Selected: selected}
}

// designEvidence checks identical case coverage, the full matrix, and original ids before resolving any bytes.
func designEvidence(r *contract.DesignOfferRequest, retain func(id, kind, sha string, c *contract.DesignReviewCase, cell *designCell) error) error {
	if strings.TrimSpace(r.RequestId) == "" || strings.TrimSpace(r.Repository) == "" || len(r.Options) < 1 || len(r.Options) > 3 {
		return fail(400, "Provide a request ID, repository and one to three options")
	}
	if err := retain(r.WalkArtifactId, "walk", "", nil, nil); err != nil {
		return err
	}
	keys := map[string]bool{}
	var scope []string
	for i, o := range r.Options {
		if !slices.Contains([]string{"A", "B", "C"}, o.Key) || keys[o.Key] || strings.TrimSpace(o.Label) == "" || len(o.Cases) == 0 {
			return fail(400, "Each design option needs a distinct key, label and cases")
		}
		keys[o.Key] = true
		if err := retain(o.SourceArtifactId, "source", "", nil, nil); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, c := range o.Cases {
			if seen[c.ScopeKey] || strings.TrimSpace(c.ScopeKey) == "" || !validDesignHash(c.Recipe) || len(c.Frames) != len(designMatrix) {
				return fail(400, "Incomplete or duplicate design case")
			}
			seen[c.ScopeKey] = true
			if i == 0 {
				scope = append(scope, c.ScopeKey)
			}
			if err := retain(c.RecipeArtifactId, "recipe", "", &c, nil); err != nil {
				return err
			}
			ids := map[string]bool{}
			for j, f := range c.Frames {
				cell := designMatrix[j]
				if ids[f.ArtifactId] || f.Theme != cell.theme || f.Viewport != cell.viewport || !validDesignHash(f.Sha256) || strings.TrimSpace(f.Critique) == "" {
					return fail(400, "Expected four distinct, critiqued PNG originals in matrix order")
				}
				ids[f.ArtifactId] = true
				if err := retain(f.ArtifactId, "png", f.Sha256, &c, &cell); err != nil {
					return err
				}
			}
		}
		if len(scope) != len(seen) || slices.ContainsFunc(scope, func(k string) bool { return !seen[k] }) {
			return fail(400, "Design options cover different scopes")
		}
	}
	return nil
}

func validDesignHash(s string) bool { return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == "" }

func designDigest(q *Request, r *contract.DesignOfferRequest, project, task, execution string, write, inspect bool) (string, error) {
	return readDesignDigest(q, r, project, task, execution, write, inspect, nil)
}

type designArtifactReader struct {
	sourceID string
	pngID    string
	read     func(id, kind, hash string, data []byte) error
}

func readDesignDigest(q *Request, r *contract.DesignOfferRequest, project, task, execution string, write, inspect bool, reader *designArtifactReader) (string, error) {
	originals := []designOriginal{}
	err := designEvidence(r, func(id, kind, expected string, c *contract.DesignReviewCase, cell *designCell) error {
		var a db.Artifact
		var err error
		if write {
			a, err = q.db().DesignOfferArtifact(q.ctx(), id)
		} else {
			a, err = q.db().ArtByID(q.ctx(), id)
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err != nil || id == "" || a.ID != id || a.State != "stored" || a.DeletedAt != nil || deref(a.TaskID) != task || deref(a.ProjectID) != project || deref(a.ExecutionID) != execution || a.Size <= 0 || !validDesignHash(a.ContentHash) || expected != "" && a.ContentHash != expected {
			return fail(409, "Design evidence is missing, changed, or outside this design execution")
		}
		mime := strings.ToLower(strings.Split(a.MediaType, ";")[0])
		if kind == "png" && mime != "image/png" || kind == "recipe" && mime != "application/json" || kind == "walk" && !slices.Contains([]string{"text/plain", "text/markdown", "application/json"}, mime) || kind == "source" && !slices.Contains([]string{"application/zip", "application/gzip", "application/x-tar", "application/octet-stream"}, mime) {
			return fail(409, "Unexpected design artifact type for "+kind)
		}
		if inspect && (kind == "recipe" || kind == "png") && (reader == nil || reader.pngID == "" || id == reader.pngID) {
			buf, err := artifactBytes(q.ctx(), q.app, id, 24<<20)
			if err != nil {
				return err
			}
			if sha256hex(buf) != a.ContentHash {
				return fail(409, "Stored design artifact hash does not match its bytes")
			}
			if err := checkDesignOriginal(buf, c, cell); err != nil {
				return err
			}
			if reader != nil && (kind == "recipe" || kind == "png" && id == reader.pngID) {
				if err := reader.read(id, kind, a.ContentHash, buf); err != nil {
					return err
				}
			}
		}
		if reader != nil && kind == "source" && id == reader.sourceID {
			buf, err := artifactBytes(q.ctx(), q.app, id, 64<<20)
			if err != nil {
				return err
			}
			if sha256hex(buf) != a.ContentHash {
				return fail(409, "Stored design source hash does not match its bytes")
			}
			if err := reader.read(id, kind, a.ContentHash, nil); err != nil {
				return err
			}
		}
		originals = append(originals, designOriginal{a.ID, mime, a.ContentHash, a.Size})
		return nil
	})
	if err != nil {
		return "", err
	}
	return sha256hex(jsonBytes(struct {
		Version, Project, Task, Execution string
		Request                           contract.DesignOfferRequest
		Originals                         []designOriginal
	}{"fleet-design-seal-v1", project, task, execution, *r, originals})), nil
}

// The preview builder hashes these fields in this order before it adds the HTML and recipe digests.
type designRecipe = contract.PreviewRecipe

func checkDesignOriginal(buf []byte, c *contract.DesignReviewCase, cell *designCell) error {
	if cell != nil {
		return checkDesignPNG(buf, *cell)
	}
	var r struct {
		designRecipe
		Digest string `json:"digest"`
		HTML   string `json:"html"`
	}
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.DisallowUnknownFields()
	if !json.Valid(buf) || dec.Decode(&r) != nil || r.Version != 1 || r.Scene+":"+r.State != c.ScopeKey ||
		r.Digest != c.Recipe || r.designRecipe.Digest() != c.Recipe || !validDesignHash(r.HTML) || !validDesignHash(r.CSS) ||
		r.Path == "" || r.Landmark == "" || r.DPR != 1 || !r.ReducedMotion || len(r.Inputs) == 0 || len(r.Matrix) != len(designMatrix) {
		return fail(409, "Retained recipe does not match the offered scene, source digest and capture settings")
	}
	for i, f := range r.Matrix {
		cell := designMatrix[i]
		if f.Viewport != cell.viewport || f.Theme != cell.theme || f.Width != cell.width || f.Height != cell.height {
			return fail(409, "Recipe capture matrix has changed")
		}
	}
	for i, f := range r.Inputs {
		if f.Path == "" || !validDesignHash(f.SHA256) || i > 0 && r.Inputs[i-1].Path >= f.Path {
			return fail(409, "Recipe needs the ordered and hashed inputs of the preview")
		}
	}
	return nil
}

func checkDesignPNG(buf []byte, cell designCell) error {
	cfg, err := png.DecodeConfig(bytes.NewReader(buf))
	if err != nil || cfg.Width != cell.width || cfg.Height != cell.height {
		return fail(409, "Original PNG has the wrong dimensions")
	}
	if _, err := png.Decode(bytes.NewReader(buf)); err != nil {
		return fail(409, "Original is not a complete PNG")
	}
	return nil
}

func activeDesignTask(q *Request, id, project string) error {
	if err := q.db().TaskLifecycleLock(q.ctx(), id); err != nil {
		return err
	}
	t, err := taskLoad(q, id)
	if err != nil {
		return err
	}
	if deref(t.ProjectID) != project || !mayAccessProjectID(q.person, project) {
		return fail(403, "Design task is outside the current project")
	}
	if t.State == db.TaskStatePaused || t.State == db.TaskStateCancelled || t.State == db.TaskStateFailed || t.State == db.TaskStateCompleted {
		return fail(409, "Design task is held or terminal")
	}
	return nil
}

func createDesignOffer(q *Request, r *contract.DesignOfferRequest) (contract.DesignOfferReply, error) {
	var out contract.DesignOfferReply
	execution, bound := strings.CutPrefix(q.agent, "execution:")
	if !bound || execution == "" {
		return out, fail(403, "Only the submitting design execution may offer evidence")
	}
	e, err := getExecution(q, execution)
	if err != nil {
		return out, err
	}
	link, err := q.db().TaskExecutionLink(q.ctx(), e.ID)
	if err != nil {
		return out, err
	}
	e.TaskID = deref(link.TaskID)
	e.ProjectID = deref(link.ProjectID)
	profile, _ := profileOf(e.AgentConfig)
	var snapshot projectSnapshot
	var settings runSettings
	_ = json.Unmarshal(e.ProjectSnapshot, &snapshot)
	_ = json.Unmarshal(snapshot.Settings, &settings)
	repo, ok := forgeSelectedRepoRef(settings, deref(e.HostID))
	if !fleetDesigner(e.ProjectID, profile.ID) || !ok || repo.Host+"/"+repo.Slug != fleetDesignRepository || r.Repository != fleetDesignRepository || e.TaskID == "" || e.ProjectID == "" {
		return out, fail(403, "Offer requires the design profile and its pinned project repository")
	}
	if err := activeDesignTask(q, e.TaskID, e.ProjectID); err != nil {
		return out, err
	}
	t, err := taskLoad(q, e.TaskID)
	if err != nil {
		return out, err
	}
	if !needsDesignSelection(t) {
		return out, fail(409, "Design offers need a Fleet task declared visual or unknown at intake")
	}
	prior, err := q.db().DesignOfferRequest(q.ctx(), db.DesignOfferRequestParams{TaskID: &e.TaskID, ExecutionID: &e.ID, RequestKey: r.RequestId})
	if err == nil {
		s, err := designRecord(prior)
		if err != nil {
			return out, err
		}
		if s == nil || string(jsonBytes(s.Request)) != string(jsonBytes(r)) {
			return out, fail(409, "Request ID already identifies a different design offer")
		}
		reply := s.reply(prior)
		latest, err := q.db().LatestDesignOffer(q.ctx(), &e.TaskID)
		if err != nil {
			return out, err
		}
		if latest.ID != prior.ID {
			reply.State = "superseded"
			reply.Selected = nil
		}
		return contract.DesignOfferReply(reply), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if e.terminal() || (e.StopRequested != nil && *e.StopRequested) || (e.TaskStopRequested != nil && *e.TaskStopRequested) {
		return out, fail(409, "Design execution has ended")
	}
	es, err := agentExecutions(q, e.SessionID)
	if err != nil {
		return out, err
	}
	if len(es) == 0 || es[len(es)-1].ID != e.ID {
		return out, fail(409, "Design execution has been superseded")
	}
	revision := 1
	latest, err := q.db().LatestDesignOffer(q.ctx(), &e.TaskID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if latest.ID != deref(r.Replaces) {
		return out, fail(409, "Design offer has been replaced")
	}
	if latest.ID != "" {
		prev, err := designRecord(latest)
		if err != nil {
			return out, err
		}
		if prev == nil {
			return out, fail(409, "Previous design offer is unreadable")
		}
		writer, err := designWriter(latest)
		if err != nil {
			return out, err
		}
		if writer != "" {
			build, err := getExecution(q, writer)
			if err != nil {
				return out, err
			}
			if !build.terminal() {
				return out, fail(409, "Stop the active implementation before replacing its design")
			}
		}
		revision = prev.Revision + 1
		if latest.State == db.QuestionStateOpen {
			if err := questionsWithdraw(q, []string{latest.ID}); err != nil {
				return out, err
			}
		}
	}
	owner := q.peopleSnapshot().person(ownerEmail(q))
	if owner == nil || owner.machine() {
		return out, fail(409, "No human Fleet owner is configured")
	}
	digest, err := designDigest(q, r, e.ProjectID, e.TaskID, e.ID, true, true)
	if err != nil {
		return out, err
	}
	seal := designSeal{Revision: revision, RequestID: r.RequestId, Request: *r, Digest: digest}
	text, options, err := designQuestion(q, r)
	if err != nil {
		return out, err
	}
	id, err := askQuestionAs(q, questionAsk{Kind: "operator_decision", ProjectID: e.ProjectID, TaskID: e.TaskID, ExecutionID: &e.ID, SessionID: &e.SessionID, AskedBy: &q.agent, Text: text, Options: jsonBytes(options), Data: jsonBytes(designData{Offer: &seal}), DirectToOwner: true})
	if err != nil {
		return out, err
	}
	if err := q.db().AddressQuestion(q.ctx(), db.AddressQuestionParams{Holder: owner.Email, ID: id}); err != nil {
		return out, err
	}
	row, err := q.db().TaskQuestion(q.ctx(), id)
	if err != nil {
		return out, err
	}
	return contract.DesignOfferReply(seal.reply(row)), nil
}

// Artifact entity codes are rendered as navigable result cards by every web question surface.
func designQuestion(q *Request, r *contract.DesignOfferRequest) (string, []string, error) {
	var b strings.Builder
	code := func(id string) (string, error) {
		a, err := q.db().DesignOfferArtifact(q.ctx(), id)
		if err != nil {
			return "", err
		}
		if deref(a.Code) == "" {
			return "", fail(409, "Design artifact needs a result code")
		}
		return *a.Code, nil
	}
	walk, err := code(r.WalkArtifactId)
	if err != nil {
		return "", nil, err
	}
	fmt.Fprintf(&b, "Choose a design for %s. Live walk %s.", r.Repository, walk)
	keys := []string{}
	for _, o := range r.Options {
		source, err := code(o.SourceArtifactId)
		if err != nil {
			return "", nil, err
		}
		keys = append(keys, o.Key)
		fmt.Fprintf(&b, "\n\n%s. %s · %s", o.Key, o.Label, source)
		for _, c := range o.Cases {
			fmt.Fprintf(&b, "\n%s", c.ScopeKey)
			for _, f := range c.Frames {
				frame, err := code(f.ArtifactId)
				if err != nil {
					return "", nil, err
				}
				fmt.Fprintf(&b, "\n%s %s · %s · %s", f.Viewport, f.Theme, frame, f.Critique)
			}
		}
	}
	return b.String(), append(keys, designRework), nil
}

func designMayAnswer(q *Request, row db.Question, s *designSeal, answer string) error {
	if q.agent != "" || q.caller != nil || q.person == nil || !q.person.Owner || q.person.machine() || deref(row.AddressedTo) != q.person.Email {
		return fail(403, "Only the addressed Fleet owner can choose a design")
	}
	if err := activeDesignTask(q, deref(row.TaskID), deref(row.ProjectID)); err != nil {
		return err
	}
	latest, err := q.db().LatestDesignOffer(q.ctx(), row.TaskID)
	if err != nil {
		return err
	}
	if latest.ID != row.ID || latest.State == db.QuestionStateWithdrawn || latest.State == db.QuestionStateAnswered && answer != deref(latest.Answer) {
		return fail(409, "Design decision has already changed")
	}
	if answer == designRework {
		return nil
	}
	if !slices.ContainsFunc(s.Request.Options, func(o contract.DesignReviewOption) bool { return o.Key == answer }) {
		return fail(400, "Choose an offered design option or request changes")
	}
	digest, err := designDigest(q, &s.Request, deref(row.ProjectID), deref(row.TaskID), deref(row.ExecutionID), true, false)
	if err != nil {
		return err
	}
	if digest != s.Digest {
		return fail(409, "The reviewed evidence has changed")
	}
	return nil
}

func getDesignReview(q *Request, r *contract.DesignReviewGetRequest) (contract.DesignReviewGetReply, error) {
	var out contract.DesignReviewGetReply
	t, err := taskLoad(q, r.TaskId)
	if err != nil {
		return out, err
	}
	if !mayAccessProjectID(q.person, deref(t.ProjectID)) {
		return out, errNotFound
	}
	row, err := q.db().LatestDesignOffer(q.ctx(), &t.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, errNotFound
	}
	if err != nil {
		return out, err
	}
	s, err := designRecord(row)
	if err != nil {
		return out, err
	}
	if s == nil {
		return out, errNotFound
	}
	digest, err := designDigest(q, &s.Request, deref(t.ProjectID), t.ID, deref(row.ExecutionID), false, false)
	if err != nil {
		return out, err
	}
	if digest != s.Digest {
		return out, fail(409, "Retained design evidence has changed")
	}
	reply := s.reply(row)
	switch t.State {
	case db.TaskStatePaused, db.TaskStateCancelled, db.TaskStateFailed:
		reply.State, reply.Selected = "inactive", nil
	}
	return reply, nil
}

func init() { op("design_offer", createDesignOffer); op("design_review_get", getDesignReview) }
