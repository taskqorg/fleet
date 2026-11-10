package main

// Artifact transfers: workers go through the `transfers` journal (uploads of collected output and artifact_save,
// artifact-copy and artifact-verify commands, artifact_get downloads). Bytes go straight between this host and a store;
// the coordinator only reserves, presigns and records. Each job resumes after a restart from its journal row.

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func uploadSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS transfers(id TEXT PRIMARY KEY,kind TEXT NOT NULL,body TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'queued',response TEXT,attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);`)
	return err
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
	if own, err := neturl.Parse(jStr(storeConfig(), "endpoint")); err == nil && own.Hostname() != "" {
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
		coordinator: &http.Client{Timeout: 60 * time.Second, CheckRedirect: noRedirect},
		// Parts are 32 MiB; a slow tunnel needs more than the outbox's 5 s. Never follows a redirect: requests carry the
		// Access service token headers, which a redirect would hand on.
		store: &http.Client{CheckRedirect: noRedirect, Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 20 * time.Second}).DialContext,
			ResponseHeaderTimeout: 300 * time.Second, Proxy: http.ProxyFromEnvironment}}}
}

// failure: retry true is retried with backoff; false is permanent.
type failure struct {
	retry bool
	msg   string
}

func (f *failure) Error() string             { return f.msg }
func retryf(f string, a ...any) *failure     { return &failure{true, fmt.Sprintf(f, a...)} }
func permanentf(f string, a ...any) *failure { return &failure{false, fmt.Sprintf(f, a...)} }

// ioFailure: network-shaped I/O errors are retried; others (a missing or unreadable local file) are permanent.
func ioFailure(err error) *failure {
	var f *failure
	if errors.As(err, &f) {
		return f
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) {
		return &failure{true, err.Error()}
	}
	return &failure{false, err.Error()}
}

func (c *transferClient) post(route string, body any) (any, *failure) {
	a := classify(postJSON(c.coordinator, c.url+route, c.token, body))
	observe(route, a)
	switch a.kind {
	case answerOK:
		return a.value, nil
	case answerEdge:
		return nil, retryf("coordinator edge answered %d (%s)", a.status, a.text)
	case answerRetry:
		return nil, retryf("%s", a.text)
	}
	// 409 no_store: no listed store is reachable now: wait and retry.
	if a.status == 409 && strings.Contains(a.text, "No reachable store") {
		return nil, retryf("409 %s", a.text)
	}
	return nil, permanentf("%d %s", a.status, a.text)
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
		return nil, permanentf("store %d %s", r.StatusCode, b)
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
func sendParts(c *transferClient, db *sql.DB, jobID, file, artifactID, storeID string, size, partSize uint64) (any, *failure) {
	count := max((size+partSize-1)/partSize, 1)
	var bodyText string
	db.QueryRow("SELECT body FROM transfers WHERE id=?", jobID).Scan(&bodyText)
	body := decM(bodyText)
	done := jArr(body, "done")
	f, err := os.Open(file)
	if err != nil {
		return nil, ioFailure(err)
	}
	defer f.Close()
	var missing []any
	for n := uint64(1); n <= count; n++ {
		have := false
		for _, d := range done {
			if x, _ := jU64(d, "partNumber"); x == n {
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
		req := M{"hostId": c.host, "artifactId": artifactID, "partNumbers": batch}
		if storeID != "" {
			req["storeId"] = storeID
		}
		urls, fl := c.post("/api/hosts/artifacts/parts", req)
		if fl != nil {
			return nil, fl
		}
		for _, p := range jArr(urls, "parts") {
			n, ok := jU64(p, "partNumber")
			if !ok || n == 0 {
				return nil, permanentf("part number")
			}
			l := min(partSize, size-min(size, (n-1)*partSize))
			chunk := make([]byte, l)
			if _, err := f.ReadAt(chunk, int64((n-1)*partSize)); err != nil && !(errors.Is(err, io.EOF) && l == 0) {
				return nil, ioFailure(err)
			}
			var etag string
			for tries := 0; ; tries++ {
				e, fl := c.putPart(jStr(p, "url"), chunk)
				if fl == nil {
					etag = e
					break
				}
				if !fl.retry || tries >= 5 {
					return nil, fl
				}
				time.Sleep(time.Duration(min(1<<(tries+1), 60)) * time.Second)
			}
			done = append(done, M{"partNumber": n, "etag": etag})
			b := jObj(jClone(body))
			b["done"] = done
			db.Exec("UPDATE transfers SET body=? WHERE id=?", enc(b), jobID)
		}
	}
	req := M{"hostId": c.host, "artifactId": artifactID, "parts": done}
	if storeID != "" {
		req["storeId"] = storeID
	}
	commit, fl := c.post("/api/hosts/artifacts/commit", req)
	if fl != nil {
		return nil, fl
	}
	if v, ok := jObj(commit)["verify"]; ok {
		a := artifactID
		if jIsStr(v, "artifactId") {
			a = jStr(v, "artifactId")
		}
		return verifyArtifact(c, a, jStr(v, "storeId"), jStr(v, "url"))
	}
	return commit, nil
}

func verifyArtifact(c *transferClient, artifactID, storeID, url string) (any, *failure) {
	r, f := c.get(url)
	if f != nil {
		return nil, f
	}
	digest, _, err := hashReader(r)
	r.Close()
	if err != nil {
		return nil, ioFailure(err)
	}
	return c.post("/api/hosts/artifacts/verified", M{"hostId": c.host, "artifactId": artifactID, "storeId": storeID, "sha256": digest})
}

func u64Or(def uint64, v any, keys ...string) uint64 {
	if x, ok := jU64(v, keys...); ok {
		return x
	}
	return def
}

func runJob(c *transferClient, db *sql.DB, id, kind string, body M) (any, *failure) {
	switch kind {
	case "upload":
		path := jStr(body, "path")
		digest, size, err := hashFile(path)
		if err != nil {
			return nil, ioFailure(err)
		}
		req := M{"hostId": c.host, "executionId": body["executionId"], "artifactId": id, "name": body["name"], "mediaType": body["mediaType"], "size": size, "sha256": digest, "required": body["required"]}
		for _, k := range []string{"derivedFrom", "role"} {
			if body[k] != nil {
				req[k] = body[k]
			}
		}
		r, f := c.post("/api/hosts/artifacts/reserve", req)
		if f != nil {
			return nil, f
		}
		db.Exec("UPDATE transfers SET response=? WHERE id=?", enc(M{"artifactId": jAt(r, "artifactId"), "state": jAt(r, "state")}), id)
		switch jStr(r, "state") {
		case "stored":
			return M{"artifactId": jAt(r, "artifactId"), "state": "stored"}, nil
		case "failed":
			return nil, permanentf("artifact failed")
		}
		a := id
		if jIsStr(r, "artifactId") {
			a = jStr(r, "artifactId")
		}
		out, f := sendParts(c, db, id, path, a, "", uint64(size), u64Or(32<<20, r, "partSize"))
		if f != nil {
			return nil, f
		}
		return M{"artifactId": jAt(r, "artifactId"), "state": jAt(out, "state")}, nil
	case "verify":
		return verifyArtifact(c, jStr(body, "artifactId"), jStr(body, "storeId"), jStr(body, "url"))
	case "copy":
		// Download the source object next to the journal, then upload it to this host's store.
		tmp := filepath.Join(jStr(body, "spool"), id+".part")
		if st, err := os.Stat(tmp); err != nil || uint64(st.Size()) != u64Or(^uint64(0), body, "size") {
			src, f := c.get(jStr(body, "source", "url"))
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
		out, f := sendParts(c, db, id, tmp, jStr(body, "artifactId"), jStr(body, "storeId"), uint64(st.Size()), u64Or(32<<20, body, "partSize"))
		os.Remove(tmp)
		return out, f
	case "get":
		r, f := c.post("/api/hosts/artifacts/get", M{"hostId": c.host, "executionId": body["executionId"], "artifactId": body["artifactId"]})
		if f != nil {
			return nil, f
		}
		if !jIsStr(r, "url") {
			av := "unavailable"
			if jIsStr(r, "availability") {
				av = jStr(r, "availability")
			}
			return nil, permanentf("artifact %s", av)
		}
		name := "artifact"
		if jIsStr(r, "name") {
			name = jStr(r, "name")
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
		dir := jStr(body, "inputDir")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, ioFailure(err)
		}
		tag := "x"
		if jIsStr(r, "id") {
			tag = jStr(r, "id")
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
		src, f := c.get(jStr(r, "url"))
		if f != nil {
			return nil, f
		}
		defer src.Close()
		if _, err := io.Copy(out, src); err != nil {
			return nil, ioFailure(err)
		}
		return M{"artifactId": jAt(r, "id"), "path": target, "mediaType": jAt(r, "mediaType"), "size": jAt(r, "size")}, nil
	case "list":
		return c.post("/api/hosts/artifacts/list", M{"hostId": c.host, "executionId": body["executionId"], "filter": body["filter"]})
	}
	return nil, permanentf("unknown transfer kind %s", kind)
}

// transferRoutes: coordinator routes only transfers call.
var transferRoutes = []string{"/api/hosts/artifacts/reserve", "/api/hosts/artifacts/parts", "/api/hosts/artifacts/commit", "/api/hosts/artifacts/verified", "/api/hosts/artifacts/get", "/api/hosts/artifacts/list", "/api/hosts/artifacts/renew"}

// leased: the artifact a queued job holds a coordinator lease on, with its store when known.
func leased(kind, id string, body M) (string, string, bool) {
	switch kind {
	case "upload":
		return id, "", true
	case "verify", "copy":
		if jIsStr(body, "artifactId") {
			return jStr(body, "artifactId"), jStr(body, "storeId"), true
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
		go transferWork(db, c, lane, after)
	}
	db, err := openJournal(journal)
	if err != nil {
		return err
	}
	go func() {
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
				if a, s, ok := leased(j.kind, j.id, decM(j.body)); ok {
					req := M{"hostId": c.host, "artifactId": a}
					if s != "" {
						req["storeId"] = s
					}
					c.post("/api/hosts/artifacts/renew", req)
				}
			}
		}
	}()
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
		body := decM(bodyText)
		v, f := runJob(c, db, id, kind, body)
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
		if f == nil {
			after(c, db, body, v)
		}
	}
}

// settleJob records a job's outcome: done or failed with the command's final receipt (and, for an upload that can never
// succeed, the artifact failure), or a retry with backoff.
func settleJob(x execer, host, id, kind string, body M, attempts int64, v any, f *failure) error {
	cmd, hasCmd := body["command"]
	switch {
	case f == nil:
		if _, err := x.Exec("UPDATE transfers SET state='done',response=? WHERE id=?", enc(v), id); err != nil {
			return err
		}
		if hasCmd {
			return queue(x, "/api/hosts/receipts", M{"hostId": host, "commandId": jAt(cmd, "id"), "executionId": jAt(cmd, "executionId"), "status": "completed"})
		}
	case f.retry:
		_, err := x.Exec("UPDATE transfers SET attempts=attempts+1,next_at=?,response=? WHERE id=?", nowMs()+backoff(attempts), enc(M{"state": "retrying", "error": f.msg}), id)
		return err
	default:
		if _, err := x.Exec("UPDATE transfers SET state='failed',response=? WHERE id=?", enc(M{"state": "failed", "error": f.msg}), id); err != nil {
			return err
		}
		if hasCmd {
			if err := queue(x, "/api/hosts/receipts", M{"hostId": host, "commandId": jAt(cmd, "id"), "executionId": jAt(cmd, "executionId"), "status": "failed", "error": f.msg}); err != nil {
				return err
			}
		}
		if kind == "upload" {
			return queue(x, "/api/hosts/artifacts/failed", M{"hostId": host, "artifactId": id, "error": f.msg})
		}
	}
	return nil
}
