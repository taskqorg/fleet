// Composer commands (docs/design-system/controls/composer.md "Commands"): a message that starts with a known /command
// changes a context item of the session instead of being sent. One parser, here on the server, so the composer's preview,
// its apply and an agent's call read a command the same way. Today the only command is /trigger (docs/designs/
// fleet-triggers.md): it edits, pauses, resumes, runs, deletes or creates a trigger of the session.
package main

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// triggerChangeKinds: the change kinds a trigger condition may name (fleet-triggers.md "Trigger").
var triggerChangeKinds = []string{"task", "question", "execution", "host", "limits"}

// triggerChangedDefault: "if changed" without kinds, the condition of the conversation-check preset.
var triggerChangedDefault = []string{"task", "question", "execution"}

// triggerCommand: one parsed /trigger command. A nil field is left as it is; IfChanged set to an empty list is "always".
type triggerCommand struct {
	Name      string    // the trigger's name; for "new", the name to give (empty: the preset's)
	Action    string    // edit, pause, resume, run, delete or new
	Preset    string    // new only
	EveryMs   *int64    // every <duration>
	Daily     *string   // daily HH:MM
	Zone      *string   // daily HH:MM <IANA zone>
	IfChanged *[]string // if changed, if <kinds>, always
	Text      *string   // after the first ": "
}

var (
	commandDuration = regexp.MustCompile(`^([1-9][0-9]*)(s|m|h|d)$`)
	commandClock    = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	commandName     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// isComposerCommand: whether text is a command for parseTriggerCommand; anything else is an ordinary message.
func isComposerCommand(text string) bool {
	t := strings.TrimSpace(text)
	return t == "/trigger" || strings.HasPrefix(t, "/trigger ")
}

// parseTriggerCommand reads `/trigger <name> [every <dur> | daily HH:MM [zone]] [if changed | if <kinds> | always] [: <text>]`,
// `/trigger <name> pause|resume|run|delete` and `/trigger new <preset> [as <name>] ...`. The error says what to write.
func parseTriggerCommand(text string) (triggerCommand, error) {
	var c triggerCommand
	t := strings.TrimSpace(text)
	if !isComposerCommand(t) {
		return c, fmt.Errorf("a command starts with /trigger")
	}
	// The message starts after the first colon that ends a word (": " or a final ":"), so "daily 09:00" stays in the head.
	rest := strings.TrimSpace(strings.TrimPrefix(t, "/trigger")) + " "
	head, body, hasText := strings.Cut(rest, ": ")
	if hasText {
		s := strings.TrimSpace(body)
		if s == "" {
			return c, fmt.Errorf("write the message after the colon, or leave the colon out to keep it")
		}
		c.Text = &s
	}
	words := strings.Fields(head)
	if len(words) == 0 {
		return c, fmt.Errorf("name the trigger: /trigger <name> every 5m: <text>")
	}
	c.Action = "edit"
	if words[0] == "new" {
		if len(words) < 2 {
			return c, fmt.Errorf("name the preset: /trigger new conversation-check")
		}
		c.Action, c.Preset, words = "new", words[1], words[2:]
		if len(words) >= 2 && words[0] == "as" {
			c.Name, words = words[1], words[2:]
		}
	} else {
		c.Name, words = words[0], words[1:]
	}
	for _, n := range []string{c.Name, c.Preset} {
		if n != "" && !commandName.MatchString(n) {
			return c, fmt.Errorf("%q is not a trigger or preset name: letters, digits, '.', '_' and '-'", n)
		}
	}
	if c.Action == "edit" && len(words) == 1 && !hasText && slices.Contains([]string{"pause", "resume", "run", "delete"}, words[0]) {
		c.Action = words[0]
		return c, nil
	}
	for i := 0; i < len(words); i++ {
		w := words[i]
		next := func(what string) (string, error) {
			if i+1 >= len(words) {
				return "", fmt.Errorf("%s needs %s", w, what)
			}
			i++
			return words[i], nil
		}
		switch w {
		case "every":
			v, err := next("a duration such as 5m")
			if err != nil {
				return c, err
			}
			m := commandDuration.FindStringSubmatch(v)
			if m == nil {
				return c, fmt.Errorf("%q is not a duration: write 30s, 5m, 2h or 1d", v)
			}
			n, _ := strconv.ParseInt(m[1], 10, 64)
			ms := n * map[string]int64{"s": 1000, "m": 60_000, "h": 3_600_000, "d": 86_400_000}[m[2]]
			c.EveryMs = &ms
		case "daily":
			v, err := next("a time such as 09:00")
			if err != nil {
				return c, err
			}
			if !commandClock.MatchString(v) {
				return c, fmt.Errorf("%q is not a time: write HH:MM, such as 09:00", v)
			}
			c.Daily = &v
			if i+1 < len(words) && (strings.Contains(words[i+1], "/") || words[i+1] == "UTC") {
				i++
				z := words[i]
				c.Zone = &z
			}
		case "if":
			v, err := next("changed, or kinds such as task,question")
			if err != nil {
				return c, err
			}
			kinds := triggerChangedDefault
			if v != "changed" {
				kinds = nil
				for _, k := range strings.Split(v, ",") {
					if !slices.Contains(triggerChangeKinds, k) {
						return c, fmt.Errorf("%q is not a change kind: use %s", k, strings.Join(triggerChangeKinds, ", "))
					}
					if !slices.Contains(kinds, k) {
						kinds = append(kinds, k)
					}
				}
			}
			c.IfChanged = &kinds
		case "always":
			c.IfChanged = &[]string{}
		default:
			return c, fmt.Errorf("%q is not part of a /trigger command: use every, daily, if changed, always, or a colon before the message", w)
		}
	}
	if c.EveryMs != nil && c.Daily != nil {
		return c, fmt.Errorf("a trigger runs every <duration> or daily at a time, not both")
	}
	if c.Action == "edit" && c.EveryMs == nil && c.Daily == nil && c.IfChanged == nil && c.Text == nil {
		return c, fmt.Errorf("say what to change: every 5m, daily 09:00, if changed, always, or a colon and the message")
	}
	return c, nil
}

// formatTriggerCommand: the command that sets a trigger to what it is now, for Edit (the composer is filled with it) and
// for Load current after a conflict. everyMs 0 and daily "" mean the trigger has neither (a manual one).
func formatTriggerCommand(name string, everyMs int64, daily, zone string, ifChanged []string, text string) string {
	var b strings.Builder
	b.WriteString("/trigger " + name)
	switch {
	case everyMs > 0:
		b.WriteString(" every " + formatCommandDuration(everyMs))
	case daily != "":
		b.WriteString(" daily " + daily)
		if zone != "" {
			b.WriteString(" " + zone)
		}
	}
	switch {
	case len(ifChanged) == 0:
		b.WriteString(" always")
	case slices.Equal(ifChanged, triggerChangedDefault):
		b.WriteString(" if changed")
	default:
		b.WriteString(" if " + strings.Join(ifChanged, ","))
	}
	if text != "" {
		b.WriteString(": " + text)
	}
	return b.String()
}

// formatCommandDuration: the largest whole unit of ms (90000 is 90s, 300000 is 5m).
func formatCommandDuration(ms int64) string {
	for _, u := range []struct {
		ms   int64
		unit string
	}{{86_400_000, "d"}, {3_600_000, "h"}, {60_000, "m"}, {1000, "s"}} {
		if ms%u.ms == 0 {
			return strconv.FormatInt(ms/u.ms, 10) + u.unit
		}
	}
	return strconv.FormatInt((ms+999)/1000, 10) + "s"
}
