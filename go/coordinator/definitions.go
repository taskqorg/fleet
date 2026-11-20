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
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

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
func validDefinition(kind, id string, raw []byte) error {
	var head struct {
		ID         any             `json:"id"`
		Capability any             `json:"capability"`
		Methods    json.RawMessage `json:"methods"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return fmt.Errorf("JSON: %v", err)
	}
	switch kind {
	case "project":
		var v doc // projectBundle and projectSettings (projects.go) validate the open bundle
		json.Unmarshal(raw, &v)
		_, err := projectBundle(id, v)
		return err
	case "agent":
		if _, err := contract.ParseProfile(raw); err != nil {
			return err
		}
		if head.ID != id {
			return fmt.Errorf("id differs from the file name")
		}
	case "pipeline":
		var v doc // validatePipeline (pipelines.go) reads the open definition
		json.Unmarshal(raw, &v)
		if _, err := validatePipeline(v); err != nil {
			return err
		}
		if head.ID != id {
			return fmt.Errorf("id differs from the file name")
		}
	case "catalogue":
		if c, _ := head.Capability.(string); c == "" {
			return fmt.Errorf("expected capability and methods")
		}
		if _, ok := jsonArray(head.Methods); !ok {
			return fmt.Errorf("expected capability and methods")
		}
	}
	return nil
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
			if err := validDefinition(kind, id, raw); err != nil {
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
		if err := validDefinition("catalogue", "catalogue", raw); err != nil {
			broken["catalogue/catalogue"] = err
		} else {
			files = append(files, definitionFile{"catalogue", "catalogue", "config/catalogue.json", raw})
		}
	}
	projects, errors := seedProjectDefinitions()
	files = append(files, projects...)
	for k, v := range errors {
		broken[k] = v
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
		st, err := q.db().DefRepoState(q.ctx(), db.DefRepoStateParams{Kind: f.kind, ID: f.id})
		if errors.Is(err, pgx.ErrNoRows) {
			if err := q.db().DefInsertShipped(q.ctx(), db.DefInsertShippedParams{Kind: f.kind, ID: f.id, Path: f.path, RepoSha: &blob, RepoContent: &text, RepoContentSha: &csha}); err != nil {
				return err
			}
			if err := q.db().DefRevisionInsert(q.ctx(), db.DefRevisionInsertParams{Kind: f.kind, ID: f.id, Revision: 1, Content: text, ContentSha: csha, RepoBase: &blob,
				Source: "repo", ActorKind: "system", Message: "Repository " + release}); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if st.RepoSha != nil && *st.RepoSha == blob && st.RetiredAt == nil {
			continue
		}
		// The repository moved (or the file came back): record it; the head stays (state repository-changed or synced).
		// A deleted agent or pipeline whose file came back is the repository's again (shipped wins); a deleted project
		// stays deleted: its data is gone and its id only reserved.
		if err := q.db().DefRecordRepo(q.ctx(), db.DefRecordRepoParams{Kind: f.kind, ID: f.id, RepoSha: &blob, RepoContent: &text, RepoContentSha: &csha}); err != nil {
			return err
		}
	}
	rs, err := q.db().DefShippedHeads(q.ctx())
	if err != nil {
		return err
	}
	for _, r := range rs {
		key := r.Kind + "/" + r.ID
		if r.Kind == "project" {
			continue
		}
		if _, present := broken[key]; seen[key] || present {
			continue
		}
		// The shipped file is gone: retire an unedited definition, otherwise keep it (repository-deleted).
		retire := r.RepoContentSha != nil && r.HeadSha == *r.RepoContentSha
		if err := q.db().DefRepoGone(q.ctx(), db.DefRepoGoneParams{Kind: r.Kind, ID: r.ID, Retire: retire}); err != nil {
			return err
		}
	}
	for key, e := range broken {
		if e == nil {
			continue
		}
		log.Printf("definitions: %s is invalid, its last valid revision stays: %v", key, e)
	}
	invalidateEnvCacheOnCommit(q)
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

// definitionFilter: which definitions definitionsOf reads; kind and id "" for any, live leaves out deleted ones, current
// retired ones; revision > 0 reads that revision instead of the head.
type definitionFilter struct {
	kind, id      string
	revision      int64
	live, current bool
}

// definitionsOf: definitions with one revision each (the head, or f.revision), ordered by kind and id.
func definitionsOf(ctx context.Context, d *db.Queries, f definitionFilter) ([]definitionRow, error) {
	rs, err := d.DefRows(ctx, db.DefRowsParams{Revision: int32(f.revision), Kind: optStr(f.kind), ID: optStr(f.id), Live: f.live, Current: f.current})
	out := make([]definitionRow, 0, len(rs))
	for _, r := range rs {
		out = append(out, definitionRowOf(r))
	}
	return out, err
}

func definitionRowOf(r db.DefRowsRow) definitionRow {
	var parent *int64
	if r.Parent != nil {
		p := int64(*r.Parent)
		parent = &p
	}
	return definitionRow{Kind: r.Kind, ID: r.ID, Path: r.Path, Head: r.Head, RepoSha: r.RepoSha, RepoContentSha: r.RepoContentSha, EverShipped: r.EverShipped,
		Retired: r.RetiredAt, Revision: r.Revision, Parent: parent, Content: []byte(r.Content), ContentSha: r.ContentSha, RepoBase: r.RepoBase, Source: r.Source,
		ActorKind: r.ActorKind, ByEmail: r.ByEmail, ByExecution: r.ByExecution, Direct: r.DirectedBy, Message: r.Message, At: r.At}
}

// definitionRows: the filter form callers outside this file still pass (entity_card.go, search.go): conjuncts of
// d.kind=$n, d.kind='project', d.id=$n, d.deleted_at IS NULL and d.retired_at IS NULL, read through definitionsOf.
func definitionRows(q *Request, where string, revision int64, args ...any) ([]definitionRow, error) {
	f := definitionFilter{revision: revision}
	arg := func(t string) (string, error) {
		n, err := strconv.Atoi(strings.TrimPrefix(t, "$"))
		if err != nil || n < 1 || n > len(args) {
			return "", fmt.Errorf("definitionRows: bad argument %q", t)
		}
		v, ok := args[n-1].(string)
		if !ok {
			return "", fmt.Errorf("definitionRows: argument %s is not text", t)
		}
		return v, nil
	}
	for _, c := range strings.Split(where, " AND ") {
		var err error
		switch c = strings.TrimSpace(c); {
		case c == "d.deleted_at IS NULL":
			f.live = true
		case c == "d.retired_at IS NULL":
			f.current = true
		case c == "d.kind='project'":
			f.kind = "project"
		case strings.HasPrefix(c, "d.kind=$"):
			f.kind, err = arg(strings.TrimPrefix(c, "d.kind="))
		case strings.HasPrefix(c, "d.id=$"):
			f.id, err = arg(strings.TrimPrefix(c, "d.id="))
		default:
			err = fmt.Errorf("definitionRows: unknown filter %q", c)
		}
		if err != nil {
			return nil, err
		}
	}
	return definitionsOf(q.ctx(), q.db(), f)
}

// personOf: a person by email as replies show them; the name comes from people.json at read time.
func personOf(q *Request, email string) *contract.Person {
	p := contract.Person{Email: email, Name: email}
	if x := q.peopleSnapshot().person(email); x != nil && x.Name != "" {
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
	case "project":
		var p contract.ProjectBundle
		err = json.Unmarshal(raw, &p)
		c.Project = &p
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

// profileHarness: an agent profile's harness; codex when the profile names none (definition_list, search agent:).
func profileHarness(content []byte) string {
	var p contract.Profile
	json.Unmarshal(content, &p)
	if p.Harness != nil {
		return string(*p.Harness)
	}
	return "codex"
}

// definitionName: the title a list shows (a profile or method name when it has one, else the id).
func definitionName(x definitionRow) string {
	var v struct {
		Name       any `json:"name"`
		Capability any `json:"capability"`
	}
	json.Unmarshal(x.Content, &v)
	if n, _ := v.Name.(string); n != "" {
		return n
	}
	if x.Kind == "catalogue" {
		c, _ := v.Capability.(string)
		return "Catalogue: " + c
	}
	return x.ID
}

func definitionKind(k contract.DefinitionKind) error {
	switch k {
	case contract.DefinitionKindAgent, contract.DefinitionKindPipeline, contract.DefinitionKindCatalogue, contract.DefinitionKindProject:
		return nil
	}
	return fail(400, "kind: agent, pipeline or catalogue")
}

// projectRoleProfiles: profile -> "project:role" for every live project's roles; with execution, also the profiles its
// settings.execution.profiles name ("project:execution.profiles").
func projectRoleProfiles(q *Request, execution bool) (map[string][]string, error) {
	out := map[string][]string{}
	ids, err := projectIDs(q)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		var s struct {
			Roles     map[string]json.RawMessage `json:"roles"`
			Execution struct {
				Profiles []json.RawMessage `json:"profiles"`
			} `json:"execution"`
		}
		json.Unmarshal(jsonBytes(projectSettingsOf(q, id)), &s)
		for role, v := range s.Roles {
			p := profileNamed(v)
			out[p] = append(out[p], id+":"+role)
		}
		if execution {
			for _, v := range s.Execution.Profiles {
				out[profileNamed(v)] = append(out[profileNamed(v)], id+":execution.profiles")
			}
		}
	}
	return out, nil
}

// profileNamed: the profile a project setting names: a string, or an object's id or profile.
func profileNamed(raw json.RawMessage) string {
	var p string
	if json.Unmarshal(raw, &p) == nil {
		return p
	}
	var o struct {
		ID      any `json:"id"`
		Profile any `json:"profile"`
	}
	json.Unmarshal(raw, &o)
	return firstString(o.ID, o.Profile)
}

func definitionList(q *Request, r *contract.DefinitionListRequest) (contract.DefinitionListReply, error) {
	out := contract.DefinitionListReply{Items: []contract.DefinitionListItem{}}
	if err := definitionKind(r.Kind); err != nil {
		return out, err
	}
	f := definitionFilter{kind: string(r.Kind), current: true}
	if r.IncludeRetired != nil && *r.IncludeRetired {
		f = definitionFilter{kind: string(r.Kind), live: true} // a deleted definition is gone, retired or not
	}
	xs, err := definitionsOf(q.ctx(), q.db(), f)
	if err != nil {
		return out, err
	}
	roles := map[string][]string{} // profile -> project:role, read once per list
	if r.Kind == contract.DefinitionKindAgent {
		var err error
		if roles, err = projectRoleProfiles(q, false); err != nil {
			return out, err
		}
	}
	windows, _, err := usageWindows(q, string(r.Kind), "", 30) // one grouped read for the whole list
	if err != nil {
		return out, err
	}
	for _, x := range xs {
		if x.Kind == "project" && q.person != nil && !mayAccessProjectID(q.person, x.ID) {
			continue
		}
		last30 := windows[x.ID]
		if last30.CostBasis == nil {
			last30.CostBasis = ptr("tokens")
		}
		if last30.Cost == nil {
			last30.Cost = []contract.CostFigure{}
		}
		if last30.Resources == nil {
			empty := emptyResourceSummary()
			last30.Resources = &empty
		}
		item := contract.DefinitionListItem{Kind: r.Kind, Id: x.ID, Name: definitionName(x), Scope: "shared", Path: x.Path, Revision: x.Head, State: x.state(),
			Last30: last30}
		if x.Kind == "agent" {
			var p contract.Profile
			json.Unmarshal(x.Content, &p)
			f := contract.AgentListFacts{Harness: profileHarness(x.Content), Model: p.Model, Effort: p.Effort, Roles: append([]string{}, roles[x.ID]...)}
			sort.Strings(f.Roles)
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
	// A deleted definition is gone; a revision an execution pinned stays readable by its number.
	xs, err := definitionsOf(q.ctx(), q.db(), definitionFilter{kind: string(r.Kind), id: r.Id, revision: rev, live: rev == 0})
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
		hs, err := definitionsOf(q.ctx(), q.db(), definitionFilter{kind: string(r.Kind), id: r.Id})
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
	if id == "" {
		return nil, false, nil
	}
	hs, err := headTextsOf(q, kind, id)
	if err != nil || len(hs) == 0 {
		return nil, false, err
	}
	return hs[0].text, true, nil
}

// definitionRef: the revision an execution uses for content it resolved: the newest revision with that exact content, or
// (no revision has it) a draft ref on the head.
func definitionRef(q *Request, kind, id string, content any) (contract.DefinitionRef, error) {
	text, err := canonicalText(jsonBytes(content))
	if err != nil {
		return contract.DefinitionRef{}, err
	}
	ref := contract.DefinitionRef{Kind: contract.DefinitionKind(kind), Id: id, ContentSha: sha256Text(text)}
	ref.Revision, err = q.db().DefRevisionBySha(q.ctx(), db.DefRevisionByShaParams{Kind: kind, ID: id, ContentSha: ref.ContentSha})
	if errors.Is(err, pgx.ErrNoRows) {
		// The same value written another way (a float64 round trip turns 30000.0 into 30000): compare with the head.
		if hs, e := definitionsOf(q.ctx(), q.db(), definitionFilter{kind: kind, id: id}); e == nil && len(hs) > 0 {
			var a, b any
			json.Unmarshal(hs[0].Content, &a)
			json.Unmarshal(jsonBytes(content), &b)
			if reflect.DeepEqual(a, b) {
				ref.Revision, ref.ContentSha = hs[0].Head, hs[0].ContentSha
				return ref, nil
			}
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		yes := true
		ref.Draft = &yes
		ref.Revision, err = q.db().DefHeadNumber(q.ctx(), db.DefHeadNumberParams{Kind: kind, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
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
	return headTextsOf(q, kind, "")
}

// headTextsOf: the current definitions of a kind (id "" for all) with their head content. Outside a transaction (long
// operations) it reads in its own snapshot.
func headTextsOf(q *Request, kind, id string) ([]headRow, error) {
	var out []headRow
	read := func(d *db.Queries) error {
		rs, err := d.DefHeadTexts(q.ctx(), db.DefHeadTextsParams{Kind: kind, ID: optStr(id)})
		for _, r := range rs {
			out = append(out, headRow{id: r.ID, text: []byte(r.Content)})
		}
		return err
	}
	if q.tx != nil {
		return out, read(q.db())
	}
	return out, q.app.store.Tx(q.ctx(), nil, false, "", func(tx pgx.Tx) error { return read(db.New(tx)) })
}

func init() {
	op("definition_list", definitionList)
	op("definition_get", definitionGet)
	// Registered before pipelines.go and lab.go (file order), so pipeline and Lab work resume on reconciled definitions.
	onStart(func(ctx context.Context, a *App) {
		files, broken := shippedDefinitions()
		release := ownRelease().Version
		if release == "" {
			release = "unknown release"
		}
		if err := runTx(ctx, a, nil, true, GLOBAL, func(q *Request) error { return reconcileDefinitions(q, files, broken, release) }); err != nil {
			log.Fatalf("definitions: reconcile: %v", err)
		}
	})
}
