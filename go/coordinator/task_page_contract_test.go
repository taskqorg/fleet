package main

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"fleet/contract"
	"fleet/mcpserve"
)

// T-FLT-69: discovery must describe the same task filters and choices as the coordinator.
// A shared filter or open-ended choice can make agents send requests the coordinator refuses.
func TestTaskPageToolContract(t *testing.T) {
	type schema struct {
		Properties map[string]schema
		Items      *schema
		Enum       []*string
		Default    any
	}
	decode := func(name string) schema {
		t.Helper()
		var out schema
		if err := json.Unmarshal(mcpserve.List([]string{name})[0].InputSchema, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	page := decode("task_page")
	filter := page.Properties["filter"].Properties
	if got := slices.Sorted(maps.Keys(filter)); !slices.Equal(got, []string{"excludeProjectId", "projectId", "search", "state"}) {
		t.Fatalf("advertised task filters: %v", got)
	}
	choices := func(label string, s schema, expected []string, nullable bool) {
		t.Helper()
		var actual []string
		var nulls int
		for _, v := range s.Enum {
			if v == nil {
				nulls++
			} else {
				actual = append(actual, *v)
			}
		}
		if !slices.Equal(slices.Sorted(slices.Values(actual)), slices.Sorted(slices.Values(expected))) || nulls > 1 || (nulls == 1) != nullable {
			t.Errorf("advertised %s: %q and %d nulls, want %q (nullable %v)", label, actual, nulls, expected, nullable)
		}
	}
	if states := filter["state"].Items; states != nil {
		choices("states", *states, listStates, false)
	} else {
		t.Error("missing task state items")
	}
	group, sort := page.Properties["group"], page.Properties["sort"]
	choices("groups", group, slices.Collect(maps.Keys(taskGroups)), true)
	choices("sorts", sort, []string{"", "changed", "created", "-changed", "-created"}, true)
	if group.Default != "none" || sort.Default != defaultTaskSort {
		t.Errorf("advertised defaults: group %v, sort %v", group.Default, sort.Default)
	}
	sessionFilter := decode("sessions_list").Properties["filter"].Properties
	for _, name := range []string{"archived", "botId", "subject", "room", "origin", "listed"} {
		if _, ok := sessionFilter[name]; !ok {
			t.Errorf("sessions_list lost the %s filter", name)
		}
	}

	e := openTestCoordinator(t, false)
	h := e.ownerHeaders()
	for range 2 {
		var task contract.TaskDetail
		e.opInto(t, "task_create", doc{"title": "Task page fixture", "held": true}, h, &task)
	}
	input := doc{"filter": doc{"projectId": []any{nil}, "state": []string{"held"}}, "group": nil, "sort": "", "limit": 1}
	var first, second contract.TaskPageReply
	e.opInto(t, "task_page", input, h, &first)
	if len(first.Tasks) != 1 || len(first.NextAfter) == 0 || first.Tasks[0].ProjectId != nil || deref(first.Tasks[0].PauseReason) != "held" {
		t.Fatalf("unexpected first page: %+v", first)
	}
	input["after"] = first.NextAfter
	e.opInto(t, "task_page", input, h, &second)
	if len(second.Tasks) != 1 || first.Tasks[0].Id == second.Tasks[0].Id {
		t.Fatalf("unexpected second page: %+v", second)
	}
	for _, c := range []struct {
		input doc
		want  string
	}{
		{doc{"filter": doc{"archived": true}}, "Unknown filter field archived"},
		{doc{"filter": doc{"state": []string{"invalid"}}}, "Invalid state"},
		{doc{"sort": "invalid"}, "sort must be changed, created, -changed or -created"},
		{doc{"group": ""}, "Invalid group"},
	} {
		status, res := e.callJSON(t, "POST", "/api/ops/task_page", c.input, h)
		if status != 400 || res["message"] != c.want {
			t.Errorf("task_page(%v): %d %v, want 400 %q", c.input, status, res, c.want)
		}
	}
}
