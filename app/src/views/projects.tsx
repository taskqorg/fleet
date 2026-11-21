// Projects configuration & resource analytics (docs/design-system/pages/projects.md).
import { useMemo, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  AreaChart, Button, Choice, CostFigures, DataTable, Empty, ErrorBanner, Icon, ListDetail, Num, SearchField,
  Totals, VirtualList, matchSearch, catalogueRow, numMeasured,
  type ChartBucket, type ChartSeries, type Measured, type TotalItem,
} from '../../../packages/ui/src/index.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';
import type { Ops } from '../api.ts';
import { useOp } from '../store.ts';
import { ProjectEditor, str } from './projects-editor.tsx';

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

/** Projects catalogue screen with resource analytics overview and configuration editor detail pane. */
export function Projects({ id }: { id?: string }) {
  const [, go] = useLocation();
  const [creating, setCreating] = useState(false);
  const [mobileAnalytics, setMobileAnalytics] = useState(false);
  const [query, setQuery] = useState('');
  const list = useOp('project_list', {});
  const rows = (list.data?.projects ?? []).map(p => ({ id: str(p.id), name: str(p.name) })).filter(p => matchSearch(query, `${p.name} ${p.id}`, { project: p.id }));
  const pane = (
    <>
      <header class="page-h">
        <span class="grow" />
        <Button label="Analytics" icon="chart-no-axes-column" kind="quiet" onClick={() => { setCreating(false); setMobileAnalytics(true); go('/projects'); }} />
        <Button label="Create project" icon="plus" onClick={() => { setMobileAnalytics(false); setCreating(true); }} />
      </header>
      <SearchField label="Find project" value={query} onInput={setQuery} onSearch={() => {}} />
      {list.error ? <div role="alert">{list.error}</div> : !list.data ? <Empty text="Loading…" /> : (
        <VirtualList
          items={rows}
          getKey={p => p.id}
          rowHeight={catalogueRow()}
          follow="top"
          label="Projects"
          columns="1.25rem minmax(0,1fr)"
          render={p => (
            <>
              {/* One line: the icon, the name, then its id apart from it (it used to run into the name: "cyprus.bzcs"). */}
              <span class="lead"><Icon name="folder-open" /></span>
              <span class="tt">
                <a
                  class="title row-link"
                  href={'/projects/' + encodeURIComponent(p.id)}
                  onClick={e => { e.preventDefault(); setCreating(false); setMobileAnalytics(false); go('/projects/' + encodeURIComponent(p.id)); }}
                >{p.name || p.id}</a>
                {p.name && p.name.toLowerCase() !== p.id.toLowerCase() && <span class="id">{p.id}</span>}
              </span>
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
        <ProjectEditor
          key={creating ? 'create' : id}
          id={creating ? undefined : id}
          onSaved={p => { setCreating(false); go('/projects/' + encodeURIComponent(p)); }}
          renderAnalytics={pid => <ProjectAnalytics projectId={pid} onOpenProject={target => go('/projects/' + encodeURIComponent(target))} />}
        />
      ) : mobileAnalytics ? overview : undefined}
      idle={overview}
      detailKey={creating ? 'create' : id || (mobileAnalytics ? 'analytics' : undefined)}
    />
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
