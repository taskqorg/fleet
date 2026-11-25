// Conversation threads (docs/designs/fleet-conversations.md): one thread per task, or per session without a task, read in pages
// of typed blocks by block key (seq:index) in both directions, searched and exported on the server.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"fleet/contract"
	"fleet/coordinator/db"
)

type bkey struct{ seq, index int64 }

func (k bkey) String() string { return fmt.Sprintf("%d:%d", k.seq, k.index) }

var keyRe = regexp.MustCompile(`^(\d+):(\d+)$`)

func parseKey(v any) (bkey, error) {
	s, _ := v.(string)
	m := keyRe.FindStringSubmatch(s)
	if m == nil {
		return bkey{}, fail(400, "Expected block key seq:index")
	}
	a, _ := strconv.ParseInt(m[1], 10, 64)
	b, _ := strconv.ParseInt(m[2], 10, 64)
	return bkey{a, b}, nil
}

func mustKey(v any) bkey { k, _ := parseKey(v); return k }

func cmpKey(a, b bkey) int64 {
	if a.seq != b.seq {
		return a.seq - b.seq
	}
	return a.index - b.index
}

var terminalState = map[string]bool{"succeeded": true, "failed": true, "cancelled": true}

// threadKey: {task} or {session}.
// thread: with session, one work-item thread of it (nil: all; "" the main thread).
type threadKey struct {
	task, session string
	thread        *string
}

func (k threadKey) view() contract.ThreadKey {
	if k.task != "" {
		return contract.ThreadKey{Task: &k.task}
	}
	return contract.ThreadKey{Session: &k.session, Thread: k.thread}
}

func threadKeyOf(k contract.ThreadKey) (threadKey, error) {
	if t := deref(k.Task); t != "" {
		return threadKey{task: t}, nil
	}
	if s := deref(k.Session); s != "" {
		return threadKey{session: s, thread: k.Thread}, nil
	}
	return threadKey{}, fail(400, "Expected key {task} or {session}")
}

// member: an execution of the thread, as its boundary and its agent blocks show it. known: the execution is a member
// (a row of another execution has none).
type member struct {
	id                     string
	sessionID, projectID   *string
	harness, model, effort *string
	state                  string
	observation            db.Observation
	startedAt, endedAt     *time.Time
	seq                    int64
	known                  bool
	// pub: the delivery stage of its linked pull request, when pubRead (prefetchPublishing reads a page's at once).
	pub     *contract.ThreadDeliveryStage
	pubRead bool
}

// prefetchPublishing reads the delivery stage of every ended execution among rows in one query, so a page of
// many ended runs does not read the publishing observations once per run.
func prefetchPublishing(q *Request, rows []trow, m map[string]member) error {
	ids := []string{}
	for _, r := range rows {
		if r.src != "event" {
			continue
		}
		v := r.row()
		if eid := deref(v.ExecutionID); eid != "" && rawText(v.Type) == "execution.ended" && !m[eid].pubRead {
			ids = append(ids, eid)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rs, err := q.db().ThreadPublishingResults(q.ctx(), db.ThreadPublishingResultsParams{Ids: ids, Changed: []string{}})
	if err != nil {
		return err
	}
	found := map[string]*contract.ThreadDeliveryStage{}
	for _, r := range rs {
		st, err := threadDeliveryStage(r.Value)
		if err != nil {
			return err
		}
		found[r.ExecutionID] = st
	}
	for _, id := range ids {
		e := m[id]
		e.pub, e.pubRead = found[id], true
		m[id] = e
	}
	return nil
}

// memberRow: the execution columns every member query selects.
type memberRow = db.ThreadMemberOfRow

func memberOf(r memberRow) member {
	m := member{id: r.ID, sessionID: r.SessionID, projectID: r.ProjectID, harness: r.Harness, model: r.Model, state: r.State, observation: r.Observation, startedAt: r.StartedAt, endedAt: r.EndedAt, seq: r.Seq, known: true}
	json.Unmarshal(r.Effort, &m.effort)
	return m
}

// members: a task's executions, plus the taskless executions of sessions whose first (or attached) task it is.
func members(q *Request, key threadKey) (map[string]member, []string, error) {
	var rs []memberRow
	if key.task != "" {
		xs, err := q.db().ThreadMembersOfTask(q.ctx(), key.task)
		if err != nil {
			return nil, nil, err
		}
		for _, x := range xs {
			rs = append(rs, memberRow(x))
		}
	} else if key.thread != nil {
		xs, err := q.db().ThreadMembersOfSessionThread(q.ctx(), db.ThreadMembersOfSessionThreadParams{SessionID: key.session, Thread: *key.thread})
		if err != nil {
			return nil, nil, err
		}
		for _, x := range xs {
			rs = append(rs, memberRow(x))
		}
	} else {
		xs, err := q.db().ThreadMembersOfSession(q.ctx(), &key.session)
		if err != nil {
			return nil, nil, err
		}
		for _, x := range xs {
			rs = append(rs, memberRow(x))
		}
	}
	if len(rs) == 0 {
		var ok bool
		var err error
		if key.session != "" {
			ok, err = q.db().ThreadSessionExists(q.ctx(), key.session)
		} else {
			ok, err = q.db().ThreadTaskExists(q.ctx(), key.task)
		}
		if err == nil && !ok {
			err = errNotFound
		}
		if err != nil {
			return nil, nil, err
		}
	}
	m, ids := map[string]member{}, []string{}
	for _, r := range rs {
		m[r.ID] = memberOf(r)
		ids = append(ids, r.ID)
	}
	return m, ids, nil
}

func tsMs(v any) any {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	for _, f := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999-07:00"} {
		if t, err := time.Parse(f, s); err == nil {
			return t.UnixMilli()
		}
	}
	return nil
}

// msOfTime: a member time as an optional contract time.
func msOfTime(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	n := t.UnixMilli()
	return &n
}

// nonEmpty: s, nil when absent or "".
func nonEmpty(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

// markOf: the agent of a member as the contract's optional fields.
func markOf(e member) (harness, model, effort *string) {
	return nonEmpty(e.harness), nonEmpty(e.model), nonEmpty(e.effort)
}

// msPtr: tsMs as an optional contract time.
func msPtr(v any) *int64 {
	if n, ok := tsMs(v).(int64); ok {
		return &n
	}
	return nil
}

// threadBlock: a block of the thread (contracts/ops-sessions.schema.json Block). Fleet's own blocks are built from their
// contract struct; an executor block is the host's open object with the thread's fields set.
type threadBlock = rawObject

// blockOf: a block from its contract struct.
func blockOf(v any) threadBlock {
	var b threadBlock
	json.Unmarshal(jsonBytes(v), &b)
	return b
}

// val: field k as its decoded JSON value (nil when absent), for open fields shown as they are (tool input and output).
func (r rawObject) val(k string) any {
	var v any
	json.Unmarshal(r[k], &v)
	return v
}

// key: the block's key.
func (r rawObject) key() bkey { return mustKey(r.text("key")) }

func boundary(e member) threadBlock {
	b := contract.BoundaryBlock{Kind: "boundary", Key: bkey{e.seq, 0}.String(), SessionId: nonEmpty(e.sessionID), ExecutionId: e.id,
		State: contract.ExecutionState(e.state), StartedAt: msOfTime(e.startedAt), EndedAt: msOfTime(e.endedAt)}
	b.Harness, b.Model, b.Effort = markOf(e)
	return blockOf(b)
}

func attachBoundaryResources(q *Request, bs []threadBlock) error {
	ids, seen := []string{}, map[string]bool{}
	for _, b := range bs {
		if b.text("kind") == "boundary" {
			if eid := b.text("executionId"); eid != "" && !seen[eid] {
				seen[eid] = true
				ids = append(ids, eid)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	resMap, err := boundaryResourcesMap(q, ids)
	if err != nil {
		return err
	}
	for _, b := range bs {
		if b.text("kind") == "boundary" {
			if res, ok := resMap[b.text("executionId")]; ok {
				b["resources"] = jsonBytes(res)
			}
		}
	}
	return nil
}

// threadQuestion: a question row (the question table's columns as JSON, without data).
type threadQuestion struct {
	ID          string          `json:"id"`
	ExecutionID *string         `json:"execution_id"`
	Text        string          `json:"text"`
	Options     json.RawMessage `json:"options"`
	State       string          `json:"state"`
	AskedAt     *string         `json:"asked_at"`
	AnsweredAt  *string         `json:"answered_at"`
	AnsweredBy  *string         `json:"answered_by"`
	Answer      *string         `json:"answer"`
}

func questionBlock(raw []byte, seq int64) threadBlock {
	var qu threadQuestion
	json.Unmarshal(raw, &qu)
	at := func(s *string) *int64 {
		if s == nil {
			return nil
		}
		return msPtr(*s)
	}
	b := contract.QuestionBlock{Kind: "question", Key: bkey{seq, 0}.String(), ExecutionId: nonEmpty(qu.ExecutionID), QuestionId: qu.ID, Text: qu.Text,
		Options: json.RawMessage("[]"), State: qu.State, AskedAt: at(qu.AskedAt), AnsweredAt: at(qu.AnsweredAt), AnsweredBy: nonEmpty(qu.AnsweredBy)}
	if len(qu.Options) > 0 && string(qu.Options) != "null" {
		b.Options = qu.Options
	}
	if qu.Answer != nil {
		b.Answer = jsonBytes(*qu.Answer)
	}
	return blockOf(b)
}

// threadRow: a message or event row (queries/threads.sql); type, text, deltaOf and at keep their JSON, which differs by row.
type threadRow struct {
	ID          string            `json:"id"`
	ExecutionID *string           `json:"executionId"`
	Type        json.RawMessage   `json:"type"`
	Text        json.RawMessage   `json:"text"`
	Blocks      []json.RawMessage `json:"blocks"`
	DeltaOf     json.RawMessage   `json:"deltaOf"`
	Closes      []json.RawMessage `json:"closes"`
	At          json.RawMessage   `json:"at"`
	SentBy      *string           `json:"sentBy"`
	Attachments []json.RawMessage `json:"attachments"`
}

// rawValue: raw decoded, nil when absent.
func rawValue(raw json.RawMessage) any {
	var v any
	json.Unmarshal(raw, &v)
	return v
}

// trow: one row of the thread: its seq, its source (message, event, event-delta, question, execution) and its JSON.
type trow struct {
	seq int64
	src string
	v   []byte
}

func (r trow) row() threadRow {
	var x threadRow
	json.Unmarshal(r.v, &x)
	return x
}

// threadRows: the rows of executions ids from seq from in direction dir; pattern (a search, ILIKE pattern) keeps only
// the messages and events whose text matches.
func threadRows(q *Request, ids []string, dir string, from int64, limit int, pattern *string) ([]trow, error) {
	out := []trow{}
	add := func(seq int64, src string, v []byte) { out = append(out, trow{seq, src, v}) }
	switch {
	case pattern != nil:
		rs, err := q.db().ThreadRowsSearch(q.ctx(), db.ThreadRowsSearchParams{Ids: ids, FromSeq: from, Lim: int32(limit), Pattern: *pattern})
		for _, r := range rs {
			add(r.Seq, r.Src, r.V)
		}
		return out, err
	case dir == "<":
		rs, err := q.db().ThreadRowsBefore(q.ctx(), db.ThreadRowsBeforeParams{Ids: ids, FromSeq: from, Lim: int32(limit)})
		for _, r := range rs {
			add(r.Seq, r.Src, r.V)
		}
		return out, err
	}
	rs, err := q.db().ThreadRowsAfter(q.ctx(), db.ThreadRowsAfterParams{Ids: ids, FromSeq: from, Lim: int32(limit)})
	for _, r := range rs {
		add(r.Seq, r.Src, r.V)
	}
	return out, err
}

// stream: the text of an open stream (event-delta rows of one deltaOf); "", false once an event closes it.
func streamText(q *Request, executionID, id string) (string, bool, error) {
	closed, err := q.db().ThreadStreamClosed(q.ctx(), db.ThreadStreamClosedParams{ExecutionID: executionID, DeltaOf: id})
	if err != nil || closed {
		return "", false, err
	}
	parts, err := q.db().ThreadStreamBlocks(q.ctx(), db.ThreadStreamBlocksParams{ExecutionID: executionID, DeltaOf: id})
	var sb strings.Builder
	for _, p := range parts {
		var bs []rawObject
		json.Unmarshal(p, &bs)
		for _, x := range bs {
			sb.WriteString(x.text("markdown"))
		}
	}
	return sb.String(), true, err
}

// expand: the blocks of one row, in index order.
func expand(q *Request, r trow, m map[string]member) ([]threadBlock, error) {
	if r.src == "question" {
		return []threadBlock{questionBlock(r.v, r.seq)}, nil
	}
	v := r.row()
	eid := deref(v.ExecutionID)
	e := m[eid]
	at := func(i int) string { return bkey{r.seq, int64(i)}.String() }
	switch r.src {
	case "message":
		var t *string
		if json.Unmarshal(v.Text, &t) != nil || t == nil {
			return nil, nil
		}
		b := contract.TextBlock{Kind: "text", Key: at(0), ExecutionId: nonEmpty(v.ExecutionID), Role: "operator", Markdown: *t, Plain: t, At: msPtr(rawValue(v.At)), By: nonEmpty(v.SentBy)}
		for _, x := range v.Attachments {
			var a rawObject
			json.Unmarshal(x, &a)
			size, _ := jsonNumber(a["size"])
			b.Attachments = append(b.Attachments, contract.ResultArtifact{Id: a.text("artifactId"), Name: a.text("name"), MediaType: a.text("mediaType"), Size: int64(size)})
		}
		return []threadBlock{blockOf(b)}, nil
	case "event-delta":
		deltaOf := rawText(v.DeltaOf)
		text, open, err := streamText(q, eid, deltaOf)
		if err != nil || !open {
			return nil, err
		}
		stream := "open"
		if e.known && terminalState[e.state] {
			stream = "cut-off"
		}
		if strings.HasPrefix(deltaOf, "reasoning:") {
			return []threadBlock{blockOf(contract.ReasoningBlock{Kind: "reasoning", Key: at(0), ExecutionId: nonEmpty(v.ExecutionID), Markdown: text, Plain: &text, Stream: &stream, At: msPtr(rawValue(v.At))})}, nil
		}
		b := contract.TextBlock{Kind: "text", Key: at(0), ExecutionId: nonEmpty(v.ExecutionID), Role: "agent", Markdown: text, Plain: &text, Stream: &stream, At: msPtr(rawValue(v.At))}
		b.Harness, b.Model, b.Effort = markOf(e)
		return []threadBlock{blockOf(b)}, nil
	}
	own := []threadBlock{}
	for i, x := range v.Blocks {
		var src rawObject
		json.Unmarshal(x, &src)
		b := threadBlock{}
		for k, y := range src {
			b[k] = y
		}
		b["key"], b["executionId"], b["at"] = jsonBytes(at(i)), jsonBytes(v.ExecutionID), jsonBytes(tsMs(rawValue(v.At)))
		if b.text("kind") == "text" {
			b["role"] = jsonBytes(firstString(src.text("role"), "agent"))
			// The member's agent as it is (null for a row of another execution).
			b["harness"], b["model"], b["effort"] = jsonBytes(e.harness), jsonBytes(e.model), jsonBytes(e.effort)
		}
		if k := b.text("kind"); k == "text" || k == "reasoning" {
			b["plain"] = jsonBytes(src.text("markdown"))
		}
		own = append(own, b)
	}
	if rawText(v.Type) == "execution.ended" {
		stage := e.pub
		if !e.pubRead {
			publishing, err := q.db().ThreadPublishingResults(q.ctx(), db.ThreadPublishingResultsParams{Ids: []string{eid}, Changed: []string{}})
			if err != nil {
				return nil, err
			}
			if len(publishing) > 0 {
				if stage, err = threadDeliveryStage(publishing[0].Value); err != nil {
					return nil, err
				}
			}
		}
		result, err := threadResult(q, eid, at(len(own)), stage)
		if err != nil {
			return nil, err
		}
		if result != nil {
			own = append(own, result)
		}
	}
	return own, nil
}

func threadResult(q *Request, eid, key string, stage *contract.ThreadDeliveryStage) (threadBlock, error) {
	arts, err := q.db().ThreadStoredArtifacts(q.ctx(), &eid)
	if err != nil || (len(arts) == 0 && stage == nil) {
		return nil, err
	}
	r := contract.ResultBlock{Kind: "result", Key: key, ExecutionId: &eid, ArtifactIds: []string{}, Artifacts: []contract.ResultArtifact{}, DeliveryStage: stage}
	for _, a := range arts {
		r.ArtifactIds = append(r.ArtifactIds, a.ID)
		r.Artifacts = append(r.Artifacts, contract.ResultArtifact{Id: a.ID, Name: a.Name, MediaType: a.MediaType, Size: a.Size, PreviewId: optStr(a.PreviewID)})
	}
	return blockOf(r), nil
}

func reverse(xs []threadBlock) []threadBlock {
	out := make([]threadBlock, len(xs))
	for i, x := range xs {
		out[len(xs)-1-i] = x
	}
	return out
}

// side: up to limit blocks strictly before ('<') or after ('>') from, nearest first; more says whether more remain.
func side(q *Request, m map[string]member, ids []string, dir string, from *bkey, limit int, inclusive bool) ([]threadBlock, bool, error) {
	out, seen := []threadBlock{}, map[int64]bool{}
	cursor := int64(0)
	if dir == "<" {
		cursor = math.MaxInt64
	}
	if from != nil {
		cursor = from.seq
	}
	keep := func(b threadBlock) bool {
		if from == nil {
			return true
		}
		c := cmpKey(b.key(), *from)
		if dir == "<" {
			return c < 0
		}
		if inclusive {
			return c >= 0
		}
		return c > 0
	}
	for {
		batch, err := threadRows(q, ids, dir, cursor, limit+1, nil)
		if err != nil {
			return nil, false, err
		}
		if err := prefetchPublishing(q, batch, m); err != nil {
			return nil, false, err
		}
		for _, r := range batch {
			if seen[r.seq] {
				continue
			}
			seen[r.seq] = true
			var bs []threadBlock
			if r.src == "execution" {
				bs = []threadBlock{boundary(m[r.row().ID])}
			} else if bs, err = expand(q, r, m); err != nil {
				return nil, false, err
			}
			if dir == "<" {
				bs = reverse(bs)
			}
			for _, b := range bs {
				if keep(b) {
					out = append(out, b)
				}
			}
			if len(out) > limit {
				break
			}
		}
		if len(out) > limit || len(batch) <= limit {
			break
		}
		cursor = batch[len(batch)-1].seq
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	if dir == "<" {
		out = reverse(out)
	}
	return out, more, nil
}

func threadTotal(q *Request, m map[string]member, ids []string) (int64, error) {
	n, err := q.db().ThreadTotal(q.ctx(), ids)
	if err != nil {
		return 0, err
	}
	n += int64(len(m))
	ss, err := q.db().ThreadOpenStreams(q.ctx(), ids)
	for _, x := range ss {
		if _, open, err := streamText(q, x.ExecutionID, rawText(x.DeltaOf)); err != nil {
			return 0, err
		} else if open {
			n++
		}
	}
	return n, err
}

func positiveLimit(v *int64) (int, error) {
	if v == nil || *v <= 0 || *v > 1<<53 {
		return 0, fail(400, "Expected positive integer limit")
	}
	return int(*v), nil
}

// keyOfSession: the thread a session shows: its attached or first task's, else its own.
func keyOfSession(q *Request, sid string) (threadKey, error) {
	ts, err := q.db().ThreadSessionTask(q.ctx(), sid)
	if err != nil {
		return threadKey{}, err
	}
	if len(ts) == 0 {
		if ts, err = q.db().ThreadSessionFirstTask(q.ctx(), &sid); err != nil {
			return threadKey{}, err
		}
	}
	if len(ts) > 0 && deref(ts[0]) != "" {
		return threadKey{task: *ts[0]}, nil
	}
	return threadKey{session: sid}, nil
}

func threadComposer(q *Request, b *contract.ThreadComposerRequest) (contract.ThreadComposerReply, error) {
	s, err := sessionByID(q, b.ConversationId)
	if err != nil {
		return contract.ThreadComposerReply{}, err
	}
	return composerTarget(q, &s)
}

func composerTarget(q *Request, s *agentSession) (contract.ThreadComposerReply, error) {
	var out contract.ThreadComposerReply
	key, err := keyOfSession(q, s.ID)
	if err != nil {
		return out, err
	}
	out.Key = key.view()
	if key.task != "" {
		visible, err := q.db().ThreadTaskExists(q.ctx(), key.task)
		if err != nil {
			return out, err
		}
		if !visible {
			return out, errNotFound
		}
	}
	m, ids, err := members(q, key)
	if err != nil {
		return out, err
	}
	sessions := []string{s.ID}
	if key.task != "" {
		memberSessions := []string{s.ID}
		for _, id := range ids {
			if sid := m[id].sessionID; sid != nil {
				memberSessions = append(memberSessions, *sid)
			}
		}
		sessions, err = q.db().ThreadComposerSessions(q.ctx(), db.ThreadComposerSessionsParams{
			TaskID: key.task, ProjectID: agentOpt(s.ProjectID), SessionIds: memberSessions,
		})
		if err != nil {
			return out, err
		}
	}
	if len(sessions) == 0 {
		return out, nil
	}
	out.SessionId = &sessions[0]
	eligible := make(map[string]bool, len(sessions))
	for _, sid := range sessions {
		eligible[sid] = true
	}
	// members orders execution boundaries by creation sequence, independently of the transcript window.
	for i := len(ids) - 1; i >= 0; i-- {
		e := m[ids[i]]
		if !eligible[deref(e.sessionID)] || e.observation != db.ObservationConfirmed || terminalState[e.state] {
			continue
		}
		if key.task != "" && (e.state != "running" || deref(e.projectID) != s.ProjectID) {
			continue
		}
		out.SessionId = e.sessionID
		out.Active = &contract.SessionExecution{Id: e.id, State: e.state, Harness: e.harness, Model: e.model, Effort: e.effort}
		break
	}
	return out, nil
}

func feedCursor(q *Request) (int64, error) { return head(q.ctx(), q.tx) }

// blocksJSON: blocks as the contract's Block values.
func blocksJSON(bs []threadBlock) []contract.Block {
	out := make([]contract.Block, 0, len(bs))
	for _, b := range bs {
		out = append(out, jsonBytes(b))
	}
	return out
}

func threadEntries(q *Request, b *contract.ThreadEntriesRequest) (contract.ThreadEntriesReply, error) {
	var none contract.ThreadEntriesReply
	key, err := threadKeyOf(b.Key)
	if err != nil {
		return none, err
	}
	if b.Resolve != nil && *b.Resolve && key.session != "" && key.thread == nil {
		if key, err = keyOfSession(q, key.session); err != nil {
			return none, err
		}
	}
	limit, err := positiveLimit(b.Limit)
	if err != nil {
		return none, err
	}
	m, ids, err := members(q, key)
	if err != nil {
		return none, err
	}
	given := 0
	for _, k := range []*string{b.Before, b.After, b.Around} {
		if k != nil {
			given++
		}
	}
	if given > 1 {
		return none, fail(400, "Expected one of before, after, around")
	}
	var entries []threadBlock
	hasBefore, hasAfter, withTotal := false, false, false
	switch {
	case b.Before != nil:
		k, err := parseKey(*b.Before)
		if err != nil {
			return none, err
		}
		if entries, hasBefore, err = side(q, m, ids, "<", &k, limit, false); err != nil {
			return none, err
		}
		hasAfter = true
	case b.After != nil:
		k, err := parseKey(*b.After)
		if err != nil {
			return none, err
		}
		if entries, hasAfter, err = side(q, m, ids, ">", &k, limit, false); err != nil {
			return none, err
		}
		hasBefore = true
	case b.Around != nil:
		k, err := parseKey(*b.Around)
		if err != nil {
			return none, err
		}
		half := limit / 2
		// A page of one is all after the key; its hasBefore is the probe below (a side of zero blocks would never end).
		var l []threadBlock
		lmore := false
		if half > 0 {
			if l, lmore, err = side(q, m, ids, "<", &k, half, false); err != nil {
				return none, err
			}
		}
		r, rmore, err := side(q, m, ids, ">", &k, limit-half, true)
		if err != nil {
			return none, err
		}
		entries, hasBefore, hasAfter, withTotal = append(l, r...), lmore, rmore, true
		if half == 0 && !hasBefore {
			one, _, err := side(q, m, ids, "<", &k, 1, false)
			if err != nil {
				return none, err
			}
			hasBefore = len(one) > 0
		}
	default:
		if entries, hasBefore, err = side(q, m, ids, "<", nil, limit, false); err != nil {
			return none, err
		}
		withTotal = true
	}
	if err := attachBoundaryResources(q, entries); err != nil {
		return none, err
	}
	full := b.Full == nil || *b.Full
	for _, e := range entries {
		summarizeTool(e, full)
	}
	sessions, seen := []string{}, map[string]bool{}
	for _, id := range ids {
		if s := deref(m[id].sessionID); s != "" && !seen[s] {
			seen[s] = true
			sessions = append(sessions, s)
		}
	}
	out := contract.ThreadEntriesReply{Key: key.view(), Entries: blocksJSON(entries), HasBefore: hasBefore, HasAfter: hasAfter, SessionIds: sessions}
	if len(entries) > 0 {
		f, l := entries[0].text("key"), entries[len(entries)-1].text("key")
		out.First, out.Last = &f, &l
	}
	if withTotal {
		n, err := threadTotal(q, m, ids)
		if err != nil {
			return none, err
		}
		out.Total = &n
	}
	out.FeedCursor, err = feedCursor(q)
	return out, err
}

func threadBlocks(q *Request, b *contract.ThreadBlocksRequest) (contract.ThreadBlocksReply, error) {
	var none contract.ThreadBlocksReply
	key, err := threadKeyOf(b.Key)
	if err != nil {
		return none, err
	}
	m, ids, err := members(q, key)
	if err != nil {
		return none, err
	}

	out, removed := []threadBlock{}, []string{}
	add := func(rs []trow) error {
		for _, r := range rs {
			bs, err := expand(q, r, m)
			if err != nil {
				return err
			}
			out = append(out, bs...)
		}
		return nil
	}
	// openRows: the delta that opened stream deltaOf of an execution.
	openRows := func(executionID *string, deltaOf string) ([]trow, error) {
		if executionID == nil {
			return nil, nil
		}
		rs, err := q.db().ThreadStreamOpenRows(q.ctx(), db.ThreadStreamOpenRowsParams{ExecutionID: *executionID, DeltaOf: deltaOf})
		out := []trow{}
		for _, r := range rs {
			out = append(out, trow{r.Seq, r.Src, r.V})
		}
		return out, err
	}
	publishingChanges := []string{}
	for _, c := range b.Rows {
		id, kind := c.Id, string(c.Kind)
		if id == "" || kind == "" {
			continue
		}
		switch kind {
		case "execution":
			if e, ok := m[id]; ok {
				out = append(out, boundary(e))
			}
		case "question":
			qs, err := q.db().ThreadQuestion(q.ctx(), db.ThreadQuestionParams{ID: id, Ids: ids})
			if err != nil {
				return none, err
			}
			for _, qu := range qs {
				out = append(out, questionBlock(qu.V, qu.Seq))
			}
		case "artifact":
			es, err := q.db().ThreadArtifactExecution(q.ctx(), id)
			if err != nil {
				return none, err
			}
			if len(es) > 0 && deref(es[0]) != "" {
				e := *es[0]
				if _, ok := m[e]; ok {
					rs, err := q.db().ThreadEventRows(q.ctx(), "ended:"+e)
					if err != nil {
						return none, err
					}
					for _, r := range rs {
						if err := add([]trow{{r.Seq, r.Src, r.V}}); err != nil {
							return none, err
						}
					}
				}
			}
		case "setting-change":
			publishingChanges = append(publishingChanges, id)
		case "event":
			rs, err := q.db().ThreadRowsByID(q.ctx(), db.ThreadRowsByIDParams{ID: id, Ids: ids})
			if err != nil {
				return none, err
			}
			for _, x := range rs {
				d := trow{x.Seq, x.Src, x.V}
				v := d.row()
				if d.src == "event-delta" {
					f, err := openRows(v.ExecutionID, rawText(v.DeltaOf))
					if err != nil {
						return none, err
					}
					if err := add(f[:min(1, len(f))]); err != nil {
						return none, err
					}
					continue
				}
				for _, cid := range v.Closes {
					os, err := openRows(v.ExecutionID, rawText(cid))
					if err != nil {
						return none, err
					}
					for _, o := range os {
						removed = append(removed, bkey{o.seq, 0}.String())
					}
				}
				if err := add([]trow{d}); err != nil {
					return none, err
				}
			}
		}
	}
	if len(publishingChanges) > 0 {
		results, err := q.db().ThreadPublishingResults(q.ctx(), db.ThreadPublishingResultsParams{Ids: ids, Changed: publishingChanges})
		if err != nil {
			return none, err
		}
		for _, result := range results {
			stage, err := threadDeliveryStage(result.Value)
			if err != nil {
				return none, err
			}
			rows, err := q.db().ThreadEventRows(q.ctx(), "ended:"+result.ExecutionID)
			if err != nil {
				return none, err
			}
			for _, row := range rows {
				key := bkey{row.Seq, int64(len((trow{v: row.V}).row().Blocks))}.String()
				block, err := threadResult(q, result.ExecutionID, key, stage)
				if err != nil {
					return none, err
				}
				out = append(out, block)
			}
		}
	}
	byKey := map[string]threadBlock{}
	for _, x := range out {
		byKey[x.text("key")] = x
	}
	entries := []threadBlock{}
	for _, x := range byKey {
		entries = append(entries, x)
	}
	sort.Slice(entries, func(i, j int) bool { return cmpKey(entries[i].key(), entries[j].key()) < 0 })
	if err := attachBoundaryResources(q, entries); err != nil {
		return none, err
	}
	full := b.Full == nil || *b.Full
	for _, e := range entries {
		summarizeTool(e, full)
	}
	return contract.ThreadBlocksReply{Removed: removed, Entries: blocksJSON(entries)}, nil
}

func searchable(b threadBlock) string {
	switch b.text("kind") {
	case "text", "reasoning":
		return firstString(b.val("plain"), b.val("markdown"))
	case "tool":
		return strings.Join([]string{fmt.Sprint(b.val("name")), jsOr(b.val("input")), jsOr(b.val("output"))}, " ")
	case "question":
		return b.text("text") + " " + b.text("answer")
	}
	return ""
}

func jsOr(v any) string {
	if v == nil {
		return `""`
	}
	return js(v)
}

func threadSearch(q *Request, b *contract.ThreadSearchRequest) (contract.ThreadSearchReply, error) {
	var none contract.ThreadSearchReply
	key, err := threadKeyOf(b.Key)
	if err != nil {
		return none, err
	}
	limit, err := positiveLimit(b.Limit)
	if err != nil {
		return none, err
	}
	qs := strings.TrimSpace(b.Q)
	if qs == "" {
		return none, fail(400, "Expected q")
	}
	var before *bkey
	if b.Before != nil {
		k, err := parseKey(*b.Before)
		if err != nil {
			return none, err
		}
		before = &k
	}
	m, ids, err := members(q, key)
	if err != nil {
		return none, err
	}
	needle := strings.ToLower(qs)
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(qs) + "%"
	cursor := int64(math.MaxInt64)
	if before != nil {
		cursor = before.seq
	}
	hits, seen := []contract.SearchHit{}, map[string]bool{}
	for {
		batch, err := threadRows(q, ids, "<", cursor, limit+1, &like)
		if err != nil {
			return none, err
		}
		qrows, err := q.db().ThreadQuestionsSearch(q.ctx(), db.ThreadQuestionsSearchParams{Ids: ids, FromSeq: cursor, Pattern: like, Lim: int32(limit + 1)})
		if err != nil {
			return none, err
		}
		lo := int64(-1)
		if len(batch) > limit {
			lo = batch[len(batch)-1].seq
		}
		all := []trow{}
		for _, r := range batch {
			if r.src != "execution" {
				all = append(all, r)
			}
		}
		for _, x := range qrows {
			if x.Seq >= lo {
				all = append(all, trow{x.Seq, "question", x.V})
			}
		}
		sort.SliceStable(all, func(i, j int) bool { return all[i].seq > all[j].seq })
		for _, r := range all {
			bs, err := expand(q, r, m)
			if err != nil {
				return none, err
			}
			for _, x := range reverse(bs) {
				k := x.text("key")
				if before != nil && cmpKey(x.key(), *before) >= 0 {
					continue
				}
				t := searchable(x)
				i := strings.Index(strings.ToLower(t), needle)
				if i < 0 || seen[k] {
					continue
				}
				seen[k] = true
				rs := []rune(t)
				ri := len([]rune(t[:i]))
				snip := ""
				if ri > 40 {
					snip = "…"
				}
				snip += string(rs[max(0, ri-40):min(len(rs), ri+len([]rune(qs))+80)])
				hits = append(hits, contract.SearchHit{Key: k, Snippet: snip})
			}
		}
		if len(hits) > limit || len(batch) <= limit {
			break
		}
		cursor = lo
	}
	return contract.ThreadSearchReply{Hits: hits[:min(len(hits), limit)], HasMore: len(hits) > limit}, nil
}

func fence(v any) string {
	s, ok := v.(string)
	if !ok {
		s = js(v)
	}
	n := 3
	for _, m := range regexp.MustCompile("`+").FindAllString(s, -1) {
		n = max(n, len(m)+1)
	}
	t := strings.Repeat("`", n)
	return t + "\n" + s + "\n" + t
}

type point struct {
	key    string
	offset int
}

// clipTime: the compact time of a copied message (OR-2, design-system/formats.md "Copy"): 11 Nov 14:03, with the year when it is not this year.
func clipTime(ms any, loc *time.Location) string {
	n, ok := numOK(ms) // int64 from tsMs, float64 from a block built through its contract struct
	if !ok {
		return ""
	}
	t := time.UnixMilli(int64(n)).In(loc)
	if t.Year() != time.Now().In(loc).Year() {
		return t.Format("2 Jan 2006 15:04")
	}
	return t.Format("2 Jan 15:04")
}

// mdCode: a file name as a markdown code span: literal, fenced by more backticks than it holds.
func mdCode(s string) string {
	n := 0
	for run, i := 0, 0; i < len(s); i++ {
		if s[i] == '`' {
			run++
			n = max(n, run)
		} else {
			run = 0
		}
	}
	f := strings.Repeat("`", n+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		return f + " " + s + " " + f
	}
	return f + s + f
}

// whoOf: the author line of a text block: the person (email local part) or the agent as harness, model and effort.
func whoOf(b threadBlock) string {
	if b.text("role") == "operator" {
		if e := b.text("by"); e != "" {
			return strings.SplitN(e, "@", 2)[0]
		}
		return "You"
	}
	parts := []string{}
	for _, k := range []string{"harness", "model", "effort"} {
		if v := b.text(k); v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) == 0 {
		return "Agent"
	}
	return strings.Join(parts, " ")
}

func markdownOf(b threadBlock, from, to *point, loc *time.Location) string {
	cut := func(s string) string {
		rs := []rune(s)
		a, z := 0, len(rs)
		if from != nil && from.key == b.text("key") {
			a = min(from.offset, len(rs))
		}
		if to != nil && to.key == b.text("key") {
			z = min(to.offset, len(rs))
		}
		if a > z {
			return ""
		}
		return string(rs[a:z])
	}
	switch b.text("kind") {
	case "text":
		head := whoOf(b)
		if t := clipTime(b.val("at"), loc); t != "" {
			head += " · " + t
		}
		s := fmt.Sprintf("**%s**\n%s\n", head, cut(firstString(b.val("plain"), b.val("markdown"))))
		var atts []rawObject
		json.Unmarshal(b["attachments"], &atts)
		for _, x := range atts {
			s += "*Attachment:* " + mdCode(x.text("name")) + "\n"
		}
		return s
	case "reasoning":
		return fmt.Sprintf("*Reasoning:*\n\n%s\n", cut(firstString(b.val("plain"), b.val("markdown"))))
	case "tool":
		s := fmt.Sprintf("**Tool** `%s` (%v)\n\n", strings.ReplaceAll(fmt.Sprint(b.val("name")), "`", ""), b.val("state"))
		if b.has("input") {
			s += fence(b.val("input")) + "\n"
		}
		if b.has("output") {
			s += fence(b.val("output")) + "\n"
		}
		return s
	case "question":
		s := "**Question:** " + b.text("text")
		if a := b.text("answer"); a != "" {
			s += "\n\n**Answer:** " + a
		}
		return s + "\n"
	case "result":
		parts := strList(b.val("artifactIds"))
		if stage := b.text("deliveryStage"); stage != "" {
			parts = append(parts, "Delivery "+strings.ReplaceAll(stage, "_", " "))
		}
		return "**Result:** " + strings.Join(parts, ", ") + "\n"
	case "boundary":
		parts := []string{}
		for _, k := range []string{"harness", "model", "effort"} {
			if v := b.text(k); v != "" {
				parts = append(parts, v)
			}
		}
		id := b.text("executionId")
		return fmt.Sprintf("---\n*Execution %s · %s · %v*\n", id[:min(8, len(id))], strings.Join(parts, " "), b.val("state"))
	}
	return ""
}

// replyText: the agent's reply text of one execution (final text blocks and cut-off streams), in order.
func replyText(q *Request, executionID string) ([]string, error) {
	return replyTextUpTo(q, executionID, 0)
}

// replyTextUpTo: replyText that stops paging once the texts hold at least maxRunes runes (0: no bound).
func replyTextUpTo(q *Request, executionID string, maxRunes int) ([]string, error) {
	es, err := q.db().ThreadMemberOf(q.ctx(), executionID)
	if err != nil || len(es) == 0 {
		return []string{}, err
	}
	m, ids := map[string]member{executionID: memberOf(es[0])}, []string{executionID}
	out, runes, page := []string{}, 0, 500
	if maxRunes > 0 {
		page = 200 // a bounded excerpt stops after the page that completes it
	}
	var after *bkey
	for {
		bs, more, err := side(q, m, ids, ">", after, page, false)
		if err != nil {
			return nil, err
		}
		for _, b := range bs {
			if md := b.text("markdown"); b.text("kind") == "text" && b.text("role") == "agent" && md != "" {
				out = append(out, md)
				runes += utf8.RuneCountInString(md)
			}
		}
		if !more || (maxRunes > 0 && runes >= maxRunes) {
			return out, nil
		}
		k := bs[len(bs)-1].key()
		after = &k
	}
}

func init() {
	op("thread_composer", threadComposer)
	op("thread_entries", threadEntries)
	op("thread_blocks", threadBlocks)
	op("thread_search", threadSearch)
	op("thread_export", func(q *Request, r *contract.ThreadExportRequest) (contract.ThreadExportReply, error) {
		key, err := threadKeyOf(r.Key)
		if err != nil {
			return contract.ThreadExportReply{}, err
		}
		var from, to *point
		for i, p := range []*contract.ThreadPoint{r.From, r.To} {
			if p == nil {
				continue
			}
			kk, err := parseKey(p.Key)
			if err != nil {
				return contract.ThreadExportReply{}, err
			}
			pt := &point{key: kk.String()}
			if p.Offset != nil {
				pt.offset = int(*p.Offset)
			}
			if i == 0 {
				from = pt
			} else {
				to = pt
			}
		}
		var sb strings.Builder
		err = exportPages(q, key, from, to, tzOf(r.Tz), func(s string) error { sb.WriteString(s); return nil })
		return contract.ThreadExportReply{Markdown: sb.String()}, err
	})
}

// exportPages: the thread as markdown, 500 blocks per page; each page in its own snapshot when q.tx is nil.
func exportPages(q *Request, key threadKey, from, to *point, loc *time.Location, emit func(string) error) error {
	var after *bkey
	if from != nil {
		k := mustKey(from.key)
		after = &k
	}
	firstPage := true
	for {
		var bs []threadBlock
		var more bool
		read := func() error {
			m, ids, err := members(q, key)
			if err != nil {
				return err
			}
			bs, more, err = side(q, m, ids, ">", after, 500, firstPage && from != nil)
			return err
		}
		err := q.app.store.Read(q.ctx(), q, func(_ *Request) error { return read() })
		if err != nil {
			return err
		}
		firstPage = false
		parts, done := []string{}, !more
		for _, x := range bs {
			if to != nil && cmpKey(x.key(), mustKey(to.key)) > 0 {
				done = true
				break
			}
			parts = append(parts, markdownOf(x, from, to, loc))
		}
		if len(parts) > 0 {
			if err := emit(strings.Join(parts, "\n") + "\n"); err != nil {
				return err
			}
		}
		if done || len(bs) == 0 {
			return nil
		}
		k := bs[len(bs)-1].key()
		after = &k
	}
}

// tzOf: the reader's time zone for copied times (body "tz", an IANA name); UTC when absent or unknown.
func tzOf(tz *string) *time.Location {
	if l, err := time.LoadLocation(deref(tz)); err == nil && deref(tz) != "" {
		return l
	}
	return time.UTC
}
