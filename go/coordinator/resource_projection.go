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
	"log"
	"math/big"
	"os"
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
var errResourceClearing = errors.New("resource build clearing")

func init() { onStart(startResourceProcessor) }

func startResourceProcessor(ctx context.Context, a *App) {
	// Processor scheduling is independent of the startup read-exposure flag; capture is always on.
	if os.Getenv("FLEET_RESOURCE_PROCESSOR") != "on" {
		return
	}

	wake := make(chan struct{}, 1)
	go resourceListen(ctx, a.store.pool.Config().ConnConfig.ConnString(), wake)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for ctx.Err() == nil {
			if err := resourceProcessorPass(ctx, a); err != nil && ctx.Err() == nil {
				log.Printf("resource processor: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-ticker.C:
			}
		}
	}()
}

// One listener per opted-in process; a failed listener is only a scheduling degradation.
func resourceListen(ctx context.Context, url string, wake chan<- struct{}) {
	for ctx.Err() == nil {
		conn, err := pgx.Connect(ctx, url)
		if err == nil {
			_, err = conn.Exec(ctx, "LISTEN fleet_resource_work")
			for err == nil {
				_, err = conn.WaitForNotification(ctx)
				if err == nil {
					select {
					case wake <- struct{}{}:
					default:
					}
				}
			}
			_ = conn.Close(context.Background())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

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
func resourceNum(v any) *string {
	if n, ok := v.(json.Number); ok {
		s := string(n)
		return &s
	}
	return nil
}
func resourceText(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}
func resourceInt(v any) *int64 {
	if n := resourceNum(v); n != nil {
		i, e := strconv.ParseInt(*n, 10, 64)
		if e == nil {
			return &i
		}
	}
	return nil
}
func resourceCount(v any) int64 {
	n := resourceInt(v)
	if n == nil {
		return 0
	}
	return *n
}
func resourceBasis(v any) contract.ResourceBasis {
	b := contract.ResourceBasis(astr(v))
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
	var l map[string]any
	dec := json.NewDecoder(bytes.NewReader(s.Leaf))
	dec.UseNumber()
	if err := dec.Decode(&l); err != nil {
		return c, contract.ResourceMethodV1{}, err
	}
	if l == nil {
		return c, contract.ResourceMethodV1{}, nil
	}
	c.ProjectId = resourceText(l["project_id"])
	c.Kind = astr(l["kind"])
	c.State = astr(l["state"])
	c.HasTotals, _ = l["has_totals"].(bool)
	c.Included = c.ProjectId != nil && c.Kind != "pipeline_run" && c.Kind != "pipeline_step" && (c.Kind != "child_agent" || c.HasTotals)
	c.StartedMs = resourceInt(l["started_ms"])
	c.EndedMs = resourceInt(l["ended_ms"])
	c.TokenActor = c.Kind == "agent_run" || c.Kind == "child_agent"
	for _, k := range []string{"tokens_input", "tokens_output", "tokens_cache_read", "tokens_cache_write", "tokens_reasoning"} {
		c.TokenActor = c.TokenActor || l[k] != nil
	}
	c.ProcessActor = c.Kind != "tool_run"
	c.ToolActor = c.Kind == "agent_run" || c.Kind == "child_agent" || c.Kind == "tool_run"
	c.HistoryIncomplete = resourceCount(l["history_incomplete"]) != 0
	for _, m := range []struct {
		name, col, reason string
		expected          bool
	}{
		{"input", "tokens_input", "", c.TokenActor}, {"output", "tokens_output", "", c.TokenActor}, {"cacheRead", "tokens_cache_read", "", c.TokenActor}, {"cacheWrite", "tokens_cache_write", "", c.TokenActor}, {"reasoning", "tokens_reasoning", "", c.TokenActor},
		{"wallMs", "wall_ms", "", true}, {"activeMs", "active_ms", "", true}, {"apiMs", "api_ms", "", true},
		{"cpuMs", "cpu_ms", "cpu_missing_reason", c.ProcessActor}, {"peakMemoryBytes", "peak_memory_bytes", "memory_missing_reason", c.ProcessActor},
		{"turns", "turn_count", "", true}, {"credits", "credits", "", false},
	} {
		v := resourceNum(l[m.col])
		if !m.expected && m.name != "credits" {
			v = nil
		}
		b := resourceBasis(l[m.col+"_basis"])
		if m.name == "credits" {
			b = resourceBasis(l["connection_cost_basis"])
		}
		if v == nil {
			b = contract.ResourceBasisUnavailable
		}
		c.Metrics[m.name] = contract.ResourceMetricV1{Value: v, Basis: b, Expected: m.expected, Reason: resourceText(l[m.reason])}
	}
	if c.Kind == "tool_run" {
		name := astr(l["tool_name"])
		if name == "" {
			name = "tool_run"
		}
		e := int64(0)
		if c.State == "failed" || c.State == "cancelled" {
			e = 1
		}
		c.Tools = append(c.Tools, contract.ResourceToolV1{Connection: astr(l["connection"]), Name: name, Calls: 1, Errors: e})
	} else if c.ToolActor && l["tool_calls"] != nil {
		tc, _ := l["tool_calls"].(map[string]any)
		bt, _ := tc["byTool"].(map[string]any)
		for name, raw := range bt {
			v, _ := raw.(map[string]any)
			c.Tools = append(c.Tools, contract.ResourceToolV1{Name: name, Calls: resourceCount(v["calls"]), Errors: resourceCount(v["errors"])})
		}
		other, _ := tc["other"].(map[string]any)
		c.OtherCalls = resourceCount(other["calls"])
		c.OtherErrors = resourceCount(other["errors"])
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
	tb := resourceBasis(l["tool_calls_basis"])
	present := c.ToolActor && (c.Kind == "tool_run" || l["tool_calls"] != nil)
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
	method := contract.ResourceMethodV1{SchemaVersion: 1, Model: resourceText(l["model"]), Connection: resourceText(l["connection"]), Provider: resourceText(l["provider"]), Components: s.Candidates}
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

func resourceValueCosts(c *contract.ResourceContributionV1, l map[string]any, m contract.ResourceMethodV1) error {
	sub, _ := l["is_sub"].(bool)
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
	if c.Kind == "tool_run" || l["connection_cost"] != nil {
		if currency := resourceText(l["direct_currency"]); currency != nil {
			b := contract.ResourceBasisMeasured
			if sub {
				b = contract.ResourceBasisSubscription
			}
			if err := add(currency, b, resourceNum(l["direct_amount"]), nil, 0); err != nil {
				return err
			}
		} else if err := price(astr(l["conn_subject"]), astr(l["conn_unit"]), resourceNum(l["conn_amount"])); err != nil {
			return err
		}
	} else {
		observed := false
		for _, p := range []struct{ col, unit string }{{"tokens_input", "input_tokens"}, {"tokens_output", "output_tokens"}, {"tokens_cache_read", "cache_read_tokens"}, {"tokens_cache_write", "cache_write_tokens"}} {
			n := resourceNum(l[p.col])
			if n != nil {
				observed = true
				r, ok := new(big.Rat).SetString(*n)
				if !ok {
					return errors.New("invalid token decimal")
				}
				if r.Sign() > 0 {
					if err := price(astr(l["model"]), p.unit, n); err != nil {
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

func resourceScopes(q *Request, id string) ([]string, error) {
	rs, err := q.tx.Query(q.ctx(), `SELECT p FROM (
 SELECT project_id p FROM execution WHERE id=$1 UNION SELECT project_id FROM execution_totals WHERE execution_id=$1
 UNION SELECT project_id FROM resource_contribution WHERE execution_id=$1
 UNION SELECT s.project_id FROM resource_work_scope s JOIN resource_work w USING(work_id) WHERE w.execution_id=$1
 ) v WHERE p IS NOT NULL ORDER BY p COLLATE "C"`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rs, pgx.RowTo[string])
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

// resourceReplace consumes only the UUIDs claimed by this attempt. The callback is replayable:
// every retry resets its local claims, snapshot and candidate. Scope expansion restarts the whole Tx.
func resourceReplace(ctx context.Context, a *App, id string) error {
	ctx, span := obs.Start(obs.WithRun(ctx, obs.Run{ExecutionID: id}), "resource.replace", "internal")
	var finalErr error
	defer func() { span.End(finalErr) }()
	projects := []string{}
	claimed := []string{}
	for {
		err := runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error {
			ps, e := resourceScopes(q, id)
			if e == nil {
				projects = ps
			}
			return e
		})
		if err != nil {
			finalErr = err
			return err
		}
		locks := []string{"execution:" + id}
		for _, p := range projects {
			locks = append(locks, "project:"+p)
		}
		lockStart := time.Now()
		err = runTxLocks(ctx, a, nil, true, locks, func(q *Request) error {
			span.Set("fleet.resource.lock_ms", strconv.FormatInt(time.Since(lockStart).Milliseconds(), 10))
			claimed = nil
			ps, e := resourceScopes(q, id)
			if e != nil {
				return e
			}
			if e = resourceCheckLocks(q, ps); e != nil {
				return e
			}
			rs, e := q.tx.Query(q.ctx(), `SELECT work_id::text FROM resource_work WHERE execution_id=$1 AND next_attempt_at<=clock_timestamp() ORDER BY work_id FOR UPDATE SKIP LOCKED`, id)
			if e != nil {
				return e
			}
			claimed, e = pgx.CollectRows(rs, pgx.RowTo[string])
			if e != nil || len(claimed) == 0 {
				return e
			}
			var raw []byte
			if e = q.tx.QueryRow(q.ctx(), resourceSourceSQL, id).Scan(&raw); e != nil {
				return e
			}
			span.Set("fleet.resource.snapshot_bytes", strconv.Itoa(len(raw)))
			var s resourceSnapshot
			if e = json.Unmarshal(raw, &s); e != nil {
				return e
			}
			c, m, e := resourceContribution(id, s)
			if e != nil {
				return e
			}
			affected := []string{}
			if c.ProjectId != nil {
				affected = append(affected, *c.ProjectId)
			}
			if s.Old != nil && s.Old.Facts.ProjectId != nil {
				affected = append(affected, *s.Old.Facts.ProjectId)
			}
			if e = resourceCheckLocks(q, append(affected, s.Scopes...)); e != nil {
				return e
			}
			// Also recheck the actual totals owner from this same source snapshot, via mismatch repair.
			if s.Mismatch {
				return errResourceOwnership
			}
			for _, p := range projects {
				if e = resourceEnsureProject(q, p); e != nil {
					return e
				}
				var phase string
				if e = q.tx.QueryRow(q.ctx(), `SELECT phase FROM resource_build WHERE project_id=$1`, p).Scan(&phase); e != nil {
					return e
				}
				if phase == "clearing" {
					return errResourceClearing
				}
				if _, e = q.tx.Exec(q.ctx(), `SELECT 1 FROM resource_project_summary WHERE project_id=$1 FOR UPDATE`, p); e != nil {
					return e
				}
			}
			sha, msha := resourceSHA(s.Fingerprint), resourceSHA(m)
			sourceExists := string(s.Leaf) != "null"
			equal := s.Old != nil && sourceExists && s.Old.SourceSHA == sha && s.Old.MethodSHA == msha && s.Old.Facts.Included == c.Included
			if !equal {
				// Summary rows are already locked in project order; all support writes are deterministic.
				for _, p := range projects {
					if s.Old != nil && s.Old.Facts.ProjectId != nil && *s.Old.Facts.ProjectId == p {
						if e = resourceApply(q, s.Old.Facts, -1); e != nil {
							return e
						}
					}
					if sourceExists && c.ProjectId != nil && *c.ProjectId == p {
						if e = resourceApply(q, c, 1); e != nil {
							return e
						}
					}
				}
				if sourceExists {
					if c.Included && s.ValuationInput {
						if e = exec(q, `INSERT INTO resource_valuation(execution_id,method_sha,method) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, msha, jsonBytes(m)); e != nil {
							return e
						}
					}
					if e = exec(q, `INSERT INTO resource_contribution(execution_id,project_id,source_sha,included,facts,method,method_sha) VALUES($1,$2,$3,$4,$5,$6,$7)
      ON CONFLICT(execution_id) DO UPDATE SET project_id=excluded.project_id,source_sha=excluded.source_sha,included=excluded.included,facts=excluded.facts,method=excluded.method,method_sha=excluded.method_sha,updated_at=clock_timestamp()`, id, c.ProjectId, sha, c.Included, jsonBytes(c), jsonBytes(m), msha); e != nil {
						return e
					}
				} else {
					if e = exec(q, `DELETE FROM resource_contribution WHERE execution_id=$1`, id); e != nil {
						return e
					}
				}
				changed := map[string]bool{}
				if c.Included && c.ProjectId != nil {
					changed[*c.ProjectId] = true
				}
				if s.Old != nil && s.Old.Facts.Included && s.Old.Facts.ProjectId != nil {
					changed[*s.Old.Facts.ProjectId] = true
				}
				for _, p := range projects {
					if changed[p] {
						if e = resourcePublish(q, p); e != nil {
							return e
						}
					}
				}
			}
			return exec(q, `DELETE FROM resource_work WHERE work_id=ANY($1::uuid[])`, claimed)
		})
		if errors.Is(err, errResourceScope) {
			continue
		}
		if errors.Is(err, errResourceClearing) {
			return nil
		} // Clearing preserves every claimed UUID.
		if err != nil {
			finalErr = err
			// Retry state is separate: rollback must never commit a half replacement.
			if len(claimed) > 0 && ctx.Err() == nil {
				stateErr := runTxLocks(ctx, a, nil, true, locks, func(q *Request) error {
					return exec(q, `UPDATE resource_work SET attempts=attempts+1,
     next_attempt_at=clock_timestamp()+least(1000,100*power(2,least(attempts,4))) * interval '1 millisecond',last_error_class=$2
     WHERE work_id=ANY($1::uuid[])`, claimed, resourceErrorClass(err))
				})
				if stateErr != nil {
					return stateErr
				}
			}
			if errors.Is(err, errResourceOwnership) {
				return repairTotalsOwnership(ctx, a, id)
			}
		}
		return err
	}
}
func resourceErrorClass(err error) string {
	if errors.Is(err, errResourceOwnership) {
		return "ownership_mismatch"
	}
	return "projection_failed"
}

// repairTotalsOwnership is a separate source transaction, parent FIRST. It never runs in replacement.
func repairTotalsOwnership(ctx context.Context, a *App, id string) error {
	for {
		var projects []string
		if err := runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error { var e error; projects, e = resourceScopes(q, id); return e }); err != nil {
			return err
		}
		locks := []string{"execution:" + id}
		for _, p := range projects {
			locks = append(locks, "project:"+p)
		}
		err := runTxLocks(ctx, a, nil, true, locks, func(q *Request) error {
			var parent *string
			e := q.tx.QueryRow(q.ctx(), `SELECT project_id FROM execution WHERE id=$1 FOR UPDATE`, id).Scan(&parent)
			if errors.Is(e, pgx.ErrNoRows) {
				return nil
			}
			if e != nil {
				return e
			}
			ps, e := resourceScopes(q, id)
			if e != nil {
				return e
			}
			if e = resourceCheckLocks(q, ps); e != nil {
				return e
			}
			return exec(q, `UPDATE execution_totals SET project_id=$2 WHERE execution_id=$1 AND project_id IS DISTINCT FROM $2`, id, parent)
		})
		if !errors.Is(err, errResourceScope) {
			return err
		}
	}
}

func resourceApply(q *Request, c contract.ResourceContributionV1, sign int64) error {
	if !c.Included || c.ProjectId == nil {
		return nil
	}
	p := *c.ProjectId
	history := int64(0)
	if c.HistoryIncomplete {
		history = sign
	}
	if err := exec(q, `UPDATE resource_project_summary SET executions=executions+$2,history_incomplete=history_incomplete+$3,
  other_calls=other_calls+$4,other_errors=other_errors+$5 WHERE project_id=$1`, p, sign, history, sign*c.OtherCalls, sign*c.OtherErrors); err != nil {
		return err
	}
	keys := make([]string, 0, len(c.Metrics))
	for k := range c.Metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		m := c.Metrics[k]
		if !contract.ResourceMetricNameV1(k).Valid() {
			return errors.New("unknown contribution metric")
		}
		present, expected := int64(0), int64(0)
		value := "0"
		if m.Value != nil {
			present = sign
			value = *m.Value
		}
		if m.Expected {
			expected = sign
		}
		reason := deref(m.Reason)
		// A subtract updates an existing support row; constraints deliberately reject corrupt ledgers.
		if sign > 0 {
			if err := exec(q, `INSERT INTO resource_metric(project_id,metric,basis,present_count,expected_count,sum_value,reasons)
    VALUES($1,$2,$3,$4,$5,$6::numeric,CASE WHEN $7='' THEN '{}'::jsonb ELSE jsonb_build_object($7,1) END)
    ON CONFLICT(project_id,metric,basis) DO UPDATE SET present_count=resource_metric.present_count+excluded.present_count,
     expected_count=resource_metric.expected_count+excluded.expected_count,sum_value=resource_metric.sum_value+excluded.sum_value,
     reasons=CASE WHEN $7='' THEN resource_metric.reasons ELSE jsonb_set(resource_metric.reasons,ARRAY[$7],to_jsonb(coalesce((resource_metric.reasons->>$7)::bigint,0)+1)) END`, p, k, m.Basis, present, expected, value, reason); err != nil {
				return err
			}
		} else {
			if err := exec(q, `UPDATE resource_metric SET present_count=present_count+$4,expected_count=expected_count+$5,sum_value=sum_value-$6::numeric,
    reasons=CASE WHEN $7='' THEN reasons WHEN (reasons->>$7)::bigint=1 THEN reasons-$7 ELSE jsonb_set(reasons,ARRAY[$7],to_jsonb((reasons->>$7)::bigint-1)) END
    WHERE project_id=$1 AND metric=$2 AND basis=$3`, p, k, m.Basis, present, expected, value, reason); err != nil {
				return err
			}
		}
		if err := exec(q, `DELETE FROM resource_metric WHERE project_id=$1 AND metric=$2 AND basis=$3 AND present_count=0 AND expected_count=0 AND reasons='{}'`, p, k, m.Basis); err != nil {
			return err
		}
	}
	for _, v := range []struct {
		metric string
		value  *int64
	}{{"start", c.StartedMs}, {"end", c.EndedMs}, {"peakMemory", resourceInt(json.Number(deref(c.Metrics["peakMemoryBytes"].Value)))}} {
		if v.value == nil {
			continue
		}
		if sign > 0 {
			if err := exec(q, `INSERT INTO resource_extreme(project_id,metric,value,multiplicity) VALUES($1,$2,$3,1)
   ON CONFLICT(project_id,metric,value) DO UPDATE SET multiplicity=resource_extreme.multiplicity+1`, p, v.metric, *v.value); err != nil {
				return err
			}
		} else {
			ct, err := q.tx.Exec(q.ctx(), `DELETE FROM resource_extreme WHERE project_id=$1 AND metric=$2 AND value=$3 AND multiplicity=1`, p, v.metric, *v.value)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				if err = exec(q, `UPDATE resource_extreme SET multiplicity=multiplicity-1 WHERE project_id=$1 AND metric=$2 AND value=$3`, p, v.metric, *v.value); err != nil {
					return err
				}
			}
		}
	}
	for _, t := range c.Tools {
		delta := int64(0)
		if sign > 0 {
			var inserted bool
			if err := q.tx.QueryRow(q.ctx(), `INSERT INTO resource_tool(project_id,connection,name,calls,errors,references_count) VALUES($1,$2,$3,$4,$5,1)
    ON CONFLICT(project_id,connection,name) DO UPDATE SET calls=resource_tool.calls+excluded.calls,errors=resource_tool.errors+excluded.errors,references_count=resource_tool.references_count+1 RETURNING (xmax=0)`, p, t.Connection, t.Name, t.Calls, t.Errors).Scan(&inserted); err != nil {
				return err
			}
			if inserted {
				delta = 1
			}
		} else {
			ct, err := q.tx.Exec(q.ctx(), `DELETE FROM resource_tool WHERE project_id=$1 AND connection=$2 AND name=$3 AND references_count=1`, p, t.Connection, t.Name)
			if err != nil {
				return err
			}
			if ct.RowsAffected() > 0 {
				delta = -1
			} else if err = exec(q, `UPDATE resource_tool SET calls=calls-$4,errors=errors-$5,references_count=references_count-1 WHERE project_id=$1 AND connection=$2 AND name=$3`, p, t.Connection, t.Name, t.Calls, t.Errors); err != nil {
				return err
			}
		}
		if err := exec(q, `UPDATE resource_project_summary SET tool_count=tool_count+$2 WHERE project_id=$1`, p, delta); err != nil {
			return err
		}
	}
	for _, cost := range c.Costs {
		delta := int64(0)
		if sign > 0 {
			var inserted bool
			if err := q.tx.QueryRow(q.ctx(), `INSERT INTO resource_cost(project_id,currency,basis,value,usages,references_count) VALUES($1,$2,$3,$4::numeric,$5,1)
    ON CONFLICT(project_id,currency,basis) DO UPDATE SET value=resource_cost.value+excluded.value,usages=resource_cost.usages+excluded.usages,references_count=resource_cost.references_count+1 RETURNING (xmax=0)`, p, deref(cost.Currency), cost.Basis, cost.Amount, cost.UnpricedCount).Scan(&inserted); err != nil {
				return err
			}
			if inserted {
				delta = 1
			}
		} else {
			ct, err := q.tx.Exec(q.ctx(), `DELETE FROM resource_cost WHERE project_id=$1 AND currency=$2 AND basis=$3 AND references_count=1`, p, deref(cost.Currency), cost.Basis)
			if err != nil {
				return err
			}
			if ct.RowsAffected() > 0 {
				delta = -1
			} else if err = exec(q, `UPDATE resource_cost SET value=value-$4::numeric,usages=usages-$5,references_count=references_count-1 WHERE project_id=$1 AND currency=$2 AND basis=$3`, p, deref(cost.Currency), cost.Basis, cost.Amount, cost.UnpricedCount); err != nil {
				return err
			}
		}
		if err := exec(q, `UPDATE resource_project_summary SET cost_count=cost_count+$2 WHERE project_id=$1`, p, delta); err != nil {
			return err
		}
		for _, r := range cost.PriceRefs {
			delta = 0
			if sign > 0 {
				var inserted bool
				if err := q.tx.QueryRow(q.ctx(), `INSERT INTO resource_price_ref(project_id,currency,basis,price_id,observed_at,references_count) VALUES($1,$2,$3,$4,$5,1)
     ON CONFLICT(project_id,currency,basis,price_id,observed_at) DO UPDATE SET references_count=resource_price_ref.references_count+1 RETURNING (xmax=0)`, p, deref(cost.Currency), cost.Basis, r.PriceId, r.ObservedAt).Scan(&inserted); err != nil {
					return err
				}
				if inserted {
					delta = 1
				}
			} else {
				ct, err := q.tx.Exec(q.ctx(), `DELETE FROM resource_price_ref WHERE project_id=$1 AND currency=$2 AND basis=$3 AND price_id=$4 AND observed_at=$5 AND references_count=1`, p, deref(cost.Currency), cost.Basis, r.PriceId, r.ObservedAt)
				if err != nil {
					return err
				}
				if ct.RowsAffected() > 0 {
					delta = -1
				} else if err = exec(q, `UPDATE resource_price_ref SET references_count=references_count-1 WHERE project_id=$1 AND currency=$2 AND basis=$3 AND price_id=$4 AND observed_at=$5`, p, deref(cost.Currency), cost.Basis, r.PriceId, r.ObservedAt); err != nil {
					return err
				}
			}
			if err := exec(q, `UPDATE resource_project_summary SET price_count=price_count+$2 WHERE project_id=$1`, p, delta); err != nil {
				return err
			}
		}
	}
	return nil
}

// resourcePreparedHeader reads only fixed-size metric support and indexed extremes. Detail support
// counts are maintained during replacement, so high-cardinality tools/prices never expand the header.
func resourcePreparedHeader(q *Request, p string) (contract.PreparedHeaderV1, error) {
	empty := emptyResourceSummary()
	h := contract.PreparedHeaderV1{Tokens: empty.Tokens, Durations: empty.Durations, PeakMemoryBytes: empty.PeakMemoryBytes, Tools: contract.PreparedToolsV1{Basis: contract.ResourceBasisUnavailable}}
	var other int64
	if err := q.tx.QueryRow(q.ctx(), `SELECT executions,other_calls,tool_count,cost_count,price_count FROM resource_project_summary WHERE project_id=$1`, p).Scan(&h.Executions, &other, &h.ItemCounts.Tools, &h.ItemCounts.Costs, &h.ItemCounts.Prices); err != nil {
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
	rs, err := q.tx.Query(q.ctx(), `SELECT metric::text,basis,present_count,expected_count,sum_value::text FROM resource_metric WHERE project_id=$1 ORDER BY metric,basis`, p)
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
	if err = q.tx.QueryRow(q.ctx(), `SELECT
 (SELECT value FROM resource_extreme WHERE project_id=$1 AND metric='start' ORDER BY value LIMIT 1),
 (SELECT value FROM resource_extreme WHERE project_id=$1 AND metric='end' ORDER BY value DESC LIMIT 1),
 (SELECT value FROM resource_extreme WHERE project_id=$1 AND metric='peakMemory' ORDER BY value DESC LIMIT 1)`, p).Scan(&start, &end, &memory); err != nil {
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
func resourcePublish(q *Request, p string) error {
	h, err := resourcePreparedHeader(q, p)
	if err != nil {
		return err
	}
	if err = exec(q, `UPDATE resource_project_summary SET header=$2,revision=revision+1,published_at=clock_timestamp() WHERE project_id=$1`, p, jsonBytes(h)); err != nil {
		return err
	}
	return exec(q, `SELECT pg_notify('fleet_resource_summary',$1)`, p)
}

// resourceProgressSQL is used by finalization; authorized readers use the same coverage predicates.
// Unenumerated metadata jobs conservatively keep ALL scopes pending until enumeration completes.
const resourceProgressSQL = `SELECT EXISTS(SELECT 1 FROM resource_work_scope WHERE project_id=$1)
 OR EXISTS(SELECT 1 FROM resource_repair_scope WHERE project_id=$1)
 OR EXISTS(SELECT 1 FROM resource_metadata_repair WHERE NOT done)`

func resourceBuildPage(ctx context.Context, a *App, p string) error {
	return runTxLocks(ctx, a, nil, true, []string{"project:" + p}, func(q *Request) error {
		if err := resourceEnsureProject(q, p); err != nil {
			return err
		}
		var generation, phase string
		var after *string
		var scanDone, ready bool
		if err := q.tx.QueryRow(q.ctx(), `SELECT generation::text,phase,after_execution_id,scan_done,ready FROM resource_build WHERE project_id=$1 FOR UPDATE`, p).Scan(&generation, &phase, &after, &scanDone, &ready); err != nil {
			return err
		}
		if phase == "ready" {
			return nil
		}
		if phase == "clearing" {
			// Delete disposable projection state in bounded key pages. Source work and valuation survive.
			var removed int64
			for _, table := range []struct{ name, order string }{{"resource_contribution", `execution_id COLLATE "C"`}, {"resource_metric", "metric,basis"}, {"resource_extreme", "metric,value"}, {"resource_tool", `name COLLATE "C",connection COLLATE "C"`}, {"resource_cost", "currency,basis"}, {"resource_price_ref", `currency,basis,price_id COLLATE "C",observed_at`}} {
				ct, err := q.tx.Exec(q.ctx(), `DELETE FROM `+table.name+` WHERE ctid IN (SELECT ctid FROM `+table.name+` WHERE project_id=$1 ORDER BY `+table.order+` LIMIT 100)`, p)
				if err != nil {
					return err
				}
				removed += ct.RowsAffected()
			}
			if removed == 0 {
				if err := exec(q, `UPDATE resource_project_summary SET executions=0,history_incomplete=0,other_calls=0,other_errors=0,tool_count=0,cost_count=0,price_count=0,header='{}',revision=0,published_at=NULL WHERE project_id=$1`, p); err != nil {
					return err
				}
				return exec(q, `UPDATE resource_build SET phase='scanning',after_execution_id=NULL,scan_done=false,last_error=NULL,updated_at=clock_timestamp() WHERE project_id=$1`, p)
			}
			return exec(q, `UPDATE resource_build SET updated_at=clock_timestamp() WHERE project_id=$1`, p)
		}
		if !scanDone {
			op, start := ">=", ""
			if after != nil {
				op, start = ">", *after
			}
			rs, err := q.tx.Query(q.ctx(), `SELECT id FROM execution WHERE project_id=$1 AND id COLLATE "C" `+op+` $2 COLLATE "C" ORDER BY id COLLATE "C" LIMIT 100`, p, start)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rs, pgx.RowTo[string])
			if err != nil {
				return err
			}
			if err = resourceSeedWork(q, ids, "build", &generation, nil); err != nil {
				return err
			}
			if len(ids) > 0 {
				after = &ids[len(ids)-1]
			}
			return exec(q, `UPDATE resource_build SET after_execution_id=$2,scan_done=$3,phase=CASE WHEN $3 THEN 'draining' ELSE 'scanning' END,last_error=NULL,updated_at=clock_timestamp() WHERE project_id=$1`, p, after, len(ids) == 0)
		}
		var pending bool
		if err := q.tx.QueryRow(q.ctx(), resourceProgressSQL, p).Scan(&pending); err != nil {
			return err
		}
		if pending {
			return exec(q, `UPDATE resource_build SET updated_at=clock_timestamp() WHERE project_id=$1`, p)
		}
		if err := exec(q, `UPDATE resource_build SET phase='ready',ready=true,last_error=NULL,updated_at=clock_timestamp() WHERE project_id=$1`, p); err != nil {
			return err
		}
		if err := exec(q, `UPDATE resource_project_summary SET ready=true WHERE project_id=$1`, p); err != nil {
			return err
		}
		return resourcePublish(q, p)
	})
}

// resourceRebuild is internal maintenance, never called by a reader. The new epoch stays
// preparing until clearing, scan and drain complete. No frozen valuation or source work is erased.
func resourceRebuild(ctx context.Context, a *App, p string) error {
	return runTxLocks(ctx, a, nil, true, []string{"project:" + p}, func(q *Request) error {
		if err := resourceEnsureProject(q, p); err != nil {
			return err
		}
		if err := exec(q, `UPDATE resource_build SET generation=gen_random_uuid(),phase='clearing',ready=false,scan_done=false,after_execution_id=NULL,last_error=NULL,updated_at=clock_timestamp() WHERE project_id=$1`, p); err != nil {
			return err
		}
		return exec(q, `UPDATE resource_project_summary SET epoch=gen_random_uuid(),ready=false,build_generation=(SELECT generation FROM resource_build WHERE project_id=$1) WHERE project_id=$1`, p)
	})
}

func resourceSeedWork(q *Request, ids []string, origin string, generation, repair *string) error {
	// One SQL statement seeds identities AND all known old/current project scopes.
	return exec(q, `WITH seeded AS (
 INSERT INTO resource_work(execution_id,origin,build_generation,repair_id)
 SELECT unnest($1::text[]),$2,$3::uuid,$4::uuid ON CONFLICT ON CONSTRAINT resource_work_source_key DO UPDATE SET execution_id=excluded.execution_id RETURNING work_id,execution_id)
 INSERT INTO resource_work_scope(work_id,project_id)
 SELECT w.work_id,v.project_id FROM seeded w CROSS JOIN LATERAL (
 SELECT project_id FROM execution WHERE id=w.execution_id UNION SELECT project_id FROM resource_contribution WHERE execution_id=w.execution_id) v
 WHERE v.project_id IS NOT NULL ON CONFLICT DO NOTHING`, ids, origin, generation, repair)
}

func resourceMetadataPage(ctx context.Context, a *App, repair string) error {
	return runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error {
		var old, new []byte
		var after *string
		var done bool
		err := q.tx.QueryRow(q.ctx(), `SELECT old_class,new_class,after_execution_id,done FROM resource_metadata_repair WHERE repair_id=$1::uuid FOR UPDATE SKIP LOCKED`, repair).Scan(&old, &new, &after, &done)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if done {
			// A repair scope remains pending until every seeded work UUID has drained.
			return exec(q, `DELETE FROM resource_metadata_repair r WHERE repair_id=$1::uuid AND NOT EXISTS(SELECT 1 FROM resource_work w WHERE w.repair_id=r.repair_id)`, repair)
		}
		accounts, connections := []string{}, []string{}
		for _, raw := range [][]byte{old, new} {
			var c struct {
				AccountID      *string `json:"accountId"`
				ConnectionName *string `json:"connectionName"`
			}
			if err = json.Unmarshal(raw, &c); err != nil {
				return err
			}
			if c.AccountID != nil {
				accounts = append(accounts, *c.AccountID)
			}
			if c.ConnectionName != nil {
				connections = append(connections, *c.ConnectionName)
			}
		}
		op, start := ">=", ""
		if after != nil {
			op, start = ">", *after
		}
		rs, err := q.tx.Query(q.ctx(), `SELECT id,project_id FROM execution WHERE id IN (
   SELECT id FROM (
    SELECT x.id FROM unnest($1::text[]) a(account) CROSS JOIN LATERAL (SELECT id FROM execution WHERE account_id=a.account AND id COLLATE "C" `+op+` $3 COLLATE "C" ORDER BY id COLLATE "C" LIMIT 100) x
    UNION SELECT x.id FROM unnest($2::text[]) c(connection) CROSS JOIN LATERAL (SELECT id FROM execution WHERE data->>'connection'=c.connection AND id COLLATE "C" `+op+` $3 COLLATE "C" ORDER BY id COLLATE "C" LIMIT 100) x
   ) ids ORDER BY id COLLATE "C" LIMIT 100) ORDER BY id COLLATE "C"`, accounts, connections, start)
		if err != nil {
			return err
		}
		type member struct {
			id      string
			project *string
		}
		members, err := pgx.CollectRows(rs, func(r pgx.CollectableRow) (member, error) { var m member; e := r.Scan(&m.id, &m.project); return m, e })
		if err != nil {
			return err
		}
		ids := []string{}
		for _, m := range members {
			ids = append(ids, m.id)
		}
		if err = resourceSeedWork(q, ids, "metadata", nil, &repair); err != nil {
			return err
		}
		// Include accounted old scopes from the seeded work too; a concurrent move cannot release coverage.
		if err = exec(q, `INSERT INTO resource_repair_scope(repair_id,project_id) SELECT DISTINCT w.repair_id,s.project_id FROM resource_work w JOIN resource_work_scope s USING(work_id) WHERE w.repair_id=$1::uuid ON CONFLICT DO NOTHING`, repair); err != nil {
			return err
		}
		if len(ids) > 0 {
			after = &ids[len(ids)-1]
		}
		return exec(q, `UPDATE resource_metadata_repair SET after_execution_id=$2,done=$3,last_error=NULL,next_attempt_at=clock_timestamp() WHERE repair_id=$1::uuid`, repair, after, len(ids) == 0)
	})
}

func resourceProcessorPass(ctx context.Context, a *App) error {
	ctx, span := obs.Start(ctx, "resource.process", "internal")
	var finalErr error
	defer func() { span.End(finalErr) }()
	// Discover projects from the configuration catalogue, never by rescanning execution history.
	var projects, repairs, ids []string
	err := runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error {
		rs, e := q.tx.Query(q.ctx(), `SELECT id FROM definition d WHERE kind='project' AND NOT EXISTS(SELECT 1 FROM resource_build b WHERE b.project_id=d.id) ORDER BY id COLLATE "C" LIMIT 100`)
		if e != nil {
			return e
		}
		projects, e = pgx.CollectRows(rs, pgx.RowTo[string])
		return e
	})
	if err != nil {
		finalErr = err
		return err
	}
	for _, p := range projects {
		if err = runTxLocks(ctx, a, nil, true, []string{"project:" + p}, func(q *Request) error { return resourceEnsureProject(q, p) }); err != nil {
			finalErr = err
		}
	}
	err = runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error {
		rs, e := q.tx.Query(q.ctx(), `SELECT project_id FROM resource_build WHERE NOT ready ORDER BY updated_at,project_id COLLATE "C" LIMIT 1`)
		if e != nil {
			return e
		}
		projects, e = pgx.CollectRows(rs, pgx.RowTo[string])
		if e != nil {
			return e
		}
		rs, e = q.tx.Query(q.ctx(), `SELECT repair_id::text FROM resource_metadata_repair r WHERE next_attempt_at<=clock_timestamp()
 AND (NOT done OR NOT EXISTS(SELECT 1 FROM resource_work w WHERE w.repair_id=r.repair_id))
 ORDER BY next_attempt_at,created_at LIMIT 1`)
		if e != nil {
			return e
		}
		repairs, e = pgx.CollectRows(rs, pgx.RowTo[string])
		if e != nil {
			return e
		}
		rs, e = q.tx.Query(q.ctx(), `SELECT execution_id FROM (SELECT execution_id,next_attempt_at,first_pending_at FROM resource_work
 WHERE next_attempt_at<=clock_timestamp() ORDER BY next_attempt_at,first_pending_at,work_id LIMIT 100) candidates
 GROUP BY execution_id ORDER BY min(next_attempt_at),min(first_pending_at),execution_id`)
		if e != nil {
			return e
		}
		ids, e = pgx.CollectRows(rs, pgx.RowTo[string])
		if e != nil {
			return e
		}
		var oldest *time.Time
		if e = q.tx.QueryRow(q.ctx(), `SELECT min(first_pending_at) FROM resource_work`).Scan(&oldest); e != nil {
			return e
		}
		if oldest != nil {
			span.Set("fleet.resource.oldest_pending_ms", strconv.FormatInt(time.Since(*oldest).Milliseconds(), 10))
		}
		return nil
	})
	if err != nil {
		finalErr = err
		return err
	}
	for _, p := range projects {
		if e := resourceBuildPage(ctx, a, p); e != nil {
			finalErr = e
			_ = runTxLocks(ctx, a, nil, true, []string{"project:" + p}, func(q *Request) error {
				return exec(q, `UPDATE resource_build SET last_error='build_failed',updated_at=clock_timestamp() WHERE project_id=$1`, p)
			})
		}
	}
	for _, r := range repairs {
		if e := resourceMetadataPage(ctx, a, r); e != nil {
			finalErr = e
			_ = runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error {
				return exec(q, `UPDATE resource_metadata_repair SET last_error='metadata_failed',next_attempt_at=clock_timestamp()+interval '100 milliseconds' WHERE repair_id=$1::uuid`, r)
			})
		}
	}
	for _, id := range ids {
		if e := resourceReplace(ctx, a, id); e != nil {
			finalErr = e
		}
	}
	span.Set("fleet.resource.candidates", strconv.Itoa(len(ids)))
	return finalErr
}

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
    SELECT min(first_pending_at) at FROM resource_work w JOIN resource_work_scope ws USING(work_id) WHERE ws.project_id=$1
    UNION ALL SELECT min(r.created_at) FROM resource_metadata_repair r WHERE NOT done OR EXISTS(SELECT 1 FROM resource_repair_scope rs WHERE rs.repair_id=r.repair_id AND rs.project_id=$1)) ages),
   coalesce((SELECT w.last_error_class FROM resource_work w JOIN resource_work_scope ws USING(work_id) WHERE ws.project_id=$1 AND w.last_error_class IS NOT NULL ORDER BY w.first_pending_at,w.work_id LIMIT 1),
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
