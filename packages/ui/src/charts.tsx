// Day charts for pace pages (controls/day-chart.md, share-bar.md, norm-bar.md): CSS only, no inline vector markup (OR-8), no chart library.
// Every figure is a calendar day (or a shorter bucket) on one shared column grid, so stacked charts line up.
import { createMemo, For, Show } from 'solid-js';
import type { JSX } from '@solidjs/web';

export type DataTone = 'd1' | 'd2' | 'd3' | 'd4';
/** A column of a day chart: a calendar day in the viewer's zone (or a shorter bucket). */
export type DayCol = { key: string; label: string; tip?: string };
/**
 * One column's figure. `v` is the value (a median or a count); `hi` an upper mark (P90). `v: null` is "not measured"
 * (`missing` says why), never zero. `partial` says the column is not over yet (today "so far"); `some` says only some of
 * its items were measured ("9 of 18 measured"): two different states with their own look.
 */
export type DayMark = { v: number | null; hi?: number | null; partial?: string; some?: string; missing?: string };

/**
 * Scale top for a set of values: their `q` quantile (P95 by default), rounded up to a clean step, so one outlier never
 * squashes the rest. With few values (a week of daily figures) the quantile is the maximum, so values over `fence` times
 * the median are left out first; they are drawn to the top with a break and their true value.
 */
export function capOf(values: (number | null | undefined)[], { q = 0.95, floor = 1, fence = 4, unit = 1 }: {
  q?: number; floor?: number;
  /** Values over this many medians are outliers; Infinity for counts, where a busy day is no outlier. */ fence?: number;
  /** The display unit the clean step is taken in: 60000 rounds milliseconds to whole minutes (40m, not 41m 40s). */ unit?: number } = {}): number {
  const xs = values.filter((v): v is number => typeof v === 'number' && v > 0).sort((a, b) => a - b);
  if (!xs.length) return floor;
  const med = xs[Math.floor((xs.length - 1) / 2)], kept = xs.filter(v => v <= fence * med);
  const top = kept[Math.min(kept.length - 1, Math.max(0, Math.ceil(q * kept.length) - 1))] / unit;
  const step = 10 ** Math.floor(Math.log10(top)), nice = [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10].map(m => m * step).find(m => m >= top) ?? top;
  return Math.max(floor, nice * unit);
}

/** The `f` quantile of xs (nearest rank: P50 of 4 values is the second); null for none. */
export function quantile(xs: number[], f: number): number | null {
  if (!xs.length) return null;
  const s = xs.toSorted((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.max(0, Math.ceil(f * s.length) - 1))];
}

const pct = (v: number, cap: number) => Math.max(0, Math.min(100, v / (cap || 1) * 100));

type DayChartProps = {
  /** Accessible name and the title on the chart's head line. */
  label: string; title?: JSX.Element;
  cols: DayCol[]; marks: DayMark[];
  /** Text of a value: `28m`, `34`. */
  value: (v: number) => string;
  /** bar: one bar per column, its value on the cap. range: a bar to `v` and a lighter extension to `hi`. */
  kind: 'bar' | 'range';
  /** Scale top (`capOf`); a figure above it is drawn to the top with a break and labelled with its true value. */
  cap: number; capNote?: string;
  tone?: DataTone; size?: 'sm' | 'md';
  /** Names of `v` and `hi` in tooltips and the values rows: P50, P90. */
  names?: [string, string?];
  /** Rows of values under the plot (range): `v` in ink, `hi` muted. */
  values?: boolean;
  /** Day labels under the plot; in a stack of charts only the last one shows them. */
  axis?: boolean;
  /** The norm (a baseline P90): a value above it is red and bold in the values rows, and its tooltip says so (OR-18). */
  alertAbove?: number; alertLabel?: string;
  selected?: string | null; onSelect?: (key: string) => void;
};

export function DayChart(p: DayChartProps) {
  const tone = () => p.tone ?? 'd1';
  const lo = () => p.names?.[0] ?? '';
  const hi = () => p.names?.[1];
  const over = (v: number | null | undefined) => v != null && p.alertAbove !== undefined && v > p.alertAbove;
  const tipOf = (c: DayCol, m: DayMark) => m.v === null ? `${c.tip ?? c.label}: not measured${m.missing ? ` (${m.missing})` : ''}`
    : `${c.tip ?? c.label}: ${lo() ? lo() + ' ' : ''}${p.value(m.v)}${m.hi != null && hi() ? `, ${hi()} ${p.value(m.hi)}` : ''}${m.some ? ` · ${m.some}` : ''}${m.partial ? ` · ${m.partial}` : ''}`
      + (over(m.v) || over(m.hi) ? ` · ${p.alertLabel ?? 'above the norm'}` : '');
  const anyPartial = createMemo(() => p.marks.some(m => m.v !== null && m.partial));
  const anySome = createMemo(() => p.marks.some(m => m.v !== null && m.some));
  const anyMissing = createMemo(() => p.marks.some(m => m.v === null));
  const anyClip = createMemo(() => p.marks.some(m => (m.v ?? 0) > p.cap || (m.hi ?? 0) > p.cap));
  const dense = createMemo(() => p.cols.length > 16);
  const every = createMemo(() => dense() ? Math.ceil(p.cols.length / 8) : 1);

  return (
    <div class={['dc', `dc-${p.size ?? 'md'}`]} data-ui="day-chart" style={{ '--n': String(p.cols.length) }}>
      <div class="dc-head">
        <Show when={p.title} keyed>{t => <b>{t}</b>}</Show>
        <span class="grow" />
        <Show when={anyClip()}><span class="dc-key"><i class="dc-sw clip" />above the scale</span></Show>
        <Show when={anySome()}><span class="dc-key"><i class={['dc-sw', 'some', tone()]} />partly measured</span></Show>
        <Show when={anyPartial()}><span class="dc-key"><i class={['dc-sw', 'part', tone()]} />part of the day</span></Show>
        <Show when={anyMissing()}><span class="dc-key"><i class="dc-sw none" />not measured</span></Show>
      </div>
      <div class="dc-plot" role="group" aria-label={p.label}>
        <span class="dc-y mono" data-tip={p.capNote ? `Scale top: ${p.capNote}` : undefined}>{p.value(p.cap)}</span>
        <For each={p.cols}>
          {(c, i) => {
            const m = createMemo(() => p.marks[i()]);
            const on = createMemo(() => p.selected === c.key);
            const clipV = createMemo(() => (m().v ?? 0) > p.cap);
            const clipH = createMemo(() => (m().hi ?? 0) > p.cap);
            return (
              <button
                type="button"
                class={['dc-col', { on: on(), none: m().v === null, part: !!m().partial && m().v !== null, some: !!m().some && m().v !== null }]}
                aria-pressed={p.onSelect ? (on() ? 'true' : 'false') : undefined}
                aria-label={tipOf(c, m())}
                data-tip={tipOf(c, m())}
                tabindex={p.onSelect ? 0 : -1}
                onClick={() => p.onSelect?.(c.key)}
              >
                <span class="dc-area">
                  <Show when={m().v !== null} fallback={<i class="dc-none" />}>
                    <Show
                      when={p.kind === 'bar'}
                      fallback={
                        <>
                          <Show when={m().hi != null}>
                            <i class={['dc-hi', tone(), { clip: clipH() }]} style={{ height: `${pct(m().hi!, p.cap)}%` }} />
                          </Show>
                          <i class={['dc-bar', tone(), { clip: clipV() }]} style={{ height: `${pct(m().v!, p.cap)}%` }} />
                        </>
                      }
                    >
                      <i class={['dc-bar', tone(), { clip: clipV() }]} style={{ height: `${pct(m().v!, p.cap)}%` }} />
                      <Show when={!dense()}>
                        <span class="dc-lab mono" style={{ bottom: `${pct(m().v!, p.cap)}%` }}>{p.value(m().v!)}</span>
                      </Show>
                    </Show>
                  </Show>
                </span>
                <Show when={(clipH() || clipV()) && p.kind !== 'bar'}>
                  <span class="dc-true mono">{p.value(clipV() ? m().v! : m().hi!)}</span>
                </Show>
              </button>
            );
          }}
        </For>
      </div>
      <Show when={p.values && !dense()}>
        <div class="dc-vals mono" aria-hidden="true">
          <span class="dc-g">{lo()}</span>
          <For each={p.marks}>
            {(m, i) => (
              <span class={{ muted: !!(m.partial || m.some), on: p.selected === p.cols[i()].key, alert: over(m.v) }}>
                {m.v === null ? '—' : p.value(m.v)}
              </span>
            )}
          </For>
          <Show when={hi()} keyed>
            {h => (
              <>
                <span class="dc-g">{h}</span>
                <For each={p.marks}>
                  {m => (
                    <span class={['muted', { alert: over(m.hi) && m.v !== null }]}>
                      {m.v === null || m.hi == null ? '' : p.value(m.hi)}
                    </span>
                  )}
                </For>
              </>
            )}
          </Show>
        </div>
      </Show>
      <Show when={p.axis}>
        <div class="dc-axis" aria-hidden="true">
          <span />
          <For each={p.cols}>
            {(c, i) => (
              <span class={{ on: p.selected === c.key }}>
                {i() % every() === 0 || p.selected === c.key ? c.label : ''}
              </span>
            )}
          </For>
        </div>
      </Show>
    </div>
  );
}

/* ---------- Share bar: one whole split into parts, with values on the parts (controls/share-bar.md) ---------- */

export type SharePart = { label: string; v: number; tone: DataTone; tip?: string };
/** A part narrower than this share of the bar carries no inline value; the legend under the bar always names every part with its value. */
const INLINE = 0.09;
export function ShareBar(p: { label: string; title?: JSX.Element; parts: SharePart[]; value: (v: number) => string; note?: JSX.Element }) {
  const sum = createMemo(() => p.parts.reduce((s, x) => s + x.v, 0) || 1);
  const share = (v: number) => Math.round(v / sum() * 100);
  const positive = createMemo(() => p.parts.filter(x => x.v > 0));
  return (
    <div class="sb" data-ui="share-bar">
      <Show when={p.title} keyed>{t => <b class="sb-title">{t}</b>}</Show>
      <div class="sb-bar" role="img" aria-label={`${p.label}: ${p.parts.map(x => `${x.label} ${p.value(x.v)} (${share(x.v)}%)`).join(', ')}`}>
        <For each={positive()}>
          {x => (
            <i class={x.tone} style={{ 'flex-grow': String(x.v) }} data-tip={x.tip ?? `${x.label}: ${p.value(x.v)}, ${share(x.v)}%`}>
              <Show when={x.v / sum() >= INLINE}><span class="mono">{p.value(x.v)}</span></Show>
            </i>
          )}
        </For>
      </div>
      <ul class="sb-legend" aria-hidden="true">
        <For each={p.parts}>
          {x => <li><i class={['sb-key', x.tone]} />{x.label} <span class="mono">{p.value(x.v)}</span> <span class="muted mono">{share(x.v)}%</span></li>}
        </For>
        <Show when={p.note} keyed>{n => <li class="muted grow-l">{n}</li>}</Show>
      </ul>
    </div>
  );
}

/* ---------- Norm bar: time in the current stage against that stage's norm (controls/norm-bar.md) ---------- */

/**
 * The track is two norms long (0 to 2 × P90), so the P90 tick sits in the middle of every row and rows compare at a glance.
 * The fill is the time so far: neutral within the norm, danger past P90; past the track's end it stops with a break.
 * Without a norm (a stage people work in) the track is empty and the caller says so.
 */
export function NormBar(p: { elapsed: number; p50?: number | null; p90?: number | null; label: string; value: (v: number) => string }) {
  return (
    <Show
      when={p.p90}
      fallback={<span class="nb none" data-ui="norm-bar" role="img" aria-label={`${p.label}: ${p.value(p.elapsed)}, no norm`} />}
    >
      {p90 => {
        const over = () => p.elapsed > p90();
        const w = () => Math.min(100, p.elapsed / (2 * p90()) * 100);
        const clip = () => p.elapsed > 2 * p90();
        return (
          <span
            class={['nb', { over: over(), clip: clip() }]}
            data-ui="norm-bar"
            role="img"
            aria-label={`${p.label}: ${p.value(p.elapsed)}; norm P50 ${p.p50 ? p.value(p.p50) : 'unknown'}, P90 ${p.value(p90())}${over() ? ', past the norm' : ''}`}
          >
            <i class="nb-fill" style={{ width: `${w()}%` }} />
            <Show when={p.p50} keyed>{p50 => <b class="nb-p50" style={{ left: `${Math.min(100, p50 / (2 * p90()) * 100)}%` }} />}</Show>
            <b class="nb-p90" />
          </span>
        );
      }}
    </Show>
  );
}
