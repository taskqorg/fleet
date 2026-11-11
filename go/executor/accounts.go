package main

// account-inspect (contracts review N6): the coordinator's account_connect / account_discover and
// subscription_limits_refresh send it; the executor reads its own registry (FLEET_NATIVE_ACCOUNTS) and, for limits, asks
// the vendor CLI on this host. No token is read, stored or sent: the CLI holds its own login.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fleet/contract"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// registryEntry is one named host account of FLEET_NATIVE_ACCOUNTS.
type registryEntry struct {
	Harness    string `json:"harness"`
	Home       string `json:"home"`
	NativeHome string `json:"nativeHome"`
	Executable string `json:"executable"`
	Readiness  string `json:"readiness"`
	// CredentialAuthority native-current (a shared login) is run only by the start path, never probed.
	CredentialAuthority string `json:"credentialAuthority"`
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
	switch {
	case x.Readiness != "ready":
		return errors.New("named host account is unprovisioned; operator login and readiness review required")
	case !filepath.IsAbs(x.Executable):
		return errors.New("host executable must be absolute")
	case x.CredentialAuthority != "":
		return errors.New("unsupported: a native-current login is not probed")
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

func (e *executor) accountInspect(c M) error {
	p, err := payloadOf[contract.AccountInspectPayload](c)
	if err != nil {
		return e.finish(c, "failed", "")
	}
	cid, _ := cmdID(c, "id")
	execution, _ := cmdID(c, "executionId")
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
		}
		r.AccountObservation = &o
	}
	if err := e.inTx(func() error { return e.queue("/api/hosts/accounts/report", r) }); err != nil {
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
func (e *executor) readLimits(c M, p contract.AccountInspectPayload, reg map[string]registryEntry, rerr error) error {
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
	var reading M
	var err error
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
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() }() // the CLI runs in its own group (newCmd)
	for _, f := range frames {
		if _, err := in.Write([]byte(enc(f) + "\n")); err != nil {
			return nil, err
		}
	}
	found := make(chan M, 1)
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			if v, ok := decAny(sc.Text()).(M); ok && accept(v) {
				found <- v
				return
			}
		}
		close(found)
	}()
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
	passEnv(c, "PATH", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR")
	setEnv(c, "HOME", x.Home)
	env := nativeEnv(x)
	if env.CODEXHOME != nil {
		setEnv(c, "CODEX_HOME", *env.CODEXHOME)
	}
	if env.CLAUDECONFIGDIR != nil {
		setEnv(c, "CLAUDE_CONFIG_DIR", *env.CLAUDECONFIGDIR)
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
		return jStr(v, "type") == "control_response" && jStr(v, "response", "request_id") == "fleet-usage"
	})
	if err != nil {
		return nil, err
	}
	if jStr(r, "response", "subtype") != "success" {
		return nil, errors.New("get_usage refused")
	}
	return claudeUsageReading(jObj(r, "response", "response"), r), nil
}

// claudeUsageReading: each window of a get_usage answer ({five_hour: {utilization, resets_at}, seven_day: ...,
// seven_day_<model>: ...}) as a reading, its period from the same table as rate_limit_event.
func claudeUsageReading(usage M, native any) M {
	windows := []contract.LimitWindow{}
	for _, k := range sortedKeys(usage) {
		if k != "five_hour" && k != "seven_day" && !strings.HasPrefix(k, "seven_day_") {
			continue // other usage (extra usage, credits) is not a subscription limit window
		}
		w, ok := usage[k].(M)
		if !ok {
			continue
		}
		u, hasU := num(w["utilization"])
		if !hasU {
			continue
		}
		lw := contract.LimitWindow{Period: claudePeriod(k), Pct: &u}
		if t, ok := str(w["resets_at"]); ok {
			if at, err := time.Parse(time.RFC3339, t); err == nil {
				t := at.UnixMilli()
				lw.ResetsAt = &t
			}
		} else {
			lw.ResetsAt = ms(w["resets_at"])
		}
		if m := strings.TrimPrefix(k, "seven_day_"); m != k {
			lw.Model = &m
		}
		windows = append(windows, lw)
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
