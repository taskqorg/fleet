// Lab (docs/designs/fleet-lab.md "/lab screen", docs/design-system/pages/laboratory.md#target): one CatalogueTree with two
// roots (Use cases, Runs) under saved views in the control column; on /lab what waits for the person as question cards,
// then the Model × Pipeline comparison (lab-matrix.tsx); UseCaseDetail, SuiteDetail, LabCreate and Experiment in the
// work area via /lab/:id prefixes.
import { createEffect, createMemo, createSignal, For, Match, Show, Switch } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { useLocation, useSearch } from '../router.ts';
import {
  StateBadge, Button, CopyButton, EntityCode, Num, Totals, DetailSection, KvRow, DecisionBlock, AnswerNote, ProblemRow,
  RowCheck, VirtualList, Empty, OfflineMarker, Icon, ListDetail, DataTable, Choice, TextField,
  SearchField, CatalogueTree, ContextMenu, ErrorBanner, Skeleton, ROW2, percent, SettingField, StepList, msMeasured,
  type StepItem, type StateOf, type Basis, type CatalogueTreeNode, type MenuItem,
  type IconName, ShellStatus, ShellActions, actions, Presets, QuestionCard, DetailHead, StateDot, MatrixCell, DiffRow,
  InheritedRows, AgeChip, cause, finishedStates, type Life, type MatrixCellState, type View,
  timeOf, basisOf,
} from '../../../packages/ui/src/index.ts';
import { op, useAlive, type OpName, type Ops } from '../api.ts';
import { useAnswers } from '../answers.ts';
import { HistoryPreview } from './history.tsx';
import { useOp, useView } from '../store.ts';
import { closedGroups, flipGroup } from '../lists.ts';
import { listGroups, setListGroups, usePins, useViews } from '../prefs.ts';
import { LabCreate } from './lab-create.tsx';
import { LabComparison } from './lab-matrix.tsx';
import { selectDefaultSwarmTrial } from './lab-swarm.ts';
import {
  ARCHIVED_COLLECTION_ID, USE_CASE_COLLECTION_PREFIX, buildUseCaseTree, collectionIconOf, collectionTitleOf,
  groupUseCases, isLabEligibleUseCase, matchingExamplesForUseCase, resolveSuiteKey, type RegistryUseCase, type SuiteKey,
} from './use-cases.ts';
import type {
  LabExampleEntry, LabExperimentRecord, LabRating, LabScoreSummary, LabVariant, ProjectAssessmentRef,
} from '../../../contracts/generated/ops.ts';
import { SwarmPanel, hasSwarmBoard, useSwarmBoard } from './swarm-panel.tsx';

/** Times for sorting and ages: a missing time is the oldest. */
const ms = (t: string | number | null | undefined) => timeOf(t, 0);
const message = (x: unknown) => x instanceof Error ? x.message : String(x);
const fixed = (x: number | null) => x === null ? '—' : x.toFixed(2);
const decodeSafe = (s: string): string => { try { return decodeURIComponent(s); } catch { return s; } };

const TERMINAL_STATES = new Set<string>([...finishedStates.experiment, ...finishedStates.execution]);

/**
 * A trial or strategy problem in words (formats.md, causes): a stored `code` or `code: detail` reads as its cause's first
 * sentence and the detail; an object reads its message, else its code the same way. The code itself is never the text.
 */
const problemText = (p: unknown): string | undefined => {
  if (p == null) return undefined;
  const coded = (v: string) => {
    const t = v.trim(), i = t.indexOf(': '), detail = i < 0 ? '' : t.slice(i + 2).trim();
    if (!t) return undefined;
    const head = cause(t).text.split('. ')[0].replace(/\.$/, ''); // the whole string: some matches span ': '
    return detail ? `${head}: ${detail}` : head;
  };
  if (typeof p === 'string') return coded(p);
  if (typeof p === 'object') {
    const r = p as { code?: unknown; message?: unknown; reason?: unknown; error?: unknown };
    const msg = [r.message, r.reason, r.error].find((x): x is string => typeof x === 'string' && x.trim().length > 0);
    if (msg) return msg.trim();
    if (typeof r.code === 'string') return coded(r.code);
  }
  return undefined;
};

/** A typed score's value: the answer given and the example's label. */
const typedValue = (v: unknown): { answer?: unknown; label?: unknown } => v && typeof v === 'object' ? v as { answer?: unknown; label?: unknown } : {};

// typedIcon: a typed score at a glance: right, wrong, escalated (middle band) or not scorable.
const typedIcon = (value: unknown) => { const v = typedValue(value); return v.answer === 'unscored' ? 'circle-alert' : v.answer === 'abstain' ? 'circle-pause' : v.answer === v.label ? 'circle-check' : 'circle-x'; };

const costBasisOf = basisOf;

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

// Saved views of the Lab catalogue (OR-33). "Mine" replaces Personal Lab: the runs the person requested. "Waiting for a
// decision": finished runs nobody has decided. A person's own views keep the search query.
const VIEW_ALL = 'All', VIEW_MINE = 'Mine', VIEW_WAITING = 'Waiting for a decision', VIEW_HISTORY = 'History';
const LAB_VIEWS = [VIEW_ALL, VIEW_MINE, VIEW_WAITING, VIEW_HISTORY].map(name => ({ name, view: { view: 'lab', preset: name } as unknown as View }));
const VIEW_TIPS: Record<string, string> = {
  [VIEW_ALL]: 'Every use case and run',
  [VIEW_MINE]: 'Runs you requested, and your Personal Lab drafts',
  [VIEW_WAITING]: 'Finished runs that wait for your decision',
  [VIEW_HISTORY]: "Lab's history: experiments and trials, newest first",
};

/** An experiment's state in the one state vocabulary (OR-55): a finished, undecided run waits for the person. */
const lifeOfExperiment = (e: Pick<LabExperimentRecord, 'state' | 'decision'>): Life =>
  e.state === 'running' ? 'working' : e.state === 'paused' ? 'waiting' : e.state === 'draft' ? 'queued'
    : e.state === 'stopped' ? 'stopped' : e.state === 'done' && e.decision === 'pending' ? 'waiting' : 'done';

export function Lab(props: { id?: string }) {
  const [, go] = useLocation();
  const search = useSearch();
  const route = createMemo(() => parseLabRoute(props.id));

  createEffect(
    () => [route().kind, search()] as const,
    ([kind, s]) => {
      if (kind !== 'idle') return;
      const legacyUseCase = new URLSearchParams(s).get('useCase')?.trim();
      if (legacyUseCase) go(`/lab/use-case:${encodeURIComponent(legacyUseCase)}`, { replace: true });
    },
  );

  const [ctlQuery, setCtlQuery] = createSignal('');
  const [preset, setPreset] = createSignal<string>(VIEW_ALL);
  const [treeMenu, setTreeMenu] = createSignal<{ kind: 'use-case' | 'run'; node: CatalogueTreeNode; at: { x: number; y: number } } | null>(null);

  const ucPins = usePins('lab:use-cases');
  const runPins = usePins('lab:runs');
  const views = useViews('lab', LAB_VIEWS);

  const me = useOp('me', {});
  // One read for the screen (lab_view, docs/designs/fleet-data-layer.md): experiments, registry, open ratings and the
  // last 30 days' leaderboard; an opened use case or suite reads its own leaderboard.
  const view = useView('lab_view', {}, { quiet: 5000 });
  const part = <T,>(of: (d: NonNullable<typeof view.data>) => T) => ({
    get data() { return view.data ? of(view.data) : null; },
    get error() { return view.error; },
    get state() { return view.state; },
    reload: () => view.reload(),
  });
  const experiments = part(d => ({ experiments: d.experiments }));
  const registry = part(d => ({ useCases: d.useCases }));
  const ratings = part(d => ({ ratings: d.ratings }));

  const suiteFolderId = createMemo(() => {
    const r = route();
    return r.kind === 'suite' && r.suiteKey ? `${USE_CASE_COLLECTION_PREFIX}${r.suiteKey}` : null;
  });
  const selectedCollectionTitle = createMemo(() => collectionTitleOf(suiteFolderId()));

  const leaderboardReq = createMemo<Ops['lab_leaderboard']['request'] | null>(() => {
    const r = route(), colTitle = selectedCollectionTitle();
    if (r.kind === 'use-case') return { useCaseId: r.useCaseId };
    if (r.kind === 'suite') {
      if (r.suiteKey === 'archived' || !colTitle) return null;
      return { collection: colTitle };
    }
    if (r.kind === 'idle') return { days: 30 };
    return null;
  });
  const leaderboardWindowLabel = createMemo(() => leaderboardReq()?.days === 30 ? 'last 30 days' : 'last 90 days');

  const detailBoard = useOp('lab_leaderboard', () => { const r = leaderboardReq(); return r && !r.days ? r : null; });
  const leaderboard = {
    get data() { return leaderboardReq()?.days ? view.data?.leaderboard ?? null : detailBoard.data; },
    get error() { return leaderboardReq()?.days ? view.error : detailBoard.error; },
    reload: () => (leaderboardReq()?.days ? view.reload() : detailBoard.reload()),
  };
  // Approved examples are read only where they show: the open use case (FLT-41: the idle screen no longer counts them).
  const approvedExamples = useOp('lab_examples_list', () => (route().kind === 'use-case' ? { state: 'approved' } : null));

  // The best score per use case for the tree's right column, from the last all-suite comparison read.
  const [bestScoresByUseCase, setBestScoresByUseCase] = createSignal<Record<string, number>>({}, { ownedWrite: true });
  createEffect(
    () => [route().kind, leaderboard.data] as const,
    ([kind, lbData]) => {
      if (kind !== 'idle' || !lbData) return;
      const next: Record<string, number> = {};
      for (const entry of lbData.entries) {
        if (entry.scoreMean == null) continue;
        const cur = next[entry.useCaseId];
        if (cur === undefined || entry.scoreMean > cur) next[entry.useCaseId] = entry.scoreMean;
      }
      setBestScoresByUseCase(next);
    },
  );

  const now = Date.now();
  const rawExperiments = createMemo(() => experiments.data?.experiments ?? []);
  const rawUseCases = createMemo<RegistryUseCase[]>(() => registry.data?.useCases ?? []);
  const rawRatings = createMemo(() => ratings.data?.ratings ?? []);
  const rawExamples = createMemo(() => approvedExamples.data?.examples ?? []);
  const projectOfExperiment = createMemo(() => new Map(rawExperiments().map(e => [e.id, e.projectId])));

  const useCaseById = createMemo(() => new Map(rawUseCases().map(u => [u.id, u])));

  const useCaseTreeNodes = createMemo(
    () => buildUseCaseTree(rawUseCases(), {
      pinnedIds: ucPins.ids,
      hrefOf: u => `/lab/use-case:${encodeURIComponent(u.id)}`,
      rightOf: u => {
        const best = bestScoresByUseCase()[u.id];
        return best !== undefined ? <span class="mono" data-tip="Best observed score">{best.toFixed(2)}</span> : undefined;
      },
    }).nodes,
  );

  const viewMatch = createMemo(() => {
    const p = preset(), email = me.data?.email ?? null;
    return p === VIEW_MINE ? (e: LabExperimentRecord) => !!email && e.requestedBy === email
      : p === VIEW_WAITING ? undecided
        : () => true;
  });
  const sortedExperiments = createMemo(
    () => rawExperiments().filter(viewMatch()).toSorted((a, b) => ms(b.launchedAt ?? b.createdAt) - ms(a.launchedAt ?? a.createdAt)),
  );

  const selectedExperimentId = createMemo(() => {
    const r = route();
    return r.kind === 'experiment' ? r.experimentId : null;
  });

  const runTreeNodes = createMemo<CatalogueTreeNode[]>(() => {
    const folders: CatalogueTreeNode[] = [];
    const selExpId = selectedExperimentId();
    for (const f of RUN_FOLDERS) {
      const items = sortedExperiments().filter(f.match);
      if (!items.length) continue;
      const containsSelected = !!selExpId && items.some(e => e.id === selExpId);
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
  });

  // A run view (Mine, Waiting for a decision) shows the runs only; All shows both roots.
  const rootNodes = createMemo<CatalogueTreeNode[]>(() => [
    ...(preset() === VIEW_MINE || preset() === VIEW_WAITING ? [] : [{
      id: ROOT_USE_CASES_ID,
      label: 'Use cases',
      icon: 'chart-no-axes-column' as const,
      defaultCollapsed: false,
      children: useCaseTreeNodes(),
    }]),
    {
      id: ROOT_RUNS_ID,
      label: 'Runs',
      icon: 'play',
      defaultCollapsed: false,
      children: runTreeNodes(),
    },
  ]);

  const defaultClosed = createMemo(() => {
    const out: string[] = [];
    const r = route(), byId = useCaseById(), selExpId = selectedExperimentId();
    const selectedUseCaseArchived = r.kind === 'use-case'
      ? (byId.get(r.useCaseId)?.status === 'obsolete' || byId.get(r.useCaseId)?.status === 'duplicate')
      : r.kind === 'suite' && r.suiteKey === 'archived';
    if (!selectedUseCaseArchived) out.push(ARCHIVED_COLLECTION_ID);
    const stoppedOpen = !!selExpId && sortedExperiments().some(e => e.id === selExpId && RUN_FOLDERS[3].match(e));
    if (!stoppedOpen) out.push('run:stopped');
    return out;
  });

  const treeFlipped = createMemo(() => listGroups('lab:tree'));
  const treeClosed = createMemo(() => closedGroups(defaultClosed(), treeFlipped()));
  const treeCollapsed = createMemo<Record<string, boolean>>(() => {
    const closed = treeClosed();
    const map: Record<string, boolean> = {
      __pinned__: closed.has('__pinned__'),
      [ROOT_USE_CASES_ID]: closed.has(ROOT_USE_CASES_ID),
      [ROOT_RUNS_ID]: closed.has(ROOT_RUNS_ID),
    };
    for (const n of useCaseTreeNodes()) map[n.id] = closed.has(n.id);
    for (const n of runTreeNodes()) map[n.id] = closed.has(n.id);
    return map;
  });

  const toggleTreeFolder = (folderId: string) => {
    setListGroups('lab:tree', flipGroup(treeFlipped(), folderId));
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
    if (useCaseById().has(node.id)) {
      go(`/lab/use-case:${encodeURIComponent(node.id)}`);
      return;
    }
    go(`/lab/${encodeURIComponent(node.id)}`);
  };

  const selectedTreeId = createMemo(() => {
    const r = route();
    return r.kind === 'use-case'
      ? r.useCaseId
      : r.kind === 'suite' && r.suiteKey
        ? `${USE_CASE_COLLECTION_PREFIX}${r.suiteKey}`
        : r.kind === 'experiment'
          ? r.experimentId
          : null;
  });

  const menuItems = createMemo<MenuItem[]>(() => {
    const tm = treeMenu();
    if (!tm) return [];
    if (tm.kind === 'use-case') {
      const u = useCaseById().get(tm.node.id);
      const pinned = ucPins.has(tm.node.id);
      const items: MenuItem[] = [
        { label: 'Open', icon: actions.open, onClick: () => go(`/lab/use-case:${encodeURIComponent(tm.node.id)}`) },
        { label: pinned ? 'Unpin' : 'Pin', icon: pinned ? actions.unpin : actions.pin, onClick: () => ucPins.toggle(tm.node.id) },
      ];
      if (u && isLabEligibleUseCase(u)) {
        items.push({ label: 'Run in Lab', icon: 'play', onClick: () => go(`/lab/new?useCase=${encodeURIComponent(u.id)}`) });
      }
      return items;
    }
    const pinned = runPins.has(tm.node.id);
    return [
      { label: 'Open', icon: actions.open, onClick: () => go(`/lab/${encodeURIComponent(tm.node.id)}`) },
      { label: pinned ? 'Unpin' : 'Pin', icon: pinned ? actions.unpin : actions.pin, onClick: () => runPins.toggle(tm.node.id) },
    ];
  });

  const initialLoading = createMemo(() => !experiments.data && !registry.data && !experiments.error && !registry.error);
  const loadError = createMemo(() =>
    (experiments.error && !experiments.data ? experiments.error : null)
    ?? (registry.error && !registry.data ? registry.error : null),
  );

  const openNewExperiment = () => {
    if (route().kind !== 'new') {
      go('/lab/new');
      return;
    }
    const detail = document.querySelector<HTMLElement>('[data-ui=detail][aria-label="Create experiment"]');
    detail?.focus();
  };

  const pickView = (name: string) => {
    const v = views.list.find(x => x.name === name);
    if (!v) return;
    // History is the History page in Lab's scope (docs/designs/fleet-history.md), not a filter of the catalogue.
    if (name === VIEW_HISTORY) { go('/history?section=lab'); return; }
    if (v.system) { setPreset(name); return; }
    setPreset(VIEW_ALL);
    setCtlQuery(v.view.q ?? '');
  };

  // Control column: ShellActions + saved views and search + one 2-root CatalogueTree (OR-23, OR-24, OR-43, OR-48).
  const list = <>
    <ShellStatus><OfflineMarker state={experiments.state} now={now} /></ShellStatus>
    <ShellActions>
      <Button label="New experiment" icon="plus" iconOnly kind="primary" pressed={route().kind === 'new'} onClick={openNewExperiment} />
    </ShellActions>
    <div class="fbar">
      <div class="fbar-line">
        <SearchField label="Search Lab" value={ctlQuery()} onInput={setCtlQuery} onSearch={() => {}} />
      </div>
      <Presets system={views.list.filter(v => v.system).map(v => v.name)} systemTips={VIEW_TIPS}
        presets={views.list.filter(v => !v.system).map(v => v.name)} active={ctlQuery() ? views.list.find(v => !v.system && v.view.q === ctlQuery())?.name ?? null : preset()}
        onPick={pickView} onSave={name => views.save({ name, view: { view: 'lab', q: ctlQuery() } as unknown as View })} onUnpin={views.remove} />
    </div>
    <section class="panel">
      <Show when={!initialLoading()} fallback={<Skeleton rows={8} />}>
        <Show when={!loadError()} fallback={<ErrorBanner text={loadError()!} onRetry={() => view.reload()} />}>
          <CatalogueTree
            fill
            label="Lab catalogue"
            nodes={rootNodes()}
            selectedId={selectedTreeId()}
            query={ctlQuery()}
            collapsed={treeCollapsed()}
            onToggle={toggleTreeFolder}
            onPin={node => {
              if (useCaseById().has(node.id)) ucPins.toggle(node.id);
              else runPins.toggle(node.id);
            }}
            onSelect={handleTreeSelect}
            onRowMenu={(node, at) => {
              if (!node.children) setTreeMenu({ kind: useCaseById().has(node.id) ? 'use-case' : 'run', node, at });
            }}
            emptyText={preset() === VIEW_MINE ? 'You have not requested a Lab run yet.' : preset() === VIEW_WAITING ? 'No finished run waits for a decision.' : 'No matching use cases or runs.'}
          />
        </Show>
      </Show>
    </section>
    <Show when={preset() === VIEW_MINE}>
      <div class="lab-note"><a class="row-link" href="/lab/mine" onClick={ev => { ev.preventDefault(); go('/personal-lab'); }}>Personal Lab drafts and submissions</a></div>
    </Show>
    <Show when={treeMenu()}>{tm => <ContextMenu at={tm().at} label={tm().node.label} items={menuItems()} onClose={() => setTreeMenu(null)} />}</Show>
  </>;

  const approvals = createMemo(() => rawRatings().filter(r => r.exampleArtifactId));
  const openRatings = createMemo(() => rawRatings().filter(r => r.experimentId));
  const groupedUseCases = createMemo(() => groupUseCases(rawUseCases()));
  const waitingExperiments = createMemo(() => rawExperiments().filter(undecided).toSorted((a, b) => ms(b.launchedAt ?? b.createdAt) - ms(a.launchedAt ?? a.createdAt)));

  const comparison = (title: string, onRun?: () => void) => (
    <LabComparison
      title={title}
      windowLabel={leaderboardWindowLabel()}
      entries={leaderboard.data?.entries ?? []}
      loading={!!leaderboardReq() && !leaderboard.data && !leaderboard.error}
      error={leaderboard.error}
      onRetry={leaderboard.reload}
      projectOf={id => projectOfExperiment().get(id)}
      onOpenExperiment={x => go(`/lab/${encodeURIComponent(x)}`)}
      onRun={onRun}
    />
  );

  // Idle (J1, under 5 s): what waits for the person first, as question cards (OR-53), then the comparison.
  const idle = (
    <article class="detail" aria-label="Lab overview">
      <Waiting experiments={waitingExperiments()} approvals={approvals()} openRatings={openRatings()} now={now}
        onOpen={x => go(`/lab/${encodeURIComponent(x)}`)} onAll={() => pickView(VIEW_WAITING)} />
      <HistoryPreview entries={view.data?.history} to="/history?section=lab" />
      {comparison('All use cases')}
    </article>
  );

  const detailKey = createMemo(() => {
    const r = route();
    if (r.kind === 'new') return 'new';
    if (r.kind === 'use-case') return `use-case:${r.useCaseId}`;
    if (r.kind === 'suite') return `suite:${r.rawSuite}`;
    if (r.kind === 'experiment') return r.experimentId;
    return undefined;
  });

  return <ListDetail
    list={list}
    detailKey={detailKey()}
    idle={idle}
    detail={
      detailKey() !== undefined ? (
        <Switch>
          <Match when={route().kind === 'new'}>
            <Show when={`new:${search()}`} keyed>{_k => <LabCreate onClose={() => go('/lab')} />}</Show>
          </Match>
          <Match when={route().kind === 'use-case'}>
            <UseCaseDetail
              useCaseId={(route() as { kind: 'use-case'; useCaseId: string }).useCaseId}
              useCase={useCaseById().get((route() as { kind: 'use-case'; useCaseId: string }).useCaseId) ?? null}
              loading={!registry.data && !registry.error}
              error={registry.error}
              onRetry={registry.reload}
              examples={rawExamples()}
              examplesLoading={!approvedExamples.data && !approvedExamples.error}
              comparison={uc => comparison(uc.id)}
              onClose={() => go('/lab')}
              onRun={u => go(`/lab/new?useCase=${encodeURIComponent(u)}`)}
            />
          </Match>
          <Match when={route().kind === 'suite'}>
            <SuiteDetail
              rawSuite={(route() as { kind: 'suite'; rawSuite: string; suiteKey: SuiteKey | null }).rawSuite}
              suiteKey={(route() as { kind: 'suite'; rawSuite: string; suiteKey: SuiteKey | null }).suiteKey}
              title={selectedCollectionTitle() ?? (route() as { kind: 'suite'; rawSuite: string }).rawSuite}
              icon={(route() as { kind: 'suite'; suiteKey: SuiteKey | null }).suiteKey ? collectionIconOf((route() as { kind: 'suite'; suiteKey: SuiteKey }).suiteKey) : 'folder-open'}
              archivedUseCases={groupedUseCases().archived}
              comparison={comparison}
              onClose={() => go('/lab')}
              onSelectUseCase={u => go(`/lab/use-case:${encodeURIComponent(u)}`)}
            />
          </Match>
          <Match when={route().kind === 'experiment'}>
            <Show when={(route() as { kind: 'experiment'; experimentId: string }).experimentId} keyed>{eid => (
              <Experiment id={eid} onClose={() => go('/lab')} />
            )}</Show>
          </Match>
        </Switch>
      ) : undefined
    }
  />;
}

const title = (e: LabExperimentRecord) => `${e.pipelineId} · ${e.kind === 'ab' ? 'A/B' : e.kind} · ${e.variants.map(v => v.label).join(' vs ')}`;
const subtitle = (e: LabExperimentRecord) => `${e.kind === 'ab' ? 'A/B' : e.kind} · ${e.variants.length > 2 ? `${e.variants.length} variants` : e.variants.map(v => v.label).join(' vs ')}`;
/** A finished experiment nobody has decided yet (decision pending). */
const undecided = (e: LabExperimentRecord) => e.state === 'done' && e.decision === 'pending';

const QUESTIONS_SHOWN = 1;
const shorten = (t: string, n = 90) => t.length > n ? t.slice(0, n - 1).trimEnd() + '…' : t;

// What the Lab waits for (OR-53): an undecided finished run is a question with its variants as choices; a typed answer is
// recorded as "inconclusive" with the text as its reason. Example approvals and open ratings follow.
function Waiting(props: {
  experiments: readonly LabExperimentRecord[]; approvals: readonly LabRating[]; openRatings: readonly LabRating[]; now: number;
  onOpen: (id: string) => void;
  /** Opens the view of every finished run waiting for a decision. */
  onAll: () => void;
}) {
  const [answering, setAnswering] = createSignal<{ id: string; label: string } | null>(null);
  const [answered, setAnswered] = createSignal<Record<string, { answer: string; at: number }>>({});
  const [failed, setFailed] = createSignal<{ id: string; text: string } | null>(null);
  const alive = useAlive();
  const count = createMemo(() => props.experiments.length + props.approvals.length + (props.openRatings.length ? 1 : 0));
  const decide = async (e: LabExperimentRecord, answer: string) => {
    const choice = choicesOf(e).find(c => c.label === answer);
    setAnswering({ id: e.id, label: answer }); setFailed(null);
    try {
      await op('lab_experiment_decide', choice ? { experimentId: e.id, decision: choice.decision } : { experimentId: e.id, decision: 'inconclusive', reason: answer }, { signal: alive });
      if (!alive.aborted) setAnswered(a => ({ ...a, [e.id]: { answer, at: Date.now() } }));
    } catch (x) {
      if (!alive.aborted) setFailed({ id: e.id, text: message(x) });
    } finally {
      if (!alive.aborted) setAnswering(null);
    }
  };
  return <Show when={count() > 0}>
    <section class="panel" aria-label="Waiting for you">
      {/* The first question is a preview (OR-65); the heading opens every finished run waiting for a decision. */}
      <header class="panel-h"><Icon name="bell" /><h2>
        <Show when={props.experiments.length > QUESTIONS_SHOWN} fallback="Waiting for you">
          <a class="dsec-open" href="/lab" onClick={e => { e.preventDefault(); props.onAll(); }}>Waiting for you<Icon name="chevron-right" /></a>
        </Show>
      </h2><span class="muted mono">{count()}</span></header>
      <div class="lab-qs stack-pad">
        <For each={props.experiments.slice(0, QUESTIONS_SHOWN)}>{e => <>
          <QuestionCard
            question={`${shorten(e.labGroup || e.goal || e.pipelineId)} finished. Which variant do you adopt?`}
            choices={choicesOf(e)}
            answering={answering()?.id === e.id ? answering()!.label : undefined}
            answered={answered()[e.id]}
            onAnswer={a => decide(e, a)}
          />
          <span class="muted"><EntityCode id={e.id} kind="experiment" /> · {subtitle(e)} · <a class="row-link" href={`/lab/${encodeURIComponent(e.id)}`} onClick={ev => { ev.preventDefault(); props.onOpen(e.id); }}>scores and decision gate</a></span>
          <Show when={failed()?.id === e.id}><ProblemRow title="Decision not recorded" facts={failed()!.text} severity="failure" actions={[]} /></Show>
        </>}</For>
        <For each={props.approvals}>{r => <Rating r={r} now={props.now} />}</For>
        <Show when={props.openRatings.length > 0}>
          <KvRow
            icon="message-circle-question"
            label={`${props.openRatings.length} open ${props.openRatings.length === 1 ? 'rating' : 'ratings'}`}
            value={
              <span class="deps">
                <For each={[...new Set(props.openRatings.map(r => r.experimentId).filter((x): x is string => !!x))].slice(0, 5)}>{eid => (
                  <a class="row-link" href={`/lab/${encodeURIComponent(eid)}`} onClick={ev => { ev.preventDefault(); props.onOpen(eid); }}><EntityCode id={eid} kind="experiment" /></a>
                )}</For>
              </span>
            }
          />
        </Show>
      </div>
    </section>
  </Show>;
}

/** The decision choices of a finished run: adopt each challenger (the first variant is the baseline), or keep the baseline. */
function choicesOf(e: LabExperimentRecord): { label: string; decision: string; primary?: boolean }[] {
  const challengers = e.variants.length > 1 ? e.variants.slice(1) : e.variants;
  return [
    ...challengers.map(v => ({ label: `Adopt ${v.label ?? ''}`.trim(), decision: `adopt ${v.label ?? ''}`.trim() })),
    ...(e.variants.length > 1 ? [{ label: 'Keep baseline', decision: 'keep baseline' }] : []),
  ];
}

// Use case open (J2): goal and expected outcome, approved examples, Run in Lab, the comparison scoped to it.
function UseCaseDetail(props: {
  useCaseId: string;
  useCase: RegistryUseCase | null;
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  examples: readonly LabExampleEntry[];
  examplesLoading: boolean;
  comparison: (uc: RegistryUseCase) => JSX.Element;
  onClose: () => void;
  onRun: (useCaseId: string) => void;
}) {
  const eligible = createMemo(() => props.useCase ? isLabEligibleUseCase(props.useCase) : false);
  return (
    <article class="detail" data-ui="detail" tabindex={-1} aria-label={props.useCase ? props.useCase.goal : `Use case ${props.useCaseId}`}>
      <DetailHead
        crumb={props.useCaseId}
        title={props.useCase?.goal ?? props.useCaseId}
        onClose={() => props.onClose()}
        state={<Show when={props.useCase}>{uc => <>
          <StateBadge
            family="source"
            state={(uc().source_status || 'current') as StateOf<'source'>}
            reason={uc().source ? `${uc().source!.repo}/${uc().source!.path}#${uc().source!.anchor}` : undefined}
          />
          <span>{uc().maturity}</span>
          <span>{uc().lab_fit}</span>
        </>}</Show>}
        actions={<Show when={eligible() && props.useCase}>{uc => <Button label="Run in Lab" icon="play" kind="primary" onClick={() => props.onRun(uc().id)} />}</Show>}
      />
      <Show when={!(props.error && !props.useCase)} fallback={<ErrorBanner text={props.error!} onRetry={() => props.onRetry()} />}>
        <Show when={!props.loading} fallback={<Skeleton rows={6} />}>
          <Show when={props.useCase} fallback={<Empty text={`Use case "${props.useCaseId}" not found.`} />}>{uc => <>
            <UseCaseCard useCase={uc()} examples={props.examples} examplesLoading={props.examplesLoading} />
            {props.comparison(uc())}
          </>}</Show>
        </Show>
      </Show>
    </article>
  );
}

function SuiteDetail(props: {
  rawSuite: string;
  suiteKey: SuiteKey | null;
  title: string;
  icon: IconName;
  archivedUseCases: readonly RegistryUseCase[];
  comparison: (title: string) => JSX.Element;
  onClose: () => void;
  onSelectUseCase: (id: string) => void;
}) {
  return (
    <article class="detail" data-ui="detail" tabindex={-1} aria-label={props.title}>
      <header class="detail-h">
        <Button label="Close" icon="x" iconOnly onClick={() => props.onClose()} />
        <Icon name={props.icon} />
        <span class="grow" />
      </header>
      <Show when={props.suiteKey} fallback={<Empty text={`Suite "${props.rawSuite}" not found.`} />}>
        <Show when={props.suiteKey !== 'archived'} fallback={<>
          <h2 class="detail-title">{props.title}</h2>
          <DetailSection title="Archived use cases">
            <Show when={props.archivedUseCases.length > 0} fallback={<Empty text="No archived use cases." />}>
              <For each={props.archivedUseCases}>{u => (
                <KvRow
                  icon="archive"
                  label={u.goal}
                  meta={<a class="row-link mono" href={`/lab/use-case:${encodeURIComponent(u.id)}`} onClick={ev => { ev.preventDefault(); props.onSelectUseCase(u.id); }}>{u.id}</a>}
                  value={<StateBadge family="source" state={(u.source_status || 'current') as StateOf<'source'>} compact />}
                />
              )}</For>
            </Show>
          </DetailSection>
        </>}>
          {props.comparison(props.title)}
        </Show>
      </Show>
    </article>
  );
}

function UseCaseCard(props: {
  useCase: RegistryUseCase;
  examples: readonly LabExampleEntry[];
  examplesLoading: boolean;
}) {
  const matched = createMemo(() => matchingExamplesForUseCase(props.examples, props.useCase.id));
  return (
    <DetailSection title="Goal">
      <Show when={props.useCase.expected_outcome}>{eo => <KvRow icon="file-text" label="Expected outcome" value={eo()} />}</Show>
      <KvRow icon="rows-3" label="Approved examples"
        meta={props.examplesLoading ? 'loading' : matched().length ? matched().slice(0, 4).map(e => e.name).join(', ') + (matched().length > 4 ? `, +${matched().length - 4}` : '') : `none yet; maturity ${props.useCase.maturity}`}
        value={props.examplesLoading ? undefined : <span class="mono">{matched().length}</span>} />
      <Show when={props.useCase.tags && props.useCase.tags.length > 0}>
        <KvRow
          icon="layers-2"
          label="Tags"
          value={<span class="ep-presets"><For each={props.useCase.tags!}>{t => <span class="chip">{t}</span>}</For></span>}
        />
      </Show>
      <Show when={props.useCase.source}>{src => (
        <KvRow
          icon="file-text"
          label="Source"
          value={<span class="mono">{src().repo}/{src().path}#{src().anchor}</span>}
        />
      )}</Show>
    </DetailSection>
  );
}


// One rating or approval question with its blind outputs and keyboard answers.
function Rating(props: { r: LabRating; now: number }) {
  const answers = useAnswers();
  const busy = () => !!answers.answering(props.r.id);
  const opts = createMemo(() => {
    const r = props.r;
    return r.form === 'pick_best' ? r.items.map(i => ({ label: i.label, kbd: i.label.toLowerCase() }))
      : r.form === 'scale' ? [1, 2, 3, 4, 5].map(n => ({ label: String(n), kbd: String(n) }))
      : [{ label: 'Pass', kbd: 'p', primary: true }, { label: 'Fail', kbd: 'f' }];
  });
  const toAnswer = (a: string) => {
    const r = props.r;
    return r.form === 'pick_best' ? (r.items.find(i => i.label === a.toUpperCase())?.trialId ?? a) : /^(pass|fail)\b/i.test(a) ? a.replace(/^(pass|fail)/i, m => m.toLowerCase()) : a;
  };
  const shown = (a: string) => {
    const r = props.r;
    return r.form === 'pick_best' ? (r.items.find(i => i.trialId === a)?.label ?? a) : a;
  };
  const files = () => props.r.items.length > 0 ? (
    <div class="stack">
      <For each={props.r.items}>{i => (
        <div><b>{i.label}</b>{' '}
          <For each={i.files.filter(f => !f.path.endsWith('/output.json') || i.files.length === 1)}>{f => (
            <a class="id" href={`/api/results/${f.artifactId}${/\.(html?|svg)$/i.test(f.path) ? '/preview' : ''}`} target="_blank" rel="noopener">{f.path.split('/').slice(1).join('/')}</a>
          )}</For>
        </div>
      )}</For>
    </div>
  ) : undefined;
  return (
    <Show when={props.r.state !== 'withdrawn'} fallback={
      <div id={`rating-${props.r.id}`}><DetailSection title="Withdrawn rating"><KvRow icon="circle-slash" label={props.r.text} meta={props.r.withdrawal?.reason ?? 'No answer or score recorded'} />{files()}</DetailSection></div>
    }>
      <div id={`rating-${props.r.id}`}>
        <DecisionBlock question={props.r.text} askedBy="lab" addressedTo="you" since={props.now} now={props.now} waits={busy() ? 'saving' : 'never blocks'} context={files()} options={opts()}
          answered={props.r.state === 'answered' ? { by: 'you', answer: shown(String(props.r.answer)), at: props.now, delivered: true } : answers.answered(props.r.id) ? { by: 'you', answer: shown(answers.answered(props.r.id)!.answer), at: answers.answered(props.r.id)!.at, delivered: true } : undefined}
          onAnswer={a => answers.send(props.r, toAnswer(a))} />
        <AnswerNote answers={answers} id={props.r.id} />
      </div>
    </Show>
  );
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

function Experiment(props: { id: string; onClose: () => void }) {
  const [pollMs, setPollMs] = createSignal<number | undefined>(5000, { ownedWrite: true });
  const exp = useOp('lab_experiment_get', () => ({ experimentId: props.id }), { every: pollMs });
  const rated = useOp('lab_ratings_list', () => ({ experimentId: props.id }), { every: pollMs });
  const ratings = createMemo(() => rated.data?.ratings ?? []);
  const isTerminal = createMemo(() => {
    const e = exp.data;
    return !!e && TERMINAL_STATES.has(e.state) && e.trials.every(t => TERMINAL_STATES.has(t.state));
  });
  createEffect(() => isTerminal(), term => { setPollMs(term ? undefined : 5000); });

  const [savingRecord, setSavingRecord] = createSignal(0);
  const [recordDraft, setRecordDraft] = createSignal<Record<string, string>>({});
  const [recordErrors, setRecordErrors] = createSignal<Record<string, string>>({});
  const [savedRecord, setSavedRecord] = createSignal<Record<string, string>>({});
  const [decisionDraft, setDecisionDraft] = createSignal<string>();
  const [reasonDraft, setReasonDraft] = createSignal<string>();
  const [savingDecision, setSavingDecision] = createSignal(false);
  const [sel, setSel] = createSignal<string[]>([]);
  const [planTrial, setPlanTrial] = createSignal<string>();
  const [msg, setMsg] = createSignal<{ text: string; tone: 'warning' | 'failure' } | null>(null);
  const [patch, setPatch] = createSignal<Ops['lab_promote']['reply'] | null>(null);
  const [promotingVariant, setPromotingVariant] = createSignal<string | null>(null);
  const [promotedVariant, setPromotedVariant] = createSignal<string | null>(null);
  const defaultSwarmTrial = createMemo(() => selectDefaultSwarmTrial(exp.data));
  const swarmBoard = useSwarmBoard('pipeline_run', () => (!planTrial() && defaultSwarmTrial() ? defaultSwarmTrial()!.id : null));
  createEffect(() => !!exp.data, ready => { if (ready && location.hash) document.getElementById(location.hash.slice(1))?.scrollIntoView(); });
  const now = Date.now();

  const act = <K extends OpName>(name: K, body: Ops[K]['request']): Promise<Ops[K]['reply'] | undefined> =>
    op(name, body).then(r => { const w = (r as { warnings?: string[] }).warnings; setMsg(w?.length ? { text: w.join('; '), tone: 'warning' } : null); return r; }).catch((x: unknown) => { setMsg({ text: message(x), tone: 'failure' }); return undefined; });
  const promoteVariant = async (variantLabel: string) => {
    if (promotingVariant()) return;
    setPromotingVariant(variantLabel);
    try {
      const r = await act('lab_promote', { experimentId: props.id, variant: variantLabel });
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
  const winningVariant = createMemo(() => {
    const e = exp.data;
    if (!e || e.state !== 'done') return null;
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
  });
  const [openCell, setOpenCell] = createSignal<string | null>(null);
  /** One example × variant as a matrix cell (controls/matrix-cell.md): its latest attempt per repeat. */
  const cellView = (c: { trials?: string[]; summary?: Record<string, LabScoreSummary> } | undefined) => {
    const e = exp.data!;
    const ts = (c?.trials ?? []).map(k => trial(k)).filter((t): t is NonNullable<ReturnType<typeof trial>> => !!t);
    const running = ts.filter(t => !TERMINAL_STATES.has(t.state));
    const failed = ts.filter(t => t.state === 'failed');
    const counted = ts.filter(t => t.comparable && t.state === 'succeeded');
    const summary = c?.summary ?? {};
    const evaluator = e.primaryEvaluator && summary[e.primaryEvaluator] ? e.primaryEvaluator : Object.keys(summary)[0];
    const score = evaluator ? variantScore(summary[evaluator]) ?? undefined : undefined;
    const state: MatrixCellState = running.length ? 'running' : ts.length === 0 ? 'excluded'
      : failed.length === ts.length ? 'fail' : counted.length < ts.length ? 'partial' : 'pass';
    const live = running[0]?.planSummary;
    const durations = ts.map(t => t.durationMs).filter((d): d is number => d != null).sort((x, y) => x - y);
    const usd = ts.flatMap(t => t.cost ?? []).filter(x => x.value != null && x.currency === 'USD');
    return {
      state, score, k: counted.length, n: Math.max(ts.length, e.repeats),
      step: live && live.planned ? `${Math.min(live.planned, live.selected ?? 0)}/${live.planned}` : undefined,
      time: durations.length ? msMeasured(durations[Math.floor((durations.length - 1) / 2)]) : undefined,
      cost: usd.length ? { value: usd.reduce((n, x) => n + x.value!, 0), basis: costBasisOf(usd[0].basis), currency: 'USD' } : undefined,
      note: state === 'fail' ? problemText(failed[0]?.problem) ?? 'failed'
        : state === 'partial' ? `${ts.length - counted.length} of ${ts.length} left out${failed.length ? `: ${problemText(failed[0].problem) ?? 'failed'}` : ''}`
          : state === 'excluded' ? (e.state === 'draft' ? 'not run yet' : undefined) : undefined,
      confounders: [...new Set(ts.flatMap(t => [...(t.confounders.hosts ?? []), ...(t.confounders.harnesses ?? []), ...(t.confounders.models ?? [])]))].join(' · ') || undefined,
      retryKeys: state === 'fail' ? (c?.trials ?? []).filter(k => trial(k)?.state === 'failed') : [],
    };
  };
  /** The live step of the furthest running trial, for the head ("step 2/3"). */
  const liveStep = createMemo(() => {
    const t = (exp.data?.trials ?? []).filter(x => !TERMINAL_STATES.has(x.state) && x.planSummary?.planned)
      .sort((a, b) => (b.planSummary.selected ?? 0) / b.planSummary.planned - (a.planSummary.selected ?? 0) / a.planSummary.planned)[0];
    return t ? `step ${Math.min(t.planSummary.planned, t.planSummary.selected ?? 0)}/${t.planSummary.planned}` : '';
  });
  const trial = (key: string) => (exp.data?.trials ?? []).filter(t => `${t.cell.example}|${t.cell.variant}|${t.cell.repeat}` === key).sort((a, b) => (b.cell.attempt ?? 0) - (a.cell.attempt ?? 0))[0];
  const toggle = (t: string) => setSel(s => s.includes(t) ? s.filter(x => x !== t) : [...s, t]);
  const rate = (form: Ops['lab_rating_create']['request']['form']) => act('lab_rating_create', { experimentId: props.id, form, trialIds: sel() }).then(r => { if (r?.id) { setSel([]); location.hash = 'rating-' + r.id; } });
  const budgetOf = (unit: string) => exp.data?.budgets.find(b => b.unit === unit);
  const note = (unit: string) => { const b = budgetOf(unit); return b ? `of ${b.limit} · ${b.action}${b.firedAt ? ' · reached' : ''}` : undefined; };
  const record = (field: string) => field === 'goal' ? exp.data?.goal : field === 'hypothesis' ? exp.data?.hypothesis : undefined;
  const decisionOptions = createMemo(() => [
    ...(exp.data?.variants ?? []).map(v => ({ id: `adopt ${v.label ?? ''}`.trim(), label: `Adopt ${v.label ?? ''}` })),
    { id: 'keep baseline', label: 'Keep baseline' },
    { id: 'inconclusive', label: 'Inconclusive' },
  ]);
  const currentDecision = createMemo(() => decisionDraft() ?? (exp.data?.decision && exp.data.decision !== 'pending' ? exp.data.decision : ''));
  const currentReason = createMemo(() => reasonDraft() ?? exp.data?.decisionReason ?? '');
  const saveDecision = async () => {
    const dec = currentDecision();
    if (!dec) return;
    setSavingDecision(true);
    try { await act('lab_experiment_decide', { experimentId: props.id, decision: dec, reason: currentReason().trim() || undefined }); }
    finally { setSavingDecision(false); }
  };
  const baselineVariant = createMemo(() => {
    const e = exp.data;
    return e && e.variants.length > 1 ? e.variants[0] : null;
  });

  return (
    <Show when={exp.data} fallback={
      <article class="detail" data-ui="detail" tabindex={-1}>
        <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => props.onClose()} /></header>
        <Show when={exp.error} fallback={<Skeleton rows={6} />}>{err => <ErrorBanner text={err()} onRetry={exp.reload} />}</Show>
      </article>
    }>{e => (
      <article class="detail" data-ui="detail" tabindex={-1} aria-label={title(e())}>
        <DetailHead
          crumb={`${e().projectId} · ${e().labGroup || e().pipelineId}`}
          title={e().goal || title(e())}
          onClose={() => props.onClose()}
          state={<>
            <StateDot state={lifeOfExperiment(e())} reason={e().stopReason ?? undefined} />
            <EntityCode id={e().id} kind="experiment" />
            <span>{title(e())}</span>
            <Show when={liveStep()}>{st => <span class="mono" data-tip="The furthest running trial: its step and the step's name">{st()}</span>}</Show>
            <OfflineMarker state={exp.state} now={now} />
          </>}
          actions={<>
          <Show when={e().state === 'draft'}>
            <Button label="Launch" icon="play" kind="primary" disabledReason={savingRecord() ? 'Saving experiment record' : Object.values(recordErrors()).some(Boolean) || Object.entries(recordDraft()).some(([field, value]) => value !== String(savedRecord()[field] ?? record(field) ?? '')) ? 'Save the experiment record successfully before launch' : !String(savedRecord().goal ?? e().goal ?? '').trim() || !String(savedRecord().hypothesis ?? e().hypothesis ?? '').trim() ? 'Save a goal and hypothesis before launch' : undefined} onClick={() => act('lab_experiment_action', { experimentId: props.id, action: 'launch' })} />
          </Show>
          <Show when={e().state === 'paused'}><Button label="Resume" icon="play" kind="primary" onClick={() => act('lab_experiment_action', { experimentId: props.id, action: 'resume' })} /></Show>
          <Show when={e().state === 'running' || e().state === 'paused'}><Button label="Stop" icon="square" kind="danger" onClick={() => act('lab_experiment_action', { experimentId: props.id, action: 'stop' })} /></Show>
          <Show when={e().state === 'done' && winningVariant()}>{wv => (
            <Button
              label={promotedVariant() === (wv().label ?? '') ? `Promoted ${wv().label ?? ''}` : `Promote ${wv().label ?? ''}`}
              icon="check-check"
              kind="primary"
              busy={promotingVariant() === (wv().label ?? '')}
              disabledReason={promotingVariant() && promotingVariant() !== (wv().label ?? '') ? 'Promotion in progress' : undefined}
              onClick={() => promoteVariant(wv().label ?? '')}
            />
          )}</Show>
          </>}
        />
        <Show when={e().state === 'draft'}>
          <DetailSection title="Experiment record">
            <For each={['goal', 'hypothesis'] as const}>{field => (
              <SettingField label={field === 'goal' ? 'Goal' : 'Hypothesis'} help="Saved in the experiment record before explicit launch" value={recordDraft()[field] ?? savedRecord()[field] ?? record(field) ?? ''} now={now} status={recordErrors()[field] ? { kind: 'apply_error', error: recordErrors()[field] } : { kind: 'pending' }} onSave={async value => {
                setRecordDraft(r => ({ ...r, [field]: value })); setSavingRecord(n => n + 1);
                try { await op('lab_experiment_note', { experimentId: props.id, kind: 'amendment', field, value, text: `Set ${field} before launch` }); setSavedRecord(r => ({ ...r, [field]: value })); setRecordErrors(r => ({ ...r, [field]: '' })); setMsg(null); }
                catch (x) { setRecordErrors(r => ({ ...r, [field]: message(x) })); setMsg({ text: message(x), tone: 'failure' }); }
                finally { setSavingRecord(n => n - 1); }
              }} />
            )}</For>
          </DetailSection>
        </Show>
        <Show when={e().state !== 'draft' || (e().decision && e().decision !== 'pending')}>
          <DetailSection title="Question & decision">
            <Show when={e().goal}>{g => <KvRow icon="circle-question-mark" label="Goal" value={g()} />}</Show>
            <Show when={e().hypothesis}>{h => <KvRow icon="file-text" label="Hypothesis" value={h()} />}</Show>
            <Show when={e().decisionRule}>{dr => <KvRow icon="sliders-horizontal" label="Decision rule" value={dr()} />}</Show>
            <Show when={e().decision && e().decision !== 'pending'}>
              <KvRow icon="check-check" label={e().decision!}
                meta={[e().decidedBy && `by ${e().decidedBy}`, e().decidedAt].filter(Boolean).join(' · ') || undefined}
                value={e().decisionReason ?? undefined} />
            </Show>
            <Show when={e().state === 'done' || e().state === 'stopped'}>
              <div class="stack">
                <Choice label="Experiment decision" options={decisionOptions()} value={currentDecision()} onChange={setDecisionDraft} />
                <TextField label="Rationale" value={currentReason()} onChange={setReasonDraft} help="Why this outcome was chosen" />
                <div><Button label={savingDecision() ? 'Saving decision…' : e().decision && e().decision !== 'pending' ? 'Update decision' : 'Record decision'} kind="primary" icon="check" disabledReason={!currentDecision() ? 'Select a decision first' : savingDecision() ? 'Saving decision' : undefined} onClick={saveDecision} /></div>
              </div>
            </Show>
          </DetailSection>
        </Show>
        <Show when={msg()}>{m => <ProblemRow title={m().tone === 'warning' ? 'Launched with warnings' : 'Action refused'} facts={m().text} severity={m().tone} actions={[]} />}</Show>
        <Show when={patch()}>{p => (
          <DetailSection title="Promote"><KvRow icon="check" label={`Recommendation set to ${p().method.id ?? ''}`}
            meta={p().catalogue ? <span class="mono">catalogue r{p().catalogue!.revision}</span> : 'method already in the catalogue'} />
            <Show when={p().note}>{n => <KvRow icon="circle-alert" label={String(n())} />}</Show></DetailSection>
        )}</Show>
        <Totals items={[
          { label: 'Trials', m: { value: e().spend.trials.value, basis: 'measured' }, kind: 'count', note: note('trials') },
          { label: 'Wall time', m: { value: e().spend.wall_ms.value / 1000, basis: 'measured' }, kind: 'duration', note: note('wall_ms') },
          { label: 'API calls', m: { value: e().spend.calls.value, basis: 'measured' }, kind: 'count', note: note('calls') },
          { label: 'Credits', m: { value: e().spend.credits.value, basis: 'measured', partial: e().spend.credits.unmeasured ? `${e().spend.credits.unmeasured} calls without usage` : undefined }, kind: 'count', note: note('credits') },
        ]} />
        <section class="panel"><header class="panel-h"><Icon name="rows-3" /><h2>Matrix</h2><span class="muted">{e().examples.length} × {e().variants.length} × {e().repeats}</span><span class="grow" />
          <Show when={sel().length === 1}><Button label="Pass or fail" icon="check" onClick={() => rate('pass_fail')} /><Button label="Scale 1–5" icon="sliders-horizontal" onClick={() => rate('scale')} /></Show>
          <Show when={sel().length >= 2}><Button label={`Pick best of ${sel().length}`} icon="check-check" onClick={() => rate('pick_best')} /></Show></header>
          <DataTable
            label="Experiment matrix"
            cls="lab-xm"
            head={
              <tr>
                <th scope="col">Example</th>
                <For each={e().variants}>{v => <th scope="col">{v.label}</th>}</For>
              </tr>
            }
          >
            <For each={e().examples}>{p => (
              <tr>
                <th scope="row">
                  <span>{p.id}</span>
                  {p.split && <span class="id">{p.split}</span>}
                </th>
                <For each={e().variants}>{v => {
                  const view = createMemo(() => cellView(e().cells.find(x => x.example === p.id && x.variant === v.label)));
                  const key = `${p.id}|${v.label}`;
                  return (
                    <td>
                      <MatrixCell state={view().state} label={`Example ${p.id}, variant ${v.label}`} score={view().score} k={view().k} n={view().n}
                        step={view().step} time={view().time} cost={view().cost} note={view().note} confounders={view().confounders}
                        onOpen={() => setOpenCell(c => c === key ? null : key)}
                        onRetry={view().retryKeys.length ? () => { for (const cell of view().retryKeys) void act('lab_cell_action', { experimentId: props.id, cell, action: 'retry' }); } : undefined} />
                    </td>
                  );
                }}</For>
              </tr>
            )}</For>
          </DataTable>
          <Show when={openCell()} keyed>{key => {
            const [ex, va] = key.split('|');
            const c = () => e().cells.find(x => x.example === ex && x.variant === va);
            return (
              <DetailSection title={`Trials · ${ex} · ${va}`}>
                <div class="lab-cell lab-trials">
                  <For each={Object.entries(c()?.summary ?? {})}>{([k, s]) => <span class="muted" data-tip={k}>{evaluatorLabel(k)} <b>{scoreText(s)}</b></span>}</For>
                  <For each={c()?.trials ?? []}>{key => {
                    const curT = () => trial(key);
                    return (
                      <Show when={curT()}>{t => {
                        const conf = () => [(t().confounders.hosts ?? []).join(', '), (t().confounders.harnesses ?? []).join(', '), (t().confounders.models ?? []).join(', '), 'web ' + String(t().confounders.webRoute)].filter(Boolean).join(' · ');
                        const problem = () => problemText(t().problem);
                        const detail = () => t().waitDetail ?? problem();
                        const repeatTag = () => `r${(t().cell.repeat ?? 0) + 1}${t().cell.attempt ? `.${t().cell.attempt}` : ''}`;
                        const plannedSteps = () => t().planSummary?.planned ?? 0;
                        const activeStep = () => Math.min(plannedSteps(), Math.max(0, t().planSummary?.selected ?? 0));
                        const showSteps = () => !TERMINAL_STATES.has(t().state) && plannedSteps() > 0;
                        const stepTip = () => `${activeStep()} of ${plannedSteps()} steps selected${t().planSummary.pending ? ` · ${t().planSummary.pending} pending` : ''}${t().planSummary.blocked ? ` · ${t().planSummary.blocked} blocked` : ''}`;
                        return <>
                          <span class="lab-trial">
                            <Button label="View trial plan" icon="network" iconOnly kind="quiet" onClick={() => setPlanTrial(t().id)} />
                            <StateBadge family="trial" state={(t().observation === 'unconfirmed' ? 'unknown' : t().state) as StateOf<'trial'>} reason={detail() ?? (t().comparable ? undefined : 'left out of comparison')} compact />
                            <Show when={t().comparable}><RowCheck checked={sel().includes(t().id)} label={`Select repeat ${(t().cell.repeat ?? 0) + 1} for rating`} onToggle={() => toggle(t().id)} /></Show>
                            <span class="muted" data-tip={conf()}>{repeatTag()}</span>
                            <Show when={showSteps()}><span class="chip-age num" data-tip={stepTip()}>step {activeStep()}/{plannedSteps()}</span></Show>
                            <Num m={{ value: t().durationMs == null ? null : t().durationMs! / 1000, basis: t().durationMs == null ? 'unavailable' : 'measured', reason: t().state }} kind="duration" />
                            <Num m={{ value: t().spend.credits ?? null, basis: 'measured', partial: t().spend.basis === 'partial' ? 'some calls without usage' : undefined }} kind="count" />
                            <Show when={t().spend.tokens != null && (t().spend.tokens! > 0 || t().state !== 'preparing')}>
                              <span class="muted" data-tip={`Tokens: ${t().spend.tokens!.toLocaleString('en')}`}><Num m={{ value: t().spend.tokens!, basis: 'measured', partial: t().spend.basis === 'partial' ? 'some calls without usage' : undefined }} kind="count" /> tok</span>
                            </Show>
                            <Show when={t().recovery}>{rec => (
                              <span class="deps" data-tip={`Reuses ${(rec().reusedStepIds ?? []).join(', ')}; original spend remains in source history`}>
                                <EntityCode id={rec().sourceRunId} kind="execution" />
                                <Button label="Inspect source trial plan" icon="network" iconOnly kind="quiet" onClick={() => setPlanTrial(rec().sourceRunId)} />
                              </span>
                            )}</Show>
                            <For each={t().cost ?? []}>{costItem => <Num m={{ value: costItem.value, basis: costBasisOf(costItem.basis), currency: costItem.currency ?? undefined, reason: costItem.value === null ? costItem.reason : `${costItem.basis} · ${costItem.prices?.length ?? 0} price${costItem.prices?.length === 1 ? '' : 's'}` }} kind="money" />}</For>
                            <For each={t().scores.filter(s => s.trialIds?.length === 1)}>{s => s.form === 'typed'
                              ? <span data-tip={`${evaluatorLabel(s.evaluator ?? '')}${s.rationale ? ': ' + s.rationale : ''}`}><Icon name={typedIcon(s.value)} label={`${evaluatorLabel(s.evaluator ?? '')} ${String(typedValue(s.value).answer)}, label ${String(typedValue(s.value).label ?? 'none')}`} />{String(typedValue(s.value).answer)}</span>
                              : <span data-tip={`${evaluatorLabel(s.evaluator ?? '')}${s.rationale ? ': ' + s.rationale : ''}`}><Icon name={s.form === 'pass_fail' ? (s.value ? 'circle-check' : 'circle-x') : 'sliders-horizontal'} label={`${evaluatorLabel(s.evaluator ?? '')} ${String(s.value)}`} />{s.form === 'pass_fail' ? '' : String(s.form === 'metric' ? Number(s.value).toFixed(2) : s.value)}</span>}</For>
                            {t().state === 'running' || t().state === 'waiting' ? <Button label="Cancel cell" icon="x" iconOnly kind="quiet" onClick={() => act('lab_cell_action', { experimentId: props.id, cell: key, action: 'cancel' })} />
                              : <Button label="Retry cell" icon="rotate-ccw" iconOnly kind="quiet" onClick={() => act('lab_cell_action', { experimentId: props.id, cell: key, action: 'retry' })} />}
                          </span>
                          <Show when={detail()}>{dt => <span class="reason">{e().repeats > 1 ? `${repeatTag()}: ` : ''}{dt()}</span>}</Show>
                        </>;
                      }}</Show>
                    );
                  }}</For>
                </div>
              </DetailSection>
            );
          }}</Show>
        </section>
        <Show when={planTrial()} keyed>{tid => (
          <TrialPlan id={tid} projectId={e().projectId} trialProblem={problemText(e().trials.find(t => t.id === tid)?.problem)} assessments={e().trials.find(t => t.id === tid)?.projectAssessments ?? []} onOpen={setPlanTrial} onClose={() => setPlanTrial(undefined)} />
        )}</Show>
        <Show when={!planTrial() && defaultSwarmTrial() && hasSwarmBoard(swarmBoard.data)}>
          <DetailSection title={`Swarm · ${defaultSwarmTrial()!.cell.variant} r${(defaultSwarmTrial()!.cell.repeat ?? 0) + 1}`}>
            <SwarmPanel scopeKind="pipeline_run" scopeId={defaultSwarmTrial()!.id} board={swarmBoard} />
          </DetailSection>
        </Show>
        <section class="panel"><header class="panel-h"><Icon name="sliders-horizontal" /><h2>Variants</h2></header>
          <DetailSection title="Spread across repeats">
            <For each={e().variants}>{(v, vi) => {
              const variantPipeline = () => v.pipelineId ?? e().pipelineId;
              const isBaselineRow = () => vi() === 0 && e().variants.length > 1;
              const paramRows = createMemo(() => computeVariantParamDiffs(v, isBaselineRow() ? null : baselineVariant(), e().pipelineId));
              const diffs = createMemo(() => paramRows().filter(r => r.diff));
              const inherited = createMemo(() => paramRows().filter(r => !r.diff));
              return <>
                <KvRow
                  icon="git-merge"
                  label={v.proposalLabel ? `${v.proposalLabel} · ${v.label}` : v.label ?? ''}
                  meta={
                    <span class="deps">
                      <span>{Object.entries(v.summary ?? {}).map(([k, s]) => `${evaluatorLabel(k)} ${scoreText(s)}`).join(' · ') || 'no scores yet'}</span>
                      <span>·</span>
                      <EntityCode id={variantPipeline()} kind="pipeline" />
                      <span class="mono">{variantPipeline()}</span>
                      <Show when={v.catalogueRef?.id}>{cid => <a href={`/catalogue/${encodeURIComponent(cid())}`}>Catalogue r{v.catalogueRef!.revision}</a>}</Show>
                      <Show when={v.config_ref}>{cr => <CopyButton label={`Executable digest: ${cr()}`} value={cr()} iconOnly />}</Show>
                      <Show when={v.catalogueConfigRef}>{ccr => <CopyButton label={`Catalogue identity: ${ccr()}`} value={ccr()} iconOnly />}</Show>
                    </span>
                  }
                  value={
                    <Button
                      label="Promote"
                      icon="check-check"
                      busy={promotingVariant() === (v.label ?? '')}
                      disabledReason={promotingVariant() && promotingVariant() !== (v.label ?? '') ? 'Promotion in progress' : undefined}
                      onClick={() => promoteVariant(v.label ?? '')}
                    />
                  }
                />
                <Show when={isBaselineRow()} fallback={
                  <div class="stack-pad">
                    <Show when={diffs().length > 0} fallback={
                      <Show when={baselineVariant()}>{bv => <KvRow icon="circle-check" label="Matches baseline" meta={bv().label} />}</Show>
                    }>
                      <For each={diffs()}>{f => <DiffRow field={f.label} value={f.currentText} base={f.baseText} />}</For>
                    </Show>
                    <Show when={inherited().length > 0}>
                      <InheritedRows count={inherited().length}>
                        <For each={inherited()}>{f => <DiffRow field={f.label} value={f.currentText} base={f.baseText} />}</For>
                      </InheritedRows>
                    </Show>
                  </div>
                }>
                  <div class="stack-pad">
                    <KvRow icon="circle-check" label="Baseline" meta={<span class="mono">{variantPipeline()}</span>} />
                    <Show when={paramRows().length > 0}>
                      <InheritedRows count={paramRows().length}>
                        <For each={paramRows()}>{f => <DiffRow field={f.label} value={f.currentText} base={f.baseText} />}</For>
                      </InheritedRows>
                    </Show>
                  </div>
                </Show>
              </>;
            }}</For>
          </DetailSection>
          <For each={e().typed ?? []}>{t => (
            <DetailSection title={`${t.variant} · typed answers · ${t.split === 'tune' ? 'tune (bands are set here)' : 'held out (reported)'}`}>
              <For each={t.stats}>{s => {
                const cov = percent(s.confident, s.n), esc = percent(100 - parseInt(cov.text, 10), 100);
                return <KvRow icon={s.aggregate ? 'chart-no-axes-column' : 'file-text'} label={s.aggregate ? 'All languages' : s.tag}
                  meta={`n ${s.n} · coverage ${cov.text} · escalated ${esc.text}`}
                  value={s.selectiveAccuracy === null ? <span class="muted">no confident answer</span> : `accuracy ${percent(s.correct, s.confident).text}`} />;
              }}</For>
            </DetailSection>
          )}</For>
          <For each={e().comparison ?? []}>{c => (
            <DetailSection title={`${c.variant} vs ${c.baseline} · ${c.split === 'held_out' ? 'held out' : 'all examples'}`}>
              <KvRow icon="git-merge" label={c.result.mean === null ? 'No paired example' : `${c.evaluator.startsWith('typed:') ? 'Correct confident answers' : 'Mean'} difference ${c.result.mean >= 0 ? '+' : ''}${c.result.mean.toFixed(2)}`}
                meta={`${c.result.tooFew ? `n ${c.result.n} · too few examples for an interval` : `n ${c.result.n} · 95% interval ${fixed(c.result.low)} to ${fixed(c.result.high)}`}${c.evaluator.startsWith('typed:') ? ' · an escalation counts as not correct; see coverage and accuracy above' : ''}`}
                value={c.reason ? <span class="muted">{c.reason}</span> : undefined} />
            </DetailSection>
          )}</For>
          <Show when={(e().strategies ?? []).length > 0}>
            <DetailSection title="Loop strategies">
              <VirtualList items={e().strategies} getKey={v => v.id ?? ''} rowHeight={ROW2} follow="top" label="Strategy runs" columns="1.25rem minmax(0,1fr) auto"
                render={v => <><span class="lead"><StateBadge family="execution" state={v.state as StateOf<'execution'>} compact /></span><span class="tt">Iteration {String(v.iteration)} · {v.output?.reason ?? problemText(v.problem) ?? 'pending'}</span></>} />
            </DetailSection>
          </Show>
          <DetailSection title="Decision gate">
            <KvRow icon="check-check" label="Judge agrees with you" meta={e().agreement.compared ? `${e().agreement.matched} of ${e().agreement.compared} (${e().agreement.percent}%)` : 'no trial rated by both yet'} />
            <Show when={(e().agreement.byVersion ?? []).length > 0}>
              <VirtualList items={e().agreement.byVersion ?? []} getKey={v => v.version ?? ''} rowHeight={ROW2} follow="top" label="Agreement by rubric version" columns="1.25rem minmax(0,1fr) auto"
                render={v => <><span class="lead"><Icon name="check-check" /></span><span class="tt">{v.version}</span><span>{v.compared ? `${v.matched} of ${v.compared} (${v.percent}%)` : 'no shared rating'}</span></>} />
            </Show>
            <Show when={(e().pairwise ?? []).length > 0}>
              <VirtualList items={e().pairwise} getKey={v => v.group ?? ''} rowHeight={ROW2} follow="top" label="Pairwise order checks" columns="1.25rem minmax(0,1fr) auto"
                render={v => <><span class="lead"><Icon name={v.reason === 'agree' ? 'check-check' : 'circle-alert'} /></span><span class="tt">{v.group}</span><span>{v.reason}</span></>} />
            </Show>
            <KvRow icon="clock" label="Priority background" meta={e().priority.note} />
          </DetailSection>
        </section>
        <section class="panel"><header class="panel-h"><Icon name="message-circle-question" /><h2>Ratings</h2><span class="muted">rated {ratings().filter(r => r.state === 'answered').length} of {ratings().filter(r => r.state !== 'withdrawn').length}</span></header>
          <Show when={ratings().length > 0} fallback={<Empty text="Select comparable trials in the matrix to ask for a rating." />}>
            <For each={ratings()}>{r => <Rating r={r} now={now} />}</For>
          </Show>
        </section>
      </article>
    )}</Show>
  );
}

// A scoped server read owns the plan; the matrix never reconstructs omitted work.
function TrialPlan(props: { id: string; projectId: string; trialProblem?: string; assessments: ProjectAssessmentRef[]; onOpen: (runId: string) => void; onClose: () => void }) {
  const [pollMs, setPollMs] = createSignal<number | undefined>(5000, { ownedWrite: true });
  const runOp = useOp('pipeline_get', () => ({ runId: props.id, projectId: props.projectId }), { every: pollMs });
  const swarmBoard = useSwarmBoard('pipeline_run', () => props.id);
  const isTerminal = createMemo(() => !!runOp.data && TERMINAL_STATES.has(runOp.data.state));
  createEffect(() => isTerminal(), term => { setPollMs(term ? undefined : 5000); });

  const items = createMemo<StepItem[]>(() => {
    const run = runOp.data;
    return (run?.plan.entries ?? []).map(entry => {
      const execution = entry.execution;
      const definitions = (run?.definition as { steps?: { id: string; kind: string }[] } | undefined)?.steps ?? [];
      const definition = definitions.find(step => step.id === entry.id);
      return { id: entry.id, kind: definition?.kind === 'api' ? 'tool' : definition?.kind === 'agent' ? 'agent' : 'program', executor: definition?.kind ?? '', reads: '', writes: '', activation: entry.activation, activationReason: entry.problem?.message ?? entry.decision?.reason,
        result: entry.id === run?.canonicalResult?.stepId,
        run: execution ? { state: execution.state, reason: execution.waitDetail ?? execution.observation, note: execution.waitDetail ?? (entry.executionRef?.mode === 'reused' ? 'reused · original resources' : execution.observation === 'unconfirmed' ? 'unconfirmed' : undefined),
          time: msMeasured(execution.durationMs), cost: { value: null, basis: 'unavailable' as const }, error: execution.error ? JSON.stringify(execution.error) : undefined } : undefined };
    });
  });
  const reusedEntries = createMemo(() => (runOp.data?.plan.entries ?? []).filter(entry => entry.executionRef?.mode === 'reused'));
  return <>
  <DetailSection title="Trial plan">
    <KvRow icon="network" label="Trial" value={<><EntityCode id={props.id} kind="execution" /><Button label="Close trial plan" icon="x" iconOnly onClick={() => props.onClose()} /></>} />
    <OfflineMarker state={runOp.state} now={Date.now()} />
    <Show when={props.trialProblem}>{tp => <ProblemRow title="Trial problem" facts={tp()} severity="failure" actions={[]} />}</Show>
    <Show when={runOp.error}>{err => <ProblemRow title="Trial plan unavailable" facts={err()} severity="failure" actions={[]} />}</Show>
    <Show when={runOp.data}>{run => <KvRow icon="rows-3" label="Plan entries" meta={`${run().planSummary.planned} planned · ${run().planSummary.freshExecutions} fresh · ${run().planSummary.reusedExecutions} reused · ${run().planSummary.notSelected} not selected · ${run().planSummary.pending} pending · ${run().planSummary.blocked} blocked`} />}</Show>
    <Show when={runOp.data} fallback={<Empty text="Plan unavailable" />}><StepList label="Trial activation and execution" items={items()} /></Show>
    <For each={reusedEntries()}>{entry => (
      <KvRow
        icon="network"
        label={`${entry.id} · reused source`}
        value={
          <span class="deps">
            <EntityCode id={entry.executionRef!.sourceRunId!} kind="execution" />
            <Button label="Inspect source trial plan" icon="network" iconOnly kind="quiet" onClick={() => props.onOpen(entry.executionRef!.sourceRunId!)} />
          </span>
        }
      />
    )}</For>
    <Show when={runOp.data?.canonicalResult}>{cr => (
      <KvRow
        icon="file-text"
        label="Canonical result"
        value={
          <span class="deps">
            <EntityCode id={cr().artifact.artifactId} kind="result" />
            <a class="btn quiet icon-only" href={`/api/results/${encodeURIComponent(cr().artifact.artifactId)}`} target="_blank" rel="noopener" aria-label="Download canonical result" data-tip="Download"><Icon name="external-link" /></a>
          </span>
        }
      />
    )}</Show>
    <For each={props.assessments}>{assessment => (
      <KvRow
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
    )}</For>
  </DetailSection>
  <Show when={hasSwarmBoard(swarmBoard.data)}>
    <DetailSection title="Swarm">
      <SwarmPanel scopeKind="pipeline_run" scopeId={props.id} board={swarmBoard} />
    </DetailSection>
  </Show>
  </>;
}
