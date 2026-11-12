package main

// Selected trusted dependency launch: the same executor image, owner, guardian and journal; no shell or target argv.
import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

type mcpTarget struct{ path, hash string }
type mcpGeneration struct {
	preparer   int
	preparerID string
	guardian   int
	guardianID string
	targets    map[string]mcpTarget
	leases     map[*net.UnixConn]bool
}

func mcpFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
func (o *mcpOwner) allowedPeer(pid int) (string, error) {
	var root int
	var rootID string
	var fence int
	if err := o.db.QueryRow(`SELECT l.grp,p.identity,l.mcp_fenced FROM launches l JOIN owned p ON p.execution=l.execution AND p.pid=l.grp JOIN commands c ON c.id=l.command WHERE l.execution=? AND c.terminal=0`, o.execution).Scan(&root, &rootID, &fence); err != nil || fence != 0 {
		return "", errors.New("MCP execution is fenced")
	}
	id, err := identity(root)
	if err != nil || id != rootID || !descendant(pid, root) {
		return "", errors.New("MCP peer belongs to another execution")
	}
	path, err := processExecutable(pid)
	self, _ := filepath.EvalSymlinks(exe())
	if err != nil || path != self {
		return "", errors.New("MCP peer executable differs")
	}
	return identity(pid)
}
func (o *mcpOwner) generationRequest(c *net.UnixConn, pid int, request M) (M, bool, func(), error) {
	peerID, err := o.allowedPeer(pid)
	if err != nil {
		return nil, false, nil, err
	}
	action := jStr(request, "action")
	if action == "prepare" {
		targets := map[string]mcpTarget{}
		for _, value := range jArr(request, "ownedDependencies") {
			name := jStr(value)
			path := jStr(request, "dependencies", name)
			if name == "" || path == "" {
				return nil, false, nil, errors.New("owned dependency missing")
			}
			path, err = trustedMCPExecutable(path)
			if err != nil {
				return nil, false, nil, err
			}
			hash, e := mcpFileHash(path)
			if e != nil {
				return nil, false, nil, e
			}
			targets[name] = mcpTarget{path, hash}
		}
		if len(targets) == 0 {
			return nil, false, nil, errors.New("no selected owned dependencies")
		}
		if current, e := o.allowedPeer(pid); e != nil || current != peerID {
			return nil, false, nil, errors.New("MCP preparer changed")
		}
		generation := newID()
		o.generations[generation] = &mcpGeneration{preparer: pid, preparerID: peerID, targets: targets, leases: map[*net.UnixConn]bool{}}
		return M{"generation": generation}, false, nil, nil
	}
	gen := o.generations[jStr(request, "generation")]
	if gen == nil {
		return nil, false, nil, errors.New("MCP generation is stale or unknown")
	}
	dependency := jStr(request, "dependency")
	if action == "lookup" || dependency != "" {
		id, e := identity(gen.guardian)
		if e != nil || id != gen.guardianID || !descendant(pid, gen.guardian) {
			return nil, false, nil, errors.New("MCP dependency generation lost its parent")
		}
		target, ok := gen.targets[dependency]
		if !ok {
			return nil, false, nil, errors.New("dependency was not selected by the binding")
		}
		hash, e := mcpFileHash(target.path)
		if e != nil || hash != target.hash {
			return nil, false, nil, errors.New("MCP dependency target changed")
		}
		if action == "lookup" {
			o.generations[jStr(request, "generation")].leases[c] = true
			return M{"command": target.path, "targetSha256": target.hash}, true, func() { delete(gen.leases, c) }, nil
		}
		if jStr(request, "command") != target.path || jStr(request, "targetSha256") != target.hash {
			return nil, false, nil, errors.New("MCP fixed dependency identity differs")
		}
	} else {
		id, e := identity(gen.preparer)
		if e != nil || id != gen.preparerID || !descendant(pid, gen.preparer) {
			return nil, false, nil, errors.New("MCP generation preparer changed")
		}
		if gen.guardian != 0 {
			return nil, false, nil, errors.New("MCP generation already has a guardian")
		}
	}
	if err = o.register(pid, jStr(request, "identity")); err != nil {
		return nil, false, nil, err
	}
	if dependency != "" {
		return M{}, true, func() {}, nil
	}
	gen.guardian, gen.guardianID = pid, jStr(request, "identity")
	generation := jStr(request, "generation")
	return M{}, true, func() {
		delete(o.generations, generation)
		for lease := range gen.leases {
			lease.Close()
		}
	}, nil
}
func mcpOwnerCall(socket string, request M) (M, *net.UnixConn, error) {
	if err := privateSocket(socket); err != nil {
		return nil, nil, err
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, nil, err
	}
	c.SetDeadline(time.Now().Add(probeWait))
	if err = json.NewEncoder(c).Encode(request); err != nil {
		c.Close()
		return nil, nil, err
	}
	var answer M
	if err = json.NewDecoder(c).Decode(&answer); err != nil {
		c.Close()
		return nil, nil, errors.New("MCP ownership acknowledgement unknown; no replay")
	}
	if !jBoolOr(false, answer, "registered") {
		c.Close()
		return nil, nil, errors.New("MCP ownership refused: " + jStr(answer, "error"))
	}
	c.SetDeadline(time.Time{})
	return answer, c, nil
}
func mcpDependencyInvocation() bool {
	if os.Getenv("FLEET_MCP_GENERATION") == "" {
		return false
	}
	self, err := filepath.EvalSymlinks(exe())
	return err == nil && filepath.Base(os.Args[0]) != filepath.Base(self)
}
func mcpOwnedCommand() error {
	socket, generation := os.Getenv("FLEET_MCP_OWNER"), os.Getenv("FLEET_MCP_GENERATION")
	name := filepath.Base(os.Args[0])
	answer, lease, err := mcpOwnerCall(socket, M{"action": "lookup", "generation": generation, "dependency": name})
	if err != nil {
		return err
	}
	defer lease.Close()
	args := []any{}
	for _, arg := range os.Args[1:] {
		args = append(args, arg)
	}
	spec := M{"command": answer["command"], "targetSha256": answer["targetSha256"], "args": args, "generation": generation, "dependency": name, "childPath": os.Getenv("PATH"), "ownerSocket": socket}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	return mcpStdio(socket, spec, os.Stdin, os.Stdout, root, "owned-command", lease)
}
