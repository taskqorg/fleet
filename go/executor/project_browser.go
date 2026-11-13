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

func browserConfig(payload any) (M, error) {
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
	bindings, err := dec(string(raw))
	if err != nil {
		return nil, err
	}
	// The coordinator supplies identity separately from normalized project settings.
	binding := jObj(bindings, jStr(payload, "projectId"))
	if binding == nil {
		return nil, errors.New("browser-project-not-granted")
	}
	previews := jArr(binding, "previewOrigins")
	if has(binding, "previewOrigins") && previews == nil {
		return nil, errors.New("browser-preview-grant-invalid")
	}
	for _, value := range previews {
		u, err := url.Parse(jStr(value))
		if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("browser-preview-grant-invalid")
		}
	}
	production := jObj(binding, "production")
	if production != nil {
		if jStr(production, "origin") != "https://fleet.ufobe.com" {
			return nil, errors.New("browser-production-origin-denied")
		}
		credential := jStr(production, "accessFile")
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
	return M{"module": os.Getenv("FLEET_PLAYWRIGHT_MODULE"), "executable": os.Getenv("FLEET_BROWSER_EXECUTABLE"), "previewOrigins": previews, "production": production, "timeoutMs": probeWait.Milliseconds()}, nil
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
