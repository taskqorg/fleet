package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"fleet/contract"
)

// Requirement: one poison leaf in a full cohort must not send every healthy leaf to a single transaction;
// risk: a large failed cohort replays as one transaction per leaf, the overhead batching removes.
func TestResourceBisectCohort(t *testing.T) {
	cs := make([]*kitClaim, resourceCohortSize)
	for i := range cs {
		cs[i] = &kitClaim{Key: fmt.Sprintf("leaf-%03d", i), Group: "P"}
	}
	var batches, singles int
	committed := map[string]bool{}
	poison := map[string]bool{cs[37].Key: true}
	kitBisect(cs, func(part []*kitClaim) error {
		batches++
		for _, c := range part {
			if poison[c.Key] {
				return errors.New("poison")
			}
		}
		for _, c := range part {
			committed[c.Key] = true
		}
		return nil
	}, func(c *kitClaim) {
		singles++
		if !poison[c.Key] {
			committed[c.Key] = true
		}
	})
	if singles != 1 || len(committed) != len(cs)-1 || committed[cs[37].Key] {
		t.Fatalf("singles=%d committed=%d", singles, len(committed))
	}
	if batches > 2*7 {
		t.Fatalf("one poison leaf in %d took %d cohort transactions", len(cs), batches)
	}
	batches, singles = 0, 0
	kitBisect(cs, func([]*kitClaim) error { batches++; return errResourceClearing }, func(*kitClaim) { singles++ })
	if batches != 1 || singles != 0 {
		t.Fatalf("a skipped cohort must wait whole: batches=%d singles=%d", batches, singles)
	}
	if q := kitOf(&App{}).queue("resource-work"); q.Limit != resourceCohortSize {
		t.Fatal("cohort size must equal the work queue's selection bound")
	}
}

// Requirement: bounded cohorts preserve project order and every selected candidate;
// risk: interleaving removes amortization, mixes uncertain scopes or starves later work.
func TestResourceCandidateCohorts(t *testing.T) {
	interleaved := make([]string, 100)
	hints := map[string]string{}
	for i := range interleaved {
		interleaved[i] = fmt.Sprintf("selected-%03d", i)
		hints[interleaved[i]] = fmt.Sprint(i % 5)
	}
	var wantInterleaved [][]string
	for project := 0; project < 5; project++ {
		var group []string
		for member := 0; member < 20; member++ {
			group = append(group, interleaved[project+5*member])
		}
		wantInterleaved = append(wantInterleaved, group)
	}
	for _, tc := range []struct {
		name  string
		ids   []string
		hints map[string]string
		want  [][]string
	}{
		{name: "empty"},
		{name: "failed hints stay scalar", ids: []string{"a", "b", "c"}, want: [][]string{{"a"}, {"b"}, {"c"}}},
		{name: "unknown earliest candidate retains priority",
			ids:   []string{"a", "unknown", "b", "a2", "b2", "missing"},
			hints: map[string]string{"a": "P", "a2": "P", "b": "Q", "b2": "Q", "unknown": ""},
			want:  [][]string{{"a", "a2"}, {"unknown"}, {"b", "b2"}, {"missing"}}},
		{name: "five interleaved projects keep one cohort each", ids: interleaved, hints: hints, want: wantInterleaved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.ids)
			claims := []*kitClaim{}
			for _, id := range tc.ids {
				claims = append(claims, &kitClaim{Key: id, Group: tc.hints[id]})
			}
			var got [][]string
			for _, cohort := range kitCohorts(claims, resourceCohortSize) {
				ids := []string{}
				for _, c := range cohort {
					ids = append(ids, c.Key)
				}
				got = append(got, ids)
			}
			if !reflect.DeepEqual(got, tc.want) || !slices.Equal(tc.ids, before) {
				t.Fatalf("cohorts=%v want=%v; input=%v before=%v", got, tc.want, tc.ids, before)
			}
			seen := map[string]bool{}
			for _, group := range got {
				if len(group) == 0 || len(group) > resourceCohortSize {
					t.Fatalf("invalid cohort size %d", len(group))
				}
				for _, id := range group {
					if seen[id] {
						t.Fatalf("selected candidate repeated: %s", id)
					}
					seen[id] = true
					if len(group) > 1 && (tc.hints[id] == "" || tc.hints[id] != tc.hints[group[0]]) {
						t.Fatalf("mixed or uncertain cohort: %v", group)
					}
				}
			}
			if len(seen) != len(tc.ids) {
				t.Fatalf("consumed %d of %d selected IDs", len(seen), len(tc.ids))
			}
		})
	}
}

// Requirement: exact nullable facts, complete tools and frozen pricing; risk: rounded money,
// lost zero/reasoning/API measurements, truncated tools, or changing historical methods.
func TestResourceContributionExact(t *testing.T) {
	leaf := `{"kind":"agent_run","state":"running","project_id":"A","has_totals":true,"model":"m","provider":"p","tokens_input":0,"tokens_input_basis":"measured","tokens_output":2,"tokens_output_basis":"measured","tokens_reasoning":7,"tokens_reasoning_basis":"measured","api_ms":0,"api_ms_basis":"measured","connection_cost":{"credits":"0.1234567890123456789"},"connection_cost_basis":"measured","credits":0.1234567890123456789,"direct_currency":"USD","direct_amount":0.1234567890123456789,"tool_calls_basis":"measured","tool_calls":{"byTool":{`
	parts := []string{}
	for i := 0; i < 55; i++ {
		parts = append(parts, fmt.Sprintf(`"tool%02d":{"calls":1,"errors":0}`, i))
	}
	leaf += strings.Join(parts, ",") + `},"other":{"calls":2,"errors":1}}}`
	c, m, err := resourceContribution("e", resourceSnapshot{Leaf: json.RawMessage(leaf)})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Included || len(c.Tools) != 55 || c.OtherCalls != 2 || c.OtherErrors != 1 {
		t.Fatalf("lost tool facts: %+v", c)
	}
	for k, want := range map[string]string{"input": "0", "reasoning": "7", "apiMs": "0", "credits": "0.1234567890123456789", "toolCalls": "57", "toolErrors": "1"} {
		if c.Metrics[k].Value == nil || *c.Metrics[k].Value != want {
			t.Errorf("%s=%+v want %s", k, c.Metrics[k], want)
		}
	}
	if c.Metrics["cpuMs"].Value != nil || c.ObservedAt != nil {
		t.Fatal("invented observation")
	}
	if len(c.Costs) != 1 || deref(c.Costs[0].Amount) != "0.1234567890123456789" {
		t.Fatalf("rounded cost %+v", c.Costs)
	}
	if len(m.Components) != 0 {
		t.Fatal("unexpected rates")
	}
	got, err := decimalArithmetic("0.000000000000000001", "9007199254740993", true)
	if err != nil || got != "0.009007199254740993" {
		t.Fatalf("decimal %q %v", got, err)
	}
}

// Requirement: confirmed turns are retained for every leaf while only agent/tool actors
// contribute tool-call coverage; risk: non-agent turns disappear or unrelated tool JSON changes basis.
func TestResourceNonAgentTurns(t *testing.T) {
	c, _, err := resourceContribution("job", resourceSnapshot{Leaf: json.RawMessage(`{"kind":"host_job","state":"succeeded","project_id":"A","has_totals":true,"turn_count":7,"turn_count_basis":"measured","tool_calls":{"byTool":{"ignored":{"calls":99,"errors":1}}},"tool_calls_basis":"estimated"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if deref(c.Metrics["turns"].Value) != "7" || c.Metrics["toolCalls"].Value != nil || len(c.Tools) != 0 {
		t.Fatalf("non-agent facts lost or invented: %+v", c)
	}
}

// Requirement: legacy resource reads include reported reasoning/API; risk: mapped SQL observations
// disappear in the reducer or reasoning is counted twice in total tokens.
func TestResourceLegacyReasoningAPI(t *testing.T) {
	got := summarizeResourceLeaves([]resourceLeafRow{{Kind: "agent_run", TokensInput: ptr(int64(5)), TokensOutput: ptr(int64(9)), TokensReasoning: ptr(int64(4)), ApiMs: ptr(int64(0)), TokensInputBasis: contract.ResourceBasisMeasured, TokensOutputBasis: contract.ResourceBasisMeasured}})
	if got.Tokens.Reasoning == nil || *got.Tokens.Reasoning != 4 || got.Durations.ApiMs == nil || *got.Durations.ApiMs != 0 || got.Tokens.Total == nil || *got.Tokens.Total != 14 {
		t.Fatalf("bad legacy mapping %+v", got)
	}
}
