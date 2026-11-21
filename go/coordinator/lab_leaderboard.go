// Lab leaderboard aggregation (R2/R7): aggregates confirmed trial results, scores, wall durations,
// tokens, and list-price/measured costs across experiments by use case, pipeline, variant, model,
// profile, configRef, and evaluator.
package main

import (
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

const (
	labCollectionBenchmarks = "LLM Code Benchmarks"
	labCollectionRealWork   = "Real Work"
	labCollectionFleet      = "Fleet & Projects"
	labCollectionArchived   = "Archived"
	labLeaderboardDaysDef   = 90
)

var labCollectionOrder = []string{
	labCollectionBenchmarks,
	labCollectionRealWork,
	labCollectionFleet,
	labCollectionArchived,
}

func useCaseCollection(useCaseID string, byID map[string]contract.UseCase) string {
	u, known := byID[useCaseID]
	if strings.HasPrefix(useCaseID, "bench:") || (known && slices.Contains(u.Tags, "llm-code-benchmarks")) {
		return labCollectionBenchmarks
	}
	if strings.HasPrefix(useCaseID, "real:") || (known && slices.Contains(u.Tags, "real-work")) {
		return labCollectionRealWork
	}
	if known && u.Status != nil && (*u.Status == contract.UseCaseStatusObsolete || *u.Status == contract.UseCaseStatusDuplicate) {
		return labCollectionArchived
	}
	return labCollectionFleet
}

func normalizeLabCollectionFilter(raw string) string {
	s := strings.TrimSpace(raw)
	switch strings.ToLower(s) {
	case "benchmarks", "llm-code-benchmarks", "llm code benchmarks":
		return labCollectionBenchmarks
	case "real-work", "real work":
		return labCollectionRealWork
	case "fleet-projects", "fleet & projects", "fleet and projects":
		return labCollectionFleet
	case "archived", "archived / obsolete":
		return labCollectionArchived
	default:
		return s
	}
}

func orderedCollections(present map[string]bool) []string {
	out := make([]string, 0, len(labCollectionOrder))
	for _, c := range labCollectionOrder {
		if present[c] {
			out = append(out, c)
		}
	}
	return out
}

type leaderboardGroupKey struct {
	useCaseID    string
	pipelineID   string
	variantLabel string
	model        string
	profile      string
	configRef    string
	evaluator    string
}

type leaderboardAcc struct {
	key                leaderboardGroupKey
	collection         string
	hasModel           bool
	hasProfile         bool
	hasConfigRef       bool
	scoreForm          string
	scores             []float64
	trials             int64
	succeededTrials    int64
	failedTrials       int64
	durationsMs        []float64
	tokensTotal        int64
	costUsd            float64
	costPriced         int
	costUnpriced       int
	costBases          []contract.ResourceBasis
	latestExperimentID string
	latestAt           time.Time
}

var labExampleUseCaseCache sync.Map // artifactID -> use_case_id (approved example artifacts are immutable by ID/hash)

func labTrialUseCaseID(q *Request, tr db.LabLeaderboardTrialsRow, exp *labExperiment, exCache map[string]string) string {
	if tr.ExampleUseCaseID != "" {
		return tr.ExampleUseCaseID
	}
	if exp == nil || tr.CellExample == "" {
		return ""
	}
	pin := exp.pin(tr.CellExample)
	if pin == nil || pin.ArtifactID == "" {
		return ""
	}
	if uc, ok := exCache[pin.ArtifactID]; ok {
		return uc
	}
	if v, ok := labExampleUseCaseCache.Load(pin.ArtifactID); ok {
		uc, _ := v.(string)
		exCache[pin.ArtifactID] = uc
		return uc
	}
	stored, err := labExampleRead(q, pin.ArtifactID)
	if err != nil {
		exCache[pin.ArtifactID] = ""
		return ""
	}
	labExampleUseCaseCache.Store(pin.ArtifactID, stored.example.UseCaseID)
	exCache[pin.ArtifactID] = stored.example.UseCaseID
	return stored.example.UseCaseID
}

func labTrialConfigRef(raw []byte, variant *labVariant) string {
	if len(raw) > 0 {
		var cfg struct {
			ConfigRef string `json:"config_ref"`
		}
		if json.Unmarshal(raw, &cfg) == nil && cfg.ConfigRef != "" {
			return cfg.ConfigRef
		}
	}
	if variant != nil {
		return variant.ConfigRef
	}
	return ""
}

func labVariantOverridesStrings(variant *labVariant) (model string, profile string) {
	if variant == nil || variant.Overrides == nil {
		return "", ""
	}
	var o struct {
		Model   string `json:"model"`
		Profile string `json:"profile"`
	}
	if json.Unmarshal(jsonBytes(variant.Overrides), &o) == nil {
		return o.Model, o.Profile
	}
	return "", ""
}

func labTrialModelAndProfile(leaves []db.LabLeafMetaRow, variant *labVariant) (model string, profile string) {
	collect := func(filterKind string) ([]string, []string) {
		var models, profiles []string
		seenM, seenP := map[string]bool{}, map[string]bool{}
		for _, l := range leaves {
			if filterKind != "" && l.Kind != filterKind {
				continue
			}
			if l.Model != "" && !seenM[l.Model] {
				seenM[l.Model] = true
				models = append(models, l.Model)
			}
			if l.ProfileID != "" && !seenP[l.ProfileID] {
				seenP[l.ProfileID] = true
				profiles = append(profiles, l.ProfileID)
			}
		}
		return models, profiles
	}
	models, profiles := collect("agent_run")
	if len(models) == 0 || len(profiles) == 0 {
		cm, cp := collect("child_agent")
		if len(models) == 0 {
			models = cm
		}
		if len(profiles) == 0 {
			profiles = cp
		}
	}
	if len(models) == 0 || len(profiles) == 0 {
		am, ap := collect("")
		if len(models) == 0 {
			models = am
		}
		if len(profiles) == 0 {
			profiles = ap
		}
	}
	ovModel, ovProfile := labVariantOverridesStrings(variant)
	if len(models) > 0 {
		model = strings.Join(models, ", ")
	} else {
		model = ovModel
	}
	if len(profiles) > 0 {
		profile = strings.Join(profiles, ", ")
	} else {
		profile = ovProfile
	}
	return model, profile
}

func labTrialDurationMs(tr db.LabLeaderboardTrialsRow) (float64, bool) {
	if tr.DataDurationMs >= 0 && !math.IsNaN(tr.DataDurationMs) && !math.IsInf(tr.DataDurationMs, 0) {
		return tr.DataDurationMs, true
	}
	if tr.StartedAt != nil && tr.EndedAt != nil {
		d := float64(tr.EndedAt.Sub(*tr.StartedAt).Milliseconds())
		if d >= 0 {
			return d, true
		}
	}
	return 0, false
}

func medianInt64(vals []float64) *int64 {
	if len(vals) == 0 {
		return nil
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	n := len(sorted)
	var m float64
	if n%2 == 1 {
		m = sorted[n/2]
	} else {
		m = (sorted[n/2-1] + sorted[n/2]) / 2
	}
	v := int64(math.Round(m))
	return &v
}

func int64OrZero(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

type trialSpendAcc struct {
	tokensTotal  int64
	costUsd      float64
	costPriced   int
	costUnpriced int
	costBases    []contract.ResourceBasis
}

func addTrialCostFigures(ts *trialSpendAcc, figs []contract.CostFigure) bool {
	sawUnpriced := false
	for _, cf := range figs {
		if cf.Basis == contract.ResourceBasisUnavailable {
			ts.costUnpriced++
			sawUnpriced = true
			continue
		}
		if cf.Value != nil && deref(cf.Currency) == "USD" {
			ts.costUsd += *cf.Value
			ts.costPriced++
			ts.costBases = append(ts.costBases, cf.Basis)
		}
	}
	return sawUnpriced
}

func labLeaderboard(q *Request, r *contract.LabLeaderboardRequest) (contract.LabLeaderboardReply, error) {
	out := contract.LabLeaderboardReply{
		Entries:     []contract.LabLeaderboardEntry{},
		Collections: []string{},
	}

	days := int32(labLeaderboardDaysDef)
	if r.Days != nil {
		if *r.Days < 1 || *r.Days > math.MaxInt32 {
			return out, fail(400, "days must be between 1 and 2147483647")
		}
		days = int32(*r.Days)
	}

	var projectID *string
	if r.ProjectId != nil && strings.TrimSpace(*r.ProjectId) != "" {
		p := strings.TrimSpace(*r.ProjectId)
		projectID = &p
	}
	useCaseFilter := ""
	if r.UseCaseId != nil && strings.TrimSpace(*r.UseCaseId) != "" {
		useCaseFilter = strings.TrimSpace(*r.UseCaseId)
	}
	collectionFilter := ""
	if r.Collection != nil && strings.TrimSpace(*r.Collection) != "" {
		collectionFilter = normalizeLabCollectionFilter(*r.Collection)
	}

	ucs, err := useCases()
	if err != nil {
		return out, err
	}
	ucByID := make(map[string]contract.UseCase, len(ucs))
	presentCollections := map[string]bool{}
	for _, u := range ucs {
		ucByID[u.Id] = u
		col := useCaseCollection(u.Id, ucByID)
		if col != labCollectionArchived {
			presentCollections[col] = true
		}
	}
	out.Collections = orderedCollections(presentCollections)

	trials, err := q.db().LabLeaderboardTrials(q.ctx(), db.LabLeaderboardTrialsParams{
		ProjectID: projectID,
		Days:      days,
	})
	if err != nil {
		return out, err
	}
	if len(trials) == 0 {
		return out, nil
	}

	expIDSet := map[string]bool{}
	expIDs := make([]string, 0)
	for _, tr := range trials {
		if tr.ExperimentID != nil && !expIDSet[*tr.ExperimentID] {
			expIDSet[*tr.ExperimentID] = true
			expIDs = append(expIDs, *tr.ExperimentID)
		}
	}

	expRows, err := q.db().LabExperimentsByIDs(q.ctx(), expIDs)
	if err != nil {
		return out, err
	}
	expsByID := make(map[string]*labExperiment, len(expRows))
	for _, er := range expRows {
		exp, err := expOf(er)
		if err != nil {
			return out, err
		}
		expsByID[exp.ID] = exp
	}

	// Resolve each trial's use_case_id + collection and apply filters before fetching leaves.
	type resolvedTrial struct {
		row        db.LabLeaderboardTrialsRow
		exp        *labExperiment
		useCaseID  string
		collection string
	}
	exCache := map[string]string{}
	filtered := make([]resolvedTrial, 0, len(trials))
	filteredTrialIDs := make([]string, 0, len(trials))
	filteredExpSet := map[string]bool{}
	filteredExpIDs := make([]string, 0, len(expIDs))
	for _, tr := range trials {
		if tr.ExperimentID == nil {
			continue
		}
		exp := expsByID[*tr.ExperimentID]
		if exp == nil {
			continue
		}
		ucID := labTrialUseCaseID(q, tr, exp, exCache)
		if ucID == "" {
			continue
		}
		if useCaseFilter != "" && ucID != useCaseFilter {
			continue
		}
		col := useCaseCollection(ucID, ucByID)
		if collectionFilter != "" && col != collectionFilter {
			continue
		}
		filtered = append(filtered, resolvedTrial{row: tr, exp: exp, useCaseID: ucID, collection: col})
		filteredTrialIDs = append(filteredTrialIDs, tr.ID)
		if !filteredExpSet[exp.ID] {
			filteredExpSet[exp.ID] = true
			filteredExpIDs = append(filteredExpIDs, exp.ID)
		}
	}
	if len(filtered) == 0 {
		return out, nil
	}

	// Fetch scores for filtered experiments.
	scoreRows, err := q.db().LabScoresByExperiments(q.ctx(), filteredExpIDs)
	if err != nil {
		return out, err
	}
	scoresByTrial := map[string][]labScore{}
	for _, sr := range scoreRows {
		sc := scoreView(sr)
		if sc.unscored() {
			continue
		}
		for _, tid := range sc.TrialIDs {
			scoresByTrial[tid] = append(scoresByTrial[tid], sc)
		}
	}

	// Fetch lineage, precomputed leaf spend/tokens, and resolved model/profile per trial.
	lineage, err := q.db().LabTrialsLineage(q.ctx(), filteredTrialIDs)
	if err != nil {
		return out, err
	}
	spendByTrial := map[string]*trialSpendAcc{}
	ensureTrialSpend := func(tid string) *trialSpendAcc {
		ts := spendByTrial[tid]
		if ts == nil {
			ts = &trialSpendAcc{}
			spendByTrial[tid] = ts
		}
		return ts
	}
	leavesByTrial := map[string][]db.LabLeafMetaRow{}
	if len(lineage.LeafIds) > 0 {
		uniqLeafSet := map[string]bool{}
		uniqLeafIDs := make([]string, 0, len(lineage.LeafIds))
		for _, lid := range lineage.LeafIds {
			if !uniqLeafSet[lid] {
				uniqLeafSet[lid] = true
				uniqLeafIDs = append(uniqLeafIDs, lid)
			}
		}
		spendRows, err := q.db().LabLeafSpend(q.ctx(), uniqLeafIDs)
		if err != nil {
			return out, err
		}
		spendByID := make(map[string]db.LabLeafSpendRow, len(spendRows))
		for _, s := range spendRows {
			spendByID[s.ExecutionID] = s
		}
		metaRows, err := q.db().LabLeafMeta(q.ctx(), uniqLeafIDs)
		if err != nil {
			return out, err
		}
		metaByID := make(map[string]db.LabLeafMetaRow, len(metaRows))
		for _, m := range metaRows {
			metaByID[m.ID] = m
		}
		for i, lid := range lineage.LeafIds {
			if i >= len(lineage.ScopeKeys) || i >= len(lineage.Scopes) {
				continue
			}
			trialID := lineage.ScopeKeys[i]
			if m, ok := metaByID[lid]; ok {
				leavesByTrial[trialID] = append(leavesByTrial[trialID], m)
			}
			sp, ok := spendByID[lid]
			if !ok {
				continue
			}
			ts := ensureTrialSpend(trialID)
			if sp.Kind == "agent_run" || sp.Kind == "child_agent" || sp.TokensInput != nil || sp.TokensOutput != nil {
				ts.tokensTotal += int64OrZero(sp.TokensInput) + int64OrZero(sp.TokensOutput) + int64OrZero(sp.TokensCacheRead) + int64OrZero(sp.TokensCacheWrite)
			}
			if sp.HasSpend {
				if len(sp.Payload) > 0 {
					figs, err := decodeCostFigures(sp.Payload)
					if err != nil {
						return out, err
					}
					sawUnpriced := addTrialCostFigures(ts, figs)
					if sp.CostUnpriced > 0 && !sawUnpriced {
						ts.costUnpriced += int(sp.CostUnpriced)
					}
				} else {
					if sp.CostUnpriced > 0 {
						ts.costUnpriced += int(sp.CostUnpriced)
					}
					if sp.CostCurrency != nil && *sp.CostCurrency == "USD" && sp.CostBasis != nil && sp.CostValue != nil {
						ts.costUsd += *sp.CostValue
						ts.costPriced++
						ts.costBases = append(ts.costBases, contract.ResourceBasis(*sp.CostBasis))
					}
				}
			} else {
				ts.costUnpriced++
			}
		}
	}

	groups := map[leaderboardGroupKey]*leaderboardAcc{}
	for _, ft := range filtered {
		tr := ft.row
		exp := ft.exp
		variantLabel := deref(tr.Variant)
		variant := exp.variant(variantLabel)
		pipelineID := firstString(deref(tr.PipelineID), variantPipelineID(exp, variant), exp.PipelineID)
		configRef := labTrialConfigRef(tr.ConfigRef, variant)
		model, profile := labTrialModelAndProfile(leavesByTrial[tr.ID], variant)

		ts := spendByTrial[tr.ID]
		var trialTokens int64
		if ts != nil {
			trialTokens = ts.tokensTotal
		}
		trialDur, hasDur := labTrialDurationMs(tr)

		// Collect evaluator -> (scoreForm, []scoreValue) for this trial.
		type evalRec struct {
			form   string
			values []float64
		}
		evals := map[string]*evalRec{}
		evalOrder := []string{}
		ensureEval := func(name, form string) *evalRec {
			er := evals[name]
			if er == nil {
				er = &evalRec{form: form}
				evals[name] = er
				evalOrder = append(evalOrder, name)
			}
			return er
		}
		for _, ev := range exp.Evaluators {
			ensureEval(ev.name(), ev.Form)
		}
		for _, sc := range scoresByTrial[tr.ID] {
			er := ensureEval(sc.Evaluator, sc.Form)
			if sc.Form == "pick_best" {
				if sc.text() == tr.ID {
					er.values = append(er.values, 1)
				} else {
					er.values = append(er.values, 0)
				}
			} else {
				er.values = append(er.values, scoreNumber(sc))
			}
		}
		if len(evalOrder) == 0 {
			ensureEval("none", "none")
		}

		for _, evName := range evalOrder {
			er := evals[evName]
			k := leaderboardGroupKey{
				useCaseID:    ft.useCaseID,
				pipelineID:   pipelineID,
				variantLabel: variantLabel,
				model:        model,
				profile:      profile,
				configRef:    configRef,
				evaluator:    evName,
			}
			acc := groups[k]
			if acc == nil {
				acc = &leaderboardAcc{
					key:          k,
					collection:   ft.collection,
					hasModel:     model != "",
					hasProfile:   profile != "",
					hasConfigRef: configRef != "",
					scoreForm:    er.form,
				}
				groups[k] = acc
			}
			acc.trials++
			if tr.State == "succeeded" {
				acc.succeededTrials++
			} else if tr.State == "failed" {
				acc.failedTrials++
			}
			acc.scores = append(acc.scores, er.values...)
			if hasDur {
				acc.durationsMs = append(acc.durationsMs, trialDur)
			}
			acc.tokensTotal += trialTokens
			if ts != nil {
				acc.costUsd += ts.costUsd
				acc.costPriced += ts.costPriced
				acc.costUnpriced += ts.costUnpriced
				acc.costBases = append(acc.costBases, ts.costBases...)
			}
			if acc.latestExperimentID == "" || tr.TrialAt.Time.After(acc.latestAt) {
				acc.latestExperimentID = exp.ID
				acc.latestAt = tr.TrialAt.Time
			}
			presentCollections[ft.collection] = true
		}
	}

	out.Collections = orderedCollections(presentCollections)
	out.Entries = make([]contract.LabLeaderboardEntry, 0, len(groups))
	for _, acc := range groups {
		var modelPtr, profilePtr, configRefPtr *string
		if acc.hasModel {
			v := acc.key.model
			modelPtr = &v
		}
		if acc.hasProfile {
			v := acc.key.profile
			profilePtr = &v
		}
		if acc.hasConfigRef {
			v := acc.key.configRef
			configRefPtr = &v
		}
		var meanPtr, minPtr, maxPtr *float64
		if len(acc.scores) > 0 {
			sum := 0.0
			lo, hi := math.Inf(1), math.Inf(-1)
			for _, s := range acc.scores {
				sum += s
				lo = math.Min(lo, s)
				hi = math.Max(hi, s)
			}
			mean := sum / float64(len(acc.scores))
			meanPtr, minPtr, maxPtr = &mean, &lo, &hi
		}
		var costPtr *float64
		costBasis := string(contract.ResourceBasisUnavailable)
		if acc.costPriced > 0 && acc.costUnpriced == 0 {
			v := acc.costUsd
			costPtr = &v
			costBasis = string(weakestBasis(acc.costBases...))
		}
		out.Entries = append(out.Entries, contract.LabLeaderboardEntry{
			UseCaseId:          acc.key.useCaseID,
			Collection:         acc.collection,
			PipelineId:         acc.key.pipelineID,
			VariantLabel:       acc.key.variantLabel,
			Model:              modelPtr,
			Profile:            profilePtr,
			ConfigRef:          configRefPtr,
			Evaluator:          acc.key.evaluator,
			ScoreForm:          acc.scoreForm,
			ScoreMean:          meanPtr,
			ScoreMin:           minPtr,
			ScoreMax:           maxPtr,
			Trials:             acc.trials,
			SucceededTrials:    acc.succeededTrials,
			FailedTrials:       acc.failedTrials,
			WallMsMedian:       medianInt64(acc.durationsMs),
			TokensTotal:        acc.tokensTotal,
			CostUsd:            costPtr,
			CostBasis:          costBasis,
			LatestExperimentId: acc.latestExperimentID,
			LatestAt:           acc.latestAt.UTC().Format(time.RFC3339Nano),
		})
	}

	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.UseCaseId != b.UseCaseId {
			return a.UseCaseId < b.UseCaseId
		}
		if a.Evaluator != b.Evaluator {
			return a.Evaluator < b.Evaluator
		}
		if (a.ScoreMean != nil) != (b.ScoreMean != nil) {
			return a.ScoreMean != nil
		}
		if a.ScoreMean != nil && b.ScoreMean != nil && *a.ScoreMean != *b.ScoreMean {
			return *a.ScoreMean > *b.ScoreMean
		}
		if a.Trials != b.Trials {
			return a.Trials > b.Trials
		}
		if a.LatestAt != b.LatestAt {
			return a.LatestAt > b.LatestAt
		}
		if a.PipelineId != b.PipelineId {
			return a.PipelineId < b.PipelineId
		}
		if a.VariantLabel != b.VariantLabel {
			return a.VariantLabel < b.VariantLabel
		}
		return deref(a.ConfigRef) < deref(b.ConfigRef)
	})

	return out, nil
}
