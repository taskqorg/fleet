package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"fleet/contract"
)

// The snapshot and call line types are the contract the coordinator writes (contracts/gateway.schema.json).
type (
	tool       = contract.GatewayTool
	provider   = contract.GatewayProvider
	connection = contract.GatewayConnection
)

type token struct {
	contract.GatewayRunToken
	// Budget scopes this run belongs to: person:<email>, project:<id>, experiment:<id>.
	scopes []string
}

type snapshot struct {
	contract.GatewaySnapshot
	revision string
	tokens   map[string]*token
}

func parseSnapshot(data []byte) (*snapshot, error) {
	s := &snapshot{}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	switch {
	case s.Schema != 2:
		return nil, errors.New("unsupported schema")
	case s.GeneratedMs == 0:
		return nil, errors.New("generated_ms is missing")
	case s.Providers == nil:
		return nil, errors.New("providers is missing")
	case s.Connections == nil:
		return nil, errors.New("connections is missing")
	case s.Tokens == nil:
		return nil, errors.New("tokens is missing")
	}
	for name, p := range s.Providers {
		if p.Tools == nil {
			return nil, errors.New("provider tools are missing")
		}
		if p.BaseUrl == "" {
			return nil, errors.New("provider baseUrl is missing")
		}
		p.BaseUrl = strings.TrimRight(p.BaseUrl, "/")
		for toolName, t := range p.Tools {
			if t.File != nil && *t.File == contract.GatewayFileModeUpload && t.Method != "" && strings.ToUpper(t.Method) != "POST" {
				return nil, fmt.Errorf("upload tool %s must POST", toolName)
			}
			if t.Method != "GET" {
				t.Method = "POST"
			}
			if !strings.HasPrefix(t.Path, "/") {
				return nil, errors.New("tool path must start with /")
			}
			for field, pattern := range t.PathPatterns {
				if _, err := regexp.Compile(pattern); err != nil {
					return nil, fmt.Errorf("tool %s path pattern %s: %w", toolName, field, err)
				}
			}
			if t.File != nil {
				switch {
				case !t.File.Valid():
					return nil, fmt.Errorf("tool %s file mode is unknown", toolName)
				case *t.File == contract.GatewayFileModeUpload && (len(t.Inject) > 0 || t.Idempotent || t.UsageOnce != nil):
					// The multipart body holds the file part only (an inject field would be dropped); an upload is never
					// sent twice, so it fails over to no other key.
					return nil, fmt.Errorf("upload tool %s must inject nothing, not be idempotent and not count usage once", toolName)
				case *t.File == contract.GatewayFileModeDownload && (t.FileFrom == nil || t.FileFrom.Url == "" || t.FileFrom.Bytes == ""):
					return nil, fmt.Errorf("download tool %s needs fileFrom url and bytes", toolName)
				}
			}
			if t.Unit == "" {
				t.Unit = "calls"
			}
			p.Tools[toolName] = t
		}
		s.Providers[name] = p
	}
	for name, c := range s.Connections {
		if !isName(name) {
			return nil, errors.New("connection name is invalid")
		}
		if c.Keys == nil {
			return nil, errors.New("connection keys are missing")
		}
		for _, k := range c.Keys {
			if !isHex(k.Id, 12) {
				return nil, errors.New("key id is not a fingerprint")
			}
			if k.State == "" {
				return nil, errors.New("key state is missing")
			}
		}
		if _, ok := s.Providers[c.Provider]; !ok {
			return nil, fmt.Errorf("connection %s names an unknown provider", name)
		}
		if l := c.Limits; l != nil {
			// A value below 1 takes its default; the other limits and connections stay as published.
			for _, v := range []**int64{&l.FileBytes, &l.ReplyBytes, &l.StallMs, &l.AnswerMs} {
				if *v != nil && **v < 1 {
					logf("connection %s: a limit below 1 is ignored; its default applies", name)
					*v = nil
				}
			}
			if *l == (contract.GatewayLimits{}) {
				c.Limits = nil // no limits: defaults everywhere
				s.Connections[name] = c
			}
		}
		if c.Intake == "" {
			c.Intake = "all"
			s.Connections[name] = c
		}
	}
	s.tokens = map[string]*token{}
	for _, rt := range s.Tokens {
		t := &token{GatewayRunToken: rt}
		if !isHex(t.Sha256, 64) {
			return nil, errors.New("token hash is not SHA-256")
		}
		if t.ExecutionId == "" {
			return nil, errors.New("token executionId is missing")
		}
		for scope, value := range map[string]string{"person": t.DirectedBy, "project": t.ProjectId, "experiment": t.ExperimentId} {
			if value != "" {
				t.scopes = append(t.scopes, scope+":"+value)
			}
		}
		s.tokens[t.Sha256] = t
	}
	s.revision = sha(string(data))[:12]
	return s, nil
}

func readSnapshot(path string) ([]byte, *snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	s, err := parseSnapshot(data)
	return data, s, err
}

func (g *gateway) loadValues(s *snapshot) {
	for name, c := range s.Connections {
		for _, k := range c.Keys {
			if _, ok := g.values.Load(k.Id); ok {
				continue
			}
			text, err := os.ReadFile(filepath.Join(g.config.KeysDir, name, k.Id))
			value := strings.TrimSpace(string(text))
			switch {
			case err != nil:
				logf("key %s/%s: %v", name, k.Id, err)
			// A file whose value does not match its fingerprint is never used under that name.
			case sha(value)[:12] != k.Id:
				logf("key file %s/%s does not match its fingerprint", name, k.Id)
			default:
				g.values.Store(k.Id, value)
			}
		}
	}
}

func (g *gateway) value(id string) (string, bool) {
	v, ok := g.values.Load(id)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// refresh adopts a newer valid snapshot; an invalid one is refused and the current one stays.
func (g *gateway) refresh(force bool) {
	info, err := os.Stat(g.config.Snapshot)
	if err != nil {
		return
	}
	g.seenLock.Lock()
	// The coordinator replaces the file by rename, so a new file is a new inode even within one mtime tick.
	if !force && g.seen != nil && os.SameFile(info, g.seen) && info.ModTime().Equal(g.seen.ModTime()) && info.Size() == g.seen.Size() {
		g.seenLock.Unlock()
		return
	}
	g.seen = info
	g.seenLock.Unlock()
	data, s, err := readSnapshot(g.config.Snapshot)
	if err != nil {
		g.rejected.Add(1)
		logf("snapshot refused, keeping the last good one: %v", err)
		return
	}
	if err := writeAtomic(filepath.Join(g.config.StateDir, "snapshot.good.json"), data); err != nil {
		logf("retain snapshot: %v", err)
	}
	g.loadValues(s)
	g.snapshot.Store(s)
}

// authenticate finds the token's run, else rereads the snapshot once (a token minted after the last adoption).
// Rereads are coalesced: a caller that finds one running waits for it instead of starting another.
func (g *gateway) authenticate(bearer string) *token {
	hash := sha(bearer)
	if t := g.snapshot.Load().tokens[hash]; t != nil {
		return t
	}
	if g.reread.TryLock() {
		g.refresh(true)
	} else {
		g.reread.Lock()
	}
	g.reread.Unlock()
	return g.snapshot.Load().tokens[hash]
}

func (g *gateway) usable(c connection) []string {
	g.disabledLock.Lock()
	defer g.disabledLock.Unlock()
	var ids []string
	for _, k := range c.Keys {
		if d, off := g.disabled[k.Id]; k.State == "active" && (!off || k.EnabledAt != nil && *k.EnabledAt > d.at) {
			ids = append(ids, k.Id)
		}
	}
	return ids
}

func blockedScope(c connection, run *token) string {
	for _, b := range c.Blocked {
		if b == "connection" || contains(run.scopes, b) {
			return b
		}
	}
	return ""
}
