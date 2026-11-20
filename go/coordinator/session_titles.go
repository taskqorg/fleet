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

	"github.com/jackc/pgx/v5/pgtype"

	"fleet/contract"
	"fleet/coordinator/db"
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
	titleWaitReply   = 2 * time.Minute // a first agent run still going after this (steered, long) is titled from what it has
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

var errTitleUnknown = errors.New("title call outcome unknown")

// errTitleShared: a failure that is not this session's own (token, gateway, key, timeout, rate, any provider error,
// a database conflict): the batch stops, the pass fails, and the session is tried again after titleRetryShared, using
// no try.
// A gateway refusal for the project (errConnUnavailable) uses no try either; its session waits titleRetry and the
// project is skipped for the rest of the pass. Only the session's own failure (its reply read, an unreadable or empty
// answer) uses one of its titleTries; it is logged, not a failure of the pass.
var errTitleShared = errors.New("title model unavailable")

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
	type part struct {
		Text string `json:"text"`
	}
	type content struct {
		Role  string `json:"role,omitempty"`
		Parts []part `json:"parts"`
	}
	type thinking struct {
		ThinkingLevel string `json:"thinkingLevel"`
	}
	type generation struct {
		MaxOutputTokens int      `json:"maxOutputTokens"`
		ThinkingConfig  thinking `json:"thinkingConfig"`
	}
	line := gatewayCall(ctx, token, titleConnection, titleTool, struct {
		Model             string     `json:"model"`
		SystemInstruction content    `json:"systemInstruction"`
		Contents          []content  `json:"contents"`
		GenerationConfig  generation `json:"generationConfig"`
	}{titleModel, content{Parts: []part{{titleSystem}}}, []content{{Role: "user", Parts: []part{{prompt}}}}, generation{256, thinking{"minimal"}}}, false)
	if line.Outcome != contract.CallOutcomeOk {
		code := deref(line.ErrorCode)
		err := fmt.Errorf("%s/%s: %s %s", titleConnection, titleTool, line.Outcome, code)
		if line.Outcome == contract.CallOutcomeUnknown {
			return "", line.CallId, fmt.Errorf("%w: %w", errTitleUnknown, err)
		}
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
var uuidRe = regexp.MustCompile(`(?i)\s*[(\[][^()\[\]]*[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}[^()\[\]]*[)\]]|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

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

// sessionTitlePass: one batch of generated titles. Each title is written only while the session still has none, so a
// rename during the call wins. A failed call marks the attempt (the reason goes to the log and the pass span, not to the
// session every reader sees); the session is tried again after titleRetry. A write is one conditional row update, so
// it takes no advisory lock; putDocument keeps the title fields when another writer puts the session back.
func sessionTitlePass(ctx context.Context, a *App) error {
	if !titleReady() {
		return nil // nothing can be generated here; nothing is read or marked
	}
	var rs []db.TitleCandidatesRow
	now := time.Now()
	if err := a.store.Read(ctx, nil, func(q *Request) (err error) {
		rs, err = q.db().TitleCandidates(q.ctx(), db.TitleCandidatesParams{WaitBefore: tstz(now.Add(-titleWaitReply)), TriedBefore: tstz(now.Add(-titleRetry)), Tries: titleTries, Batch: titleBatch})
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
		id, project := r.ID, deref(r.ProjectID)
		// Each reply is read on its own, so one session that cannot be read is marked and the others go on.
		var reply []string
		var err error
		if ungranted[project] {
			err = errConnUnavailable
		} else if err = a.store.Read(ctx, nil, func(q *Request) (err error) {
			// Some native events carry no project column; the candidate ties them to a run in this project.
			if err := q.db().TitleReadScope(q.ctx(), project+","); err != nil {
				return err
			}
			reply, err = replyTextUpTo(q, r.ReplyExecution, 1500)
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
		raw, callID, attempt := "", "", ""
		if err == nil && strings.TrimSpace(strings.Join(reply, "")) == "" {
			continue // The execution changed or had an incomplete stream; wait for readable reply text.
		}
		if err == nil {
			attempt = newID()
			claimed, claimErr := claimTitleAttempt(ctx, a, id, attempt, r.ProjectID)
			if claimErr != nil {
				return errors.Join(failed, claimErr)
			}
			if !claimed {
				continue
			}
			prompt := "First message:\n" + headTail(deref(r.FirstMessage), 3000) + "\n\nFirst reply:\n" + clip(strings.Join(reply, "\n\n"), 1500)
			raw, callID, err = sessionTitleCall(ctx, a, project, prompt)
		}
		if err != nil && ctx.Err() != nil && attempt == "" {
			return failed
		}

		title := cleanTitle(raw)
		if err == nil && title == "" {
			err = fmt.Errorf("empty title from %s", titleModel)
		}
		werr := a.store.Write(context.WithoutCancel(ctx), nil, nil, func(q *Request) error {
			d := q.db()
			if errors.Is(err, errTitleUnknown) {
				return d.TitleUnknown(q.ctx(), db.TitleUnknownParams{Call: callID, ID: id, ProjectID: r.ProjectID, Attempt: attempt})
			}
			if err != nil {
				at, used := time.Now(), int32(1)
				if errors.Is(err, errTitleShared) { // tried again after titleRetryShared, not titleRetry
					at, used = at.Add(-(titleRetry - titleRetryShared)), 0
				} else if errors.Is(err, errConnUnavailable) {
					used = 0
				}
				return d.TitleFailed(q.ctx(), db.TitleFailedParams{At: tstz(at), Used: used, ID: id, ProjectID: r.ProjectID, Attempt: attempt})
			}
			var call *string
			if callID != "" {
				c := "call:" + callID
				call = &c
			}
			// A rename during the call keeps its title; the call stays a tool_run of system activity.
			return d.TitleGenerated(q.ctx(), db.TitleGeneratedParams{Title: title, TitleCall: call, ID: id, ProjectID: r.ProjectID, Attempt: attempt})
		})
		if err != nil {
			log.Printf("session title %s: %v", id, err)
			// Only a failure that is not the session's own fails the pass; a refused project and a session's own failure
			// (its reply read, an empty answer) are logged.
			if errors.Is(err, errConnUnavailable) {
				ungranted[project] = true
			} else if errors.Is(err, errTitleShared) || errors.Is(err, errTitleUnknown) {
				failed = errors.Join(failed, err) // one message per cause, not per session, so the error tracker groups it
			}
		}
		failed = errors.Join(failed, werr)
		if errors.Is(err, errTitleShared) || errors.Is(err, errTitleUnknown) {
			return failed // the next sessions would most likely fail the same way
		}
	}
	return failed
}

func tstz(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// Reserve before sending. A crash or lost result leaves the reservation visible and cannot cause another call.
func claimTitleAttempt(ctx context.Context, a *App, id, attempt string, project *string) (bool, error) {
	claimed := false
	err := a.store.Write(ctx, nil, nil, func(q *Request) error {
		n, err := q.db().TitleClaim(q.ctx(), db.TitleClaimParams{Attempt: attempt, ID: id, ProjectID: project, TriedBefore: tstz(time.Now().Add(-titleRetry)), Tries: titleTries})
		claimed = err == nil && n == 1
		return err
	})
	return claimed, err
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
	n, err := q.db().TitleRename(q.ctx(), db.TitleRenameParams{Title: title, TitleBy: by, ID: sid})
	if err != nil {
		return contract.SessionRenameReply{}, err
	}
	if n == 0 {
		return contract.SessionRenameReply{}, fail(404, "Unknown session")
	}
	return contract.SessionRenameReply{SessionId: sid, Title: title, TitleBy: by}, nil
}

func init() {
	op("session_rename", sessionRename)
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "session-titles", envMs("FLEET_SESSION_TITLE_TICK_MS", 30_000), func(ctx context.Context) error { return sessionTitlePass(ctx, a) })
	})
}
