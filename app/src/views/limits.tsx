// Limits (docs/design-system/pages/hosts.md "Limits", fleet-people-access.md section 8 and section 12): one card per
// harness and period with a chart, and a table per subscription. The Hosts page shows it while no host is open.
import { useState } from 'preact/hooks';
import { AreaChart, type ChartPoint, Button, Choice, FillBar, AgeChip, Empty, ErrorBanner, Icon, StateBadge, countdown, exact, type ChartSeries } from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useDoc } from '../store.ts';

const DAY = 864e5, HOUR = 36e5;
type Point = { at: number; left: number };

/** The recorded pool line: at each member reading, the weighted sum of every member's last known left. */
function recordedPool(pool: any, subs: any[]): ChartPoint[] {
  const members = subs.filter(s => pool.members.includes(s.id));
  // Members with no reading in range have no recorded line; the pool line covers the members that do.
  const hist = members.map(s => ({ w: s.planWeight, h: (s.periods.find((p: any) => p.period === pool.period)?.history ?? []) as Point[] })).filter(m => m.h.length > 0);
  const times = [...new Set(hist.flatMap(m => m.h.map(p => p.at)))].sort((a, b) => a - b);
  // Before a member's first reading in range its first value stands in (an older reading held), never 0.
  return times.map(t => ({ at: t, v: hist.reduce((sum, m) => { const last = [...m.h].reverse().find(p => p.at <= t) ?? m.h[0]; return sum + m.w * (last ? last.left : 0); }, 0) }));
}

/** Burn per hour between consecutive points (drops only; a reset adds nothing), as a step line. */
function burnLine(points: ChartPoint[]): ChartPoint[] {
  const out: ChartPoint[] = [];
  for (let i = 1; i < points.length; i++) {
    const h = (points[i].at - points[i - 1].at) / HOUR;
    out.push({ at: points[i - 1].at, v: h > 0 ? Math.max(0, points[i - 1].v - points[i].v) / h : 0 });
  }
  if (out.length) out.push({ at: points[points.length - 1].at, v: out[out.length - 1].v });
  return out;
}

const when = (e: any, now: number) => e.state === 'at' ? <time data-tip={exact(e.at)} aria-label={countdown(e.at, now).long}>{countdown(e.at, now).text}</time>
  : <span class="muted">{e.state === 'not-before-end' ? 'not before the line ends' : 'unknown'}</span>;

export function Limits() {
  const [win, setWin] = useState('3');
  const [shift, setShift] = useState(0);
  const [mode, setMode] = useState('left');
  const [busy, setBusy] = useState<string | null>(null);
  const centre = Math.floor(Date.now() / HOUR) * HOUR + shift * DAY;
  const from = centre - 7 * DAY, to = centre + 7 * DAY;
  // Readings arrive as events, which the change feed does not tell apart: the doc reloads every 30 s and on account
  // and setting changes.
  const { data, error } = useDoc<any>(`subscription-limits:${win}:${centre}`, async () => await op('subscription_limits', { windowHours: Number(win), from, to }),
    c => c.kind === 'managed-account' || c.kind === 'setting-change', 30000);
  const [note, setNote] = useState<string | null>(null);
  const refresh = async (id?: string) => {
    setBusy(id ?? '*'); setNote(null);
    try {
      const r: any = await op('subscription_limits_refresh', id ? { subscriptionId: id } : {});
      setNote([r.sent.length ? `${r.sent.length} sent` : '', ...r.skipped.map((x: any) => `${x.subscriptionId}: ${x.reason}`)].filter(Boolean).join(' · ') || 'nothing to refresh');
    } catch (e: any) { setNote(`Refresh failed: ${e?.message ?? e}`); } finally { setBusy(null); }
  };
  const now = data?.serverNow ?? Date.now();
  const periods: string[] = data ? [...new Set<string>(data.subscriptions.flatMap((s: any) => s.periods.map((p: any) => p.period)))].sort() : [];
  const pct = (v: number) => `${Math.round(v)}%`;
  return <section class="limits" aria-label="Limits">
    <header class="page-h"><h1><Icon name="gauge" /> Limits</h1><span class="grow" />
      <Choice label="Burn window" options={[{ id: '1', label: '1h' }, { id: '3', label: '3h' }, { id: '6', label: '6h' }]} value={win} onChange={setWin} />
      <Choice label="Chart" options={[{ id: 'left', label: 'Left' }, { id: 'burn', label: 'Burn' }]} value={mode} onChange={setMode} />
      <Button label="Earlier" icon="arrow-left" iconOnly onClick={() => setShift(shift - 1)} />
      <Button label="Today" onClick={() => setShift(0)} pressed={shift === 0} />
      <Button label="Later" icon="arrow-right" iconOnly onClick={() => setShift(shift + 1)} />
      <Button label="Refresh all" icon="rotate-cw" iconOnly busy={busy === '*'} onClick={() => refresh()} />
    </header>
    <p class="limits-note muted" aria-live="polite">{note ?? ''}</p>
    {error && <ErrorBanner text={`Did not load: ${error}`} />}
    {!data ? <Empty text="Loading…" /> : <>
    <div class="limit-cards">
      {data.pools.length === 0 && <Empty text="No readings yet: a run or a refresh reports them." />}
      {data.pools.map((p: any) => {
        const recorded = recordedPool(p, data.subscriptions), projected = (p.projection as Point[]).map(q => ({ at: q.at, v: q.left }));
        const series: ChartSeries[] = mode === 'left'
          ? [{ label: 'recorded', points: recorded, tone: 'd1' }, { label: 'projected', points: projected, tone: 'd1', projected: true }]
          : [{ label: 'burn', points: burnLine(recorded), tone: 'd2', step: true },
            ...(p.burn !== null ? [{ label: 'burn', points: [{ at: now, v: p.burn }, { at: to, v: p.burn }], tone: 'd2' as const, projected: true, step: true }] : [])];
        const max = mode === 'left' ? p.capacity : Math.max(1, ...series.flatMap(s => s.points.map(q => q.v)));
        return <article key={p.harness + p.period} class="panel limit-card" aria-label={`${p.harness} ${p.period}`}>
          <header class="panel-h"><h2>{p.harness} <span class="id">{p.period}</span></h2><span class="grow" />
            {p.leftPct !== null ? <span class="num">{pct(p.leftPct)}</span> : <span class="muted">unknown</span>}</header>
          <dl class="limit-facts">
            <dt>Next reset</dt><dd>{p.nextReset.at ? <><time data-tip={exact(p.nextReset.at)}>{countdown(p.nextReset.at, now).text}</time> {p.nextReset.restore !== null ? <span class="num">+{Math.round(p.nextReset.restore)}</span> : <span class="muted">restore unknown</span>}</> : <span class="muted">unknown</span>}</dd>
            <dt>First empty</dt><dd>{when(p.firstEmpty, now)}</dd>
            <dt>Pool empty</dt><dd>{when(p.poolEmpty, now)}</dd>
            <dt>Burn</dt><dd>{p.burn !== null ? <span class="num">{p.burn.toFixed(1)}/h</span> : <span class="muted">unknown</span>}</dd>
            {p.unknown.length > 0 && <><dt>Unknown</dt><dd>{p.unknown.length}</dd></>}
          </dl>
          <AreaChart label={`${p.harness} ${p.period} ${mode}`} series={series} from={from} to={to} now={now} max={max}
            value={v => mode === 'left' ? pct(v / (p.capacity || 1) * 100) : `${v.toFixed(1)}/h`} />
        </article>;
      })}
    </div>
    <section class="panel"><header class="panel-h"><h2>Subscriptions</h2></header>
      <div class="table-frame"><table class="limit-table">
        <thead><tr><th scope="col">Subscription</th>{periods.map(p => <th key={p} scope="col">{p}</th>)}<th scope="col"><span class="sr-only">Refresh</span></th></tr></thead>
        <tbody>{data.subscriptions.map((s: any) => <tr key={s.id}>
          <th scope="row"><StateBadge family="subscription" state={s.intake === 'paused' ? 'paused' : 'all'} compact /> <span class="title">{s.name}</span> <span class="id">{s.harness}</span>{s.planWeight !== 1 && <span class="num muted"> ×{s.planWeight}</span>}</th>
          {periods.map(name => {
            const p = s.periods.find((x: any) => x.period === name);
            if (!p) return <td key={name} class="muted">—</td>;
            return <td key={name}><span class="limit-cell">
              {p.left !== null ? <FillBar total={100} segments={[{ label: 'used', bytes: 100 - p.left }]} warnAt={s.lowPct ?? undefined} unit="percent" /> : <span class="muted">{p.status ?? 'unknown'}</span>}
              {p.resetsAt && <time data-tip={exact(p.resetsAt)} aria-label={`resets ${countdown(p.resetsAt, now).long}`}>{countdown(p.resetsAt, now).text}</time>}
              {p.pace !== 'unknown' && <span class={`chip${p.pace === 'ahead' ? ' t-attention' : ''}`} data-tip="pace against the period's elapsed share">{p.pace}</span>}
              {p.low && <span class="chip t-danger"><Icon name="triangle-alert" />Low</span>}
              {p.readingAt && <AgeChip t={p.readingAt} now={now} />}
              <span class="muted">{p.source}</span>
            </span></td>;
          })}
          <td><Button label={`Refresh ${s.name}`} icon="rotate-cw" iconOnly busy={busy === s.id} onClick={() => refresh(s.id)} /></td>
        </tr>)}</tbody>
      </table></div>
    </section>
    </>}
  </section>;
}
