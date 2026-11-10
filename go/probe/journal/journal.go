// Store spike without SQLite: one append-only file of framed commits. A commit is a list of row writes applied together;
// every process (executor, its threads, `mcp delegation` children) takes flock before appending and replays the tail
// others wrote, so all of them see one order. A torn tail (crash mid-append) fails its checksum and is cut by the next
// writer. Unix only: Windows needs LockFileEx instead of flock.
package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"os"
	"syscall"
)

type Op struct {
	Table string          `json:"t"`
	Key   string          `json:"k"`
	Value json.RawMessage `json:"v,omitempty"` // empty deletes the row
}

type Journal struct {
	f    *os.File
	off  int64                                 // end of the last good commit replayed
	Rows map[string]map[string]json.RawMessage // table -> key -> row
	Seq  int64                                 // commits seen; gives the outbox its order
}

func Open(path string) (*Journal, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	j := &Journal{f: f, Rows: map[string]map[string]json.RawMessage{}}
	return j, j.Refresh()
}

// Refresh replays commits appended since the last call (by this or another process).
func (j *Journal) Refresh() error {
	for {
		var head [8]byte
		if _, err := j.f.ReadAt(head[:], j.off); err != nil {
			return nil // end, or a torn header
		}
		n := int64(binary.LittleEndian.Uint32(head[:4]))
		body := make([]byte, n)
		if _, err := j.f.ReadAt(body, j.off+8); err != nil || crc32.ChecksumIEEE(body) != binary.LittleEndian.Uint32(head[4:]) {
			return nil // torn tail: not acknowledged, ignored
		}
		var ops []Op
		if err := json.Unmarshal(body, &ops); err != nil {
			return err
		}
		for _, o := range ops {
			t := j.Rows[o.Table]
			if t == nil {
				t = map[string]json.RawMessage{}
				j.Rows[o.Table] = t
			}
			if len(o.Value) == 0 {
				delete(t, o.Key)
			} else {
				t[o.Key] = o.Value
			}
		}
		j.off += 8 + n
		j.Seq++
	}
}

// Commit runs check against the current state under the writer lock (the BEGIN IMMEDIATE equivalent), then appends the ops
// it returns as one commit and fsyncs before returning. A nil error means the commit survives kill -9.
func (j *Journal) Commit(check func(*Journal) ([]Op, error)) error {
	if err := syscall.Flock(int(j.f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(j.f.Fd()), syscall.LOCK_UN)
	if err := j.Refresh(); err != nil {
		return err
	}
	ops, err := check(j)
	if err != nil || len(ops) == 0 {
		return err
	}
	body, _ := json.Marshal(ops)
	frame := make([]byte, 8, 8+len(body))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(body)))
	binary.LittleEndian.PutUint32(frame[4:], crc32.ChecksumIEEE(body))
	frame = append(frame, body...)
	if err := j.f.Truncate(j.off); err != nil { // cut a torn tail left by a crashed writer
		return err
	}
	if _, err := j.f.WriteAt(frame, j.off); err != nil {
		return err
	}
	if err := j.f.Sync(); err != nil { // F_FULLFSYNC on darwin
		return err
	}
	return j.Refresh()
}

var ErrConflict = errors.New("command identity conflict")
