package main

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"fleet/contract"
	"fleet/errtrack"
)

type siteAccessHealth struct {
	mu        sync.Mutex
	last      time.Time
	busy      bool
	proof     map[siteObservationKey]contract.BrowserTargetObservation
	requested []contract.BrowserTargetRef
}

type siteObservationKey struct {
	project, target, execution string
}

// siteBrowserFence stays with the trusted worker. The browser gets a boolean via its owner socket.
type siteBrowserFence struct {
	Coordinator string                        `json:"coordinator"`
	Token       string                        `json:"token"`
	Request     contract.BrowserAccessRequest `json:"request"`
}

func (f *siteBrowserFence) current() bool {
	reply, status := requestBrowserAccess(context.Background(), f.Coordinator, f.Token, f.Request)
	return status == http.StatusOK && siteManaged(reply.Managed) &&
		reply.Generation != nil && *reply.Generation == str0(f.Request.Generation)
}

func siteManaged(b *bool) bool { return b != nil && *b }

func (e *executor) recordSiteProbe(project, execution string, reply contract.BrowserAccessReply, reason string, elapsed time.Duration) {
	if reply.TargetId == nil || reply.Generation == nil || reply.IssuedAt == nil {
		return
	}
	observation := contract.BrowserTargetObservation{ProjectId: project, TargetId: *reply.TargetId, Generation: *reply.Generation,
		StartedAt: *reply.IssuedAt, CheckedAt: *reply.IssuedAt + elapsed.Milliseconds(), ReasonCode: reason}
	if execution != "" {
		observation.ExecutionId = &execution
	}
	key := siteObservationKey{project, *reply.TargetId, execution}
	health := &e.siteHealth
	health.mu.Lock()
	defer health.mu.Unlock()
	if health.proof == nil {
		health.proof = map[siteObservationKey]contract.BrowserTargetObservation{}
	}
	if prior, ok := health.proof[key]; !ok || observation.StartedAt >= prior.StartedAt {
		health.proof[key] = observation
	}
}

func (e *executor) probeSiteTarget(ref contract.BrowserTargetRef) {
	request := contract.BrowserAccessRequest{Managed: new(true), ProjectId: ref.ProjectId, TargetId: &ref.TargetId}
	reply, status := requestBrowserAccess(e.baseCtx(), e.url, e.token, request)
	if status != http.StatusOK || reply.Production == nil || reply.TargetId == nil || *reply.TargetId != ref.TargetId {
		return
	}
	started := time.Now()
	reason := "runtime_unavailable"
	authority, err := newBrowserAuthority(M{"projectId": ref.ProjectId}, reply)
	if err == nil {
		home, err := os.MkdirTemp("", "fleet-site-access-check-")
		if err == nil {
			defer os.RemoveAll(home)
			reason = managedSiteProbeReason(browserProbe(authority.Config, home, authority))
		}
	}
	if e.baseCtx().Err() == nil {
		e.recordSiteProbe(ref.ProjectId, "", reply, reason, time.Since(started))
	}
}

func managedSiteProbeReason(err error) string {
	if err == nil {
		return "verified"
	}
	var failure browserProbeError
	if errors.As(err, &failure) {
		return string(failure)
	}
	return "probe_failed"
}

// Triggered both by normal host observations and by explicit project environment checks.
func (e *executor) projectSiteStatus(active []string) []contract.BrowserTargetObservation {
	health := &e.siteHealth
	health.mu.Lock()
	defer health.mu.Unlock()
	refresh := time.Since(health.last) >= contract.SiteAccessRefresh
	if !health.busy && (refresh || len(health.requested) > 0) {
		health.busy = true
		requested := health.requested
		health.requested = nil
		errtrack.Go("browser.site-access.check", func() {
			defer func() {
				health.mu.Lock()
				health.busy = false
				if refresh {
					health.last = time.Now()
				}
				health.mu.Unlock()
			}()
			if refresh {
				plan, status := requestBrowserAccess(e.baseCtx(), e.url, e.token, contract.BrowserAccessRequest{Managed: new(true)})
				if status == http.StatusOK && siteManaged(plan.Managed) {
					requested = append(requested, plan.Targets...)
				}
			}
			seen := map[contract.BrowserTargetRef]bool{}
			for _, ref := range requested {
				if e.baseCtx().Err() != nil {
					return
				}
				if !seen[ref] {
					seen[ref] = true
					e.probeSiteTarget(ref)
				}
			}
		})
	}
	if len(health.proof) == 0 {
		return nil
	}
	cutoff := time.Now().Add(-contract.SiteAccessEvidenceAge).UnixMilli()
	out := make([]contract.BrowserTargetObservation, 0, len(health.proof))
	for key, proof := range health.proof {
		if proof.StartedAt < cutoff && (key.execution == "" || !slices.Contains(active, key.execution)) {
			delete(health.proof, key)
			continue
		}
		out = append(out, proof)
	}
	slices.SortFunc(out, func(a, b contract.BrowserTargetObservation) int {
		return cmp.Or(cmp.Compare(a.ProjectId, b.ProjectId), cmp.Compare(a.TargetId, b.TargetId),
			cmp.Compare(str0(a.ExecutionId), str0(b.ExecutionId)))
	})
	return out
}

// Request a fresh independent host observation. The legacy readiness job can finish or
// be cancelled without handing off a partial browser result.
func (e *executor) recheckProjectSites(payload any, project *contract.AgentSiteAccess) {
	if project == nil || project.Mode != contract.AgentSiteAccessModeWeb {
		return
	}
	e.siteHealth.mu.Lock()
	defer e.siteHealth.mu.Unlock()
	for _, target := range project.Targets {
		ref := contract.BrowserTargetRef{ProjectId: browserProject(payload), TargetId: target.Id}
		if !slices.Contains(e.siteHealth.requested, ref) {
			e.siteHealth.requested = append(e.siteHealth.requested, ref)
		}
	}
}

func (e *executor) prepareExecutionBrowser(payload any, execution, home string, declared bool) (M, *browserAuthority, error) {
	project := browserProject(payload)
	request := contract.BrowserAccessRequest{Managed: new(true), ProjectId: project, ExecutionId: &execution}
	reply, status := requestBrowserAccess(e.baseCtx(), e.url, e.token, request)
	if !declared && status == http.StatusOK && reply.Managed != nil && !*reply.Managed {
		// The coordinator confirmed that this project still uses the legacy binding.
		return e.prepareBrowser(payload, home)
	}
	if reply.Production != nil && siteManaged(reply.Managed) && reply.Generation != nil && reply.TargetId != nil {
		started := time.Now()
		reason := "runtime_unavailable"
		authority, err := newBrowserAuthority(payload, reply)
		if err == nil {
			reason = managedSiteProbeReason(browserProbe(authority.Config, home, authority))
			if reason == "verified" {
				request.Generation, request.TargetId = reply.Generation, reply.TargetId
				fence := &siteBrowserFence{Coordinator: e.url, Token: e.token, Request: request}
				if fence.current() {
					e.recordSiteProbe(project, execution, reply, reason, time.Since(started))
					authority.Fence = fence
					return authority.Config, authority, nil
				}
				return e.siteBrowserPreview(payload, home)
			}
		}
		e.recordSiteProbe(project, execution, reply, reason, time.Since(started))
	}
	if declared || siteManaged(reply.Managed) {
		return e.siteBrowserPreview(payload, home)
	}
	cfg, authority, err := e.siteBrowserPreview(payload, home)
	if cfg == nil && err == nil {
		err = errors.New("browser-production-authority-unavailable")
	}
	return cfg, authority, err
}

func (e *executor) siteBrowserPreview(payload any, home string) (M, *browserAuthority, error) {
	config, err := browserConfig(payload, "")
	if err != nil {
		// Setup findings stay advisory when this project has declared its site access intent.
		return nil, nil, nil
	}
	return config, nil, browserProbe(config, home, nil)
}
