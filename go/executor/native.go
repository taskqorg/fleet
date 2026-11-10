package main

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

type proc struct {
	pid, ppid int
	stat      string
}

// processes is one ps snapshot: pid, parent and state.
func processes() ([]proc, error) {
	out, err := exec.Command("/bin/ps", "-axo", "pid=,ppid=,stat=").Output()
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
			ps = append(ps, proc{pid, ppid, f[2]})
		}
	}
	return ps, nil
}
