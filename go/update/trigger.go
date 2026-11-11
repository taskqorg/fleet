package main

// The executor-host trigger (docs/designs/fleet-delivery-pipeline.md#6-faster-pickup-on-hosts): fleet-update runs as
// soon as the executor rewrites update.json. Each executor-mode run writes the trigger unit for the real data dir
// (FLEET_DATA_DIR or the default), so no host edits a path by hand; an unchanged unit is left alone.

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// trigger is one host's unit file and the commands that load it after a change.
type trigger struct {
	file string
	body []byte
	load [][]string
}

// runTrigger runs one load command; replaced in tests.
var runTrigger = func(argv []string) error { return command(argv...)() }

func triggerFor(home, goos, signal string, uid int) *trigger {
	switch goos {
	case "linux":
		dir := filepath.Join(home, ".config", "systemd", "user")
		if !exists(filepath.Join(dir, "fleet-update.service")) {
			return nil // not a systemd user install
		}
		return &trigger{file: filepath.Join(dir, "fleet-update.path"),
			body: []byte(fmt.Sprintf("# Written by fleet-update executor; edits are overwritten.\n[Unit]\nDescription=Fleet self-update on a new target\n\n[Path]\nPathChanged=%s\nUnit=fleet-update.service\n\n[Install]\nWantedBy=default.target\n", signal)),
			load: [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", "fleet-update.path"}}}
	case "darwin":
		dir := filepath.Join(home, "Library", "LaunchAgents")
		if !exists(filepath.Join(dir, "com.ufobe.fleet-update.plist")) {
			return nil
		}
		gui := "gui/" + strconv.Itoa(uid)
		file := filepath.Join(dir, "com.ufobe.fleet-update-watch.plist")
		var b bytes.Buffer
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
`, gui, xmlEscape(signal))
		return &trigger{file: file, body: b.Bytes(),
			load: [][]string{{"launchctl", "bootout", gui + "/com.ufobe.fleet-update-watch"}, {"launchctl", "bootstrap", gui, file}}}
	}
	return nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ensureTrigger writes and loads the trigger when it is missing or differs; failures only log (the timer still runs).
func ensureTrigger(signal string) {
	home, _ := os.UserHomeDir()
	t := triggerFor(home, runtime.GOOS, signal, os.Getuid())
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
	for i, argv := range t.load {
		if err := runTrigger(argv); err != nil && !(runtime.GOOS == "darwin" && i == 0) { // bootout of a job not loaded fails
			log.Print("trigger: ", err)
			return
		}
	}
	log.Printf("trigger: %s watches %s", t.file, signal)
}
