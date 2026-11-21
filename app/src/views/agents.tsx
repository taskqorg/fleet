// Unified Library screen (docs/designs/fleet-ia-and-journeys.md, docs/design-system/pages/agents.md):
// one 4-root CatalogueTree (Methods, Pipelines, Agents, Skills) in the control column, Library summary + drift banner
// when idle, and DefinitionDetail / SkillDetail / Changes in the work area.
import { useMemo, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  Button, CatalogueTree, Choice, ContextMenu, DetailSection, Empty, EntityCode, ErrorBanner, HarnessMark, Icon, KvRow,
  ListDetail, Num, OfflineMarker, ProblemRow, SearchField, ShellActions, ShellStatus, Skeleton, StateBadge, Totals,
  actions, copyText, harnesses, matchSearch,
  type CatalogueTreeNode, type Harness, type IconName, type StateOf, type TotalItem,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { closedGroups, flipGroup } from '../lists.ts';
import { listGroups, setListGroups, usePins } from '../prefs.ts';
import { useOp } from '../store.ts';
import { Changes } from './changes.tsx';
import { DefinitionDetail, baseAgentDefaults } from './definitions.tsx';
import type { DefinitionKey, DefinitionListItem, SkillSummary } from '../../../contracts/generated/ops.ts';

export type LibrarySection = 'agents' | 'pipelines' | 'catalogue' | 'changes';
type AgentRoleGroup = 'core' | 'research-design' | 'assets-3d' | 'other' | 'lab-variants';

const LIBRARY_PINS = 'library';
const LIBRARY_TREE_GROUPS = 'library:tree';
const ROOT_METHODS_ID = 'root:methods';
const ROOT_PIPELINES_ID = 'root:pipelines';
const ROOT_AGENTS_ID = 'root:agents';
const ROOT_SKILLS_ID = 'root:skills';

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

function agentDeltaSub(
  d: DefinitionListItem,
  byId: ReadonlyMap<string, DefinitionListItem>,
  baseline?: DefinitionListItem['agent'],
): string | undefined {
  const a = d.agent;
  if (!a) return undefined;
  const parent = a.variantOf ? byId.get(a.variantOf) : undefined;
  const defaults = baseAgentDefaults(parent?.agent, baseline, a.harness);
  const baseEffort = parent ? (parent.agent?.effort ?? null) : (a.harness && a.harness !== defaults.harness ? null : defaults.effort);

  const parts: string[] = [];
  if (a.model && a.model !== defaults.model) parts.push(a.model);
  if (a.effort && a.effort !== baseEffort) parts.push(a.effort);
  if (a.variantOf && parts.length === 0) {
    const parentSkills = parent?.agent?.skills ?? [];
    const curSkills = a.skills ?? [];
    const skillsDiffer = parent && (curSkills.length !== parentSkills.length || curSkills.some(s => !parentSkills.includes(s)));
    parts.push(skillsDiffer ? `${curSkills.length} skills` : `var of ${a.variantOf}`);
  } else if (!a.variantOf && a.harness !== defaults.harness && parts.length === 0) {
    parts.push(a.harness);
  }
  return parts.length ? parts.join(' · ') : undefined;
}

const rawNodeId = (nodeId: string): string => {
  const idx = nodeId.indexOf(':');
  return idx >= 0 ? nodeId.slice(idx + 1) : nodeId;
};

export function Agents({ section = 'agents', id }: { section?: LibrarySection; id?: string }) {
  const [, go] = useLocation();
  const [q, setQ] = useState('');
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<{ severity: 'warning' | 'failure'; title: string; facts: string } | null>(null);
  const [previewRepoKeys, setPreviewRepoKeys] = useState<DefinitionKey[] | null>(null);
  const [menu, setMenu] = useState<{ node: CatalogueTreeNode; at: { x: number; y: number } } | null>(null);

  const pins = usePins(LIBRARY_PINS);
  const treeFlipped = listGroups(LIBRARY_TREE_GROUPS);

  const agentsOp = useOp('definition_list', { kind: 'agent' });
  const pipelinesOp = useOp('definition_list', { kind: 'pipeline' });
  const catalogueOp = useOp('definition_list', { kind: 'catalogue' });
  const skillsOp = useOp('skills_list', {});
  const changesOp = useOp('system_changes', {});

  const agentItems = agentsOp.data?.items ?? [];
  const pipelineItems = pipelinesOp.data?.items ?? [];
  const catalogueItems = catalogueOp.data?.items ?? [];
  const skillItems = skillsOp.data?.skills ?? [];

  const categories = useMemo(
    () => [...new Set(agentItems.map(a => a.agent?.category).filter((x): x is string => !!x))].sort(),
    [agentItems],
  );
  const agentById = useMemo(() => new Map(agentItems.map(a => [a.id, a])), [agentItems]);
  const baselineAgent = agentById.get('baseline')?.agent;

  const isSkillRoute = section === 'agents' && !!id && id.startsWith('skill:');
  const skillId = isSkillRoute ? id!.slice('skill:'.length) : undefined;
  const selectedTreeId = section === 'catalogue' && id
    ? `catalogue:${id}`
    : section === 'pipelines' && id
      ? `pipeline:${id}`
      : isSkillRoute && skillId
        ? `skill:${skillId}`
        : section === 'agents' && id
          ? `agent:${id}`
          : null;

  const treeQuery = useMemo(() => q.replace(/\b(?:agent|host|project):(\S+)/gi, '$1'), [q]);

  const sortDefs = (items: readonly DefinitionListItem[]) =>
    items.toSorted((a, b) => Number(!!b.draft) - Number(!!a.draft) || b.last30.runs - a.last30.runs || a.name.localeCompare(b.name));

  const libraryTreeNodes = useMemo<CatalogueTreeNode[]>(() => {
    const methodLeaves: CatalogueTreeNode[] = sortDefs(catalogueItems).map(d => {
      const nodeId = `catalogue:${d.id}`;
      return {
        id: nodeId,
        label: d.name,
        sub: d.name !== d.id ? d.id : undefined,
        state: { family: 'definition', state: d.draft ? 'draft' : 'ready' },
        right: d.state === 'repository-changed' ? <Icon name="download" label="Repository update waiting" /> : undefined,
        pinned: pins.has(nodeId),
        href: `/catalogue/${encodeURIComponent(d.id)}`,
        searchText: [d.id, d.name, d.scope, d.state].filter(Boolean).join(' '),
      };
    });

    const pipelineLeaves: CatalogueTreeNode[] = sortDefs(pipelineItems).map(d => {
      const nodeId = `pipeline:${d.id}`;
      const hasRight = d.state === 'repository-changed' || d.last30.runs > 0;
      return {
        id: nodeId,
        label: d.name,
        sub: d.name !== d.id ? d.id : undefined,
        state: { family: 'definition', state: d.draft ? 'draft' : d.last30.runs > 0 ? 'ready' : 'never' },
        right: hasRight ? (
          <>
            {d.state === 'repository-changed' && <Icon name="download" label="Repository update waiting" />}
            {d.last30.runs > 0 && <Num m={{ value: d.last30.runs, basis: 'measured' }} kind="count" />}
          </>
        ) : undefined,
        pinned: pins.has(nodeId),
        href: `/pipelines/${encodeURIComponent(d.id)}`,
        searchText: [d.id, d.name, d.scope, d.state].filter(Boolean).join(' '),
      };
    });

    const buckets = new Map<AgentRoleGroup, DefinitionListItem[]>(ROLE_GROUPS.map(g => [g.key, []]));
    for (const a of agentItems) buckets.get(agentRoleGroup(a))!.push(a);

    const agentFolders: CatalogueTreeNode[] = [];
    for (const group of ROLE_GROUPS) {
      const items = sortDefs(buckets.get(group.key) ?? []);
      if (!items.length) continue;
      agentFolders.push({
        id: group.id,
        label: group.label,
        icon: group.icon,
        defaultCollapsed: group.defaultCollapsed,
        children: items.map(d => {
          const nodeId = `agent:${d.id}`;
          const parent = d.agent?.variantOf ? agentById.get(d.agent.variantOf)?.agent : undefined;
          const baseHarness = baseAgentDefaults(parent, baselineAgent, d.agent?.harness).harness;
          const showHarness = !!(d.agent && d.agent.harness !== baseHarness && harnesses[d.agent.harness]);
          const right = (d.state === 'repository-changed' || showHarness) ? (
            <>
              {d.state === 'repository-changed' && <Icon name="download" label="Repository update waiting" />}
              {showHarness && <HarnessMark h={d.agent!.harness as Harness} />}
            </>
          ) : undefined;
          return {
            id: nodeId,
            label: d.name,
            sub: agentDeltaSub(d, agentById, baselineAgent),
            state: {
              family: 'definition',
              state: d.draft ? 'draft' : compatibilityState(d.compatibility),
            },
            right,
            pinned: pins.has(nodeId),
            href: `/agents/${encodeURIComponent(d.id)}`,
            searchText: agentSearchText(d),
            problem: d.compatibility === 'failed',
          };
        }),
      });
    }

    const skillLeaves: CatalogueTreeNode[] = skillItems
      .toSorted((a, b) => a.name.localeCompare(b.name))
      .map(s => {
        const nodeId = `skill:${s.id}`;
        return {
          id: nodeId,
          label: s.name,
          sub: s.id !== s.name ? s.id : undefined,
          state: { family: 'definition', state: s.agents.length ? 'ready' : 'never' },
          right: <span class="mono muted" data-tip="Enabled agents">{s.agents.length}/{s.totalAgents}</span>,
          pinned: pins.has(nodeId),
          href: `/agents/skill:${encodeURIComponent(s.id)}`,
          searchText: [s.id, s.name, s.description, ...s.agents.map(a => `agent:${a} ${a}`)].filter(Boolean).join(' '),
        };
      });

    return [
      { id: ROOT_METHODS_ID, label: 'Methods', icon: 'rows-3', defaultCollapsed: false, children: methodLeaves },
      { id: ROOT_PIPELINES_ID, label: 'Pipelines', icon: 'network', defaultCollapsed: false, children: pipelineLeaves },
      { id: ROOT_AGENTS_ID, label: 'Agents', icon: 'bot', defaultCollapsed: false, children: agentFolders },
      { id: ROOT_SKILLS_ID, label: 'Skills', icon: 'file-text', defaultCollapsed: false, children: skillLeaves },
    ];
  }, [catalogueItems, pipelineItems, agentItems, skillItems, agentById, baselineAgent, pins.ids]);

  const selectedIsLabVariant = section === 'agents' && !!id && !isSkillRoute && (() => {
    const a = agentById.get(id);
    return !!a && agentRoleGroup(a) === 'lab-variants';
  })();

  const defaultClosed = useMemo(
    () => (selectedIsLabVariant ? [] : ['group:lab-variants']),
    [selectedIsLabVariant],
  );
  const treeClosed = closedGroups(defaultClosed, treeFlipped);
  const collapsedMap = useMemo<Record<string, boolean>>(() => {
    const map: Record<string, boolean> = {
      __pinned__: treeClosed.has('__pinned__'),
      [ROOT_METHODS_ID]: treeClosed.has(ROOT_METHODS_ID),
      [ROOT_PIPELINES_ID]: treeClosed.has(ROOT_PIPELINES_ID),
      [ROOT_AGENTS_ID]: treeClosed.has(ROOT_AGENTS_ID),
      [ROOT_SKILLS_ID]: treeClosed.has(ROOT_SKILLS_ID),
    };
    for (const g of ROLE_GROUPS) map[g.id] = treeClosed.has(g.id);
    return map;
  }, [treeClosed]);

  const toggleTreeFolder = (folderId: string) => {
    setListGroups(LIBRARY_TREE_GROUPS, flipGroup(treeFlipped, folderId));
  };

  const countMatchingLeaves = (nodes: readonly CatalogueTreeNode[]): number => {
    let total = 0;
    for (const n of nodes) {
      if (Array.isArray(n.children)) {
        total += countMatchingLeaves(n.children);
      } else if (!treeQuery.trim() || matchSearch(treeQuery, [n.label, n.id, n.sub, n.searchText].filter(Boolean).join(' '), n.state ? { state: n.state.state } : {})) {
        total++;
      }
    }
    return total;
  };
  const totalCount = catalogueItems.length + pipelineItems.length + agentItems.length + skillItems.length;
  const shownCount = treeQuery.trim() ? countMatchingLeaves(libraryTreeNodes) : totalCount;

  const initialLoading = !agentsOp.data && !pipelinesOp.data && !catalogueOp.data && !skillsOp.data
    && !agentsOp.error && !pipelinesOp.error && !catalogueOp.error && !skillsOp.error;
  const loadError = (agentsOp.error && !agentsOp.data ? agentsOp.error : null)
    ?? (pipelinesOp.error && !pipelinesOp.data ? pipelinesOp.error : null)
    ?? (catalogueOp.error && !catalogueOp.data ? catalogueOp.error : null)
    ?? (skillsOp.error && !skillsOp.data ? skillsOp.error : null);

  const reloadAll = () => {
    void Promise.all([agentsOp.reload(), pipelinesOp.reload(), catalogueOp.reload(), skillsOp.reload(), changesOp.reload()]);
  };

  const listPane = (
    <>
      <ShellStatus><OfflineMarker state={agentsOp.state} now={Date.now()} /></ShellStatus>
      <ShellActions>
        <Button
          label="System changes"
          icon="git-pull-request"
          iconOnly
          kind="quiet"
          pressed={section === 'changes'}
          onClick={() => go(section === 'changes' ? '/agents' : '/changes')}
        />
      </ShellActions>
      <header class="page-h">
        <SearchField label="Find in library" value={q} onInput={setQ} onSearch={() => {}} />
        <span class="grow" />
        <span class="muted mono" aria-label={`${shownCount} of ${totalCount} library items`}>
          {shownCount < totalCount ? `${shownCount} of ${totalCount}` : totalCount}
        </span>
      </header>
      {loadError && <ErrorBanner text={loadError} onRetry={reloadAll} />}
      <section class="panel">
        {initialLoading ? (
          <Skeleton rows={8} />
        ) : (
          <CatalogueTree
            fill
            nodes={libraryTreeNodes}
            label="Library"
            selectedId={selectedTreeId}
            query={treeQuery}
            collapsed={collapsedMap}
            emptyText="No matching items."
            onToggle={toggleTreeFolder}
            onSelect={node => {
              if (Array.isArray(node.children)) {
                toggleTreeFolder(node.id);
              } else if (node.href) {
                go(node.href);
              }
            }}
            onPin={node => pins.toggle(node.id)}
            onRowMenu={(node, at) => {
              if (!Array.isArray(node.children)) setMenu({ node, at });
            }}
          />
        )}
        {menu && (
          <ContextMenu
            at={menu.at}
            label={menu.node.label}
            onClose={() => setMenu(null)}
            items={[
              { label: 'Open', icon: actions.open, onClick: () => { if (menu.node.href) go(menu.node.href); } },
              { label: pins.has(menu.node.id) ? 'Unpin' : 'Pin', icon: pins.has(menu.node.id) ? actions.unpin : actions.pin, onClick: () => pins.toggle(menu.node.id) },
              { label: 'Copy id', icon: actions.copy, onClick: () => copyText(rawNodeId(menu.node.id)) },
            ]}
          />
        )}
      </section>
    </>
  );

  const runTakeRepo = async (keys: DefinitionKey[], preview: boolean) => {
    setBusy(true);
    setNotice(null);
    try {
      const r = await op('definition_take_repository', { keys, preview, message: 'Take repository version from Library' });
      if (preview) {
        setPreviewRepoKeys(keys);
        setNotice({ severity: 'warning', title: `Preview: take repository version for ${r.items.filter(i => i.status === 'would-apply').length} definitions`, facts: r.items.map(i => `${i.kind}/${i.id}: ${i.status}`).join(' · ') });
      } else {
        setPreviewRepoKeys(null);
        setNotice({ severity: 'warning', title: `Updated ${r.applied} definitions from repository`, facts: r.items.map(i => `${i.kind}/${i.id}: ${i.status}`).join(' · ') });
        await Promise.all([agentsOp.reload(), pipelinesOp.reload(), catalogueOp.reload(), changesOp.reload()]);
      }
    } catch (e) {
      setNotice({ severity: 'failure', title: 'Repository sync failed', facts: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  const harnessSummary = useMemo(() =>
    Object.entries(harnesses).map(([key, info]) => {
      const items = agentItems.filter(a => a.agent?.harness === key);
      return {
        key,
        label: info.label,
        icon: info.icon,
        count: items.length,
        runs: items.reduce((s, a) => s + a.last30.runs, 0),
        tokens: items.reduce((s, a) => s + a.last30.tokens, 0),
      };
    }).filter(x => x.count > 0),
  [agentItems]);

  const categorySummary = useMemo(() => {
    const byCat = new Map<string, DefinitionListItem[]>();
    for (const a of agentItems) {
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
  }, [agentItems]);

  const primaryAgents = agentItems.filter(a => !a.agent?.variantOf);
  const variantAgents = agentItems.filter(a => !!a.agent?.variantOf);
  const activePipelines = pipelineItems.filter(p => p.last30.runs > 0).length;
  const activeSkills = skillItems.filter(s => s.agents.length > 0).length;
  const totalRuns = agentItems.reduce((s, a) => s + a.last30.runs, 0) + pipelineItems.reduce((s, p) => s + p.last30.runs, 0);
  const totalTokens = agentItems.reduce((s, a) => s + a.last30.tokens, 0) + pipelineItems.reduce((s, p) => s + p.last30.tokens, 0);

  const summaryTotals: TotalItem[] = [
    { label: 'Methods', m: { value: catalogueItems.length, basis: 'measured' }, kind: 'count' },
    {
      label: 'Pipelines',
      m: { value: pipelineItems.length, basis: 'measured' },
      kind: 'count',
      note: activePipelines > 0 ? `${activePipelines} active (30d)` : undefined,
    },
    {
      label: 'Agents',
      m: { value: agentItems.length, basis: 'measured' },
      kind: 'count',
      note: variantAgents.length ? `${primaryAgents.length} primary · ${variantAgents.length} variants` : undefined,
    },
    {
      label: 'Skills',
      m: { value: skillItems.length, basis: 'measured' },
      kind: 'count',
      note: activeSkills > 0 ? `${activeSkills} enabled` : undefined,
    },
    { label: '30d runs', m: { value: totalRuns, basis: 'measured' }, kind: 'count' },
    { label: '30d tokens', m: { value: totalTokens, basis: 'measured' }, kind: 'count' },
  ];

  const changesData = changesOp.data;
  const takeableKeys: DefinitionKey[] = changesData
    ? changesData.repositoryChanged.filter(d => d.state === 'repository-changed').map(d => ({ kind: d.kind, id: d.id }))
    : [...agentItems, ...pipelineItems, ...catalogueItems]
        .filter(d => d.state === 'repository-changed')
        .map(d => ({ kind: d.kind, id: d.id }));
  const behindHosts = changesData?.versions.hosts.filter(h => h.behind) ?? [];
  const behindHarnesses = changesData?.harnessDrift.filter(h => h.behind) ?? [];
  const driftFacts = changesData ? [
    changesData.repositoryChanged.length > 0 && `${changesData.repositoryChanged.length} repo changed`,
    changesData.storeOnly.length > 0 && `${changesData.storeOnly.length} store-only`,
    changesData.ahead.length > 0 && `${changesData.ahead.length} ahead`,
    behindHosts.length > 0 && `${behindHosts.length} hosts behind`,
    behindHarnesses.length > 0 && `${behindHarnesses.length} harness pins drifting`,
  ].filter((x): x is string => !!x) : (takeableKeys.length > 0 ? [`${takeableKeys.length} repo changed`] : []);

  const idleSummary = (
    <article class="detail stack" aria-label="Library summary">
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
            : [{ label: 'Dismiss', onClick: () => setNotice(null) }]}
        />
      )}

      {driftFacts.length > 0 && (
        <ProblemRow
          severity="warning"
          title="System drift"
          facts={driftFacts.join(' · ')}
          actions={[
            ...(takeableKeys.length > 0 ? [{ label: `Take repo (${takeableKeys.length})`, icon: 'download' as const, primary: true, onClick: () => void runTakeRepo(takeableKeys, true) }] : []),
            { label: 'Changes', icon: 'git-pull-request' as const, onClick: () => go('/changes') },
          ]}
        />
      )}

      <Totals items={summaryTotals} />

      {harnessSummary.length > 0 && (
        <DetailSection title="By harness">
          {harnessSummary.map(h => (
            <KvRow
              key={h.key}
              icon={h.icon}
              label={h.label}
              meta={`${h.count} agent${h.count === 1 ? '' : 's'} · ${h.runs} runs (30d)`}
              value={<Num m={{ value: h.tokens, basis: 'measured' }} kind="count" />}
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
              value={<Num m={{ value: c.tokens, basis: 'measured' }} kind="count" />}
            />
          ))}
        </DetailSection>
      )}
    </article>
  );

  const detail = section === 'changes'
    ? <Changes key="changes" onClose={() => go('/agents')} />
    : isSkillRoute && skillId
      ? (
        <SkillDetail
          key={`skill/${skillId}`}
          skillId={skillId}
          skills={skillItems}
          loading={!skillsOp.data && !skillsOp.error}
          error={skillsOp.error}
          onRetry={skillsOp.reload}
          categories={categories}
          agentItems={agentItems}
          busy={busy}
          setBusy={setBusy}
          onChanged={async () => { await Promise.all([skillsOp.reload(), agentsOp.reload()]); }}
          onClose={() => go('/agents')}
        />
      )
      : section === 'catalogue' && id
        ? <DefinitionDetail key={`catalogue/${id}`} kind="catalogue" id={id} agentById={agentById} onClose={() => go('/catalogue')} />
        : section === 'pipelines' && id
          ? <DefinitionDetail key={`pipeline/${id}`} kind="pipeline" id={id} agentById={agentById} onClose={() => go('/pipelines')} />
          : section === 'agents' && id
            ? <DefinitionDetail key={`agent/${id}`} kind="agent" id={id} agentById={agentById} onClose={() => go('/agents')} />
            : undefined;

  return (
    <ListDetail
      list={listPane}
      detail={detail}
      detailKey={section === 'changes' ? 'changes' : selectedTreeId ?? undefined}
      idle={idleSummary}
    />
  );
}

function SkillDetail({
  skillId,
  skills,
  loading,
  error,
  onRetry,
  categories,
  agentItems,
  busy,
  setBusy,
  onChanged,
  onClose,
}: {
  skillId: string;
  skills: readonly SkillSummary[];
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  categories: readonly string[];
  agentItems: readonly DefinitionListItem[];
  busy: boolean;
  setBusy: (b: boolean) => void;
  onChanged: () => Promise<void>;
  onClose: () => void;
}) {
  const [targetCat, setTargetCat] = useState('');
  const [notice, setNotice] = useState<{ severity: 'warning' | 'failure'; title: string; facts: string } | null>(null);
  const [previewSkill, setPreviewSkill] = useState<{ skill: string; category?: string; agents?: string[] } | null>(null);

  const skill = skills.find(s => s.id === skillId);
  const chosenCat = targetCat || categories[0] || '';
  const missingAgents = useMemo(
    () => agentItems.filter(a => !(a.agent?.skills ?? []).includes(skillId)).map(a => a.id),
    [agentItems, skillId],
  );
  const repoOnly = skill ? skill.repoAgents.filter(a => !skill.agents.includes(a)) : [];

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
        await onChanged();
      }
    } catch (e) {
      setNotice({ severity: 'failure', title: 'Add skill failed', facts: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <article class="detail stack" data-ui="detail" tabIndex={-1} aria-label={skill?.name ?? skillId}>
      <header class="detail-h">
        <Button label="Close" icon="x" iconOnly onClick={onClose} />
        <span class="muted mono">{skill?.path ?? `skill:${skillId}`}</span>
        <span class="grow" />
        {skill && <StateBadge family="definition" state={skill.agents.length ? 'ready' : 'never'} />}
      </header>
      <h2 class="detail-title">{skill?.name ?? skillId}</h2>

      {notice && (
        <ProblemRow
          severity={notice.severity}
          title={notice.title}
          facts={notice.facts}
          actions={previewSkill
            ? [
                { label: `Add ${previewSkill.skill}`, primary: true, onClick: () => void runAddSkill(previewSkill, false) },
                { label: 'Dismiss', onClick: () => { setPreviewSkill(null); setNotice(null); } },
              ]
            : [{ label: 'Dismiss', onClick: () => setNotice(null) }]}
        />
      )}

      {error ? (
        <ErrorBanner text={error} onRetry={onRetry} />
      ) : loading ? (
        <Empty text="Loading…" />
      ) : !skill ? (
        <Empty text={`Skill ${skillId} not found.`} />
      ) : (
        <>
          <DetailSection title="Rollout">
            <KvRow
              icon={skill.agents.length ? 'circle-check' : 'file-text'}
              label={skill.description || 'Shipped skill'}
              meta={`Active in ${skill.agents.length} of ${skill.totalAgents} store agents · ${skill.repoAgents.length} in repository`}
            />
            {skill.agents.length > 0 && (
              <KvRow
                icon="bot"
                label="Enabled on"
                meta={<span class="acts">{skill.agents.map(a => <EntityCode key={a} code={a} id={a} kind="agent" cls="inline" />)}</span>}
              />
            )}
            {repoOnly.length > 0 && (
              <KvRow
                icon="git-merge"
                label="Repository only"
                meta={<span class="acts">{repoOnly.map(a => <EntityCode key={a} code={a} id={a} kind="agent" cls="inline" />)}</span>}
              />
            )}
            {(categories.length > 0 || missingAgents.length > 0) && (
              <KvRow
                icon="plus"
                label="Add to agents"
                value={
                  <span class="acts">
                    {categories.length > 0 && (
                      <>
                        <Choice
                          label={`Target category for ${skill.id}`}
                          value={chosenCat}
                          options={categories.map(c => ({ id: c, label: c }))}
                          onChange={setTargetCat}
                        />
                        <Button
                          label={`Add to ${chosenCat}`}
                          icon="plus"
                          kind="quiet"
                          busy={busy}
                          onClick={() => void runAddSkill({ skill: skill.id, category: chosenCat }, true)}
                        />
                      </>
                    )}
                    {missingAgents.length > 0 && (
                      <Button
                        label={`Add to all missing (${missingAgents.length})`}
                        icon="plus"
                        kind="quiet"
                        busy={busy}
                        onClick={() => void runAddSkill({ skill: skill.id, agents: missingAgents }, true)}
                      />
                    )}
                  </span>
                }
              />
            )}
          </DetailSection>

          <DetailSection title="Instructions">
            <pre class="skill-body">{skill.body}</pre>
          </DetailSection>
        </>
      )}
    </article>
  );
}
