package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func serenaResultText(result M) (string, error) {
	if jAt(result, "isError") == true {
		return "", errors.New("Serena reported tool failure")
	}
	text := ""
	for _, c := range jArr(result, "content") {
		if jStr(c, "type") == "text" {
			text += jStr(c, "text")
		}
	}
	if text == "" {
		return "", errors.New("Serena returned no textual witness")
	}
	return text, nil
}

func serenaSymbolWitness(text string) error {
	value, err := dec(text)
	if err != nil {
		return errors.New("Serena symbol result is not structured navigation evidence (check language-server readiness)")
	}
	for _, names := range jObj(value) {
		for _, name := range jArr(names) {
			if jStr(name) == "CapabilityWitness" {
				return nil
			}
		}
	}
	return errors.New("Serena did not observe the known fixture symbol")
}

// qualifySerena runs no model. It proves the actual selected local MCP tool boundary and retains only safe witnesses.
func qualifySerena(args []string) (resultErr error) {
	if len(args) != 5 {
		return errors.New("qualify-serena requires profile file, temporary fixture root, receipt file, named host account and host registry path")
	}
	raw, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	profile, err := dec(string(raw))
	if err != nil {
		return err
	}
	if err = profileValidate(profile); err != nil {
		return err
	}
	if !has(profileServers(profile, args[1], pharness(jObj(profile)), args[3]), "serena") {
		return errors.New("profile does not select Serena")
	}
	root, err := filepath.Abs(args[1])
	if err != nil {
		return err
	}
	source, err := os.ReadFile(filepath.Join(root, "fixture.go"))
	if err != nil {
		return err
	}
	started := time.Now()
	deps, err := serenaDependencies(args[4], args[3])
	if err != nil {
		return err
	}
	cmd := exec.Command(exe(), "mcp", "serena", root, pharness(jObj(profile)), args[4], args[3])
	cmd.Dir = root
	// The program fixture owns a temporary HOME; never inherit the operator's Serena configuration.
	home, err := os.MkdirTemp("", "fleet-serena-home-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	for _, name := range []string{"SYSTEMROOT", "TEMP", "TMP", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if v, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+v)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	stop, err := ownSerena(cmd)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, stop())
		stdin.Close()
		cmd.Process.Kill()
		stdout.Close()
		cmd.Wait()
	}()
	encoder, decoder := json.NewEncoder(stdin), json.NewDecoder(stdout)
	id := 0
	calls := []string{}
	rpc := func(method string, params M) (M, error) {
		id++
		if err := encoder.Encode(M{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		var answer M
		if err := decoder.Decode(&answer); err != nil {
			return nil, errors.New("MCP response missing; no replay")
		}
		if enc(answer["id"]) != fmt.Sprint(id) || has(answer, "error") {
			return nil, errors.New("MCP request refused or response identity mismatch")
		}
		return jObj(answer, "result"), nil
	}
	if _, err = rpc("initialize", M{"protocolVersion": "2024-11-05", "capabilities": M{}, "clientInfo": M{"name": "fleet-qualification", "version": "1"}}); err != nil {
		return err
	}
	if err = encoder.Encode(M{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return err
	}
	listing, err := rpc("tools/list", M{})
	if err != nil {
		return err
	}
	advertised := []string{}
	for _, tool := range jArr(listing, "tools") {
		advertised = append(advertised, jStr(tool, "name"))
	}
	for _, name := range serenaTools {
		found := false
		for _, actual := range advertised {
			if name == actual {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("required selected Serena tool not advertised: %s", name)
		}
	}
	call := func(name string, a M) (M, error) {
		calls = append(calls, name)
		return rpc("tools/call", M{"name": name, "arguments": a})
	}
	manual, err := call("initial_instructions", M{})
	if err != nil {
		return err
	}
	manualText, err := serenaResultText(manual)
	if err != nil {
		return err
	}
	onboarding, err := call("check_onboarding_performed", M{})
	if err != nil {
		return err
	}
	onboardingText, err := serenaResultText(onboarding)
	if err != nil {
		return err
	}
	setupMs := time.Since(started).Milliseconds()
	workStarted := time.Now()
	symbols, err := call("get_symbols_overview", M{"relative_path": "fixture.go", "depth": 0})
	if err != nil {
		return err
	}
	text, err := serenaResultText(symbols)
	if err != nil {
		return err
	}
	if err := serenaSymbolWitness(text); err != nil {
		return err
	}
	workMs := time.Since(workStarted).Milliseconds()
	versions := M{}
	for _, name := range []string{"go", "gopls"} {
		b, e := exec.Command(jStr(deps, name), "version").Output()
		if e != nil {
			return fmt.Errorf("required executable %s version probe failed", name)
		}
		versions[name] = string(b)
	}
	receipt := M{"kind": "serena-program-qualification", "profileSha256": profileHash(profile), "instructionsSha256": sha([]byte(jStr(profile, "instructions"))), "sourceSha256": sha(source), "manualSha256": sha([]byte(manualText)), "symbolResultSha256": sha([]byte(text)), "advertisedTools": advertised, "actualCalls": calls, "versions": versions, "resolvedPaths": deps, "nativeAccount": args[3], "onboardingStatus": map[bool]string{true: "pending-cache", false: "observed"}[strings.Contains(onboardingText, "not performed")], "symbol": "CapabilityWitness", "symbolObserved": true, "setupStartupMs": setupMs, "workMs": workMs, "modelInvoked": false, "nativeInstructionFollowing": "not tested by program probe", "costUsd": nil, "usageBasis": "local program; no model/provider consumption"}
	if err = os.MkdirAll(filepath.Dir(args[2]), 0755); err != nil {
		return err
	}
	return os.WriteFile(args[2], []byte(enc(receipt)+"\n"), 0600)
}
