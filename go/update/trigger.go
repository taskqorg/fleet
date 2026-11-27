package main

import (
	"bytes"
	"log"
	"os"
	"time"
)

// trigger is one unit file and the commands that load it after a change.
type trigger struct {
	file string
	body []byte
	load []step
}

// step is one load command; tolerate: its failure is expected and ignored; retry: tries with a second between.
type step struct {
	argv     []string
	tolerate bool
	retry    int
}

// runTrigger runs one load command; replaced in tests.
var runTrigger = func(argv []string) error { return command(argv...)() }

var triggerWait = time.Second

func loadTrigger(t *trigger) {
	if t == nil {
		return
	}
	if old, err := os.ReadFile(t.file); err == nil && bytes.Equal(old, t.body) {
		return
	}
	if err := writeFrom(t.file, bytes.NewReader(t.body), 0o644); err != nil {
		log.Print("trigger: ", err)
		return
	}
	for _, st := range t.load {
		err := runTrigger(st.argv)
		for n := 1; err != nil && n < st.retry; n++ {
			time.Sleep(triggerWait)
			err = runTrigger(st.argv)
		}
		if err != nil && !st.tolerate {
			log.Print("trigger: ", err)
			os.Remove(t.file)
			return
		}
	}
	log.Printf("trigger: %s loaded", t.file)
}
