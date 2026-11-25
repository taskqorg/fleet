// The provisioner's SSH work for one grant (docs/designs/fleet-project-access.md#recipes), run by the access-work
// queue's call outside any transaction: the recipes as root through the provisioner key, then the probes, the last of
// them a login as the project's OS user over the path agents use. Its outcome carries no credential value.
package main

import (
	"context"
	"strings"
)

// accessPlan: one grant's work and everything it needs from the database.
type accessPlan struct {
	Op                                    string
	Grant                                 accessGrant
	Hops                                  []accessHop
	OSUser, DBUser, KeyName, PasswordName string
	Paths                                 []string // the OS user's mounts after this run (the project's live files grants)
	Others                                int      // the project's other live grants on this server
}

// accessOutcome: what a run did; Error is empty when it worked.
type accessOutcome struct {
	Steps      []string
	Error      string
	Credential string
}

func (p accessPlan) apply(ctx context.Context) any {
	ctx, cancel := context.WithTimeout(ctx, accessCallBudget)
	defer cancel()
	return p.run(ctx)
}

func (p accessPlan) run(ctx context.Context) (out accessOutcome) {
	failed := func(err error) accessOutcome { out.Error = err.Error(); return out }
	prov, err := accessKey("provisioner")
	if err != nil {
		return failed(err)
	}
	proj, err := accessKey(p.KeyName)
	if err != nil {
		return failed(err)
	}
	body, fingerprint := accessPublic(proj)
	g := p.Grant
	target := p.Hops[len(p.Hops)-1]
	label := strings.ToLower(p.KeyName)
	// onVia runs a recipe on the jump server, when there is one.
	onVia := func(args ...string) error {
		if len(p.Hops) < 2 {
			return nil
		}
		c, err := accessDial(ctx, p.Hops[:1], "root", prov, nil)
		if err != nil {
			return err
		}
		defer c.Close()
		_, err = accessRun(c, args, "")
		if err == nil {
			out.Steps = append(out.Steps, p.Hops[0].ID+": "+args[0]+" "+label)
		}
		return err
	}
	c, err := accessDial(ctx, p.Hops, "root", prov, nil)
	if err != nil {
		return failed(err)
	}
	defer c.Close()
	run := func(stdin string, args ...string) error {
		text, err := accessRun(c, args, stdin)
		if err == nil {
			for _, line := range strings.Split(text, "\n") {
				if line = strings.TrimSpace(line); line != "" {
					out.Steps = append(out.Steps, target.ID+": "+line)
				}
			}
		}
		return err
	}
	paths := "-"
	if len(p.Paths) > 0 {
		paths = strings.Join(p.Paths, ",")
	}
	password := ""
	if g.Kind == "mysql" {
		if password, err = accessSecret(p.PasswordName); err != nil {
			return failed(err)
		}
	}
	if p.Op == "revoke" {
		if g.Kind == "mysql" {
			if err := run("", "revoke-mysql", p.DBUser); err != nil {
				return failed(err)
			}
			if err := accessForget(p.PasswordName); err != nil {
				return failed(err)
			}
		}
		if p.Others > 0 {
			if err := run("", "os-user", p.OSUser, body, paths); err != nil {
				return failed(err)
			}
			return out
		}
		if err := run("", "revoke-os-user", p.OSUser); err != nil {
			return failed(err)
		}
		if err := onVia("jump-deny", label); err != nil {
			return failed(err)
		}
		if err := accessForget(p.KeyName); err != nil {
			return failed(err)
		}
		return out
	}
	if p.Op == "provision" {
		if err := onVia("jump-allow", label, body, target.hostPort()); err != nil {
			return failed(err)
		}
		if err := run("", "os-user", p.OSUser, body, paths); err != nil {
			return failed(err)
		}
		out.Credential = fingerprint
		if g.Kind == "mysql" {
			if err := run(password+"\n", "mysql-user", p.DBUser, deref(g.Scope.Database), strings.Join(g.Scope.Tables, ","), g.Level); err != nil {
				return failed(err)
			}
			out.Credential += " " + accessSecretFingerprint(password)
		}
	}
	// Probes: as root for the grant's level, then a login as the OS user over the agents' path.
	switch g.Kind {
	case "mysql":
		if err := run(password+"\n", "probe-mysql", p.DBUser, deref(g.Scope.Database), g.Scope.Tables[0], g.Level); err != nil {
			return failed(err)
		}
	case "files":
		for _, item := range accessPathItems(g.Level, g.Scope) {
			if err := run("", "probe-files", p.OSUser, item); err != nil {
				return failed(err)
			}
		}
	}
	uc, err := accessDial(ctx, p.Hops, p.OSUser, proj, nil)
	if err != nil {
		return failed(err)
	}
	defer uc.Close()
	switch g.Kind {
	case "mysql":
		v, err := accessProbeForward(uc, "127.0.0.1:3306")
		if err != nil {
			return failed(err)
		}
		out.Steps = append(out.Steps, p.OSUser+" login forwards to MySQL "+v)
	case "files":
		if err := accessProbeSFTP(uc); err != nil {
			return failed(err)
		}
		out.Steps = append(out.Steps, p.OSUser+" login opens SFTP")
	}
	return out
}
