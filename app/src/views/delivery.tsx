// Delivery (docs/design-system/pages/delivery.md): pull requests of every connected project by delivery stage, from
// open to installed on hosts. One card per stage in pipeline order: the count now, the oldest age (red past the stage's
// limit), the count over the last 24 hours, and the pull requests in it. Data: delivery_status (cached GitHub reads and
// recorded counts in the coordinator). The project focus (OR-34) filters items and samples by project. The trend panel:
// P50/P90 per stage per day against the rolling 7-day baseline, and the open regressions (delivery_baseline,
// delivery_regressions; docs/designs/fleet-delivery-pipeline.md, Delivery health).
import { AreaChart, type ChartSeries, AgeChip, Empty, ErrorBanner, Icon, Num, StatStrip, type IconName, type Tone } from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useDoc } from '../store.ts';
import { focusIds, prefsRead } from '../prefs.ts';

const HOUR = 36e5;
// alert: seconds in the stage after which the oldest pull request needs a look; none for stages people work in.
const STAGES: { id: string; label: string; icon: IconName; alert?: number }[] = [
  { id: 'pr_open', label: 'Open', icon: 'git-pull-request' },
  { id: 'pr_ci', label: 'PR CI', icon: 'circle-dashed', alert: 1800 },
  { id: 'mq_wait', label: 'Queue wait', icon: 'list-ordered', alert: 900 },
  { id: 'mq_ci', label: 'Queue CI', icon: 'git-merge', alert: 1800 },
  { id: 'releasing', label: 'Releasing', icon: 'rotate-cw', alert: 1200 },
  { id: 'installing', label: 'Installing', icon: 'download', alert: 1200 },
  { id: 'installed', label: 'Installed 24h', icon: 'check-check' },
  { id: 'merged', label: 'Merged 24h', icon: 'check-check' },
];
const SHOWN = 6;
const DAY = 864e5;
// Trend stages (DeliveryPr durations), in pipeline order.
const TREND: { key: string; label: string; icon: IconName }[] = [
  { key: 'pr_ci', label: 'PR CI', icon: 'circle-dashed' }, { key: 'queue', label: 'Queue', icon: 'git-merge' },
  { key: 'release', label: 'Release', icon: 'rotate-cw' }, { key: 'install', label: 'Exact install', icon: 'download' },
  { key: 'availability', label: 'Change available', icon: 'download' },
  { key: 'time_to_available', label: 'Time to available', icon: 'clock' },
  { key: 'lead', label: 'Lead', icon: 'git-pull-request' },
];
const sec = (ms: number) => ({ value: Math.round(ms / 1000), basis: 'measured' as const });

// Trend: one card per stage, P90 and P50 per day as areas over the rolling baseline's P50 (striped); the numbers of the
// last day with values and the baseline stay in the head (OR-19). Regressions of the last check list above the cards.
function Trend({ now }: { now: number }) {
  const { data, error } = useDoc<any>('delivery-trend', () => Promise.all([op('delivery_baseline', { days: 14 }), op('delivery_regressions', {}), op('delivery_history', {})]).then(([b, r, h]) => ({ b, r, h })), () => false, 300000);
  const head = <header class="panel-h"><h2><Icon name="activity" /> Trend, 14 days</h2><span class="grow" />
    {data?.r?.checkedAt && <AgeChip t={Date.parse(data.r.checkedAt)} now={now} alertAfter={26 * 3600} alertLabel="regression check is stale" />}</header>;
  // The history is the fleet repository's: hidden for a person without the fleet project or focused away from it.
  const focus = focusIds();
  if (error?.includes('404') || error?.includes('Unknown project') || focus && focus.length && !focus.includes('fleet')) return null;
  if (!data) return <section class="panel delivery-trend" aria-label="Delivery trend">{head}{error ? <ErrorBanner text={`Trend did not load: ${error}`} /> : <Empty text="Loading…" />}</section>;
  const days: any[] = data.b.trend;
  const from = days.length ? Date.parse(days[0].day + 'T00:00:00Z') : now - 14 * DAY;
  // The baseline the last check compared with (the 7 days before it, or the named snapshot), else the rolling one.
  const baseline = data.r.baseline ?? data.b.rolling;
  const incompatible = baseline.metricBasis !== 'exact-install-v1';
  const base = new Map<string, any>(baseline.stats.filter((s: any) => !incompatible || !['install', 'total'].includes(s.key)).map((s: any) => [s.key, s]));
  const regs: any[] = data.r.regressions;
  // Keys without a card (enqueue, merge, ui, total, run, job:<name>, wait:<name>) show as the record names them.
  const label = (k: string) => TREND.find(t => t.key === k)?.label ?? k;
  return <section class="panel delivery-trend" aria-label="Delivery trend">
    {head}
    <p class="muted">Exact install samples require healthy observations of that version. Superseded releases establish change availability; legacy install observations have unknown exact evidence.</p>
    {incompatible && <p class="muted">Exact install and total baseline comparisons unavailable: incompatible historical basis.</p>}
    {regs.length === 0 ? <p class="muted">No regression against the baseline.</p> : <ul class="kv">
      {regs.map(r => <li key={r.key + r.metric} class="t-danger">
        <Icon name="triangle-alert" label="regression" />
        <span class="grow">{label(r.key)} {r.metric.toUpperCase()} <Num m={sec(r.currentMs)} kind="duration" />, baseline <Num m={sec(r.baselineMs)} kind="duration" /></span>
        {r.firstPr && <a class="id" href={r.links.find((l: string) => l.endsWith(`/pull/${r.firstPr}`)) ?? r.links[0]} target="_blank" rel="noopener">#{r.firstPr}</a>}
        {r.compareUrl && <a href={r.compareUrl} target="_blank" rel="noopener" data-tip="Changes since the last pull request under the threshold"><Icon name="git-merge" label="compare" /></a>}
      </li>)}
    </ul>}
    <details><summary>Release observations</summary>
      <ul class="kv">{[...(data.h?.releases ?? [])].reverse().slice(0, 20).map((r: any) => <li key={r.tag}>
        <span class="id">{r.tag}</span>
        <span class="grow">{Object.entries(r.installed ?? {}).map(([host, at]) => `${host}: ${r.installedVersions?.[host] ? 'exact healthy install' : 'legacy observation, exact install unknown'} at ${at}`).join('; ') || 'Exact install unknown'}</span>
        <span>{Object.entries(r.available ?? {}).map(([host, at]) => `${host}: change available via ${r.availabilityVersions?.[host] ?? 'unknown version'} at ${at}`).join('; ')}</span>
        <span>{Object.entries(r.updateObservations ?? {}).map(([host, u]: [string, any]) => `${host}: ${u.version} ${u.state} at ${u.at}${u.error ? `: ${u.error}` : ''}`).join('; ')}</span>
      </li>)}</ul>
    </details>
    <div class="limit-cards">
      {TREND.map(t => {
        const pts = (q: 'p50Ms' | 'p90Ms') => { const xs = days.flatMap(d => { const s = d.stats.find((x: any) => x.key === t.key); return s ? [{ at: Date.parse(d.day + 'T00:00:00Z'), v: s[q] / 6e4 }] : []; }); const today = days.length ? Date.parse(days[days.length - 1].day + 'T00:00:00Z') : 0; return xs.length && xs[xs.length - 1].at === today ? [...xs, { at: now, v: xs[xs.length - 1].v }] : xs; };
        // A day without values is left out, never drawn as 0.
        const b = base.get(t.key);
        const last = [...days].reverse().map(d => d.stats.find((s: any) => s.key === t.key)).find(Boolean);
        const p50 = pts('p50Ms'), p90 = pts('p90Ms');
        const bl = b ? [{ at: from, v: b.p50Ms / 6e4 }, { at: now, v: b.p50Ms / 6e4 }] : [];
        const slow = regs.some(r => r.key === t.key);
        return <article key={t.key} class="panel limit-card" aria-label={t.label}>
          <header class="panel-h"><h2><Icon name={t.icon} /> {t.label}</h2><span class="grow" />
            {last && <span class={slow ? 't-danger' : undefined} data-tip="P50 and P90, the last day with values">P50 <Num m={sec(last.p50Ms)} kind="duration" /> P90 <Num m={sec(last.p90Ms)} kind="duration" /></span>}
            {b && <span class="muted" data-tip="Baseline P50 of the regression check">base <Num m={sec(b.p50Ms)} kind="duration" /></span>}</header>
          <AreaChart label={`${t.label} per day in minutes`} from={from} to={now} now={now} value={v => `${Math.round(v)}m`}
            max={Math.max(1, ...p90.map(p => p.v), ...bl.map(p => p.v))}
            series={[{ label: 'P90', points: p90, tone: 'd2', step: true }, { label: 'P50', points: p50, tone: 'd1', step: true }, { label: 'baseline P50', points: bl, tone: 'd4', pattern: true, projected: true }]} />
        </article>;
      })}
    </div>
  </section>;
}
// The stacked chart's layers, bottom up in pipeline order: PR stages in the two greys, the merge queue in amber, the
// release tail in green; the queue's and the tail's second stage striped (tokens.md: four data hues, never new ones).
const LAYERS: Record<string, Pick<ChartSeries, 'tone' | 'pattern'>> = {
  pr_open: { tone: 'd2' }, pr_ci: { tone: 'd1' }, mq_wait: { tone: 'd4' }, mq_ci: { tone: 'd4', pattern: true },
  releasing: { tone: 'd3' }, installing: { tone: 'd3', pattern: true },
};

export function Delivery() {
  // GitHub reads run every few minutes on the coordinator, not on change rows: re-read every 60 s.
  const { data, error } = useDoc<any>(prefsRead.value ? 'delivery:' + (focusIds() ?? []).join(',') : null, () => op('delivery_status', { projectIds: focusIds() }), () => false, 60000);
  const now = Date.now();
  const head = <header class="page-h"><h1><Icon name="git-merge" /> Delivery</h1><span class="grow" />
    {data?.source === 'public' && <span class="chip-age t-attention" data-tip="No github key in API keys: unauthenticated reads, the fleet repository only">public GitHub</span>}
    {data?.refreshedAt && <AgeChip t={Date.parse(data.refreshedAt)} now={now} alertAfter={900} alertLabel="GitHub read is stale" />}
  </header>;
  if (!data) return <>{head}{error ? <ErrorBanner text={`Delivery did not load: ${error}`} /> : <Empty text="Loading…" />}</>;
  const items: any[] = data.items;
  // Samples recorded before per-repository samples (no repo) came once per project: one per read and key.
  const samples: any[] = [...new Map(data.samples.map((s: any) => [`${s.at} ${s.repo ?? s.projectId}`, s])).values()];
  const used = STAGES.filter(s => s.id !== 'merged' || items.some(i => i.stage === 'merged'));
  const of = (id: string) => items.filter(i => i.stage === id).sort((a, b) => a.since.localeCompare(b.since));
  // One line per stage: the sum over projects at each read.
  const sums = new Map<string, Record<string, number>>();
  for (const s of samples) { const m = sums.get(s.at) ?? {}; for (const k in s.counts) m[k] = (m[k] ?? 0) + s.counts[k]; sums.set(s.at, m); }
  const at = [...sums.keys()].sort();
  const lines = Object.fromEntries(used.map(s => [s.id, at.map(t => ({ at: Date.parse(t), v: sums.get(t)![s.id] ?? 0 }))]));
  // The 24-hour totals (installed, merged) grow all day: they get their own scale, so the waiting stages stay readable.
  const done = (id: string) => id === 'installed' || id === 'merged';
  const scale = (d: boolean) => Math.max(1, ...used.filter(s => done(s.id) === d).flatMap(s => lines[s.id].map(p => p.v)));
  const max = scale(false), doneMax = scale(true);
  const late = (s: typeof STAGES[number]) => { const o = of(s.id)[0]; return !!(s.alert && o && now - Date.parse(o.since) >= s.alert * 1000); };
  const first = used.find(late);
  return <section class="delivery" aria-label="Delivery">
    {head}
    {data.error && <ErrorBanner text={`GitHub: ${data.error}`} />}
    <StatStrip label="Pull requests by stage" stats={[
      ...used.map(s => ({ label: s.label, icon: s.icon, value: { value: of(s.id).length, basis: 'measured' as const }, tone: (late(s) ? 'danger' : undefined) as Tone | undefined })),
      { label: 'workers waiting to land', icon: 'clock', value: { value: data.landing, basis: 'measured' as const } },
    ]} />
    <p class="muted" aria-live="polite">{first ? `Waiting starts at ${first.label}.` : 'Nothing waits past its limit.'}</p>
    <section class="panel delivery-flow" aria-label="Pull requests in flight over 24 hours">
      <header class="panel-h"><h2>In flight, 24 hours</h2></header>
      <AreaChart label="Pull requests in flight by stage over 24 hours" stacked from={now - 24 * HOUR} to={now} now={now} value={v => String(Math.round(v))}
        max={Math.max(1, ...at.map((_, j) => used.filter(s => LAYERS[s.id]).reduce((n, s) => n + lines[s.id][j].v, 0)))}
        series={used.filter(s => LAYERS[s.id]).map(s => ({ label: s.label, points: lines[s.id], step: true, ...LAYERS[s.id] }))} />
    </section>
    <Trend now={now} />
    <div class="limit-cards">
      {used.map(s => { const list = of(s.id); return <article key={s.id} class="panel limit-card" aria-label={s.label}>
        <header class="panel-h"><h2><Icon name={s.icon} /> {s.label}</h2><span class="grow" />
          {list[0] && s.alert && <AgeChip t={Date.parse(list[0].since)} now={now} alertAfter={s.alert} alertLabel="waits too long" />}
          <Num m={{ value: list.length, basis: 'measured' }} kind="count" /></header>
        <AreaChart label={`${s.label} over 24 hours`} series={[{ label: s.label, points: lines[s.id], tone: 'd1', step: true }]} from={now - 24 * HOUR} to={now} now={now} max={done(s.id) ? doneMax : max} value={v => String(Math.round(v))} />
        {list.length === 0 ? <Empty text="None." /> : <ul class="kv">
          {list.slice(0, SHOWN).map(i => <li key={i.repo + i.number}>
            <Icon name={s.icon} label={s.label} />
            <a class="row-link grow" href={i.url} target="_blank" rel="noopener">{i.title || `#${i.number}`} <span class="id">#{i.number}</span></a>
            {i.release && <span class="id">{i.release}</span>}
            <AgeChip t={Date.parse(i.since)} now={now} alertAfter={s.alert} alertLabel="waits too long" />
          </li>)}
          {list.length > SHOWN && <li class="muted">{list.length - SHOWN} more</li>}
        </ul>}
      </article>; })}
    </div>
  </section>;
}
