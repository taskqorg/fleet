// Projects configuration & resource analytics (docs/design-system/pages/projects.md).
import { useEffect, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  AreaChart, Button, Choice, CodeEditor, CostFigures, DetailSection, Empty, ErrorBanner, Icon,
  KvRow, ListDetail, Num, SearchField, SettingField, Totals, VirtualList, matchSearch,
  msMeasured, numMeasured, type ChartBucket, type ChartSeries, type TotalItem,
} from '../../../packages/ui/src/index.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';
import { op, useAlive, writeOnce, type Ops } from '../api.ts';
import { useDoc } from '../store.ts';
import { resourceTotals } from './resources.tsx';

type Bundle = { settings: Record<string, any>; contextFiles: { path: string; text: string }[] };
type AnalyticsReply = Ops['analytics_query']['reply'];
type AnalyticsDim = NonNullable<Ops['analytics_query']['request']['dimensions']>[number];
type AnalyticsFilter = NonNullable<Ops['analytics_query']['request']['filters']>;
type WindowKey = '24h' | '7d' | '30d';
type GrainKey = 'hour' | 'day' | 'week';
type ChartMetric = 'tokens' | 'executions' | 'cost';

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

const fresh = (): Bundle => ({
  settings: { id: '', name: '', contextFiles: ['rules.md'], roles: { pm: 'pm' }, repos: [], environments: [] },
  contextFiles: [{ path: 'rules.md', text: '' }],
});

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

function sumTokens(m: AnalyticsReply['rows'][number]['measures']): { value: number | null; basis: 'measured' | 'estimated' | 'subscription' | 'unavailable' } {
  const parts = [m.tokens_input, m.tokens_output, m.tokens_cache_read, m.tokens_cache_write, m.tokens_reasoning].filter(Boolean);
  if (!parts.length) return { value: null, basis: 'unavailable' };
  let sum = 0;
  let known = false;
  let basis: 'measured' | 'estimated' | 'subscription' | 'unavailable' = 'measured';
  for (const p of parts) {
    if (p.value != null) { sum += p.value; known = true; }
    if (p.basis === 'estimated') basis = 'estimated';
    else if (p.basis === 'unavailable' && !known) basis = 'unavailable';
  }
  return { value: known ? sum : null, basis: known ? basis : 'unavailable' };
}

export function Projects({ id }: { id?: string }) {
  const [, go] = useLocation();
  const [creating, setCreating] = useState(false);
  const [mobileAnalytics, setMobileAnalytics] = useState(false);
  const [query, setQuery] = useState('');
  const list = useDoc<any>('projects', () => op('project_list', {}), c => c.kind === 'definition');
  const rows = (list.data?.projects ?? []).filter((p: any) => matchSearch(query, `${p.name} ${p.id}`, { project: p.id }));
  const pane = (
    <>
      <header class="page-h">
        <h1>Projects</h1>
        <span class="grow" />
        <Button label="Analytics" icon="chart-no-axes-column" kind="quiet" onClick={() => { setCreating(false); setMobileAnalytics(true); go('/projects'); }} />
        <Button label="Create project" icon="plus" onClick={() => { setMobileAnalytics(false); setCreating(true); }} />
      </header>
      <SearchField label="Find project" value={query} onInput={setQuery} onSearch={() => {}} />
      {list.error ? <div role="alert">{list.error}</div> : !list.data ? <Empty text="Loading…" /> : (
        <VirtualList
          items={rows}
          getKey={(p: any) => p.id}
          rowHeight={52}
          follow="top"
          label="Projects"
          render={(p: any) => (
            <>
              <Icon name="folder-open" />
              <a
                class="title row-link"
                href={'/projects/' + encodeURIComponent(p.id)}
                onClick={e => { e.preventDefault(); setCreating(false); setMobileAnalytics(false); go('/projects/' + encodeURIComponent(p.id)); }}
              >
                {p.name}<span class="id">{p.id}</span>
              </a>
            </>
          )}
        />
      )}
      <span>{rows.length} of {list.data?.projects.length ?? '—'}</span>
    </>
  );
  const overview = (
    <article class="detail" data-ui="detail" tabIndex={-1} aria-label="Projects analytics">
      <header class="detail-h">
        {mobileAnalytics && <Button label="Close" icon="x" iconOnly onClick={() => setMobileAnalytics(false)} />}
        <h2 class="detail-title">Resource analytics</h2>
      </header>
      <ProjectAnalytics onOpenProject={pid => { setMobileAnalytics(false); go('/projects/' + encodeURIComponent(pid)); }} />
    </article>
  );
  return (
    <ListDetail
      list={pane}
      detail={creating || id ? (
        <ProjectEditor key={creating ? 'create' : id} id={creating ? undefined : id} onSaved={p => { setCreating(false); go('/projects/' + encodeURIComponent(p)); }} />
      ) : mobileAnalytics ? overview : undefined}
      idle={overview}
      detailKey={creating ? 'create' : id || (mobileAnalytics ? 'analytics' : undefined)}
    />
  );
}

function ProjectEditor({ id, onSaved }: { id?: string; onSaved: (id: string) => void }) {
  const [, go] = useLocation();
  const [bundle, setBundle] = useState<Bundle>(fresh);
  const [revision, setRevision] = useState(0);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [ready, setReady] = useState<any>(null);
  const [access, setAccess] = useState<any>(null);
  const [resources, setResources] = useState<Ops['project_context']['reply']['resources']>(undefined);
  const [repoSha, setRepoSha] = useState<string | null>(null);
  const [sourceState, setSourceState] = useState('');
  const requestId = useRef(crypto.randomUUID()), alive = useAlive();
  const [loaded, setLoaded] = useState(!id);
  const load = async () => {
    if (!id) return;
    try {
      const p: any = await op('project_context', { projectId: id });
      setBundle(p.content); setRevision(p.revision); setAccess(p.initialAccess);
      setResources(p.resources);
      setRepoSha(p.repoSha ?? null); setSourceState(p.sourceState ?? '');
      requestId.current = crypto.randomUUID(); setReady(null); setError(''); setLoaded(true);
    } catch (e: any) { setError(e.message); }
  };
  useEffect(() => { void load(); }, [id]);
  const setting = (key: string, value: any) => setBundle(b => ({ ...b, settings: { ...b.settings, [key]: value } }));
  const field = (label: string, value: string, save: (s: string) => void) => (
    <SettingField help="Edit this project field; Save project applies the complete version." label={label} value={value} status={{ kind: 'pending' }} now={Date.now()} onSave={save} />
  );
  const save = async () => {
    setBusy(true);
    try {
      const unknown = () => setError('unknown, retrying');
      const p: any = id
        ? await writeOnce('project_update', { projectId: id, requestId: requestId.current, baseRevision: revision, content: bundle, repoSha }, unknown, { signal: alive })
        : await writeOnce('project_create', { id: bundle.settings.id, requestId: requestId.current, content: bundle }, unknown, { signal: alive });
      const check: any = await op('project_context', { projectId: p.projectId });
      if (check.revision !== p.revision || check.contentSha !== p.contentSha) throw Error('Saved version changed; reload before editing.');
      setBundle(check.content); setRevision(check.revision); setAccess(check.initialAccess);
      setResources(check.resources);
      setRepoSha(check.repoSha ?? null); setSourceState(check.sourceState ?? '');
      requestId.current = crypto.randomUUID(); setError(''); onSaved(p.projectId);
    } catch (e: any) { setError(e.message); } finally { setBusy(false); }
  };
  const bindings = (kind: 'repos' | 'environments', title: string, keys: string[]) => {
    const items: any[] = bundle.settings[kind] ?? [];
    const change = (i: number, k: string, v: string) => setting(kind, items.map((item, n) => n === i ? { ...item, [k]: v } : item));
    return (
      <DetailSection title={title}>
        {items.map((item, i) => (
          <div key={i}>
            {keys.map(k => field(`${title} ${i + 1} ${{ id: 'ID', remote: 'URL', defaultBranch: 'Default branch', connection: 'Git connection', hostId: 'Host', checkoutPath: 'Checkout path', repo: 'Repository' }[k] ?? k}`, item[k] ?? '', v => change(i, k, v)))}
            <Button label={`Remove ${title} ${i + 1}`} icon="x" onClick={() => setting(kind, items.filter((_, n) => n !== i))} />
          </div>
        ))}
        <Button label={`Add ${title}`} icon="plus" onClick={() => setting(kind, [...items, kind === 'repos' ? { id: 'repo-' + (items.length + 1), remote: '', forge: 'github', defaultBranch: 'main' } : { hostId: '', checkoutPath: '' }])} />
      </DetailSection>
    );
  };
  if (!loaded) return error ? <ErrorBanner text={error} onRetry={() => void load()} /> : <Empty text="Loading project…" />;
  const ctxTotals = resources ? resourceTotals(resources, { hideUnavailable: false }) : [];
  return (
    <article class="detail" data-ui="detail">
      <header class="detail-h">
        <h2>{id ? bundle.settings.name : 'Create project'}</h2><span class="grow" />
        <Button label="Save project" icon="save" busy={busy} onClick={save} />
        {id && <Button label="Reload project" icon="rotate-cw" onClick={() => void load()} />}
      </header>
      {error && <div role="alert">{error}</div>}
      {sourceState === 'repository-changed' && <p>Source configuration changed. Saving keeps this edited project version.</p>}
      <DetailSection title="Context">
        {!id && field('Project ID', bundle.settings.id, v => setting('id', v))}
        {field('Name', bundle.settings.name, v => setting('name', v))}
        {bundle.contextFiles.map((f, i) => (
          <div key={f.path}>
            <CodeEditor label={`Project context ${f.path}`} value={f.text} onChange={text => setBundle(b => ({ ...b, contextFiles: b.contextFiles.map((old, n) => i === n ? { ...old, text } : old) }))} />
          </div>
        ))}
        <KvRow icon="folder-open" label="Saved version" value={revision ? String(revision) : 'Not saved'} />
      </DetailSection>
      {bindings('repos', 'Repository', ['id', 'remote', 'defaultBranch', 'connection'])}
      {bindings('environments', 'Environment', ['hostId', 'checkoutPath', 'repo'])}
      <DetailSection title="Agent roles">
        {Object.entries(bundle.settings.roles ?? {}).map(([role, profile]) => field(role, String(profile), v => setting('roles', { ...bundle.settings.roles, [role]: v })))}
      </DetailSection>
      {id && (
        <DetailSection title="Readiness">
          <Button label="Check readiness" icon="circle-check" onClick={() => op('project_readiness', { projectId: id }).then(setReady, e => setError(e.message))} />
          {ready && (
            <>
              {(ready.hosts ?? []).length === 0 ? <Empty text="No environment is configured." /> : (ready.hosts ?? []).map((h: any) =>
                h.executionId ? <ReadinessResult key={h.executionId} hostId={h.hostId} executionId={h.executionId} /> : (
                  <KvRow icon="panels-top-left" label={h.hostId ?? 'Environment'} value="Unavailable" meta={h.problem ?? 'Readiness check unavailable'} />
                )
              )}
            </>
          )}
          <Button label="Open PM conversation" icon="messages-square" onClick={() => location.assign('/conversations?new=' + encodeURIComponent(id))} />
        </DetailSection>
      )}
      {access && (
        <DetailSection title="Initial access">
          <KvRow icon="folder-open" label="Initial member" value={access.member ?? 'None'} />
          <KvRow icon="folder-open" label="State" value={!access.member ? 'None' : access.revoked ? 'Revoked' : 'Active'} />
          {access.member && !access.revoked && id && (
            <Button label="Revoke initial access" icon="shield" onClick={() => op('project_initial_access_revoke', { projectId: id, baseAccessVersion: access.version }).then(() => load(), e => setError(e.message))} />
          )}
          <p>Separate explicit grants and full administrator access remain effective.</p>
        </DetailSection>
      )}
      {id && ctxTotals.length > 0 && (
        <DetailSection title="Project resources">
          <Totals items={ctxTotals} />
        </DetailSection>
      )}
      {id && (
        <DetailSection title="Resource analytics">
          <ProjectAnalytics projectId={id} onOpenProject={pid => go('/projects/' + encodeURIComponent(pid))} />
        </DetailSection>
      )}
    </article>
  );
}

function ReadinessResult({ hostId, executionId }: { hostId: string; executionId: string }) {
  const result = useDoc<any>('project-readiness:' + executionId, () => op('execution_status', { executionId }), c => c.kind === 'event' || (c.kind === 'execution' && c.id === executionId));
  const latest = result.data?.readiness?.at(-1);
  return (
    <>
      <KvRow icon="panels-top-left" label={hostId} value={result.error ? 'Observation unavailable' : latest ? (latest.ok ? 'Ready' : 'Not ready') : result.data?.state ?? 'Check requested'} meta={result.error ?? (latest ? undefined : 'Awaiting readiness result')} />
      {(latest?.checks ?? []).map((check: any) => <KvRow icon="circle-check" label={check.check} value={check.status} meta={[check.target, check.reason].filter(Boolean).join(' · ')} />)}
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
    return out;
  });
  const [trend, setTrend] = useState<AnalyticsReply | null>(null);
  const [breakdown, setBreakdown] = useState<AnalyticsReply | null>(null);
  const [error, setError] = useState<string | null>(null);

  const now = Date.now();
  const from = now - WINDOW_MS[win];
  const filterKey = JSON.stringify(filters);

  useEffect(() => {
    let live = true;
    setError(null);
    op('analytics_query', {
      window: { from, to: now },
      dimensions: [grain],
      measures: ANALYTICS_MEASURES,
      filters,
      ...(projectId ? { projectId } : {}),
    }).then(
      r => { if (live) setTrend(r); },
      e => { if (live) setError(String(e?.message ?? e)); },
    );
    return () => { live = false; };
  }, [projectId, win, grain, filterKey]);

  useEffect(() => {
    let live = true;
    const range = bucket ? parseBucketMs(bucket, grain) : null;
    const bw = range ? { from: range[0], to: range[1] } : { from, to: now };
    op('analytics_query', {
      window: bw,
      dimensions: [dim],
      measures: ANALYTICS_MEASURES,
      filters,
      ...(projectId ? { projectId } : {}),
    }).then(
      r => { if (live) setBreakdown(r); },
      e => { if (live) setError(String(e?.message ?? e)); },
    );
    return () => { live = false; };
  }, [projectId, win, grain, dim, bucket, filterKey]);

  const changeWin = (w: WindowKey) => {
    setWin(w);
    setBucket(null);
    if (w === '24h' && grain === 'week') setGrain('hour');
    if (w !== '24h' && grain === 'hour') setGrain('day');
  };

  const dimsAvail = projectId ? DIMS.filter(d => d.id !== 'project') : DIMS;
  const activeDim = dimsAvail.some(d => d.id === dim) ? dim : dimsAvail[0].id;
  const trendRows = [...(trend?.rows ?? [])].sort((a, b) => (a.dimensions[grain] ?? '').localeCompare(b.dimensions[grain] ?? ''));
  const totalM = trend?.totals.measures;
  const totalTokens = totalM ? sumTokens(totalM) : null;
  const totalItems: TotalItem[] = trend ? [
    { label: 'Executions', m: numMeasured(totalM?.executions), kind: 'count', note: totalM?.failed?.value ? `${totalM.failed.value} failed` : undefined },
    { label: 'Wall', m: numMeasured(totalM?.wall_ms, 0.001), kind: 'duration' },
    ...(totalM?.cpu_ms?.value != null ? [{ label: 'CPU', m: numMeasured(totalM.cpu_ms, 0.001), kind: 'duration' } satisfies TotalItem] : []),
    ...(totalM?.peak_memory_bytes?.value != null ? [{ label: 'Peak memory', m: numMeasured(totalM.peak_memory_bytes), kind: 'bytes' } satisfies TotalItem] : []),
    ...(totalTokens && totalTokens.value != null ? [{ label: 'Tokens', m: totalTokens, kind: 'count' } satisfies TotalItem] : []),
    ...(totalM?.calls?.value ? [{ label: 'Tools', m: numMeasured(totalM.calls), kind: 'count' } satisfies TotalItem] : []),
    ...(trend.totals.cost.length ? [{ label: 'Cost', cost: trend.totals.cost, showUnavailable: true } satisfies TotalItem] : []),
  ] : [];

  const buckets: ChartBucket[] = trendRows.map(r => {
    const k = r.dimensions[grain] ?? '';
    const vTokens = sumTokens(r.measures).value ?? 0;
    const vRuns = r.measures.executions?.value ?? 0;
    const vCost = r.cost.filter(c => c.value != null).map(c => `${c.basis === 'subscription' ? '≈' : ''}${c.value!.toFixed(2)} ${c.currency ?? 'USD'}${c.basis === 'subscription' ? ' sub' : ''}`).join(', ');
    return {
      key: k,
      label: shortBucketLabel(k, grain),
      tip: `${k}: ${vRuns} runs · ${vTokens.toLocaleString('en')} tokens${vCost ? ` · ${vCost}` : ''}`,
    };
  });

  const valOf = (r: AnalyticsReply['rows'][number]): number => {
    if (metric === 'executions') return r.measures.executions?.value ?? 0;
    if (metric === 'cost') return r.cost.reduce((n, c) => n + (c.value ?? 0), 0);
    return sumTokens(r.measures).value ?? 0;
  };
  const points = trendRows.flatMap(r => {
    const range = parseBucketMs(r.dimensions[grain] ?? '', grain);
    return range ? [{ at: range[0], v: valOf(r) }] : [];
  });
  const chartMax = Math.max(1, ...points.map(p => p.v));
  const series: ChartSeries[] = [{ label: metric === 'executions' ? 'executions' : metric === 'cost' ? 'cost equivalents' : 'tokens', points, tone: 'd1', step: true }];
  const chartFormat = (v: number) => metric === 'cost' ? `$${v.toFixed(2)}` : metric === 'executions' ? String(Math.round(v)) : (v >= 1000 ? `${(v / 1000).toFixed(1)}k` : String(Math.round(v)));

  const filterChips = Object.entries(filters).flatMap(([k, vals]) => Array.isArray(vals) ? vals.map(v => ({ k: k as keyof AnalyticsFilter, v: String(v) })) : []);
  const toggleFilter = (d: AnalyticsDim, val: string) => {
    if (!val || val === '(none)' || d === 'pipeline_revision' || d === 'project') {
      if (d === 'project' && val) onOpenProject(val);
      return;
    }
    const key = d as keyof AnalyticsFilter;
    const cur = (filters[key] as string[] | undefined) ?? [];
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
        <span class="grow" />
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
              <button type="button" aria-label={`Remove ${f.k} filter ${f.v}`} onClick={() => setFilters({ ...filters, [f.k]: ((filters[f.k] as string[]) ?? []).filter(x => x !== f.v) })}><Icon name="x" /></button>
            </span>
          ))}
        </div>
      )}

      {error && <ErrorBanner text={`Analytics failed: ${error}`} />}
      {!trend && !error && <Empty text="Loading analytics…" />}
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

      {breakdown && (
        breakdown.rows.length === 0 ? <Empty text="No recorded analytics in this window." /> : (
          <div class="table-frame">
            <table class="limit-table">
              <thead>
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
              </thead>
              <tbody>
                {breakdown.rows.map(r => {
                  const keyVal = r.dimensions[activeDim] || '(none)';
                  const tok = sumTokens(r.measures);
                  const active = activeDim !== 'project' && Array.isArray((filters as any)[activeDim]) && (filters as any)[activeDim].includes(keyVal);
                  return (
                    <tr key={keyVal} class={active ? 'on' : undefined}>
                      <th scope="row">
                        <button type="button" class="seg" onClick={() => toggleFilter(activeDim, keyVal)} data-tip={activeDim === 'project' ? `Open project ${keyVal}` : `Filter by ${keyVal}`}>
                          <span class="mono">{keyVal}</span>
                        </button>
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
              </tbody>
            </table>
          </div>
        )
      )}
    </div>
  );
}
