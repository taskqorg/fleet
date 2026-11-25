package main

import (
	"encoding/json"
	"testing"
)

func TestCheckDevEnvironment(t *testing.T) {
	podman := json.RawMessage(`["podman"]`)
	for _, c := range []struct {
		env   string
		tools json.RawMessage
		ok    bool
	}{
		{`{"build":"Dockerfile","workdir":"/app","services":[{"name":"db","image":"mysql:8.0","env":{"MYSQL_DATABASE":"video"}}],"setup":"composer install"}`, podman, true},
		{`{"image":"php:8.2-cli"}`, podman, true},
		{`{"image":"php:8.2-cli"}`, json.RawMessage(`["node"]`), false},
		{`{"image":"php:8.2-cli","build":"Dockerfile"}`, podman, false},
		{`{}`, podman, false},
		{`{"build":"../Dockerfile"}`, podman, false},
		{`{"image":"x","workdir":"/"}`, podman, false},
		{`{"image":"x","services":[{"name":"main","image":"y"}]}`, podman, false},
		{`{"image":"x","services":[{"name":"db","image":"y"},{"name":"db","image":"z"}]}`, podman, false},
		{`{"image":"x","ports":[80]}`, podman, false},
	} {
		if err := checkDevEnvironment(json.RawMessage(c.env), c.tools); (err == nil) != c.ok {
			t.Errorf("%s %s: err %v, want ok %v", c.env, c.tools, err, c.ok)
		}
	}
}

func TestProjectSettingsEnvironment(t *testing.T) {
	_, unknown, err := projectSettings(map[string]any{"execution": map[string]any{"tools": []any{"podman"}, "environment": map[string]any{"image": "php:8.2-cli"}}})
	if err != nil || len(unknown) != 0 {
		t.Fatalf("err %v unknown %v", err, unknown)
	}
	if _, _, err := projectSettings(map[string]any{"execution": map[string]any{"environment": map[string]any{"image": "php:8.2-cli"}}}); err == nil {
		t.Fatal("environment without podman accepted")
	}
}
