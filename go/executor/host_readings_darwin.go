//go:build darwin

package main

import (
	"encoding/binary"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// load1: vm.loadavg is struct loadavg {fixpt_t ldavg[3]; long fscale} (uint32 x3, padding, int64 on arm64 and amd64).
func load1() (float64, bool) {
	b, err := unix.SysctlRaw("vm.loadavg")
	if err != nil || len(b) < 24 {
		return 0, false
	}
	scale := float64(binary.LittleEndian.Uint64(b[16:24]))
	if scale == 0 {
		return 0, false
	}
	return float64(binary.LittleEndian.Uint32(b[0:4])) / scale, true
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

// processEnv: one variable of a process's environment, from kern.procargs2 (int32 argc, exec path, padding, argc
// arguments, then the environment, each NUL-ended). Readable for processes of the executor's own user.
func processEnv(pid int, key string) (string, bool) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return "", false
	}
	argc := int(binary.LittleEndian.Uint32(b[:4]))
	parts := strings.Split(string(b[4:]), "\x00")
	i := 1 // exec path
	for i < len(parts) && parts[i] == "" {
		i++ // padding after the exec path
	}
	i += argc
	for ; i < len(parts); i++ {
		if v, ok := strings.CutPrefix(parts[i], key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// swap: vm.swapusage is struct xsw_usage {u_int64_t total, avail, used; u_int32_t pagesize; boolean_t encrypted}.
func swap() (used, total float64, ok bool) {
	b, err := unix.SysctlRaw("vm.swapusage")
	if err != nil || len(b) < 24 {
		return 0, 0, false
	}
	return float64(binary.LittleEndian.Uint64(b[16:24])), float64(binary.LittleEndian.Uint64(b[0:8])), true
}

// compressed: bytes held by the memory compressor (vm_stat "Pages occupied by compressor").
func compressed() (float64, bool) {
	out, err := exec.Command("/usr/bin/vm_stat").Output()
	if err != nil {
		return 0, false
	}
	page := 4096.0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "page size of") {
			for _, w := range strings.Fields(l) {
				if n, err := strconv.ParseFloat(w, 64); err == nil {
					page = n
				}
			}
		}
		if v, ok := strings.CutPrefix(l, "Pages occupied by compressor:"); ok {
			n, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "."), 64)
			return n * page, err == nil
		}
	}
	return 0, false
}

// tmpfs: /tmp is never tmpfs on macOS.
func tmpfs() (used, total float64, ok bool) { return 0, 0, false }

// swappedBytes: macOS has no per-process swap reading.
func swappedBytes(int) float64 { return 0 }
