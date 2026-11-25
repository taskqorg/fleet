package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// codingTool checks the same clean HOME and PATH used by a managed native command.
func codingTool(tool, home string) error {
	var c *exec.Cmd
	switch tool {
	case "node", "pnpm":
		c = exec.Command(tool, "--version")
	case "podman": // rootless storage is the executor user's own, never the harness HOME
		c = exec.Command("podman", "info", "--format", "{{.Host.Security.Rootless}}")
		c.Env = podmanEnv(os.Getenv("HOME"), os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("PATH"))
		c.Dir = os.Getenv("HOME")
		_, err := boundedOutput(c, probeWait)
		return err
	default:
		return os.ErrInvalid
	}
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + os.TempDir()}
	c.Dir = filepath.Dir(home)
	_, err := boundedOutput(c, probeWait)
	return err
}

func commandHome(c *exec.Cmd) string {
	for _, v := range c.Env {
		if strings.HasPrefix(v, "HOME=") {
			return strings.TrimPrefix(v, "HOME=")
		}
	}
	return ""
}
