package main

import "testing"

// Requirement: a purge of project-less content ("-") takes only the project-less copies; the same content in a project
// stays stored and is not marked purged there. Risk: projectId null meant every project, and a cleanup of project-less
// pipeline files deleted the same page images in project dam (2026-11-26).
func TestArtifactPurgeNoProjectKeepsProjectCopies(t *testing.T) {
	e := openTestCoordinator(t, false)
	hash := "ab" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	for id, project := range map[string]any{"purge-orphan": nil, "purge-kept": "hello-world"} {
		if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO artifact (id, project_id, scope, content_hash, size, media_type, name, state)
			VALUES ($1, $2, 'prod', $3, 1, 'image/jpeg', 'pages/a.jpg', 'stored')`, id, project, hash); err != nil {
			t.Fatal(err)
		}
	}
	none := "-"
	reply, err := artifactPurge(e.ctx, e.app, hash, &none, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Artifacts != 1 {
		t.Fatalf("purged %d artifacts, want 1 (the project-less one)", reply.Artifacts)
	}
	state := func(id string) string {
		var s string
		if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT state FROM artifact WHERE id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if state("purge-orphan") != "deleted" || state("purge-kept") != "stored" {
		t.Fatalf("orphan %s, project copy %s; want deleted and stored", state("purge-orphan"), state("purge-kept"))
	}
	var marked bool
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT EXISTS (SELECT 1 FROM purged_hash WHERE project_id='hello-world' AND content_hash=$1)`, hash).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if marked {
		t.Fatal("the project's content was marked purged")
	}
}
