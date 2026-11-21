// Lab (docs/designs/fleet-lab.md "/lab screen", docs/design-system/pages/laboratory.md): experiments list, use-case
// registry and examples awaiting approval on /lab; one experiment's matrix (rows examples, columns variants), spend against
// budgets, judge-to-owner agreement, rating queue and Promote on /lab/<experiment id>. Ratings and budget questions are
// answered through questions_answer like every question; rating outputs show blind (A, B, …) in the seeded order.
import { Fragment, type ComponentChildren } from 'preact';
import { useEffect, useMemo, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  StateBadge, Button, Disclosure, EntityCode, Num, Totals, DetailSection, KvRow, DecisionBlock, ProblemRow, RowCheck,
  AgeChip, VirtualList, Empty, OfflineMarker, Icon, ListDetail, DataTable, SortColHead, Choice, TextField,
  SearchField, CatalogueTree, ContextMenu, ErrorBanner, ROW2, percent, SettingField, StepList, msMeasured,
  agentLabel, type StepItem, type StateOf, type Basis, type CatalogueTreeNode, type MenuItem, ShellStatus, actions,
} from '../../../packages/ui/src/index.ts';
import { op, writeOnce, useAlive, type OpName, type Ops } from '../api.ts';
import { useOp } from '../store.ts';
import { closedGroups, flipGroup } from '../lists.ts';
import { listGroups, setListGroups, usePins } from '../prefs.ts';
import { LabCreate } from './lab-create.tsx';
import {
  ARCHIVED_COLLECTION_ID, buildUseCaseTree, collectionTitleOf, groupUseCases, isLabEligibleUseCase,
  matchesUseCaseSlug, useCaseSlug, type RegistryUseCase,
} from './use-cases.ts';
import type {
  LabExperimentRecord, LabLeaderboardEntry, LabRating, LabScoreSummary, ProjectAssessmentRef,
} from '../../../contracts/generated/ops.ts';

const ms = (t: string | number | null | undefined) => typeof t === 'number' ? t : Date.parse(t ?? '');
const message = (x: unknown) => x instanceof Error ? x.message : String(x);
const fixed = (x: number | null) => x === null ? '—' : x.toFixed(2);
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

export function Lab({ id }: { id?: string }) {
  if (id === 'new') return <LabCreate />;
  return <LabView id={id} />;
}

const RUN_FOLDERS = [
  { id: 'run:active', label: 'Active', icon: 'activity' as const, match: (e: LabExperimentRecord) => e.state === 'running' || e.state === 'paused', defaultCollapsed: false },
  { id: 'run:drafts', label: 'Drafts', icon: 'circle-dashed' as const, match: (e: LabExperimentRecord) => e.state === 'draft', defaultCollapsed: false },
  { id: 'run:completed', label: 'Completed', icon: 'circle-check' as const, match: (e: LabExperimentRecord) => e.state === 'done', defaultCollapsed: false },
  { id: 'run:stopped', label: 'Stopped', icon: 'circle-slash' as const, match: (e: LabExperimentRecord) => e.state === 'stopped', defaultCollapsed: true },
] as const;

function LabView({ id }: { id?: string }) {
  const [, go] = useLocation();
  const [navMode, setNavMode] = useState<'use-cases' | 'runs'>(() => (id ? 'runs' : 'use-cases'));
  const [ctlQuery, setCtlQuery] = useState('');
  const [selectedFolderId, setSelectedFolderId] = useState<string | null>(null);
  const [selectedUseCaseId, setSelectedUseCaseId] = useState<string | null>(null);
  const [selectedRunFolderId, setSelectedRunFolderId] = useState<string | null>(null);
  const [treeMenu, setTreeMenu] = useState<{ kind: 'use-case' | 'run'; node: CatalogueTreeNode; at: { x: number; y: number } } | null>(null);
  const [bestScoresByUseCase, setBestScoresByUseCase] = useState<Record<string, number>>({});

  const ucPins = usePins('lab:use-cases');
  const runPins = usePins('lab:runs');

  useEffect(() => {
    if (id) setNavMode('runs');
  }, [id]);

  const experiments = useOp('lab_experiments_list', {});
  const registry = useOp('lab_registry', {});
  const ratings = useOp('lab_ratings_list', { state: 'open' });

  const selectedCollectionTitle = collectionTitleOf(selectedFolderId);
  const leaderboardReq = useMemo<Ops['lab_leaderboard']['request'] | null>(() => {
    if (id || navMode !== 'use-cases') return null;
    if (selectedUseCaseId) return { useCaseId: selectedUseCaseId };
    if (selectedCollectionTitle && selectedCollectionTitle !== 'Archived / Obsolete') return { collection: selectedCollectionTitle };
    return {};
  }, [id, navMode, selectedUseCaseId, selectedCollectionTitle]);

  const leaderboard = useOp('lab_leaderboard', leaderboardReq);
  const approvedExamples = useOp('lab_examples_list', !id && navMode === 'use-cases' ? { state: 'approved' } : null);

  useEffect(() => {
    if (!leaderboard.data || selectedUseCaseId || selectedCollectionTitle) return;
    const next: Record<string, number> = {};
    for (const entry of leaderboard.data.entries) {
      if (entry.scoreMean == null) continue;
      const cur = next[entry.useCaseId];
      if (cur === undefined || entry.scoreMean > cur) next[entry.useCaseId] = entry.scoreMean;
    }
    setBestScoresByUseCase(next);
  }, [leaderboard.data, selectedUseCaseId, selectedCollectionTitle]);

  const now = Date.now();
  const rawExperiments = experiments.data?.experiments ?? [];
  const rawUseCases: RegistryUseCase[] = registry.data?.useCases ?? [];
  const rawRatings = ratings.data?.ratings ?? [];
  const rawExamples = approvedExamples.data?.examples ?? [];

  const { nodes: useCaseTreeNodes } = useMemo(
    () => buildUseCaseTree(rawUseCases, {
      pinnedIds: ucPins.ids,
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

  const runTreeNodes = useMemo<CatalogueTreeNode[]>(() => {
    const folders: CatalogueTreeNode[] = [];
    for (const f of RUN_FOLDERS) {
      const items = sortedExperiments.filter(f.match);
      if (!items.length) continue;
      const containsSelected = !!id && items.some(e => e.id === id);
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
          href: `/lab/${e.id}`,
          pinned: runPins.has(e.id),
          problem: undecided(e),
          right: <>{undecided(e) && <Icon name="bell" label="Undecided" />}<AgeChip t={ms(e.createdAt)} /></>,
          searchText: [e.id, e.labGroup, e.goal, e.pipelineId, e.kind, e.state, ...e.variants.map(v => v.label)].filter(Boolean).join(' '),
        })),
      });
    }
    return folders;
  }, [sortedExperiments, id, runPins.ids]);

  if (!experiments.data) return <Empty text="Loading…" />;

  const data = { experiments: rawExperiments, useCases: rawUseCases, ratings: rawRatings }, state = experiments.state;
  const approvals = data.ratings.filter(r => r.exampleArtifactId), open = data.ratings.filter(r => r.experimentId);

  const ucFlipped = listGroups('lab:use-cases');
  const ucClosed = closedGroups([ARCHIVED_COLLECTION_ID], ucFlipped);
  const ucCollapsed: Record<string, boolean> = {
    __pinned__: ucClosed.has('__pinned__'),
    ...Object.fromEntries(useCaseTreeNodes.map(n => [n.id, ucClosed.has(n.id)])),
  };

  const defaultRunClosed = RUN_FOLDERS
    .filter(f => f.defaultCollapsed && !(id && sortedExperiments.some(e => e.id === id && f.match(e))))
    .map(f => f.id);
  const runFlipped = listGroups('lab:runs');
  const runClosed = closedGroups(defaultRunClosed, runFlipped);
  const runCollapsed: Record<string, boolean> = {
    __pinned__: runClosed.has('__pinned__'),
    ...Object.fromEntries(runTreeNodes.map(n => [n.id, runClosed.has(n.id)])),
  };

  const handleUseCaseSelect = (node: CatalogueTreeNode) => {
    if (node.id === '__pinned__') {
      setListGroups('lab:use-cases', flipGroup(ucFlipped, '__pinned__'));
      return;
    }
    if (node.children) {
      setSelectedFolderId(node.id);
      setSelectedUseCaseId(null);
      if (id) go('/lab');
      return;
    }
    setSelectedUseCaseId(node.id);
    setSelectedFolderId(null);
    if (id) go('/lab');
  };

  const handleRunSelect = (node: CatalogueTreeNode) => {
    if (node.id === '__pinned__') {
      setListGroups('lab:runs', flipGroup(runFlipped, '__pinned__'));
      return;
    }
    if (node.children) {
      setSelectedRunFolderId(prev => (prev === node.id ? null : node.id));
      if (id) go('/lab');
      return;
    }
    go(`/lab/${node.id}`);
  };

  const menuItems: MenuItem[] = !treeMenu ? [] : treeMenu.kind === 'use-case'
    ? (() => {
        const u = rawUseCases.find(x => x.id === treeMenu.node.id);
        const pinned = ucPins.has(treeMenu.node.id);
        const items: MenuItem[] = [
          { label: 'Open', icon: actions.open, onClick: () => handleUseCaseSelect(treeMenu.node) },
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
          { label: 'Open', icon: actions.open, onClick: () => go(`/lab/${treeMenu.node.id}`) },
          { label: pinned ? 'Unpin' : 'Pin', icon: pinned ? actions.unpin : actions.pin, onClick: () => runPins.toggle(treeMenu.node.id) },
        ];
      })();

  // Control column: mode switch, search, Waiting for you, and CatalogueTree (one scroller, OR-25).
  const list = <>
    <ShellStatus><OfflineMarker state={state} now={now} /></ShellStatus>
    <header class="page-h">
      <Choice
        label="Lab navigation"
        value={navMode}
        onChange={v => {
          const next = v as 'use-cases' | 'runs';
          setNavMode(next);
          setCtlQuery('');
        }}
        options={[{ id: 'use-cases', label: 'Use cases' }, { id: 'runs', label: 'Runs' }]}
      />
      <SearchField label={navMode === 'use-cases' ? 'Search use cases' : 'Search runs'} value={ctlQuery} onInput={setCtlQuery} onSearch={() => {}} />
      <span class="grow" />
      <Button label="New experiment" icon="plus" iconOnly onClick={() => go('/lab/new')} />
    </header>
    {(approvals.length > 0 || open.length > 0) && <section class="panel"><header class="panel-h"><Icon name="bell" /><h2>Waiting for you</h2></header>
      {open.length > 0 && <div class="vl-row static"><span>{open.length} open ratings</span></div>}
      {approvals.map(r => <Rating key={r.id} r={r} now={now} />)}
    </section>}
    <section class="panel">
      {navMode === 'use-cases' ? (
        <CatalogueTree
          fill
          label="Use cases"
          nodes={useCaseTreeNodes}
          selectedId={!id ? (selectedUseCaseId ?? selectedFolderId) : null}
          query={ctlQuery}
          collapsed={ucCollapsed}
          onToggle={folderId => setListGroups('lab:use-cases', flipGroup(ucFlipped, folderId))}
          onPin={node => ucPins.toggle(node.id)}
          onSelect={handleUseCaseSelect}
          onRowMenu={(node, at) => { if (!node.children) setTreeMenu({ kind: 'use-case', node, at }); }}
          emptyText={rawUseCases.length === 0 ? 'No use cases in registry.' : 'No matching use cases.'}
        />
      ) : (
        <CatalogueTree
          fill
          label="Experiments"
          nodes={runTreeNodes}
          selectedId={id ?? selectedRunFolderId}
          query={ctlQuery}
          collapsed={runCollapsed}
          onToggle={folderId => setListGroups('lab:runs', flipGroup(runFlipped, folderId))}
          onPin={node => runPins.toggle(node.id)}
          onSelect={handleRunSelect}
          onRowMenu={(node, at) => { if (!node.children) setTreeMenu({ kind: 'run', node, at }); }}
          emptyText={data.experiments.length === 0 ? 'No experiments yet. Choose New experiment to create a draft.' : 'No matching runs.'}
        />
      )}
    </section>
    {treeMenu && <ContextMenu at={treeMenu.at} label={treeMenu.node.label} items={menuItems} onClose={() => setTreeMenu(null)} />}
  </>;

  const selectedUseCase = selectedUseCaseId ? rawUseCases.find(u => u.id === selectedUseCaseId) ?? null : null;
  const activeRunFolder = selectedRunFolderId ? RUN_FOLDERS.find(f => f.id === selectedRunFolderId) : null;
  const shownExperiments = activeRunFolder ? data.experiments.filter(activeRunFolder.match) : data.experiments;

  const idle = navMode === 'use-cases'
    ? <article class="detail" aria-label={selectedUseCase ? selectedUseCase.goal : (selectedCollectionTitle ?? 'Lab suite leaderboard')}>
        <Undecided experiments={data.experiments} onOpen={x => go(`/lab/${x}`)} />
        {selectedUseCase && (
          <UseCaseCard
            useCase={selectedUseCase}
            examples={rawExamples}
            onClose={() => setSelectedUseCaseId(null)}
            onRun={u => go(`/lab/new?useCase=${encodeURIComponent(u)}`)}
          />
        )}
        <SuiteLeaderboard
          scopeLabel={selectedUseCase ? selectedUseCase.id : (selectedCollectionTitle ?? 'All suites')}
          entries={leaderboard.data?.entries ?? []}
          loading={!leaderboard.data && !leaderboard.error}
          error={leaderboard.error}
          onRetry={leaderboard.reload}
          showUseCase={!selectedUseCase}
          onOpenExperiment={x => go(`/lab/${x}`)}
          onOpenPipeline={p => go(`/pipelines/${p}`)}
          onSelectUseCase={u => { setSelectedUseCaseId(u); setSelectedFolderId(null); }}
        />
        {!selectedUseCase && (
          <Registry
            useCases={rawUseCases}
            collectionTitle={selectedCollectionTitle}
            onSelect={u => { setSelectedUseCaseId(u); setSelectedFolderId(null); }}
            onRun={u => go(`/lab/new?useCase=${encodeURIComponent(u)}`)}
          />
        )}
      </article>
    : <article class="detail" aria-label="Lab questions">
        <Questions experiments={shownExperiments} onOpen={x => go(`/lab/${x}`)} />
      </article>;

  return <ListDetail list={list} detail={id ? <Experiment key={id} id={id} onClose={() => go('/lab')} /> : undefined} detailKey={id} idle={idle} />;
}

function Questions({ experiments, onOpen }: { experiments: LabExperimentRecord[]; onOpen: (id: string) => void }) {
  const groups = new Map<string, LabExperimentRecord[]>();
  for (const e of experiments) {
    const key = e.labGroup || 'Ungrouped';
    const items = groups.get(key);
    if (items) items.push(e);
    else groups.set(key, [e]);
  }
  const ordered = [...groups.entries()].sort((a, b) => Math.max(...b[1].map(e => ms(e.launchedAt ?? e.createdAt))) - Math.max(...a[1].map(e => ms(e.launchedAt ?? e.createdAt))));
  return <>
    <Undecided experiments={experiments} onOpen={onOpen} />
    {ordered.length === 0 && <Empty text="No experiment questions yet" />}
    {ordered.map(([group, items]) => {
      const sorted = items.toSorted((a, b) => ms(b.launchedAt ?? b.createdAt) - ms(a.launchedAt ?? a.createdAt));
      const latestGoal = sorted.find(e => e.goal)?.goal;
      const latestHyp = sorted.find(e => e.hypothesis)?.hypothesis;
      return <DetailSection key={group} title={group}>
        {latestGoal && <KvRow icon="circle-question-mark" label={latestGoal} meta={latestHyp ?? undefined} />}
        <DataTable label={`${group} experiments`} head={<tr><th>Experiment</th><th>State</th><th>Goal</th><th>Decision</th><th /></tr>}>
          {sorted.map(e => <tr key={e.id}>
            <td><EntityCode id={e.id} kind="experiment" /><div class="muted">{subtitle(e)}</div></td>
            <td><StateBadge family="experiment" state={e.state as StateOf<'experiment'>} reason={e.stopReason ?? undefined} compact /></td>
            <td>{e.goal ?? title(e)}</td>
            <td>{e.decision && e.decision !== 'pending' ? <><b>{e.decision}</b>{e.decisionReason && <div class="muted">{e.decisionReason}</div>}</> : <span class="muted">Undecided</span>}</td>
            <td><Button label="Open" kind="quiet" onClick={() => onOpen(e.id)} /></td>
          </tr>)}
        </DataTable>
      </DetailSection>;
    })}
  </>;
}

const title = (e: LabExperimentRecord) => `${e.pipelineId} · ${e.kind === 'ab' ? 'A/B' : e.kind} · ${e.variants.map(v => v.label).join(' vs ')}`;
const subtitle = (e: LabExperimentRecord) => `${e.kind === 'ab' ? 'A/B' : e.kind} · ${e.variants.length > 2 ? `${e.variants.length} variants` : e.variants.map(v => v.label).join(' vs ')}`;
/** A finished experiment nobody has decided yet (decision pending). */
const undecided = (e: LabExperimentRecord) => e.state === 'done' && e.decision === 'pending';

// What the Lab waits for, above the registry while no experiment is open (2026-11-20 decision): finished experiments
// without a decision, newest first.
function Undecided({ experiments, onOpen }: { experiments: LabExperimentRecord[]; onOpen: (id: string) => void }) {
  const waiting = experiments.filter(undecided).sort((a, b) => ms(b.launchedAt ?? b.createdAt) - ms(a.launchedAt ?? a.createdAt));
  if (!waiting.length) return null;
  const series = new Set(waiting.map(e => e.labGroup || e.pipelineId)).size;
  return <>
    <ProblemRow severity="warning" title={`${waiting.length} finished ${waiting.length === 1 ? 'experiment waits' : 'experiments wait'} for a decision`}
      facts={`in ${series} series`} actions={[{ label: 'Open the newest', primary: true, onClick: () => onOpen(waiting[0].id) }]} />
    <DetailSection title="Newest undecided">{waiting.slice(0, 5).map(e => <KvRow key={e.id} icon="chart-no-axes-column" label={e.goal ?? title(e)}
      meta={`${e.labGroup || e.pipelineId} · ${subtitle(e)}`} value={<Button label="Open" kind="quiet" onClick={() => onOpen(e.id)} />} />)}</DetailSection>
  </>;
}

function UseCaseCard({
  useCase,
  examples,
  onClose,
  onRun,
}: {
  useCase: RegistryUseCase;
  examples: readonly { artifactId: string; name: string }[];
  onClose: () => void;
  onRun: (useCaseId: string) => void;
}) {
  const slug = useCaseSlug(useCase.id);
  const matched = examples.filter(ex => matchesUseCaseSlug(ex.name, slug));
  const eligible = isLabEligibleUseCase(useCase);
  const readiness = matched.length > 0
    ? `${matched.length} approved (${matched.slice(0, 4).map(e => e.name).join(', ')}${matched.length > 4 ? `, +${matched.length - 4}` : ''})`
    : `0 approved matching ${slug} (maturity: ${useCase.maturity})`;
  return (
    <section class="panel" aria-label={`Use case ${useCase.id}`}>
      <header class="panel-h">
        <Button label="All suites" icon="x" iconOnly onClick={onClose} />
        <StateBadge
          family="source"
          state={(useCase.source_status || 'current') as StateOf<'source'>}
          reason={useCase.source ? `${useCase.source.repo}/${useCase.source.path}#${useCase.source.anchor}` : undefined}
        />
        <span class="id mono">{useCase.id}</span>
        <span class="grow" />
        {eligible && <Button label="Run in Lab" icon="play" kind="primary" onClick={() => onRun(useCase.id)} />}
      </header>
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

type LeaderboardSortCol = 'variant' | 'pipeline' | 'score' | 'runs' | 'wall' | 'tokens' | 'cost';
const lbCollator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

function SuiteLeaderboard({
  scopeLabel,
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

  const toggleSort = (col: LeaderboardSortCol) => {
    if (sortCol === col) {
      setSortDir(d => (d === 'ascending' ? 'descending' : 'ascending'));
    } else {
      setSortCol(col);
      setSortDir(col === 'variant' || col === 'pipeline' || col === 'wall' || col === 'cost' ? 'ascending' : 'descending');
    }
  };

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
        case 'tokens': return cmpNum(a.tokensTotal, b.tokensTotal);
        case 'cost': return cmpNum(a.costUsd, b.costUsd);
      }
    });
  }, [entries, sortCol, sortDir]);

  const sortStatus = sortCol ? `Sorted by ${sortCol}, ${sortDir}, ${sorted.length} rows` : '';

  return (
    <section class="panel" aria-label={`Suite leaderboard: ${scopeLabel}`}>
      <header class="panel-h">
        <Icon name="chart-no-axes-column" />
        <h2>Suite leaderboard</h2>
        <span class="muted">{scopeLabel}</span>
        <span class="grow" />
        <span class="muted mono">{entries.length}</span>
      </header>
      <span class="sr-only" role="status">{sortStatus}</span>
      {error && <ErrorBanner text={error} onRetry={onRetry} />}
      {loading && !error ? (
        <Empty text="Loading leaderboard…" />
      ) : sorted.length === 0 ? (
        <Empty text={`No completed leaderboard trials for ${scopeLabel} yet.`} />
      ) : (
        <DataTable
          label={`Suite leaderboard (${scopeLabel})`}
          head={
            <tr>
              <SortColHead label="Variant" active={sortCol === 'variant'} dir={sortDir} onSort={() => toggleSort('variant')} />
              <SortColHead label="Pipeline" active={sortCol === 'pipeline'} dir={sortDir} onSort={() => toggleSort('pipeline')} />
              <th scope="col">Model / Profile</th>
              <th scope="col">Evaluator</th>
              <SortColHead label="Score" active={sortCol === 'score'} dir={sortDir} numeric onSort={() => toggleSort('score')} />
              <SortColHead label="Runs" active={sortCol === 'runs'} dir={sortDir} numeric onSort={() => toggleSort('runs')} />
              <SortColHead label="Median time" active={sortCol === 'wall'} dir={sortDir} numeric onSort={() => toggleSort('wall')} />
              <SortColHead label="Tokens" active={sortCol === 'tokens'} dir={sortDir} numeric onSort={() => toggleSort('tokens')} />
              <SortColHead label="Cost" active={sortCol === 'cost'} dir={sortDir} numeric onSort={() => toggleSort('cost')} />
              <th scope="col">Latest</th>
            </tr>
          }
        >
          {sorted.map((entry, idx) => {
            const key = `${entry.useCaseId}|${entry.pipelineId}|${entry.variantLabel}|${entry.model ?? ''}|${entry.profile ?? ''}|${entry.configRef ?? ''}|${entry.evaluator}|${idx}`;
            const modelText = entry.model ? agentLabel(entry.model) : '—';
            const rangeText = entry.scoreMin != null && entry.scoreMax != null && entry.scoreMin !== entry.scoreMax
              ? ` (${entry.scoreMin.toFixed(2)}–${entry.scoreMax.toFixed(2)})`
              : '';
            const runsTip = `${entry.succeededTrials} succeeded, ${entry.failedTrials} failed of ${entry.trials} trials`;
            const modelTip = [entry.model, entry.profile, entry.configRef].filter(Boolean).join(' · ') || undefined;
            return (
              <tr key={key}>
                <th scope="row">
                  <div>{entry.variantLabel || 'baseline'}</div>
                  {showUseCase && (
                    <div class="muted">
                      <Button label={entry.useCaseId} kind="quiet" rowLink onClick={() => onSelectUseCase(entry.useCaseId)} />
                    </div>
                  )}
                </th>
                <td>
                  <a href={`/pipelines/${entry.pipelineId}`} onClick={ev => { ev.preventDefault(); onOpenPipeline(entry.pipelineId); }}>
                    {entry.pipelineId}
                  </a>
                </td>
                <td data-tip={modelTip}>
                  <div>{modelText}</div>
                  {entry.profile && <div class="muted">{entry.profile}</div>}
                </td>
                <td>{evaluatorLabel(entry.evaluator)}</td>
                <td class="num">
                  <Num m={{ value: entry.scoreMean, basis: entry.scoreMean == null ? 'unavailable' : 'measured' }} kind="score" />
                  {rangeText && <span class="muted">{rangeText}</span>}
                </td>
                <td class="num" data-tip={runsTip}>
                  {entry.succeededTrials}/{entry.trials}
                </td>
                <td class="num">
                  <Num m={msMeasured(entry.wallMsMedian)} kind="duration" />
                </td>
                <td class="num">
                  <Num m={{ value: entry.tokensTotal, basis: entry.tokensTotal > 0 || entry.trials === 0 ? 'measured' : 'unavailable', reason: 'no token usage' }} kind="count" />
                </td>
                <td class="num">
                  <Num m={{ value: entry.costUsd, basis: costBasisOf(entry.costBasis), currency: 'USD', reason: entry.costBasis }} kind="money" />
                  <span class="muted"> {entry.costBasis}</span>
                </td>
                <td>
                  <span class="deps">
                    <EntityCode id={entry.latestExperimentId} kind="experiment" />
                    <Button label="Open" kind="quiet" onClick={() => onOpenExperiment(entry.latestExperimentId)} />
                    <AgeChip t={ms(entry.latestAt)} />
                  </span>
                </td>
              </tr>
            );
          })}
        </DataTable>
      )}
    </section>
  );
}

// The use-case registry fills the work area below the leaderboard while no specific use case or experiment is open.
function Registry({
  useCases,
  collectionTitle,
  onSelect,
  onRun,
}: {
  useCases: RegistryUseCase[];
  collectionTitle: string | null;
  onSelect: (useCaseId: string) => void;
  onRun: (useCaseId: string) => void;
}) {
  const { collections, archived } = groupUseCases(useCases);
  const shownCollections = collectionTitle && collectionTitle !== 'Archived / Obsolete'
    ? collections.filter(c => c.title === collectionTitle)
    : collectionTitle === 'Archived / Obsolete'
      ? []
      : collections;

  const row = (u: RegistryUseCase) => (
    <div key={u.id} role="row" class="vl-row static">
      <StateBadge family="source" state={u.source_status as StateOf<'source'>} reason={u.source ? `${u.source.repo}/${u.source.path}#${u.source.anchor}` : undefined} compact />
      <span class="tt">
        <Button label={u.goal} rowLink kind="quiet" onClick={() => onSelect(u.id)} />
        <span class="id">{u.id}{u.maps_to?.length ? ` → ${u.maps_to.join(', ')}` : ''}</span>
      </span>
      <span class="muted">{u.status ?? 'current'}</span>
      <span class="muted">{u.lab_fit}</span>
      <span class="muted">{u.maturity}</span>
      {isLabEligibleUseCase(u) ? <Button label="Run in Lab" icon="play" kind="quiet" tip={`Run ${u.id} in Lab`} onClick={() => onRun(u.id)} /> : <span />}
    </div>
  );

  const table = (items: RegistryUseCase[]) => (
    <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) 6rem 5rem 9rem auto' }}>
      <div class="vl-head" role="row"><span /><span>Use case</span><span>Status</span><span>Fit</span><span>Maturity</span><span /></div>
      {items.map(row)}
    </div>
  );

  return <>
    <h2 class="detail-title">{collectionTitle ?? 'Use-case registry'}</h2>
    {useCases.length === 0 && <Empty text="No use cases in registry." />}
    {shownCollections.map(c => <DetailSection key={c.key} title={`${c.title} (${c.items.length})`}>{table(c.items)}</DetailSection>)}
    {collectionTitle === 'Archived / Obsolete' && archived.length > 0 && (
      <DetailSection title={`Archived / Obsolete (${archived.length})`}>{table(archived)}</DetailSection>
    )}
    {!collectionTitle && archived.length > 0 && (
      <Disclosure summaryClass="muted" summary={`Archived / Obsolete (${archived.length})`}>{table(archived)}</Disclosure>
    )}
  </>;
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

function Experiment({ id, onClose }: { id: string; onClose: () => void }) {
  const exp = useOp('lab_experiment_get', { experimentId: id }, { every: 5000 }), rated = useOp('lab_ratings_list', { experimentId: id }, { every: 5000 });
  const e = exp.data, state = exp.state, ratings = rated.data?.ratings ?? [];
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
  useEffect(() => { if (e && location.hash) document.getElementById(location.hash.slice(1))?.scrollIntoView(); }, [!!e]);
  const now = Date.now();
  if (!e) return <article class="detail" data-ui="detail" tabIndex={-1}><Empty text="Loading…" /></article>;
  const act = <K extends OpName>(name: K, body: Ops[K]['request']): Promise<Ops[K]['reply'] | undefined> => op(name, body).then(r => { const w = (r as { warnings?: string[] }).warnings; setMsg(w?.length ? { text: w.join('; '), tone: 'warning' } : null); return r; }).catch((x: unknown) => { setMsg({ text: message(x), tone: 'failure' }); return undefined; });
  const trial = (key: string) => e.trials.filter(t => `${t.cell.example}|${t.cell.variant}|${t.cell.repeat}` === key).sort((a, b) => (b.cell.attempt ?? 0) - (a.cell.attempt ?? 0))[0];
  const toggle = (t: string) => setSel(s => s.includes(t) ? s.filter(x => x !== t) : [...s, t]);
  const rate = (form: Ops['lab_rating_create']['request']['form']) => act('lab_rating_create', { experimentId: id, form, trialIds: sel }).then(r => { if (r?.id) { setSel([]); location.hash = 'rating-' + r.id; } });
  const budgetOf = (unit: string) => e.budgets.find(b => b.unit === unit);
  const note = (unit: string) => { const b = budgetOf(unit); return b ? `of ${b.limit} · ${b.action}${b.firedAt ? ' · reached' : ''}` : undefined; };
  const record = (field: string) => field === 'goal' ? e.goal : field === 'hypothesis' ? e.hypothesis : undefined;
  const cols = `minmax(8rem,14rem) ${e.variants.map(() => 'minmax(12rem,1fr)').join(' ')}`;
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
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={title(e)}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /><StateBadge family="experiment" state={e.state as StateOf<'experiment'>} reason={e.stopReason ?? undefined} /><EntityCode id={e.id} kind="experiment" /><span class="grow" />
      {e.state === 'draft' && <Button label="Launch" icon="play" kind="primary" disabledReason={savingRecord ? 'Saving experiment record' : Object.values(recordErrors).some(Boolean) || Object.entries(recordDraft).some(([field, value]) => value !== String(savedRecord[field] ?? record(field) ?? '')) ? 'Save the experiment record successfully before launch' : !String(savedRecord.goal ?? e.goal ?? '').trim() || !String(savedRecord.hypothesis ?? e.hypothesis ?? '').trim() ? 'Save a goal and hypothesis before launch' : undefined} onClick={() => act('lab_experiment_action', { experimentId: id, action: 'launch' })} />}
      {e.state === 'paused' && <Button label="Resume" icon="play" kind="primary" onClick={() => act('lab_experiment_action', { experimentId: id, action: 'resume' })} />}
      {(e.state === 'running' || e.state === 'paused') && <Button label="Stop" icon="square" kind="danger" onClick={() => act('lab_experiment_action', { experimentId: id, action: 'stop' })} />}
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
    <Totals items={[
      { label: 'Trials', m: { value: e.spend.trials.value, basis: 'measured' }, kind: 'count', note: note('trials') },
      { label: 'Wall time', m: { value: e.spend.wall_ms.value / 1000, basis: 'measured' }, kind: 'duration', note: note('wall_ms') },
      { label: 'API calls', m: { value: e.spend.calls.value, basis: 'measured' }, kind: 'count', note: note('calls') },
      { label: 'Credits', m: { value: e.spend.credits.value, basis: 'measured', partial: e.spend.credits.unmeasured ? `${e.spend.credits.unmeasured} calls without usage` : undefined }, kind: 'count', note: note('credits') },
    ]} />
    <section class="panel"><header class="panel-h"><Icon name="rows-3" /><h2>Matrix</h2><span class="muted">{e.examples.length} × {e.variants.length} × {e.repeats}</span><span class="grow" />
      {sel.length === 1 && <><Button label="Pass or fail" icon="check" onClick={() => rate('pass_fail')} /><Button label="Scale 1–5" icon="sliders-horizontal" onClick={() => rate('scale')} /></>}
      {sel.length >= 2 && <Button label={`Pick best of ${sel.length}`} icon="check-check" onClick={() => rate('pick_best')} />}</header>
      <div class="vl-wrap" style={{ '--cols': cols }}>
        <div class="vl-head" role="row"><span>Example</span>{e.variants.map(v => <span key={v.label}>{v.label}</span>)}</div>
        {e.examples.map(p => <div key={p.id} role="row" class="vl-row static lab-row">
          <span class="title">{p.id}<span class="id">{p.split}</span></span>
          {e.variants.map(v => { const c = e.cells.find(x => x.example === p.id && x.variant === v.label);
            return <div key={v.label} class="lab-cell">
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
                    {t.recovery && <span data-tip={`Reuses ${(t.recovery.reusedStepIds ?? []).join(', ')}; original spend remains in source history`}><Button label="Source run" icon="network" kind="quiet" onClick={() => setPlanTrial(t.recovery?.sourceRunId)} /></span>}
                    {(t.cost ?? []).map((c, i) => <Num key={i} m={{ value: c.value, basis: costBasisOf(c.basis), currency: c.currency ?? undefined, reason: c.value === null ? c.reason : `${c.basis} · ${c.prices?.length ?? 0} price${c.prices?.length === 1 ? '' : 's'}` }} kind="money" />)}
                    {t.scores.filter(s => s.trialIds?.length === 1).map(s => s.form === 'typed'
                      ? <span key={s.id} data-tip={`${evaluatorLabel(s.evaluator ?? '')}${s.rationale ? ": " + s.rationale : ""}`}><Icon name={typedIcon(s.value)} label={`${evaluatorLabel(s.evaluator ?? '')} ${String(typedValue(s.value).answer)}, label ${String(typedValue(s.value).label ?? 'none')}`} />{String(typedValue(s.value).answer)}</span>
                      : <span key={s.id} data-tip={`${evaluatorLabel(s.evaluator ?? '')}${s.rationale ? ': ' + s.rationale : ''}`}><Icon name={s.form === 'pass_fail' ? (s.value ? 'circle-check' : 'circle-x') : 'sliders-horizontal'} label={`${evaluatorLabel(s.evaluator ?? '')} ${String(s.value)}`} />{s.form === 'pass_fail' ? '' : String(s.form === 'metric' ? Number(s.value).toFixed(2) : s.value)}</span>)}
                    {t.state === 'running' ? <Button label="Cancel cell" icon="x" iconOnly kind="quiet" onClick={() => act('lab_cell_action', { experimentId: id, cell: key, action: 'cancel' })} />
                      : <Button label="Retry cell" icon="rotate-ccw" iconOnly kind="quiet" onClick={() => act('lab_cell_action', { experimentId: id, cell: key, action: 'retry' })} />}
                  </span>
                  {problem && <span class="reason" data-tip={problem}>{e.repeats > 1 ? `${repeatTag}: ` : ''}{problem}</span>}
                </Fragment>; })}
            </div>; })}
        </div>)}
      </div>
    </section>
    {planTrial && <TrialPlan key={planTrial} id={planTrial} projectId={e.projectId} trialProblem={problemText(e.trials.find(t => t.id === planTrial)?.problem)} assessments={e.trials.find(t => t.id === planTrial)?.projectAssessments ?? []} onOpen={setPlanTrial} onClose={() => setPlanTrial(undefined)} />}
    <section class="panel"><header class="panel-h"><Icon name="sliders-horizontal" /><h2>Variants</h2></header>
      <DetailSection title="Spread across repeats">{e.variants.map(v => <KvRow key={v.label} icon="git-merge" label={v.proposalLabel ? `${v.proposalLabel} · ${v.label}` : v.label ?? ''}
        meta={<><span>{Object.entries(v.summary ?? {}).map(([k, s]) => `${evaluatorLabel(k)} ${scoreText(s)}`).join(' · ') || 'no scores yet'}</span><span> · Pipeline: <a href={`/pipelines/${v.pipelineId ?? e.pipelineId}`}>{v.pipelineId ?? e.pipelineId}</a></span><span class="id"> · Executable digest: {v.config_ref}</span>{v.catalogueConfigRef && <span class="id"> · Catalogue identity: {v.catalogueConfigRef}</span>}{v.catalogueRef && <span> · <a href={`/catalogue/${v.catalogueRef.id}`}>Catalogue r{v.catalogueRef.revision}</a></span>}</>}
        value={<Button label="Promote" icon="check-check" onClick={() => act('lab_promote', { experimentId: id, variant: v.label ?? '' }).then(r => setPatch(r ?? null))} />} />)}</DetailSection>
      {patch && <DetailSection title="Promote"><KvRow icon="check" label={`Recommendation set to ${patch.method.id ?? ''}`}
        meta={patch.catalogue ? <span class="mono">catalogue r{patch.catalogue.revision}</span> : 'method already in the catalogue'} />
        {patch.note && <KvRow icon="circle-alert" label={String(patch.note)} />}</DetailSection>}
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
  const { data: run, state, error } = useOp('pipeline_get', { runId: id, projectId }, { every: 5000 });
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
    {(run?.plan.entries ?? []).filter(entry => entry.executionRef?.mode === 'reused').map(entry => <KvRow key={entry.id} icon="network" label={`${entry.id} · reused source`} value={<Button label="Open source run" icon="network" kind="quiet" onClick={() => onOpen(entry.executionRef!.sourceRunId!)} />} />)}
    {run?.canonicalResult && <KvRow icon="file-text" label="Canonical result" value={<a href={`/api/results/${run.canonicalResult.artifact.artifactId}`} target="_blank" rel="noopener">Open result</a>} />}
    {assessments.map(assessment => <KvRow key={`${assessment.checkId}:${assessment.evaluatorSha256}:${assessment.inputSha256}`} icon="file-text" label={`Project assessment · ${assessment.checkId}`} meta={assessment.schemaId} value={<a href={`/api/results/${assessment.artifactId}`} target="_blank" rel="noopener">Open assessment</a>} />)}
  </DetailSection>;
}
