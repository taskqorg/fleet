package main

import (
	"encoding/json"
	"testing"
)

// Requirement: a list of every project's roles (definition_list agents, library_view) reads each project's settings
// from its head as projectContext does, without its context files. Risk: the light path drifts from projectSettingsOf
// and an agent's roles change between the list and the project page.
func TestProjectHeadSettingsMatchContext(t *testing.T) {
	e := openTestCoordinator(t, false)
	err := e.app.store.Read(e.ctx, nil, func(q *Request) error {
		heads, err := headTexts(q, "project")
		if err != nil {
			return err
		}
		if len(heads) == 0 {
			t.Fatal("no seeded projects")
		}
		for _, h := range heads {
			light, _ := json.Marshal(projectHeadSettings(h.id, h.text))
			full, _ := json.Marshal(projectSettingsOf(q, h.id))
			if string(light) != string(full) {
				t.Fatalf("%s: settings differ:\n%s\n%s", h.id, light, full)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
