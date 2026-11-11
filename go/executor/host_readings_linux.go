//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
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
			free = v * 1024
		case "MemTotal:":
			total = v * 1024
		}
	}
	return free, total, total > 0
}
