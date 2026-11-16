// Browser errors of the operator app (docs/designs/fleet-telemetry.md section 9). The app reports uncaught errors,
// unhandled rejections and render crashes with client_error_report; reports group by a fingerprint of name, message and
// normalized top stack frame. A new group opens a needs-you problem for the owner; agents and the owner read groups with
// client_errors_list and client_error_get. Storage is bounded: the latest clientErrorReportsKept reports per group, at
// most clientErrorGroupsKept groups, and groups unseen for clientErrorRetention go (with their problem).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fleet/contract"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	clientErrorReportsKept = 20                  // clientErrors.reportsKept
	clientErrorGroupsKept  = 1000                // clientErrors.groupsKept
	clientErrorRetention   = 30 * 24 * time.Hour // clientErrors.retention
	clientErrorPeopleKept  = 100
	clientErrorVersions    = 20
	clientErrorCause       = "problem:client-error:"
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
}

var (
	// A V8 frame ("at fn (url:1:2)", "at url:1:2") or a Firefox/Safari one ("fn@url:1:2").
	clientFrameLine   = regexp.MustCompile(`^(at\s.+|\S*@\S+:\d+(:\d+)?)$`)
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
	person := ""
	if q.person != nil {
		person = q.person.Email
	}
	top := clientTopFrame(r.Stack)
	fp := clientFingerprint(r.Name, r.Message, top)
	// The groups are system data the reporting person may not read: written in the coordinator's own scope.
	return unscoped(q, func() (contract.ClientErrorReportReply, error) {
		var inserted bool
		err := q.tx.QueryRow(q.ctx(), `INSERT INTO client_error AS c(fingerprint,name,message,top_frame,source,count,last_version,last_path,last_user_agent,people,versions)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,CASE WHEN $10::text='' THEN '{}'::text[] ELSE ARRAY[$10::text] END,CASE WHEN $7::text='' THEN '{}'::text[] ELSE ARRAY[$7::text] END)
			ON CONFLICT (fingerprint) DO UPDATE SET last_seen=now(), count=c.count+EXCLUDED.count, last_version=EXCLUDED.last_version,
			  last_path=EXCLUDED.last_path, last_user_agent=EXCLUDED.last_user_agent,
			  people=CASE WHEN $10::text='' OR $10::text=ANY(c.people) OR cardinality(c.people)>=$11 THEN c.people ELSE c.people||$10::text END,
			  versions=CASE WHEN $7::text='' OR c.versions[cardinality(c.versions)]=$7::text THEN c.versions
			    ELSE (SELECT v[greatest(1,cardinality(v)-$12+1):] FROM (SELECT array_remove(c.versions,$7::text)||$7::text AS v) s) END
			RETURNING count,(xmax=0)`,
			fp, r.Name, r.Message, top, string(r.Source), r.Count, r.Version, r.Path, r.UserAgent, person, clientErrorPeopleKept, clientErrorVersions).Scan(&rep.Count, &inserted)
		if err != nil {
			return rep, err
		}
		rep.Fingerprint, rep.First = fp, inserted
		if err := exec(q, "INSERT INTO client_error_report(fingerprint,client_at,person,version,path,user_agent,count,source,stack) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)",
			fp, at, person, r.Version, r.Path, r.UserAgent, r.Count, string(r.Source), r.Stack); err != nil {
			return rep, err
		}
		if err := exec(q, "DELETE FROM client_error_report WHERE fingerprint=$1 AND seq < (SELECT min(seq) FROM (SELECT seq FROM client_error_report WHERE fingerprint=$1 ORDER BY seq DESC LIMIT $2) k)", fp, clientErrorReportsKept); err != nil {
			return rep, err
		}
		if !inserted {
			return rep, nil
		}
		// Only a new group can push the store past its bounds; pruned groups take their problem with them.
		gone, err := queryRows(q, `DELETE FROM client_error WHERE last_seen < $1
			OR fingerprint IN (SELECT fingerprint FROM client_error ORDER BY last_seen DESC, fingerprint OFFSET $2) RETURNING fingerprint`,
			time.Now().Add(-clientErrorRetention), clientErrorGroupsKept)
		if err != nil {
			return rep, err
		}
		for _, g := range gone {
			if err := problemClose(q, clientErrorCause+astr(g["fingerprint"])); err != nil {
				return rep, err
			}
		}
		if h, err := problemHeld(q, clientErrorCause+fp); err != nil || h != nil {
			return rep, err
		}
		return rep, problemAsk(q, "direct", Problem{Cause: clientErrorCause + fp, Text: clientErrorText(r, fp), Data: doc{"fingerprint": fp}})
	})
}

func clientErrorText(r *contract.ClientErrorReportRequest, fp string) string {
	msg := r.Message
	if len(msg) > 200 {
		msg = strings.ToValidUTF8(msg[:200], "") + "…"
	}
	return "Browser error on " + r.Path + ": " + r.Name + ": " + msg + ". Details: client_error_get {\"fingerprint\": \"" + fp + "\"}."
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
		e.Source, e.FirstSeen, e.LastSeen = contract.ClientErrorSource(source), rfc3339(first), rfc3339(last)
		out = append(out, e)
	}
	return out, rows.Err()
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

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
		x.At, x.ClientAt, x.Source = rfc3339(at), rfc3339(clientAt), contract.ClientErrorSource(source)
		rep.Reports = append(rep.Reports, x)
	}
	return rep, rows.Err()
}
