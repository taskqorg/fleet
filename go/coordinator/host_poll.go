package main

import (
	"encoding/json"
	"errors"

	"fleet/contract"
)

// POST /api/hosts/poll (SL5). The poll records the host observation and its stores, reconciles delegations,
// marks problems, promotes queued messages, runs dispatch, and replies with the polling host's pending commands that may go
// out now.

func init() {
	route("POST /api/hosts/poll", func(q *Request) (any, error) { return q.app.poll(q) })
}

// hostProblems: the edge problems a host reports (at most 20, texts cut), redirects only.
func hostProblems(list []contract.EdgeProblem) []any {
	out := []any{}
	cut := func(t string, n int) string {
		if r := []rune(t); len(r) > n {
			return string(r[:n])
		}
		return t
	}
	for _, p := range list {
		if p.Kind != contract.ProblemKindEdgeRedirect {
			continue
		}
		var loc any
		if p.Location != nil {
			loc = cut(*p.Location, 500)
		}
		out = append(out, doc{"kind": string(p.Kind), "route": cut(p.Route, 200), "status": p.Status, "location": loc, "detail": cut(p.Detail, 500)})
		if len(out) == 20 {
			break
		}
	}
	return out
}

func (a *App) poll(q *Request) (any, error) {
	var b contract.PollRequest
	if err := decode(q.body, &b); err != nil {
		return nil, err
	}
	host := hostOf(q)
	var stores any
	json.Unmarshal(jsonBytes(b.Stores), &stores)
	if err := storesObserve(q.ctx(), q.tx, host, stores); err != nil {
		return nil, err
	}
	obs := doc{"id": host, "holder": q.host["holder"], "lastSeenAt": nowMs()}
	switch b.Platform {
	case contract.PlatformDarwin, contract.PlatformWin32, contract.PlatformLinux:
		obs["platform"] = string(b.Platform)
	}
	if b.Capabilities != nil {
		obs["capabilities"] = b.Capabilities
	}
	if b.ProjectRoots != nil {
		obs["projectRoots"] = b.ProjectRoots
	}
	ps := hostProblems(b.Problems)
	if b.Version != nil {
		v := *b.Version
		obs["version"] = v
		// N-1 is served without a flag; N-2 and older show version-behind (work still runs).
		if prev := ownRelease().Previous; olderVersion(v, prev) {
			ps = append(ps, doc{"kind": string(contract.ProblemKindVersionBehind), "route": "", "status": 0, "location": nil,
				"detail": "executor " + v + ", coordinator serves " + prev + " and newer"})
		}
	}
	if ps != nil {
		obs["problems"] = ps
	}
	var upd any // the plain JSON value: stored and compared with the last poll's
	if b.Update != nil {
		json.Unmarshal(jsonBytes(b.Update), &upd)
	}
	if host != "" && host == pds18Host() {
		upd = mergeUpdate(upd) // PDS18: the coordinator side's result from fleet-update joins the executor's
	}
	if r, ok := decodeUpdate(upd); ok {
		obs["update"] = upd
		// Polls run about every 100 ms: the question is looked at only when the result changes.
		if prev, err := get(q, host, "host-observation"); err != nil && !errors.Is(err, errNotFound) {
			return nil, err
		} else if js(mapOf(prev)["update"]) != js(upd) {
			if err := updateQuestion(q, host, q.host["holder"], r); err != nil {
				return nil, err
			}
		}
	}
	if err := put(q, "host-observation", obs); err != nil {
		return nil, err
	}
	if err := a.delegationReconcile(q); err != nil {
		return nil, err
	}
	ids, err := hostIDs(q)
	if err != nil {
		return nil, err
	}
	envs := map[string][]string{}
	for _, p := range projectIDs() {
		if st, err := projectSettingsByID(p); err == nil {
			hs := []string{}
			for _, x := range anys(st["environments"]) {
				hs = append(hs, astr(mapOf(x)["hostId"]))
			}
			envs[p] = hs
		}
	}
	if err := detectProblems(q, ids, envs); err != nil {
		return nil, err
	}
	if err := agentPromote(q); err != nil {
		return nil, err
	}
	if err := a.dispatchRun(q, host); err != nil {
		return nil, err
	}
	pending, err := rows(q, "SELECT data FROM document WHERE kind='command' AND data->>'hostId'=$1 AND data->>'status'='pending' ORDER BY seq", host)
	if err != nil {
		return nil, err
	}
	out := []doc{}
	for _, c := range pending {
		ok, err := agentReady(q, c)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		e, err := get(q, c["executionId"], "execution")
		if err != nil {
			return nil, err
		}
		// Held-host trust: work that carries project content goes only to a host whose holder may see the project.
		if k := astr(c["kind"]); k == "start" || k == "script" || k == "delivery-step" {
			if !a.hostMaySee(q.host, nonNil(e["projectId"], mapOf(c["payload"])["projectId"])) {
				continue
			}
		}
		if c["kind"] != "stop" {
			ok, err := a.delegationReady(q, e)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		// The ready filter may rewrite the command (delegation inputs): reply with it as stored now.
		fresh, err := get(q, c["id"], "command")
		if err != nil {
			return nil, err
		}
		out = append(out, fresh)
	}
	reply := contract.PollReply{Target: healthyTarget(), AutoUpdate: ptr(autoUpdateOf(q.host))}
	return doc{"commands": out, "target": reply.Target, "autoUpdate": reply.AutoUpdate}, nil
}

func safeInt(v any) any {
	f, ok := v.(float64)
	if ok && f == float64(int64(f)) && f >= -(1<<53-1) && f <= 1<<53-1 {
		return int64(f)
	}
	return nil
}
