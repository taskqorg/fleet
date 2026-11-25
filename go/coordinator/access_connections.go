// Project access connections (docs/designs/fleet-project-access.md#how-agents-use-a-grant, option A): a ready grant
// is served by fleet-gateway as an API connection of provider project-mysql or project-files, named <project>-mysql or
// <project>-files, open to its project's runs. The gateway gets the grant's way and credential file names in the
// snapshot (accessGatewayAccess), never a value. A profile grants every such connection of the run's project with the
// one name project-access.
package main

import (
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

// accessGrantAll: the profile connection name that stands for the run's project's access connections.
const accessGrantAll = "project-access"

func accessProvider(kind string) string { return "project-" + kind }

func isAccessProvider(p string) bool { return p == "project-mysql" || p == "project-files" }

// accessConnName: <project>-<kind>; when another live grant already serves that name, <project>-<server>-<kind>.
func accessConnName(q *Request, g accessGrant) (string, error) {
	base := strings.TrimPrefix(accessOSUser(g.ProjectID), "fleet-")
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "p" + base
	}
	name := base + "-" + g.Kind
	r, err := q.db().AccessGrantByConnection(q.ctx(), name)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && r.ID == g.ID {
		return name, nil
	}
	if err != nil {
		return "", err
	}
	server := strings.Trim(accessNameStrip.ReplaceAllString(g.ServerID, "-"), "-")
	return base + "-" + server + "-" + g.Kind, nil
}

// accessConnectionUp opens grant g's connection to its project's runs (on ready).
func accessConnectionUp(q *Request, g accessGrant) error {
	name := deref(g.connection())
	if name == "" {
		var err error
		if name, err = accessConnName(q, g); err != nil {
			return err
		}
		if err := q.db().AccessGrantNote(q.ctx(), db.AccessGrantNoteParams{ID: g.ID, Field: "connection", Value: jsonBytes(name)}); err != nil {
			return err
		}
	}
	old, err := apiConnectionMaybe(q, "connection:"+name)
	if err != nil {
		return err
	}
	c := apiConnection{ID: "connection:" + name, Name: name, Provider: accessProvider(g.Kind), Holder: ownerEmail(q),
		Access: connAccess{Projects: []string{g.ProjectID}}.stored(), Intake: "all", Tools: toolNames(accessProvider(g.Kind)),
		Comment: "Project access grant " + g.ID + " on " + g.ServerID, CreatedAt: jsonBytes(nowMs())}
	if old != nil {
		c.CreatedAt, c.Limits, c.KeyPolicy = old.CreatedAt, old.Limits, old.KeyPolicy
	}
	if err := apiConnectionPut(q, c); err != nil {
		return err
	}
	publish(q)
	return nil
}

// accessConnectionDown pauses grant g's connection (revoked); the gateway refuses its calls at once.
func accessConnectionDown(q *Request, g accessGrant) error {
	name := deref(g.connection())
	if name == "" {
		return nil
	}
	c, err := apiConnectionMaybe(q, "connection:"+name)
	if err != nil || c == nil {
		return err
	}
	c.Intake = "paused"
	if err := apiConnectionPut(q, *c); err != nil {
		return err
	}
	publish(q)
	return nil
}

// connection: the connection a grant serves, once it was ready.
func (g accessGrant) connection() *string {
	var p struct {
		Connection string `json:"connection"`
	}
	lenientDecode(g.Payload, &p)
	if p.Connection == "" {
		return nil
	}
	return &p.Connection
}

// accessGatewayAccess: what the gateway needs to serve connection name: the ready grant's way, user, credential file
// names and scope; nil when no ready grant serves it (the gateway then refuses its calls).
func accessGatewayAccess(q *Request, name string) (*contract.GatewayAccess, error) {
	r, err := q.db().AccessGrantByConnection(q.ctx(), name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g := accessGrantOf(db.AccessGrantGetRow(r))
	hops, _, err := accessHops(q, g.ServerID)
	if err != nil {
		return nil, err
	}
	a := &contract.GatewayAccess{Grant: g.ID, Level: contract.GatewayAccessLevel(g.Level), User: accessOSUser(g.ProjectID),
		Key: accessKeyName(g.ProjectID, g.ServerID) + "/id_ed25519", Hops: []contract.GatewayHop{}, Paths: g.Scope.Paths}
	for _, h := range hops {
		a.Hops = append(a.Hops, contract.GatewayHop{Address: h.hostPort(), HostKey: h.HostKey})
	}
	if g.Kind == "mysql" {
		a.DbUser, a.Password, a.Database, a.Tables = new(accessDBUser(g.ProjectID, g.ID)), new(accessPasswordName(g)), g.Scope.Database, g.Scope.Tables
	}
	return a, nil
}

// runConnections: an agent run's requested connections: its profile's, plus project-access. Every agent run of a
// project gets its project's access connections whatever the session's profile snapshot lists, since a session keeps
// the profile it started with and an owner-approved grant must still reach it (DAM pilot, 2026-11-26).
func runConnections(profile []string) []string {
	if slices.Contains(profile, accessGrantAll) {
		return profile
	}
	return append(append([]string{}, profile...), accessGrantAll)
}

// accessExpand replaces project-access in a profile's connection list by the run project's access connections.
func accessExpand(q *Request, run mintRun, requested []string) ([]string, error) {
	i := -1
	for n, r := range requested {
		if r == accessGrantAll {
			i = n
		}
	}
	if i < 0 {
		return requested, nil
	}
	out := append(append([]string{}, requested[:i]...), requested[i+1:]...)
	if run.ProjectID == nil || *run.ProjectID == "" {
		return out, nil
	}
	all, err := apiConnectionAll(q)
	if err != nil {
		return nil, err
	}
	for _, c := range all {
		if isAccessProvider(c.Provider) && connAllowedFor(c, run) {
			out = append(out, c.Name)
		}
	}
	return out, nil
}
