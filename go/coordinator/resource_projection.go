// Prepared project resources. Source capture is unconditional; this maintenance pass is opt-in.
package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
	"fleet/obs"
	"github.com/jackc/pgx/v5"
)

var errResourceScope = errors.New("resource scope expanded")
var errResourceOwnership = errors.New("ownership_mismatch")
var errResourceClearing = fmt.Errorf("resource build clearing: %w", errKitSkip)

// resourceSourceSQL reads source, classification, old contribution and retained valuation in ONE
// statement snapshot. Only work rows are claimed; no source row is locked by replacement.
var resourceSourceSQL = `WITH leaf AS MATERIALIZED (` + resourceBaseLeavesSQL() + ` WHERE e.id=$1)
 SELECT jsonb_build_object(
 'leaf',(SELECT to_jsonb(l) || jsonb_build_object(
  'started_ms',CASE WHEN l.started_at IS NOT NULL THEN (extract(epoch FROM l.started_at)*1000)::bigint END,
  'ended_ms',CASE WHEN l.ended_at IS NOT NULL THEN (extract(epoch FROM l.ended_at)*1000)::bigint END,
  'turn_count',t.turn_count,'turn_count_basis',t.turn_count_basis,'cpu_missing_reason',t.cpu_missing_reason,
  'memory_missing_reason',t.memory_missing_reason,'history_incomplete',coalesce(t.history_incomplete,0))
  FROM leaf l LEFT JOIN execution_totals t ON t.execution_id=l.id),
 'fingerprint',(SELECT jsonb_build_array(resource_execution_facts(e),
  to_jsonb(t)-ARRAY['computed_at','rate_limit_last','rate_limit_last_basis'],l.provider,l.is_sub)
  FROM execution e JOIN leaf l ON l.id=e.id LEFT JOIN execution_totals t ON t.execution_id=e.id),
 'scopes',(SELECT coalesce(jsonb_agg(p ORDER BY p COLLATE "C"),'[]') FROM (SELECT project_id p FROM execution WHERE id=$1 UNION SELECT project_id FROM execution_totals WHERE execution_id=$1 UNION SELECT project_id FROM resource_contribution WHERE execution_id=$1 UNION SELECT ws.project_id FROM resource_work_scope ws JOIN resource_work w USING(work_id) WHERE w.execution_id=$1) scopes WHERE p IS NOT NULL),
 'valuationInput',EXISTS(SELECT 1 FROM leaf l WHERE l.leaf_at IS NOT NULL AND (
  (nullif(l.model,'') IS NOT NULL AND l.model NOT LIKE '%-latest' AND coalesce(l.tokens_input,l.tokens_output,l.tokens_cache_read,l.tokens_cache_write) IS NOT NULL)
  OR l.direct_amount IS NOT NULL OR (l.conn_amount IS NOT NULL AND nullif(l.conn_unit,'') IS NOT NULL))),
 'mismatch',EXISTS(SELECT 1 FROM execution e JOIN execution_totals t ON t.execution_id=e.id WHERE e.id=$1 AND e.project_id IS DISTINCT FROM t.project_id),
 'old',(SELECT to_jsonb(c) FROM resource_contribution c WHERE c.execution_id=$1),
 'valuation',(SELECT method FROM resource_valuation WHERE execution_id=$1),
 'candidates',coalesce((SELECT jsonb_agg(jsonb_build_object('subject',u.subject,'unit',u.unit,
  'priceId',p.id,'rate',p.price::text,'currency',p.currency,'observedAt',(extract(epoch FROM p.observed_at)*1000)::bigint)
  ORDER BY u.subject COLLATE "C",u.unit COLLATE "C") FROM leaf l
  CROSS JOIN LATERAL (VALUES(coalesce(l.model,''),'input_tokens'),(coalesce(l.model,''),'output_tokens'),
   (coalesce(l.model,''),'cache_read_tokens'),(coalesce(l.model,''),'cache_write_tokens'),(l.conn_subject,coalesce(l.conn_unit,''))) u(subject,unit)
  LEFT JOIN LATERAL (SELECT id,price,currency,observed_at FROM price_entry p
   WHERE p.subject=u.subject AND p.unit=u.unit AND p.effective_from<=l.leaf_at
    AND NOT EXISTS(SELECT 1 FROM resource_valuation WHERE execution_id=$1)
   ORDER BY p.effective_from DESC,p.seq DESC LIMIT 1) p ON true),'[]'))`

// The maintenance reader uses direct non-secret metadata joins so classification shares the
// parent statement snapshot; the legacy SECURITY DEFINER SPI helper can take fresh snapshots.
func resourceBaseLeavesSQL() string {
	s := strings.ReplaceAll(analyticsBaseLeavesSelect, "::float8", "::numeric")
	return strings.Replace(s, `SELECT m.provider, m.is_api_conn
  FROM fleet_exec_conn_meta(e.data->>'connection', e.account_id) m
  WHERE e.data ? 'connection' OR e.account_id IS NOT NULL`,
		`SELECT coalesce(
   (SELECT data->>'provider' FROM document WHERE kind='api-connection' AND id='connection:'||(e.data->>'connection')),
   (SELECT data->>'provider' FROM document WHERE kind='api-connection' AND id=e.account_id)) AS provider,
   EXISTS(SELECT 1 FROM document WHERE kind='api-connection' AND id=e.account_id) AS is_api_conn`, 1)
}

type resourceSnapshot struct {
	Leaf           json.RawMessage `json:"leaf"`
	Fingerprint    json.RawMessage `json:"fingerprint"`
	ValuationInput bool            `json:"valuationInput"`
	Mismatch       bool            `json:"mismatch"`
	Scopes         []string        `json:"scopes"`
	Old            *struct {
		Facts     contract.ResourceContributionV1 `json:"facts"`
		SourceSHA string                          `json:"source_sha"`
		MethodSHA string                          `json:"method_sha"`
	} `json:"old"`
	Valuation  *contract.ResourceMethodV1           `json:"valuation"`
	Candidates []contract.ResourceMethodComponentV1 `json:"candidates"`
}

func resourceSHA(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// resourceLeaf: one source leaf as resourceSourceSQL reads it (analyticsBaseLeavesSelect with exact numerics, plus the
// totals fields it adds). A number stays its exact decimal text; a missing value is nil.
type resourceLeaf struct {
	ProjectID           *string            `json:"project_id"`
	Kind                string             `json:"kind"`
	State               string             `json:"state"`
	HasTotals           bool               `json:"has_totals"`
	SystemActor         *string            `json:"system_actor"`
	StartedMs           *json.Number       `json:"started_ms"`
	EndedMs             *json.Number       `json:"ended_ms"`
	Model               *string            `json:"model"`
	Provider            *string            `json:"provider"`
	Connection          *string            `json:"connection"`
	ToolName            *string            `json:"tool_name"`
	IsSub               bool               `json:"is_sub"`
	TokensInput         *json.Number       `json:"tokens_input"`
	TokensInputBasis    string             `json:"tokens_input_basis"`
	TokensOutput        *json.Number       `json:"tokens_output"`
	TokensOutputBasis   string             `json:"tokens_output_basis"`
	TokensCacheRead     *json.Number       `json:"tokens_cache_read"`
	TokensCacheRBasis   string             `json:"tokens_cache_read_basis"`
	TokensCacheWrite    *json.Number       `json:"tokens_cache_write"`
	TokensCacheWBasis   string             `json:"tokens_cache_write_basis"`
	TokensReasoning     *json.Number       `json:"tokens_reasoning"`
	TokensReasonBasis   string             `json:"tokens_reasoning_basis"`
	WallMs              *json.Number       `json:"wall_ms"`
	WallMsBasis         string             `json:"wall_ms_basis"`
	ActiveMs            *json.Number       `json:"active_ms"`
	ActiveMsBasis       string             `json:"active_ms_basis"`
	ApiMs               *json.Number       `json:"api_ms"`
	ApiMsBasis          string             `json:"api_ms_basis"`
	CpuMs               *json.Number       `json:"cpu_ms"`
	CpuMsBasis          string             `json:"cpu_ms_basis"`
	CpuMissingReason    *string            `json:"cpu_missing_reason"`
	PeakMemory          *json.Number       `json:"peak_memory_bytes"`
	PeakMemoryBasis     string             `json:"peak_memory_bytes_basis"`
	MemoryMissingReason *string            `json:"memory_missing_reason"`
	TurnCount           *json.Number       `json:"turn_count"`
	TurnCountBasis      string             `json:"turn_count_basis"`
	Credits             *json.Number       `json:"credits"`
	ConnectionCost      *json.RawMessage   `json:"connection_cost"`
	ConnectionCostBasis string             `json:"connection_cost_basis"`
	HistoryIncomplete   resourceCount      `json:"history_incomplete"`
	ToolCalls           *resourceToolCalls `json:"tool_calls"`
	ToolCallsBasis      string             `json:"tool_calls_basis"`
	DirectCurrency      *string            `json:"direct_currency"`
	DirectAmount        *json.Number       `json:"direct_amount"`
	ConnSubject         string             `json:"conn_subject"`
	ConnUnit            *string            `json:"conn_unit"`
	ConnAmount          *json.Number       `json:"conn_amount"`
}

// resourceToolCalls: execution_totals.tool_calls. Its shape is not enforced at write time, so any part that is not the
// expected object or integer reads as empty or 0.
type resourceToolCalls struct {
	ByTool resourceByTool    `json:"byTool"`
	Other  resourceCallCount `json:"other"`
}

type resourceByTool map[string]resourceCallCount

type resourceCallCount struct {
	Calls  resourceCount `json:"calls"`
	Errors resourceCount `json:"errors"`
}

// resourceCount: a JSON integer, or 0 for anything else.
type resourceCount int64

func (n *resourceCount) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		v = 0
	}
	*n = resourceCount(v)
	return nil
}

func (t *resourceToolCalls) UnmarshalJSON(b []byte) error {
	type plain resourceToolCalls
	var p plain
	if json.Unmarshal(b, &p) != nil {
		p = plain{}
	}
	*t = resourceToolCalls(p)
	return nil
}

func (m *resourceByTool) UnmarshalJSON(b []byte) error {
	var v map[string]json.RawMessage
	*m = resourceByTool{}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	for name, raw := range v {
		var c resourceCallCount
		_ = json.Unmarshal(raw, &c)
		(*m)[name] = c
	}
	return nil
}

func (c *resourceCallCount) UnmarshalJSON(b []byte) error {
	type plain resourceCallCount
	var p plain
	if json.Unmarshal(b, &p) != nil {
		p = plain{}
	}
	*c = resourceCallCount(p)
	return nil
}

// resourceDecimal: the exact decimal text of n, or nil.
func resourceDecimal(n *json.Number) *string {
	if n == nil {
		return nil
	}
	s := string(*n)
	return &s
}

// resourceInteger: n as an integer, or nil when it is missing or not one.
func resourceInteger(n *json.Number) *int64 {
	if n == nil {
		return nil
	}
	i, err := strconv.ParseInt(string(*n), 10, 64)
	if err != nil {
		return nil
	}
	return &i
}

func resourceBasis(s string) contract.ResourceBasis {
	b := contract.ResourceBasis(s)
	if !b.Valid() {
		return contract.ResourceBasisUnavailable
	}
	return b
}

// decimalArithmetic stays exact, including scientific-notation inputs. Every accepted source/rate
// has a finite decimal expansion; no float conversion or arbitrary precision rounding is used.
func decimalArithmetic(a, b string, multiply bool) (string, error) {
	x, ok := new(big.Rat).SetString(a)
	if !ok {
		return "", fmt.Errorf("invalid resource decimal")
	}
	y, ok := new(big.Rat).SetString(b)
	if !ok {
		return "", fmt.Errorf("invalid resource decimal")
	}
	if multiply {
		x.Mul(x, y)
	} else {
		x.Add(x, y)
	}
	den := new(big.Int).Set(x.Denom())
	two, five := 0, 0
	for den.Bit(0) == 0 {
		den.Rsh(den, 1)
		two++
	}
	rem := new(big.Int)
	for {
		q := new(big.Int)
		q.QuoRem(den, big.NewInt(5), rem)
		if rem.Sign() != 0 {
			break
		}
		den = q
		five++
	}
	if den.Cmp(big.NewInt(1)) != 0 {
		return "", errors.New("non-decimal resource value")
	}
	s := x.FloatString(max(two, five))
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return s, nil
}

func resourceContribution(id string, s resourceSnapshot) (contract.ResourceContributionV1, contract.ResourceMethodV1, error) {
	c := contract.ResourceContributionV1{SchemaVersion: 1, ExecutionId: id, Metrics: map[string]contract.ResourceMetricV1{}, Tools: []contract.ResourceToolV1{}, Costs: []contract.ResourceCostV1{}}
	var l *resourceLeaf
	if err := json.Unmarshal(s.Leaf, &l); err != nil {
		return c, contract.ResourceMethodV1{}, err
	}
	if l == nil {
		return c, contract.ResourceMethodV1{}, nil
	}
	c.ProjectId = l.ProjectID
	c.Kind = l.Kind
	c.State = l.State
	c.HasTotals = l.HasTotals
	// Fleet's own background calls (system_actor) are not the project's work (docs/designs/fleet-analytics.md, System activity).
	c.Included = c.ProjectId != nil && c.Kind != "pipeline_run" && c.Kind != "pipeline_step" && (c.Kind != "child_agent" || c.HasTotals) && l.SystemActor == nil
	c.StartedMs = resourceInteger(l.StartedMs)
	c.EndedMs = resourceInteger(l.EndedMs)
	c.TokenActor = c.Kind == "agent_run" || c.Kind == "child_agent" || l.TokensInput != nil || l.TokensOutput != nil || l.TokensCacheRead != nil || l.TokensCacheWrite != nil || l.TokensReasoning != nil
	c.ProcessActor = c.Kind != "tool_run"
	c.ToolActor = c.Kind == "agent_run" || c.Kind == "child_agent" || c.Kind == "tool_run"
	c.HistoryIncomplete = l.HistoryIncomplete != 0
	for _, m := range []struct {
		name     string
		value    *json.Number
		basis    string
		reason   *string
		expected bool
	}{
		{"input", l.TokensInput, l.TokensInputBasis, nil, c.TokenActor}, {"output", l.TokensOutput, l.TokensOutputBasis, nil, c.TokenActor},
		{"cacheRead", l.TokensCacheRead, l.TokensCacheRBasis, nil, c.TokenActor}, {"cacheWrite", l.TokensCacheWrite, l.TokensCacheWBasis, nil, c.TokenActor},
		{"reasoning", l.TokensReasoning, l.TokensReasonBasis, nil, c.TokenActor},
		{"wallMs", l.WallMs, l.WallMsBasis, nil, true}, {"activeMs", l.ActiveMs, l.ActiveMsBasis, nil, true}, {"apiMs", l.ApiMs, l.ApiMsBasis, nil, true},
		{"cpuMs", l.CpuMs, l.CpuMsBasis, l.CpuMissingReason, c.ProcessActor}, {"peakMemoryBytes", l.PeakMemory, l.PeakMemoryBasis, l.MemoryMissingReason, c.ProcessActor},
		{"turns", l.TurnCount, l.TurnCountBasis, nil, true}, {"credits", l.Credits, l.ConnectionCostBasis, nil, false},
	} {
		v := resourceDecimal(m.value)
		if !m.expected && m.name != "credits" {
			v = nil
		}
		b := resourceBasis(m.basis)
		if v == nil {
			b = contract.ResourceBasisUnavailable
		}
		c.Metrics[m.name] = contract.ResourceMetricV1{Value: v, Basis: b, Expected: m.expected, Reason: m.reason}
	}
	if c.Kind == "tool_run" {
		name := deref(l.ToolName)
		if name == "" {
			name = "tool_run"
		}
		e := int64(0)
		if c.State == "failed" || c.State == "cancelled" {
			e = 1
		}
		c.Tools = append(c.Tools, contract.ResourceToolV1{Connection: deref(l.Connection), Name: name, Calls: 1, Errors: e})
	} else if c.ToolActor && l.ToolCalls != nil {
		for name, v := range l.ToolCalls.ByTool {
			c.Tools = append(c.Tools, contract.ResourceToolV1{Name: name, Calls: int64(v.Calls), Errors: int64(v.Errors)})
		}
		c.OtherCalls = int64(l.ToolCalls.Other.Calls)
		c.OtherErrors = int64(l.ToolCalls.Other.Errors)
	}
	sort.Slice(c.Tools, func(i, j int) bool {
		if c.Tools[i].Name != c.Tools[j].Name {
			return c.Tools[i].Name < c.Tools[j].Name
		}
		return c.Tools[i].Connection < c.Tools[j].Connection
	})
	calls, errs := big.NewInt(c.OtherCalls), big.NewInt(c.OtherErrors)
	for _, t := range c.Tools {
		calls.Add(calls, big.NewInt(t.Calls))
		errs.Add(errs, big.NewInt(t.Errors))
	}
	tb := resourceBasis(l.ToolCallsBasis)
	present := c.ToolActor && (c.Kind == "tool_run" || l.ToolCalls != nil)
	if c.Kind == "tool_run" && tb == contract.ResourceBasisUnavailable {
		tb = contract.ResourceBasisMeasured
	}
	for k, v := range map[string]string{"toolCalls": calls.String(), "toolErrors": errs.String()} {
		m := contract.ResourceMetricV1{Expected: c.ToolActor, Basis: tb}
		if present {
			m.Value = ptr(v)
		} else {
			m.Basis = contract.ResourceBasisUnavailable
		}
		c.Metrics[k] = m
	}
	for k, v := range map[string]bool{"tokenActors": c.TokenActor, "processActors": c.ProcessActor, "toolActors": c.ToolActor} {
		n := "0"
		if v {
			n = "1"
		}
		c.Metrics[k] = contract.ResourceMetricV1{Value: &n, Basis: contract.ResourceBasisMeasured, Expected: true}
	}
	method := contract.ResourceMethodV1{SchemaVersion: 1, Model: l.Model, Connection: l.Connection, Provider: l.Provider, Components: s.Candidates}
	if method.Components == nil {
		method.Components = []contract.ResourceMethodComponentV1{}
	}
	if s.Valuation != nil {
		method = *s.Valuation
	}
	if err := resourceValueCosts(&c, l, method); err != nil {
		return c, method, err
	}
	return c, method, nil
}

func resourceValueCosts(c *contract.ResourceContributionV1, l *resourceLeaf, m contract.ResourceMethodV1) error {
	sub := l.IsSub
	add := func(currency *string, basis contract.ResourceBasis, amount *string, ref *contract.ResourcePriceRefV1, unpriced int64) error {
		if amount == nil {
			basis = contract.ResourceBasisUnavailable
			currency = nil
		}
		i := sort.Search(len(c.Costs), func(i int) bool {
			return deref(c.Costs[i].Currency)+"/"+string(c.Costs[i].Basis) >= deref(currency)+"/"+string(basis)
		})
		if i == len(c.Costs) || deref(c.Costs[i].Currency) != deref(currency) || c.Costs[i].Basis != basis {
			c.Costs = append(c.Costs, contract.ResourceCostV1{})
			copy(c.Costs[i+1:], c.Costs[i:])
			c.Costs[i] = contract.ResourceCostV1{Currency: currency, Basis: basis, PriceRefs: []contract.ResourcePriceRefV1{}}
		}
		g := &c.Costs[i]
		g.UnpricedCount += unpriced
		if amount != nil {
			a := *amount
			if g.Amount != nil {
				var err error
				a, err = decimalArithmetic(*g.Amount, a, false)
				if err != nil {
					return err
				}
			}
			g.Amount = &a
		}
		if ref != nil {
			for _, p := range g.PriceRefs {
				if p == *ref {
					return nil
				}
			}
			g.PriceRefs = append(g.PriceRefs, *ref)
		}
		return nil
	}
	price := func(subject, unit string, amount *string) error {
		for _, p := range m.Components {
			if p.Subject == subject && p.Unit == unit && p.Rate != nil && p.PriceId != nil && p.Currency != nil && p.ObservedAt != nil && amount != nil {
				a, e := decimalArithmetic(*p.Rate, *amount, true)
				if e != nil {
					return e
				}
				b := contract.ResourceBasisEstimated
				if sub {
					b = contract.ResourceBasisSubscription
				}
				return add(p.Currency, b, &a, &contract.ResourcePriceRefV1{PriceId: *p.PriceId, ObservedAt: *p.ObservedAt}, 0)
			}
		}
		return add(nil, contract.ResourceBasisUnavailable, nil, nil, 1)
	}
	if c.Kind == "tool_run" || l.ConnectionCost != nil {
		if currency := l.DirectCurrency; currency != nil {
			b := contract.ResourceBasisMeasured
			if sub {
				b = contract.ResourceBasisSubscription
			}
			if err := add(currency, b, resourceDecimal(l.DirectAmount), nil, 0); err != nil {
				return err
			}
		} else if err := price(l.ConnSubject, deref(l.ConnUnit), resourceDecimal(l.ConnAmount)); err != nil {
			return err
		}
	} else {
		observed := false
		for _, p := range []struct {
			value *json.Number
			unit  string
		}{{l.TokensInput, "input_tokens"}, {l.TokensOutput, "output_tokens"}, {l.TokensCacheRead, "cache_read_tokens"}, {l.TokensCacheWrite, "cache_write_tokens"}} {
			n := resourceDecimal(p.value)
			if n != nil {
				observed = true
				r, ok := new(big.Rat).SetString(*n)
				if !ok {
					return errors.New("invalid token decimal")
				}
				if r.Sign() > 0 {
					if err := price(deref(l.Model), p.unit, n); err != nil {
						return err
					}
				}
			}
		}
		if !observed && (c.Kind == "agent_run" || c.Kind == "child_agent") {
			if err := add(nil, contract.ResourceBasisUnavailable, nil, nil, 1); err != nil {
				return err
			}
		}
	}
	for i := range c.Costs {
		sort.Slice(c.Costs[i].PriceRefs, func(a, b int) bool {
			x, y := c.Costs[i].PriceRefs[a], c.Costs[i].PriceRefs[b]
			if x.PriceId != y.PriceId {
				return x.PriceId < y.PriceId
			}
			return x.ObservedAt < y.ObservedAt
		})
	}
	return nil
}

const resourceScopesSQL = `SELECT p FROM (
 SELECT project_id p FROM execution WHERE id=$1 UNION SELECT project_id FROM execution_totals WHERE execution_id=$1
 UNION SELECT project_id FROM resource_contribution WHERE execution_id=$1
 UNION SELECT s.project_id FROM resource_work_scope s JOIN resource_work w USING(work_id) WHERE w.execution_id=$1
 ) v WHERE p IS NOT NULL ORDER BY p COLLATE "C"`

func resourceScopes(q *Request, id string) ([]string, error) {
	rs, err := q.tx.Query(q.ctx(), resourceScopesSQL, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rs, pgx.RowTo[string])
}

// Materialization claims only work rows due at $2 (the kit's clock) before evaluating the source in this same
// statement snapshot. Later work is neither observed as claimed nor deleted.
func resourceClaimSnapshotSQL() string {
	return `WITH claimed AS MATERIALIZED (
 SELECT work_id FROM resource_work WHERE execution_id=$1 AND next_attempt_at<=$2
 ORDER BY work_id FOR UPDATE SKIP LOCKED)
 SELECT (SELECT coalesce(array_agg(work_id::text ORDER BY work_id),'{}'::text[]) FROM claimed),
 CASE WHEN EXISTS(SELECT 1 FROM claimed) THEN (` + resourceSourceSQL + `) ELSE NULL END`
}

func resourceCheckLocks(q *Request, projects []string) error {
	for _, p := range projects {
		if !txHasLock(q.tx, "project:"+p) {
			return errResourceScope
		}
	}
	return nil
}
func resourceEnsureProject(q *Request, p string) error {
	if err := exec(q, `INSERT INTO resource_build(project_id) VALUES($1) ON CONFLICT DO NOTHING`, p); err != nil {
		return err
	}
	return exec(q, `INSERT INTO resource_project_summary(project_id,build_generation) SELECT project_id,generation FROM resource_build WHERE project_id=$1 ON CONFLICT DO NOTHING`, p)
}

type resourceReplacement struct {
	id           string
	projects     []string
	claimed      []string
	snapshot     resourceSnapshot
	contribution contract.ResourceContributionV1
	method       contract.ResourceMethodV1
}

func resourcePrepareReplacement(q *Request, id string, projects []string, now time.Time) (*resourceReplacement, error) {
	w := &resourceReplacement{id: id, projects: projects}
	var raw []byte
	e := q.tx.QueryRow(q.ctx(), resourceClaimSnapshotSQL(), id, now).Scan(&w.claimed, &raw)
	if e != nil || len(w.claimed) == 0 {
		return w, e
	}
	var s resourceSnapshot
	if e = json.Unmarshal(raw, &s); e != nil {
		return w, e
	}
	c, m, e := resourceContribution(id, s)
	if e != nil {
		return w, e
	}
	affected := []string{}
	if c.ProjectId != nil {
		affected = append(affected, *c.ProjectId)
	}
	if s.Old != nil && s.Old.Facts.ProjectId != nil {
		affected = append(affected, *s.Old.Facts.ProjectId)
	}
	if e = resourceCheckLocks(q, append(affected, s.Scopes...)); e != nil {
		return w, e
	}
	// Also recheck the actual totals owner from this same source snapshot, via mismatch repair.
	if s.Mismatch {
		return w, errResourceOwnership
	}
	w.snapshot, w.contribution, w.method = s, c, m
	return w, nil
}

func resourceLockSummaries(q *Request, projects []string) error {
	var e error
	for _, p := range projects {
		var phase string
		lockSummary := func() error {
			return q.tx.QueryRow(q.ctx(), `SELECT b.phase FROM resource_build b JOIN resource_project_summary s USING(project_id) WHERE b.project_id=$1 FOR UPDATE OF s`, p).Scan(&phase)
		}
		e = lockSummary()
		if errors.Is(e, pgx.ErrNoRows) {
			if e = resourceEnsureProject(q, p); e != nil {
				return e
			}
			e = lockSummary()
		}
		if e != nil {
			return e
		}
		if phase == "clearing" {
			return errResourceClearing
		}
	}
	return nil
}

// resourceApplyReplacement records the leaf's support changes in deltas (one per project, written once by the caller
// after every leaf of the transaction) or, with nil deltas, writes them before returning.
func resourceApplyReplacement(q *Request, w *resourceReplacement, deltas map[string]*resourceDelta) (map[string]bool, error) {
	id, projects, s, c, m := w.id, w.projects, w.snapshot, w.contribution, w.method
	changed := map[string]bool{}
	var e error
	flush := deltas == nil
	if flush {
		deltas = map[string]*resourceDelta{}
	}
	delta := func(p string) *resourceDelta {
		if deltas[p] == nil {
			deltas[p] = newResourceDelta(p)
		}
		return deltas[p]
	}
	sha, msha := resourceSHA(s.Fingerprint), resourceSHA(m)
	sourceExists := string(s.Leaf) != "null"
	equal := s.Old != nil && sourceExists && s.Old.SourceSHA == sha && s.Old.MethodSHA == msha && s.Old.Facts.Included == c.Included
	if !equal {
		// Summary rows are already locked in project order; all support writes are deterministic.
		for _, p := range projects {
			if s.Old != nil && s.Old.Facts.ProjectId != nil && *s.Old.Facts.ProjectId == p {
				if e = delta(p).add(s.Old.Facts, -1); e != nil {
					return nil, e
				}
			}
			if sourceExists && c.ProjectId != nil && *c.ProjectId == p {
				if e = delta(p).add(c, 1); e != nil {
					return nil, e
				}
			}
		}
		if flush {
			if e = resourceFlushDeltas(q, projects, deltas); e != nil {
				return nil, e
			}
		}
		if sourceExists {
			if c.Included && s.ValuationInput {
				if e = exec(q, `INSERT INTO resource_valuation(execution_id,method_sha,method) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, msha, jsonBytes(m)); e != nil {
					return nil, e
				}
			}
			if e = exec(q, `INSERT INTO resource_contribution(execution_id,project_id,source_sha,included,facts,method,method_sha) VALUES($1,$2,$3,$4,$5,$6,$7)
      ON CONFLICT(execution_id) DO UPDATE SET project_id=excluded.project_id,source_sha=excluded.source_sha,included=excluded.included,facts=excluded.facts,method=excluded.method,method_sha=excluded.method_sha,updated_at=clock_timestamp()`, id, c.ProjectId, sha, c.Included, jsonBytes(c), jsonBytes(m), msha); e != nil {
				return nil, e
			}
		} else {
			if e = exec(q, `DELETE FROM resource_contribution WHERE execution_id=$1`, id); e != nil {
				return nil, e
			}
		}

		if c.Included && c.ProjectId != nil {
			changed[*c.ProjectId] = true
		}
		if s.Old != nil && s.Old.Facts.Included && s.Old.Facts.ProjectId != nil {
			changed[*s.Old.Facts.ProjectId] = true
		}

	}
	if err := exec(q, `DELETE FROM resource_work WHERE work_id=ANY($1::uuid[])`, w.claimed); err != nil {
		return nil, err
	}
	return changed, nil
}

// resourceFlushDeltas writes accumulated support changes in the already locked project order.
func resourceFlushDeltas(q *Request, projects []string, deltas map[string]*resourceDelta) error {
	for _, p := range projects {
		if d := deltas[p]; d != nil {
			if err := d.flush(q); err != nil {
				return err
			}
		}
	}
	return nil
}

// resourceReplaceStep (work queue) consumes only the work UUIDs it claims; c.Guard keeps them, so a failure written
// after an uncertain COMMIT pauses nothing that came later. Source rows are never locked. A scope the hint did not know
// runs the step again with the scopes it learned.
func resourceReplaceStep(q *Request, now time.Time, c *kitClaim) (err error) {
	_, span := obs.Start(obs.WithRun(q.ctx(), obs.Run{ExecutionID: c.Key}), "resource.replace", "internal")
	defer func() { span.End(kitSpanError(err)) }()
	projects, err := resourceScopes(q, c.Key)
	if err != nil {
		return err
	}
	locks := []string{"execution:" + c.Key}
	for _, p := range projects {
		locks = append(locks, "project:"+p)
	}
	if err = kitLock(q, locks...); err != nil {
		return err
	}
	w, err := resourcePrepareReplacement(q, c.Key, projects, now)
	c.Guard = []any{w.claimed}
	if errors.Is(err, errResourceScope) {
		return fmt.Errorf("%w: %w", errKitAgain, err)
	}
	if err != nil || len(w.claimed) == 0 {
		return err
	}
	if err = resourceLockSummaries(q, projects); err != nil {
		return err
	}
	changed, err := resourceApplyReplacement(q, w, nil)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if changed[p] {
			if err = resourcePublish(q, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// kitSpanError: a skipped step is no failure.
func kitSpanError(err error) error {
	if errors.Is(err, errKitSkip) {
		return nil
	}
	return err
}

// resourceReplace runs one execution's replacement as a pass does (a failure is written), waiting for its locks. A
// recorded ownership mismatch, its totals repaired, is no error.
func resourceReplace(ctx context.Context, a *App, id string) error {
	cs, err := resourceWorkClaims(ctx, a, []string{id})
	if err != nil {
		return err
	}
	k := kitOf(a)
	stepErr, bookErr := k.one(ctx, k.queue("work"), k.now(), cs[0])
	if errors.Is(stepErr, errResourceOwnership) && bookErr == nil {
		return nil
	}
	return errors.Join(stepErr, bookErr)
}

// A cohort may hold every same-project leaf of one pass (the pass selects at most 100 due IDs): each extra transaction
// costs a lock set, scope hint, summary lock, publication and COMMIT, which dominated burst publication age at 16.
const resourceCohortSize = 100

// The selected-ID union avoids repeating an unrelated pending-scope scan per leaf.
const resourceBatchHintsSQL = `WITH candidates AS MATERIALIZED (
 SELECT id,project_id FROM execution WHERE id=ANY($1::text[]) AND project_id IS NOT NULL AND project_id<>''
), selected_work AS MATERIALIZED (
 SELECT work_id,execution_id FROM resource_work WHERE execution_id=ANY($1::text[])
), selected_work_scopes AS MATERIALIZED (
 SELECT work_id,project_id FROM resource_work_scope WHERE work_id=ANY(ARRAY(SELECT work_id FROM selected_work))
), scopes AS (
 SELECT id,project_id FROM candidates
 UNION SELECT execution_id,project_id FROM execution_totals WHERE execution_id=ANY($1::text[])
 UNION SELECT execution_id,project_id FROM resource_contribution WHERE execution_id=ANY($1::text[])
 UNION SELECT w.execution_id,s.project_id FROM selected_work w JOIN selected_work_scopes s USING(work_id)
), merged AS MATERIALIZED (
 SELECT id,min(project_id) project_id,count(DISTINCT project_id) scope_count FROM scopes WHERE project_id IS NOT NULL GROUP BY id
)
SELECT c.id,c.project_id FROM candidates c JOIN merged m USING(id) WHERE m.scope_count=1 AND m.project_id=c.project_id`

// resourceBatchHintsQ: only a scheduling hint (locked per-leaf snapshots stay authoritative): the selected executions
// whose every known scope is their one current project.
func resourceBatchHintsQ(q *Request, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rs, err := q.tx.Query(q.ctx(), resourceBatchHintsSQL, ids)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	for rs.Next() {
		var id, p string
		if err = rs.Scan(&id, &p); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rs.Err()
}

// resourceWorkClaimsQ: claims of the given executions with their attempts and cohort hint.
func resourceWorkClaimsQ(q *Request, ids []string) ([]*kitClaim, error) {
	hints, err := resourceBatchHintsQ(q, ids)
	if err != nil {
		return nil, err
	}
	attempts := map[string]int{}
	rs, err := q.tx.Query(q.ctx(), `SELECT execution_id,max(attempts) FROM resource_work WHERE execution_id=ANY($1::text[]) GROUP BY 1`, ids)
	if err != nil {
		return nil, err
	}
	for rs.Next() {
		var id string
		var n int
		if err = rs.Scan(&id, &n); err != nil {
			rs.Close()
			return nil, err
		}
		attempts[id] = n
	}
	rs.Close()
	if err = rs.Err(); err != nil {
		return nil, err
	}
	cs := make([]*kitClaim, len(ids))
	for i, id := range ids {
		cs[i] = &kitClaim{Key: id, Group: hints[id], Attempts: attempts[id], Guard: []any{[]string(nil)}}
	}
	return cs, nil
}

func resourceWorkClaims(ctx context.Context, a *App, ids []string) (cs []*kitClaim, err error) {
	err = kitOf(a).read(ctx, func(q *Request) error { cs, err = resourceWorkClaimsQ(q, ids); return err })
	return cs, err
}

// resourceReplaceBatchStep (work queue cohorts): same-project executions in one transaction: the complete sorted lock
// set, every member claimed and checked before the first support change, one set of support writes and one
// publication. Any failure rolls the whole cohort back; the kit retries it in halves.
func resourceReplaceBatchStep(q *Request, now time.Time, cs []*kitClaim) (err error) {
	project := cs[0].Group
	if len(cs) < 2 || len(cs) > resourceCohortSize || project == "" {
		return errResourceScope
	}
	locks := []string{"project:" + project}
	unique := map[string]bool{}
	for _, c := range cs {
		if c.Group != project || unique[c.Key] {
			return errResourceScope
		}
		unique[c.Key] = true
		locks = append(locks, "execution:"+c.Key)
	}
	_, span := obs.Start(q.ctx(), "resource.batch", "internal")
	span.Set("fleet.resource.batch_size", strconv.Itoa(len(cs)))
	defer func() { span.End(kitSpanError(err)) }()
	if err = kitLock(q, locks...); err != nil {
		return err
	}
	works := make([]*resourceReplacement, 0, len(cs))
	for _, c := range cs {
		w, err := resourcePrepareReplacement(q, c.Key, []string{project}, now)
		if err != nil {
			return err
		}
		if len(w.claimed) == 0 {
			continue
		}
		x, s := w.contribution, w.snapshot
		if x.ProjectId == nil || *x.ProjectId != project || len(s.Scopes) != 1 || s.Scopes[0] != project || s.Old != nil && (s.Old.Facts.ProjectId == nil || *s.Old.Facts.ProjectId != project) {
			return errResourceScope
		}
		works = append(works, w)
	}
	if err = resourceLockSummaries(q, []string{project}); err != nil {
		return err
	}
	changed := false
	deltas := map[string]*resourceDelta{}
	for _, w := range works {
		affected, err := resourceApplyReplacement(q, w, deltas)
		if err != nil {
			return err
		}
		changed = changed || affected[project]
	}
	touched := make([]string, 0, len(deltas))
	for p := range deltas {
		touched = append(touched, p)
	}
	sort.Strings(touched)
	if err = resourceFlushDeltas(q, touched, deltas); err != nil {
		return err
	}
	if changed {
		return resourcePublish(q, project)
	}
	return nil
}

// resourceReplaceBatch: one cohort transaction attempt of the given executions, waiting for its locks.
func resourceReplaceBatch(ctx context.Context, a *App, ids []string) error {
	cs, err := resourceWorkClaims(ctx, a, ids)
	if err != nil {
		return err
	}
	k := kitOf(a)
	return k.tx(ctx, func(q *Request) error { return resourceReplaceBatchStep(q, k.now(), cs) })
}

// resourceReplaceCandidates runs the given executions as a pass runs its selected work: cohorts, halves, single steps.
func resourceReplaceCandidates(ctx context.Context, a *App, ids []string) error {
	cs, err := resourceWorkClaims(ctx, a, ids)
	if err != nil {
		return err
	}
	k := kitOf(a)
	stepErr, bookErr := k.run(ctx, k.queue("work"), k.now(), cs)
	return errors.Join(stepErr, bookErr)
}

func resourceErrorClass(err error) string {
	if errors.Is(err, errResourceOwnership) {
		return "ownership_mismatch"
	}
	return "projection_failed"
}

// resourceRepairOwnership runs in the transaction that records an ownership mismatch, never in a replacement: the
// execution's totals follow their parent, parent row first. A busy lock or a moved scope leaves it to the next attempt.
func resourceRepairOwnership(q *Request, c *kitClaim, cause error) error {
	if !errors.Is(cause, errResourceOwnership) {
		return nil
	}
	projects, err := resourceScopes(q, c.Key)
	if err != nil {
		return err
	}
	locks := []string{"execution:" + c.Key}
	for _, p := range projects {
		locks = append(locks, "project:"+p)
	}
	if err = kitLock(q, locks...); errors.Is(err, errKitSkip) {
		return nil
	} else if err != nil {
		return err
	}
	var parent *string
	if err = q.tx.QueryRow(q.ctx(), `SELECT project_id FROM execution WHERE id=$1 FOR UPDATE`, c.Key).Scan(&parent); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if projects, err = resourceScopes(q, c.Key); err != nil {
		return err
	}
	if resourceCheckLocks(q, projects) != nil {
		return nil
	}
	return exec(q, `UPDATE execution_totals SET project_id=$2 WHERE execution_id=$1 AND project_id IS DISTINCT FROM $2`, c.Key, parent)
}

// resourcePreparedHeader reads only fixed-size metric support and indexed extremes. Detail support
// counts are maintained during replacement, so high-cardinality tools/prices never expand the header.
func resourcePreparedHeader(q *Request, p string) (out contract.PreparedHeaderV1, resultErr error) {
	empty := emptyResourceSummary()
	h := contract.PreparedHeaderV1{Tokens: empty.Tokens, Durations: empty.Durations, PeakMemoryBytes: empty.PeakMemoryBytes, Tools: contract.PreparedToolsV1{Basis: contract.ResourceBasisUnavailable}}
	var other int64
	batch := &pgx.Batch{}
	batch.Queue(`SELECT executions,other_calls,tool_count,cost_count,price_count FROM resource_project_summary WHERE project_id=$1`, p)
	batch.Queue(`SELECT metric::text,basis,present_count,expected_count,sum_value::text FROM resource_metric WHERE project_id=$1 ORDER BY metric,basis`, p)
	batch.Queue(`SELECT
 (SELECT value FROM resource_extreme WHERE project_id=$1 AND metric='start' ORDER BY value LIMIT 1),
 (SELECT value FROM resource_extreme WHERE project_id=$1 AND metric='end' ORDER BY value DESC LIMIT 1),
 (SELECT value FROM resource_extreme WHERE project_id=$1 AND metric='peakMemory' ORDER BY value DESC LIMIT 1)`, p)
	results := q.tx.SendBatch(q.ctx(), batch)
	// Closing consumes even unread results; a late SQL error must precede publication.
	defer func() {
		if err := results.Close(); resultErr == nil {
			resultErr = err
		}
	}()
	if err := results.QueryRow().Scan(&h.Executions, &other, &h.ItemCounts.Tools, &h.ItemCounts.Costs, &h.ItemCounts.Prices); err != nil {
		return h, err
	}
	if other > 0 {
		h.Tools.Other = &other
	}
	type measure struct {
		present, expected int64
		sum               string
		bases             []contract.ResourceBasis
	}
	ms := map[string]*measure{}
	rs, err := results.Query()
	if err != nil {
		return h, err
	}
	for rs.Next() {
		var name, basis, sum string
		var present, expected int64
		if err = rs.Scan(&name, &basis, &present, &expected, &sum); err != nil {
			rs.Close()
			return h, err
		}
		v := ms[name]
		if v == nil {
			v = &measure{sum: "0"}
			ms[name] = v
		}
		v.present += present
		v.expected += expected
		v.sum, err = decimalArithmetic(v.sum, sum, false)
		if err != nil {
			rs.Close()
			return h, err
		}
		if present > 0 || expected > 0 {
			v.bases = append(v.bases, contract.ResourceBasis(basis))
		}
	}
	err = rs.Err()
	rs.Close()
	if err != nil {
		return h, err
	}
	var conversionErr error
	value := func(k string) *int64 {
		m := ms[k]
		if m == nil || m.present == 0 {
			return nil
		}
		v, e := strconv.ParseInt(m.sum, 10, 64)
		if e != nil {
			conversionErr = fmt.Errorf("resource metric %s exceeds integer contract: %w", k, e)
			return nil
		}
		return &v
	}
	count := func(k string) int64 {
		v := value(k)
		if v == nil {
			return 0
		}
		return *v
	}
	basis := func(k string) contract.ResourceBasis {
		m := ms[k]
		if m == nil || m.present == 0 || m.present < m.expected {
			return contract.ResourceBasisUnavailable
		}
		return weakestBasis(m.bases...)
	}
	h.Tokens.Input = value("input")
	h.Tokens.Output = value("output")
	h.Tokens.CacheRead = value("cacheRead")
	h.Tokens.CacheWrite = value("cacheWrite")
	h.Tokens.Reasoning = value("reasoning")
	total := new(big.Int)
	hasToken := false
	for _, v := range []*int64{h.Tokens.Input, h.Tokens.Output, h.Tokens.CacheRead, h.Tokens.CacheWrite} {
		if v != nil {
			total.Add(total, big.NewInt(*v))
			hasToken = true
		}
	}
	if hasToken {
		if !total.IsInt64() {
			return h, errors.New("resource token total exceeds integer contract")
		}
		h.Tokens.Total = ptr(total.Int64())
		h.Tokens.Basis = weakestBasis(basis("input"), basis("output"))
		for _, k := range []string{"input", "output", "cacheRead", "cacheWrite"} {
			if m := ms[k]; m != nil && m.present > 0 {
				h.Tokens.Basis = weakestBasis(h.Tokens.Basis, weakestBasis(m.bases...))
			}
		}
	}
	h.Durations.ExecutionWallMs = value("wallMs")
	h.Durations.ActiveMs = value("activeMs")
	h.Durations.ApiMs = value("apiMs")
	h.Durations.CpuMs = value("cpuMs")
	h.Durations.TotalRuns = count("processActors")
	if m := ms["cpuMs"]; m != nil {
		h.Durations.MeasuredCpuRuns = m.present
	}
	h.Durations.Basis = basis("wallMs")
	var start, end, memory *int64
	if err = results.QueryRow().Scan(&start, &end, &memory); err != nil {
		return h, err
	}
	if start != nil && end != nil && *end >= *start {
		h.Durations.ElapsedMs = ptr(*end - *start)
	}
	if memory != nil {
		h.PeakMemoryBytes = contract.MeasuredInt{Value: memory, Basis: basis("peakMemoryBytes")}
		if m := ms["peakMemoryBytes"]; m != nil && m.present < m.expected {
			h.PeakMemoryBytes.Partial = ptr(fmt.Sprintf("%d of %d measured", m.present, m.expected))
		}
	}
	if m := ms["credits"]; m != nil && m.present > 0 {
		v, e := strconv.ParseFloat(m.sum, 64)
		if e != nil {
			return h, e
		}
		h.Credits = &v
	} // Wire conversion only; support stays numeric.
	h.Tools.TotalCalls = count("toolCalls")
	h.Tools.FailedCalls = count("toolErrors")
	h.Tools.Turns = count("turns")
	h.Tools.Basis = basis("toolCalls")
	return h, conversionErr
}

// resourcePublish commits the stored header/version and public resource_summary change together.
// The revision trigger uses the existing permission-filtered change/XID/SSE path.
func resourcePublish(q *Request, p string) (resultErr error) {
	_, publishSpan := obs.Start(q.ctx(), "resource.publish", "internal")
	defer func() { publishSpan.End(resultErr) }()
	stage := time.Now()

	h, err := resourcePreparedHeader(q, p)
	publishSpan.Set("fleet.resource.header_us", strconv.FormatInt(time.Since(stage).Microseconds(), 10))
	stage = time.Now()
	if err != nil {
		return err
	}
	var version string
	// The enqueue column is PostgreSQL void; deliberately skip its codec.
	err = q.tx.QueryRow(q.ctx(), `WITH published AS (
 UPDATE resource_project_summary SET header=$2,revision=revision+1,published_at=clock_timestamp()
 WHERE project_id=$1 RETURNING project_id,epoch,revision)
 SELECT epoch::text||':'||revision::text,fleet_enqueue_wake('fleet_resource_summary',project_id) FROM published`, p, jsonBytes(h)).Scan(&version, nil)
	if err == nil {
		publishSpan.Set("fleet.resource.publication_version", version)
		obs.From(q.ctx()).Set("fleet.resource.publication_version", version)
	}
	publishSpan.Set("fleet.resource.publication_update_and_wake_us", strconv.FormatInt(time.Since(stage).Microseconds(), 10))
	return err
}

// resourceProgressSQL is used by finalization; authorized readers use the same coverage predicates.
// Unenumerated metadata jobs conservatively keep ALL scopes pending until enumeration completes.
const resourceProgressSQL = `SELECT EXISTS(SELECT 1 FROM resource_work_scope WHERE project_id=$1)
 OR EXISTS(SELECT 1 FROM resource_repair_scope WHERE project_id=$1)
 OR EXISTS(SELECT 1 FROM resource_metadata_repair WHERE NOT done)`

// resourceBuildStep (build queue): one transition of a project's build (buildTransition) under its project lock.
func resourceBuildStep(q *Request, now time.Time, c *kitClaim) error {
	if err := kitLock(q, "project:"+c.Key); err != nil {
		return err
	}
	return buildTransition(q, c.Key, buildPage)
}

// resourceBuildPage runs one build step as a pass does, without writing a failure, waiting for its lock.
func resourceBuildPage(ctx context.Context, a *App, p string) error {
	k := kitOf(a)
	return k.step(ctx, k.queue("build"), k.now(), &kitClaim{Key: p})
}

// resourceRebuild is internal maintenance, never called by a reader: a new generation and epoch start clearing; no
// frozen valuation or source work is erased.
func resourceRebuild(ctx context.Context, a *App, p string) error {
	k := kitOf(a)
	return runTxLocks(ctx, a, nil, true, []string{"project:" + p}, func(q *Request) error {
		if err := buildTransition(q, p, buildRebuild); err != nil {
			return err
		}
		return kitRestart(q, k.queue("build").Retry, p, k.now())
	})
}

func resourceSeedWork(q *Request, ids []string, origin string, generation, repair *string) error {
	// One SQL statement seeds identities AND all known old/current project scopes.
	return exec(q, `WITH seeded AS (
 INSERT INTO resource_work(execution_id,origin,build_generation,repair_id)
 SELECT unnest($1::text[]),$2,$3::uuid,$4::uuid ON CONFLICT ON CONSTRAINT resource_work_source_key DO UPDATE SET execution_id=excluded.execution_id RETURNING work_id,execution_id,first_pending_at)
 INSERT INTO resource_work_scope(work_id,project_id,first_pending_at)
 SELECT w.work_id,v.project_id,w.first_pending_at FROM seeded w CROSS JOIN LATERAL (
 SELECT project_id FROM execution WHERE id=w.execution_id UNION SELECT project_id FROM resource_contribution WHERE execution_id=w.execution_id) v
 WHERE v.project_id IS NOT NULL ON CONFLICT DO NOTHING`, ids, origin, generation, repair)
}

// resourceRepairStep (repair queue): one transition of a metadata repair (repairTransition).
func resourceRepairStep(q *Request, now time.Time, c *kitClaim) error {
	return repairTransition(q, c.Key)
}

// resourceMetadataPage runs one repair step as a pass does, without writing a failure.
func resourceMetadataPage(ctx context.Context, a *App, repair string) error {
	k := kitOf(a)
	return k.step(ctx, k.queue("repair"), k.now(), &kitClaim{Key: repair})
}

// resourceQueues: the processor's queues in pass order: project builds (a missing build row is due too), metadata
// repairs, execution work.
func resourceQueues() []*kitQueue {
	return []*kitQueue{
		{Name: "build", Limit: 4, Due: resourceBuildsDue, Step: resourceBuildStep, Retry: kitRetry{Table: "resource_build", Key: "project_id", Error: "last_error",
			Guard:  `NOT resource_build.ready AND resource_build.generation IS NOT DISTINCT FROM $5::uuid AND resource_build.updated_at IS NOT DISTINCT FROM $6::timestamptz`,
			Set:    `,updated_at=clock_timestamp()`,
			Upsert: true, Class: func(error) string { return "build_failed" }}},
		{Name: "repair", Limit: 1, Due: resourceRepairsDue, Step: resourceRepairStep, Retry: kitRetry{Table: "resource_metadata_repair", Key: "repair_id", Error: "last_error",
			Guard: `resource_metadata_repair.after_execution_id IS NOT DISTINCT FROM $5::text AND resource_metadata_repair.done=$6::boolean`,
			Class: func(error) string { return "metadata_failed" }}},
		{Name: "work", Limit: resourceCohortSize, Due: resourceWorkDue, Step: resourceReplaceStep, Batch: resourceReplaceBatchStep, Retry: kitRetry{Table: "resource_work", Key: "execution_id", Error: "last_error_class",
			Guard: `(coalesce(cardinality($5::uuid[]),0)=0 OR EXISTS(SELECT 1 FROM resource_work g WHERE g.work_id=ANY($5::uuid[])))`,
			Class: resourceErrorClass, OnFail: resourceRepairOwnership}},
	}
}

// Projects are discovered from the configuration catalogue, never by rescanning execution history.
func resourceBuildsDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.tx.Query(q.ctx(), `SELECT key,attempts,generation,updated_at FROM (
 SELECT id AS key,0 AS attempts,NULL::text AS generation,NULL::timestamptz AS updated_at FROM definition d WHERE kind='project' AND NOT EXISTS(SELECT 1 FROM resource_build b WHERE b.project_id=d.id)
 UNION ALL SELECT project_id,attempts,generation::text,updated_at FROM resource_build WHERE NOT ready AND next_attempt_at<=$1) due
 ORDER BY updated_at NULLS FIRST,key COLLATE "C" LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rs, func(r pgx.CollectableRow) (*kitClaim, error) {
		var c kitClaim
		var generation *string
		var updated *time.Time
		err := r.Scan(&c.Key, &c.Attempts, &generation, &updated)
		c.Guard = []any{generation, updated}
		return &c, err
	})
}

// A repair whose enumeration is done waits until its seeded work drains.
func resourceRepairsDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.tx.Query(q.ctx(), `SELECT repair_id::text,attempts,after_execution_id,done FROM resource_metadata_repair r WHERE next_attempt_at<=$1
 AND (NOT done OR NOT EXISTS(SELECT 1 FROM resource_work w WHERE w.repair_id=r.repair_id)) ORDER BY next_attempt_at,created_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rs, func(r pgx.CollectableRow) (*kitClaim, error) {
		var c kitClaim
		var after *string
		var done bool
		err := r.Scan(&c.Key, &c.Attempts, &after, &done)
		c.Guard = []any{after, done}
		return &c, err
	})
}

// An execution is one item: a paused execution (a failed row still waiting) is no candidate, even through a new due
// row, and when it is picked every failed row is due, so a success consumes them all. Executions with a clearing scope
// yield, so they cannot occupy every slot.
const resourceCandidatesSQL = `SELECT execution_id FROM (SELECT execution_id,next_attempt_at,first_pending_at FROM resource_work candidate
 WHERE next_attempt_at<=$1
 AND NOT EXISTS(SELECT 1 FROM resource_work paused WHERE paused.execution_id=candidate.execution_id AND paused.attempts>0 AND paused.next_attempt_at>$1)
 AND (NOT EXISTS(SELECT 1 FROM resource_build WHERE phase='clearing') OR NOT EXISTS(
  SELECT 1 FROM resource_work blocked JOIN resource_work_scope scope USING(work_id)
  JOIN resource_build build ON build.project_id=scope.project_id
  WHERE blocked.execution_id=candidate.execution_id AND build.phase='clearing'))
 ORDER BY next_attempt_at,first_pending_at,work_id LIMIT $2) candidates
 GROUP BY execution_id ORDER BY min(next_attempt_at),min(first_pending_at),execution_id`

func resourceWorkDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.tx.Query(q.ctx(), resourceCandidatesSQL, now, limit)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rs, pgx.RowTo[string])
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	return resourceWorkClaimsQ(q, ids)
}

// resourceProcessorPass runs one pass of every queue.
func resourceProcessorPass(ctx context.Context, a *App) error { return kitOf(a).pass(ctx) }

// resourcePreparedState is an INTERNAL read seam. The caller must authorize a non-null project
// before calling it in its scoped read transaction. It never reconstructs history or starts work.
type resourcePreparedState struct {
	Header          contract.PreparedHeaderV1
	Epoch           string
	Revision        string // decimal bigint, never a float or a source cursor
	Ready           bool
	Pending         bool
	ValidatedAt     time.Time
	PublishedAt     *time.Time
	OldestPendingAt *time.Time
	ErrorClass      *string
}

func resourceReadPrepared(q *Request, p string) (resourcePreparedState, error) {
	var out resourcePreparedState
	var header []byte
	// First enforce the caller's ordinary project RLS. Only after that authorized row is read
	// may the maintenance-only progress check widen scope, without returning any source facts.
	err := q.tx.QueryRow(q.ctx(), `SELECT s.header,s.epoch::text,s.revision::text,s.ready,transaction_timestamp(),s.published_at,b.last_error
 FROM resource_project_summary s JOIN resource_build b USING(project_id) WHERE s.project_id=$1`, p).Scan(&header, &out.Epoch, &out.Revision, &out.Ready, &out.ValidatedAt, &out.PublishedAt, &out.ErrorClass)
	if errors.Is(err, pgx.ErrNoRows) {
		out.Pending = true
		// No epoch exists yet, but validation still describes this read snapshot.
		err = q.tx.QueryRow(q.ctx(), `SELECT transaction_timestamp()`).Scan(&out.ValidatedAt)
		return out, err
	}
	if err != nil {
		return out, err
	}
	// The caller's Store read transaction is REPEATABLE READ: both statements describe exactly
	// the same snapshot, even if a processor/source commits between them.
	_, err = unscoped(q, func() (bool, error) {
		var workError *string
		e := q.tx.QueryRow(q.ctx(), `SELECT
   EXISTS(SELECT 1 FROM resource_work_scope WHERE project_id=$1)
    OR EXISTS(SELECT 1 FROM resource_repair_scope WHERE project_id=$1)
    OR EXISTS(SELECT 1 FROM resource_metadata_repair WHERE NOT done),
   (SELECT min(at) FROM (
    (SELECT first_pending_at at FROM resource_work_scope WHERE project_id=$1 AND first_pending_at IS NOT NULL ORDER BY first_pending_at LIMIT 1)
    UNION ALL SELECT min(w.first_pending_at) FROM resource_work_scope ws JOIN resource_work w USING(work_id) WHERE ws.project_id=$1 AND ws.first_pending_at IS NULL
    UNION ALL SELECT min(r.created_at) FROM resource_metadata_repair r WHERE NOT done OR EXISTS(SELECT 1 FROM resource_repair_scope rs WHERE rs.repair_id=r.repair_id AND rs.project_id=$1)) ages),
   coalesce((SELECT w.last_error_class FROM resource_work w WHERE w.last_error_class IS NOT NULL AND EXISTS(SELECT 1 FROM resource_work_scope ws WHERE ws.work_id=w.work_id AND ws.project_id=$1) ORDER BY w.first_pending_at,w.work_id LIMIT 1),
    (SELECT r.last_error FROM resource_metadata_repair r WHERE r.last_error IS NOT NULL AND (NOT done OR EXISTS(SELECT 1 FROM resource_repair_scope rs WHERE rs.repair_id=r.repair_id AND rs.project_id=$1)) ORDER BY r.created_at,r.repair_id LIMIT 1))`, p).Scan(&out.Pending, &out.OldestPendingAt, &workError)
		if out.ErrorClass == nil {
			out.ErrorClass = workError
		}
		return true, e
	})
	if err == nil && out.Ready {
		err = json.Unmarshal(header, &out.Header)
	}
	return out, err
}

func resourcePublicProgress(s resourcePreparedState) contract.ResourceProgress {
	timestamp := func(t *time.Time) *string {
		if t == nil {
			return nil
		}
		v := t.UTC().Format(time.RFC3339Nano)
		return &v
	}
	state := contract.ResourceProgressState("initializing")
	if s.Ready {
		state = "current"
		if s.Pending {
			state = "updating"
		}
	}
	errClass := s.ErrorClass
	if errClass != nil {
		state = "error"
		if !contract.ResourceErrorClass(*errClass).Valid() {
			v := "projection_failed"
			errClass = &v
		}
	}
	return contract.ResourceProgress{State: state, ValidatedAt: s.ValidatedAt.UTC().Format(time.RFC3339Nano), PublishedAt: timestamp(s.PublishedAt), OldestPendingAt: timestamp(s.OldestPendingAt), Pending: s.Pending, ErrorClass: jsonBytes(errClass)}
}

// A cursor is a position in a build, never a grant or a frozen revision. Every page is a
// fresh repeatable-read snapshot; continuously advancing revisions cannot starve navigation.
type resourceItemCursor struct {
	SchemaVersion int                       `json:"schemaVersion"`
	ProjectID     string                    `json:"projectId"`
	BuildEpoch    string                    `json:"buildEpoch"`
	Category      contract.ResourceCategory `json:"category"`
	Group         *contract.ResourceGroup   `json:"group"`
	Query         string                    `json:"query"`
	Direction     string                    `json:"direction"`
	Key           contract.ResourceSortKey  `json:"key"`
}

type resourceItemQuery struct {
	table, tuple, key, item string
	where                   string
	args                    []any
}

func resourceStrictJSON(raw []byte, v any) error {
	// encoding/json accepts null into non-pointer scalars as their zero value. A client
	// position must retain the declared scalar type, including a measured timestamp of zero.
	t := reflect.TypeOf(v).Elem()
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.Type.Kind() != reflect.Pointer && bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
				return fail(400, "Invalid resource position field "+name)
			}
		}
	}
	if err := hostKeys(raw, reflect.TypeOf(v)); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fail(400, "Invalid resource position")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fail(400, "Invalid resource position")
	}
	return nil
}
func resourceKeyValues(raw contract.ResourceSortKey, category contract.ResourceCategory) ([]any, error) {
	textOK := func(s string) bool { return len([]rune(s)) <= 200 }
	switch category {
	case "tools":
		var tag struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(raw, &tag) == nil && tag.Kind == "tool-position" {
			var packed contract.ResourcePackedToolKey
			if err := resourceStrictJSON(raw, &packed); err != nil {
				return nil, err
			}
			decoded, err := resourceUnpack(packed.Encoded, "tp1.")
			if err != nil {
				return nil, err
			}
			// Keep the original inner JSON until scalar validation: decoding directly into
			// ResourceToolKey would turn name:null into the empty string before it is checked.
			var envelope struct {
				SchemaVersion int             `json:"schemaVersion"`
				Key           json.RawMessage `json:"key"`
			}
			if err = resourceStrictJSON(decoded, &envelope); err != nil {
				return nil, err
			}
			if envelope.SchemaVersion != 1 {
				return nil, fail(400, "Invalid tool position version")
			}
			raw = envelope.Key
		}
		var k contract.ResourceToolKey
		if err := resourceStrictJSON(raw, &k); err != nil {
			return nil, err
		}
		// Native usage already admits longer tool names; full persisted tuples must remain
		// usable for continuation and deletion-safe re-anchors. HTTP bounds the request.
		if k.Kind != "tool" {
			return nil, fail(400, "Invalid tool anchor")
		}
		c := ""
		if k.Connection != nil {
			c = *k.Connection
		}
		return []any{k.Name, c}, nil
	case "costs":
		var k contract.ResourceCostKey
		if err := resourceStrictJSON(raw, &k); err != nil {
			return nil, err
		}
		if k.Kind != "cost" || !k.Basis.Valid() || (k.Currency != nil && !textOK(*k.Currency)) {
			return nil, fail(400, "Invalid cost anchor")
		}
		c := ""
		if k.Currency != nil {
			c = *k.Currency
		}
		return []any{c, string(k.Basis)}, nil
	case "prices":
		var k contract.ResourcePriceKey
		if err := resourceStrictJSON(raw, &k); err != nil {
			return nil, err
		}
		if k.Kind != "price" || !textOK(k.PriceId) || k.ObservedAt < 0 {
			return nil, fail(400, "Invalid price anchor")
		}
		return []any{k.PriceId, k.ObservedAt}, nil
	}
	return nil, fail(400, "Invalid resource category")
}
func resourceItemsQuery(r *contract.ProjectResourceItemsRequest, query string) resourceItemQuery {
	q := resourceItemQuery{where: "project_id=$1", args: []any{r.ProjectId}}
	switch r.Category {
	case "tools":
		q.table = "resource_tool"
		q.tuple = `(name COLLATE "C",connection COLLATE "C")`
		q.key = `jsonb_build_object('kind','tool','name',name,'connection',nullif(connection,''))`
		q.item = q.key + ` || jsonb_build_object('calls',calls,'errors',errors)`
		if query != "" {
			q.where += ` AND strpos(lower(name),lower($2))>0`
			q.args = append(q.args, query)
		}
	case "costs":
		q.table = "resource_cost"
		q.tuple = `(currency COLLATE "C",basis COLLATE "C")`
		q.key = `jsonb_build_object('kind','cost','currency',nullif(currency,''),'basis',basis)`
		q.item = q.key + ` || jsonb_build_object('value',value,'usages',usages,'note',CASE WHEN basis='subscription' THEN 'Subscription list-price equivalent' END,'reason',CASE WHEN basis='unavailable' THEN 'No recorded valuation' END,'priceCount',(SELECT count(*) FROM resource_price_ref p WHERE p.project_id=resource_cost.project_id AND p.currency=resource_cost.currency AND p.basis=resource_cost.basis))`
	case "prices":
		q.table = "resource_price_ref"
		q.tuple = `(price_id COLLATE "C",observed_at)`
		q.key = `jsonb_build_object('kind','price','priceId',price_id,'observedAt',observed_at)`
		q.item = q.key + ` || jsonb_build_object('group',jsonb_build_object('currency',nullif(currency,''),'basis',basis))`
		currency := ""
		if r.Group.Currency != nil {
			currency = *r.Group.Currency
		}
		q.where += ` AND currency=$2 AND basis=$3`
		q.args = append(q.args, currency, string(r.Group.Basis))
	}
	return q
}
func (s resourceItemQuery) position(values []any, comparison string) (string, []any) {
	args := slices.Clone(s.args)
	predicate := s.where
	if values != nil {
		a := len(args) + 1
		args = append(args, values...)
		predicate += fmt.Sprintf(" AND %s %s ($%d,$%d)", s.tuple, comparison, a, a+1)
	}
	return predicate, args
}
func (s resourceItemQuery) page(q *Request, values []any, comparison string, descending bool, limit int64) ([]contract.ResourceSortKey, []contract.ResourceItem, error) {
	where, args := s.position(values, comparison)
	direction := "ASC"
	if descending {
		direction = "DESC"
	}
	// Direction applies independently to both tuple elements (ORDER BY tuple DESC would not
	// use the two-column C-order index). The tuple itself is used only for comparisons.
	order := strings.TrimSuffix(strings.TrimPrefix(s.tuple, "("), ")")
	order = strings.ReplaceAll(order, ",", " "+direction+",") + " " + direction
	rs, err := q.tx.Query(q.ctx(), "SELECT "+s.key+","+s.item+" FROM "+s.table+" WHERE "+where+" ORDER BY "+order+" LIMIT "+strconv.FormatInt(limit, 10), args...)
	if err != nil {
		return nil, nil, err
	}
	defer rs.Close()
	keys := []contract.ResourceSortKey{}
	items := []contract.ResourceItem{}
	for rs.Next() {
		var key, item []byte
		if err := rs.Scan(&key, &item); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
		items = append(items, item)
	}
	return keys, items, rs.Err()
}
func (s resourceItemQuery) exists(q *Request, key contract.ResourceSortKey, category contract.ResourceCategory, comparison string) (bool, error) {
	values, err := resourceKeyValues(key, category)
	if err != nil {
		return false, err
	}
	where, args := s.position(values, comparison)
	return has(q, "SELECT 1 FROM "+s.table+" WHERE "+where+" LIMIT 1", args...)
}

// Identifiers and versions only; a publication version on a failed parent is
// uncommitted. A response version describes the actual successful typed read.
func resourceTraceResponse(q *Request, project, version, kind string) {
	if span := obs.From(q.ctx()); span != nil {
		span.ProjectID = ptr(project)
		span.Set("fleet.resource.response_kind", kind)
		if version != "" {
			span.Set("fleet.resource.response_version", version)
		}
	}
}

func resourceReadItems(q *Request, r *contract.ProjectResourceItemsRequest) (contract.ProjectResourceItemsReply, error) {
	limit := int64(50)
	if r.Limit != nil {
		limit = *r.Limit
	}
	if limit < 1 || limit > 100 {
		return nil, fail(400, "Expected limit 1..100")
	}
	query := ""
	if r.Query != nil {
		query = *r.Query
	}
	if len([]rune(query)) > 200 || (r.Query != nil && r.Category != "tools") {
		return nil, fail(400, "Query is tools-only, at most 200 characters")
	}
	if (r.Category == "prices") != (r.Group != nil) {
		return nil, fail(400, "Group required only for prices")
	}
	if r.Group != nil && r.Group.Currency != nil && len([]rune(*r.Group.Currency)) > 200 {
		return nil, fail(400, "Invalid currency")
	}
	choices := 0
	for _, set := range []bool{r.Cursor != nil, r.Anchor != nil, r.Around != nil} {
		if set {
			choices++
		}
	}
	if choices > 1 {
		return nil, fail(400, "Use one of cursor, anchor, around")
	}
	var cursor *resourceItemCursor
	var values []any
	var err error
	descending := r.Anchor != nil && *r.Anchor == "end"
	comparison := ">"
	var anchor contract.ResourceSortKey
	if r.Cursor != nil {
		cursorVersion := 1
		var raw []byte
		var e error
		if strings.HasPrefix(*r.Cursor, "r2.") {
			cursorVersion = 2
			raw, e = resourceUnpack(*r.Cursor, "r2.")
		} else {
			raw, e = base64.RawURLEncoding.DecodeString(*r.Cursor)
		}
		if e != nil {
			return nil, fail(400, "Invalid resource cursor")
		}

		cursor = &resourceItemCursor{}
		if e = resourceStrictJSON(raw, cursor); e != nil {
			return nil, e
		}
		if cursor.SchemaVersion != cursorVersion || cursor.ProjectID != r.ProjectId || cursor.Category != r.Category || !reflect.DeepEqual(cursor.Group, r.Group) || cursor.Query != query || (cursor.Direction != "after" && cursor.Direction != "before") {
			return nil, fail(400, "Resource cursor binding mismatch")
		}
		if !resourceEpochPattern.MatchString(cursor.BuildEpoch) {
			return nil, fail(400, "Invalid resource cursor epoch")
		}
		values, err = resourceKeyValues(cursor.Key, r.Category)
		if err != nil {
			return nil, err
		}
		descending = cursor.Direction == "before"
	}
	if r.Around != nil {
		values, err = resourceKeyValues(r.Around.Key, r.Category)
		if err != nil {
			return nil, err
		}
		descending = r.Around.Direction == "atOrBefore"
	}
	if descending {
		comparison = "<"
	}
	s, err := resourceReadPrepared(q, r.ProjectId)
	if err != nil {
		return nil, err
	}
	if cursor != nil && cursor.BuildEpoch != s.Epoch {
		return nil, fail(409, "Resource build replaced; restart with around anchor")
	}
	if !s.Ready {
		resourceTraceResponse(q, r.ProjectId, "", "preparing")
		return jsonBytes(contract.ResourcePreparingReply{Kind: "preparing", Version: json.RawMessage("null"), ProjectId: r.ProjectId, Progress: resourcePublicProgress(s)}), nil
	}
	iq := resourceItemsQuery(r, query)
	if r.Around != nil {
		inclusive := comparison + "="
		keys, _, e := iq.page(q, values, inclusive, descending, 1)
		if e != nil {
			return nil, e
		}
		if len(keys) == 0 {
			keys, _, e = iq.page(q, values, map[bool]string{true: ">=", false: "<="}[descending], !descending, 1)
			if e != nil {
				return nil, e
			}
		}
		if len(keys) > 0 {
			anchor = keys[0]
			values, err = resourceKeyValues(anchor, r.Category)
			if err != nil {
				return nil, err
			}
		}
		comparison = inclusive
	}
	keys, items, err := iq.page(q, values, comparison, descending, limit)
	if err != nil {
		return nil, err
	}
	if descending {
		slices.Reverse(keys)
		slices.Reverse(items)
	}
	if len(keys) > 0 && anchor == nil {
		anchor = keys[0]
	}
	if anchor == nil {
		anchor = json.RawMessage("null")
	}
	total := int64(0)
	switch r.Category {
	case "tools":
		total = s.Header.ItemCounts.Tools
	case "costs":
		total = s.Header.ItemCounts.Costs
	case "prices":
		total = s.Header.ItemCounts.Prices
	}
	if query != "" || r.Category == "prices" {
		if err = q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM "+iq.table+" WHERE "+iq.where, iq.args...).Scan(&total); err != nil {
			return nil, err
		}
	}
	if r.Category == "tools" {
		for i, key := range keys {
			packed, err := resourceToolPosition(key)
			if err != nil {
				return nil, err
			}
			if packed != nil {
				var item contract.ResourceToolItem
				if err = json.Unmarshal(items[i], &item); err != nil {
					return nil, err
				}
				item.Position = packed
				items[i] = jsonBytes(item)
			}
		}
		if string(anchor) != "null" {
			packed, err := resourceToolPosition(anchor)
			if err != nil {
				return nil, err
			}
			if packed != nil {
				anchor = jsonBytes(packed)
			}
		}
	}
	out := contract.ResourceItemsPage{ProjectId: r.ProjectId, Category: r.Category, Version: s.Epoch + ":" + s.Revision, Header: s.Header, Progress: resourcePublicProgress(s), Items: items, Total: total, ResolvedAnchor: anchor}
	encode := func(key contract.ResourceSortKey, direction string) (*string, error) {
		c := resourceItemCursor{SchemaVersion: 1, ProjectID: r.ProjectId, BuildEpoch: s.Epoch, Category: r.Category, Group: r.Group, Query: query, Direction: direction, Key: key}
		raw := jsonBytes(c)
		if len(raw) > resourcePackThreshold {
			c.SchemaVersion = 2
			token, err := resourcePack(jsonBytes(c), "r2.")
			return &token, err
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		return &token, nil
	}
	if len(keys) > 0 {
		more, e := iq.exists(q, keys[len(keys)-1], r.Category, ">")
		if e != nil {
			return nil, e
		}
		if more {
			out.NextCursor, err = encode(keys[len(keys)-1], "after")
			if err != nil {
				return nil, err
			}
		}
		more, e = iq.exists(q, keys[0], r.Category, "<")
		if e != nil {
			return nil, e
		}
		if more {
			out.PreviousCursor, err = encode(keys[0], "before")
			if err != nil {
				return nil, err
			}
		}
	}
	resourceTraceResponse(q, r.ProjectId, out.Version, "page")
	return jsonBytes(out), nil
}

var resourceEpochPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Each tool-key field can originate in a separate admitted 1 MiB source body.
// Go JSON can expand each source UTF-8 byte to six bytes (controls, <, >, &).
// Two full fields therefore need at most 12 MiB, plus 4096 bytes for the tuple,
// project/epoch/category, query<=200 runes, group<=200 runes and fixed envelope.
// This is a decode allocation bound, not a tool-name admission cap.
const resourceDecodedPositionMax = 12*(1<<20) + 4096
const resourcePackThreshold = 32 * 1024 // representation choice only; small positions keep their original wire form

type resourcePackedToolPosition struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Key           contract.ResourceToolKey `json:"key"`
}

func resourcePack(raw []byte, prefix string) (string, error) {
	if len(raw) > resourceDecodedPositionMax {
		return "", fail(503, "Resource position exceeds admitted-source envelope")
	}
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	if _, err := w.Write(raw); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	token := prefix + base64.RawURLEncoding.EncodeToString(b.Bytes())
	// Reserve the bounded query/group/project and JSON request envelope. Never emit a
	// token that this operation cannot receive under the unchanged shared HTTP bound.
	if len(token) > (1<<20)-4096 {
		return "", fail(503, "Resource position exceeds HTTP envelope")
	}
	return token, nil
}
func resourceUnpack(token, prefix string) ([]byte, error) {
	invalid := func() ([]byte, error) { return nil, fail(400, "Invalid compressed resource position") }
	if !strings.HasPrefix(token, prefix) {
		return invalid()
	}
	compressed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, prefix))
	if err != nil {
		return invalid()
	}
	source := bytes.NewReader(compressed)
	r, err := zlib.NewReader(source)
	if err != nil {
		return invalid()
	}
	raw, readErr := io.ReadAll(io.LimitReader(r, resourceDecodedPositionMax+1))
	closeErr := r.Close()
	if readErr != nil || closeErr != nil || len(raw) > resourceDecodedPositionMax || source.Len() != 0 {
		return invalid()
	}
	return raw, nil
}
func resourceToolPosition(key contract.ResourceSortKey) (*contract.ResourcePackedToolKey, error) {
	// Canonical serialization includes JSON escaping expansion in the threshold decision.
	var k contract.ResourceToolKey
	if err := json.Unmarshal(key, &k); err != nil {
		return nil, err
	}
	raw := jsonBytes(resourcePackedToolPosition{SchemaVersion: 1, Key: k})
	if len(raw) <= resourcePackThreshold {
		return nil, nil
	}
	token, err := resourcePack(raw, "tp1.")
	if err != nil {
		return nil, err
	}
	return &contract.ResourcePackedToolKey{Kind: "tool-position", Encoded: token}, nil
}
