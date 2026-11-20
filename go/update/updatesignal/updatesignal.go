// Package updatesignal is <data dir>/update/update.json, the one file the executor writes from its poll answers and
// fleet-update reads (docs/designs/fleet-self-update.md, section 4). Both sides use this one type.
package updatesignal

// Signal: update.json. A nil Target or RestartTarget is no target.
type Signal struct {
	RestartTarget *string `json:"restartTarget,omitempty"`
	HostID        string  `json:"hostId"`
	Target        *string `json:"target"`
	AutoUpdate    string  `json:"autoUpdate"`
	Running       string  `json:"running"`
	PollOk        bool    `json:"pollOk"`
	PollError     string  `json:"pollError,omitempty"`
	At            string  `json:"at"`
}

// Str: the value of an optional field, "" when absent.
func Str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
