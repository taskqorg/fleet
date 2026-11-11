package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// identity is the start-time identity of a process (processIdentity per platform); a variable so a check can make
// the read fail.
var identity = processIdentity

type proc struct {
	pid, ppid int
	stat      string
	rssKB     uint64  // resident memory, KiB
	cpu       float64 // CPU seconds used so far (ps time)
	name      string  // base name of the program (the first word of args: comm is cut to 15 bytes on Linux)
}

// processes is one ps snapshot: pid, parent and state.
func processes() ([]proc, error) {
	out, err := exec.Command("/bin/ps", "-ww", "-axo", "pid=,ppid=,stat=,rss=,time=,args=").Output()
	if err != nil {
		return nil, errors.New("process snapshot failed")
	}
	var ps []proc
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 == nil && e2 == nil {
			p := proc{pid: pid, ppid: ppid, stat: f[2]}
			if len(f) >= 5 {
				p.rssKB, _ = strconv.ParseUint(f[3], 10, 64)
				p.cpu = psTime(f[4])
			}
			if len(f) >= 6 {
				p.name = strings.TrimSuffix(filepath.Base(f[5]), ":") // postgres children retitle as "postgres: walwriter"
			}
			ps = append(ps, p)
		}
	}
	return ps, nil
}

// psTime: ps "time" as seconds: [[dd-]hh:]mm:ss[.cc] (procps prints hh:mm:ss, macOS mm:ss.cc).
func psTime(t string) float64 {
	days := 0.0
	if d, rest, ok := strings.Cut(t, "-"); ok {
		days, _ = strconv.ParseFloat(d, 64)
		t = rest
	}
	total := 0.0
	for _, part := range strings.Split(t, ":") {
		v, _ := strconv.ParseFloat(part, 64)
		total = total*60 + v
	}
	return days*86400 + total
}
