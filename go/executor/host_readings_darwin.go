//go:build darwin

package main

import (
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func load1() (float64, bool) {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "vm.loadavg").Output() // "{ 1.23 1.10 1.05 }"
	if err != nil {
		return 0, false
	}
	f := strings.Fields(strings.Trim(strings.TrimSpace(string(out)), "{}"))
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}

// memory: free + inactive + speculative pages of vm_stat (what macOS can hand out without paging), and hw.memsize.
func memory() (free, total float64, ok bool) {
	t, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, 0, false
	}
	out, err := exec.Command("/usr/bin/vm_stat").Output()
	if err != nil {
		return 0, float64(t), false
	}
	page := 4096.0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "page size of") {
			for _, w := range strings.Fields(l) {
				if n, err := strconv.ParseFloat(w, 64); err == nil {
					page = n
				}
			}
			continue
		}
		k, v, found := strings.Cut(l, ":")
		if !found {
			continue
		}
		n, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "."), 64)
		switch k {
		case "Pages free", "Pages inactive", "Pages speculative":
			free += n * page
		}
	}
	return free, float64(t), true
}
