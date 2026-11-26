package main

import (
	"testing"
	"time"
)

// Requirement: a bare id in agent text (a run's UUID) resolves to its card kind (entity-card.md, OR-38), so it renders
// as a reference instead of the UUID; an id no table holds is left out.
// Risk: an agent's UUID shows as text, or resolves to the wrong kind and opens the wrong card.
func TestEntityKindsOf(t *testing.T) {
	e := openTestCoordinator(t, false)
	at := time.Now().UTC()
	if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO execution (id, kind, state, project_id, started_at, data, observation, config_ref)
	 VALUES ('ek-run', 'tool_run'::execution_kind, 'succeeded'::execution_state, 'fleet', $1, '{}', 'confirmed', '{}')`, at); err != nil {
		t.Fatal(err)
	}
	err := e.app.store.Read(e.ctx, nil, func(q *Request) error {
		got, err := kindsOf(q, []string{"ek-run", "ek-none"})
		if err != nil {
			return err
		}
		if len(got) != 1 || got["ek-run"] != "execution" {
			t.Fatalf("kinds: %v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Requirement: a conversation title never shows a raw URL (entity-card.md, OR-38): a pull request link becomes its
// reference, an issue link its repository and number, any other link its host and last path part.
func TestPlainTitle(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/taskqorg/fleet/pull/1207":                        "taskqorg/fleet#1207",
		"Live check: https://github.com/taskqorg/fleet/pull/1207/files — ok": "Live check: taskqorg/fleet#1207 — ok",
		"see https://github.com/a/b/issues/9.":                               "see a/b issue #9",
		"https://www.example.com/docs/guide?x=1#top":                         "example.com/guide",
		"http://host.test/": "host.test",
		"no link here":      "no link here",
	} {
		if got := plainTitle(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
