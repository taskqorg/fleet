package main

import (
	"path/filepath"
	"testing"

	"fleet/contract"
)

// Requirement: fleet-definition-editors.md "Content is stored canonical" and "every shipped definition file is canonical".
// Risk: the first export reformats whole files, or a contentSha changes without a content change.
func TestShippedDefinitionsAreCanonical(t *testing.T) {
	root := filepath.Join("..", "..")
	t.Setenv("FLEET_PROFILES_DIRECTORY", filepath.Join(root, "config", "agents"))
	t.Setenv("FLEET_PROJECT_CONFIG_DIRECTORY", filepath.Join(root, "config"))
	files, broken := shippedDefinitions()
	for k, e := range broken {
		if e != nil {
			t.Errorf("%s: %v", k, e)
		}
	}
	if len(files) == 0 {
		t.Fatalf("broken %v, files %d", broken, len(files))
	}
	for _, f := range files {
		text, err := canonicalText(f.raw)
		if err != nil || text != string(f.raw) {
			t.Errorf("%s is not canonical (rewrite it with keys sorted, two-space indent, trailing newline)", f.path)
		}
	}
	if gitBlobSha([]byte("hello\n")) != "ce013625030ba8dba906f756967f9e9ca394464a" { // git hash-object
		t.Fatal("git blob sha differs from git hash-object")
	}
}

// Requirement: the state decision list (Reconcile at start, State). Risk: a definition edited in the store shows as synced,
// or a release that changed a file shows as ahead.
func TestDefinitionState(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		shipped              bool
		repo, repoCSha, base *string
		head                 string
		want                 contract.DefinitionState
	}{
		{false, nil, nil, nil, "h", contract.DefinitionStateStoreOnly},
		{true, nil, nil, s("b1"), "h", contract.DefinitionStateRepositoryDeleted},
		{true, s("b1"), s("h"), s("b0"), "h", contract.DefinitionStateSynced},
		{true, s("b2"), s("c2"), s("b1"), "h", contract.DefinitionStateRepositoryChanged},
		{true, s("b1"), s("c1"), s("b1"), "h", contract.DefinitionStateAhead},
	}
	for _, c := range cases {
		if got := definitionState(c.shipped, c.repo, c.repoCSha, c.head, c.base); got != c.want {
			t.Errorf("%+v: got %s, want %s", c, got, c.want)
		}
	}
}
