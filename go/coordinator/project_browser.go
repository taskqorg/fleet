package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"fleet/contract"
)

func init() {
	hostRoute(contract.RouteBrowserAccess, productionBrowserAccess)
}

func productionBrowserAccess(q *Request, request *contract.BrowserAccessRequest) (contract.BrowserAccessReply, error) {
	var empty contract.BrowserAccessReply
	projectID := request.ProjectId
	if !filepath.IsLocal(projectID) || strings.ContainsAny(projectID, `/\`) || projectID == "." {
		return empty, nil
	}
	granted, err := productionBrowserGranted(q, projectID)
	if err != nil || !granted {
		return empty, err
	}
	var access contract.BrowserAccessReply
	if projectID == contract.ProductionBrowserProject {
		access, err = readProductionBrowserAccess(secretDir())
	} else {
		access, err = readGenericBrowserAccess(secretDir(), projectID)
	}
	if err != nil {
		return empty, fail(http.StatusServiceUnavailable, err.Error())
	}
	return access, nil
}

func productionBrowserGranted(q *Request, projectID string) (bool, error) {
	holder := q.peopleSnapshot().person(q.host.Holder)
	if holder == nil || !holder.Owner {
		return false, nil
	}
	settings, err := projectSettingsByID(q, projectID)
	if err != nil {
		var missing *HTTPError
		if errors.As(err, &missing) && missing.Code == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	project := agentView[contract.ProjectContext](settings)
	return project.Execution != nil && slices.Contains(project.Execution.Tools, "browser") &&
		slices.ContainsFunc(project.Environments, func(env contract.ProjectEnvironment) bool {
			return deref(env.HostId) == q.hostID()
		}), nil
}

func readGenericBrowserAccess(root, projectID string) (contract.BrowserAccessReply, error) {
	var access contract.BrowserAccessReply
	unready := errors.New("Coordinator project browser binding or credential is invalid")
	if root == "" {
		return access, nil
	}
	if !filepath.IsAbs(root) {
		return access, unready
	}
	dir := filepath.Join(root, "project-browser")
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return access, nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return access, unready
	}
	raw, err := readPrivateBrowserFile(filepath.Join(dir, projectID+".binding.json"))
	if errors.Is(err, os.ErrNotExist) {
		return access, nil
	}
	var grant contract.BrowserBindingProduction
	if err != nil || json.Unmarshal(raw, &grant) != nil || !filepath.IsLocal(grant.AccessFile) ||
		strings.ContainsAny(grant.AccessFile, `/\`) || grant.AccessFile == "." {
		return access, unready
	}
	env, err := readPrivateBrowserFile(filepath.Join(dir, grant.AccessFile))
	if err != nil {
		return access, unready
	}
	access.Production, err = grant.Resolve(string(env))
	reply, marshalErr := json.Marshal(access)
	if err != nil || marshalErr != nil || len(reply) >= contract.BrowserAccessBytes {
		return contract.BrowserAccessReply{}, unready
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
	raw, err := readPrivateBrowserFile(filepath.Join(dir, "site-agents.json"))
	if err != nil || json.Unmarshal(raw, &access) != nil || access.Production != nil ||
		!validBrowserCredential(access.ClientId) || !validBrowserCredential(access.ClientSecret) {
		return contract.BrowserAccessReply{}, unready
	}
	return access, nil
}

func readPrivateBrowserFile(path string) ([]byte, error) {
	unready := errors.New("browser authority unavailable")
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode() != 0600 {
		return nil, unready
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, unready
	}
	defer file.Close()
	if opened, err := file.Stat(); err != nil || opened.Mode() != 0600 || !os.SameFile(st, opened) {
		return nil, unready
	}
	raw, err := io.ReadAll(io.LimitReader(file, contract.BrowserAccessBytes+1))
	if err != nil || len(raw) > contract.BrowserAccessBytes {
		return nil, unready
	}
	return raw, nil
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
