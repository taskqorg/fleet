// A small SFTP version 3 client (draft-ietf-secsh-filexfer-02) for the files transport: list a folder, stat and read a
// file, one request at a time on one subsystem session per call. It reads only; the grant's OS user is chrooted to its
// granted folders on the server.
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"sort"
	"time"

	"golang.org/x/crypto/ssh"

	"fleet/contract"
)

type httpWriter = http.ResponseWriter

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

const (
	fxpInit     = 1
	fxpVersion  = 2
	fxpOpen     = 3
	fxpClose    = 4
	fxpRead     = 5
	fxpOpendir  = 11
	fxpReaddir  = 12
	fxpStat     = 17
	fxpStatus   = 101
	fxpHandle   = 102
	fxpData     = 103
	fxpName     = 104
	fxpAttrs    = 105
	fxfRead     = 1
	fxEOF       = 1
	attrSize    = 1
	attrUIDGID  = 2
	attrPerms   = 4
	attrTimes   = 8
	attrExtends = 0x80000000
	sftpChunk   = 32 << 10
)

type sftpSession struct {
	session *ssh.Session
	in      io.WriteCloser
	out     io.Reader
	id      uint32
}

func openSFTP(c *ssh.Client) (*sftpSession, error) {
	s, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	in, err := s.StdinPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	out, err := s.StdoutPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	if err := s.RequestSubsystem("sftp"); err != nil {
		s.Close()
		return nil, fmt.Errorf("sftp: %w", err)
	}
	f, err := newSFTP(in, out)
	if err != nil {
		s.Close()
		return nil, err
	}
	f.session = s
	return f, nil
}

// newSFTP speaks SFTP over a stream pair: the version handshake first.
func newSFTP(in io.WriteCloser, out io.Reader) (*sftpSession, error) {
	f := &sftpSession{in: in, out: out}
	if err := f.send(fxpInit, []byte{0, 0, 0, 3}, false); err != nil {
		return nil, err
	}
	if t, _, err := f.recv(); err != nil || t != fxpVersion {
		return nil, fmt.Errorf("sftp: no version answer")
	}
	return f, nil
}

func (f *sftpSession) close() {
	if f.session != nil {
		_ = f.session.Close()
	} else {
		_ = f.in.Close()
	}
}

// send writes one packet; withID adds the next request id.
func (f *sftpSession) send(kind byte, payload []byte, withID bool) error {
	var head []byte
	if withID {
		f.id++
		head = binary.BigEndian.AppendUint32(nil, f.id)
	}
	n := 1 + len(head) + len(payload)
	pkt := binary.BigEndian.AppendUint32(make([]byte, 0, 4+n), uint32(n))
	pkt = append(append(append(pkt, kind), head...), payload...)
	_, err := f.in.Write(pkt)
	return err
}

// recv reads one packet: its type and the body after the type (with the request id for answers).
func (f *sftpSession) recv() (byte, []byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(f.out, lenBuf[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > 1<<20 {
		return 0, nil, errors.New("sftp: bad packet length")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(f.out, buf); err != nil {
		return 0, nil, err
	}
	return buf[0], buf[1:], nil
}

func sftpString(s string) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(s))), s...)
}

type reader struct {
	b   []byte
	err error
}

func (r *reader) u32() uint32 {
	if r.err != nil || len(r.b) < 4 {
		r.err = errors.New("sftp: short packet")
		return 0
	}
	v := binary.BigEndian.Uint32(r.b)
	r.b = r.b[4:]
	return v
}

func (r *reader) u64() uint64 {
	hi, lo := r.u32(), r.u32()
	return uint64(hi)<<32 | uint64(lo)
}

func (r *reader) str() []byte {
	n := r.u32()
	if r.err != nil || uint32(len(r.b)) < n {
		r.err = errors.New("sftp: short packet")
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

type sftpAttrs struct {
	size  int64
	perms uint32
	mtime int64
}

func (r *reader) attrs() sftpAttrs {
	var a sftpAttrs
	flags := r.u32()
	if flags&attrSize != 0 {
		a.size = int64(r.u64())
	}
	if flags&attrUIDGID != 0 {
		r.u32()
		r.u32()
	}
	if flags&attrPerms != 0 {
		a.perms = r.u32()
	}
	if flags&attrTimes != 0 {
		r.u32()
		a.mtime = int64(r.u32())
	}
	if flags&attrExtends != 0 {
		for n := r.u32(); n > 0 && r.err == nil; n-- {
			r.str()
			r.str()
		}
	}
	return a
}

// call sends one request and reads its answer, checking the request id; a status answer that is not ok is an error
// (io.EOF for end of file or of a listing).
func (f *sftpSession) call(kind byte, payload []byte) (byte, *reader, error) {
	if err := f.send(kind, payload, true); err != nil {
		return 0, nil, err
	}
	t, body, err := f.recv()
	if err != nil {
		return 0, nil, err
	}
	r := &reader{b: body}
	if id := r.u32(); r.err != nil || id != f.id {
		return 0, nil, errors.New("sftp: answer to another request")
	}
	if t == fxpStatus {
		code := r.u32()
		msg := string(r.str())
		if code == 0 {
			return t, r, nil
		}
		if code == fxEOF {
			return t, r, io.EOF
		}
		return t, r, fmt.Errorf("sftp: %s (status %d)", msg, code)
	}
	return t, r, nil
}

func (f *sftpSession) handle(kind byte, payload []byte) ([]byte, error) {
	t, r, err := f.call(kind, payload)
	if err != nil {
		return nil, err
	}
	if t != fxpHandle {
		return nil, errors.New("sftp: no handle")
	}
	h := r.str()
	return append([]byte(nil), h...), r.err
}

func (f *sftpSession) closeHandle(h []byte) { _, _, _ = f.call(fxpClose, sftpString(string(h))) }

type fileEntry struct {
	Name     string `json:"name"`
	Folder   bool   `json:"folder,omitempty"`
	Bytes    int64  `json:"bytes"`
	Modified string `json:"modified,omitempty"`
}

func (f *sftpSession) list(dir string) ([]fileEntry, error) {
	h, err := f.handle(fxpOpendir, sftpString(dir))
	if err != nil {
		return nil, err
	}
	defer f.closeHandle(h)
	out := []fileEntry{}
	for {
		t, r, err := f.call(fxpReaddir, sftpString(string(h)))
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if t != fxpName {
			return nil, errors.New("sftp: no names")
		}
		for n := r.u32(); n > 0 && r.err == nil; n-- {
			name := string(r.str())
			r.str() // long name
			a := r.attrs()
			if name == "." || name == ".." {
				continue
			}
			e := fileEntry{Name: name, Folder: a.perms&0o170000 == 0o040000, Bytes: a.size}
			if a.mtime > 0 {
				e.Modified = time.Unix(a.mtime, 0).UTC().Format(time.RFC3339)
			}
			out = append(out, e)
		}
		if r.err != nil {
			return nil, r.err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *sftpSession) stat(p string) (sftpAttrs, error) {
	t, r, err := f.call(fxpStat, sftpString(p))
	if err != nil {
		return sftpAttrs{}, err
	}
	if t != fxpAttrs {
		return sftpAttrs{}, errors.New("sftp: no attributes")
	}
	a := r.attrs()
	return a, r.err
}

// streamSFTP sends one file as a download (the X-Fleet-File stream the connection client saves), with a started and
// a completed spool line like an HTTP download.
func (g *gateway) streamSFTP(w httpWriter, q *request, f *sftpSession, p string, replyCap int64) *answer {
	g.transfers.Add(1)
	defer g.transfers.Add(-1)
	detail := "get: " + p
	fail := func(status int, code, reason string) *answer {
		a := refusal(status, "error", code, reason)
		a.detail = detail
		return a
	}
	a, err := f.stat(p)
	if err != nil {
		return fail(404, "file-refused", err.Error())
	}
	if a.perms&0o170000 != 0o100000 {
		return fail(400, "bad-path", p+" is not a file; list it with files_list")
	}
	_, limit, _, _ := limits(q.c)
	if limit > 0 && a.size > limit {
		return fail(413, "reply-too-large", fmt.Sprintf("the file is larger than the connection's limit of %d bytes", limit))
	}
	h, err := f.handle(fxpOpen, append(append(sftpString(p), binary.BigEndian.AppendUint32(nil, fxfRead)...), 0, 0, 0, 0))
	if err != nil {
		return fail(404, "file-refused", err.Error())
	}
	defer f.closeHandle(h)
	name := path.Base(p)
	mediaType := mime.TypeByExtension(path.Ext(name))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	started := g.line(q, &answer{status: 200, outcome: "unknown", err: "download-unfinished", detail: detail})
	started.Phase = ptr(contract.CallPhaseStarted)
	started.Transfer = &contract.CallTransfer{Direction: contract.CallTransferDirectionDownload, Bytes: a.size, MediaType: mediaType, Name: name}
	if !g.spool.append(started) {
		return fail(503, "spool-failed", "the call could not be recorded; the file was not sent")
	}
	meta, _ := json.Marshal(downloadHeaderMeta{Bytes: a.size, MediaType: mediaType, Name: name})
	writeRaw(w, a.size, map[string]string{"X-Fleet-Call-Id": q.id, "X-Fleet-Outcome": "ok", "X-Fleet-File": base64.RawURLEncoding.EncodeToString(meta)})
	hash := sha256.New()
	var delivered int64
	code := ""
	for code == "" && delivered < a.size {
		req := append(append(sftpString(string(h)), binary.BigEndian.AppendUint64(nil, uint64(delivered))...), binary.BigEndian.AppendUint32(nil, sftpChunk)...)
		t, r, err := f.call(fxpRead, req)
		if err != nil || t != fxpData {
			code = "download-cut"
			break
		}
		data := r.str()
		if r.err != nil || len(data) == 0 || delivered+int64(len(data)) > a.size {
			code = "download-cut"
			break
		}
		written, werr := w.Write(data)
		hash.Write(data[:written])
		delivered += int64(written)
		if werr != nil {
			code = "download-cut"
		}
	}
	done := g.line(q, &answer{status: 200, outcome: "ok", detail: detail})
	done.Phase = ptr(contract.CallPhaseCompleted)
	done.Transfer = &contract.CallTransfer{Direction: contract.CallTransferDirectionDownload, Bytes: delivered, MediaType: mediaType, Name: name}
	if code == "" {
		sum := hex.EncodeToString(hash.Sum(nil))
		done.Transfer.Sha256 = &sum
	} else {
		done.Outcome, done.ErrorCode = contract.CallOutcomeError, &code
	}
	g.spool.append(done)
	if code != "" {
		panic(http.ErrAbortHandler)
	}
	return nil
}
