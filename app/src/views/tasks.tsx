// Tasks list and detail (docs/design-system/pages/tasks.md). Server-ordered list with views, state counters, grouping and
// search (list-view pattern, list-page.tsx); the detail shows outcome, totals, open decisions and the execution tree.
import { useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  ListDetail, RowFacts, Fact, ProblemRow, StateBadge, HarnessMark, AgeChip, Button,
  SplitButton, Totals, Icon, DetailSection, Markdown, actions, OutcomeBlock, DecisionBlock, ExecTree, Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses,
  type TreeNode, type Harness, type StateOf, states as states_,
  keyConnection, EntityCode, actor, fileLabel, viewOf,
} from '../../../packages/ui/src/index.ts';
import { op, online, addRequestedKey, writeOnce, useAlive, type Ops } from '../api.ts';
import { useDoc } from '../store.ts';
import { DEFAULT_TASK_SORT, DEFAULT_TASK_GROUP, type View } from '../../../packages/view-url/view-url.ts';
import { usePins, withFocus, setFocus } from '../prefs.ts';
import { useListPage, ListPagePanel } from '../list-page.tsx';
import { ScopedRollupDetails, buildExecTree, resourceTotals } from './resources.tsx';

const GROUPS = [['priority', 'Priority'], ['state', 'State'], ['project', 'Project'], ['agent', 'Agent'], ['host', 'Host'], ['none', 'None']] as const;
const SORTS = [['changed', 'Changed'], ['created', 'Created']] as const;
type TaskRow = { id: string; code?: string | null; title: string; state: StateOf<'task'>; pauseReason?: string | null; waitReason: string | null; priority: 'urgent' | 'normal' | 'background'; agent: string | null; host: string | null; startedAt: string | null; createdAt: string; projectId: string | null; openQuestions: number;
  stateAt?: string; executions?: number; lastAgent?: string | null; sortKey: (string | number)[] };
const ms = (v: string | number | null | undefined) => (v === null || v === undefined ? null : typeof v === 'number' ? v : Date.parse(v));
const isHarness = (h: string | null): h is Harness => !!h && h in harnesses;

// System presets (P5): fixed, shown before the person's saved views. Running: every non-terminal state.
// A system preset sets the state filter only; grouping, sort, projects and search stay as the person set them.
const SYSTEM_VIEWS = [{ name: 'Needs me', view: { view: 'tasks' as const, state: ['failed', 'waiting'] } }, { name: 'Running', view: { view: 'tasks' as const, state: ['queued', 'in_progress', 'waiting', 'held', 'paused'] } }];
// Saved views from before the project default kept no group: they were grouped by priority.
const savedView = (v: View): View => v.group ? v : { ...v, group: 'priority' };

export function Tasks({ id }: { id?: string }) {
  // Pinned tasks are read by id with an empty filter and live through task changes.
  const page = useListPage<TaskRow>({
    view: 'tasks', kind: 'tasks', defaultGroup: DEFAULT_TASK_GROUP, defaultSort: DEFAULT_TASK_SORT, system: SYSTEM_VIEWS, groupLabel,
    pinDoc: 'task-pins', pinRead: () => ({ key: [], filter: {} }), pinTouches: (c, has) => c.kind === 'task' && has(c.id) || !!c.taskId && has(c.taskId),
    savedView, saveGroup: DEFAULT_TASK_GROUP,
  });
  const { view, set, list, chosen, off, rowsGroup, confirm } = page;
  const canPause = (t: TaskRow) => !TERMINAL.includes(t.state) && t.state !== 'paused', canResume = (t: TaskRow) => t.state === 'paused';
  const bulk = (action: 'pause' | 'resume' | 'cancel', ts: TaskRow[]) => page.bulk(ts, t => op('task_update', { taskId: t.id, action }), `did not ${action}`);
  const now = Date.now();
  const { filtered } = page, focused = !view.project?.length && !!withFocus(view).project?.length;
  const listPane = <>
    <header class="page-h"><h1>Tasks</h1><OfflineMarker state={list.state} since={list.at || undefined} now={now} /></header>
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
      facts={t => <TaskFacts t={t} now={now} />} />
  </>;
  return <ListDetail list={listPane} detail={id ? <TaskDetail id={id} onClose={() => set({ focus: undefined })} /> : undefined} detailKey={id} />;
}

const TERMINAL = ['completed', 'failed', 'cancelled'];
/** Row facts of a task (OR-37): time since taken into work, executions, time in the current state, agent, host. */
function TaskFacts({ t, now }: { t: TaskRow; now: number }) {
  const taken = ms(t.startedAt), since = ms(t.stateAt), agent = t.lastAgent ?? t.agent, n = t.executions ?? 0;
  const label = states_.task[t.pauseReason === 'held' ? 'held' : t.state].label;
  return <RowFacts>
    {taken !== null && !TERMINAL.includes(t.state) && <Fact icon="clock" tip="Since taken into work"><AgeChip t={taken} now={now} alertAfter={t.state === 'in_progress' ? 2 * 3600 : undefined} alertLabel="running too long" /></Fact>}
    {n > 0 && <Fact icon={actions.runs} text={String(n)} tip={n === 1 ? '1 execution' : `${n} executions`} />}
    {since !== null && <Fact tip={`${label} for`}><AgeChip t={since} now={now} /></Fact>}
    {isHarness(agent) && <HarnessMark h={agent} />}
    {t.host && <Fact icon="panels-top-left" tip={`Host ${t.host}`} />}
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

/** Dependencies by code (OR-38); ones the viewer cannot see are only counted. */
function dependsOn(ds: { code?: string | null }[]) {
  const codes = ds.flatMap(d => d.code ? [d.code] : []), hidden = ds.length - codes.length;
  return `Depends on ${[...codes, ...(hidden ? [`${hidden} other task${hidden > 1 ? 's' : ''}`] : [])].join(', ')}`;
}

function TaskDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const [, go] = useLocation();
  const { data: t, error, state } = useDoc<any>(`task:${id}`, () => op('task_get', { taskId: id }), c => c.taskId === id || c.id === id);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const alive = useAlive();
  const [confirming, setConfirming] = useState(false);
  const pins = usePins('tasks');
  const now = Date.now(), off = !online.value;
  const act = async (action: string) => { setBusy(action); setFailure(null); try { await op('task_update', { taskId: id, action }); } catch (e: any) { setFailure(e.message); } finally { setBusy(null); setConfirming(false); } };
  if (error && !t) return <article class="detail" data-ui="detail" tabIndex={-1}><ErrorBanner text={`Task did not load: ${error}`} /></article>;
  if (!t) return <article class="detail" data-ui="detail" tabIndex={-1}><Skeleton rows={6} /></article>;
  const req = t.completionRequirements && typeof t.completionRequirements === 'object' ? Object.entries(t.completionRequirements).map(([k, v]) => ({ text: typeof v === 'string' ? `${k}: ${v}` : k, met: false })) : [];
  const open = (t.questions ?? []).filter((q: any) => q.state === 'open');
  const rollup = (t as Ops['task_get']['reply']).resources;
  const nodes: TreeNode[] = buildExecTree(t.executions ?? []);
  const disabled = off ? 'offline' : undefined;
  // OR-39: statement, lifecycle, result, decision; a total is shown only when it is known (no empty token or cost fields).
  // The execution count is the tree below (OR-17: no second 'Executions').
  const totals = resourceTotals(rollup?.total, { extraCost: t.cost, hideUnavailable: true });
  const latestExecution = t.executions?.at(-1); // task_get preserves execution sequence.
  const reported = (t.executions ?? []).filter((e: any) => e.report).sort((a: any, b: any) => b.report.at - a.report.at)[0];
  const decided = (t.questions ?? []).filter((q: any) => q.state === 'answered').sort((a: any, b: any) => (ms(b.answeredAt) ?? 0) - (ms(a.answeredAt) ?? 0)).slice(0, 3);
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={t.title}>
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
      <span class="muted mono">{t.projectId ?? 'no project'}</span><span class="grow" />
      <OfflineMarker state={state} now={now} />
      <Button label={pins.has(id) ? 'Unpin' : 'Pin'} icon={pins.has(id) ? 'pin-off' : 'pin'} iconOnly pressed={pins.has(id)} onClick={() => pins.toggle(id)} />
      {t.state === 'paused'
        ? <Button label={t.pauseReason === 'held' ? 'Release' : 'Continue'} icon="rotate-cw" busy={busy === 'resume'} disabledReason={disabled} onClick={() => act('resume')} />
        : !['completed', 'failed', 'cancelled'].includes(t.state) && <SplitButton main={{ label: 'Pause', icon: 'pause', onClick: () => !off && act('pause') }} alternatives={[{ label: 'Cancel task', icon: 'circle-slash', onClick: () => setConfirming(true) }]} />}
    </header>
    {confirming && <div class="problem warning" role="group" aria-label="Confirm cancel"><b>Cancel this task?</b><span class="facts">Running work is asked to stop; finished external effects stay.</span>
      <span class="acts"><Button label="Yes, cancel" kind="danger" busy={busy === 'cancel'} disabledReason={off ? 'offline' : undefined} onClick={() => act('cancel')} /><Button label="Keep" onClick={() => setConfirming(false)} /></span></div>}
    {failure && <ErrorBanner text={failure} />}
    <h2 class="detail-title">{t.title}<EntityCode code={t.code} id={t.id} /></h2>
    <div class="state-line"><StateBadge family="task" state={t.pauseReason === 'held' ? 'held' : t.state} reason={t.waitReason ?? undefined} />{t.createdBy && <span class="muted">by {actor(t.createdBy)}</span>}{ms(t.createdAt) && <AgeChip t={ms(t.createdAt)!} now={now} />}</div>
    {t.latestResult?.id && <p class="state-line"><Icon name={viewOf(t.latestResult.mediaType ?? '') === 'download' ? 'archive' : 'file-text'} label={t.latestResult.mediaType ?? 'Result'} />
      <a class="row-link" href={`/results/${encodeURIComponent(t.latestResult.id)}`} onClick={ev => { ev.preventDefault(); go(`/results/${encodeURIComponent(t.latestResult.id)}`); }}>{fileLabel(t.latestResult.path ?? t.latestResult.name ?? '') || 'Result'}</a>
      <EntityCode id={t.latestResult.sha256 ?? t.latestResult.id} what={t.latestResult.sha256 ? 'sha256' : 'Result'} /></p>}
    {open.map((q: any) => <DecisionBlock key={q.id} question={q.text} askedBy={actor(q.askedBy) || q.kind} addressedTo="you" since={ms(q.askedAt) ?? now} now={now}
      options={(Array.isArray(q.options) ? q.options : q.kind === 'acceptance' ? ['accepted', 'rejected'] : keyConnection(q.options) ? ['decline'] : []).map((o: string, i: number) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
      onAnswer={a => { if (!off) writeOnce('questions_answer', { questionId: q.id, answer: a }, () => setFailure('unknown, retrying'), { signal: alive }).then(() => setFailure(null), e => setFailure(e.message)); }}
      onSecret={keyConnection(q.options) ? v => addRequestedKey(q.id, keyConnection(q.options)!, v, alive) : undefined} />)}
    {latestExecution?.problem && <DetailSection title="Latest execution problem">
      <ErrorBanner text={latestExecution.problem} />
    </DetailSection>}
    {reported && <DetailSection title="Latest available worker report">
      <AgeChip t={reported.report.at} now={now} />
      <StateBadge family="execution" state={reported.report.outcome === 'succeeded' ? 'succeeded' : 'failed'} />
      {reported.report.summary ? <Markdown text={reported.report.summary} /> : <p class="muted">No report summary provided.</p>}
    </DetailSection>}
    {t.expectedOutcome && <OutcomeBlock outcome={t.expectedOutcome} requirements={req} inputs={[]} depends={t.dependencies?.length ? dependsOn(t.dependencies) : undefined} />}
    {totals.length > 0 && <Totals items={totals} />}
    <ScopedRollupDetails rollup={rollup} cost={t.cost} title="Resource breakdown" />
    {decided.length > 0 && <DetailSection title="Previous answers">
    {decided.map((q: any) => <p key={q.id} class="state-line"><Icon name={q.answer === 'accepted' ? 'circle-check' : q.answer === 'rejected' ? 'circle-x' : 'message-circle-question'} />
      <span>{q.kind === 'acceptance' ? q.answer : `${q.text}: ${q.answer}`}</span>{q.answeredBy && <span class="muted">by {actor(q.answeredBy)}</span>}{ms(q.answeredAt) && <AgeChip t={ms(q.answeredAt)!} now={now} />}</p>)}
    </DetailSection>}
    <h3 class="sub-h">Executions</h3>
    {nodes.length ? <ExecTree nodes={nodes} now={now} label="Executions" /> : <Empty text="No executions yet." />}
  </article>;
}
