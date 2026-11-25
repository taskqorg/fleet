package main

import (
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"fleet/contract"
)

// Requirement: a read grant runs one read statement; anything else is refused before it reaches the server (whose
// grants refuse it again).
func TestAccessSQLText(t *testing.T) {
	read := contract.GatewayAccessLevelRead
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"SELECT id FROM project LIMIT 5;", "SELECT id FROM project LIMIT 5", true},
		{"  -- note\n/* x */ select 1", "select 1", true},
		{"WITH a AS (SELECT 1) SELECT * FROM a", "WITH a AS (SELECT 1) SELECT * FROM a", true},
		{"SHOW TABLES", "SHOW TABLES", true},
		{"SELECT 1; DROP TABLE project", "", false},
		{"DELETE FROM project", "", false},
		{"INSERT INTO project SELECT * FROM project", "", false},
		{"selectx 1", "", false},
		{"", "", false},
		{"/* open", "", false},
	} {
		got, err := sqlText(c.in, read)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("%q: %q %v, want %q ok %v", c.in, got, err, c.want, c.ok)
		}
	}
	if _, err := sqlText("UPDATE project SET a=1", contract.GatewayAccessLevelWrite); err != nil {
		t.Errorf("a write grant refused an update: %v", err)
	}
}

// Requirement: an agent names files by the server's path or by the chroot's folder; nothing outside the granted
// folders is reachable.
func TestAccessChrootPath(t *testing.T) {
	granted := []string{"/home/revilio/revilio.com/web/uploads"}
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"/home/revilio/revilio.com/web/uploads/12/a.pdf", "/uploads/12/a.pdf", true},
		{"/home/revilio/revilio.com/web/uploads", "/uploads", true},
		{"/uploads/12", "/uploads/12", true},
		{"uploads/12", "/uploads/12", true},
		{"/", "/", true},
		{"/home/revilio/revilio.com/config/db.php", "", false},
		{"/uploads/../etc/passwd", "", false},
		{"/home/revilio/revilio.com/web/uploadsx/1", "", false},
		{"/uploads/12/scan..v2.pdf", "/uploads/12/scan..v2.pdf", true},
	} {
		got, err := chrootPath(granted, c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("%q: %q %v, want %q ok %v", c.in, got, err, c.want, c.ok)
		}
	}
}

// fakeSFTP serves one folder /uploads with file a.txt over the SFTP v3 packets the client sends.
func fakeSFTP(t *testing.T, in io.Reader, out io.Writer, content string) {
	pkt := func(kind byte, body []byte) {
		b := binary.BigEndian.AppendUint32(nil, uint32(1+len(body)))
		out.Write(append(append(b, kind), body...))
	}
	str := func(s string) []byte { return append(binary.BigEndian.AppendUint32(nil, uint32(len(s))), s...) }
	status := func(id uint32, code uint32) []byte {
		b := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, id), code)
		return append(append(b, str("msg")...), str("")...)
	}
	listed := false
	for {
		var l [4]byte
		if _, err := io.ReadFull(in, l[:]); err != nil {
			return
		}
		buf := make([]byte, binary.BigEndian.Uint32(l[:]))
		io.ReadFull(in, buf)
		kind, body := buf[0], buf[1:]
		if kind == fxpInit {
			pkt(fxpVersion, []byte{0, 0, 0, 3})
			continue
		}
		id := binary.BigEndian.Uint32(body)
		rest := body[4:]
		arg := string(rest[4 : 4+binary.BigEndian.Uint32(rest)])
		idb := binary.BigEndian.AppendUint32(nil, id)
		switch kind {
		case fxpOpendir, fxpOpen:
			pkt(fxpHandle, append(idb, str("h:"+arg)...))
		case fxpReaddir:
			if listed {
				pkt(fxpStatus, status(id, fxEOF))
				continue
			}
			listed = true
			b := binary.BigEndian.AppendUint32(idb, 2)
			b = append(append(b, str("a.txt")...), str("")...)
			b = binary.BigEndian.AppendUint32(b, attrSize|attrPerms)
			b = binary.BigEndian.AppendUint64(b, uint64(len(content)))
			b = binary.BigEndian.AppendUint32(b, 0o100644)
			b = append(append(b, str("12")...), str("")...)
			b = binary.BigEndian.AppendUint32(b, attrPerms)
			b = binary.BigEndian.AppendUint32(b, 0o040755)
			pkt(fxpName, b)
		case fxpStat:
			b := binary.BigEndian.AppendUint32(idb, attrSize|attrPerms)
			b = binary.BigEndian.AppendUint64(b, uint64(len(content)))
			b = binary.BigEndian.AppendUint32(b, 0o100644)
			pkt(fxpAttrs, b)
		case fxpRead:
			r := rest[4+len(arg):]
			off := binary.BigEndian.Uint64(r)
			if off >= uint64(len(content)) {
				pkt(fxpStatus, status(id, fxEOF))
				continue
			}
			end := min(uint64(len(content)), off+5)
			pkt(fxpData, append(idb, str(content[off:end])...))
		case fxpClose:
			pkt(fxpStatus, status(id, 0))
		default:
			pkt(fxpStatus, status(id, 8))
		}
	}
}

// Requirement: the SFTP client lists a folder (sorted, folders marked) and reads a file to its end.
func TestAccessSFTPClient(t *testing.T) {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	content := "hello project files"
	go fakeSFTP(t, sr, sw, content)
	f, err := newSFTP(cw, cr)
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	entries, err := f.list("/uploads")
	if err != nil || len(entries) != 2 || entries[0].Name != "12" || !entries[0].Folder || entries[1].Bytes != int64(len(content)) {
		t.Fatalf("list: %+v %v", entries, err)
	}
	a, err := f.stat("/uploads/a.txt")
	if err != nil || a.size != int64(len(content)) || a.perms&0o170000 != 0o100000 {
		t.Fatalf("stat: %+v %v", a, err)
	}
	h, err := f.handle(fxpOpen, sftpString("/uploads/a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for off := uint64(0); ; {
		req := append(append(sftpString(string(h)), binary.BigEndian.AppendUint64(nil, off)...), binary.BigEndian.AppendUint32(nil, sftpChunk)...)
		kind, r, err := f.call(fxpRead, req)
		if err == io.EOF {
			break
		}
		if err != nil || kind != fxpData {
			t.Fatalf("read: %v", err)
		}
		d := r.str()
		got.Write(d)
		off += uint64(len(d))
	}
	if got.String() != content {
		t.Fatalf("read %q", got.String())
	}
}
