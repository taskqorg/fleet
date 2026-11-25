package main

// project-env (docs/configuration.md#project-environment): the project's dev environment as a rootless Podman pod,
// run by this MCP server outside the agent's native sandbox. The worktree is mounted in the main container; services
// share the pod network and resolve by name to 127.0.0.1. One pod per workspace session, created on the first call,
// removed by env_reset and by teardown. No production data: only what the spec and the worktree hold.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fleet/contract"
)

// projectEnvConfig: what the launch hands the server (one JSON argument).
type projectEnvConfig struct {
	Pod      string                         `json:"pod"`
	Project  string                         `json:"project"`
	Worktree string                         `json:"worktree"`
	Spec     contract.ProjectDevEnvironment `json:"spec"`
	Home     string                         `json:"home"`
	Runtime  string                         `json:"runtime"`
	Path     string                         `json:"path"`
}

var projectEnvTools = []any{
	M{
		"name":        "env_exec",
		"description": "Run a shell command (sh -lc) in the project's dev environment container, with the worktree mounted at its workdir and the project's services (for example a database) reachable by name. The first call starts the environment and runs its setup. Answers exitCode and output (the last 64 KiB).",
		"inputSchema": M{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"command"},
			"properties": M{
				"command":        M{"type": "string"},
				"timeoutSeconds": M{"type": "integer", "minimum": 1, "maximum": 3600, "description": "Default 600."},
			},
		},
	},
	M{
		"name":        "env_reset",
		"description": "Remove the project's dev environment (containers and their data, never the worktree). The next env_exec starts it fresh and runs setup again.",
		"inputSchema": M{"type": "object", "additionalProperties": false, "properties": M{}},
	},
}

// projectEnvPod: the pod name of a workspace session (podman names: short, lowercase).
func projectEnvPod(workspace string) string {
	sum := sha256.Sum256([]byte(workspace))
	return fmt.Sprintf("fleet-env-%x", sum[:8])
}

// podmanEnv: the executor user's own HOME and runtime dir, so rootless storage is the user's, never a harness HOME.
func podmanEnv(home, runtime, path string) []string {
	env := []string{"HOME=" + home, "PATH=" + path, "TMPDIR=" + os.TempDir()}
	if runtime != "" {
		env = append(env, "XDG_RUNTIME_DIR="+runtime)
	}
	return env
}

func (c projectEnvConfig) podman(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Env = podmanEnv(c.Home, c.Runtime, c.Path)
	cmd.Dir = c.Home
	return cmd
}

func (c projectEnvConfig) run(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := c.podman(ctx, args...).CombinedOutput()
	if ctx.Err() != nil {
		return envTail(out), fmt.Errorf("podman %s: timed out after %s", args[0], timeout)
	}
	if err != nil {
		return envTail(out), fmt.Errorf("podman %s: %v: %s", args[0], err, strings.TrimSpace(tail(out)))
	}
	return string(out), nil
}

const envOutputMax = 64 << 10

func envTail(b []byte) string {
	if len(b) > envOutputMax {
		b = b[len(b)-envOutputMax:]
	}
	return string(b)
}

func (c projectEnvConfig) workdir() string {
	if w := str0(c.Spec.Workdir); w != "" {
		return w
	}
	return "/workspace"
}

// image: the spec's image, or the build file's image tagged by the file's hash and built once per host.
func (c projectEnvConfig) image() (string, error) {
	if img := str0(c.Spec.Image); img != "" {
		return img, nil
	}
	build := str0(c.Spec.Build)
	file := filepath.Join(c.Worktree, build)
	if !filepath.IsLocal(build) {
		return "", errors.New("build must be a path inside the worktree")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	tag := fmt.Sprintf("localhost/fleet-env-%s:%x", strings.ToLower(c.Project), sum[:6])
	if _, err := c.run(time.Minute, "image", "exists", tag); err == nil {
		return tag, nil
	}
	if _, err := c.run(45*time.Minute, "build", "-t", tag, "-f", file, c.Worktree); err != nil {
		return "", err
	}
	return tag, nil
}

func envArgs(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []string{}
	for _, k := range keys {
		out = append(out, "-e", k+"="+env[k])
	}
	return out
}

// ensure starts the pod when its main container is not running; the answer carries setup output on a fresh start.
func (c projectEnvConfig) ensure() (string, error) {
	main := c.Pod + "-main"
	if out, err := c.run(time.Minute, "container", "inspect", "-f", "{{.State.Running}}", main); err == nil && strings.TrimSpace(out) == "true" {
		return "", nil
	}
	_, _ = c.run(2*time.Minute, "pod", "rm", "-f", "--ignore", c.Pod)
	img, err := c.image()
	if err != nil {
		return "", err
	}
	// keep-id on the pod (podman refuses it per container in a pod): files the main container writes stay the executor user's.
	create := []string{"pod", "create", "--name", c.Pod, "--userns", "keep-id", "--label", "fleet.env=1", "--label", "fleet.project=" + c.Project}
	for _, s := range c.Spec.Services {
		create = append(create, "--add-host", s.Name+":127.0.0.1")
	}
	if _, err := c.run(2*time.Minute, create...); err != nil {
		return "", err
	}
	for _, s := range c.Spec.Services {
		args := append([]string{"run", "-d", "--pod", c.Pod, "--name", c.Pod + "-" + s.Name}, envArgs(s.Env)...)
		if _, err := c.run(15*time.Minute, append(args, s.Image)...); err != nil {
			return "", err
		}
	}
	args := []string{"run", "-d", "--pod", c.Pod, "--name", main, "-v", c.Worktree + ":" + c.workdir(), "-w", c.workdir(), "--entrypoint", "sleep"}
	args = append(args, envArgs(c.Spec.Env)...)
	if _, err := c.run(15*time.Minute, append(args, img, "infinity")...); err != nil {
		return "", err
	}
	if setup := str0(c.Spec.Setup); setup != "" {
		code, out, err := c.exec(setup, 30*time.Minute)
		if err != nil {
			return out, err
		}
		if code != 0 {
			return out, fmt.Errorf("setup exited %d", code)
		}
		return out, nil
	}
	return "", nil
}

func (c projectEnvConfig) exec(command string, timeout time.Duration) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out bytes.Buffer
	cmd := c.podman(ctx, "exec", "-w", c.workdir(), c.Pod+"-main", "sh", "-lc", command)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return -1, envTail(out.Bytes()), fmt.Errorf("timed out after %s", timeout)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), envTail(out.Bytes()), nil
	}
	if err != nil {
		return -1, envTail(out.Bytes()), err
	}
	return 0, envTail(out.Bytes()), nil
}

func projectEnvCall(name string, args any, cfg projectEnvConfig) (any, error) {
	a, _ := args.(M)
	switch name {
	case "env_exec":
		command, _ := a["command"].(string)
		if strings.TrimSpace(command) == "" {
			return nil, errors.New("command required")
		}
		timeout := 600 * time.Second
		if t, ok := a["timeoutSeconds"].(float64); ok && t >= 1 && t <= 3600 {
			timeout = time.Duration(t) * time.Second
		}
		setup, err := cfg.ensure()
		if err != nil {
			return mcpText(M{"error": "environment did not start: " + err.Error(), "setupOutput": setup}, &yes), nil
		}
		code, out, err := cfg.exec(command, timeout)
		r := M{"exitCode": code, "output": out}
		if setup != "" {
			r["setupOutput"] = setup
		}
		if err != nil {
			r["error"] = err.Error()
			return mcpText(r, &yes), nil
		}
		return mcpText(r, nil), nil
	case "env_reset":
		if _, err := cfg.run(5*time.Minute, "pod", "rm", "-f", "--ignore", cfg.Pod); err != nil {
			return mcpText(M{"error": err.Error()}, &yes), nil
		}
		return mcpText(M{"reset": true}, nil), nil
	}
	return nil, errors.New("Unsupported tool or arguments")
}

// projectEnvServer: the launch's project-env server spec, when the project declares an environment.
func projectEnvServer(project string, spec *contract.ProjectDevEnvironment, workspace, worktree string) M {
	cfg := projectEnvConfig{Pod: projectEnvPod(workspace), Project: project, Worktree: worktree, Spec: *spec,
		Home: os.Getenv("HOME"), Runtime: os.Getenv("XDG_RUNTIME_DIR"), Path: os.Getenv("PATH")}
	return M{"command": exe(), "args": []any{"mcp", "project-env", enc(cfg)}, "tools": []any{"env_exec", "env_reset"}}
}

// removeProjectEnv: teardown's step; a host without podman has no pod to remove.
func removeProjectEnv(workspace string) {
	if _, err := exec.LookPath("podman"); err != nil {
		return
	}
	c := projectEnvConfig{Pod: projectEnvPod(workspace), Home: os.Getenv("HOME"), Runtime: os.Getenv("XDG_RUNTIME_DIR"), Path: os.Getenv("PATH")}
	_, _ = c.run(5*time.Minute, "pod", "rm", "-f", "--ignore", c.Pod)
}
