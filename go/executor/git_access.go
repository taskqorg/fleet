package main

// Git identity and credentials per execution: GIT_AUTHOR_*/GIT_COMMITTER_* from the project identity; the SSH key of the
// repo connection under FLEET_CONNECTIONS_DIR (<dir>/<connection>/id_ed25519, <dir>/known_hosts). The ambient ssh agent
// and ~/.ssh config are never used. Errors are reason codes; key contents never leave the host.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

func connectionsDir() string {
	d := os.Getenv("FLEET_CONNECTIONS_DIR")
	if !filepath.IsAbs(d) {
		return ""
	}
	return d
}

// gitRepo: repo by id; a nil id means the project's first repo. An unknown id resolves to nil.
func gitRepo(project any, id any) any {
	repos := jArr(project, "repos")
	if id == nil {
		if len(repos) > 0 {
			return repos[0]
		}
		return nil
	}
	for _, r := range repos {
		if jEq(jAt(r, "id"), id) {
			return r
		}
	}
	return nil
}

func isFile(p string) bool { s, err := os.Stat(p); return err == nil && s.Mode().IsRegular() }

func onlyBytes(s string, ok func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

func alnum(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' }

// gitConnections: the names of the git connections this host holds (a directory with an id_ed25519 under the
// connections dir), sent with each poll so dispatch places a run only where its repository's connection exists.
// Names only: no key bytes and no paths leave the host.
func gitConnections(dir string) []string {
	out := []string{}
	if dir == "" {
		return out
	}
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if e.IsDir() && isFile(filepath.Join(dir, e.Name(), "id_ed25519")) {
			out = append(out, e.Name())
		}
	}
	return out
}

func gitEnv(project, repo any, dir string) ([]string, error) {
	var v []string
	if n, e := jStr(project, "identity", "name"), jStr(project, "identity", "email"); jIsStr(project, "identity", "name") && jIsStr(project, "identity", "email") {
		for _, who := range []string{"AUTHOR", "COMMITTER"} {
			v = append(v, "GIT_"+who+"_NAME="+n, "GIT_"+who+"_EMAIL="+e)
		}
	}
	// ponytail: repos without `connection` keep host ssh; fail closed once all projects declare one.
	if !jIsStr(repo, "connection") {
		return v, nil
	}
	name := jStr(repo, "connection")
	if name == "" || !onlyBytes(name, func(b byte) bool { return alnum(b) || b == '-' || b == '_' }) {
		return nil, errors.New("connection-invalid")
	}
	if dir == "" {
		return nil, errors.New("connections-unconfigured")
	}
	key, known := filepath.Join(dir, name, "id_ed25519"), filepath.Join(dir, "known_hosts")
	if !isFile(key) {
		return nil, errors.New("connection-missing")
	}
	if !isFile(known) {
		return nil, errors.New("known-hosts-missing")
	}
	// GIT_SSH_COMMAND is shell-parsed: refuse paths that would need quoting.
	for _, s := range []string{key, known} {
		if !onlyBytes(s, func(b byte) bool { return alnum(b) || b == '/' || b == '.' || b == '_' || b == '-' }) {
			return nil, errors.New("connection-path-invalid")
		}
	}
	v = append(v, "GIT_SSH_COMMAND=ssh -F /dev/null -i "+key+" -o IdentitiesOnly=yes -o IdentityAgent=none -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile="+known)
	return v, nil
}

// setEnv replaces or adds KEY=VALUE entries in a command environment (nil Env means the parent's).
func setEnvKV(c *exec.Cmd, kv ...string) {
	if c.Env == nil {
		c.Env = os.Environ()
	}
	for _, e := range kv {
		k := e[:len(e)-len(e[indexByte(e, '=')+1:])]
		out := c.Env[:0]
		for _, x := range c.Env {
			if len(x) < len(k) || x[:len(k)] != k {
				out = append(out, x)
			}
		}
		c.Env = append(out, e)
	}
}

func unsetEnv(c *exec.Cmd, key string) {
	if c.Env == nil {
		c.Env = os.Environ()
	}
	out := c.Env[:0]
	for _, x := range c.Env {
		if len(x) <= len(key) || x[:len(key)+1] != key+"=" {
			out = append(out, x)
		}
	}
	c.Env = out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return len(s)
}

func gitApply(c *exec.Cmd, vars []string) {
	unsetEnv(c, "SSH_AUTH_SOCK")
	setEnvKV(c, "GIT_TERMINAL_PROMPT=0")
	setEnvKV(c, vars...)
}
