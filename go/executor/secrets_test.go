package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Requirement: the Access service token goes only to a configured store tunnel. Risk: a presigned URL the coordinator
// hands out (or a compromised store record) points elsewhere and receives the host's Access credentials.
func TestAccessHeadersOnlyForStoreHosts(t *testing.T) {
	t.Setenv("FLEET_STORE_ENDPOINT_HOSTS", "fleet-store-pds18.ufobe.com, .stores.example")
	cfg := filepath.Join(t.TempDir(), "store.json")
	os.WriteFile(cfg, []byte(`{"endpoint":"https://fleet-store-mac.ufobe.com"}`), 0o600)
	t.Setenv("FLEET_STORE_CONFIG", cfg)
	for u, want := range map[string]bool{
		"https://fleet-store-pds18.ufobe.com/fleet/k?X-Amz-Signature=x": true,
		"https://fleet-store-mac.ufobe.com/fleet/k":                     true,
		"https://a.stores.example/k":                                    true,
		"https://evil.example/k":                                        false,
		"https://fleet-store-pds18.ufobe.com.evil.example/k":            false,
		"http://fleet-store-pds18.ufobe.com/k":                          false,
		"http://127.0.0.1:3900/k":                                       false,
	} {
		if accessAllowed(u) != want {
			t.Fatal(u)
		}
	}
}

// Requirement: the run token reaches the connection servers only, through the private run file. Risk: the agent reads
// the token from its own environment and calls the gateway outside the granted tools.
func TestRunFileLoadsOnlyConnectionKeys(t *testing.T) {
	f := filepath.Join(t.TempDir(), "run.env")
	os.WriteFile(f, []byte("FLEET_RUN_TOKEN=tok\nFLEET_GATEWAY_URL=https://gw\nPATH=/evil\n"), 0o600)
	path := os.Getenv("PATH")
	t.Setenv("FLEET_RUN_TOKEN", "")
	t.Setenv("FLEET_GATEWAY_URL", "")
	if err := loadRunFile(f); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FLEET_RUN_TOKEN") != "tok" || os.Getenv("FLEET_GATEWAY_URL") != "https://gw" || os.Getenv("PATH") != path {
		t.Fatal("run file keys")
	}
}
