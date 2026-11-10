package main

// Small project and agent lookups shared across domains.

func anys(v any) []any { xs, _ := v.([]any); return xs }

func projectSetting(projectID any, key string) any {
	p, ok := projectID.(string)
	if !ok || p == "" {
		return nil
	}
	st, err := projectSettingsByID(p)
	if err != nil {
		return nil
	}
	return st[key]
}

// agentReady: agent.ready(command) in the poll's ready filter (a steer of a finished execution waits forever).
func agentReady(q *Request, c doc) (bool, error) {
	if c["kind"] != "steer" {
		return true, nil
	}
	e, err := aget(q, c["executionId"], "execution")
	return err == nil && !legacyTerminal(e), err
}

func takeTaskFrom(q *Request, taskID, taker string, from any) error {
	return takeTask(q, taskID, taker, astr(from))
}

// projectSettingsByID: project_context(projectId).settings; error when unknown.
func projectSettingsByID(id string) (doc, error) {
	p, err := projectContext(id)
	if err != nil {
		return nil, err
	}
	return mapOf(p["settings"]), nil
}

// gitNetworkOf: whether the session's agent may use the network for git (its profile, else a project role with repos).
func gitNetworkOf(s doc) bool {
	if cfg, ok := s["agentConfig"].(map[string]any); ok {
		if v, ok := cfg["gitNetwork"].(bool); ok {
			return v
		}
	}
	p := astr(s["projectId"])
	role := astr(s["role"])
	if p == "" || (role != "pm" && role != "reviewer" && role != "worker") {
		return false
	}
	st, err := projectSettingsByID(p)
	return err == nil && len(anys(st["repos"])) > 0
}
