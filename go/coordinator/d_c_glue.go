package main

// Artifact functions for area C (pipelines, Lab), in the signatures of c_pipelines.go and c_lab.go. Wiring, in area C's
// file: cArtifactSave, cArtifactRead, cArtifactWriteObject = dArtifactSave, dArtifactRead, dArtifactWriteObject.
// They run in the caller's transaction when it has one (withTx), so a write route does not wait on its own GLOBAL lock.

import "context"

func projectOf(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	if p, ok := v.(*string); ok {
		return p
	}
	return nil
}

func dArtifactSave(q *Request, projectID any, scope, executionID, name string, b []byte, mediaType string) (string, error) {
	id, _, err := saveObject(withTx(q), q.app, projectOf(projectID), scope, executionID, name, b, mediaType)
	return id, err
}

func dArtifactRead(ctx context.Context, a *App, id string, max int64) ([]byte, error) {
	return artifactBytes(ctx, a, id, max)
}

func dArtifactWriteObject(q *Request, projectID any, scope string, b []byte, mediaType string) (string, string, error) {
	s, hash, err := writeObject(withTx(q), q.app, projectOf(projectID), scope, b, mediaType)
	if err != nil {
		return "", "", err
	}
	return hash, s.ID, nil
}
