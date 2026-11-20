package main

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
)

var (
	filepathGlob = filepath.Glob
	filepathAbs  = filepath.Abs
)

// runGoTest runs TestLaw with an overlay; the run stops at the first law violation.
func runGoTest(overlay, mutant string) (string, error) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		goBin = "go"
	}
	cmd := osexec.Command(goBin, "test", "-overlay", overlay, "-run", "^TestLaw$", "-count=1", "-failfast", ".")
	cmd.Env = append(os.Environ(), "FLEET_LAW_MUTANT="+mutant)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
