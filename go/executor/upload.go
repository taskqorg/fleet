package main

// Artifact transfers: workers go through the `transfers` journal (uploads of collected output and artifact_save,
// artifact-copy and artifact-verify commands, artifact_get downloads). Bytes go straight between this host and a store;
// the coordinator only reserves, presigns and records. Each job resumes after a restart from its journal row.

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"fleet/contract"
	"fleet/errtrack"
)

// transferJob: a `transfers` row's body. Uploads (collected output, artifact_save, previews) carry the file;
// artifact-copy and artifact-verify carry the command payload (contract.ArtifactTransferPayload and the keys the worker
// reads) with the command and the spool folder; get and list carry the MCP call; done is the journaled per-part
// progress of an upload or copy.
type transferJob struct {
	Path        string          `json:"path"`
	Spool       string          `json:"spool"` // copy: the spool folder (an upload's true is read by the cleanup)
	Name        string          `json:"name"`
	MediaType   string          `json:"mediaType"`
	ExecutionId string          `json:"executionId"`
	DerivedFrom string          `json:"derivedFrom"`
	Role        string          `json:"role"`
	Required    bool            `json:"required"`
	ArtifactId  *string         `json:"artifactId"`
	StoreId     string          `json:"storeId"`
	Url         string          `json:"url"`
	StagingKey  *string         `json:"stagingKey"`
	Size        *uint64         `json:"size"`
	PartSize    *uint64         `json:"partSize"`
	Source      transferSource  `json:"source"`
	InputDir    string          `json:"inputDir"`
	Filter      *transferFilter `json:"filter"`
	Done        []donePart      `json:"done"`
	Command     jsonValue       `json:"command"` // present (even null): the job sends the command's final receipt
	Recoveries  int             `json:"recoveries"`
}

type transferSource struct {
	Url string `json:"url"`
}

type transferFilter struct {
	ExecutionId string `json:"executionId"`
	TaskId      string `json:"taskId"`
	MediaType   string `json:"mediaType"`
	Cursor      string `json:"cursor"`
}

// donePart: one journaled part, written as {partNumber, etag}.
type donePart struct {
	PartNumber uint64 `json:"partNumber"`
	Etag       string `json:"etag"`
}

// readJob: a body that is not a JSON object reads as an empty job.
func readJob(body string) transferJob { return typedRaw[transferJob]([]byte(body)) }

// orU64: *p, or def when p is nil.
func orU64(p *uint64, def uint64) uint64 {
	if p == nil {
		return def
	}
	return *p
}

// enqueue queues a job; id is also the artifact id of an upload, so a repeated reserve is the same reservation.
func enqueue(db execer, id, kind string, body any) error {
	_, err := db.Exec("INSERT OR IGNORE INTO transfers(id,kind,body,created_at) VALUES(?,?,?,?)", id, kind, enc(body), nowMs())
	return err
}

// accessToken reads a Cloudflare Access service token env file (CF_ACCESS_CLIENT_ID, CF_ACCESS_CLIENT_SECRET).
func accessToken(file string) *[2]string {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var id, secret *string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
		if k, v, ok := strings.Cut(line, "="); ok {
			v = unquote(v)
			if k == "CF_ACCESS_CLIENT_ID" {
				id = &v
			} else if k == "CF_ACCESS_CLIENT_SECRET" {
				secret = &v
			}
		}
	}
	if id == nil || secret == nil {
		return nil
	}
	return &[2]string{*id, *secret}
}

func accessFile() string {
	if f := os.Getenv("FLEET_CF_ACCESS_FILE"); f != "" {
		return f
	}
	if h := os.Getenv("HOME"); h != "" {
		return filepath.Join(h, ".config/fleet/cf-access-machine.env")
	}
	return ""
}

func isLoopback(url string) bool {
	rest := url
	if _, r, ok := strings.Cut(url, "://"); ok {
		rest = r
	}
	host := rest
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		host = rest[:i]
	}
	if strings.HasPrefix(host, "[") {
		host = strings.TrimPrefix(strings.SplitN(host, "]", 2)[0], "[")
	} else {
		host = strings.SplitN(host, ":", 2)[0]
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// accessAllowed: the Access service token goes only to a store tunnel: the host of this host's own store endpoint
// (FLEET_STORE_CONFIG) or a host in FLEET_STORE_ENDPOINT_HOSTS (comma-separated; ".example.com" matches subdomains), the
// list the coordinator accepts store endpoints from. Never to loopback or any other presigned URL.
func accessAllowed(raw string) bool {
	u, err := neturl.Parse(raw)
	if err != nil || u.Scheme != "https" || isLoopback(raw) {
		return false
	}
	h := u.Hostname()
	hosts := storeEndpointHosts()
	if own, err := neturl.Parse(storeConfig().endpoint()); err == nil && own.Hostname() != "" {
		hosts = append(hosts, own.Hostname())
	}
	for _, x := range hosts {
		x = strings.TrimSpace(x)
		if x != "" && (h == x || strings.HasPrefix(x, ".") && strings.HasSuffix(h, x)) {
			return true
		}
	}
	return false
}

type transferClient struct {
	url, token, host string
	access           *[2]string
	coordinator      *http.Client
	store            *http.Client
}

func newTransferClient(url, token, host string, access *[2]string) *transferClient {
	return &transferClient{url: url, token: token, host: host, access: access,
		coordinator: httpClient(60 * time.Second),
		// Parts are 32 MiB; a slow tunnel needs more than the outbox's 5 s.
		store: streamClient(20*time.Second, 300*time.Second)}
}

// failure: retry true is retried with backoff; false is permanent; recoverable true indicates a vanished staging
// object (NoSuchKey) or vanished multipart upload session (NoSuchUpload / Upload not found) that an upload job can
// recover from by clearing stale session state and re-reserving from the local file copy.
type failure struct {
	retry       bool
	recoverable bool
	refused     bool // the coordinator refused the reservation (4xx): never retried, and it fails the execution
	msg         string
}

func (f *failure) Error() string             { return f.msg }
func retryf(f string, a ...any) *failure     { return &failure{retry: true, msg: fmt.Sprintf(f, a...)} }
func permanentf(f string, a ...any) *failure { return &failure{msg: fmt.Sprintf(f, a...)} }

func isRecoverableUploadText(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "nosuchkey") ||
		strings.Contains(l, "nosuchupload") ||
		strings.Contains(l, "upload not found") ||
		strings.Contains(l, "no upload in progress")
}

// ioFailure: a transfer step's error as a failure, by the one classifier (outcome). Local filesystem path errors
// (such as a missing or unreadable local file) are permanent because *os.PathError wraps syscall.Errno which satisfies
// net.Error in Go's stdlib.
func ioFailure(err error) *failure {
	var pe *os.PathError
	if errors.As(err, &pe) && !errors.Is(err, syscall.EINTR) && !errors.Is(err, syscall.EAGAIN) {
		return permanentf("%s", err)
	}
	a := outcome(err)
	return &failure{retry: a.kind == answerRetry, msg: a.text}
}

func (c *transferClient) post(route string, body any) (json.RawMessage, *failure) {
	a := classify(postJSON(c.coordinator, c.url+route, c.token, body))
	observe(route, a)
	switch a.kind {
	case answerOK:
		return a.value, nil
	case answerEdge:
		return nil, retryf("coordinator edge answered %d (%s)", a.status, a.text)
	case answerRetry:
		if route != contract.RouteArtifactsRenew.Path && isRecoverableUploadText(a.text) {
			return nil, &failure{recoverable: true, msg: a.text}
		}
		return nil, retryf("%s", a.text)
	}
	// 409 no_store: no listed store is reachable now: wait and retry.
	if a.status == 409 && strings.Contains(a.text, "No reachable store") {
		return nil, retryf("409 %s", a.text)
	}
	if route == contract.RouteArtifactsRenew.Path {
		var problem struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal([]byte(a.text), &problem)
		return nil, &failure{recoverable: a.status == 409 && problem.Code == "no_such_key", msg: fmt.Sprintf("%d %s", a.status, a.text)}
	}
	msg := fmt.Sprintf("%d %s", a.status, a.text)
	return nil, &failure{recoverable: isRecoverableUploadText(a.text), msg: msg}
}

// Decode coordinator-owned fields once; opaque MCP results are still forwarded as raw JSON.
func transferReply[Req, Rep any](c *transferClient, r contract.HostRoute[Req, Rep], body Req) (Rep, *failure) {
	var value Rep
	route := r.Path
	raw, f := c.post(route, body)
	if f != nil {
		return value, f
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, retryf("coordinator %s reply: %s", route, err)
	}
	return value, nil
}

// storeDo: Access headers are added (never signed) unless the URL is loopback; a redirect is an edge condition of that
// store and is retried, never followed; 4xx is permanent. The caller closes the body.
func (c *transferClient) storeDo(method, url string, body []byte) (*http.Response, *failure) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return nil, permanentf("%s", err)
	}
	if c.access != nil && accessAllowed(url) {
		req.Header.Set("CF-Access-Client-Id", c.access[0])
		req.Header.Set("CF-Access-Client-Secret", c.access[1])
	}
	rest := url
	if _, r, ok := strings.Cut(url, "://"); ok {
		rest = r
	}
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		rest = rest[:i]
	}
	key := "store " + rest
	r, err := c.store.Do(req)
	if err != nil {
		return nil, retryf("%s", err)
	}
	switch statusClass(r.StatusCode, false) {
	case classEdge:
		to := location(r)
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		edgeRedirect(key, r.StatusCode, to)
		return nil, retryf("store redirected with %d to %s", r.StatusCode, to)
	case classOK:
		edgeOK(key)
		return r, nil
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if statusClass(r.StatusCode, false) == classReject {
		edgeOK(key)
		msg := fmt.Sprintf("store %d %s", r.StatusCode, b)
		return nil, &failure{recoverable: isRecoverableUploadText(msg), msg: msg}
	}
	return nil, retryf("store %d %s", r.StatusCode, b)
}

func (c *transferClient) putPart(url string, b []byte) (string, *failure) {
	r, f := c.storeDo("PUT", url, b)
	if f != nil {
		return "", f
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	e := r.Header.Get("ETag")
	if e == "" {
		return "", retryf("part without ETag")
	}
	return e, nil
}

func (c *transferClient) get(url string) (io.ReadCloser, *failure) {
	r, f := c.storeDo("GET", url, nil)
	if f != nil {
		return nil, f
	}
	return r.Body, nil
}

func hashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	return fmt.Sprintf("%x", h.Sum(nil)), n, err
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return hashReader(f)
}

// sendParts uploads the file parts to the reservation, commits, and, when the coordinator answers with a verify URL
// (this host owns the store), re-hashes over loopback.
func sendParts(c *transferClient, db *sql.DB, jobID, file, artifactID, storeID string, size, partSize uint64) (contract.ArtifactStateReply, *failure) {
	if partSize == 0 {
		partSize = 32 << 20
	}
	count := max((size+partSize-1)/partSize, 1)
	var bodyText string
	db.QueryRow("SELECT body FROM transfers WHERE id=?", jobID).Scan(&bodyText)
	done := readJob(bodyText).Done
	f, err := os.Open(file)
	if err != nil {
		return contract.ArtifactStateReply{}, ioFailure(err)
	}
	defer f.Close()
	var missing []uint64
	for n := uint64(1); n <= count; n++ {
		have := false
		for _, d := range done {
			if d.PartNumber == n {
				have = true
			}
		}
		if !have {
			missing = append(missing, n)
		}
	}
	for len(missing) > 0 {
		batch := missing[:min(16, len(missing))]
		missing = missing[len(batch):]
		req := contract.ArtifactPartsRequest{HostId: c.host, ArtifactId: artifactID, StoreId: optStr(storeID)}
		for _, n := range batch {
			req.PartNumbers = append(req.PartNumbers, int64(n))
		}
		urls, fl := transferReply(c, contract.RouteArtifactsParts, req)
		if fl != nil {
			return contract.ArtifactStateReply{}, fl
		}
		for _, p := range urls.Parts {
			n := uint64(p.PartNumber)
			if p.PartNumber <= 0 {
				return contract.ArtifactStateReply{}, permanentf("part number")
			}
			l := min(partSize, size-min(size, (n-1)*partSize))
			chunk := make([]byte, l)
			if _, err := f.ReadAt(chunk, int64((n-1)*partSize)); err != nil && !(errors.Is(err, io.EOF) && l == 0) {
				return contract.ArtifactStateReply{}, ioFailure(err)
			}
			// A failed part ends this run of the job: a retryable one is retried with the job's backoff, from the
			// parts already journaled as done.
			etag, fl := c.putPart(p.Url, chunk)
			if fl != nil {
				return contract.ArtifactStateReply{}, fl
			}
			done = append(done, donePart{n, etag})
			// progress: the backoff starts over
			db.Exec("UPDATE transfers SET body=json_set(body,'$.done',json(?)),attempts=0 WHERE id=?", enc(done), jobID)
		}
	}
	req := contract.ArtifactCommitRequest{HostId: c.host, ArtifactId: artifactID, StoreId: optStr(storeID)}
	for _, d := range done {
		n, etag := d.PartNumber, d.Etag
		req.Parts = append(req.Parts, contract.ArtifactPart{PartNumber: int64(n), Etag: &etag})
	}
	commit, fl := transferReply(c, contract.RouteArtifactsCommit, req)
	if fl != nil {
		return contract.ArtifactStateReply{}, fl
	}
	if v := commit.Verify; v != nil {
		a := artifactID
		if v.ArtifactId != "" {
			a = v.ArtifactId
		}
		return verifyArtifact(c, a, v.StoreId, v.Url, v.StagingKey)
	}
	return commit, nil
}

func verifyArtifact(c *transferClient, artifactID, storeID, url string, stagingKey *string) (contract.ArtifactStateReply, *failure) {
	r, f := c.get(url)
	if f != nil {
		return contract.ArtifactStateReply{}, f
	}
	digest, _, err := hashReader(r)
	r.Close()
	if err != nil {
		return contract.ArtifactStateReply{}, ioFailure(err)
	}
	return transferReply(c, contract.RouteArtifactsVerified, contract.ArtifactVerifiedRequest{HostId: c.host, ArtifactId: artifactID, StoreId: storeID, Sha256: digest, StagingKey: stagingKey})
}

const maxUploadRecoveries = 3

func clearUploadState(db *sql.DB, id string, body *transferJob, recoveries int) {
	body.ArtifactId = nil
	body.StoreId = ""
	body.Url = ""
	body.PartSize = nil
	body.Done = nil
	body.Recoveries = recoveries
	if db != nil {
		db.Exec("UPDATE transfers SET body=json_set(json_remove(body,'$.artifactId','$.storeId','$.url','$.partSize','$.done'),'$.recoveries',?),attempts=0 WHERE id=?", recoveries, id)
	}
}

// recoverUpload clears stale session state and re-hashes the local file copy for bounded recovery.
func recoverUpload(db *sql.DB, id, path string, body *transferJob, digest *string, size *int64, f *failure) (*failure, bool) {
	if !f.recoverable {
		return f, false
	}
	if body.Recoveries >= maxUploadRecoveries {
		return permanentf("upload recovery exhausted after %d attempts: %s", body.Recoveries, f.msg), false
	}
	clearUploadState(db, id, body, body.Recoveries+1)
	d, sz, err := hashFile(path)
	if err != nil {
		return ioFailure(err), false
	}
	*digest, *size = d, sz
	return nil, true
}

func runJob(c *transferClient, db *sql.DB, id, kind string, body transferJob) (any, *failure) {
	switch kind {
	case "upload":
		path := body.Path
		digest, size, err := hashFile(path)
		if err != nil {
			return nil, ioFailure(err)
		}
		required := body.Required
		for {
			if body.Url == "verifying" && body.ArtifactId != nil && *body.ArtifactId != "" {
				renew, f := transferReply(c, contract.RouteArtifactsRenew, contract.ArtifactLeaseRequest{HostId: c.host, ArtifactId: *body.ArtifactId, StoreId: optStr(body.StoreId)})
				if f != nil {
					if outErr, ok := recoverUpload(db, id, path, &body, &digest, &size, f); ok {
						continue
					} else {
						return nil, outErr
					}
				}
				switch renew.State {
				case "stored":
					return M{"artifactId": *body.ArtifactId, "state": "stored"}, nil
				case "pending", "verifying", "copying":
					return nil, retryf("awaiting sha256 verification")
				default:
					if outErr, ok := recoverUpload(db, id, path, &body, &digest, &size, &failure{recoverable: true, msg: "artifact verification failed (" + renew.State + ")"}); ok {
						continue
					} else {
						return nil, outErr
					}
				}
			}
			if body.ArtifactId == nil || *body.ArtifactId == "" {
				req := contract.ArtifactReserveRequest{HostId: c.host, ExecutionId: body.ExecutionId, ArtifactId: optStr(id), Name: body.Name, MediaType: body.MediaType,
					Size: int64(size), Sha256: digest, Required: &required, DerivedFrom: optStr(body.DerivedFrom), Role: optStr(body.Role)}
				r, f := transferReply(c, contract.RouteArtifactsReserve, req)
				if f != nil {
					if !f.retry {
						// A refused reservation (purged content, no store, a bad name): the coordinator said no on purpose.
						return nil, &failure{refused: true, msg: refusalText(f.msg)}
					}
					return nil, f
				}
				a := id
				if r.ArtifactId != "" {
					a = r.ArtifactId
				}
				partSize := uint64(32 << 20)
				if r.PartSize != nil && *r.PartSize > 0 {
					partSize = uint64(*r.PartSize)
				}
				storeID := str0(r.StoreId)
				body.ArtifactId = &a
				body.StoreId = storeID
				body.PartSize = &partSize
				if db != nil {
					db.Exec("UPDATE transfers SET body=json_set(body,'$.artifactId',?,'$.storeId',?,'$.partSize',?),response=? WHERE id=?",
						a, storeID, int64(partSize), enc(M{"artifactId": r.ArtifactId, "state": r.State}), id)
					wake(db, id)
				}
				switch r.State {
				case "stored":
					return M{"artifactId": r.ArtifactId, "state": "stored"}, nil
				case "failed":
					return nil, permanentf("artifact failed")
				}
			}
			a := str0(body.ArtifactId)
			if a == "" {
				a = id
			}
			partSize := orU64(body.PartSize, 32<<20)
			out, f := sendParts(c, db, id, path, a, "", uint64(size), partSize)
			if f != nil {
				if outErr, ok := recoverUpload(db, id, path, &body, &digest, &size, f); ok {
					continue
				} else {
					return nil, outErr
				}
			}
			if out.State != "stored" {
				// Keep local spool intact until SHA-256 verification succeeds on the remote store host.
				body.Url = "verifying"
				if db != nil {
					db.Exec("UPDATE transfers SET body=json_set(body,'$.url','verifying') WHERE id=?", id)
				}
				return nil, retryf("awaiting sha256 verification")
			}
			return M{"artifactId": a, "state": out.State}, nil
		}
	case "verify":
		return verifyArtifact(c, str0(body.ArtifactId), body.StoreId, body.Url, body.StagingKey)
	case "copy":
		// Download the source object next to the journal, then upload it to this host's store.
		tmp := filepath.Join(body.Spool, id+".part")
		if st, err := os.Stat(tmp); err != nil || uint64(st.Size()) != orU64(body.Size, ^uint64(0)) {
			src, f := c.get(body.Source.Url)
			if f != nil {
				return nil, f
			}
			out, err := os.Create(tmp)
			if err == nil {
				_, err = io.Copy(out, src)
				if cerr := out.Close(); err == nil {
					err = cerr
				}
			}
			src.Close()
			if err != nil {
				return nil, ioFailure(err)
			}
		}
		st, err := os.Stat(tmp)
		if err != nil {
			return nil, ioFailure(err)
		}
		out, f := sendParts(c, db, id, tmp, str0(body.ArtifactId), body.StoreId, uint64(st.Size()), orU64(body.PartSize, 32<<20))
		if f == nil || !f.retry {
			os.Remove(tmp)
		}
		return out, f
	case "get":
		r, f := transferReply(c, contract.RouteArtifactsGet, contract.HostArtifactGetRequest{HostId: c.host, ExecutionId: optStr(body.ExecutionId), ArtifactId: str0(body.ArtifactId)})
		if f != nil {
			return nil, f
		}
		if r.Url == nil {
			av := "unavailable"
			if r.Availability != "" {
				av = r.Availability
			}
			return nil, permanentf("artifact %s", av)
		}
		name := "artifact"
		if r.Name != "" {
			name = r.Name
		}
		name = name[strings.LastIndexByte(name, '/')+1:]
		safe := []byte(name)
		for i, b := range safe {
			if !(alnum(b) || b == '.' || b == '_' || b == '-') {
				safe[i] = '_'
			}
		}
		// ponytail: per byte, so a multibyte character becomes several '_' where Rust wrote one.
		name = string(safe)
		dir := body.InputDir
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, ioFailure(err)
		}
		tag := "x"
		if r.Id != "" {
			tag = r.Id
		}
		if len(tag) > 8 {
			tag = tag[:8]
		}
		// The folder is opened once without following a link, and the file is created relative to that descriptor.
		dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, permanentf("input folder is not a directory")
		}
		var target string
		var out *os.File
		for n := 0; n < 1000 && out == nil; n++ {
			file := tag + "-" + name
			if n > 0 {
				file = fmt.Sprintf("%s-%d-%s", tag, n, name)
			}
			if fd, err := unix.Openat(dfd, file, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600); err == nil {
				target, out = filepath.Join(dir, file), os.NewFile(uintptr(fd), file)
			}
		}
		unix.Close(dfd)
		if out == nil {
			return nil, permanentf("cannot create the input file")
		}
		defer out.Close()
		src, f := c.get(*r.Url)
		if f != nil {
			return nil, f
		}
		defer src.Close()
		if _, err := io.Copy(out, src); err != nil {
			return nil, ioFailure(err)
		}
		return M{"artifactId": r.Id, "path": target, "mediaType": r.MediaType, "size": r.Size}, nil
	case "list":
		req := contract.HostArtifactListRequest{HostId: c.host, ExecutionId: body.ExecutionId}
		if f := body.Filter; f != nil {
			req.Filter = &contract.HostArtifactListFilter{ExecutionId: optStr(f.ExecutionId), TaskId: optStr(f.TaskId), MediaType: optStr(f.MediaType), Cursor: optStr(f.Cursor)}
		}
		return c.post(contract.RouteArtifactsList.Path, req)
	}
	return nil, permanentf("unknown transfer kind %s", kind)
}

// transferRoutes: coordinator routes only transfers call.
var transferRoutes = []string{contract.RouteArtifactsReserve.Path, contract.RouteArtifactsParts.Path, contract.RouteArtifactsCommit.Path, contract.RouteArtifactsVerified.Path, contract.RouteArtifactsGet.Path, contract.RouteArtifactsList.Path, contract.RouteArtifactsRenew.Path}

// leased: the artifact a queued job holds a coordinator lease on, with its store when known.
func leased(kind, id string, body transferJob) (string, string, bool) {
	switch kind {
	case "upload":
		return id, "", true
	case "verify", "copy":
		if body.ArtifactId != nil {
			return *body.ArtifactId, body.StoreId, true
		}
	}
	return "", "", false
}

type afterJob func(c *transferClient, db *sql.DB, body M, response any)

// spawnTransfers: two lanes, so a verification never waits behind a long upload or copy; each takes one job at a time,
// oldest first. Retryable failures back off and stay queued. A third goroutine renews the coordinator lease of every
// queued or running job every 10 minutes.
func spawnTransfers(journal string, c *transferClient, after afterJob) error {
	for _, lane := range []string{"kind<>'verify'", "kind='verify'"} {
		db, err := openJournal(journal)
		if err != nil {
			return err
		}
		errtrack.Go("transfer.lane", func() { transferWork(db, c, lane, after) })
	}
	db, err := openJournal(journal)
	if err != nil {
		return err
	}
	errtrack.Go("transfer.renew", func() {
		for {
			time.Sleep(10 * time.Minute)
			type job struct{ id, kind, body string }
			var jobs []job
			if rs, err := db.Query("SELECT id,kind,body FROM transfers WHERE state IN ('queued','running')"); err == nil {
				for rs.Next() {
					var j job
					if rs.Scan(&j.id, &j.kind, &j.body) == nil {
						jobs = append(jobs, j)
					}
				}
				rs.Close()
			}
			if len(jobs) == 0 {
				edgeClearIdle(transferRoutes)
			}
			for _, j := range jobs {
				if a, s, ok := leased(j.kind, j.id, readJob(j.body)); ok {
					c.post(contract.RouteArtifactsRenew.Path, contract.ArtifactLeaseRequest{HostId: c.host, ArtifactId: a, StoreId: optStr(s)}) // best effort, not a delivery
				}
			}
		}
	})
	return nil
}

func transferWork(db *sql.DB, c *transferClient, lane string, after afterJob) {
	for {
		var id, kind, bodyText string
		var attempts int64
		err := db.QueryRow("SELECT id,kind,body,attempts FROM transfers WHERE state IN ('queued','running') AND "+lane+" AND next_at<=? ORDER BY created_at LIMIT 1", nowMs()).Scan(&id, &kind, &bodyText, &attempts)
		if err == nil {
			_, err = db.Exec("UPDATE transfers SET state='running' WHERE id=?", id)
		}
		if err != nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		body := readJob(bodyText)
		v, f := runJob(c, db, id, kind, body)
		var freshBody string
		if db.QueryRow("SELECT body,attempts FROM transfers WHERE id=?", id).Scan(&freshBody, &attempts) == nil {
			body = readJob(freshBody)
		} else {
			db.QueryRow("SELECT attempts FROM transfers WHERE id=?", id).Scan(&attempts) // a run that journaled parts reset it
		}
		// The terminal state and its outbound notices commit together. In this process a failed commit is retried as a
		// commit, not by running the job again (a crash before the commit leaves the row running, and the job runs again
		// after restart: transfers resume by their journal row). The cleanup callback runs only after the commit; a crash
		// between them skips previews and leaves the spool copy.
		for {
			err := dbExec{db}.tx(func(x execer) error { return settleJob(x, c.host, id, kind, body, attempts, v, f) })
			if err == nil {
				break
			}
			fmt.Fprintln(os.Stderr, "executor: transfer journal write failed; retrying:", err)
			time.Sleep(time.Second)
		}
		wake(db, id)
		if f == nil {
			after(c, db, decM(bodyText), v) // the cleanup and previews read the body as written (executor.go, preview.go)
		}
	}
}

// settleJob records a job's outcome: done or failed with the command's final receipt (and, for an upload that can never
// succeed, the artifact failure), or a retry with backoff.
// transferReceipt: the final receipt of an artifact-copy or artifact-verify command.
func transferReceipt(host string, cmd jsonValue, status contract.ReceiptStatus, failure string) contract.Receipt {
	c := typed[struct { // a command that is not an object names no command
		Id          string `json:"id"`
		ExecutionId string `json:"executionId"`
	}](cmd)
	r := contract.Receipt{HostId: host, CommandId: c.Id, ExecutionId: c.ExecutionId, Status: status}
	if failure != "" {
		r.Error = &failure
	}
	return r
}

func settleJob(x execer, host, id, kind string, body transferJob, attempts int64, v any, f *failure) error {
	cmd, hasCmd := body.Command, body.Command.Set
	switch {
	case f == nil:
		if _, err := x.Exec("UPDATE transfers SET state='done',response=? WHERE id=?", enc(v), id); err != nil {
			return err
		}
		if hasCmd {
			return queueTo(x, contract.RouteReceipts, transferReceipt(host, cmd, contract.ReceiptStatusCompleted, ""))
		}
	case f.retry:
		_, err := x.Exec("UPDATE transfers SET attempts=attempts+1,next_at=?,response=? WHERE id=?", nowMs()+backoff(attempts), enc(M{"state": "retrying", "error": f.msg}), id)
		return err
	default:
		if _, err := x.Exec("UPDATE transfers SET state='failed',response=? WHERE id=?", enc(M{"state": "failed", "error": f.msg}), id); err != nil {
			return err
		}
		if hasCmd {
			if err := queueTo(x, contract.RouteReceipts, transferReceipt(host, cmd, contract.ReceiptStatusFailed, f.msg)); err != nil {
				return err
			}
		}
		if kind == "upload" {
			aid := id
			if body.ArtifactId != nil && *body.ArtifactId != "" {
				aid = *body.ArtifactId
			}
			req := contract.ArtifactFailedRequest{HostId: host, ArtifactId: aid, Error: f.msg, Name: optStr(body.Name)}
			if body.ExecutionId != "" {
				req.ExecutionId = &body.ExecutionId
			}
			if f.refused {
				req.Refused = &f.refused
			}
			return queueTo(x, contract.RouteArtifactsFailed, req)
		}
	}
	return nil
}

// refusalText: the message of a coordinator refusal ("409 {\"message\":\"Content was purged\"}" reads "409 Content was
// purged"), or the text as it is.
func refusalText(msg string) string {
	code, rest, ok := strings.Cut(strings.TrimSpace(msg), " ")
	if !ok {
		return msg
	}
	var v struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(rest), &v) != nil || v.Message == "" {
		return msg
	}
	return code + " " + v.Message
}
