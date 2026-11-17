package main

// Artifact tools of the delegation server: artifact_save spools a file from the working area (walked component by
// component with openat O_NOFOLLOW, so no symlink can lead outside it; an absolute path must name the working area or
// its resolved spelling), artifact_list (own execution by default, paged) and artifact_get queue transfers.

import (
	"database/sql"
	_ "embed"
	"errors"
	"fleet/contract"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

//go:embed mcp-mime.json
var mcpMimeJSON []byte

// Captured Python mimetypes table keeps the former adapter's defaults without host Python.
var mimeTable, _ = dec(string(mcpMimeJSON))

const notLink = "path must be a regular file inside your working area, not a link"

// refusal is a tool-level refusal (isError result), as opposed to a journal error.
type refusal string

func (r refusal) Error() string { return string(r) }

func extension(path string) string {
	name := path[strings.LastIndex(path, "/")+1:]
	if i := strings.LastIndex(name, "."); i >= 0 && strings.Trim(name[:i], ".") != "" {
		return name[i:]
	}
	return ""
}

func mime(name string) string {
	path := name
	for {
		ext := extension(path)
		suffix, ok := str(jAt(mimeTable, "suffixes", strings.ToLower(ext)))
		if !ok || ext == "" {
			break
		}
		path = path[:len(path)-len(ext)] + suffix
	}
	ext := extension(path)
	if has(jAt(mimeTable, "encodings"), ext) {
		ext = extension(path[:len(path)-len(ext)])
	}
	if t, ok := str(jAt(mimeTable, "types", strings.ToLower(ext))); ok {
		return t
	}
	return "application/octet-stream"
}

// openInside opens workdir/parts... refusing any symlink component.
func openInside(workdir string, parts []string) (*os.File, error) {
	fd, err := unix.Open(workdir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, refusal(notLink)
	}
	for i, p := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC // O_NONBLOCK: a FIFO never blocks the open
		if i+1 < len(parts) {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(fd, p, flags, 0)
		unix.Close(fd)
		if err != nil {
			return nil, refusal(notLink)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), parts[len(parts)-1]), nil
}

// pathParts: the components of a slash path without empty and "." ones; nil when one is "..".
func pathParts(path string) []string {
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p == ".." {
			return nil
		}
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	return parts
}

// insideParts: an absolute path's components below the working area, which it may name as given or resolved (a root
// reached through a link, such as /var and /private/var on macOS); nil when the path is outside it. Only the root's
// spelling is resolved: the components below it are still opened one by one without following any link.
func insideParts(workdir string, parts []string) []string {
	roots := []string{workdir}
	if r, err := filepath.EvalSymlinks(workdir); err == nil {
		roots = append(roots, r)
	}
	for _, root := range roots {
		rp := pathParts(root)
		if len(rp) == 0 || len(parts) <= len(rp) || !slices.Equal(parts[:len(rp)], rp) {
			continue
		}
		return parts[len(rp):]
	}
	return nil
}

func artifactSave(ctx, q any, db dbExec) (any, error) {
	path, ok := str(jAt(q, "path"))
	if !ok {
		return nil, refusal("path must be a file inside your working area")
	}
	workdir, ok := str(jAt(ctx, "workdir"))
	if !ok {
		return nil, refusal("workdir")
	}
	parts := pathParts(path)
	if strings.HasPrefix(path, "/") {
		parts = insideParts(workdir, parts)
	}
	if len(parts) == 0 {
		return nil, refusal("path must be a file inside your working area")
	}
	f, err := openInside(workdir, parts)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Sys().(*syscall.Stat_t).Nlink != 1 {
		return nil, refusal("path must be a regular file with one link")
	}
	key := newID()
	spoolDir, ok := str(jAt(ctx, "spool"))
	if !ok {
		return nil, refusal("spool")
	}
	spool := filepath.Join(spoolDir, key)
	if err := os.MkdirAll(spoolDir, 0o755); err != nil {
		return nil, err
	}
	out, err := os.OpenFile(spool, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(out, f)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	name := strings.Join(parts, "/")
	body := M{"path": spool, "spool": true, "previews": true, "name": name, "mediaType": mime(name), "executionId": jAt(ctx, "executionId"), "required": jAt(q, "required") == true}
	if truth(jAt(q, "name")) {
		body["name"] = jAt(q, "name")
	}
	if truth(jAt(q, "media_type")) {
		body["mediaType"] = jAt(q, "media_type")
	}
	if err := db.tx(func(x execer) error {
		if _, err := x.Exec("INSERT INTO transfers(id,kind,body,created_at) VALUES(?,'upload',?,?)", key, enc(body), nowMs()); err != nil {
			return err
		}
		if body["required"] == true {
			decl := contract.ArtifactDeclareRequest{HostId: jStr(ctx, "hostId"), ExecutionId: jStr(ctx, "executionId"), Name: jStr(body, "name")}
			_, err := x.Exec("INSERT INTO outbox(route,body) VALUES('/api/hosts/artifacts/declare',?)", enc(decl))
			return err
		}
		return nil
	}); err != nil {
		return nil, dbe(err)
	}
	r, err := wait(db.DB, "transfers", key, 30, true)
	if r == nil && err == nil {
		r = M{"artifactId": key, "state": "pending"}
	}
	return r, err
}

func artifactCall(ctx any, name string, q any, db dbExec) (any, error) {
	var spec any
	for _, t := range mcpTools(ctx)["tools"].([]any) {
		if jAt(t, "name") == name {
			spec = jAt(t, "inputSchema")
		}
	}
	qo, ok := q.(M)
	bad := !ok
	for k, v := range qo {
		_, isS := v.(string)
		_, isB := v.(bool)
		bad = bad || !has(jAt(spec, "properties"), k) || !(isS || isB)
	}
	req, _ := jAt(spec, "required").([]any)
	for _, k := range req {
		bad = bad || !has(qo, jStr(k))
	}
	if bad {
		return nil, errors.New("Invalid artifact tool arguments")
	}
	var result any
	var err error
	if name == "artifact_save" {
		result, err = artifactSave(ctx, q, db)
	} else {
		key := newID()
		kind, seconds := "get", 600
		body := M{"executionId": jAt(ctx, "executionId"), "artifactId": jAt(q, "artifact_id"), "inputDir": jAt(ctx, "inputDir")}
		if name == "artifact_list" {
			kind, seconds = "list", 20
			scope := jAt(q, "scope")
			if scope != nil && scope != "execution" && scope != "project" {
				return mcpText(M{"error": `artifact tool refused: scope must be "execution" or "project"`}, &yes), nil
			}
			// Without a filter an agent lists its own execution's artifacts; the whole project only on request.
			execution := jAt(q, "execution_id")
			if execution == nil && jAt(q, "task_id") == nil && scope != "project" {
				execution = jAt(ctx, "executionId")
			}
			body = M{"executionId": jAt(ctx, "executionId"), "filter": M{"executionId": execution, "taskId": jAt(q, "task_id"), "mediaType": jAt(q, "media_type"), "cursor": jAt(q, "cursor")}}
		}
		if _, err := db.Exec("INSERT INTO transfers(id,kind,body,created_at) VALUES(?,?,?,?)", key, kind, enc(body), nowMs()); err != nil {
			return nil, dbe(err)
		}
		result, err = wait(db.DB, "transfers", key, seconds, false)
		if err == nil {
			if r, ok := result.(M); ok {
				r["transferId"] = key
			} else {
				result = M{"state": "pending", "transferId": key, "instruction": "Still in progress on this host; list artifacts later."}
			}
		}
	}
	var de dbError
	var ur unreadReply
	var rf refusal
	switch {
	case err == nil:
		return mcpText(result, nil), nil
	case errors.As(err, &de), errors.As(err, &ur): // a stored reply that does not decode is no refusal: outcome unknown
		return nil, err
	case errors.As(err, &rf):
		return mcpText(M{"error": "artifact tool refused: " + string(rf)}, &yes), nil
	}
	return mcpText(M{"error": "artifact tool refused: " + notLink}, &yes), nil
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

type dbExec struct{ *sql.DB }

func (d dbExec) tx(f func(execer) error) error {
	t, err := d.Begin()
	if err != nil {
		return err
	}
	if err := f(t); err != nil {
		t.Rollback()
		return err
	}
	return t.Commit()
}
