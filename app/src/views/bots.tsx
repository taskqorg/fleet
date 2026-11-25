// A persistent bot's definition in Library (pages/library.md, Bots root): runtime controls, automations, tasks, runs and
// settings. Its open questions show on Home; its cost is a purpose on System > Spend (OR-54).
import { For, Match, Show, Switch, createMemo, createSignal, untrack } from 'solid-js';
import { useLocation, useSearch } from '../router.ts';
import {
  Ago, Avatar, Button, Choice, DataTable, DetailSection, Empty, EntityCode, EntityPicker, ErrorBanner,
  KvRow, ListEnd, ProblemRow, Skeleton, StateBadge, TabBar, TextField, states as uiStates, causeOf, type Pager,
  type StateOf,
} from '../../../packages/ui/src/index.ts';
import { writeOnce, useAlive, type Ops } from '../api.ts';
import {
  BOT_TABS, botAvatarState, botPicture, botStatusBadge, classifyBotTaskBucket, filterBotRuns, filterBotTasks, reasonLabel, sourceLabel, wakeLabel,
  type BotRunFilter, type BotTab, type BotTaskBucket,
} from '../bot-view.ts';

const VALID_BOT_RUN_STATES = new Set<string>(Object.keys(uiStates.botRun));
const VALID_TASK_STATES = new Set<string>(Object.keys(uiStates.task));
const asBotRunState = (s: string | null | undefined): StateOf<'botRun'> =>
  s && VALID_BOT_RUN_STATES.has(s) ? (s as StateOf<'botRun'>) : 'queued';
const asTaskState = (s: string): StateOf<'task'> =>
  VALID_TASK_STATES.has(s) ? (s as StateOf<'task'>) : 'queued';
import { useOp } from '../store.ts';
import { usePins } from '../prefs.ts';
import { PIN_LISTS } from '../rail-pinned.ts';
import { useOpPages } from '../op-pages.ts';
import { parseMs as ms } from '../task-facts.ts';
import { BotAutomations } from './bot-automations.tsx';
import { BotAvatarField } from './bot-avatar.tsx';

type Bot = Ops['bot_get']['reply']['bot'];
type Settings = Bot['settings'];
type BotRun = Ops['bot_runs']['reply']['runs'][number];
/** A failed or cancelled run's cause in words (formats.md); a finished run that carries a problem did not fail. */
const runCause = (r: BotRun) => r.state === 'failed' || r.state === 'cancelled' ? causeOf(r.failure, r.problem) : undefined;
type BotTask = Ops['bot_tasks']['reply']['tasks'][number];

/** The Overview hero's state line, in the words of the avatar's light (controls/avatar.md). */
const botAvatarWords = { asleep: 'Asleep', working: 'Working now', waiting: 'Waiting for you', failed: 'Last run failed', paused: 'Paused' } as const;

const blank: Settings = { name: '', purpose: '', profileId: '', instructions: '', soul: '' };
/** A bot's Library route: its D17 code (B-FLT-1), else its id. */
export const botPath = (b: { id: string; code?: string | null }) => '/library/bots/' + encodeURIComponent(b.code || b.id);
const path = (b: { id: string; code?: string | null }) => botPath(b);


export function BotCreateForm(props: {
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

export function BotDetail(props: {
  bot: Bot;
  projectNames: Map<string, string>;
  projects: { id: string; label: string; icon: 'folder-open' }[];
  onClose: () => void;
  onReload: () => void;
}) {
  const [, go] = useLocation();
  // The bot's pin (nav-rail.md, Pinned): its Library pin, also the navigation's Pinned zone.
  const pins = usePins(PIN_LISTS.bots);
  const search = useSearch();
  const params = createMemo(() => new URLSearchParams(search()));
  const tab = () => BOT_TABS.find(t => t.id === params().get('tab'))?.id ?? 'activity';
  const showTab = (tab: BotTab, extra: Record<string, string> = {}) => {
    go(tab === 'activity' ? path(props.bot)
      : path(props.bot) + '?' + new URLSearchParams({ tab, ...extra }));
  };
  const setTab = (tab: BotTab) => showTab(tab);
  const automationId = () => tab() === 'runs' ? params().get('automation') : null;
  const [actionBusy, setActionBusy] = createSignal(false);
  const [actionNote, setActionNote] = createSignal('');
  const [actionError, setActionError] = createSignal('');
  const signal = useAlive();

  // Runs and tasks load a page at a time as their tables scroll near the end (OR-65); the overview previews the first page.
  const runsPaged = useOpPages('bot_runs', () => ({ botId: props.bot.id, limit: 50 }), {
    page: r => ({ rows: r.runs, next: r.nextAfter ?? null }), after: after => ({ after }), key: r => r.id,
  });
  const runsOp = runsPaged.first;
  const automationPaged = useOpPages('bot_runs', () => {
    const id = automationId();
    return id ? { botId: props.bot.id, automationId: id, limit: 50 } : null;
  }, { page: r => ({ rows: r.runs, next: r.nextAfter ?? null }), after: after => ({ after }), key: r => r.id });
  const automationRuns = automationPaged.first;
  const tasksPaged = useOpPages('bot_tasks', () => ({ botId: props.bot.id, limit: 50 }), {
    page: r => ({ rows: r.tasks, next: r.nextAfter ?? null }), after: after => ({ after }), key: t => t.taskId,
  });
  const tasksOp = tasksPaged.first;

  const reloadAll = () => {
    props.onReload();
    runsOp.reload();
    automationRuns.reload();
    tasksOp.reload();
  };

  const runs = createMemo(() => runsPaged.pages.rows);
  const tasks = createMemo(() => tasksPaged.pages.rows);
  const statusBadge = createMemo(() => botStatusBadge({
    paused: props.bot.paused,
    activeRunId: props.bot.activeRunId,
    lastRunState: props.bot.lastRunState,
  }));

  const tabsWithCounts = createMemo(() => BOT_TABS.map(t => {
    // Counts only for a complete list: a list that loads on scroll knows no total (OR-65).
    if (t.id === 'tasks' && tasks().length > 0 && tasksPaged.pages.done) return { ...t, count: tasks().length };
    if (t.id === 'runs' && runs().length > 0 && runsPaged.pages.done) return { ...t, count: runs().length };
    return t;
  }));

  const runAction = async (kind: 'run' | 'pause' | 'stop') => {
    if (actionBusy()) return;
    setActionBusy(true); setActionError(''); setActionNote('');
    try {
      if (kind === 'run') {
        const rep = await writeOnce('bot_run', { botId: props.bot.id, requestId: crypto.randomUUID() }, () => setActionNote('Queuing run…'), { signal });
        setActionNote(rep.admitted ? 'Run queued.' : `Wake recorded (${reasonLabel(rep.run.reason) ?? rep.run.state}).`);
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
          <Avatar letter={props.bot.settings.name} tone="success" size="lg" picture={botPicture(props.bot)} state={botAvatarState(props.bot)} />
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
              {props.projectNames.get(props.bot.projectId) ?? props.bot.projectId}
            </p>
          </div>
          <div class="row" style={{ gap: 'var(--space-2)', 'flex-wrap': 'wrap', 'align-items': 'center' }}>
            <Show when={props.bot.homeSessionId}>
              {sid => <Button label="Talk to it" icon="messages-square" onClick={() => go('/conversations/' + encodeURIComponent(sid()))} />}
            </Show>
            <Button label={tab() === 'automations' ? 'Run bot now' : 'Run now'} icon="play" kind="primary" busy={actionBusy()} onClick={() => void runAction('run')} />
            <Button label={(props.bot.paused ? 'Resume' : 'Pause') + (tab() === 'automations' ? ' bot' : '')} icon={props.bot.paused ? 'play' : 'pause'} busy={actionBusy()} onClick={() => void runAction('pause')} />
            <Show when={props.bot.activeRunId}>
              <Button label="Stop" icon="circle-slash" kind="danger" busy={actionBusy()} onClick={() => void runAction('stop')} />
            </Show>
            <Button label={pins.has('bot:' + props.bot.id) ? 'Unpin' : 'Pin to navigation'} icon="pin" iconOnly pressed={pins.has('bot:' + props.bot.id)} onClick={() => pins.toggle('bot:' + props.bot.id)} />
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
                onSelectTab={setTab}
                tabHref={t => path(props.bot) + '?' + new URLSearchParams({ tab: t })}
              />
            </Match>
            <Match when={tab() === 'automations'}>
              <BotAutomations bot={props.bot} projectName={props.projectNames.get(props.bot.projectId) ?? props.bot.projectId}
                edit={params().get('edit')}
                onEdit={id => showTab('automations', id ? { edit: id } : {})}
                onHistory={id => showTab('runs', { automation: id })}
                onRun={reloadAll} />
            </Match>
            <Match when={tab() === 'tasks'}>
              <BotTasksTab pages={tasksPaged.pages} tasks={tasks()} projectNames={props.projectNames} loading={!tasksOp.data && !tasksOp.error} error={tasksOp.error} onRetry={tasksOp.reload} />
            </Match>
            <Match when={tab() === 'runs'}>
              <Show when={automationId()} keyed fallback={
                <BotRunsTab pages={runsPaged.pages} runs={runs()} projectNames={props.projectNames} loading={!runsOp.data && !runsOp.error} error={runsOp.error} onRetry={runsOp.reload} />
              }>{id => (
                <>
                  <div class="row" style={{ 'flex-wrap': 'wrap', gap: 'var(--space-2)' }}>
                    <Button label="Automations" icon="arrow-left" iconOnly onClick={() => showTab('automations')} />
                  </div>
                  <BotRunsTab pages={automationPaged.pages} runs={automationPaged.pages.rows} projectNames={props.projectNames}
                    loading={!automationRuns.data && !automationRuns.error} error={automationRuns.error} onRetry={automationRuns.reload} />
                </>
              )}</Show>
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
  onSelectTab: (tab: BotTab) => void;
  tabHref: (tab: BotTab) => string;
}) {
  const [, go] = useLocation();
  const activeOrWaitingTasks = createMemo(() => props.tasks.filter(t => classifyBotTaskBucket(t.state) !== 'history'));
  const failedOrUnconf = createMemo(() =>
    props.runs.filter(r => r.state === 'failed' || r.observation === 'unconfirmed' || !!r.problem),
  );

  return (
    <div class="stack">
      <div class="bot-hero">
        <Avatar letter={props.bot.settings.name} tone="success" size="xl" picture={botPicture(props.bot)} state={botAvatarState(props.bot)} />
        <div class="stack" style={{ gap: 'var(--space-1)', 'min-width': 0 }}>
          <span class="muted type-sm">{botAvatarWords[botAvatarState(props.bot)]}</span>
          <p style={{ margin: 0 }}>{props.bot.settings.purpose || 'No purpose set'}</p>
        </div>
      </div>
      <DetailSection title="Status">
        <KvRow icon="bot" label="Purpose" value={<span>{props.bot.settings.purpose || 'No purpose set'}</span>} />
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


      <Show when={failedOrUnconf().length > 0}>
        <DetailSection title="Run problems & unconfirmed wakes" count={failedOrUnconf().length} href={props.tabHref('runs')} onOpen={() => props.onSelectTab('runs')}>
          <For each={failedOrUnconf().slice(0, 3)}>
            {r => (
              <ProblemRow
                severity={r.state === 'failed' || r.problem ? 'failure' : 'warning'}
                title={runCause(r)?.text ?? (r.observation === 'unconfirmed' ? 'Run outcome unconfirmed' : `Run ${r.state}`)}
                code={runCause(r)?.long}
                facts={<>{wakeLabel(r.cause)} · <Ago t={ms(r.createdAt)!} /></>}
                actions={r.sessionId ? [{ label: 'Open conversation', onClick: () => go('/conversations/' + encodeURIComponent(r.sessionId!)) }] : []}
              />
            )}
          </For>
        </DetailSection>
      </Show>

      {/* Previews of the first rows; the title opens the tab, which loads the rest on scroll (OR-65). */}
      <DetailSection title="Active & waiting tasks" href={props.tabHref('tasks')} onOpen={() => props.onSelectTab('tasks')}>
        <Show when={activeOrWaitingTasks().length > 0} fallback={<Empty text="No active or waiting tasks." />}>
          <BotTasksTable tasks={activeOrWaitingTasks().slice(0, 8)} projectNames={props.projectNames} />
        </Show>
      </DetailSection>

      <DetailSection title="Recent runs" href={props.tabHref('runs')} onOpen={() => props.onSelectTab('runs')}>
        <Show when={props.runs.length > 0} fallback={<Empty text="No wake runs recorded yet." />}>
          <BotRunsTable runs={props.runs.slice(0, 8)} projectNames={props.projectNames} />
        </Show>
      </DetailSection>
    </div>
  );
}

function BotTasksTab(props: {
  pages: Pager; tasks: BotTask[]; projectNames: Map<string, string>; loading: boolean; error: string | null; onRetry: () => void;
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
        <ListEnd pages={props.pages} what="Older tasks" />
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
  pages: Pager; runs: BotRun[]; projectNames: Map<string, string>; loading: boolean; error: string | null; onRetry: () => void;
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
        <ListEnd pages={props.pages} what="Older runs" />
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
                <StateBadge family="botRun" state={asBotRunState(r.state)} unconfirmed={r.observation === 'unconfirmed'} reason={reasonLabel(r.reason)} />
                <Show when={r.quiet}><span class="badge t-neutral">Quiet</span></Show>
              </div>
            </td>
            <td>
              <div><strong>{wakeLabel(r.cause)}</strong></div>
              <Show when={r.automation}>{a => <div>{a().name} · revision {a().revision}</div>}</Show>
              <Show when={r.triggers.length > 0}>
                <div class="muted">{r.triggers.map(tr => tr.summary || tr.kind.replace(/_/g, ' ')).join(', ')}</div>
              </Show>
            </td>
            <td>
              <Show when={runCause(r)} keyed>{c => <div><span class="badge t-danger" data-tip={c.long}>{c.text}</span></div>}</Show>
              <Show when={!runCause(r) && r.reason}><span class="muted">{reasonLabel(r.reason)}</span></Show>
              <Show when={!runCause(r) && !r.reason}><span class="muted">{props.projectNames.get(r.projectId) ?? 'Project not in view'}</span></Show>
            </td>
            <td>
              <div class="row" style={{ gap: 'var(--space-1)', 'flex-wrap': 'wrap' }}>
                <Show when={r.executionId} keyed>{eid => <EntityCode kind="execution" id={eid} />}</Show>
                <Show when={r.sessionId} keyed>{sid => <EntityCode kind="conversation" id={sid} />}</Show>
                <For each={r.linkedTaskIds}>{tid => <EntityCode kind="task" id={tid} />}</For>
              </div>
            </td>
            <td class="muted">{[r.hostId, r.sentBy, sourceLabel(r.source)].filter(Boolean).join(' · ') || '—'}</td>
            <td><Ago t={ms(r.startedAt ?? r.createdAt)!} /></td>
          </tr>
        )}
      </For>
    </DataTable>
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
  const field = (key: keyof Settings, value: string) => setSettings(s => ({ ...s, [key]: value }));

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
        <Button label={props.bot ? 'Save settings' : 'Create bot'} kind="primary" busy={busy()} disabledReason={projectId() ? undefined : 'Select a project first'} onClick={() => void save('settings')} />
      </DetailSection>
      <Show when={props.bot}>{bot => <BotAvatarField bot={bot()} onSaved={props.onSaved} />}</Show>
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
