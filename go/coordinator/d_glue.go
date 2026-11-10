package main

// Area A's artifact hooks (a_hooks.go): the artifact records of an execution (stored originals, in the record shape the old
// readers used) and its bounded bytes.

import "context"

func readArtifact(a *App, id string) ([]byte, error) {
	return artifactBytes(context.Background(), a, id, 128000)
}

func executionArtifacts(q *Request, executionID string) ([]doc, error) {
	rows, err := artifacts(withTx(q), q.app, "WHERE execution_id=$1 AND state='stored' AND role='original' ORDER BY seq", executionID)
	if err != nil {
		return nil, err
	}
	out := []doc{}
	for _, x := range rows {
		id := x.ID
		if x.LegacyID != nil {
			id = *x.LegacyID
		}
		out = append(out, doc{"id": id, "artifactId": x.ID, "executionId": x.ExecutionID, "path": x.Name, "mediaType": x.MediaType, "size": x.Size, "sha256": x.ContentHash})
	}
	return out, nil
}
