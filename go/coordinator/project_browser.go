package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"fleet/contract"
)

func init() {
	hostRoute(contract.RouteBrowserAccess, productionBrowserAccess)
}

func productionBrowserAccess(q *Request, request *contract.BrowserAccessRequest) (contract.BrowserAccessReply, error) {
	var empty contract.BrowserAccessReply
	holder := q.peopleSnapshot().person(q.host.Holder)
	if request.ProjectId != contract.ProductionBrowserProject || holder == nil || !holder.Owner {
		return empty, nil
	}
	settings, err := projectSettingsByID(q, contract.ProductionBrowserProject)
	if err != nil {
		var missing *HTTPError
		if errors.As(err, &missing) && missing.Code == http.StatusNotFound {
			return empty, nil
		}
		return empty, err
	}
	project := agentView[contract.ProjectContext](settings)
	if project.Execution == nil || !slices.Contains(project.Execution.Tools, "browser") ||
		!slices.ContainsFunc(project.Environments, func(env contract.ProjectEnvironment) bool {
			return deref(env.HostId) == q.hostID()
		}) {
		return empty, nil
	}
	access, err := readProductionBrowserAccess(secretDir())
	if err != nil {
		return empty, fail(http.StatusServiceUnavailable, err.Error())
	}
	return access, nil
}

func readProductionBrowserAccess(root string) (contract.BrowserAccessReply, error) {
	var access contract.BrowserAccessReply
	unready := errors.New("Coordinator production browser credential is missing or invalid")
	if !filepath.IsAbs(root) {
		return access, unready
	}
	dir := filepath.Join(root, "project-browser")
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		return access, unready
	}
	path := filepath.Join(dir, "site-agents.json")
	st, err = os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return access, unready
	}
	file, err := os.Open(path)
	if err != nil {
		return access, unready
	}
	defer file.Close()
	if opened, err := file.Stat(); err != nil || !os.SameFile(st, opened) {
		return access, unready
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &access) != nil ||
		!validBrowserCredential(access.ClientId) || !validBrowserCredential(access.ClientSecret) {
		return contract.BrowserAccessReply{}, unready
	}
	return access, nil
}

func validBrowserCredential(value string) bool {
	if len(value) == 0 || len(value) > 512 {
		return false
	}
	for _, char := range value {
		if char <= ' ' || char >= 127 {
			return false
		}
	}
	return true
}
