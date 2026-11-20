package main

import "fleet/coordinator/db"

// labNewScore: one score of an experiment's trials, before it is recorded.
type labNewScore struct {
	ExperimentID, ProjectID           string
	TrialIDs                          []string
	Evaluator, EvaluatorVersion, Form string
	Value                             any
	SourceExecutionID, QuestionID     *string
	Rationale, Replaces               *string
}

// scoreInsert records s once its value fits its form; a rationale keeps its first 4000 bytes.
func scoreInsert(q *Request, s labNewScore) (string, error) {
	trials := []any{}
	ids := []string{}
	for _, t := range s.TrialIDs {
		trials = append(trials, t)
		ids = append(ids, t)
	}
	if err := checkValue(s.Form, s.Value, trials); err != nil {
		return "", err
	}
	rationale := s.Rationale
	if rationale != nil && len(*rationale) > 4000 {
		r := (*rationale)[:4000]
		rationale = &r
	}
	id := newID()
	return id, q.db().LabScoreInsert(q.ctx(), db.LabScoreInsertParams{ID: id, ExperimentID: s.ExperimentID, ProjectID: s.ProjectID, TrialIds: ids, Evaluator: s.Evaluator,
		EvaluatorVersion: s.EvaluatorVersion, Form: s.Form, Value: jsonBytes(s.Value), SourceExecutionID: s.SourceExecutionID, QuestionID: s.QuestionID, Rationale: rationale,
		ReplacesScoreID: s.Replaces})
}
