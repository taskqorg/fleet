package main

// The static publication program runs in the coordinator service, not in a
// compatibility host. Destination authority stays in the existing host-only
// project configuration. A durable per-run manifest fences every promotion.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"fleet/obs"
)

type publicationDestination struct{ Root, Authority, URL string }
type publicationFile struct {
	Name, ArtifactID, SHA256 string
	Size                     int64
}
type publicationManifest struct {
	Version, RunID, ProjectID, DefinitionSHA256, InputSHA256, Previous string
	Files                                                              []publicationFile
}

func publicationTarget(project string) (publicationDestination, error) {
	var out publicationDestination
	if project != "hello-world" && project != "hello-world-alt" {
		return out, fail(403, "Project has no static publication destination")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return out, err
	}
	b, err := os.ReadFile(filepath.Join(home, ".config/fleet/program-worker.prototype.json"))
	if err != nil {
		return out, fail(503, "Publication authority configuration unavailable")
	}
	var cfg struct {
		HostID   string `json:"hostId"`
		Projects map[string]struct {
			Root  string `json:"publicationRoot"`
			URL   string `json:"publicUrl"`
			Check string `json:"checkUrl"`
		} `json:"projects"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return out, fail(503, "Invalid publication authority configuration")
	}
	c, ok := cfg.Projects[project]
	root := os.Getenv("FLEET_TEST_SITE_ROOT")
	expected := filepath.Join(root, project)
	if !ok || root == "" || !filepath.IsAbs(root) || filepath.Clean(c.Root) != expected || cfg.HostID == "" || c.URL != "https://fleet.ufobe.com/test-sites/"+project+"/index.html" || c.Check != "http://127.0.0.1/test-sites/"+project+"/index.html" {
		return out, fail(503, "Publication destination does not match protected test-site binding")
	}
	// A symlinked destination would change the configured authority boundary.
	real, err := filepath.EvalSymlinks(expected)
	if err != nil || real != expected {
		return out, fail(503, "Publication root is unavailable or indirect")
	}
	info, e := os.Stat(expected)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return out, fail(503, "Publication root must be private to its existing service owner")
	}
	return publicationDestination{Root: expected, Authority: cfg.HostID, URL: c.URL}, nil
}

func publicationInputHash(files []publicationFile) string { return sha256hex(jsonBytes(files)) }

// publishStatic accepts only streams, never source filesystem paths. New files
// are exclusive and the rooted directory handle bounds every filesystem access.
// The caller holds the canonical project lock across staging/readback/reconcile.
func publishStatic(ctx context.Context, dest publicationDestination, manifest publicationManifest, open func(publicationFile) (io.ReadCloser, error), readback func(publicationFile) (io.ReadCloser, error)) (doc, error) {
	if !regexp.MustCompile(`^fleet-[a-f0-9]{32}$`).MatchString(manifest.Version) || manifest.InputSHA256 != publicationInputHash(manifest.Files) || len(manifest.Files) == 0 {
		return nil, fmt.Errorf("Invalid publication identity")
	}
	for _, f := range manifest.Files {
		if !testSiteFile.MatchString(f.Name) || !shaPattern.MatchString(f.SHA256) || f.Size < 0 {
			return nil, fmt.Errorf("Invalid publication file")
		}
	}
	root, err := publicationRoot(dest.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if _, err = root.Lstat("releases"); errors.Is(err, os.ErrNotExist) {
		err = root.Mkdir("releases", 0700)
	}
	if err != nil {
		return nil, err
	}
	st, err := root.Lstat("releases")
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("Publication releases directory rejected")
	}
	rel := "releases/" + manifest.Version
	if info, e := root.Lstat(rel); e == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("Publication release directory rejected")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	existing, err := publicationRegular(root, rel+"/manifest.json")
	if err == nil {
		b, e := io.ReadAll(existing)
		existing.Close()
		if e != nil {
			return nil, e
		}
		var prior publicationManifest
		if json.Unmarshal(b, &prior) != nil || prior.RunID != manifest.RunID || prior.ProjectID != manifest.ProjectID || prior.InputSHA256 != manifest.InputSHA256 || prior.DefinitionSHA256 != manifest.DefinitionSHA256 || prior.Version != manifest.Version || string(jsonBytes(prior.Files)) != string(jsonBytes(manifest.Files)) {
			return nil, fmt.Errorf("Publication manifest conflict; outcome unknown")
		}
		manifest = prior
	} else if errors.Is(err, os.ErrNotExist) {
		// An interrupted directory without its durable manifest is never promoted or rewritten.
		if err = root.Mkdir(rel, 0700); err != nil {
			return nil, fmt.Errorf("Publication staging incomplete; outcome unknown: %w", err)
		}
		manifest.Previous, err = root.Readlink("current")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil {
			target := manifest.Previous
			if filepath.IsAbs(target) {
				target, err = filepath.Rel(dest.Root, target)
				if err != nil {
					return nil, err
				}
			}
			prev, e := root.Open(target)
			if e != nil {
				return nil, fmt.Errorf("Previous publication pointer rejected: %w", e)
			}
			prev.Close()
		}

		for _, f := range manifest.Files {
			if !testSiteFile.MatchString(f.Name) {
				return nil, fmt.Errorf("Publication filename rejected")
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			src, e := open(f)
			if e != nil {
				return nil, e
			}
			dst, e := root.OpenFile(rel+"/"+f.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				src.Close()
				return nil, e
			}
			h := sha256.New()
			n, e := io.Copy(io.MultiWriter(dst, h), src)
			src.Close()
			if e == nil {
				e = dst.Sync()
			}
			ce := dst.Close()
			if e == nil {
				e = ce
			}
			if e != nil {
				return nil, e
			}
			if n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
				return nil, fmt.Errorf("Publication artifact integrity mismatch")
			}
		}
		mf, e := root.OpenFile(rel+"/manifest.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, e = mf.Write(jsonBytes(manifest))
		if e == nil {
			e = mf.Sync()
		}
		ce := mf.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			return nil, e
		}
		dir, e := root.Open(rel)
		if e != nil {
			return nil, e
		}
		e = dir.Sync()
		dir.Close()
		if e != nil {
			return nil, e
		}
	} else {
		return nil, err
	}
	if manifest.Previous != "" {
		target := manifest.Previous
		if filepath.IsAbs(target) {
			target, err = filepath.Rel(dest.Root, target)
			if err != nil {
				return nil, err
			}
		}
		prev, e := root.Open(target)
		if e != nil {
			return nil, fmt.Errorf("Previous pointer outside publication root")
		}
		info, e := prev.Stat()
		prev.Close()
		if e != nil || !info.IsDir() {
			return nil, fmt.Errorf("Previous publication is not a directory")
		}
	}
	for _, f := range manifest.Files {
		fd, e := publicationRegular(root, rel+"/"+f.Name)
		if e != nil {
			return nil, e
		}
		h := sha256.New()
		n, e := io.Copy(h, fd)
		fd.Close()
		if e != nil {
			return nil, e
		}
		if n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
			return nil, fmt.Errorf("Publication manifest bytes changed; outcome unknown")
		}
	}
	if _, e := root.Lstat(rel + "/failed"); e == nil {
		publicationRestore(root, rel, manifest.Previous, manifest.Version)
		return nil, fmt.Errorf("Previous publication attempt failed; not repeated")
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	parent, e := root.Open("releases")
	if e != nil {
		return nil, e
	}
	e = parent.Sync()
	parent.Close()
	if e != nil {
		return nil, e
	}
	current, err := root.Readlink("current")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if current != rel {
		if current != manifest.Previous {
			return nil, fmt.Errorf("Publication superseded; outcome unknown, not republished")
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		// The private project root and project advisory lock exclude other publishers.
		next := ".next-" + manifest.Version
		if err = root.Symlink(rel, next); err != nil {
			target, e := root.Readlink(next)
			if e != nil || target != rel {
				return nil, fmt.Errorf("Publication promotion uncertain")
			}
		}
		if err = root.Rename(next, "current"); err != nil {
			return nil, err
		}
		dir, e := root.Open(".")
		if e != nil {
			return nil, e
		}
		e = dir.Sync()
		dir.Close()
		if e != nil {
			return nil, e
		}
	}
	// Even after a restart, success requires actual current bytes via the protected
	// HTTP route. A failed readback leaves the durable effect explicit, never resent.
	failReadback := func(cause error) (doc, error) {
		f, e := root.OpenFile(rel+"/failed", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e == nil {
			_, e = f.Write([]byte("readback failed; do not republish\n"))
			if e == nil {
				e = f.Sync()
			}
			f.Close()
		}
		if e != nil {
			return nil, fmt.Errorf("Publication readback failed; recovery uncertain")
		}
		if e = publicationRestore(root, rel, manifest.Previous, manifest.Version); e != nil {
			return nil, e
		}
		return nil, cause
	}
	outputs := []any{}
	for _, f := range manifest.Files {
		r, e := readback(f)
		if e != nil {
			return failReadback(e)
		}
		h := sha256.New()
		n, e := io.Copy(h, r)
		r.Close()
		if e != nil {
			return failReadback(e)
		}
		if n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
			return failReadback(fmt.Errorf("Published readback integrity mismatch; previous version restored"))
		}
		outputs = append(outputs, doc{"name": f.Name, "artifactId": f.ArtifactID, "sha256": f.SHA256, "size": f.Size, "url": strings.TrimSuffix(dest.URL, "index.html") + f.Name})
	}
	selected, e := root.Readlink("current")
	if e != nil || selected != rel {
		return nil, fmt.Errorf("Publication superseded during readback; not restored")
	}
	return doc{"version": manifest.Version, "runId": manifest.RunID, "projectId": manifest.ProjectID, "inputSha256": manifest.InputSHA256, "previousVersion": manifest.Previous, "executorService": "coordinator", "publicationAuthority": dest.Authority, "url": dest.URL, "verified": true, "files": outputs}, nil
}

func runPublication(ctx context.Context, a *App, run doc, i int, def doc) (bool, error) {
	step := mapOf(anys(run["steps"])[i])
	row := stepRow(run, i)
	job := row + ":publication"
	email := astr(mapOf(run["requestedBy"])["email"])
	failStart := func(cause error) (bool, error) {
		return false, runTx(context.WithoutCancel(ctx), a, nil, true, "", func(q *Request) error {
			if step["startedAt"] == nil {
				step["startedAt"] = nowMs()
			}
			if e := exec(q, "UPDATE execution SET state='failed',ended_at=now(),problem='step:publication-start' WHERE id=$1 AND state='running'", job); e != nil {
				return e
			}
			if e := writeTotals(q, job); e != nil {
				return e
			}
			return failStep(q, run, i, "step:publication-start", cause.Error())
		})
	}
	person, err := a.resolveAccess(ctx, email)
	if err != nil || person == nil || !mayAccessProjectID(person, astr(run["projectId"])) {
		return failStart(fail(403, "Publication requester no longer has project access"))
	}
	q := backgroundRequest(ctx, a, nil)
	q.person = person
	var dest publicationDestination
	var files []publicationFile
	original := jsonBytes(run)
	err = runTx(ctx, a, q, true, "project:"+astr(run["projectId"]), func(q *Request) error {
		var restored doc
		if e := json.Unmarshal(original, &restored); e != nil {
			return e
		}
		for k := range run {
			delete(run, k)
		}
		for k, v := range restored {
			run[k] = v
		}
		step = mapOf(anys(run["steps"])[i])
		var e error
		files = nil
		dest, e = publicationTarget(astr(run["projectId"]))
		if e != nil {
			return e
		}
		host, e := hostByID(q, dest.Authority)
		if e != nil {
			return e
		}
		if host == nil {
			return fail(403, "Configured publication authority unavailable")
		}
		role := false
		for _, v := range anys(host["roles"]) {
			role = role || v == "publication"
		}
		if !role {
			return fail(403, "Configured publication authority has no publication role")
		}
		inputs, e := inputsFor(q, run, i)
		if e != nil {
			return e
		}
		for name, value := range mapOf(def["files"]) {
			id := ""
			for _, v := range inputs {
				f := mapOf(v)
				if f["path"] == value {
					id = astr(f["artifactId"])
					break
				}
			}
			if id == "" {
				return fail(409, "Publication source input missing: "+astr(value))
			}
			x, e := artifactRow(withTx(q), a, id)
			if e != nil {
				return e
			}
			if x.ProjectID == nil || *x.ProjectID != astr(run["projectId"]) || x.State != "stored" {
				return fail(403, "Publication requires stored same-project artifacts")
			}
			files = append(files, publicationFile{Name: name, ArtifactID: x.ID, SHA256: x.ContentHash, Size: x.Size})
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
		if step["state"] == "preparing" {
			step["state"], step["startedAt"], step["executionId"] = "running", nowMs(), job
			step["executorService"], step["publicationAuthority"] = "coordinator", dest.Authority
		}
		if e = putRun(q, run); e != nil {
			return e
		}
		_, _, _, revision := definitionScalars(run["definitions"], nil)
		_, e = insertExecution(q, doc{"id": job, "kind": "host_job", "state": "running", "projectId": run["projectId"], "parentExecutionId": row, "taskId": run["taskId"], "sessionId": run["sessionId"], "pipelineId": run["pipelineId"], "pipelineRevision": revision, "stepId": step["id"], "directedBy": email, "startedAt": step["startedAt"], "configRef": doc{"executorService": "coordinator", "publicationAuthority": dest.Authority, "program": "publish-static"}, "data": doc{"executorService": "coordinator", "publicationAuthority": dest.Authority, "program": "publish-static", "pipelineRunId": run["id"]}})
		return e
	})
	if err != nil {
		return failStart(err)
	}
	tp := ""
	_ = runTx(ctx, a, q, false, "", func(q *Request) error { e, e2 := get(q, job, "execution"); tp = astr(e["traceparent"]); return e2 })
	ctx, span := obs.Start(obs.WithRun(obs.WithRemote(ctx, tp), obs.Run{ProjectID: astr(run["projectId"]), TaskID: astr(run["taskId"]), ExecutionID: job, Actor: email}), "program.publish-static", obs.KindInternal)
	defer func() { span.End(err) }()
	_ = runTx(ctx, a, nil, true, "", func(q *Request) error {
		return exec(q, "UPDATE execution SET data=data||$2::jsonb WHERE id=$1", job, jsonBytes(doc{"traceparent": obs.Traceparent(ctx)}))
	})
	var output doc
	// Persist the job before effects. The canonical project lock serializes all
	// publications and retained manifests reconcile any transaction/restart retry.
	err = runTx(ctx, a, q, true, "project:"+astr(run["projectId"]), func(q *Request) error {
		if e := refreshRequestAccess(q); e != nil {
			return e
		}
		if !mayAccessProjectID(q.person, astr(run["projectId"])) {
			return fail(403, "Publication project access revoked")
		}
		currentTarget, e := publicationTarget(astr(run["projectId"]))
		if e != nil {
			return e
		}
		if currentTarget != dest {
			return fail(409, "Publication destination changed before effect")
		}
		authority, e := hostByID(q, dest.Authority)
		if e != nil {
			return e
		}
		allowed := false
		for _, role := range anys(authority["roles"]) {
			allowed = allowed || role == "publication"
		}
		if !allowed {
			return fail(403, "Publication authority revoked")
		}
		m := publicationManifest{Version: "fleet-" + sha256hex([]byte(job))[:32], RunID: astr(run["id"]), ProjectID: astr(run["projectId"]), DefinitionSHA256: astr(run["definitionSha256"]), Files: files, InputSHA256: publicationInputHash(files)}
		output, err = publishStatic(ctx, dest, m, func(f publicationFile) (io.ReadCloser, error) {
			source, open, e := artifactRead(withTx(q), a, f.ArtifactID, nil)
			if e != nil {
				return nil, e
			}
			if source.ProjectID == nil || *source.ProjectID != astr(run["projectId"]) {
				return nil, fail(403, "Publication source project changed")
			}
			return open()
		}, func(f publicationFile) (io.ReadCloser, error) {
			port := os.Getenv("PORT")
			if port == "" {
				port = "8787"
			}
			n, e := strconv.Atoi(port)
			if e != nil || n < 1 || n > 65535 {
				return nil, fmt.Errorf("Invalid coordinator loopback port")
			}
			r, e := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:"+port+"/test-sites/"+m.ProjectID+"/"+f.Name, nil)
			if e != nil {
				return nil, e
			}
			r.Header.Set("Authorization", "Bearer "+a.browserSecret)
			r.Header.Set("X-Fleet-Person", email)
			client := http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, e := client.Do(r)
			if e != nil {
				return nil, fmt.Errorf("Publication readback unavailable")
			}
			if resp.StatusCode != 200 {
				resp.Body.Close()
				return nil, fmt.Errorf("Publication readback HTTP %d", resp.StatusCode)
			}
			return resp.Body, nil
		})
		return err
	})
	endCtx := context.WithoutCancel(ctx)
	persistErr := runTx(endCtx, a, nil, true, "", func(q *Request) error {
		state := "succeeded"
		var problem any
		if err != nil {
			state = "failed"
			problem = "unknown:publication"
		}
		if e := exec(q, "UPDATE execution SET state=$2::execution_state,ended_at=now(),problem=$3,data=data||$4::jsonb WHERE id=$1", job, state, problem, jsonBytes(doc{"output": output})); e != nil {
			return e
		}
		if e := writeTotals(q, job); e != nil {
			return e
		}
		if err != nil {
			return failStep(q, run, i, "unknown:publication", err.Error())
		}
		return completeStep(q, run, i, output, nil)
	})
	if err == nil {
		err = persistErr
	}
	return err == nil, persistErr
}

// Regular files only; hardlinks could otherwise disclose bytes outside the root.
func publicationRegular(root *os.Root, name string) (*os.File, error) {
	st, e := root.Lstat(name)
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("Publication non-regular file rejected")
	}
	sys := reflect.ValueOf(st.Sys())
	if sys.Kind() == reflect.Pointer {
		sys = sys.Elem()
	}
	if sys.Kind() == reflect.Struct {
		n := sys.FieldByName("Nlink")
		if n.IsValid() && n.CanUint() && n.Uint() != 1 {
			return nil, fmt.Errorf("Publication hardlink rejected")
		}
	}
	f, e := root.Open(name)
	if e != nil {
		return nil, e
	}
	actual, e := f.Stat()
	if e != nil || !os.SameFile(st, actual) {
		f.Close()
		return nil, fmt.Errorf("Publication file changed during open")
	}
	return f, nil
}
func publicationRestore(root *os.Root, own, previous, version string) error {
	current, e := root.Readlink("current")
	if e != nil {
		return e
	}
	if current == previous {
		return nil
	}
	if current != own {
		return fmt.Errorf("Publication changed; previous pointer not restored")
	}
	if previous == "" {
		return root.Remove("current")
	}
	next := ".restore-" + version
	if e = root.Symlink(previous, next); e != nil {
		target, err := root.Readlink(next)
		if err != nil || target != previous {
			return e
		}
	}
	if e = root.Rename(next, "current"); e != nil {
		return e
	}
	d, e := root.Open(".")
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// Walk anchored directory handles and compare the opened identity with each
// no-symlink observation. A renamed/symlink-swapped ancestor cannot redirect a
// later open into a different configured root.
func publicationRoot(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("Publication root must be absolute and clean")
	}
	volume := filepath.VolumeName(path)
	root, e := os.OpenRoot(volume + string(filepath.Separator))
	if e != nil {
		return nil, e
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, volume+string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		st, e := root.Lstat(component)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, fmt.Errorf("Publication root ancestor rejected")
		}
		next, e := root.OpenRoot(component)
		if e != nil {
			root.Close()
			return nil, e
		}
		f, e := next.Open(".")
		if e != nil {
			root.Close()
			next.Close()
			return nil, e
		}
		actual, e := f.Stat()
		f.Close()
		if e != nil || !os.SameFile(st, actual) {
			root.Close()
			next.Close()
			return nil, fmt.Errorf("Publication root ancestor changed")
		}
		root.Close()
		root = next
	}
	return root, nil
}
func publicationSiteFile(base, name string) (*os.File, error) {
	root, e := publicationRoot(base)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	target, e := root.Readlink("current")
	if e != nil {
		return nil, e
	}
	if filepath.IsAbs(target) {
		target, e = filepath.Rel(base, target)
		if e != nil {
			return nil, e
		}
	}
	return publicationRegular(root, filepath.Join(target, name))
}
