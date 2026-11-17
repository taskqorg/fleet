package contract

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// File calls (docs/designs/fleet-connections.md §9): an upload sends a local file's raw bytes, a download writes the
// gateway's byte stream into a local file. The caller has opened and checked the file (the executor's path guard).

// Upload is a file opened for an upload with its first-pass size and sha256.
type Upload struct {
	File            io.ReaderAt
	Size            int64
	Name, MediaType string
	Sha256          string
}

// GatewayUpload sends one file to an upload tool: the body is the raw bytes with an exact Content-Length, X-Fleet-Args
// the path fields with the file's name and media type, X-Fleet-Sha256 the first-pass hash. A file changed since that
// pass reaches the gateway with another hash and is refused there (sha-mismatch). The output on ok is
// {reply, name, mediaType, bytes, sha256, callId}: the provider's reply and what was sent.
func GatewayUpload(ctx context.Context, t GatewayTarget, connection, tool string, args map[string]any, u Upload) CallLine {
	fields := map[string]any{}
	for k, v := range args {
		fields[k] = v
	}
	fields["name"], fields["mediaType"] = u.Name, u.MediaType
	encoded, _ := json.Marshal(fields)
	_, answer := t.waits()
	x := send(ctx, t, connection, tool, func() io.Reader { return io.NewSectionReader(u.File, 0, u.Size) }, u.Size, true, map[string]string{
		"Content-Type":   "application/octet-stream",
		"X-Fleet-Args":   base64.RawURLEncoding.EncodeToString(encoded),
		"X-Fleet-Sha256": u.Sha256,
		"Expect":         "100-continue",
	}, answer)
	defer x.close()
	sum := u.Sha256
	transfer := &CallTransfer{Direction: CallTransferDirectionUpload, Bytes: u.Size, MediaType: u.MediaType, Name: u.Name, Sha256: &sum}
	if x.failed {
		if x.line.Outcome == CallOutcomeUnknown {
			x.line.Transfer = transfer // what may have been stored: the agent compares it with the issue's attachments
		}
		return x.line
	}
	line := x.answerJSON()
	if line.Outcome != CallOutcomeError {
		line.Transfer = transfer // sent, or may have been stored; a refusal sent nothing on
	}
	if line.Outcome == CallOutcomeOk {
		line.Output, _ = json.Marshal(map[string]any{"reply": line.Output, "name": u.Name, "mediaType": u.MediaType, "bytes": u.Size, "sha256": u.Sha256, "callId": line.CallId})
	}
	return line
}

// Download names where a downloaded file goes: <Root>/<Folder...>/<Prefix>-<name>, name from the gateway's X-Fleet-File
// made safe (SafeName). Root is the caller's trusted folder (the run's input folder, FLEET_FILES_OUT); Folder elements
// (for example attachments, TGT-12) and Prefix come from the caller and the call's arguments.
type Download struct {
	Root   string
	Folder []string
	Prefix string
}

// segment: one path element taken from a call argument.
func segment(s string) bool {
	return s != "" && s != "." && s != ".." && len(s) <= 200 && !strings.ContainsFunc(s, func(r rune) bool {
		return r == '/' || r == '\\' || unicode.IsControl(r) || r == unicode.ReplacementChar
	})
}

// SafeName is a provider's file name as one local path element: '/', '\', control and bidi characters and a leading '.'
// become '_', and it is cut to 200 bytes.
func SafeName(name string) string { return cutName(safeChars(name), 200) }

// cutName cuts name to at most n bytes on a rune boundary; an empty name is "file".
func cutName(name string, n int) string {
	// The stem is cut, not a short extension: the agent's file tools go by it.
	ext := filepath.Ext(name)
	if len(name) <= n || len(ext) > 20 || len(ext) >= n || ext == name {
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	for len(stem)+len(ext) > n {
		_, size := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-size]
	}
	if stem == "" && ext != "" {
		stem = "file"[:min(4, n-len(ext))]
	}
	name = stem + ext
	if name == "" {
		return "file"
	}
	return name
}

func safeChars(name string) string {
	var b strings.Builder
	for i, r := range strings.ToValidUTF8(name, "_") {
		if r == '/' || r == '\\' || unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || i == 0 && r == '.' {
			r = '_'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// GatewayDownload calls a download tool and writes the stream to <final>.<random>.part (O_EXCL, 0600), counted,
// hashed and fsynced, then renamed over the final name; any failure removes the part file. The output on ok is
// {path, name, bytes, mimeType, sha256, callId}.
func GatewayDownload(ctx context.Context, t GatewayTarget, connection, tool string, args []byte, d Download) CallLine {
	_, answer := t.waits()
	refused := CallLine{Tool: tool, Connection: connection, Outcome: CallOutcomeError}
	for i, s := range append([]string{d.Prefix}, d.Folder...) {
		if (i > 0 || s != "") && !segment(s) {
			code := "bad-request"
			refused.ErrorCode, refused.Output = &code, textOf([]byte("an argument naming the download folder or file is not one path element: "+s))
			return refused
		}
	}
	x := send(ctx, t, connection, tool, func() io.Reader { return bytes.NewReader(args) }, int64(len(args)), false, map[string]string{"Accept": "application/octet-stream", "Accept-Encoding": "identity"}, answer)
	defer x.close()
	if x.failed {
		return x.line
	}
	if x.res.StatusCode != 200 {
		line := x.answerJSON()
		if line.Outcome == CallOutcomeOk {
			x.fail(CallOutcomeError, "invalid-response") // a 2xx without the file stream: nothing was saved
		}
		return x.line
	}
	var meta struct {
		Name      string `json:"name"`
		MediaType string `json:"mediaType"`
		Bytes     int64  `json:"bytes"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(x.res.Header.Get("X-Fleet-File"), "="))
	if err == nil {
		err = json.Unmarshal(raw, &meta)
	}
	size := x.res.ContentLength
	if size < 0 {
		size = meta.Bytes // an edge may re-frame the stream without Content-Length; the gateway named the size
	}
	if err != nil || meta.Bytes < 0 || meta.Bytes != size {
		x.fail(CallOutcomeError, "invalid-response") // the tool may be repeated: nothing is lost
		return x.line
	}
	root, err := openDownloadRoot(d.Root)
	if err != nil {
		x.fail(CallOutcomeError, "path-refused")
		return x.line
	}
	defer root.Close()
	folder, err := openFolder(root, d.Folder)
	if err != nil {
		x.fail(CallOutcomeError, "path-refused")
		return x.line
	}
	defer folder.Close()
	name := SafeName(meta.Name)
	if d.Prefix != "" {
		// 200 bytes leave room for the part suffix inside a 255-byte file name.
		name = cutName(d.Prefix+"-"+name, 200)
	}
	if fi, err := folder.Lstat(name); err == nil && !fi.Mode().IsRegular() {
		x.fail(CallOutcomeError, "path-refused") // a symlink or folder planted at the final name
		return x.line
	}
	relative := filepath.Join(d.Folder...)
	final := filepath.Join(d.Root, relative, name)
	part := name + "." + partTag() + ".part"
	// Inside the opened folder: os.Root never leaves it, and O_EXCL refuses a planted part file or symlink.
	file, err := folder.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		x.fail(CallOutcomeError, "write-failed")
		return x.line
	}
	// From here bytes are read: the transfer counts what arrived.
	x.line.Transfer = &CallTransfer{Direction: CallTransferDirectionDownload, MediaType: meta.MediaType, Name: meta.Name}
	kept := false
	defer func() {
		if !kept {
			file.Close()
			folder.Remove(part)
		}
	}()
	hash, buf := sha256.New(), make([]byte, 256<<10)
	// At most one byte past the announced size: a longer stream is cut, never written to disk whole.
	body := io.LimitReader(x.reader(), size+1)
	var got int64
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			got += int64(n)
			x.line.Transfer.Bytes = got
			if _, werr := file.Write(buf[:n]); werr != nil {
				x.fail(CallOutcomeError, "write-failed")
				return x.line
			}
			hash.Write(buf[:n])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if x.dog.fired.Load() {
				x.fail(CallOutcomeError, "client-stalled")
			} else {
				x.fail(CallOutcomeError, "download-cut")
			}
			return x.line
		}
	}
	if got != size {
		x.fail(CallOutcomeError, "download-cut")
		return x.line
	}
	if file.Sync() != nil || file.Close() != nil {
		x.fail(CallOutcomeError, "write-failed")
		return x.line
	}
	rootHandle, err := root.Stat(".")
	rootPath, rootErr := os.Lstat(d.Root)
	if err != nil || rootErr != nil || !os.SameFile(rootHandle, rootPath) {
		x.fail(CallOutcomeError, "path-refused")
		return x.line
	}
	original, err := folder.Stat(".")
	current, statErr := root.Lstat(filepath.Join(".", relative))
	if err != nil || statErr != nil || !os.SameFile(original, current) {
		x.fail(CallOutcomeError, "path-refused")
		return x.line
	}
	if err := root.Rename(filepath.Join(relative, part), filepath.Join(relative, name)); err != nil {
		x.fail(CallOutcomeError, "write-failed")
		return x.line
	}
	kept = true
	sum := hex.EncodeToString(hash.Sum(nil))
	x.line.Transfer.Sha256 = &sum
	x.line.Outcome = CallOutcomeOk
	x.line.Output, _ = json.Marshal(map[string]any{"path": final, "name": meta.Name, "bytes": size, "mimeType": meta.MediaType, "sha256": sum, "callId": x.line.CallId})
	return x.line
}

// partTag makes a download's part file name unpredictable (a test fixes it).
var partTag = func() string {
	tag := make([]byte, 8)
	rand.Read(tag)
	return hex.EncodeToString(tag)
}

func openDownloadRoot(path string) (*os.Root, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	expected, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !expected.IsDir() {
		return nil, fs.ErrPermission
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(expected, actual) {
		root.Close()
		return nil, fs.ErrPermission
	}
	return root, nil
}

// openFolder opens components relative to their parents and verifies each opened directory's identity.
func openFolder(root *os.Root, parts []string) (*os.Root, error) {
	folder, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, name := range parts {
		next, err := openDownloadChild(folder, name)
		folder.Close()
		if err != nil {
			return nil, err
		}
		folder = next
	}
	return folder, nil
}

func openDownloadChild(parent *os.Root, name string) (*os.Root, error) {
	expected, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := parent.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		expected, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if !expected.IsDir() {
		return nil, fs.ErrPermission
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	actual, err := child.Stat(".")
	if err != nil || !os.SameFile(expected, actual) {
		child.Close()
		return nil, fs.ErrPermission
	}
	return child, nil
}
