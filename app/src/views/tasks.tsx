// Tasks list and detail (docs/design-system/pages/tasks.md). Server-ordered list with views, state counters, grouping and
// search (list-view pattern, list-page.tsx); the detail shows outcome, totals, open decisions and the execution tree.
import { For, Match, Show, Switch, createEffect, createMemo, createSignal } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { useLocation } from '../router.ts';
import {
  ListDetail, LiveRow, RowFacts, Fact, ProblemRow, StateBadge, HarnessMark, StageChip, AgeChip, Button, WorkFacts, peerFence,
  SplitButton, Icon, DetailSection, Markdown, actions, OutcomeBlock, DecisionBlock, ExecTree, Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses,
  OpsSummary, StatStrip, StateCounters, KvRow, Num, TabBar, useTick,
  type TreeNode, type Harness, type StateOf, type Stat, states as states_, isFinished, openStates,
  type Tone, life, keyConnection, answerChoices, AnswerNote, EntityCode, actor, fileLabel, viewOf, ShellStatus, causeOf,
} from '../../../packages/ui/src/index.ts';
import { op, online } from '../api.ts';
import { useAnswers } from '../answers.ts';
import { useOp } from '../store.ts';
import type { TaskRow as ContractTaskRow } from '../../../contracts/generated/ops.ts';
import { HistoryFeed } from './history.tsx';
import { DEFAULT_TASK_SORT, DEFAULT_TASK_GROUP, type View } from '../../../packages/view-url/view-url.ts';
import { usePins, prefs, setFocus } from '../prefs.ts';
import { useListPage, ListPagePanel, type ListPage, type Frame } from '../list-page.tsx';
import { useItemId } from '../codes.ts';
import { SYSTEM_VIEWS } from '../system-views.ts';
import { describeTaskFacts, isUnconfirmedActiveTask, parseMs as ms, TERMINAL_TASK_STATES as TERMINAL } from '../task-facts.ts';
import { RowSpend, useRowSpend, ScopedRollupBar, buildExecTree } from './resources.tsx';
import { SwarmPanel, hasSwarmBoard, useSwarmBoard } from './swarm-panel.tsx';
import { TaskQuestionContext, typedAnswer } from './task-question-context.tsx';
import { CHIP_STAGE } from './delivery-model.tsx';

const GROUPS = [['priority', 'Priority'], ['state', 'State'], ['project', 'Project'], ['agent', 'Agent'], ['host', 'Host'], ['none', 'None']] as const;
const SORTS = [['changed', 'Changed'], ['created', 'Created']] as const;
/** The contract's task row; its state is a list state (held included). */
type TaskRow = ContractTaskRow & { state: StateOf<'task'> };
const SYSTEM_PRESET_TIPS = {
  Running: 'All unresolved tasks: queued, in progress, waiting, held, paused',
  'Needs me': 'Tasks waiting on you or failed',
};

// System presets (P5): system-views.ts. Running: every non-terminal state.
// Saved views from before the project default kept no group: they were grouped by priority.
const savedView = (v: View): View => (v.group ? v : { ...v, group: 'priority' });

/**
 * The task list and an opened task. `id` is the URL item, a task code (T-FLT-42) or an id. In a project's Tasks facet
 * (`project`, docs/design-system/pages/projects.md) the list stays on that project and `frame` lays it out in one column.
 */
export function Tasks(props: { id?: string; project?: string; frame?: Frame }) {
  const taskId = useItemId(() => props.id);
  // Pinned tasks are read by id with an empty filter and live through task changes.
  const page = useListPage<TaskRow>({
    view: 'tasks', entity: 'task', kind: 'tasks', defaultGroup: DEFAULT_TASK_GROUP, defaultSort: DEFAULT_TASK_SORT, system: SYSTEM_VIEWS.tasks, groupLabel,
    scope: () => props.project, focusOf: r => r.code ?? r.id,
    pinRead: () => ({ filter: {} }),
    savedView, saveGroup: DEFAULT_TASK_GROUP,
    // Completed (7) and cancelled (8) start collapsed under state grouping (the state group keys of task_page, attention
    // order). A filter to those states alone is a context of its own, where they start open.
    closedGroups: { state: ['7', '8'] }, groupsContext: v => v.state?.length && v.state.every(s => s === 'completed' || s === 'cancelled') ? 'done' : undefined,
  });
  const { set } = page;
  const spend = useRowSpend('tasks', () => page.displayed);
  const canPause = (t: TaskRow) => !TERMINAL.includes(t.state as (typeof TERMINAL)[number]) && t.state !== 'paused';
  const canResume = (t: TaskRow) => t.state === 'paused';
  const bulk = (action: 'pause' | 'resume' | 'cancel', ts: TaskRow[]) => page.bulk(ts, t => op('task_update', { taskId: t.id, action }), `did not ${action}`);
  const now = useTick(30_000);
  // Peers for the work-facts outliers (OR-63): the open tasks on this page, never a fixed limit.
  const fences = createMemo(() => {
    const open = (page.rows as TaskRow[]).filter(t => !TERMINAL.includes(t.state as (typeof TERMINAL)[number]));
    return {
      attempts: peerFence(open.map(t => t.executions)),
      work: peerFence(open.map(t => (now() - (ms(t.startedAt) ?? NaN)) / 1000).filter(Number.isFinite)),
    };
  });
  // The empty text names the focus only when a focus is set: ignored projects alone are not a focus "Show all projects" could clear.
  const focused = createMemo(() => !page.view.project?.length && prefs.value.focus.length > 0);
  const listPane = (
    <>
      <ShellStatus><OfflineMarker state={page.list.state} since={page.list.at || undefined} now={now()} /></ShellStatus>
      <ListPagePanel
        page={page}
        noun="Tasks"
        placeholder="Search tasks, agent:claude host:mac"
        family="task"
        groups={GROUPS}
        sorts={SORTS}
        focused={taskId() ?? props.id}
        systemTips={SYSTEM_PRESET_TIPS}
        actions={() => [
          { label: 'Pause', icon: 'pause', onClick: () => bulk('pause', page.chosen.filter(canPause)), disabledReason: page.off ?? (page.chosen.some(canPause) ? undefined : 'None can pause') },
          { label: 'Continue', icon: 'rotate-cw', onClick: () => bulk('resume', page.chosen.filter(canResume)), disabledReason: page.off ?? (page.chosen.some(canResume) ? undefined : 'None paused') },
          { label: 'Cancel', icon: 'circle-slash', danger: true, onClick: () => page.setConfirm(page.chosen.filter(t => !TERMINAL.includes(t.state as (typeof TERMINAL)[number]))), disabledReason: page.off ?? (page.chosen.some(t => !TERMINAL.includes(t.state as (typeof TERMINAL)[number])) ? undefined : 'None can cancel') },
        ]}
        menu={t => [
          ...(canResume(t) ? [{ label: t.pauseReason === 'held' ? 'Release' : 'Continue', icon: 'rotate-cw' as const, disabledReason: page.off, onClick: () => bulk('resume', [t]) }] : []),
          ...(canPause(t) ? [{ label: 'Pause', icon: 'pause' as const, disabledReason: page.off, onClick: () => bulk('pause', [t]) }] : []),
        ]}
        confirm={page.confirm ? (
          <ProblemRow
            severity="warning"
            title={`Cancel ${page.confirm.length} ${page.confirm.length === 1 ? 'task' : 'tasks'}?`}
            facts="Running work is asked to stop; finished external effects stay."
            actions={[
              { label: 'Yes, cancel', primary: true, onClick: () => { const ts = page.confirm!; page.setConfirm(null); bulk('cancel', ts); } },
              { label: 'Keep', onClick: () => page.setConfirm(null) },
            ]}
          />
        ) : undefined}
        empty={
          <Empty
            text={page.filtered ? 'No tasks match these filters.' : focused() ? 'No tasks in the focused projects.' : 'No tasks yet.'}
            action={page.filtered ? <Button label="Clear filters" onClick={page.clearFilters} /> : focused() ? <Button label="Show all projects" onClick={() => setFocus([])} /> : undefined}
          />
        }
        listLabel="Tasks"
        title={t => t.title}
        groupIcon={g => (g === 'priority' ? 'list-ordered' : 'folder-open')}
        state={t => {
          const unconfirmed = isUnconfirmedActiveTask(t);
          return (
            <StateBadge
              family="task"
              state={t.pauseReason === 'held' ? 'held' : t.state}
              reason={t.waitReason ?? undefined}
              unconfirmed={unconfirmed}
              lastSeen={unconfirmed ? (ms(t.activeLastSeenAt) ?? undefined) : undefined}
              compact
            />
          );
        }}
        before={t => (page.rowsGroup !== 'priority' ? <PriorityChip p={t.priority} /> : undefined)}
        facts={t => (
          <TaskFacts
            t={t}
            now={now()}
            fences={fences()}
            spend={
              <RowSpend
                cost={spend.cost.get(t.id)}
                stale={!!spend.error}
                name={t.code ?? t.title}
                href={page.href({ focus: t.code ?? t.id })}
                open={extend => (page.selecting || extend ? page.select(t) : set({ focus: t.code ?? t.id }))}
              />
            }
          />
        )}
      />
    </>
  );
  const Layout = props.frame ?? ListDetail;
  return (
    <Layout
      list={listPane}
      detail={props.id ? <Show when={taskId()} keyed fallback={<Skeleton rows={6} />}>{id => <TaskDetail id={id} onClose={() => set({ focus: undefined })} />}</Show> : undefined}
      idle={<TasksSummary page={page} filtered={page.filtered} focused={focused()} />}
      detailKey={props.id}
    />
  );
}

function TasksSummary(props: { page: ListPage<TaskRow>; filtered: boolean; focused: boolean }) {
  const counts = () => props.page.list.counts as Partial<Record<StateOf<'task'>, number>>;
  const activeStats = createMemo<Stat[]>(() => {
    const c = counts();
    return ([
      { label: 'total', icon: 'list', value: { value: props.page.list.total, basis: 'measured' } },
      { label: 'waiting', icon: states_.task.waiting.icon, value: { value: c.waiting ?? 0, basis: 'measured' }, tone: (c.waiting ?? 0) > 0 ? 'attention' : undefined },
      { label: 'failed', icon: states_.task.failed.icon, value: { value: c.failed ?? 0, basis: 'measured' }, tone: (c.failed ?? 0) > 0 ? 'danger' : undefined },
      { label: 'in progress', icon: states_.task.in_progress.icon, value: { value: c.in_progress ?? 0, basis: 'measured' } },
      { label: 'queued', icon: states_.task.queued.icon, value: { value: c.queued ?? 0, basis: 'measured' } },
      { label: 'paused', icon: states_.task.paused.icon, value: { value: c.paused ?? 0, basis: 'measured' } },
    ] satisfies Stat[]).filter(s => (s.value.value ?? 0) > 0);
  });
  const nonZeroGroups = createMemo(() => props.page.list.groups.filter(g => g.count > 0));
  const groupTitle = createMemo(() => GROUPS.find(g => g[0] === props.page.rowsGroup)?.[1] ?? props.page.rowsGroup);
  const hasCounts = createMemo(() => Object.values(counts()).some(n => (n ?? 0) > 0));

  return (
    <article class="detail" aria-label="Tasks summary">
      <OpsSummary>
        <Show when={props.page.activePreset === 'Running'}>
          <p class="muted">Unresolved tasks: queued, in progress, waiting, held, and paused.</p>
        </Show>
        <Show when={!(props.page.list.pending && !props.page.list.total)} fallback={<Skeleton rows={3} />}>
          <Show when={activeStats().length > 0}>
            <StatStrip label="Tasks" stats={activeStats()} />
          </Show>
          <Switch fallback={<Empty text={props.filtered ? 'No tasks match these filters.' : props.focused ? 'No tasks in the focused projects.' : 'No tasks yet.'} />}>
            <Match when={nonZeroGroups().length > 0 && props.page.rowsGroup !== 'none'}>
              <DetailSection title={`By ${groupTitle().toLowerCase()}`}>
                <For each={nonZeroGroups()}>
                  {g => (
                    <KvRow
                      icon={props.page.rowsGroup === 'priority' ? 'list-ordered' : props.page.rowsGroup === 'agent' ? 'bot' : props.page.rowsGroup === 'host' ? 'panels-top-left' : 'folder-open'}
                      label={groupLabel(props.page.rowsGroup, g.label) || g.key}
                      value={<Num m={{ value: g.count, basis: 'measured' }} kind="count" />}
                    />
                  )}
                </For>
              </DetailSection>
            </Match>
            <Match when={hasCounts()}>
              <DetailSection title="By state">
                <StateCounters
                  family="task"
                  label="Tasks by state"
                  counts={counts()}
                  active={(props.page.view.state ?? []) as StateOf<'task'>[]}
                  hideZero
                  onAll={() => props.page.set({ state: [] })}
                  onToggle={s => {
                    const cur = (props.page.view.state ?? []) as StateOf<'task'>[];
                    props.page.set({ state: cur.includes(s) ? cur.filter(x => x !== s) : [...cur, s] });
                  }}
                />
              </DetailSection>
            </Match>
          </Switch>
        </Show>
      </OpsSummary>
    </article>
  );
}

/**
 * Row facts of a task (OR-37, OR-63): open questions, delivery stage, run evidence that needs a look (stale, stalled,
 * unconfirmed), cumulative spend, then the shared work facts last (controls/work-facts.md: attempts, agent, time in work, the
 * time always the rightmost fact; the latest attempt and the time in state in its tooltip). A finished task shows how long ago it reached its state instead
 * of a time in work. Outliers stand out against the page's open tasks (peerFence), never against a fixed limit.
 */
function TaskFacts(props: { t: TaskRow; now: number; spend: JSX.Element; fences: { attempts?: number; work?: number } }) {
  const f = createMemo(() => describeTaskFacts(props.t, props.now));
  const label = createMemo(() => states_.task[props.t.pauseReason === 'held' ? 'held' : props.t.state].label);
  return (
    <RowFacts>
      <Show when={f().openQuestions > 0}>
        <span
          class="chip-age t-attention alert"
          data-tip={f().openQuestions === 1 ? '1 open question' : `${f().openQuestions} open questions`}
          aria-label={f().openQuestions === 1 ? '1 open question' : `${f().openQuestions} open questions`}
        >
          {f().openQuestions}
        </span>
      </Show>
      <Show when={props.t.stage}>{s => <StageChip stage={CHIP_STAGE[s()]} />}</Show>
      <Switch>
        <Match when={f().active.kind === 'stale'}>
          <Fact icon="history" tip="Run evidence stale">
            <AgeChip
              t={(f().active as { kind: 'stale'; lastSeenAt: number }).lastSeenAt}
              alertAfter={0}
              tone="attention"
              alertLabel="run evidence stale"
            />
          </Fact>
        </Match>
        <Match when={f().active.kind === 'stalled'}>
          {() => {
            const st = f().active as { kind: 'stalled'; lastSeenAt: number | null };
            return (
              <Show
                when={st.lastSeenAt !== null}
                fallback={<span class="chip-age t-attention alert" data-tip="Run stalled" aria-label="Run stalled"><Icon name="triangle-alert" />stalled</span>}
              >
                <Fact icon="circle-pause" tip="Run stalled"><AgeChip t={st.lastSeenAt!} alertAfter={0} tone="attention" alertLabel="run stalled" /></Fact>
              </Show>
            );
          }}
        </Match>
        <Match when={f().active.kind === 'unconfirmed'}>
          {() => {
            const un = f().active as { kind: 'unconfirmed'; lastSeenAt: number | null };
            return (
              <Show
                when={un.lastSeenAt !== null}
                fallback={<span class="chip-age t-attention alert" data-tip="Run unconfirmed" aria-label="Run unconfirmed"><Icon name="circle-question-mark" />unconfirmed</span>}
              >
                <Fact icon="circle-question-mark" tip="Run unconfirmed"><AgeChip t={un.lastSeenAt!} alertAfter={0} tone="attention" alertLabel="run unconfirmed" /></Fact>
              </Show>
            );
          }}
        </Match>
      </Switch>
      {props.spend}
      <WorkFacts
        attempts={f().executions}
        attemptsFence={f().terminal ? undefined : props.fences.attempts}
        workAt={f().terminal ? undefined : f().taskAgeAt ?? undefined}
        workFence={props.fences.work}
        lastAt={f().terminal ? undefined : ms(props.t.lastStartedAt ?? props.t.activeStartedAt) ?? undefined}
        state={f().stateAgeAt !== null ? { label: label(), at: f().stateAgeAt! } : undefined}
        agent={props.t.agent ?? props.t.lastAgent}
      />
    </RowFacts>
  );
}

/** Priority class chip (owner decision): urgent red, background grey, normal none (OR-17). */
function PriorityChip(props: { p: TaskRow['priority'] }) {
  return (
    <Show when={props.p !== 'normal'}>
      <span class={`chip-age${props.p === 'urgent' ? ' t-danger alert' : ''}`} aria-label={props.p === 'urgent' ? 'urgent priority' : 'background priority'}>
        {props.p === 'urgent' ? 'urgent' : 'background'}
      </span>
    </Show>
  );
}

function groupLabel(group: string, label: string | null) {
  if (group === 'priority') return label === 'urgent' ? 'Urgent' : label === 'background' ? 'Background' : 'Normal';
  if (group === 'agent') return label && label in harnesses ? harnesses[label as Harness].label : 'No agent';
  if (group === 'state') return label ?? '';
  return label ?? 'None';
}

/** task_get latestResult: an artifact record (open in the contract); its id, name, path and media type. */
function latestResult(v: unknown): { id: string; artifactId?: string; name?: string; path?: string; mediaType?: string } | null {
  if (!v || typeof v !== 'object') return null;
  const r = v as Record<string, unknown>, s = (k: string) => (typeof r[k] === 'string' ? (r[k] as string) : undefined);
  const id = s('id'); return id ? { id, artifactId: s('artifactId'), name: s('name'), path: s('path'), mediaType: s('mediaType') } : null;
}
function dependsOn(ds: { id: string; code?: string | null }[]) {
  const shown = ds.filter(d => d.code), hidden = ds.length - shown.length;
  return (
    <span class="deps">
      Depends on{' '}
      <For each={shown}>{d => <EntityCode code={d.code} id={d.id} kind="task" cls="inline" />}</For>
      <Show when={hidden > 0}><span>{hidden} other task{hidden > 1 ? 's' : ''}</span></Show>
    </span>
  );
}

function TaskDetail(props: { id: string; onClose: () => void }) {
  const [, go] = useLocation();
  const tDoc = useOp('task_get', () => ({ taskId: props.id }));
  const board = useSwarmBoard('task', () => props.id);
  const hasSwarm = createMemo(() => hasSwarmBoard(board.data));
  const [tab, setTab] = createSignal<'history' | 'details' | 'swarm'>('history', { ownedWrite: true });
  const [busy, setBusy] = createSignal<string | null>(null, { ownedWrite: true });
  const [failure, setFailure] = createSignal<string | null>(null, { ownedWrite: true });
  const answers = useAnswers();
  const [confirming, setConfirming] = createSignal(false, { ownedWrite: true });
  const pins = usePins('tasks');
  createEffect(
    () => props.id,
    () => {
      setTab('history');
      setBusy(null);
      setFailure(null);
      setConfirming(false);
    },
    { defer: true },
  );
  const now = () => Date.now();
  const off = () => !online.value;
  const act = async (action: string) => {
    setBusy(action);
    setFailure(null);
    try { await op('task_update', { taskId: props.id, action }); }
    catch (e) { setFailure(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(null); setConfirming(false); }
  };

  const t = () => tDoc.data;
  const activeTab = () => (tab() === 'swarm' && !hasSwarm() ? 'history' : tab());
  const swarmCount = createMemo(() => (board.data ? (board.data.summary.messagesTotal || board.data.roster.length || undefined) : undefined));
  const result = createMemo(() => latestResult(t()?.latestResult));
  const req = createMemo(() => {
    const cr = t()?.completionRequirements;
    return cr && typeof cr === 'object'
      ? Object.entries(cr).map(([key, value]) => ({
          text: `${key}: ${typeof value === 'string' ? value : JSON.stringify(value)}`,
        }))
      : [];
  });
  const openQs = createMemo(() => (t()?.questions ?? []).filter(q => q.state === 'open'));
  const nodes = createMemo<TreeNode[]>(() => buildExecTree(t()?.executions ?? []));
  const disabled = () => (off() ? 'offline' : undefined);
  const latestExecution = createMemo(() => t()?.executions.findLast(e => e.kind !== 'child_agent')); // task_get preserves execution sequence.
  const unconfirmed = createMemo(() => {
    const task = t(), le = latestExecution();
    return !!task && !!le && le.observation === 'unconfirmed'
      && (openStates('execution') as string[]).includes(le.state)
      && !(TERMINAL as readonly string[]).includes(task.state) && task.state !== 'paused';
  });
  const conversation = () => latestExecution()?.sessionId;
  // The task's pull requests: those from branches of its sessions (items' taskId), each in its current delivery
  // stage, the first (lowest number) first (J4).
  // The project's read (not taskId): a delivery change names the project, so it rereads this one.
  const delivery = useOp('publishing_status', () => (t()?.projectId ? { projectId: t()!.projectId! } : null), { every: 300_000 });
  const prs = createMemo(() => (delivery.data?.items ?? []).filter(it => it.taskId === t()?.id)
    .sort((a, b) => a.number - b.number || a.repo.localeCompare(b.repo)));
  const reported = createMemo(() =>
    (t()?.executions ?? []).flatMap(e => (e.report ? [e.report] : [])).sort((a, b) => b.at - a.at)[0],
  );
  const decided = createMemo(() =>
    (t()?.questions ?? []).filter(q => q.state === 'answered').sort((a, b) => (ms(b.answeredAt) ?? 0) - (ms(a.answeredAt) ?? 0)).slice(0, 3),
  );

  const renderDetailsBody = () => {
    const task = t()!;
    return (
      <>
        <Show when={unconfirmed()}>
          <ProblemRow
            severity="warning"
            title="Execution status is unconfirmed"
            facts="Wait for the host to confirm the outcome. Review the conversation before starting new work."
            actions={conversation() ? [{ label: 'Open conversation', onClick: () => go(`/conversations/${encodeURIComponent(conversation()!)}`) }] : []}
          />
        </Show>
        <For each={openQs()}>
          {q => (<>
            <DecisionBlock
              question={q.text}
              askedBy={actor(q.askedBy) || q.kind}
              addressedTo="you"
              since={ms(q.askedAt) ?? now()}
              now={now()}
              context={<TaskQuestionContext question={q} />}
              typed={typedAnswer(q)}
              options={answerChoices(q).map((c, i) => ({ ...c, kbd: String(i + 1) }))}
              answered={answers.answered(q.id) ? { by: 'you', answer: answers.answered(q.id)!.answer, at: answers.answered(q.id)!.at, delivered: true } : undefined}
              answering={answers.answering(q.id)}
              onAnswer={a => answers.send(q, a)}
              onSecret={keyConnection(q.options) ? v => answers.addKey(q, v) : undefined}
            />
            <AnswerNote answers={answers} id={q.id} />
          </>)}
        </For>
        <Show when={causeOf(latestExecution()?.failure, latestExecution()?.problem)} keyed>
          {c => (
            <DetailSection title="Latest execution problem">
              <ProblemRow title={c.text} code={c.long} severity="failure" facts="" actions={[]} />
            </DetailSection>
          )}
        </Show>
        <Show when={task.completionReportMissing}>
          <p class="muted">The latest run finished successfully. No completion report is available for that run.</p>
        </Show>
        <Show when={reported()} keyed>
          {rep => (
            <DetailSection title="Latest available worker report">
              <AgeChip t={rep.at} />
              <StateBadge family="execution" state={rep.outcome === 'succeeded' ? 'succeeded' : 'failed'} />
              <Show when={rep.summary}><Markdown text={rep.summary!} /></Show>
            </DetailSection>
          )}
        </Show>
        <Show when={decided().length > 0}>
          <DetailSection title="Previous answers">
            <For each={decided()}>
              {q => (
                <p class="state-line">
                  <Icon name={q.answer === 'accepted' ? 'circle-check' : q.answer === 'rejected' ? 'circle-x' : 'message-circle-question'} />
                  <span>{q.kind === 'acceptance' ? q.answer : `${q.text}: ${q.answer}`}</span>
                  <Show when={q.answeredBy}><span class="muted">by {actor(q.answeredBy!)}</span></Show>
                  <Show when={ms(q.answeredAt)}>{at => <AgeChip t={at()} />}</Show>
                </p>
              )}
            </For>
          </DetailSection>
        </Show>
        <Show when={task.expectedOutcome || req().length > 0 || task.dependencies.length > 0}>
          <OutcomeBlock outcome={task.expectedOutcome ?? ''} requirements={req()} inputs={[]} depends={task.dependencies.length ? dependsOn(task.dependencies) : undefined} />
        </Show>
        <ScopedRollupBar rollup={task.resources} cost={task.cost} />
        <Show when={nodes().length > 0}>
          <DetailSection title="Executions">
            <ExecTree nodes={nodes()} now={now()} label="Executions" />
          </DetailSection>
        </Show>
      </>
    );
  };

  return (
    <Switch
      fallback={
        <article class="detail" data-ui="detail" tabindex={-1} aria-label={t()!.title}>
          <header class="detail-h">
            <Button label="Close" icon="x" iconOnly onClick={props.onClose} />
            <Show when={t()!.projectId} fallback={<span class="muted">No project</span>}>
              {pid => <EntityCode code={pid()} id={pid()} kind="project" />}
            </Show>
            <span class="grow" />
            <Show when={conversation()}>{sid => <Button label="Open session" icon="messages-square" onClick={() => go(`/conversations/${encodeURIComponent(sid())}`)} />}</Show>
            <Button label={pins.has(props.id) ? 'Unpin' : 'Pin'} icon={pins.has(props.id) ? 'pin-off' : 'pin'} iconOnly pressed={pins.has(props.id)} onClick={() => pins.toggle(props.id)} />
            <Show
              when={t()!.state === 'paused'}
              fallback={
                <Show when={!isFinished('task', t()!.state)}>
                  <SplitButton
                    main={{ label: 'Pause', icon: 'pause', onClick: () => !off() && act('pause') }}
                    alternatives={[{ label: 'Cancel task', icon: 'circle-slash', onClick: () => setConfirming(true) }]}
                  />
                </Show>
              }
            >
              <Button label={t()!.pauseReason === 'held' ? 'Release' : 'Continue'} icon="rotate-cw" busy={busy() === 'resume'} disabledReason={disabled()} onClick={() => act('resume')} />
            </Show>
          </header>
          <Show when={confirming()}>
            <ProblemRow
              severity="warning"
              title="Cancel this task?"
              facts="Running work is asked to stop; finished external effects stay."
              actions={[
                { label: 'Yes, cancel', primary: true, onClick: () => { if (!off() && !busy()) void act('cancel'); } },
                { label: 'Keep', onClick: () => setConfirming(false) },
              ]}
            />
          </Show>
          <Show when={failure()}>{f => <ErrorBanner text={f()} />}</Show>
          <h2 class="detail-title">{t()!.title}<EntityCode code={t()!.code} id={t()!.id} kind="task" /></h2>
          <div class="state-line">
            <StateBadge family="task" state={t()!.pauseReason === 'held' ? 'held' : t()!.state} unconfirmed={unconfirmed()} reason={t()!.waitReason ?? undefined} />
            <Show when={t()!.createdBy}><span class="muted">by {actor(t()!.createdBy!)}</span></Show>
            <Show when={ms(t()!.createdAt)}>{at => <AgeChip t={at()} />}</Show>
            <Show when={prs()[0]}>{pr => <StageChip stage={CHIP_STAGE[pr().stage]} />}</Show>
          </div>
          <Show when={prs().length > 0}>
            <ul class="live-list" aria-label="Pull requests">
              <For each={prs()}>{pr => (
                <LiveRow kind="delivery" tone={(states_.delivery[pr.stage]?.tone ?? 'neutral') as Tone} code={`#${pr.number}`} title={pr.title}
                  stage={CHIP_STAGE[pr.stage]} trouble={pr.stage.endsWith('failed')}
                  onOpen={() => go(`/delivery/${encodeURIComponent(`${pr.repo.replace('/', '~')}~${pr.number}`)}`)} />
              )}</For>
            </ul>
          </Show>
          <Show when={result()} keyed>
            {res => (
              <p class="state-line">
                <Icon name={viewOf(res.mediaType ?? '') === 'download' ? 'archive' : 'file-text'} label={res.mediaType ?? 'Result'} />
                <a class="row-link" href={`/results/${encodeURIComponent(res.id)}`} onClick={ev => { ev.preventDefault(); go(`/results/${encodeURIComponent(res.id)}`); }}>
                  {fileLabel(res.path ?? res.name ?? '') || 'Result'}
                </a>
                <EntityCode id={res.artifactId ?? res.id} kind="result" />
              </p>
            )}
          </Show>
          <TabBar
            cls="detail-tabs"
            label="Task views"
            tabs={[
              { id: 'history', label: 'History' },
              { id: 'details', label: 'Details' },
              ...(hasSwarm() ? [{ id: 'swarm' as const, label: 'Swarm', count: swarmCount() }] : []),
            ]}
            value={activeTab()}
            onChange={setTab}
          />
          <div role="tabpanel" aria-label={activeTab() === 'swarm' ? 'Swarm' : activeTab() === 'history' ? 'History' : 'Details'} class="stack">
            <Switch fallback={renderDetailsBody()}>
              <Match when={activeTab() === 'swarm'}><SwarmPanel scopeKind="task" scopeId={props.id} board={board} /></Match>
              {/* History (docs/designs/fleet-history.md): the task's activity, its conversations' runs, questions and state moves
                  interleaved; a row opens its conversation. */}
              <Match when={activeTab() === 'history' && t()}>{task => <HistoryFeed request={() => ({ taskId: task().id })} label="Task history" inside="task" />}</Match>
            </Switch>
          </div>
        </article>
      }
    >
      <Match when={tDoc.error && !t()}>
        <article class="detail" data-ui="detail" tabindex={-1}><ErrorBanner text={`Task did not load: ${tDoc.error}`} /></article>
      </Match>
      <Match when={!t()}>
        <article class="detail" data-ui="detail" tabindex={-1}><Skeleton rows={6} /></article>
      </Match>
    </Switch>
  );
}

