package main

import (
	"os"
	"path/filepath"
	"testing"

	"fleet/contract"
)

// Requirement: a key for a provider fills its keyless connection instead of a second one (an explicit other name is
// refused unless second), and connections_key_move moves a key's row and value file to another connection of the same
// provider without the value leaving the server.
func TestConnectionsKeyMoveAndKeylessFill(t *testing.T) {
	e := openTestCoordinator(t, false)
	secrets := t.TempDir()
	t.Setenv("FLEET_SECRET_DIR", secrets)
	h := e.ownerHeaders()
	var ignore map[string]any
	e.opInto(t, "connections_create", map[string]any{"name": "openai", "provider": "openai"}, h, &ignore)
	if code, _ := e.callJSON(t, "POST", "/api/ops/access_onboard", map[string]any{"kind": "key", "provider": "openai", "connection": "openai-2"}, h); code != 409 {
		t.Fatalf("a second name beside a keyless openai: %d", code)
	}
	var on contract.AccessOnboardReply
	e.opInto(t, "access_onboard", map[string]any{"kind": "key", "provider": "openai"}, h, &on)
	if on.Connection == nil || *on.Connection != "openai" || on.Created == nil || *on.Created {
		t.Fatalf("fill: %+v", on)
	}
	e.opInto(t, "access_onboard", map[string]any{"kind": "key", "provider": "openai", "connection": "openai-2", "second": true}, h, &on)
	if on.Connection == nil || *on.Connection != "openai-2" || !*on.Created {
		t.Fatalf("second: %+v", on)
	}
	var k connKey
	e.opInto(t, "connections_key_add", map[string]any{"connection": "openai-2", "value": "sk-invented-key-value-0001"}, h, &k)
	var moved contract.ConnectionsKeyMoveReply
	e.opInto(t, "connections_key_move", map[string]any{"connection": "openai-2", "key": k.Fingerprint, "to": "openai"}, h, &moved)
	if moved.Connection != "openai" || moved.Key != k.Fingerprint {
		t.Fatalf("move: %+v", moved)
	}
	if b, err := os.ReadFile(filepath.Join(secrets, "connections", "openai", k.Fingerprint)); err != nil || string(b) != "sk-invented-key-value-0001\n" {
		t.Fatalf("value at the target: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(secrets, "connections", "openai-2", k.Fingerprint)); !os.IsNotExist(err) {
		t.Fatalf("value left at the source: %v", err)
	}
	e.opInto(t, "connections_delete", map[string]any{"connection": "openai-2"}, h, &ignore)
	if code, _ := e.callJSON(t, "POST", "/api/ops/connections_key_move", map[string]any{"connection": "openai", "key": k.Fingerprint, "to": "nowhere"}, h); code != 404 {
		t.Fatalf("unknown target: %d", code)
	}
}
