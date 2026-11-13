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
	case "browser":
		c = exec.Command("node", "-e", `const {chromium}=require(process.env.FLEET_PLAYWRIGHT_MODULE); (async()=>{const b=await chromium.launch({headless:true,executablePath:process.env.FLEET_BROWSER_EXECUTABLE});try {const p=await b.newPage();await p.setContent('<title>Fleet readiness</title>');if(await p.title()!=='Fleet readiness')process.exitCode=1;}finally{await b.close();}})().catch(()=>process.exitCode=1)`)
	default:
		return os.ErrInvalid
	}
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + os.TempDir()}
	if tool == "browser" {
		c.Env = append(c.Env, "FLEET_PLAYWRIGHT_MODULE="+os.Getenv("FLEET_PLAYWRIGHT_MODULE"), "FLEET_BROWSER_EXECUTABLE="+os.Getenv("FLEET_BROWSER_EXECUTABLE"))
	}
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
