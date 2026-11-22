// API connection operations and configuration endpoints (connections_*).
package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"slices"
	"sort"
	"strings"

	"fleet/contract"
	"github.com/jackc/pgx/v5"
)

func init() {
	// Agent dispatch mints the run token for the profile's connections (dispatch.go).
	op("connections_list", func(q *Request, _ *contract.ConnectionsListRequest) (contract.ConnectionsListReply, error) {
		out, err := connectionsList(q)
		return replyAs[contract.ConnectionsListReply](out, err)
	})
	op("connections_create", func(q *Request, r *contract.ConnectionsCreateRequest) (contract.ConnectionsCreateReply, error) {
		out, err := connectionsCreate(q, r)
		return replyAs[contract.ConnectionsCreateReply](out, err)
	})
	op("connections_key_add", func(q *Request, r *contract.ConnectionsKeyAddRequest) (contract.ConnectionsKeyAddReply, error) {
		out, err := connectionsKeyAdd(q, r)
		return replyAs[contract.ConnectionsKeyAddReply](out, err)
	})
	op("connections_key_request", func(q *Request, r *contract.ConnectionsKeyRequestRequest) (contract.ConnectionsKeyRequestReply, error) {
		out, err := connectionsKeyRequest(q, r)
		return replyAs[contract.ConnectionsKeyRequestReply](out, err)
	})
	op("connections_delete", connectionsDelete)
	op("connections_key_state", func(q *Request, r *contract.ConnectionsKeyStateRequest) (contract.ConnectionsKeyStateReply, error) {
		out, err := connectionsKeyState(q, r)
		return replyAs[contract.ConnectionsKeyStateReply](out, err)
	})
	op("connections_set_tools", func(q *Request, r *contract.ConnectionsSetToolsRequest) (contract.ConnectionsSetToolsReply, error) {
		out, err := connectionsSetTools(q, r)
		return replyAs[contract.ConnectionsSetToolsReply](out, err)
	})
	op("connections_set_access", func(q *Request, r *contract.ConnectionsSetAccessRequest) (contract.ConnectionsSetAccessReply, error) {
		out, err := connectionsSetAccess(q, r)
		return replyAs[contract.ConnectionsSetAccessReply](out, err)
	})
	op("connections_set_intake", func(q *Request, r *contract.ConnectionsSetIntakeRequest) (contract.ConnectionsSetIntakeReply, error) {
		out, err := connectionsSetIntake(q, r)
		return replyAs[contract.ConnectionsSetIntakeReply](out, err)
	})
	op("connections_set_limits", func(q *Request, r *contract.ConnectionsSetLimitsRequest) (contract.ConnectionsSetLimitsReply, error) {
		out, err := connectionsSetLimits(q, r)
		return replyAs[contract.ConnectionsSetLimitsReply](out, err)
	})
	op("connections_set_key_policy", func(q *Request, r *contract.ConnectionsSetKeyPolicyRequest) (contract.ConnectionsSetKeyPolicyReply, error) {
		out, err := connectionsSetKeyPolicy(q, r)
		return replyAs[contract.ConnectionsSetKeyPolicyReply](out, err)
	})
	op("connections_token_create", func(q *Request, r *contract.ConnectionsTokenCreateRequest) (contract.ConnectionsTokenCreateReply, error) {
		out, err := connectionsTokenCreate(q, r)
		return replyAs[contract.ConnectionsTokenCreateReply](out, err)
	})
	op("connections_token_list", func(q *Request, _ *contract.ConnectionsTokenListRequest) (contract.ConnectionsTokenListReply, error) {
		return connectionsTokenList(q)
	})
	op("connections_token_revoke", connectionsTokenRevoke)
}

// connectionView: a connection as connections_list shows it.
type connectionView struct {
	apiConnection
	Keys    []connKey         `json:"keys"`
	Spend   connSpend         `json:"spend"`
	History []json.RawMessage `json:"history,omitempty"`
}

type connSpend struct {
	Basis string `json:"basis"`
}

func connectionsList(q *Request) (any, error) {
	keys, err := connKeysList(q, nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].AddedAt < keys[j].AddedAt })
	connections, err := apiConnectionAll(q)
	if err != nil {
		return nil, err
	}
	keysByConn := make(map[string][]connKey, len(connections))
	for _, k := range keys {
		keysByConn[k.ConnectionID] = append(keysByConn[k.ConnectionID], k)
	}
	out := make([]connectionView, 0, len(connections))
	for _, c := range connections {
		ks := keysByConn[c.Name]
		if ks == nil {
			ks = []connKey{}
		}
		out = append(out, connectionView{apiConnection: c, Keys: ks, Spend: connSpend{Basis: "unavailable"}})
	}
	return struct {
		Providers   map[string]json.RawMessage `json:"providers"`
		Connections []connectionView           `json:"connections"`
	}{providerCatalogue, out}, nil
}

func connectionsCreate(q *Request, r *contract.ConnectionsCreateRequest) (any, error) {
	name, provider := r.Name, r.Provider
	if _, known := gatewayProviders[provider]; !connName.MatchString(name) || !known {
		return nil, fail(400, "Expected name and a known provider")
	}
	if slices.Contains(contract.ExecutorMCPServers, name) {
		return nil, fail(400, "Connection name "+name+" is an executor MCP server name")
	}
	if old, err := apiConnectionMaybe(q, "connection:"+name); err != nil {
		return nil, err
	} else if old != nil {
		return nil, fail(409, "API key name exists")
	}
	holder := directorOf(q)
	if r.Holder != nil {
		holder = strings.ToLower(*r.Holder)
	}
	if q.person != nil && !fullAccess(q.person) && holder != q.person.Email {
		return nil, fail(403, "Only the owner creates an API key for another person")
	}
	comment := deref(r.Comment)
	if len(comment) > 2000 {
		comment = comment[:2000]
	}
	c := apiConnection{ID: "connection:" + name, Name: name, Provider: provider, Holder: holder, Access: connAccess{}.stored(),
		Intake: "all", Tools: toolNames(provider), Comment: comment, CreatedAt: jsonBytes(nowMs())}
	if err := apiConnectionPut(q, c); err != nil {
		return nil, err
	}
	publish(q)
	return c, nil
}

// connectionsDelete (§1, owner) once every key is retired (no value file left), so a test connection can go. Run tokens
// drop the name (a new connection with it is not granted to them), open key requests are withdrawn, the history moves to
// an audit row that a new connection of the same name does not inherit.
func connectionsDelete(q *Request, r *contract.ConnectionsDeleteRequest) (contract.ConnectionsDeleteReply, error) {
	var no contract.ConnectionsDeleteReply
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return no, err
	}
	name := c.Name
	if _, err := q.db().APIConnectionByIDLocked(q.ctx(), c.ID); errors.Is(err, pgx.ErrNoRows) {
		return no, errNotFound
	} else if err != nil {
		return no, err
	}
	keys, err := connKeys(q, name)
	if err != nil {
		return no, err
	}
	for _, k := range keys {
		if k.State != "retired" {
			return no, fail(409, "Retire every key of "+name+" first")
		}
	}
	toks, err := runTokensAll(q)
	if err != nil {
		return no, err
	}
	for _, t := range toks {
		if slices.Contains(t.Connections, name) {
			t.Connections = slices.DeleteFunc(t.Connections, func(n string) bool { return n == name })
			if err := runTokenPut(q, t); err != nil {
				return no, err
			}
		}
	}
	if err := q.db().DeleteAPIConnection(q.ctx(), c.ID); err != nil {
		return no, err
	}
	if c.ID != "" {
		secrets, err := q.db().ConnOpenKeyRequests(q.ctx(), name)
		if err != nil {
			return no, err
		}
		if err := questionsWithdraw(q, secrets); err != nil {
			return no, err
		}
	}
	if err := audit(q, "deleted-connection:"+name, "connection", nil, "deleted", auditExtra{}); err != nil {
		return no, err
	}
	if dir, err := connDir(name); err == nil {
		afterCommit(q, func() { // every key is retired: only stale stages can remain, and connSync must not re-register them
			if err := os.RemoveAll(dir); err != nil {
				log.Printf("connection %s: %v", name, err)
			}
		})
	}
	publish(q)
	return contract.ConnectionsDeleteReply{Name: name, Deleted: true}, nil
}

// connectionsSetTools (owner): the explicit provider tool grant.
func connectionsSetTools(q *Request, r *contract.ConnectionsSetToolsRequest) (any, error) {
	if r.Connection != "" {
		if _, err := q.db().APIConnectionByIDLocked(q.ctx(), "connection:"+r.Connection); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	seen, tools := map[string]bool{}, []string{}
	for _, name := range r.Tools {
		if _, known := gatewayProviders[c.Provider].Tools[name]; name == "" || seen[name] || !known {
			return nil, fail(400, "tools must be unique recognized provider tool names")
		}
		seen[name] = true
		tools = append(tools, name)
	}
	if js(tools) != js(c.Tools) {
		if err := connChange(q, c, "tools", c.Tools, tools); err != nil {
			return nil, err
		}
		c.Tools = tools
		if err := connUpdated(q, c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func connectionsSetAccess(q *Request, r *contract.ConnectionsSetAccessRequest) (any, error) {
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	people, err := idList(r.Access.People, "people")
	if err != nil {
		return nil, err
	}
	for i, p := range people {
		people[i] = strings.ToLower(p)
	}
	projects, err := idList(r.Access.Projects, "projects")
	if err != nil {
		return nil, err
	}
	exps, err := idList(r.Access.Experiments, "experiments")
	if err != nil {
		return nil, err
	}
	next, prev := connAccess{People: people, Projects: projects, Experiments: exps}.stored(), c.Access.stored()
	if js(next) != js(prev) {
		if err := connChange(q, c, "access", c.Access, next); err != nil {
			return nil, err
		}
		c.Access = next
		if err := connUpdated(q, c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func connectionsSetIntake(q *Request, r *contract.ConnectionsSetIntakeRequest) (any, error) {
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	in := string(r.Intake)
	if c.Intake != in {
		if err := connChange(q, c, "intake", c.Intake, in); err != nil {
			return nil, err
		}
		c.Intake = in
		if err := connUpdated(q, c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// connectionsSetLimits: file tool limits (§9); the stored object is replaced as a whole; an omitted field takes the
// gateway's default.
func connectionsSetLimits(q *Request, r *contract.ConnectionsSetLimitsRequest) (any, error) {
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	for _, f := range []struct {
		name string
		v    *int64
	}{{"fileBytes", r.Limits.FileBytes}, {"replyBytes", r.Limits.ReplyBytes}, {"stallMs", r.Limits.StallMs}, {"answerMs", r.Limits.AnswerMs}} {
		if f.v != nil && (*f.v < 1 || *f.v > 1<<53) {
			return nil, fail(400, "limits."+f.name+" must be a positive integer")
		}
	}
	next := r.Limits
	previous := json.RawMessage("{}")
	if !isNull(c.Limits) {
		previous = c.Limits
	}
	if stored, err := c.limits(); err != nil || js(next) != js(stored) { // the fields are pointers: compared as written
		if err := connChange(q, c, "limits", previous, next); err != nil {
			return nil, err
		}
		c.Limits = nil
		if next != (contract.GatewayLimits{}) {
			c.Limits = jsonBytes(next)
		}
		if err := connUpdated(q, c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// connectionsSetKeyPolicy: key balancing and failover policy; the stored object is replaced as a whole; an omitted
// field takes the gateway's default.
func connectionsSetKeyPolicy(q *Request, r *contract.ConnectionsSetKeyPolicyRequest) (any, error) {
	c, err := connNamed(q, r.Connection)
	if err != nil {
		return nil, err
	}
	next := r.KeyPolicy
	if next.Strategy != nil {
		if *next.Strategy == "" {
			next.Strategy = nil
		} else if !next.Strategy.Valid() {
			return nil, fail(400, "keyPolicy.strategy must be sticky, round_robin or least_used")
		}
	}
	if len(next.FailoverStatuses) > 0 {
		seen := make(map[int64]bool, len(next.FailoverStatuses))
		statuses := make([]int64, 0, len(next.FailoverStatuses))
		for _, s := range next.FailoverStatuses {
			if s < 400 || s > 599 {
				return nil, fail(400, "keyPolicy.failoverStatuses must be HTTP status codes between 400 and 599")
			}
			if !seen[s] {
				seen[s] = true
				statuses = append(statuses, s)
			}
		}
		next.FailoverStatuses = statuses
	} else {
		next.FailoverStatuses = nil
	}
	for _, f := range []struct {
		name string
		v    *int64
	}{{"cooldownSeconds", next.CooldownSeconds}, {"quotaCooldownSeconds", next.QuotaCooldownSeconds}} {
		if f.v != nil && (*f.v < 1 || *f.v > 1<<53) {
			return nil, fail(400, "keyPolicy."+f.name+" must be a positive integer")
		}
	}
	previous := json.RawMessage("{}")
	if !isNull(c.KeyPolicy) {
		previous = c.KeyPolicy
	}
	if stored, err := c.keyPolicy(); err != nil || js(next) != js(stored) {
		if err := connChange(q, c, "keyPolicy", previous, next); err != nil {
			return nil, err
		}
		c.KeyPolicy = nil
		if !keyPolicyEmpty(next) {
			c.KeyPolicy = jsonBytes(next)
		}
		if err := connUpdated(q, c); err != nil {
			return nil, err
		}
	}
	return c, nil
}
