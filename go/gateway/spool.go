package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fleet/contract"
)

func writeAtomic(path string, data []byte) error {
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// day is the UTC date of a millisecond time, for the spool file name.
func day(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02") }

// spool keeps one JSON line per call in <state>/spool/<day>.jsonl.
// Durability contract: append returns only after the line is fsynced (and, for a new day file, its directory entry),
// so a call answered to its caller survives a machine crash. A line that cannot be made durable counts in the lost
// counter, persisted in <state>/spool-lost; lostRecorded is false while that counter on disk is behind the real one.
type spool struct {
	dir          string
	lost         atomic.Uint64
	lostRecorded atomic.Bool
	write        sync.Mutex
}

func openSpool(stateDir string) (*spool, error) {
	s := &spool{dir: filepath.Join(stateDir, "spool")}
	s.lostRecorded.Store(true)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, fmt.Errorf("spool directory: %w", err)
	}
	if text, err := os.ReadFile(filepath.Join(stateDir, "spool-lost")); err == nil {
		lost, _ := strconv.ParseUint(strings.TrimSpace(string(text)), 10, 64)
		s.lost.Store(lost)
	}
	return s, nil
}

// append reports whether the line is durable.
func (s *spool) append(line contract.CallLine) bool {
	s.write.Lock()
	defer s.write.Unlock()
	data, _ := json.Marshal(line)
	err := s.appendDurably(append(data, '\n'))
	if err != nil {
		lost := s.lost.Add(1)
		logf("spool write failed (%v); lost %d", err, lost)
		path := filepath.Join(filepath.Dir(s.dir), "spool-lost")
		if err := writeAtomic(path, []byte(strconv.FormatUint(lost, 10))); err != nil {
			s.lostRecorded.Store(false)
			logf("lost counter not persisted (%v); %s is behind by at least one line", err, path)
		} else {
			s.lostRecorded.Store(true)
		}
	}
	return err == nil
}

func (s *spool) appendDurably(data []byte) error {
	path := filepath.Join(s.dir, day(nowMs())+".jsonl")
	_, statErr := os.Stat(path)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil && statErr != nil {
		// A new file: its name is durable only once the directory is synced.
		var dir *os.File
		if dir, err = os.Open(s.dir); err == nil {
			err = dir.Sync()
			dir.Close()
		}
	}
	return err
}
