package main

// boot (docs/designs/fleet-data-layer.md, "Boot"): the app's start in one request. A cold page used to wait for three
// round trips through Access in turn before any read (me, then the change stream's ready, then the person's settings,
// ~330 ms each on prod); boot answers all three, and the stream then connects from its cursor while the page reads.
// A person Access signed in but Fleet does not know gets person unknown and their email instead of a 403 (which the
// app took for an expired session): they read nothing else.

import (
	"strings"

	"fleet/contract"
)

func init() {
	op("boot", boot)
}

func boot(q *Request, _ *contract.BootRequest) (contract.BootReply, error) {
	if q.person == nil {
		me := contract.MeReply{Roles: []string{}, ChangeLogRetentionDays: 7}
		if q.unknownEmail != "" {
			me.Email = ptr(q.unknownEmail)
		}
		return contract.BootReply{Person: contract.BootPersonUnknown, Me: me}, nil
	}
	out := contract.BootReply{Person: contract.BootPersonKnown, Me: meReply(q)}
	settings, err := personSettings(q, q.person.Email)
	if err != nil {
		return out, err
	}
	out.Settings = &settings
	cursor, err := head(q.ctx(), q.tx)
	if err != nil {
		return out, err
	}
	out.Cursor = &cursor
	return out, nil
}

// bootUnknownAllowed: the one request a person Fleet does not know may make (boot answers who they are).
func bootUnknownAllowed(path string) bool { return strings.TrimSuffix(path, "/") == "/api/ops/boot" }
