// Lab (docs/designs/fleet-lab.md "/lab screen", docs/design-system/pages/laboratory.md): 2-root CatalogueTree
// (Use cases + Runs) in the control column; Waiting for you, Totals, and SuiteLeaderboard on /lab; UseCaseDetail,
// SuiteDetail, LabCreate, and Experiment in the detail slot via /lab/:id prefixes.
import { Fragment, type ComponentChildren } from 'preact';
import { useEffect, useMemo, useState } from 'preact/hooks';
import { useLocation, useSearch } from 'wouter-preact';
import {
  StateBadge, Button, CopyButton, Disclosure, EntityCode, Num, Totals, DetailSection, KvRow, DecisionBlock, ProblemRow,
  RowCheck, AgeChip, VirtualList, Empty, OfflineMarker, Icon, ListDetail, DataTable, SortColHead, Choice, TextField,
  SearchField, CatalogueTree, ContextMenu, ErrorBanner, Skeleton, ROW2, percent, SettingField, StepList, msMeasured,
  agentLabel, type StepItem, type StateOf, type Basis, type CatalogueTreeNode, type MenuItem, type TotalItem,
  type IconName, ShellStatus, ShellActions, actions,
} from '../../../packages/ui/src/index.ts';
import { op, writeOnce, useAlive, type OpName, type Ops } from '../api.ts';
import { useOp } from '../store.ts';
import { closedGroups, flipGroup } from '../lists.ts';
import { listGroups, setListGroups, usePins } from '../prefs.ts';
import { LabCreate } from './lab-create.tsx';
import {
  ARCHIVED_COLLECTION_ID, USE_CASE_COLLECTION_PREFIX, buildUseCaseTree, collectionIconOf, collectionTitleOf,
  groupUseCases, isLabEligibleUseCase, matchingExamplesForUseCase, resolveSuiteKey, type RegistryUseCase, type SuiteKey,
} from './use-cases.ts';
import type {
  LabExampleEntry, LabExperimentRecord, LabLeaderboardEntry, LabRating, LabScoreSummary, LabVariant, ProjectAssessmentRef,
} from '../../../contracts/generated/ops.ts';

const ms = (t: string | number | null | undefined) => typeof t === 'number' ? t : Date.parse(t ?? '');
const message = (x: unknown) => x instanceof Error ? x.message : String(x);
const fixed = (x: number | null) => x === null ? '—' : x.toFixed(2);
const decodeSafe = (s: string): string => { try { return decodeURIComponent(s); } catch { return s; } };

const TERMINAL_STATES = new Set(['done', 'stopped', 'succeeded', 'failed', 'cancelled']);

/** Extract a concise human-readable problem string from a trial or strategy problem field. */
const problemText = (p: unknown): string | undefined => {
  if (p == null) return undefined;
  if (typeof p === 'string') return p.trim() || undefined;
  if (typeof p === 'object') {
    const r = p as { code?: unknown; message?: unknown; reason?: unknown; error?: unknown };
    const parts = [r.code, r.message ?? r.reason ?? r.error].filter((x): x is string => typeof x === 'string' && x.trim().length > 0);
    if (parts.length > 0) return parts.join(': ');
    try { return JSON.stringify(p); } catch { return String(p); }
  }
  return String(p);
};

/** A typed score's value: the answer given and the example's label. */
const typedValue = (v: unknown): { answer?: unknown; label?: unknown } => v && typeof v === 'object' ? v as { answer?: unknown; label?: unknown } : {};

// typedIcon: a typed score at a glance: right, wrong, escalated (middle band) or not scorable.
const typedIcon = (value: unknown) => { const v = typedValue(value); return v.answer === 'unscored' ? 'circle-alert' : v.answer === 'abstain' ? 'circle-pause' : v.answer === v.label ? 'circle-check' : 'circle-x'; };

const costBasisOf = (b: string | null | undefined): Basis =>
  b === 'measured' || b === 'estimated' || b === 'subscription' || b === 'unavailable' ? b : 'unavailable';

type LabRoute =
  | { kind: 'idle' }
  | { kind: 'new' }
  | { kind: 'use-case'; useCaseId: string }
  | { kind: 'suite'; rawSuite: string; suiteKey: SuiteKey | null }
  | { kind: 'experiment'; experimentId: string };

function parseLabRoute(id?: string): LabRoute {
  if (!id) return { kind: 'idle' };
  const decoded = decodeSafe(id);
  if (decoded === 'new') return { kind: 'new' };
  if (decoded.startsWith('use-case:')) return { kind: 'use-case', useCaseId: decoded.slice('use-case:'.length) };
  if (decoded.startsWith('suite:')) {
    const rawSuite = decoded.slice('suite:'.length);
    return { kind: 'suite', rawSuite, suiteKey: resolveSuiteKey(rawSuite) };
  }
  return { kind: 'experiment', experimentId: decoded };
}

const ROOT_USE_CASES_ID = 'root:use-cases';
const ROOT_RUNS_ID = 'root:runs';

const RUN_FOLDERS = [
  { id: 'run:active', label: 'Active', icon: 'activity' as const, match: (e: LabExperimentRecord) => e.state === 'running' || e.state === 'paused', defaultCollapsed: false },
  { id: 'run:drafts', label: 'Drafts', icon: 'circle-dashed' as const, match: (e: LabExperimentRecord) => e.state === 'draft', defaultCollapsed: false },
  { id: 'run:completed', label: 'Completed', icon: 'circle-check' as const, match: (e: LabExperimentRecord) => e.state === 'done', defaultCollapsed: false },
  { id: 'run:stopped', label: 'Stopped', icon: 'circle-slash' as const, match: (e: LabExperimentRecord) => e.state === 'stopped', defaultCollapsed: true },
] as const;

export function Lab({ id }: { id?: string }) {
  const [, go] = useLocation();
  const search = useSearch();
  const route = useMemo(() => parseLabRoute(id), [id]);

  useEffect(() => {
    if (route.kind !== 'idle') return;
    const legacyUseCase = new URLSearchParams(search).get('useCase')?.trim();
    if (legacyUseCase) go(`/lab/use-case:${encodeURIComponent(legacyUseCase)}`, { replace: true });
  }, [route.kind, search]);

  const [ctlQuery, setCtlQuery] = useState('');
  const [treeMenu, setTreeMenu] = useState<{ kind: 'use-case' | 'run'; node: CatalogueTreeNode; at: { x: number; y: number } } | null>(null);
  const [bestScoresByUseCase, setBestScoresByUseCase] = useState<Record<string, number>>({});

  const ucPins = usePins('lab:use-cases');
  const runPins = usePins('lab:runs');

  const experiments = useOp('lab_experiments_list', {});
  const registry = useOp('lab_registry', {});
  const ratings = useOp('lab_ratings_list', { state: 'open' });

  const suiteFolderId = route.kind === 'suite' && route.suiteKey ? `${USE_CASE_COLLECTION_PREFIX}${route.suiteKey}` : null;
  const selectedCollectionTitle = collectionTitleOf(suiteFolderId);

  const leaderboardReq = useMemo<Ops['lab_leaderboard']['request'] | null>(() => {
    if (route.kind === 'use-case') return { useCaseId: route.useCaseId };
    if (route.kind === 'suite') {
      if (route.suiteKey === 'archived' || !selectedCollectionTitle) return null;
      return { collection: selectedCollectionTitle };
    }
    if (route.kind === 'idle') return { days: 30 };
    return null;
  }, [route, selectedCollectionTitle]);
  const leaderboardWindowLabel = leaderboardReq?.days === 30 ? 'Last 30 days' : 'Last 90 days';

  const leaderboard = useOp('lab_leaderboard', leaderboardReq);
  const needsExamples = route.kind === 'idle' || route.kind === 'use-case' || route.kind === 'suite';
  const approvedExamples = useOp('lab_examples_list', needsExamples ? { state: 'approved' } : null);

  useEffect(() => {
    if (route.kind !== 'idle' || !leaderboard.data) return;
    const next: Record<string, number> = {};
    for (const entry of leaderboard.data.entries) {
      if (entry.scoreMean == null) continue;
      const cur = next[entry.useCaseId];
      if (cur === undefined || entry.scoreMean > cur) next[entry.useCaseId] = entry.scoreMean;
    }
    setBestScoresByUseCase(next);
  }, [route.kind, leaderboard.data]);

  const now = Date.now();
  const rawExperiments = experiments.data?.experiments ?? [];
  const rawUseCases: RegistryUseCase[] = registry.data?.useCases ?? [];
  const rawRatings = ratings.data?.ratings ?? [];
  const rawExamples = approvedExamples.data?.examples ?? [];

  const useCaseById = useMemo(() => new Map(rawUseCases.map(u => [u.id, u])), [rawUseCases]);

  const { nodes: useCaseTreeNodes } = useMemo(
    () => buildUseCaseTree(rawUseCases, {
      pinnedIds: ucPins.ids,
      hrefOf: u => `/lab/use-case:${encodeURIComponent(u.id)}`,
      rightOf: u => {
        const best = bestScoresByUseCase[u.id];
        return best !== undefined ? <span class="mono" data-tip="Best observed score">{best.toFixed(2)}</span> : undefined;
      },
    }),
    [rawUseCases, ucPins.ids, bestScoresByUseCase],
  );

  const sortedExperiments = useMemo(
    () => rawExperiments.toSorted((a, b) => ms(b.launchedAt ?? b.createdAt) - ms(a.launchedAt ?? a.createdAt)),
    [rawExperiments],
  );

  const selectedExperimentId = route.kind === 'experiment' ? route.experimentId : null;

  const runTreeNodes = useMemo<CatalogueTreeNode[]>(() => {
    const folders: CatalogueTreeNode[] = [];
    for (const f of RUN_FOLDERS) {
      const items = sortedExperiments.filter(f.match);
      if (!items.length) continue;
      const containsSelected = !!selectedExperimentId && items.some(e => e.id === selectedExperimentId);
      folders.push({
        id: f.id,
        label: f.label,
        icon: f.icon,
        defaultCollapsed: f.defaultCollapsed && !containsSelected,
        children: items.map(e => ({
          id: e.id,
          label: e.labGroup || e.goal || e.pipelineId,
          sub: subtitle(e),
          state: { family: 'experiment', state: e.state, reason: e.stopReason ?? undefined },
          href: `/lab/${encodeURIComponent(e.id)}`,
          pinned: runPins.has(e.id),
          problem: undecided(e),
          right: <>{undecided(e) && <Icon name="bell" label="Undecided" />}<AgeChip t={ms(e.createdAt)} /></>,
          searchText: [e.id, e.labGroup, e.goal, e.pipelineId, e.kind, e.state, ...e.variants.map(v => v.label)].filter(Boolean).join(' '),
        })),
      });
    }
    return folders;
  }, [sortedExperiments, selectedExperimentId, runPins.ids]);

  const rootNodes = useMemo<CatalogueTreeNode[]>(() => [
    {
      id: ROOT_USE_CASES_ID,
      label: 'Use cases',
      icon: 'chart-no-axes-column',
      defaultCollapsed: false,
      children: useCaseTreeNodes,
    },
    {
      id: ROOT_RUNS_ID,
      label: 'Runs',
      icon: 'play',
      defaultCollapsed: false,
      children: runTreeNodes,
    },
  ], [useCaseTreeNodes, runTreeNodes]);

  const defaultClosed = useMemo(() => {
    const out: string[] = [];
    const selectedUseCaseArchived = route.kind === 'use-case'
      ? (useCaseById.get(route.useCaseId)?.status === 'obsolete' || useCaseById.get(route.useCaseId)?.status === 'duplicate')
      : route.kind === 'suite' && route.suiteKey === 'archived';
    if (!selectedUseCaseArchived) out.push(ARCHIVED_COLLECTION_ID);
    const stoppedOpen = !!selectedExperimentId && sortedExperiments.some(e => e.id === selectedExperimentId && RUN_FOLDERS[3].match(e));
    if (!stoppedOpen) out.push('run:stopped');
    return out;
  }, [route, useCaseById, selectedExperimentId, sortedExperiments]);

  const treeFlipped = listGroups('lab:tree');
  const treeClosed = closedGroups(defaultClosed, treeFlipped);
  const treeCollapsed = useMemo<Record<string, boolean>>(() => {
    const map: Record<string, boolean> = {
      __pinned__: treeClosed.has('__pinned__'),
      [ROOT_USE_CASES_ID]: treeClosed.has(ROOT_USE_CASES_ID),
      [ROOT_RUNS_ID]: treeClosed.has(ROOT_RUNS_ID),
    };
    for (const n of useCaseTreeNodes) map[n.id] = treeClosed.has(n.id);
    for (const n of runTreeNodes) map[n.id] = treeClosed.has(n.id);
    return map;
  }, [treeClosed, useCaseTreeNodes, runTreeNodes]);

  const toggleTreeFolder = (folderId: string) => {
    setListGroups('lab:tree', flipGroup(treeFlipped, folderId));
  };

  const handleTreeSelect = (node: CatalogueTreeNode) => {
    if (node.id === '__pinned__' || node.id === ROOT_USE_CASES_ID || node.id === ROOT_RUNS_ID || node.id.startsWith('run:')) {
      toggleTreeFolder(node.id);
      return;
    }
    if (node.id.startsWith(USE_CASE_COLLECTION_PREFIX)) {
      const key = node.id.slice(USE_CASE_COLLECTION_PREFIX.length);
      go(`/lab/suite:${encodeURIComponent(key)}`);
      return;
    }
    if (useCaseById.has(node.id)) {
      go(`/lab/use-case:${encodeURIComponent(node.id)}`);
      return;
    }
    go(`/lab/${encodeURIComponent(node.id)}`);
  };

  const selectedTreeId = route.kind === 'use-case'
    ? route.useCaseId
    : route.kind === 'suite' && route.suiteKey
      ? `${USE_CASE_COLLECTION_PREFIX}${route.suiteKey}`
      : route.kind === 'experiment'
        ? route.experimentId
        : null;

  const menuItems: MenuItem[] = !treeMenu ? [] : treeMenu.kind === 'use-case'
    ? (() => {
        const u = useCaseById.get(treeMenu.node.id);
        const pinned = ucPins.has(treeMenu.node.id);
        const items: MenuItem[] = [
          { label: 'Open', icon: actions.open, onClick: () => go(`/lab/use-case:${encodeURIComponent(treeMenu.node.id)}`) },
          { label: pinned ? 'Unpin' : 'Pin', icon: pinned ? actions.unpin : actions.pin, onClick: () => ucPins.toggle(treeMenu.node.id) },
        ];
        if (u && isLabEligibleUseCase(u)) {
          items.push({ label: 'Run in Lab', icon: 'play', onClick: () => go(`/lab/new?useCase=${encodeURIComponent(u.id)}`) });
        }
        return items;
      })()
    : (() => {
        const pinned = runPins.has(treeMenu.node.id);
        return [
          { label: 'Open', icon: actions.open, onClick: () => go(`/lab/${encodeURIComponent(treeMenu.node.id)}`) },
          { label: pinned ? 'Unpin' : 'Pin', icon: pinned ? actions.unpin : actions.pin, onClick: () => runPins.toggle(treeMenu.node.id) },
        ];
      })();

  const initialLoading = !experiments.data && !registry.data && !experiments.error && !registry.error;
  const loadError = (experiments.error && !experiments.data ? experiments.error : null)
    ?? (registry.error && !registry.data ? registry.error : null);

  const openNewExperiment = () => {
    if (route.kind !== 'new') {
      go('/lab/new');
      return;
    }
    const detail = document.querySelector<HTMLElement>('[data-ui=detail][aria-label="Create experiment"]');
    detail?.focus();
  };

  // Control column: ShellActions + search filter + single 2-root CatalogueTree (never hijacked, OR-23, OR-24, OR-43).
  const list = <>
    <ShellStatus><OfflineMarker state={experiments.state} now={now} /></ShellStatus>
    <ShellActions>
      <Button label="New experiment" icon="plus" iconOnly kind="primary" pressed={route.kind === 'new'} onClick={openNewExperiment} />
    </ShellActions>
    <div class="fbar">
      <div class="fbar-line">
        <SearchField label="Search Lab" value={ctlQuery} onInput={setCtlQuery} onSearch={() => {}} />
      </div>
    </div>
    <section class="panel">
      {initialLoading ? (
        <Skeleton rows={8} />
      ) : loadError ? (
        <ErrorBanner text={loadError} onRetry={() => { experiments.reload(); registry.reload(); }} />
      ) : (
        <CatalogueTree
          fill
          label="Lab catalogue"
          nodes={rootNodes}
          selectedId={selectedTreeId}
          query={ctlQuery}
          collapsed={treeCollapsed}
          onToggle={toggleTreeFolder}
          onPin={node => {
            if (useCaseById.has(node.id)) ucPins.toggle(node.id);
            else runPins.toggle(node.id);
          }}
          onSelect={handleTreeSelect}
          onRowMenu={(node, at) => {
            if (!node.children) setTreeMenu({ kind: useCaseById.has(node.id) ? 'use-case' : 'run', node, at });
          }}
          emptyText="No matching use cases or runs."
        />
      )}
    </section>
    {treeMenu && <ContextMenu at={treeMenu.at} label={treeMenu.node.label} items={menuItems} onClose={() => setTreeMenu(null)} />}
  </>;

  const approvals = rawRatings.filter(r => r.exampleArtifactId);
  const openRatings = rawRatings.filter(r => r.experimentId);
  const groupedUseCases = useMemo(() => groupUseCases(rawUseCases), [rawUseCases]);
  const selectedCollection = route.kind === 'suite' && route.suiteKey && route.suiteKey !== 'archived'
    ? groupedUseCases.collections.find(c => c.key === route.suiteKey) ?? null
    : null;
  const activeUseCaseCount = route.kind === 'suite' && route.suiteKey === 'archived'
    ? 0
    : selectedCollection
      ? selectedCollection.items.length
      : groupedUseCases.active.length;
  const waitingExperiments = rawExperiments.filter(undecided);
  const waitingSeriesCount = new Set(waitingExperiments.map(e => e.labGroup || e.pipelineId)).size;
  const summaryTotals: TotalItem[] = [
    {
      label: 'Active use cases',
      m: registry.data ? { value: activeUseCaseCount, basis: 'measured' } : { value: null, basis: 'unavailable', reason: 'loading' },
      kind: 'count',
      note: selectedCollection
        ? `${groupedUseCases.active.length} total`
        : groupedUseCases.archived.length > 0
          ? `${groupedUseCases.archived.length} archived`
          : undefined,
    },
    {
      label: 'Approved examples',
      m: approvedExamples.data ? { value: rawExamples.length, basis: 'measured' } : { value: null, basis: 'unavailable', reason: 'loading' },
      kind: 'count',
    },
    {
      label: 'Leaderboard entries',
      m: leaderboard.data ? { value: (leaderboard.data.entries ?? []).length, basis: 'measured' } : { value: null, basis: 'unavailable', reason: 'loading' },
      kind: 'count',
      note: leaderboardWindowLabel,
    },
    {
      label: 'Undecided series',
      m: { value: waitingSeriesCount, basis: 'measured' },
      kind: 'count',
      note: waitingExperiments.length > 0 ? `${waitingExperiments.length} waiting` : undefined,
    },
  ];

  const waitingSection = (approvals.length > 0 || openRatings.length > 0) && (
    <section class="panel" aria-label="Waiting for you">
      <header class="panel-h"><Icon name="bell" /><h2>Waiting for you</h2></header>
      <div class="stack">
        {openRatings.length > 0 && (
          <KvRow
            icon="message-circle-question"
            label={`${openRatings.length} open ${openRatings.length === 1 ? 'rating' : 'ratings'}`}
            value={
              <span class="deps">
                {[...new Set(openRatings.map(r => r.experimentId).filter((x): x is string => !!x))].slice(0, 5).map(eid => (
                  <EntityCode key={eid} id={eid} kind="experiment" />
                ))}
              </span>
            }
          />
        )}
        {approvals.map(r => <Rating key={r.id} r={r} now={now} />)}
      </div>
    </section>
  );

  const idle = (
    <article class="detail" aria-label="Lab overview">
      {waitingSection}
      <Undecided experiments={rawExperiments} onOpen={x => go(`/lab/${encodeURIComponent(x)}`)} />
      <Totals items={summaryTotals} />
      <SuiteLeaderboard
        scopeLabel="All suites"
        windowLabel={leaderboardWindowLabel}
        entries={leaderboard.data?.entries ?? []}
        loading={!leaderboard.data && !leaderboard.error}
        error={leaderboard.error}
        onRetry={leaderboard.reload}
        showUseCase
        onOpenExperiment={x => go(`/lab/${encodeURIComponent(x)}`)}
        onOpenPipeline={p => go(`/pipelines/${encodeURIComponent(p)}`)}
        onSelectUseCase={u => go(`/lab/use-case:${encodeURIComponent(u)}`)}
      />
    </article>
  );

  let detail: ComponentChildren;
  let detailKey: string | undefined;

  if (route.kind === 'new') {
    detail = <LabCreate key={`new:${search}`} onClose={() => go('/lab')} />;
    detailKey = 'new';
  } else if (route.kind === 'use-case') {
    const selectedUseCase = useCaseById.get(route.useCaseId) ?? null;
    detailKey = `use-case:${route.useCaseId}`;
    detail = (
      <UseCaseDetail
        useCaseId={route.useCaseId}
        useCase={selectedUseCase}
        loading={!registry.data && !registry.error}
        error={registry.error}
        onRetry={registry.reload}
        examples={rawExamples}
        leaderboardEntries={leaderboard.data?.entries ?? []}
        leaderboardLoading={!leaderboard.data && !leaderboard.error}
        leaderboardError={leaderboard.error}
        onRetryLeaderboard={leaderboard.reload}
        windowLabel={leaderboardWindowLabel}
        onClose={() => go('/lab')}
        onRun={u => go(`/lab/new?useCase=${encodeURIComponent(u)}`)}
        onOpenExperiment={x => go(`/lab/${encodeURIComponent(x)}`)}
        onOpenPipeline={p => go(`/pipelines/${encodeURIComponent(p)}`)}
        onSelectUseCase={u => go(`/lab/use-case:${encodeURIComponent(u)}`)}
      />
    );
  } else if (route.kind === 'suite') {
    detailKey = `suite:${route.rawSuite}`;
    detail = (
      <SuiteDetail
        rawSuite={route.rawSuite}
        suiteKey={route.suiteKey}
        title={selectedCollectionTitle ?? route.rawSuite}
        icon={route.suiteKey ? collectionIconOf(route.suiteKey) : 'folder-open'}
        archivedUseCases={groupedUseCases.archived}
        experiments={rawExperiments}
        totals={summaryTotals}
        leaderboardEntries={leaderboard.data?.entries ?? []}
        leaderboardLoading={!!leaderboardReq && !leaderboard.data && !leaderboard.error}
        leaderboardError={leaderboard.error}
        onRetryLeaderboard={leaderboard.reload}
        windowLabel={leaderboardWindowLabel}
        onClose={() => go('/lab')}
        onOpenExperiment={x => go(`/lab/${encodeURIComponent(x)}`)}
        onOpenPipeline={p => go(`/pipelines/${encodeURIComponent(p)}`)}
        onSelectUseCase={u => go(`/lab/use-case:${encodeURIComponent(u)}`)}
      />
    );
  } else if (route.kind === 'experiment') {
    detailKey = route.experimentId;
    detail = <Experiment key={route.experimentId} id={route.experimentId} onClose={() => go('/lab')} />;
  }

  return <ListDetail list={list} detail={detail} detailKey={detailKey} idle={idle} />;
}

const title = (e: LabExperimentRecord) => `${e.pipelineId} · ${e.kind === 'ab' ? 'A/B' : e.kind} · ${e.variants.map(v => v.label).join(' vs ')}`;
const subtitle = (e: LabExperimentRecord) => `${e.kind === 'ab' ? 'A/B' : e.kind} · ${e.variants.length > 2 ? `${e.variants.length} variants` : e.variants.map(v => v.label).join(' vs ')}`;
/** A finished experiment nobody has decided yet (decision pending). */
const undecided = (e: LabExperimentRecord) => e.state === 'done' && e.decision === 'pending';

// What the Lab waits for, above the registry while no experiment is open (2026-11-20 decision): finished experiments
// without a decision, newest first.
function Undecided({ experiments, onOpen }: { experiments: readonly LabExperimentRecord[]; onOpen: (id: string) => void }) {
  const waiting = experiments.filter(undecided).sort((a, b) => ms(b.launchedAt ?? b.createdAt) - ms(a.launchedAt ?? a.createdAt));
  if (!waiting.length) return null;
  const series = new Set(waiting.map(e => e.labGroup || e.pipelineId)).size;
  return <>
    <ProblemRow severity="warning" title={`${waiting.length} finished ${waiting.length === 1 ? 'experiment waits' : 'experiments wait'} for a decision`}
      facts={`in ${series} series`} actions={[{ label: 'Open the newest', primary: true, onClick: () => onOpen(waiting[0].id) }]} />
    <DetailSection title="Newest undecided">{waiting.slice(0, 5).map(e => <KvRow key={e.id} icon="chart-no-axes-column"
      label={e.goal ?? title(e)}
      meta={<><a class="row-link" href={`/lab/${encodeURIComponent(e.id)}`} onClick={ev => { ev.preventDefault(); onOpen(e.id); }}>{e.labGroup || e.pipelineId}</a> · {subtitle(e)}</>}
      value={<EntityCode id={e.id} kind="experiment" />} />)}</DetailSection>
  </>;
}

function UseCaseDetail({
  useCaseId,
  useCase,
  loading,
  error,
  onRetry,
  examples,
  leaderboardEntries,
  leaderboardLoading,
  leaderboardError,
  onRetryLeaderboard,
  windowLabel,
  onClose,
  onRun,
  onOpenExperiment,
  onOpenPipeline,
  onSelectUseCase,
}: {
  useCaseId: string;
  useCase: RegistryUseCase | null;
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  examples: readonly LabExampleEntry[];
  leaderboardEntries: readonly LabLeaderboardEntry[];
  leaderboardLoading: boolean;
  leaderboardError: string | null;
  onRetryLeaderboard: () => void;
  windowLabel: string;
  onClose: () => void;
  onRun: (useCaseId: string) => void;
  onOpenExperiment: (id: string) => void;
  onOpenPipeline: (id: string) => void;
  onSelectUseCase: (id: string) => void;
}) {
  const eligible = useCase ? isLabEligibleUseCase(useCase) : false;
  return (
    <article class="detail" data-ui="detail" tabIndex={-1} aria-label={useCase ? useCase.goal : `Use case ${useCaseId}`}>
      <header class="detail-h">
        <Button label="Close" icon="x" iconOnly onClick={onClose} />
        {useCase && (
          <>
            <StateBadge
              family="source"
              state={(useCase.source_status || 'current') as StateOf<'source'>}
              reason={useCase.source ? `${useCase.source.repo}/${useCase.source.path}#${useCase.source.anchor}` : undefined}
            />
            <span class="id mono">{useCase.id}</span>
            <span class="grow" />
            {eligible && <Button label="Run in Lab" icon="play" kind="primary" onClick={() => onRun(useCase.id)} />}
          </>
        )}
      </header>
      {error && !useCase ? (
        <ErrorBanner text={error} onRetry={onRetry} />
      ) : loading ? (
        <Skeleton rows={6} />
      ) : !useCase ? (
        <Empty text={`Use case "${useCaseId}" not found.`} />
      ) : (
        <>
          <UseCaseCard useCase={useCase} examples={examples} />
          <SuiteLeaderboard
            scopeLabel={useCase.id}
            windowLabel={windowLabel}
            entries={leaderboardEntries}
            loading={leaderboardLoading}
            error={leaderboardError}
            onRetry={onRetryLeaderboard}
            showUseCase={false}
            onOpenExperiment={onOpenExperiment}
            onOpenPipeline={onOpenPipeline}
            onSelectUseCase={onSelectUseCase}
          />
        </>
      )}
    </article>
  );
}

function SuiteDetail({
  rawSuite,
  suiteKey,
  title: suiteTitle,
  icon,
  archivedUseCases,
  experiments,
  totals,
  leaderboardEntries,
  leaderboardLoading,
  leaderboardError,
  onRetryLeaderboard,
  windowLabel,
  onClose,
  onOpenExperiment,
  onOpenPipeline,
  onSelectUseCase,
}: {
  rawSuite: string;
  suiteKey: SuiteKey | null;
  title: string;
  icon: IconName;
  archivedUseCases: readonly RegistryUseCase[];
  experiments: readonly LabExperimentRecord[];
  totals: TotalItem[];
  leaderboardEntries: readonly LabLeaderboardEntry[];
  leaderboardLoading: boolean;
  leaderboardError: string | null;
  onRetryLeaderboard: () => void;
  windowLabel: string;
  onClose: () => void;
  onOpenExperiment: (id: string) => void;
  onOpenPipeline: (id: string) => void;
  onSelectUseCase: (id: string) => void;
}) {
  return (
    <article class="detail" data-ui="detail" tabIndex={-1} aria-label={suiteTitle}>
      <header class="detail-h">
        <Button label="Close" icon="x" iconOnly onClick={onClose} />
        <Icon name={icon} />
        <span class="grow" />
      </header>
      <h2 class="detail-title">{suiteTitle}</h2>
      {!suiteKey ? (
        <Empty text={`Suite "${rawSuite}" not found.`} />
      ) : suiteKey === 'archived' ? (
        <>
          <Totals items={totals} />
          <DetailSection title="Archived use cases">
            {archivedUseCases.length === 0 ? (
              <Empty text="No archived use cases." />
            ) : (
              archivedUseCases.map(u => (
                <KvRow
                  key={u.id}
                  icon="archive"
                  label={u.goal}
                  meta={<a class="row-link mono" href={`/lab/use-case:${encodeURIComponent(u.id)}`} onClick={ev => { ev.preventDefault(); onSelectUseCase(u.id); }}>{u.id}</a>}
                  value={<StateBadge family="source" state={(u.source_status || 'current') as StateOf<'source'>} compact />}
                />
              ))
            )}
          </DetailSection>
        </>
      ) : (
        <>
          <Undecided experiments={experiments} onOpen={onOpenExperiment} />
          <Totals items={totals} />
          <SuiteLeaderboard
            scopeLabel={suiteTitle}
            windowLabel={windowLabel}
            entries={leaderboardEntries}
            loading={leaderboardLoading}
            error={leaderboardError}
            onRetry={onRetryLeaderboard}
            showUseCase
            onOpenExperiment={onOpenExperiment}
            onOpenPipeline={onOpenPipeline}
            onSelectUseCase={onSelectUseCase}
          />
        </>
      )}
    </article>
  );
}

function UseCaseCard({
  useCase,
  examples,
}: {
  useCase: RegistryUseCase;
  examples: readonly LabExampleEntry[];
}) {
  const matched = matchingExamplesForUseCase(examples, useCase.id);
  const readiness = matched.length > 0
    ? `${matched.length} approved (${matched.slice(0, 4).map(e => e.name).join(', ')}${matched.length > 4 ? `, +${matched.length - 4}` : ''})`
    : `0 approved matching ${useCase.id} (maturity: ${useCase.maturity})`;
  return (
    <section class="panel" aria-label={`Use case ${useCase.id}`}>
      <div class="stack">
        <h2 class="detail-title">{useCase.goal}</h2>
        <DetailSection title="Specification & readiness">
          <KvRow icon="check-check" label="Status" value={useCase.status ?? 'current'} />
          <KvRow icon="sliders-horizontal" label="Lab fit" value={useCase.lab_fit} />
          <KvRow icon="chart-no-axes-column" label="Maturity" value={useCase.maturity} />
          <KvRow icon="rows-3" label="Approved examples" value={readiness} />
          {useCase.tags && useCase.tags.length > 0 && (
            <KvRow
              icon="layers-2"
              label="Tags"
              value={<span class="ep-presets">{useCase.tags.map(t => <span key={t} class="chip">{t}</span>)}</span>}
            />
          )}
          {useCase.expected_outcome && <KvRow icon="file-text" label="Expected outcome" value={useCase.expected_outcome} />}
          {useCase.source && (
            <KvRow
              icon="file-text"
              label="Source"
              value={<span class="mono">{useCase.source.repo}/{useCase.source.path}#{useCase.source.anchor}</span>}
            />
          )}
        </DetailSection>
      </div>
    </section>
  );
}

type LeaderboardSortCol = 'variant' | 'pipeline' | 'score' | 'runs' | 'wall' | 'cost';
const lbCollator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

function SuiteLeaderboard({
  scopeLabel,
  windowLabel,
  entries,
  loading,
  error,
  onRetry,
  showUseCase,
  onOpenExperiment,
  onOpenPipeline,
  onSelectUseCase,
}: {
  scopeLabel: string;
  windowLabel: string;
  entries: readonly LabLeaderboardEntry[];
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  showUseCase: boolean;
  onOpenExperiment: (id: string) => void;
  onOpenPipeline: (id: string) => void;
  onSelectUseCase: (id: string) => void;
}) {
  const [sortCol, setSortCol] = useState<LeaderboardSortCol | null>(null);
  const [sortDir, setSortDir] = useState<'ascending' | 'descending'>('descending');
  const [promotingKey, setPromotingKey] = useState<string | null>(null);
  const [promoted, setPromoted] = useState<{ key: string; reply: Ops['lab_promote']['reply'] } | null>(null);
  const [promoteError, setPromoteError] = useState<string | null>(null);
  const alive = useAlive();

  const toggleSort = (col: LeaderboardSortCol) => {
    if (sortCol === col) {
      setSortDir(d => (d === 'ascending' ? 'descending' : 'ascending'));
    } else {
      setSortCol(col);
      setSortDir(col === 'variant' || col === 'pipeline' || col === 'wall' || col === 'cost' ? 'ascending' : 'descending');
    }
  };

  const bestByGroup = useMemo(() => {
    const map = new Map<string, number>();
    for (const entry of entries) {
      if (entry.succeededTrials <= 0 || entry.scoreMean == null || entry.scoreMean <= 0) continue;
      const gk = `${entry.useCaseId}\0${entry.evaluator}`;
      const prev = map.get(gk);
      if (prev === undefined || entry.scoreMean > prev) map.set(gk, entry.scoreMean);
    }
    return map;
  }, [entries]);

  const sorted = useMemo(() => {
    if (!sortCol) return [...entries];
    const dir = sortDir === 'ascending' ? 1 : -1;
    const cmpNum = (a: number | null | undefined, b: number | null | undefined): number => {
      if (a == null && b == null) return 0;
      if (a == null) return 1;
      if (b == null) return -1;
      return dir * (a - b);
    };
    return entries.toSorted((a, b) => {
      switch (sortCol) {
        case 'variant': return dir * lbCollator.compare(a.variantLabel, b.variantLabel);
        case 'pipeline': return dir * lbCollator.compare(a.pipelineId, b.pipelineId);
        case 'score': return cmpNum(a.scoreMean, b.scoreMean);
        case 'runs': return cmpNum(a.succeededTrials / Math.max(1, a.trials), b.succeededTrials / Math.max(1, b.trials)) || cmpNum(a.trials, b.trials);
        case 'wall': return cmpNum(a.wallMsMedian, b.wallMsMedian);
        case 'cost': return cmpNum(a.costUsd, b.costUsd) || cmpNum(a.tokensTotal, b.tokensTotal);
      }
    });
  }, [entries, sortCol, sortDir]);

  const promoteRow = async (key: string, entry: LabLeaderboardEntry) => {
    if (promotingKey) return;
    setPromotingKey(key);
    setPromoteError(null);
    try {
      const reply = await op('lab_promote', { experimentId: entry.latestExperimentId, variant: entry.variantLabel }, { signal: alive });
      if (!alive.aborted) setPromoted({ key, reply });
    } catch (x) {
      if (!alive.aborted) setPromoteError(message(x));
    } finally {
      if (!alive.aborted) setPromotingKey(null);
    }
  };

  const sortStatus = sortCol ? `Sorted by ${sortCol}, ${sortDir}, ${sorted.length} rows` : '';
  const ellipsisStyle = { overflow: 'hidden', textOverflow: 'ellipsis' } as const;

  return (
    <section class="panel" aria-label={`Suite leaderboard: ${scopeLabel}`}>
      <header class="panel-h">
        <Icon name="chart-no-axes-column" />
        <h2>Suite leaderboard</h2>
        <span class="muted">{scopeLabel} · {windowLabel}</span>
        <span class="grow" />
        <span class="muted mono">{entries.length}</span>
      </header>
      <span class="sr-only" role="status">{sortStatus}</span>
      {error && <ErrorBanner text={error} onRetry={onRetry} />}
      {promoteError && <ProblemRow title="Promote refused" facts={promoteError} severity="failure" actions={[]} />}
      {promoted && (
        <DetailSection title="Promote">
          <KvRow
            icon="check"
            label={`Recommendation set to ${promoted.reply.method.id ?? ''}`}
            meta={promoted.reply.catalogue ? <span class="mono">catalogue r{promoted.reply.catalogue.revision}</span> : 'method already in the catalogue'}
          />
          {promoted.reply.note && <KvRow icon="circle-alert" label={String(promoted.reply.note)} />}
        </DetailSection>
      )}
      {loading && !error ? (
        <Skeleton rows={4} />
      ) : sorted.length === 0 ? (
        <Empty text={`No completed leaderboard trials for ${scopeLabel} yet.`} />
      ) : (
        <DataTable
          label={`Suite leaderboard (${scopeLabel})`}
          head={
            <tr>
              <SortColHead label="Variant" active={sortCol === 'variant'} dir={sortDir} onSort={() => toggleSort('variant')} />
              <SortColHead label="Pipeline" active={sortCol === 'pipeline'} dir={sortDir} onSort={() => toggleSort('pipeline')} />
              <SortColHead label="Score" active={sortCol === 'score'} dir={sortDir} numeric onSort={() => toggleSort('score')} />
              <SortColHead label="Runs" active={sortCol === 'runs'} dir={sortDir} numeric onSort={() => toggleSort('runs')} />
              <SortColHead label="Median" active={sortCol === 'wall'} dir={sortDir} numeric onSort={() => toggleSort('wall')} />
              <SortColHead label="Tokens / Cost" active={sortCol === 'cost'} dir={sortDir} numeric onSort={() => toggleSort('cost')} />
              <th scope="col">Actions</th>
            </tr>
          }
        >
          {sorted.map((entry, idx) => {
            const key = `${entry.useCaseId}|${entry.pipelineId}|${entry.variantLabel}|${entry.model ?? ''}|${entry.profile ?? ''}|${entry.configRef ?? ''}|${entry.evaluator}|${idx}`;
            const modelText = entry.model ? agentLabel(entry.model) : '';
            const modelLine = [modelText, entry.profile].filter(Boolean).join(' · ');
            const rangeText = entry.scoreMin != null && entry.scoreMax != null && entry.scoreMin !== entry.scoreMax
              ? `${entry.scoreMin.toFixed(2)}–${entry.scoreMax.toFixed(2)}`
              : '';
            const runsTip = `${entry.succeededTrials} succeeded, ${entry.failedTrials} failed of ${entry.trials} trials`;
            const modelTip = [entry.model, entry.profile, entry.configRef].filter(Boolean).join(' · ') || undefined;
            const isWinner =
              entry.succeededTrials > 0 &&
              entry.scoreMean != null &&
              entry.scoreMean > 0 &&
              entry.scoreMean === bestByGroup.get(`${entry.useCaseId}\0${entry.evaluator}`);
            const isPromoted = promoted?.key === key;
            return (
              <tr key={key}>
                <th scope="row">
                  <div style={{ maxWidth: '6.75rem', ...ellipsisStyle }} data-tip={(entry.variantLabel || '').length > 16 ? entry.variantLabel : undefined}>
                    {entry.variantLabel || 'baseline'}
                  </div>
                  {modelLine && (
                    <div class="muted" style={{ maxWidth: '6.75rem', ...ellipsisStyle }} data-tip={modelTip}>
                      {modelLine}
                    </div>
                  )}
                  {showUseCase && (
                    <div class="muted" style={{ maxWidth: '6.75rem', ...ellipsisStyle }}>
                      <a
                        href={`/lab/use-case:${encodeURIComponent(entry.useCaseId)}`}
                        data-tip={entry.useCaseId.length > 16 ? entry.useCaseId : undefined}
                        style={{ display: 'block', ...ellipsisStyle }}
                        onClick={ev => { ev.preventDefault(); onSelectUseCase(entry.useCaseId); }}
                      >
                        {entry.useCaseId}
                      </a>
                    </div>
                  )}
                </th>
                <td>
                  <div style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--space-1)', maxWidth: '6.75rem' }}>
                    <EntityCode id={entry.pipelineId} kind="pipeline" />
                    <a
                      href={`/pipelines/${encodeURIComponent(entry.pipelineId)}`}
                      data-tip={entry.pipelineId.length > 14 ? entry.pipelineId : undefined}
                      style={{ ...ellipsisStyle }}
                      onClick={ev => { ev.preventDefault(); onOpenPipeline(entry.pipelineId); }}
                    >
                      {entry.pipelineId}
                    </a>
                  </div>
                  <div class="muted" style={{ maxWidth: '6.75rem', ...ellipsisStyle }} data-tip={entry.evaluator}>
                    {evaluatorLabel(entry.evaluator)}
                  </div>
                </td>
                <td class="num">
                  <div>
                    <Num m={{ value: entry.scoreMean, basis: entry.scoreMean == null ? 'unavailable' : 'measured' }} kind="score" />
                  </div>
                  {rangeText && <div class="muted">{rangeText}</div>}
                </td>
                <td class="num" data-tip={runsTip}>
                  {entry.succeededTrials}/{entry.trials}
                </td>
                <td class="num">
                  <Num m={msMeasured(entry.wallMsMedian)} kind="duration" />
                </td>
                <td class="num">
                  <div>
                    <Num m={{ value: entry.tokensTotal, basis: entry.tokensTotal > 0 || entry.trials === 0 ? 'measured' : 'unavailable', reason: 'no token usage' }} kind="count" />
                    <span class="muted"> tok</span>
                  </div>
                  <div>
                    <Num m={{ value: entry.costUsd, basis: costBasisOf(entry.costBasis), currency: 'USD', reason: entry.costBasis }} kind="money" />
                    {entry.costBasis && entry.costBasis !== 'measured' && entry.costBasis !== 'unavailable' && entry.costUsd != null && (
                      <span class="muted"> {entry.costBasis === 'subscription' ? 'sub' : entry.costBasis}</span>
                    )}
                  </div>
                </td>
                <td>
                  <div style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--space-1)' }}>
                    <EntityCode id={entry.latestExperimentId} kind="experiment" />
                    <Button label="Open experiment" icon="external-link" iconOnly kind="quiet" tip="Open latest experiment" onClick={() => onOpenExperiment(entry.latestExperimentId)} />
                    <AgeChip t={ms(entry.latestAt)} />
                  </div>
                  {isWinner && (
                    <div>
                      <Button
                        label={isPromoted ? 'Promoted' : 'Promote'}
                        icon="check-check"
                        kind="quiet"
                        busy={promotingKey === key}
                        disabledReason={promotingKey && promotingKey !== key ? 'Promotion in progress' : undefined}
                        tip={`Promote ${entry.variantLabel || 'baseline'} to Library recommendation`}
                        onClick={() => promoteRow(key, entry)}
                      />
                    </div>
                  )}
                </td>
              </tr>
            );
          })}
        </DataTable>
      )}
    </section>
  );
}

// One rating or approval question with its blind outputs and keyboard answers.
function Rating({ r, now }: { r: LabRating; now: number }) {
  const [busy, setBusy] = useState(false);
  const opts = r.form === 'pick_best' ? r.items.map(i => ({ label: i.label, kbd: i.label.toLowerCase() }))
    : r.form === 'scale' ? [1, 2, 3, 4, 5].map(n => ({ label: String(n), kbd: String(n) }))
    : [{ label: 'Pass', kbd: 'p', primary: true }, { label: 'Fail', kbd: 'f' }];
  const alive = useAlive();
  const toAnswer = (a: string) => r.form === 'pick_best' ? (r.items.find(i => i.label === a.toUpperCase())?.trialId ?? a) : /^(pass|fail)\b/i.test(a) ? a.replace(/^(pass|fail)/i, m => m.toLowerCase()) : a;
  const shown = (a: string) => r.form === 'pick_best' ? (r.items.find(i => i.trialId === a)?.label ?? a) : a;
  const files = r.items.length > 0 && <div class="stack">{r.items.map(i => <div key={i.label}><b>{i.label}</b>{' '}
    {i.files.filter(f => !f.path.endsWith('/output.json') || i.files.length === 1).map(f => <a key={f.artifactId} class="id" href={`/api/results/${f.artifactId}${/\.(html?|svg)$/i.test(f.path) ? '/preview' : ''}`} target="_blank" rel="noopener">{f.path.split('/').slice(1).join('/')}</a>)}</div>)}</div>;
  if (r.state === 'withdrawn') return <div id={`rating-${r.id}`}><DetailSection title="Withdrawn rating"><KvRow icon="circle-slash" label={r.text} meta={r.withdrawal?.reason ?? 'No answer or score recorded'} />{files}</DetailSection></div>;
  return <div id={`rating-${r.id}`}><DecisionBlock question={r.text} askedBy="lab" addressedTo="you" since={now} now={now} waits={busy ? 'saving' : 'never blocks'} context={files || undefined} options={opts}
    answered={r.state === 'answered' ? { by: 'you', answer: shown(String(r.answer)), at: now, delivered: true } : undefined}
    onAnswer={a => { setBusy(true); writeOnce('questions_answer', { questionId: r.id, answer: toAnswer(a) }, () => {}, { signal: alive }).catch(() => { /* the rating stays open */ }).finally(() => setBusy(false)); }} /></div>;
}

const scoreText = (s: LabScoreSummary) => s.form === 'pick_best' ? `${s.wins ?? 0} of ${s.n} picks` : s.form === 'pass_fail' ? `${s.passed}/${s.n} pass` : s.mean == null ? '' : s.min === s.max ? s.mean.toFixed(2) : `${s.mean.toFixed(2)} (${s.min}–${s.max})`;
const evaluatorLabel = (k: string) => k.split(':')[0] === 'human' ? 'owner ' + k.split(':')[2] : k.split(':').slice(0, 2).join(' ');

type VariantParamMap = Record<string, unknown>;
type VariantParamDiff = { key: string; icon: IconName; label: string; baseText: string; currentText: string; diff: boolean };

const KNOWN_PARAM_ORDER: readonly { key: string; label: string; icon: IconName }[] = [
  { key: 'pipeline', label: 'Pipeline', icon: 'network' },
  { key: 'profile', label: 'Profile', icon: 'bot' },
  { key: 'model', label: 'Model', icon: 'sliders-horizontal' },
  { key: 'skills', label: 'Skills', icon: 'layers-2' },
  { key: 'prompt', label: 'Prompt', icon: 'file-text' },
  { key: 'web_route', label: 'Web route', icon: 'external-link' },
  { key: 'account', label: 'Account', icon: 'shield' },
  { key: 'host', label: 'Host', icon: 'panels-top-left' },
  { key: 'arguments', label: 'Arguments', icon: 'terminal' },
  { key: 'bands', label: 'Bands', icon: 'chart-no-axes-column' },
];

const variantParamsObj = (v: LabVariant): VariantParamMap => {
  const raw = (v as LabVariant & { overrides?: unknown }).overrides;
  return raw && typeof raw === 'object' && !Array.isArray(raw) ? (raw as VariantParamMap) : {};
};

const canonicalParam = (val: unknown): string => {
  if (val === undefined || val === null || val === '') return '';
  if (Array.isArray(val)) return val.length === 0 ? '' : JSON.stringify(val);
  if (typeof val === 'object') {
    const entries = Object.entries(val as Record<string, unknown>).sort(([a], [b]) => a.localeCompare(b));
    return entries.length === 0 ? '' : JSON.stringify(Object.fromEntries(entries));
  }
  return String(val);
};

const formatParamText = (val: unknown): string => {
  if (val === undefined || val === null || val === '') return 'default';
  if (Array.isArray(val)) return val.length === 0 ? 'default' : val.map(x => String(x)).join(', ');
  if (typeof val === 'object') {
    const entries = Object.entries(val as Record<string, unknown>);
    return entries.length === 0 ? 'default' : JSON.stringify(val);
  }
  return String(val);
};

/** Compute OR-45 parameter diffs for a variant against its baseline (first variant or experiment base pipeline). */
function computeVariantParamDiffs(
  v: LabVariant,
  baseline: LabVariant | null,
  experimentPipelineId: string,
): VariantParamDiff[] {
  const curObj = variantParamsObj(v);
  const baseObj = baseline ? variantParamsObj(baseline) : {};
  const curPipeline = v.pipelineId || experimentPipelineId;
  const basePipeline = baseline ? (baseline.pipelineId || experimentPipelineId) : experimentPipelineId;

  const extraKeys = [...new Set([...Object.keys(curObj), ...Object.keys(baseObj)])]
    .filter(k => !KNOWN_PARAM_ORDER.some(p => p.key === k))
    .sort();
  const keys: { key: string; label: string; icon: IconName }[] = [
    ...KNOWN_PARAM_ORDER,
    ...extraKeys.map(k => ({ key: k, label: k, icon: 'sliders-horizontal' as const })),
  ];

  const rows: VariantParamDiff[] = [];
  for (const { key, label, icon } of keys) {
    const curVal = key === 'pipeline' ? curPipeline : curObj[key];
    const baseVal = key === 'pipeline' ? basePipeline : baseObj[key];
    const curCanon = canonicalParam(curVal);
    const baseCanon = canonicalParam(baseVal);
    // Skip parameters that are unset in both variant and baseline (except pipeline which is always defined).
    if (!curCanon && !baseCanon) continue;
    rows.push({
      key,
      icon,
      label,
      baseText: formatParamText(baseVal),
      currentText: formatParamText(curVal),
      diff: curCanon !== baseCanon,
    });
  }
  return rows;
}

function Experiment({ id, onClose }: { id: string; onClose: () => void }) {
  const [pollMs, setPollMs] = useState<number | undefined>(5000);
  const exp = useOp('lab_experiment_get', { experimentId: id }, { every: pollMs });
  const rated = useOp('lab_ratings_list', { experimentId: id }, { every: pollMs });
  const e = exp.data, state = exp.state, ratings = rated.data?.ratings ?? [];
  const isTerminal = !!e && TERMINAL_STATES.has(e.state) && !e.trials.some(t => t.state === 'running' || t.state === 'preparing');
  useEffect(() => {
    setPollMs(isTerminal ? undefined : 5000);
  }, [isTerminal]);

  const [savingRecord, setSavingRecord] = useState(0);
  const [recordDraft, setRecordDraft] = useState<Record<string, string>>({});
  const [recordErrors, setRecordErrors] = useState<Record<string, string>>({});
  const [savedRecord, setSavedRecord] = useState<Record<string, string>>({});
  const [decisionDraft, setDecisionDraft] = useState<string>();
  const [reasonDraft, setReasonDraft] = useState<string>();
  const [savingDecision, setSavingDecision] = useState(false);
  const [sel, setSel] = useState<string[]>([]);
  const [planTrial, setPlanTrial] = useState<string>();
  const [msg, setMsg] = useState<{ text: string; tone: 'warning' | 'failure' } | null>(null);
  const [patch, setPatch] = useState<Ops['lab_promote']['reply'] | null>(null);
  const [promotingVariant, setPromotingVariant] = useState<string | null>(null);
  const [promotedVariant, setPromotedVariant] = useState<string | null>(null);
  useEffect(() => { if (e && location.hash) document.getElementById(location.hash.slice(1))?.scrollIntoView(); }, [!!e]);
  const now = Date.now();
  if (!e) {
    return (
      <article class="detail" data-ui="detail" tabIndex={-1}>
        <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /></header>
        {exp.error ? <ErrorBanner text={exp.error} onRetry={exp.reload} /> : <Skeleton rows={6} />}
      </article>
    );
  }
  const act = <K extends OpName>(name: K, body: Ops[K]['request']): Promise<Ops[K]['reply'] | undefined> => op(name, body).then(r => { const w = (r as { warnings?: string[] }).warnings; setMsg(w?.length ? { text: w.join('; '), tone: 'warning' } : null); return r; }).catch((x: unknown) => { setMsg({ text: message(x), tone: 'failure' }); return undefined; });
  const promoteVariant = async (variantLabel: string) => {
    if (promotingVariant) return;
    setPromotingVariant(variantLabel);
    try {
      const r = await act('lab_promote', { experimentId: id, variant: variantLabel });
      if (r) {
        setPatch(r);
        setPromotedVariant(variantLabel);
      }
    } finally {
      setPromotingVariant(null);
    }
  };
  const variantScore = (s: LabScoreSummary | undefined): number | null => {
    if (!s) return null;
    if (s.form === 'pick_best') return s.n ? (s.wins ?? 0) / s.n : null;
    if (s.form === 'pass_fail') return s.n ? (s.passed ?? 0) / s.n : null;
    return s.mean ?? null;
  };
  const winningVariant = (() => {
    if (e.state !== 'done') return null;
    if (e.decision?.startsWith('adopt ')) {
      const target = e.decision.slice('adopt '.length).trim();
      const adopted = e.variants.find(v => (v.label ?? '').trim() === target);
      if (adopted) return adopted;
    }
    let best: (typeof e.variants)[number] | null = null;
    let bestScore = 0;
    for (const v of e.variants) {
      const summaryMap = v.summary ?? {};
      const key = e.primaryEvaluator && summaryMap[e.primaryEvaluator]
        ? e.primaryEvaluator
        : Object.keys(summaryMap)[0];
      const sc = key ? variantScore(summaryMap[key]) : null;
      if (sc != null && sc > bestScore) {
        bestScore = sc;
        best = v;
      }
    }
    return best;
  })();
  const trial = (key: string) => e.trials.filter(t => `${t.cell.example}|${t.cell.variant}|${t.cell.repeat}` === key).sort((a, b) => (b.cell.attempt ?? 0) - (a.cell.attempt ?? 0))[0];
  const toggle = (t: string) => setSel(s => s.includes(t) ? s.filter(x => x !== t) : [...s, t]);
  const rate = (form: Ops['lab_rating_create']['request']['form']) => act('lab_rating_create', { experimentId: id, form, trialIds: sel }).then(r => { if (r?.id) { setSel([]); location.hash = 'rating-' + r.id; } });
  const budgetOf = (unit: string) => e.budgets.find(b => b.unit === unit);
  const note = (unit: string) => { const b = budgetOf(unit); return b ? `of ${b.limit} · ${b.action}${b.firedAt ? ' · reached' : ''}` : undefined; };
  const record = (field: string) => field === 'goal' ? e.goal : field === 'hypothesis' ? e.hypothesis : undefined;
  const decisionOptions = [
    ...e.variants.map(v => ({ id: `adopt ${v.label ?? ''}`.trim(), label: `Adopt ${v.label ?? ''}` })),
    { id: 'keep baseline', label: 'Keep baseline' },
    { id: 'inconclusive', label: 'Inconclusive' },
  ];
  const currentDecision = decisionDraft ?? (e.decision && e.decision !== 'pending' ? e.decision : '');
  const currentReason = reasonDraft ?? e.decisionReason ?? '';
  const saveDecision = async () => {
    if (!currentDecision) return;
    setSavingDecision(true);
    try { await act('lab_experiment_decide', { experimentId: id, decision: currentDecision, reason: currentReason.trim() || undefined }); }
    finally { setSavingDecision(false); }
  };
  const baselineVariant = e.variants.length > 1 ? e.variants[0] : null;
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={title(e)}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /><StateBadge family="experiment" state={e.state as StateOf<'experiment'>} reason={e.stopReason ?? undefined} /><EntityCode id={e.id} kind="experiment" /><span class="grow" />
      {e.state === 'draft' && <Button label="Launch" icon="play" kind="primary" disabledReason={savingRecord ? 'Saving experiment record' : Object.values(recordErrors).some(Boolean) || Object.entries(recordDraft).some(([field, value]) => value !== String(savedRecord[field] ?? record(field) ?? '')) ? 'Save the experiment record successfully before launch' : !String(savedRecord.goal ?? e.goal ?? '').trim() || !String(savedRecord.hypothesis ?? e.hypothesis ?? '').trim() ? 'Save a goal and hypothesis before launch' : undefined} onClick={() => act('lab_experiment_action', { experimentId: id, action: 'launch' })} />}
      {e.state === 'paused' && <Button label="Resume" icon="play" kind="primary" onClick={() => act('lab_experiment_action', { experimentId: id, action: 'resume' })} />}
      {(e.state === 'running' || e.state === 'paused') && <Button label="Stop" icon="square" kind="danger" onClick={() => act('lab_experiment_action', { experimentId: id, action: 'stop' })} />}
      {e.state === 'done' && winningVariant && (
        <Button
          label={promotedVariant === (winningVariant.label ?? '') ? `Promoted ${winningVariant.label ?? ''}` : `Promote ${winningVariant.label ?? ''}`}
          icon="check-check"
          kind="primary"
          busy={promotingVariant === (winningVariant.label ?? '')}
          disabledReason={promotingVariant && promotingVariant !== (winningVariant.label ?? '') ? 'Promotion in progress' : undefined}
          onClick={() => promoteVariant(winningVariant.label ?? '')}
        />
      )}
      <OfflineMarker state={state} now={now} /></header>
    <h2 class="detail-title">{title(e)}</h2>
    {e.state === 'draft' && <DetailSection title="Experiment record">{(['goal', 'hypothesis'] as const).map(field => <SettingField key={field} label={field === 'goal' ? 'Goal' : 'Hypothesis'} help="Saved in the experiment record before explicit launch" value={recordDraft[field] ?? savedRecord[field] ?? record(field) ?? ''} now={now} status={recordErrors[field] ? {kind: 'apply_error', error: recordErrors[field]} : {kind: 'pending'}} onSave={async value => {
      setRecordDraft(r => ({...r, [field]: value})); setSavingRecord(n => n + 1);
      try { await op('lab_experiment_note', {experimentId: id, kind: 'amendment', field, value, text: `Set ${field} before launch`}); setSavedRecord(r => ({...r, [field]: value})); setRecordErrors(r => ({...r, [field]: ''})); setMsg(null); }
      catch (x) { setRecordErrors(r => ({...r, [field]: message(x)})); setMsg({text: message(x), tone: 'failure'}); }
      finally { setSavingRecord(n => n - 1); }
    }} />)}</DetailSection>}
    {(e.state !== 'draft' || (e.decision && e.decision !== 'pending')) && <DetailSection title="Question & decision">
      {e.goal && <KvRow icon="circle-question-mark" label="Goal" value={e.goal} />}
      {e.hypothesis && <KvRow icon="file-text" label="Hypothesis" value={e.hypothesis} />}
      {e.decisionRule && <KvRow icon="sliders-horizontal" label="Decision rule" value={e.decisionRule} />}
      {e.decision && e.decision !== 'pending' && <KvRow icon="check-check" label={e.decision}
        meta={[e.decidedBy && `by ${e.decidedBy}`, e.decidedAt].filter(Boolean).join(' · ') || undefined}
        value={e.decisionReason ?? undefined} />}
      {(e.state === 'done' || e.state === 'stopped') && <div class="stack">
        <Choice label="Experiment decision" options={decisionOptions} value={currentDecision} onChange={setDecisionDraft} />
        <TextField label="Rationale" value={currentReason} onChange={setReasonDraft} help="Why this outcome was chosen" />
        <div><Button label={savingDecision ? 'Saving decision…' : e.decision && e.decision !== 'pending' ? 'Update decision' : 'Record decision'} kind="primary" icon="check" disabledReason={!currentDecision ? 'Select a decision first' : savingDecision ? 'Saving decision' : undefined} onClick={saveDecision} /></div>
      </div>}
    </DetailSection>}
    {msg && <ProblemRow title={msg.tone === 'warning' ? 'Launched with warnings' : 'Action refused'} facts={msg.text} severity={msg.tone} actions={[]} />}
    {patch && <DetailSection title="Promote"><KvRow icon="check" label={`Recommendation set to ${patch.method.id ?? ''}`}
      meta={patch.catalogue ? <span class="mono">catalogue r{patch.catalogue.revision}</span> : 'method already in the catalogue'} />
      {patch.note && <KvRow icon="circle-alert" label={String(patch.note)} />}</DetailSection>}
    <Totals items={[
      { label: 'Trials', m: { value: e.spend.trials.value, basis: 'measured' }, kind: 'count', note: note('trials') },
      { label: 'Wall time', m: { value: e.spend.wall_ms.value / 1000, basis: 'measured' }, kind: 'duration', note: note('wall_ms') },
      { label: 'API calls', m: { value: e.spend.calls.value, basis: 'measured' }, kind: 'count', note: note('calls') },
      { label: 'Credits', m: { value: e.spend.credits.value, basis: 'measured', partial: e.spend.credits.unmeasured ? `${e.spend.credits.unmeasured} calls without usage` : undefined }, kind: 'count', note: note('credits') },
    ]} />
    <section class="panel"><header class="panel-h"><Icon name="rows-3" /><h2>Matrix</h2><span class="muted">{e.examples.length} × {e.variants.length} × {e.repeats}</span><span class="grow" />
      {sel.length === 1 && <><Button label="Pass or fail" icon="check" onClick={() => rate('pass_fail')} /><Button label="Scale 1–5" icon="sliders-horizontal" onClick={() => rate('scale')} /></>}
      {sel.length >= 2 && <Button label={`Pick best of ${sel.length}`} icon="check-check" onClick={() => rate('pick_best')} />}</header>
      <DataTable
        label="Experiment matrix"
        head={
          <tr>
            <th scope="col">Example</th>
            {e.variants.map(v => <th key={v.label} scope="col">{v.label}</th>)}
          </tr>
        }
      >
        {e.examples.map(p => (
          <tr key={p.id}>
            <th scope="row">
              <span>{p.id}</span>
              {p.split && <span class="id">{p.split}</span>}
            </th>
            {e.variants.map(v => {
              const c = e.cells.find(x => x.example === p.id && x.variant === v.label);
              return (
                <td key={v.label}>
                  <div class="lab-cell">
                    {Object.entries(c?.summary ?? {}).map(([k, s]) => <span key={k} class="muted" data-tip={k}>{evaluatorLabel(k)} <b>{scoreText(s)}</b></span>)}
                    {(c?.trials ?? []).map(key => { const t = trial(key); if (!t) return null;
                      const conf = [(t.confounders.hosts ?? []).join(', '), (t.confounders.harnesses ?? []).join(', '), (t.confounders.models ?? []).join(', '), 'web ' + String(t.confounders.webRoute)].filter(Boolean).join(' · ');
                      const problem = problemText(t.problem);
                      const repeatTag = `r${(t.cell.repeat ?? 0) + 1}${t.cell.attempt ? `.${t.cell.attempt}` : ''}`;
                      const plannedSteps = t.planSummary?.planned ?? 0;
                      const activeStep = Math.min(plannedSteps, Math.max(0, t.planSummary?.selected ?? 0));
                      const showSteps = (t.state === 'running' || t.state === 'preparing') && plannedSteps > 0;
                      const stepTip = `${activeStep} of ${plannedSteps} steps selected${t.planSummary.pending ? ` · ${t.planSummary.pending} pending` : ''}${t.planSummary.blocked ? ` · ${t.planSummary.blocked} blocked` : ''}`;
                      return <Fragment key={key}>
                        <span class="lab-trial">
                          <Button label="View trial plan" icon="network" iconOnly kind="quiet" onClick={() => setPlanTrial(t.id)} />
                          <StateBadge family="trial" state={(t.observation === 'unconfirmed' ? 'unknown' : t.state) as StateOf<'trial'>} reason={problem ?? (t.comparable ? undefined : 'left out of comparison')} compact />
                          {t.comparable && <RowCheck checked={sel.includes(t.id)} label={`Select repeat ${(t.cell.repeat ?? 0) + 1} for rating`} onToggle={() => toggle(t.id)} />}
                          <span class="muted" data-tip={conf}>{repeatTag}</span>
                          {showSteps && <span class="chip-age num" data-tip={stepTip}>step {activeStep}/{plannedSteps}</span>}
                          <Num m={{ value: t.durationMs == null ? null : t.durationMs / 1000, basis: t.durationMs == null ? 'unavailable' : 'measured', reason: 'running' }} kind="duration" />
                          <Num m={{ value: t.spend.credits ?? null, basis: 'measured', partial: t.spend.basis === 'partial' ? 'some calls without usage' : undefined }} kind="count" />
                          {t.spend.tokens != null && (t.spend.tokens > 0 || t.state !== 'preparing') && (
                            <span class="muted" data-tip={`Tokens: ${t.spend.tokens.toLocaleString('en')}`}><Num m={{ value: t.spend.tokens, basis: 'measured', partial: t.spend.basis === 'partial' ? 'some calls without usage' : undefined }} kind="count" /> tok</span>
                          )}
                          {t.recovery && (
                            <span class="deps" data-tip={`Reuses ${(t.recovery.reusedStepIds ?? []).join(', ')}; original spend remains in source history`}>
                              <EntityCode id={t.recovery.sourceRunId} kind="execution" />
                              <Button label="Inspect source trial plan" icon="network" iconOnly kind="quiet" onClick={() => setPlanTrial(t.recovery?.sourceRunId)} />
                            </span>
                          )}
                          {(t.cost ?? []).map((costItem, i) => <Num key={i} m={{ value: costItem.value, basis: costBasisOf(costItem.basis), currency: costItem.currency ?? undefined, reason: costItem.value === null ? costItem.reason : `${costItem.basis} · ${costItem.prices?.length ?? 0} price${costItem.prices?.length === 1 ? '' : 's'}` }} kind="money" />)}
                          {t.scores.filter(s => s.trialIds?.length === 1).map(s => s.form === 'typed'
                            ? <span key={s.id} data-tip={`${evaluatorLabel(s.evaluator ?? '')}${s.rationale ? ': ' + s.rationale : ''}`}><Icon name={typedIcon(s.value)} label={`${evaluatorLabel(s.evaluator ?? '')} ${String(typedValue(s.value).answer)}, label ${String(typedValue(s.value).label ?? 'none')}`} />{String(typedValue(s.value).answer)}</span>
                            : <span key={s.id} data-tip={`${evaluatorLabel(s.evaluator ?? '')}${s.rationale ? ': ' + s.rationale : ''}`}><Icon name={s.form === 'pass_fail' ? (s.value ? 'circle-check' : 'circle-x') : 'sliders-horizontal'} label={`${evaluatorLabel(s.evaluator ?? '')} ${String(s.value)}`} />{s.form === 'pass_fail' ? '' : String(s.form === 'metric' ? Number(s.value).toFixed(2) : s.value)}</span>)}
                          {t.state === 'running' ? <Button label="Cancel cell" icon="x" iconOnly kind="quiet" onClick={() => act('lab_cell_action', { experimentId: id, cell: key, action: 'cancel' })} />
                            : <Button label="Retry cell" icon="rotate-ccw" iconOnly kind="quiet" onClick={() => act('lab_cell_action', { experimentId: id, cell: key, action: 'retry' })} />}
                        </span>
                        {problem && <span class="reason" data-tip={problem}>{e.repeats > 1 ? `${repeatTag}: ` : ''}{problem}</span>}
                      </Fragment>;
                    })}
                  </div>
                </td>
              );
            })}
          </tr>
        ))}
      </DataTable>
    </section>
    {planTrial && <TrialPlan key={planTrial} id={planTrial} projectId={e.projectId} trialProblem={problemText(e.trials.find(t => t.id === planTrial)?.problem)} assessments={e.trials.find(t => t.id === planTrial)?.projectAssessments ?? []} onOpen={setPlanTrial} onClose={() => setPlanTrial(undefined)} />}
    <section class="panel"><header class="panel-h"><Icon name="sliders-horizontal" /><h2>Variants</h2></header>
      <DetailSection title="Spread across repeats">
        {e.variants.map((v, vi) => {
          const variantPipeline = v.pipelineId ?? e.pipelineId;
          const isBaselineRow = vi === 0 && e.variants.length > 1;
          const paramRows = computeVariantParamDiffs(v, isBaselineRow ? null : baselineVariant, e.pipelineId);
          const diffs = paramRows.filter(r => r.diff);
          const inherited = paramRows.filter(r => !r.diff);
          return (
            <Fragment key={v.label}>
              <KvRow
                icon="git-merge"
                label={v.proposalLabel ? `${v.proposalLabel} · ${v.label}` : v.label ?? ''}
                meta={
                  <span class="deps">
                    <span>{Object.entries(v.summary ?? {}).map(([k, s]) => `${evaluatorLabel(k)} ${scoreText(s)}`).join(' · ') || 'no scores yet'}</span>
                    <span>·</span>
                    <EntityCode id={variantPipeline} kind="pipeline" />
                    <span class="mono">{variantPipeline}</span>
                    {v.catalogueRef?.id && <a href={`/catalogue/${encodeURIComponent(v.catalogueRef.id)}`}>Catalogue r{v.catalogueRef.revision}</a>}
                    {v.config_ref && <CopyButton label={`Executable digest: ${v.config_ref}`} value={v.config_ref} iconOnly />}
                    {v.catalogueConfigRef && <CopyButton label={`Catalogue identity: ${v.catalogueConfigRef}`} value={v.catalogueConfigRef} iconOnly />}
                  </span>
                }
                value={
                  <Button
                    label="Promote"
                    icon="check-check"
                    busy={promotingVariant === (v.label ?? '')}
                    disabledReason={promotingVariant && promotingVariant !== (v.label ?? '') ? 'Promotion in progress' : undefined}
                    onClick={() => promoteVariant(v.label ?? '')}
                  />
                }
              />
              {isBaselineRow ? (
                <div class="stack-pad">
                  <KvRow icon="circle-check" label="Baseline" meta={<span class="mono">{variantPipeline}</span>} />
                  {paramRows.length > 0 && (
                    <Disclosure summary={`Inherited defaults (${paramRows.length})`} summaryClass="muted">
                      {paramRows.map(f => (
                        <KvRow key={f.key} icon={f.icon} label={f.label} meta={<span class="mono">{f.currentText}</span>} />
                      ))}
                    </Disclosure>
                  )}
                </div>
              ) : (
                <div class="stack-pad">
                  {diffs.length > 0 ? (
                    diffs.map(f => (
                      <KvRow
                        key={f.key}
                        icon={f.icon}
                        label={f.label}
                        value={<span class="mono" data-tip={baselineVariant ? `Baseline: ${f.baseText}` : `Default: ${f.baseText}`}>{f.currentText}</span>}
                      />
                    ))
                  ) : baselineVariant ? (
                    <KvRow icon="circle-check" label="Matches baseline" meta={baselineVariant.label} />
                  ) : null}
                  {inherited.length > 0 && (
                    <Disclosure summary={`Inherited defaults (${inherited.length})`} summaryClass="muted">
                      {inherited.map(f => (
                        <KvRow key={f.key} icon={f.icon} label={f.label} meta={<span class="mono">{f.currentText}</span>} />
                      ))}
                    </Disclosure>
                  )}
                </div>
              )}
            </Fragment>
          );
        })}
      </DetailSection>
      {(e.typed ?? []).map(t => <DetailSection key={`${t.variant}/${t.split}`} title={`${t.variant} · typed answers · ${t.split === 'tune' ? 'tune (bands are set here)' : 'held out (reported)'}`}>
        {t.stats.map(s => { const cov = percent(s.confident, s.n), esc = percent(100 - parseInt(cov.text, 10), 100);
          return <KvRow key={s.aggregate ? '\u0000all' : s.tag} icon={s.aggregate ? 'chart-no-axes-column' : 'file-text'} label={s.aggregate ? 'All languages' : s.tag}
            meta={`n ${s.n} · coverage ${cov.text} · escalated ${esc.text}`}
            value={s.selectiveAccuracy === null ? <span class="muted">no confident answer</span> : `accuracy ${percent(s.correct, s.confident).text}`} />; })}
      </DetailSection>)}
      {(e.comparison ?? []).map(c => <DetailSection key={c.variant} title={`${c.variant} vs ${c.baseline} · ${c.split === 'held_out' ? 'held out' : 'all examples'}`}>
        <KvRow icon="git-merge" label={c.result.mean === null ? 'No paired example' : `${c.evaluator.startsWith('typed:') ? 'Correct confident answers' : 'Mean'} difference ${c.result.mean >= 0 ? '+' : ''}${c.result.mean.toFixed(2)}`}
          meta={`${c.result.tooFew ? `n ${c.result.n} · too few examples for an interval` : `n ${c.result.n} · 95% interval ${fixed(c.result.low)} to ${fixed(c.result.high)}`}${c.evaluator.startsWith('typed:') ? ' · an escalation counts as not correct; see coverage and accuracy above' : ''}`}
          value={c.reason ? <span class="muted">{c.reason}</span> : undefined} />
      </DetailSection>)}
      {(e.strategies ?? []).length > 0 && <DetailSection title="Loop strategies"><VirtualList items={e.strategies} getKey={v => v.id ?? ''} rowHeight={ROW2} follow="top" label="Strategy runs" columns="1.25rem minmax(0,1fr) auto"
        render={v => <><span class="lead"><StateBadge family="execution" state={v.state as StateOf<'execution'>} compact /></span><span class="tt">Iteration {String(v.iteration)} · {v.output?.reason ?? problemText(v.problem) ?? 'pending'}</span></>} /></DetailSection>}
      <DetailSection title="Decision gate">
        <KvRow icon="check-check" label="Judge agrees with you" meta={e.agreement.compared ? `${e.agreement.matched} of ${e.agreement.compared} (${e.agreement.percent}%)` : 'no trial rated by both yet'} />
        {(e.agreement.byVersion ?? []).length > 0 && <VirtualList items={e.agreement.byVersion ?? []} getKey={v => v.version ?? ''} rowHeight={ROW2} follow="top" label="Agreement by rubric version" columns="1.25rem minmax(0,1fr) auto"
          render={v => <><span class="lead"><Icon name="check-check" /></span><span class="tt">{v.version}</span><span>{v.compared ? `${v.matched} of ${v.compared} (${v.percent}%)` : 'no shared rating'}</span></>} />}
        {(e.pairwise ?? []).length > 0 && <VirtualList items={e.pairwise} getKey={v => v.group ?? ''} rowHeight={ROW2} follow="top" label="Pairwise order checks" columns="1.25rem minmax(0,1fr) auto"
          render={v => <><span class="lead"><Icon name={v.reason === 'agree' ? 'check-check' : 'circle-alert'} /></span><span class="tt">{v.group}</span><span>{v.reason}</span></>} />}
        <KvRow icon="clock" label="Priority background" meta={e.priority.note} />
      </DetailSection>
    </section>
    <section class="panel"><header class="panel-h"><Icon name="message-circle-question" /><h2>Ratings</h2><span class="muted">rated {ratings.filter(r => r.state === 'answered').length} of {ratings.filter(r => r.state !== 'withdrawn').length}</span></header>
      {ratings.length ? ratings.map(r => <Rating key={r.id} r={r} now={now} />) : <Empty text="Select comparable trials in the matrix to ask for a rating." />}
    </section>
  </article>;
}

// A scoped server read owns the plan; the matrix never reconstructs omitted work.
function TrialPlan({ id, projectId, trialProblem, assessments, onOpen, onClose }: { id: string; projectId: string; trialProblem?: string; assessments: ProjectAssessmentRef[]; onOpen: (runId: string) => void; onClose: () => void }) {
  const [pollMs, setPollMs] = useState<number | undefined>(5000);
  const { data: run, state, error } = useOp('pipeline_get', { runId: id, projectId }, { every: pollMs });
  const isTerminal = !!run && TERMINAL_STATES.has(run.state);
  useEffect(() => {
    setPollMs(isTerminal ? undefined : 5000);
  }, [isTerminal]);

  const items: StepItem[] = (run?.plan.entries ?? []).map(entry => {
    const execution = entry.execution;
    const definitions = (run?.definition as { steps?: { id: string; kind: string }[] } | undefined)?.steps ?? [];
    const definition = definitions.find(step => step.id === entry.id);
    return { id: entry.id, kind: definition?.kind === 'api' ? 'tool' : definition?.kind === 'agent' ? 'agent' : 'program', executor: definition?.kind ?? '', reads: '', writes: '', activation: entry.activation, activationReason: entry.problem?.code ?? entry.decision?.reason,
      result: entry.id === run?.canonicalResult?.stepId,
      run: execution ? { state: execution.state, reason: execution.observation, note: entry.executionRef?.mode === 'reused' ? 'reused · original resources' : execution.observation === 'unconfirmed' ? 'unconfirmed' : undefined,
        time: msMeasured(execution.durationMs), cost: { value: null, basis: 'unavailable' as const }, error: execution.error ? JSON.stringify(execution.error) : undefined } : undefined };
  });
  return <DetailSection title="Trial plan">
    <KvRow icon="network" label="Trial" value={<><EntityCode id={id} kind="execution" /><Button label="Close trial plan" icon="x" iconOnly onClick={onClose} /></>} />
    <OfflineMarker state={state} now={Date.now()} />
    {trialProblem && <ProblemRow title="Trial problem" facts={trialProblem} severity="failure" actions={[]} />}
    {error && <ProblemRow title="Trial plan unavailable" facts={error} severity="failure" actions={[]} />}
    {run && <KvRow icon="rows-3" label="Plan entries" meta={`${run.planSummary.planned} planned · ${run.planSummary.freshExecutions} fresh · ${run.planSummary.reusedExecutions} reused · ${run.planSummary.notSelected} not selected · ${run.planSummary.pending} pending · ${run.planSummary.blocked} blocked`} />}
    {run ? <StepList label="Trial activation and execution" items={items} /> : <Empty text="Plan unavailable" />}
    {(run?.plan.entries ?? []).filter(entry => entry.executionRef?.mode === 'reused').map(entry => (
      <KvRow
        key={entry.id}
        icon="network"
        label={`${entry.id} · reused source`}
        value={
          <span class="deps">
            <EntityCode id={entry.executionRef!.sourceRunId!} kind="execution" />
            <Button label="Inspect source trial plan" icon="network" iconOnly kind="quiet" onClick={() => onOpen(entry.executionRef!.sourceRunId!)} />
          </span>
        }
      />
    ))}
    {run?.canonicalResult && (
      <KvRow
        icon="file-text"
        label="Canonical result"
        value={
          <span class="deps">
            <EntityCode id={run.canonicalResult.artifact.artifactId} kind="result" />
            <a class="btn quiet icon-only" href={`/api/results/${encodeURIComponent(run.canonicalResult.artifact.artifactId)}`} target="_blank" rel="noopener" aria-label="Download canonical result" data-tip="Download"><Icon name="external-link" /></a>
          </span>
        }
      />
    )}
    {assessments.map(assessment => (
      <KvRow
        key={`${assessment.checkId}:${assessment.evaluatorSha256}:${assessment.inputSha256}`}
        icon="file-text"
        label={`Project assessment · ${assessment.checkId}`}
        meta={assessment.schemaId}
        value={
          <span class="deps">
            <EntityCode id={assessment.artifactId} kind="result" />
            <a class="btn quiet icon-only" href={`/api/results/${encodeURIComponent(assessment.artifactId)}`} target="_blank" rel="noopener" aria-label={`Download ${assessment.checkId} assessment`} data-tip="Download"><Icon name="external-link" /></a>
          </span>
        }
      />
    ))}
  </DetailSection>;
}
