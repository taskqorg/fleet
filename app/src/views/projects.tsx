// Projects configuration, tabs (Work & Rules · Methods · Delivery · Results), OR-45 diff-from-default, and resource analytics (docs/design-system/pages/projects.md).
import { For, Match, Show, Switch, createMemo, createSignal, untrack } from 'solid-js';
import { useLocation } from '../router.ts';
import {
  AgeChip, AreaChart, Button, Choice, CostFigures, DataTable, DetailSection, Disclosure, Empty, EntityCode,
  ErrorBanner, FilterBar, Icon, KvRow, Latest, ListDetail, ListFrame, Num, RowFacts, SearchField, ShellActions, Skeleton, StateBadge, StateCounters, TabBar,
  Totals, VirtualList, bytes, catalogueRow, fileLabel, matchSearch, msMeasured, numMeasured, states as states_,
  type ChartBucket, type ChartSeries, type IconName, type Measured, type StateOf, type TotalItem,
} from '../../../packages/ui/src/index.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';
import { op, type Ops } from '../api.ts';
import { useOp } from '../store.ts';
import { SYSTEM_VIEWS } from '../system-views.ts';
import { idOf } from './delivery-model.tsx';
import { ProjectEditor, str, type ProjectDetailData } from './projects-editor.tsx';
import { RowSpend, useRowSpend } from './resources.tsx';

type AnalyticsReply = Ops['analytics_query']['reply'];
type AnalyticsDim = NonNullable<Ops['analytics_query']['request']['dimensions']>[number];
type AnalyticsFilter = NonNullable<Ops['analytics_query']['request']['filters']>;
type WindowKey = '24h' | '7d' | '30d';
type GrainKey = 'hour' | 'day' | 'week';
type ChartMetric = 'tokens' | 'executions' | 'cost';
type ProjectTab = 'work' | 'methods' | 'delivery' | 'results';

const ANALYTICS_MEASURES: NonNullable<Ops['analytics_query']['request']['measures']> = [
  'executions', 'failed', 'tokens_input', 'tokens_output', 'tokens_cache_read', 'tokens_cache_write',
  'tokens_reasoning', 'wall_ms', 'cpu_ms', 'peak_memory_bytes', 'calls', 'credits', 'cost',
];
const WINDOW_MS: Record<WindowKey, number> = { '24h': 86400e3, '7d': 7 * 86400e3, '30d': 30 * 86400e3 };
const DIMS: { id: AnalyticsDim; label: string }[] = [
  { id: 'model', label: 'Model' },
  { id: 'harness', label: 'Harness' },
  { id: 'pipeline_revision', label: 'Pipeline rev' },
  { id: 'project', label: 'Project' },
  { id: 'profile_id', label: 'Agent' },
  { id: 'host', label: 'Host' },
  { id: 'outcome', label: 'Outcome' },
  { id: 'state', label: 'State' },
];

const RUNNING_TASK_STATES = SYSTEM_VIEWS.tasks.find(v => v.name === 'Running')?.view.state ?? ['in_progress', 'waiting'];
const ACTIVE_EXEC_STATES = new Set(['preparing', 'running', 'waiting']);

function isRec(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v);
}

function parseBucketMs(key: string, grain: GrainKey): [number, number] | null {
  const start = Date.parse(key.includes('T') ? key : `${key}T00:00:00Z`);
  if (!Number.isFinite(start)) return null;
  const span = grain === 'hour' ? 3600e3 : grain === 'week' ? 7 * 86400e3 : 86400e3;
  return [start, start + span];
}

function shortBucketLabel(key: string, grain: GrainKey): string {
  if (grain === 'hour') return key.slice(11, 16) || key;
  return key.length >= 10 ? key.slice(5, 10) : key;
}

function sumTokens(m: AnalyticsReply['rows'][number]['measures']): Measured {
  const req = [m.tokens_input, m.tokens_output, m.tokens_cache_read, m.tokens_cache_write].filter((p): p is NonNullable<typeof p> => !!p);
  const all = m.tokens_reasoning ? [...req, m.tokens_reasoning] : req;
  if (!all.length) return { value: null, basis: 'unavailable' };
  let sum = 0, known = 0, sub = false, est = false, partial: string | undefined;
  for (const p of all) {
    if (p.value != null) {
      sum += p.value;
      known++;
      if (p.basis === 'subscription') sub = true;
      else if (p.basis === 'estimated') est = true;
      if (p.partial && !partial) partial = p.partial;
    } else if (p !== m.tokens_reasoning && p.basis === 'unavailable' && !partial) {
      partial = 'incomplete telemetry';
    }
  }
  if (known === 0) return { value: null, basis: 'unavailable' };
  const basis: Measured['basis'] = est ? 'estimated' : sub ? 'subscription' : 'measured';
  return { value: sum, basis, ...(partial ? { partial } : {}) };
}

function projectStateBadge(p: Record<string, unknown>) {
  const sourceState = str(p.sourceState);
  const settings = isRec(p.settings) ? p.settings : null;
  const envs = Array.isArray(settings?.environments) ? settings.environments : [];
  if (sourceState === 'repository-deleted') return <StateBadge family="source" state="missing" reason="Repository deleted" compact />;
  if (sourceState === 'repository-changed') return <StateBadge family="source" state="changed" reason="Repository changed" compact />;
  if (sourceState === 'ahead') return <StateBadge family="definition" state="draft" reason="Local changes ahead" compact />;
  if (envs.length > 0) return <StateBadge family="definition" state="ready" compact />;
  return <StateBadge family="definition" state="unchecked" reason="No environment configured" compact />;
}

/** Projects catalogue screen with resource analytics overview and tabbed project detail. */
export function Projects(props: { id?: string }) {
  const [, go] = useLocation();
  const [creating, setCreating] = createSignal(false);
  const [query, setQuery] = createSignal('');
  const list = useOp('project_list', {});
  const allProjects = () => list.data?.projects ?? [];
  const rows = createMemo(() =>
    allProjects().map(p => ({ id: str(p.id), name: str(p.name), raw: p })).filter(p => matchSearch(query(), `${p.name} ${p.id}`, { project: p.id })),
  );
  const spend = useRowSpend('projects', rows);
  const selectedName = createMemo(() => rows().find(r => r.id === props.id)?.name || str(allProjects().find(p => str(p.id) === props.id)?.name));
  const pane = (
    <>
      <ShellActions>
        <Button label="Bots" icon="bot" iconOnly onClick={() => go('/bots')} />
        <Button label="Create project" icon="plus" iconOnly kind="primary" pressed={creating()} onClick={() => setCreating(true)} />
      </ShellActions>
      <Show when={list.error}>{err => <ErrorBanner text={`Projects did not load: ${err()}`} onRetry={list.reload} />}</Show>
      <section class="panel">
        <ListFrame
          pending={!list.data && !list.error}
          bar={<FilterBar query={query()} onQuery={setQuery} placeholder="Find project" shown={rows().length} total={allProjects().length} />}
        >
          <Switch
            fallback={
              <VirtualList
                fill
                items={rows()}
                getKey={p => p.id}
                rowHeight={catalogueRow()}
                follow="top"
                label="Projects"
                columns="1.25rem minmax(0,1fr) auto"
                rowClass={p => (!creating() && p.id === props.id ? 'sel' : '')}
                render={p => (
                  <>
                    <span class="lead">{projectStateBadge(p.raw)}</span>
                    <span class="tt">
                      <a
                        class="title row-link"
                        href={'/projects/' + encodeURIComponent(p.id)}
                        onClick={e => { e.preventDefault(); setCreating(false); go('/projects/' + encodeURIComponent(p.id)); }}
                      >{p.name || p.id}</a>
                      <Show when={p.name && p.name.toLowerCase() !== p.id.toLowerCase()}><span class="id">{p.id}</span></Show>
                    </span>
                    <span class="rz">
                      <RowFacts>
                        <RowSpend
                          cost={spend.cost.get(p.id)}
                          stale={!!spend.error}
                          name={p.name || p.id}
                          href={'/projects/' + encodeURIComponent(p.id)}
                          open={() => { setCreating(false); go('/projects/' + encodeURIComponent(p.id)); }}
                        />
                      </RowFacts>
                    </span>
                  </>
                )}
              />
            }
          >
            <Match when={!list.data && !list.error}><Skeleton rows={6} /></Match>
            <Match when={rows().length === 0}><Empty text={query() ? 'No projects match.' : 'No projects yet.'} /></Match>
          </Switch>
        </ListFrame>
      </section>
    </>
  );
  const overview = (
    <article class="detail" data-ui="detail" tabindex={-1} aria-label="Projects analytics">
      <header class="detail-h">
        <h2 class="detail-title">Resource analytics</h2>
      </header>
      <ProjectAnalytics onOpenProject={pid => go('/projects/' + encodeURIComponent(pid))} />
    </article>
  );
  return (
    <ListDetail
      list={pane}
      detail={
        creating() ? (
          <ProjectEditor
            id={undefined}
            onClose={() => setCreating(false)}
            onSaved={p => { setCreating(false); go('/projects/' + encodeURIComponent(p)); }}
            renderAnalytics={pid => <ProjectAnalytics projectId={pid} onOpenProject={target => go('/projects/' + encodeURIComponent(target))} />}
          />
        ) : props.id ? (
          <Show when={props.id} keyed>
            {id => (
              <ProjectDetail
                id={id}
                name={selectedName()}
                onClose={() => go('/projects')}
                onSaved={p => go('/projects/' + encodeURIComponent(p))}
              />
            )}
          </Show>
        ) : undefined
      }
      idle={overview}
      detailKey={creating() ? 'create' : props.id}
    />
  );
}

/** Tabbed project detail: Work & Rules, Methods, Delivery, and Results (docs/designs/fleet-ia-and-journeys.md). */
function ProjectDetail(props: { id: string; name?: string; onClose: () => void; onSaved: (id: string) => void }) {
  const [, go] = useLocation();
  const [tab, setTab] = createSignal<ProjectTab>('work');
  const tabs: { id: ProjectTab; label: string }[] = [
    { id: 'work', label: 'Work & Rules' },
    { id: 'methods', label: 'Methods' },
    { id: 'delivery', label: 'Publishing' },
    { id: 'results', label: 'Results' },
  ];

  // Outer CSS grid keeps TabBar fixed at the top while the active .detail remains the column's sole vertical scroller (OR-25).
  return (
    <div style={{ display: 'grid', 'grid-template-rows': 'auto minmax(0, 1fr)', 'min-width': 0, 'min-height': 0, background: 'var(--surface)' }}>
      <div style={{ 'padding-inline': 'clamp(var(--space-3), calc(8% - var(--space-7)), var(--space-6))', 'padding-top': 'var(--space-3)' }}>
        <TabBar
          cls="detail-tabs"
          label="Project sections"
          tabs={tabs}
          value={tab()}
          onChange={setTab}
        />
      </div>
      <Show
        when={tab() === 'work'}
        fallback={
          <article class="detail" data-ui="detail" tabindex={-1} aria-label={`${props.id} ${tab()}`}>
            <header class="detail-h">
              <Button label="Close" icon="x" iconOnly onClick={props.onClose} />
              <h2 class="detail-title">{props.name || props.id}</h2>
            </header>
            <div role="tabpanel" aria-label={tabs.find(t => t.id === tab())?.label ?? tab()} class="stack">
              <Switch>
                <Match when={tab() === 'methods'}><ProjectMethodsTab projectId={props.id} /></Match>
                <Match when={tab() === 'delivery'}><ProjectDeliveryTab projectId={props.id} /></Match>
                <Match when={tab() === 'results'}><ProjectResultsTab projectId={props.id} /></Match>
              </Switch>
            </div>
          </article>
        }
      >
        <div role="tabpanel" aria-label="Work & Rules" style={{ display: 'grid', 'grid-template-rows': 'minmax(0, 1fr)', 'min-width': 0, 'min-height': 0 }}>
          <ProjectEditor
            id={props.id}
            onClose={props.onClose}
            onSaved={props.onSaved}
            renderWork={(pid, detail) => (
              <>
                <ProjectWorkSection projectId={pid} />
                <ProjectSettingsDiff reply={detail} />
              </>
            )}
            renderAnalytics={pid => (
              <ProjectAnalytics projectId={pid} onOpenProject={target => go('/projects/' + encodeURIComponent(target))} />
            )}
          />
        </div>
      </Show>
    </div>
  );
}

/* ---------- Work & Rules: active work overview + OR-45 diff from default ---------- */

function ProjectWorkSection(props: { projectId: string }) {
  const [, go] = useLocation();
  const tasksDoc = useOp('task_page', () => ({
    filter: { projectId: [props.projectId], state: RUNNING_TASK_STATES },
    group: 'none',
    sort: '-changed',
    limit: 6,
  }));
  const execsDoc = useOp('executions_list', () => ({ projectId: props.projectId, limit: 6 }));
  const tasksHref = () => formatView({ view: 'tasks', project: [props.projectId] });
  const tasks = () => tasksDoc.data?.tasks ?? [];
  const counts = () => tasksDoc.data?.counts ?? {};
  const hasCounts = createMemo(() => Object.values(counts()).some(n => n > 0));
  const activeExecs = createMemo(() => (execsDoc.data?.executions ?? []).filter(e => ACTIVE_EXEC_STATES.has(e.state)));

  return (
    <DetailSection title="Active work">
      <div class="res-bar">
        <Show when={hasCounts()}>
          <StateCounters
            family="task"
            label="Project tasks by state"
            counts={counts() as Partial<Record<StateOf<'task'>, number>>}
            active={[]}
            hideZero
            onToggle={s => go(formatView({ view: 'tasks', project: [props.projectId], state: [String(s)] }))}
          />
        </Show>
        <span class="grow" />
        <a class="row-link" href={tasksHref()} onClick={e => { e.preventDefault(); go(tasksHref()); }}>Open in Tasks</a>
      </div>
      <Show when={tasksDoc.error}><ErrorBanner text={`Tasks did not load: ${tasksDoc.error}`} onRetry={tasksDoc.reload} /></Show>
      <Show when={!tasksDoc.data && !tasksDoc.error}><Empty text="Loading active tasks…" /></Show>
      <Show when={tasksDoc.data && tasks().length === 0 && activeExecs().length === 0}>
        <Empty text="No active tasks or executions for this project." />
      </Show>
      <For each={tasks()}>
        {t => {
          const rawState = t.pauseReason === 'held' ? 'held' : t.state;
          const taskState = (rawState in states_.task ? rawState : 'queued') as StateOf<'task'>;
          return (
            <KvRow
              icon={states_.task[taskState].icon}
              label={t.title}
              meta={
                <span class="deps">
                  <StateBadge family="task" state={taskState} reason={t.waitReason ?? undefined} compact />
                  <EntityCode code={t.code} id={t.id} kind="task" />
                </span>
              }
              value={t.stateAt ? <AgeChip t={Date.parse(t.stateAt)} /> : undefined}
            />
          );
        }}
      </For>
      <For each={activeExecs()}>
        {ex => (
          <KvRow
            icon="activity"
            label={ex.kind}
            meta={
              <span class="deps">
                <StateBadge family="execution" state={ex.state} compact />
                <EntityCode id={ex.id} kind="execution" />
                <Show when={ex.harness}><span class="mono">{ex.harness}</span></Show>
                <Show when={ex.hostId}><span class="mono">{ex.hostId}</span></Show>
                <Show when={ex.taskId}><EntityCode id={ex.taskId!} kind="task" /></Show>
              </span>
            }
            value={ex.startedAt ? <AgeChip t={Date.parse(ex.startedAt)} /> : undefined}
          />
        )}
      </For>
    </DetailSection>
  );
}

type DiffField = {
  key: string;
  icon: IconName;
  label: string;
  baseText: string;
  currentText: string;
  isCode?: boolean;
  diff: boolean;
};

function stableVal(v: unknown): string {
  if (v === undefined) return 'null';
  if (Array.isArray(v)) return JSON.stringify(v.map(x => (isRec(x) ? Object.fromEntries(Object.entries(x).sort(([a], [b]) => a.localeCompare(b))) : x)));
  if (isRec(v)) return JSON.stringify(Object.fromEntries(Object.entries(v).sort(([a], [b]) => a.localeCompare(b))));
  return JSON.stringify(v);
}

function fmtVal(v: unknown, emptyLabel = 'none'): string {
  if (v === null || v === undefined || v === '') return emptyLabel;
  if (typeof v === 'boolean' || typeof v === 'number') return String(v);
  if (typeof v === 'string') return v;
  if (Array.isArray(v)) {
    if (v.length === 0) return emptyLabel;
    if (v.every(x => typeof x === 'string')) return (v as string[]).join(', ');
    return `${v.length} configured`;
  }
  if (isRec(v)) {
    const entries = Object.entries(v);
    if (entries.length === 0) return emptyLabel;
    return entries.map(([k, val]) => `${k}=${typeof val === 'string' ? val : JSON.stringify(val)}`).join(', ');
  }
  return String(v);
}

function ProjectSettingsDiff(props: { reply: ProjectDetailData | null }) {
  const computed = createMemo(() => {
    const reply = props.reply;
    if (!reply) return { diffs: [] as DiffField[], inherited: [] as DiffField[], unknownKeys: [] as string[] };
    const resolved = isRec(reply.settings) ? reply.settings : {};
    const authored = isRec(reply.content?.settings) ? reply.content.settings : {};
    const get = (k: string, sub?: string): unknown => {
      const top = k in resolved ? resolved[k] : authored[k];
      if (!sub) return top;
      return isRec(top) ? top[sub] : undefined;
    };
    const rows: DiffField[] = [];
    const check = (key: string, icon: IconName, label: string, def: unknown, curRaw: unknown, opts: { empty?: string; isCode?: boolean } = {}) => {
      const cur = curRaw === undefined ? def : curRaw;
      const isCode = opts.isCode ?? (typeof def === 'string' || typeof def === 'boolean' || typeof def === 'number');
      rows.push({
        key,
        icon,
        label,
        baseText: fmtVal(def, opts.empty ?? 'none'),
        currentText: fmtVal(cur, opts.empty ?? 'none'),
        isCode,
        diff: stableVal(cur) !== stableVal(def),
      });
    };

    // Defaults match go/coordinator/projects.go settingsDefaults.
    check('worktree.envMode', 'git-pull-request', 'Worktree mode', 'worktree', get('worktree', 'envMode'));
    check('worktree.branchPrefix', 'git-pull-request', 'Branch prefix', 'fleet', get('worktree', 'branchPrefix'));
    check('worktree.baseRef', 'git-pull-request', 'Base ref', null, get('worktree', 'baseRef'), { isCode: true });
    check('worktree.startFromOrigin', 'git-pull-request', 'Start from origin', true, get('worktree', 'startFromOrigin'));
    check('worktree.linkPaths', 'link', 'Link paths', [], get('worktree', 'linkPaths'));
    check('worktree.sharedDirectories', 'folder-open', 'Shared directories', [], get('worktree', 'sharedDirectories'));
    check('scripts.setup', 'terminal', 'Setup script', null, get('scripts', 'setup'), { isCode: true });
    check('scripts.teardown', 'terminal', 'Teardown script', null, get('scripts', 'teardown'), { isCode: true });
    check('setupStart', 'play', 'Setup start', 'wait', get('setupStart'));
    check('integration.mode', 'git-merge', 'Integration mode', 'pr', get('integration', 'mode'));
    check('integration.mergeMethod', 'git-merge', 'Merge method', 'squash', get('integration', 'mergeMethod'));
    check('integration.autoMerge', 'git-merge', 'Auto-merge', true, get('integration', 'autoMerge'));
    check('integration.syncCheckoutAfterMerge', 'rotate-cw', 'Sync checkout after merge', true, get('integration', 'syncCheckoutAfterMerge'));
    check('cleanup.keepWhen', 'archive', 'Keep worktree when', ['uncommitted', 'unpushed', 'teardown-failed'], get('cleanup', 'keepWhen'));
    check('identity', 'bot', 'Git identity', null, get('identity'));
    check('subscriptions', 'bell', 'Subscriptions', [], get('subscriptions'));
    check('execution.tools', 'settings', 'Execution tools', [], get('execution', 'tools'));
    check('execution.forge', 'git-pull-request', 'Execution forge', false, get('execution', 'forge'));
    check('execution.profiles', 'bot', 'Execution profiles', [], get('execution', 'profiles'));
    check('errors.bugsink', 'circle-alert', 'Bugsink projects', [], get('errors', 'bugsink'));
    check('presets', 'messages-square', 'Conversation presets', [], get('presets'), { empty: 'system defaults' });
    check('contextSources', 'file-text', 'Context sources', [], get('contextSources'));
    check('artifact_locations', 'save', 'Artifact locations', null, get('artifact_locations'));
    check('artifact_rules', 'archive', 'Artifact rules', [], get('artifact_rules'));
    check('roles', 'bot', 'Agent roles', {}, get('roles'));
    check('repos', 'git-pull-request', 'Repositories', [], get('repos'));
    check('environments', 'panels-top-left', 'Environments', [], get('environments'));
    check('capability', 'rows-3', 'Capability', null, authored.capability ?? null);
    check('recommendedMethod', 'circle-check', 'Recommended method', null, authored.recommendedMethod ?? null, { empty: 'shared default' });

    return {
      diffs: rows.filter(r => r.diff),
      inherited: rows.filter(r => !r.diff),
      unknownKeys: Array.isArray(reply.unknownKeys) ? reply.unknownKeys : [],
    };
  });

  return (
    <Show when={props.reply}>
      <DetailSection title="Settings from defaults">
        <Show
          when={computed().diffs.length > 0}
          fallback={<KvRow icon="circle-check" label="Matches base defaults" meta="All settings match shared project defaults" />}
        >
          <For each={computed().diffs}>
            {f => (
              <KvRow
                icon={f.icon}
                label={f.label}
                meta={
                  <span data-tip={`Default: ${f.baseText}`}>
                    {f.isCode ? <code>{f.currentText}</code> : f.currentText}
                  </span>
                }
              />
            )}
          </For>
        </Show>
        <Show when={computed().unknownKeys.length > 0}>
          <KvRow icon="triangle-alert" label="Unknown keys" meta={computed().unknownKeys.join(', ')} />
        </Show>
        <Show when={computed().inherited.length > 0}>
          <Disclosure summary={`Inherited defaults (${computed().inherited.length})`} summaryClass="muted">
            <For each={computed().inherited}>
              {f => (
                <KvRow
                  icon={f.icon}
                  label={f.label}
                  meta={f.isCode ? <code>{f.currentText}</code> : f.currentText}
                />
              )}
            </For>
          </Disclosure>
        </Show>
      </DetailSection>
    </Show>
  );
}

/* ---------- Methods tab ---------- */

function methodStatusTone(s: string | undefined): 'success' | 'attention' | 'neutral' {
  return s === 'recommended' ? 'success' : s === 'experimental' ? 'attention' : 'neutral';
}

function methodStatusIcon(s: string | undefined): IconName {
  return s === 'recommended' ? 'circle-check' : s === 'experimental' ? 'activity' : 'circle-slash';
}

function ProjectMethodsTab(props: { projectId: string }) {
  const [, go] = useLocation();
  const [search, setSearch] = createSignal('');
  const [busy, setBusy] = createSignal<string | null>(null);
  const [actionError, setActionError] = createSignal<string | null>(null);
  const cat = useOp('catalogue', () => ({ projectId: props.projectId }));
  const c = () => cat.data;

  const recommend = async (methodId: string) => {
    setBusy(methodId);
    setActionError(null);
    try {
      await op('recommendation_set', { projectId: props.projectId, methodId });
      cat.reload();
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  const recommendedMethod = () => {
    const data = c();
    return data && typeof data.recommendedMethod === 'string' ? data.recommendedMethod : null;
  };
  const sharedDefault = () => {
    const data = c();
    return data && typeof data.sharedDefault === 'string' ? data.sharedDefault : null;
  };
  const source = () => {
    const data = c();
    return data && typeof data.recommendationSource === 'string' ? data.recommendationSource : 'shared';
  };
  const capability = () => {
    const data = c();
    return data && typeof data.capability === 'string' ? data.capability : null;
  };
  const filtered = createMemo(() => {
    const data = c();
    if (!data) return [];
    const q = search();
    return data.methods.filter(m =>
      matchSearch(q, `${m.name} ${m.id} ${m.capability ?? ''} ${m.description ?? ''} ${m.status ?? ''} ${m.pipeline}`),
    );
  });

  return (
    <Switch
      fallback={
        <>
          <div class="res-bar">
            <SearchField label="Find method" value={search()} onInput={setSearch} onSearch={() => {}} />
            <span class="grow" />
            <a class="row-link" href="/catalogue" onClick={e => { e.preventDefault(); go('/catalogue'); }}>Open in Library</a>
            <a class="row-link" href="/lab" onClick={e => { e.preventDefault(); go('/lab'); }}>Open in Lab</a>
          </div>
          <Show when={actionError()}>{err => <ErrorBanner text={err()} />}</Show>
          <DetailSection title="Current recommendation">
            <KvRow
              icon="circle-check"
              label={recommendedMethod() ?? 'None'}
              meta={
                <span data-tip={sharedDefault() ? `Shared default: ${sharedDefault()}` : undefined}>
                  source: <code>{source()}</code>
                  <Show when={capability()}>{cap => <> · capability: <code>{cap()}</code></>}</Show>
                </span>
              }
            />
          </DetailSection>
          <DetailSection title={`Methods (${filtered().length})`}>
            <Show when={filtered().length > 0} fallback={<Empty text="No methods match." />}>
              <For each={filtered()}>
                {m => {
                  const isCurrent = () => m.id === recommendedMethod();
                  const a = m.assessment;
                  return (
                    <div class="stack">
                      <KvRow
                        icon={isCurrent() ? 'circle-check' : 'network'}
                        label={m.name}
                        meta={
                          <span class="deps">
                            <code>{m.id}</code>
                            <span>{m.scope}</span>
                            <a class="row-link" href={'/pipelines/' + encodeURIComponent(m.pipeline)} onClick={e => { e.preventDefault(); go('/pipelines/' + encodeURIComponent(m.pipeline)); }}>{m.pipeline}</a>
                            <Show when={m.status}>
                              <span class={`badge t-${methodStatusTone(m.status)}`}>
                                <Icon name={methodStatusIcon(m.status)} /><span>{m.status}</span>
                              </span>
                            </Show>
                          </span>
                        }
                        value={
                          <span class="deps">
                            <Show when={isCurrent()}><span class="badge t-success">Recommended</span></Show>
                            <Button
                              label={isCurrent() ? 'Selected' : 'Recommend'}
                              icon="check"
                              kind={isCurrent() ? 'quiet' : 'default'}
                              busy={busy() === m.id}
                              disabledReason={isCurrent() ? 'Already recommended for this project' : undefined}
                              onClick={() => void recommend(m.id)}
                            />
                          </span>
                        }
                      />
                      <Show when={m.description}><p class="muted">{m.description}</p></Show>
                      <KvRow
                        icon="chart-no-axes-column"
                        label="Assessment"
                        meta={
                          <span class="deps">
                            <span>{a.basis}</span>
                            <Show when={a.score != null}><Num m={{ value: a.score!, basis: a.basis === 'none' ? 'unavailable' : 'measured' }} kind="score" /></Show>
                            <Show when={a.wallMsMedian != null}><Num m={msMeasured(a.wallMsMedian!, 'measured')} kind="duration" /></Show>
                            <Show when={a.costUsd != null}><Num m={{ value: a.costUsd!, basis: a.costBasis === 'subscription' ? 'subscription' : 'measured', currency: 'USD' }} kind="money" /></Show>
                            <Show when={a.examples != null}><span>{a.examples} {a.examples === 1 ? 'example' : 'examples'}</span></Show>
                          </span>
                        }
                        value={a.experimentId ? (
                          <a class="row-link" href={'/lab/' + encodeURIComponent(a.experimentId)} onClick={e => { e.preventDefault(); go('/lab/' + encodeURIComponent(a.experimentId!)); }}>Experiment</a>
                        ) : undefined}
                      />
                    </div>
                  );
                }}
              </For>
            </Show>
          </DetailSection>
        </>
      }
    >
      <Match when={cat.status === 404}><Empty text="No method catalogue is configured." /></Match>
      <Match when={cat.error && !c()}><ErrorBanner text={`Methods did not load: ${cat.error}`} onRetry={cat.reload} /></Match>
      <Match when={!c()}><Skeleton rows={5} /></Match>
    </Switch>
  );
}

/* ---------- Delivery tab ---------- */

function ProjectDeliveryTab(props: { projectId: string }) {
  const [, go] = useLocation();
  const [search, setSearch] = createSignal('');
  const [stageFilter, setStageFilter] = createSignal<StateOf<'delivery'>[]>([]);
  const status = useOp('publishing_status', () => ({ projectIds: [props.projectId] }), { every: 300000 });
  const deliveryHref = () => formatView({ view: 'delivery', q: `project:${props.projectId}` });
  const items = createMemo(() => (status.data?.items ?? []).filter(it => it.stage in states_.delivery));

  const searched = createMemo(() => {
    const q = search();
    return items().filter(it =>
      matchSearch(q, `${it.title} #${it.number} ${it.repo} ${it.release ?? ''}`, { state: it.stage, project: it.projectId }),
    );
  });
  const counts = createMemo(() => {
    const c: Partial<Record<StateOf<'delivery'>, number>> = {};
    for (const it of searched()) c[it.stage] = (c[it.stage] ?? 0) + 1;
    return c;
  });
  const shown = createMemo(() => {
    const sf = stageFilter();
    return sf.length ? searched().filter(it => sf.includes(it.stage)) : searched();
  });

  return (
    <>
      <div class="res-bar">
        <SearchField label="Find pull requests" value={search()} onInput={setSearch} onSearch={() => {}} />
        <Show when={status.data?.refreshedAt}>
          {refAt => <AgeChip t={Date.parse(refAt())} alertAfter={900} alertLabel="GitHub read is stale" />}
        </Show>
        <span class="grow" />
        <a class="row-link" href={deliveryHref()} onClick={e => { e.preventDefault(); go(deliveryHref()); }}>Open in Publishing</a>
      </div>
      <Show when={items().length > 0}>
        <StateCounters
          family="delivery"
          label="Project pull requests by stage"
          counts={counts()}
          active={stageFilter()}
          hideZero
          onAll={() => setStageFilter([])}
          onToggle={s => setStageFilter(cur => (cur.includes(s) ? cur.filter(x => x !== s) : [...cur, s]))}
        />
      </Show>
      <Show when={status.error && !status.data}><ErrorBanner text={`Delivery did not load: ${status.error}`} onRetry={status.reload} /></Show>
      <Show when={status.data?.error}>{err => <ErrorBanner text={`GitHub: ${err()}`} onRetry={status.reload} />}</Show>
      <Show when={status.data || status.error} fallback={<Skeleton rows={5} />}>
        <Show when={status.data}>
          <DetailSection title={`Pull requests (${shown().length})`}>
            <Show
              when={shown().length > 0}
              fallback={<Empty text={items().length ? 'No pull requests match.' : 'No pull requests in flight for this project.'} />}
            >
              <DataTable
                label="Project pull requests"
                head={
                  <tr>
                    <th scope="col">Stage</th>
                    <th scope="col">PR</th>
                    <th scope="col">Title</th>
                    <th scope="col">Repository</th>
                    <th scope="col" class="num">In stage</th>
                  </tr>
                }
              >
                <For each={shown()}>
                  {it => {
                    const prId = idOf(it);
                    return (
                      <tr>
                        <td><StateBadge family="delivery" state={it.stage} /></td>
                        <td><EntityCode code={`#${it.number}`} id={prId} kind="pull_request" /></td>
                        <th scope="row">
                          <a class="row-link" href={'/delivery/' + encodeURIComponent(prId)} onClick={e => { e.preventDefault(); go('/delivery/' + encodeURIComponent(prId)); }}>
                            {it.title || `#${it.number}`}
                          </a>
                          <Show when={it.release}><> <EntityCode code={it.release!} id={it.release!} kind="release" /></></Show>
                        </th>
                        <td><code>{it.repo}</code></td>
                        <td class="num"><AgeChip t={Date.parse(it.since)} /></td>
                      </tr>
                    );
                  }}
                </For>
              </DataTable>
            </Show>
          </DetailSection>
        </Show>
      </Show>
    </>
  );
}

/* ---------- Results tab ---------- */

function ProjectResultsTab(props: { projectId: string }) {
  const [, go] = useLocation();
  const [search, setSearch] = createSignal('');
  const [stateFilter, setStateFilter] = createSignal<StateOf<'artifact'>[]>([]);
  const read = useOp('artifact_list', () => ({ projectIds: [props.projectId], limit: 100 }));
  const resultsHref = () => formatView({ view: 'results', project: [props.projectId] });
  const artifacts = () => read.data?.artifacts ?? [];
  const nextBefore = () => read.data?.nextBefore ?? null;

  const searched = createMemo(() => {
    const q = search();
    return artifacts().filter(a =>
      matchSearch(q, `${fileLabel(a.name) || a.name} ${a.mediaType} ${a.taskCode ?? ''}`, { state: a.state, project: a.projectId ?? undefined }),
    );
  });
  const counts = createMemo(() => {
    const c: Partial<Record<StateOf<'artifact'>, number>> = {};
    for (const a of searched()) {
      const s = a.state as StateOf<'artifact'>;
      c[s] = (c[s] ?? 0) + 1;
    }
    return c;
  });
  const shown = createMemo(() => {
    const sf = stateFilter();
    return sf.length ? searched().filter(a => sf.includes(a.state as StateOf<'artifact'>)) : searched();
  });

  return (
    <>
      <div class="res-bar">
        <SearchField label="Find results" value={search()} onInput={setSearch} onSearch={() => {}} />
        <span class="grow" />
        <a class="row-link" href={resultsHref()} onClick={e => { e.preventDefault(); go(resultsHref()); }}>Open in Results</a>
      </div>
      <Show when={artifacts().length > 0}>
        <StateCounters
          family="artifact"
          label="Project results by state"
          counts={counts()}
          active={stateFilter()}
          hideZero
          onAll={() => setStateFilter([])}
          onToggle={s => setStateFilter(cur => (cur.includes(s) ? cur.filter(x => x !== s) : [...cur, s]))}
        />
      </Show>
      <Show when={read.error && !read.data}><ErrorBanner text={`Results did not load: ${read.error}`} onRetry={read.reload} /></Show>
      <Show when={read.data || read.error} fallback={<Skeleton rows={5} />}>
        <Show when={read.data}>
          <DetailSection title={`Results (${shown().length}${nextBefore() != null ? '+' : ''})`}>
            <Show
              when={shown().length > 0}
              fallback={<Empty text={artifacts().length ? 'No results match.' : 'No results stored for this project.'} />}
            >
              <Latest
                n={25}
                items={shown().map(a => {
                  const artState = (a.state in states_.artifact ? a.state : 'stored') as StateOf<'artifact'>;
                  return (
                    <KvRow
                      icon={states_.artifact[artState].icon}
                      label={fileLabel(a.name) || a.name}
                      meta={
                        <span class="deps">
                          <StateBadge family="artifact" state={artState} compact />
                          <EntityCode id={a.id} kind="result" />
                          <Show when={a.taskId && a.taskCode}><EntityCode code={a.taskCode!} id={a.taskId!} kind="task" /></Show>
                        </span>
                      }
                      value={
                        <span class="deps">
                          <span class="muted">{bytes(a.size).text}</span>
                          <AgeChip t={a.createdAt} />
                        </span>
                      }
                    />
                  );
                })}
              />
            </Show>
            <Show when={nextBefore() != null}>
              <p class="muted">
                More results exist for this project. <a class="row-link" href={resultsHref()} onClick={e => { e.preventDefault(); go(resultsHref()); }}>View all in Results</a>
              </p>
            </Show>
          </DetailSection>
        </Show>
      </Show>
    </>
  );
}

function ProjectAnalytics(props: { projectId?: string; onOpenProject: (id: string) => void }) {
  const [, go] = useLocation();
  const initialSearch = typeof location !== 'undefined' ? new URLSearchParams(location.search) : null;
  const initWin = (() => {
    const w = initialSearch?.get('window');
    return w === '24h' || w === '7d' || w === '30d' ? w : '7d';
  })();
  const initGrain = (() => {
    const g = initialSearch?.get('grain');
    return g === 'hour' || g === 'day' || g === 'week' ? g : 'day';
  })();
  const initDim = (() => {
    const d = initialSearch?.get('dim') as AnalyticsDim | null;
    if (d && DIMS.some(x => x.id === d)) return d;
    return untrack(() => props.projectId) ? 'model' : 'project';
  })();
  const initFilters = (() => {
    const out: AnalyticsFilter = {};
    const m = initialSearch?.get('model'); if (m) out.model = [m];
    const h = initialSearch?.get('harness'); if (h) out.harness = [h];
    const p = initialSearch?.get('pipeline'); if (p) out.pipeline = [p];
    const pr = initialSearch?.get('profile_id'); if (pr) out.profile_id = [pr];
    const pv = initialSearch?.get('pipeline_revision'); if (pv) out.pipeline_revision = [pv];
    return out;
  })();

  const [win, setWin] = createSignal<WindowKey>(initWin);
  const [grain, setGrain] = createSignal<GrainKey>(initGrain);
  const [metric, setMetric] = createSignal<ChartMetric>('tokens');
  const [dim, setDim] = createSignal<AnalyticsDim>(initDim);
  const [bucket, setBucket] = createSignal<string | null>(initialSearch?.get('bucket') || null);
  const [filters, setFilters] = createSignal<AnalyticsFilter>(initFilters);
  const [costCur, setCostCur] = createSignal<string>('USD');
  const [rev, setRev] = createSignal(0);

  const now = createMemo(() => {
    void props.projectId; void win(); void grain(); void rev();
    return Date.now();
  });
  const from = () => now() - WINDOW_MS[win()];

  const dimsAvail = createMemo(() => (props.projectId ? DIMS.filter(d => d.id !== 'project') : DIMS));
  const activeDim = createMemo(() => (dimsAvail().some(d => d.id === dim()) ? dim() : dimsAvail()[0].id));

  // Two reads of the same operation: the trend over the window by time bucket, and the breakdown of the picked bucket
  // (or the window) by the chosen dimension. A retry moves the window to now (rev), which names new reads.
  const trendDoc = useOp('analytics_query', () => ({
    window: { from: from(), to: now() },
    dimensions: [grain()],
    measures: ANALYTICS_MEASURES,
    filters: filters(),
    ...(props.projectId ? { projectId: props.projectId } : {}),
  }));
  const range = createMemo(() => (bucket() ? parseBucketMs(bucket()!, grain()) : null));
  const breakDoc = useOp('analytics_query', () => {
    const r = range(), n = now();
    return {
      window: r ? { from: r[0], to: Math.max(r[0] + 1, Math.min(r[1], n)) } : { from: from(), to: n },
      dimensions: [activeDim()],
      measures: ANALYTICS_MEASURES,
      filters: filters(),
      ...(props.projectId ? { projectId: props.projectId } : {}),
    };
  });

  const changeWin = (w: WindowKey) => {
    setWin(w);
    setBucket(null);
    if (w === '24h' && untrack(grain) === 'week') setGrain('hour');
    if (w !== '24h' && untrack(grain) === 'hour') setGrain('day');
  };

  const trend = () => trendDoc.data;
  const breakdown = () => breakDoc.data;
  const allTrendRows = () => trend()?.rows ?? [];
  const unkBucketRuns = createMemo(() => allTrendRows().find(r => r.dimensions[grain()] === 'unknown')?.measures.executions?.value ?? 0);
  const trendRows = createMemo(() => {
    const g = grain();
    return allTrendRows()
      .filter(r => parseBucketMs(r.dimensions[g] ?? '', g) != null)
      .sort((a, b) => (a.dimensions[g] ?? '').localeCompare(b.dimensions[g] ?? ''));
  });
  const totalItems = createMemo<TotalItem[]>(() => {
    const tr = trend();
    if (!tr) return [];
    const totalM = tr.totals.measures;
    const execM = numMeasured(totalM?.executions);
    const wallM = numMeasured(totalM?.wall_ms, 0.001);
    const cpuM = numMeasured(totalM?.cpu_ms, 0.001);
    const peakM = numMeasured(totalM?.peak_memory_bytes);
    const totalTokens = totalM ? sumTokens(totalM) : null;
    const callsM = numMeasured(totalM?.calls);
    const hasTotalCost = !!tr.totals.cost.some(c => (c.value !== null && c.basis !== 'unavailable') || c.basis === 'subscription');
    return [
      ...(execM.basis !== 'unavailable' ? [{ label: 'Executions', m: execM, kind: 'count' as const, note: totalM?.failed?.value ? `${totalM.failed.value} failed` : undefined }] : []),
      ...(wallM.basis !== 'unavailable' ? [{ label: 'Wall', m: wallM, kind: 'duration' as const }] : []),
      ...(cpuM.basis !== 'unavailable' ? [{ label: 'CPU', m: cpuM, kind: 'duration' as const }] : []),
      ...(peakM.basis !== 'unavailable' ? [{ label: 'Peak memory', m: peakM, kind: 'bytes' as const }] : []),
      ...(totalTokens && totalTokens.basis !== 'unavailable' ? [{ label: 'Tokens', m: totalTokens, kind: 'count' as const }] : []),
      ...(totalM?.calls?.value && callsM.basis !== 'unavailable' ? [{ label: 'Tools', m: callsM, kind: 'count' as const }] : []),
      ...(hasTotalCost ? [{ label: 'Cost', cost: tr.totals.cost, showUnavailable: false }] : []),
    ];
  });

  const buckets = createMemo<ChartBucket[]>(() => {
    const g = grain();
    return trendRows().map(r => {
      const k = r.dimensions[g] ?? '';
      const tokM = sumTokens(r.measures);
      const tokStr = tokM.value != null && tokM.basis !== 'unavailable' ? ` · ${tokM.partial ? '≥ ' : ''}${tokM.value.toLocaleString('en')} tokens` : '';
      const vRuns = r.measures.executions?.value ?? 0;
      const vCost = r.cost.filter(c => c.value != null).map(c => `${c.basis === 'subscription' ? '≈' : ''}${c.value!.toFixed(2)} ${c.currency ?? 'USD'}${c.basis === 'subscription' ? ' sub' : ''}`).join(', ');
      return {
        key: k,
        label: shortBucketLabel(k, g),
        tip: `${k}: ${vRuns} runs${tokStr}${vCost ? ` · ${vCost}` : ''}`,
      };
    });
  });

  const costCurrencies = createMemo(() =>
    [...new Set(trendRows().flatMap(r => r.cost.filter(c => c.value != null).map(c => c.currency ?? 'USD')))]
      .sort((a, b) => (a === 'USD' ? -1 : b === 'USD' ? 1 : a.localeCompare(b))),
  );
  const activeCur = createMemo(() => {
    const cs = costCurrencies(), cc = costCur();
    return cs.includes(cc) ? cc : (cs[0] ?? 'USD');
  });
  const sumCostKind = (r: AnalyticsReply['rows'][number], sub: boolean, cur: string) =>
    r.cost.reduce((n, c) => ((c.currency ?? 'USD') === cur && (c.basis === 'subscription') === sub && c.value != null ? n + c.value : n), 0);
  const series = createMemo<ChartSeries[]>(() => {
    const tr = trendRows(), g = grain(), n = now(), m = metric(), cur = activeCur();
    if (m === 'cost') {
      const meteredPts = tr.flatMap(r => {
        const rg = parseBucketMs(r.dimensions[g] ?? '', g);
        return rg ? [{ at: rg[0], end: Math.min(rg[1], n), v: sumCostKind(r, false, cur) }] : [];
      });
      const subPts = tr.flatMap(r => {
        const rg = parseBucketMs(r.dimensions[g] ?? '', g);
        return rg ? [{ at: rg[0], end: Math.min(rg[1], n), v: sumCostKind(r, true, cur) }] : [];
      });
      const hasMetered = meteredPts.some(q => q.v > 0);
      const hasSub = subPts.some(q => q.v > 0);
      const out: ChartSeries[] = [];
      if (hasMetered || !hasSub) out.push({ label: `${cur} metered`, points: meteredPts, tone: 'd1', step: true });
      if (hasSub) out.push({ label: `${cur} subscription equiv.`, points: subPts, tone: hasMetered ? 'd2' : 'd1', pattern: true, step: true });
      return out;
    }
    const pts = tr.flatMap(r => {
      const rg = parseBucketMs(r.dimensions[g] ?? '', g);
      const v = m === 'executions' ? (r.measures.executions?.value ?? 0) : (sumTokens(r.measures).value ?? 0);
      return rg ? [{ at: rg[0], end: Math.min(rg[1], n), v }] : [];
    });
    return [{ label: m === 'executions' ? 'executions' : 'tokens', points: pts, tone: 'd1', step: true }];
  });
  const chartMax = createMemo(() => Math.max(1, ...series().flatMap(s => s.points.map(q => q.v))));
  const chartFormat = (v: number) => {
    const m = metric(), cur = activeCur();
    return m === 'cost'
      ? (cur === 'USD' ? `$${v.toFixed(2)}` : `${v.toFixed(2)} ${cur}`)
      : m === 'executions' ? String(Math.round(v)) : (v >= 1000 ? `${(v / 1000).toFixed(1)}k` : String(Math.round(v)));
  };

  const filterChips = createMemo(() =>
    Object.entries(filters()).flatMap(([k, vals]) => (Array.isArray(vals) ? vals.map(v => ({ k: k as keyof AnalyticsFilter, v: String(v) })) : [])),
  );
  const toggleFilter = (d: AnalyticsDim, val: string) => {
    if (!val || val === '(none)') return;
    if (d === 'project') { props.onOpenProject(val); return; }
    const key = d as keyof AnalyticsFilter;
    const curFilters = untrack(filters);
    const cur = ((curFilters[key] as unknown[] | undefined) ?? []).map(String);
    setFilters({ ...curFilters, [key]: cur.includes(val) ? cur.filter(x => x !== val) : [...cur, val] });
  };

  const qTerms = createMemo(() => {
    const f = filters();
    return [
      f.harness?.[0] ? `agent:${f.harness[0]}` : '',
      f.host?.[0] ? `host:${f.host[0]}` : '',
    ].filter(Boolean).join(' ') || undefined;
  });
  const tasksHref = () => formatView({ view: 'tasks', project: props.projectId ? [props.projectId] : undefined, q: qTerms() });
  const convHref = () => formatView({ view: 'conversations', project: props.projectId ? [props.projectId] : undefined });

  return (
    <div class="stack">
      <div class="res-bar">
        <Choice label="Window" value={win()} onChange={w => changeWin(w as WindowKey)} options={[{ id: '24h', label: '24h' }, { id: '7d', label: '7d' }, { id: '30d', label: '30d' }]} />
        <Choice label="Grain" value={grain()} onChange={g => { setGrain(g as GrainKey); setBucket(null); }} options={[{ id: 'hour', label: 'Hour' }, { id: 'day', label: 'Day' }, { id: 'week', label: 'Week' }]} />
        <Choice label="Series" value={metric()} onChange={m => setMetric(m as ChartMetric)} options={[{ id: 'tokens', label: 'Tokens' }, { id: 'executions', label: 'Executions' }, { id: 'cost', label: 'Cost' }]} />
        <Show when={metric() === 'cost' && costCurrencies().length > 1}>
          <Choice label="Currency" value={activeCur()} onChange={setCostCur} options={costCurrencies().map(c => ({ id: c, label: c }))} />
        </Show>
        <span class="grow" />
        <Show when={unkBucketRuns() > 0}>
          <span class="chip" data-tip="Executions missing start time are excluded from time buckets">
            <Icon name="circle-alert" label="Unbucketed" /><Num m={{ value: unkBucketRuns(), basis: 'measured' }} kind="count" /> unbucketed
          </span>
        </Show>
        <Show when={(trend()?.unknown ?? 0) > 0}>
          <span class="chip" data-tip="Executions missing usage or price telemetry in this window">
            <Icon name="circle-alert" label="Unknown telemetry" /><Num m={{ value: trend()!.unknown, basis: 'measured' }} kind="count" /> unknown
          </span>
        </Show>
        <Show when={(trend()?.hidden ?? 0) > 0}>
          <span class="chip" data-tip="Executions excluded by project visibility permissions">
            <Icon name="circle-alert" label="Hidden executions" /><Num m={{ value: trend()!.hidden, basis: 'measured' }} kind="count" /> hidden
          </span>
        </Show>
      </div>

      <Show when={filterChips().length > 0}>
        <div class="res-bar">
          <For each={filterChips()}>
            {f => (
              <span class="chip">
                {f.k}: {f.v}
                <Button
                  label={`Remove ${f.k} filter ${f.v}`}
                  icon="x"
                  iconOnly
                  kind="quiet"
                  onClick={() => {
                    const cur = untrack(filters);
                    setFilters({ ...cur, [f.k]: ((cur[f.k] as unknown[]) ?? []).filter(x => String(x) !== f.v) });
                  }}
                />
              </span>
            )}
          </For>
        </div>
      </Show>

      <Show when={trendDoc.error}>{err => <ErrorBanner text={`Analytics failed: ${err()}`} onRetry={() => setRev(r => r + 1)} />}</Show>
      <Show when={!trend() && !trendDoc.error}><Empty text="Loading analytics…" /></Show>
      <Show when={totalItems().length > 0}><Totals items={totalItems()} /></Show>

      <Show when={trend()}>
        <AreaChart
          label={`${props.projectId ?? 'All projects'} ${metric()} by ${grain()}`}
          series={series()}
          from={from()}
          to={now()}
          now={now()}
          max={chartMax()}
          value={chartFormat}
          buckets={buckets()}
          selectedBucket={bucket()}
          onSelectBucket={k => setBucket(cur => (cur === k ? null : k))}
        />
      </Show>

      <div class="res-bar">
        <Choice label="Breakdown" value={activeDim()} onChange={d => setDim(d as AnalyticsDim)} options={dimsAvail()} />
        <Show when={bucket()} keyed>
          {b => <Button label={`Clear bucket (${b})`} icon="x" kind="quiet" onClick={() => setBucket(null)} />}
        </Show>
        <span class="grow" />
        <a class="row-link" href={tasksHref()} onClick={e => { e.preventDefault(); go(tasksHref()); }}>Tasks</a>
        <a class="row-link" href={convHref()} onClick={e => { e.preventDefault(); go(convHref()); }}>Conversations</a>
        <a class="row-link" href="/agents" onClick={e => { e.preventDefault(); go('/agents'); }}>Agents</a>
        <a class="row-link" href="/pipelines" onClick={e => { e.preventDefault(); go('/pipelines'); }}>Pipelines</a>
        <a class="row-link" href="/runners" onClick={e => { e.preventDefault(); go('/runners'); }}>Runners</a>
        <a class="row-link" href="/hosts" onClick={e => { e.preventDefault(); go('/hosts'); }}>Hosts</a>
      </div>

      <Show when={breakDoc.error}>{err => <ErrorBanner text={`Breakdown failed: ${err()}`} onRetry={() => setRev(r => r + 1)} />}</Show>
      <Show when={breakdown()} keyed>
        {bd => (
          <Show when={bd.rows.length > 0} fallback={<Empty text="No recorded analytics in this window." />}>
            <DataTable
              label="Analytics breakdown"
              head={
                <tr>
                  <th scope="col">{dimsAvail().find(d => d.id === activeDim())?.label ?? activeDim()}</th>
                  <th scope="col" class="num">Runs</th>
                  <th scope="col" class="num">Wall</th>
                  <th scope="col" class="num">CPU</th>
                  <th scope="col" class="num">Peak mem</th>
                  <th scope="col" class="num">Tokens</th>
                  <th scope="col" class="num">Tools</th>
                  <th scope="col" class="num">Cost</th>
                </tr>
              }
            >
              <For each={bd.rows}>
                {r => {
                  const rawVal = r.dimensions[activeDim()];
                  const tok = sumTokens(r.measures);
                  const active = () => {
                    const curFilter = (filters() as Record<string, unknown[] | undefined>)[activeDim()];
                    return activeDim() !== 'project' && Array.isArray(curFilter) && curFilter.some(x => String(x) === rawVal);
                  };
                  return (
                    <tr class={{ on: active() }}>
                      <th scope="row">
                        <Show when={rawVal} fallback={<span class="muted">(none)</span>}>
                          <Button
                            label={rawVal!}
                            kind="quiet"
                            pressed={active()}
                            tip={activeDim() === 'project' ? `Open project ${rawVal}` : `Filter by ${rawVal}`}
                            onClick={() => toggleFilter(activeDim(), rawVal!)}
                          />
                        </Show>
                      </th>
                      <td class="num">
                        <Num m={numMeasured(r.measures.executions)} kind="count" />
                        <Show when={r.measures.failed?.value}><span class="chip t-danger">{r.measures.failed!.value} fail</span></Show>
                      </td>
                      <td class="num"><Num m={numMeasured(r.measures.wall_ms, 0.001)} kind="duration" /></td>
                      <td class="num">
                        <Show when={r.measures.cpu_ms?.value != null} fallback={<span class="muted">—</span>}>
                          <Num m={numMeasured(r.measures.cpu_ms, 0.001)} kind="duration" />
                        </Show>
                      </td>
                      <td class="num">
                        <Show when={r.measures.peak_memory_bytes?.value != null} fallback={<span class="muted">—</span>}>
                          <Num m={numMeasured(r.measures.peak_memory_bytes)} kind="bytes" />
                        </Show>
                      </td>
                      <td class="num"><Num m={tok} kind="count" /></td>
                      <td class="num">
                        <Show when={r.measures.calls?.value} fallback={<span class="muted">—</span>}>
                          <Num m={numMeasured(r.measures.calls)} kind="count" />
                        </Show>
                      </td>
                      <td class="num"><CostFigures cost={r.cost} showUnavailable empty="—" /></td>
                    </tr>
                  );
                }}
              </For>
            </DataTable>
          </Show>
        )}
      </Show>
    </div>
  );
}
