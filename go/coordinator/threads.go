// Conversation threads (docs/designs/fleet-conversations.md): one thread per task, or per session without a task, read in pages
// of typed blocks by block key (seq:index) in both directions, searched and exported on the server.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
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

const maxSeq = "9223372036854775807"

var terminalState = map[string]bool{"succeeded": true, "failed": true, "cancelled": true}

// threadKey: {task} or {session}.
type threadKey struct{ task, session string }

func (k threadKey) view() doc {
	if k.task != "" {
		return doc{"task": k.task}
	}
	return doc{"session": k.session}
}

func threadKeyOf(k contract.ThreadKey) (threadKey, error) {
	if t := deref(k.Task); t != "" {
		return threadKey{task: t}, nil
	}
	if s := deref(k.Session); s != "" {
		return threadKey{session: s}, nil
	}
	return threadKey{}, fail(400, "Expected key {task} or {session}")
}

type member struct {
	doc
	seq int64
}

// members: a task's executions, plus the taskless executions of sessions whose first (or attached) task it is.
func members(q *Request, key threadKey) (map[string]member, []string, error) {
	cols := "id,session_id,harness,model,config_ref->>'effort' AS effort,state::text AS state,started_at,ended_at,seq"
	var rs []doc
	var err error
	if key.task != "" {
		rs, err = rowsOf(q, `WITH attached AS (SELECT id AS session_id FROM session WHERE task_id=$1),
		 first_task AS (SELECT DISTINCT ON (session_id) session_id,task_id FROM execution WHERE task_id IS NOT NULL AND session_id IN (SELECT session_id FROM execution WHERE task_id=$1) ORDER BY session_id,seq)
		 SELECT `+cols+` FROM execution WHERE kind='agent_run' AND (task_id=$1 OR (task_id IS NULL AND (session_id IN (SELECT session_id FROM first_task WHERE task_id=$1) OR session_id IN (SELECT session_id FROM attached)))) ORDER BY seq`, key.task)
	} else {
		rs, err = rowsOf(q, "SELECT "+cols+" FROM execution WHERE kind='agent_run' AND session_id=$1 ORDER BY seq", key.session)
	}
	if err != nil {
		return nil, nil, err
	}
	if len(rs) == 0 {
		var ok bool
		if key.session != "" {
			ok, err = has(q, "SELECT 1 FROM session WHERE id=$1", key.session)
		} else {
			ok, err = has(q, "SELECT 1 FROM task WHERE id=$1", key.task)
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
		m[astr(r["id"])] = member{r, int64(num(r["seq"]))}
		ids = append(ids, astr(r["id"]))
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

func mark(e doc) doc { return doc{"harness": e["harness"], "model": e["model"], "effort": e["effort"]} }

// markOf: mark as the contract's optional fields.
func markOf(e doc) (harness, model, effort *string) {
	return strPtr(e["harness"]), strPtr(e["model"]), strPtr(e["effort"])
}

// msPtr: tsMs as an optional contract time.
func msPtr(v any) *int64 {
	if n, ok := tsMs(v).(int64); ok {
		return &n
	}
	return nil
}

// blockDoc: a block built from its contract struct (contracts/ops-sessions.schema.json Block), as the doc the thread code sorts and
// merges. Top-level integers stay int64, as in the executor blocks (tsMs), so every block carries one number type.
func blockDoc(v any) doc {
	dec := json.NewDecoder(bytes.NewReader(jsonBytes(v)))
	dec.UseNumber()
	var d doc
	dec.Decode(&d)
	for k, x := range d {
		if n, ok := x.(json.Number); ok {
			if i, err := n.Int64(); err == nil {
				d[k] = i
			}
		}
	}
	return d
}

func boundary(e member) doc {
	b := contract.BoundaryBlock{Kind: "boundary", Key: bkey{e.seq, 0}.String(), SessionId: strPtr(e.doc["session_id"]), ExecutionId: astr(e.doc["id"]),
		State: contract.ExecutionState(astr(e.doc["state"])), StartedAt: msPtr(e.doc["started_at"]), EndedAt: msPtr(e.doc["ended_at"])}
	b.Harness, b.Model, b.Effort = markOf(e.doc)
	return blockDoc(b)
}

func attachBoundaryResources(q *Request, bs []doc) error {
	ids, seen := []string{}, map[string]bool{}
	for _, b := range bs {
		if b["kind"] == "boundary" {
			if eid := astr(b["executionId"]); eid != "" && !seen[eid] {
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
		if b["kind"] == "boundary" {
			if res, ok := resMap[astr(b["executionId"])]; ok {
				b["resources"] = res
			}
		}
	}
	return nil
}

func questionBlock(qu doc, seq int64) doc {
	b := contract.QuestionBlock{Kind: "question", Key: bkey{seq, 0}.String(), ExecutionId: strPtr(qu["execution_id"]), QuestionId: astr(qu["id"]), Text: astr(qu["text"]),
		Options: json.RawMessage("[]"), State: astr(qu["state"]), AskedAt: msPtr(qu["asked_at"]), AnsweredAt: msPtr(qu["answered_at"]), AnsweredBy: strPtr(qu["answered_by"])}
	if qu["options"] != nil {
		b.Options = jsonBytes(qu["options"])
	}
	if qu["answer"] != nil {
		b.Answer = jsonBytes(qu["answer"])
	}
	return blockDoc(b)
}

// messageRow, eventRow: a thread row of a message or an event (migration 77 tables).
const messageRowExpr = `jsonb_build_object('id',id,'executionId',execution_id,'type',NULL,'text',to_jsonb(text),'blocks',NULL,'deltaOf',NULL,'closes',NULL,'at',updated_at,'sentBy',sent_by,'attachments',payload->'attachments')`
const eventRowExpr = `jsonb_build_object('id',id,'executionId',execution_id,'type',type,'text',payload->'text','blocks',payload->'data'->'fleetNormalized'->'blocks','deltaOf',payload->'data'->'fleetNormalized'->'deltaOf','closes',payload->'data'->'fleetNormalized'->'closes','at',updated_at,'sentBy',payload->>'sentBy','attachments',payload->'attachments')`

// threadSearchText: the text of an event a thread search matches (its blocks' text, names and tool input and output).
const threadSearchText = `(SELECT string_agg(concat_ws(' ',b->>'markdown',b->>'name',(b->'input')::text,(b->'output')::text),' ') FROM jsonb_array_elements(coalesce(payload->'data'->'fleetNormalized'->'blocks','[]'::jsonb)) b)`

type trow struct {
	seq int64
	src string
	v   doc
}

// threadRows: the rows of executions ids from seq from in direction dir; like (a search, ILIKE pattern) keeps only the
// messages and events whose text matches.
func threadRows(q *Request, ids []string, dir, from string, limit int, like ...any) ([]trow, error) {
	order := "ASC"
	if dir == "<" {
		order = "DESC"
	}
	off, msg, ev := "", "", ""
	if len(like) > 0 {
		off, msg, ev = " AND false", " AND text ILIKE $4", " AND "+threadSearchText+" ILIKE $4"
	}
	window := func(src, table, cond string) string {
		return `SELECT d.seq,d.src,d.v FROM unnest($1::text[]) m(id),
	  LATERAL (SELECT seq,'` + src + `' AS src,` + map[string]string{"message": messageRowExpr, "event": eventRowExpr}[table] + ` AS v FROM ` + table + ` WHERE execution_id=m.id AND seq ` + dir + `= $2::bigint` + cond + `
	   ORDER BY seq ` + order + ` LIMIT $3) d`
	}
	sql := `SELECT seq,src,v FROM (
	 ` + window("message", "message", msg) + `
	 UNION ALL ` + window("event", "event", ` AND NOT delta AND (type='execution.ended' OR jsonb_array_length(coalesce(payload->'data'->'fleetNormalized'->'blocks','[]'::jsonb))>0)`+ev) + `
	 UNION ALL ` + window("event-delta", "event", ` AND delta AND payload->'data'->'fleetNormalized'->>'opens'='true'`+off) + `
	 UNION ALL SELECT seq,'question',to_jsonb(q)-'data' FROM question q WHERE execution_id=ANY($1) AND seq ` + dir + `= $2::bigint` + off + `
	 UNION ALL SELECT seq,'execution',jsonb_build_object('id',id) FROM execution WHERE id=ANY($1) AND seq ` + dir + `= $2::bigint` + off + `
	) x ORDER BY seq ` + order + ` LIMIT $3`
	rs, err := rowsOf(q, sql, append([]any{ids, from, limit}, like...)...)
	out := []trow{}
	for _, r := range rs {
		out = append(out, trow{int64(num(r["seq"])), astr(r["src"]), mapOf(r["v"])})
	}
	return out, err
}

// stream: the text of an open stream (event-delta rows of one deltaOf); "", false once an event closes it.
func streamText(q *Request, executionID, id string) (string, bool, error) {
	closed, err := has(q, "SELECT 1 FROM event WHERE NOT delta AND execution_id=$1 AND payload->'data'->'fleetNormalized'->'closes' ? $2", executionID, id)
	if err != nil || closed {
		return "", false, err
	}
	parts, err := rowsOf(q, "SELECT payload->'data'->'fleetNormalized'->'blocks' AS b FROM event WHERE delta AND execution_id=$1 AND payload->'data'->'fleetNormalized'->>'deltaOf'=$2 ORDER BY seq", executionID, id)
	var sb strings.Builder
	for _, p := range parts {
		bs, _ := p["b"].([]any)
		for _, x := range bs {
			sb.WriteString(astr(x.(map[string]any)["markdown"]))
		}
	}
	return sb.String(), true, err
}

// expand: the blocks of one row, in index order.
// ponytail: the delivery stage on the result block is dropped with delivery (dead code, owner 2026-11-11).
func expand(q *Request, r trow, m map[string]member) ([]doc, error) {
	v := r.v
	e := m[firstString(v["executionId"], v["execution_id"])]
	at := func(i int) string { return bkey{r.seq, int64(i)}.String() }
	switch r.src {
	case "question":
		return []doc{questionBlock(v, r.seq)}, nil
	case "message":
		t, ok := v["text"].(string)
		if !ok {
			return nil, nil
		}
		b := contract.TextBlock{Kind: "text", Key: at(0), ExecutionId: strPtr(v["executionId"]), Role: "operator", Markdown: t, Plain: &t, At: msPtr(v["at"]), By: strPtr(v["sentBy"])}
		for _, x := range anys(v["attachments"]) {
			m := mapOf(x)
			b.Attachments = append(b.Attachments, contract.ResultArtifact{Id: astr(m["artifactId"]), Name: astr(m["name"]), MediaType: astr(m["mediaType"]), Size: int64(num(m["size"]))})
		}
		return []doc{blockDoc(b)}, nil
	case "event-delta":
		text, open, err := streamText(q, astr(v["executionId"]), astr(v["deltaOf"]))
		if err != nil || !open {
			return nil, err
		}
		stream := "open"
		if e.doc != nil && terminalState[astr(e.doc["state"])] {
			stream = "cut-off"
		}
		if strings.HasPrefix(astr(v["deltaOf"]), "reasoning:") {
			return []doc{blockDoc(contract.ReasoningBlock{Kind: "reasoning", Key: at(0), ExecutionId: strPtr(v["executionId"]), Markdown: text, Plain: &text, Stream: &stream, At: msPtr(v["at"])})}, nil
		}
		b := contract.TextBlock{Kind: "text", Key: at(0), ExecutionId: strPtr(v["executionId"]), Role: "agent", Markdown: text, Plain: &text, Stream: &stream, At: msPtr(v["at"])}
		b.Harness, b.Model, b.Effort = markOf(e.doc)
		return []doc{blockDoc(b)}, nil
	}
	own := []doc{}
	bs, _ := v["blocks"].([]any)
	for i, x := range bs {
		src, _ := x.(map[string]any)
		b := doc{}
		for k, y := range src {
			b[k] = y
		}
		b["key"], b["executionId"], b["at"] = at(i), v["executionId"], tsMs(v["at"])
		if astr(b["kind"]) == "text" {
			b["role"] = firstString(src["role"], "agent")
			for k, y := range mark(e.doc) {
				b[k] = y
			}
		}
		if k := astr(b["kind"]); k == "text" || k == "reasoning" {
			b["plain"] = astr(src["markdown"])
		}
		own = append(own, b)
	}
	if astr(v["type"]) == "execution.ended" {
		arts, err := storedArtifacts(q, astr(v["executionId"]))
		if err != nil {
			return nil, err
		}
		if len(arts) > 0 {
			r := contract.ResultBlock{Kind: "result", Key: at(len(own)), ExecutionId: strPtr(v["executionId"]), ArtifactIds: []string{}}
			for _, a := range arts {
				r.ArtifactIds = append(r.ArtifactIds, astr(a["id"]))
				r.Artifacts = append(r.Artifacts, contract.ResultArtifact{Id: astr(a["id"]), Name: astr(a["name"]), MediaType: astr(a["mediaType"]), Size: int64(num(a["size"]))})
			}
			own = append(own, blockDoc(r))
		}
	}
	return own, nil
}

func storedArtifacts(q *Request, executionID string) ([]doc, error) {
	return rowsOf(q, `SELECT coalesce(legacy_id,id) AS id,name,media_type AS "mediaType",coalesce(size,0) AS size FROM artifact WHERE execution_id=$1 AND state='stored' ORDER BY seq`, executionID)
}

func reverse(xs []doc) []doc {
	out := make([]doc, len(xs))
	for i, x := range xs {
		out[len(xs)-1-i] = x
	}
	return out
}

// side: up to limit blocks strictly before ('<') or after ('>') from, nearest first; more says whether more remain.
func side(q *Request, m map[string]member, ids []string, dir string, from *bkey, limit int, inclusive bool) ([]doc, bool, error) {
	out, seen := []doc{}, map[int64]bool{}
	cursor := "0"
	if dir == "<" {
		cursor = maxSeq
	}
	if from != nil {
		cursor = strconv.FormatInt(from.seq, 10)
	}
	keep := func(b doc) bool {
		if from == nil {
			return true
		}
		c := cmpKey(mustKey(b["key"]), *from)
		if dir == "<" {
			return c < 0
		}
		if inclusive {
			return c >= 0
		}
		return c > 0
	}
	for {
		batch, err := threadRows(q, ids, dir, cursor, limit+1)
		if err != nil {
			return nil, false, err
		}
		for _, r := range batch {
			if seen[r.seq] {
				continue
			}
			seen[r.seq] = true
			var bs []doc
			if r.src == "execution" {
				bs = []doc{boundary(m[astr(r.v["id"])])}
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
		cursor = strconv.FormatInt(batch[len(batch)-1].seq, 10)
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
	var n int64
	if err := q.tx.QueryRow(q.ctx(), `SELECT (SELECT count(*) FROM message WHERE execution_id=ANY($1))
	 +(SELECT coalesce(sum(jsonb_array_length(coalesce(payload->'data'->'fleetNormalized'->'blocks','[]'::jsonb))),0) FROM event WHERE NOT delta AND execution_id=ANY($1))
	 +(SELECT count(*) FROM question WHERE execution_id=ANY($1))
	 +(SELECT count(*) FROM event v WHERE NOT v.delta AND v.type='execution.ended' AND v.execution_id=ANY($1) AND EXISTS(SELECT 1 FROM artifact a WHERE a.execution_id=v.execution_id AND a.state='stored'))`, ids).Scan(&n); err != nil {
		return 0, err
	}
	n += int64(len(m))
	ss, err := rowsOf(q, "SELECT execution_id AS e,payload->'data'->'fleetNormalized'->>'deltaOf' AS id FROM event WHERE delta AND execution_id=ANY($1) AND payload->'data'->'fleetNormalized'->>'opens'='true'", ids)
	for _, x := range ss {
		if _, open, err := streamText(q, astr(x["e"]), astr(x["id"])); err != nil {
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
	t, err := first(q, "SELECT coalesce((SELECT task_id FROM session WHERE id=$1),(SELECT task_id FROM execution WHERE session_id=$1 AND task_id IS NOT NULL ORDER BY seq LIMIT 1)) AS t", sid)
	if err != nil {
		return threadKey{}, err
	}
	if tid := astr(t["t"]); tid != "" {
		return threadKey{task: tid}, nil
	}
	return threadKey{session: sid}, nil
}

func feedCursor(q *Request) (int64, error) { return head(q.ctx(), q.tx) }

func threadEntries(q *Request, b *contract.ThreadEntriesRequest) (doc, error) {
	key, err := threadKeyOf(b.Key)
	if err != nil {
		return nil, err
	}
	if b.Resolve != nil && *b.Resolve && key.session != "" {
		if key, err = keyOfSession(q, key.session); err != nil {
			return nil, err
		}
	}
	limit, err := positiveLimit(b.Limit)
	if err != nil {
		return nil, err
	}
	m, ids, err := members(q, key)
	if err != nil {
		return nil, err
	}
	given := 0
	for _, k := range []*string{b.Before, b.After, b.Around} {
		if k != nil {
			given++
		}
	}
	if given > 1 {
		return nil, fail(400, "Expected one of before, after, around")
	}
	var entries []doc
	hasBefore, hasAfter, withTotal := false, false, false
	switch {
	case b.Before != nil:
		k, err := parseKey(*b.Before)
		if err != nil {
			return nil, err
		}
		if entries, hasBefore, err = side(q, m, ids, "<", &k, limit, false); err != nil {
			return nil, err
		}
		hasAfter = true
	case b.After != nil:
		k, err := parseKey(*b.After)
		if err != nil {
			return nil, err
		}
		if entries, hasAfter, err = side(q, m, ids, ">", &k, limit, false); err != nil {
			return nil, err
		}
		hasBefore = true
	case b.Around != nil:
		k, err := parseKey(*b.Around)
		if err != nil {
			return nil, err
		}
		half := limit / 2
		l, lmore, err := side(q, m, ids, "<", &k, half, false)
		if err != nil {
			return nil, err
		}
		r, rmore, err := side(q, m, ids, ">", &k, limit-half, true)
		if err != nil {
			return nil, err
		}
		entries, hasBefore, hasAfter, withTotal = append(l, r...), lmore, rmore, true
		if half == 0 && !hasBefore {
			one, _, err := side(q, m, ids, "<", &k, 1, false)
			if err != nil {
				return nil, err
			}
			hasBefore = len(one) > 0
		}
	default:
		if entries, hasBefore, err = side(q, m, ids, "<", nil, limit, false); err != nil {
			return nil, err
		}
		withTotal = true
	}
	if err := attachBoundaryResources(q, entries); err != nil {
		return nil, err
	}
	sessions, seen := []any{}, map[string]bool{}
	for _, id := range ids {
		if s := astr(m[id].doc["session_id"]); s != "" && !seen[s] {
			seen[s] = true
			sessions = append(sessions, s)
		}
	}
	out := doc{"key": key.view(), "entries": entries, "first": nil, "last": nil, "hasBefore": hasBefore, "hasAfter": hasAfter, "sessionIds": sessions}
	if len(entries) > 0 {
		out["first"], out["last"] = entries[0]["key"], entries[len(entries)-1]["key"]
	}
	if withTotal {
		if out["total"], err = threadTotal(q, m, ids); err != nil {
			return nil, err
		}
	}
	out["feedCursor"], err = feedCursor(q)
	return out, err
}

// eventRow: the thread rows of the events (and, with messages, the messages) where sql holds.
func eventRow(q *Request, messages bool, sql string, args ...any) ([]trow, error) {
	src := "SELECT seq,CASE WHEN delta THEN 'event-delta' ELSE 'event' END AS src," + eventRowExpr + " AS v FROM event WHERE " + sql
	if messages {
		src += " UNION ALL SELECT seq,'message' AS src," + messageRowExpr + " AS v FROM message WHERE " + sql
	}
	rs, err := rowsOf(q, src, args...)
	out := []trow{}
	for _, r := range rs {
		out = append(out, trow{int64(num(r["seq"])), astr(r["src"]), mapOf(r["v"])})
	}
	return out, err
}

func threadBlocks(q *Request, b *contract.ThreadBlocksRequest) (doc, error) {
	key, err := threadKeyOf(b.Key)
	if err != nil {
		return nil, err
	}
	m, ids, err := members(q, key)
	if err != nil {
		return nil, err
	}

	out, removed := []doc{}, []string{}
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
			qs, err := rowsOf(q, "SELECT * FROM question WHERE id=$1 AND execution_id=ANY($2)", id, ids)
			if err != nil {
				return nil, err
			}
			for _, qu := range qs {
				out = append(out, questionBlock(qu, int64(num(qu["seq"]))))
			}
		case "artifact":
			r, err := first(q, "SELECT execution_id AS e FROM artifact WHERE id=$1 OR legacy_id=$1", id)
			if err != nil {
				return nil, err
			}
			if e := astr(r["e"]); e != "" {
				if _, ok := m[e]; ok {
					rs, err := eventRow(q, false, "id=$1 AND NOT delta", "ended:"+e)
					if err != nil {
						return nil, err
					}
					if err := add(rs); err != nil {
						return nil, err
					}
				}
			}
		case "event":
			rs, err := eventRow(q, true, "id=$1 AND execution_id=ANY($2)", id, ids)
			if err != nil {
				return nil, err
			}
			for _, d := range rs {
				if d.src == "event-delta" {
					f, err := eventRow(q, false, "delta AND execution_id=$1 AND payload->'data'->'fleetNormalized'->>'deltaOf'=$2 AND payload->'data'->'fleetNormalized'->>'opens'='true'", d.v["executionId"], d.v["deltaOf"])
					if err != nil {
						return nil, err
					}
					if err := add(f[:min(1, len(f))]); err != nil {
						return nil, err
					}
					continue
				}
				closes, _ := d.v["closes"].([]any)
				for _, cid := range closes {
					os, err := rowsOf(q, "SELECT seq FROM event WHERE delta AND execution_id=$1 AND payload->'data'->'fleetNormalized'->>'deltaOf'=$2 AND payload->'data'->'fleetNormalized'->>'opens'='true'", d.v["executionId"], cid)
					if err != nil {
						return nil, err
					}
					for _, o := range os {
						removed = append(removed, bkey{int64(num(o["seq"])), 0}.String())
					}
				}
				if err := add([]trow{d}); err != nil {
					return nil, err
				}
			}
		}
	}
	byKey := map[string]doc{}
	for _, x := range out {
		byKey[astr(x["key"])] = x
	}
	entries := []doc{}
	for _, x := range byKey {
		entries = append(entries, x)
	}
	sort.Slice(entries, func(i, j int) bool { return cmpKey(mustKey(entries[i]["key"]), mustKey(entries[j]["key"])) < 0 })
	if err := attachBoundaryResources(q, entries); err != nil {
		return nil, err
	}
	return doc{"removed": removed, "entries": entries}, nil
}

func searchable(b doc) string {
	switch astr(b["kind"]) {
	case "text", "reasoning":
		return firstString(b["plain"], b["markdown"])
	case "tool":
		return strings.Join([]string{fmt.Sprint(b["name"]), jsOr(b["input"]), jsOr(b["output"])}, " ")
	case "question":
		return astr(b["text"]) + " " + astr(b["answer"])
	}
	return ""
}

func jsOr(v any) string {
	if v == nil {
		return `""`
	}
	return js(v)
}

func threadSearch(q *Request, b *contract.ThreadSearchRequest) (doc, error) {
	key, err := threadKeyOf(b.Key)
	if err != nil {
		return nil, err
	}
	limit, err := positiveLimit(b.Limit)
	if err != nil {
		return nil, err
	}
	qs := strings.TrimSpace(b.Q)
	if qs == "" {
		return nil, fail(400, "Expected q")
	}
	var before *bkey
	if b.Before != nil {
		k, err := parseKey(*b.Before)
		if err != nil {
			return nil, err
		}
		before = &k
	}
	m, ids, err := members(q, key)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(qs)
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(qs) + "%"
	cursor := maxSeq
	if before != nil {
		cursor = strconv.FormatInt(before.seq, 10)
	}
	hits, seen := []doc{}, map[string]bool{}
	for {
		batch, err := threadRows(q, ids, "<", cursor, limit+1, like)
		if err != nil {
			return nil, err
		}
		qrows, err := rowsOf(q, "SELECT * FROM question WHERE execution_id=ANY($1) AND seq<=$2::bigint AND (text ILIKE $3 OR coalesce(answer,'') ILIKE $3) ORDER BY seq DESC LIMIT $4", ids, cursor, like, limit+1)
		if err != nil {
			return nil, err
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
			if s := int64(num(x["seq"])); s >= lo {
				all = append(all, trow{s, "question", x})
			}
		}
		sort.SliceStable(all, func(i, j int) bool { return all[i].seq > all[j].seq })
		for _, r := range all {
			bs, err := expand(q, r, m)
			if err != nil {
				return nil, err
			}
			for _, x := range reverse(bs) {
				if before != nil && cmpKey(mustKey(x["key"]), *before) >= 0 {
					continue
				}
				t := searchable(x)
				i := strings.Index(strings.ToLower(t), needle)
				if i < 0 || seen[astr(x["key"])] {
					continue
				}
				seen[astr(x["key"])] = true
				rs := []rune(t)
				ri := len([]rune(t[:i]))
				snip := ""
				if ri > 40 {
					snip = "…"
				}
				snip += string(rs[max(0, ri-40):min(len(rs), ri+len([]rune(qs))+80)])
				hits = append(hits, doc{"key": x["key"], "snippet": snip})
			}
		}
		if len(hits) > limit || len(batch) <= limit {
			break
		}
		cursor = strconv.FormatInt(lo, 10)
	}
	return doc{"hits": hits[:min(len(hits), limit)], "hasMore": len(hits) > limit}, nil
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
func whoOf(b doc) string {
	if astr(b["role"]) == "operator" {
		if e := astr(b["by"]); e != "" {
			return strings.SplitN(e, "@", 2)[0]
		}
		return "You"
	}
	parts := []string{}
	for _, k := range []string{"harness", "model", "effort"} {
		if v := astr(b[k]); v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) == 0 {
		return "Agent"
	}
	return strings.Join(parts, " ")
}

func markdownOf(b doc, from, to *point, loc *time.Location) string {
	cut := func(s string) string {
		rs := []rune(s)
		a, z := 0, len(rs)
		if from != nil && from.key == astr(b["key"]) {
			a = min(from.offset, len(rs))
		}
		if to != nil && to.key == astr(b["key"]) {
			z = min(to.offset, len(rs))
		}
		if a > z {
			return ""
		}
		return string(rs[a:z])
	}
	switch astr(b["kind"]) {
	case "text":
		head := whoOf(b)
		if t := clipTime(b["at"], loc); t != "" {
			head += " · " + t
		}
		s := fmt.Sprintf("**%s**\n%s\n", head, cut(firstString(b["plain"], b["markdown"])))
		for _, x := range anys(b["attachments"]) {
			s += "*Attachment:* " + mdCode(astr(mapOf(x)["name"])) + "\n"
		}
		return s
	case "reasoning":
		return fmt.Sprintf("*Reasoning:*\n\n%s\n", cut(firstString(b["plain"], b["markdown"])))
	case "tool":
		s := fmt.Sprintf("**Tool** `%s` (%v)\n\n", strings.ReplaceAll(fmt.Sprint(b["name"]), "`", ""), b["state"])
		if b["input"] != nil {
			s += fence(b["input"]) + "\n"
		}
		if b["output"] != nil {
			s += fence(b["output"]) + "\n"
		}
		return s
	case "question":
		s := "**Question:** " + astr(b["text"])
		if a := astr(b["answer"]); a != "" {
			s += "\n\n**Answer:** " + a
		}
		return s + "\n"
	case "result":
		return "**Result:** " + strings.Join(strList(b["artifactIds"]), ", ") + "\n"
	case "boundary":
		parts := []string{}
		for _, k := range []string{"harness", "model", "effort"} {
			if v := astr(b[k]); v != "" {
				parts = append(parts, v)
			}
		}
		id := astr(b["executionId"])
		return fmt.Sprintf("---\n*Execution %s · %s · %v*\n", id[:min(8, len(id))], strings.Join(parts, " "), b["state"])
	}
	return ""
}

// replyText: the agent's reply text of one execution (final text blocks and cut-off streams), in order.
func replyText(q *Request, executionID string) ([]string, error) {
	return replyTextUpTo(q, executionID, 0)
}

// replyTextUpTo: replyText that stops paging once the texts hold at least maxRunes runes (0: no bound).
func replyTextUpTo(q *Request, executionID string, maxRunes int) ([]string, error) {
	e, err := first(q, "SELECT id,session_id,harness,model,config_ref->>'effort' AS effort,state::text AS state,started_at,ended_at,seq FROM execution WHERE id=$1", executionID)
	if err != nil || e == nil {
		return []string{}, err
	}
	m, ids := map[string]member{executionID: {e, int64(num(e["seq"]))}}, []string{executionID}
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
			if astr(b["kind"]) == "text" && astr(b["role"]) == "agent" && astr(b["markdown"]) != "" {
				out = append(out, astr(b["markdown"]))
				runes += utf8.RuneCountInString(astr(b["markdown"]))
			}
		}
		if !more || (maxRunes > 0 && runes >= maxRunes) {
			return out, nil
		}
		k := mustKey(bs[len(bs)-1]["key"])
		after = &k
	}
}

func init() {
	op("thread_entries", func(q *Request, r *contract.ThreadEntriesRequest) (contract.ThreadEntriesReply, error) {
		return replyAs[contract.ThreadEntriesReply](threadEntries(q, r))
	})
	op("thread_blocks", func(q *Request, r *contract.ThreadBlocksRequest) (contract.ThreadBlocksReply, error) {
		return replyAs[contract.ThreadBlocksReply](threadBlocks(q, r))
	})
	op("thread_search", func(q *Request, r *contract.ThreadSearchRequest) (contract.ThreadSearchReply, error) {
		return replyAs[contract.ThreadSearchReply](threadSearch(q, r))
	})
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
		var bs []doc
		var more bool
		read := func() error {
			m, ids, err := members(q, key)
			if err != nil {
				return err
			}
			bs, more, err = side(q, m, ids, ">", after, 500, firstPage && from != nil)
			return err
		}
		var err error
		if q.tx != nil {
			err = read()
		} else {
			err = q.app.store.Tx(q.ctx(), scopeOf(q.person), false, "", func(tx pgx.Tx) error {
				q.tx = tx
				defer func() { q.tx = nil }()
				return read()
			})
		}
		if err != nil {
			return err
		}
		firstPage = false
		parts, done := []string{}, !more
		for _, x := range bs {
			if to != nil && cmpKey(mustKey(x["key"]), mustKey(to.key)) > 0 {
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
		k := mustKey(bs[len(bs)-1]["key"])
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
