// Tasks list and detail (docs/design-system/pages/tasks.md). Server-ordered list with views, state counters, grouping and
// search (list-view pattern, list-page.tsx); the detail shows outcome, totals, open decisions and the execution tree.
import { useEffect, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  ListDetail, RowFacts, Fact, ProblemRow, StateBadge, HarnessMark, StageChip, AgeChip, Button,
  SplitButton, Icon, DetailSection, Markdown, actions, OutcomeBlock, DecisionBlock, ExecTree, Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses,
  OpsSummary, StatStrip, StateCounters, KvRow, Num, TabBar,
  type TreeNode, type Harness, type Stage, type StateOf, type Stat, states as states_, stages,
  keyConnection, EntityCode, actor, fileLabel, viewOf, ShellStatus } from '../../../packages/ui/src/index.ts';
import { op, online, addRequestedKey, writeOnce, useAlive } from '../api.ts';
import { useOp } from '../store.ts';
import type { TaskRow as ContractTaskRow } from '../../../contracts/generated/ops.ts';
import { DEFAULT_TASK_SORT, DEFAULT_TASK_GROUP, type View } from '../../../packages/view-url/view-url.ts';
import { usePins, prefs, setFocus } from '../prefs.ts';
import { useListPage, ListPagePanel, type ListPage } from '../list-page.tsx';
import { SYSTEM_VIEWS } from '../system-views.ts';
import { ScopedRollupBar, buildExecTree } from './resources.tsx';
import { SwarmPanel, hasSwarmBoard, useSwarmBoard } from './swarm-panel.tsx';

const GROUPS = [['priority', 'Priority'], ['state', 'State'], ['project', 'Project'], ['agent', 'Agent'], ['host', 'Host'], ['none', 'None']] as const;
const SORTS = [['changed', 'Changed'], ['created', 'Created']] as const;
/** The contract's task row; its state is a list state (held included). */
type TaskRow = ContractTaskRow & { state: StateOf<'task'>; stage?: Stage | null };
const ms = (v: string | number | null | undefined) => (v === null || v === undefined ? null : typeof v === 'number' ? v : Date.parse(v));
const isHarness = (h: string | null): h is Harness => !!h && h in harnesses;
const isStage = (s: string | null | undefined): s is Stage => !!s && s in stages;

// System presets (P5): system-views.ts. Running: every non-terminal state.
// Saved views from before the project default kept no group: they were grouped by priority.
const savedView = (v: View): View => v.group ? v : { ...v, group: 'priority' };

export function Tasks({ id }: { id?: string }) {
  // Pinned tasks are read by id with an empty filter and live through task changes.
  const page = useListPage<TaskRow>({
    view: 'tasks', entity: 'task', kind: 'tasks', defaultGroup: DEFAULT_TASK_GROUP, defaultSort: DEFAULT_TASK_SORT, system: SYSTEM_VIEWS.tasks, groupLabel,
    pinRead: () => ({ filter: {} }),
    savedView, saveGroup: DEFAULT_TASK_GROUP,
    // Completed (7) and cancelled (8) start collapsed under state grouping (the state group keys of task_page, attention
    // order). A filter to those states alone is a context of its own, where they start open.
    closedGroups: { state: ['7', '8'] }, groupsContext: v => v.state?.length && v.state.every(s => s === 'completed' || s === 'cancelled') ? 'done' : undefined,
  });
  const { view, set, list, chosen, off, rowsGroup, confirm } = page;
  const canPause = (t: TaskRow) => !TERMINAL.includes(t.state) && t.state !== 'paused', canResume = (t: TaskRow) => t.state === 'paused';
  const bulk = (action: 'pause' | 'resume' | 'cancel', ts: TaskRow[]) => page.bulk(ts, t => op('task_update', { taskId: t.id, action }), `did not ${action}`);
  const now = Date.now();
  // The empty text names the focus only when a focus is set: ignored projects alone are not a focus "Show all projects" could clear.
  const { filtered } = page, focused = !view.project?.length && prefs.value.focus.length > 0;
  const listPane = <>
    <ShellStatus><OfflineMarker state={list.state} since={list.at || undefined} now={now} /></ShellStatus>
    <ListPagePanel page={page} noun="Tasks" placeholder="Search tasks, agent:claude host:mac" family="task" groups={GROUPS} sorts={SORTS} focused={id}
      actions={() => [
        { label: 'Pause', icon: 'pause', onClick: () => bulk('pause', chosen.filter(canPause)), disabledReason: off ?? (chosen.some(canPause) ? undefined : 'None can pause') },
        { label: 'Continue', icon: 'rotate-cw', onClick: () => bulk('resume', chosen.filter(canResume)), disabledReason: off ?? (chosen.some(canResume) ? undefined : 'None paused') },
        { label: 'Cancel', icon: 'circle-slash', danger: true, onClick: () => page.setConfirm(chosen.filter(t => !TERMINAL.includes(t.state))), disabledReason: off ?? (chosen.some(t => !TERMINAL.includes(t.state)) ? undefined : 'None can cancel') },
      ]}
      menu={t => [
        ...(canResume(t) ? [{ label: t.pauseReason === 'held' ? 'Release' : 'Continue', icon: 'rotate-cw' as const, disabledReason: off, onClick: () => bulk('resume', [t]) }] : []),
        ...(canPause(t) ? [{ label: 'Pause', icon: 'pause' as const, disabledReason: off, onClick: () => bulk('pause', [t]) }] : []),
      ]}
      confirm={confirm && <ProblemRow severity="warning" title={`Cancel ${confirm.length} ${confirm.length === 1 ? 'task' : 'tasks'}?`} facts="Running work is asked to stop; finished external effects stay."
        actions={[{ label: 'Yes, cancel', primary: true, onClick: () => { page.setConfirm(null); bulk('cancel', confirm); } }, { label: 'Keep', onClick: () => page.setConfirm(null) }]} />}
      empty={<Empty text={filtered ? 'No tasks match these filters.' : focused ? 'No tasks in the focused projects.' : 'No tasks yet.'} action={filtered ? <Button label="Clear filters" onClick={page.clearFilters} /> : focused ? <Button label="Show all projects" onClick={() => setFocus([])} /> : undefined} />}
      listLabel="Tasks" title={t => t.title} groupIcon={g => g === 'priority' ? 'list-ordered' : 'folder-open'}
      state={t => <StateBadge family="task" state={t.pauseReason === 'held' ? 'held' : t.state} reason={t.waitReason ?? undefined} compact />}
      before={t => rowsGroup !== 'priority' && <PriorityChip p={t.priority} />}
      facts={t => <TaskFacts t={t} />} />
  </>;
  return <ListDetail list={listPane} detail={id ? <TaskDetail id={id} onClose={() => set({ focus: undefined })} /> : undefined} idle={<TasksSummary page={page} filtered={filtered} focused={focused} />} detailKey={id} />;
}

function TasksSummary({ page, filtered, focused }: { page: ListPage<TaskRow>; filtered: boolean; focused: boolean }) {
  const { list, view, set, rowsGroup } = page;
  const counts = list.counts as Partial<Record<StateOf<'task'>, number>>;
  const activeStats: Stat[] = ([
    { label: 'total', icon: 'list', value: { value: list.total, basis: 'measured' } },
    { label: 'waiting', icon: states_.task.waiting.icon, value: { value: counts.waiting ?? 0, basis: 'measured' }, tone: (counts.waiting ?? 0) > 0 ? 'attention' : undefined },
    { label: 'failed', icon: states_.task.failed.icon, value: { value: counts.failed ?? 0, basis: 'measured' }, tone: (counts.failed ?? 0) > 0 ? 'danger' : undefined },
    { label: 'in progress', icon: states_.task.in_progress.icon, value: { value: counts.in_progress ?? 0, basis: 'measured' } },
    { label: 'queued', icon: states_.task.queued.icon, value: { value: counts.queued ?? 0, basis: 'measured' } },
    { label: 'paused', icon: states_.task.paused.icon, value: { value: counts.paused ?? 0, basis: 'measured' } },
  ] satisfies Stat[]).filter(s => (s.value.value ?? 0) > 0);
  const nonZeroGroups = list.groups.filter(g => g.count > 0);
  const groupTitle = GROUPS.find(g => g[0] === rowsGroup)?.[1] ?? rowsGroup;
  const hasCounts = Object.values(counts).some(n => (n ?? 0) > 0);

  return (
    <article class="detail" aria-label="Tasks summary">
      <OpsSummary>
        {list.pending && !list.total ? <Skeleton rows={3} /> : (
          <>
            {activeStats.length > 0 && <StatStrip label="Tasks" stats={activeStats} />}
            {nonZeroGroups.length > 0 && rowsGroup !== 'none' ? (
              <DetailSection title={`By ${groupTitle.toLowerCase()}`}>
                {nonZeroGroups.map(g => (
                  <KvRow
                    key={g.key}
                    icon={rowsGroup === 'priority' ? 'list-ordered' : rowsGroup === 'agent' ? 'bot' : rowsGroup === 'host' ? 'panels-top-left' : 'folder-open'}
                    label={groupLabel(rowsGroup, g.label) || g.key}
                    value={<Num m={{ value: g.count, basis: 'measured' }} kind="count" />}
                  />
                ))}
              </DetailSection>
            ) : hasCounts ? (
              <DetailSection title="By state">
                <StateCounters
                  family="task"
                  label="Tasks by state"
                  counts={counts}
                  active={(view.state ?? []) as StateOf<'task'>[]}
                  hideZero
                  onAll={() => set({ state: [] })}
                  onToggle={s => {
                    const cur = (view.state ?? []) as StateOf<'task'>[];
                    set({ state: cur.includes(s) ? cur.filter(x => x !== s) : [...cur, s] });
                  }}
                />
              </DetailSection>
            ) : (
              <Empty text={filtered ? 'No tasks match these filters.' : focused ? 'No tasks in the focused projects.' : 'No tasks yet.'} />
            )}
          </>
        )}
      </OpsSummary>
    </article>
  );
}

const TERMINAL = ['completed', 'failed', 'cancelled'];
/** Row facts of a task (OR-37): open questions, delivery stage, time since taken into work, executions from two, time in state, agent while running. */
function TaskFacts({ t }: { t: TaskRow }) {
  const taken = ms(t.startedAt), since = ms(t.stateAt), agent = t.lastAgent ?? t.agent, n = t.executions ?? 0, q = t.openQuestions ?? 0;
  const label = states_.task[t.pauseReason === 'held' ? 'held' : t.state].label;
  return <RowFacts>
    {q > 0 && <span class="chip-age t-attention alert" data-tip={q === 1 ? '1 open question' : `${q} open questions`} aria-label={q === 1 ? '1 open question' : `${q} open questions`}>{q}</span>}
    {isStage(t.stage) && <StageChip stage={t.stage} />}
    {taken !== null && !TERMINAL.includes(t.state) && <Fact icon="clock" tip="Since taken into work"><AgeChip t={taken} alertAfter={t.state === 'in_progress' ? 2 * 3600 : undefined} alertLabel="running too long" /></Fact>}
    {/* One run is the usual case and says nothing (OR-17): the count shows from two; the host is in the detail. */}
    {n > 1 && <Fact icon={actions.runs} text={String(n)} tip={`${n} executions`} />}
    {since !== null && <Fact tip={`${label} for`}><AgeChip t={since} /></Fact>}
    {t.state === 'in_progress' && isHarness(agent) && <HarnessMark h={agent} />}
  </RowFacts>;
}

/** Priority class chip (owner decision): urgent red, background grey, normal none (OR-17). */
function PriorityChip({ p }: { p: TaskRow['priority'] }) {
  if (p === 'normal') return null;
  return <span class={`chip-age${p === 'urgent' ? ' t-danger alert' : ''}`} aria-label={p === 'urgent' ? 'urgent priority' : 'background priority'}>{p === 'urgent' ? 'urgent' : 'background'}</span>;
}

function groupLabel(group: string, label: string | null) {
  if (group === 'priority') return label === 'urgent' ? 'Urgent' : label === 'background' ? 'Background' : 'Normal';
  if (group === 'agent') return label && label in harnesses ? harnesses[label as Harness].label : 'No agent';
  if (group === 'state') return label ?? '';
  return label ?? 'None';
}

/** Dependencies as task references with their cards (OR-38); ones the viewer cannot see are only counted. */
/** task_get latestResult: an artifact record (open in the contract); its id, name, path and media type. */
function latestResult(v: unknown): { id: string; artifactId?: string; name?: string; path?: string; mediaType?: string } | null {
  if (!v || typeof v !== 'object') return null;
  const r = v as Record<string, unknown>, s = (k: string) => typeof r[k] === 'string' ? r[k] as string : undefined;
  const id = s('id'); return id ? { id, artifactId: s('artifactId'), name: s('name'), path: s('path'), mediaType: s('mediaType') } : null;
}
function dependsOn(ds: { id: string; code?: string | null }[]) {
  const shown = ds.filter(d => d.code), hidden = ds.length - shown.length;
  return <span class="deps">Depends on {shown.map(d => <EntityCode key={d.id} code={d.code} id={d.id} kind="task" cls="inline" />)}
    {hidden > 0 && <span>{hidden} other task{hidden > 1 ? 's' : ''}</span>}</span>;
}

function TaskDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const [, go] = useLocation();
  const { data: t, error } = useOp('task_get', { taskId: id });
  const board = useSwarmBoard('task', id);
  const hasSwarm = hasSwarmBoard(board.data);
  const [tab, setTab] = useState<'details' | 'swarm'>('details');
  useEffect(() => { setTab('details'); }, [id]);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const alive = useAlive();
  const [confirming, setConfirming] = useState(false);
  const pins = usePins('tasks');
  const now = Date.now(), off = !online.value;
  const act = async (action: string) => { setBusy(action); setFailure(null); try { await op('task_update', { taskId: id, action }); } catch (e) { setFailure(e instanceof Error ? e.message : String(e)); } finally { setBusy(null); setConfirming(false); } };
  if (error && !t) return <article class="detail" data-ui="detail" tabIndex={-1}><ErrorBanner text={`Task did not load: ${error}`} /></article>;
  if (!t) return <article class="detail" data-ui="detail" tabIndex={-1}><Skeleton rows={6} /></article>;
  const activeTab = hasSwarm ? tab : 'details';
  const swarmCount = board.data ? (board.data.summary.messagesTotal || board.data.roster.length || undefined) : undefined;
  const result = latestResult(t.latestResult);
  const req = t.completionRequirements && typeof t.completionRequirements === 'object'
    ? Object.entries(t.completionRequirements).map(([key, value]) => ({
      text: `${key}: ${typeof value === 'string' ? value : JSON.stringify(value)}`,
    }))
    : [];
  const open = t.questions.filter(q => q.state === 'open');
  const rollup = t.resources;
  const nodes: TreeNode[] = buildExecTree(t.executions);
  const disabled = off ? 'offline' : undefined;
  const latestExecution = t.executions.at(-1); // task_get preserves execution sequence.
  const reported = t.executions.flatMap(e => e.report ? [e.report] : []).sort((a, b) => b.at - a.at)[0];
  const decided = t.questions.filter(q => q.state === 'answered').sort((a, b) => (ms(b.answeredAt) ?? 0) - (ms(a.answeredAt) ?? 0)).slice(0, 3);
  const detailsBody = (
    <>
      {open.map(q => <DecisionBlock key={q.id} question={q.text} askedBy={actor(q.askedBy) || q.kind} addressedTo="you" since={ms(q.askedAt) ?? now} now={now}
        options={(Array.isArray(q.options) ? q.options as unknown[] : q.kind === 'acceptance' ? ['accepted', 'rejected'] : keyConnection(q.options) ? ['decline'] : []).map((o, i) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
        onAnswer={a => { if (!off) writeOnce('questions_answer', { questionId: q.id, answer: a }, () => setFailure('unknown, retrying'), { signal: alive }).then(() => setFailure(null), e => setFailure(e.message)); }}
        onSecret={keyConnection(q.options) ? v => addRequestedKey(q.id, keyConnection(q.options)!, v, alive) : undefined} />)}
      {latestExecution?.problem && <DetailSection title="Latest execution problem">
        <ErrorBanner text={latestExecution.problem} />
      </DetailSection>}
      {reported && <DetailSection title="Latest available worker report">
        <AgeChip t={reported.at} />
        <StateBadge family="execution" state={reported.outcome === 'succeeded' ? 'succeeded' : 'failed'} />
        {reported.summary && <Markdown text={reported.summary} />}
      </DetailSection>}
      {decided.length > 0 && <DetailSection title="Previous answers">
      {decided.map(q => <p key={q.id} class="state-line"><Icon name={q.answer === 'accepted' ? 'circle-check' : q.answer === 'rejected' ? 'circle-x' : 'message-circle-question'} />
        <span>{q.kind === 'acceptance' ? q.answer : `${q.text}: ${q.answer}`}</span>{q.answeredBy && <span class="muted">by {actor(q.answeredBy)}</span>}{ms(q.answeredAt) && <AgeChip t={ms(q.answeredAt)!} />}</p>)}
      </DetailSection>}
      {(t.expectedOutcome || req.length > 0 || t.dependencies.length > 0) && <OutcomeBlock outcome={t.expectedOutcome ?? ''} requirements={req} inputs={[]} depends={t.dependencies.length ? dependsOn(t.dependencies) : undefined} />}
      <ScopedRollupBar rollup={rollup} cost={t.cost} />
      {nodes.length > 0 && <DetailSection title="Executions"><ExecTree nodes={nodes} now={now} label="Executions" /></DetailSection>}
    </>
  );
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={t.title}>
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
      {t.projectId ? <EntityCode code={t.projectId} id={t.projectId} kind="project" /> : <span class="muted">No project</span>}<span class="grow" />
      <Button label={pins.has(id) ? 'Unpin' : 'Pin'} icon={pins.has(id) ? 'pin-off' : 'pin'} iconOnly pressed={pins.has(id)} onClick={() => pins.toggle(id)} />
      {t.state === 'paused'
        ? <Button label={t.pauseReason === 'held' ? 'Release' : 'Continue'} icon="rotate-cw" busy={busy === 'resume'} disabledReason={disabled} onClick={() => act('resume')} />
        : !['completed', 'failed', 'cancelled'].includes(t.state) && <SplitButton main={{ label: 'Pause', icon: 'pause', onClick: () => !off && act('pause') }} alternatives={[{ label: 'Cancel task', icon: 'circle-slash', onClick: () => setConfirming(true) }]} />}
    </header>
    {confirming && <ProblemRow severity="warning" title="Cancel this task?" facts="Running work is asked to stop; finished external effects stay."
      actions={[{ label: 'Yes, cancel', primary: true, onClick: () => { if (!off && !busy) void act('cancel'); } }, { label: 'Keep', onClick: () => setConfirming(false) }]} />}
    {failure && <ErrorBanner text={failure} />}
    <h2 class="detail-title">{t.title}<EntityCode code={t.code} id={t.id} kind="task" /></h2>
    <div class="state-line"><StateBadge family="task" state={t.pauseReason === 'held' ? 'held' : t.state} reason={t.waitReason ?? undefined} />{t.createdBy && <span class="muted">by {actor(t.createdBy)}</span>}{ms(t.createdAt) && <AgeChip t={ms(t.createdAt)!} />}</div>
    {result && <p class="state-line"><Icon name={viewOf(result.mediaType ?? '') === 'download' ? 'archive' : 'file-text'} label={result.mediaType ?? 'Result'} />
      <a class="row-link" href={`/results/${encodeURIComponent(result.id)}`} onClick={ev => { ev.preventDefault(); go(`/results/${encodeURIComponent(result.id)}`); }}>{fileLabel(result.path ?? result.name ?? '') || 'Result'}</a>
      <EntityCode id={result.artifactId ?? result.id} kind="result" /></p>}
    {hasSwarm && (
      <TabBar
        cls="detail-tabs"
        label="Task views"
        tabs={[{ id: 'details', label: 'Details' }, { id: 'swarm', label: 'Swarm', count: swarmCount }]}
        value={activeTab}
        onChange={setTab}
      />
    )}
    {hasSwarm ? (
      <div role="tabpanel" aria-label={activeTab === 'swarm' ? 'Swarm' : 'Details'} class="stack">
        {activeTab === 'swarm' ? <SwarmPanel scopeKind="task" scopeId={id} board={board} /> : detailsBody}
      </div>
    ) : detailsBody}
  </article>;
}
