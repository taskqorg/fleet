// Reusable Fleet Swarm GUI panel (docs/designs/fleet-swarm.md, OR-1..OR-49):
// Overview (plan, metrics, thread cards), Board (live filterable feed), Roster (real workers), Timeline & Screenshots.
import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
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
export function useSwarmBoard(scopeKind: BoardScopeKind, scopeId: string | null | undefined) {
  const scopeRef = useRef<string[]>([]);
  const res = useOp(
    'work_board_get',
    scopeId ? { scopeKind, scopeId } : null,
    { quiet: 500, scope: scopeId ? { ids: scopeRef.current } : undefined },
  );
  const d = res.data;
  const nextIds = useMemo(() => {
    if (!scopeId) return [];
    const set = new Set<string>([scopeId]);
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
  }, [scopeId, d]);
  scopeRef.current = nextIds;
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
export function SwarmPanel({
  scopeKind,
  scopeId,
  board: externalBoard,
}: {
  scopeKind: BoardScopeKind;
  scopeId: string;
  board?: ReturnType<typeof useSwarmBoard>;
}) {
  const internalBoard = useSwarmBoard(scopeKind, externalBoard ? null : scopeId);
  const board = externalBoard ?? internalBoard;
  const [tab, setTab] = useState<SwarmTab>('overview');
  const [threadFilter, setThreadFilter] = useState<string | null>(null);
  const [kindFilter, setKindFilter] = useState<BoardKindFilter>('all');
  const [search, setSearch] = useState('');
  useEffect(() => {
    setTab('overview');
    setThreadFilter(null);
    setKindFilter('all');
    setSearch('');
  }, [scopeKind, scopeId]);

  if (board.error && !board.data) {
    return <ErrorBanner text={`Swarm board did not load: ${board.error}`} onRetry={board.reload} />;
  }
  if (!board.data) {
    return <Skeleton rows={5} />;
  }
  if (!hasSwarmBoard(board.data)) {
    return <Empty text="No Swarm plan or board activity yet." />;
  }

  const data = board.data;
  const plan = data.plan;
  const threads = plan?.threads ?? [];
  const roster = data.roster;
  const messages = data.messages;
  const summary = data.summary;

  const milestones = messages.filter(m => m.kind === 'brief' || m.kind === 'blocker' || m.kind === 'done');
  const mediaMessages = messages.filter(m => m.kind === 'screenshot' || !!m.artifactId);
  const timelineCount = milestones.length + mediaMessages.length;

  const tabs: { id: SwarmTab; label: string; count?: number }[] = [
    { id: 'overview', label: 'Overview', count: threads.length || undefined },
    { id: 'board', label: 'Board', count: summary.messagesTotal || undefined },
    { id: 'roster', label: 'Roster', count: summary.membersTotal || undefined },
    { id: 'timeline', label: 'Timeline & Screenshots', count: timelineCount || undefined },
  ];

  const openThreadOnBoard = (threadId: string) => {
    setThreadFilter(threadId);
    setKindFilter('all');
    setTab('board');
  };

  return (
    <div class="stack" data-ui="swarm-panel">
      <TabBar cls="detail-tabs" label="Swarm views" tabs={tabs} value={tab} onChange={setTab} />
      {tab === 'overview' && (
        <div role="tabpanel" aria-label="Overview" class="stack">
          <SwarmOverviewTab data={data} onOpenThread={openThreadOnBoard} />
        </div>
      )}
      {tab === 'board' && (
        <div role="tabpanel" aria-label="Board" class="stack">
          <SwarmBoardTab
            threads={threads}
            roster={roster}
            messages={messages}
            screenshotsCount={summary.screenshotsCount}
            threadFilter={threadFilter}
            onThreadFilter={setThreadFilter}
            kindFilter={kindFilter}
            onKindFilter={setKindFilter}
            search={search}
            onSearch={setSearch}
          />
        </div>
      )}
      {tab === 'roster' && (
        <div role="tabpanel" aria-label="Roster" class="stack">
          <SwarmRosterTab threads={threads} roster={roster} />
        </div>
      )}
      {tab === 'timeline' && (
        <div role="tabpanel" aria-label="Timeline & Screenshots" class="stack">
          <SwarmTimelineTab threads={threads} roster={roster} milestones={milestones} mediaMessages={mediaMessages} />
        </div>
      )}
    </div>
  );
}

function SwarmOverviewTab({
  data,
  onOpenThread,
}: {
  data: WorkBoardGetReply;
  onOpenThread: (threadId: string) => void;
}) {
  const { plan, roster, messages, summary } = data;
  const threads = plan?.threads ?? [];

  const threadsDone = summary.threadsByStatus.done ?? 0;
  const tasksDone = summary.tasksByStatus.done ?? 0;
  const activeWorkers = (summary.membersByState.running ?? 0)
    + (summary.membersByState.preparing ?? 0)
    + (summary.membersByState.waiting ?? 0);

  const validHarnessCounts = useMemo(() => {
    const out: Partial<Record<Harness, number>> = {};
    for (const [k, v] of Object.entries(summary.harnessCounts)) {
      if (isHarness(k) && v > 0) out[k] = v;
    }
    return out;
  }, [summary.harnessCounts]);

  const hasHarnesses = Object.keys(validHarnessCounts).length > 0;
  const mergedSpend = useMemo(() => mergeBoardCosts(roster.map(r => r.cost)), [roster]);
  const hasVisibleSpend = mergedSpend.some(c => (c.value !== null && c.basis !== 'unavailable') || c.basis === 'subscription');

  const stats: Stat[] = [
    ...(summary.threadsTotal > 0 ? [{
      label: 'threads done',
      icon: 'git-merge' as IconName,
      value: { value: threadsDone, basis: 'measured' as const },
      of: summary.threadsTotal,
      tone: threadsDone === summary.threadsTotal ? ('success' as const) : undefined,
    }] : []),
    ...(summary.tasksTotal > 0 ? [{
      label: 'tasks done',
      icon: 'check-check' as IconName,
      value: { value: tasksDone, basis: 'measured' as const },
      of: summary.tasksTotal,
      tone: tasksDone === summary.tasksTotal ? ('success' as const) : undefined,
    }] : []),
    ...(activeWorkers > 0 ? [{
      label: 'active workers',
      icon: 'activity' as IconName,
      value: { value: activeWorkers, basis: 'measured' as const },
      tone: 'running' as const,
      ...(hasHarnesses ? { harness: validHarnessCounts } : {}),
    }] : []),
    ...(summary.messagesTotal > 0 ? [{
      label: 'board posts',
      icon: 'messages-square' as IconName,
      value: { value: summary.messagesTotal, basis: 'measured' as const },
    }] : []),
    ...(summary.blockersCount > 0 ? [{
      label: 'blockers',
      icon: 'triangle-alert' as IconName,
      value: { value: summary.blockersCount, basis: 'measured' as const },
      tone: 'danger' as const,
    }] : []),
  ];

  const rosterByName = useMemo(() => new Map(roster.map(r => [r.memberName, r])), [roster]);
  const postsByThread = useMemo(() => {
    const counts = new Map<string, number>();
    for (const m of messages) {
      if (m.threadId) counts.set(m.threadId, (counts.get(m.threadId) ?? 0) + 1);
    }
    return counts;
  }, [messages]);

  return (
    <>
      {plan && (
        <section class="panel" aria-label={plan.title}>
          <header class="panel-h">
            <Icon name="network" />
            <h2>{plan.title}</h2>
            <span class="grow" />
            {plan.strategy && <span class="chip mono" data-tip="Coordination strategy">{plan.strategy}</span>}
            <span class="chip mono" data-tip={`Updated ${exact(plan.updatedAt)}`}>v{plan.version}</span>
          </header>
          {plan.goal && (
            <div class="stack">
              <OutcomeBlock outcome={plan.goal} requirements={[]} inputs={[]} />
            </div>
          )}
        </section>
      )}

      {stats.length > 0 && <StatStrip label="Swarm summary" stats={stats} />}

      {hasVisibleSpend && (
        <Totals items={[{ label: 'Spend', cost: mergedSpend, showUnavailable: false }]} compact />
      )}

      {hasHarnesses && activeWorkers === 0 && (
        <div class="res-bar">
          <span class="muted">Harnesses</span>
          <HarnessCounts counts={validHarnessCounts} label="Workers by harness" />
        </div>
      )}

      {threads.length > 0 ? (
        threads.map(thread => (
          <ThreadCard
            key={thread.id}
            thread={thread}
            roster={roster}
            rosterByName={rosterByName}
            postCount={postsByThread.get(thread.id) ?? 0}
            onOpenThread={() => onOpenThread(thread.id)}
          />
        ))
      ) : (
        <Empty text="No work plan threads defined yet; inspect the Board tab for messages." />
      )}
    </>
  );
}

function ThreadCard({
  thread,
  roster,
  rosterByName,
  postCount,
  onOpenThread,
}: {
  thread: WorkPlanThread;
  roster: readonly WorkBoardRosterEntry[];
  rosterByName: Map<string, WorkBoardRosterEntry>;
  postCount: number;
  onOpenThread: () => void;
}) {
  const st = THREAD_STATE[thread.status ?? 'planned'];
  const doneCount = thread.tasks.filter(t => t.status === 'done').length;

  const workers = useMemo(() => {
    const map = new Map<string, WorkBoardRosterEntry>();
    for (const r of roster) {
      if (r.threadId === thread.id) map.set(r.memberName, r);
    }
    for (const t of thread.tasks) {
      const m = t.ownerMember?.trim();
      if (!m || map.has(m)) continue;
      const existing = rosterByName.get(m);
      if (existing) {
        map.set(m, existing);
      } else {
        map.set(m, {
          memberName: m,
          role: t.role ?? 'worker',
          threadId: thread.id,
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
  }, [thread, roster, rosterByName]);

  return (
    <section class="panel" aria-label={thread.title}>
      <header class="panel-h">
        <StateBadge family="task" state={st.state} reason={st.reason} compact />
        <h2>{thread.title}</h2>
        <span class="id">{thread.id}</span>
        <span class="grow" />
        <span class="muted mono" data-tip={`${doneCount} of ${thread.tasks.length} tasks done`}>{doneCount}/{thread.tasks.length}</span>
        <Button
          label={postCount > 0 ? `Board (${postCount})` : 'Board'}
          icon="messages-square"
          kind="quiet"
          tip={`Filter Board to ${thread.title}`}
          onClick={onOpenThread}
        />
      </header>
      <div class="stack">
        {thread.goal && <p class="muted" style={{ margin: 0 }}>{thread.goal}</p>}

        {workers.length > 0 && (
          <div class="tag-row" role="group" aria-label={`Workers in ${thread.title}`}>
            {workers.map(w => {
              const b = rosterBadge(w.state);
              const tip = [w.role, w.model ? agentLabel(w.model) : w.harness, w.hostId].filter(Boolean).join(' · ') || undefined;
              return (
                <span key={w.memberName} class="chip in" data-tip={tip}>
                  <StateBadge family={b.family} state={b.state as never} reason={b.reason} compact />
                  {isHarness(w.harness) && <HarnessMark h={w.harness} />}
                  <span>{w.memberName}</span>
                  {w.role && w.role !== 'worker' && <span class="muted">{w.role}</span>}
                </span>
              );
            })}
          </div>
        )}

        {thread.tasks.length > 0 && (
          <DetailSection title="Sub-tasks">
            {thread.tasks.map(tk => {
              const tst = TASK_STATE[tk.status ?? 'pending'];
              const pathLabel = tk.ownedPaths?.length === 1
                ? tk.ownedPaths[0]
                : tk.ownedPaths && tk.ownedPaths.length > 1
                  ? `${tk.ownedPaths.length} paths`
                  : null;
              return (
                <div key={tk.id} class="kv-row" style={{ flexWrap: 'wrap' }}>
                  <StateBadge family="task" state={tst.state} reason={tst.reason} compact />
                  <b>{tk.title}</b>
                  {tk.ownerMember && (
                    <span class="muted">
                      {tk.ownerMember}
                      {tk.workspaceMode ? ` · ${tk.workspaceMode}` : ''}
                    </span>
                  )}
                  {tk.dependsOn && tk.dependsOn.length > 0 && (
                    <span class="muted mono" data-tip={`Depends on ${tk.dependsOn.join(', ')}`}>
                      after {tk.dependsOn.join(', ')}
                    </span>
                  )}
                  {pathLabel && (
                    <span class="muted mono" data-tip={tk.ownedPaths!.join('\n')}>
                      {pathLabel}
                    </span>
                  )}
                  <span class="grow" />
                  {isHarness(tk.harness) && <HarnessMark h={tk.harness} />}
                  {tk.sessionId && <EntityCode id={tk.sessionId} kind="conversation" />}
                  {tk.executionId && <EntityCode id={tk.executionId} kind="execution" />}
                  {tk.taskId && <EntityCode id={tk.taskId} kind="task" />}
                </div>
              );
            })}
          </DetailSection>
        )}
      </div>
    </section>
  );
}

function SwarmBoardTab({
  threads,
  roster,
  messages,
  screenshotsCount,
  threadFilter,
  onThreadFilter,
  kindFilter,
  onKindFilter,
  search,
  onSearch,
}: {
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
  const rosterByName = useMemo(() => new Map(roster.map(r => [r.memberName, r])), [roster]);
  const threadTitleMap = useMemo(() => new Map(threads.map(t => [t.id, t.title])), [threads]);

  const threadPickerNodes: CatalogueTreeNode[] = useMemo(() => threads.map(t => ({
    id: t.id,
    label: t.title,
    sub: t.id,
    icon: 'git-merge',
    searchText: `${t.title} ${t.id} ${t.goal ?? ''}`,
  })), [threads]);

  const filtered = useMemo(() => messages.filter(m => {
    if (threadFilter && m.threadId !== threadFilter) return false;
    if (kindFilter !== 'all' && m.kind !== kindFilter) return false;
    if (!search.trim()) return true;
    const text = [m.text, m.authorMember, m.authorRole, m.threadId, threadTitleMap.get(m.threadId), ...m.mentions].filter(Boolean).join(' ');
    return matchSearch(search, text, {
      agent: rosterByName.get(m.authorMember)?.harness ?? null,
      state: m.kind,
    });
  }), [messages, threadFilter, kindFilter, search, threadTitleMap, rosterByName]);

  return (
    <>
      <div class="fbar" data-ui="filter-bar">
        <div class="fbar-line">
          <SearchField label="Search board" value={search} onInput={onSearch} onSearch={() => {}} />
          <Choice
            label="Message kind"
            options={KIND_CHOICES}
            value={kindFilter === 'screenshot' ? '' : kindFilter}
            onChange={id => onKindFilter(id as BoardKindFilter)}
          />
          {(screenshotsCount > 0 || kindFilter === 'screenshot') && (
            <Button
              label="Screenshots"
              icon="panels-top-left"
              kind="quiet"
              pressed={kindFilter === 'screenshot'}
              onClick={() => onKindFilter(kindFilter === 'screenshot' ? 'all' : 'screenshot')}
            />
          )}
          <span class="count" aria-live="polite">
            {filtered.length < messages.length
              ? `${filtered.length.toLocaleString('en')} of ${messages.length.toLocaleString('en')}`
              : `${messages.length.toLocaleString('en')} posts`}
          </span>
        </div>
        {(threads.length > 1 || threadFilter !== null) && (
          <div class="fbar-line">
            <EntityPicker
              label="Thread"
              mode="single"
              nodes={threadPickerNodes}
              value={threadFilter}
              placeholder="All threads"
              searchPlaceholder="Find thread"
              onChange={onThreadFilter}
            />
            {threadFilter !== null && (
              <Button label="All threads" icon="x" kind="quiet" onClick={() => onThreadFilter(null)} />
            )}
          </div>
        )}
      </div>

      {filtered.length === 0 ? (
        <Empty
          text={messages.length === 0 ? 'No board posts yet.' : 'No board posts match these filters.'}
          action={messages.length > 0 ? (
            <Button label="Clear filters" onClick={() => { onSearch(''); onKindFilter('all'); onThreadFilter(null); }} />
          ) : undefined}
        />
      ) : (
        <div class="stack">
          <Latest
            n={25}
            items={filtered.map(m => (
              <BoardMessageCard
                key={m.id}
                m={m}
                member={rosterByName.get(m.authorMember)}
                threadTitle={m.threadId ? threadTitleMap.get(m.threadId) : undefined}
              />
            ))}
          />
        </div>
      )}
    </>
  );
}

function BoardMessageCard({
  m,
  member,
  threadTitle,
}: {
  m: BoardMessage;
  member?: WorkBoardRosterEntry;
  threadTitle?: string;
}) {
  const km = KIND_META[m.kind];
  const [, go] = useLocation();
  const [imgFailed, setImgFailed] = useState(false);
  const src = m.artifactId ? `/api/results/${encodeURIComponent(m.artifactId)}` : null;

  return (
    <section class="panel" aria-label={`Board post #${m.seq} by ${m.authorMember}`}>
      <header class="panel-h" style={{ flexWrap: 'wrap' }}>
        <span class="mono muted">#{m.seq}</span>
        <span class={`badge t-${km.tone}`}>
          <Icon name={km.icon} />
          <span>{km.label}</span>
        </span>
        {isHarness(member?.harness) && <HarnessMark h={member.harness} />}
        <b>{m.authorMember}</b>
        {m.authorRole && <span class="muted">{m.authorRole}</span>}
        {m.threadId && <span class="chip mono" data-tip={m.threadId}>{threadTitle ?? m.threadId}</span>}
        {m.mentions.map(target => (
          <span key={target} class="chip mono">@{target}</span>
        ))}
        <span class="grow" />
        {(m.authorSessionId || member?.sessionCode) && (
          <EntityCode id={m.authorSessionId ?? member?.sessionId} code={member?.sessionCode} kind="conversation" />
        )}
        {m.authorExecutionId && <EntityCode id={m.authorExecutionId} kind="execution" />}
        <AgeChip t={m.createdAt} />
      </header>
      <div class="stack">
        <Markdown text={m.text} />
        {m.artifactId && (
          <div class="stack">
            <div class="res-bar">
              <EntityCode id={m.artifactId} kind="result" label />
              {src && m.kind === 'screenshot' && !imgFailed && (
                <a
                  class="row-link"
                  href={`/results/${encodeURIComponent(m.artifactId)}`}
                  onClick={e => {
                    if (!e.metaKey && !e.ctrlKey && !e.shiftKey && e.button === 0) {
                      e.preventDefault();
                      go(`/results/${encodeURIComponent(m.artifactId!)}`);
                    }
                  }}
                >
                  Open screenshot
                </a>
              )}
            </div>
            {src && m.kind === 'screenshot' && !imgFailed && (
              <div class="av-frame">
                <img
                  src={src}
                  alt={`Screenshot #${m.seq} by ${m.authorMember}`}
                  loading="lazy"
                  onError={() => setImgFailed(true)}
                />
              </div>
            )}
          </div>
        )}
      </div>
    </section>
  );
}

function SwarmRosterTab({
  threads,
  roster,
}: {
  threads: readonly WorkPlanThread[];
  roster: readonly WorkBoardRosterEntry[];
}) {
  const [sortCol, setSortCol] = useState<RosterSortCol | null>(null);
  const [sortDir, setSortDir] = useState<'ascending' | 'descending'>('ascending');
  const threadTitleMap = useMemo(() => new Map(threads.map(t => [t.id, t.title])), [threads]);

  const toggleSort = (col: RosterSortCol) => {
    if (sortCol === col) {
      setSortDir(d => d === 'ascending' ? 'descending' : 'ascending');
    } else {
      setSortCol(col);
      setSortDir(col === 'duration' || col === 'spend' || col === 'lastPost' ? 'descending' : 'ascending');
    }
  };

  const sorted = useMemo(() => {
    if (!sortCol) return [...roster];
    const dir = sortDir === 'ascending' ? 1 : -1;
    const cmpNum = (a: number | null | undefined, b: number | null | undefined) => {
      if (a == null && b == null) return 0;
      if (a == null) return 1;
      if (b == null) return -1;
      return dir * (a - b);
    };
    return roster.toSorted((a, b) => {
      switch (sortCol) {
        case 'state': return dir * collator.compare(a.state ?? '', b.state ?? '');
        case 'member': return dir * collator.compare(a.memberName, b.memberName);
        case 'thread': return dir * collator.compare(threadTitleMap.get(a.threadId) ?? a.threadId, threadTitleMap.get(b.threadId) ?? b.threadId);
        case 'duration': return cmpNum(a.durationMs, b.durationMs);
        case 'spend': return cmpNum(primarySpendAmount(a.cost), primarySpendAmount(b.cost));
        case 'lastPost': return cmpNum(a.lastPostSeq, b.lastPostSeq);
      }
    });
  }, [roster, sortCol, sortDir, threadTitleMap]);

  if (roster.length === 0) {
    return <Empty text="No workers in the Swarm roster yet." />;
  }

  const sortStatus = sortCol ? `Sorted by ${sortCol}, ${sortDir}, ${sorted.length} workers` : '';

  return (
    <section class="panel" aria-label="Swarm worker roster">
      <span class="sr-only" role="status">{sortStatus}</span>
      <DataTable
        label="Swarm worker roster"
        head={
          <tr>
            <SortColHead label="State" active={sortCol === 'state'} dir={sortDir} onSort={() => toggleSort('state')} />
            <SortColHead label="Member" active={sortCol === 'member'} dir={sortDir} onSort={() => toggleSort('member')} />
            <th scope="col">Agent</th>
            <th scope="col">Host</th>
            <SortColHead label="Thread" active={sortCol === 'thread'} dir={sortDir} onSort={() => toggleSort('thread')} />
            <SortColHead label="Duration" active={sortCol === 'duration'} dir={sortDir} numeric onSort={() => toggleSort('duration')} />
            <SortColHead label="Spend" active={sortCol === 'spend'} dir={sortDir} numeric onSort={() => toggleSort('spend')} />
            <SortColHead label="Last post" active={sortCol === 'lastPost'} dir={sortDir} numeric onSort={() => toggleSort('lastPost')} />
            <th scope="col">Transcript</th>
          </tr>
        }
      >
        {sorted.map(r => {
          const b = rosterBadge(r.state);
          const tokTotal = (r.tokensInput ?? 0) + (r.tokensOutput ?? 0);
          const modelText = agentLabel(r.model) || r.harness || '—';
          return (
            <tr key={r.memberName}>
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
                  {isHarness(r.harness) && <HarnessMark h={r.harness} />}
                  <span class="lbl">{modelText}</span>
                </span>
              </td>
              <td>
                {r.hostId ? <EntityCode id={r.hostId} code={r.hostId} kind="host" cls="inline" /> : <span class="muted">—</span>}
              </td>
              <td>
                {r.threadId ? (threadTitleMap.get(r.threadId) ?? r.threadId) : <span class="muted">—</span>}
              </td>
              <td class="num">
                <div><Num m={msMeasured(r.durationMs)} kind="duration" /></div>
                {tokTotal > 0 && (
                  <div class="muted">
                    <Num m={{ value: tokTotal, basis: 'measured' }} kind="count" /> tok
                  </div>
                )}
              </td>
              <td class="num">
                <CostFigures cost={r.cost} empty="—" />
              </td>
              <td class="num">
                {r.lastPostSeq != null ? <span class="mono">#{r.lastPostSeq}</span> : <span class="muted">—</span>}
              </td>
              <td>
                <span class="deps">
                  {r.sessionId && <EntityCode code={r.sessionCode} id={r.sessionId} kind="conversation" />}
                  {r.executionId && <EntityCode id={r.executionId} kind="execution" />}
                  {!r.sessionId && !r.executionId && <span class="muted">—</span>}
                </span>
              </td>
            </tr>
          );
        })}
      </DataTable>
    </section>
  );
}

function SwarmTimelineTab({
  threads,
  roster,
  milestones,
  mediaMessages,
}: {
  threads: readonly WorkPlanThread[];
  roster: readonly WorkBoardRosterEntry[];
  milestones: readonly BoardMessage[];
  mediaMessages: readonly BoardMessage[];
}) {
  const rosterByName = useMemo(() => new Map(roster.map(r => [r.memberName, r])), [roster]);
  const threadTitleMap = useMemo(() => new Map(threads.map(t => [t.id, t.title])), [threads]);

  return (
    <>
      <section class="panel" aria-label="Handoff milestones">
        <header class="panel-h">
          <Icon name="git-merge" />
          <h2>Handoff milestones</h2>
          <span class="grow" />
          <span class="muted mono">{milestones.length}</span>
        </header>
        <div class="stack">
          {milestones.length === 0 ? (
            <Empty text="No brief, blocker, or completion milestones yet." />
          ) : (
            milestones.map(m => {
              const km = KIND_META[m.kind];
              const member = rosterByName.get(m.authorMember);
              return (
                <div key={m.id} class="dsec">
                  <KvRow
                    icon={km.icon}
                    label={`#${m.seq} · ${km.label} · ${m.authorMember}`}
                    meta={
                      <span class="deps">
                        {isHarness(member?.harness) && <HarnessMark h={member.harness} />}
                        {m.threadId && <span class="mono">{threadTitleMap.get(m.threadId) ?? m.threadId}</span>}
                        {m.mentions.map(target => <span key={target} class="chip mono">@{target}</span>)}
                      </span>
                    }
                    value={
                      <span class="deps">
                        {m.artifactId && <EntityCode id={m.artifactId} kind="result" />}
                        {m.authorSessionId && <EntityCode id={m.authorSessionId} code={member?.sessionCode} kind="conversation" />}
                        <AgeChip t={m.createdAt} />
                      </span>
                    }
                  />
                  <Markdown text={m.text} />
                </div>
              );
            })
          )}
        </div>
      </section>

      <section class="panel" aria-label="Screenshots and artifacts">
        <header class="panel-h">
          <Icon name="panels-top-left" />
          <h2>Screenshots & artifacts</h2>
          <span class="grow" />
          <span class="muted mono">{mediaMessages.length}</span>
        </header>
        <div class="stack">
          {mediaMessages.length === 0 ? (
            <Empty text="No screenshots or attached artifacts posted yet." />
          ) : (
            mediaMessages.map(m => (
              <SwarmMediaCard key={m.id} m={m} member={rosterByName.get(m.authorMember)} />
            ))
          )}
        </div>
      </section>
    </>
  );
}

function SwarmMediaCard({ m, member }: { m: BoardMessage; member?: WorkBoardRosterEntry }) {
  const artifact = useOp('artifact_get', m.artifactId ? { artifactId: m.artifactId } : null);
  const a = artifact.data;
  const src = a ? `/api/results/${encodeURIComponent(a.id)}` : null;

  return (
    <div class="dsec">
      <KvRow
        icon={KIND_META[m.kind].icon}
        label={`#${m.seq} · ${m.authorMember}`}
        meta={isHarness(member?.harness) ? <HarnessMark h={member.harness} /> : undefined}
        value={
          <span class="deps">
            {m.artifactId && <EntityCode id={m.artifactId} kind="result" label />}
            <AgeChip t={m.createdAt} />
          </span>
        }
      />
      {m.text && <Markdown text={m.text} />}
      {m.artifactId && (
        artifact.error && !a ? (
          <ErrorBanner text={`Artifact unavailable: ${artifact.error}`} onRetry={artifact.reload} />
        ) : !a ? (
          <Skeleton rows={2} />
        ) : a.state === 'stored' && src ? (
          <Disclosure summary={a.name} open={m.kind === 'screenshot'}>
            <ArtifactView a={{ id: a.id, name: a.name, mediaType: a.mediaType, size: Number(a.size) || 0 }} src={src} />
          </Disclosure>
        ) : (
          <Empty text={`Artifact is ${a.state}.`} />
        )
      )}
    </div>
  );
}
