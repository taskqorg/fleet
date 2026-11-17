package main

// Path guard and client tripwire for connection file uploads (docs/designs/fleet-connections.md §9 Files). The file is
// opened by walking its resolved path one folder at a time with O_NOFOLLOW, and every folder and the file itself are
// checked, by device and inode of the opened descriptor, against the credential denylist.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// runFilePath is the run file this server read its token from (`mcp connection` argument 3); it is denylisted.
var runFilePath string

// uploadRoots, when not nil, are folders an upload must lie in, besides FLEET_FILES_ROOTS: the code server's sandbox
// read roots, so a bridged upload reads nothing the sandbox could not.
var uploadRoots []string

// dataStores are the credential stores inside an executor data folder: the journal (start commands with run tokens),
// the run files, the spool and the account registry copy. Sessions and worktrees in it stay uploadable.
func dataStores(dir string) []string {
	if dir == "" {
		return nil
	}
	list := []string{}
	for _, rel := range []string{"journal.sqlite", "journal.sqlite-wal", "journal.sqlite-shm", "runs", "spool", "accounts.json"} {
		list = append(list, filepath.Join(dir, rel))
	}
	return list
}

// homeEntries are the credential stores under one home folder.
func homeEntries(home string) []string {
	if home == "" {
		return nil
	}
	data := ".local/state/fleet/executor" // the executor's default data folder
	if runtime.GOOS == "darwin" {
		data = "Library/Application Support/Fleet/executor"
	}
	list := dataStores(filepath.Join(home, data))
	for _, rel := range []string{".config/fleet", ".local/share/fleet/gateway", ".ssh", ".aws", ".gnupg", ".netrc",
		".config/gh", ".hermes/.env", "Library/Keychains", ".claude/.credentials.json", ".codex/auth.json"} {
		list = append(list, filepath.Join(home, rel))
	}
	return list
}

// denyEntries are the credential stores no upload may read: Fleet's own credentials (owner and run tokens, the Access
// file, the executor's data folder with its journal and run files, host secret, gateway keys and state) and the common
// secret stores of the hosts' harnesses and tools. A directory covers everything below it; a missing entry is skipped.
// Built on every upload, so a file created later is covered too. In a Fleet run HOME is the harness's home, so the run
// file names the executor user's home (FLEET_USER_HOME) and the executor's own stores (FLEET_RUN_DENY) as well.
func denyEntries() []string {
	home, _ := os.UserHomeDir()
	list := append(homeEntries(home), homeEntries(os.Getenv("FLEET_USER_HOME"))...)
	list = append(list, os.Getenv("FLEET_TOKEN_FILE"), os.Getenv("FLEET_CF_ACCESS_FILE"), os.Getenv("FLEET_HOST_SECRET_FILE"))
	list = append(list, dataStores(os.Getenv("FLEET_DATA_DIR"))...)
	var named []string // FLEET_RUN_DENY is a JSON list: a path may hold the path-list separator
	json.Unmarshal([]byte(os.Getenv("FLEET_RUN_DENY")), &named)
	list = append(list, named...)
	list = append(list, executorStores()...)
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

// ident is a file's device and inode.
type ident struct{ dev, ino uint64 }

func identOf(st *unix.Stat_t) ident { return ident{uint64(st.Dev), uint64(st.Ino)} }

// deniedIdents: each existing entry's device and inode, mapped to the entry's name.
func deniedIdents() map[ident]string {
	out := map[ident]string{}
	for _, entry := range denyEntries() {
		if entry == "" {
			continue
		}
		var st unix.Stat_t
		if unix.Stat(entry, &st) == nil {
			out[identOf(&st)] = entry
		}
	}
	return out
}

// guardBeforeOpen runs between resolving and opening (a test swaps the file there).
var guardBeforeOpen = func() {}

// guardError is a refused path: the reason names the rule or the matching entry, never the file's content.
type guardError struct{ reason string }

func (e guardError) Error() string { return e.reason }

func inRoots(path string, roots []string) bool {
	for _, r := range roots {
		if rr, err := filepath.EvalSymlinks(r); err == nil && (path == rr || rr == "/" || strings.HasPrefix(path, rr+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

// openForUpload resolves path (relative to base) and opens it when it is one regular file with a single link, outside
// every denylisted entry and inside FLEET_FILES_ROOTS (and uploadRoots) when set. The resolved path is walked from / one
// component at a time with O_NOFOLLOW, so a folder or the file swapped for a symlink after resolving is refused, and
// the checks see the folders actually opened.
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
	if roots := os.Getenv("FLEET_FILES_ROOTS"); roots != "" && !inRoots(resolved, filepath.SplitList(roots)) {
		return nil, nil, guardError{"the file is outside FLEET_FILES_ROOTS"}
	}
	if uploadRoots != nil && !inRoots(resolved, uploadRoots) {
		return nil, nil, guardError{"the file is outside the folders this session may read"}
	}
	guardBeforeOpen()
	deny := deniedIdents()
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, guardError{"the file cannot be opened"}
	}
	parts := strings.Split(strings.TrimPrefix(resolved, "/"), "/")
	for i := 0; ; i++ {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			unix.Close(fd)
			return nil, nil, guardError{"the file cannot be read"}
		}
		if entry, ok := deny[identOf(&st)]; ok {
			unix.Close(fd)
			return nil, nil, guardError{"the file is in a credential store (" + entry + ")"}
		}
		if i == len(parts) {
			switch {
			case st.Mode&unix.S_IFMT != unix.S_IFREG:
				unix.Close(fd)
				return nil, nil, guardError{"not a regular file"}
			case uint64(st.Nlink) != 1:
				unix.Close(fd)
				return nil, nil, guardError{"the file has more than one hard link"}
			}
			f := os.NewFile(uintptr(fd), resolved)
			info, err := f.Stat()
			if err != nil {
				f.Close()
				return nil, nil, guardError{"the file cannot be read"}
			}
			return f, info, nil
		}
		if i > 0 && st.Mode&unix.S_IFMT != unix.S_IFDIR {
			unix.Close(fd)
			return nil, nil, guardError{"not a regular file"}
		}
		// O_NONBLOCK: opening a FIFO never waits for a writer; it is refused above as not a regular file.
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(fd, parts[i], flags, 0)
		unix.Close(fd)
		if err != nil {
			return nil, nil, guardError{"the path changed while it was opened, or a part of it is a symlink"}
		}
		fd = next
	}
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

// uploadDenyLines are the run file lines that anchor a run's upload denylist (the harness's HOME is not this user's
// home): the executor user's home, the executor's own stores (data folder stores, host secret, Codex account, account
// registry) and every registry account's login folders, and the executor's FLEET_FILES_ROOTS.
func uploadDenyLines(dataDir string) string {
	userHome, _ := os.UserHomeDir()
	deny := append(dataStores(dataDir), envOr("FLEET_HOST_SECRET_FILE", defaultSecretFile()))
	deny = append(deny, executorStores()...)
	if registry, err := readRegistry(); err == nil {
		for _, a := range registry {
			deny = append(deny, a.NativeHome) // CODEX_HOME, CLAUDE_CONFIG_DIR or GEMINI_HOME: the login itself
			deny = append(deny, homeEntries(a.Home)...)
			deny = append(deny, homeEntries(a.CredentialHome)...)
		}
	}
	deny = slices.DeleteFunc(deny, func(p string) bool { return p == "" })
	lines := "FLEET_RUN_DENY=" + enc(deny) + "\n" // JSON escapes newlines, so one line
	if !strings.ContainsAny(userHome, "\n\r") {
		lines += "FLEET_USER_HOME=" + userHome + "\n"
	}
	if roots := os.Getenv("FLEET_FILES_ROOTS"); roots != "" && !strings.ContainsAny(roots, "\n\r") {
		lines += "FLEET_FILES_ROOTS=" + roots + "\n"
	}
	return lines
}

// executorStores are the executor's other credential stores named by its environment: the Codex account, the account
// registry, the forge's GitHub store, the artifact store's config and key file, and the browser bindings with each
// project's Access file.
func executorStores() []string {
	list := []string{os.Getenv("FLEET_CODEX_HOME"), os.Getenv("FLEET_NATIVE_ACCOUNTS"), os.Getenv("FLEET_FORGE_CONFIG_DIR"), os.Getenv("FLEET_STORE_CONFIG"), os.Getenv("FLEET_BROWSER_BINDINGS")}
	if c := storeConfig(); c != nil {
		list = append(list, jStr(c, "keyFile"))
	}
	if b, err := os.ReadFile(os.Getenv("FLEET_BROWSER_BINDINGS")); err == nil {
		if v, err := dec(string(b)); err == nil {
			for _, binding := range jObj(v) {
				list = append(list, jStr(binding, "production", "accessFile"))
			}
		}
	}
	return slices.DeleteFunc(list, func(p string) bool { return p == "" })
}
