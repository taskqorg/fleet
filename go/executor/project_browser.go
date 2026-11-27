package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"fleet/contract"
	"fleet/errtrack"
)

//go:embed project_browser.cjs
var projectBrowserScript string

func browserCaptureDir(root, execution string) string {
	return filepath.Join(runTempDir(root, execution), "browser-captures")
}

// browserBinding describes a local preview grant. The legacy Access path remains on the
// file denylist until hosts remove their obsolete production entries.
type browserBinding struct {
	PreviewOrigins jsonValue `json:"previewOrigins"`
	Production     struct {
		AccessFile string `json:"accessFile"`
	} `json:"production"`
}

type browserAuthority struct {
	Config M                           `json:"config"`
	Node   string                      `json:"node"`
	Access contract.BrowserAccessReply `json:"access"`
}

func browserProject(payload any) string {
	return typed[struct {
		ProjectId string `json:"projectId"`
	}](payload).ProjectId
}

func browserRuntime() bool {
	return isFile(os.Getenv("FLEET_PLAYWRIGHT_MODULE")) && isFile(os.Getenv("FLEET_BROWSER_EXECUTABLE"))
}

func browserConfig(payload any, production bool) (M, error) {
	projectID := browserProject(payload)
	if projectID == "" || (production && projectID != contract.ProductionBrowserProject) {
		return nil, errors.New("browser-project-not-granted")
	}
	var binding browserBinding
	path := os.Getenv("FLEET_BROWSER_BINDINGS")
	if path != "" {
		binding = readBrowserBinding(path, projectID)
	}
	previews, err := previewOrigins(binding)
	if err != nil && !production {
		return nil, err
	}
	if len(previews) == 0 && !production {
		return nil, errors.New("browser-project-not-granted")
	}
	if !browserRuntime() {
		return nil, errors.New("browser-runtime-unready")
	}
	config := M{"module": os.Getenv("FLEET_PLAYWRIGHT_MODULE"), "executable": os.Getenv("FLEET_BROWSER_EXECUTABLE"), "ownedExecutor": exe(), "previewOrigins": previews, "timeoutMs": probeWait.Milliseconds()}
	if production {
		config["production"] = M{"origin": contract.ProductionBrowserOrigin}
	}
	return config, nil
}

func previewOrigins(binding browserBinding) ([]any, error) {
	previews, isList := binding.PreviewOrigins.V.([]any)
	invalid := errors.New("browser-preview-grant-invalid")
	if binding.PreviewOrigins.Set && !isList {
		return nil, invalid
	}
	for _, value := range previews {
		origin, _ := value.(string)
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, invalid
		}
	}
	return previews, nil
}

func readBrowserBinding(path, projectID string) browserBinding {
	if private(path) != nil {
		return browserBinding{}
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || owner(st) != os.Geteuid() {
		return browserBinding{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return browserBinding{}
	}
	return typed[map[string]browserBinding](json.RawMessage(raw))[projectID]
}

// browserPipe sends only a small host-authored JSON value over an anonymous descriptor.
func browserPipe(value any) (*os.File, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("browser authority unavailable")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errtrack.Go("browser.authority", func() {
		defer w.Close()
		_, _ = w.Write(data)
	})
	return r, nil
}

func browserCommand(config any, home string, check bool) *exec.Cmd {
	return browserNodeCommand("node", config, home, check)
}

func browserNodeCommand(node string, config any, home string, check bool) *exec.Cmd {
	args := []string{"-e", projectBrowserScript, enc(config)}
	if check {
		args = append(args, "check")
	}
	c := exec.Command(node, args...)
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + os.TempDir()}
	for _, key := range []string{"FLEET_MCP_OWNER", "FLEET_MCP_GENERATION"} {
		if value := os.Getenv(key); value != "" {
			c.Env = append(c.Env, key+"="+value)
		}
	}
	return c
}

func serveProjectBrowser(arg string) error {
	var cmd *exec.Cmd
	if arg == "managed" {
		answer, connection, err := mcpOwnerCall(os.Getenv("FLEET_MCP_OWNER"), M{
			"action": "browser-authority", "generation": os.Getenv("FLEET_MCP_GENERATION"),
		})
		if err != nil {
			return errors.New("production browser authority is ungranted")
		}
		connection.Close()
		authority := typed[browserAuthority](answer["authority"])
		pipe, err := browserPipe(authority.Access)
		if err != nil {
			return err
		}
		defer pipe.Close()
		cmd = browserNodeCommand(authority.Node, authority.Config, os.Getenv("HOME"), false)
		cmd.ExtraFiles = []*os.File{pipe}
	} else {
		cfg, err := dec(arg)
		if err != nil {
			return err
		}
		cmd = browserCommand(cfg, os.Getenv("HOME"), false)
	}
	cmd.Stdin, cmd.Stdout = os.Stdin, os.Stdout
	return cmd.Run()
}

func browserProbe(config M, home string, authority *browserAuthority) error {
	cmd := browserCommand(config, home, true)
	if authority != nil {
		cmd = browserNodeCommand(authority.Node, config, home, true)
		r, err := browserPipe(authority.Access)
		if err != nil {
			return err
		}
		defer r.Close()
		cmd.ExtraFiles = []*os.File{r}
	}
	if _, err := boundedOutput(cmd, probeWait); err != nil {
		return errors.New("browser-check-failed")
	}
	return nil
}

func (e *executor) browserAccess(ctx context.Context) (contract.BrowserAccessReply, int) {
	var access contract.BrowserAccessReply
	base, err := url.Parse(e.url)
	if err != nil || (base.Scheme != "https" && !(base.Scheme == "http" && (base.Hostname() == "127.0.0.1" || base.Hostname() == "::1" || base.Hostname() == "localhost"))) {
		return access, 0
	}
	resp, err := postJSONCtx(ctx, outboxClient, e.url+contract.RouteBrowserAccess.Path, e.token, contract.BrowserAccessRequest{ProjectId: contract.ProductionBrowserProject})
	if err != nil {
		return access, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return access, resp.StatusCode
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&access) != nil {
		return contract.BrowserAccessReply{}, 0
	}
	if access.ClientId == "" && access.ClientSecret == "" {
		return access, http.StatusNoContent
	}
	if access.ClientId == "" || access.ClientSecret == "" {
		return contract.BrowserAccessReply{}, 0
	}
	return access, http.StatusOK
}

func newBrowserAuthority(payload any, access contract.BrowserAccessReply) (*browserAuthority, error) {
	config, err := browserConfig(payload, true)
	if err != nil {
		return nil, err
	}
	executable, err := trustedMCPExecutable(os.Getenv("FLEET_BROWSER_EXECUTABLE"))
	if err != nil {
		return nil, errors.New("browser-runtime-unready")
	}
	module := os.Getenv("FLEET_PLAYWRIGHT_MODULE")
	if !filepath.IsAbs(module) {
		return nil, errors.New("browser-runtime-unready")
	}
	config["executable"] = executable
	node, err := exec.LookPath("node")
	if err == nil {
		node, err = trustedMCPExecutable(node)
	}
	if err != nil {
		return nil, errors.New("browser-runtime-unready")
	}
	return &browserAuthority{Config: config, Node: node, Access: access}, nil
}

// prepareBrowser checks the same runtime used by MCP. An independently granted preview
// can still start while the production probe is failing.
func (e *executor) prepareBrowser(payload any, home string) (M, *browserAuthority, error) {
	var productionErr error
	if browserProject(payload) == contract.ProductionBrowserProject {
		access, status := e.browserAccess(e.baseCtx())
		if status != http.StatusOK {
			productionErr = errors.New("browser-production-authority-unavailable")
		} else if authority, err := newBrowserAuthority(payload, access); err != nil {
			productionErr = err
		} else if err := browserProbe(authority.Config, home, authority); err != nil {
			productionErr = errors.New("browser-production-identity-unready")
		} else {
			return authority.Config, authority, nil
		}
	}
	config, err := browserConfig(payload, false)
	if err != nil {
		if productionErr != nil {
			return nil, nil, productionErr
		}
		return nil, nil, err
	}
	return config, nil, browserProbe(config, home, nil)
}

const browserReadinessInterval = 10 * time.Minute

type productionBrowserHealth struct {
	sync.Mutex
	last   time.Time
	busy   bool
	detail string
	ready  bool
}

// productionBrowserStatus samples readiness in the background, independently of launches.
func (e *executor) productionBrowserStatus() (*bool, []contract.EdgeProblem) {
	h := &e.browserHealth
	h.Lock()
	defer h.Unlock()
	if !h.busy && time.Since(h.last) >= browserReadinessInterval {
		h.busy = true
		errtrack.Go("browser.production.check", func() {
			ready, detail := e.probeProductionBrowser()
			h.Lock()
			h.ready, h.detail, h.last, h.busy = ready, detail, time.Now(), false
			h.Unlock()
		})
	}
	// Keep the last observation while one refresh can still finish within its own deadlines.
	if time.Since(h.last) > browserReadinessInterval+outboxClient.Timeout+probeWait {
		return nil, nil
	}
	ready := h.ready
	if h.detail == "" {
		return &ready, nil
	}
	return &ready, []contract.EdgeProblem{{Kind: contract.ProblemKindBrowserProductionUnready, Detail: h.detail}}
}

func (e *executor) probeProductionBrowser() (bool, string) {
	access, status := e.browserAccess(e.baseCtx())
	switch status {
	case http.StatusNoContent:
		return false, "" // the host has no production browser grant
	case http.StatusServiceUnavailable:
		return false, "Coordinator production browser credential is missing or invalid"
	case http.StatusOK:
	default:
		return false, "Production browser credential could not be retrieved"
	}
	authority, err := newBrowserAuthority(M{"projectId": contract.ProductionBrowserProject}, access)
	if err != nil {
		return false, "Production browser runtime or project configuration is unavailable"
	}
	home, err := os.MkdirTemp("", "fleet-browser-check-")
	if err != nil {
		return false, "Production browser probe could not prepare an isolated home"
	}
	defer os.RemoveAll(home)
	if browserProbe(authority.Config, home, authority) != nil {
		return false, "Production browser failed the site-agents identity check"
	}
	return true, ""
}
