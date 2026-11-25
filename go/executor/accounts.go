package main

// account-inspect (contracts review N6): the coordinator's account_connect / account_discover and
// subscription_limits_refresh send it; the executor reads its own registry (FLEET_NATIVE_ACCOUNTS) and, for limits, asks
// the vendor CLI on this host. No token is read, stored or sent: the CLI holds its own login.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fleet/contract"
	"fleet/errtrack"
	"fleet/executor/supervise"
)

// registryEntry is one named host account of FLEET_NATIVE_ACCOUNTS.
type registryEntry struct {
	ReleaseManaged bool   `json:"-"`
	Harness        string `json:"harness"`
	Home           string `json:"home"`
	NativeHome     string `json:"nativeHome"`
	Executable     string `json:"executable"`
	Readiness      string `json:"readiness"`
	// CredentialAuthority native-current: the login lives in CredentialHome (HOME for Claude), as the start path uses it.
	CredentialAuthority string `json:"credentialAuthority"`
	CredentialHome      string `json:"credentialHome"`
}

func readRegistry() (map[string]registryEntry, error) {
	path := os.Getenv("FLEET_NATIVE_ACCOUNTS")
	if path == "" {
		return nil, errors.New("FLEET_NATIVE_ACCOUNTS required")
	}
	if err := private(path); err != nil {
		return nil, err
	}
	if st, err := os.Stat(path); err != nil || owner(st) != os.Geteuid() {
		return nil, errors.New("registry must be owned by executor user")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r map[string]registryEntry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, errors.New("account registry unreadable")
	}
	return r, nil
}

// runnable: the same gate as the start path (commandRegistry) before anything is launched under an entry: reviewed
// ready, absolute executable, private home directories, no shared native-current login, Claude placement.
func runnable(x registryEntry) error {
	executable, managed, err := managedExecutable(x.Harness, x.Executable, "")
	if err != nil {
		return err
	}
	switch {
	case x.Readiness != "ready":
		return errors.New("named host account is unprovisioned; operator login and readiness review required")
	case !filepath.IsAbs(executable):
		return errors.New("host executable must be absolute")
	case x.CredentialAuthority != "" && x.CredentialAuthority != "native-current":
		return errors.New("unsupported credential authority")
	case x.CredentialAuthority == "native-current":
		// The same check as commandRegistry: an owner-controlled directory.
		st, err := os.Lstat(x.CredentialHome)
		if err != nil || !filepath.IsAbs(x.CredentialHome) || !st.IsDir() || owner(st) != os.Geteuid() || st.Mode().Perm()&0o022 != 0 {
			return errors.New("native-current credential home must be owner-controlled")
		}
	}
	if managed && !isFile(executable) {
		return errors.New("release-managed executable missing")
	}
	for _, d := range []string{x.Home, x.NativeHome} {
		if err := private(d); err != nil {
			return err
		}
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			return errors.New("homes must be directories")
		}
	}
	if x.Harness == "claude" {
		if _, err := claudePlacementHere(); err != nil {
			return errors.New("Claude is ineligible on this host: " + err.Error())
		}
	}
	return nil
}

// nativeEnv: the environment variables a harness reads its home from.
func nativeEnv(e registryEntry) contract.AccountEnvironment {
	env := contract.AccountEnvironment{HOME: &e.Home}
	switch e.Harness {
	case "codex":
		env.CODEXHOME = &e.NativeHome
	case "claude":
		env.CLAUDECONFIGDIR = &e.NativeHome
	case "antigravity":
		env.GEMINIHOME = &e.NativeHome
	}
	return env
}

// loginObserved: the harness's own credential file exists in its native home; presence only, never read.
func loginObserved(e registryEntry) *bool {
	file := map[string]string{"codex": "auth.json", "claude": ".credentials.json"}[e.Harness]
	if file == "" {
		return nil
	}
	_, err := os.Stat(filepath.Join(e.NativeHome, file))
	ok := err == nil
	return &ok
}

func (e *executor) accountInspect(c contract.Command) error {
	p, err := payloadOf[contract.AccountInspectPayload](c)
	if err != nil {
		return e.finish(c, "failed", "")
	}
	cid, _ := idOf(c, "id")
	execution, _ := idOf(c, "executionId")
	mode := str0(p.Mode)
	reg, rerr := readRegistry()
	if mode == "limits" {
		return e.readLimits(c, p, reg, rerr)
	}
	r := contract.AccountReceipt{HostId: e.host, CommandId: cid, ExecutionId: execution}
	if mode == "catalogue" {
		if rerr != nil {
			if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: rerr.Error()}); err != nil {
				return err
			}
			return e.finish(c, "failed", "")
		}
		cat := contract.AccountCatalogue{AccountId: p.AccountId, Binding: p.Binding, Bindings: []contract.AccountBinding{}}
		for _, name := range sortedKeysOf(reg) {
			if x := reg[name]; x.Harness == p.Harness && filepath.IsAbs(x.Executable) {
				cat.Bindings = append(cat.Bindings, contract.AccountBinding{Name: name, Executable: x.Executable, Environment: nativeEnv(x)})
			}
		}
		r.AccountCatalogue = &cat
	} else {
		binding := str0(p.Binding)
		x, known := reg[binding]
		o := contract.AccountObservation{AccountId: p.AccountId, Binding: binding, Platform: contract.Platform(platform()), Quota: "unknown", Readiness: "unknown"}
		switch {
		case rerr != nil:
			o.Reason = rerr.Error()
		case !known || x.Harness != p.Harness || !filepath.IsAbs(x.Executable):
			o.Readiness, o.Reason = "unavailable", "binding not in this host's account registry for "+p.Harness
		default:
			o.NativeLoginObserved = loginObserved(x)
			if x.Readiness == "ready" {
				o.Readiness, o.Reason = "ready", "registry readiness ready"
			} else {
				o.Reason = "registry readiness " + nonEmpty(x.Readiness, "unset")
			}
			o.Roots = &contract.AccountRoots{Home: &x.Home, NativeHome: &x.NativeHome, Executable: &x.Executable}
			if o.Readiness == "ready" {
				o.Models = probeModels(x) // J5: absent when the probe fails, never an empty list
			}
		}
		r.AccountObservation = &o
	}
	if err := e.inTx(func() error { return queueTo(e.ex, contract.RouteAccountsReport, r) }); err != nil {
		return err
	}
	return e.finish(c, "completed", "")
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// readLimits (section 8 "Read without a run"): ask the vendor CLI for its current limits and report them as a
// limit_reading event with source probe. A harness with no such request answers unsupported.
func (e *executor) readLimits(c contract.Command, p contract.AccountInspectPayload, reg map[string]registryEntry, rerr error) error {
	fail := func(reason string) error {
		if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: reason}); err != nil {
			return err
		}
		return e.finish(c, "failed", "")
	}
	if rerr != nil {
		return fail(rerr.Error())
	}
	x, ok := reg[str0(p.Binding)]
	if !ok || x.Harness != p.Harness {
		return fail("binding not in this host's account registry for " + p.Harness)
	}
	if err := runnable(x); err != nil {
		return fail(err.Error())
	}
	executable, managed, err := managedExecutable(x.Harness, x.Executable, "")
	if err != nil {
		return fail(err.Error())
	}
	x.Executable = executable
	x.ReleaseManaged = managed
	var reading M
	switch p.Harness {
	case "codex":
		reading, err = codexReadLimits(x)
	case "claude":
		reading, err = claudeReadLimits(x)
	default:
		return fail("unsupported: " + p.Harness + " has no limits request")
	}
	if err != nil {
		return fail("limits read failed: " + err.Error())
	}
	reading["source"], reading["subscriptionId"], reading["hostId"] = string(contract.LimitSourceProbe), p.AccountId, e.host
	if err := e.event(c, 1, contract.FleetEventTypeLimitReading, reading); err != nil {
		return err
	}
	return e.finish(c, "completed", "")
}

// ponytail: one bounded wait for a vendor answer (60 s), so a hung CLI never holds the command loop; a background
// probe is the upgrade if reads get slow.
const probeWait = 60 * time.Second

// rpcExchange starts cmd, writes frames (one JSON line each) and returns the first stdout object accept takes.
func rpcExchange(cmd *exec.Cmd, frames []M, accept func(M) bool) (M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeWait)
	defer cancel()
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// Our own pipe, not StdoutPipe: the reaper's Wait would close that one at exit, before its last lines are read.
	out, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer out.Close()
	cmd.Stdout = w
	p, err := supervise.Spawn(supervise.Spec{Cmd: cmd})
	w.Close()
	if err != nil {
		return nil, err
	}
	defer func() { p.Kill(); p.Wait() }() // the CLI runs in its own group
	for _, f := range frames {
		if _, err := in.Write([]byte(enc(f) + "\n")); err != nil {
			return nil, err
		}
	}
	found := make(chan M, 1)
	errtrack.Go("rpc.read", func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			if v, ok := decAny(sc.Text()).(M); ok && accept(v) {
				found <- v
				return
			}
		}
		close(found)
	})
	select {
	case v, ok := <-found:
		if !ok {
			return nil, errors.New("the CLI ended without an answer")
		}
		return v, nil
	case <-ctx.Done():
		return nil, errors.New("no answer from the CLI")
	}
}

func decAny(s string) any { v, _ := dec(s); return v }

func probeCmd(x registryEntry, args ...string) *exec.Cmd {
	c := newCmd(x.Executable, x.Home)
	if x.ReleaseManaged && x.Harness == "claude" {
		setEnv(c, "DISABLE_AUTOUPDATER", "1")
	}
	passEnv(c, "PATH", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR")
	setEnv(c, "HOME", x.Home)
	env := nativeEnv(x)
	if env.CODEXHOME != nil {
		setEnv(c, "CODEX_HOME", *env.CODEXHOME)
	}
	if env.CLAUDECONFIGDIR != nil {
		setEnv(c, "CLAUDE_CONFIG_DIR", *env.CLAUDECONFIGDIR)
	}
	if x.Harness == "claude" {
		// As the start path: the user names the login keychain; a native-current login is read from its own HOME.
		if u, err := user.Current(); err == nil && u.Username != "" {
			setEnv(c, "USER", u.Username)
			setEnv(c, "LOGNAME", u.Username)
		}
		if x.CredentialAuthority == "native-current" {
			setEnv(c, "HOME", x.CredentialHome)
		}
	}
	c.Args = append(c.Args, args...)
	return c
}

// codexReadLimits: codex app-server, initialize, then account/rateLimits/read.
func codexReadLimits(x registryEntry) (M, error) {
	// Fleet's fixed isolation: no user MCP servers, hooks, plugins or apps start for a limits read.
	r, err := rpcExchange(probeCmd(x, "app-server", "-c", "mcp_servers={}", "--disable", "hooks", "--disable", "plugins", "--disable", "apps", "--disable", "memories"), []M{
		{"id": 1, "method": "initialize", "params": M{"clientInfo": M{"name": "fleet", "version": "0.1.0"}}},
		{"method": "initialized"},
		{"id": 2, "method": "account/rateLimits/read"},
	}, func(v M) bool { return isID(v, 2) })
	if err != nil {
		return nil, err
	}
	if has(r, "error") {
		return nil, errors.New("account/rateLimits/read refused")
	}
	limits := jAt(r, "result", "rateLimits")
	if limits == nil {
		limits = jAt(r, "result")
	}
	return codexLimitReading(limits, r), nil
}

// claudeReadLimits: Claude Code stream-json control request get_usage; its windows carry utilization 0-100 already.
func claudeReadLimits(x registryEntry) (M, error) {
	r, err := rpcExchange(probeCmd(x, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--strict-mcp-config", "--setting-sources", ""), []M{
		{"type": "control_request", "request_id": "fleet-init", "request": M{"subtype": "initialize"}},
		{"type": "control_request", "request_id": "fleet-usage", "request": M{"subtype": "get_usage"}},
	}, func(v M) bool {
		a := claudeControlAnswer(v)
		return a.Type == "control_response" && a.Response.RequestId == "fleet-usage"
	})
	if err != nil {
		return nil, err
	}
	a := claudeControlAnswer(r)
	if a.Response.Subtype != "success" {
		return nil, errors.New("get_usage refused")
	}
	native := typed[struct {
		RateLimits jsonValue `json:"rate_limits"`
	}](a.Response.Response).RateLimits.V
	return claudeUsageReading(json.RawMessage(enc(a.Response.Response.V)), native), nil
}

// claudeControl: a Claude Code stream-json control response; a field of another type reads as absent.
// A response that is not an object: only the type is read.
type claudeControl struct {
	Type     string `json:"type"`
	Response struct {
		RequestId string    `json:"request_id"`
		Subtype   string    `json:"subtype"`
		Response  jsonValue `json:"response"`
	} `json:"response"`
}

func claudeControlAnswer(v M) claudeControl { return typed[claudeControl](v) }

// claudeUsageReading: a get_usage answer (Claude Code 2.1.319, seen live 2026-11-11): response.rate_limits holds
// five_hour, seven_day and seven_day_<name> as {utilization 0-100, resets_at RFC 3339} or null, and model_scoped as
// [{display_name, utilization, resets_at}] for per-model weekly limits. Other keys (extra usage, spend, breakdowns) are
// not limit windows.
func claudeUsageReading(answer json.RawMessage, native any) M {
	// The windows: rate_limits when it is an object, else the answer itself; a value that is not an object has none.
	ans := decRaw(answer)
	outer := typed[struct {
		RateLimits jsonValue `json:"rate_limits"`
	}](ans)
	usage, ok := outer.RateLimits.V.(M)
	if !ok {
		usage, _ = ans.(M)
	}
	type window struct {
		Utilization jsonValue `json:"utilization"`
		ResetsAt    jsonValue `json:"resets_at"`
		DisplayName string    `json:"display_name"`
	}
	windows := []contract.LimitWindow{}
	add := func(period string, w window, model *string) {
		u, ok := num(w.Utilization.V)
		if !ok {
			return
		}
		lw := contract.LimitWindow{Period: period, Pct: &u, Model: model}
		if t, ok := valStr(w.ResetsAt); ok {
			if at, err := time.Parse(time.RFC3339, t); err == nil {
				v := at.UnixMilli()
				lw.ResetsAt = &v
			}
		} else {
			lw.ResetsAt = ms(w.ResetsAt.V) // epoch seconds, as rate_limit_event sends
		}
		windows = append(windows, lw)
	}
	read := func(v any) window { return typed[window](v) }
	for _, k := range sortedKeysOf(usage) {
		if k == "five_hour" || k == "seven_day" || strings.HasPrefix(k, "seven_day_") {
			var model *string
			if m := strings.TrimPrefix(k, "seven_day_"); m != k {
				model = &m
			}
			add(claudePeriod(k), read(usage[k]), model)
		}
	}
	scoped, _ := usage["model_scoped"].([]any)
	for _, raw := range scoped {
		// One model name for both sources: the lower-case key form (seven_day_opus and a display name "Opus" agree).
		w := read(raw)
		if name := strings.ToLower(strings.ReplaceAll(w.DisplayName, " ", "-")); name != "" {
			add("weekly-"+name, w, &name)
		}
	}
	return limitReading("claude", windows, native)
}

// claudePeriod: fleet-people-access.md section 8 period table for Claude window names.
func claudePeriod(t string) string {
	switch {
	case t == "five_hour":
		return "5h"
	case t == "seven_day":
		return "weekly"
	case strings.HasPrefix(t, "seven_day_"):
		return "weekly-" + strings.TrimPrefix(t, "seven_day_")
	}
	return t
}

// probeModels (J5, docs/designs/fleet-ia-and-journeys.md section 3): the models the vendor CLI offers this binding,
// read at inspection: codex app-server model/list, Claude Code's stream-json initialize answer, Antigravity's ACP
// session/new answer. Nil when the CLI cannot
// answer, so the coordinator reads "not probed", not "offers nothing".
func probeModels(x registryEntry) []string {
	if runnable(x) != nil {
		return nil
	}
	executable, managed, err := managedExecutable(x.Harness, x.Executable, "")
	if err != nil {
		return nil
	}
	x.Executable, x.ReleaseManaged = executable, managed
	var list any
	switch x.Harness {
	case "codex":
		r, err := rpcExchange(probeCmd(x, "app-server", "-c", "mcp_servers={}", "--disable", "hooks", "--disable", "plugins", "--disable", "apps", "--disable", "memories"), []M{
			{"id": 1, "method": "initialize", "params": M{"clientInfo": M{"name": "fleet", "version": "0.1.0"}}},
			{"method": "initialized"},
			{"id": 2, "method": "model/list", "params": M{}},
		}, func(v M) bool { return isID(v, 2) })
		if err != nil || has(r, "error") {
			return nil
		}
		list = jAt(r, "result", "data")
		if list == nil {
			list = jAt(r, "result", "models")
		}
	case "claude":
		r, err := rpcExchange(probeCmd(x, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
			"--strict-mcp-config", "--setting-sources", ""), []M{
			{"type": "control_request", "request_id": "fleet-init", "request": M{"subtype": "initialize"}},
		}, func(v M) bool {
			a := claudeControlAnswer(v)
			return a.Type == "control_response" && a.Response.RequestId == "fleet-init"
		})
		if err != nil || claudeControlAnswer(r).Response.Subtype != "success" {
			return nil
		}
		list = jAt(r, "response", "response", "models")
	case "antigravity":
		// ACP: initialize, then session/new answers the models the login offers. No prompt is sent, so no quota is used.
		// The run's argv (harness_command.go) and its GEMINI_HOME; the session's folder is the binding's own home.
		cmd := probeCmd(x, "acp", "--gemini-dir", x.NativeHome, "--app-data-dir", "antigravity-cli", "--include-default-customizations=false")
		setEnv(cmd, "GEMINI_HOME", x.NativeHome)
		r, err := rpcExchange(cmd, []M{
			acpRequest(1, "initialize", M{"protocolVersion": 1, "clientCapabilities": M{}, "clientInfo": M{"name": "fleet", "version": "0.1.0"}}),
			acpRequest(2, "session/new", M{"cwd": x.Home, "mcpServers": []any{}}),
		}, func(v M) bool { return isID(v, 2) })
		if err != nil || has(r, "error") {
			return nil
		}
		list = acpModelList(typed[acpResult](jAt(r, "result")))
	}
	return modelNames(list)
}

// acpModelList: the model ids an ACP session/new answer offers: the legacy models.availableModels and the values of the
// model select option (grouped values included), as the run path reads them (acpSelect). Nil when it names none.
func acpModelList(r acpResult) []any {
	var out []any
	for _, m := range r.Models.AvailableModels {
		if m.ModelID != nil {
			out = append(out, *m.ModelID)
		}
	}
	if opt := acpSelect(r, "model", "", ""); opt != nil {
		for _, v := range opt.Options {
			for _, x := range append([]acpOptionValue{v}, v.Options...) {
				if x.Value != nil {
					out = append(out, *x.Value)
				}
			}
		}
	}
	return out
}

// modelNames: the names in a vendor model list (strings, or objects naming value, id, model or name), at most 200,
// each at most 200 characters, duplicates dropped. Nil when the list holds none.
func modelNames(list any) []string {
	items, _ := list.([]any)
	var out []string
	seen := map[string]bool{}
	for _, it := range items {
		name, _ := it.(string)
		if m, ok := it.(M); ok {
			for _, k := range []string{"value", "id", "model", "name"} {
				if v, ok := m[k].(string); ok && v != "" {
					name = v
					break
				}
			}
		}
		name = strings.TrimSpace(name)
		if name == "" || len(name) > 200 || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) == 200 {
			break
		}
	}
	return out
}
