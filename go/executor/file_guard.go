package main

// Path guard and client tripwire for connection file uploads (docs/designs/fleet-connections.md §9 Files). Every check
// runs on the opened descriptor, compared with the resolved path by device and inode.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// runFilePath is the run file this server read its token from (`mcp connection` argument 3); it is denylisted.
var runFilePath string

// denyEntries are the credential stores no upload may read: Fleet's own credentials and the common secret stores of
// the hosts' harnesses and tools. A directory covers everything below it; a missing entry is skipped. Built on every
// upload, so a file created later is covered too.
func denyEntries() []string {
	home, _ := os.UserHomeDir()
	at := func(rel string) string { return filepath.Join(home, rel) }
	list := []string{os.Getenv("FLEET_TOKEN_FILE"), os.Getenv("FLEET_CF_ACCESS_FILE"),
		at(".config/fleet"), at(".ssh"), at(".aws"), at(".gnupg"), at(".netrc"), at(".config/gh"), at(".hermes/.env"),
		at("Library/Keychains"), at(".claude/.credentials.json"), at(".codex/auth.json")}
	if runFilePath != "" {
		// The run file's folder holds every live run's token file, not this run's alone.
		list = append(list, runFilePath, filepath.Dir(runFilePath))
	}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		list = append(list, filepath.Join(d, ".credentials.json"))
	}
	if d := os.Getenv("CODEX_HOME"); d != "" {
		list = append(list, filepath.Join(d, "auth.json"))
	}
	return list
}

// guardBeforeOpen runs between resolving and opening (a test swaps the file there).
var guardBeforeOpen = func() {}

// guardError is a refused path: the reason names the rule or the matching entry, never the file's content.
type guardError struct{ reason string }

func (e guardError) Error() string { return e.reason }

// openForUpload resolves path (relative to base), opens it without following a last symlink and answers the open file
// when it is one regular file outside every denylisted entry and, when FLEET_FILES_ROOTS is set, inside one of them.
func openForUpload(path, base string) (*os.File, os.FileInfo, error) {
	if path == "" {
		return nil, nil, guardError{"path is required"}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, nil, guardError{"the file does not exist or cannot be resolved"}
	}
	if roots := os.Getenv("FLEET_FILES_ROOTS"); roots != "" {
		inside := false
		for _, r := range filepath.SplitList(roots) {
			if rr, err := filepath.EvalSymlinks(r); err == nil && (resolved == rr || strings.HasPrefix(resolved, rr+string(filepath.Separator))) {
				inside = true
			}
		}
		if !inside {
			return nil, nil, guardError{"the file is outside FLEET_FILES_ROOTS"}
		}
	}
	guardBeforeOpen()
	// O_NONBLOCK: opening a FIFO never waits for a writer; it is refused below as not a regular file.
	f, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, guardError{"the file cannot be opened"}
	}
	info, err := f.Stat()
	refuse := func(reason string) (*os.File, os.FileInfo, error) { f.Close(); return nil, nil, guardError{reason} }
	if err != nil {
		return refuse("the file cannot be read")
	}
	if !info.Mode().IsRegular() {
		return refuse("not a regular file")
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || uint64(st.Nlink) != 1 {
		return refuse("the file has more than one hard link")
	}
	if now, err := os.Stat(resolved); err != nil || !os.SameFile(now, info) {
		return refuse("the file changed while it was opened")
	}
	// The file itself and each folder above it, against every denylisted entry.
	chain := []os.FileInfo{info}
	for dir := filepath.Dir(resolved); ; dir = filepath.Dir(dir) {
		if d, err := os.Stat(dir); err == nil {
			chain = append(chain, d)
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	for _, entry := range denyEntries() {
		if entry == "" {
			continue
		}
		er, err := filepath.EvalSymlinks(entry)
		if err != nil {
			continue
		}
		ei, err := os.Stat(er)
		if err != nil {
			continue
		}
		for _, c := range chain {
			if os.SameFile(ei, c) {
				return refuse("the file is in a credential store (" + entry + ")")
			}
		}
	}
	return f, info, nil
}

// scanFile is the first pass over an open file: its sha256, and whether it holds one of patterns (the caller's bearer,
// the Access secret). A pattern split across two reads is found: each chunk is scanned after the previous chunk's
// last maxLen-1 bytes.
func scanFile(r io.Reader, patterns [][]byte) (string, bool, error) {
	longest := 0
	for _, p := range patterns {
		longest = max(longest, len(p))
	}
	hash, buf := sha256.New(), make([]byte, 256<<10)
	var tail []byte
	found := false
	for {
		n, err := r.Read(buf)
		if n > 0 {
			hash.Write(buf[:n])
			if !found && longest > 0 {
				window := append(tail, buf[:n]...)
				for _, p := range patterns {
					if len(p) > 0 && bytes.Contains(window, p) {
						found = true
					}
				}
				tail = append([]byte(nil), window[max(0, len(window)-(longest-1)):]...)
			}
		}
		if err == io.EOF {
			return hex.EncodeToString(hash.Sum(nil)), found, nil
		}
		if err != nil {
			return "", false, err
		}
	}
}

var mediaTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".pdf": "application/pdf", ".mov": "video/quicktime", ".mp4": "video/mp4", ".webm": "video/webm",
	".zip": "application/zip", ".csv": "text/csv", ".md": "text/markdown", ".txt": "text/plain",
	".json": "application/json", ".log": "text/plain",
}

// mediaTypeOf is the file's media type by its extension, else by its first 512 bytes, without parameters.
func mediaTypeOf(name string, head []byte) string {
	if t, ok := mediaTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return t
	}
	t, _, _ := strings.Cut(http.DetectContentType(head), ";")
	return strings.TrimSpace(t)
}
