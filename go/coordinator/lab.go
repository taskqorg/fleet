// Lab (docs/designs/fleet-lab.md): examples, experiments, scores and ratings.
// An example is a lab-scope artifact with role 'example'. An experiment is pinned approved examples x variants x repeats of
// one v2 pipeline; a trial is a pipeline_run with experiment_id, variant and request_id; a judge run has variant null.
// labReconcile(id) is the one function that moves an experiment, under the experiment's scope lock.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

var (
	labIdent     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)
	labIdent200  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)
	labOrigin    = regexp.MustCompile(`^(task|export):[A-Za-z0-9._-]{1,80}$`) // export: a project export row (contracts/lab-export.schema.json)
	labCommit    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	labLabel     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+-]{0,59}$`)
	draftExample = regexp.MustCompile(`/examples/[^/]+\.json$`)
	labOverrides = []string{"profile", "model", "skills", "prompt", "web_route", "account", "host", "arguments"}
	// labVariantKeys: a Lab variant's keys; bands steer its typed evaluator, never a run, so runs take labOverrides only.
	labVariantKeys = append(append([]string{}, labOverrides...), "bands")
)

func labStr(v any, what string, max int) (string, error) {
	s, ok := v.(string)
	if !ok || s == "" || len(s) > max {
		return "", fail(400, "Invalid "+what)
	}
	return s, nil
}

func labID(v any, what string) (string, error) {
	s, ok := v.(string)
	if !ok || !labIdent.MatchString(s) {
		return "", fail(400, "Invalid "+what)
	}
	return s, nil
}

func expID(v any, what string) (string, error) {
	s, ok := v.(string)
	if !ok || !labIdent200.MatchString(s) {
		return "", fail(400, "Invalid "+what)
	}
	return s, nil
}

var (
	labSha   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	labMedia = regexp.MustCompile(`^[a-z]+/[A-Za-z0-9.+-]+$`)
)

// exampleFileBytes: a text file's text, or a binary file's stored bytes, found by its content hash among the example
// files of the run's project or of no project.
func exampleFileBytes(q *Request, projectID any, f doc) ([]byte, error) {
	if f["sha256"] == nil {
		return []byte(astr(f["text"])), nil
	}
	rs, err := queryRows(q, "SELECT id FROM artifact WHERE role='example-file' AND scope='lab' AND state='stored' AND content_hash=$1 AND coalesce(project_id,'') IN ('', $2) ORDER BY created_at LIMIT 1", f["sha256"], strOr(projectID))
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, fail(409, "Example file "+astr(f["path"])+" is not stored; import the example again")
	}
	id := astr(rs[0]["id"])
	return artifactBytes(withTx(q), q.app, id, 64<<20)
}

// storeExampleFiles checks each binary file of an example against the seed directory and, with write, stores it.
func storeExampleFiles(q *Request, projectID any, id string, ex orderedDoc, write bool) error {
	for _, kv := range ex {
		if kv[0] != "inputs" && kv[0] != "references" {
			continue
		}
		for _, x := range kv[1].([]any) {
			o := x.(orderedDoc)
			if o[1][0] != "sha256" {
				continue
			}
			path, sha, media := o[0][1].(string), o[1][1].(string), o[2][1].(string)
			b, err := os.ReadFile(filepath.Join(labDir(), "examples", id, path))
			if err != nil {
				return fail(400, "Example file "+path+" is missing next to the seed")
			}
			if sha256hex(b) != sha {
				return fail(400, "Example file "+path+" does not match its sha256")
			}
			if len(b) > 64<<20 {
				return fail(400, "Example file "+path+" is over 64 MiB")
			}
			if !write {
				continue
			}
			var n int
			if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM artifact WHERE role='example-file' AND scope='lab' AND state='stored' AND content_hash=$1 AND coalesce(project_id,'')=$2", sha, strOr(projectID)).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			h, storeID, err := artifactWriteObject(q, projectID, "lab", b, media)
			if err != nil {
				return err
			}
			aid := newID()
			if err := exec(q, `INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,role,state) VALUES($1,$2,'lab',$3,$4,$5,$6,'example-file','stored')`,
				aid, projectID, h, len(b), media, "examples/"+id+"/"+path); err != nil {
				return err
			}
			if err := exec(q, "INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,verified_at) VALUES($1,$2,$3,'verified',now())", aid, storeID, projectID); err != nil {
				return err
			}
		}
	}
	return nil
}

func labFiles(v any, what string) ([]any, error) {
	if v == nil {
		return []any{}, nil
	}
	l, ok := v.([]any)
	if !ok {
		return nil, fail(400, "Invalid "+what)
	}
	seen, out := map[string]bool{}, []any{}
	for _, x := range l {
		f := mapOf(x)
		p, _ := f["path"].(string)
		bad := p == "" || len(p) > 300 || strings.HasPrefix(p, "/")
		for _, part := range strings.Split(p, "/") {
			bad = bad || part == "" || part == "." || part == ".."
		}
		if bad {
			return nil, fail(400, "Invalid file path")
		}
		if seen[p] {
			return nil, fail(400, "Duplicate "+what+" path "+p)
		}
		seen[p] = true
		if sha, media := astr(f["sha256"]), astr(f["media_type"]); f["text"] == nil && labSha.MatchString(sha) && labMedia.MatchString(media) {
			// Binary form: the bytes sit next to the seed (config/lab/examples/<example id>/<path>) and import stores them.
			out = append(out, orderedDoc{{"path", p}, {"sha256", sha}, {"media_type", media}})
			continue
		}
		t, ok := f["text"].(string)
		if !ok {
			return nil, fail(400, "Invalid "+what+" text")
		}
		out = append(out, orderedDoc{{"path", p}, {"text", t}})
	}
	return out, nil
}

// orderedDoc marshals its fields in order (an example's canonical shape: equal content gives an equal hash).
type orderedDoc [][2]any

func (o orderedDoc) MarshalJSON() ([]byte, error) {
	b := []byte{'{'}
	for i, kv := range o {
		if i > 0 {
			b = append(b, ',')
		}
		k, _ := json.Marshal(kv[0])
		v, err := json.Marshal(kv[1])
		if err != nil {
			return nil, err
		}
		b = append(append(append(b, k...), ':'), v...)
	}
	return append(b, '}'), nil
}

func useCases() ([]doc, error) {
	dir := filepath.Join(labDir(), "use-cases")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := []doc{}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		var l []doc
		if err := json.Unmarshal(b, &l); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		for _, e := range l {
			if err := validUseCase(e); err != nil {
				return nil, fmt.Errorf("%s: %v: %w", n, e["id"], err)
			}
		}
		out = append(out, l...)
	}
	ids := map[string]bool{}
	for _, e := range out {
		ids[astr(e["id"])] = true
	}
	for _, e := range out {
		for _, t := range anys(e["maps_to"]) {
			if !fleetUC.MatchString(astr(t)) || !ids[astr(t)] {
				return nil, fmt.Errorf("use case %v: maps_to %v is not an existing fleet:UCn entry", e["id"], t)
			}
		}
	}
	return out, nil
}

var fleetUC = regexp.MustCompile(`^fleet:UC[0-9]+$`)

// validUseCase checks one registry entry against contracts/lab-registry.schema.json (UseCase).
func validUseCase(e doc) error {
	var u contract.UseCase
	dec := json.NewDecoder(bytes.NewReader(jsonBytes(e)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&u); err != nil {
		return err
	}
	if u.Id == "" {
		return contract.Missing("id")
	}
	return u.Validate()
}

// parseExample validates one example into its canonical shape.
func parseExample(v any, ids map[string]bool) (orderedDoc, doc, error) {
	e := mapOf(v)
	if e == nil {
		return nil, nil, fail(400, "Expected an example object")
	}
	id, err := labID(e["id"], "example id")
	if err != nil {
		return nil, nil, err
	}
	uc, err := labStr(e["use_case_id"], "use_case_id", 200)
	if err != nil {
		return nil, nil, err
	}
	if !ids[uc] {
		return nil, nil, fail(400, "Unknown use case "+uc)
	}
	version, ok := intOK(e["version"])
	if !ok || version < 1 {
		return nil, nil, fail(400, "Invalid version")
	}
	origin, _ := e["origin"].(string)
	if origin != "synthetic" && !labOrigin.MatchString(origin) {
		return nil, nil, fail(400, "Invalid origin")
	}
	var fixture any
	if f := mapOf(e["fixture"]); f != nil {
		repo, err := labID(f["repo"], "fixture repo")
		if err != nil {
			return nil, nil, err
		}
		c, _ := f["commit"].(string)
		if !labCommit.MatchString(c) {
			return nil, nil, fail(400, "Invalid fixture commit")
		}
		fixture = orderedDoc{{"repo", repo}, {"commit", c}}
	} else if e["fixture"] != nil {
		return nil, nil, fail(400, "Invalid fixture repo")
	}
	cl, ok := e["checks"].([]any)
	if !ok {
		return nil, nil, fail(400, "Invalid checks")
	}
	checks, seen := []any{}, map[string]bool{}
	for _, x := range cl {
		c := mapOf(x)
		cid, err := labID(c["id"], "check id")
		if err != nil {
			return nil, nil, err
		}
		run, err := labStr(c["run"], "check command", 10000)
		if err != nil {
			return nil, nil, err
		}
		if seen[cid] {
			return nil, nil, fail(400, "Duplicate check id")
		}
		seen[cid] = true
		checks = append(checks, orderedDoc{{"id", cid}, {"run", run}})
	}
	split, _ := e["split"].(string)
	if split != "tune" && split != "held_out" {
		return nil, nil, fail(400, "Invalid split")
	}
	prompt, err := labStr(e["prompt"], "prompt", 100000)
	if err != nil {
		return nil, nil, err
	}
	inputs, err := labFiles(e["inputs"], "input")
	if err != nil {
		return nil, nil, err
	}
	refs, err := labFiles(e["references"], "reference")
	if err != nil {
		return nil, nil, err
	}
	o := orderedDoc{{"id", id}, {"use_case_id", uc}, {"version", version}, {"origin", origin}, {"fixture", fixture}, {"prompt", prompt},
		{"inputs", inputs}, {"references", refs}, {"checks", checks}, {"split", split}}
	return o, doc{"id": id, "version": version}, nil
}

// Import: an identical example (same id, version and content) returns the stored artifact; other content under the same
// id and version is refused. A refused batch stores nothing.
func labExamplesAdd(q *Request, b doc) (doc, error) {
	list := anys(b["examples"])
	if len(list) == 0 {
		return nil, fail(400, "Expected examples")
	}
	var projectID any
	if b["projectId"] != nil {
		p, err := labID(b["projectId"], "projectId")
		if err != nil {
			return nil, err
		}
		projectID = p
	}
	ucs, err := useCases()
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, u := range ucs {
		ids[astr(u["id"])] = true
	}
	type plan struct {
		meta  doc
		bytes []byte
		name  string
		hash  string
		old   doc
		ex    orderedDoc
	}
	existing := func(name string) (doc, error) {
		rs, err := queryRows(q, "SELECT id,content_hash,approved_at FROM artifact WHERE role='example' AND state<>'deleted' AND name=$1 AND coalesce(project_id,'')=$2", name, strOr(projectID))
		if err != nil || len(rs) == 0 {
			return nil, err
		}
		return rs[0], nil
	}
	planned := []plan{}
	for _, x := range list {
		ex, meta, err := parseExample(x, ids)
		if err != nil {
			return nil, err
		}
		bytes, _ := json.MarshalIndent(ex, "", " ")
		bytes = append(bytes, '\n')
		name := "examples/" + exampleRef(meta["id"], meta["version"]) + ".json"
		hash := sha256hex(bytes)
		pid := strOr(projectID)
		if pid == "" {
			pid = "_"
		}
		var n int
		if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM purged_hash WHERE project_id=$1 AND scope='lab' AND content_hash=$2", pid, hash).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, fail(409, name+": content was purged")
		}
		old, err := existing(name)
		if err != nil {
			return nil, err
		}
		if old != nil && old["content_hash"] != hash {
			return nil, fail(409, name+" exists with other content; raise version")
		}
		if err := storeExampleFiles(q, projectID, astr(meta["id"]), ex, false); err != nil {
			return nil, err
		}
		planned = append(planned, plan{meta, bytes, name, hash, old, ex})
	}
	approved := b["approved"] == true
	// Fix: approval lets the coordinator run the example's check commands, so only the owner approves at import.
	if approved && q.person != nil && !fullAccess(q.person) {
		return nil, fail(403, "Only the owner approves examples")
	}
	out := []any{}
	for _, p := range planned {
		// Binary files are written only once the whole batch passed, so a refused batch stores nothing.
		if err := storeExampleFiles(q, projectID, astr(p.meta["id"]), p.ex, true); err != nil {
			return nil, err
		}
		old := p.old
		if old == nil {
			h, storeID, err := artifactWriteObject(q, projectID, "lab", p.bytes, "application/json")
			if err != nil {
				return nil, err
			}
			id := newID()
			var at any
			if approved {
				at = time.Now()
			}
			rs, err := queryRows(q, `INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,role,state,approved_at) VALUES($1,$2,'lab',$3,$4,'application/json',$5,'example','stored',$6)
			 ON CONFLICT (coalesce(project_id,''),name) WHERE role='example' AND state<>'deleted' DO NOTHING RETURNING id`, id, projectID, h, len(p.bytes), p.name, at)
			if err != nil {
				return nil, err
			}
			if len(rs) > 0 {
				if err := exec(q, "INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,verified_at) VALUES($1,$2,$3,'verified',now())", id, storeID, projectID); err != nil {
					return nil, err
				}
				out = append(out, doc{"artifactId": id, "id": p.meta["id"], "version": p.meta["version"], "hash": p.hash, "created": true})
				continue
			}
			if old, err = existing(p.name); err != nil || old == nil {
				return nil, fail(500, "Example row missing after conflict")
			}
			if old["content_hash"] != p.hash {
				return nil, fail(409, p.name+" exists with other content; raise version")
			}
		}
		if approved && old["approved_at"] == nil {
			if err := exec(q, "UPDATE artifact SET approved_at=now() WHERE id=$1", old["id"]); err != nil {
				return nil, err
			}
		}
		out = append(out, doc{"artifactId": old["id"], "id": p.meta["id"], "version": p.meta["version"], "hash": p.hash, "created": false})
	}
	return doc{"examples": out}, nil
}

func labExamplesList(q *Request, state any) (doc, error) {
	cond := ""
	switch state {
	case nil:
	case "approved":
		cond = " AND approved_at IS NOT NULL"
	case "proposed":
		cond = " AND approved_at IS NULL"
	default:
		return nil, fail(400, "Invalid state")
	}
	rs, err := queryRows(q, "SELECT id,project_id,name,content_hash,approved_at,created_at FROM artifact WHERE role='example' AND state='stored'"+cond+" ORDER BY name")
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, r := range rs {
		st := "proposed"
		if r["approved_at"] != nil {
			st = "approved"
		}
		out = append(out, doc{"artifactId": r["id"], "projectId": r["project_id"], "name": r["name"], "hash": r["content_hash"], "state": st, "approvedAt": r["approved_at"], "createdAt": r["created_at"]})
	}
	return doc{"examples": out}, nil
}

func labExampleGet(q *Request, id string) (doc, error) {
	rs, err := queryRows(q, "SELECT id,approved_at FROM artifact WHERE id=$1 AND role='example'", id)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, fail(404, "Unknown example")
	}
	b, err := artifactBytes(withTx(q), q.app, id, 8<<20)
	if err != nil {
		return nil, err
	}
	var ex doc
	if json.Unmarshal(b, &ex) != nil {
		return nil, fail(500, "Invalid example artifact")
	}
	st := "proposed"
	if rs[0]["approved_at"] != nil {
		st = "approved"
	}
	return doc{"artifactId": id, "state": st, "example": ex}, nil
}

// ---- scores ----

// labShuffle: seeded shuffle (mulberry32 seeded from sha256(seed)), the same order as the TypeScript one.
func labShuffle(items []any, seed string) []any {
	h := sha256.Sum256([]byte(seed))
	a := binary.LittleEndian.Uint32(h[:4])
	rnd := func() float64 {
		a += 0x6D2B79F5
		t := (a ^ a>>15) * (1 | a)
		t = (t + (t^t>>7)*(61|t)) ^ t
		return float64(t^t>>14) / 4294967296
	}
	out := append([]any{}, items...)
	for i := len(out) - 1; i > 0; i-- {
		j := int(math.Floor(rnd() * float64(i+1)))
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func checkValue(form string, v any, trials []any) error {
	ok := false
	switch form {
	case "pass_fail":
		_, ok = v.(bool)
	case "scale":
		n, isInt := intOK(v)
		ok = isInt && n >= 1 && n <= 5
	case "pick_best":
		for _, t := range trials {
			ok = ok || t == v
		}
	case "metric":
		f, isNum := numOK(v)
		ok = isNum && !math.IsInf(f, 0) && !math.IsNaN(f)
	case "typed":
		m := mapOf(v)
		_, isP := numOK(m["p"])
		ok = m != nil && (isP && (m["answer"] == "yes" || m["answer"] == "no" || m["answer"] == "abstain") && (m["label"] == "yes" || m["label"] == "no") || m["answer"] == "unscored" && m["reason"] != nil)
	}
	if !ok {
		return fail(400, "Invalid "+form+" value")
	}
	return nil
}

func parseRating(form, answer string, trials []any) (any, any, error) {
	head, rest, _ := strings.Cut(answer, ":")
	var note any
	if n := strings.TrimSpace(rest); n != "" {
		note = n
	}
	h := strings.TrimSpace(head)
	switch form {
	case "pass_fail":
		if h != "pass" && h != "fail" {
			return nil, nil, fail(400, "A pass_fail answer is pass or fail")
		}
		return h == "pass", note, nil
	case "scale":
		n, err := strconv.ParseFloat(h, 64)
		if err != nil {
			return nil, nil, fail(400, "Invalid scale value")
		}
		return n, note, checkValue("scale", n, nil)
	case "pick_best":
		v := strings.TrimSpace(answer)
		return v, nil, checkValue("pick_best", v, trials)
	}
	return nil, nil, fail(400, "Owner ratings are pass_fail, pick_best or scale")
}

// runChecks: each check (a shell command, exit 0 = pass) in a fresh directory holding the files, FLEET_OUTPUT_DIR set to it.
// Cancelling ctx kills the running check and returns ctx's error: the trial stays unscored, never failed.
func runChecks(ctx context.Context, checks []any, files []labFile) (doc, error) {
	dir, err := os.MkdirTemp("", "fleet-eval-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for _, f := range files {
		p := filepath.Join(dir, f.path)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, f.bytes, 0o644); err != nil {
			return nil, err
		}
	}
	failed := []any{}
	for _, x := range checks {
		c := mapOf(x)
		cmd := osexec.CommandContext(ctx, "sh", "-c", astr(c["run"]))
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "FLEET_OUTPUT_DIR=" + dir}
		// On Unix the check runs in its own process group; cancelling, and finishing, kills the whole group, so a child the shell
		// left behind (`sleep 1000 &`) never outlives the check or its deleted directory.
		ownGroup(cmd)
		err := cmd.Run()
		killGroup(cmd)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			failed = append(failed, c["id"])
		}
	}
	return doc{"passed": len(checks) - len(failed), "total": len(checks), "failed": failed}, nil
}

func scoreView(r doc) doc {
	return doc{"id": r["id"], "experimentId": r["experiment_id"], "trialIds": r["trial_ids"], "evaluator": r["evaluator"], "evaluatorVersion": r["evaluator_version"],
		"form": r["form"], "value": r["value"], "sourceExecutionId": r["source_execution_id"], "questionId": r["question_id"], "rationale": r["rationale"],
		"replacesScoreId": r["replaces_score_id"], "createdAt": r["created_at"]}
}

func scoreAdd(q *Request, s doc) (string, error) {
	trials := anys(s["trialIds"])
	if err := checkValue(astr(s["form"]), s["value"], trials); err != nil {
		return "", err
	}
	ids := []string{}
	for _, t := range trials {
		ids = append(ids, astr(t))
	}
	var rat any
	if r, ok := s["rationale"].(string); ok {
		if len(r) > 4000 {
			r = r[:4000]
		}
		rat = r
	}
	id := newID()
	return id, exec(q, `INSERT INTO score(id,experiment_id,project_id,trial_ids,evaluator,evaluator_version,form,value,source_execution_id,question_id,rationale,replaces_score_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		id, s["experimentId"], s["projectId"], ids, s["evaluator"], s["evaluatorVersion"], s["form"], jsonBytes(s["value"]), s["sourceExecutionId"], s["questionId"], rat, s["replaces"])
}

func scoresCurrent(q *Request, expID string) ([]doc, error) {
	rs, err := queryRows(q, "SELECT * FROM score s WHERE experiment_id=$1 AND NOT EXISTS (SELECT 1 FROM score r WHERE r.replaces_score_id=s.id) ORDER BY seq", expID)
	out := []doc{}
	for _, r := range rs {
		out = append(out, scoreView(r))
	}
	return out, err
}

func trialIDsOf(s doc) []any {
	return anys(s["trialIds"])
}

func firstTrial(s doc) any {
	if t := trialIDsOf(s); len(t) > 0 {
		return t[0]
	}
	return nil
}

func agreementBase(scores []doc) doc {
	compared, matched := 0, 0
	var judges []doc
	for _, s := range scores {
		if strings.HasPrefix(astr(s["evaluator"]), "judge:") {
			judges = append(judges, s)
		}
	}
	judgeOf := func(form string, trial any) doc {
		for _, j := range judges {
			if j["form"] == form && firstTrial(j) == trial {
				return j
			}
		}
		return nil
	}
	for _, o := range scores {
		if !strings.HasPrefix(astr(o["evaluator"]), "human:") {
			continue
		}
		switch o["form"] {
		case "pass_fail":
			if j := judgeOf("pass_fail", firstTrial(o)); j != nil {
				compared++
				if j["value"] == o["value"] {
					matched++
				}
			}
		case "pick_best":
			ts := trialIDsOf(o)
			paired := false
			for _, j := range judges {
				if j["form"] == "pick_best" && sameTrialSet(ts, trialIDsOf(j)) {
					compared++
					if j["value"] == o["value"] {
						matched++
					}
					paired = true
					break
				}
			}
			if paired {
				continue
			}
			vals, ok := []float64{}, true
			for _, t := range ts {
				j := judgeOf("scale", t)
				if j == nil {
					ok = false
					break
				}
				v, _ := numOK(j["value"])
				vals = append(vals, v)
			}
			if !ok {
				continue
			}
			best := math.Inf(-1)
			for _, v := range vals {
				best = math.Max(best, v)
			}
			top := []any{}
			for i, v := range vals {
				if v == best {
					top = append(top, ts[i])
				}
			}
			if len(top) != 1 {
				continue
			}
			compared++
			if top[0] == o["value"] {
				matched++
			}
		}
	}
	var pct any
	if compared > 0 {
		pct = int(math.Round(100 * float64(matched) / float64(compared)))
	}
	return doc{"compared": compared, "matched": matched, "percent": pct}
}

// scoreNumber: a one-trial score as a number (pass is 1, fail 0; metric and scale as given).
func scoreNumber(s doc) float64 {
	if s["form"] == "typed" { // correct confident answer 1; a wrong one or an abstention 0
		m := mapOf(s["value"])
		if (m["answer"] == "yes" || m["answer"] == "no") && m["answer"] == m["label"] {
			return 1
		}
		return 0
	}
	if s["form"] == "pass_fail" {
		if s["value"] == true {
			return 1
		}
		return 0
	}
	v, _ := numOK(s["value"])
	return v
}

func summarize(scores []doc, trials []string) doc {
	in := map[string]bool{}
	for _, t := range trials {
		in[t] = true
	}
	type acc struct {
		form   string
		n      int
		values []float64
	}
	m, order := map[string]*acc{}, []string{}
	for _, s := range scores {
		mine := false
		for _, t := range trialIDsOf(s) {
			mine = mine || in[astr(t)]
		}
		if !mine || s["form"] == "typed" && mapOf(s["value"])["answer"] == "unscored" {
			continue
		}
		k := astr(s["evaluator"])
		e := m[k]
		if e == nil {
			e = &acc{form: astr(s["form"])}
			m[k] = e
			order = append(order, k)
		}
		e.n++
		if s["form"] == "pick_best" {
			if in[astr(s["value"])] {
				e.values = append(e.values, 1)
			}
		} else {
			e.values = append(e.values, scoreNumber(s))
		}
	}
	out := doc{}
	for _, k := range order {
		e := m[k]
		r := doc{"form": e.form, "n": e.n}
		sum := 0.0
		for _, v := range e.values {
			sum += v
		}
		switch e.form {
		case "pick_best":
			r["wins"] = len(e.values)
		case "pass_fail":
			r["passed"] = sum
		default:
			if len(e.values) > 0 {
				lo, hi := math.Inf(1), math.Inf(-1)
				for _, v := range e.values {
					lo, hi = math.Min(lo, v), math.Max(hi, v)
				}
				r["mean"], r["min"], r["max"] = sum/float64(len(e.values)), lo, hi
			}
		}
		out[k] = r
	}
	return out
}

// ---- experiments ----

func evaluatorName(e doc) string { return fmt.Sprintf("%v:%v:%v", e["kind"], e["ref"], e["form"]) }
func cellKey(ex, variant any, repeat any) string {
	r, _ := intOK(repeat)
	return fmt.Sprintf("%v|%v|%d", ex, variant, r)
}

func expRow(q *Request, id string) (doc, error) {
	rs, err := queryRows(q, "SELECT * FROM experiment WHERE id=$1", id)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, fail(404, "Unknown experiment")
	}
	return rs[0], nil
}

func expView(r doc) doc {
	return doc{"id": r["id"], "projectId": r["project_id"], "kind": r["kind"], "pipelineId": r["pipeline_id"], "examples": r["examples"], "variants": r["variants"],
		"repeats": r["repeats"], "evaluators": r["evaluators"], "primaryEvaluator": r["primary_evaluator"], "strategy": r["strategy"], "budgets": r["budgets"], "state": r["state"],
		"stopReason": r["stop_reason"], "createdAt": r["created_at"], "goal": r["goal"], "hypothesis": r["hypothesis"], "decisionRule": r["decision_rule"],
		"labGroup": r["lab_group"], "decision": r["decision"], "decisionReason": r["decision_reason"], "decidedBy": r["decided_by"], "decidedAt": r["decided_at"],
		"launchedAt": r["launched_at"], "requestedBy": r["requested_by"], "requesterRemovedAt": r["requester_removed_at"]}
}

// recordFields: the experiment record fields of a create request, frozen at launch (fleet-lab.md, Experiment record).
var recordFields = []string{"goal", "hypothesis", "decisionRule", "labGroup", "primaryEvaluator"}

var recordColumns = map[string]string{"goal": "goal", "hypothesis": "hypothesis", "decisionRule": "decision_rule", "labGroup": "lab_group", "primaryEvaluator": "primary_evaluator"}

// expLocked: the experiment row, locked for this transaction (a record change reads and writes it once).
func expLocked(q *Request, id string) (doc, error) {
	rs, err := queryRows(q, "SELECT * FROM experiment WHERE id=$1 FOR UPDATE", id)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, fail(404, "Unknown experiment")
	}
	return rs[0], nil
}

// expLaunch: draft to running, refused without a goal and a hypothesis; the record is frozen from here on.
func expLaunch(q *Request, id string) error {
	e, err := expRow(q, id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(astr(e["goal"])) == "" || strings.TrimSpace(astr(e["hypothesis"])) == "" {
		return fail(400, "Launch needs the experiment's goal and hypothesis")
	}
	return expSetState(q, id, []string{"draft"}, "running", nil) // the experiment_record trigger stamps launched_at
}

// expNote: an append-only note, or an amendment that changes one record field and keeps its old and new value.
func expNote(q *Request, b doc) (doc, error) {
	if !keysOnly(b, "experimentId", "kind", "field", "value", "text") {
		return nil, fail(400, "Expected experimentId, kind, field, value, text")
	}
	e, err := expLocked(q, astr(b["experimentId"]))
	if err != nil {
		return nil, err
	}
	text, _ := b["text"].(string)
	if strings.TrimSpace(text) == "" {
		return nil, fail(400, "A note needs text")
	}
	kind, field := astr(b["kind"]), astr(b["field"])
	var old, value any
	switch kind {
	case "note":
		if b["field"] != nil || b["value"] != nil {
			return nil, fail(400, "A note has no field or value")
		}
	case "amendment":
		col, ok := recordColumns[field]
		if !ok {
			return nil, fail(400, "An amendment changes one of "+strings.Join(recordFields, ", "))
		}
		v, isText := b["value"].(string)
		if b["value"] != nil && !isText {
			return nil, fail(400, "value must be text or null")
		}
		if (field == "goal" || field == "hypothesis") && strings.TrimSpace(v) == "" {
			return nil, fail(400, field+" cannot be cleared")
		}
		if field == "primaryEvaluator" && v != "" && !slices.ContainsFunc(anys(e["evaluators"]), func(x any) bool { return evaluatorName(mapOf(x)) == v }) {
			return nil, fail(400, "primaryEvaluator names none of the evaluators")
		}
		old, value = e[col], b["value"]
		if err := exec(q, "UPDATE experiment SET "+col+"=$2 WHERE id=$1", e["id"], nilIf(v)); err != nil {
			return nil, err
		}
	default:
		return nil, fail(400, "kind is amendment or note")
	}
	n := doc{"id": newID(), "experimentId": e["id"], "kind": kind, "field": nilIf(field), "old": old, "new": value, "text": text, "author": actor(q, "operator")}
	if err := exec(q, "INSERT INTO experiment_note(id,experiment_id,project_id,kind,field,old,new,text,author) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)",
		n["id"], e["id"], e["project_id"], kind, n["field"], jsonBytes(old), jsonBytes(value), text, n["author"]); err != nil {
		return nil, err
	}
	// A note changes the experiment as its readers see it: touch the row so its change reaches the feed.
	return n, exec(q, "UPDATE experiment SET id=id WHERE id=$1", e["id"])
}

// expDecide: a person confirms or overrides the decision of a stopped or done experiment; an override needs a reason and
// is an amendment.
func expDecide(q *Request, b doc) (doc, error) {
	if !keysOnly(b, "experimentId", "decision", "reason") {
		return nil, fail(400, "Expected experimentId, decision, reason")
	}
	e, err := expLocked(q, astr(b["experimentId"]))
	if err != nil {
		return nil, err
	}
	if e["state"] != "stopped" && e["state"] != "done" {
		return nil, fail(409, "An experiment is decided once it is stopped or done")
	}
	d, reason := astr(b["decision"]), strings.TrimSpace(astr(b["reason"]))
	label, adopt := strings.CutPrefix(d, "adopt ")
	if adopt && !slices.ContainsFunc(anys(e["variants"]), func(v any) bool { return mapOf(v)["label"] == label }) {
		return nil, fail(400, "Unknown variant "+label)
	}
	if !adopt && d != "keep baseline" && d != "inconclusive" {
		return nil, fail(400, "decision is adopt <variant>, keep baseline or inconclusive")
	}
	prev := astr(e["decision"])
	if prev == d {
		return expGet(q, astr(e["id"])) // already decided so: nothing changes, no second record
	}
	if prev != "" && prev != "pending" && prev != d {
		if reason == "" {
			return nil, fail(400, "Overriding a decision needs a reason")
		}
		if err := exec(q, "INSERT INTO experiment_note(id,experiment_id,project_id,kind,field,old,new,text,author) VALUES($1,$2,$3,'amendment','decision',$4,$5,$6,$7)",
			newID(), e["id"], e["project_id"], jsonBytes(prev), jsonBytes(d), reason, actor(q, "operator")); err != nil {
			return nil, err
		}
	}
	if err := exec(q, "UPDATE experiment SET decision=$2,decision_reason=$3,decided_by=$4,decided_at=now() WHERE id=$1",
		e["id"], d, nilIf(reason), actor(q, "operator")); err != nil {
		return nil, err
	}
	return expGet(q, astr(e["id"]))
}

func expCreate(q *Request, b doc) (doc, error) {
	// Serialize creator validation with the existing people projection/removal transaction.
	if err := exec(q, "SELECT pg_advisory_xact_lock(hashtext($1))", GLOBAL); err != nil {
		return nil, err
	}
	if q.person == nil {
		return nil, fail(403, "Lab creation needs an authenticated requester")
	}
	if err := refreshRequestAccess(q); err != nil {
		return nil, err
	}
	if err := q.app.guardOp(q, "lab_experiment_create", jsonBytes(b)); err != nil {
		return nil, err
	}

	if !keysOnly(b, "kind", "projectId", "pipelineId", "examples", "variants", "repeats", "evaluators", "budgets", "primaryEvaluator", "strategy", "goal", "hypothesis", "decisionRule", "labGroup") {
		return nil, fail(400, "Expected kind, projectId, pipelineId, examples, variants, repeats, evaluators, budgets, primaryEvaluator, strategy, goal, hypothesis, decisionRule, labGroup")
	}
	for _, k := range recordFields {
		if _, ok := b[k].(string); b[k] != nil && !ok {
			return nil, fail(400, k+" must be text")
		}
	}
	kind := astr(b["kind"])
	if kind != "single" && kind != "ab" && kind != "loop" {
		return nil, fail(400, "Invalid kind")
	}
	if b["strategy"] != nil && kind != "loop" {
		return nil, fail(400, "strategy is for loop experiments")
	}
	projectID, err := expID(b["projectId"], "projectId")
	if err != nil {
		return nil, err
	}
	pipelineID, err := expID(b["pipelineId"], "pipelineId")
	if err != nil {
		return nil, err
	}
	if _, err := loadPipeline(q, pipelineID+".json"); err != nil {
		return nil, err
	}
	if !projectKnown(q, projectID) {
		return nil, fail(400, "Unknown project "+projectID)
	}
	exl := anys(b["examples"])
	if len(exl) == 0 {
		return nil, fail(400, "Expected examples (approved example artifact IDs)")
	}
	examples, seen := []any{}, map[string]bool{}
	for _, x := range exl {
		id, err := expID(x, "example artifact")
		if err != nil {
			return nil, err
		}
		ex, err := labExampleGet(q, id)
		if he, ok := err.(*HTTPError); ok && he.Code == 404 {
			return nil, fail(400, "Unknown example "+id)
		} else if err != nil {
			return nil, err
		}
		if ex["state"] != "approved" {
			return nil, fail(409, "Example "+id+" is not approved")
		}
		var hash string
		if err := q.tx.QueryRow(q.ctx(), "SELECT content_hash FROM artifact WHERE id=$1", id).Scan(&hash); err != nil {
			return nil, err
		}
		e := mapOf(ex["example"])
		pin := exampleRef(e["id"], e["version"])
		if seen[pin] {
			return nil, fail(400, "Duplicate example")
		}
		seen[pin] = true
		examples = append(examples, doc{"id": pin, "artifact_id": id, "hash": hash, "split": e["split"]})
	}
	vl := anys(b["variants"])
	if len(vl) == 0 {
		return nil, fail(400, "Expected variants")
	}
	variants, labels := []any{}, map[string]bool{}
	for _, x := range vl {
		v := mapOf(x)
		label, _ := v["label"].(string)
		if !labLabel.MatchString(label) {
			return nil, fail(400, "Invalid variant label")
		}
		ov := v["overrides"]
		if ov == nil {
			ov = doc{}
		}
		o := mapOf(ov)
		if o == nil || !keysOnly(o, labVariantKeys...) {
			return nil, fail(400, "Variant overrides are "+strings.Join(labVariantKeys, ", "))
		}
		if q.person != nil && !fullAccess(q.person) && (o["host"] != nil || o["account"] != nil || o["profile"] != nil) {
			return nil, fail(403, "Only the owner overrides host, account or profile")
		}
		if w, has := o["web_route"]; has && w != "connections" && w != "builtin" {
			return nil, fail(400, "web_route must be connections or builtin")
		}
		if args, has := o["arguments"]; has {
			m := mapOf(args)
			for _, step := range m {
				if mapOf(step) == nil {
					m = nil
				}
			}
			if m == nil {
				return nil, fail(400, "arguments must be {<step id>: {<argument>: value}}")
			}
		}
		if bands, has := o["bands"]; has {
			if _, _, err := bandsOf(bands); err != nil {
				return nil, err
			}
		}
		if labels[label] {
			return nil, fail(400, "Duplicate variant label")
		}
		labels[label] = true
		variantPipeline := pipelineID
		if v["pipelineId"] != nil {
			variantPipeline, err = expID(v["pipelineId"], "variant pipelineId")
			if err != nil {
				return nil, err
			}
			if _, err := loadPipeline(q, variantPipeline+".json"); err != nil {
				return nil, err
			}
		}
		variants = append(variants, doc{"label": label, "pipelineId": variantPipeline, "overrides": o, "config_ref": labConfigDigest(variantPipeline, o)})
	}
	if kind == "single" && len(variants) != 1 {
		return nil, fail(400, "A single experiment has one variant")
	}
	if kind == "ab" && len(variants) < 2 {
		return nil, fail(400, "An ab experiment has two or more variants")
	}
	repeats := int64(1)
	if b["repeats"] != nil {
		r, ok := intOK(b["repeats"])
		if !ok || r < 1 {
			return nil, fail(400, "Invalid repeats")
		}
		repeats = r
	}
	evaluators := []any{}
	names := map[string]bool{}
	for _, x := range anys(b["evaluators"]) {
		e := mapOf(x)
		k, f, ref := astr(e["kind"]), astr(e["form"]), e["ref"]
		switch k {
		case "script":
			if ref != "example.checks" || f != "pass_fail" && f != "metric" {
				return nil, fail(400, "A script evaluator is {kind: script, ref: example.checks, form: pass_fail|metric}")
			}
		case "judge":
			r, err := expID(ref, "judge pipeline")
			if err != nil {
				return nil, err
			}
			if _, err := loadPipeline(q, r+".json"); err != nil {
				return nil, err
			}
			if f != "pass_fail" && f != "scale" && f != "pick_best" {
				return nil, fail(400, "A judge form is pass_fail, scale or pick_best")
			}
		case "typed":
			if ref != "labels" || f != "typed" {
				return nil, fail(400, "A typed evaluator is {kind: typed, ref: labels, form: typed}: the trial's p through the variant's bands against the example's label")
			}
		case "human":
			if ref != "owner" || f != "pass_fail" && f != "pick_best" && f != "scale" {
				return nil, fail(400, "A human evaluator is {kind: human, ref: owner, form: pass_fail|pick_best|scale}")
			}
		default:
			return nil, fail(400, "Evaluator kind is script, typed, judge or human")
		}
		ev := doc{"kind": k, "ref": ref, "form": f}
		if o, has := e["overrides"]; has {
			ov := mapOf(o)
			if k != "judge" || ov == nil || !keysOnly(ov, labOverrides...) {
				return nil, fail(400, "Judge overrides use pipeline override keys")
			}
			if q.person != nil && !fullAccess(q.person) && (ov["host"] != nil || ov["account"] != nil || ov["profile"] != nil) {
				return nil, fail(403, "Only the owner overrides host, account or profile")
			}
			ev["overrides"] = ov
		}
		names[evaluatorName(ev)] = true
		evaluators = append(evaluators, ev)
	}
	if b["primaryEvaluator"] != nil && !names[astr(b["primaryEvaluator"])] {
		return nil, fail(400, "primaryEvaluator names none of the evaluators")
	}
	var strategy any
	if kind == "loop" {
		if astr(b["primaryEvaluator"]) == "" || strings.HasSuffix(astr(b["primaryEvaluator"]), ":pick_best") {
			return nil, fail(400, "Loop primaryEvaluator must name a scalar evaluator")
		}
		st, err := loopStrategy(q, b["strategy"])
		if err != nil {
			return nil, err
		}
		strategy = st
	}
	budgets := []any{}
	for _, x := range anys(b["budgets"]) {
		bu := mapOf(x)
		u := astr(bu["unit"])
		if u != "trials" && u != "wall_ms" && u != "calls" && u != "credits" {
			return nil, fail(400, "Budget unit is trials, wall_ms, calls, credits")
		}
		l, ok := numOK(bu["limit"])
		if !ok || !(l > 0) || math.IsInf(l, 0) {
			return nil, fail(400, "Invalid budget limit")
		}
		act := astr(bu["action"])
		if bu["action"] == nil {
			act = "pause"
		}
		if act != "warn" && act != "pause" && act != "stop" {
			return nil, fail(400, "Budget action is warn, pause or stop")
		}
		budgets = append(budgets, doc{"unit": u, "limit": l, "action": act})
	}
	id := newID()
	if err := exec(q, "INSERT INTO experiment(id,project_id,kind,pipeline_id,examples,variants,repeats,evaluators,primary_evaluator,budgets,goal,hypothesis,decision_rule,lab_group,strategy,requested_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)",
		id, projectID, kind, pipelineID, jsonBytes(examples), jsonBytes(variants), repeats, jsonBytes(evaluators), b["primaryEvaluator"], jsonBytes(budgets),
		b["goal"], b["hypothesis"], b["decisionRule"], b["labGroup"], jsonBytes(strategy), q.person.Email); err != nil {
		return nil, err
	}
	return expGet(q, id)
}

func expSpend(q *Request, id string) (doc, error) {
	var trials, calls, unmeasured int
	var wall, credits float64
	if err := q.tx.QueryRow(q.ctx(), `SELECT count(*) FILTER (WHERE variant IS NOT NULL)::int,
	 coalesce(sum(extract(epoch FROM coalesce(ended_at,now())-started_at)*1000) FILTER (WHERE started_at IS NOT NULL),0)::float8
	 FROM execution WHERE experiment_id=$1 AND kind='pipeline_run'`, id).Scan(&trials, &wall); err != nil {
		return nil, err
	}
	if err := q.tx.QueryRow(q.ctx(), `SELECT count(*)::int,coalesce(sum((t.connection_cost->>'credits')::numeric),0)::float8,count(*) FILTER (WHERE t.connection_cost_basis IS DISTINCT FROM 'measured')::int
	 FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.experiment_id=$1 AND e.kind='tool_run'`, id).Scan(&calls, &credits, &unmeasured); err != nil {
		return nil, err
	}
	basis := "measured"
	if unmeasured > 0 {
		basis = "partial"
	}
	return doc{"trials": doc{"value": trials, "basis": "measured"}, "wall_ms": doc{"value": math.Round(wall), "basis": "measured"}, "calls": doc{"value": calls, "basis": "measured"},
		"credits": doc{"value": credits, "basis": basis, "unmeasured": unmeasured}}, nil
}

func expRuns(q *Request, id string) ([]doc, error) {
	rs, err := queryRows(q, "SELECT id,variant,problem FROM execution WHERE experiment_id=$1 AND "+runRows+" ORDER BY seq", id)
	if err != nil {
		return nil, err
	}
	out := []doc{}
	for _, r := range rs {
		run, err := getRun(q, astr(r["id"]))
		if err != nil {
			return nil, err
		}
		if run != nil {
			run["variant"], run["problem"] = r["variant"], r["problem"]
			out = append(out, run)
		}
	}
	return out, nil
}

func expTrials(runs []doc) []doc {
	out := []doc{}
	for _, r := range runs {
		if r["cell"] != nil {
			out = append(out, r)
		}
	}
	return out
}

// latest attempt per cell, in first-seen order.
func expLatest(trials []doc) ([]string, map[string]doc) {
	order, m := []string{}, map[string]doc{}
	for _, t := range trials {
		c := mapOf(t["cell"])
		k := cellKey(c["example"], c["variant"], c["repeat"])
		o, ok := m[k]
		if !ok {
			order = append(order, k)
		}
		a, _ := numOK(c["attempt"])
		var oa float64
		if ok {
			oa, _ = numOK(mapOf(o["cell"])["attempt"])
		}
		if !ok || a > oa {
			m[k] = t
		}
	}
	return order, m
}

func comparable(t doc) bool { return t["state"] == "succeeded" && !unconfirmed(t) }

func expInsertRun(q *Request, exp doc, requestID, file string, example any, variant doc, extra doc) (string, error) {
	return expInsertRunReuse(q, exp, requestID, file, example, variant, extra, nil)
}

// expInsertRunReuse reserves exactly once; an existing request never reinitializes a run.
func expInsertRunReuse(q *Request, exp doc, requestID, file string, example any, variant doc, extra, reuseOptions doc) (string, error) {
	requester, err := labRequester(q, astr(exp["id"]))
	if err != nil {
		return "", err
	}

	id := newID()
	var label, cfg any
	if variant != nil {
		label, cfg = variant["label"], doc{"config_ref": variant["config_ref"]}
	}
	ok, err := insertExecution(q, doc{"id": id, "kind": "pipeline_run", "state": "preparing", "projectId": exp["project_id"], "experimentId": exp["id"], "variant": label, "requestId": requestID, "configRef": cfg, "data": doc{"recovery": extra["recovery"]}})
	if err != nil || !ok {
		return "", err
	}
	ov := doc{}
	if variant != nil {
		ov = mapOf(variant["overrides"])
	}
	extra["priority"] = "background"
	opts := doc{"id": id, "file": file, "projectId": exp["project_id"], "requestId": requestID, "example": example, "overrides": ov,
		"experimentId": exp["id"], "variant": label, "requestedBy": doc{"kind": "lab", "experimentId": exp["id"], "email": requester.Email}, "extra": extra}
	for k, v := range reuseOptions {
		opts[k] = v
	}
	opts["requestedBy"] = doc{"kind": "lab", "experimentId": exp["id"], "email": requester.Email}
	_, err = pipelineStart(q, opts)
	return id, err
}

// exampleOf: the pinned example, refused when its content changed; run is the example without references.
func exampleOf(q *Request, pin doc) (doc, doc, error) {
	ex, err := labExampleGet(q, astr(pin["artifact_id"]))
	if err != nil {
		return nil, nil, err
	}
	var hash string
	q.tx.QueryRow(q.ctx(), "SELECT content_hash FROM artifact WHERE id=$1", pin["artifact_id"]).Scan(&hash)
	if hash != pin["hash"] {
		return nil, nil, fail(409, fmt.Sprintf("Example %v changed since it was pinned", pin["id"]))
	}
	full := mapOf(ex["example"])
	run := doc{}
	for k, v := range full {
		if k != "references" {
			run[k] = v
		}
	}
	return full, run, nil
}

func expLaunchTrial(q *Request, exp, pin, variant doc, repeat, attempt int64) (string, error) {
	iteration, _ := intOK(variant["iteration"])
	cell := doc{"iteration": iteration, "example": pin["id"], "variant": variant["label"], "repeat": repeat, "attempt": attempt}
	_, run, err := exampleOf(q, pin)
	if err != nil {
		return "", err
	}
	rid := "lab:" + sha256hex(jsonBytes([]any{exp["id"], iteration, pin["id"], variant["label"], repeat, attempt}))
	return expInsertRun(q, exp, rid, variantPipelineID(exp, variant)+".json", run, variant, doc{"cell": cell})
}

type labFile struct {
	path  string
	bytes []byte
}

// trialFiles: example inputs, then each step's files at the top level (later steps win).
func trialFiles(q *Request, trial, example doc) ([]labFile, error) {
	order, m := []string{}, map[string][]byte{}
	set := func(p string, b []byte) {
		if _, ok := m[p]; !ok {
			order = append(order, p)
		}
		m[p] = b
	}
	for _, x := range anys(example["inputs"]) {
		f := mapOf(x)
		b, err := exampleFileBytes(q, trial["projectId"], f)
		if err != nil {
			return nil, err
		}
		set(astr(f["path"]), b)
	}
	outs, err := pipelineOutputs(q, trial, -1)
	if err != nil {
		return nil, err
	}
	for _, o := range outs {
		_, rel, _ := strings.Cut(o.Path, "/")
		if rel == "" {
			continue
		}
		b, err := artifactBytes(withTx(q), q.app, o.ArtifactID, 64<<20)
		if err != nil {
			return nil, err
		}
		set(rel, b)
	}
	out := []labFile{}
	for _, p := range order {
		out = append(out, labFile{p, m[p]})
	}
	return out, nil
}

func findPin(exp doc, id any) doc {
	for _, p := range anys(exp["examples"]) {
		if mapOf(p)["id"] == id {
			return mapOf(p)
		}
	}
	return nil
}

func findVariant(exp doc, label any) doc {
	for _, v := range anys(exp["variants"]) {
		if mapOf(v)["label"] == label {
			return mapOf(v)
		}
	}
	return nil
}

// expEvaluate: automatic evaluators over completed trials; true when every completed trial is evaluated by each.
func expEvaluate(q *Request, exp doc, trials, runs, current []doc, launched *[]string, checked map[string]doc) (bool, error) {
	complete, typedExamples := true, map[string]doc{} // each pinned example read once per pass
	for _, x := range anys(exp["evaluators"]) {
		e := mapOf(x)
		if e["kind"] == "judge" && e["form"] == "pick_best" {
			done, err := pairwiseEvaluate(q, exp, e, trials, runs, current, launched)
			if err != nil {
				return false, err
			}
			complete = complete && done
		}
	}
	for _, t := range trials {
		if !comparable(t) {
			continue
		}
		for _, x := range anys(exp["evaluators"]) {
			e := mapOf(x)
			if e["kind"] == "human" || e["kind"] == "judge" && e["form"] == "pick_best" {
				continue
			}
			name := evaluatorName(e)
			has := false
			for _, s := range current {
				has = has || s["evaluator"] == name && firstTrial(s) == t["id"]
			}
			if has {
				continue
			}
			pin := findPin(exp, mapOf(t["cell"])["example"])
			if e["kind"] == "typed" {
				if err := scoreTyped(q, exp, t, pin, name, typedExamples); err != nil {
					return false, err
				}
				continue
			}
			if e["kind"] == "script" {
				r := checked[astr(t["id"])]
				if r == nil {
					complete = false
					continue
				}
				full, _, err := exampleOf(q, pin)
				if err != nil {
					return false, err
				}
				passed, _ := numOK(r["passed"])
				total, _ := numOK(r["total"])
				failed := anys(r["failed"])
				var value any = len(failed) == 0
				if e["form"] == "metric" {
					value = 0.0
					if total > 0 {
						value = passed / total
					}
				}
				rat := fmt.Sprintf("%v of %v checks pass", passed, total)
				if len(failed) > 0 {
					fs := []string{}
					for _, f := range failed {
						fs = append(fs, astr(f))
					}
					rat = "failed: " + strings.Join(fs, ", ")
				}
				if _, err := scoreAdd(q, doc{"experimentId": exp["id"], "projectId": exp["project_id"], "trialIds": []any{t["id"]}, "evaluator": name,
					"evaluatorVersion": sha256hex(jsonBytes(full["checks"]))[:16], "form": e["form"], "value": value, "rationale": rat}); err != nil {
					return false, err
				}
				continue
			}
			var run doc
			for _, r := range runs {
				j := mapOf(r["judge"])
				if j != nil && j["evaluator"] == name && j["trialId"] == t["id"] {
					run = r
				}
			}
			if run == nil && exp["state"] != "running" {
				complete = false
				continue
			}
			if run == nil {
				full, _, err := exampleOf(q, pin)
				if err != nil {
					return false, err
				}
				files, err := trialFiles(q, t, full)
				if err != nil {
					return false, err
				}
				inputs := []any{}
				for _, f := range files {
					// Judges inspect renders and measurements, never the source GLB.
					if strings.HasSuffix(strings.ToLower(f.path), ".glb") {
						continue
					}
					id, err := saveArtifact(q, t, astr(t["id"]), "judge/"+f.path, f.bytes, http.DetectContentType(f.bytes))
					if err != nil {
						return false, err
					}
					inputs = append(inputs, doc{"artifactId": id, "path": "example/trial/" + f.path})
				}
				jid, err := expInsertRun(q, exp, "lab:"+sha256hex(jsonBytes([]any{exp["id"], "judge", name, t["id"]})), astr(e["ref"])+".json",
					doc{"prompt": full["prompt"], "form": e["form"]}, doc{"overrides": e["overrides"], "config_ref": sha256hex(jsonBytes(e["overrides"]))}, doc{"exampleInputs": inputs, "judge": doc{"evaluator": name, "trialId": t["id"]}})
				if err != nil {
					return false, err
				}
				if jid != "" {
					*launched = append(*launched, jid)
				}
				complete = false
				continue
			}
			if !terminal(run) {
				complete = false
				continue
			}
			if run["state"] != "succeeded" {
				continue // a failed judge run counts as evaluated; it has no score
			}
			steps := anys(run["steps"])
			var last doc
			if len(steps) > 0 {
				last = mapOf(mapOf(steps[len(steps)-1])["output"])
			}
			if checkValue(astr(e["form"]), last["value"], nil) != nil {
				continue
			}
			ver := "unknown"
			if d := astr(run["definitionSha256"]); len(d) >= 16 {
				ver = sha256hex(jsonBytes([]any{d, e["overrides"]}))[:16]
			}
			if _, err := scoreAdd(q, doc{"experimentId": exp["id"], "projectId": exp["project_id"], "trialIds": []any{t["id"]}, "evaluator": name, "evaluatorVersion": ver,
				"form": e["form"], "value": last["value"], "sourceExecutionId": run["id"], "rationale": last["rationale"]}); err != nil {
				return false, err
			}
		}
	}
	return complete, nil
}

var expLocks sync.Map

func expLock(id string) *sync.Mutex {
	m, _ := expLocks.LoadOrStore(id, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// labReconcile moves one experiment, serial per experiment.
func labReconcile(ctx context.Context, a *App, id string) error {
	m := expLock(id)
	m.Lock()
	defer m.Unlock()
	launched := []string{}
	// Script checks run after the read snapshot closed and before the write transaction, so a slow check holds neither.
	type pending struct {
		checks []any
		files  []labFile
	}
	todo := map[string]pending{}
	if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		exp, err := expRow(q, id)
		if err != nil || exp["state"] == "draft" {
			return err
		}
		hasScript := false
		for _, e := range anys(exp["evaluators"]) {
			hasScript = hasScript || mapOf(e)["kind"] == "script"
		}
		if !hasScript {
			return nil
		}
		cur, err := scoresCurrent(q, id)
		if err != nil {
			return err
		}
		runs, err := expRuns(q, id)
		if err != nil {
			return err
		}
		for _, t := range expTrials(runs) {
			if !comparable(t) {
				continue
			}
			done := false
			for _, s := range cur {
				done = done || strings.HasPrefix(astr(s["evaluator"]), "script:") && firstTrial(s) == t["id"]
			}
			if done {
				continue
			}
			full, _, err := exampleOf(q, findPin(exp, mapOf(t["cell"])["example"]))
			if err != nil {
				return err
			}
			files, err := trialFiles(q, t, full)
			if err != nil {
				return err
			}
			// Script checks only (never a judge or an agent): the known-good result under references/, for comparison.
			for _, x := range anys(full["references"]) {
				f := mapOf(x)
				b, err := exampleFileBytes(q, t["projectId"], f)
				if err != nil {
					return err
				}
				files = append(files, labFile{"references/" + astr(f["path"]), b})
			}
			todo[astr(t["id"])] = pending{anys(full["checks"]), files}
		}
		return nil
	}); err != nil {
		return err
	}
	checked := map[string]doc{}
	for id, p := range todo {
		r, err := runChecks(ctx, p.checks, p.files)
		if err != nil {
			return err
		}
		checked[id] = r
	}
	err := runTx(ctx, a, nil, true, "experiment:"+id, func(q *Request) error {
		exp, err := expRow(q, id)
		if err != nil || exp["state"] == "draft" {
			return err
		}
		runs, err := expRuns(q, id)
		if err != nil {
			return err
		}
		trials := expTrials(runs)
		cur, err := scoresCurrent(q, id)
		if err != nil {
			return err
		}
		if exp["state"] != "running" {
			_, err := expEvaluate(q, exp, trials, runs, cur, &launched, checked)
			return err
		}
		if _, err := labRequester(q, id); err != nil {
			var reason *codedError
			if errors.As(err, &reason) && strings.HasPrefix(reason.code, "requester:") {
				state := "stopped"
				if reason.code == "requester:access-revoked" {
					state = "paused"
				}
				return exec(q, "UPDATE experiment SET state=$2,stop_reason=$3 WHERE id=$1", id, state, reason.code)
			}
			return err
		}
		if strings.HasPrefix(astr(exp["stop_reason"]), "requester:") {
			if err := exec(q, "UPDATE experiment SET stop_reason=NULL WHERE id=$1", id); err != nil {
				return err
			}
		}
		order, last := expLatest(trials)
		repeats, _ := intOK(exp["repeats"])
		total := len(anys(exp["examples"])) * len(anys(exp["variants"])) * int(repeats)
		missing := total - len(order)
		s, err := expSpend(q, id)
		if err != nil {
			return err
		}
		budgets := []any{}
		if missing > 0 || exp["kind"] == "loop" {
			budgets = anys(exp["budgets"])
		}
		state, changed := "running", false
		var reason any
		reached := func(b doc) bool {
			v, _ := numOK(mapOf(s[astr(b["unit"])])["value"])
			l, _ := numOK(b["limit"])
			return b["firedAt"] == nil && v >= l
		}
		for _, x := range budgets {
			b := mapOf(x)
			if b["action"] == "stop" && reached(b) {
				b["firedAt"], changed, state, reason = nowMs(), true, "stopped", "budget:"+astr(b["unit"])
				break
			}
		}
		if state == "running" {
			for _, x := range budgets {
				b := mapOf(x)
				if b["action"] == "stop" || !reached(b) {
					continue
				}
				b["firedAt"], changed = nowMs(), true
				if b["action"] == "pause" {
					state = "paused"
					v := mapOf(s[astr(b["unit"])])["value"]
					if _, err := askRow(q, "operator_decision", fmt.Sprintf("Lab budget reached: %v %v of %v. Running trials finish; resume launches or stop the experiment?", b["unit"], v, b["limit"]),
						exp["project_id"], []any{"resume", "stop"}, "lab", doc{"experimentId": id, "budget": b["unit"]}); err != nil {
						return err
					}
					break
				}
			}
		}
		if changed || state != "running" {
			if err := exec(q, "UPDATE experiment SET budgets=$2,state=$3,stop_reason=coalesce($4,stop_reason) WHERE id=$1", id, jsonBytes(budgets), state, reason); err != nil {
				return err
			}
		}
		evalExp := mapOf(clone(exp))
		evalExp["state"] = state
		evaluated, err := expEvaluate(q, evalExp, trials, runs, cur, &launched, checked)
		if err != nil {
			return err
		}
		if state != "running" {
			return nil
		}
		// A trials budget that has not fired caps this pass's launches, so it is reached exactly.
		room := math.Inf(1)
		tv, _ := numOK(mapOf(s["trials"])["value"])
		for _, x := range budgets {
			b := mapOf(x)
			if b["unit"] == "trials" && b["action"] != "warn" && b["firedAt"] == nil {
				l, _ := numOK(b["limit"])
				room = math.Min(room, l-tv)
			}
		}
	launch:
		for _, p := range anys(exp["examples"]) {
			for _, v := range anys(exp["variants"]) {
				for r := int64(0); r < repeats; r++ {
					if _, ok := last[cellKey(mapOf(p)["id"], mapOf(v)["label"], r)]; ok {
						continue
					}
					if room <= 0 {
						break launch
					}
					rid, err := expLaunchTrial(q, exp, mapOf(p), mapOf(v), r, 0)
					if err != nil {
						return err
					}
					if rid != "" {
						launched = append(launched, rid)
						room--
					}
				}
			}
		}
		allFinal := true
		for _, k := range order {
			allFinal = allFinal && terminal(last[k])
		}
		if len(launched) == 0 && len(order) == total && allFinal && evaluated {
			if exp["kind"] == "loop" {
				// Evaluators may have just inserted the final scores in this transaction.
				fresh, err := scoresCurrent(q, id)
				if err != nil {
					return err
				}
				return loopAdvance(q, exp, trials, runs, fresh, s, &launched)
			}
			return exec(q, "UPDATE experiment SET state='done',stop_reason='complete' WHERE id=$1 AND state='running'", id)
		}
		return nil
	})
	for _, r := range launched {
		pipelineAdvance(a, r)
	}
	return err
}

func labReconcileWhere(ctx context.Context, a *App, states string, skipBusy bool) {
	var ids []string
	runTx(ctx, a, nil, false, "", func(q *Request) error {
		rs, err := queryRows(q, "SELECT id FROM experiment WHERE state = ANY(string_to_array($1, ','))", states)
		for _, r := range rs {
			ids = append(ids, astr(r["id"]))
		}
		return err
	})
	for _, id := range ids {
		if skipBusy {
			m := expLock(id)
			if !m.TryLock() {
				continue
			}
			m.Unlock()
		}
		if err := labReconcile(ctx, a, id); err != nil {
			log.Printf("lab reconcile %s: %v", id, err)
		}
	}
}

func labReconcileRunning(ctx context.Context, a *App) { labReconcileWhere(ctx, a, "running", true) }

func expSetState(q *Request, id string, from []string, to string, reason any) error {
	rs, err := queryRows(q, "UPDATE experiment SET state=$2,stop_reason=coalesce($3,stop_reason) WHERE id=$1 AND state=ANY($4) RETURNING id", id, to, reason, from)
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		e, err := expRow(q, id)
		if err != nil {
			return err
		}
		return fail(409, fmt.Sprintf("Experiment is %v", e["state"]))
	}
	// A budget question answered by the buttons instead is withdrawn, so a late answer never moves a later pause.
	return exec(q, "UPDATE question SET state='withdrawn' WHERE state='open' AND kind='operator_decision' AND data->>'experimentId'=$1", id)
}

func labReadTx(q *Request, fn func(q *Request) error) error {
	return q.app.store.Tx(q.ctx(), scopeOf(q.person), false, "", func(tx pgx.Tx) error {
		q.tx = tx
		defer func() { q.tx = nil }()
		return fn(q)
	})
}

func expGetTx(q *Request, id string) (out doc, err error) {
	err = labReadTx(q, func(q *Request) error { out, err = expGet(q, id); return err })
	return
}

func expAction(q *Request, id, action string) (doc, error) {
	var err error
	switch action {
	case "launch":
		err = runTx(q.ctx(), q.app, q, true, "", func(q *Request) error { return expLaunch(q, id) })
	case "stop":
		err = runTx(q.ctx(), q.app, q, true, "", func(q *Request) error { return expSetState(q, id, []string{"running", "paused"}, "stopped", "owner") })
	case "resume":
		err = runTx(q.ctx(), q.app, q, true, "", func(q *Request) error { return expSetState(q, id, []string{"paused"}, "running", nil) })
	default:
		return nil, fail(400, "action is launch, stop or resume")
	}
	if err != nil {
		return nil, err
	}
	if err := labReconcile(q.ctx(), q.app, id); err != nil {
		return nil, err
	}
	out, err := expGetTx(q, id)
	if err != nil || action != "launch" {
		return out, err
	}
	// Drift warns on launch, never refuses (registry source_status of the pinned examples' use cases).
	ucs := map[string]bool{}
	if err := labReadTx(q, func(q *Request) error {
		for _, p := range anys(out["examples"]) {
			ex, err := labExampleGet(q, astr(mapOf(p)["artifact_id"]))
			if err != nil {
				return err
			}
			ucs[astr(mapOf(ex["example"])["use_case_id"])] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	warnings := []any{}
	reg, _ := useCases()
	for _, u := range reg {
		if ucs[astr(u["id"])] && u["source_status"] != "current" {
			warnings = append(warnings, fmt.Sprintf("%v: source %v", u["id"], u["source_status"]))
		}
	}
	out["warnings"] = warnings
	return out, nil
}

func expCellAction(q *Request, id, cell, action string) (doc, error) {
	var exp, t doc
	if err := labReadTx(q, func(q *Request) error {
		var err error
		if exp, err = expRow(q, id); err != nil {
			return err
		}
		runs, err := expRuns(q, id)
		if err != nil {
			return err
		}
		_, last := expLatest(expTrials(runs))
		t = last[cell]
		return nil
	}); err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fail(404, "Unknown cell")
	}
	if action == "cancel" {
		if terminal(t) {
			return nil, fail(409, "Trial already finished")
		}
		own, err := pipelineCancel(q.ctx(), q.app, astr(t["id"]))
		if err != nil {
			return nil, err
		}
		if own != "" {
			if err := stopExecution(q.ctx(), q.app, own); err != nil && !isNotFound(err) { // the run is cancelled already
				return nil, err
			}
		}
	} else {
		if !terminal(t) {
			return nil, fail(409, "Trial still running; cancel it first")
		}
		if exp["state"] == "draft" || exp["state"] == "stopped" {
			return nil, fail(409, fmt.Sprintf("Experiment is %v", exp["state"]))
		}
		c := mapOf(t["cell"])
		var rid string
		if err := runTx(q.ctx(), q.app, q, true, "experiment:"+id, func(q *Request) error {
			runs, err := expRuns(q, id)
			if err != nil {
				return err
			}
			_, latest := expLatest(expTrials(runs))
			if latest[cell] == nil || latest[cell]["id"] != t["id"] {
				return fail(409, "Cell source changed; read it again")
			}
			if err := exec(q, "UPDATE experiment SET state='running' WHERE id=$1 AND state='done'", id); err != nil {
				return err
			}
			rep, _ := intOK(c["repeat"])
			att, _ := intOK(c["attempt"])
			rid, err = expLaunchTrial(q, exp, findPin(exp, c["example"]), findVariant(exp, c["variant"]), rep, att+1)
			return err
		}); err != nil {
			return nil, err
		}
		if rid != "" {
			pipelineAdvance(q.app, rid)
		}
	}
	if err := labReconcile(q.ctx(), q.app, id); err != nil {
		return nil, err
	}
	return expGetTx(q, id)
}

var methodSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Promote: the variant as a catalogue method, saved as a catalogue revision (source lab) unless a method with its
// config_ref exists; then the project recommendation is set to it (recommend: the recommendation_set operation).
func expPromote(q *Request, id, label string) (doc, error) {
	exp, err := expRow(q, id)
	if err != nil {
		return nil, err
	}
	v := findVariant(exp, label)
	if v == nil {
		return nil, fail(400, "Unknown variant")
	}
	identity := firstString(v["catalogueConfigRef"], v["config_ref"])
	slug := strings.Trim(methodSlug.ReplaceAllString(strings.ToLower(label), "-"), "-")
	// Bands steer the Lab's typed evaluator; a method run applies no overrides of that kind, so they are left out and named.
	overrides, note := doc{}, any(nil)
	for k, x := range mapOf(v["overrides"]) {
		if k == "bands" {
			note = fmt.Sprintf("bands %s are a Lab scoring setting and do not travel with the method", jsonBytes(x))
			continue
		}
		overrides[k] = x
	}
	method := doc{"id": fmt.Sprintf("%v-%s", variantPipelineID(exp, v), slug), "name": fmt.Sprintf("%v %s", variantPipelineID(exp, v), label), "scope": exp["project_id"],
		"pipeline": variantPipelineID(exp, v) + ".json", "overrides": overrides, "configRef": identity, "sourceExperiment": id}
	// A method's config ref is its configRef, else the hash Lab gives a variant (pipeline id and overrides).
	refOf := func(m doc) string {
		if r := astr(m["configRef"]); r != "" {
			return r
		}
		o := mapOf(m["overrides"])
		if o == nil {
			o = doc{}
		}
		return sha256hex([]byte(canonical(doc{"pipeline": strings.TrimSuffix(astr(m["pipeline"]), ".json"), "overrides": o})))
	}
	var existing doc
	c, err := catalogueBase(q, astr(exp["project_id"]))
	if err != nil {
		return nil, err
	}
	for _, m := range anys(c["methods"]) {
		if refOf(mapOf(m)) == identity && labConfigDigest(strings.TrimSuffix(astr(mapOf(m)["pipeline"]), ".json"), mapOf(mapOf(m)["overrides"])) == labConfigDigest(variantPipelineID(exp, v), overrides) {
			existing = mapOf(m)
		}
	}
	// A new method is saved as a catalogue revision (source lab) in this transaction, so the recommendation below sees it
	// (fleet-definition-editors.md, Promote); the repository file stays the seed.
	var ref any
	if existing == nil {
		head, err := headOf(q, "catalogue", "catalogue", true)
		if err != nil {
			return nil, err
		}
		if head == nil {
			return nil, fail(404, "No catalogue definition")
		}
		if st := head.state(); st == contract.DefinitionStateRepositoryChanged || st == contract.DefinitionStateRepositoryDeleted {
			return nil, &HTTPError{Code: 409, Msg: "The catalogue's repository file changed: resolve it in the Catalogue editor, then promote", Extra: map[string]any{"reason": contract.ConflictReasonRepositoryChanged, "head": head.Head, "repoSha": head.RepoSha}}
		}
		c, _ := decodeNumbers(string(head.Content)).(map[string]any) // numbers keep their text
		if c == nil {
			return nil, fail(500, "Invalid catalogue definition")
		}
		ids := map[string]bool{}
		for _, m := range anys(c["methods"]) {
			ids[astr(mapOf(m)["id"])] = true
		}
		for base, n := astr(method["id"]), 2; ids[astr(method["id"])]; n++ { // an id already taken gets a number
			method["id"] = fmt.Sprintf("%s-%d", base, n)
		}
		c["methods"] = append(anys(c["methods"]), method)
		text, err := canonicalText(jsonBytes(c))
		if err != nil {
			return nil, err
		}
		if ps := problems("catalogue", "catalogue", text); len(ps) > 0 {
			return nil, fail(400, "The promoted catalogue is invalid: "+ps[0].Message)
		}
		rev, err := appendRevision(q, "catalogue", "catalogue", head.Head, &text, head.RepoSha, "lab", fmt.Sprintf("Promote %s from %s", label, id))
		if err != nil {
			return nil, err
		}
		ref = contract.DefinitionRef{Kind: contract.DefinitionKindCatalogue, Id: "catalogue", Revision: rev.Revision, ContentSha: rev.ContentSha}
		existing = method
	}
	rec, err := recommend(q, astr(exp["project_id"]), astr(existing["id"]))
	if err != nil {
		return nil, err
	}
	return doc{"cataloguePatch": nil, "method": existing, "catalogue": ref, "recommendation": rec, "note": note}, nil
}

func expRate(q *Request, b doc) (doc, error) {
	eid, err := expID(b["experimentId"], "experimentId")
	if err != nil {
		return nil, err
	}
	exp, err := expRow(q, eid)
	if err != nil {
		return nil, err
	}
	form := astr(b["form"])
	if form != "pass_fail" && form != "pick_best" && form != "scale" {
		return nil, fail(400, "form is pass_fail, pick_best or scale")
	}
	l, ok := b["trialIds"].([]any)
	if !ok {
		return nil, fail(400, "Expected trialIds")
	}
	ids := []any{}
	for _, x := range l {
		s, err := expID(x, "trial")
		if err != nil {
			return nil, err
		}
		ids = append(ids, s)
	}
	if form == "pick_best" && len(ids) < 2 {
		return nil, fail(400, "pick_best rates two or more trials")
	}
	if form != "pick_best" && len(ids) != 1 {
		return nil, fail(400, form+" rates one trial")
	}
	runs, err := expRuns(q, eid)
	if err != nil {
		return nil, err
	}
	trials := expTrials(runs)
	for _, t := range ids {
		var x doc
		for _, y := range trials {
			if y["id"] == t {
				x = y
			}
		}
		if x == nil {
			return nil, fail(400, fmt.Sprint("Unknown trial ", t))
		}
		if !comparable(x) {
			return nil, fail(409, fmt.Sprint("Trial ", t, " has no comparable result"))
		}
	}
	text := "Rate this output from 1 to 5"
	if form == "pick_best" {
		text = fmt.Sprintf("Pick the best of %d outputs", len(ids))
	} else if form == "pass_fail" {
		text = "Pass or fail this output"
	}
	qid, err := askRow(q, "rating", text, exp["project_id"], doc{"form": form, "experiment_id": eid, "trial_ids": ids, "seed": newID()}, "operator", doc{"experimentId": eid})
	if err != nil {
		return nil, err
	}
	return labRating(q, qid)
}

// labRating: outputs in the seeded order, labelled A, B, ...; never the variant.
func labRating(q *Request, qid string) (doc, error) {
	rs, err := queryRows(q, "SELECT * FROM question WHERE id=$1", qid)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, fail(404, "Unknown question")
	}
	r := rs[0]
	o := mapOf(r["options"])
	var runs []doc
	if eid := astr(o["experiment_id"]); eid != "" {
		if runs, err = expRuns(q, eid); err != nil {
			return nil, err
		}
	}
	items := []any{}
	for i, tid := range labShuffle(anys(o["trial_ids"]), astr(o["seed"])) {
		files := []any{}
		for _, t := range runs {
			if t["id"] == tid {
				fs, err := pipelineOutputs(q, t, -1)
				if err != nil {
					return nil, err
				}
				for _, f := range fs {
					files = append(files, f.doc())
				}
			}
		}
		items = append(items, doc{"label": string(rune('A' + i)), "trialId": tid, "files": files})
	}
	return doc{"id": r["id"], "kind": r["kind"], "form": o["form"], "experimentId": o["experiment_id"], "exampleArtifactId": o["example_artifact_id"],
		"state": r["state"], "answer": r["answer"], "text": r["text"], "items": items, "withdrawal": mapOf(r["data"])["withdrawal"]}, nil
}

func labRatings(q *Request, experimentID, state any) (doc, error) {
	rs, err := queryRows(q, "SELECT id FROM question WHERE kind='rating' AND ($1::text IS NULL OR options->>'experiment_id'=$1) AND ($2::text IS NULL OR state::text=$2) ORDER BY seq", experimentID, state)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, r := range rs {
		x, err := labRating(q, astr(r["id"]))
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return doc{"ratings": out}, nil
}

// labAnswerCheck and labAnswerAfter: the answer hook for questions_answer (questions_answer calls both inside its transaction:
// check before recording the answer, after once it is recorded). question: the question row (snake_case columns).
func labAnswerCheck(q *Request, question doc, answer string) error {
	o := mapOf(question["options"])
	d := mapOf(question["data"])
	// Lab ratings are the owner's (scored as human:owner; a pass on a drafted example lets the coordinator run its checks)
	// and budget asks move the owner's spend: owner only.
	lab := question["kind"] == "rating" && (o["example_artifact_id"] != nil || o["experiment_id"] != nil) || d["experimentId"] != nil && d["budget"] != nil
	if lab && q.person != nil && !fullAccess(q.person) {
		return fail(403, "Only the owner answers Lab ratings and budget questions")
	}
	if question["kind"] == "rating" {
		_, _, err := parseRating(astr(o["form"]), answer, anys(o["trial_ids"]))
		return err
	}
	if d["experimentId"] != nil && d["budget"] != nil && answer != "resume" && answer != "stop" {
		return fail(400, "Answer resume or stop")
	}
	return nil
}

func labAnswerAfter(q *Request, question doc) error {
	o, d := mapOf(question["options"]), mapOf(question["data"])
	answer := astr(question["answer"])
	eid := ""
	switch {
	case question["kind"] == "rating" && o["experiment_id"] != nil:
		v, rat, err := parseRating(astr(o["form"]), answer, anys(o["trial_ids"]))
		if err != nil {
			return err
		}
		exp, err := expRow(q, astr(o["experiment_id"]))
		if err != nil {
			return err
		}
		if _, err := scoreAdd(q, doc{"experimentId": exp["id"], "projectId": exp["project_id"], "trialIds": o["trial_ids"], "evaluator": "human:owner:" + astr(o["form"]),
			"evaluatorVersion": "1", "form": o["form"], "value": v, "questionId": question["id"], "rationale": rat}); err != nil {
			return err
		}
		eid = astr(exp["id"])
	case question["kind"] == "rating" && o["example_artifact_id"] != nil:
		if v, _, err := parseRating("pass_fail", answer, nil); err == nil && v == true {
			return exec(q, "UPDATE artifact SET approved_at=now() WHERE id=$1 AND role='example' AND approved_at IS NULL", o["example_artifact_id"])
		}
		return nil
	case d["experimentId"] != nil && d["budget"] != nil:
		to := "stopped"
		if answer == "resume" {
			to = "running"
		}
		if err := exec(q, "UPDATE experiment SET state=$2,stop_reason=CASE WHEN $2='stopped' THEN 'owner' ELSE stop_reason END WHERE id=$1 AND state='paused'", d["experimentId"], to); err != nil {
			return err
		}
		eid = astr(d["experimentId"])
	default:
		return nil
	}
	a := q.app
	go func() {
		time.Sleep(50 * time.Millisecond) // after the answer committed
		labReconcile(context.Background(), a, eid)
	}()
	return nil
}

// importDrafts: each examples/*.json file of a completed draft-examples run becomes a proposed example with one pass_fail
// rating question; the owner's pass approves it. A file that is not a valid example is skipped.
func importDrafts(ctx context.Context, a *App, run doc) {
	var files []fileRef
	runTx(ctx, a, nil, false, "", func(q *Request) (err error) { files, err = pipelineOutputs(q, run, -1); return })
	for _, f := range files {
		if !draftExample.MatchString(f.Path) {
			continue
		}
		b, err := artifactBytes(ctx, a, f.ArtifactID, 64<<20)
		if err != nil {
			continue
		}
		var ex any
		if json.Unmarshal(b, &ex) != nil {
			continue
		}
		runTx(ctx, a, nil, true, "", func(q *Request) error {
			res, err := labExamplesAdd(q, doc{"examples": []any{ex}, "projectId": run["projectId"], "approved": false})
			if err != nil {
				return nil
			}
			st := mapOf(anys(res["examples"])[0])
			aid := astr(st["artifactId"])
			var n int
			if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM question WHERE kind='rating' AND options->>'example_artifact_id'=$1", aid).Scan(&n); err != nil || n > 0 {
				return err
			}
			got, err := labExampleGet(q, aid)
			if err != nil {
				return err
			}
			lines := []string{}
			for _, c := range anys(mapOf(got["example"])["checks"]) {
				lines = append(lines, fmt.Sprintf("%v: %v", mapOf(c)["id"], mapOf(c)["run"]))
			}
			_, err = askRow(q, "rating", fmt.Sprintf("Approve drafted example %s? Approving lets the coordinator run its checks:\n%s", exampleRef(st["id"], st["version"]), strings.Join(lines, "\n")),
				run["projectId"], doc{"form": "pass_fail", "example_artifact_id": aid, "seed": aid}, "lab", doc{"exampleArtifactId": aid})
			return err
		})
	}
}

func expGet(q *Request, id string) (doc, error) {
	exp, err := expRow(q, id)
	if err != nil {
		return nil, err
	}
	runs, err := expRuns(q, id)
	if err != nil {
		return nil, err
	}
	trials := expTrials(runs)
	cur, err := scoresCurrent(q, id)
	if err != nil {
		return nil, err
	}
	s, err := expSpend(q, id)
	if err != nil {
		return nil, err
	}
	stepRows, err := queryRows(q, `SELECT data->>'pipelineRunId' AS run,host_id,harness,harness_version,model FROM execution WHERE experiment_id=$1 AND data ? 'pipelineStepId' AND data ? 'agentConfig'`, id)
	if err != nil {
		return nil, err
	}
	credits, err := queryRows(q, `SELECT coalesce(e.data->>'pipelineRunId',split_part(e.parent_execution_id,':step:',1)) AS run,count(*)::int AS calls,coalesce(sum((t.connection_cost->>'credits')::numeric),0)::float8 AS credits,
	 count(*) FILTER (WHERE t.connection_cost_basis IS DISTINCT FROM 'measured')::int AS unmeasured FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.experiment_id=$1 AND e.kind='tool_run' GROUP BY 1`, id)
	if err != nil {
		return nil, err
	}
	// Each trial's provider usage, priced per call (fleet-analytics.md: usage x the latest entry in force at the call).
	usageRows, err := queryRows(q, `SELECT coalesce(e.data->>'pipelineRunId',split_part(e.parent_execution_id,':step:',1)) AS run,
	 coalesce(e.data->>'connection','')||'/'||coalesce(e.data->>'tool','') AS subject,t.connection_cost->>'unit' AS unit,
	 coalesce(t.connection_cost->>'amount',t.connection_cost->>(t.connection_cost->>'unit'))::float8 AS amount,
	 coalesce(to_timestamp((e.data->>'startedAt')::bigint/1000.0),e.started_at,now()) AS at
	 FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.experiment_id=$1 AND e.kind='tool_run'`, id)
	if err != nil {
		return nil, err
	}
	costs := map[any][]usage{}
	for _, u := range usageRows {
		var amount *float64
		if a, ok := numOK(u["amount"]); ok {
			amount = &a
		}
		at, _ := u["at"].(time.Time)
		costs[u["run"]] = append(costs[u["run"]], usage{astr(u["subject"]), astr(u["unit"]), amount, at})
	}
	prices, err := priceEntries(q)
	if err != nil {
		return nil, err
	}
	trialCost := map[any][]contract.CostFigure{}
	for run, us := range costs {
		trialCost[run] = estimateCost(prices, us)
	}
	uniq := func(l []any) []any {
		seen, out := map[string]bool{}, []any{}
		for _, x := range l {
			if s := astr(x); s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		return out
	}
	trialView := func(t doc) doc {
		hosts, harnesses, models := []any{}, []any{}, []any{}
		for _, r := range stepRows {
			if r["run"] != t["id"] {
				continue
			}
			hosts, models = append(hosts, r["host_id"]), append(models, r["model"])
			h := strings.TrimSpace(strings.Join([]string{astr(r["harness"]), astr(r["harness_version"])}, " "))
			harnesses = append(harnesses, h)
		}
		spend := doc{"calls": 0, "credits": 0, "basis": "measured"}
		for _, c := range credits {
			if c["run"] == t["id"] {
				spend["calls"], spend["credits"] = c["calls"], c["credits"]
				if n, _ := numOK(c["unmeasured"]); n > 0 {
					spend["basis"] = "partial"
				}
			}
		}
		web := mapOf(t["overrides"])["web_route"]
		if web == nil {
			web = "connections"
		}
		mine := []any{}
		for _, sc := range cur {
			for _, x := range trialIDsOf(sc) {
				if x == t["id"] {
					mine = append(mine, sc)
					break
				}
			}
		}
		v := doc{"id": t["id"], "cell": t["cell"], "state": t["state"], "observation": t["observation"], "problem": t["problem"], "comparable": comparable(t), "startedAt": t["startedAt"], "finishedAt": t["finishedAt"],
			"durationMs": t["durationMs"], "spend": spend, "cost": append([]contract.CostFigure{}, trialCost[t["id"]]...), "confounders": doc{"hosts": uniq(hosts), "harnesses": uniq(harnesses), "models": uniq(models), "webRoute": web}, "scores": mine}
		if recovery := mapOf(t["recovery"]); recovery != nil {
			v["recovery"] = doc{"sourceRunId": mapOf(recovery["identity"])["sourceRunId"], "reusedStepIds": recovery["reusedStepIds"]}
		}
		return v
	}
	order, last := expLatest(trials)
	idsOf := func(match func(c doc) bool, onlyComparable bool) []string {
		out := []string{}
		for _, k := range order {
			t := last[k]
			if match(mapOf(t["cell"])) && (!onlyComparable || comparable(t)) {
				out = append(out, astr(t["id"]))
			}
		}
		return out
	}
	cells := []any{}
	for _, px := range anys(exp["examples"]) {
		p := mapOf(px)
		for _, vx := range anys(exp["variants"]) {
			v := mapOf(vx)
			ts := []doc{}
			for _, k := range order {
				c := mapOf(last[k]["cell"])
				if c["example"] == p["id"] && c["variant"] == v["label"] {
					ts = append(ts, last[k])
				}
			}
			sort.SliceStable(ts, func(i, j int) bool {
				a, _ := numOK(mapOf(ts[i]["cell"])["repeat"])
				b, _ := numOK(mapOf(ts[j]["cell"])["repeat"])
				return a < b
			})
			keys, comp := []any{}, []string{}
			for _, t := range ts {
				keys = append(keys, cellKey(p["id"], v["label"], mapOf(t["cell"])["repeat"]))
				if comparable(t) {
					comp = append(comp, astr(t["id"]))
				}
			}
			cells = append(cells, doc{"example": p["id"], "variant": v["label"], "trials": keys, "summary": summarize(cur, comp)})
		}
	}
	variants := []any{}
	for _, vx := range anys(exp["variants"]) {
		v := mapOf(clone(vx))
		v["summary"] = summarize(cur, idsOf(func(c doc) bool { return c["variant"] == v["label"] }, true))
		variants = append(variants, v)
	}
	tv := []any{}
	for _, t := range trials {
		tv = append(tv, trialView(t))
	}
	judges := []any{}
	for _, r := range runs {
		if j := mapOf(r["judge"]); j != nil {
			judges = append(judges, doc{"id": r["id"], "state": r["state"], "evaluator": j["evaluator"], "trialId": j["trialId"], "trialIds": j["trialIds"], "groupId": j["groupId"], "order": j["order"], "output": judgeOutput(r), "problem": r["problem"]})
		}
	}
	scores := []any{}
	for _, sc := range cur {
		scores = append(scores, sc)
	}
	out := expView(exp)
	notes, err := queryRows(q, "SELECT id,kind,field,old,new,text,author,at FROM experiment_note WHERE experiment_id=$1 ORDER BY seq", id)
	if err != nil {
		return nil, err
	}
	out["notes"] = notes
	out["variants"], out["spend"], out["trials"], out["cells"], out["scores"], out["agreement"], out["judges"] = variants, s, tv, cells, scores, agreement(cur), judges
	out["pairwise"] = pairwiseView(runs)
	strategies := []any{}
	for _, r := range runs {
		if st := mapOf(r["loopStrategy"]); st != nil {
			strategies = append(strategies, doc{"id": r["id"], "iteration": st["iteration"], "state": r["state"], "problem": r["problem"], "output": judgeOutput(r)})
		}
	}
	out["strategies"] = strategies
	out["comparison"] = expComparison(exp, trials, cur)
	out["typed"] = expTyped(exp, trials, cur)
	out["priority"] = doc{"value": "background", "note": "No effect until the scheduler exists"}
	return out, nil
}

func labRegistry() ([]any, error) {
	ucs, err := useCases()
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, e := range ucs {
		status, mapsTo := e["status"], e["maps_to"]
		if status == nil {
			status = "current"
		}
		if mapsTo == nil {
			mapsTo = []any{}
		}
		out = append(out, doc{"id": e["id"], "goal": e["goal"], "source": e["source"], "source_status": e["source_status"], "lab_fit": e["lab_fit"], "maturity": e["maturity"], "checks": e["checks"], "status": status, "maps_to": mapsTo})
	}
	return out, nil
}

func labIDArg(v any, what string) (string, error) {
	s, ok := v.(string)
	if !ok || s == "" || len(s) > 300 {
		return "", fail(400, "Expected "+what)
	}
	return s, nil
}

// labShapes: each Lab operation's contract types (contracts/ops-<domain>.schema.json); registration fails without one.
var labShapes = map[string]func(handler) handler{
	"lab_compare_methods":   shaped[contract.LabCompareMethodsRequest, contract.LabCompareMethodsReply],
	"lab_cell_action":       shaped[contract.LabCellActionRequest, contract.LabCellActionReply],
	"lab_example_get":       shaped[contract.LabExampleGetRequest, contract.LabExampleGetReply],
	"lab_examples_add":      shaped[contract.LabExamplesAddRequest, contract.LabExamplesAddReply],
	"lab_examples_list":     shaped[contract.LabExamplesListRequest, contract.LabExamplesListReply],
	"lab_experiment_action": shaped[contract.LabExperimentActionRequest, contract.LabExperimentActionReply],
	"lab_experiment_create": shaped[contract.LabExperimentCreateRequest, contract.LabExperimentCreateReply],
	"lab_experiment_decide": shaped[contract.LabExperimentDecideRequest, contract.LabExperimentDecideReply],
	"lab_experiment_get":    shaped[contract.LabExperimentGetRequest, contract.LabExperimentGetReply],
	"lab_experiment_note":   shaped[contract.LabExperimentNoteRequest, contract.LabExperimentNoteReply],
	"lab_experiments_list":  shaped[contract.LabExperimentsListRequest, contract.LabExperimentsListReply],
	"lab_promote":           shaped[contract.LabPromoteRequest, contract.LabPromoteReply],
	"lab_rating_create":     shaped[contract.LabRatingCreateRequest, contract.LabRatingCreateReply],
	"lab_rating_withdraw":   shaped[contract.LabRatingWithdrawRequest, contract.LabRatingWithdrawReply],
	"lab_rating_get":        shaped[contract.LabRatingGetRequest, contract.LabRatingGetReply],
	"lab_ratings_list":      shaped[contract.LabRatingsListRequest, contract.LabRatingsListReply],
	"lab_registry":          shaped[contract.LabRegistryRequest, contract.LabRegistryReply],
	"lab_score_correct":     shaped[contract.LabScoreCorrectRequest, contract.LabScoreCorrectReply],
}

// labOps: every Lab operation; the same names serve POST /api/ops/<name> and the /api/lab routes. Long operations run
// their own transactions (q.tx nil); the rest run in the route's transaction.
var labOps = map[string]struct {
	long, read bool
	h          func(q *Request, b doc) (any, error)
}{
	"lab_compare_methods": {false, false, func(q *Request, b doc) (any, error) { return labCompareMethods(q, b) }},
	"lab_examples_add":    {false, false, func(q *Request, b doc) (any, error) { return labExamplesAdd(q, b) }},
	"lab_examples_list":   {false, true, func(q *Request, b doc) (any, error) { return labExamplesList(q, b["state"]) }},
	"lab_example_get": {false, true, func(q *Request, b doc) (any, error) {
		id, err := labIDArg(b["artifactId"], "artifactId")
		if err != nil {
			return nil, err
		}
		return labExampleGet(q, id)
	}},
	"lab_experiment_create": {false, false, func(q *Request, b doc) (any, error) { return expCreate(q, b) }},
	"lab_experiment_note":   {false, false, func(q *Request, b doc) (any, error) { return expNote(q, b) }},
	"lab_experiment_decide": {false, false, func(q *Request, b doc) (any, error) { return expDecide(q, b) }},
	"lab_experiments_list": {false, true, func(q *Request, b doc) (any, error) {
		rs, err := queryRows(q, "SELECT * FROM experiment ORDER BY seq DESC")
		out := []any{}
		for _, r := range rs {
			out = append(out, expView(r))
		}
		return doc{"experiments": out}, err
	}},
	"lab_experiment_get": {false, true, func(q *Request, b doc) (any, error) {
		id, err := labIDArg(b["experimentId"], "experimentId")
		if err != nil {
			return nil, err
		}
		return expGet(q, id)
	}},
	"lab_experiment_action": {true, false, func(q *Request, b doc) (any, error) {
		id, err := labIDArg(b["experimentId"], "experimentId")
		if err != nil {
			return nil, err
		}
		return expAction(q, id, astr(b["action"]))
	}},
	"lab_cell_action": {true, false, func(q *Request, b doc) (any, error) {
		act := astr(b["action"])
		if act != "cancel" && act != "retry" && act != "retry_suffix" {
			return nil, fail(400, "action is cancel, retry or retry_suffix")
		}
		id, err := labIDArg(b["experimentId"], "experimentId")
		if err != nil {
			return nil, err
		}
		cell, err := labIDArg(b["cell"], "cell")
		if err != nil {
			return nil, err
		}
		if act == "retry_suffix" {
			return expRetrySuffix(q, id, cell, b)
		}
		return expCellAction(q, id, cell, act)
	}},
	"lab_promote": {false, false, func(q *Request, b doc) (any, error) {
		id, err := labIDArg(b["experimentId"], "experimentId")
		if err != nil {
			return nil, err
		}
		v, err := labIDArg(b["variant"], "variant")
		if err != nil {
			return nil, err
		}
		return expPromote(q, id, v)
	}},
	"lab_rating_create":   {false, false, func(q *Request, b doc) (any, error) { return expRate(q, b) }},
	"lab_rating_withdraw": {false, false, func(q *Request, b doc) (any, error) { return labRatingWithdraw(q, b) }},
	"lab_ratings_list":    {false, true, func(q *Request, b doc) (any, error) { return labRatings(q, b["experimentId"], b["state"]) }},
	"lab_rating_get": {false, true, func(q *Request, b doc) (any, error) {
		id, err := labIDArg(b["questionId"], "questionId")
		if err != nil {
			return nil, err
		}
		return labRating(q, id)
	}},
	"lab_score_correct": {false, false, func(q *Request, b doc) (any, error) {
		id, err := labIDArg(b["scoreId"], "scoreId")
		if err != nil {
			return nil, err
		}
		return scoreCorrect(q, id, b)
	}},
	"lab_registry": {false, true, func(q *Request, b doc) (any, error) {
		r, err := labRegistry()
		return doc{"useCases": r}, err
	}},
}

// scoreCorrect: a correction keeps the trials, evaluator and form; only a current score can be corrected.
func scoreCorrect(q *Request, id string, b doc) (doc, error) {
	rs, err := queryRows(q, "SELECT * FROM score WHERE id=$1", id)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, fail(404, "Unknown score")
	}
	old := rs[0]
	if old["form"] == "typed" {
		return nil, fail(400, "A typed score follows from the trial's p, its bands and the label; correct the example or the bands instead")
	}
	if err := exec(q, "SELECT pg_advisory_xact_lock(hashtext($1))", "experiment:"+astr(old["experiment_id"])); err != nil {
		return nil, err
	}
	var n int
	if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM score WHERE replaces_score_id=$1", id).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, fail(409, "Score already corrected")
	}
	trials := []any{}
	switch t := old["trial_ids"].(type) {
	case []any:
		trials = t
	case []string:
		for _, x := range t {
			trials = append(trials, x)
		}
	}
	nid, err := scoreAdd(q, doc{"experimentId": old["experiment_id"], "projectId": old["project_id"], "trialIds": trials, "evaluator": old["evaluator"], "evaluatorVersion": old["evaluator_version"],
		"form": old["form"], "value": b["value"], "questionId": old["question_id"], "sourceExecutionId": old["source_execution_id"], "rationale": b["rationale"], "replaces": id})
	if err != nil {
		return nil, err
	}
	rs, err = queryRows(q, "SELECT * FROM score WHERE id=$1", nid)
	if err != nil {
		return nil, err
	}
	return scoreView(rs[0]), nil
}

func runLabOp(name string, q *Request, b doc) (any, error) {
	op := labOps[name]
	if op.long || q.tx != nil {
		return op.h(q, b)
	}
	var out any
	err := q.app.store.Tx(q.ctx(), scopeOf(q.person), !op.read, "", func(tx pgx.Tx) error {
		q.tx = tx
		var err error
		out, err = op.h(q, b)
		return err
	})
	return out, err
}

func init() {
	// /api/ops/lab_*: registered through the long dispatcher below, since an operation() always runs in a transaction.
	for name, op := range labOps {
		name, op := name, op
		shape := labShapes[name]
		if shape == nil {
			panic("lab operation " + name + " has no contract types (labShapes)")
		}
		if op.long {
			longOp(name, shape(func(q *Request) (any, error) {
				b := body(q)
				return runLabOp(name, q, b)
			}))
			continue
		}
		gatedOp(name, op.read, shape(func(q *Request) (any, error) {
			b := body(q)
			return op.h(q, b)
		}))
	}
	r := func(pattern, name string, body func(q *Request) (doc, error)) {
		gatedLongRoute(pattern, func(q *Request) (any, error) {
			b, err := body(q)
			if err != nil {
				return nil, err
			}
			return runLabOp(name, q, b)
		})
	}
	bodyOf := func(q *Request) (doc, error) { return body(q), nil }
	none := func(q *Request) (doc, error) { return doc{}, nil }
	r("POST /api/lab/examples", "lab_examples_add", bodyOf)
	r("GET /api/lab/examples", "lab_examples_list", func(q *Request) (doc, error) {
		b := doc{}
		if s := q.r.URL.Query().Get("state"); s != "" {
			b["state"] = s
		}
		return b, nil
	})
	r("GET /api/lab/examples/{id}", "lab_example_get", func(q *Request) (doc, error) { return doc{"artifactId": q.r.PathValue("id")}, nil })
	r("POST /api/lab/experiments", "lab_experiment_create", bodyOf)
	r("GET /api/lab/experiments", "lab_experiments_list", none)
	r("GET /api/lab/experiments/{id}", "lab_experiment_get", func(q *Request) (doc, error) { return doc{"experimentId": q.r.PathValue("id")}, nil })
	for _, a := range []string{"launch", "stop", "resume"} {
		a := a
		r("POST /api/lab/experiments/{id}/"+a, "lab_experiment_action", func(q *Request) (doc, error) { return doc{"experimentId": q.r.PathValue("id"), "action": a}, nil })
	}
	for _, a := range []string{"cancel", "retry", "retry_suffix"} {
		a := a
		r("POST /api/lab/experiments/{id}/cells/{cell}/"+a, "lab_cell_action", func(q *Request) (doc, error) {
			b := doc{"experimentId": q.r.PathValue("id"), "cell": q.r.PathValue("cell"), "action": a}
			if a == "retry_suffix" {
				b["sourceRunId"], b["requestId"] = body(q)["sourceRunId"], body(q)["requestId"]
			}
			return b, nil
		})
	}
	r("POST /api/lab/experiments/{id}/promote", "lab_promote", func(q *Request) (doc, error) {
		return doc{"experimentId": q.r.PathValue("id"), "variant": body(q)["variant"]}, nil
	})
	r("POST /api/lab/ratings", "lab_rating_create", bodyOf)
	r("GET /api/lab/ratings", "lab_ratings_list", func(q *Request) (doc, error) {
		b := doc{}
		for _, k := range []string{"experimentId", "state"} {
			if v := q.r.URL.Query().Get(k); v != "" {
				b[k] = v
			}
		}
		return b, nil
	})
	r("GET /api/lab/ratings/{id}", "lab_rating_get", func(q *Request) (doc, error) { return doc{"questionId": q.r.PathValue("id")}, nil })
	r("POST /api/lab/scores/{id}/correct", "lab_score_correct", func(q *Request) (doc, error) {
		b := body(q)
		b["scoreId"] = q.r.PathValue("id")
		return b, nil
	})
	r("GET /api/lab/registry", "lab_registry", none)

	onStart(func(ctx context.Context, a *App) {
		go func() {
			labReconcileWhere(ctx, a, "running,paused,stopped", false)
			// A finish hook lost to a restart: completed draft runs are imported again (idempotent).
			var runs []doc
			runTx(ctx, a, nil, false, "", func(q *Request) (err error) {
				runs, err = runsWhere(q, "data->>'pipelineId'='draft-examples' AND state='succeeded'")
				return
			})
			for _, r := range runs {
				importDrafts(ctx, a, r)
			}
		}()
	})
}

func pipelineFinished(ctx context.Context, a *App, run doc) {
	if id := astr(run["experimentId"]); id != "" {
		if err := labReconcile(ctx, a, id); err != nil {
			log.Printf("lab reconcile %s: %v", id, err)
		}
	}
	if run["pipelineId"] == "draft-examples" && run["state"] == "succeeded" {
		importDrafts(ctx, a, run)
	}
}

// expComparison: each non-baseline variant against the baseline (the first variant) on the primary evaluator, through
// the one statistics function, over held_out examples when the suite has any (bands are tuned on tune), else over all.
// Only the latest attempt of a cell counts, with its latest primary score, and only one evaluator version (the newest):
// rows of different versions are never averaged. An unscored typed trial is left out.
func expComparison(exp doc, trials []doc, cur []doc) []any {
	primary, vs := astr(exp["primary_evaluator"]), anys(exp["variants"])
	out := []any{}
	if primary == "" || len(vs) < 2 {
		return out
	}
	baseline := astr(mapOf(vs[0])["label"])
	scope := "all"
	if hasHeldOut(exp) {
		scope = "held_out"
	}
	reason := ""
	if strings.HasSuffix(primary, ":pick_best") {
		reason = "pick_best ranks trials together; it gives no per-trial value to pair"
	}
	version := ""
	for _, sc := range cur {
		if sc["evaluator"] == primary && len(trialIDsOf(sc)) == 1 {
			version = astr(sc["evaluatorVersion"])
		}
	}
	values, versions := map[string]map[string][]float64{}, map[string]bool{}
	for _, st := range scoredLatest(exp, trials, cur, func(sc doc) bool { return sc["evaluator"] == primary }) {
		if scope == "held_out" && st.split != "held_out" || mapOf(st.score["value"])["answer"] == "unscored" {
			continue
		}
		versions[astr(st.score["evaluatorVersion"])] = true
		if astr(st.score["evaluatorVersion"]) != version {
			continue
		}
		if values[st.variant] == nil {
			values[st.variant] = map[string][]float64{}
		}
		values[st.variant][st.example] = append(values[st.variant][st.example], scoreNumber(st.score))
	}
	if len(versions) > 1 && reason == "" {
		reason = "scores of older evaluator versions are left out"
	}
	if len(values[baseline]) == 0 && reason == "" {
		reason = "the baseline has no scored trial"
	}
	for _, x := range vs[1:] {
		label := astr(mapOf(x)["label"])
		out = append(out, doc{"variant": label, "baseline": baseline, "evaluator": primary, "evaluatorVersion": version, "split": scope, "reason": nilIf(reason),
			"result": compare(values[baseline], values[label])})
	}
	return out
}

// bandsOf: a variant's bands {yes, no}: p >= yes answers yes, p <= no answers no, between abstains. Without bands every
// answer is confident at 0.5 (no middle band).
func bandsOf(v any) (float64, float64, error) {
	if v == nil {
		return 0.5, 0.5, nil
	}
	m := mapOf(v)
	yes, okY := numOK(m["yes"])
	no, okN := numOK(m["no"])
	if m == nil || len(m) != 2 || !okY || !okN || no < 0 || yes > 1 || no > yes {
		return 0, 0, fail(400, "bands are {yes, no} with 0 <= no <= yes <= 1")
	}
	return yes, no, nil
}

// scoreTyped: the typed evaluator for one trial: its output's p (the last step's output in the run record) through the
// variant's bands, against the example's label (references/expected.json {expected, tag?}). A trial it cannot score is
// recorded as {answer: unscored, reason} so the reason is visible and it is not tried again; it counts in no statistic.
func scoreTyped(q *Request, exp, t, pin doc, name string, examples map[string]doc) error {
	key := astr(pin["artifact_id"])
	full := examples[key]
	if full == nil {
		var err error
		if full, _, err = exampleOf(q, pin); err != nil {
			return err
		}
		examples[key] = full
	}
	var label, tag, reason string
	for _, x := range anys(full["references"]) {
		if f := mapOf(x); f["path"] == "expected.json" && label == "" && reason == "" { // paths are unique; the first counts
			var e map[string]any
			if err := json.Unmarshal([]byte(astr(f["text"])), &e); err != nil {
				reason = "references/expected.json is not a JSON object"
			}
			label, tag = astr(e["expected"]), astr(e["tag"])
		}
	}
	steps := anys(t["steps"])
	var out doc
	if len(steps) > 0 {
		out = mapOf(mapOf(steps[len(steps)-1])["output"])
	}
	p, isP := numOK(out["p"])
	switch {
	case reason != "":
	case label != "yes" && label != "no":
		reason = "the example has no yes/no label in references/expected.json"
	case !isP:
		reason = "the trial output has no number p"
	case p < 0 || p > 1:
		reason = fmt.Sprintf("the trial output p %v is outside 0..1", out["p"])
	}
	var value doc
	rationale := ""
	if reason != "" {
		value, rationale = doc{"answer": "unscored", "reason": reason, "label": nilIf(label), "tag": nilIf(tag)}, "unscored: "+reason
	} else {
		yes, no, err := bandsOf(mapOf(findVariant(exp, mapOf(t["cell"])["variant"])["overrides"])["bands"])
		if err != nil {
			return err
		}
		answer := bandAnswer(p, yes, no)
		value = doc{"answer": answer, "label": label, "p": p, "tag": nilIf(tag)}
		rationale = fmt.Sprintf("%s at p %.3g (bands %.3g/%.3g), label %s", answer, p, yes, no, label)
	}
	_, err := scoreAdd(q, doc{"experimentId": exp["id"], "projectId": exp["project_id"], "trialIds": []any{t["id"]}, "evaluator": name,
		"evaluatorVersion": "labels-v1", "form": "typed", "value": value, "rationale": rationale})
	return err
}

// scoredTrial: the latest attempt of one cell with its latest score of one evaluator.
type scoredTrial struct {
	trial, score   doc
	example, split string
	variant        string
}

// scoredLatest: the latest comparable attempt of every cell with its latest score that match accepts, and the split of
// its example. Comparisons and typed statistics read trials only through it.
func scoredLatest(exp doc, trials, cur []doc, match func(doc) bool) []scoredTrial {
	split := map[string]string{}
	for _, p := range anys(exp["examples"]) {
		split[astr(mapOf(p)["id"])] = astr(mapOf(p)["split"])
	}
	byTrial := map[any]doc{}
	for _, sc := range cur { // in order: a later score of a trial replaces the earlier
		if match(sc) && len(trialIDsOf(sc)) == 1 {
			byTrial[firstTrial(sc)] = sc
		}
	}
	out := []scoredTrial{}
	order, latest := expLatest(trials)
	for _, k := range order {
		t := latest[k]
		cell := mapOf(t["cell"])
		if sc := byTrial[t["id"]]; sc != nil && comparable(t) {
			out = append(out, scoredTrial{t, sc, astr(cell["example"]), split[astr(cell["example"])], astr(cell["variant"])})
		}
	}
	return out
}

func hasHeldOut(exp doc) bool {
	for _, p := range anys(exp["examples"]) {
		if mapOf(p)["split"] == "held_out" {
			return true
		}
	}
	return false
}

// expTyped: per variant and split, the typed evaluator's coverage, selective accuracy and escalation share per example
// tag (the language). Bands are tuned on tune and reported on held_out, so both splits are shown, never mixed.
func expTyped(exp doc, trials []doc, cur []doc) []any {
	out := []any{}
	answers, perExample := map[[2]string][]typedAnswer{}, map[[3]string][]typedAnswer{}
	for _, st := range scoredLatest(exp, trials, cur, func(sc doc) bool { return sc["form"] == "typed" }) {
		v := mapOf(st.score["value"])
		if v["answer"] == "unscored" {
			continue
		}
		perExample[[3]string{st.variant, st.split, st.example}] = append(perExample[[3]string{st.variant, st.split, st.example}], typedAnswer{astr(v["tag"]), astr(v["answer"]), astr(v["label"])})
	}
	// Identical repeats of one example count as one answer (a near-deterministic variant); differing ones count each.
	for k, as := range perExample {
		same := true
		for _, a := range as[1:] {
			same = same && a.Answer == as[0].Answer
		}
		if same {
			as = as[:1]
		}
		answers[[2]string{k[0], k[1]}] = append(answers[[2]string{k[0], k[1]}], as...)
	}
	for _, x := range anys(exp["variants"]) {
		label := astr(mapOf(x)["label"])
		for _, sp := range []string{"tune", "held_out"} {
			if as := answers[[2]string{label, sp}]; len(as) > 0 {
				out = append(out, doc{"variant": label, "split": sp, "stats": typedStats(as)})
			}
		}
	}
	return out
}

// exampleRef: <id>@<version>. A version read back from stored example JSON is a float64, which %v prints as 1e+06; a
// whole number prints as one. Anything else keeps %v (the importer only stores whole versions).
func exampleRef(id, version any) string {
	if v, ok := intOK(version); ok {
		return fmt.Sprintf("%v@%d", id, v)
	}
	return fmt.Sprintf("%v@%v", id, version)
}

func labRatingWithdraw(q *Request, b doc) (doc, error) {
	if q.person != nil && !fullAccess(q.person) {
		return nil, fail(403, "Only the owner withdraws Lab ratings")
	}
	id, err := labIDArg(b["questionId"], "questionId")
	if err != nil {
		return nil, err
	}
	reason, err := labStr(b["reason"], "withdrawal reason", 4000)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		return nil, fail(400, "Withdrawal reason is empty")
	}

	row, err := first(q, "SELECT * FROM question WHERE id=$1 AND kind='rating' FOR UPDATE", id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fail(404, "Unknown Lab rating")
	}
	if row["state"] == "answered" {
		return nil, fail(409, "Rating already answered")
	}
	if row["state"] == "open" {
		withdrawal := doc{"reason": reason, "actor": actor(q, "operator"), "at": nowMs()}
		if err := exec(q, "UPDATE question SET state='withdrawn',data=coalesce(data,'{}'::jsonb)||jsonb_build_object('withdrawal',$2::jsonb) WHERE id=$1 AND state='open'", id, jsonBytes(withdrawal)); err != nil {
			return nil, err
		}
	}
	return labRating(q, id)
}

// The existing GLOBAL lock serializes this check/enqueue with people replacement; no owner fallback for Lab.
func labRequester(q *Request, id string) (*Person, error) {
	if err := exec(q, "SELECT pg_advisory_xact_lock(hashtext($1))", GLOBAL); err != nil {
		return nil, err
	}
	exp, err := expRow(q, id)
	if err != nil {
		return nil, err
	}
	email := astr(exp["requested_by"])
	if email == "" {
		return nil, failCode(409, "Lab requester is unknown; retained history cannot authorize new work", "requester:unknown")
	}
	if exp["requester_removed_at"] != nil {
		return nil, failCode(409, "Lab requester was removed; this experiment cannot transfer identity", "requester:removed")
	}
	p, err := unscoped(q, func() (*Person, error) { return resolveAccessTx(q.ctx(), q.tx, email) })
	if err != nil {
		var denied *HTTPError
		if errors.As(err, &denied) && denied.Code == 403 {
			return nil, failCode(409, "Lab requester is no longer present", "requester:removed")
		}
		return nil, err
	}
	if (!fullAccess(p) && !hasRole(p, "architect")) || !mayAccessProjectID(p, astr(exp["project_id"])) {
		return nil, failCode(403, "Lab requester lacks current role or project access", "requester:access-revoked")
	}
	return p, nil
}
