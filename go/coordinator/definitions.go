// Definitions (docs/designs/fleet-definition-editors.md): agents, pipelines and the catalogue live in the store as
// append-only revisions; config/ in the release is the seed. Reconcile at start imports a file the store has not seen and
// records the repository's version of every shipped file; it never moves a head. This file is the one writer of the
// definition and definition_revision tables.
package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

// definitionFile: one shipped file of a kind; path is the repository path (also with a directory override).
type definitionFile struct {
	kind, id, path string
	raw            []byte
}

// canonicalText: content as stored and exported: keys sorted, two-space indent, no HTML escaping, a trailing newline.
func canonicalText(v any) (string, error) {
	if raw, ok := v.([]byte); ok { // file bytes: numbers keep their exact text
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return "", err
		}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return b.String(), nil
}

func sha256Text(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// gitBlobSha: the sha git gives the file bytes (git hash-object).
func gitBlobSha(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// validDefinition: the kind's own validator, on parsed content.
func validDefinition(kind, id string, raw []byte) (any, error) {
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("JSON: %v", err)
	}
	switch kind {
	case "agent":
		if _, err := contract.ParseProfile(raw); err != nil {
			return nil, err
		}
		if v["id"] != id {
			return nil, fmt.Errorf("id differs from the file name")
		}
	case "pipeline":
		if _, err := validatePipeline(v); err != nil {
			return nil, err
		}
		if v["id"] != id {
			return nil, fmt.Errorf("id differs from the file name")
		}
	case "catalogue":
		if astr(v["capability"]) == "" || anys(v["methods"]) == nil {
			return nil, fmt.Errorf("expected capability and methods")
		}
	}
	return v, nil
}

// shippedDefinitions: the definition files of the running release. A profile file that is not a valid profile is not a
// definition and is skipped (as listProfiles skips it); a broken pipeline or catalogue is returned with its error.
func shippedDefinitions() (files []definitionFile, broken map[string]error) {
	broken = map[string]error{}
	read := func(kind, dir, repoDir string) {
		es, _ := os.ReadDir(dir)
		for _, e := range es {
			n := e.Name()
			id := strings.TrimSuffix(n, ".json")
			if !strings.HasSuffix(n, ".json") || !idPattern.MatchString(id) {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				continue
			}
			f := definitionFile{kind, id, repoDir + "/" + n, raw}
			if _, err := validDefinition(kind, id, raw); err != nil {
				// Not imported. A broken pipeline or catalogue keeps its last valid revision; a file in the profile directory that
				// is not a profile is not a definition (as listProfiles skips it), and an imported one stays while its file exists.
				if kind == "agent" {
					log.Printf("definitions: %s is not a profile, skipped: %v", f.path, err)
					broken[kind+"/"+id] = nil
				} else {
					broken[kind+"/"+id] = err
				}
				continue
			}
			files = append(files, f)
		}
	}
	read("agent", profilesDir(), "config/agents")
	read("pipeline", filepath.Join(configDir(), "pipelines"), "config/pipelines")
	if raw, err := os.ReadFile(filepath.Join(configDir(), "catalogue.json")); err == nil {
		if _, err := validDefinition("catalogue", "catalogue", raw); err != nil {
			broken["catalogue/catalogue"] = err
		} else {
			files = append(files, definitionFile{"catalogue", "catalogue", "config/catalogue.json", raw})
		}
	}
	return files, broken
}

// reconcileDefinitions: import a file the store lacks, record the repository version of every shipped file, retire a
// definition whose file is gone when its head equals the last repository content. Heads move only by a save.
func reconcileDefinitions(q *Request, files []definitionFile, broken map[string]error, release string) error {
	seen := map[string]bool{}
	for _, f := range files {
		key := f.kind + "/" + f.id
		seen[key] = true
		text, err := canonicalText(f.raw)
		if err != nil {
			return err
		}
		blob, csha := gitBlobSha(f.raw), sha256Text(text)
		var repoSha *string
		var retired *time.Time
		err = q.tx.QueryRow(q.ctx(), "SELECT repo_sha,retired_at FROM definition WHERE kind=$1 AND id=$2 FOR UPDATE", f.kind, f.id).Scan(&repoSha, &retired)
		if err == pgx.ErrNoRows {
			if err := exec(q, `INSERT INTO definition(kind,id,path,head,repo_sha,repo_content,repo_content_sha,ever_shipped) VALUES($1,$2,$3,1,$4,$5,$6,true)`,
				f.kind, f.id, f.path, blob, text, csha); err != nil {
				return err
			}
			if err := exec(q, `INSERT INTO definition_revision(kind,id,revision,content,content_sha,repo_base,source,actor_kind,message) VALUES($1,$2,1,$3,$4,$5,'repo','system',$6)`,
				f.kind, f.id, text, csha, blob, "Repository "+release); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if repoSha != nil && *repoSha == blob && retired == nil {
			continue
		}
		// The repository moved (or the file came back): record it; the head stays (state repository-changed or synced).
		if err := exec(q, `UPDATE definition SET repo_sha=$3,repo_content=$4,repo_content_sha=$5,ever_shipped=true,retired_at=NULL WHERE kind=$1 AND id=$2`,
			f.kind, f.id, blob, text, csha); err != nil {
			return err
		}
	}
	rs, err := queryRows(q, `SELECT d.kind,d.id,d.repo_content_sha,r.content_sha AS head_sha FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND r.revision=d.head
	  WHERE d.ever_shipped AND d.retired_at IS NULL AND d.repo_sha IS NOT NULL`)
	if err != nil {
		return err
	}
	for _, r := range rs {
		key := astr(r["kind"]) + "/" + astr(r["id"])
		if _, present := broken[key]; seen[key] || present {
			continue
		}
		// The shipped file is gone: retire an unedited definition, otherwise keep it (repository-deleted).
		retire := "NULL"
		if astr(r["head_sha"]) == astr(r["repo_content_sha"]) {
			retire = "now()"
		}
		if err := exec(q, "UPDATE definition SET repo_sha=NULL,repo_content=NULL,repo_content_sha=NULL,retired_at="+retire+" WHERE kind=$1 AND id=$2", r["kind"], r["id"]); err != nil {
			return err
		}
	}
	for key, e := range broken {
		if e == nil {
			continue
		}
		log.Printf("definitions: %s is invalid, its last valid revision stays: %v", key, e)
	}
	return nil
}

// definitionState: computed on read (fleet-definition-editors.md, Reconcile at start), first match wins.
func definitionState(everShipped bool, repoSha *string, repoContentSha *string, headSha string, headRepoBase *string) contract.DefinitionState {
	switch {
	case !everShipped:
		return contract.DefinitionStateStoreOnly
	case repoSha == nil:
		return contract.DefinitionStateRepositoryDeleted
	case repoContentSha != nil && *repoContentSha == headSha:
		return contract.DefinitionStateSynced
	case headRepoBase == nil || *headRepoBase != *repoSha:
		return contract.DefinitionStateRepositoryChanged
	}
	return contract.DefinitionStateAhead
}

type definitionRow struct {
	Kind, ID, Path               string
	Head                         int64
	RepoSha, RepoContentSha      *string
	EverShipped                  bool
	Retired                      *time.Time
	Revision                     int64
	Parent                       *int64
	Content                      []byte
	ContentSha                   string
	RepoBase                     *string
	Source, ActorKind            string
	ByEmail, ByExecution, Direct *string
	Message                      string
	At                           time.Time
}

const definitionCols = `d.kind,d.id,d.path,d.head,d.repo_sha,d.repo_content_sha,d.ever_shipped,d.retired_at,r.revision,r.parent,r.content,r.content_sha,r.repo_base,r.source,r.actor_kind,r.by_email,r.by_execution,r.directed_by,r.message,r.at`

func scanDefinition(r pgx.CollectableRow) (definitionRow, error) {
	var x definitionRow
	err := r.Scan(&x.Kind, &x.ID, &x.Path, &x.Head, &x.RepoSha, &x.RepoContentSha, &x.EverShipped, &x.Retired, &x.Revision, &x.Parent, &x.Content, &x.ContentSha, &x.RepoBase,
		&x.Source, &x.ActorKind, &x.ByEmail, &x.ByExecution, &x.Direct, &x.Message, &x.At)
	return x, err
}

// definitionRows: definitions with one revision each (the head, or revision when > 0).
func definitionRows(q *Request, where string, revision int64, args ...any) ([]definitionRow, error) {
	join := "r.revision=d.head"
	if revision > 0 {
		join = fmt.Sprintf("r.revision=%d", revision)
	}
	rs, err := q.tx.Query(q.ctx(), "SELECT "+definitionCols+" FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND "+join+" WHERE "+where+" ORDER BY d.kind,d.id", args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rs, scanDefinition)
}

// personOf: a person by email as replies show them; the name comes from people.json at read time.
func personOf(q *Request, email string) *contract.Person {
	p := contract.Person{Email: email, Name: email}
	if x := q.app.people[email]; x != nil && x.Name != "" {
		p.Name = x.Name
	}
	return &p
}

func (x definitionRow) state() contract.DefinitionState {
	headSha, base := x.ContentSha, x.RepoBase
	return definitionState(x.EverShipped, x.RepoSha, x.RepoContentSha, headSha, base)
}

func (x definitionRow) revisionView(q *Request) contract.Revision {
	a := contract.Actor{Kind: contract.ActorKind(x.ActorKind)}
	if x.ByEmail != nil {
		a.Person = personOf(q, *x.ByEmail)
	}
	if x.ByExecution != nil {
		a.ExecutionId = x.ByExecution
	}
	if x.Direct != nil {
		a.DirectedBy = personOf(q, *x.Direct)
	}
	return contract.Revision{Revision: x.Revision, Parent: x.Parent, ContentSha: x.ContentSha, RepoBase: x.RepoBase, Source: contract.RevisionSource(x.Source), Actor: a,
		Message: x.Message, At: x.At.UTC().Format(time.RFC3339Nano)}
}

// definitionContent: stored content as the contract's typed union (one property, by kind).
func definitionContent(kind string, raw []byte) (contract.DefinitionContent, error) {
	var c contract.DefinitionContent
	var err error
	switch kind {
	case "agent":
		var p contract.AgentDefinition
		err = json.Unmarshal(raw, &p)
		c.Agent = &p
	case "pipeline":
		var p contract.PipelineDefinition
		err = json.Unmarshal(raw, &p)
		c.Pipeline = &p
	case "catalogue":
		var p contract.CatalogueDefinition
		err = json.Unmarshal(raw, &p)
		c.Catalogue = &p
	}
	return c, err
}

// definitionName: the title a list shows (a profile or method name when it has one, else the id).
func definitionName(x definitionRow) string {
	var v map[string]any
	json.Unmarshal(x.Content, &v)
	if n := astr(v["name"]); n != "" {
		return n
	}
	if x.Kind == "catalogue" {
		return "Catalogue: " + astr(v["capability"])
	}
	return x.ID
}

func definitionKind(k contract.DefinitionKind) error {
	switch k {
	case contract.DefinitionKindAgent, contract.DefinitionKindPipeline, contract.DefinitionKindCatalogue:
		return nil
	}
	return fail(400, "kind: agent, pipeline or catalogue")
}

func definitionList(q *Request, r *contract.DefinitionListRequest) (contract.DefinitionListReply, error) {
	out := contract.DefinitionListReply{Items: []contract.DefinitionListItem{}}
	if err := definitionKind(r.Kind); err != nil {
		return out, err
	}
	where := "d.kind=$1 AND d.retired_at IS NULL"
	if r.IncludeRetired != nil && *r.IncludeRetired {
		where = "d.kind=$1"
	}
	xs, err := definitionRows(q, where, 0, string(r.Kind))
	if err != nil {
		return out, err
	}
	roles := map[string][]string{} // profile -> project:role, read once per list
	if r.Kind == contract.DefinitionKindAgent {
		for _, id := range projectIDs() {
			for role, v := range mapOf(projectSettingsOf(id)["roles"]) {
				p := astr(v)
				if p == "" {
					p = astr(mapOf(v)["profile"])
				}
				roles[p] = append(roles[p], id+":"+role)
			}
		}
	}
	for _, x := range xs {
		// ponytail: last30 is zero until executions record definition references (the run step of this build).
		item := contract.DefinitionListItem{Kind: r.Kind, Id: x.ID, Name: definitionName(x), Scope: "shared", Path: x.Path, Revision: x.Head, State: x.state(),
			Last30: contract.Window{}}
		if x.Kind == "agent" {
			var p contract.Profile
			json.Unmarshal(x.Content, &p)
			f := contract.AgentListFacts{Harness: "codex", Model: p.Model, Effort: p.Effort, Roles: append([]string{}, roles[x.ID]...)}
			sort.Strings(f.Roles)
			if p.Harness != nil {
				f.Harness = string(*p.Harness)
			}
			item.Agent = &f
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func definitionGet(q *Request, r *contract.DefinitionGetRequest) (contract.DefinitionGetReply, error) {
	var out contract.DefinitionGetReply
	if err := definitionKind(r.Kind); err != nil {
		return out, err
	}
	var rev int64
	if r.Revision != nil {
		if rev = *r.Revision; rev < 1 {
			return out, fail(400, "revision: 1 or more")
		}
	}
	xs, err := definitionRows(q, "d.kind=$1 AND d.id=$2", rev, string(r.Kind), r.Id)
	if err != nil {
		return out, err
	}
	if len(xs) == 0 {
		return out, fail(404, "Unknown definition or revision")
	}
	x := xs[0]
	content, err := definitionContent(x.Kind, x.Content)
	if err != nil {
		return out, err
	}
	state := x.state()
	if rev > 0 && rev != x.Head { // state is the definition's: compute it from its head
		hs, err := definitionRows(q, "d.kind=$1 AND d.id=$2", 0, string(r.Kind), r.Id)
		if err != nil {
			return out, err
		}
		state = hs[0].state()
	}
	return contract.DefinitionGetReply{Kind: r.Kind, Id: x.ID, Path: x.Path, Scope: "shared", Revision: x.Revision, Head: x.Head, State: state, RepoSha: x.RepoSha,
		Text: string(x.Content), Content: content, Meta: x.revisionView(q)}, nil
}

// headText: the head content of a live definition; an execution reads definitions only from the store (the repository
// file is a seed). Outside a transaction (long operations) it reads in its own snapshot.
func headText(q *Request, kind, id string) ([]byte, bool, error) {
	var text []byte
	read := func(tx pgx.Tx) error {
		return tx.QueryRow(q.ctx(), `SELECT r.content FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND r.revision=d.head
		  WHERE d.kind=$1 AND d.id=$2 AND d.retired_at IS NULL`, kind, id).Scan(&text)
	}
	var err error
	if q.tx != nil {
		err = read(q.tx)
	} else {
		err = q.app.store.Tx(q.ctx(), nil, false, "", read)
	}
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	return text, err == nil, err
}

// definitionRef: the revision an execution uses for content it resolved: the newest revision with that exact content, or
// (no revision has it) a draft ref on the head.
func definitionRef(q *Request, kind, id string, content any) (contract.DefinitionRef, error) {
	text, err := canonicalText(jsonBytes(content))
	if err != nil {
		return contract.DefinitionRef{}, err
	}
	ref := contract.DefinitionRef{Kind: contract.DefinitionKind(kind), Id: id, ContentSha: sha256Text(text)}
	err = q.tx.QueryRow(q.ctx(), "SELECT revision FROM definition_revision WHERE kind=$1 AND id=$2 AND content_sha=$3 ORDER BY revision DESC LIMIT 1", kind, id, ref.ContentSha).Scan(&ref.Revision)
	if err == pgx.ErrNoRows {
		// The same value written another way (a float64 round trip turns 30000.0 into 30000): compare with the head.
		var head int64
		var headSha string
		var raw []byte
		if e := q.tx.QueryRow(q.ctx(), `SELECT d.head,r.content_sha,r.content FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND r.revision=d.head WHERE d.kind=$1 AND d.id=$2`, kind, id).Scan(&head, &headSha, &raw); e == nil {
			var a, b any
			json.Unmarshal(raw, &a)
			json.Unmarshal(jsonBytes(content), &b)
			if reflect.DeepEqual(a, b) {
				ref.Revision, ref.ContentSha = head, headSha
				return ref, nil
			}
		}
	}
	if err == pgx.ErrNoRows {
		yes := true
		ref.Draft = &yes
		err = q.tx.QueryRow(q.ctx(), "SELECT head FROM definition WHERE kind=$1 AND id=$2", kind, id).Scan(&ref.Revision)
		if err == pgx.ErrNoRows {
			ref.Revision, err = 0, nil
		}
	}
	return ref, err
}

type headRow struct {
	id   string
	text []byte
}

// headTexts: every live definition of a kind with its head content, in one read.
func headTexts(q *Request, kind string) ([]headRow, error) {
	var out []headRow
	read := func(tx pgx.Tx) error {
		rs, err := tx.Query(q.ctx(), `SELECT d.id,r.content FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND r.revision=d.head
		  WHERE d.kind=$1 AND d.retired_at IS NULL ORDER BY d.id`, kind)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rs, func(r pgx.CollectableRow) (headRow, error) { var h headRow; return h, r.Scan(&h.id, &h.text) })
		return err
	}
	if q.tx != nil {
		return out, read(q.tx)
	}
	return out, q.app.store.Tx(q.ctx(), nil, false, "", read)
}

func init() {
	gatedObjOp("definition_list", true, typedOp(definitionList))
	gatedObjOp("definition_get", true, typedOp(definitionGet))
	// Registered before pipelines.go and lab.go (file order), so pipeline and Lab work resume on reconciled definitions.
	onStart(func(ctx context.Context, a *App) {
		files, broken := shippedDefinitions()
		release := ownRelease().Version
		if release == "" {
			release = "unknown release"
		}
		if err := runTx(ctx, a, nil, true, "GLOBAL", func(q *Request) error { return reconcileDefinitions(q, files, broken, release) }); err != nil {
			log.Fatalf("definitions: reconcile: %v", err)
		}
	})
}
