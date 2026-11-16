// Browser errors of the operator app (docs/designs/fleet-telemetry.md section 9). The app reports uncaught errors,
// unhandled rejections and render crashes with client_error_report; reports group by a fingerprint of name, message and
// normalized top stack frame. A new group opens a needs-you problem for the owner; agents and the owner read groups with
// client_errors_list and client_error_get. Every reported string is untrusted browser data: the problem text holds only
// fixed wording and the fingerprint. Storage is bounded without a report ever deleting another group: each group keeps
// its latest clientErrorReportsKept reports; one person opens at most clientErrorNewPerHour groups an hour and
// clientErrorPerPerson in all, people without full access at most clientErrorGroupsKept together; past a limit a report
// counts into the fixed over-limit group. Only a person's first clientErrorProblemsPerDay groups a day open a problem;
// the hourly client-errors pass deletes groups unseen for clientErrorRetention whose problem is not open or answered.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fleet/contract"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	clientErrorReportsKept    = 20                  // clientErrors.reportsKept
	clientErrorGroupsKept     = 1000                // clientErrors.groupsKept: groups opened by people without full access; past it they are refused (counted)
	clientErrorNewPerHour     = 20                  // clientErrors.newGroupsPerHour, per person
	clientErrorPerPerson      = 100                 // clientErrors.groupsPerPerson: total groups one person opened
	clientErrorProblemsPerDay = 5                   // clientErrors.problemsPerDay: a person's groups a day that get a problem
	clientErrorOverLimit      = "over-limit"        // the group that counts reports refused a new group
	clientErrorRetention      = 30 * 24 * time.Hour // clientErrors.retention
	clientErrorPeopleKept     = 100
	clientErrorVersions       = 20
	clientErrorCause          = "problem:client-error:"
)

// The byte limits of ClientErrorReportRequest (contracts/ops-telemetry.schema.json); a larger field is 400.
var clientErrorLimits = []struct {
	field    string
	min, max int
	value    func(r *contract.ClientErrorReportRequest) string
}{
	{"name", 1, 200, func(r *contract.ClientErrorReportRequest) string { return r.Name }},
	{"message", 0, 2000, func(r *contract.ClientErrorReportRequest) string { return r.Message }},
	{"stack", 0, 8192, func(r *contract.ClientErrorReportRequest) string { return r.Stack }},
	{"path", 1, 2048, func(r *contract.ClientErrorReportRequest) string { return r.Path }},
	{"version", 0, 200, func(r *contract.ClientErrorReportRequest) string { return r.Version }},
	{"userAgent", 0, 512, func(r *contract.ClientErrorReportRequest) string { return r.UserAgent }},
	{"at", 1, 64, func(r *contract.ClientErrorReportRequest) string { return r.At }},
}

func init() {
	operation("client_error_report", false, typedOp(clientErrorReport))
	operation("client_errors_list", true, ownerFirst("Browser errors need full access", typedOp(clientErrorsList)))
	operation("client_error_get", true, ownerFirst("Browser errors need full access", typedOp(clientErrorGet)))
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "client-errors", envMs("FLEET_CLIENT_ERRORS_TICK_MS", 3600_000), func(ctx context.Context) error {
			n, err := clientErrorsPrune(ctx, a, time.Now().Add(-clientErrorRetention))
			if err != nil {
				log.Printf("client errors prune: %v", err)
			} else if n > 0 {
				log.Printf("client errors prune: %d groups", n)
			}
			return err
		})
	})
}

// clientErrorsPrune: the scheduled retention (never a report): groups unseen since before cutoff, unless a problem
// question holds their cause (open, or answered by the owner).
func clientErrorsPrune(ctx context.Context, a *App, cutoff time.Time) (int64, error) {
	var n int64
	err := runTx(ctx, a, nil, true, "", func(q *Request) error {
		t, err := q.tx.Exec(q.ctx(), `DELETE FROM client_error c WHERE c.last_seen < $1
			AND NOT EXISTS (SELECT 1 FROM question x WHERE x.cause='`+clientErrorCause+`'||c.fingerprint AND x.state IN ('open','answered'))`, cutoff)
		n = t.RowsAffected()
		return err
	})
	return n, err
}

var (
	// A V8 frame ("at fn (url:1:2)", "at url:1:2") or a Firefox/Safari one ("fn@url:1:2", "global code@url:1:2").
	clientFrameLine   = regexp.MustCompile(`^(at\s.+|[^@\s][^@]*@\S+:\d+(:\d+)?|@\S+:\d+(:\d+)?)$`)
	clientFrameOrigin = regexp.MustCompile(`[a-z][a-z0-9+.-]*://[^/\s)]+`)
	clientFrameHash   = regexp.MustCompile(`-[A-Za-z0-9]{8,}\.(m?js|css)\b`)
	clientFramePos    = regexp.MustCompile(`(:\d+)+`)
)

// clientTopFrame: the first stack frame in a script file (a built-in frame such as "at Date.toISOString (<anonymous>)"
// names no call site; it is used only when no script frame follows), without origin, asset content hash and
// line/column, so one error keeps its fingerprint across hosts and rebuilds that only move it.
func clientTopFrame(stack string) string {
	builtin := ""
	for _, l := range strings.Split(stack, "\n") {
		l = strings.TrimSpace(l)
		if !clientFrameLine.MatchString(l) {
			continue
		}
		if !clientFrameOrigin.MatchString(l) && !strings.Contains(l, "/") {
			if builtin == "" {
				builtin = l
			}
			continue
		}
		l = clientFrameOrigin.ReplaceAllString(l, "")
		l = clientFrameHash.ReplaceAllString(l, ".$1")
		return strings.TrimSpace(clientFramePos.ReplaceAllString(l, ""))
	}
	return builtin
}

func clientFingerprint(name, message, top string) string {
	h := sha256.Sum256([]byte(name + "\n" + message + "\n" + top))
	return hex.EncodeToString(h[:8])
}

func clientErrorReport(q *Request, r *contract.ClientErrorReportRequest) (contract.ClientErrorReportReply, error) {
	var rep contract.ClientErrorReportReply
	for _, l := range clientErrorLimits {
		if n := len(l.value(r)); n < l.min || n > l.max {
			if n > l.max {
				return rep, fail(400, "Field "+l.field+" is longer than "+itoa(l.max)+" bytes")
			}
			return rep, fail(400, "Expected "+l.field)
		}
	}
	if !strings.HasPrefix(r.Path, "/") {
		return rep, fail(400, "Invalid path: expected the app path starting with /")
	}
	if r.Count < 1 || r.Count > 1_000_000 {
		return rep, fail(400, "Invalid count: expected 1..1000000")
	}
	at, err := time.Parse(time.RFC3339Nano, r.At)
	if err != nil {
		return rep, fail(400, "Invalid at: expected RFC 3339")
	}
	person, full := "", fullAccess(q.person)
	if q.person != nil {
		person = q.person.Email
	}
	top := clientTopFrame(r.Stack)
	fp := clientFingerprint(r.Name, r.Message, top)
	// The groups are system data the reporting person may not read: written in the coordinator's own scope.
	return unscoped(q, func() (contract.ClientErrorReportReply, error) {
		name, message, source := r.Name, r.Message, string(r.Source)
		known, err := has(q, "SELECT 1 FROM client_error WHERE fingerprint=$1", fp)
		if err != nil {
			return rep, err
		}
		if !known {
			// A new group: within the person's hourly and total allowances and, for a person without full access, the
			// members' global cap; else counted in the over-limit group. One lock orders concurrent new groups, so
			// parallel tabs cannot all pass the same count.
			if err := exec(q, "SELECT pg_advisory_xact_lock(hashtextextended('client-error:new-group',0))"); err != nil {
				return rep, err
			}
			var hour, mine, members int
			if err := q.tx.QueryRow(q.ctx(), `SELECT count(*) FILTER (WHERE first_person=$1 AND first_seen > now()-interval '1 hour'),
				count(*) FILTER (WHERE first_person=$1), count(*) FILTER (WHERE NOT first_full) FROM client_error WHERE fingerprint<>$2`, person, clientErrorOverLimit).Scan(&hour, &mine, &members); err != nil {
				return rep, err
			}
			if hour >= clientErrorNewPerHour || mine >= clientErrorPerPerson || (!full && members >= clientErrorGroupsKept) {
				fp, top = clientErrorOverLimit, ""
				name, message, source = "OverLimit", "Reports refused a new group: the reporter's hourly new-group limit or the global group cap was reached", "error"
			}
		}
		var inserted bool
		err = q.tx.QueryRow(q.ctx(), `INSERT INTO client_error AS c(fingerprint,name,message,top_frame,source,count,last_version,last_path,last_user_agent,people,versions,first_person,first_full)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,CASE WHEN $10::text='' THEN '{}'::text[] ELSE ARRAY[$10::text] END,CASE WHEN $7::text='' THEN '{}'::text[] ELSE ARRAY[$7::text] END,$10,$13)
			ON CONFLICT (fingerprint) DO UPDATE SET last_seen=now(), count=c.count+EXCLUDED.count, last_version=EXCLUDED.last_version,
			  last_path=EXCLUDED.last_path, last_user_agent=EXCLUDED.last_user_agent,
			  people=CASE WHEN $10::text='' OR $10::text=ANY(c.people) OR cardinality(c.people)>=$11 THEN c.people ELSE c.people||$10::text END,
			  versions=CASE WHEN $7::text='' OR c.versions[cardinality(c.versions)]=$7::text THEN c.versions
			    ELSE (SELECT v[greatest(1,cardinality(v)-$12+1):] FROM (SELECT array_remove(c.versions,$7::text)||$7::text AS v) s) END
			RETURNING count,(xmax=0)`,
			fp, name, message, top, source, r.Count, r.Version, r.Path, r.UserAgent, person, clientErrorPeopleKept, clientErrorVersions, full).Scan(&rep.Count, &inserted)
		if err != nil {
			return rep, err
		}
		rep.Fingerprint, rep.First = fp, inserted
		if err := exec(q, "INSERT INTO client_error_report(fingerprint,client_at,person,version,path,user_agent,count,source,stack) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)",
			fp, at, person, r.Version, r.Path, r.UserAgent, r.Count, string(r.Source), r.Stack); err != nil {
			return rep, err
		}
		// The group's own oldest reports only; a report never deletes another group or closes a problem.
		if err := exec(q, "DELETE FROM client_error_report WHERE fingerprint=$1 AND seq < (SELECT min(seq) FROM (SELECT seq FROM client_error_report WHERE fingerprint=$1 ORDER BY seq DESC LIMIT $2) k)", fp, clientErrorReportsKept); err != nil {
			return rep, err
		}
		if !inserted {
			return rep, nil
		}
		if fp != clientErrorOverLimit {
			// Only a person's first groups of a day get a problem: later ones are stored unpinned and age out.
			var today int
			if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM client_error WHERE first_person=$1 AND first_seen > now()-interval '24 hours' AND fingerprint<>$2", person, clientErrorOverLimit).Scan(&today); err != nil {
				return rep, err
			}
			if today > clientErrorProblemsPerDay {
				return rep, nil
			}
		}
		if h, err := problemHeld(q, clientErrorCause+fp); err != nil || h != nil {
			return rep, err
		}
		return rep, problemAsk(q, "direct", Problem{Cause: clientErrorCause + fp, Text: clientErrorText(fp), Data: doc{"fingerprint": fp}})
	})
}

// clientErrorText: fixed wording and the fingerprint only. Reported strings are untrusted and never enter a question
// that people and agents read as Fleet's own words.
func clientErrorText(fp string) string {
	if fp == clientErrorOverLimit {
		return "Browser error reports exceed the new-group limits; they are counted in one group: client_error_get {\"fingerprint\": \"" + fp + "\"}."
	}
	return "A browser error group appeared: client_error_get {\"fingerprint\": \"" + fp + "\"}."
}

const clientErrorColumns = `c.fingerprint,c.name,c.message,c.top_frame,c.source,c.first_seen,c.last_seen,c.count,c.last_version,c.last_path,c.last_user_agent,c.people,c.versions,
	(SELECT x.id FROM question x WHERE x.cause='` + clientErrorCause + `'||c.fingerprint AND x.state IN ('open','answered') ORDER BY x.seq DESC LIMIT 1)`

func scanClientErrors(rows pgx.Rows) ([]contract.ClientError, error) {
	out := []contract.ClientError{}
	for rows.Next() {
		var e contract.ClientError
		var first, last time.Time
		var source string
		if err := rows.Scan(&e.Fingerprint, &e.Name, &e.Message, &e.TopFrame, &source, &first, &last, &e.Count, &e.LastVersion, &e.LastPath, &e.LastUserAgent, &e.People, &e.Versions, &e.QuestionId); err != nil {
			rows.Close()
			return nil, err
		}
		e.Source, e.FirstSeen, e.LastSeen = contract.ClientErrorSource(source), clientTime(first), clientTime(last)
		out = append(out, e)
	}
	return out, rows.Err()
}

func clientTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func clientErrorsList(q *Request, r *contract.ClientErrorsListRequest) (contract.ClientErrorsListReply, error) {
	var rep contract.ClientErrorsListReply
	limit := int64(50)
	if r.Limit != nil {
		if *r.Limit < 1 || *r.Limit > 500 {
			return rep, fail(400, "Invalid limit: expected 1..500")
		}
		limit = *r.Limit
	}
	var since *time.Time
	if r.Since != nil {
		t, err := time.Parse(time.RFC3339Nano, *r.Since)
		if err != nil {
			return rep, fail(400, "Invalid since: expected RFC 3339")
		}
		since = &t
	}
	rows, err := q.tx.Query(q.ctx(), "SELECT "+clientErrorColumns+` FROM client_error c
		WHERE ($1::timestamptz IS NULL OR c.last_seen >= $1) AND ($2::text IS NULL OR $2 = ANY(c.versions)) AND ($3::text IS NULL OR c.fingerprint = $3)
		ORDER BY c.last_seen DESC, c.fingerprint LIMIT $4`, since, r.Version, r.Fingerprint, limit)
	if err != nil {
		return rep, err
	}
	rep.Errors, err = scanClientErrors(rows)
	return rep, err
}

func clientErrorGet(q *Request, r *contract.ClientErrorGetRequest) (contract.ClientErrorGetReply, error) {
	var rep contract.ClientErrorGetReply
	rows, err := q.tx.Query(q.ctx(), "SELECT "+clientErrorColumns+" FROM client_error c WHERE c.fingerprint=$1", r.Fingerprint)
	if err != nil {
		return rep, err
	}
	groups, err := scanClientErrors(rows)
	if err != nil {
		return rep, err
	}
	if len(groups) == 0 {
		return rep, fail(404, "Unknown browser error")
	}
	rep.Error = groups[0]
	rows, err = q.tx.Query(q.ctx(), "SELECT at,client_at,person,version,path,user_agent,count,source,stack FROM client_error_report WHERE fingerprint=$1 ORDER BY seq DESC LIMIT $2", r.Fingerprint, clientErrorReportsKept)
	if err != nil {
		return rep, err
	}
	defer rows.Close()
	rep.Reports = []contract.ClientErrorReport{}
	for rows.Next() {
		var x contract.ClientErrorReport
		var at, clientAt time.Time
		var source string
		if err := rows.Scan(&at, &clientAt, &x.Person, &x.Version, &x.Path, &x.UserAgent, &x.Count, &source, &x.Stack); err != nil {
			return rep, err
		}
		x.At, x.ClientAt, x.Source = clientTime(at), clientTime(clientAt), contract.ClientErrorSource(source)
		rep.Reports = append(rep.Reports, x)
	}
	return rep, rows.Err()
}
