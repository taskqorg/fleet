package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// qualifyMCP uses declared fixture calls, the shared stdio boundary and existing execution ownership; no model.
func qualifyMCP(args []string) (resultErr error) {
	totalStarted := time.Now()
	if len(args) != 6 {
		return errors.New("qualify-mcp requires profile, fixture root, receipt, host account, host registry and checks")
	}
	socket := os.Getenv("FLEET_MCP_OWNER")
	if err := privateSocket(socket); err != nil {
		return fmt.Errorf("execution MCP ownership unavailable: %w", err)
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
	root, err := filepath.Abs(args[1])
	if err != nil {
		return err
	}
	raw, err = os.ReadFile(args[5])
	if err != nil {
		return err
	}
	checks, err := dec(string(raw))
	if err != nil {
		return err
	}
	sources := M{}
	for _, value := range jArr(checks, "sources") {
		path := jStr(value)
		if filepath.IsAbs(path) || path == "" {
			return errors.New("MCP fixture source must be relative")
		}
		target, err := filepath.EvalSymlinks(filepath.Join(root, path))
		if err != nil {
			return err
		}
		base, err := filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("MCP fixture source escapes root")
		}
		source, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		sources[path] = sha(source)
	}

	home, err := os.MkdirTemp("", "fleet-mcp-home-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	// This dedicated program process owns its temporary HOME; no account/personal MCP configuration is imported.
	oldHome, hadHome := os.LookupEnv("HOME")
	defer func() {
		if hadHome {
			os.Setenv("HOME", oldHome)
		} else {
			os.Unsetenv("HOME")
		}
	}()
	os.Setenv("HOME", home)
	evidence := []any{}
	var setupMs, workMs int64
	for _, grant := range jArr(profile, "mcpServers") {
		name := jStr(grant, "id")
		bindingStarted := time.Now()
		binding, err := namedMCPBinding(args[4], args[3], name, root)
		if err != nil {
			return err
		}
		hashes := M{}
		hash, err := mcpFileHash(jStr(binding, "command"))
		if err != nil {
			return err
		}
		hashes["command"] = hash
		for name, path := range jObj(binding, "dependencies") {
			hash, err := mcpFileHash(jStr(path))
			if err != nil {
				return err
			}
			hashes[name] = hash
		}
		binding["observedExecutableHashes"] = hashes
		setupMs += time.Since(bindingStarted).Milliseconds()
		binding["tools"] = jAt(grant, "data", "tools")
		calls := jArr(checks, "servers", name)
		if len(calls) == 0 {
			return fmt.Errorf("MCP %s has no declared qualification calls", name)
		}
		inReader, inWriter, err := os.Pipe()
		if err != nil {
			return err
		}
		outReader, outWriter, err := os.Pipe()
		if err != nil {
			inReader.Close()
			inWriter.Close()
			return err
		}
		done := make(chan error, 1)
		go func() {
			err := mcpStdio(socket, binding, inReader, outWriter, root, jStr(binding, "pathPolicy"))
			outWriter.Close()
			done <- err
		}()
		err = qualifyMCPServer(name, binding, calls, inWriter, outReader, &evidence, &setupMs, &workMs)
		inWriter.Close()
		outReader.Close()
		serverErr := <-done
		if err != nil || serverErr != nil {
			return errors.Join(err, serverErr)
		}
	}
	if len(evidence) == 0 {
		return errors.New("no selected MCP qualification")
	}
	runnerHash, err := mcpFileHash(exe())
	if err != nil {
		return err
	}
	receipt := M{"executorSha256": runnerHash, "sessionId": os.Getenv("FLEET_SESSION_ID"), "kind": "mcp-program-qualification", "profileSha256": profileHash(profile), "instructionsSha256": sha([]byte(jStr(profile, "instructions"))), "sourceSha256": profileHash(sources), "sources": sources, "checksSha256": sha(raw), "nativeAccount": args[3], "calls": evidence, "setupStartupMs": setupMs, "workMs": workMs, "totalMs": time.Since(totalStarted).Milliseconds(), "timingBasis": "setupStartupMs includes binding identity/init/discovery/startup calls; workMs includes declared work calls; totalMs includes preparation/teardown", "modelInvoked": false, "nativeInstructionFollowing": "not tested by program probe", "costUsd": nil, "usageBasis": "local program; no model/provider consumption", "transport": "stdio", "platform": platform()}
	if err = os.MkdirAll(filepath.Dir(args[2]), 0755); err != nil {
		return err
	}
	return os.WriteFile(args[2], []byte(enc(receipt)+"\n"), 0600)
}
func qualifyMCPServer(name string, binding M, calls []any, input io.Writer, output io.Reader, evidence *[]any, setupMs, workMs *int64) error {
	encoder, decoder := json.NewEncoder(input), json.NewDecoder(output)
	id := 0
	started := time.Now()
	rpc := func(method string, params M) (M, error) {
		id++
		if err := encoder.Encode(M{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		var answer M
		if err := decoder.Decode(&answer); err != nil {
			return nil, errors.New("MCP response unavailable; no replay")
		}
		if enc(answer["id"]) != fmt.Sprint(id) || has(answer, "error") {
			return nil, errors.New("MCP call refused or response identity differs")
		}
		return jObj(answer, "result"), nil
	}
	initialization, err := rpc("initialize", M{"protocolVersion": "2024-11-05", "capabilities": M{}, "clientInfo": M{"name": "fleet-program-qualification", "version": "1"}})
	if err != nil {
		return err
	}
	if err := encoder.Encode(M{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return err
	}
	listing, err := rpc("tools/list", M{})
	if err != nil {
		return err
	}
	*setupMs += time.Since(started).Milliseconds()
	for _, call := range calls {
		tool := jStr(call, "tool")
		start := time.Now()
		result, err := rpc("tools/call", M{"name": tool, "arguments": jObj(call, "arguments")})
		if err != nil {
			return err
		}
		text, err := mcpResultText(result)
		if err != nil {
			return err
		}
		switch jStr(call, "oracle") {
		case "nonempty":
		case "onboarding-status":
		case "go-symbol":
			if err = serenaSymbolWitness(text); err != nil {
				return err
			}
		case "text-contains":
			if !strings.Contains(text, jStr(call, "expected")) || jStr(call, "expected") == "" {
				return errors.New("MCP fixture semantic witness missing")
			}
		case "text-equals":
			if text != jStr(call, "expected") {
				return errors.New("MCP fixture result differs")
			}
		default:
			return errors.New("MCP declared oracle unsupported")
		}
		elapsed := time.Since(start).Milliseconds()
		if jStr(call, "phase") == "startup" {
			*setupMs += elapsed
		} else {
			*workMs += elapsed
		}
		observation := ""
		if jStr(call, "oracle") == "onboarding-status" {
			observation = "unknown"
			if !strings.HasPrefix(strings.TrimSpace(text), "Error") {
				observation = "observed"
			}
			if strings.Contains(text, "not performed") {
				observation = "pending-cache"
			}
		}
		*evidence = append(*evidence, M{"observation": observation, "server": name, "tool": tool, "resultSha256": sha([]byte(text)), "advertisedToolsSha256": profileHash(listing), "serverInfo": M{"name": jAt(initialization, "serverInfo", "name"), "version": jAt(initialization, "serverInfo", "version")}, "resolvedCommand": binding["command"], "dependencyPaths": binding["dependencies"], "executableHashes": binding["observedExecutableHashes"], "ownedDependencies": binding["ownedDependencies"], "passed": true, "oracle": jAt(call, "oracle"), "phase": jAt(call, "phase"), "durationMs": elapsed})
	}
	return nil
}
