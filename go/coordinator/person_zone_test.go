package main

import "testing"

// A person's time zone is an IANA name or empty; Local (the coordinator's own zone) and junk are refused.
func TestKnownZone(t *testing.T) {
	for tz, want := range map[string]bool{"": true, "Asia/Bangkok": true, "Europe/Nicosia": true, "UTC": true, "Local": false, "Mars/Olympus": false, "../etc/passwd": false} {
		if got := knownZone(tz); got != want {
			t.Errorf("knownZone(%q) = %v, want %v", tz, got, want)
		}
	}
}
