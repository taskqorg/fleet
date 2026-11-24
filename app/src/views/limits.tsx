// Limits (docs/design-system/pages/hosts.md "Limits", fleet-people-access.md section 8 and section 12): one card per
// harness and period with a chart, and a table per subscription. Rendered inside the Infrastructure work area on /hosts/limits.
import { createMemo, createSignal, For, Show } from 'solid-js';
import { AreaChart, type ChartPoint, Button, Choice, DataTable, FillBar, AgeChip, Empty, ErrorBanner, Icon, Skeleton, StateBadge, countdown, exact, percent, type ChartSeries } from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useOp } from '../store.ts';
import type { LimitPool, LimitSubscription, LimitEmpty } from '../../../contracts/generated/ops.ts';

const DAY = 864e5, HOUR = 36e5;
/** A subscription's name (stored as written; the id when it has none). */
const nameOf = (s: LimitSubscription) => typeof s.name === 'string' ? s.name : s.id;

/** The recorded pool line: at each member reading, the weighted sum of every member's last known left. */
function recordedPool(pool: LimitPool, subs: LimitSubscription[]): ChartPoint[] {
  const members = subs.filter(s => pool.members.includes(s.id));
  // Members with no reading in range have no recorded line; the pool line covers the members that do.
  const hist = members.map(s => ({ w: s.planWeight, h: s.periods.find(p => p.period === pool.period)?.history ?? [] })).filter(m => m.h.length > 0);
  const times = [...new Set(hist.flatMap(m => m.h.map(p => p.at)))].sort((a, b) => a - b);
  // Before a member's first reading in range its first value stands in (an older reading held), never 0.
  return times.map(t => ({ at: t, v: hist.reduce((sum, m) => { const last = m.h.findLast(p => p.at <= t) ?? m.h[0]; return sum + m.w * last.left; }, 0) }));
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

const when = (e: LimitEmpty, now: number) => e.state === 'at' && e.at !== null
  ? <time data-tip={exact(e.at)} aria-label={countdown(e.at, now).long}>{countdown(e.at, now).text}</time>
  : e.state === 'not-before-end'
    ? <span class="muted">not before the line ends</span>
    : <span class="muted" data-tip="Not enough readings to project">—</span>;

export function Limits(props: { onClose?: () => void } = {}) {
  const [win, setWin] = createSignal('3');
  const [shift, setShift] = createSignal(0);
  const [mode, setMode] = createSignal('left');
  const [busy, setBusy] = createSignal<string | null>(null);
  const centre = createMemo(() => Math.floor(Date.now() / HOUR) * HOUR + shift() * DAY);
  const from = createMemo(() => centre() - 7 * DAY);
  const to = createMemo(() => centre() + 7 * DAY);
  // Readings arrive as events, which the change feed does not tell apart: the doc reloads every 30 s and on account
  // and setting changes.
  const limitsOp = useOp('subscription_limits', () => ({ windowHours: Number(win()), from: from(), to: to() }), { every: 30000 });
  const [note, setNote] = createSignal<string | null>(null);
  const refresh = async (id?: string) => {
    setBusy(id ?? '*'); setNote(null);
    try {
      const r = await op('subscription_limits_refresh', id ? { subscriptionId: id } : {});
      setNote([r.sent.length ? `${r.sent.length} sent` : '', ...r.skipped.map(x => `${x.subscriptionId}: ${x.reason}`)].filter(Boolean).join(' · ') || 'nothing to refresh');
    } catch (e) { setNote(`Refresh failed: ${e instanceof Error ? e.message : String(e)}`); } finally { setBusy(null); }
  };
  const poolSeries = createMemo(() => {
    const map = new Map<string, { recorded: ChartPoint[]; burn: ChartPoint[] }>();
    const data = limitsOp.data;
    if (!data) return map;
    for (const p of data.pools) {
      const recorded = recordedPool(p, data.subscriptions);
      map.set(`${p.harness}\0${p.period}`, { recorded, burn: burnLine(recorded) });
    }
    return map;
  });
  const now = createMemo(() => limitsOp.data?.serverNow ?? Date.now());
  const periods = createMemo<string[]>(() =>
    limitsOp.data ? [...new Set<string>(limitsOp.data.subscriptions.flatMap(s => s.periods.map(p => p.period)))].sort() : [],
  );
  return (
    <article class="detail stack" data-ui="detail" tabindex={-1} aria-label="Accounts & Limits">
      <header class="detail-h">
        <Show when={props.onClose}>{onClose => <Button label="Close" icon="x" iconOnly onClick={onClose()} />}</Show>
        <h2 class="detail-title">Accounts & Limits</h2>
        <span class="grow" />
        <Button label="Refresh all" icon="rotate-cw" iconOnly busy={busy() === '*'} onClick={() => refresh()} />
      </header>
      <div class="res-bar">
        <Choice label="Burn window" options={[{ id: '1', label: '1h' }, { id: '3', label: '3h' }, { id: '6', label: '6h' }]} value={win()} onChange={setWin} />
        <Choice label="Chart" options={[{ id: 'left', label: 'Left' }, { id: 'burn', label: 'Burn' }]} value={mode()} onChange={setMode} />
        <span class="acts">
          <Button label="Earlier" icon="arrow-left" iconOnly onClick={() => setShift(s => s - 1)} />
          <Button label="Today" onClick={() => setShift(0)} pressed={shift() === 0} />
          <Button label="Later" icon="arrow-right" iconOnly onClick={() => setShift(s => s + 1)} />
        </span>
      </div>
      <p class="limits-note muted" aria-live="polite">{note() ?? ''}</p>
      <Show when={limitsOp.error}>{err => <ErrorBanner text={`Did not load: ${err()}`} onRetry={limitsOp.reload} />}</Show>
      <Show when={limitsOp.data} fallback={!limitsOp.error ? <Skeleton rows={5} /> : undefined}>{data => <>
        <div class="limit-cards">
          <Show when={data().pools.length === 0}><Empty text="No readings yet: a run or a refresh reports them." /></Show>
          <For each={data().pools}>{p => {
            const series = createMemo<ChartSeries[]>(() => {
              const cached = poolSeries().get(`${p.harness}\0${p.period}`) ?? { recorded: [], burn: [] };
              const projected = p.projection.map(q => ({ at: q.at, v: q.left }));
              return mode() === 'left'
                ? [{ label: 'recorded', points: cached.recorded, tone: 'd1' }, { label: 'projected', points: projected, tone: 'd1', projected: true }]
                : [{ label: 'burn', points: cached.burn, tone: 'd2', step: true },
                  ...(p.burn !== null ? [{ label: 'burn', points: [{ at: now(), v: p.burn }, { at: to(), v: p.burn }], tone: 'd2' as const, projected: true, step: true }] : [])];
            });
            const max = createMemo(() => mode() === 'left' ? (p.capacity || 1) : Math.max(1, ...series().flatMap(s => s.points.map(q => q.v))));
            const leftShare = createMemo(() => p.leftPct !== null ? percent(p.leftPct, 100) : null);
            return (
              <article class="panel limit-card" aria-label={`${p.harness} ${p.period}`}>
                <header class="panel-h">
                  <h2>{p.harness} <span class="id">{p.period}</span></h2>
                  <span class="grow" />
                  <Show when={leftShare()} fallback={<span class="muted" data-tip="No current reading">—</span>}>{ls => <span class="num" aria-label={ls().long}>{ls().text}</span>}</Show>
                </header>
                <dl class="limit-facts">
                  <dt>Next reset</dt>
                  <dd>
                    <Show when={p.nextReset.at} fallback={<span class="muted" data-tip="Reset time unknown">—</span>}>{at => <>
                      <time data-tip={exact(at())}>{countdown(at(), now()).text}</time>{' '}
                      {p.nextReset.restore !== null ? <span class="num">+{Math.round(p.nextReset.restore)}</span> : <span class="muted" data-tip="Restore amount unknown">—</span>}
                    </>}</Show>
                  </dd>
                  <dt>First empty</dt><dd>{when(p.firstEmpty, now())}</dd>
                  <dt>Pool empty</dt><dd>{when(p.poolEmpty, now())}</dd>
                  <dt>Burn</dt><dd>{p.burn !== null ? <span class="num">{p.burn.toFixed(1)}/h</span> : <span class="muted" data-tip="Fewer than two readings in window">—</span>}</dd>
                  <Show when={p.unknown.length > 0}><dt>Without reading</dt><dd data-tip={p.unknown.join(', ')}>{p.unknown.length}</dd></Show>
                </dl>
                <AreaChart label={`${p.harness} ${p.period} ${mode()}`} series={series()} from={from()} to={to()} now={now()} max={max()}
                  value={v => mode() === 'left' ? percent(v, p.capacity || 1).text : `${v.toFixed(1)}/h`} />
              </article>
            );
          }}</For>
        </div>
        <section class="panel">
          <header class="panel-h"><h2>Subscriptions</h2></header>
          <DataTable label="Subscriptions" head={<tr><th scope="col">Subscription</th><For each={periods()}>{p => <th scope="col">{p}</th>}</For><th scope="col"><span class="sr-only">Refresh</span></th></tr>}>
            <For each={data().subscriptions}>{s => (
              <tr>
                <th scope="row"><StateBadge family="subscription" state={s.intake === 'paused' ? 'paused' : 'all'} compact /> <span class="title">{nameOf(s)}</span> <span class="id">{s.harness}</span>{s.planWeight !== 1 && <span class="num muted"> ×{s.planWeight}</span>}</th>
                <For each={periods()}>{name => {
                  const periodCell = createMemo(() => s.periods.find(x => x.period === name));
                  return (
                    <Show when={periodCell()} fallback={<td class="muted">—</td>}>{p => (
                      <td><span class="limit-cell">
                        {p().left !== null
                          ? <FillBar total={100} segments={[{ label: 'used', bytes: 100 - p().left! }]} warnAt={s.lowPct ?? undefined} unit="percent" />
                          : <span class="muted" data-tip={p().status ?? 'No reading'}>{p().status && p().status !== 'unknown' ? p().status : '—'}</span>}
                        <Show when={p().resetsAt}>{rAt => <time data-tip={exact(rAt())} aria-label={`resets ${countdown(rAt(), now()).long}`}>{countdown(rAt(), now()).text}</time>}</Show>
                        {p().pace !== 'unknown' && <span class={`chip${p().pace === 'ahead' ? ' t-attention' : ''}`} data-tip="pace against the period's elapsed share">{p().pace}</span>}
                        {p().low && <span class="badge compact t-danger" role="img" aria-label="Low capacity" data-tip="Low capacity"><Icon name="triangle-alert" /></span>}
                        <Show when={p().readingAt}>{rAt => <AgeChip t={rAt()} />}</Show>
                        <span class="muted">{p().source}</span>
                      </span></td>
                    )}</Show>
                  );
                }}</For>
                <td><Button label={`Refresh ${nameOf(s)}`} icon="rotate-cw" iconOnly busy={busy() === s.id} onClick={() => refresh(s.id)} /></td>
              </tr>
            )}</For>
          </DataTable>
        </section>
      </>}</Show>
    </article>
  );
}
