// Persistent bots directory, runtime controls, tasks, runs, questions/errors, analytics, and settings (FLT-20 PR 4).
import { For, Match, Show, Switch, createMemo, createSignal, untrack } from 'solid-js';
import { useLocation } from '../router.ts';
import {
  AgeChip, Ago, Button, CatalogueTree, Choice, CostFigures, DataTable, DecisionBlock,
  DetailSection, Disclosure, Empty, EntityCode, EntityPicker, ErrorBanner, FilterBar,
  KvRow, ListDetail, ListFrame, Num, ProblemRow, ShellActions, Skeleton, SortColHead,
  StateBadge, TabBar, TextField, Totals, actor, keyConnection, matchSearch, states as uiStates,
  type CostFigureLike, type StateOf, type TotalItem,
} from '../../../packages/ui/src/index.ts';
import { addRequestedKey, online, writeOnce, useAlive, type Ops } from '../api.ts';
import {
  BOT_ANALYTICS_WINDOWS, BOT_TABS, analyticsWindowBounds,
  botStatusBadge, bucketProjectSortKey, classifyBotTaskBucket, filterBotRuns,
  filterBotTasks, formatBotTriggers, measureToUiMs, measureToUiNum, sumTokenMeasures,
  type BotAnalyticsWindowId, type BotRunFilter, type BotTab, type BotTaskBucket,
} from '../bot-view.ts';

const VALID_BOT_RUN_STATES = new Set<string>(Object.keys(uiStates.botRun));
const VALID_TASK_STATES = new Set<string>(Object.keys(uiStates.task));
const VALID_EXEC_STATES = new Set<string>(Object.keys(uiStates.execution));
const asBotRunState = (s: string | null | undefined): StateOf<'botRun'> =>
  s && VALID_BOT_RUN_STATES.has(s) ? (s as StateOf<'botRun'>) : 'queued';
const asTaskState = (s: string): StateOf<'task'> =>
  VALID_TASK_STATES.has(s) ? (s as StateOf<'task'>) : 'queued';
const asExecState = (s: string): StateOf<'execution'> =>
  VALID_EXEC_STATES.has(s) ? (s as StateOf<'execution'>) : 'preparing';
import { prefs, prefsRead, scopeIds, usePins } from '../prefs.ts';
import { useOp } from '../store.ts';
import { parseMs as ms } from '../task-facts.ts';

type Bot = Ops['bot_get']['reply']['bot'];
type Settings = Bot['settings'];
type BotRun = Ops['bot_runs']['reply']['runs'][number];
type BotTask = Ops['bot_tasks']['reply']['tasks'][number];
type QuestionRow = Ops['questions_list']['reply']['questions'][number];
type ExecutionRow = Ops['executions_list']['reply']['executions'][number];
type ProjectSortCol = 'project' | 'executions' | 'failed' | 'wall' | 'active' | 'tokens' | 'cost';

const blank: Settings = { name: '', purpose: '', profileId: '', instructions: '', soul: '', scheduleTime: '', scheduleZone: '', wakeOnNeedsYou: false };
const path = (id: string) => '/bots/' + encodeURIComponent(id);

export function Bots(props: { id?: string }) {
  const [, go] = useLocation();
  const [query, setQuery] = createSignal('');
  const bots = useOp('bot_list', () => (prefsRead.value ? scopeIds() : null));
  const projects = useOp('project_list', {});
  const chosen = useOp('bot_get', () => (props.id && props.id !== 'new' ? { botId: props.id } : null));
  const pins = usePins('bots');
  const projectNodes = createMemo(() => {
    const ignored = prefs.value.ignored;
    return (projects.data?.projects ?? []).filter(p => !ignored.includes(String(p.id)))
      .map(p => ({ id: String(p.id), label: String(p.name ?? p.id), icon: 'folder-open' as const }));
  });
  const names = createMemo(() => new Map(projectNodes().map(p => [p.id, p.label])));
  const rows = createMemo(() => bots.data?.bots ?? []);
  const visible = createMemo(() =>
    rows().filter(b => matchSearch(query(), [b.name, b.purpose, names().get(b.projectId)].filter(Boolean).join(' '), { project: b.projectId })),
  );
  const list = <>
    <ShellActions><Button label="New bot" icon="plus" iconOnly onClick={() => go('/bots/new')} /></ShellActions>
    <Show when={bots.error}>{err => <ErrorBanner text={err()} onRetry={bots.reload} />}</Show>
    <ListFrame pending={!bots.data && !bots.error} bar={<FilterBar query={query()} onQuery={setQuery} placeholder="Find bot" shown={visible().length} total={rows().length} />}>
      <Show when={bots.data || bots.error} fallback={<Skeleton rows={4} />}>
        <CatalogueTree label="Bots" selectedId={props.id}
          nodes={visible().map(b => {
            const st = botStatusBadge(b);
            const subParts = [names().get(b.projectId), b.paused ? 'Paused' : b.activeRunId ? 'Running' : st ? b.lastRunState : null].filter(Boolean);
            return { id: b.id, label: b.name, sub: subParts.join(' · '), icon: 'bot', href: path(b.id), pinned: pins.has(b.id) };
          })}
          onPin={node => pins.toggle(node.id)} onSelect={node => go(path(node.id))} emptyText={query() ? 'No matching bots.' : 'No bots in view.'} />
      </Show>
    </ListFrame>
  </>;
  const close = () => go('/bots');
  return (
    <ListDetail
      list={list}
      detail={
        props.id === 'new' ? <BotCreateForm onClose={close} onSaved={b => go(path(b.id))} projects={projectNodes()} />
        : chosen.data ? <Show when={chosen.data.bot.id} keyed>{_botId => <BotDetail bot={chosen.data!.bot} onClose={close} onReload={() => chosen.reload()} projectNames={names()} projects={projectNodes()} />}</Show>
        : chosen.error ? <ErrorBanner text={chosen.error} onRetry={chosen.reload} />
        : props.id ? <Skeleton rows={4} />
        : undefined
      }
      detailKey={props.id}
      idle={<Empty text="Select a bot to inspect its tasks, runs, analytics, and settings." />}
    />
  );
}

function BotCreateForm(props: {
  projects: { id: string; label: string; icon: 'folder-open' }[]; onClose: () => void; onSaved: (bot: Bot) => void;
}) {
  return (
    <article class="detail" data-ui="detail" tabindex={-1} aria-label="New bot">
      <header class="detail-h">
        <h2 class="detail-title">New bot</h2>
        <Button label="Close" icon="x" iconOnly onClick={() => props.onClose()} />
      </header>
      <BotSettingsForm onSaved={props.onSaved} projects={props.projects} />
    </article>
  );
}

function BotDetail(props: {
  bot: Bot;
  projectNames: Map<string, string>;
  projects: { id: string; label: string; icon: 'folder-open' }[];
  onClose: () => void;
  onReload: () => void;
}) {
  const [, go] = useLocation();
  const [tab, setTab] = createSignal<BotTab>('activity');
  const [actionBusy, setActionBusy] = createSignal(false);
  const [actionNote, setActionNote] = createSignal('');
  const [actionError, setActionError] = createSignal('');
  const signal = useAlive();

  const runsOp = useOp('bot_runs', () => ({ botId: props.bot.id, limit: 100 }));
  const tasksOp = useOp('bot_tasks', () => ({ botId: props.bot.id, limit: 100 }));
  const questionsOp = useOp('questions_list', () => ({ botId: props.bot.id, limit: 100 }));
  const execsOp = useOp('executions_list', () => ({ botId: props.bot.id, limit: 100 }));

  const reloadAll = () => {
    props.onReload();
    runsOp.reload();
    tasksOp.reload();
    questionsOp.reload();
    execsOp.reload();
  };

  const runs = createMemo(() => runsOp.data?.runs ?? []);
  const tasks = createMemo(() => tasksOp.data?.tasks ?? []);
  const questions = createMemo(() => questionsOp.data?.questions ?? []);
  const execs = createMemo(() => execsOp.data?.executions ?? []);
  const openQuestions = createMemo(() => questions().filter(q => q.state === 'open'));
  const errorCount = createMemo(() =>
    runs().filter(r => r.state === 'failed' || r.observation === 'unconfirmed' || !!r.problem).length
    + execs().filter(e => e.state === 'failed' || e.observation === 'unconfirmed' || !!e.problem).length,
  );
  const statusBadge = createMemo(() => botStatusBadge({
    paused: props.bot.paused,
    activeRunId: props.bot.activeRunId,
    lastRunState: props.bot.lastRunState,
  }));

  const tabsWithCounts = createMemo(() => BOT_TABS.map(t => {
    if (t.id === 'tasks' && tasks().length > 0) return { ...t, count: tasks().length };
    if (t.id === 'runs' && runs().length > 0) return { ...t, count: runs().length };
    if (t.id === 'questions' && (openQuestions().length + errorCount()) > 0) {
      return { ...t, count: openQuestions().length + errorCount() };
    }
    return t;
  }));

  const runAction = async (kind: 'run' | 'pause' | 'stop') => {
    if (actionBusy()) return;
    setActionBusy(true); setActionError(''); setActionNote('');
    try {
      if (kind === 'run') {
        const rep = await writeOnce('bot_run', { botId: props.bot.id, requestId: crypto.randomUUID() }, () => setActionNote('Queuing run…'), { signal });
        setActionNote(rep.admitted ? 'Run queued.' : `Wake recorded (${rep.run.reason ?? rep.run.state}).`);
      } else if (kind === 'pause') {
        const nextPaused = !props.bot.paused;
        await writeOnce('bot_pause', { botId: props.bot.id, paused: nextPaused }, () => setActionNote(nextPaused ? 'Pausing…' : 'Resuming…'), { signal });
        setActionNote(nextPaused ? 'Bot paused.' : 'Bot resumed.');
      } else {
        const rep = await writeOnce('bot_stop', { botId: props.bot.id }, () => setActionNote('Stopping active run…'), { signal });
        setActionNote(rep.stopped ? `Stop requested (${rep.stop?.outcome ?? 'pending'}).` : 'No active run to stop.');
      }
      reloadAll();
    } catch (e) {
      if (!signal.aborted) { setActionNote(''); setActionError(e instanceof Error ? e.message : String(e)); }
    } finally {
      if (!signal.aborted) setActionBusy(false);
    }
  };

  // Outer CSS grid keeps TabBar fixed at the top while .detail remains the column's sole vertical scroller (OR-25).
  return (
    <div style={{ display: 'grid', 'grid-template-rows': 'auto minmax(0, 1fr)', 'min-width': 0, 'min-height': 0, background: 'var(--surface)' }}>
      <div style={{ 'padding-inline': 'clamp(var(--space-3), calc(8% - var(--space-7)), var(--space-6))', 'padding-top': 'var(--space-3)' }}>
        <TabBar cls="detail-tabs" label="Bot sections" tabs={tabsWithCounts()} value={tab()} onChange={setTab} />
      </div>
      <article class="detail" data-ui="detail" tabindex={-1} aria-label={`${props.bot.settings.name} ${tab()}`}>
        <header class="detail-h">
          <div class="stack" style={{ gap: 'var(--space-1)', 'min-width': 0, flex: '1 1 auto' }}>
            <div class="row" style={{ gap: 'var(--space-2)', 'flex-wrap': 'wrap', 'align-items': 'center' }}>
              <h2 class="detail-title">{props.bot.settings.name}</h2>
              <Show when={statusBadge()}>
                {st => (
                  <Show when={st().family === 'task'} fallback={<StateBadge family="botRun" state={asBotRunState(st().state)} />}>
                    <StateBadge family="task" state={asTaskState(st().state)} />
                  </Show>
                )}
              </Show>
              <Show when={props.bot.paused}><span class="badge t-attention">Paused</span></Show>
            </div>
            <p class="muted" style={{ margin: 0 }}>
              {props.projectNames.get(props.bot.projectId) ?? props.bot.projectId} · {formatBotTriggers(props.bot.settings)}
            </p>
          </div>
          <div class="row" style={{ gap: 'var(--space-2)', 'flex-wrap': 'wrap', 'align-items': 'center' }}>
            <Show when={props.bot.homeSessionId}>
              {sid => <Button label="Home" icon="messages-square" onClick={() => go('/conversations/' + encodeURIComponent(sid()))} />}
            </Show>
            <Button label="Run now" icon="play" kind="primary" busy={actionBusy()} onClick={() => void runAction('run')} />
            <Button label={props.bot.paused ? 'Resume' : 'Pause'} icon={props.bot.paused ? 'play' : 'pause'} busy={actionBusy()} onClick={() => void runAction('pause')} />
            <Show when={props.bot.activeRunId}>
              <Button label="Stop" icon="circle-slash" kind="danger" busy={actionBusy()} onClick={() => void runAction('stop')} />
            </Show>
            <Button label="Close" icon="x" iconOnly onClick={() => props.onClose()} />
          </div>
        </header>

        <Show when={actionError()}>{err => <ErrorBanner text={err()} />}</Show>
        <Show when={actionNote()}>{msg => <p role="status" class="muted">{msg()}</p>}</Show>

        <div role="tabpanel" aria-label={BOT_TABS.find(t => t.id === tab())?.label ?? tab()} class="stack">
          <Switch>
            <Match when={tab() === 'activity'}>
              <BotOverviewTab
                bot={props.bot}
                projectNames={props.projectNames}
                tasks={tasks()}
                runs={runs()}
                openQuestions={openQuestions()}
                execs={execs()}
                onSelectTab={setTab}
                onChanged={reloadAll}
              />
            </Match>
            <Match when={tab() === 'tasks'}>
              <BotTasksTab tasks={tasks()} projectNames={props.projectNames} loading={!tasksOp.data && !tasksOp.error} error={tasksOp.error} onRetry={tasksOp.reload} />
            </Match>
            <Match when={tab() === 'runs'}>
              <BotRunsTab runs={runs()} projectNames={props.projectNames} loading={!runsOp.data && !runsOp.error} error={runsOp.error} onRetry={runsOp.reload} />
            </Match>
            <Match when={tab() === 'questions'}>
              <BotQuestionsErrorsTab
                questions={questions()}
                runs={runs()}
                execs={execs()}
                loading={(!questionsOp.data && !questionsOp.error) || (!execsOp.data && !execsOp.error)}
                error={questionsOp.error ?? execsOp.error ?? runsOp.error}
                onRetry={reloadAll}
                onAnswered={reloadAll}
              />
            </Match>
            <Match when={tab() === 'analytics'}>
              <BotAnalyticsTab botId={props.bot.id} projectNames={props.projectNames} execs={execs()} execsError={execsOp.error} onRetryExecs={execsOp.reload} />
            </Match>
            <Match when={tab() === 'settings'}>
              <BotSettingsForm bot={props.bot} onSaved={reloadAll} projects={props.projects} />
            </Match>
          </Switch>
        </div>
      </article>
    </div>
  );
}

function BotOverviewTab(props: {
  bot: Bot;
  projectNames: Map<string, string>;
  tasks: BotTask[];
  runs: BotRun[];
  openQuestions: QuestionRow[];
  execs: ExecutionRow[];
  onSelectTab: (tab: BotTab) => void;
  onChanged: () => void;
}) {
  const [, go] = useLocation();
  const alive = useAlive();
  const activeOrWaitingTasks = createMemo(() => props.tasks.filter(t => classifyBotTaskBucket(t.state) !== 'history'));
  const failedOrUnconf = createMemo(() =>
    props.runs.filter(r => r.state === 'failed' || r.observation === 'unconfirmed' || !!r.problem),
  );

  return (
    <div class="stack">
      <DetailSection title="Status & Triggers">
        <KvRow icon="bot" label="Purpose" value={<span>{props.bot.settings.purpose || 'No purpose set'}</span>} />
        <KvRow icon="clock" label="Schedule" value={<span>{props.bot.settings.scheduleTime ? `${props.bot.settings.scheduleTime} (${props.bot.settings.scheduleZone || 'UTC'})` : 'Manual / event-driven only'}</span>} />
        <KvRow icon="bell" label="Wake on Needs you" value={<span>{props.bot.settings.wakeOnNeedsYou ? 'Enabled' : 'Disabled'}</span>} />
        <KvRow
          icon="messages-square"
          label="Home conversation"
          value={
            props.bot.homeSessionId
              ? <span class="row" style={{ gap: 'var(--space-2)' }}><EntityCode kind="conversation" id={props.bot.homeSessionId} /><Button label="Open Home" kind="quiet" onClick={() => go('/conversations/' + encodeURIComponent(props.bot.homeSessionId!))} /></span>
              : <span class="muted">Not created</span>
          }
        />
        <Show when={props.bot.lastRunAt} keyed>
          {at => (
            <KvRow
              icon="activity"
              label="Last run"
              meta={<Ago t={ms(at)!} />}
              value={<StateBadge family="botRun" state={asBotRunState(props.bot.lastRunState)} />}
            />
          )}
        </Show>
      </DetailSection>

      <Show when={props.openQuestions.length > 0}>
        <DetailSection title={`Open questions (${props.openQuestions.length})`}>
          <For each={props.openQuestions.slice(0, 3)}>
            {q => <BotQuestionCard question={q} onAnswered={props.onChanged} alive={alive} />}
          </For>
          <Show when={props.openQuestions.length > 3}>
            <Button label={`View all ${props.openQuestions.length} questions`} kind="quiet" onClick={() => props.onSelectTab('questions')} />
          </Show>
        </DetailSection>
      </Show>

      <Show when={failedOrUnconf().length > 0}>
        <DetailSection title="Run problems & unconfirmed wakes">
          <For each={failedOrUnconf().slice(0, 3)}>
            {r => (
              <ProblemRow
                severity={r.state === 'failed' || r.problem ? 'failure' : 'warning'}
                title={r.problem ?? (r.observation === 'unconfirmed' ? 'Run outcome unconfirmed' : `Run ${r.state}`)}
                facts={<>{r.cause} · <Ago t={ms(r.createdAt)!} /></>}
                actions={r.sessionId ? [{ label: 'Open conversation', onClick: () => go('/conversations/' + encodeURIComponent(r.sessionId!)) }] : []}
              />
            )}
          </For>
        </DetailSection>
      </Show>

      <DetailSection title={`Active & waiting tasks (${activeOrWaitingTasks().length})`}>
        <Show when={activeOrWaitingTasks().length > 0} fallback={<Empty text="No active or waiting tasks." />}>
          <BotTasksTable tasks={activeOrWaitingTasks().slice(0, 8)} projectNames={props.projectNames} />
          <Show when={props.tasks.length > activeOrWaitingTasks().length}>
            <Button label={`Inspect all ${props.tasks.length} tasks`} kind="quiet" onClick={() => props.onSelectTab('tasks')} />
          </Show>
        </Show>
      </DetailSection>

      <DetailSection title={`Recent runs (${props.runs.length})`}>
        <Show when={props.runs.length > 0} fallback={<Empty text="No wake runs recorded yet." />}>
          <BotRunsTable runs={props.runs.slice(0, 8)} projectNames={props.projectNames} />
          <Show when={props.runs.length > 8}>
            <Button label={`Inspect all ${props.runs.length} runs`} kind="quiet" onClick={() => props.onSelectTab('runs')} />
          </Show>
        </Show>
      </DetailSection>
    </div>
  );
}

function BotTasksTab(props: {
  tasks: BotTask[]; projectNames: Map<string, string>; loading: boolean; error: string | null; onRetry: () => void;
}) {
  const [bucket, setBucket] = createSignal<BotTaskBucket>('all');
  const filtered = createMemo(() => filterBotTasks(props.tasks, bucket()));
  const counts = createMemo(() => ({
    all: props.tasks.length,
    active: props.tasks.filter(t => classifyBotTaskBucket(t.state) === 'active').length,
    waiting: props.tasks.filter(t => classifyBotTaskBucket(t.state) === 'waiting').length,
    history: props.tasks.filter(t => classifyBotTaskBucket(t.state) === 'history').length,
  }));

  return (
    <div class="stack">
      <Show when={props.error}>{err => <ErrorBanner text={err()} onRetry={props.onRetry} />}</Show>
      <div class="row" style={{ 'justify-content': 'space-between', 'align-items': 'center', 'flex-wrap': 'wrap', gap: 'var(--space-2)' }}>
        <Choice
          label="Task state"
          value={bucket()}
          onChange={v => setBucket(v as BotTaskBucket)}
          options={[
            { id: 'all', label: `All (${counts().all})` },
            { id: 'active', label: `Active (${counts().active})` },
            { id: 'waiting', label: `Waiting (${counts().waiting})` },
            { id: 'history', label: `Historical (${counts().history})` },
          ]}
        />
      </div>
      <Show when={!props.loading} fallback={<Skeleton rows={4} />}>
        <Show when={filtered().length > 0} fallback={<Empty text="No tasks in this category." />}>
          <BotTasksTable tasks={filtered()} projectNames={props.projectNames} />
        </Show>
      </Show>
    </div>
  );
}

function BotTasksTable(props: { tasks: BotTask[]; projectNames: Map<string, string> }) {
  const [, go] = useLocation();
  return (
    <DataTable
      label="Managed bot tasks"
      head={
        <tr>
          <th scope="col">Task</th>
          <th scope="col">State</th>
          <th scope="col">Title</th>
          <th scope="col">Link</th>
          <th scope="col" class="num">Questions</th>
          <th scope="col" class="num">Executions</th>
          <th scope="col">Updated</th>
        </tr>
      }
    >
      <For each={props.tasks}>
        {t => (
          <tr>
            <td><EntityCode kind="task" code={t.taskCode} id={t.taskId} /></td>
            <td><StateBadge family="task" state={asTaskState(t.state)} reason={t.waitReason ?? t.pauseReason ?? undefined} /></td>
            <td>
              <Button label={t.title} rowLink onClick={() => go('/tasks/' + encodeURIComponent(t.taskId))} />
              <div class="muted">{props.projectNames.get(t.projectId) ?? t.projectId}</div>
            </td>
            <td><span class="badge t-neutral">{t.linkKind}</span></td>
            <td class="num"><span class={t.openQuestions > 0 ? 'badge t-attention' : 'muted'}>{t.openQuestions}</span></td>
            <td class="num">{t.executionCount}</td>
            <td><Ago t={ms(t.updatedAt)!} /></td>
          </tr>
        )}
      </For>
    </DataTable>
  );
}

function BotRunsTab(props: {
  runs: BotRun[]; projectNames: Map<string, string>; loading: boolean; error: string | null; onRetry: () => void;
}) {
  const [filter, setFilter] = createSignal<BotRunFilter>('all');
  const filtered = createMemo(() => filterBotRuns(props.runs, filter()));

  return (
    <div class="stack">
      <Show when={props.error}>{err => <ErrorBanner text={err()} onRetry={props.onRetry} />}</Show>
      <Choice
        label="Run filter"
        value={filter()}
        onChange={v => setFilter(v as BotRunFilter)}
        options={[
          { id: 'all', label: `All (${props.runs.length})` },
          { id: 'active', label: `Active (${filterBotRuns(props.runs, 'active').length})` },
          { id: 'quiet', label: `Quiet (${filterBotRuns(props.runs, 'quiet').length})` },
          { id: 'skipped', label: `Skipped (${filterBotRuns(props.runs, 'skipped').length})` },
          { id: 'queued', label: `Queued (${filterBotRuns(props.runs, 'queued').length})` },
          { id: 'failed', label: `Failed (${filterBotRuns(props.runs, 'failed').length})` },
          { id: 'unconfirmed', label: `Unconfirmed (${filterBotRuns(props.runs, 'unconfirmed').length})` },
        ]}
      />
      <Show when={!props.loading} fallback={<Skeleton rows={4} />}>
        <Show when={filtered().length > 0} fallback={<Empty text="No runs match this filter." />}>
          <BotRunsTable runs={filtered()} projectNames={props.projectNames} />
        </Show>
      </Show>
    </div>
  );
}

function BotRunsTable(props: { runs: BotRun[]; projectNames: Map<string, string> }) {
  return (
    <DataTable
      label="Bot runs"
      head={
        <tr>
          <th scope="col">State</th>
          <th scope="col">Cause &amp; Triggers</th>
          <th scope="col">Details / Reason</th>
          <th scope="col">Refs</th>
          <th scope="col">Host / Source</th>
          <th scope="col">Created</th>
        </tr>
      }
    >
      <For each={props.runs}>
        {r => (
          <tr>
            <td>
              <div class="row" style={{ gap: 'var(--space-1)', 'flex-wrap': 'wrap' }}>
                <StateBadge family="botRun" state={asBotRunState(r.state)} unconfirmed={r.observation === 'unconfirmed'} reason={r.reason ?? undefined} />
                <Show when={r.quiet}><span class="badge t-neutral">Quiet</span></Show>
              </div>
            </td>
            <td>
              <div><strong>{r.cause}</strong></div>
              <Show when={r.triggers.length > 0}>
                <div class="muted">{r.triggers.map(tr => tr.summary || `${tr.kind}:${tr.id}`).join(', ')}</div>
              </Show>
            </td>
            <td>
              <Show when={r.problem}><div><span class="badge t-danger">{r.problem}</span></div></Show>
              <Show when={!r.problem && r.reason}><span class="muted">{r.reason}</span></Show>
              <Show when={!r.problem && !r.reason}><span class="muted">{props.projectNames.get(r.projectId) ?? r.projectId}</span></Show>
            </td>
            <td>
              <div class="row" style={{ gap: 'var(--space-1)', 'flex-wrap': 'wrap' }}>
                <Show when={r.executionId} keyed>{eid => <EntityCode kind="execution" id={eid} />}</Show>
                <Show when={r.sessionId} keyed>{sid => <EntityCode kind="conversation" id={sid} />}</Show>
                <For each={r.linkedTaskIds}>{tid => <EntityCode kind="task" id={tid} />}</For>
              </div>
            </td>
            <td class="muted">{[r.hostId, r.sentBy, r.source].filter(Boolean).join(' · ') || '—'}</td>
            <td><Ago t={ms(r.startedAt ?? r.createdAt)!} /></td>
          </tr>
        )}
      </For>
    </DataTable>
  );
}

function BotQuestionCard(props: { question: QuestionRow; onAnswered: () => void; alive: AbortSignal }) {
  const [failure, setFailure] = createSignal<string | null>(null);
  const q = () => props.question;
  const conn = () => keyConnection(q().options);
  const opts = () => (Array.isArray(q().options) ? q().options as unknown[] : q().kind === 'acceptance' ? ['accepted', 'rejected'] : conn() ? ['decline'] : [])
    .map((o, i) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }));

  return (
    <div class="stack">
      <Show when={failure()}>{err => <ErrorBanner text={err()} />}</Show>
      <DecisionBlock
        question={q().text}
        askedBy={actor(q().askedBy) || q().kind}
        addressedTo="you"
        since={ms(q().askedAt) ?? Date.now()}
        now={Date.now()}
        context={
          <div class="row" style={{ gap: 'var(--space-2)', 'flex-wrap': 'wrap' }}>
            <Show when={q().taskId} keyed>{tid => <EntityCode kind="task" id={tid} />}</Show>
            <Show when={q().executionId} keyed>{eid => <EntityCode kind="execution" id={eid} />}</Show>
            <Show when={q().sessionId} keyed>{sid => <EntityCode kind="conversation" id={sid} />}</Show>
          </div>
        }
        options={opts()}
        onAnswer={a => {
          if (!online.value) return;
          writeOnce('questions_answer', { questionId: q().id, answer: a }, () => setFailure('Confirming answer…'), { signal: props.alive })
            .then(() => { setFailure(null); props.onAnswered(); }, e => setFailure(e instanceof Error ? e.message : String(e)));
        }}
        onSecret={conn() ? v => addRequestedKey(q().id, conn()!, v, props.alive).then(() => props.onAnswered()) : undefined}
      />
    </div>
  );
}

function BotQuestionsErrorsTab(props: {
  questions: QuestionRow[];
  runs: BotRun[];
  execs: ExecutionRow[];
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  onAnswered: () => void;
}) {
  const [, go] = useLocation();
  const alive = useAlive();
  const openQs = createMemo(() => props.questions.filter(q => q.state === 'open'));
  const closedQs = createMemo(() => props.questions.filter(q => q.state !== 'open'));
  const problemRuns = createMemo(() => props.runs.filter(r => r.state === 'failed' || r.observation === 'unconfirmed' || !!r.problem));
  const problemExecs = createMemo(() => props.execs.filter(e => e.state === 'failed' || e.observation === 'unconfirmed' || !!e.problem));

  return (
    <div class="stack">
      <Show when={props.error}>{err => <ErrorBanner text={err()} onRetry={props.onRetry} />}</Show>
      <Show when={!props.loading} fallback={<Skeleton rows={4} />}>
        <DetailSection title={`Open questions (${openQs().length})`}>
          <Show when={openQs().length > 0} fallback={<Empty text="No open questions for this bot." />}>
            <For each={openQs()}>
              {q => <BotQuestionCard question={q} onAnswered={props.onAnswered} alive={alive} />}
            </For>
          </Show>
        </DetailSection>

        <DetailSection title={`Errors & unconfirmed work (${problemRuns().length + problemExecs().length})`}>
          <Show when={problemRuns().length + problemExecs().length > 0} fallback={<Empty text="No run or execution errors recorded." />}>
            <For each={problemRuns()}>
              {r => (
                <ProblemRow
                  severity={r.state === 'failed' || r.problem ? 'failure' : 'warning'}
                  title={`Bot run (${r.cause}): ${r.problem ?? (r.observation === 'unconfirmed' ? 'unconfirmed outcome' : r.state)}`}
                  facts={<>{r.reason ? `${r.reason} · ` : ''}<Ago t={ms(r.createdAt)!} /></>}
                  actions={r.sessionId ? [{ label: 'Open conversation', onClick: () => go('/conversations/' + encodeURIComponent(r.sessionId!)) }] : []}
                />
              )}
            </For>
            <For each={problemExecs()}>
              {e => (
                <ProblemRow
                  severity={e.state === 'failed' || e.problem ? 'failure' : 'warning'}
                  title={`Execution ${e.kind}: ${e.problem ?? (e.observation === 'unconfirmed' ? 'unconfirmed outcome' : e.state)}`}
                  facts={<>{e.projectId ?? 'No project'} · <AgeChip t={ms(e.startedAt ?? e.lastSeenAt) ?? Date.now()} /></>}
                  actions={[
                    ...(e.sessionId ? [{ label: 'Conversation', onClick: () => go('/conversations/' + encodeURIComponent(e.sessionId!)) }] : []),
                    ...(e.taskId ? [{ label: 'Task', onClick: () => go('/tasks/' + encodeURIComponent(e.taskId!)) }] : []),
                  ]}
                />
              )}
            </For>
          </Show>
        </DetailSection>

        <Show when={closedQs().length > 0}>
          <Disclosure summary={<span>Answered / withdrawn questions ({closedQs().length})</span>}>
            <DataTable
              label="Historical bot questions"
              head={
                <tr>
                  <th scope="col">Question</th>
                  <th scope="col">State</th>
                  <th scope="col">Answer</th>
                  <th scope="col">Asked</th>
                </tr>
              }
            >
              <For each={closedQs()}>
                {q => (
                  <tr>
                    <td>
                      <div>{q.text}</div>
                      <div class="row" style={{ gap: 'var(--space-1)' }}>
                        <Show when={q.code} keyed>{c => <EntityCode kind="question" code={c} id={q.id} />}</Show>
                        <Show when={q.taskId} keyed>{tid => <EntityCode kind="task" id={tid} />}</Show>
                      </div>
                    </td>
                    <td><StateBadge family="question" state={q.state as 'open' | 'answered' | 'withdrawn'} /></td>
                    <td>{q.answer ?? '—'}</td>
                    <td><Ago t={ms(q.askedAt)!} /></td>
                  </tr>
                )}
              </For>
            </DataTable>
          </Disclosure>
        </Show>
      </Show>
    </div>
  );
}

function BotAnalyticsTab(props: {
  botId: string;
  projectNames: Map<string, string>;
  execs: ExecutionRow[];
  execsError: string | null;
  onRetryExecs: () => void;
}) {
  const [winId, setWinId] = createSignal<BotAnalyticsWindowId>('all');
  const [sortCol, setSortCol] = createSignal<ProjectSortCol>('cost');
  const [sortDesc, setSortDesc] = createSignal(true);
  const [nowAnchor] = createSignal(Date.now());

  const req = createMemo<Ops['analytics_query']['request']>(() => ({
    window: analyticsWindowBounds(winId(), nowAnchor()),
    dimensions: ['project'],
    filters: { bot: [props.botId] },
    measures: ['executions', 'failed', 'wall_ms', 'active_ms', 'tokens_input', 'tokens_output', 'cost'],
  }));

  const analyticsOp = useOp('analytics_query', () => (prefsRead.value ? req() : null), { quiet: 30_000 });

  const totalItems = createMemo((): TotalItem[] => {
    const d = analyticsOp.data;
    if (!d) return [];
    const m = d.totals.measures;
    return [
      { label: 'Executions', m: measureToUiNum(m.executions), kind: 'count' },
      { label: 'Failed', m: measureToUiNum(m.failed), kind: 'count' },
      { label: 'Wall time', m: measureToUiMs(m.wall_ms), kind: 'duration' },
      { label: 'Active time', m: measureToUiMs(m.active_ms), kind: 'duration' },
      { label: 'Tokens', m: sumTokenMeasures(m), kind: 'count' },
      { label: 'Cost', cost: d.totals.cost as CostFigureLike[], showUnavailable: true },
    ];
  });

  const sortedRows = createMemo(() => {
    const rows = [...(analyticsOp.data?.rows ?? [])];
    const col = sortCol();
    const mult = sortDesc() ? -1 : 1;
    rows.sort((a, b) => {
      const ka = bucketProjectSortKey(a, col);
      const kb = bucketProjectSortKey(b, col);
      if (typeof ka === 'string' && typeof kb === 'string') return mult * ka.localeCompare(kb);
      return mult * ((ka as number) - (kb as number));
    });
    return rows;
  });

  const toggleSort = (col: ProjectSortCol) => {
    if (sortCol() === col) setSortDesc(d => !d);
    else { setSortCol(col); setSortDesc(col !== 'project'); }
  };

  return (
    <div class="stack">
      <div class="row" style={{ 'justify-content': 'space-between', 'align-items': 'center', 'flex-wrap': 'wrap', gap: 'var(--space-2)' }}>
        <Choice
          label="Period"
          value={winId()}
          onChange={v => setWinId(v as BotAnalyticsWindowId)}
          options={BOT_ANALYTICS_WINDOWS.map(w => ({ id: w.id, label: w.label }))}
        />
        <Button label="Refresh" icon="rotate-cw" kind="quiet" onClick={() => { analyticsOp.reload(); props.onRetryExecs(); }} />
      </div>

      <Show when={analyticsOp.error}>{err => <ErrorBanner text={err()} onRetry={analyticsOp.reload} />}</Show>
      <Show when={analyticsOp.data} fallback={<Skeleton rows={4} />}>
        {data => (
          <>
            <Totals items={totalItems()} />
            <Show when={data().unknown > 0 || data().hidden > 0}>
              <p class="muted">
                <Show when={data().unknown > 0}>Unknown/unpriced observations: {data().unknown}. </Show>
                <Show when={data().hidden > 0}>Restricted project executions excluded from view: {data().hidden}.</Show>
              </p>
            </Show>

            <DetailSection title="Spend & Usage by Recorded Execution Project">
              <Show when={sortedRows().length > 0} fallback={<Empty text="No attributed leaf executions in this window." />}>
                <DataTable
                  label="Bot analytics by project"
                  head={
                    <tr>
                      <SortColHead label="Project" active={sortCol() === 'project'} dir={sortDesc() ? 'descending' : 'ascending'} onSort={() => toggleSort('project')} />
                      <SortColHead label="Executions" numeric active={sortCol() === 'executions'} dir={sortDesc() ? 'descending' : 'ascending'} onSort={() => toggleSort('executions')} />
                      <SortColHead label="Failed" numeric active={sortCol() === 'failed'} dir={sortDesc() ? 'descending' : 'ascending'} onSort={() => toggleSort('failed')} />
                      <SortColHead label="Wall time" numeric active={sortCol() === 'wall'} dir={sortDesc() ? 'descending' : 'ascending'} onSort={() => toggleSort('wall')} />
                      <SortColHead label="Active time" numeric active={sortCol() === 'active'} dir={sortDesc() ? 'descending' : 'ascending'} onSort={() => toggleSort('active')} />
                      <SortColHead label="Tokens" numeric active={sortCol() === 'tokens'} dir={sortDesc() ? 'descending' : 'ascending'} onSort={() => toggleSort('tokens')} />
                      <SortColHead label="Cost" numeric active={sortCol() === 'cost'} dir={sortDesc() ? 'descending' : 'ascending'} onSort={() => toggleSort('cost')} />
                    </tr>
                  }
                >
                  <For each={sortedRows()}>
                    {r => {
                      const projId = r.dimensions.project ?? 'unknown';
                      return (
                        <tr>
                          <td>
                            <Show when={projId !== 'unknown'} fallback={<span class="badge t-neutral">unknown</span>}>
                              <span class="row" style={{ gap: 'var(--space-1)', 'align-items': 'center' }}>
                                <EntityCode kind="project" id={projId} />
                                <Show when={props.projectNames.get(projId) && props.projectNames.get(projId) !== projId}>
                                  <span class="muted">{props.projectNames.get(projId)}</span>
                                </Show>
                              </span>
                            </Show>
                          </td>
                          <td class="num"><Num m={measureToUiNum(r.measures.executions)} kind="count" /></td>
                          <td class="num"><Num m={measureToUiNum(r.measures.failed)} kind="count" /></td>
                          <td class="num"><Num m={measureToUiMs(r.measures.wall_ms)} kind="duration" /></td>
                          <td class="num"><Num m={measureToUiMs(r.measures.active_ms)} kind="duration" /></td>
                          <td class="num"><Num m={sumTokenMeasures(r.measures)} kind="count" /></td>
                          <td class="num"><CostFigures cost={r.cost as CostFigureLike[]} showUnavailable empty="—" /></td>
                        </tr>
                      );
                    }}
                  </For>
                </DataTable>
              </Show>
            </DetailSection>
          </>
        )}
      </Show>

      <DetailSection title={`Attributed executions (${props.execs.length})`}>
        <Show when={props.execsError}>{err => <ErrorBanner text={err()} onRetry={props.onRetryExecs} />}</Show>
        <Show when={props.execs.length > 0} fallback={<Empty text="No attributed source executions found." />}>
          <DataTable
            label="Attributed executions"
            head={
              <tr>
                <th scope="col">Execution</th>
                <th scope="col">State</th>
                <th scope="col">Kind</th>
                <th scope="col">Project</th>
                <th scope="col">Links</th>
                <th scope="col">Started</th>
              </tr>
            }
          >
            <For each={props.execs}>
              {e => (
                <tr>
                  <td><EntityCode kind="execution" id={e.id} /></td>
                  <td><StateBadge family="execution" state={asExecState(e.state)} unconfirmed={e.observation === 'unconfirmed'} reason={e.problem ?? undefined} /></td>
                  <td>{e.kind}</td>
                  <td>{e.projectId ? <EntityCode kind="project" id={e.projectId} /> : <span class="muted">unknown</span>}</td>
                  <td>
                    <div class="row" style={{ gap: 'var(--space-1)', 'flex-wrap': 'wrap' }}>
                      <Show when={e.taskId} keyed>{tid => <EntityCode kind="task" id={tid} />}</Show>
                      <Show when={e.sessionId} keyed>{sid => <EntityCode kind="conversation" id={sid} />}</Show>
                    </div>
                  </td>
                  <td><Show when={e.startedAt ?? e.lastSeenAt} keyed fallback={<span class="muted">—</span>}>{at => <Ago t={ms(at)!} />}</Show></td>
                </tr>
              )}
            </For>
          </DataTable>
        </Show>
      </DetailSection>
    </div>
  );
}

function BotSettingsForm(props: {
  bot?: Bot; projects: { id: string; label: string; icon: 'folder-open' }[]; onSaved: (bot: Bot) => void;
}) {
  const initial = untrack(() => props.bot);
  const botId = initial?.id ?? crypto.randomUUID();
  const [projectId, setProjectId] = createSignal(initial?.projectId ?? '');
  const [settings, setSettings] = createSignal<Settings>(initial?.settings ?? blank);
  const [revision, setRevision] = createSignal(initial?.revision ?? 0);
  const [memory, setMemory] = createSignal(initial?.memory ?? '');
  const [memoryRev, setMemoryRev] = createSignal(initial?.memoryRevision ?? 0);
  const [busy, setBusy] = createSignal(false);
  const [status, setStatus] = createSignal('');
  const [error, setError] = createSignal('');
  const signal = useAlive();
  const presets = useOp('session_presets', () => (projectId() ? { projectId: projectId() } : null));
  const profiles = createMemo(() => {
    const map = new Map<string, { id: string; label: string; sub: string; icon: 'bot' }>();
    for (const preset of presets.data?.presets ?? []) {
      if (preset.profileId && !preset.unavailable && !map.has(preset.profileId)) {
        map.set(preset.profileId, { id: preset.profileId, label: preset.name, sub: [preset.harness, preset.model].filter(Boolean).join(' · '), icon: 'bot' });
      }
    }
    const curProfileId = settings().profileId;
    if (curProfileId && !map.has(curProfileId)) {
      map.set(curProfileId, { id: curProfileId, label: 'Saved profile', sub: '', icon: 'bot' });
    }
    return [...map.values()];
  });
  const field = (key: keyof Omit<Settings, 'wakeOnNeedsYou'>, value: string) => setSettings(s => ({ ...s, [key]: value }));

  const save = async (kind: 'settings' | 'memory') => {
    if (busy()) return;
    setBusy(true); setError(''); setStatus('');
    try {
      const reply = kind === 'settings'
        ? await writeOnce('bot_settings_save', { botId, projectId: projectId(), baseRevision: revision(), settings: settings() }, () => setStatus('Confirming save…'), { signal })
        : await writeOnce('bot_memory_save', { botId, baseRevision: memoryRev(), memory: memory() }, () => setStatus('Confirming save…'), { signal });
      if (kind === 'settings') setRevision(reply.bot.revision);
      else setMemoryRev(reply.bot.memoryRevision);
      setStatus(kind === 'settings' ? 'Settings saved.' : 'Memory saved.');
      props.onSaved(reply.bot);
    } catch (e) {
      if (!signal.aborted) { setStatus(''); setError(e instanceof Error ? e.message : String(e)); }
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  };

  return (
    <div class="stack">
      <Show when={error()}>{err => <ErrorBanner text={err()} />}</Show>
      <DetailSection title="Settings">
        <Show when={!props.bot}>
          <EntityPicker label="Project" nodes={props.projects} value={projectId() || null} onChange={id => { setProjectId(id ?? ''); field('profileId', ''); }} />
        </Show>
        <TextField label="Name" value={settings().name} onChange={v => field('name', v)} />
        <TextField label="Purpose" multiline value={settings().purpose} onChange={v => field('purpose', v)} />
        <EntityPicker label="Fleet profile" nodes={profiles()} value={settings().profileId || null} onChange={v => field('profileId', v ?? '')}
          disabledReason={projectId() ? undefined : 'Select a project first'} loading={!!projectId() && !presets.data && !presets.error} error={presets.error} onRetry={presets.reload} />
        <TextField label="Instructions" multiline value={settings().instructions} onChange={v => field('instructions', v)} />
        <TextField label="SOUL" multiline value={settings().soul} onChange={v => field('soul', v)} />
        <TextField label="Daily wake time" value={settings().scheduleTime} onChange={v => field('scheduleTime', v)} help="Use HH:MM in the selected time zone. Leave blank to omit a schedule." />
        <TextField label="Time zone" value={settings().scheduleZone} onChange={v => field('scheduleZone', v)} help="Use an IANA time zone, such as Etc/UTC." />
        <Choice label="Wake on new Needs you entries" value={settings().wakeOnNeedsYou ? 'on' : 'off'} options={[{ id: 'off', label: 'Needs you off' }, { id: 'on', label: 'Needs you on' }]}
          onChange={v => setSettings(s => ({ ...s, wakeOnNeedsYou: v === 'on' }))} />
        <Button label={props.bot ? 'Save settings' : 'Create bot'} kind="primary" busy={busy()} disabledReason={projectId() ? undefined : 'Select a project first'} onClick={() => void save('settings')} />
      </DetailSection>
      <Show when={props.bot}>
        <DetailSection title="Project memory">
          <TextField label="Memory" multiline value={memory()} onChange={setMemory} />
          <Button label="Save memory" busy={busy()} onClick={() => void save('memory')} />
        </DetailSection>
      </Show>
      <p role="status">{status()}</p>
    </div>
  );
}
