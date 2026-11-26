package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
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
	// comm is the executable's path (macOS) or name (Linux), the last column so a path with spaces stays whole;
	// user:32 keeps names such as fleet-ci-vm whole where ps would cut them to eight characters.
	user := "user="
	if runtime.GOOS == "linux" {
		user = "user:32=" // procps; BSD ps on macOS prints whole names already
	}
	b, err := exec.Command("/bin/ps", "-axww", "-o", "pid=,"+user+",rss=,etime=,comm=").Output()
	if err != nil {
		return nil
	}
	var all []contract.HostConsumer
	for _, l := range strings.Split(string(b), "\n") {
		pid, user, rss, age, command, ok := psConsumer(l)
		if !ok {
			continue
		}
		all = append(all, contract.HostConsumer{Pid: int64(pid), User: &user, Command: command,
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
		fleet := marked || isFleetProgram(all[i].Command) || strings.HasPrefix(*all[i].User, "fleet")
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

// psConsumer parses one ps row (pid, user, rss in KB, etime, comm): the command is the base name of everything after
// the fourth column, so "/Applications/Google Chrome.app/.../Google Chrome Helper" is "Google Chrome Helper".
func psConsumer(l string) (pid int, user string, rss, age float64, command string, ok bool) {
	rest := strings.TrimSpace(l)
	var f [4]string
	for i := range f {
		j := strings.IndexAny(rest, " \t")
		if j < 0 {
			return
		}
		f[i], rest = rest[:j], strings.TrimSpace(rest[j:])
	}
	if rest == "" {
		return
	}
	pid, err := strconv.Atoi(f[0])
	if err != nil {
		return
	}
	rss, _ = strconv.ParseFloat(f[2], 64)
	return pid, f[1], rss, psTime(f[3]), strings.TrimSuffix(filepath.Base(rest), ":"), true
}
