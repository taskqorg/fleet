// Conversation titles (docs/designs/fleet-conversations.md, Titles): a session's title is set explicitly by a person or
// an agent (session_rename) or generated once by a small model after the first reply. A generated title never replaces
// a set one, and a set one is never regenerated. The same pass backfills older conversations that have none.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"fleet/contract"
)

// The title model: the cheapest text model of a connection granted to every project. Its calls are Fleet's own
// background calls (system activity) of the session's project, each a tool_run with the provider's token count.
const (
	titleConnection  = "google-ai"
	titleTool        = "generate_content"
	titleModel       = "gemini-3.5-flash-lite"
	titleBatch       = 10              // sessions per pass; ponytail: a backfill of N conversations takes N/10 passes
	titleRetry       = time.Hour       // a failed call is tried again after this
	titleRetryShared = 5 * time.Minute // a failure that is not the session's own: soon, without a try
	titleTries       = 3               // then the session keeps its first message (ponytail: Rename still names it)
	titleWaitReply   = "2 minutes"     // a first agent run still going after this (steered, long) is titled from what it has
	titleMaxRunes    = 80
	titleSystem      = "You name conversations between a person and an AI agent. Reply with the title only: 3 to 7 words in sentence case, " +
		"in the language the person writes the task in, no quotes, no final period. Begin with a task code the person gives (such as ABC-12), if any. " +
		"Name the actual work asked for. " +
		"Ignore standing instructions, budgets, rules preambles and boilerplate that could open any task."
)

var errNoGateway = errors.New("no gateway configured")

// errConnUnavailable: the connection is not granted to the project or its intake is closed (readTokenFor, or the
// gateway's answer).
var errConnUnavailable = errors.New("connection-unavailable")

// errTitleShared: a failure that is not this session's own (token, gateway, key, timeout, rate, any provider error,
// a database conflict): the batch stops, the pass fails, and the session is tried again after titleRetryShared, using
// no try.
// A gateway refusal for the project (errConnUnavailable) uses no try either; its session waits titleRetry and the
// project is skipped for the rest of the pass. Only the session's own failure (its reply read, an unreadable or empty
// answer) uses one of its titleTries; it is logged, not a failure of the pass.
var errTitleShared = errors.New("title model unavailable")

// sessionTitleKeep: a session put back by putDocument keeps the stored title fields (the upsert's data expression).
// Session writers read the whole document and put it back; the title fields are the only ones updated in place.
// ponytail: one kind-specific upsert; if more session fields get in-place writers, move the session writers to a
// merge (data || patch) instead.
const sessionTitleKeep = `(excluded.data-'title'-'titleBy'-'titleCall'-'titleAttemptAt'-'titleAttempts')||jsonb_strip_nulls(jsonb_build_object(` +
	`'title',document.data->'title','titleBy',document.data->'titleBy','titleCall',document.data->'titleCall',` +
	`'titleAttemptAt',document.data->'titleAttemptAt','titleAttempts',document.data->'titleAttempts'))`

// titleReady: whether a title can be asked for at all (a gateway is configured); tests answer without one.
var titleReady = func() bool { return gatewayURL() != "" }

// sessionTitleCall asks the title model for a title of prompt as project; it returns the raw text and the call's id.
// A variable so tests answer without a gateway.
var sessionTitleCall = func(ctx context.Context, a *App, project, prompt string) (string, string, error) {
	if gatewayURL() == "" {
		return "", "", errNoGateway
	}
	token, err := readTokenFor(ctx, a, titleConnection, project)
	if err != nil {
		if !errors.Is(err, errConnUnavailable) {
			err = fmt.Errorf("%w: %w", errTitleShared, err)
		}
		return "", "", err
	}
	line := gatewayCall(ctx, token, titleConnection, titleTool, doc{
		"model":             titleModel,
		"systemInstruction": doc{"parts": []any{doc{"text": titleSystem}}},
		"contents":          []any{doc{"role": "user", "parts": []any{doc{"text": prompt}}}},
		"generationConfig":  doc{"maxOutputTokens": 256, "thinkingConfig": doc{"thinkingLevel": "minimal"}},
	})
	if line.Outcome != contract.CallOutcomeOk {
		code := deref(line.ErrorCode)
		err := fmt.Errorf("%s/%s: %s %s", titleConnection, titleTool, line.Outcome, code)
		switch code {
		case "connection-unavailable", "not-granted", "intake-paused", "budget-reached": // the gateway's refusals for the project
			err = fmt.Errorf("%w: %w", errConnUnavailable, err)
		default: // a 400 too: it usually answers Fleet's own request (model, generationConfig), the same for every session
			err = fmt.Errorf("%w: %w", errTitleShared, err)
		}
		return "", line.CallId, err
	}
	var r struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(line.Output, &r); err != nil {
		return "", line.CallId, fmt.Errorf("title reply: %w", err)
	}
	out := ""
	for _, c := range r.Candidates {
		if c.FinishReason == "MAX_TOKENS" { // a cut answer is no title; it counts as an empty one
			return "", line.CallId, nil
		}
		for _, p := range c.Content.Parts {
			if !p.Thought {
				out += p.Text
			}
		}
	}
	return out, line.CallId, nil
}

// leadMark: markdown a model puts before a title: headings, bold, a bullet or quote mark, list numbering.
var leadMark = regexp.MustCompile("^(?:```\\S*\\s*|#+\\s+|\\*+\\s*|[-*>]\\s+|\\d+[.)]\\s+)+")

// titleLabel: a label a model puts before its answer (English or Russian).
var titleLabel = regexp.MustCompile(`(?i)^\**(?:title|название|заголовок)\**\s*:\s*`)

// titleQuotes: an opening quote and the closing ones that pair with it.
var titleQuotes = map[rune]string{'"': `"`, '\'': `'`, '`': "`", '«': "»", '“': "”", '„': "“”"}

// uuidRe: a UUID, with the brackets around it and what they hold when it is inside brackets ("(see <uuid>)" goes
// whole, so a removed UUID leaves no "(see )").
var uuidRe = regexp.MustCompile(`(?i)\s*[(\[][^()\[\]]*\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b[^()\[\]]*[)\]]|\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

// hasWord: a cleaned line that holds a letter or a digit (a code fence or a bare label line does not).
var hasWord = regexp.MustCompile(`[\p{L}\p{N}]`)

// cleanTitle: the first line of a model reply that holds a word once cleaned (a code fence or a "Title:" line on its
// own is skipped), without wrapping quotes, markdown marks, a label, a final period or a UUID (OR-38), whitespace
// collapsed, at most titleMaxRunes; "" when no line holds a word.
func cleanTitle(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := cleanLine(l); hasWord.MatchString(t) {
			return t
		}
	}
	return ""
}

func cleanLine(line string) string {
	line = uuidRe.ReplaceAllString(strings.TrimSpace(line), "")
	line = titleLabel.ReplaceAllString(leadMark.ReplaceAllString(line, ""), "")
	for prev := ""; prev != line; { // emphasis, wrapping quotes and a final period in any order (`"Fix it".`)
		prev = line
		line = strings.TrimRight(strings.Trim(line, "* "), ". ")
		// A quote goes only as one pair around the whole title: `Support for "dark mode"` and `'a' vs 'b'` keep theirs.
		if r := []rune(line); len(r) > 1 {
			if closing, ok := titleQuotes[r[0]]; ok && strings.ContainsRune(closing, r[len(r)-1]) {
				if inner := string(r[1 : len(r)-1]); !strings.ContainsRune(inner, r[0]) && !strings.ContainsAny(inner, closing) {
					line = inner
				}
			}
		}
	}
	line = strings.Join(strings.Fields(line), " ")
	if r := []rune(line); len(r) > titleMaxRunes {
		line = strings.TrimSpace(string(r[:titleMaxRunes-1])) + "…"
	}
	return line
}

// headTail: a long text as its first and last n/2 characters: a first message often opens with standing rules and
// states the task at its end.
func headTail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n/2]) + "\n…\n" + string(r[len(r)-n/2:])
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// titleCandidates: unarchived sessions with no title whose first agent run has ended (or has run titleWaitReply) and
// that have a first message, not host command sessions, not tried within titleRetry nor titleTries times; newest first,
// so a new conversation gets its title in the next pass. Messages are found by session_ref (document_session_ref),
// executions by session_id: indexed under RLS. ponytail: every untitled session (no message, no agent run, tries used)
// is walked each pass; give the query a partial index or a terminal mark if the session count makes that slow.
var titleCandidates = `SELECT s.id, s.project_id, m.text AS first_message, r.id AS reply_execution
 FROM document s
 JOIN LATERAL (SELECT e.id, e.ended_at, e.started_at FROM execution e WHERE e.session_id=s.id AND e.kind='agent_run' ORDER BY e.seq LIMIT 1) r
  ON r.ended_at IS NOT NULL OR r.started_at < now()-interval '` + titleWaitReply + `'
 JOIN LATERAL (SELECT x.data->>'text' AS text FROM document x WHERE x.session_ref=s.id AND x.kind='message' ORDER BY x.seq LIMIT 1) m ON true
 WHERE s.kind='session' AND NOT s.data ? 'title' AND NOT s.data ? 'archivedAt' AND coalesce(s.data->>'actor','')<>'program-worker'
  AND coalesce((s.data->>'titleAttemptAt')::bigint,0) < $1 AND coalesce((s.data->>'titleAttempts')::int,0) < ` + fmt.Sprint(titleTries) + `
 ORDER BY s.seq DESC LIMIT $2`

// sessionTitlePass: one batch of generated titles. Each title is written only while the session still has none, so a
// rename during the call wins. A failed call marks the attempt (the reason goes to the log and the pass span, not to the
// session every reader sees); the session is tried again after titleRetry. A write is one conditional row update, so
// it takes no advisory lock; putDocument keeps the title fields when another writer puts the session back.
func sessionTitlePass(ctx context.Context, a *App) error {
	if !titleReady() {
		return nil // nothing can be generated here; nothing is read or marked
	}
	var rs []doc
	if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) {
		rs, err = rowsOf(q, titleCandidates, nowMs()-titleRetry.Milliseconds(), titleBatch)
		return err
	}); err != nil {
		return err
	}
	var failed error
	ungranted := map[string]bool{} // projects the connection is not granted to: no token is minted again this pass
	// ponytail: one call after another; a slow provider (each call up to the gateway's timeout) holds the batch, so
	// run the calls concurrently if new conversations wait on it.
	for _, r := range rs {
		if ctx.Err() != nil { // shutting down: the rest are not attempts and are not marked
			return failed
		}
		id := astr(r["id"])
		// Each reply is read on its own, so one session that cannot be read is marked and the others go on.
		var reply []string
		var err error
		if ungranted[astr(r["project_id"])] {
			err = errConnUnavailable
		} else if err = runTx(ctx, a, nil, false, "", func(q *Request) (err error) {
			reply, err = replyTextUpTo(q, astr(r["reply_execution"]), 1500)
			return err
		}); err != nil {
			// A conflict, a lost connection or a timeout is the database's (classes 40, 08, 57, as host_poll.go reads
			// them); any other read failure is this session's own: it uses a try and the batch goes on.
			if c := pgCode(err); c == "" || strings.HasPrefix(c, "40") || strings.HasPrefix(c, "08") || strings.HasPrefix(c, "57") { // no SQLSTATE: a lost connection
				err = fmt.Errorf("%w: reply: %w", errTitleShared, err)
			} else {
				err = fmt.Errorf("reply: %w", err)
			}
		}
		raw, callID := "", ""
		if err == nil {
			prompt := "First message:\n" + headTail(astr(r["first_message"]), 3000) + "\n\nFirst reply:\n" + clip(strings.Join(reply, "\n\n"), 1500)
			raw, callID, err = sessionTitleCall(ctx, a, astr(r["project_id"]), prompt)
		}
		if err != nil && ctx.Err() != nil {
			return failed
		}

		title := cleanTitle(raw)
		if err == nil && title == "" {
			err = fmt.Errorf("empty title from %s", titleModel)
		}
		werr := runTx(context.WithoutCancel(ctx), a, nil, true, NOLOCK, func(q *Request) error {
			if err != nil {
				at, used := nowMs(), 1
				if errors.Is(err, errTitleShared) { // tried again after titleRetryShared, not titleRetry
					at, used = at-(titleRetry-titleRetryShared).Milliseconds(), 0
				} else if errors.Is(err, errConnUnavailable) {
					used = 0
				}
				return exec(q, `UPDATE document SET data=data||jsonb_build_object('titleAttemptAt',$2::bigint,'titleAttempts',coalesce((data->>'titleAttempts')::int,0)+$3::int) WHERE id=$1 AND kind='session' AND NOT data ? 'title'`, id, at, used)
			}
			// updated_at stays: a title is no conversation activity, so a backfill does not reorder the list.
			set := doc{"title": title, "titleBy": "auto"}
			if callID != "" {
				set["titleCall"] = "call:" + callID
			}
			// A rename during the call keeps its title; the call stays a tool_run of system activity.
			return exec(q, `UPDATE document SET data=(data-'titleAttemptAt'-'titleAttempts')||$2::jsonb WHERE id=$1 AND kind='session' AND NOT data ? 'title'`, id, jsonBytes(set))
		})
		if err != nil {
			log.Printf("session title %s: %v", id, err)
			// Only a failure that is not the session's own fails the pass; a refused project and a session's own failure
			// (its reply read, an empty answer) are logged.
			if errors.Is(err, errConnUnavailable) {
				ungranted[astr(r["project_id"])] = true
			} else if errors.Is(err, errTitleShared) {
				failed = errors.Join(failed, err) // one message per cause, not per session, so the error tracker groups it
			}
		}
		failed = errors.Join(failed, werr)
		if errors.Is(err, errTitleShared) {
			return failed // the next sessions would most likely fail the same way
		}
	}
	return failed
}

// sessionRename: the explicit title of a session (a person who directs it, the owner, or an agent bound to it by
// boundArguments); the generated one never replaces it.
func sessionRename(q *Request, b *contract.SessionRenameRequest) (contract.SessionRenameReply, error) {
	title, sid := strings.Join(strings.Fields(b.Title), " "), deref(b.SessionId)
	if sid == "" || title == "" {
		return contract.SessionRenameReply{}, fail(400, "Expected sessionId and title")
	}
	if len([]rune(title)) > 200 {
		return contract.SessionRenameReply{}, fail(400, "title is at most 200 characters")
	}
	if !hasWord.MatchString(title) {
		return contract.SessionRenameReply{}, fail(400, "A title holds a word")
	}
	if uuidRe.MatchString(title) {
		return contract.SessionRenameReply{}, fail(400, "A title names the work, never a UUID")
	}
	s, err := get(q, sid, "session")
	if err != nil {
		return contract.SessionRenameReply{}, err
	}
	if err := directs(q, s); err != nil {
		return contract.SessionRenameReply{}, err
	}
	by := actor(q, "operator")
	set := doc{"title": title, "titleBy": by}
	tag, err := q.tx.Exec(q.ctx(), `UPDATE document SET data=(data-'titleAttemptAt'-'titleAttempts'-'titleCall')||$2::jsonb WHERE id=$1 AND kind='session'`, sid, jsonBytes(set))
	if err != nil {
		return contract.SessionRenameReply{}, err
	}
	if tag.RowsAffected() == 0 {
		return contract.SessionRenameReply{}, fail(404, "Unknown session")
	}
	return contract.SessionRenameReply{SessionId: sid, Title: title, TitleBy: by}, nil
}

func init() {
	agentDescriptions["session_rename"] = "Name a conversation (sessionId, default your own; title up to 200 characters, no UUID). An explicit title is kept: the generated title never replaces it"
	agentTypedOp("session_rename", false, sessionRename)
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "session-titles", envMs("FLEET_SESSION_TITLE_TICK_MS", 30_000), func(ctx context.Context) error { return sessionTitlePass(ctx, a) })
	})
}
