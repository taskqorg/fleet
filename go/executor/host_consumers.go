package main

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"fleet/contract"
)

// hostPressure: the readings beside free memory that show what holds a host (docs/designs/fleet-host-cleanup.md#5-readings):
// swap, the macOS compressor, /tmp when it is tmpfs, and the top memory consumers. Unreadable values stay absent.
func hostPressure(out *contract.HostLoad) {
	if used, total, ok := swap(); ok {
		out.SwapUsed, out.SwapTotal = &used, &total
	}
	if c, ok := compressed(); ok {
		out.MemCompressed = &c
	}
	if used, total, ok := tmpfs(); ok {
		out.TmpUsed, out.TmpTotal = &used, &total
	}
	out.Top = topConsumers(5)
}

// topConsumers: the n processes holding the most resident plus swapped memory. command is the program base name only
// (no arguments or paths); fleet marks a Fleet program or a process carrying a run marker.
func topConsumers(n int) []contract.HostConsumer {
	b, err := exec.Command("/bin/ps", "-axww", "-o", "pid=,user=,rss=,etime=,args=").Output()
	if err != nil {
		return nil
	}
	var all []contract.HostConsumer
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		rss, _ := strconv.ParseFloat(f[2], 64)
		user, age := f[1], psTime(f[3])
		all = append(all, contract.HostConsumer{Pid: int64(pid), User: &user, Command: strings.TrimSuffix(filepath.Base(f[4]), ":"),
			MemBytes: rss*1024 + swappedBytes(pid), AgeSeconds: &age})
	}
	slices.SortFunc(all, func(a, b contract.HostConsumer) int {
		switch {
		case a.MemBytes > b.MemBytes:
			return -1
		case a.MemBytes < b.MemBytes:
			return 1
		}
		return 0
	})
	all = all[:min(n, len(all))]
	for i := range all {
		_, marked := processEnv(int(all[i].Pid), runMarkerKey)
		fleet := marked || isFleetProgram(all[i].Command)
		all[i].Fleet = &fleet
	}
	return all
}

func isFleetProgram(command string) bool {
	for _, names := range fleetProcesses {
		if slices.Contains(names, command) {
			return true
		}
	}
	return false
}

// topAttr: the consumers as "command:MB" (a trailing * marks Fleet's), largest first, for the heartbeat span.
func topAttr(top []contract.HostConsumer) string {
	parts := make([]string, 0, len(top))
	for _, c := range top {
		s := c.Command + ":" + strconv.FormatFloat(c.MemBytes/(1<<20), 'f', 0, 64)
		if c.Fleet != nil && *c.Fleet {
			s += "*"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}
