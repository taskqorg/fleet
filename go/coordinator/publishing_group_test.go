package main

import (
	"testing"

	"fleet/contract"
)

// Requirement: configuration.md a host grant "<host>/<group>/*" places publishing for every project of the group;
// risk: a new project of the group needs a per-project host step, or a grant leaks to another host or group.
func TestHostCapsHaveRepoGroupGrant(t *testing.T) {
	caps := []string{contract.PublishingHostCapability("gitlab", "gitlab.ufobe.com", "gradus/*")}
	for slug, want := range map[string]bool{"gradus/dam": true, "gradus/sub/app": true, "other/dam": false, "gradusx/dam": false} {
		ref := contract.RepoRef{Provider: "gitlab", Host: "gitlab.ufobe.com", Slug: slug}
		if got := hostCapsHaveRepo(caps, ref); got != want {
			t.Errorf("%s: got %v, want %v", slug, got, want)
		}
	}
	if hostCapsHaveRepo(caps, contract.RepoRef{Provider: "gitlab", Host: "gitlab.com", Slug: "gradus/dam"}) {
		t.Error("group grant matched another host")
	}
}
