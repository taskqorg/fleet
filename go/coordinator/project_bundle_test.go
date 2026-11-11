package main

import "testing"

func TestProjectBundleOwnership(t *testing.T) {
	makeBundle := func(name string) doc {
		return doc{"settings": doc{"id": "trial", "name": "Trial", "contextFiles": []any{name}}, "contextFiles": []any{doc{"path": name, "text": "Project rules"}}}
	}
	if _, err := projectBundle("trial", makeBundle("rules.md")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../private", "/private", "a/../private", "a\\private", "."} {
		if _, err := projectBundle("trial", makeBundle(name)); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	b := makeBundle("rules.md")
	b["createdBy"] = "someone"
	if _, err := projectBundle("trial", b); err == nil {
		t.Fatal("accepted authority metadata")
	}
	b = makeBundle("rules.md")
	b["contextFiles"] = []any{}
	if _, err := projectBundle("trial", b); err == nil {
		t.Fatal("accepted missing context")
	}
	b = makeBundle("rules.md")
	b["contextFiles"] = append(b["contextFiles"].([]any), doc{"path": "rules.md", "text": "other"})
	if _, err := projectBundle("trial", b); err == nil {
		t.Fatal("accepted duplicate context")
	}
}
