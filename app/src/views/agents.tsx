// Agent Library screen (FLT-7, docs/designs/fleet-definition-editors.md, docs/design-system/pages/agents.md):
// one consistent control-column list of agents/skills, idle Library Summary + Skills rollout in the work area,
// and DefinitionDetail in the work area when an agent is opened.
import { useMemo, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  Button, CatalogueTree, Choice, ContextMenu, DetailSection, Disclosure, Empty, ErrorBanner, HarnessMark, Icon, KvRow,
  ListDetail, Num, OfflineMarker, ProblemRow, SearchField, ShellStatus, StateBadge, TabBar, Totals, VirtualList,
  actions, catalogueRow, copyText, harnesses, matchSearch,
  type CatalogueTreeNode, type Harness, type IconName, type StateOf, type TotalItem,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { flipGroup } from '../lists.ts';
import { listGroups, setListGroups, usePins } from '../prefs.ts';
import { useOp } from '../store.ts';
import { DefinitionDetail } from './definitions.tsx';
import type { DefinitionKey, DefinitionListItem, SkillSummary } from '../../../contracts/generated/ops.ts';

type LibTab = 'agents' | 'skills';
type AgentRoleGroup = 'core' | 'research-design' | 'assets-3d' | 'other' | 'lab-variants';

const AGENT_TREE_LIST = 'agents';
const AGENT_TREE_GROUPS = 'agents:role';

const ROLE_GROUPS: readonly { key: AgentRoleGroup; id: string; label: string; icon: IconName; defaultCollapsed: boolean }[] = [
  { key: 'core', id: 'group:core', label: 'Core', icon: 'bot', defaultCollapsed: false },
  { key: 'research-design', id: 'group:research-design', label: 'Research & Design', icon: 'search', defaultCollapsed: false },
  { key: 'assets-3d', id: 'group:assets-3d', label: 'Assets & 3D', icon: 'archive', defaultCollapsed: false },
  { key: 'other', id: 'group:other', label: 'Other', icon: 'layers-2', defaultCollapsed: false },
  { key: 'lab-variants', id: 'group:lab-variants', label: 'Lab variants', icon: 'chart-no-axes-column', defaultCollapsed: true },
];

const compatibilityState = (c?: string): StateOf<'definition'> =>
  c === 'checked' ? 'ready' : c === 'failed' ? 'incompatible' : 'unchecked';

const agentRoleGroup = (a: DefinitionListItem): AgentRoleGroup => {
  const cat = a.agent?.category?.toLowerCase() ?? '';
  const id = a.id.toLowerCase();
  const tags = (a.agent?.tags ?? []).map(t => t.toLowerCase());
  if (a.agent?.variantOf || a.agent?.experiment || id.startsWith('lab-') || cat === 'lab' || tags.includes('lab')) {
    return 'lab-variants';
  }
  if (cat === 'assets' || cat === '3d' || cat === 'media' || id.startsWith('asset-') || id.startsWith('assets-')) {
    return 'assets-3d';
  }
  if (cat === 'research' || cat === 'design' || /(researcher|designer|design-rules|website-review)/.test(id)) {
    return 'research-design';
  }
  if (
    cat === 'coding' || cat === 'review' || cat === 'operations' || cat === 'core'
    || id === 'pm' || id === 'baseline' || /-(worker|reviewer|judge)$/.test(id)
  ) {
    return 'core';
  }
  return 'other';
};

const agentSearchText = (a: DefinitionListItem): string =>
  [
    a.id,
    a.name,
    a.agent?.purpose,
    a.agent?.category,
    a.agent?.harness ? `harness:${a.agent.harness}` : '',
    a.agent?.category ? `category:${a.agent.category}` : '',
    a.agent?.model,
    a.agent?.effort,
    a.agent?.variantOf,
    ...(a.agent?.tags ?? []),
    ...(a.agent?.skills ?? []).map(s => `skill:${s} ${s}`),
    ...(a.agent?.roles ?? []),
  ].filter(Boolean).join(' ');

function agentDeltaSub(d: DefinitionListItem, byId: ReadonlyMap<string, DefinitionListItem>): string | undefined {
  const a = d.agent;
  if (!a) return undefined;
  const parent = a.variantOf ? byId.get(a.variantOf) : undefined;
  const baseHarness = parent?.agent?.harness ?? 'codex';
  const baseModel = parent?.agent?.model ?? 'gpt-6.2-sol';
  const baseEffort = parent
    ? (parent.agent?.effort ?? null)
    : (a.harness && a.harness !== 'codex' ? null : 'medium');

  const parts: string[] = [];
  if (a.model && a.model !== baseModel) parts.push(a.model);
  if (a.effort && a.effort !== baseEffort) parts.push(a.effort);
  if (a.variantOf && parts.length === 0) {
    const parentSkills = parent?.agent?.skills ?? [];
    const curSkills = a.skills ?? [];
    const skillsDiffer = parent && (curSkills.length !== parentSkills.length || curSkills.some(s => !parentSkills.includes(s)));
    parts.push(skillsDiffer ? `${curSkills.length} skills` : `var of ${a.variantOf}`);
  } else if (!a.variantOf && a.harness !== baseHarness && parts.length === 0) {
    parts.push(a.harness);
  }
  return parts.length ? parts.join(' · ') : undefined;
}

export function Agents({ id }: { id?: string }) {
  const [, go] = useLocation();
  const [tab, setTab] = useState<LibTab>('agents');
  const [q, setQ] = useState('');
  const [harness, setHarness] = useState('');
  const [category, setCategory] = useState('');
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<{ severity: 'warning' | 'failure'; title: string; facts: string } | null>(null);
  const [previewRepoKeys, setPreviewRepoKeys] = useState<DefinitionKey[] | null>(null);
  const [previewSkill, setPreviewSkill] = useState<{ skill: string; category?: string; agents?: string[] } | null>(null);
  const [skillTargetCat, setSkillTargetCat] = useState<Record<string, string>>({});
  const [menu, setMenu] = useState<{ node: CatalogueTreeNode; at: { x: number; y: number } } | null>(null);

  const pins = usePins(AGENT_TREE_LIST);
  const flippedGroups = listGroups(AGENT_TREE_GROUPS);

  const agentsOp = useOp('definition_list', { kind: 'agent' });
  const skillsOp = useOp('skills_list', {});

  const agentItems = agentsOp.data?.items ?? [];
  const skillItems = skillsOp.data?.skills ?? [];

  const categories = useMemo(
    () => [...new Set(agentItems.map(a => a.agent?.category).filter((x): x is string => !!x))].sort(),
    [agentItems],
  );

  const agentById = useMemo(() => new Map(agentItems.map(a => [a.id, a])), [agentItems]);

  const facetAgents = useMemo(() => {
    const agentTerm = q.toLowerCase().split(/\s+/).find(w => w.startsWith('agent:') && w.length > 6)?.slice(6) ?? '';
    return agentItems.filter(a => (!harness || a.agent?.harness === harness)
      && (!category || a.agent?.category === category)
      && (!agentTerm || (a.agent?.harness ?? '').toLowerCase().includes(agentTerm)));
  }, [agentItems, harness, category, q]);

  const filteredAgents = useMemo(() => {
    return agentItems
      .filter(a => (!harness || a.agent?.harness === harness)
        && (!category || a.agent?.category === category)
        && matchSearch(q, agentSearchText(a), {
          agent: a.agent?.harness ?? null,
          state: a.draft ? 'draft' : compatibilityState(a.compatibility),
        }))
      .toSorted((a, b) => Number(!!b.draft) - Number(!!a.draft) || b.last30.runs - a.last30.runs || a.name.localeCompare(b.name));
  }, [agentItems, harness, category, q]);

  const treeQuery = useMemo(() => q.replace(/\b(?:agent|host|project):(\S+)/gi, '$1'), [q]);

  const collapsedMap = useMemo(() => {
    const map: Record<string, boolean> = {};
    if (flippedGroups.includes('__pinned__')) map['__pinned__'] = true;
    for (const g of ROLE_GROUPS) {
      if (flippedGroups.includes(g.id)) map[g.id] = !g.defaultCollapsed;
    }
    return map;
  }, [flippedGroups]);

  const agentTreeNodes = useMemo<CatalogueTreeNode[]>(() => {
    const buckets = new Map<AgentRoleGroup, DefinitionListItem[]>(ROLE_GROUPS.map(g => [g.key, []]));
    for (const a of facetAgents) {
      buckets.get(agentRoleGroup(a))!.push(a);
    }
    const sortItems = (items: DefinitionListItem[]) =>
      items.toSorted((a, b) => Number(!!b.draft) - Number(!!a.draft) || b.last30.runs - a.last30.runs || a.name.localeCompare(b.name));

    const out: CatalogueTreeNode[] = [];
    for (const group of ROLE_GROUPS) {
      const items = sortItems(buckets.get(group.key) ?? []);
      if (!items.length) continue;
      out.push({
        id: group.id,
        label: group.label,
        icon: group.icon,
        defaultCollapsed: group.defaultCollapsed,
        children: items.map(d => {
          const parentHarness = d.agent?.variantOf ? agentById.get(d.agent.variantOf)?.agent?.harness : undefined;
          const baseHarness = parentHarness ?? 'codex';
          const showHarness = !!(d.agent && d.agent.harness !== baseHarness && harnesses[d.agent.harness]);
          const right = (d.state === 'repository-changed' || showHarness) ? (
            <>
              {d.state === 'repository-changed' && <Icon name="download" label="Repository update waiting" />}
              {showHarness && <HarnessMark h={d.agent!.harness as Harness} />}
            </>
          ) : undefined;
          return {
            id: d.id,
            label: d.name,
            sub: agentDeltaSub(d, agentById),
            state: {
              family: 'definition',
              state: d.draft ? 'draft' : compatibilityState(d.compatibility),
            },
            right,
            pinned: pins.has(d.id),
            href: `/agents/${encodeURIComponent(d.id)}`,
            searchText: agentSearchText(d),
            problem: d.compatibility === 'failed',
          };
        }),
      });
    }
    return out;
  }, [facetAgents, agentById, pins.ids]);

  const filteredSkills = useMemo(
    () => skillItems.filter(s => matchSearch(q, `${s.id} ${s.name} ${s.description}`)),
    [skillItems, q],
  );

  const repoChangedAgents = useMemo(
    () => agentItems.filter(a => a.state === 'repository-changed'),
    [agentItems],
  );
  const repoChangedKeys: DefinitionKey[] = useMemo(
    () => repoChangedAgents.map(a => ({ kind: 'agent', id: a.id })),
    [repoChangedAgents],
  );

  const harnessSummary = useMemo(() => {
    const base = agentItems.filter(a => (!category || a.agent?.category === category)
      && matchSearch(q, agentSearchText(a), { agent: a.agent?.harness ?? null }));
    return Object.entries(harnesses).map(([key, info]) => {
      const items = base.filter(a => a.agent?.harness === key);
      return {
        key,
        label: info.label,
        icon: info.icon,
        count: items.length,
        runs: items.reduce((s, a) => s + a.last30.runs, 0),
        tokens: items.reduce((s, a) => s + a.last30.tokens, 0),
      };
    }).filter(x => x.count > 0);
  }, [agentItems, category, q]);

  const categorySummary = useMemo(() => {
    const base = agentItems.filter(a => (!harness || a.agent?.harness === harness)
      && matchSearch(q, agentSearchText(a), { agent: a.agent?.harness ?? null }));
    const byCat = new Map<string, DefinitionListItem[]>();
    for (const a of base) {
      const cat = a.agent?.category;
      if (!cat) continue;
      const arr = byCat.get(cat);
      if (arr) arr.push(a);
      else byCat.set(cat, [a]);
    }
    return [...byCat.entries()]
      .sort((a, b) => a[0].localeCompare(b[0]))
      .map(([cat, items]) => ({
        cat,
        count: items.length,
        runs: items.reduce((s, a) => s + a.last30.runs, 0),
        tokens: items.reduce((s, a) => s + a.last30.tokens, 0),
      }));
  }, [agentItems, harness, q]);

  const runTakeRepo = async (keys: DefinitionKey[], preview: boolean) => {
    setBusy(true);
    setNotice(null);
    try {
      const r = await op('definition_take_repository', { keys, preview, message: 'Take repository version from Agent Library' });
      if (preview) {
        setPreviewRepoKeys(keys);
        setNotice({ severity: 'warning', title: `Preview: take repository version for ${r.items.filter(i => i.status === 'would-apply').length} agents`, facts: r.items.map(i => `${i.id}: ${i.status}`).join(' · ') });
      } else {
        setPreviewRepoKeys(null);
        setNotice({ severity: 'warning', title: `Updated ${r.applied} agents from repository`, facts: r.items.map(i => `${i.id}: ${i.status}`).join(' · ') });
        await agentsOp.reload();
      }
    } catch (e) {
      setNotice({ severity: 'failure', title: 'Repository sync failed', facts: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  const runAddSkill = async (req: { skill: string; category?: string; agents?: string[] }, preview: boolean) => {
    setBusy(true);
    setNotice(null);
    try {
      const r = await op('definition_add_skill', { ...req, preview, message: `Add skill ${req.skill}` });
      if (preview) {
        setPreviewSkill(req);
        const readyCount = r.items.filter(i => i.status === 'would-apply').length;
        setNotice({ severity: 'warning', title: `Preview: add skill ${req.skill} to ${readyCount} agent${readyCount === 1 ? '' : 's'}`, facts: r.items.map(i => `${i.id}: ${i.status}${i.reason ? ` (${i.reason})` : ''}`).join(' · ') });
      } else {
        setPreviewSkill(null);
        setNotice({ severity: 'warning', title: `Added skill ${req.skill} to ${r.applied} agents`, facts: r.items.map(i => `${i.id}: ${i.status}`).join(' · ') });
        await Promise.all([skillsOp.reload(), agentsOp.reload()]);
      }
    } catch (e) {
      setNotice({ severity: 'failure', title: 'Add skill failed', facts: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  // When an agent detail is open, keep the Control Column on the Agents catalogue with that row selected.
  const listTab: LibTab = id ? 'agents' : tab;

  const listPane = (
    <>
      <ShellStatus><OfflineMarker state={agentsOp.state} now={Date.now()} /></ShellStatus>
      <header class="page-h">
        <SearchField label={listTab === 'agents' ? 'Find agents' : 'Find skills'} value={q} onInput={setQ} onSearch={() => {}} />
        <span class="muted mono">{listTab === 'agents' ? `${filteredAgents.length} of ${agentItems.length}` : `${filteredSkills.length} of ${skillItems.length}`}</span>
        {listTab === 'agents' && harness && (
          <Button label={harnesses[harness]?.label ?? harness} icon="x" kind="quiet" onClick={() => setHarness('')} />
        )}
        {listTab === 'agents' && category && (
          <Button label={category} icon="x" kind="quiet" onClick={() => setCategory('')} />
        )}
      </header>
      {listTab === 'agents' && agentsOp.error && <ErrorBanner text={agentsOp.error} onRetry={agentsOp.reload} />}
      {listTab === 'skills' && skillsOp.error && <ErrorBanner text={skillsOp.error} onRetry={skillsOp.reload} />}
      <section class="panel">
        {listTab === 'agents' && (
          !agentsOp.data && !agentsOp.error ? <Empty text="Loading…" /> :
          agentTreeNodes.length ? (
            <CatalogueTree
              fill
              nodes={agentTreeNodes}
              label="Agents"
              selectedId={id ?? null}
              query={treeQuery}
              collapsed={collapsedMap}
              emptyText="No matching agents."
              onToggle={folderId => setListGroups(AGENT_TREE_GROUPS, flipGroup(listGroups(AGENT_TREE_GROUPS), folderId))}
              onSelect={node => {
                if (Array.isArray(node.children)) {
                  setListGroups(AGENT_TREE_GROUPS, flipGroup(listGroups(AGENT_TREE_GROUPS), node.id));
                } else {
                  go(`/agents/${encodeURIComponent(node.id)}`);
                }
              }}
              onPin={node => pins.toggle(node.id)}
              onRowMenu={(node, at) => {
                if (!Array.isArray(node.children)) setMenu({ node, at });
              }}
            />
          ) : <Empty text="No matching agents." />
        )}
        {menu && (
          <ContextMenu
            at={menu.at}
            label={menu.node.label}
            onClose={() => setMenu(null)}
            items={[
              { label: 'Open', icon: actions.open, onClick: () => go(`/agents/${encodeURIComponent(menu.node.id)}`) },
              { label: pins.has(menu.node.id) ? 'Unpin' : 'Pin', icon: pins.has(menu.node.id) ? actions.unpin : actions.pin, onClick: () => pins.toggle(menu.node.id) },
              { label: 'Copy id', icon: actions.copy, onClick: () => copyText(menu.node.id) },
            ]}
          />
        )}
        {listTab === 'skills' && (
          !skillsOp.data && !skillsOp.error ? <Empty text="Loading…" /> :
          filteredSkills.length ? (
            <VirtualList
              items={filteredSkills}
              getKey={s => s.id}
              rowHeight={catalogueRow()}
              follow="top"
              label="Skills"
              columns="1.5rem minmax(0,1fr) auto"
              render={s => (
                <>
                  <span class="lead"><StateBadge family="definition" state={s.agents.length ? 'ready' : 'never'} compact /></span>
                  <span class="tt"><span class="title">{s.name}</span><span class="sub muted">{s.id}</span></span>
                  <span class="mono muted">{s.agents.length}/{s.totalAgents}</span>
                </>
              )}
            />
          ) : <Empty text="No shipped skills found." />
        )}
      </section>
    </>
  );

  const primaryAgents = filteredAgents.filter(a => !a.agent?.variantOf);
  const variantAgents = filteredAgents.filter(a => !!a.agent?.variantOf);
  const activeCount = filteredAgents.filter(a => a.last30.runs > 0).length;
  const unusedAgents = filteredAgents.filter(a => a.last30.runs === 0 && !a.agent?.variantOf);
  const totalRuns = filteredAgents.reduce((s, a) => s + a.last30.runs, 0);
  const totalTokens = filteredAgents.reduce((s, a) => s + a.last30.tokens, 0);

  const summaryTotals: TotalItem[] = [
    {
      label: 'Agents',
      m: { value: filteredAgents.length, basis: 'measured' },
      kind: 'count',
      note: variantAgents.length ? `${primaryAgents.length} primary · ${variantAgents.length} variants` : undefined,
    },
    {
      label: 'Active (30d)',
      m: { value: activeCount, basis: 'measured' },
      kind: 'count',
      note: unusedAgents.length ? `${unusedAgents.length} unused` : undefined,
    },
    { label: '30d runs', m: { value: totalRuns, basis: 'measured' }, kind: 'count' },
    { label: '30d tokens', m: { value: totalTokens, basis: 'measured' }, kind: 'count' },
  ];

  const workArea = (
    <article class="detail stack" aria-label="Agent Library">
      <header class="detail-h">
        <TabBar
          label="Library sections"
          tabs={[
            { id: 'agents', label: 'Summary', count: agentItems.length },
            { id: 'skills', label: 'Skills', count: skillItems.length },
          ]}
          value={tab}
          onChange={setTab}
        />
        <span class="grow" />
        {tab === 'agents' && repoChangedKeys.length > 0 && (
          <Button label={`Take repo (${repoChangedKeys.length})`} icon="download" busy={busy} onClick={() => void runTakeRepo(repoChangedKeys, true)} />
        )}
        <Button label="Pipelines" icon="network" kind="quiet" onClick={() => go('/pipelines')} />
      </header>

      {notice && (
        <ProblemRow
          severity={notice.severity}
          title={notice.title}
          facts={notice.facts}
          actions={previewRepoKeys
            ? [
                { label: 'Apply repository versions', primary: true, onClick: () => void runTakeRepo(previewRepoKeys, false) },
                { label: 'Dismiss', onClick: () => { setPreviewRepoKeys(null); setNotice(null); } },
              ]
            : previewSkill
              ? [
                  { label: `Add ${previewSkill.skill}`, primary: true, onClick: () => void runAddSkill(previewSkill, false) },
                  { label: 'Dismiss', onClick: () => { setPreviewSkill(null); setNotice(null); } },
                ]
              : [{ label: 'Dismiss', onClick: () => setNotice(null) }]}
        />
      )}

      <div role="tabpanel">
        {tab === 'agents' && (
          <div class="stack">
            <Totals items={summaryTotals} />

            {repoChangedAgents.length > 0 && (
              <DetailSection title={`Pending repository updates (${repoChangedAgents.length})`}>
                {repoChangedAgents.map(d => (
                  <KvRow
                    key={d.id}
                    icon="download"
                    label={d.name}
                    meta={d.path}
                    value={
                      <span class="acts">
                        <Button label="Open" kind="quiet" onClick={() => go(`/agents/${d.id}`)} />
                        <Button label="Take repo" icon="download" kind="quiet" busy={busy} onClick={() => void runTakeRepo([{ kind: 'agent', id: d.id }], true)} />
                      </span>
                    }
                  />
                ))}
              </DetailSection>
            )}

            {harnessSummary.length > 0 && (
              <DetailSection title="By harness">
                {harnessSummary.map(h => (
                  <KvRow
                    key={h.key}
                    icon={h.icon}
                    label={h.label}
                    meta={`${h.count} agent${h.count === 1 ? '' : 's'} · ${h.runs} runs (30d)`}
                    value={
                      <span class="acts">
                        <Num m={{ value: h.tokens, basis: 'measured' }} kind="count" />
                        <Button label={harness === h.key ? 'All harnesses' : 'Filter'} kind={harness === h.key ? 'primary' : 'quiet'} onClick={() => setHarness(harness === h.key ? '' : h.key)} />
                      </span>
                    }
                  />
                ))}
              </DetailSection>
            )}

            {categorySummary.length > 0 && (
              <DetailSection title="By category">
                {categorySummary.map(c => (
                  <KvRow
                    key={c.cat}
                    icon="layers-2"
                    label={c.cat}
                    meta={`${c.count} agent${c.count === 1 ? '' : 's'} · ${c.runs} runs (30d)`}
                    value={
                      <span class="acts">
                        <Num m={{ value: c.tokens, basis: 'measured' }} kind="count" />
                        <Button label={category === c.cat ? 'All categories' : 'Filter'} kind={category === c.cat ? 'primary' : 'quiet'} onClick={() => setCategory(category === c.cat ? '' : c.cat)} />
                      </span>
                    }
                  />
                ))}
              </DetailSection>
            )}

            {variantAgents.length > 0 && (
              <Disclosure summary={`Lab variants (${variantAgents.length})`} summaryClass="muted">
                {variantAgents.map(d => (
                  <KvRow
                    key={d.id}
                    icon="bot"
                    label={d.name}
                    meta={`variant of ${d.agent?.variantOf}${d.agent?.experiment ? ` (${d.agent.experiment})` : ''}`}
                    value={<Button label="Open" kind="quiet" onClick={() => go(`/agents/${d.id}`)} />}
                  />
                ))}
              </Disclosure>
            )}
          </div>
        )}

        {tab === 'skills' && (
          <div class="stack">
            {skillsOp.error && <ErrorBanner text={skillsOp.error} onRetry={skillsOp.reload} />}
            {filteredSkills.length ? filteredSkills.map((s: SkillSummary) => {
              const targetCat = skillTargetCat[s.id] || categories[0] || '';
              const missingAgents = agentItems.filter(a => !(a.agent?.skills ?? []).includes(s.id)).map(a => a.id);
              return (
                <DetailSection key={s.id} title={`${s.name} (${s.id})`}>
                  <KvRow icon={(s.agents.length ? 'circle-check' : 'file-text') as IconName} label={s.description || 'Shipped skill'}
                    meta={`${s.path} · active in ${s.agents.length} of ${s.totalAgents} store agents (repo: ${s.repoAgents.length})`}
                    value={
                      <span class="acts">
                        {categories.length > 0 && (
                          <>
                            <Choice label={`Target category for ${s.id}`} value={targetCat}
                              options={categories.map(c => ({ id: c, label: c }))}
                              onChange={c => setSkillTargetCat(prev => ({ ...prev, [s.id]: c }))} />
                            <Button label={`Add to ${targetCat}`} icon="plus" kind="quiet" busy={busy}
                              onClick={() => void runAddSkill({ skill: s.id, category: targetCat }, true)} />
                          </>
                        )}
                        {missingAgents.length > 0 && (
                          <Button label={`Add to all missing (${missingAgents.length})`} icon="plus" kind="quiet" busy={busy}
                            onClick={() => void runAddSkill({ skill: s.id, agents: missingAgents }, true)} />
                        )}
                      </span>
                    } />
                  {s.agents.length > 0 && <KvRow icon="bot" label="Enabled on agents" meta={s.agents.join(', ')} />}
                  <Disclosure summary={`Instructions (${s.path})`} summaryClass="muted">
                    <pre class="skill-body">{s.body}</pre>
                  </Disclosure>
                </DetailSection>
              );
            }) : <Empty text="No shipped skills match your search." />}
          </div>
        )}
      </div>
    </article>
  );

  return (
    <ListDetail
      list={listPane}
      detail={id ? <DefinitionDetail key={`agent/${id}`} kind="agent" id={id} onClose={() => go('/agents')} /> : undefined}
      detailKey={id}
      idle={workArea}
    />
  );
}
