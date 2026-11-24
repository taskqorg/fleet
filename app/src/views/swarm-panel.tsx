// Reusable Fleet Swarm GUI panel (docs/designs/fleet-swarm.md, OR-1..OR-49):
// Overview (plan, metrics, thread cards), Board (live filterable feed), Roster (real workers), Timeline & Screenshots.
import { For, Match, Show, Switch, createEffect, createMemo, createSignal, untrack } from 'solid-js';
import { useLocation } from '../router.ts';
import { readValue, type MaybeAccessor } from '../signal.ts';
import {
  AgeChip, ArtifactView, Button, Choice, CostFigures, DataTable, DetailSection, Disclosure, Empty,
  EntityCode, EntityPicker, ErrorBanner, HarnessCounts, HarnessMark, Icon, KvRow, Latest, Markdown,
  Num, OutcomeBlock, SearchField, Skeleton, SortColHead, StateBadge, StatStrip, TabBar, Totals,
  agentLabel, exact, harnesses, matchSearch, msMeasured, splitCostFigures,
  type CatalogueTreeNode, type CostFigureLike, type Harness, type IconName, type StateOf, type Stat, type Tone,
} from '../../../packages/ui/src/index.ts';
import type {
  BoardMessage, BoardMessageKind, BoardScopeKind, CostFigure, WorkBoardGetReply, WorkBoardRosterEntry,
  WorkPlanTaskStatus, WorkPlanThread, WorkPlanThreadStatus,
} from '../../../contracts/generated/ops.ts';
import { useOp } from '../store.ts';

export type SwarmTab = 'overview' | 'board' | 'roster' | 'timeline';
type BoardKindFilter = 'all' | 'brief' | 'progress' | 'blocker' | 'done' | 'screenshot';
type RosterSortCol = 'state' | 'member' | 'thread' | 'duration' | 'spend' | 'lastPost';

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });
const isHarness = (h: string | null | undefined): h is Harness => !!h && h in harnesses;

/** Read a Swarm work board with live SSE invalidation scoped to the board and its active member entities. */
export function useSwarmBoard(scopeKindInput: MaybeAccessor<BoardScopeKind>, scopeIdInput: MaybeAccessor<string | null | undefined>) {
  const sid = createMemo(() => readValue(scopeIdInput));
  const sk = createMemo(() => readValue(scopeKindInput));
  let res: ReturnType<typeof useOp<'work_board_get'>>;
  const nextIds = createMemo(() => {
    const id = sid();
    if (!id) return [];
    const d = res?.data;
    const set = new Set<string>([id]);
    for (const r of d?.roster ?? []) {
      if (r.sessionId) set.add(r.sessionId);
      if (r.executionId) set.add(r.executionId);
    }
    for (const th of d?.plan?.threads ?? []) {
      for (const tk of th.tasks) {
        if (tk.sessionId) set.add(tk.sessionId);
        if (tk.executionId) set.add(tk.executionId);
        if (tk.taskId) set.add(tk.taskId);
      }
    }
    return [...set];
  });
  res = useOp(
    'work_board_get',
    () => {
      const id = sid();
      return id ? { scopeKind: sk(), scopeId: id } : null;
    },
    {
      quiet: 500,
      scope: () => untrack(() => (sid() ? { ids: nextIds() } : undefined)),
    },
  );
  return res;
}

/** True when a scope has a work plan, board messages, or roster members (OR-19: avoid empty Swarm tabs). */
export function hasSwarmBoard(board: WorkBoardGetReply | null | undefined): boolean {
  if (!board) return false;
  return board.plan != null || board.messages.length > 0 || board.roster.length > 0;
}

const THREAD_STATE: Record<WorkPlanThreadStatus, { state: StateOf<'task'>; reason?: string }> = {
  planned: { state: 'queued' },
  active: { state: 'in_progress' },
  blocked: { state: 'waiting', reason: 'blocked' },
  done: { state: 'completed' },
  failed: { state: 'failed' },
  cancelled: { state: 'cancelled' },
};

const TASK_STATE: Record<WorkPlanTaskStatus, { state: StateOf<'task'>; reason?: string }> = {
  pending: { state: 'queued' },
  in_progress: { state: 'in_progress' },
  blocked: { state: 'waiting', reason: 'blocked' },
  review: { state: 'waiting', reason: 'review' },
  done: { state: 'completed' },
  failed: { state: 'failed' },
  cancelled: { state: 'cancelled' },
};

function rosterBadge(raw: string | null | undefined): { family: 'execution' | 'session'; state: string; reason?: string } {
  if (raw === 'preparing' || raw === 'running' || raw === 'waiting' || raw === 'succeeded' || raw === 'failed' || raw === 'cancelled') {
    return { family: 'execution', state: raw };
  }
  if (raw === 'idle') return { family: 'session', state: 'idle' };
  return { family: 'execution', state: 'preparing', reason: raw || 'unassigned' };
}

const KIND_META: Record<BoardMessageKind, { label: string; icon: IconName; tone: Tone }> = {
  brief: { label: 'Brief', icon: 'file-text', tone: 'running' },
  message: { label: 'Message', icon: 'messages-square', tone: 'neutral' },
  progress: { label: 'Progress', icon: 'activity', tone: 'running' },
  blocker: { label: 'Blocker', icon: 'triangle-alert', tone: 'danger' },
  done: { label: 'Done', icon: 'circle-check', tone: 'success' },
  screenshot: { label: 'Screenshot', icon: 'panels-top-left', tone: 'neutral' },
};

const KIND_CHOICES: { id: Exclude<BoardKindFilter, 'screenshot'>; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'brief', label: 'Brief' },
  { id: 'progress', label: 'Progress' },
  { id: 'blocker', label: 'Blocker' },
  { id: 'done', label: 'Done' },
];

function mergeBoardCosts(lists: readonly (readonly CostFigure[] | null | undefined)[]): CostFigure[] {
  const byKey = new Map<string, CostFigure>();
  for (const list of lists) {
    for (const c of list ?? []) {
      const k = `${c.currency ?? ''}\0${c.basis}`;
      const prev = byKey.get(k);
      if (!prev) {
        byKey.set(k, { ...c });
        continue;
      }
      if (prev.value == null || c.value == null) prev.value = prev.value ?? c.value;
      else prev.value = +(prev.value + c.value).toFixed(6);
      if (c.usages != null) prev.usages = (prev.usages ?? 0) + c.usages;
      if (!prev.reason && c.reason) prev.reason = c.reason;
      if (!prev.note && c.note) prev.note = c.note;
    }
  }
  return [...byKey.values()];
}

function primarySpendAmount(cost: readonly CostFigureLike[]): number | null {
  const { metered, subscription } = splitCostFigures(cost);
  const firstMetered = metered.find(c => c.value !== null && c.basis !== 'unavailable');
  if (firstMetered?.value != null) return firstMetered.value;
  const firstSub = subscription.find(c => c.value !== null);
  return firstSub?.value ?? null;
}

/** Reusable Swarm GUI panel backed by Variant C work_board_get / board_read. */
export function SwarmPanel(props: {
  scopeKind: BoardScopeKind;
  scopeId: string;
  board?: ReturnType<typeof useSwarmBoard>;
}) {
  const internalBoard = useSwarmBoard(() => props.scopeKind, () => (props.board ? null : props.scopeId));
  const board = () => props.board ?? internalBoard;
  const [tab, setTab] = createSignal<SwarmTab>('overview', { ownedWrite: true });
  const [threadFilter, setThreadFilter] = createSignal<string | null>(null, { ownedWrite: true });
  const [kindFilter, setKindFilter] = createSignal<BoardKindFilter>('all', { ownedWrite: true });
  const [search, setSearch] = createSignal('', { ownedWrite: true });
  createEffect(
    () => [props.scopeKind, props.scopeId] as const,
    () => {
      setTab('overview');
      setThreadFilter(null);
      setKindFilter('all');
      setSearch('');
    },
    { defer: true },
  );

  const data = () => board().data;
  const plan = () => data()?.plan;
  const threads = () => plan()?.threads ?? [];
  const roster = () => data()?.roster ?? [];
  const messages = () => data()?.messages ?? [];
  const summary = () => data()?.summary;

  const milestones = createMemo(() => messages().filter(m => m.kind === 'brief' || m.kind === 'blocker' || m.kind === 'done'));
  const mediaMessages = createMemo(() => messages().filter(m => m.kind === 'screenshot' || !!m.artifactId));
  const timelineCount = createMemo(() => milestones().length + mediaMessages().length);

  const tabs = createMemo<{ id: SwarmTab; label: string; count?: number }[]>(() => {
    const s = summary();
    return [
      { id: 'overview', label: 'Overview', count: threads().length || undefined },
      { id: 'board', label: 'Board', count: s?.messagesTotal || undefined },
      { id: 'roster', label: 'Roster', count: s?.membersTotal || undefined },
      { id: 'timeline', label: 'Timeline & Screenshots', count: timelineCount() || undefined },
    ];
  });

  const openThreadOnBoard = (threadId: string) => {
    setThreadFilter(threadId);
    setKindFilter('all');
    setTab('board');
  };

  return (
    <Switch
      fallback={
        <div class="stack" data-ui="swarm-panel">
          <TabBar cls="detail-tabs" label="Swarm views" tabs={tabs()} value={tab()} onChange={setTab} />
          <Switch>
            <Match when={tab() === 'overview'}>
              <div role="tabpanel" aria-label="Overview" class="stack">
                <SwarmOverviewTab data={data()!} onOpenThread={openThreadOnBoard} />
              </div>
            </Match>
            <Match when={tab() === 'board'}>
              <div role="tabpanel" aria-label="Board" class="stack">
                <SwarmBoardTab
                  threads={threads()}
                  roster={roster()}
                  messages={messages()}
                  screenshotsCount={summary()!.screenshotsCount}
                  threadFilter={threadFilter()}
                  onThreadFilter={setThreadFilter}
                  kindFilter={kindFilter()}
                  onKindFilter={setKindFilter}
                  search={search()}
                  onSearch={setSearch}
                />
              </div>
            </Match>
            <Match when={tab() === 'roster'}>
              <div role="tabpanel" aria-label="Roster" class="stack">
                <SwarmRosterTab threads={threads()} roster={roster()} />
              </div>
            </Match>
            <Match when={tab() === 'timeline'}>
              <div role="tabpanel" aria-label="Timeline & Screenshots" class="stack">
                <SwarmTimelineTab threads={threads()} roster={roster()} milestones={milestones()} mediaMessages={mediaMessages()} />
              </div>
            </Match>
          </Switch>
        </div>
      }
    >
      <Match when={board().error && !data()}>
        <ErrorBanner text={`Swarm board did not load: ${board().error}`} onRetry={board().reload} />
      </Match>
      <Match when={!data()}>
        <Skeleton rows={5} />
      </Match>
      <Match when={!hasSwarmBoard(data())}>
        <Empty text="No Swarm plan or board activity yet." />
      </Match>
    </Switch>
  );
}

function SwarmOverviewTab(props: {
  data: WorkBoardGetReply;
  onOpenThread: (threadId: string) => void;
}) {
  const plan = () => props.data.plan;
  const roster = () => props.data.roster;
  const messages = () => props.data.messages;
  const summary = () => props.data.summary;
  const threads = () => plan()?.threads ?? [];

  const threadsDone = () => summary().threadsByStatus.done ?? 0;
  const tasksDone = () => summary().tasksByStatus.done ?? 0;
  const activeWorkers = () =>
    (summary().membersByState.running ?? 0)
    + (summary().membersByState.preparing ?? 0)
    + (summary().membersByState.waiting ?? 0);

  const validHarnessCounts = createMemo(() => {
    const out: Partial<Record<Harness, number>> = {};
    for (const [k, v] of Object.entries(summary().harnessCounts)) {
      if (isHarness(k) && v > 0) out[k] = v;
    }
    return out;
  });

  const hasHarnesses = createMemo(() => Object.keys(validHarnessCounts()).length > 0);
  const mergedSpend = createMemo(() => mergeBoardCosts(roster().map(r => r.cost)));
  const hasVisibleSpend = createMemo(() =>
    mergedSpend().some(c => (c.value !== null && c.basis !== 'unavailable') || c.basis === 'subscription'),
  );

  const stats = createMemo<Stat[]>(() => {
    const s = summary();
    return [
      ...(s.threadsTotal > 0 ? [{
        label: 'threads done',
        icon: 'git-merge' as IconName,
        value: { value: threadsDone(), basis: 'measured' as const },
        of: s.threadsTotal,
        tone: threadsDone() === s.threadsTotal ? ('success' as const) : undefined,
      }] : []),
      ...(s.tasksTotal > 0 ? [{
        label: 'tasks done',
        icon: 'check-check' as IconName,
        value: { value: tasksDone(), basis: 'measured' as const },
        of: s.tasksTotal,
        tone: tasksDone() === s.tasksTotal ? ('success' as const) : undefined,
      }] : []),
      ...(activeWorkers() > 0 ? [{
        label: 'active workers',
        icon: 'activity' as IconName,
        value: { value: activeWorkers(), basis: 'measured' as const },
        tone: 'running' as const,
        ...(hasHarnesses() ? { harness: validHarnessCounts() } : {}),
      }] : []),
      ...(s.messagesTotal > 0 ? [{
        label: 'board posts',
        icon: 'messages-square' as IconName,
        value: { value: s.messagesTotal, basis: 'measured' as const },
      }] : []),
      ...(s.blockersCount > 0 ? [{
        label: 'blockers',
        icon: 'triangle-alert' as IconName,
        value: { value: s.blockersCount, basis: 'measured' as const },
        tone: 'danger' as const,
      }] : []),
    ];
  });

  const rosterByName = createMemo(() => new Map(roster().map(r => [r.memberName, r])));
  const postsByThread = createMemo(() => {
    const counts = new Map<string, number>();
    for (const m of messages()) {
      if (m.threadId) counts.set(m.threadId, (counts.get(m.threadId) ?? 0) + 1);
    }
    return counts;
  });

  return (
    <>
      <Show when={plan()} keyed>
        {p => (
          <section class="panel" aria-label={p.title}>
            <header class="panel-h" style={{ 'flex-wrap': 'wrap' }}>
              <Icon name="network" />
              <h2 style={{ 'min-inline-size': 0, 'overflow-wrap': 'anywhere' }}>{p.title}</h2>
              <span class="grow" />
              <Show when={p.strategy}>
                <span
                  class="chip in"
                  data-tip={p.strategy}
                  style={{ 'min-inline-size': 0, 'max-inline-size': '100%' }}
                >
                  <span style={{ 'min-inline-size': 0, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
                    {p.strategy}
                  </span>
                </span>
              </Show>
              <span class="chip in mono" data-tip={`Updated ${exact(p.updatedAt)}`}>v{p.version}</span>
            </header>
            <Show when={p.goal}>
              <div class="stack">
                <OutcomeBlock outcome={p.goal!} requirements={[]} inputs={[]} />
              </div>
            </Show>
          </section>
        )}
      </Show>

      <Show when={stats().length > 0}><StatStrip label="Swarm summary" stats={stats()} /></Show>

      <Show when={hasVisibleSpend()}>
        <Totals items={[{ label: 'Spend', cost: mergedSpend(), showUnavailable: false }]} compact />
      </Show>

      <Show when={hasHarnesses() && activeWorkers() === 0}>
        <div class="res-bar">
          <span class="muted">Harnesses</span>
          <HarnessCounts counts={validHarnessCounts()} label="Workers by harness" />
        </div>
      </Show>

      <Show
        when={threads().length > 0}
        fallback={<Empty text="No work plan threads defined yet; inspect the Board tab for messages." />}
      >
        <For each={threads()}>
          {thread => (
            <ThreadCard
              thread={thread}
              roster={roster()}
              rosterByName={rosterByName()}
              postCount={postsByThread().get(thread.id) ?? 0}
              onOpenThread={() => props.onOpenThread(thread.id)}
            />
          )}
        </For>
      </Show>
    </>
  );
}

function ThreadCard(props: {
  thread: WorkPlanThread;
  roster: readonly WorkBoardRosterEntry[];
  rosterByName: Map<string, WorkBoardRosterEntry>;
  postCount: number;
  onOpenThread: () => void;
}) {
  const st = () => THREAD_STATE[props.thread.status ?? 'planned'];
  const doneCount = createMemo(() => props.thread.tasks.filter(t => t.status === 'done').length);

  const workers = createMemo(() => {
    const map = new Map<string, WorkBoardRosterEntry>();
    for (const r of props.roster) {
      if (r.threadId === props.thread.id) map.set(r.memberName, r);
    }
    for (const t of props.thread.tasks) {
      const m = t.ownerMember?.trim();
      if (!m || map.has(m)) continue;
      const existing = props.rosterByName.get(m);
      if (existing) {
        map.set(m, existing);
      } else {
        map.set(m, {
          memberName: m,
          role: t.role ?? 'worker',
          threadId: props.thread.id,
          sessionId: t.sessionId ?? null,
          executionId: t.executionId ?? null,
          state: null,
          harness: t.harness ?? null,
          model: t.model ?? null,
          hostId: null,
          startedAt: null,
          endedAt: null,
          durationMs: null,
          tokensInput: null,
          tokensOutput: null,
          cost: [],
          lastPostSeq: null,
        });
      }
    }
    return [...map.values()];
  });

  return (
    <section class="panel" aria-label={props.thread.title}>
      <header class="panel-h">
        <StateBadge family="task" state={st().state} reason={st().reason} compact />
        <h2>{props.thread.title}</h2>
        <span class="id">{props.thread.id}</span>
        <span class="grow" />
        <span class="muted mono" data-tip={`${doneCount()} of ${props.thread.tasks.length} tasks done`}>{doneCount()}/{props.thread.tasks.length}</span>
        <Button
          label={props.postCount > 0 ? `Board (${props.postCount})` : 'Board'}
          icon="messages-square"
          kind="quiet"
          tip={`Filter Board to ${props.thread.title}`}
          onClick={props.onOpenThread}
        />
      </header>
      <div class="stack">
        <Show when={props.thread.goal}><p class="muted" style={{ margin: 0 }}>{props.thread.goal}</p></Show>

        <Show when={workers().length > 0}>
          <div class="tag-row" role="group" aria-label={`Workers in ${props.thread.title}`}>
            <For each={workers()}>
              {w => {
                const b = rosterBadge(w.state);
                const tip = [w.role, w.model ? agentLabel(w.model) : w.harness, w.hostId].filter(Boolean).join(' · ') || undefined;
                return (
                  <span class="chip in" data-tip={tip}>
                    <StateBadge family={b.family} state={b.state as never} reason={b.reason} compact />
                    <Show when={isHarness(w.harness)}><HarnessMark h={w.harness!} /></Show>
                    <span>{w.memberName}</span>
                    <Show when={w.role && w.role !== 'worker'}><span class="muted">{w.role}</span></Show>
                  </span>
                );
              }}
            </For>
          </div>
        </Show>

        <Show when={props.thread.tasks.length > 0}>
          <DetailSection title="Sub-tasks">
            <For each={props.thread.tasks}>
              {tk => {
                const tst = TASK_STATE[tk.status ?? 'pending'];
                const pathLabel = tk.ownedPaths?.length === 1
                  ? tk.ownedPaths[0]
                  : tk.ownedPaths && tk.ownedPaths.length > 1
                    ? `${tk.ownedPaths.length} paths`
                    : null;
                return (
                  <div class="kv-row" style={{ 'flex-wrap': 'wrap' }}>
                    <StateBadge family="task" state={tst.state} reason={tst.reason} compact />
                    <b>{tk.title}</b>
                    <Show when={tk.ownerMember}>
                      <span class="muted">
                        {tk.ownerMember}
                        {tk.workspaceMode ? ` · ${tk.workspaceMode}` : ''}
                      </span>
                    </Show>
                    <Show when={tk.dependsOn && tk.dependsOn.length > 0}>
                      <span class="muted mono" data-tip={`Depends on ${tk.dependsOn!.join(', ')}`}>
                        after {tk.dependsOn!.join(', ')}
                      </span>
                    </Show>
                    <Show when={pathLabel}>
                      <span class="muted mono" data-tip={tk.ownedPaths!.join('\n')}>
                        {pathLabel}
                      </span>
                    </Show>
                    <span class="grow" />
                    <Show when={isHarness(tk.harness)}><HarnessMark h={tk.harness!} /></Show>
                    <Show when={tk.sessionId}><EntityCode id={tk.sessionId!} kind="conversation" /></Show>
                    <Show when={tk.executionId}><EntityCode id={tk.executionId!} kind="execution" /></Show>
                    <Show when={tk.taskId}><EntityCode id={tk.taskId!} kind="task" /></Show>
                  </div>
                );
              }}
            </For>
          </DetailSection>
        </Show>
      </div>
    </section>
  );
}

function SwarmBoardTab(props: {
  threads: readonly WorkPlanThread[];
  roster: readonly WorkBoardRosterEntry[];
  messages: readonly BoardMessage[];
  screenshotsCount: number;
  threadFilter: string | null;
  onThreadFilter: (id: string | null) => void;
  kindFilter: BoardKindFilter;
  onKindFilter: (kind: BoardKindFilter) => void;
  search: string;
  onSearch: (q: string) => void;
}) {
  const rosterByName = createMemo(() => new Map(props.roster.map(r => [r.memberName, r])));
  const threadTitleMap = createMemo(() => new Map(props.threads.map(t => [t.id, t.title])));

  const threadPickerNodes = createMemo<CatalogueTreeNode[]>(() => props.threads.map(t => ({
    id: t.id,
    label: t.title,
    sub: t.id,
    icon: 'git-merge',
    searchText: `${t.title} ${t.id} ${t.goal ?? ''}`,
  })));

  const filtered = createMemo(() => {
    const rb = rosterByName();
    const tt = threadTitleMap();
    const tf = props.threadFilter;
    const kf = props.kindFilter;
    const q = props.search;
    return props.messages.filter(m => {
      if (tf && m.threadId !== tf) return false;
      if (kf !== 'all' && m.kind !== kf) return false;
      if (!q.trim()) return true;
      const text = [m.text, m.authorMember, m.authorRole, m.threadId, tt.get(m.threadId), ...m.mentions].filter(Boolean).join(' ');
      return matchSearch(q, text, {
        agent: rb.get(m.authorMember)?.harness ?? null,
        state: m.kind,
      });
    });
  });

  return (
    <>
      <div class="fbar" data-ui="filter-bar">
        <div class="fbar-line">
          <SearchField label="Search board" value={props.search} onInput={props.onSearch} onSearch={() => {}} />
          <Choice
            label="Message kind"
            options={KIND_CHOICES}
            value={props.kindFilter === 'screenshot' ? '' : props.kindFilter}
            onChange={id => props.onKindFilter(id as BoardKindFilter)}
          />
          <Show when={props.screenshotsCount > 0 || props.kindFilter === 'screenshot'}>
            <Button
              label="Screenshots"
              icon="panels-top-left"
              kind="quiet"
              pressed={props.kindFilter === 'screenshot'}
              onClick={() => props.onKindFilter(props.kindFilter === 'screenshot' ? 'all' : 'screenshot')}
            />
          </Show>
          <span class="count" aria-live="polite">
            {filtered().length < props.messages.length
              ? `${filtered().length.toLocaleString('en')} of ${props.messages.length.toLocaleString('en')}`
              : `${props.messages.length.toLocaleString('en')} posts`}
          </span>
        </div>
        <Show when={props.threads.length > 1 || props.threadFilter !== null}>
          <div class="fbar-line">
            <EntityPicker
              label="Thread"
              mode="single"
              nodes={threadPickerNodes()}
              value={props.threadFilter}
              placeholder="All threads"
              searchPlaceholder="Find thread"
              onChange={props.onThreadFilter}
            />
            <Show when={props.threadFilter !== null}>
              <Button label="All threads" icon="x" kind="quiet" onClick={() => props.onThreadFilter(null)} />
            </Show>
          </div>
        </Show>
      </div>

      <Show
        when={filtered().length > 0}
        fallback={
          <Empty
            text={props.messages.length === 0 ? 'No board posts yet.' : 'No board posts match these filters.'}
            action={props.messages.length > 0 ? (
              <Button label="Clear filters" onClick={() => { props.onSearch(''); props.onKindFilter('all'); props.onThreadFilter(null); }} />
            ) : undefined}
          />
        }
      >
        <div class="stack">
          <Latest
            n={25}
            items={filtered().map(m => (
              <BoardMessageCard
                m={m}
                member={rosterByName().get(m.authorMember)}
                threadTitle={m.threadId ? threadTitleMap().get(m.threadId) : undefined}
              />
            ))}
          />
        </div>
      </Show>
    </>
  );
}

function BoardMessageCard(props: {
  m: BoardMessage;
  member?: WorkBoardRosterEntry;
  threadTitle?: string;
}) {
  const km = () => KIND_META[props.m.kind];
  const [, go] = useLocation();
  const [imgFailed, setImgFailed] = createSignal(false, { ownedWrite: true });
  createEffect(() => props.m.artifactId, () => { setImgFailed(false); }, { defer: true });
  const src = () => (props.m.artifactId ? `/api/results/${encodeURIComponent(props.m.artifactId)}` : null);

  return (
    <section class="panel" aria-label={`Board post #${props.m.seq} by ${props.m.authorMember}`}>
      <header class="panel-h" style={{ 'flex-wrap': 'wrap' }}>
        <span class="mono muted">#{props.m.seq}</span>
        <span class={`badge t-${km().tone}`}>
          <Icon name={km().icon} />
          <span>{km().label}</span>
        </span>
        <Show when={isHarness(props.member?.harness)}><HarnessMark h={props.member!.harness!} /></Show>
        <b>{props.m.authorMember}</b>
        <Show when={props.m.authorRole}><span class="muted">{props.m.authorRole}</span></Show>
        <Show when={props.m.threadId}><span class="chip mono" data-tip={props.m.threadId}>{props.threadTitle ?? props.m.threadId}</span></Show>
        <For each={props.m.mentions}>{target => <span class="chip mono">@{target}</span>}</For>
        <span class="grow" />
        <Show when={props.m.authorSessionId || props.member?.sessionCode}>
          <EntityCode id={props.m.authorSessionId ?? props.member?.sessionId} code={props.member?.sessionCode} kind="conversation" />
        </Show>
        <Show when={props.m.authorExecutionId}><EntityCode id={props.m.authorExecutionId!} kind="execution" /></Show>
        <AgeChip t={props.m.createdAt} />
      </header>
      <div class="stack">
        <Markdown text={props.m.text} />
        <Show when={props.m.artifactId} keyed>
          {artifactId => (
            <div class="stack">
              <div class="res-bar">
                <EntityCode id={artifactId} kind="result" label />
                <Show when={src() && props.m.kind === 'screenshot' && !imgFailed()}>
                  <a
                    class="row-link"
                    href={`/results/${encodeURIComponent(artifactId)}`}
                    onClick={e => {
                      if (!e.metaKey && !e.ctrlKey && !e.shiftKey && e.button === 0) {
                        e.preventDefault();
                        go(`/results/${encodeURIComponent(artifactId)}`);
                      }
                    }}
                  >
                    Open screenshot
                  </a>
                </Show>
              </div>
              <Show when={src() && props.m.kind === 'screenshot' && !imgFailed()}>
                <div class="av-frame">
                  <img
                    src={src()!}
                    alt={`Screenshot #${props.m.seq} by ${props.m.authorMember}`}
                    loading="lazy"
                    onError={() => setImgFailed(true)}
                  />
                </div>
              </Show>
            </div>
          )}
        </Show>
      </div>
    </section>
  );
}

function SwarmRosterTab(props: {
  threads: readonly WorkPlanThread[];
  roster: readonly WorkBoardRosterEntry[];
}) {
  const [sortCol, setSortCol] = createSignal<RosterSortCol | null>(null);
  const [sortDir, setSortDir] = createSignal<'ascending' | 'descending'>('ascending');
  const threadTitleMap = createMemo(() => new Map(props.threads.map(t => [t.id, t.title])));

  const toggleSort = (col: RosterSortCol) => {
    if (sortCol() === col) {
      setSortDir(d => d === 'ascending' ? 'descending' : 'ascending');
    } else {
      setSortCol(col);
      setSortDir(col === 'duration' || col === 'spend' || col === 'lastPost' ? 'descending' : 'ascending');
    }
  };

  const sorted = createMemo(() => {
    const col = sortCol();
    if (!col) return [...props.roster];
    const dir = sortDir() === 'ascending' ? 1 : -1;
    const tt = threadTitleMap();
    const cmpNum = (a: number | null | undefined, b: number | null | undefined) => {
      if (a == null && b == null) return 0;
      if (a == null) return 1;
      if (b == null) return -1;
      return dir * (a - b);
    };
    return props.roster.toSorted((a, b) => {
      switch (col) {
        case 'state': return dir * collator.compare(a.state ?? '', b.state ?? '');
        case 'member': return dir * collator.compare(a.memberName, b.memberName);
        case 'thread': return dir * collator.compare(tt.get(a.threadId) ?? a.threadId, tt.get(b.threadId) ?? b.threadId);
        case 'duration': return cmpNum(a.durationMs, b.durationMs);
        case 'spend': return cmpNum(primarySpendAmount(a.cost), primarySpendAmount(b.cost));
        case 'lastPost': return cmpNum(a.lastPostSeq, b.lastPostSeq);
      }
    });
  });

  const sortStatus = createMemo(() => (sortCol() ? `Sorted by ${sortCol()}, ${sortDir()}, ${sorted().length} workers` : ''));

  return (
    <Show when={props.roster.length > 0} fallback={<Empty text="No workers in the Swarm roster yet." />}>
      <section class="panel" aria-label="Swarm worker roster">
        <span class="sr-only" role="status">{sortStatus()}</span>
        <DataTable
          label="Swarm worker roster"
          head={
            <tr>
              <SortColHead label="State" active={sortCol() === 'state'} dir={sortDir()} onSort={() => toggleSort('state')} />
              <SortColHead label="Member" active={sortCol() === 'member'} dir={sortDir()} onSort={() => toggleSort('member')} />
              <th scope="col">Agent</th>
              <th scope="col">Host</th>
              <SortColHead label="Thread" active={sortCol() === 'thread'} dir={sortDir()} onSort={() => toggleSort('thread')} />
              <SortColHead label="Duration" active={sortCol() === 'duration'} dir={sortDir()} numeric onSort={() => toggleSort('duration')} />
              <SortColHead label="Spend" active={sortCol() === 'spend'} dir={sortDir()} numeric onSort={() => toggleSort('spend')} />
              <SortColHead label="Last post" active={sortCol() === 'lastPost'} dir={sortDir()} numeric onSort={() => toggleSort('lastPost')} />
              <th scope="col">Transcript</th>
            </tr>
          }
        >
          <For each={sorted()}>
            {r => {
              const b = rosterBadge(r.state);
              const tokTotal = (r.tokensInput ?? 0) + (r.tokensOutput ?? 0);
              const modelText = agentLabel(r.model) || r.harness || '—';
              return (
                <tr>
                  <td>
                    <StateBadge family={b.family} state={b.state as never} reason={b.reason} />
                  </td>
                  <th scope="row">
                    <div>{r.memberName}</div>
                    <div class="muted">
                      {r.role}
                      {r.workspaceMode ? ` · ${r.workspaceMode}` : ''}
                    </div>
                  </th>
                  <td>
                    <span class="agent-mark" data-tip={r.model ?? undefined}>
                      <Show when={isHarness(r.harness)}><HarnessMark h={r.harness!} /></Show>
                      <span class="lbl">{modelText}</span>
                    </span>
                  </td>
                  <td>
                    <Show when={r.hostId} fallback={<span class="muted">—</span>}>
                      <EntityCode id={r.hostId!} code={r.hostId!} kind="host" cls="inline" />
                    </Show>
                  </td>
                  <td>
                    {r.threadId ? (threadTitleMap().get(r.threadId) ?? r.threadId) : <span class="muted">—</span>}
                  </td>
                  <td class="num">
                    <div><Num m={msMeasured(r.durationMs)} kind="duration" /></div>
                    <Show when={tokTotal > 0}>
                      <div class="muted">
                        <Num m={{ value: tokTotal, basis: 'measured' }} kind="count" /> tok
                      </div>
                    </Show>
                  </td>
                  <td class="num">
                    <CostFigures cost={r.cost} empty="—" />
                  </td>
                  <td class="num">
                    <Show when={r.lastPostSeq != null} fallback={<span class="muted">—</span>}>
                      <span class="mono">#{r.lastPostSeq}</span>
                    </Show>
                  </td>
                  <td>
                    <span class="deps">
                      <Show when={r.sessionId}><EntityCode code={r.sessionCode} id={r.sessionId!} kind="conversation" /></Show>
                      <Show when={r.executionId}><EntityCode id={r.executionId!} kind="execution" /></Show>
                      <Show when={!r.sessionId && !r.executionId}><span class="muted">—</span></Show>
                    </span>
                  </td>
                </tr>
              );
            }}
          </For>
        </DataTable>
      </section>
    </Show>
  );
}

function SwarmTimelineTab(props: {
  threads: readonly WorkPlanThread[];
  roster: readonly WorkBoardRosterEntry[];
  milestones: readonly BoardMessage[];
  mediaMessages: readonly BoardMessage[];
}) {
  const rosterByName = createMemo(() => new Map(props.roster.map(r => [r.memberName, r])));
  const threadTitleMap = createMemo(() => new Map(props.threads.map(t => [t.id, t.title])));

  return (
    <>
      <section class="panel" aria-label="Handoff milestones">
        <header class="panel-h">
          <Icon name="git-merge" />
          <h2>Handoff milestones</h2>
          <span class="grow" />
          <span class="muted mono">{props.milestones.length}</span>
        </header>
        <div class="stack">
          <Show
            when={props.milestones.length > 0}
            fallback={<Empty text="No brief, blocker, or completion milestones yet." />}
          >
            <For each={props.milestones}>
              {m => {
                const km = KIND_META[m.kind];
                const member = rosterByName().get(m.authorMember);
                return (
                  <div class="dsec">
                    <KvRow
                      icon={km.icon}
                      label={`#${m.seq} · ${km.label} · ${m.authorMember}`}
                      meta={
                        <span class="deps">
                          <Show when={isHarness(member?.harness)}><HarnessMark h={member!.harness!} /></Show>
                          <Show when={m.threadId}><span class="mono">{threadTitleMap().get(m.threadId!) ?? m.threadId}</span></Show>
                          <For each={m.mentions}>{target => <span class="chip mono">@{target}</span>}</For>
                        </span>
                      }
                      value={
                        <span class="deps">
                          <Show when={m.artifactId}><EntityCode id={m.artifactId!} kind="result" /></Show>
                          <Show when={m.authorSessionId}><EntityCode id={m.authorSessionId!} code={member?.sessionCode} kind="conversation" /></Show>
                          <AgeChip t={m.createdAt} />
                        </span>
                      }
                    />
                    <Markdown text={m.text} />
                  </div>
                );
              }}
            </For>
          </Show>
        </div>
      </section>

      <section class="panel" aria-label="Screenshots and artifacts">
        <header class="panel-h">
          <Icon name="panels-top-left" />
          <h2>Screenshots & artifacts</h2>
          <span class="grow" />
          <span class="muted mono">{props.mediaMessages.length}</span>
        </header>
        <div class="stack">
          <Show
            when={props.mediaMessages.length > 0}
            fallback={<Empty text="No screenshots or attached artifacts posted yet." />}
          >
            <For each={props.mediaMessages}>
              {m => <SwarmMediaCard m={m} member={rosterByName().get(m.authorMember)} />}
            </For>
          </Show>
        </div>
      </section>
    </>
  );
}

function SwarmMediaCard(props: { m: BoardMessage; member?: WorkBoardRosterEntry }) {
  const artifact = useOp('artifact_get', () => (props.m.artifactId ? { artifactId: props.m.artifactId } : null));
  const a = () => artifact.data;
  const src = () => {
    const item = a();
    return item ? `/api/results/${encodeURIComponent(item.id)}` : null;
  };

  return (
    <div class="dsec">
      <KvRow
        icon={KIND_META[props.m.kind].icon}
        label={`#${props.m.seq} · ${props.m.authorMember}`}
        meta={isHarness(props.member?.harness) ? <HarnessMark h={props.member!.harness!} /> : undefined}
        value={
          <span class="deps">
            <Show when={props.m.artifactId}><EntityCode id={props.m.artifactId!} kind="result" label /></Show>
            <AgeChip t={props.m.createdAt} />
          </span>
        }
      />
      <Show when={props.m.text}><Markdown text={props.m.text} /></Show>
      <Show when={props.m.artifactId}>
        <Switch
          fallback={
            <Show when={a() && a()!.state === 'stored' && src()} fallback={<Empty text={`Artifact is ${a()?.state ?? ''}.`} />}>
              <Disclosure summary={a()!.name} open={props.m.kind === 'screenshot'}>
                <ArtifactView a={{ id: a()!.id, name: a()!.name, mediaType: a()!.mediaType, size: Number(a()!.size) || 0 }} src={src()!} />
              </Disclosure>
            </Show>
          }
        >
          <Match when={artifact.error && !a()}>
            <ErrorBanner text={`Artifact unavailable: ${artifact.error}`} onRetry={artifact.reload} />
          </Match>
          <Match when={!a()}>
            <Skeleton rows={2} />
          </Match>
        </Switch>
      </Show>
    </div>
  );
}
