// Unified Library screen (docs/designs/fleet-ia-and-journeys.md, docs/design-system/pages/agents.md):
// one 4-root CatalogueTree (Methods, Pipelines, Agents, Skills) in the control column, Library summary + drift banner
// when idle, and DefinitionDetail / SkillDetail / Changes in the work area.
import { createMemo, createSignal, For, Match, Show, Switch } from 'solid-js';
import { useLocation } from '../router.ts';
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

export function Agents(props: { section?: LibrarySection; id?: string }) {
  const section = () => props.section ?? 'agents';
  const [, go] = useLocation();
  const [q, setQ] = createSignal('');
  const [busy, setBusy] = createSignal(false);
  const [notice, setNotice] = createSignal<{ severity: 'warning' | 'failure'; title: string; facts: string } | null>(null);
  const [previewRepoKeys, setPreviewRepoKeys] = createSignal<DefinitionKey[] | null>(null);
  const [menu, setMenu] = createSignal<{ node: CatalogueTreeNode; at: { x: number; y: number } } | null>(null);

  const pins = usePins(LIBRARY_PINS);
  const treeFlipped = createMemo(() => listGroups(LIBRARY_TREE_GROUPS));

  const agentsOp = useOp('definition_list', { kind: 'agent' });
  const pipelinesOp = useOp('definition_list', { kind: 'pipeline' });
  const catalogueOp = useOp('definition_list', { kind: 'catalogue' });
  const skillsOp = useOp('skills_list', {});
  const changesOp = useOp('system_changes', {});

  const agentItems = createMemo(() => agentsOp.data?.items ?? []);
  const pipelineItems = createMemo(() => pipelinesOp.data?.items ?? []);
  const catalogueItems = createMemo(() => catalogueOp.data?.items ?? []);
  const skillItems = createMemo(() => skillsOp.data?.skills ?? []);

  const categories = createMemo(
    () => [...new Set(agentItems().map(a => a.agent?.category).filter((x): x is string => !!x))].sort(),
  );
  const agentById = createMemo(() => new Map(agentItems().map(a => [a.id, a])));
  const baselineAgent = createMemo(() => agentById().get('baseline')?.agent);

  const isSkillRoute = createMemo(() => section() === 'agents' && !!props.id && props.id.startsWith('skill:'));
  const skillId = createMemo(() => (isSkillRoute() ? props.id!.slice('skill:'.length) : undefined));
  const selectedTreeId = createMemo(() => {
    const sec = section(), id = props.id, sid = skillId();
    return sec === 'catalogue' && id
      ? `catalogue:${id}`
      : sec === 'pipelines' && id
        ? `pipeline:${id}`
        : isSkillRoute() && sid
          ? `skill:${sid}`
          : sec === 'agents' && id
            ? `agent:${id}`
            : null;
  });

  const treeQuery = createMemo(() => q().replace(/\b(?:agent|host|project):(\S+)/gi, '$1'));

  const sortDefs = (items: readonly DefinitionListItem[]) =>
    items.toSorted((a, b) => Number(!!b.draft) - Number(!!a.draft) || b.last30.runs - a.last30.runs || a.name.localeCompare(b.name));

  const libraryTreeNodes = createMemo<CatalogueTreeNode[]>(() => {
    const byId = agentById(), base = baselineAgent();
    const methodLeaves: CatalogueTreeNode[] = sortDefs(catalogueItems()).map(d => {
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

    const pipelineLeaves: CatalogueTreeNode[] = sortDefs(pipelineItems()).map(d => {
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
    for (const a of agentItems()) buckets.get(agentRoleGroup(a))!.push(a);

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
          const parent = d.agent?.variantOf ? byId.get(d.agent.variantOf)?.agent : undefined;
          const baseHarness = baseAgentDefaults(parent, base, d.agent?.harness).harness;
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
            sub: agentDeltaSub(d, byId, base),
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

    const skillLeaves: CatalogueTreeNode[] = skillItems()
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
  });

  const selectedIsLabVariant = createMemo(() => {
    if (section() !== 'agents' || !props.id || isSkillRoute()) return false;
    const a = agentById().get(props.id);
    return !!a && agentRoleGroup(a) === 'lab-variants';
  });

  const defaultClosed = createMemo(() => (selectedIsLabVariant() ? [] : ['group:lab-variants']));
  const treeClosed = createMemo(() => closedGroups(defaultClosed(), treeFlipped()));
  const collapsedMap = createMemo<Record<string, boolean>>(() => {
    const closed = treeClosed();
    const map: Record<string, boolean> = {
      __pinned__: closed.has('__pinned__'),
      [ROOT_METHODS_ID]: closed.has(ROOT_METHODS_ID),
      [ROOT_PIPELINES_ID]: closed.has(ROOT_PIPELINES_ID),
      [ROOT_AGENTS_ID]: closed.has(ROOT_AGENTS_ID),
      [ROOT_SKILLS_ID]: closed.has(ROOT_SKILLS_ID),
    };
    for (const g of ROLE_GROUPS) map[g.id] = closed.has(g.id);
    return map;
  });

  const toggleTreeFolder = (folderId: string) => {
    setListGroups(LIBRARY_TREE_GROUPS, flipGroup(treeFlipped(), folderId));
  };

  const countMatchingLeaves = (nodes: readonly CatalogueTreeNode[]): number => {
    let total = 0;
    const tq = treeQuery();
    for (const n of nodes) {
      if (Array.isArray(n.children)) {
        total += countMatchingLeaves(n.children);
      } else if (!tq.trim() || matchSearch(tq, [n.label, n.id, n.sub, n.searchText].filter(Boolean).join(' '), n.state ? { state: n.state.state } : {})) {
        total++;
      }
    }
    return total;
  };
  const totalCount = createMemo(() => catalogueItems().length + pipelineItems().length + agentItems().length + skillItems().length);
  const shownCount = createMemo(() => treeQuery().trim() ? countMatchingLeaves(libraryTreeNodes()) : totalCount());

  const initialLoading = createMemo(() =>
    !agentsOp.data && !pipelinesOp.data && !catalogueOp.data && !skillsOp.data
    && !agentsOp.error && !pipelinesOp.error && !catalogueOp.error && !skillsOp.error,
  );
  const loadError = createMemo(() =>
    (agentsOp.error && !agentsOp.data ? agentsOp.error : null)
    ?? (pipelinesOp.error && !pipelinesOp.data ? pipelinesOp.error : null)
    ?? (catalogueOp.error && !catalogueOp.data ? catalogueOp.error : null)
    ?? (skillsOp.error && !skillsOp.data ? skillsOp.error : null),
  );

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
          pressed={section() === 'changes'}
          onClick={() => go(section() === 'changes' ? '/agents' : '/changes')}
        />
      </ShellActions>
      <header class="page-h">
        <SearchField label="Find in library" value={q()} onInput={setQ} onSearch={() => {}} />
        <span class="grow" />
        <span class="muted mono" aria-label={`${shownCount()} of ${totalCount()} library items`}>
          {shownCount() < totalCount() ? `${shownCount()} of ${totalCount()}` : totalCount()}
        </span>
      </header>
      <Show when={loadError()}>{err => <ErrorBanner text={err()} onRetry={reloadAll} />}</Show>
      <section class="panel">
        <Show when={!initialLoading()} fallback={<Skeleton rows={8} />}>
          <CatalogueTree
            fill
            nodes={libraryTreeNodes()}
            label="Library"
            selectedId={selectedTreeId()}
            query={treeQuery()}
            collapsed={collapsedMap()}
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
        </Show>
        <Show when={menu()}>{m => (
          <ContextMenu
            at={m().at}
            label={m().node.label}
            onClose={() => setMenu(null)}
            items={[
              { label: 'Open', icon: actions.open, onClick: () => { const href = m().node.href; if (href) go(href); } },
              { label: pins.has(m().node.id) ? 'Unpin' : 'Pin', icon: pins.has(m().node.id) ? actions.unpin : actions.pin, onClick: () => pins.toggle(m().node.id) },
              { label: 'Copy id', icon: actions.copy, onClick: () => copyText(rawNodeId(m().node.id)) },
            ]}
          />
        )}</Show>
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

  const harnessSummary = createMemo(() =>
    Object.entries(harnesses).map(([key, info]) => {
      const items = agentItems().filter(a => a.agent?.harness === key);
      return {
        key,
        label: info.label,
        icon: info.icon,
        count: items.length,
        runs: items.reduce((s, a) => s + a.last30.runs, 0),
        tokens: items.reduce((s, a) => s + a.last30.tokens, 0),
      };
    }).filter(x => x.count > 0),
  );

  const categorySummary = createMemo(() => {
    const byCat = new Map<string, DefinitionListItem[]>();
    for (const a of agentItems()) {
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
  });

  const summaryTotals = createMemo<TotalItem[]>(() => {
    const agents = agentItems(), pipelines = pipelineItems(), cats = catalogueItems(), skills = skillItems();
    const primaryAgents = agents.filter(a => !a.agent?.variantOf);
    const variantAgents = agents.filter(a => !!a.agent?.variantOf);
    const activePipelines = pipelines.filter(p => p.last30.runs > 0).length;
    const activeSkills = skills.filter(s => s.agents.length > 0).length;
    const totalRuns = agents.reduce((s, a) => s + a.last30.runs, 0) + pipelines.reduce((s, p) => s + p.last30.runs, 0);
    const totalTokens = agents.reduce((s, a) => s + a.last30.tokens, 0) + pipelines.reduce((s, p) => s + p.last30.tokens, 0);
    return [
      { label: 'Methods', m: { value: cats.length, basis: 'measured' }, kind: 'count' },
      {
        label: 'Pipelines',
        m: { value: pipelines.length, basis: 'measured' },
        kind: 'count',
        note: activePipelines > 0 ? `${activePipelines} active (30d)` : undefined,
      },
      {
        label: 'Agents',
        m: { value: agents.length, basis: 'measured' },
        kind: 'count',
        note: variantAgents.length ? `${primaryAgents.length} primary · ${variantAgents.length} variants` : undefined,
      },
      {
        label: 'Skills',
        m: { value: skills.length, basis: 'measured' },
        kind: 'count',
        note: activeSkills > 0 ? `${activeSkills} enabled` : undefined,
      },
      { label: '30d runs', m: { value: totalRuns, basis: 'measured' }, kind: 'count' },
      { label: '30d tokens', m: { value: totalTokens, basis: 'measured' }, kind: 'count' },
    ];
  });

  const takeableKeys = createMemo<DefinitionKey[]>(() => {
    const changesData = changesOp.data;
    return changesData
      ? changesData.repositoryChanged.filter(d => d.state === 'repository-changed').map(d => ({ kind: d.kind, id: d.id }))
      : [...agentItems(), ...pipelineItems(), ...catalogueItems()]
          .filter(d => d.state === 'repository-changed')
          .map(d => ({ kind: d.kind, id: d.id }));
  });
  const driftFacts = createMemo(() => {
    const changesData = changesOp.data;
    if (!changesData) return takeableKeys().length > 0 ? [`${takeableKeys().length} repo changed`] : [];
    const behindHosts = changesData.versions.hosts.filter(h => h.behind);
    const behindHarnesses = changesData.harnessDrift.filter(h => h.behind);
    return [
      changesData.repositoryChanged.length > 0 && `${changesData.repositoryChanged.length} repo changed`,
      changesData.storeOnly.length > 0 && `${changesData.storeOnly.length} store-only`,
      changesData.ahead.length > 0 && `${changesData.ahead.length} ahead`,
      behindHosts.length > 0 && `${behindHosts.length} hosts behind`,
      behindHarnesses.length > 0 && `${behindHarnesses.length} harness pins drifting`,
    ].filter((x): x is string => !!x);
  });

  const idleSummary = (
    <article class="detail stack" aria-label="Library summary">
      <Show when={notice()}>{n => (
        <ProblemRow
          severity={n().severity}
          title={n().title}
          facts={n().facts}
          actions={previewRepoKeys()
            ? [
                { label: 'Apply repository versions', primary: true, onClick: () => void runTakeRepo(previewRepoKeys()!, false) },
                { label: 'Dismiss', onClick: () => { setPreviewRepoKeys(null); setNotice(null); } },
              ]
            : [{ label: 'Dismiss', onClick: () => setNotice(null) }]}
        />
      )}</Show>

      <Show when={driftFacts().length > 0}>
        <ProblemRow
          severity="warning"
          title="System drift"
          facts={driftFacts().join(' · ')}
          actions={[
            ...(takeableKeys().length > 0 ? [{ label: `Take repo (${takeableKeys().length})`, icon: 'download' as const, primary: true, onClick: () => void runTakeRepo(takeableKeys(), true) }] : []),
            { label: 'Changes', icon: 'git-pull-request' as const, onClick: () => go('/changes') },
          ]}
        />
      </Show>

      <Totals items={summaryTotals()} />

      <Show when={harnessSummary().length > 0}>
        <DetailSection title="By harness">
          <For each={harnessSummary()}>{h => (
            <KvRow
              icon={h.icon}
              label={h.label}
              meta={`${h.count} agent${h.count === 1 ? '' : 's'} · ${h.runs} runs (30d)`}
              value={<Num m={{ value: h.tokens, basis: 'measured' }} kind="count" />}
            />
          )}</For>
        </DetailSection>
      </Show>

      <Show when={categorySummary().length > 0}>
        <DetailSection title="By category">
          <For each={categorySummary()}>{c => (
            <KvRow
              icon="layers-2"
              label={c.cat}
              meta={`${c.count} agent${c.count === 1 ? '' : 's'} · ${c.runs} runs (30d)`}
              value={<Num m={{ value: c.tokens, basis: 'measured' }} kind="count" />}
            />
          )}</For>
        </DetailSection>
      </Show>
    </article>
  );

  return (
    <ListDetail
      list={listPane}
      detailKey={section() === 'changes' ? 'changes' : selectedTreeId() ?? undefined}
      idle={idleSummary}
      detail={
        section() === 'changes' || (isSkillRoute() && skillId()) || (!!props.id && (section() === 'catalogue' || section() === 'pipelines' || section() === 'agents')) ? (
          <Switch>
            <Match when={section() === 'changes'}><Changes onClose={() => go('/agents')} /></Match>
            <Match when={isSkillRoute() && skillId()}>
              <Show when={skillId()} keyed>{sid => (
                <SkillDetail
                  skillId={sid}
                  skills={skillItems()}
                  loading={!skillsOp.data && !skillsOp.error}
                  error={skillsOp.error}
                  onRetry={skillsOp.reload}
                  categories={categories()}
                  agentItems={agentItems()}
                  busy={busy()}
                  setBusy={setBusy}
                  onChanged={async () => { await Promise.all([skillsOp.reload(), agentsOp.reload()]); }}
                  onClose={() => go('/agents')}
                />
              )}</Show>
            </Match>
            <Match when={section() === 'catalogue' && props.id}>
              <Show when={props.id} keyed>{did => <DefinitionDetail kind="catalogue" id={did} agentById={agentById()} onClose={() => go('/catalogue')} />}</Show>
            </Match>
            <Match when={section() === 'pipelines' && props.id}>
              <Show when={props.id} keyed>{did => <DefinitionDetail kind="pipeline" id={did} agentById={agentById()} onClose={() => go('/pipelines')} />}</Show>
            </Match>
            <Match when={section() === 'agents' && props.id}>
              <Show when={props.id} keyed>{did => <DefinitionDetail kind="agent" id={did} agentById={agentById()} onClose={() => go('/agents')} />}</Show>
            </Match>
          </Switch>
        ) : undefined
      }
    />
  );
}

function SkillDetail(props: {
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
  const [targetCat, setTargetCat] = createSignal('');
  const [notice, setNotice] = createSignal<{ severity: 'warning' | 'failure'; title: string; facts: string } | null>(null);
  const [previewSkill, setPreviewSkill] = createSignal<{ skill: string; category?: string; agents?: string[] } | null>(null);

  const skill = createMemo(() => props.skills.find(s => s.id === props.skillId));
  const chosenCat = createMemo(() => targetCat() || props.categories[0] || '');
  const missingAgents = createMemo(() => props.agentItems.filter(a => !(a.agent?.skills ?? []).includes(props.skillId)).map(a => a.id));
  const repoOnly = createMemo(() => {
    const s = skill();
    return s ? s.repoAgents.filter(a => !s.agents.includes(a)) : [];
  });

  const runAddSkill = async (req: { skill: string; category?: string; agents?: string[] }, preview: boolean) => {
    props.setBusy(true);
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
        await props.onChanged();
      }
    } catch (e) {
      setNotice({ severity: 'failure', title: 'Add skill failed', facts: e instanceof Error ? e.message : String(e) });
    } finally {
      props.setBusy(false);
    }
  };

  return (
    <article class="detail stack" data-ui="detail" tabindex={-1} aria-label={skill()?.name ?? props.skillId}>
      <header class="detail-h">
        <Button label="Close" icon="x" iconOnly onClick={() => props.onClose()} />
        <span class="muted mono">{skill()?.path ?? `skill:${props.skillId}`}</span>
        <span class="grow" />
        <Show when={skill()}>{s => <StateBadge family="definition" state={s().agents.length ? 'ready' : 'never'} />}</Show>
      </header>
      <h2 class="detail-title">{skill()?.name ?? props.skillId}</h2>

      <Show when={notice()}>{n => (
        <ProblemRow
          severity={n().severity}
          title={n().title}
          facts={n().facts}
          actions={previewSkill()
            ? [
                { label: `Add ${previewSkill()!.skill}`, primary: true, onClick: () => void runAddSkill(previewSkill()!, false) },
                { label: 'Dismiss', onClick: () => { setPreviewSkill(null); setNotice(null); } },
              ]
            : [{ label: 'Dismiss', onClick: () => setNotice(null) }]}
        />
      )}</Show>

      <Show when={props.error} fallback={
        <Show when={!props.loading} fallback={<Empty text="Loading…" />}>
          <Show when={skill()} fallback={<Empty text={`Skill ${props.skillId} not found.`} />}>{s => <>
            <DetailSection title="Rollout">
              <KvRow
                icon={s().agents.length ? 'circle-check' : 'file-text'}
                label={s().description || 'Shipped skill'}
                meta={`Active in ${s().agents.length} of ${s().totalAgents} store agents · ${s().repoAgents.length} in repository`}
              />
              <Show when={s().agents.length > 0}>
                <KvRow
                  icon="bot"
                  label="Enabled on"
                  meta={<span class="acts"><For each={s().agents}>{a => <EntityCode code={a} id={a} kind="agent" cls="inline" />}</For></span>}
                />
              </Show>
              <Show when={repoOnly().length > 0}>
                <KvRow
                  icon="git-merge"
                  label="Repository only"
                  meta={<span class="acts"><For each={repoOnly()}>{a => <EntityCode code={a} id={a} kind="agent" cls="inline" />}</For></span>}
                />
              </Show>
              <Show when={props.categories.length > 0 || missingAgents().length > 0}>
                <KvRow
                  icon="plus"
                  label="Add to agents"
                  value={
                    <span class="acts">
                      <Show when={props.categories.length > 0}>
                        <Choice
                          label={`Target category for ${s().id}`}
                          value={chosenCat()}
                          options={props.categories.map(c => ({ id: c, label: c }))}
                          onChange={setTargetCat}
                        />
                        <Button
                          label={`Add to ${chosenCat()}`}
                          icon="plus"
                          kind="quiet"
                          busy={props.busy}
                          onClick={() => void runAddSkill({ skill: s().id, category: chosenCat() }, true)}
                        />
                      </Show>
                      <Show when={missingAgents().length > 0}>
                        <Button
                          label={`Add to all missing (${missingAgents().length})`}
                          icon="plus"
                          kind="quiet"
                          busy={props.busy}
                          onClick={() => void runAddSkill({ skill: s().id, agents: missingAgents() }, true)}
                        />
                      </Show>
                    </span>
                  }
                />
              </Show>
            </DetailSection>

            <DetailSection title="Instructions">
              <pre class="skill-body">{s().body}</pre>
            </DetailSection>
          </>}</Show>
        </Show>
      }>{err => <ErrorBanner text={err()} onRetry={() => props.onRetry()} />}</Show>
    </article>
  );
}
