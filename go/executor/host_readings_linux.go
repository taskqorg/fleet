//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func load1() (float64, bool) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}

// memory: MemAvailable and MemTotal of /proc/meminfo, bytes. In WSL this is the VM's memory.
func memory() (free, total float64, ok bool) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseFloat(f[1], 64)
		switch f[0] {
		case "MemAvailable:":
			free, ok = v*1024, true
		case "MemTotal:":
			total = v * 1024
		}
	}
	return free, total, ok && total > 0 // no MemAvailable (old kernels): unknown, never 0
}

// processEnv: one variable of a process's environment (/proc/<pid>/environ), readable for the executor's own user.
func processEnv(pid int, key string) (string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return "", false
	}
	for _, kv := range strings.Split(string(b), "\x00") {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

func meminfo(keys ...string) (map[string]float64, bool) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, false
	}
	out := map[string]float64{}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		for _, k := range keys {
			if f[0] == k+":" {
				v, _ := strconv.ParseFloat(f[1], 64)
				out[k] = v * 1024
			}
		}
	}
	return out, len(out) == len(keys)
}

func swap() (used, total float64, ok bool) {
	m, ok := meminfo("SwapTotal", "SwapFree")
	if !ok {
		return 0, 0, false
	}
	return m["SwapTotal"] - m["SwapFree"], m["SwapTotal"], true
}

// compressed: Linux has no compressor reading (zswap is counted in swap).
func compressed() (float64, bool) { return 0, false }

// tmpfs: /tmp use when it is tmpfs (its files are memory, which MemAvailable does not show).
func tmpfs() (used, total float64, ok bool) {
	var st unix.Statfs_t
	if unix.Statfs("/tmp", &st) != nil || st.Type != unix.TMPFS_MAGIC {
		return 0, 0, false
	}
	total = float64(st.Blocks) * float64(st.Bsize)
	return total - float64(st.Bfree)*float64(st.Bsize), total, true
}

// swappedBytes: VmSwap of /proc/<pid>/status.
func swappedBytes(pid int) float64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "VmSwap:"); ok {
			f := strings.Fields(v)
			if len(f) > 0 {
				n, _ := strconv.ParseFloat(f[0], 64)
				return n * 1024
			}
		}
	}
	return 0
}
