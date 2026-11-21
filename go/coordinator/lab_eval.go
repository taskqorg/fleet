// Lab evaluation, scoring, script checks, metrics, comparisons, ratings, and promotion/recommendations.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

// ---- scores ----

// labShuffle: seeded shuffle (mulberry32 seeded from sha256(seed)), the same order as the TypeScript one.
func labShuffle(items []any, seed string) []any {
	h := sha256.Sum256([]byte(seed))
	a := binary.LittleEndian.Uint32(h[:4])
	rnd := func() float64 {
		a += 0x6D2B79F5
		t := (a ^ a>>15) * (1 | a)
		t = (t + (t^t>>7)*(61|t)) ^ t
		return float64(t^t>>14) / 4294967296
	}
	out := append([]any{}, items...)
	for i := len(out) - 1; i > 0; i-- {
		j := int(math.Floor(rnd() * float64(i+1)))
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func checkValue(form string, v any, trials []any) error {
	ok := false
	switch form {
	case "pass_fail":
		_, ok = v.(bool)
	case "scale":
		n, isInt := intOK(v)
		ok = isInt && n >= 1 && n <= 5
	case "pick_best":
		for _, t := range trials {
			ok = ok || t == v
		}
	case "metric":
		f, isNum := numOK(v)
		ok = isNum && !math.IsInf(f, 0) && !math.IsNaN(f)
	case "typed":
		m, isObject := labRawObject(rawOf(v)) // exact keys, as the value's own object
		_, isP := numOK(rawValue(m["p"]))
		answer, label := rawValue(m["answer"]), rawValue(m["label"])
		ok = isObject && (isP && (answer == "yes" || answer == "no" || answer == "abstain") && (label == "yes" || label == "no") || answer == "unscored" && rawValue(m["reason"]) != nil)
	}
	if !ok {
		return fail(400, "Invalid "+form+" value")
	}
	return nil
}

func parseRating(form, answer string, trials []any) (any, any, error) {
	head, rest, _ := strings.Cut(answer, ":")
	var note any
	if n := strings.TrimSpace(rest); n != "" {
		note = n
	}
	h := strings.TrimSpace(head)
	switch form {
	case "pass_fail":
		if h != "pass" && h != "fail" {
			return nil, nil, fail(400, "A pass_fail answer is pass or fail")
		}
		return h == "pass", note, nil
	case "scale":
		n, err := strconv.ParseFloat(h, 64)
		if err != nil {
			return nil, nil, fail(400, "Invalid scale value")
		}
		return n, note, checkValue("scale", n, nil)
	case "pick_best":
		v := strings.TrimSpace(answer)
		return v, nil, checkValue("pick_best", v, trials)
	}
	return nil, nil, fail(400, "Owner ratings are pass_fail, pick_best or scale")
}

// labCheckRun: a script check run of one trial (runChecks), the assessments it captured, then stored and linked
// (materializeLabAssessments), and what they were computed from (labWorkStep).
type labCheckRun struct {
	Passed             int                    `json:"passed"`
	Total              int                    `json:"total"`
	Failed             []string               `json:"failed"`
	Assessments        []labAssessmentCapture `json:"assessments,omitempty"`
	ProjectAssessments []labAssessmentRef     `json:"projectAssessments,omitempty"`
	AssessmentSnapshot *labAssessmentSnap     `json:"assessmentSnapshot,omitempty"`
}

// runChecks: each check (a shell command, exit 0 = pass) in a fresh directory holding the files, FLEET_OUTPUT_DIR set to it.
// Cancelling ctx kills the running check and returns ctx's error: the trial stays unscored, never failed.
func runChecks(ctx context.Context, checks []labCheck, files []labFile) (*labCheckRun, error) {
	dir, err := os.MkdirTemp("", "fleet-eval-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	seenFiles := map[string]bool{}
	for _, f := range files {
		if !pipelineInputPath(f.path) || seenFiles[f.path] {
			return nil, fail(400, "Invalid evaluator input path")
		}
		seenFiles[f.path] = true
		p := filepath.Join(dir, f.path)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, f.bytes, 0o644); err != nil {
			return nil, err
		}
	}
	out := &labCheckRun{Total: len(checks), Failed: []string{}, Assessments: []labAssessmentCapture{}}
	for _, c := range checks {
		// Never attribute a previous check's file to the current check.
		if err := os.RemoveAll(filepath.Join(dir, "evaluation")); err != nil {
			return nil, err
		}
		cmd := osexec.CommandContext(ctx, "sh", "-c", c.Run)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "FLEET_OUTPUT_DIR=" + dir}
		// On Unix the check runs in its own process group; cancelling, and finishing, kills the whole group, so a child the shell
		// left behind (`sleep 1000 &`) never outlives the check or its deleted directory.
		ownGroup(cmd)
		err := cmd.Run()
		killGroup(cmd)
		assessment, captureErr := captureLabAssessment(dir)
		if assessment != nil {
			assessment.CheckID = c.ID
			out.Assessments = append(out.Assessments, *assessment)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err() // capture precedes cleanup; cancelled work remains unscored
		}
		if err != nil || captureErr != nil {
			out.Failed = append(out.Failed, c.ID)
		}
	}
	out.Passed = len(checks) - len(out.Failed)
	return out, nil
}

// scoreView: a score row as the Lab reads and shows it.
func scoreView(r db.Score) labScore {
	trials := append([]string{}, r.TrialIds...)
	return labScore{ID: r.ID, ExperimentID: r.ExperimentID, TrialIDs: trials, Evaluator: r.Evaluator, EvaluatorVersion: r.EvaluatorVersion, Form: r.Form,
		Value: r.Value, SourceExecutionID: r.SourceExecutionID, QuestionID: r.QuestionID, Rationale: r.Rationale, ReplacesScoreID: r.ReplacesScoreID, CreatedAt: r.CreatedAt}
}

func scoresCurrent(q *Request, expID string) ([]labScore, error) {
	rs, err := q.db().LabScoresCurrent(q.ctx(), expID)
	out := []labScore{}
	for _, r := range rs {
		out = append(out, scoreView(r))
	}
	return out, err
}

// labAgreement: how often a human rating matches the judges' (agreementBase), overall and per judge version.
type labAgreement struct {
	Compared  int               `json:"compared"`
	Matched   int               `json:"matched"`
	Percent   *int              `json:"percent"`
	ByVersion []labAgreementRow `json:"byVersion,omitempty"`
}

// labAgreementRow: the agreement with one judge version.
type labAgreementRow struct {
	Compared int    `json:"compared"`
	Matched  int    `json:"matched"`
	Percent  *int   `json:"percent"`
	Version  string `json:"version"`
}

func agreementBase(scores []labScore) labAgreement {
	compared, matched := 0, 0
	var judges []labScore
	for _, s := range scores {
		if strings.HasPrefix(s.Evaluator, "judge:") {
			judges = append(judges, s)
		}
	}
	judgeOf := func(form string, trial string) *labScore {
		for i, j := range judges {
			if j.Form == form && j.first() == trial {
				return &judges[i]
			}
		}
		return nil
	}
	for _, o := range scores {
		if !strings.HasPrefix(o.Evaluator, "human:") {
			continue
		}
		switch o.Form {
		case "pass_fail":
			if j := judgeOf("pass_fail", o.first()); j != nil {
				compared++
				if j.value() == o.value() {
					matched++
				}
			}
		case "pick_best":
			ts := o.TrialIDs
			paired := false
			for _, j := range judges {
				if j.Form == "pick_best" && sameTrialSet(ts, j.TrialIDs) {
					compared++
					if j.value() == o.value() {
						matched++
					}
					paired = true
					break
				}
			}
			if paired {
				continue
			}
			vals, ok := []float64{}, true
			for _, t := range ts {
				j := judgeOf("scale", t)
				if j == nil {
					ok = false
					break
				}
				v, _ := numOK(j.value())
				vals = append(vals, v)
			}
			if !ok {
				continue
			}
			best := math.Inf(-1)
			for _, v := range vals {
				best = math.Max(best, v)
			}
			top := []string{}
			for i, v := range vals {
				if v == best {
					top = append(top, ts[i])
				}
			}
			if len(top) != 1 {
				continue
			}
			compared++
			if o.value() == any(top[0]) {
				matched++
			}
		}
	}
	var pct *int
	if compared > 0 {
		p := int(math.Round(100 * float64(matched) / float64(compared)))
		pct = &p
	}
	return labAgreement{Compared: compared, Matched: matched, Percent: pct}
}

// scoreNumber: a one-trial score as a number (pass is 1, fail 0; metric and scale as given).
func scoreNumber(sc labScore) float64 {
	if sc.Form == "typed" { // correct confident answer 1; a wrong one or an abstention 0
		if v := sc.typed(); (v.Answer == "yes" || v.Answer == "no") && v.Answer == v.Label {
			return 1
		}
		return 0
	}
	if sc.Form == "pass_fail" {
		if rawValue(sc.Value) == true {
			return 1
		}
		return 0
	}
	v, _ := numOK(rawValue(sc.Value))
	return v
}

func summarize(scores []labScore, trials []string) map[string]labSummary {
	in := map[string]bool{}
	for _, t := range trials {
		in[t] = true
	}
	type acc struct {
		form   string
		n      int
		values []float64
	}
	m, order := map[string]*acc{}, []string{}
	for _, sc := range scores {
		mine := false
		for _, t := range sc.TrialIDs {
			mine = mine || in[t]
		}
		if !mine || sc.unscored() {
			continue
		}
		k := sc.Evaluator
		e := m[k]
		if e == nil {
			e = &acc{form: sc.Form}
			m[k] = e
			order = append(order, k)
		}
		e.n++
		if sc.Form == "pick_best" {
			if in[sc.text()] {
				e.values = append(e.values, 1)
			}
		} else {
			e.values = append(e.values, scoreNumber(sc))
		}
	}
	out := map[string]labSummary{}
	for _, k := range order {
		e := m[k]
		r := labSummary{Form: e.form, N: e.n}
		sum := 0.0
		for _, v := range e.values {
			sum += v
		}
		switch e.form {
		case "pick_best":
			wins := len(e.values)
			r.Wins = &wins
		case "pass_fail":
			r.Passed = &sum
		default:
			if len(e.values) > 0 {
				lo, hi := math.Inf(1), math.Inf(-1)
				for _, v := range e.values {
					lo, hi = math.Min(lo, v), math.Max(hi, v)
				}
				mean := sum / float64(len(e.values))
				r.Mean, r.Min, r.Max = &mean, &lo, &hi
			}
		}
		out[k] = r
	}
	return out
}

// labSummary: one evaluator's scores of a set of trials (summarize).
type labSummary struct {
	Form   string   `json:"form"`
	N      int      `json:"n"`
	Wins   *int     `json:"wins,omitempty"`
	Passed *float64 `json:"passed,omitempty"`
	Mean   *float64 `json:"mean,omitempty"`
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
}

// expEvaluate: automatic evaluators over completed trials; true when every completed trial is evaluated by each.
func expEvaluate(q *Request, exp *labExperiment, trials, runs []*labRun, current []labScore, launched *[]string, checked map[string]*labCheckRun) (bool, error) {
	_, latest := expLatest(trials)
	complete, typedExamples := true, map[string]labExample{} // each pinned example read once per pass
	for _, e := range exp.Evaluators {
		if e.Kind == "judge" && e.Form == "pick_best" {
			done, err := pairwiseEvaluate(q, exp, e, trials, runs, current, launched)
			if err != nil {
				return false, err
			}
			complete = complete && done
		}
	}
	for _, t := range trials {
		if !comparable(t) {
			continue
		}
		for _, e := range exp.Evaluators {
			if e.Kind == "human" || e.Kind == "judge" && e.Form == "pick_best" {
				continue
			}
			name := e.name()
			has := false
			for _, s := range current {
				has = has || s.Evaluator == name && s.first() == t.ID
			}
			if has {
				continue
			}
			pin := exp.pin(t.cell().Example)
			if e.Kind == "typed" {
				if err := scoreTyped(q, exp, t, pin, name, typedExamples); err != nil {
					return false, err
				}
				continue
			}
			if e.Kind == "script" {
				if l := latest[t.cell().key()]; l == nil || l.ID != t.ID {
					continue // superseded evidence cannot link or obstruct the current attempt
				}
				r := checked[t.ID]
				if r == nil {
					complete = false
					continue
				}
				full, err := exampleOf(q, pin)
				if err != nil {
					return false, err
				}
				if len(r.ProjectAssessments) > 0 {
					if e.Form != "pass_fail" {
						return false, fail(409, "Project assessment contract checks require pass_fail")
					}
					if err := validateLabAssessmentSnapshot(q, t.run, full, r); err != nil {
						return false, err
					}
					if err := linkLabAssessments(q, t.run, r); err != nil {
						return false, err
					}
				}
				var value any = len(r.Failed) == 0
				if e.Form == "metric" {
					value = 0.0
					if r.Total > 0 {
						value = float64(r.Passed) / float64(r.Total)
					}
				}
				rat := fmt.Sprintf("%v of %v checks pass", r.Passed, r.Total)
				if len(r.ProjectAssessments) > 0 {
					rat += "; project assessment contract only, see retained artifacts for quality"
				}
				if len(r.Failed) > 0 {
					rat = "failed: " + strings.Join(r.Failed, ", ")
				}
				if _, err := scoreInsert(q, labNewScore{ExperimentID: exp.ID, ProjectID: exp.ProjectID, TrialIDs: []string{t.ID}, Evaluator: name,
					EvaluatorVersion: sha256hex(jsonBytes(full.Checks))[:16], Form: e.Form, Value: value, Rationale: &rat}); err != nil {
					return false, err
				}
				continue
			}
			var run *labRun
			for _, r := range runs {
				if j := r.Judge; j != nil && j.Evaluator == name && j.TrialID == t.ID {
					run = r
				}
			}
			if run == nil && exp.State != "running" {
				complete = false
				continue
			}
			if run == nil {
				full, err := exampleOf(q, pin)
				if err != nil {
					return false, err
				}
				files, err := trialFiles(q, t, full)
				if err != nil {
					return false, err
				}
				inputs := []labExampleInput{}
				for _, f := range files {
					// Judges inspect renders and measurements, never the source GLB.
					if strings.HasSuffix(strings.ToLower(f.path), ".glb") {
						continue
					}
					id, err := t.run.saveArtifact(q, t.ID, "judge/"+f.path, f.bytes, http.DetectContentType(f.bytes))
					if err != nil {
						return false, err
					}
					inputs = append(inputs, labExampleInput{ArtifactID: id, Path: "example/trial/" + f.path})
				}
				jid, err := labStartRun(q, exp, labRunStart{RequestID: "lab:" + sha256hex(jsonBytes([]any{exp.ID, "judge", name, t.ID})), File: e.Ref + ".json",
					Example: labJudgeExample{Prompt: full.Prompt, Form: e.Form}, Variant: &labVariant{Overrides: e.Overrides, ConfigRef: sha256hex(jsonBytes(e.Overrides))},
					Extra: labRunExtra{ExampleInputs: jsonBytes(inputs), Judge: &labJudge{Evaluator: name, TrialID: t.ID}}})
				if err != nil {
					return false, err
				}
				if jid != "" {
					*launched = append(*launched, jid)
				}
				complete = false
				continue
			}
			if !run.terminal() {
				complete = false
				continue
			}
			if run.State != "succeeded" {
				continue // a failed judge run counts as evaluated; it has no score
			}
			var last struct {
				Value     json.RawMessage `json:"value"`
				Rationale *string         `json:"rationale"`
			}
			json.Unmarshal(run.outputObject(), &last)
			if checkValue(e.Form, rawValue(last.Value), nil) != nil {
				continue
			}
			ver := "unknown"
			if d := run.DefinitionSha256; len(d) >= 16 {
				ver = sha256hex(jsonBytes([]any{d, e.Overrides}))[:16]
			}
			if _, err := scoreInsert(q, labNewScore{ExperimentID: exp.ID, ProjectID: exp.ProjectID, TrialIDs: []string{t.ID}, Evaluator: name, EvaluatorVersion: ver,
				Form: e.Form, Value: rawValue(last.Value), SourceExecutionID: &run.ID, Rationale: last.Rationale}); err != nil {
				return false, err
			}
		}
	}
	return complete, nil
}

var methodSlug = regexp.MustCompile(`[^a-z0-9]+`)

// labMethod: a catalogue method as Promote writes it and reads it back (the catalogue's methods).
type labMethod struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Scope            string          `json:"scope"`
	Pipeline         string          `json:"pipeline"`
	Overrides        json.RawMessage `json:"overrides"`
	ConfigRef        string          `json:"configRef,omitempty"`
	SourceExperiment string          `json:"sourceExperiment,omitempty"`
}

// overridesValue: the method's overrides decoded ({} when it has none), as its config digest reads them.
func (m labMethod) overridesValue() any {
	if _, ok := labRawObject(m.Overrides); ok {
		return decodeNumbers(string(m.Overrides))
	}
	return decodeNumbers("{}")
}

// Promote: the variant as a catalogue method, saved as a catalogue revision (source lab) unless a method with its
// config_ref exists; then the project recommendation is set to it (recommend: the recommendation_set operation).
func expPromote(q *Request, id, label string) (contract.LabPromoteReply, error) {
	var out contract.LabPromoteReply
	exp, err := expRow(q, id)
	if err != nil {
		return out, err
	}
	v := exp.variant(label)
	if v == nil {
		return out, fail(400, "Unknown variant")
	}
	identity := firstString(v.CatalogueConfigRef, v.ConfigRef)
	slug := strings.Trim(methodSlug.ReplaceAllString(strings.ToLower(label), "-"), "-")
	// Bands steer the Lab's typed evaluator; a method run applies no overrides of that kind, so they are left out and named.
	overrides := map[string]json.RawMessage{}
	var note *string
	vo, _ := labRawObject(jsonBytes(v.Overrides))
	for k, x := range vo {
		if k == "bands" {
			n := fmt.Sprintf("bands %s are a Lab scoring setting and do not travel with the method", jsonBytes(rawValue(x)))
			note = &n
			continue
		}
		overrides[k] = jsonBytes(rawValue(x))
	}
	pipeline := variantPipelineID(exp, v)
	method := labMethod{ID: fmt.Sprintf("%v-%s", pipeline, slug), Name: fmt.Sprintf("%v %s", pipeline, label), Scope: exp.ProjectID, Pipeline: pipeline + ".json",
		Overrides: jsonBytes(overrides), ConfigRef: identity, SourceExperiment: id}
	digest := labConfigDigestOf(pipeline, rawValue(jsonBytes(overrides)))
	// A method's config ref is its configRef, else the hash Lab gives a variant (pipeline id and overrides).
	refOf := func(m labMethod) string {
		if m.ConfigRef != "" {
			return m.ConfigRef
		}
		return sha256hex([]byte(canonical(labConfigIdentity{Overrides: m.overridesValue(), Pipeline: strings.TrimSuffix(m.Pipeline, ".json")})))
	}
	var existing json.RawMessage
	base, err := catalogueBase(q, exp.ProjectID)
	if err != nil {
		return out, err
	}
	c, _ := asT[struct {
		Methods []json.RawMessage `json:"methods"`
	}](base)
	for _, raw := range c.Methods {
		var m labMethod
		json.Unmarshal(raw, &m)
		if refOf(m) == identity && labConfigDigestOf(strings.TrimSuffix(m.Pipeline, ".json"), m.overridesValue()) == digest {
			existing = raw
		}
	}
	// A new method is saved as a catalogue revision (source lab) in this transaction, so the recommendation below sees it
	// (fleet-definition-editors.md, Promote); the repository file stays the seed.
	if existing == nil {
		head, err := headOf(q, "catalogue", "catalogue", true)
		if err != nil {
			return out, err
		}
		if head == nil {
			return out, fail(404, "No catalogue definition")
		}
		if st := head.state(); st == contract.DefinitionStateRepositoryChanged || st == contract.DefinitionStateRepositoryDeleted {
			return out, &HTTPError{Code: 409, Msg: "The catalogue's repository file changed: resolve it in the Catalogue editor, then promote", Extra: conflictExtra(contract.ConflictReasonRepositoryChanged, head.Head, head.RepoSha)}
		}
		var content map[string]json.RawMessage // numbers keep their text
		if _, ok := labRawObject(head.Content); !ok || json.Unmarshal(head.Content, &content) != nil {
			return out, fail(500, "Invalid catalogue definition")
		}
		var methods []json.RawMessage
		json.Unmarshal(content["methods"], &methods)
		ids := map[string]bool{}
		for _, m := range methods {
			var x struct {
				ID any `json:"id"`
			}
			json.Unmarshal(m, &x)
			if s, ok := x.ID.(string); ok {
				ids[s] = true
			}
		}
		for base, n := method.ID, 2; ids[method.ID]; n++ { // an id already taken gets a number
			method.ID = fmt.Sprintf("%s-%d", base, n)
		}
		content["methods"] = jsonBytes(append(methods, jsonBytes(method)))
		text, err := canonicalText(jsonBytes(content))
		if err != nil {
			return out, err
		}
		if ps := problems("catalogue", "catalogue", text); len(ps) > 0 {
			return out, fail(400, "The promoted catalogue is invalid: "+ps[0].Message)
		}
		rev, err := appendRevision(q, "catalogue", "catalogue", head.Head, &text, head.RepoSha, "lab", fmt.Sprintf("Promote %s from %s", label, id))
		if err != nil {
			return out, err
		}
		out.Catalogue = &contract.DefinitionRef{Kind: contract.DefinitionKindCatalogue, Id: "catalogue", Revision: rev.Revision, ContentSha: rev.ContentSha}
		existing = jsonBytes(method)
	}
	var chosen labMethod
	json.Unmarshal(existing, &chosen)
	rec, err := recommend(q, exp.ProjectID, chosen.ID)
	if err != nil {
		return out, err
	}
	m, err := asT[contract.LabPromotedMethod](existing)
	if err != nil {
		return out, err
	}
	out.CataloguePatch, out.Method, out.Recommendation, out.Note = json.RawMessage("null"), m, jsonBytes(rec), note
	return out, nil
}

func expRate(q *Request, r *contract.LabRatingCreateRequest) (labRatingView, error) {
	eid, err := expID(r.ExperimentId, "experimentId")
	if err != nil {
		return labRatingView{}, err
	}
	exp, err := expRow(q, eid)
	if err != nil {
		return labRatingView{}, err
	}
	form := string(r.Form)
	if form != "pass_fail" && form != "pick_best" && form != "scale" {
		return labRatingView{}, fail(400, "form is pass_fail, pick_best or scale")
	}
	if r.TrialIds == nil {
		return labRatingView{}, fail(400, "Expected trialIds")
	}
	ids := []string{}
	for _, x := range r.TrialIds {
		s, err := expID(x, "trial")
		if err != nil {
			return labRatingView{}, err
		}
		ids = append(ids, s)
	}
	if form == "pick_best" && len(ids) < 2 {
		return labRatingView{}, fail(400, "pick_best rates two or more trials")
	}
	if form != "pick_best" && len(ids) != 1 {
		return labRatingView{}, fail(400, form+" rates one trial")
	}
	runs, err := expRuns(q, eid)
	if err != nil {
		return labRatingView{}, err
	}
	trials := expTrials(runs)
	for _, t := range ids {
		var x *labRun
		for _, y := range trials {
			if y.ID == t {
				x = y
			}
		}
		if x == nil {
			return labRatingView{}, fail(400, fmt.Sprint("Unknown trial ", t))
		}
		if !comparable(x) {
			return labRatingView{}, fail(409, fmt.Sprint("Trial ", t, " has no comparable result"))
		}
	}
	text := "Rate this output from 1 to 5"
	if form == "pick_best" {
		text = fmt.Sprintf("Pick the best of %d outputs", len(ids))
	} else if form == "pass_fail" {
		text = "Pass or fail this output"
	}
	qid, err := askRow(q, "rating", text, exp.ProjectID, labRatingAsk{Form: form, ExperimentID: eid, TrialIDs: ids, Seed: newID()}, "operator", labBudgetAsk{ExperimentID: eid})
	if err != nil {
		return labRatingView{}, err
	}
	return labRating(q, qid)
}

// labRatingAsk: a rating question's options as expRate writes them (labRatingOptions reads them back).
type labRatingAsk struct {
	Form              string   `json:"form"`
	ExperimentID      string   `json:"experiment_id,omitempty"`
	ExampleArtifactID string   `json:"example_artifact_id,omitempty"`
	TrialIDs          []string `json:"trial_ids,omitempty"`
	Seed              string   `json:"seed"`
}

// labRating: outputs in the seeded order, labelled A, B, ...; never the variant.
func labRating(q *Request, qid string) (labRatingView, error) {
	row, err := q.db().LabRating(q.ctx(), qid)
	if errors.Is(err, pgx.ErrNoRows) {
		return labRatingView{}, fail(404, "Unknown question")
	}
	if err != nil {
		return labRatingView{}, err
	}
	var o labRatingOptions
	json.Unmarshal(row.Options, &o)
	var d labQuestionData
	json.Unmarshal(row.Data, &d)
	var runs []*labRun
	if eid := deref(o.ExperimentID); eid != "" {
		if runs, err = expRuns(q, eid); err != nil {
			return labRatingView{}, err
		}
	}
	items := []labRatingItem{}
	for i, tid := range labShuffle(o.TrialIDs, o.Seed) {
		files := []labExampleInput{}
		for _, t := range runs {
			if t.ID == tid {
				fs, err := t.run.outputFiles(q, -1)
				if err != nil {
					return labRatingView{}, err
				}
				for _, f := range fs {
					files = append(files, labExampleInput{ArtifactID: f.ArtifactID, Path: f.Path})
				}
			}
		}
		items = append(items, labRatingItem{Label: string(rune('A' + i)), TrialID: tid, Files: files})
	}
	return labRatingView{ID: row.ID, Kind: row.Kind, Form: o.Form, ExperimentID: o.ExperimentID, ExampleArtifactID: o.ExampleArtifactID, State: row.State, Answer: row.Answer,
		Text: row.Text, Items: items, Withdrawal: d.Withdrawal}, nil
}

// labRatingOptions: a rating question's options (expRate, importDraft).
type labRatingOptions struct {
	ExperimentID      *string `json:"experiment_id"`
	ExampleArtifactID *string `json:"example_artifact_id"`
	Form              *string `json:"form"`
	TrialIDs          []any   `json:"trial_ids"`
	Seed              string  `json:"seed"`
}

// labQuestionData: the Lab fields of a question's data (a budget ask, a withdrawal).
type labQuestionData struct {
	ExperimentID *string         `json:"experimentId"`
	Budget       json.RawMessage `json:"budget"`
	Withdrawal   json.RawMessage `json:"withdrawal"`
}

// labQuestion: an answered or answering question as pipelines.go hands it over.
type labQuestion struct {
	ID      string           `json:"id"`
	Kind    string           `json:"kind"`
	Answer  string           `json:"answer"`
	Options labRatingOptions `json:"options"`
	Data    labQuestionData  `json:"data"`
}

func (q labQuestion) budget() bool {
	return q.Data.ExperimentID != nil && rawValue(q.Data.Budget) != nil
}

// labRatingView: a rating with its outputs in the seeded order (labRating).
type labRatingView struct {
	ID                string          `json:"id"`
	Kind              string          `json:"kind"`
	Form              *string         `json:"form"`
	ExperimentID      *string         `json:"experimentId"`
	ExampleArtifactID *string         `json:"exampleArtifactId"`
	State             string          `json:"state"`
	Answer            *string         `json:"answer"`
	Text              string          `json:"text"`
	Items             []labRatingItem `json:"items"`
	Withdrawal        json.RawMessage `json:"withdrawal"`
}

type labRatingItem struct {
	Label   string            `json:"label"`
	TrialID any               `json:"trialId"`
	Files   []labExampleInput `json:"files"`
}

func labRatings(q *Request, experimentID, state *string) (contract.LabRatingsListReply, error) {
	out := contract.LabRatingsListReply{Ratings: []contract.LabRating{}}
	rs, err := q.db().LabRatingIDs(q.ctx(), db.LabRatingIDsParams{ExperimentID: experimentID, State: state})
	if err != nil {
		return out, err
	}
	for _, id := range rs {
		x, err := labRating(q, id)
		if err != nil {
			return out, err
		}
		r, err := asT[contract.LabRating](x)
		if err != nil {
			return out, err
		}
		out.Ratings = append(out.Ratings, r)
	}
	return out, nil
}

// labAnswerCheck and labAnswerAfter: the answer hook for questions_answer (questions_answer calls both inside its transaction:
// check before recording the answer, after once it is recorded). question: the question row (snake_case columns).
func labAnswerCheck(q *Request, question json.RawMessage, answer string) error {
	x, _ := asT[labQuestion](question)
	// Lab ratings are the owner's (scored as human:owner; a pass on a drafted example lets the coordinator run its checks)
	// and budget asks move the owner's spend: owner only.
	lab := x.Kind == "rating" && (x.Options.ExampleArtifactID != nil || x.Options.ExperimentID != nil) || x.budget()
	if lab && q.person != nil && !fullAccess(q.person) {
		return fail(403, "Only the owner answers Lab ratings and budget questions")
	}
	if x.Kind == "rating" {
		_, _, err := parseRating(deref(x.Options.Form), answer, x.Options.TrialIDs)
		return err
	}
	if x.budget() && answer != "resume" && answer != "stop" {
		return fail(400, "Answer resume or stop")
	}
	return nil
}

// labNoteText: a rating's note (parseRating), when it has one.
func labNoteText(note any) *string {
	if s, ok := note.(string); ok {
		return &s
	}
	return nil
}

func labAnswerAfter(q *Request, question json.RawMessage) error {
	x, _ := asT[labQuestion](question)
	o := x.Options
	eid := ""
	switch {
	case x.Kind == "rating" && o.ExperimentID != nil:
		v, rat, err := parseRating(deref(o.Form), x.Answer, o.TrialIDs)
		if err != nil {
			return err
		}
		exp, err := q.db().LabExperiment(q.ctx(), *o.ExperimentID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "Unknown experiment")
		}
		if err != nil {
			return err
		}
		form := deref(o.Form)
		if _, err := scoreInsert(q, labNewScore{ExperimentID: exp.ID, ProjectID: exp.ProjectID, TrialIDs: labIDStrings(o.TrialIDs), Evaluator: "human:owner:" + form,
			EvaluatorVersion: "1", Form: form, Value: v, QuestionID: strPtr(x.ID), Rationale: labNoteText(rat)}); err != nil {
			return err
		}
		eid = exp.ID
	case x.Kind == "rating" && o.ExampleArtifactID != nil:
		if v, _, err := parseRating("pass_fail", x.Answer, nil); err == nil && v == true {
			return q.db().LabExampleApproveOpen(q.ctx(), *o.ExampleArtifactID)
		}
		return nil
	case x.budget():
		to := "stopped"
		if x.Answer == "resume" {
			to = "running"
		}
		var owner *string
		if to == "stopped" {
			owner = ptr("owner")
		}
		if _, err := experimentTransition(q, *x.Data.ExperimentID, experimentEvent{To: to, From: []string{"paused"}, StopReason: owner}); err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		eid = *x.Data.ExperimentID
	default:
		return nil
	}
	return labEnqueue(q, eid)
}

// scoreCorrect: a correction keeps the trials, evaluator and form; only a current score can be corrected.
func scoreCorrect(q *Request, r *contract.LabScoreCorrectRequest) (labScore, error) {
	id := r.ScoreId
	old, err := q.db().LabScore(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return labScore{}, fail(404, "Unknown score")
	}
	if err != nil {
		return labScore{}, err
	}
	if old.Form == "typed" {
		return labScore{}, fail(400, "A typed score follows from the trial's p, its bands and the label; correct the example or the bands instead")
	}
	if err := acquireTxLock(q, "experiment:"+old.ExperimentID); err != nil {
		return labScore{}, err
	}
	n, err := q.db().LabScoreReplacedCount(q.ctx(), &id)
	if err != nil {
		return labScore{}, err
	}
	if n > 0 {
		return labScore{}, fail(409, "Score already corrected")
	}
	nid, err := scoreInsert(q, labNewScore{ExperimentID: old.ExperimentID, ProjectID: old.ProjectID, TrialIDs: append([]string{}, old.TrialIds...), Evaluator: old.Evaluator,
		EvaluatorVersion: old.EvaluatorVersion, Form: old.Form, Value: rawValue(r.Value), QuestionID: old.QuestionID, SourceExecutionID: old.SourceExecutionID,
		Rationale: r.Rationale, Replaces: &id})
	if err != nil {
		return labScore{}, err
	}
	s, err := q.db().LabScore(q.ctx(), nid)
	if err != nil {
		return labScore{}, err
	}
	return scoreView(s), nil
}

// expComparison: each non-baseline variant against the baseline (the first variant) on the primary evaluator, through
// the one statistics function, over held_out examples when the suite has any (bands are tuned on tune), else over all.
// Only the latest attempt of a cell counts, with its latest primary score, and only one evaluator version (the newest):
// rows of different versions are never averaged. An unscored typed trial is left out.
func expComparison(e *labExperiment, trials []*labRun, cur []labScore) []labComparisonRow {
	primary, vs := e.primary(), e.Variants
	out := []labComparisonRow{}
	if primary == "" || len(vs) < 2 {
		return out
	}
	baseline := vs[0].Label
	scope := "all"
	if hasHeldOut(e) {
		scope = "held_out"
	}
	reason := ""
	if strings.HasSuffix(primary, ":pick_best") {
		reason = "pick_best ranks trials together; it gives no per-trial value to pair"
	}
	version := ""
	for _, sc := range cur {
		if sc.Evaluator == primary && len(sc.TrialIDs) == 1 {
			version = sc.EvaluatorVersion
		}
	}
	values, versions := map[string]map[string][]float64{}, map[string]bool{}
	for _, st := range scoredLatest(e, trials, cur, func(sc labScore) bool { return sc.Evaluator == primary }) {
		sc := st.score
		if scope == "held_out" && st.split != "held_out" || sc.typed().Answer == "unscored" {
			continue
		}
		versions[sc.EvaluatorVersion] = true
		if sc.EvaluatorVersion != version {
			continue
		}
		if values[st.variant] == nil {
			values[st.variant] = map[string][]float64{}
		}
		values[st.variant][st.example] = append(values[st.variant][st.example], scoreNumber(st.score))
	}
	if len(versions) > 1 && reason == "" {
		reason = "scores of older evaluator versions are left out"
	}
	if len(values[baseline]) == 0 && reason == "" {
		reason = "the baseline has no scored trial"
	}
	for _, v := range vs[1:] {
		out = append(out, labComparisonRow{Variant: v.Label, Baseline: baseline, Evaluator: primary, EvaluatorVersion: version, Split: scope, Reason: strPtr(reason),
			Result: compare(values[baseline], values[v.Label])})
	}
	return out
}

// labComparisonRow: one non-baseline variant against the baseline (expComparison).
type labComparisonRow struct {
	Variant          string     `json:"variant"`
	Baseline         string     `json:"baseline"`
	Evaluator        string     `json:"evaluator"`
	EvaluatorVersion string     `json:"evaluatorVersion"`
	Split            string     `json:"split"`
	Reason           *string    `json:"reason"`
	Result           comparison `json:"result"`
}

// bandsOf: a variant's bands {yes, no}: p >= yes answers yes, p <= no answers no, between abstains. Without bands every
// answer is confident at 0.5 (no middle band).
func bandsOf(v any) (float64, float64, error) {
	if v == nil {
		return 0.5, 0.5, nil
	}
	m, isObject := labRawObject(jsonBytes(v))
	yes, okY := numOK(rawValue(m["yes"]))
	no, okN := numOK(rawValue(m["no"]))
	if !isObject || len(m) != 2 || !okY || !okN || no < 0 || yes > 1 || no > yes {
		return 0, 0, fail(400, "bands are {yes, no} with 0 <= no <= yes <= 1")
	}
	return yes, no, nil
}

// scoreTyped: the typed evaluator for one trial: its output's p (the last step's output in the run record) through the
// variant's bands, against the example's label (references/expected.json {expected, tag?}). A trial it cannot score is
// recorded as {answer: unscored, reason} so the reason is visible and it is not tried again; it counts in no statistic.
func scoreTyped(q *Request, exp *labExperiment, t *labRun, pin *labPin, name string, examples map[string]labExample) error {
	key := ""
	if pin != nil {
		key = pin.ArtifactID
	}
	full, ok := examples[key]
	if !ok {
		var err error
		if full, err = exampleOf(q, pin); err != nil {
			return err
		}
		examples[key] = full
	}
	var label, tag, reason string
	for _, f := range full.References {
		if f.Path == "expected.json" && label == "" && reason == "" { // paths are unique; the first counts
			var e map[string]json.RawMessage // exact keys
			if err := json.Unmarshal([]byte(deref(f.Text)), &e); err != nil {
				reason = "references/expected.json is not a JSON object"
			}
			json.Unmarshal(e["expected"], &label)
			json.Unmarshal(e["tag"], &tag)
		}
	}
	var out struct {
		P json.RawMessage `json:"p"`
	}
	json.Unmarshal(t.output(), &out)
	pv := rawValue(out.P)
	p, isP := numOK(pv)
	switch {
	case reason != "":
	case label != "yes" && label != "no":
		reason = "the example has no yes/no label in references/expected.json"
	case !isP:
		reason = "the trial output has no number p"
	case p < 0 || p > 1:
		reason = fmt.Sprintf("the trial output p %v is outside 0..1", pv)
	}
	var value labTypedValue
	rationale := ""
	if reason != "" {
		value, rationale = labTypedValue{Answer: "unscored", Reason: reason, Label: strPtr(label), Tag: strPtr(tag)}, "unscored: "+reason
	} else {
		var bands any
		if v := exp.variant(t.cell().Variant); v != nil {
			bands = v.bands()
		}
		yes, no, err := bandsOf(bands)
		if err != nil {
			return err
		}
		answer := bandAnswer(p, yes, no)
		value = labTypedValue{Answer: answer, Label: &label, P: &p, Tag: strPtr(tag)}
		rationale = fmt.Sprintf("%s at p %.3g (bands %.3g/%.3g), label %s", answer, p, yes, no, label)
	}
	_, err := scoreInsert(q, labNewScore{ExperimentID: exp.ID, ProjectID: exp.ProjectID, TrialIDs: []string{t.ID}, Evaluator: name, EvaluatorVersion: "labels-v1", Form: "typed",
		Value: value, Rationale: &rationale})
	return err
}

// labTypedValue: a typed score's value: the banded answer against the example's label, or unscored with its reason.
type labTypedValue struct {
	Answer string   `json:"answer"`
	Label  *string  `json:"label"`
	P      *float64 `json:"p,omitempty"`
	Reason string   `json:"reason,omitempty"`
	Tag    *string  `json:"tag"`
}

// scoredTrial: the latest attempt of one cell with its latest score of one evaluator.
type scoredTrial struct {
	trial          *labRun
	score          labScore
	example, split string
	variant        string
}

// scoredLatest: the latest comparable attempt of every cell with its latest score that match accepts, and the split of
// its example. Comparisons and typed statistics read trials only through it.
func scoredLatest(e *labExperiment, trials []*labRun, cur []labScore, match func(labScore) bool) []scoredTrial {
	split := map[string]string{}
	for _, p := range e.Examples {
		split[p.ID] = p.Split
	}
	byTrial := map[string]labScore{}
	for _, sc := range cur { // in order: a later score of a trial replaces the earlier
		if match(sc) && len(sc.TrialIDs) == 1 {
			byTrial[sc.TrialIDs[0]] = sc
		}
	}
	out := []scoredTrial{}
	order, latest := expLatest(trials)
	for _, k := range order {
		t := latest[k]
		c := t.cell()
		if sc, ok := byTrial[t.ID]; ok && comparable(t) {
			out = append(out, scoredTrial{t, sc, c.Example, split[c.Example], c.Variant})
		}
	}
	return out
}

func hasHeldOut(e *labExperiment) bool {
	for _, p := range e.Examples {
		if p.Split == "held_out" {
			return true
		}
	}
	return false
}

// expTyped: per variant and split, the typed evaluator's coverage, selective accuracy and escalation share per example
// tag (the language). Bands are tuned on tune and reported on held_out, so both splits are shown, never mixed.
func expTyped(e *labExperiment, trials []*labRun, cur []labScore) []labTypedRow {
	out := []labTypedRow{}
	answers, perExample := map[[2]string][]typedAnswer{}, map[[3]string][]typedAnswer{}
	for _, st := range scoredLatest(e, trials, cur, func(sc labScore) bool { return sc.Form == "typed" }) {
		v := st.score.typed()
		if v.Answer == "unscored" {
			continue
		}
		perExample[[3]string{st.variant, st.split, st.example}] = append(perExample[[3]string{st.variant, st.split, st.example}], typedAnswer{v.Tag, v.Answer, v.Label})
	}
	// Identical repeats of one example count as one answer (a near-deterministic variant); differing ones count each.
	for k, as := range perExample {
		same := true
		for _, a := range as[1:] {
			same = same && a.Answer == as[0].Answer
		}
		if same {
			as = as[:1]
		}
		answers[[2]string{k[0], k[1]}] = append(answers[[2]string{k[0], k[1]}], as...)
	}
	for _, v := range e.Variants {
		for _, sp := range []string{"tune", "held_out"} {
			if as := answers[[2]string{v.Label, sp}]; len(as) > 0 {
				out = append(out, labTypedRow{Variant: v.Label, Split: sp, Stats: typedStats(as)})
			}
		}
	}
	return out
}

// labTypedRow: a variant's typed statistics on one split (expTyped).
type labTypedRow struct {
	Variant string      `json:"variant"`
	Split   string      `json:"split"`
	Stats   []typedStat `json:"stats"`
}

func labRatingWithdraw(q *Request, id, reason string) (labRatingView, error) {
	if err := labIDArg(id, "questionId"); err != nil {
		return labRatingView{}, err
	}
	if _, err := labStr(reason, "withdrawal reason", 4000); err != nil {
		return labRatingView{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return labRatingView{}, fail(400, "Withdrawal reason is empty")
	}

	state, err := q.db().LabRatingLockState(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return labRatingView{}, fail(404, "Unknown Lab rating")
	}
	if err != nil {
		return labRatingView{}, err
	}
	if state == "answered" {
		return labRatingView{}, fail(409, "Rating already answered")
	}
	if state == "open" {
		withdrawal := labWithdrawal{Reason: reason, Actor: actor(q, "operator"), At: nowMs()}
		if _, err := questionTransition(q, id, questionEvent{To: "withdrawn", Withdrawal: jsonBytes(withdrawal)}); err != nil {
			return labRatingView{}, err
		}
	}
	return labRating(q, id)
}

// labWithdrawal: why and by whom a rating was withdrawn.
type labWithdrawal struct {
	Reason string `json:"reason"`
	Actor  string `json:"actor"`
	At     int64  `json:"at"`
}
