package main

import (
	_ "embed"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed project_browser.cjs
var projectBrowserScript string

// browserBinding: one project's entry of FLEET_BROWSER_BINDINGS.
type browserBinding struct {
	PreviewOrigins jsonValue `json:"previewOrigins"`
	Production     jsonValue `json:"production"`
}

// browserProduction: a binding's production grant; its other keys reach the browser script unchanged.
type browserProduction struct {
	Origin     string `json:"origin"`
	AccessFile string `json:"accessFile"`
}

func browserConfig(payload any) (M, error) {
	projectID := typed[struct {
		ProjectId string `json:"projectId"`
	}](payload).ProjectId
	if projectID == "" {
		return nil, errors.New("browser-project-not-granted")
	}
	path := os.Getenv("FLEET_BROWSER_BINDINGS")
	if !filepath.IsAbs(path) || private(path) != nil {
		return nil, errors.New("browser-binding-unready")
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || owner(st) != os.Geteuid() {
		return nil, errors.New("browser-binding-unready")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	all, err := dec(string(raw))
	if err != nil {
		return nil, err
	}
	// The coordinator supplies identity separately from normalized project settings.
	bindings, _ := all.(M)
	entry, ok := bindings[projectID].(M)
	if !ok {
		return nil, errors.New("browser-project-not-granted")
	}
	binding := typed[browserBinding](entry)
	previews, isList := binding.PreviewOrigins.V.([]any)
	if binding.PreviewOrigins.Set && !isList {
		return nil, errors.New("browser-preview-grant-invalid")
	}
	for _, value := range previews {
		origin, _ := value.(string)
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("browser-preview-grant-invalid")
		}
	}
	var production any // the grant as written, for the browser script; nil unless an object
	if p, ok := binding.Production.V.(M); ok {
		production = p
		grant := typed[browserProduction](p)
		if grant.Origin != "https://fleet.ufobe.com" {
			return nil, errors.New("browser-production-origin-denied")
		}
		credential := grant.AccessFile
		credentialInfo, statErr := os.Stat(credential)
		if !filepath.IsAbs(credential) || private(credential) != nil || statErr != nil || !credentialInfo.Mode().IsRegular() || owner(credentialInfo) != os.Geteuid() {
			return nil, errors.New("browser-authority-unready")
		}
	}
	if len(previews) == 0 && production == nil {
		return nil, errors.New("browser-project-not-granted")
	}
	if !isFile(os.Getenv("FLEET_PLAYWRIGHT_MODULE")) || !isFile(os.Getenv("FLEET_BROWSER_EXECUTABLE")) {
		return nil, errors.New("browser-runtime-unready")
	}
	return M{"module": os.Getenv("FLEET_PLAYWRIGHT_MODULE"), "executable": os.Getenv("FLEET_BROWSER_EXECUTABLE"), "previewOrigins": binding.PreviewOrigins.V, "production": production, "timeoutMs": probeWait.Milliseconds()}, nil
}
func browserCommand(config any, home string, check bool) *exec.Cmd {
	args := []string{"-e", projectBrowserScript, enc(config)}
	if check {
		args = append(args, "check")
	}
	c := exec.Command("node", args...)
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + os.TempDir()}
	for _, key := range []string{"FLEET_MCP_OWNER", "FLEET_MCP_GENERATION"} {
		if value := os.Getenv(key); value != "" {
			c.Env = append(c.Env, key+"="+value)
		}
	}
	return c
}
func browserReady(payload any, home string) error {
	config, err := browserConfig(payload)
	if err != nil {
		return err
	}
	_, err = boundedOutput(browserCommand(config, home, true), probeWait)
	return err
}
