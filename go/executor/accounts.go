package main

// account-inspect (contracts review N6): the coordinator's account_connect / account_discover and
// subscription_limits_refresh send it; the executor reads its own registry (FLEET_NATIVE_ACCOUNTS) and, for limits, asks
// the vendor CLI on this host. No token is read, stored or sent: the CLI holds its own login.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	if mode == "login" {
		return e.accountLogin(c, p, reg, rerr)
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
	case "antigravity":
		reading, err = antigravityReadLimits(x)
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
	passEnv(c, nativeEnvAllow...)
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
	// Fleet's fixed isolation (codexIsolation), as for a run: no user MCP servers, hooks, plugins or apps start for a limits read.
	r, err := rpcExchange(probeCmd(x, codexAppServerArgs()...), []M{
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

// antigravityReadLimits: agy's /usage slash command in print mode (agy 1.5.10, seen live 2026-11-25). It sends no prompt,
// so it uses no quota. It runs with the run's GEMINI_HOME and, for a native-current login, its own HOME.
func antigravityReadLimits(x registryEntry) (M, error) {
	cmd := probeCmd(x, "-p", "/usage")
	setEnv(cmd, "GEMINI_HOME", x.NativeHome)
	if x.CredentialAuthority == "native-current" {
		setEnv(cmd, "HOME", x.CredentialHome)
	}
	text, err := textExchange(cmd)
	if err != nil {
		return nil, err
	}
	windows := antigravityUsageWindows(text)
	if len(windows) == 0 {
		return nil, errors.New("agy /usage answered no limit lines")
	}
	return limitReading("antigravity", windows, text), nil
}

// antigravityUsageWindows reads agy /usage lines "<model group>\t<Weekly|Five Hour> Limit Remaining\t<n>%\t<RFC 3339
// reset>". Pct is the used share (100 minus remaining), as for the other harnesses. A reset at the Unix epoch means the
// window has not started, so it has no reset time. The model is the group's name in lower case with dashes
// ("gemini-models", "claude-and-gpt-models").
func antigravityUsageWindows(text string) []contract.LimitWindow {
	windows := []contract.LimitWindow{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) < 3 || !strings.HasSuffix(f[2], "%") {
			continue
		}
		period := map[string]string{"weekly limit remaining": "weekly", "five hour limit remaining": "5h"}[strings.ToLower(f[1])]
		left, err := strconv.ParseFloat(strings.TrimSuffix(f[2], "%"), 64)
		if period == "" || err != nil || left < 0 || left > 100 {
			continue
		}
		used := math.Round((100-left)*100) / 100
		model := strings.ToLower(strings.Join(strings.Fields(f[0]), "-"))
		w := contract.LimitWindow{Period: period, Pct: &used, Model: &model}
		if len(f) > 3 {
			if at, err := time.Parse(time.RFC3339, f[3]); err == nil && at.Unix() > 0 {
				v := at.UnixMilli()
				w.ResetsAt = &v
			}
		}
		windows = append(windows, w)
	}
	return windows
}

// textExchange starts cmd and returns its whole stdout once it exits, within probeWait. A CLI that asks for a sign-in on
// stderr (agy: "Authentication required") ends the wait at once with errSignIn instead of the timeout.
func textExchange(cmd *exec.Cmd) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeWait)
	defer cancel()
	out, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer out.Close()
	errOut, errW, err := os.Pipe()
	if err != nil {
		w.Close()
		return "", err
	}
	defer errOut.Close()
	cmd.Stdout, cmd.Stderr = w, errW
	p, err := supervise.Spawn(supervise.Spec{Cmd: cmd})
	w.Close()
	errW.Close()
	if err != nil {
		return "", err
	}
	defer func() { p.Kill(); p.Wait() }() // the CLI runs in its own group
	type stderrEnd struct {
		signIn bool
		tail   string
	}
	read, stderr := make(chan string, 1), make(chan stderrEnd, 2)
	errtrack.Go("text.read", func() {
		b, _ := io.ReadAll(io.LimitReader(out, 1<<20))
		read <- string(b)
	})
	errtrack.Go("text.stderr", func() {
		sc := bufio.NewScanner(errOut)
		var tail string
		for sc.Scan() {
			if strings.Contains(sc.Text(), "Authentication required") {
				stderr <- stderrEnd{signIn: true}
				return
			}
			if line := strings.TrimSpace(sc.Text()); line != "" {
				tail = line
			}
		}
		stderr <- stderrEnd{tail: tail}
	})
	var ended *stderrEnd // stderr's end, once it came first
	for {
		select {
		case text := <-read:
			// stdout ended: the CLI exited, so its stderr ends too; a sign-in prompt there wins over an empty answer.
			if ended == nil {
				select {
				case e := <-stderr:
					ended = &e
				case <-ctx.Done():
					ended = &stderrEnd{}
				}
			}
			if ended.signIn {
				return "", errSignIn
			}
			if strings.TrimSpace(text) == "" && ended.tail != "" {
				return "", errors.New("the CLI answered nothing: " + ended.tail)
			}
			return text, nil
		case e := <-stderr:
			if e.signIn {
				return "", errSignIn
			}
			ended, stderr = &e, nil // the answer still comes on stdout
		case <-ctx.Done():
			return "", errors.New("no answer from the CLI")
		}
	}
}

// errSignIn: the CLI is not signed in on this binding; it asked for a browser sign-in.
var errSignIn = errors.New("not signed in: the CLI asks for a Google sign-in (run agy on this host and sign in)")

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
// session/new answer. Nil when the CLI cannot answer, so the coordinator reads "not probed", not "offers nothing".
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
		r, err := rpcExchange(probeCmd(x, codexAppServerArgs()...), []M{
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
		if x.CredentialAuthority == "native-current" {
			setEnv(cmd, "HOME", x.CredentialHome) // as the start path: the login is read from its own HOME
		}
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

// Login on the host (inspection mode login, System › Access "Sign in"): the owner pressed Sign in for this binding.
// The vendor CLI's login (contracts/vocabulary.json loginArgv: codex device auth, claude, agy) starts under the binding's
// environment with its output in a private file; the command reports the sign-in link and code as soon as the CLI
// prints them and ends, while the CLI keeps waiting on the host for the owner's sign-in (at most loginWait). Fleet never
// sees a password; a later status inspection shows the login.
const (
	loginWait   = 15 * time.Minute
	loginPrompt = 60 * time.Second
)

var (
	loginURL  = regexp.MustCompile(`https://[^\s"'<>\x1b]+`)
	loginCode = regexp.MustCompile(`\b[A-Z0-9]{4,5}-[A-Z0-9]{4,5}\b`)
	ansiEsc   = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
)

func loginArgvOf(harness string) []string {
	for _, h := range contract.Vocabulary.Harnesses {
		if h.ID == harness {
			return h.LoginArgv
		}
	}
	return nil
}

func (e *executor) accountLogin(c contract.Command, p contract.AccountInspectPayload, reg map[string]registryEntry, rerr error) error {
	cid, _ := idOf(c, "id")
	execution, _ := idOf(c, "executionId")
	binding := str0(p.Binding)
	x, known := reg[binding]
	o := contract.AccountObservation{AccountId: p.AccountId, Binding: binding, Platform: contract.Platform(platform()), Quota: "unknown", Readiness: "unknown", Reason: "login-started"}
	prompt := contract.AccountLoginPrompt{State: "failed", StartedAt: nowMs()}
	argv := loginArgvOf(p.Harness)
	switch {
	case rerr != nil:
		o.Reason = rerr.Error()
	case !known || x.Harness != p.Harness || !filepath.IsAbs(x.Executable):
		o.Readiness, o.Reason = "unavailable", "binding not in this host's account registry for "+p.Harness
	case len(argv) == 0:
		o.Reason = "this harness has no login command"
	default:
		o.Readiness = nonEmpty(x.Readiness, "unknown")
		o.Roots = &contract.AccountRoots{Home: &x.Home, NativeHome: &x.NativeHome, Executable: &x.Executable}
		prompt = runLogin(x, argv)
		if prompt.State == "failed" && x.Harness == "claude" {
			// Claude Code's login takes the code back on its own terminal, which a host login started by Fleet does not have.
			t := "Claude's login asks for the code back in its own terminal; sign in on the host itself. " + str0(prompt.Text)
			prompt.Text = &t
		}
	}
	o.Login = &prompt
	r := contract.AccountReceipt{HostId: e.host, CommandId: cid, ExecutionId: execution, AccountObservation: &o}
	if err := e.inTx(func() error { return queueTo(e.ex, contract.RouteAccountsReport, r) }); err != nil {
		return err
	}
	return e.finish(c, "completed", "")
}

// runLogin starts the login and reads its output file until the CLI printed a link (and its code), ended, or
// loginPrompt passed. The CLI runs on in its own process group until it ends or loginWait passes.
func runLogin(x registryEntry, argv []string) contract.AccountLoginPrompt {
	out := contract.AccountLoginPrompt{State: "failed", StartedAt: nowMs()}
	executable, managed, err := managedExecutable(x.Harness, x.Executable, "")
	if err != nil {
		return withText(out, err.Error())
	}
	x.Executable, x.ReleaseManaged = executable, managed
	cmd := probeCmd(x, argv...)
	if x.Harness == "antigravity" {
		setEnv(cmd, "GEMINI_HOME", x.NativeHome)
		if x.CredentialAuthority == "native-current" {
			setEnv(cmd, "HOME", x.CredentialHome)
		}
	}
	log := filepath.Join(x.Home, ".fleet-login.log")
	f, err := os.OpenFile(log, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return withText(out, err.Error())
	}
	cmd.Stdout, cmd.Stderr = f, f
	proc, err := supervise.Spawn(supervise.Spec{Cmd: cmd})
	f.Close()
	if err != nil {
		return withText(out, err.Error())
	}
	ended := make(chan error, 1)
	errtrack.Go("account.login", func() {
		t := time.AfterFunc(loginWait, func() { proc.Kill() })
		ended <- proc.Wait()
		t.Stop()
	})
	deadline := time.Now().Add(loginPrompt)
	var urlSeen time.Time
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(log)
		text := ansiEsc.ReplaceAllString(string(b), "")
		u, code := loginURL.FindString(text), loginCode.FindString(text)
		select {
		case werr := <-ended:
			out = withText(out, text)
			if u != "" {
				out.Url = &u
			}
			if werr == nil {
				out.State = "signed-in"
			}
			return out
		default:
		}
		if u != "" && urlSeen.IsZero() {
			urlSeen = time.Now()
		}
		if u != "" && (code != "" || time.Since(urlSeen) > 3*time.Second) {
			out = withText(out, text)
			out.State, out.Url = "waiting", &u
			if code != "" {
				out.Code = &code
			}
			return out
		}
		time.Sleep(300 * time.Millisecond)
	}
	b, _ := os.ReadFile(log)
	return withText(out, "the login printed no sign-in link within a minute: "+ansiEsc.ReplaceAllString(string(b), ""))
}

// withText: the last lines of the CLI's output, at most 500 characters.
func withText(p contract.AccountLoginPrompt, text string) contract.AccountLoginPrompt {
	t := strings.TrimSpace(text)
	if r := []rune(t); len(r) > 500 {
		t = string(r[len(r)-500:])
	}
	if t != "" {
		p.Text = &t
	}
	return p
}
