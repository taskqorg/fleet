package main

import "testing"

func TestCiRunner(t *testing.T) {
	s := func(v string) *string { return &v }
	for _, c := range []struct {
		j      ciJob
		name   string
		hosted bool
	}{
		{ciJob{RunnerGroup: s("GitHub Actions"), RunnerName: s("GitHub Actions 12"), Labels: []string{"ubuntu-latest"}}, "ubuntu-latest", true},
		{ciJob{RunnerGroup: s("fleet-pds18-trusted-main"), RunnerName: s("fleet-pds18-kvm-1"), Labels: []string{"self-hosted", "fleet-pds18"}}, "fleet-pds18-kvm-1", false},
		{ciJob{Labels: []string{"ubuntu-latest"}}, "ubuntu-latest", true}, // queued: no runner yet
		{ciJob{Labels: []string{"self-hosted", "fleet-pds18"}}, "fleet-pds18", false},
		{ciJob{RunnerGroup: s("GitHub Actions"), RunnerName: s("GitHub Actions 7")}, "GitHub-hosted", true}, // no labels
		{ciJob{}, "unknown", true},
	} {
		if n, h := ciRunner(c.j); n != c.name || h != c.hosted {
			t.Errorf("%+v: got %s %v", c.j, n, h)
		}
	}
}
