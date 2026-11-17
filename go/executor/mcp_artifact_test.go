package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// answerTransfers plays the transfer lane: every queued transfer gets reply until the test ends.
func answerTransfers(t *testing.T, db *sql.DB, reply string) {
	stop, done := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(stop); <-done }) // before the journal closes: cleanups run last first
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				db.Exec("UPDATE transfers SET state='done',response=? WHERE response IS NULL", reply)
			}
		}
	}()
}

func artifactJournal(t *testing.T) *sql.DB {
	db, err := openJournal(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	answerTransfers(t, db, `{"artifactId":"a","state":"done"}`)
	return db
}

func refused(r any) bool { return jAt(r, "isError") == true }

// Risk: an agent that names a file of its working area by its absolute path is refused (Lab trial 2026-11-17:
// /Users/Shared/fleet/.../worktree/output/report.md), or one reached through a linked spelling of the root is; while a
// path outside the area, or through a link inside it, must stay refused.
func TestArtifactSaveAbsolutePath(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir()) // macOS: /var is a link to /private/var
	noErr(t, err)
	real := filepath.Join(base, "real")
	work := filepath.Join(real, "work")
	noErr(t, os.MkdirAll(filepath.Join(work, "output"), 0o755))
	noErr(t, os.WriteFile(filepath.Join(work, "output", "report.md"), []byte("report"), 0o644))
	noErr(t, os.WriteFile(filepath.Join(base, "secret.txt"), []byte("secret"), 0o644))
	noErr(t, os.Symlink(base, filepath.Join(work, "out")))   // a link inside the area that points outside it
	noErr(t, os.Symlink(real, filepath.Join(base, "alias"))) // the working area's root reached through a link
	linkedWork := filepath.Join(base, "alias", "work")
	db := artifactJournal(t)
	for _, c := range []struct {
		workdir, path string
		ok            bool
	}{
		{work, "output/report.md", true},
		{work, filepath.Join(work, "output", "report.md"), true},
		{work, work + "/./output//report.md", true},
		{work, "/output/report.md", false},                                               // not the working area's root
		{work + "/../work", filepath.Join(work, "output", "report.md"), true},            // a configured root that is not clean
		{linkedWork, filepath.Join(linkedWork, "output", "report.md"), true},             // as configured
		{linkedWork, filepath.Join(work, "output", "report.md"), true},                   // resolved
		{work, filepath.Join(linkedWork, "output", "report.md"), true},                   // configured resolved, named through the link
		{work, filepath.Join(work, "out", "real", "work", "output", "report.md"), false}, // through a link below the root
		{work, filepath.Join(base, "secret.txt"), false},
		{work, filepath.Join(work, "out", "secret.txt"), false},
		{work, work + "/../secret.txt", false},
		{work, work + "/output/../output/report.md", false},
		{work, work, false},
		{work, work + "x/output/report.md", false},
	} {
		ctx := M{"hostId": "h", "executionId": "e", "workdir": c.workdir, "spool": filepath.Join(base, "spool")}
		r, err := artifactCall(ctx, "artifact_save", M{"path": c.path}, dbExec{db})
		if err != nil {
			t.Fatalf("%s in %s: %v", c.path, c.workdir, err)
		}
		if refused(r) == c.ok {
			t.Fatalf("%s in %s: want ok=%v, got %v", c.path, c.workdir, c.ok, r)
		}
	}
	var name string
	noErr(t, db.QueryRow("SELECT json_extract(body,'$.name') FROM transfers WHERE kind='upload' ORDER BY rowid DESC LIMIT 1").Scan(&name))
	if name != "output/report.md" {
		t.Fatalf("an absolute path saves under its name in the working area, got %q", name)
	}
}

// Risk: artifact_list without arguments returns every artifact of the project in one reply (Lab trial 2026-11-17:
// ~220k characters, over the harness's tool-output limit); the project needs scope "project" and pages by cursor.
func TestArtifactListDefaultsToOwnExecution(t *testing.T) {
	db := artifactJournal(t)
	ctx := M{"hostId": "h", "executionId": "own", "workdir": t.TempDir()}
	for _, c := range []struct {
		args   M
		filter string
	}{
		{M{}, `{"cursor":null,"executionId":"own","mediaType":null,"taskId":null}`},
		{M{"scope": "execution"}, `{"cursor":null,"executionId":"own","mediaType":null,"taskId":null}`},
		{M{"execution_id": "", "task_id": true}, `{"cursor":null,"executionId":"own","mediaType":null,"taskId":true}`}, // empty or not text is no filter
		{M{"scope": "project", "cursor": "17"}, `{"cursor":"17","executionId":null,"mediaType":null,"taskId":null}`},
		{M{"task_id": "t"}, `{"cursor":null,"executionId":null,"mediaType":null,"taskId":"t"}`},
		{M{"execution_id": "other", "media_type": "text/plain"}, `{"cursor":null,"executionId":"other","mediaType":"text/plain","taskId":null}`},
	} {
		if r, err := artifactCall(ctx, "artifact_list", c.args, dbExec{db}); err != nil || refused(r) {
			t.Fatalf("%v: %v %v", c.args, r, err)
		}
		var body string
		noErr(t, db.QueryRow("SELECT body FROM transfers WHERE kind='list' ORDER BY rowid DESC LIMIT 1").Scan(&body))
		b, _ := dec(body)
		if got := enc(jAt(b, "filter")); got != c.filter {
			t.Fatalf("%v: filter %s, want %s", c.args, got, c.filter)
		}
	}
	for _, args := range []M{{"scope": "everything"}, {"scope": "project", "execution_id": "x"}, {"scope": "execution", "task_id": "t"}} {
		if r, _ := artifactCall(ctx, "artifact_list", args, dbExec{db}); !refused(r) || !strings.Contains(enc(r), "scope") {
			t.Fatalf("%v: an unknown scope, or one beside a filter, is refused: %v", args, r)
		}
	}
	var schema any
	for _, tool := range mcpTools(ctx)["tools"].([]any) {
		if jAt(tool, "name") == "artifact_list" {
			schema = tool
		}
	}
	if d := jStr(jAt(schema, "description")); !strings.Contains(d, "this execution") || !strings.Contains(d, `scope="project"`) || !strings.Contains(d, "nextCursor") {
		t.Fatalf("the description names the default, the project scope and paging: %q", d)
	}
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
