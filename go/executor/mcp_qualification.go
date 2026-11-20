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

	"fleet/contract"
	"fleet/errtrack"
)

// qualificationChecks: qualify-mcp's declared checks (argument 6): fixture sources and each server's calls.
type qualificationChecks struct {
	Sources []string                       `json:"sources"`
	Servers map[string][]qualificationCall `json:"servers"`
}

// qualificationCall: one declared fixture call and its oracle.
type qualificationCall struct {
	Tool      string    `json:"tool"`
	Arguments jsonValue `json:"arguments"`
	Oracle    jsonValue `json:"oracle"` // a string names the oracle; the evidence keeps the value as declared
	Expected  string    `json:"expected"`
	Phase     jsonValue `json:"phase"`
}

// mcpBindingView: the members of a named MCP binding (namedMCPBinding) qualification reads.
type mcpBindingView struct {
	Command      string            `json:"command"`
	Dependencies map[string]string `json:"dependencies"`
	PathPolicy   string            `json:"pathPolicy"`
}

// qualifyMCP uses declared fixture calls, the shared stdio boundary and existing execution ownership; no model.
func qualifyMCP(args []string) (resultErr error) {
	totalStarted := time.Now()

	evidence := []M{}
	diagnostic := M{"phase": "arguments", "requestOrdinal": 0, "method": "none"}
	var setupMs, workMs int64
	defer func() {
		if resultErr == nil {
			return
		}
		target := ""
		if dir := os.Getenv("FLEET_OUTPUT_DIR"); dir != "" {
			target = filepath.Join(dir, "capability-receipt.json")
		} else if len(args) == 6 {
			target = args[2]
		}
		if target == "" {
			return
		}
		completed := []any{}
		for _, value := range evidence {
			row := M{}
			for _, key := range []string{"server", "tool", "resultSha256", "advertisedToolsSha256", "executableHashes", "passed", "oracle", "phase", "durationMs"} {
				if v := value[key]; v != nil {
					row[key] = v
				}
			}
			completed = append(completed, row)
		}
		diagnostic["lifecycle"] = safeMCPStatus(nil)
		var lifecycle *mcpLifecycleError
		if errors.As(resultErr, &lifecycle) {
			diagnostic["lifecycle"] = lifecycle.status
		}
		receipt := M{"kind": "mcp-program-qualification", "status": "failed", "diagnostic": diagnostic, "sessionId": os.Getenv("FLEET_SESSION_ID"), "executionId": os.Getenv("FLEET_EXECUTION_ID"), "calls": completed, "setupStartupMs": setupMs, "workMs": workMs, "totalMs": time.Since(totalStarted).Milliseconds(), "timingBasis": "partial measurements only; no inferred completion or cleanup duration", "modelInvoked": false, "costUsd": nil, "platform": platform()}
		if hash, err := mcpFileHash(exe()); err == nil {
			receipt["executorSha256"] = hash
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			resultErr = errors.Join(resultErr, errors.New("qualification diagnostic retention failed"))
			return
		}
		if err := os.WriteFile(target, []byte(enc(receipt)+"\n"), 0600); err != nil {
			resultErr = errors.Join(resultErr, errors.New("qualification diagnostic retention failed"))
		}
	}()
	if len(args) != 6 {
		return errors.New("qualify-mcp requires profile, fixture root, receipt, host account, host registry and checks")
	}
	diagnostic["phase"] = "ownership"
	socket := os.Getenv("FLEET_MCP_OWNER")
	if err := privateSocket(socket); err != nil {
		return fmt.Errorf("execution MCP ownership unavailable: %w", err)
	}
	diagnostic["phase"] = "profile"
	raw, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	profile, err := dec(string(raw)) // hashed as written
	if err != nil {
		return err
	}
	parsed, err := contract.ParseProfile([]byte(enc(profile))) // profileValidate's check
	if err != nil {
		return err
	}
	diagnostic["profileSha256"] = profileHash(profile)
	diagnostic["instructionsSha256"] = sha([]byte(parsed.Instructions))
	diagnostic["phase"] = "checks"
	root, err := filepath.Abs(args[1])
	if err != nil {
		return err
	}
	raw, err = os.ReadFile(args[5])
	if err != nil {
		return err
	}
	declared, err := dec(string(raw))
	if err != nil {
		return err
	}
	checks := typed[qualificationChecks](declared)
	diagnostic["checksSha256"] = sha(raw)
	diagnostic["phase"] = "sources"
	sources := M{}
	for _, path := range checks.Sources {
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

	diagnostic["sourceSha256"] = profileHash(sources)
	diagnostic["phase"] = "home"
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
	for _, grant := range parsed.McpServers {
		name := grant.Id
		diagnostic["phase"] = "binding"
		diagnostic["server"] = name
		bindingStarted := time.Now()
		binding, err := namedMCPBinding(args[4], args[3], name, root)
		if err != nil {
			return err
		}
		view := typed[mcpBindingView](binding)
		hashes := M{}
		hash, err := mcpFileHash(view.Command)
		if err != nil {
			return err
		}
		hashes["command"] = hash
		for name, path := range view.Dependencies {
			hash, err := mcpFileHash(path)
			if err != nil {
				return err
			}
			hashes[name] = hash
		}
		binding["observedExecutableHashes"] = hashes
		diagnostic["bindingSha256"] = profileHash(binding)
		setupMs += time.Since(bindingStarted).Milliseconds()
		binding["tools"] = decRaw(grant.Data["tools"])
		calls := checks.Servers[name]
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
		errtrack.Go("qualify.serve", func() {
			err := mcpStdio(socket, binding, inReader, outWriter, root, view.PathPolicy)
			outWriter.Close()
			done <- err
		})
		err = qualifyMCPServer(name, binding, calls, inWriter, outReader, &evidence, &setupMs, &workMs, diagnostic)
		cleanupStarted := time.Now()
		inWriter.Close()
		outReader.Close()
		serverErr := <-done
		diagnostic["stdioCloseMs"] = time.Since(cleanupStarted).Milliseconds()
		if err != nil || serverErr != nil {
			return errors.Join(err, serverErr)
		}
	}
	diagnostic["phase"] = "receipt"
	if len(evidence) == 0 {
		return errors.New("no selected MCP qualification")
	}
	runnerHash, err := mcpFileHash(exe())
	if err != nil {
		return err
	}
	receipt := M{"executorSha256": runnerHash, "sessionId": os.Getenv("FLEET_SESSION_ID"), "kind": "mcp-program-qualification", "profileSha256": profileHash(profile), "instructionsSha256": sha([]byte(parsed.Instructions)), "sourceSha256": profileHash(sources), "sources": sources, "checksSha256": sha(raw), "nativeAccount": args[3], "calls": evidence, "setupStartupMs": setupMs, "workMs": workMs, "totalMs": time.Since(totalStarted).Milliseconds(), "timingBasis": "setupStartupMs includes binding identity/init/discovery/startup calls; workMs includes declared work calls; totalMs includes preparation/teardown", "modelInvoked": false, "nativeInstructionFollowing": "not tested by program probe", "costUsd": nil, "usageBasis": "local program; no model/provider consumption", "transport": "stdio", "platform": platform()}
	if err = os.MkdirAll(filepath.Dir(args[2]), 0755); err != nil {
		return err
	}
	return os.WriteFile(args[2], []byte(enc(receipt)+"\n"), 0600)
}
func qualifyMCPServer(name string, binding M, calls []qualificationCall, input io.Writer, output io.Reader, evidence *[]M, setupMs, workMs *int64, diagnostic M) error {
	encoder, decoder := json.NewEncoder(input), json.NewDecoder(output)
	id := 0
	started := time.Now()
	decoder.UseNumber()
	// rpc answers the result of one request, as written (numbers as json.Number).
	rpc := func(method string, params M) (any, error) {
		requestStarted := time.Now()
		defer func() { diagnostic["requestDurationMs"] = time.Since(requestStarted).Milliseconds() }()
		id++
		diagnostic["phase"], diagnostic["requestOrdinal"], diagnostic["method"] = "rpc", id, method
		if err := encoder.Encode(M{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		var frame any
		if err := decoder.Decode(&frame); err != nil {
			return nil, errors.New("MCP response unavailable; no replay")
		}
		if _, ok := frame.(M); !ok {
			return nil, errors.New("MCP response unavailable; no replay")
		}
		answer := typed[struct {
			ID     *float64  `json:"id"`
			Error  jsonValue `json:"error"`
			Result jsonValue `json:"result"`
		}](frame)
		if answer.ID == nil || *answer.ID != float64(id) || answer.Error.Set {
			return nil, errors.New("MCP call refused or response identity differs")
		}
		return answer.Result.V, nil
	}
	initialization, err := rpc("initialize", M{"protocolVersion": "2024-11-05", "capabilities": M{}, "clientInfo": M{"name": "fleet-program-qualification", "version": "1"}})
	if err != nil {
		return err
	}
	if err := encoder.Encode(M{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return err
	}
	listed, err := rpc("tools/list", M{})
	if err != nil {
		return err
	}
	var listing M // hashed as decoded with float numbers; a result that is no object hashes as null
	json.Unmarshal([]byte(enc(listed)), &listing)
	server := typed[struct {
		ServerInfo struct {
			Name    jsonValue `json:"name"`
			Version jsonValue `json:"version"`
		} `json:"serverInfo"`
	}](initialization)
	*setupMs += time.Since(started).Milliseconds()
	for _, call := range calls {
		tool := call.Tool
		oracle, _ := valStr(call.Oracle)
		start := time.Now()
		var arguments any // an object, else null
		if a, ok := call.Arguments.V.(M); ok {
			arguments = a
		}
		result, err := rpc("tools/call", M{"name": tool, "arguments": arguments})
		if err != nil {
			return err
		}
		diagnostic["phase"] = "oracle"
		text, err := mcpResultText(result)
		if err != nil {
			return err
		}
		switch oracle {
		case "nonempty":
		case "onboarding-status":
		case "go-symbol":
			if err = serenaSymbolWitness(text); err != nil {
				return err
			}
		case "text-contains":
			if !strings.Contains(text, call.Expected) || call.Expected == "" {
				return errors.New("MCP fixture semantic witness missing")
			}
		case "text-equals":
			if text != call.Expected {
				return errors.New("MCP fixture result differs")
			}
		default:
			return errors.New("MCP declared oracle unsupported")
		}
		elapsed := time.Since(start).Milliseconds()
		if phase, _ := valStr(call.Phase); phase == "startup" {
			*setupMs += elapsed
		} else {
			*workMs += elapsed
		}
		observation := ""
		if oracle == "onboarding-status" {
			observation = "unknown"
			if !strings.HasPrefix(strings.TrimSpace(text), "Error") {
				observation = "observed"
			}
			if strings.Contains(text, "not performed") {
				observation = "pending-cache"
			}
		}
		*evidence = append(*evidence, M{"observation": observation, "server": name, "tool": tool, "resultSha256": sha([]byte(text)), "advertisedToolsSha256": profileHash(listing), "serverInfo": M{"name": server.ServerInfo.Name.V, "version": server.ServerInfo.Version.V}, "resolvedCommand": binding["command"], "dependencyPaths": binding["dependencies"], "executableHashes": binding["observedExecutableHashes"], "ownedDependencies": binding["ownedDependencies"], "passed": true, "oracle": call.Oracle.V, "phase": call.Phase.V, "durationMs": elapsed})
	}
	return nil
}
