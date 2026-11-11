package main

// The executor-host trigger (docs/designs/fleet-delivery-pipeline.md#6-faster-pickup-on-hosts): fleet-update runs as
// soon as the executor rewrites update.json. Each executor-mode run writes the trigger unit for the real data dir
// (FLEET_DATA_DIR or the default), so no host edits a path by hand; an unchanged unit is left alone.

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// trigger is one host's unit file and the commands that load it after a change.
type trigger struct {
	file string
	body []byte
	load []step
}

// step is one load command; tolerate: its failure is expected and ignored (bootout of a job not loaded); retry: tries
// with a second between (launchd finishes a bootout asynchronously).
type step struct {
	argv     []string
	tolerate bool
	retry    int
}

// runTrigger runs one load command; replaced in tests.
var runTrigger = func(argv []string) error { return command(argv...)() }

var triggerWait = time.Second

func triggerFor(home, goos, signal string, uid int) *trigger {
	switch goos {
	case "linux":
		dir := filepath.Join(home, ".config", "systemd", "user")
		if !exists(filepath.Join(dir, "fleet-update.service")) {
			return nil // not a systemd user install
		}
		return &trigger{file: filepath.Join(dir, "fleet-update.path"),
			body: []byte(fmt.Sprintf("# Written by fleet-update executor; edits are overwritten.\n[Unit]\nDescription=Fleet self-update on a new target\n\n[Path]\nPathChanged=%s\nUnit=fleet-update.service\n\n[Install]\nWantedBy=default.target\n", strings.ReplaceAll(signal, "%", "%%"))),
			// restart, not enable --now: an active path unit keeps watching its old path until restarted.
			load: []step{{argv: []string{"systemctl", "--user", "daemon-reload"}}, {argv: []string{"systemctl", "--user", "enable", "fleet-update.path"}},
				{argv: []string{"systemctl", "--user", "restart", "fleet-update.path"}}}}
	case "darwin":
		dir := filepath.Join(home, "Library", "LaunchAgents")
		if !exists(filepath.Join(dir, "com.ufobe.fleet-update.plist")) {
			return nil
		}
		gui := "gui/" + strconv.Itoa(uid)
		file := filepath.Join(dir, "com.ufobe.fleet-update-watch.plist")
		var esc bytes.Buffer
		xml.EscapeText(&esc, []byte(signal))
		var b bytes.Buffer
		// The file, not its directory: fleet-update writes update-state.json there, which would restart it in a loop.
		fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by fleet-update executor; edits are overwritten. Starts com.ufobe.fleet-update when update.json changes. -->
<plist version="1.0">
<dict>
  <key>Label</key><string>com.ufobe.fleet-update-watch</string>
  <key>ProgramArguments</key>
  <array><string>/bin/launchctl</string><string>kickstart</string><string>%s/com.ufobe.fleet-update</string></array>
  <key>WatchPaths</key>
  <array><string>%s</string></array>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`, gui, esc.String())
		return &trigger{file: file, body: b.Bytes(),
			load: []step{{argv: []string{"launchctl", "bootout", gui + "/com.ufobe.fleet-update-watch"}, tolerate: true},
				{argv: []string{"launchctl", "bootstrap", gui, file}, retry: 5}}}
	}
	return nil
}

// ensureTrigger writes and loads the trigger when it is missing or differs; failures only log (the timer still runs).
// A failed load removes the file, so the next run tries again.
func ensureTrigger(signal string) {
	home, _ := os.UserHomeDir()
	loadTrigger(triggerFor(home, runtime.GOOS, signal, os.Getuid()))
}

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
