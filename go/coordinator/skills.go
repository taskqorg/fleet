// Agent Library skills listing and bulk definition operations (skills_list, definition_take_repository,
// definition_add_skill) per docs/designs/fleet-definition-editors.md and FLT-7.
package main

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("skills_list", skillsList)
	op("definition_take_repository", definitionTakeRepository)
	op("definition_add_skill", definitionAddSkill)
}

func parseSkillFrontmatter(id, raw string) (name, desc, body string) {
	name, body = id, raw
	if !strings.HasPrefix(raw, "---\n") && !strings.HasPrefix(raw, "---\r\n") {
		return
	}
	rest := strings.TrimPrefix(raw, "---\r\n")
	if rest == raw {
		rest = strings.TrimPrefix(raw, "---\n")
	}
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return
	}
	header := rest[:idx]
	body = strings.TrimSpace(rest[idx+4:])
	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "name:"); ok {
			if t := strings.Trim(strings.TrimSpace(v), `"'`); t != "" {
				name = t
			}
		} else if v, ok := strings.CutPrefix(line, "description:"); ok {
			desc = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return
}

func shippedSkillsDir() string {
	return filepath.Join(configDir(), "skills")
}

func skillsList(q *Request, _ *contract.SkillsListRequest) (contract.SkillsListReply, error) {
	out := contract.SkillsListReply{Skills: []contract.SkillSummary{}}
	agents, err := definitionsOf(q.ctx(), q.db(), definitionFilter{kind: "agent", current: true})
	if err != nil {
		return out, err
	}
	headBySkill := map[string][]string{}
	for _, a := range agents {
		var p contract.Profile
		if p, err = contract.ParseProfile(a.Content); err == nil {
			for _, sk := range p.Skills {
				headBySkill[sk] = append(headBySkill[sk], a.ID)
			}
		}
	}

	repoBySkill := map[string][]string{}
	if entries, err := os.ReadDir(profilesDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(profilesDir(), e.Name()))
			if err != nil {
				continue
			}
			if p, err := contract.ParseProfile(b); err == nil {
				for _, sk := range p.Skills {
					repoBySkill[sk] = append(repoBySkill[sk], p.Id)
				}
			}
		}
	}

	dir := shippedSkillsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		skillPath := filepath.Join(dir, id, "SKILL.md")
		b, err := os.ReadFile(skillPath)
		if err != nil {
			continue
		}
		name, desc, body := parseSkillFrontmatter(id, string(b))
		ha := append([]string{}, headBySkill[id]...)
		ra := append([]string{}, repoBySkill[id]...)
		sort.Strings(ha)
		sort.Strings(ra)
		out.Skills = append(out.Skills, contract.SkillSummary{
			Id:          id,
			Name:        name,
			Description: desc,
			Path:        "config/skills/" + id + "/SKILL.md",
			Body:        body,
			Agents:      ha,
			RepoAgents:  ra,
			TotalAgents: int64(len(agents)),
		})
	}
	sort.Slice(out.Skills, func(i, j int) bool { return out.Skills[i].Id < out.Skills[j].Id })
	return out, nil
}

func definitionTakeRepository(q *Request, r *contract.DefinitionTakeRepositoryRequest) (contract.DefinitionTakeRepositoryReply, error) {
	out := contract.DefinitionTakeRepositoryReply{Items: []contract.DefinitionBulkResult{}}
	if len(r.Keys) == 0 {
		return out, fail(400, "keys must not be empty")
	}
	msg := strings.TrimSpace(deref(r.Message))
	if msg == "" {
		msg = "Take repository version"
	}
	for _, k := range r.Keys {
		if err := definitionKind(k.Kind); err != nil {
			return out, err
		}
		if k.Kind == contract.DefinitionKindProject {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: k.Kind, Id: k.Id, Status: contract.BulkOpStatusSkipped,
				Reason: ptr("projects use project save"),
			})
			continue
		}
		head, err := headOf(q, string(k.Kind), k.Id, !r.Preview)
		if err != nil {
			return out, err
		}
		if head == nil {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: k.Kind, Id: k.Id, Status: contract.BulkOpStatusError,
				Reason: ptr("Unknown definition"),
			})
			continue
		}
		st := head.state()
		if st != contract.DefinitionStateRepositoryChanged {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: k.Kind, Id: k.Id, Status: contract.BulkOpStatusSkipped,
				Reason: ptr("state is " + string(st)),
			})
			continue
		}
		repoRaw, err := q.db().DefRepoContent(q.ctx(), db.DefRepoContentParams{Kind: string(k.Kind), ID: k.Id})
		if err != nil {
			return out, err
		}
		if repoRaw == nil || *repoRaw == "" {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: k.Kind, Id: k.Id, Status: contract.BulkOpStatusSkipped,
				Reason: ptr("no repository content stored"),
			})
			continue
		}
		repoText, err := canonicalText([]byte(*repoRaw))
		if err != nil {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: k.Kind, Id: k.Id, Status: contract.BulkOpStatusError,
				Reason: ptr(err.Error()),
			})
			continue
		}
		if repoText == string(head.Content) {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: k.Kind, Id: k.Id, Status: contract.BulkOpStatusSkipped,
				Reason: ptr("no difference from head"),
			})
			continue
		}
		if ps := problems(string(k.Kind), k.Id, repoText); len(ps) > 0 {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: k.Kind, Id: k.Id, Status: contract.BulkOpStatusError,
				Reason: ptr(ps[0].Message),
			})
			continue
		}
		if r.Preview {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: k.Kind, Id: k.Id, Status: contract.BulkOpStatusWouldApply,
			})
			continue
		}
		rev, err := appendRevision(q, string(k.Kind), k.Id, head.Head, &repoText, head.RepoSha, "editor", msg)
		if err != nil {
			return out, err
		}
		out.Applied++
		out.Items = append(out.Items, contract.DefinitionBulkResult{
			Kind: k.Kind, Id: k.Id, Status: contract.BulkOpStatusApplied, Revision: &rev.Revision,
		})
	}
	return out, nil
}

func definitionAddSkill(q *Request, r *contract.DefinitionAddSkillRequest) (contract.DefinitionAddSkillReply, error) {
	out := contract.DefinitionAddSkillReply{Skill: r.Skill, Items: []contract.DefinitionBulkResult{}}
	if r.Skill == "" || strings.ContainsAny(r.Skill, "/\\") || strings.Contains(r.Skill, "..") || r.Skill[0] == '.' {
		return out, fail(400, "Invalid skill id")
	}
	if _, err := os.Stat(filepath.Join(shippedSkillsDir(), r.Skill, "SKILL.md")); err != nil {
		return out, fail(404, "Unknown shipped skill: "+r.Skill)
	}
	targetIDs := []string{}
	seen := map[string]bool{}
	for _, id := range r.Agents {
		if id != "" && !seen[id] {
			seen[id] = true
			targetIDs = append(targetIDs, id)
		}
	}
	if len(targetIDs) == 0 && r.Category != nil && *r.Category != "" {
		agents, err := definitionsOf(q.ctx(), q.db(), definitionFilter{kind: "agent", current: true})
		if err != nil {
			return out, err
		}
		for _, a := range agents {
			if p, err := contract.ParseProfile(a.Content); err == nil && p.Category != nil && *p.Category == *r.Category {
				targetIDs = append(targetIDs, a.ID)
			}
		}
	}
	if len(targetIDs) == 0 {
		return out, fail(400, "Specify agents or a category with matching agents")
	}
	msg := strings.TrimSpace(deref(r.Message))
	if msg == "" {
		msg = "Add skill " + r.Skill
	}
	for _, id := range targetIDs {
		head, err := headOf(q, "agent", id, !r.Preview)
		if err != nil {
			return out, err
		}
		if head == nil {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: contract.DefinitionKindAgent, Id: id, Status: contract.BulkOpStatusError,
				Reason: ptr("Unknown agent definition"),
			})
			continue
		}
		p, err := contract.ParseProfile(head.Content)
		if err != nil {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: contract.DefinitionKindAgent, Id: id, Status: contract.BulkOpStatusError,
				Reason: ptr(err.Error()),
			})
			continue
		}
		if slices.Contains(p.Skills, r.Skill) {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: contract.DefinitionKindAgent, Id: id, Status: contract.BulkOpStatusSkipped,
				Reason: ptr("already has skill"),
			})
			continue
		}
		p.Skills = append(p.Skills, r.Skill)
		text, err := canonicalText(jsonBytes(p))
		if err != nil {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: contract.DefinitionKindAgent, Id: id, Status: contract.BulkOpStatusError,
				Reason: ptr(err.Error()),
			})
			continue
		}
		if ps := problems("agent", id, text); len(ps) > 0 {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: contract.DefinitionKindAgent, Id: id, Status: contract.BulkOpStatusError,
				Reason: ptr(ps[0].Message),
			})
			continue
		}
		if r.Preview {
			out.Items = append(out.Items, contract.DefinitionBulkResult{
				Kind: contract.DefinitionKindAgent, Id: id, Status: contract.BulkOpStatusWouldApply,
			})
			continue
		}
		rev, err := appendRevision(q, "agent", id, head.Head, &text, head.RepoSha, "editor", msg)
		if err != nil {
			return out, err
		}
		out.Applied++
		out.Items = append(out.Items, contract.DefinitionBulkResult{
			Kind: contract.DefinitionKindAgent, Id: id, Status: contract.BulkOpStatusApplied, Revision: &rev.Revision,
		})
	}
	return out, nil
}
