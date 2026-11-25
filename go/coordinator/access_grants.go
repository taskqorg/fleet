// Project resource access (docs/designs/fleet-project-access.md): access servers, grants, their approval card and the
// operations people and agents share. The provisioner's work is the access-work queue (access_work.go); its SSH side
// is access_ssh.go; grant states are access_states.go.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("access_server_add", accessServerAdd)
	op("access_server_check", accessServerCheck)
	op("access_grants", accessGrants)
	op("access_grant_ask", accessGrantAsk)
	op("access_grant_check", accessGrantCheck)
	op("access_grant_revoke", accessGrantRevoke)
}

var (
	accessServerID   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)
	accessAddress    = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?$`)
	accessDBName     = regexp.MustCompile(`^[A-Za-z0-9_$]{1,64}$`)
	accessPath       = regexp.MustCompile(`^/[A-Za-z0-9._/@+-]{1,255}$`)
	accessNameStrip  = regexp.MustCompile(`[^a-z0-9]+`)
	accessCallBudget = 3 * time.Minute
)

// accessGrantPayload: access_grant.payload.
type accessGrantPayload struct {
	Scope  contract.AccessScope        `json:"scope"`
	Reason string                      `json:"reason"`
	Probe  *contract.AccessCheckResult `json:"probe,omitempty"`
	Error  string                      `json:"error,omitempty"`
}

// accessServerPayload: access_server.payload.
type accessServerPayload struct {
	Checked *contract.AccessCheckResult `json:"checked,omitempty"`
}

// accessGrant: one grant row as the code reads it.
type accessGrant struct {
	db.AccessGrantGetRow
	accessGrantPayload
}

func accessGrantOf(r db.AccessGrantGetRow) accessGrant {
	g := accessGrant{AccessGrantGetRow: r}
	_ = json.Unmarshal(r.Payload, &g.accessGrantPayload)
	return g
}

// accessOSUser: the OS user of a project on its servers (shared by its grants there).
func accessOSUser(project string) string {
	s := strings.Trim(accessNameStrip.ReplaceAllString(strings.ToLower(project), "-"), "-")
	if len(s) > 24 {
		s = strings.TrimRight(s[:24], "-")
	}
	return "fleet-" + s
}

// accessDBUser: the database user of one grant: the project's name and a short hash of the grant.
func accessDBUser(project, grantID string) string {
	s := strings.Trim(accessNameStrip.ReplaceAllString(strings.ToLower(project), "_"), "_")
	if len(s) > 14 {
		s = strings.TrimRight(s[:14], "_")
	}
	sum := sha256.Sum256([]byte(grantID))
	return "fleet_" + s + "_" + hex.EncodeToString(sum[:3])
}

func accessKeyName(project, server string) string { return project + "@" + server }

// accessJumpLabel: the name of a project's key on a jump server (fleet-provision's label pattern).
func accessJumpLabel(project, server string) string {
	return strings.TrimPrefix(accessOSUser(project), "fleet-") + "@" + server
}

func accessPasswordName(g accessGrant) string {
	return accessKeyName(g.ProjectID, g.ServerID) + "/" + accessDBUser(g.ProjectID, g.ID) + ".pw"
}

// accessPathItems: a files grant's paths as fleet-provision takes them (ro:/path or rw:/path).
func accessPathItems(level string, scope contract.AccessScope) []string {
	mode := "ro"
	if level == "write" {
		mode = "rw"
	}
	out := make([]string, 0, len(scope.Paths))
	for _, p := range scope.Paths {
		out = append(out, mode+":"+p)
	}
	return out
}

// accessRecipe: what the provisioner runs for g, as the card and access_grants show it.
func accessRecipe(g accessGrant, via string) []string {
	user := accessOSUser(g.ProjectID)
	var out []string
	if via != "" {
		out = append(out, fmt.Sprintf("On %s: user fleet-jump may forward %s's key to %s only", via, user, g.ServerID))
	}
	switch g.Kind {
	case "files":
		mode := "read-only"
		if g.Level == "write" {
			mode = "read-write"
		}
		out = append(out, fmt.Sprintf("On %s: OS user %s, SFTP only, chrooted to /srv/fleet/%s; %s bind mounts of %s", g.ServerID, user, user, mode, strings.Join(g.Scope.Paths, ", ")))
	case "mysql":
		privs := "SELECT"
		if g.Level == "write" {
			privs = "SELECT, INSERT, UPDATE, DELETE"
		}
		on := make([]string, 0, len(g.Scope.Tables))
		for _, t := range g.Scope.Tables {
			on = append(on, deref(g.Scope.Database)+"."+t)
		}
		out = append(out,
			fmt.Sprintf("On %s: OS user %s, no shell, may forward only to 127.0.0.1:3306", g.ServerID, user),
			fmt.Sprintf("MySQL: CREATE USER %s@127.0.0.1; GRANT %s ON %s; nothing else", accessDBUser(g.ProjectID, g.ID), privs, strings.Join(on, ", ")))
	}
	probe := "Probe: reading works"
	if g.Level == "read" {
		probe += ", writing is refused"
	}
	return append(out, probe, "Credentials stay on PDS18 (fleet secrets); only fingerprints are stored")
}

func accessGrantView(g accessGrant, via string) contract.AccessGrantView {
	v := contract.AccessGrantView{Id: g.ID, ProjectId: g.ProjectID, Server: g.ServerID, Kind: contract.AccessKind(g.Kind), Level: contract.AccessLevel(g.Level),
		State: contract.AccessGrantState(g.State), RequestId: g.RequestID, AskedBy: g.AskedBy, QuestionId: g.QuestionID, Credential: g.Credential,
		Scope: g.Scope, Reason: g.Reason, Recipe: accessRecipe(g, via), User: accessOSUser(g.ProjectID), Probe: g.Probe, CreatedAt: g.CreatedAt.UnixMilli()}
	if g.ExpiresAt != nil {
		v.ExpiresAt = new(g.ExpiresAt.UnixMilli())
	}
	if g.Error != "" {
		v.Error = new(g.Error)
	}
	v.Connection = g.connection()
	return v
}

type accessServer struct {
	db.AccessServerGetRow
	accessServerPayload
}

func accessServerGet(q *Request, id string) (*accessServer, error) {
	r, err := q.db().AccessServerGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := &accessServer{AccessServerGetRow: r}
	_ = json.Unmarshal(r.Payload, &s.accessServerPayload)
	return s, nil
}

func (s *accessServer) hop() accessHop {
	return accessHop{ID: s.ID, Address: s.Address, HostKey: deref(s.HostKey)}
}

func (s *accessServer) view() contract.AccessServerView {
	v := contract.AccessServerView{Id: s.ID, Address: s.Address, Via: s.Via, HostKey: s.HostKey, Checked: s.Checked}
	if s.EnrolledAt != nil {
		v.EnrolledAt = new(s.EnrolledAt.UnixMilli())
	}
	return v
}

// accessHops: the way to server id: its jump server first when it names one.
func accessHops(q *Request, id string) ([]accessHop, *accessServer, error) {
	s, err := accessServerGet(q, id)
	if err != nil || s == nil {
		if err == nil {
			err = fail(404, "Unknown access server "+id)
		}
		return nil, nil, err
	}
	hops := []accessHop{s.hop()}
	if s.Via != nil {
		v, err := accessServerGet(q, *s.Via)
		if err != nil || v == nil {
			if err == nil {
				err = fail(409, "Jump server "+*s.Via+" is gone")
			}
			return nil, nil, err
		}
		hops = []accessHop{v.hop(), s.hop()}
	}
	return hops, s, nil
}

func accessHostPort(address string) string {
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	return net.JoinHostPort(address, "22")
}

// accessServerAdd: records a server and pins its host key; a server behind a jump server is allowed there first.
func accessServerAdd(q *Request, r *contract.AccessServerAddRequest) (contract.AccessServerAddReply, error) {
	var no contract.AccessServerAddReply
	if !accessServerID.MatchString(r.Id) || !accessAddress.MatchString(r.Address) || (r.Via != nil && !accessServerID.MatchString(*r.Via)) {
		return no, fail(400, "Expected id (a lowercase name), address (host or host:port) and an optional via (a server id)")
	}
	var old, via *accessServer
	if err := q.app.store.Read(q.ctx(), q, func(q *Request) (err error) {
		if old, err = accessServerGet(q, r.Id); err == nil && r.Via != nil {
			via, err = accessServerGet(q, *r.Via)
		}
		return
	}); err != nil {
		return no, err
	}
	if r.Via != nil && (via == nil || via.EnrolledAt == nil) {
		return no, fail(409, "Jump server "+*r.Via+" is not enrolled: add it, run its enroll command, then access_server_check")
	}
	if old != nil && (old.Address != r.Address || deref(old.Via) != deref(r.Via)) {
		return no, fail(409, "Server "+r.Id+" exists with address "+old.Address)
	}
	signer, err := accessKey("provisioner")
	if err != nil {
		return no, err
	}
	body, _ := accessPublic(signer)
	ctx, cancel := context.WithTimeout(q.ctx(), accessCallBudget)
	defer cancel()
	target := accessHop{ID: r.Id, Address: r.Address}
	hops := []accessHop{target}
	if via != nil {
		c, err := accessDial(ctx, []accessHop{via.hop()}, "root", signer, nil)
		if err != nil {
			return no, fail(409, err.Error())
		}
		_, err = accessRun(c, []string{"jump-allow", "provisioner@" + r.Id, body, accessHostPort(r.Address)}, "")
		c.Close()
		if err != nil {
			return no, fail(409, "On "+via.ID+": "+err.Error())
		}
		hops = []accessHop{via.hop(), target}
	}
	var learned string
	if _, err := accessDial(ctx, hops, "root", signer, &learned); !errors.Is(err, errHostKeyLearned) {
		if err == nil {
			err = errors.New("no host key presented")
		}
		return no, fail(409, "Could not learn the host key of "+r.Id+": "+err.Error())
	}
	var out contract.AccessServerView
	err = q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(q *Request) error {
		cur, err := accessServerGet(q, r.Id)
		if err != nil {
			return err
		}
		if cur != nil && deref(cur.HostKey) != learned {
			return fail(409, fmt.Sprintf("Server %s presents host key %s, pinned %s: remove the server before trusting a new key", r.Id, accessKeyFingerprint(learned), accessKeyFingerprint(deref(cur.HostKey))))
		}
		if cur == nil {
			if err := q.db().AccessServerInsert(q.ctx(), db.AccessServerInsertParams{ID: r.Id, Address: r.Address, Via: r.Via, HostKey: &learned}); err != nil {
				return err
			}
			if err := audit(q, "access-server:"+r.Id, "hostKey", nil, accessKeyFingerprint(learned), auditExtra{Note: r.Address}); err != nil {
				return err
			}
			if cur, err = accessServerGet(q, r.Id); err != nil {
				return err
			}
		}
		out = cur.view()
		return nil
	})
	out.Enroll = new(accessEnrollCommand(body))
	return out, err
}

// accessServerCheck: the provisioner logs in and runs version; the first success marks the server enrolled.
func accessServerCheck(q *Request, r *contract.AccessServerCheckRequest) (contract.AccessServerCheckReply, error) {
	var no contract.AccessServerCheckReply
	var hops []accessHop
	if err := q.app.store.Read(q.ctx(), q, func(q *Request) (err error) { hops, _, err = accessHops(q, r.Id); return }); err != nil {
		return no, err
	}
	res := contract.AccessCheckResult{At: nowMs()}
	signer, err := accessKey("provisioner")
	if err != nil {
		return no, err
	}
	ctx, cancel := context.WithTimeout(q.ctx(), accessCallBudget)
	defer cancel()
	if c, err := accessDial(ctx, hops, "root", signer, nil); err != nil {
		res.Error = new(err.Error())
	} else {
		v, err := accessRun(c, []string{"version"}, "")
		if err != nil {
			res.Error = new(err.Error())
		} else {
			res.Ok, res.Version = true, new(strings.TrimPrefix(v, "fleet-provision "))
			if *res.Version != fleetProvisionVersion() {
				res.Steps = []string{"fleet-provision " + *res.Version + " is installed, " + fleetProvisionVersion() + " is current: run the enroll command again"}
			}
			if out, err := accessRun(c, []string{"mysql-databases"}, ""); err == nil {
				for _, line := range strings.Split(out, "\n") {
					if name, ok := strings.CutPrefix(strings.TrimSpace(line), "database "); ok {
						res.Databases = append(res.Databases, name)
					}
				}
			}
		}
		c.Close() // before the write: no root login stays open behind a lock
	}
	var out contract.AccessServerView
	err = q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(q *Request) error {
		if err := q.db().AccessServerChecked(q.ctx(), db.AccessServerCheckedParams{ID: r.Id, Checked: jsonBytes(res), Ok: res.Ok}); err != nil {
			return err
		}
		s, err := accessServerGet(q, r.Id)
		if err == nil && s != nil {
			out = s.view()
		}
		return err
	})
	return out, err
}

func accessGrants(q *Request, r *contract.AccessGrantsRequest) (contract.AccessGrantsReply, error) {
	out := contract.AccessGrantsReply{Servers: []contract.AccessServerView{}, Grants: []contract.AccessGrantView{}}
	project := deref(r.ProjectId)
	if project != "" {
		if _, err := projectContext(q, project); err != nil {
			return out, err
		}
	}
	servers, err := q.db().AccessServerList(q.ctx())
	if err != nil {
		return out, err
	}
	via := map[string]string{}
	for _, s := range servers {
		v := accessServer{AccessServerGetRow: db.AccessServerGetRow(s)}
		_ = json.Unmarshal(s.Payload, &v.accessServerPayload)
		out.Servers = append(out.Servers, v.view())
		via[s.ID] = deref(s.Via)
	}
	rows, err := q.db().AccessGrantList(q.ctx(), project)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		g := accessGrantOf(db.AccessGrantGetRow(r))
		out.Grants = append(out.Grants, accessGrantView(g, via[g.ServerID]))
	}
	return out, nil
}

// accessScopeCheck: a scope the recipes can apply: mysql names a database and tables (or *), files absolute paths.
func accessScopeCheck(kind string, s *contract.AccessScope) error {
	switch kind {
	case "mysql":
		if s.Database == nil || !accessDBName.MatchString(*s.Database) || len(s.Tables) == 0 || len(s.Tables) > 200 || len(s.Paths) > 0 {
			return fail(400, "A mysql scope names database and tables (table names, or [\"*\"] for every table)")
		}
		for _, t := range s.Tables {
			if t == "*" && len(s.Tables) == 1 {
				continue
			}
			if !accessDBName.MatchString(t) {
				return fail(400, "Bad table name "+t)
			}
		}
	case "files":
		if len(s.Paths) == 0 || len(s.Paths) > 50 || s.Database != nil || len(s.Tables) > 0 {
			return fail(400, "A files scope names absolute paths")
		}
		for _, p := range s.Paths {
			if !accessPath.MatchString(p) || strings.Contains(p, "..") {
				return fail(400, "Bad path "+p)
			}
		}
	default:
		return fail(400, "Unknown kind "+kind)
	}
	return nil
}

func sameScope(a, b contract.AccessScope) bool {
	return deref(a.Database) == deref(b.Database) && slices.Equal(a.Tables, b.Tables) && slices.Equal(a.Paths, b.Paths)
}

// accessGrantAsk creates a grant asked with its approval card, or reads it again by requestId.
func accessGrantAsk(q *Request, r *contract.AccessGrantAskRequest) (contract.AccessGrantAskReply, error) {
	var no contract.AccessGrantAskReply
	reason := strings.TrimSpace(r.Reason)
	if r.RequestId == "" || len(r.RequestId) > 200 || reason == "" || len([]rune(reason)) > 1000 {
		return no, fail(400, "Expected requestId and a reason of at most 1000 characters")
	}
	if _, err := projectContext(q, r.ProjectId); err != nil {
		return no, err
	}
	if err := accessScopeCheck(string(r.Kind), &r.Scope); err != nil {
		return no, err
	}
	_, server, err := accessHops(q, r.Server)
	if err != nil {
		return no, err
	}
	via := deref(server.Via)
	prev, err := q.db().AccessGrantByRequest(q.ctx(), db.AccessGrantByRequestParams{ProjectID: r.ProjectId, RequestID: r.RequestId})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return no, err
	}
	payload := accessGrantPayload{Scope: r.Scope, Reason: reason}
	if err == nil {
		g := accessGrantOf(db.AccessGrantGetRow(prev))
		if g.ServerID == r.Server && g.Kind == string(r.Kind) && g.Level == string(r.Level) && sameScope(g.Scope, r.Scope) {
			return accessGrantView(g, via), nil
		}
		if g.State != "asked" || g.ServerID != r.Server || g.Kind != string(r.Kind) || g.Level != string(r.Level) {
			return no, fail(409, "Request "+r.RequestId+" already names grant "+g.ID+" ("+g.State+"): use a new requestId")
		}
		// A changed scope withdraws the old card and asks again: an approval covers exactly what it named.
		if g.QuestionID != nil {
			if err := questionsWithdrawKeep(q, *g.QuestionID); err != nil {
				return no, err
			}
		}
		g.accessGrantPayload = payload
		qid, err := accessAskCard(q, g, via)
		if err != nil {
			return no, err
		}
		if err := q.db().AccessGrantSetQuestion(q.ctx(), db.AccessGrantSetQuestionParams{ID: g.ID, QuestionID: &qid, Payload: jsonBytes(payload)}); err != nil {
			return no, err
		}
		g.QuestionID = &qid
		return accessGrantView(g, via), nil
	}
	g := accessGrant{AccessGrantGetRow: db.AccessGrantGetRow{ID: "grant:" + newID(), ProjectID: r.ProjectId, ServerID: r.Server, Kind: string(r.Kind), Level: string(r.Level),
		State: "asked", RequestID: r.RequestId, AskedBy: actor(q, "operator"), CreatedAt: time.Now()}, accessGrantPayload: payload}
	if err := accessGrantInsert(q, db.AccessGrantInsertParams{ID: g.ID, ProjectID: g.ProjectID, ServerID: g.ServerID, Kind: g.Kind, Level: g.Level,
		RequestID: g.RequestID, AskedBy: g.AskedBy, Payload: jsonBytes(payload)}); err != nil {
		return no, err
	}
	qid, err := accessAskCard(q, g, via)
	if err != nil {
		return no, err
	}
	if err := q.db().AccessGrantSetQuestion(q.ctx(), db.AccessGrantSetQuestionParams{ID: g.ID, QuestionID: &qid, Payload: jsonBytes(payload)}); err != nil {
		return no, err
	}
	g.QuestionID = &qid
	return accessGrantView(g, via), nil
}

// questionsWithdrawKeep withdraws a card without ending its grant (the grant asks again at once).
func questionsWithdrawKeep(q *Request, id string) error {
	_, err := questionTransition(q, id, questionEvent{To: "withdrawn", Withdrawal: jsonBytes(struct {
		Reason string `json:"reason"`
	}{"scope changed"})})
	return err
}

// accessAskCard: the owner's approval card for g (action access, owner only): the exact recipe, the project's data
// class and the reason. An agent's card also shows in its conversation.
func accessAskCard(q *Request, g accessGrant, via string) (string, error) {
	data := "internal"
	if raw := projectSettingsOf(q, g.ProjectID); raw != nil {
		var s struct {
			Data string `json:"data"`
		}
		lenientDecode(jsonBytes(raw), &s)
		if s.Data != "" {
			data = s.Data
		}
	}
	what := fmt.Sprintf("%s access to %s on %s for project %s (%s data): %s", g.Level, g.Kind, g.ServerID, g.ProjectID, data, g.Reason)
	scope := approvalScope{RequestID: g.ID, Action: "access", Risk: "radical", What: what, Where: g.ServerID}
	text := approvalText(scope, approverNames(approvers(q, g.ProjectID, "access"))) + "\n\n" + strings.Join(accessRecipe(g, via), "\n")
	ask := questionAsk{Kind: "operator_decision", Text: text, ProjectID: g.ProjectID, AskedBy: new(g.AskedBy), RequestID: new("access:" + g.ID),
		Options: jsonBytes([]string{approvalYes, approvalNo}), Data: jsonBytes(struct {
			Approval approvalScope `json:"approval"`
			Access   struct {
				Grant string `json:"grant"`
			} `json:"access"`
		}{Approval: scope, Access: struct {
			Grant string `json:"grant"`
		}{g.ID}})}
	if eid, ok := strings.CutPrefix(q.agent, "execution:"); ok {
		sid, err := q.db().ConnExecutionSession(q.ctx(), eid)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		ask.Kind, ask.ExecutionID = "agent_question", &eid
		if err == nil && sid != nil && *sid != "" {
			ask.SessionID = sid
		}
	}
	return askQuestionAs(q, ask)
}

// accessQuestionEnded: questionTransition's hook. Approve on an access card provisions its grant, Not now declines it,
// a withdrawal (but a scope change, which asks again) withdraws it.
func accessQuestionEnded(q *Request, questionID string, ev questionEvent) error {
	if ev.To != "answered" && ev.To != "withdrawn" {
		return nil
	}
	id, err := q.db().AccessGrantByQuestion(q.ctx(), &questionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	grant, err := q.db().AccessGrantLockState(q.ctx(), id)
	if err != nil || grant.State != "asked" {
		return err
	}
	switch {
	case ev.To == "answered" && deref(ev.Answer) == approvalYes:
		return accessQueue(q, id, "provisioning", "provision")
	case ev.To == "answered":
		return accessGrantTransition(q, id, "declined")
	case strings.Contains(string(ev.Withdrawal), "scope changed"):
		return nil
	default:
		return accessGrantTransition(q, id, "withdrawn")
	}
}

// accessQueue moves grant id to state (when given) and queues the provisioner's work op for it.
func accessQueue(q *Request, id, state, work string) error {
	if state != "" {
		if err := accessGrantTransition(q, id, state); err != nil {
			return err
		}
	}
	if err := q.db().AccessWorkPut(q.ctx(), db.AccessWorkPutParams{GrantID: id, Op: work}); err != nil {
		return err
	}
	if err := kitSchedule(q, "access-work", id, time.Now()); err != nil {
		return err
	}
	return kitNotify(q, "access-work")
}

func accessGrantGet(q *Request, id string) (accessGrant, string, error) {
	r, err := q.db().AccessGrantGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return accessGrant{}, "", fail(404, "Unknown grant")
	}
	if err != nil {
		return accessGrant{}, "", err
	}
	g := accessGrantOf(r)
	s, err := accessServerGet(q, g.ServerID)
	if err != nil || s == nil {
		return g, "", err
	}
	return g, deref(s.Via), nil
}

// accessGrantCheck: a ready grant is probed again; a failed one is provisioned again.
func accessGrantCheck(q *Request, r *contract.AccessGrantCheckRequest) (contract.AccessGrantCheckReply, error) {
	g, via, err := accessGrantGet(q, r.Grant)
	if err != nil {
		return contract.AccessGrantCheckReply{}, err
	}
	switch g.State {
	case "ready":
		err = accessQueue(q, g.ID, "", "probe")
	case "failed":
		err = accessQueue(q, g.ID, "provisioning", "provision")
		g.State = "provisioning"
	default:
		err = fail(409, "Grant "+g.ID+" is "+g.State+": only a ready or failed grant is checked")
	}
	return accessGrantView(g, via), err
}

// accessGrantRevoke: an asked grant is withdrawn with its card; a provisioned one is revoked on the server.
func accessGrantRevoke(q *Request, r *contract.AccessGrantRevokeRequest) (contract.AccessGrantRevokeReply, error) {
	g, via, err := accessGrantGet(q, r.Grant)
	if err != nil {
		return contract.AccessGrantRevokeReply{}, err
	}
	switch g.State {
	case "asked":
		if g.QuestionID != nil {
			err = questionsWithdraw(q, []string{*g.QuestionID})
		}
		if err == nil {
			err = accessGrantTransition(q, g.ID, "withdrawn")
		}
		g.State = "withdrawn"
	case "provisioning", "ready", "failed":
		err = accessQueue(q, g.ID, "revoking", "revoke")
		g.State = "revoking"
	case "revoking": // a revoke that failed runs again
		err = accessQueue(q, g.ID, "", "revoke")
	default:
		err = fail(409, "Grant "+g.ID+" is "+g.State)
	}
	return accessGrantView(g, via), err
}
