// Delivery (docs/design-system/pages/delivery.md): pull requests of every connected project by delivery stage, from
// open to installed on hosts. One card per stage in pipeline order: the count now, the oldest age (red past the stage's
// limit), the count over the last 24 hours, and the pull requests in it. Data: delivery_status (cached GitHub reads and
// recorded counts in the coordinator). A project focus later filters items and samples by projectId.
import { AreaChart, AgeChip, Empty, ErrorBanner, Icon, Num, StatStrip, type IconName, type Tone } from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useDoc } from '../store.ts';

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

export function Delivery() {
  // GitHub reads run every few minutes on the coordinator, not on change rows: re-read every 60 s.
  const { data, error } = useDoc<any>('delivery', () => op('delivery_status', {}), () => false, 60000);
  const now = Date.now();
  const head = <header class="page-h"><h1><Icon name="git-merge" /> Delivery</h1><span class="grow" />
    {data?.source === 'public' && <span class="chip-age t-attention" data-tip="No github key in API keys: unauthenticated reads, the fleet repository only">public GitHub</span>}
    {data?.refreshedAt && <AgeChip t={Date.parse(data.refreshedAt)} now={now} alertAfter={900} alertLabel="GitHub read is stale" />}
  </header>;
  if (!data) return <>{head}{error ? <ErrorBanner text={`Delivery did not load: ${error}`} /> : <Empty text="Loading…" />}</>;
  const items: any[] = data.items;
  const used = STAGES.filter(s => s.id !== 'merged' || items.some(i => i.stage === 'merged'));
  const of = (id: string) => items.filter(i => i.stage === id).sort((a, b) => a.since.localeCompare(b.since));
  // One line per stage: the sum over projects at each read.
  const sums = new Map<string, Record<string, number>>();
  for (const s of data.samples) { const m = sums.get(s.at) ?? {}; for (const k in s.counts) m[k] = (m[k] ?? 0) + s.counts[k]; sums.set(s.at, m); }
  const at = [...sums.keys()].sort();
  const lines = Object.fromEntries(used.map(s => [s.id, at.map(t => ({ at: Date.parse(t), v: sums.get(t)![s.id] ?? 0 }))]));
  const max = Math.max(1, ...Object.values(lines).flat().map(p => p.v));
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
    <div class="limit-cards">
      {used.map(s => { const list = of(s.id); return <article key={s.id} class="panel limit-card" aria-label={s.label}>
        <header class="panel-h"><h2><Icon name={s.icon} /> {s.label}</h2><span class="grow" />
          {list[0] && s.alert && <AgeChip t={Date.parse(list[0].since)} now={now} alertAfter={s.alert} alertLabel="waits too long" />}
          <Num m={{ value: list.length, basis: 'measured' }} kind="count" /></header>
        <AreaChart label={`${s.label} over 24 hours`} series={[{ label: s.label, points: lines[s.id], tone: 'd1', step: true }]} from={now - 24 * HOUR} to={now} now={now} max={max} value={v => String(Math.round(v))} />
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
