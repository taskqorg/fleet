package main

import (
	"strings"
	"testing"

	"fleet/contract"
)

// owner-mac reports no gitlab.ufobe.com login, pds18 a working one, windows an executor too old to report.
func TestForgeLoginItem(t *testing.T) {
	key := "gitlab|gitlab.ufobe.com"
	reported := map[string]map[string]contract.ForgeLogin{
		"owner-mac": {},
		"pds18":     {key: {Provider: "gitlab", Host: "gitlab.ufobe.com", Ok: true}},
	}
	it := forgeLoginItem("gitlab", "gitlab.ufobe.com", map[string]bool{"owner-mac": true, "pds18": true, "windows": true}, []string{"dam"}, reported)
	if it.State != contract.AccessStateNeedsLogin || it.StateText != "owner-mac has no gitlab.ufobe.com login; works on pds18" {
		t.Fatalf("state %s %q", it.State, it.StateText)
	}
	if it.Next == nil || it.Next.Action != contract.AccessActionCheck || it.Guide == nil || !strings.Contains(*it.Guide.Login, "glab auth login --hostname gitlab.ufobe.com") {
		t.Fatalf("next %+v guide %+v", it.Next, it.Guide)
	}
	byHost := map[string]string{}
	for _, h := range it.Hosts {
		byHost[h.HostId] = h.Readiness
	}
	if byHost["owner-mac"] != "unavailable" || byHost["pds18"] != "ready" || byHost["windows"] != "unknown" {
		t.Fatalf("hosts %v", byHost)
	}
	ok := forgeLoginItem("gitlab", "gitlab.ufobe.com", map[string]bool{"pds18": true}, []string{"dam"}, reported)
	if ok.State != contract.AccessStateWorks || ok.Next != nil {
		t.Fatalf("works row %s %+v", ok.State, ok.Next)
	}
}
