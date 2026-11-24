// Projects configuration, tabs (Work & Rules · Methods · Delivery · Results), OR-45 diff-from-default, and resource analytics (docs/design-system/pages/projects.md).
import { useMemo, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
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
export function Projects({ id }: { id?: string }) {
  const [, go] = useLocation();
  const [creating, setCreating] = useState(false);
  const [query, setQuery] = useState('');
  const list = useOp('project_list', {});
  const allProjects = list.data?.projects ?? [];
  const rows = allProjects.map(p => ({ id: str(p.id), name: str(p.name), raw: p })).filter(p => matchSearch(query, `${p.name} ${p.id}`, { project: p.id }));
  const spend = useRowSpend('projects', rows);
  const selectedName = rows.find(r => r.id === id)?.name || str(allProjects.find(p => str(p.id) === id)?.name);
  const pane = (
    <>
      <ShellActions>
        <Button label="Create project" icon="plus" iconOnly kind="primary" pressed={creating} onClick={() => setCreating(true)} />
      </ShellActions>
      {list.error && <ErrorBanner text={`Projects did not load: ${list.error}`} onRetry={list.reload} />}
      <section class="panel">
        <ListFrame
          pending={!list.data && !list.error}
          bar={<FilterBar query={query} onQuery={setQuery} placeholder="Find project" shown={rows.length} total={allProjects.length} />}
        >
          {!list.data && !list.error ? <Skeleton rows={6} /> : rows.length === 0 ? (
            <Empty text={query ? 'No projects match.' : 'No projects yet.'} />
          ) : (
            <VirtualList
              fill
              items={rows}
              getKey={p => p.id}
              rowHeight={catalogueRow()}
              follow="top"
              label="Projects"
              columns="1.25rem minmax(0,1fr) auto"
              rowClass={p => !creating && p.id === id ? 'sel' : ''}
              render={p => (
                <>
                  <span class="lead">{projectStateBadge(p.raw)}</span>
                  <span class="tt">
                    <a
                      class="title row-link"
                      href={'/projects/' + encodeURIComponent(p.id)}
                      onClick={e => { e.preventDefault(); setCreating(false); go('/projects/' + encodeURIComponent(p.id)); }}
                    >{p.name || p.id}</a>
                    {p.name && p.name.toLowerCase() !== p.id.toLowerCase() && <span class="id">{p.id}</span>}
                  </span>
                  <span class="rz"><RowFacts><RowSpend cost={spend.cost.get(p.id)} stale={!!spend.error} name={p.name || p.id}
                    href={'/projects/' + encodeURIComponent(p.id)}
                    open={() => { setCreating(false); go('/projects/' + encodeURIComponent(p.id)); }} /></RowFacts></span>
                </>
              )}
            />
          )}
        </ListFrame>
      </section>
    </>
  );
  const overview = (
    <article class="detail" data-ui="detail" tabIndex={-1} aria-label="Projects analytics">
      <header class="detail-h">
        <h2 class="detail-title">Resource analytics</h2>
      </header>
      <ProjectAnalytics onOpenProject={pid => go('/projects/' + encodeURIComponent(pid))} />
    </article>
  );
  return (
    <ListDetail
      list={pane}
      detail={creating ? (
        <ProjectEditor
          key="create"
          id={undefined}
          onClose={() => setCreating(false)}
          onSaved={p => { setCreating(false); go('/projects/' + encodeURIComponent(p)); }}
          renderAnalytics={pid => <ProjectAnalytics projectId={pid} onOpenProject={target => go('/projects/' + encodeURIComponent(target))} />}
        />
      ) : id ? (
        <ProjectDetail
          key={id}
          id={id}
          name={selectedName}
          onClose={() => go('/projects')}
          onSaved={p => go('/projects/' + encodeURIComponent(p))}
        />
      ) : undefined}
      idle={overview}
      detailKey={creating ? 'create' : id}
    />
  );
}

/** Tabbed project detail: Work & Rules, Methods, Delivery, and Results (docs/designs/fleet-ia-and-journeys.md). */
function ProjectDetail({ id, name, onClose, onSaved }: { id: string; name?: string; onClose: () => void; onSaved: (id: string) => void }) {
  const [, go] = useLocation();
  const [tab, setTab] = useState<ProjectTab>('work');
  const tabs: { id: ProjectTab; label: string }[] = [
    { id: 'work', label: 'Work & Rules' },
    { id: 'methods', label: 'Methods' },
    { id: 'delivery', label: 'Delivery' },
    { id: 'results', label: 'Results' },
  ];

  // Outer CSS grid keeps TabBar fixed at the top while the active .detail remains the column's sole vertical scroller (OR-25).
  return (
    <div style={{ display: 'grid', gridTemplateRows: 'auto minmax(0, 1fr)', minWidth: 0, minHeight: 0, background: 'var(--surface)' }}>
      <div style={{ paddingInline: 'clamp(var(--space-3), calc(8% - var(--space-7)), var(--space-6))', paddingTop: 'var(--space-3)' }}>
        <TabBar
          cls="detail-tabs"
          label="Project sections"
          tabs={tabs}
          value={tab}
          onChange={setTab}
        />
      </div>
      {tab === 'work' ? (
        <div role="tabpanel" aria-label="Work & Rules" style={{ display: 'grid', gridTemplateRows: 'minmax(0, 1fr)', minWidth: 0, minHeight: 0 }}>
          <ProjectEditor
            id={id}
            onClose={onClose}
            onSaved={onSaved}
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
      ) : (
        <article class="detail" data-ui="detail" tabIndex={-1} aria-label={`${id} ${tab}`}>
          <header class="detail-h">
            <Button label="Close" icon="x" iconOnly onClick={onClose} />
            <h2 class="detail-title">{name || id}</h2>
          </header>
          <div role="tabpanel" aria-label={tabs.find(t => t.id === tab)?.label ?? tab} class="stack">
            {tab === 'methods' && <ProjectMethodsTab projectId={id} />}
            {tab === 'delivery' && <ProjectDeliveryTab projectId={id} />}
            {tab === 'results' && <ProjectResultsTab projectId={id} />}
          </div>
        </article>
      )}
    </div>
  );
}

/* ---------- Work & Rules: active work overview + OR-45 diff from default ---------- */

function ProjectWorkSection({ projectId }: { projectId: string }) {
  const [, go] = useLocation();
  const tasksDoc = useOp('task_page', {
    filter: { projectId: [projectId], state: RUNNING_TASK_STATES },
    group: 'none',
    sort: '-changed',
    limit: 6,
  });
  const execsDoc = useOp('executions_list', { projectId, limit: 6 });
  const tasksHref = formatView({ view: 'tasks', project: [projectId] });
  const tasks = tasksDoc.data?.tasks ?? [];
  const counts = tasksDoc.data?.counts ?? {};
  const hasCounts = Object.values(counts).some(n => n > 0);
  const activeExecs = (execsDoc.data?.executions ?? []).filter(e => ACTIVE_EXEC_STATES.has(e.state));

  return (
    <DetailSection title="Active work">
      <div class="res-bar">
        {hasCounts && (
          <StateCounters
            family="task"
            label="Project tasks by state"
            counts={counts as Partial<Record<StateOf<'task'>, number>>}
            active={[]}
            hideZero
            onToggle={s => go(formatView({ view: 'tasks', project: [projectId], state: [String(s)] }))}
          />
        )}
        <span class="grow" />
        <a class="row-link" href={tasksHref} onClick={e => { e.preventDefault(); go(tasksHref); }}>Open in Tasks</a>
      </div>
      {tasksDoc.error && <ErrorBanner text={`Tasks did not load: ${tasksDoc.error}`} onRetry={tasksDoc.reload} />}
      {!tasksDoc.data && !tasksDoc.error && <Empty text="Loading active tasks…" />}
      {tasksDoc.data && tasks.length === 0 && activeExecs.length === 0 && (
        <Empty text="No active tasks or executions for this project." />
      )}
      {tasks.map(t => {
        const rawState = t.pauseReason === 'held' ? 'held' : t.state;
        const taskState = (rawState in states_.task ? rawState : 'queued') as StateOf<'task'>;
        return (
          <KvRow
            key={t.id}
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
      })}
      {activeExecs.map(ex => (
        <KvRow
          key={ex.id}
          icon="activity"
          label={ex.kind}
          meta={
            <span class="deps">
              <StateBadge family="execution" state={ex.state} compact />
              <EntityCode id={ex.id} kind="execution" />
              {ex.harness && <span class="mono">{ex.harness}</span>}
              {ex.hostId && <span class="mono">{ex.hostId}</span>}
              {ex.taskId && <EntityCode id={ex.taskId} kind="task" />}
            </span>
          }
          value={ex.startedAt ? <AgeChip t={Date.parse(ex.startedAt)} /> : undefined}
        />
      ))}
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

function ProjectSettingsDiff({ reply }: { reply: ProjectDetailData | null }) {
  const { diffs, inherited, unknownKeys } = useMemo(() => {
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
  }, [reply]);

  if (!reply) return null;

  return (
    <DetailSection title="Settings from defaults">
      {diffs.length > 0 ? (
        diffs.map(f => (
          <KvRow
            key={f.key}
            icon={f.icon}
            label={f.label}
            meta={
              <span data-tip={`Default: ${f.baseText}`}>
                {f.isCode ? <code>{f.currentText}</code> : f.currentText}
              </span>
            }
          />
        ))
      ) : (
        <KvRow icon="circle-check" label="Matches base defaults" meta="All settings match shared project defaults" />
      )}
      {unknownKeys.length > 0 && (
        <KvRow icon="triangle-alert" label="Unknown keys" meta={unknownKeys.join(', ')} />
      )}
      {inherited.length > 0 && (
        <Disclosure summary={`Inherited defaults (${inherited.length})`} summaryClass="muted">
          {inherited.map(f => (
            <KvRow
              key={f.key}
              icon={f.icon}
              label={f.label}
              meta={f.isCode ? <code>{f.currentText}</code> : f.currentText}
            />
          ))}
        </Disclosure>
      )}
    </DetailSection>
  );
}

/* ---------- Methods tab ---------- */

function methodStatusTone(s: string | undefined): 'success' | 'attention' | 'neutral' {
  return s === 'recommended' ? 'success' : s === 'experimental' ? 'attention' : 'neutral';
}

function methodStatusIcon(s: string | undefined): IconName {
  return s === 'recommended' ? 'circle-check' : s === 'experimental' ? 'activity' : 'circle-slash';
}

function ProjectMethodsTab({ projectId }: { projectId: string }) {
  const [, go] = useLocation();
  const [search, setSearch] = useState('');
  const [busy, setBusy] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const cat = useOp('catalogue', { projectId });
  const c = cat.data;

  const recommend = async (methodId: string) => {
    setBusy(methodId);
    setActionError(null);
    try {
      await op('recommendation_set', { projectId, methodId });
      cat.reload();
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  if (cat.status === 404) {
    return <Empty text="No method catalogue is configured." />;
  }
  if (cat.error && !c) {
    return <ErrorBanner text={`Methods did not load: ${cat.error}`} onRetry={cat.reload} />;
  }
  if (!c) {
    return <Skeleton rows={5} />;
  }

  const recommendedMethod = typeof c.recommendedMethod === 'string' ? c.recommendedMethod : null;
  const sharedDefault = typeof c.sharedDefault === 'string' ? c.sharedDefault : null;
  const source = typeof c.recommendationSource === 'string' ? c.recommendationSource : 'shared';
  const capability = typeof c.capability === 'string' ? c.capability : null;
  const filtered = c.methods.filter(m =>
    matchSearch(search, `${m.name} ${m.id} ${m.capability ?? ''} ${m.description ?? ''} ${m.status ?? ''} ${m.pipeline}`),
  );

  return (
    <>
      <div class="res-bar">
        <SearchField label="Find method" value={search} onInput={setSearch} onSearch={() => {}} />
        <span class="grow" />
        <a class="row-link" href="/catalogue" onClick={e => { e.preventDefault(); go('/catalogue'); }}>Open in Library</a>
        <a class="row-link" href="/lab" onClick={e => { e.preventDefault(); go('/lab'); }}>Open in Lab</a>
      </div>
      {actionError && <ErrorBanner text={actionError} />}
      <DetailSection title="Current recommendation">
        <KvRow
          icon="circle-check"
          label={recommendedMethod ?? 'None'}
          meta={
            <span data-tip={sharedDefault ? `Shared default: ${sharedDefault}` : undefined}>
              source: <code>{source}</code>
              {capability ? <> · capability: <code>{capability}</code></> : null}
            </span>
          }
        />
      </DetailSection>
      <DetailSection title={`Methods (${filtered.length})`}>
        {filtered.length === 0 ? <Empty text="No methods match." /> : filtered.map(m => {
          const isCurrent = m.id === recommendedMethod;
          const a = m.assessment;
          return (
            <div key={m.id} class="stack">
              <KvRow
                icon={isCurrent ? 'circle-check' : 'network'}
                label={m.name}
                meta={
                  <span class="deps">
                    <code>{m.id}</code>
                    <span>{m.scope}</span>
                    <a class="row-link" href={'/pipelines/' + encodeURIComponent(m.pipeline)} onClick={e => { e.preventDefault(); go('/pipelines/' + encodeURIComponent(m.pipeline)); }}>{m.pipeline}</a>
                    {m.status && (
                      <span class={`badge t-${methodStatusTone(m.status)}`}>
                        <Icon name={methodStatusIcon(m.status)} /><span>{m.status}</span>
                      </span>
                    )}
                  </span>
                }
                value={
                  <span class="deps">
                    {isCurrent && <span class="badge t-success">Recommended</span>}
                    <Button
                      label={isCurrent ? 'Selected' : 'Recommend'}
                      icon="check"
                      kind={isCurrent ? 'quiet' : 'default'}
                      busy={busy === m.id}
                      disabledReason={isCurrent ? 'Already recommended for this project' : undefined}
                      onClick={() => void recommend(m.id)}
                    />
                  </span>
                }
              />
              {m.description && <p class="muted">{m.description}</p>}
              <KvRow
                icon="chart-no-axes-column"
                label="Assessment"
                meta={
                  <span class="deps">
                    <span>{a.basis}</span>
                    {a.score != null && <Num m={{ value: a.score, basis: a.basis === 'none' ? 'unavailable' : 'measured' }} kind="score" />}
                    {a.wallMsMedian != null && <Num m={msMeasured(a.wallMsMedian, 'measured')} kind="duration" />}
                    {a.costUsd != null && <Num m={{ value: a.costUsd, basis: a.costBasis === 'subscription' ? 'subscription' : 'measured', currency: 'USD' }} kind="money" />}
                    {a.examples != null && <span>{a.examples} {a.examples === 1 ? 'example' : 'examples'}</span>}
                  </span>
                }
                value={a.experimentId ? (
                  <a class="row-link" href={'/lab/' + encodeURIComponent(a.experimentId)} onClick={e => { e.preventDefault(); go('/lab/' + encodeURIComponent(a.experimentId!)); }}>Experiment</a>
                ) : undefined}
              />
            </div>
          );
        })}
      </DetailSection>
    </>
  );
}

/* ---------- Delivery tab ---------- */

function ProjectDeliveryTab({ projectId }: { projectId: string }) {
  const [, go] = useLocation();
  const [search, setSearch] = useState('');
  const [stageFilter, setStageFilter] = useState<StateOf<'delivery'>[]>([]);
  const status = useOp('delivery_status', { projectIds: [projectId] }, { every: 300000 });
  const deliveryHref = formatView({ view: 'delivery', q: `project:${projectId}` });
  const items = (status.data?.items ?? []).filter(it => it.stage in states_.delivery);

  const searched = items.filter(it =>
    matchSearch(search, `${it.title} #${it.number} ${it.repo} ${it.release ?? ''}`, { state: it.stage, project: it.projectId }),
  );
  const counts: Partial<Record<StateOf<'delivery'>, number>> = {};
  for (const it of searched) counts[it.stage] = (counts[it.stage] ?? 0) + 1;
  const shown = stageFilter.length ? searched.filter(it => stageFilter.includes(it.stage)) : searched;

  return (
    <>
      <div class="res-bar">
        <SearchField label="Find pull requests" value={search} onInput={setSearch} onSearch={() => {}} />
        {status.data?.refreshedAt && <AgeChip t={Date.parse(status.data.refreshedAt)} alertAfter={900} alertLabel="GitHub read is stale" />}
        <span class="grow" />
        <a class="row-link" href={deliveryHref} onClick={e => { e.preventDefault(); go(deliveryHref); }}>Open in Delivery</a>
      </div>
      {items.length > 0 && (
        <StateCounters
          family="delivery"
          label="Project pull requests by stage"
          counts={counts}
          active={stageFilter}
          hideZero
          onAll={() => setStageFilter([])}
          onToggle={s => setStageFilter(cur => cur.includes(s) ? cur.filter(x => x !== s) : [...cur, s])}
        />
      )}
      {status.error && !status.data && <ErrorBanner text={`Delivery did not load: ${status.error}`} onRetry={status.reload} />}
      {status.data?.error && <ErrorBanner text={`GitHub: ${status.data.error}`} onRetry={status.reload} />}
      {!status.data && !status.error ? <Skeleton rows={5} /> : (
        <DetailSection title={`Pull requests (${shown.length})`}>
          {shown.length === 0 ? (
            <Empty text={items.length ? 'No pull requests match.' : 'No pull requests in flight for this project.'} />
          ) : (
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
              {shown.map(it => {
                const prId = idOf(it);
                return (
                  <tr key={prId}>
                    <td><StateBadge family="delivery" state={it.stage} /></td>
                    <td><EntityCode code={`#${it.number}`} id={prId} kind="pull_request" /></td>
                    <th scope="row">
                      <a class="row-link" href={'/delivery/' + encodeURIComponent(prId)} onClick={e => { e.preventDefault(); go('/delivery/' + encodeURIComponent(prId)); }}>
                        {it.title || `#${it.number}`}
                      </a>
                      {it.release && <> <EntityCode code={it.release} id={it.release} kind="release" /></>}
                    </th>
                    <td><code>{it.repo}</code></td>
                    <td class="num"><AgeChip t={Date.parse(it.since)} /></td>
                  </tr>
                );
              })}
            </DataTable>
          )}
        </DetailSection>
      )}
    </>
  );
}

/* ---------- Results tab ---------- */

function ProjectResultsTab({ projectId }: { projectId: string }) {
  const [, go] = useLocation();
  const [search, setSearch] = useState('');
  const [stateFilter, setStateFilter] = useState<StateOf<'artifact'>[]>([]);
  const read = useOp('artifact_list', { projectIds: [projectId], limit: 100 });
  const resultsHref = formatView({ view: 'results', project: [projectId] });
  const artifacts = read.data?.artifacts ?? [];
  const nextBefore = read.data?.nextBefore ?? null;

  const searched = artifacts.filter(a =>
    matchSearch(search, `${fileLabel(a.name) || a.name} ${a.mediaType} ${a.taskCode ?? ''}`, { state: a.state, project: a.projectId ?? undefined }),
  );
  const counts: Partial<Record<StateOf<'artifact'>, number>> = {};
  for (const a of searched) {
    const s = a.state as StateOf<'artifact'>;
    counts[s] = (counts[s] ?? 0) + 1;
  }
  const shown = stateFilter.length ? searched.filter(a => stateFilter.includes(a.state as StateOf<'artifact'>)) : searched;

  return (
    <>
      <div class="res-bar">
        <SearchField label="Find results" value={search} onInput={setSearch} onSearch={() => {}} />
        <span class="grow" />
        <a class="row-link" href={resultsHref} onClick={e => { e.preventDefault(); go(resultsHref); }}>Open in Results</a>
      </div>
      {artifacts.length > 0 && (
        <StateCounters
          family="artifact"
          label="Project results by state"
          counts={counts}
          active={stateFilter}
          hideZero
          onAll={() => setStateFilter([])}
          onToggle={s => setStateFilter(cur => cur.includes(s) ? cur.filter(x => x !== s) : [...cur, s])}
        />
      )}
      {read.error && !read.data && <ErrorBanner text={`Results did not load: ${read.error}`} onRetry={read.reload} />}
      {!read.data && !read.error ? <Skeleton rows={5} /> : (
        <DetailSection title={`Results (${shown.length}${nextBefore != null ? '+' : ''})`}>
          {shown.length === 0 ? (
            <Empty text={artifacts.length ? 'No results match.' : 'No results stored for this project.'} />
          ) : (
            <Latest
              n={25}
              items={shown.map(a => {
                const artState = (a.state in states_.artifact ? a.state : 'stored') as StateOf<'artifact'>;
                return (
                  <KvRow
                    key={a.id}
                    icon={states_.artifact[artState].icon}
                    label={fileLabel(a.name) || a.name}
                    meta={
                      <span class="deps">
                        <StateBadge family="artifact" state={artState} compact />
                        <EntityCode id={a.id} kind="result" />
                        {a.taskId && a.taskCode && <EntityCode code={a.taskCode} id={a.taskId} kind="task" />}
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
          )}
          {nextBefore != null && (
            <p class="muted">
              More results exist for this project. <a class="row-link" href={resultsHref} onClick={e => { e.preventDefault(); go(resultsHref); }}>View all in Results</a>
            </p>
          )}
        </DetailSection>
      )}
    </>
  );
}

function ProjectAnalytics({ projectId, onOpenProject }: { projectId?: string; onOpenProject: (id: string) => void }) {
  const [, go] = useLocation();
  const initialSearch = typeof location !== 'undefined' ? new URLSearchParams(location.search) : null;
  const [win, setWin] = useState<WindowKey>(() => {
    const w = initialSearch?.get('window');
    return w === '24h' || w === '7d' || w === '30d' ? w : '7d';
  });
  const [grain, setGrain] = useState<GrainKey>(() => {
    const g = initialSearch?.get('grain');
    return g === 'hour' || g === 'day' || g === 'week' ? g : 'day';
  });
  const [metric, setMetric] = useState<ChartMetric>('tokens');
  const [dim, setDim] = useState<AnalyticsDim>(() => {
    const d = initialSearch?.get('dim') as AnalyticsDim | null;
    if (d && DIMS.some(x => x.id === d)) return d;
    return projectId ? 'model' : 'project';
  });
  const [bucket, setBucket] = useState<string | null>(() => initialSearch?.get('bucket') || null);
  const [filters, setFilters] = useState<AnalyticsFilter>(() => {
    const out: AnalyticsFilter = {};
    const m = initialSearch?.get('model'); if (m) out.model = [m];
    const h = initialSearch?.get('harness'); if (h) out.harness = [h];
    const p = initialSearch?.get('pipeline'); if (p) out.pipeline = [p];
    const pr = initialSearch?.get('profile_id'); if (pr) out.profile_id = [pr];
    const pv = initialSearch?.get('pipeline_revision'); if (pv) out.pipeline_revision = [pv];
    return out;
  });
  const [costCur, setCostCur] = useState<string>('USD');
  const [rev, setRev] = useState(0);

  const now = useMemo(() => Date.now(), [projectId, win, grain, rev]);
  const from = now - WINDOW_MS[win];

  const dimsAvail = projectId ? DIMS.filter(d => d.id !== 'project') : DIMS;
  const activeDim = dimsAvail.some(d => d.id === dim) ? dim : dimsAvail[0].id;

  // Two reads of the same operation: the trend over the window by time bucket, and the breakdown of the picked bucket
  // (or the window) by the chosen dimension. A retry moves the window to now (rev), which names new reads.
  const scope = projectId ? { projectId } : {};
  const trendDoc = useOp('analytics_query', { window: { from, to: now }, dimensions: [grain], measures: ANALYTICS_MEASURES, filters, ...scope });
  const range = bucket ? parseBucketMs(bucket, grain) : null;
  const breakDoc = useOp('analytics_query', { window: range ? { from: range[0], to: Math.max(range[0] + 1, Math.min(range[1], now)) } : { from, to: now }, dimensions: [activeDim], measures: ANALYTICS_MEASURES, filters, ...scope });
  const trend = trendDoc.data, breakdown = breakDoc.data, trendErr = trendDoc.error, breakErr = breakDoc.error;

  const changeWin = (w: WindowKey) => {
    setWin(w);
    setBucket(null);
    if (w === '24h' && grain === 'week') setGrain('hour');
    if (w !== '24h' && grain === 'hour') setGrain('day');
  };

  const allTrendRows = trend?.rows ?? [];
  const unkBucketRuns = allTrendRows.find(r => r.dimensions[grain] === 'unknown')?.measures.executions?.value ?? 0;
  const trendRows = allTrendRows
    .filter(r => parseBucketMs(r.dimensions[grain] ?? '', grain) != null)
    .sort((a, b) => (a.dimensions[grain] ?? '').localeCompare(b.dimensions[grain] ?? ''));
  const totalM = trend?.totals.measures;
  const execM = numMeasured(totalM?.executions);
  const wallM = numMeasured(totalM?.wall_ms, 0.001);
  const cpuM = numMeasured(totalM?.cpu_ms, 0.001);
  const peakM = numMeasured(totalM?.peak_memory_bytes);
  const totalTokens = totalM ? sumTokens(totalM) : null;
  const callsM = numMeasured(totalM?.calls);
  const hasTotalCost = !!trend?.totals.cost.some(c => (c.value !== null && c.basis !== 'unavailable') || c.basis === 'subscription');
  const totalItems: TotalItem[] = trend ? [
    ...(execM.basis !== 'unavailable' ? [{ label: 'Executions', m: execM, kind: 'count' as const, note: totalM?.failed?.value ? `${totalM.failed.value} failed` : undefined }] : []),
    ...(wallM.basis !== 'unavailable' ? [{ label: 'Wall', m: wallM, kind: 'duration' as const }] : []),
    ...(cpuM.basis !== 'unavailable' ? [{ label: 'CPU', m: cpuM, kind: 'duration' as const }] : []),
    ...(peakM.basis !== 'unavailable' ? [{ label: 'Peak memory', m: peakM, kind: 'bytes' as const }] : []),
    ...(totalTokens && totalTokens.basis !== 'unavailable' ? [{ label: 'Tokens', m: totalTokens, kind: 'count' as const }] : []),
    ...(totalM?.calls?.value && callsM.basis !== 'unavailable' ? [{ label: 'Tools', m: callsM, kind: 'count' as const }] : []),
    ...(hasTotalCost ? [{ label: 'Cost', cost: trend.totals.cost, showUnavailable: false }] : []),
  ] : [];

  const buckets: ChartBucket[] = trendRows.map(r => {
    const k = r.dimensions[grain] ?? '';
    const tokM = sumTokens(r.measures);
    const tokStr = tokM.value != null && tokM.basis !== 'unavailable' ? ` · ${tokM.partial ? '≥ ' : ''}${tokM.value.toLocaleString('en')} tokens` : '';
    const vRuns = r.measures.executions?.value ?? 0;
    const vCost = r.cost.filter(c => c.value != null).map(c => `${c.basis === 'subscription' ? '≈' : ''}${c.value!.toFixed(2)} ${c.currency ?? 'USD'}${c.basis === 'subscription' ? ' sub' : ''}`).join(', ');
    return {
      key: k,
      label: shortBucketLabel(k, grain),
      tip: `${k}: ${vRuns} runs${tokStr}${vCost ? ` · ${vCost}` : ''}`,
    };
  });

  const costCurrencies = [...new Set(trendRows.flatMap(r => r.cost.filter(c => c.value != null).map(c => c.currency ?? 'USD')))]
    .sort((a, b) => (a === 'USD' ? -1 : b === 'USD' ? 1 : a.localeCompare(b)));
  const activeCur = costCurrencies.includes(costCur) ? costCur : (costCurrencies[0] ?? 'USD');
  const sumCostKind = (r: AnalyticsReply['rows'][number], sub: boolean) =>
    r.cost.reduce((n, c) => ((c.currency ?? 'USD') === activeCur && (c.basis === 'subscription') === sub && c.value != null ? n + c.value : n), 0);
  const series: ChartSeries[] = (() => {
    if (metric === 'cost') {
      const meteredPts = trendRows.flatMap(r => {
        const range = parseBucketMs(r.dimensions[grain] ?? '', grain);
        return range ? [{ at: range[0], end: Math.min(range[1], now), v: sumCostKind(r, false) }] : [];
      });
      const subPts = trendRows.flatMap(r => {
        const range = parseBucketMs(r.dimensions[grain] ?? '', grain);
        return range ? [{ at: range[0], end: Math.min(range[1], now), v: sumCostKind(r, true) }] : [];
      });
      const hasMetered = meteredPts.some(q => q.v > 0);
      const hasSub = subPts.some(q => q.v > 0);
      const out: ChartSeries[] = [];
      if (hasMetered || !hasSub) out.push({ label: `${activeCur} metered`, points: meteredPts, tone: 'd1', step: true });
      if (hasSub) out.push({ label: `${activeCur} subscription equiv.`, points: subPts, tone: hasMetered ? 'd2' : 'd1', pattern: true, step: true });
      return out;
    }
    const pts = trendRows.flatMap(r => {
      const range = parseBucketMs(r.dimensions[grain] ?? '', grain);
      const v = metric === 'executions' ? (r.measures.executions?.value ?? 0) : (sumTokens(r.measures).value ?? 0);
      return range ? [{ at: range[0], end: Math.min(range[1], now), v }] : [];
    });
    return [{ label: metric === 'executions' ? 'executions' : 'tokens', points: pts, tone: 'd1', step: true }];
  })();
  const chartMax = Math.max(1, ...series.flatMap(s => s.points.map(q => q.v)));
  const chartFormat = (v: number) => metric === 'cost'
    ? (activeCur === 'USD' ? `$${v.toFixed(2)}` : `${v.toFixed(2)} ${activeCur}`)
    : metric === 'executions' ? String(Math.round(v)) : (v >= 1000 ? `${(v / 1000).toFixed(1)}k` : String(Math.round(v)));

  const filterChips = Object.entries(filters).flatMap(([k, vals]) => Array.isArray(vals) ? vals.map(v => ({ k: k as keyof AnalyticsFilter, v: String(v) })) : []);
  const toggleFilter = (d: AnalyticsDim, val: string) => {
    if (!val || val === '(none)') return;
    if (d === 'project') { onOpenProject(val); return; }
    const key = d as keyof AnalyticsFilter;
    const cur = ((filters[key] as unknown[] | undefined) ?? []).map(String);
    setFilters({ ...filters, [key]: cur.includes(val) ? cur.filter(x => x !== val) : [...cur, val] });
  };

  const qTerms = [
    filters.harness?.[0] ? `agent:${filters.harness[0]}` : '',
    filters.host?.[0] ? `host:${filters.host[0]}` : '',
  ].filter(Boolean).join(' ') || undefined;
  const tasksHref = formatView({ view: 'tasks', project: projectId ? [projectId] : undefined, q: qTerms });
  const convHref = formatView({ view: 'conversations', project: projectId ? [projectId] : undefined });

  return (
    <div class="stack">
      <div class="res-bar">
        <Choice label="Window" value={win} onChange={w => changeWin(w as WindowKey)} options={[{ id: '24h', label: '24h' }, { id: '7d', label: '7d' }, { id: '30d', label: '30d' }]} />
        <Choice label="Grain" value={grain} onChange={g => { setGrain(g as GrainKey); setBucket(null); }} options={[{ id: 'hour', label: 'Hour' }, { id: 'day', label: 'Day' }, { id: 'week', label: 'Week' }]} />
        <Choice label="Series" value={metric} onChange={m => setMetric(m as ChartMetric)} options={[{ id: 'tokens', label: 'Tokens' }, { id: 'executions', label: 'Executions' }, { id: 'cost', label: 'Cost' }]} />
        {metric === 'cost' && costCurrencies.length > 1 && (
          <Choice label="Currency" value={activeCur} onChange={setCostCur} options={costCurrencies.map(c => ({ id: c, label: c }))} />
        )}
        <span class="grow" />
        {unkBucketRuns > 0 && (
          <span class="chip" data-tip="Executions missing start time are excluded from time buckets">
            <Icon name="circle-alert" label="Unbucketed" /><Num m={{ value: unkBucketRuns, basis: 'measured' }} kind="count" /> unbucketed
          </span>
        )}
        {(trend?.unknown ?? 0) > 0 && (
          <span class="chip" data-tip="Executions missing usage or price telemetry in this window">
            <Icon name="circle-alert" label="Unknown telemetry" /><Num m={{ value: trend!.unknown, basis: 'measured' }} kind="count" /> unknown
          </span>
        )}
        {(trend?.hidden ?? 0) > 0 && (
          <span class="chip" data-tip="Executions excluded by project visibility permissions">
            <Icon name="circle-alert" label="Hidden executions" /><Num m={{ value: trend!.hidden, basis: 'measured' }} kind="count" /> hidden
          </span>
        )}
      </div>

      {filterChips.length > 0 && (
        <div class="res-bar">
          {filterChips.map(f => (
            <span key={`${f.k}:${f.v}`} class="chip">
              {f.k}: {f.v}
              <Button label={`Remove ${f.k} filter ${f.v}`} icon="x" iconOnly kind="quiet" onClick={() => setFilters({ ...filters, [f.k]: ((filters[f.k] as unknown[]) ?? []).filter(x => String(x) !== f.v) })} />
            </span>
          ))}
        </div>
      )}

      {trendErr && <ErrorBanner text={`Analytics failed: ${trendErr}`} onRetry={() => setRev(r => r + 1)} />}
      {!trend && !trendErr && <Empty text="Loading analytics…" />}
      {totalItems.length > 0 && <Totals items={totalItems} />}

      {trend && (
        <AreaChart
          label={`${projectId ?? 'All projects'} ${metric} by ${grain}`}
          series={series}
          from={from}
          to={now}
          now={now}
          max={chartMax}
          value={chartFormat}
          buckets={buckets}
          selectedBucket={bucket}
          onSelectBucket={k => setBucket(cur => cur === k ? null : k)}
        />
      )}

      <div class="res-bar">
        <Choice label="Breakdown" value={activeDim} onChange={d => setDim(d as AnalyticsDim)} options={dimsAvail} />
        {bucket && (
          <Button label={`Clear bucket (${bucket})`} icon="x" kind="quiet" onClick={() => setBucket(null)} />
        )}
        <span class="grow" />
        <a class="row-link" href={tasksHref} onClick={e => { e.preventDefault(); go(tasksHref); }}>Tasks</a>
        <a class="row-link" href={convHref} onClick={e => { e.preventDefault(); go(convHref); }}>Conversations</a>
        <a class="row-link" href="/agents" onClick={e => { e.preventDefault(); go('/agents'); }}>Agents</a>
        <a class="row-link" href="/pipelines" onClick={e => { e.preventDefault(); go('/pipelines'); }}>Pipelines</a>
        <a class="row-link" href="/runners" onClick={e => { e.preventDefault(); go('/runners'); }}>Runners</a>
        <a class="row-link" href="/hosts" onClick={e => { e.preventDefault(); go('/hosts'); }}>Hosts</a>
      </div>

      {breakErr && <ErrorBanner text={`Breakdown failed: ${breakErr}`} onRetry={() => setRev(r => r + 1)} />}
      {breakdown && (
        breakdown.rows.length === 0 ? <Empty text="No recorded analytics in this window." /> : (
          <DataTable label="Analytics breakdown" head={
                <tr>
                  <th scope="col">{dimsAvail.find(d => d.id === activeDim)?.label ?? activeDim}</th>
                  <th scope="col" class="num">Runs</th>
                  <th scope="col" class="num">Wall</th>
                  <th scope="col" class="num">CPU</th>
                  <th scope="col" class="num">Peak mem</th>
                  <th scope="col" class="num">Tokens</th>
                  <th scope="col" class="num">Tools</th>
                  <th scope="col" class="num">Cost</th>
                </tr>
          }>
                {breakdown.rows.map(r => {
                  const rawVal = r.dimensions[activeDim];
                  const keyVal = rawVal || '(none)';
                  const tok = sumTokens(r.measures);
                  const curFilter = (filters as Record<string, unknown[] | undefined>)[activeDim];
                  const active = activeDim !== 'project' && Array.isArray(curFilter) && curFilter.some(x => String(x) === rawVal);
                  return (
                    <tr key={keyVal} class={active ? 'on' : undefined}>
                      <th scope="row">
                        {rawVal ? (
                          <Button label={rawVal} kind="quiet" pressed={active} tip={activeDim === 'project' ? `Open project ${rawVal}` : `Filter by ${rawVal}`} onClick={() => toggleFilter(activeDim, rawVal)} />
                        ) : <span class="muted">(none)</span>}
                      </th>
                      <td class="num"><Num m={numMeasured(r.measures.executions)} kind="count" />{r.measures.failed?.value ? <span class="chip t-danger">{r.measures.failed.value} fail</span> : null}</td>
                      <td class="num"><Num m={numMeasured(r.measures.wall_ms, 0.001)} kind="duration" /></td>
                      <td class="num">{r.measures.cpu_ms?.value != null ? <Num m={numMeasured(r.measures.cpu_ms, 0.001)} kind="duration" /> : <span class="muted">—</span>}</td>
                      <td class="num">{r.measures.peak_memory_bytes?.value != null ? <Num m={numMeasured(r.measures.peak_memory_bytes)} kind="bytes" /> : <span class="muted">—</span>}</td>
                      <td class="num"><Num m={tok} kind="count" /></td>
                      <td class="num">{r.measures.calls?.value ? <Num m={numMeasured(r.measures.calls)} kind="count" /> : <span class="muted">—</span>}</td>
                      <td class="num"><CostFigures cost={r.cost} showUnavailable empty="—" /></td>
                    </tr>
                  );
                })}
          </DataTable>
        )
      )}
    </div>
  );
}
