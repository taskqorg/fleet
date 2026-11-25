// Library section (docs/design-system/pages/library.md): one CatalogueTree with the roots Methods, Pipelines, Agents,
// Bots, Skills and Changes in the control column; what waits first when idle; a definition, skill, bot or Changes in the
// work area. Library speaks its target routes (/library/<root>/<id>, routes.ts).
import { createMemo, createSignal, For, Match, Show, Switch } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { useLocation } from '../router.ts';
import {
  Ago, Avatar, Button, CatalogueTree, Choice, ContextMenu, DetailSection, Empty, EntityCode, ErrorBanner, FigureTile, HarnessMark,
  KvRow, ListDetail, LiveRow, OfflineMarker, ProblemRow, SearchField, ShellActions, ShellStatus, Skeleton, StateBadge,
  actions, copyText, harnesses, matchSearch,
  type CatalogueTreeNode, type Harness, type IconName, type StateOf,
  plural,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { closedGroups, flipGroup } from '../lists.ts';
import { listGroups, prefs, prefsRead, scopeIds, setListGroups, usePins } from '../prefs.ts';
import { useOp } from '../store.ts';
import { parseMs as ms } from '../task-facts.ts';
import { Changes } from './changes.tsx';
import { BotCreateForm, BotDetail, botPath } from './bots.tsx';
import { botAvatarState, botPicture } from '../bot-view.ts';
import { DefinitionDetail, baseAgentDefaults } from './definitions.tsx';
import type { DefinitionKey, DefinitionListItem, SkillSummary } from '../../../contracts/generated/ops.ts';
import type { BotListItem } from '../../../contracts/generated/ops-bots.ts';

export type LibrarySection = 'idle' | 'agents' | 'pipelines' | 'methods' | 'bots' | 'skills' | 'changes';
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

const ROOT_BOTS_ID = 'root:bots';
const CHANGES_ID = 'changes';

/** A bot in the one state vocabulary (OR-55): paused is Stopped, an active run Working, a failed last run Failed. */
const botLife = (b: BotListItem): StateOf<'life'> =>
  b.paused ? 'stopped' : b.activeRunId ? 'working' : b.lastRunState === 'failed' ? 'failed' : 'asleep';

/** A definition leaf's state: a newer repository version waits first, then the draft, then its own state. */
const leafState = (d: DefinitionListItem, own: StateOf<'definition'>): StateOf<'definition'> =>
  d.state === 'repository-changed' ? 'repository' : d.draft ? 'draft' : own;

const runsRight = (runs: number) => runs > 0 ? <span class="mono muted" data-tip="Runs in 30 days">{runs}</span> : undefined;


export function Library(props: { section?: LibrarySection; id?: string }) {
  const section = () => props.section ?? 'idle';
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
  const botsOp = useOp('bot_list', () => (prefsRead.value ? scopeIds() : null));
  const projectsOp = useOp('project_list', {});
  // Host observations touch system_changes every few seconds; reread it at most every 30 s (FLT-38).
  const changesOp = useOp('system_changes', {}, { quiet: 30_000 });
  // Recommendations projects selected this week: a Lab promotion names its experiment (J3).
  const recentRecs = useOp('recommendation_changes', {});

  const agentItems = createMemo(() => agentsOp.data?.items ?? []);
  const pipelineItems = createMemo(() => pipelinesOp.data?.items ?? []);
  const catalogueItems = createMemo(() => catalogueOp.data?.items ?? []);
  const skillItems = createMemo(() => skillsOp.data?.skills ?? []);
  const botItems = createMemo(() => botsOp.data?.bots ?? []);
  const projectNodes = createMemo(() => {
    const ignored = prefs.value.ignored;
    return (projectsOp.data?.projects ?? []).filter(p => !ignored.includes(String(p.id)))
      .map(p => ({ id: String(p.id), label: String(p.name ?? p.id), icon: 'folder-open' as const }));
  });
  const projectNames = createMemo(() => new Map(projectNodes().map(p => [p.id, p.label])));

  const categories = createMemo(
    () => [...new Set(agentItems().map(a => a.agent?.category).filter((x): x is string => !!x))].sort(),
  );
  const agentById = createMemo(() => new Map(agentItems().map(a => [a.id, a])));
  const baselineAgent = createMemo(() => agentById().get('baseline')?.agent);

  // A bot opens by its D17 code (B-FLT-1), its id or its unique name; a code the list does not hold resolves on the server.
  const botFromList = createMemo(() => {
    const id = section() === 'bots' && props.id && props.id !== 'new' ? props.id : null;
    if (!id) return null;
    const low = id.toLowerCase();
    return botItems().find(b => b.code?.toLowerCase() === low || b.id === id || b.name === id) ?? null;
  });
  const resolvedCode = useOp('entity_resolve', () => {
    const id = section() === 'bots' ? props.id : undefined;
    return id && /^B-/i.test(id) && botsOp.data && !botFromList() ? { code: id } : null;
  });
  // A raw segment is tried as an id only once the list is read and does not hold it as a code, id or name.
  const botId = createMemo(() => botFromList()?.id ?? resolvedCode.data?.id
    ?? (props.id && botsOp.data && !/^B-/i.test(props.id) ? props.id : null));
  const chosenBot = useOp('bot_get', () => (section() === 'bots' && props.id !== 'new' && botId() ? { botId: botId()! } : null));

  const selectedTreeId = createMemo(() => {
    const id = props.id;
    switch (section()) {
      case 'methods': return id ? `catalogue:${id}` : null;
      case 'pipelines': return id ? `pipeline:${id}` : null;
      case 'agents': return id ? `agent:${id}` : null;
      case 'skills': return id ? `skill:${id}` : null;
      case 'bots': return botId() ? `bot:${botId()}` : null;
      case 'changes': return CHANGES_ID;
      default: return null;
    }
  });

  const treeQuery = createMemo(() => q().replace(/\b(?:agent|host|project):(\S+)/gi, '$1'));

  const sortDefs = (items: readonly DefinitionListItem[]) =>
    items.toSorted((a, b) => Number(b.state === 'repository-changed') - Number(a.state === 'repository-changed')
      || Number(!!b.draft) - Number(!!a.draft) || b.last30.runs - a.last30.runs || a.name.localeCompare(b.name));

  const driftCount = createMemo(() => {
    const c = changesOp.data;
    if (!c) return 0;
    return c.repositoryChanged.length + c.storeOnly.length + c.ahead.length
      + c.versions.hosts.filter(h => h.behind).length + c.harnessDrift.filter(h => h.behind).length;
  });

  const libraryTreeNodes = createMemo<CatalogueTreeNode[]>(() => {
    const byId = agentById(), base = baselineAgent();
    const defLeaf = (d: DefinitionListItem, prefix: string, route: string, own: StateOf<'definition'>, right?: JSX.Element, extra: Partial<CatalogueTreeNode> = {}): CatalogueTreeNode => ({
      id: `${prefix}:${d.id}`,
      label: d.name,
      sub: d.state === 'store-only' ? 'store only' : d.name !== d.id ? d.id : undefined,
      state: { family: 'definition', state: leafState(d, own) },
      right,
      pinned: pins.has(`${prefix}:${d.id}`),
      href: `/library/${route}/${encodeURIComponent(d.id)}`,
      searchText: [d.id, d.name, d.scope, `state:${d.state}`].filter(Boolean).join(' '),
      ...extra,
    });

    const methodLeaves = sortDefs(catalogueItems()).map(d => defLeaf(d, 'catalogue', 'methods', 'ready'));
    const pipelineLeaves = sortDefs(pipelineItems()).map(d =>
      defLeaf(d, 'pipeline', 'pipelines', d.last30.runs > 0 ? 'ready' : 'never', runsRight(d.last30.runs)));

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
          const parent = d.agent?.variantOf ? byId.get(d.agent.variantOf)?.agent : undefined;
          const baseHarness = baseAgentDefaults(parent, base, d.agent?.harness).harness;
          const showHarness = !!(d.agent && d.agent.harness !== baseHarness && harnesses[d.agent.harness]);
          const right = (showHarness || d.last30.runs > 0) ? (
            <>
              {showHarness && <HarnessMark h={d.agent!.harness as Harness} />}
              {runsRight(d.last30.runs)}
            </>
          ) : undefined;
          return defLeaf(d, 'agent', 'agents', compatibilityState(d.compatibility), right, {
            sub: d.state === 'store-only' ? 'store only' : agentDeltaSub(d, byId, base),
            searchText: agentSearchText(d) + ` state:${d.state}`,
            problem: d.compatibility === 'failed',
          });
        }),
      });
    }

    const botLeaves: CatalogueTreeNode[] = botItems()
      .toSorted((a, b) => Number(!!b.activeRunId) - Number(!!a.activeRunId) || a.name.localeCompare(b.name))
      .map(b => {
        const nodeId = `bot:${b.id}`;
        const at = b.lastRunAt ? ms(b.lastRunAt) : null;
        return {
          id: nodeId,
          label: b.name,
          sub: b.code ?? undefined,
          state: { family: 'life', state: botLife(b) },
          lead: <Avatar letter={b.name} tone="success" size="sm" picture={botPicture(b)} state={botAvatarState(b)} />,
          right: at ? <Ago t={at} /> : undefined,
          pinned: pins.has(nodeId),
          href: botPath(b),
          searchText: [b.code, b.name, b.purpose, projectNames().get(b.projectId), `project:${b.projectId}`].filter(Boolean).join(' '),
          problem: botLife(b) === 'failed',
        };
      });

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
          href: `/library/skills/${encodeURIComponent(s.id)}`,
          searchText: [s.id, s.name, s.description, ...s.agents.map(a => `agent:${a} ${a}`)].filter(Boolean).join(' '),
        };
      });

    return [
      { id: ROOT_METHODS_ID, label: 'Methods', icon: 'rows-3', defaultCollapsed: false, children: methodLeaves },
      { id: ROOT_PIPELINES_ID, label: 'Pipelines', icon: 'network', defaultCollapsed: false, children: pipelineLeaves },
      { id: ROOT_AGENTS_ID, label: 'Agents', icon: 'bot', defaultCollapsed: false, children: agentFolders },
      { id: ROOT_BOTS_ID, label: 'Bots', icon: 'bot', defaultCollapsed: false, children: botLeaves },
      { id: ROOT_SKILLS_ID, label: 'Skills', icon: 'file-text', defaultCollapsed: true, children: skillLeaves },
      // Changes is one page, not a folder: its drift count is the root's counter (OR-29, hidden at zero).
      {
        id: CHANGES_ID, label: 'Changes', icon: 'git-pull-request', href: '/library/changes', searchText: 'changes drift repository store hosts',
        right: driftCount() > 0 ? <span class="badge t-attention" data-tip="Definitions, hosts and harness pins out of line">{driftCount()}</span> : undefined,
      },
    ];
  });

  const selectedIsLabVariant = createMemo(() => {
    if (section() !== 'agents' || !props.id) return false;
    const a = agentById().get(props.id);
    return !!a && agentRoleGroup(a) === 'lab-variants';
  });

  // The defaults never depend on the route, so a stored flip keeps its meaning; the open item's folders show open.
  const treeClosed = createMemo(() => closedGroups([ROOT_SKILLS_ID, 'group:lab-variants'], treeFlipped()));
  const collapsedMap = createMemo<Record<string, boolean>>(() => {
    const closed = treeClosed();
    const map: Record<string, boolean> = { __pinned__: closed.has('__pinned__') };
    for (const id of [ROOT_METHODS_ID, ROOT_PIPELINES_ID, ROOT_AGENTS_ID, ROOT_BOTS_ID, ROOT_SKILLS_ID]) map[id] = closed.has(id);
    for (const g of ROLE_GROUPS) map[g.id] = closed.has(g.id);
    if (section() === 'skills' && props.id) map[ROOT_SKILLS_ID] = false;
    if (selectedIsLabVariant()) { map[ROOT_AGENTS_ID] = false; map['group:lab-variants'] = false; }
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
      } else if (n.id !== CHANGES_ID && (!tq.trim() || matchSearch(tq, [n.label, n.id, n.sub, n.searchText].filter(Boolean).join(' '), n.state ? { state: n.state.state } : {}))) {
        total++;
      }
    }
    return total;
  };
  const totalCount = createMemo(() => catalogueItems().length + pipelineItems().length + agentItems().length + botItems().length + skillItems().length);
  const shownCount = createMemo(() => treeQuery().trim() ? countMatchingLeaves(libraryTreeNodes()) : totalCount());

  const lists = [agentsOp, pipelinesOp, catalogueOp, skillsOp, botsOp] as const;
  const initialLoading = createMemo(() => lists.every(o => !o.data && !o.error));
  const loadError = createMemo(() => lists.map(o => (o.error && !o.data ? o.error : null)).find(Boolean) ?? null);
  const reloadAll = () => { void Promise.all([...lists.map(o => o.reload()), changesOp.reload()]); };

  const listPane = (
    <>
      <ShellStatus><OfflineMarker state={agentsOp.state} now={Date.now()} /></ShellStatus>
      <ShellActions>
        <Button label="New bot" icon="plus" iconOnly onClick={() => go('/library/bots/new')} />
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
              if (Array.isArray(node.children)) toggleTreeFolder(node.id);
              else if (node.href) go(node.href);
            }}
            onPin={node => pins.toggle(node.id)}
            onRowMenu={(node, at) => {
              if (!Array.isArray(node.children) && node.id !== CHANGES_ID) setMenu({ node, at });
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
              { label: 'Copy id', icon: actions.copy, onClick: () => copyText(m().node.id.startsWith('bot:') ? (m().node.sub ?? rawNodeId(m().node.id)) : rawNodeId(m().node.id)) },
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
        setNotice({ severity: 'warning', title: `Preview: ${plural(r.items.filter(i => i.status === 'would-apply').length, 'definition')} take the repository version`, facts: r.items.map(i => `${i.kind}/${i.id}: ${i.status}`).join(' · ') });
      } else {
        setPreviewRepoKeys(null);
        setNotice({ severity: 'warning', title: `${plural(r.applied, 'definition')} took the repository version`, facts: r.items.map(i => `${i.kind}/${i.id}: ${i.status}`).join(' · ') });
        await Promise.all([agentsOp.reload(), pipelinesOp.reload(), catalogueOp.reload(), changesOp.reload()]);
      }
    } catch (e) {
      setNotice({ severity: 'failure', title: 'Repository sync failed', facts: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  const takeable = createMemo(() => (changesOp.data?.repositoryChanged ?? []).filter(d => d.state === 'repository-changed'));
  const notInRepo = createMemo(() => changesOp.data ? [...changesOp.data.storeOnly, ...changesOp.data.ahead] : []);
  const kindWord = (xs: readonly { kind: string }[]) => {
    const kinds = new Set(xs.map(x => x.kind));
    return kinds.size === 1 ? (kinds.has('agent') ? 'agent' : kinds.has('pipeline') ? 'pipeline' : 'definition') : 'definition';
  };
  const namesFact = (xs: readonly { name: string }[]) =>
    xs.length ? xs[0].name + (xs.length > 1 ? ` and ${xs.length - 1} more` : '') : '';

  const agentsActive = createMemo(() => agentItems().filter(a => a.last30.runs > 0).length);
  const pipelinesRan = createMemo(() => pipelineItems().filter(p => p.last30.runs > 0).length);
  const botsAwake = createMemo(() => botItems().filter(b => !b.paused).length);

  const idleSummary = (
    <article class="detail stack" data-ui="detail" aria-label="Library summary">
      <Show when={notice()}>{n => (
        <ProblemRow
          severity={n().severity}
          title={n().title}
          facts={n().facts}
          actions={previewRepoKeys()
            ? [
                { label: 'Take repository versions', icon: 'download', primary: true, onClick: () => void runTakeRepo(previewRepoKeys()!, false) },
                { label: 'Dismiss', onClick: () => { setPreviewRepoKeys(null); setNotice(null); } },
              ]
            : [{ label: 'Dismiss', onClick: () => setNotice(null) }]}
        />
      )}</Show>

      <Show when={changesOp.error || takeable().length > 0 || notInRepo().length > 0}>
        <DetailSection title="Needs a look">
          <Show when={changesOp.error}>{err => (
            <ProblemRow severity="failure" title="Changes could not be read" facts={err()}
              actions={[{ label: 'Retry', icon: actions.retry, primary: true, onClick: () => void changesOp.reload() }]} />
          )}</Show>
          <Show when={takeable().length > 0}>
            <ProblemRow
              severity="warning"
              title={`${plural(takeable().length, kindWord(takeable()))} wait for a repository version`}
              facts={namesFact(takeable())}
              actions={[
                { label: 'Preview and take all', icon: 'download', primary: true, onClick: busy() ? undefined : () => void runTakeRepo(takeable().map(d => ({ kind: d.kind, id: d.id })), true) },
                { label: 'Open Changes', icon: 'git-pull-request', onClick: () => go('/library/changes') },
              ]}
            />
          </Show>
          <Show when={notInRepo().length > 0}>
            <ProblemRow
              severity="warning"
              title={`${plural(notInRepo().length, kindWord(notInRepo()))} made in Fleet ${notInRepo().length === 1 ? 'is' : 'are'} not in the repository`}
              facts={namesFact(notInRepo())}
              actions={[{ label: 'Save to repository', icon: 'git-merge', onClick: () => go('/library/changes') }]}
            />
          </Show>
        </DetailSection>
      </Show>

      <Show when={recentRecs.error || (recentRecs.data?.changes.length ?? 0) > 0}>
        <DetailSection title={`Recommendations this week · ${recentRecs.data?.changes.length ?? 0}`}>
          <Show when={recentRecs.error}>{err => (
            <ProblemRow severity="failure" title="Recommendations could not be read" facts={err()}
              actions={[{ label: 'Retry', icon: actions.retry, primary: true, onClick: () => void recentRecs.reload() }]} />
          )}</Show>
          <ul class="live-list" aria-label="Recommendations this week">
            <For each={recentRecs.data?.changes ?? []}>{c => {
              const name = c.methodName ?? c.methodId;
              const exp = c.sourceExperiment;
              return exp
                ? <LiveRow kind="event" at={c.selectedAt} state="done" text={`${name} promoted from ${exp}`} onOpen={() => go('/lab/' + encodeURIComponent(exp))} />
                : <LiveRow kind="event" at={c.selectedAt} state="done" text={`${name} set for ${projectNames().get(c.projectId) ?? c.projectId}`}
                    onOpen={() => go('/projects/' + encodeURIComponent(c.projectId))} />;
            }}</For>
          </ul>
        </DetailSection>
      </Show>

      <div class="ftiles">
        <FigureTile label="Agents active in 30 days" value={{ value: agentsActive(), basis: 'measured' }} of={agentItems().length}
          trend={[]} trendLabel="Agents active in 30 days" />
        <FigureTile label="Pipelines that ran" value={{ value: pipelinesRan(), basis: 'measured' }} of={pipelineItems().length}
          trend={[]} trendLabel="Pipelines that ran in 30 days" />
        <FigureTile label="Bots awake" value={{ value: botsAwake(), basis: 'measured' }} of={botItems().length}
          trend={[]} trendLabel="Bots awake" />
      </div>
    </article>
  );

  const close = () => go('/library');
  const detail = createMemo(() => {
    const sec = section(), id = props.id;
    if (sec === 'changes') return 'changes';
    if (!id) return null;
    return sec;
  });

  return (
    <ListDetail
      list={listPane}
      detailKey={detail() ? `${section()}:${props.id ?? ''}` : undefined}
      idle={idleSummary}
      detail={detail() ? (
        <Switch>
          <Match when={detail() === 'changes'}><Changes onClose={close} /></Match>
          <Match when={detail() === 'skills'}>
            <Show when={props.id} keyed>{sid => (
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
                onClose={close}
              />
            )}</Show>
          </Match>
          <Match when={detail() === 'bots' && props.id === 'new'}>
            <BotCreateForm onClose={close} onSaved={b => go(botPath(b))} projects={projectNodes()} />
          </Match>
          <Match when={detail() === 'bots'}>
            <Switch fallback={<Skeleton rows={4} />}>
              <Match when={chosenBot.data}>{d => (
                <Show when={d().bot.id} keyed>{_id => <BotDetail bot={chosenBot.data!.bot} onClose={close} onReload={() => chosenBot.reload()} projectNames={projectNames()} projects={projectNodes()} />}</Show>
              )}</Match>
              <Match when={chosenBot.error ?? resolvedCode.error}>{err => <ErrorBanner text={err()} onRetry={() => { void resolvedCode.reload(); void chosenBot.reload(); }} />}</Match>
            </Switch>
          </Match>
          <Match when={detail() === 'methods'}>
            <Show when={props.id} keyed>{did => <DefinitionDetail kind="catalogue" id={did} agentById={agentById()} onClose={close} />}</Show>
          </Match>
          <Match when={detail() === 'pipelines'}>
            <Show when={props.id} keyed>{did => <DefinitionDetail kind="pipeline" id={did} agentById={agentById()} onClose={close} />}</Show>
          </Match>
          <Match when={detail() === 'agents'}>
            <Show when={props.id} keyed>{did => <DefinitionDetail kind="agent" id={did} agentById={agentById()} onClose={close} />}</Show>
          </Match>
        </Switch>
      ) : undefined}
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
