package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// status writes an OSC 7501 program status report (https://www.superlogical.com/rex/docs/build/program-status) to stderr
// when stderr is a terminal: state working | blocked (kind permission|question|auth) | done | error. Repeats are dropped.
var lastStatus string

func status(state, kind string) {
	s := "\x1b]7501;state=" + state + ":app=fleet-executor"
	if kind != "" {
		s += ":kind=" + kind
	}
	s += "\x1b\\"
	if s == lastStatus || !isTerminal(os.Stderr) {
		return
	}
	lastStatus = s
	os.Stderr.WriteString(s)
}

func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	return err == nil
}
