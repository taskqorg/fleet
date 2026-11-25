package main

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

var (
	filepathGlob = filepath.Glob
	filepathAbs  = filepath.Abs
)

// runGoTest runs TestLaw with an overlay on the caller's cluster (sock, port, bin); the run stops at the first law
// violation. FLEET_REQUIRE_PG=1 makes a child that cannot reach PostgreSQL fail rather than skip, so a mutant never
// survives only because its laws did not run. The child always runs the default seed set: a FLEET_LAW_SEED(S) the
// caller set to replay one seed would let a mutant that only some seeds reach survive.
func runGoTest(overlay, mutant, sock string, port int, bin string) (string, error) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		goBin = "go"
	}
	cmd := osexec.Command(goBin, "test", "-overlay", overlay, "-run", "^TestLaw$", "-count=1", "-failfast", ".")
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "FLEET_LAW_SEED=") || strings.HasPrefix(kv, "FLEET_LAW_SEEDS=")
	})
	cmd.Env = append(env, "FLEET_LAW_MUTANT="+mutant, "FLEET_REQUIRE_PG=1",
		testPGClusterEnv+"="+sock+"|"+strconv.Itoa(port)+"|"+bin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
