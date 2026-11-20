package main

// Artifact tools of the delegation server: artifact_save spools a file from the working area (walked component by
// component with openat O_NOFOLLOW, so no symlink can lead outside it; an absolute path must name the working area or
// its resolved spelling), artifact_list (own execution by default, paged) and artifact_get queue transfers.

import (
	"database/sql"
	_ "embed"
	"encoding/json"
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
var mimeTable = func() (t struct {
	Encodings map[string]string `json:"encodings"`
	Suffixes  map[string]string `json:"suffixes"`
	Types     map[string]string `json:"types"`
}) {
	json.Unmarshal(mcpMimeJSON, &t)
	return t
}()

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
		suffix, ok := mimeTable.Suffixes[strings.ToLower(ext)]
		if !ok || ext == "" {
			break
		}
		path = path[:len(path)-len(ext)] + suffix
	}
	ext := extension(path)
	if _, ok := mimeTable.Encodings[ext]; ok {
		ext = extension(path[:len(path)-len(ext)])
	}
	if t, ok := mimeTable.Types[strings.ToLower(ext)]; ok {
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

// insideParts: an absolute path's components below the working area; nil when no leading part of it is the working
// area's directory. The leading part may spell the area any way that reaches the same directory (a root through a link,
// such as /var and /private/var on macOS, either way round; the configured, then the resolved spelling is compared first,
// other prefixes are compared by directory identity); the components below it are still opened one by one
// without following a link.
func insideParts(workdir string, parts []string) []string {
	below := func(w string) []string { // parts below the spelling w of the working area, or nil
		if wp := pathParts(w); len(wp) > 0 && len(parts) > len(wp) && slices.Equal(parts[:len(wp)], wp) {
			return parts[len(wp):]
		}
		return nil
	}
	if p := below(filepath.Clean(workdir)); p != nil { // as configured: no filesystem call
		return p
	}
	if r, err := filepath.EvalSymlinks(workdir); err == nil && below(r) != nil {
		return below(r)
	}
	root, err := os.Stat(workdir)
	if err != nil {
		return nil
	}
	for i := range parts {
		if st, err := os.Stat("/" + strings.Join(parts[:i], "/")); err == nil && os.SameFile(st, root) {
			return parts[i:]
		}
	}
	return nil
}

// artifactArgs: the arguments of artifact_save, artifact_list and artifact_get (mcp-tools.json); artifactCall admits
// only text and booleans, and a value passed on keeps its type.
type artifactArgs struct {
	Path        lax[string] `json:"path"`
	Name        lax[string] `json:"name"`
	MediaType   lax[string] `json:"media_type"`
	Required    lax[bool]   `json:"required"`
	ArtifactId  lax[string] `json:"artifact_id"`
	ExecutionId lax[string] `json:"execution_id"`
	TaskId      lax[string] `json:"task_id"`
	Scope       lax[string] `json:"scope"`
	Cursor      lax[string] `json:"cursor"`
}

func artifactSave(ctx *delegationContext, q artifactArgs, db dbExec) (any, error) {
	path := q.Path.V
	if !q.Path.Ok {
		return nil, refusal("path must be a file inside your working area")
	}
	workdir := ctx.Workdir.V
	if !ctx.Workdir.Ok {
		return nil, refusal("workdir")
	}
	parts := pathParts(path)
	rooted := false // an absolute path not through the working area keeps its former meaning: below the working area
	if strings.HasPrefix(path, "/") && parts != nil {
		if inside := insideParts(workdir, parts); inside != nil {
			parts = inside
		} else {
			rooted = true
		}
	}
	if len(parts) == 0 {
		return nil, refusal("path must be a file inside your working area")
	}
	f, err := openInside(workdir, parts)
	if err != nil && rooted {
		return nil, refusal("path must be a regular file inside your working area " + workdir + " (relative, or absolute through it), not a link")
	}
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
	spoolDir := ctx.Spool.V
	if !ctx.Spool.Ok {
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
	body := M{"path": spool, "spool": true, "previews": true, "name": name, "mediaType": mime(name), "executionId": ctx.ExecutionId.value(), "required": q.Required.V}
	declared := name
	if truth(q.Name.value()) {
		body["name"], declared = q.Name.value(), q.Name.V
	}
	if truth(q.MediaType.value()) {
		body["mediaType"] = q.MediaType.value()
	}
	if err := db.tx(func(x execer) error {
		if _, err := x.Exec("INSERT INTO transfers(id,kind,body,created_at) VALUES(?,'upload',?,?)", key, enc(body), nowMs()); err != nil {
			return err
		}
		if q.Required.V {
			decl := contract.ArtifactDeclareRequest{HostId: ctx.HostId.V, ExecutionId: ctx.ExecutionId.V, Name: declared}
			return queueTo(x, contract.RouteArtifactsDeclare, decl)
		}
		return nil
	}); err != nil {
		return nil, dbe(err)
	}
	r, err := wait(db.DB, "transfers", key, 30, true)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return M{"artifactId": key, "state": "pending"}, nil
	}
	return r.Value, nil
}

func artifactCall(context any, name string, q any, db dbExec) (any, error) {
	ctx := contextOf(context)
	spec := toolSchemas[name]
	var keys lax[map[string]json.RawMessage]
	viewOf(q, &keys)
	text := map[string]bool{} // each argument key: its value is text
	bad := !keys.Ok
	for k, v := range keys.V {
		var s lax[string]
		var b lax[bool]
		json.Unmarshal(v, &s)
		json.Unmarshal(v, &b)
		_, known := spec.Properties[k]
		text[k] = s.Ok
		bad = bad || !known || !(s.Ok || b.Ok)
	}
	for _, k := range spec.Required {
		_, in := keys.V[k]
		bad = bad || !in
	}
	if bad {
		return nil, errors.New("Invalid artifact tool arguments")
	}
	var args artifactArgs
	viewOf(q, &args)
	var result any
	var err error
	if name == "artifact_save" {
		result, err = artifactSave(ctx, args, db)
	} else {
		key := newID()
		kind, seconds := "get", 600
		body := M{"executionId": ctx.ExecutionId.value(), "artifactId": args.ArtifactId.value(), "inputDir": ctx.InputDir.value()}
		if name == "artifact_list" {
			kind, seconds = "list", 20
			for k, isText := range text {
				if !isText {
					return mcpText(M{"error": "artifact tool refused: " + k + " must be text"}, &yes), nil
				}
			}
			var scope any
			if s := args.Scope.V; s != "" { // empty is absent, as for the filters
				scope = s
			}
			if scope != nil && scope != "execution" && scope != "project" {
				return mcpText(M{"error": `artifact tool refused: scope must be "execution" or "project"`}, &yes), nil
			}
			if scope != nil && (args.ExecutionId.V != "" || args.TaskId.V != "") {
				return mcpText(M{"error": "artifact tool refused: scope goes without execution_id and task_id"}, &yes), nil
			}
			// Without a filter an agent lists its own execution's artifacts; the whole project only on request.
			execution := args.ExecutionId.V // empty is absent, as the transfer drops it
			if execution == "" && args.TaskId.V == "" && scope != "project" {
				execution = ctx.ExecutionId.V
			}
			body = M{"executionId": ctx.ExecutionId.value(), "filter": M{"executionId": optStr(execution), "taskId": args.TaskId.value(), "mediaType": args.MediaType.value(), "cursor": args.Cursor.value()}}
		}
		if _, err := db.Exec("INSERT INTO transfers(id,kind,body,created_at) VALUES(?,?,?,?)", key, kind, enc(body), nowMs()); err != nil {
			return nil, dbe(err)
		}
		var r *hostReply
		r, err = wait(db.DB, "transfers", key, seconds, false)
		if err == nil {
			if m, ok := r.valueObject(); ok {
				m["transferId"] = key
				result = m
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
