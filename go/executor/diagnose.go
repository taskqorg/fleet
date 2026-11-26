package main

// The executor's own log and its host_diagnose report (docs/designs/fleet-system-management.md#hosts). Every line the
// executor writes to stderr gets its UTC time and also goes to <FLEET_DATA_DIR>/logs/executor.log (rotated at
// logRotateBytes, one previous file kept); a start line and an hourly status line mean a quiet log is a stopped
// executor. When a poll reply asks (PollReply.diagnose), the next poll carries the report: the log tail, the outbox,
// refused outbox rows, failed transfers and unsettled commands.

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fleet/contract"
	"fleet/errtrack"
)

const (
	logRotateBytes = 10 << 20
	logTailLines   = 200
	logTailBytes   = 256 << 10
	statusEvery    = time.Hour
)

var (
	logPath      string // the executor log file; empty until startLog
	logStarted   = time.Now()
	diagnoseNext atomic.Bool // the last poll reply asked for a report
)

// startLog sends every later stderr line through a timestamping writer to the original stderr and the log file. It
// never fails the executor: without the file the lines still reach stderr.
func startLog(root string) {
	dir := filepath.Join(root, "logs")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	path := filepath.Join(dir, "executor.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	r, w, err := os.Pipe()
	if err != nil {
		f.Close()
		return
	}
	orig := os.Stderr
	l := &logSink{path: path, file: f, orig: orig}
	logPath = path
	os.Stderr = w
	errtrack.Go("log.copy", func() { l.copy(r) })
}

type logSink struct {
	mu   sync.Mutex
	path string
	file *os.File
	orig *os.File
}

func (l *logSink) copy(r io.Reader) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), 1<<20)
	for s.Scan() {
		l.line(s.Text())
	}
}

func (l *logSink) line(text string) {
	line := time.Now().UTC().Format("2006-01-02T15:04:05.000Z") + " " + text + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	l.orig.WriteString(line)
	if l.file == nil {
		return
	}
	l.file.WriteString(line)
	if st, err := l.file.Stat(); err == nil && st.Size() > logRotateBytes {
		l.file.Close()
		os.Rename(l.path, l.path+".1")
		l.file, _ = os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
}

// logStatus writes the start line now and a status line every statusEvery.
func logStatus(db *sql.DB, host string) {
	fmt.Fprintf(os.Stderr, "executor %s started: host %s, pid %d, log %s\n", version, host, os.Getpid(), logPath)
	errtrack.Go("log.status", func() {
		for range time.Tick(statusEvery) {
			var rows, failed, transfers int64
			db.QueryRow("SELECT count(*) FROM outbox").Scan(&rows)
			db.QueryRow("SELECT count(*) FROM outbox_failed WHERE failed_at > ?", nowMs()-statusEvery.Milliseconds()).Scan(&failed)
			db.QueryRow("SELECT count(*) FROM transfers WHERE state NOT IN ('done','failed')").Scan(&transfers)
			fmt.Fprintf(os.Stderr, "executor %s alive: up %s, outbox %d rows, %d refused in the last hour, %d transfers open\n",
				version, time.Since(logStarted).Round(time.Minute), rows, failed, transfers)
		}
	})
}

// secretText: credentials a log line may carry (a bearer token, Access headers, forge tokens, key=value secrets).
var secretText = regexp.MustCompile(`(?i)(bearer\s+|cf-access-client-(?:id|secret)[:=]\s*|(?:token|secret|password|api[_-]?key)["']?\s*[:=]\s*["']?)[^\s"',;]+|\b(?:glpat|ghp|gho|ghs|github_pat|sk|xox[bp])[-_][A-Za-z0-9_\-]{8,}`)

func redactLine(s string) string {
	return secretText.ReplaceAllStringFunc(s, func(m string) string {
		if p := secretText.FindStringSubmatch(m); p != nil && p[1] != "" {
			return p[1] + "[REDACTED]"
		}
		return "[REDACTED]"
	})
}

// logTail: the last lines of the log file (and of its previous file when the current one is short), redacted.
func logTail(path string, n int) []string {
	out := []string{}
	if path == "" {
		return out
	}
	read := func(p string) []string {
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return nil
		}
		off := max(st.Size()-logTailBytes, 0)
		buf := make([]byte, st.Size()-off)
		if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
			return nil
		}
		lines := strings.Split(strings.TrimRight(strings.ToValidUTF8(string(buf), ""), "\n"), "\n")
		if off > 0 && len(lines) > 0 {
			lines = lines[1:] // a cut first line
		}
		return lines
	}
	lines := read(path)
	if len(lines) < n {
		lines = append(read(path+".1"), lines...)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, l := range lines {
		if l != "" {
			out = append(out, redactLine(l))
		}
	}
	return out
}

// diagnosis builds the report for host_diagnose from the journal and the log.
func diagnosis(db *sql.DB) *contract.HostDiagnosis {
	pid, started := int64(os.Getpid()), logStarted.UnixMilli()
	d := &contract.HostDiagnosis{At: nowMs(), Pid: &pid, StartedAt: &started, LogTail: logTail(logPath, logTailLines),
		Outbox: contract.DiagnosisOutbox{Routes: []contract.DiagnosisRoute{}}, OutboxFailed: []contract.DiagnosisOutboxFailed{},
		TransfersFailed: []contract.DiagnosisTransfer{}, Commands: []contract.DiagnosisCommand{}}
	if logPath != "" {
		d.LogPath = &logPath
	}
	if rs, err := db.Query("SELECT route,count(*) FROM outbox GROUP BY route ORDER BY count(*) DESC"); err == nil {
		for rs.Next() {
			var r contract.DiagnosisRoute
			if rs.Scan(&r.Route, &r.Rows) == nil {
				d.Outbox.Routes = append(d.Outbox.Routes, r)
				d.Outbox.Rows += r.Rows
			}
		}
		rs.Close()
	}
	text := func(body, key string) *string {
		var m map[string]any
		if json.Unmarshal([]byte(body), &m) != nil {
			return nil
		}
		if s, ok := m[key].(string); ok && s != "" {
			return &s
		}
		return nil
	}
	if rs, err := db.Query("SELECT route,body,status,reason,failed_at FROM outbox_failed WHERE failed_at > ? ORDER BY rowid DESC LIMIT 20", nowMs()-24*time.Hour.Milliseconds()); err == nil {
		for rs.Next() {
			var f contract.DiagnosisOutboxFailed
			var body string
			if rs.Scan(&f.Route, &body, &f.Status, &f.Reason, &f.At) == nil {
				f.Reason = redactLine(f.Reason)
				f.ExecutionId, f.ArtifactId = text(body, "executionId"), text(body, "artifactId")
				d.OutboxFailed = append(d.OutboxFailed, f)
			}
		}
		rs.Close()
	}
	if rs, err := db.Query("SELECT id,kind,body,coalesce(response,''),created_at FROM transfers WHERE state='failed' AND created_at > ? ORDER BY created_at DESC LIMIT 20",
		nowMs()-24*time.Hour.Milliseconds()); err == nil {
		for rs.Next() {
			var t contract.DiagnosisTransfer
			var body, resp string
			if rs.Scan(&t.Id, &t.Kind, &body, &resp, &t.At) == nil {
				t.Name, t.ExecutionId = text(body, "name"), text(body, "executionId")
				t.Error = redactLine(deref(text(resp, "error")))
				d.TransfersFailed = append(d.TransfersFailed, t)
			}
		}
		rs.Close()
	}
	if rs, err := db.Query("SELECT id,body FROM commands WHERE terminal=0 ORDER BY rowid LIMIT 50"); err == nil {
		for rs.Next() {
			var c contract.DiagnosisCommand
			var body string
			if rs.Scan(&c.Id, &body) == nil {
				c.Kind, c.ExecutionId, c.State = text(body, "kind"), text(body, "executionId"), "open"
				d.Commands = append(d.Commands, c)
			}
		}
		rs.Close()
	}
	return d
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
