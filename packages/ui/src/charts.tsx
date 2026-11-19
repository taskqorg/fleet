// Day charts for pace pages (controls/day-chart.md, share-bar.md, norm-bar.md): CSS only, no inline vector markup (OR-8), no chart library.
// Every figure is a calendar day (or a shorter bucket) on one shared column grid, so stacked charts line up.
import type { ComponentChildren } from 'preact';

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
  label: string; title?: ComponentChildren;
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
  const tone = p.tone ?? 'd1', [lo, hi] = p.names ?? [''];
  const over = (v: number | null | undefined) => v != null && p.alertAbove !== undefined && v > p.alertAbove;
  const tipOf = (c: DayCol, m: DayMark) => m.v === null ? `${c.tip ?? c.label}: not measured${m.missing ? ` (${m.missing})` : ''}`
    : `${c.tip ?? c.label}: ${lo ? lo + ' ' : ''}${p.value(m.v)}${m.hi != null && hi ? `, ${hi} ${p.value(m.hi)}` : ''}${m.some ? ` · ${m.some}` : ''}${m.partial ? ` · ${m.partial}` : ''}`
      + (over(m.v) || over(m.hi) ? ` · ${p.alertLabel ?? 'above the norm'}` : '');
  const anyPartial = p.marks.some(m => m.v !== null && m.partial), anySome = p.marks.some(m => m.v !== null && m.some), anyMissing = p.marks.some(m => m.v === null), anyClip = p.marks.some(m => (m.v ?? 0) > p.cap || (m.hi ?? 0) > p.cap);
  const pick = p.onSelect, dense = p.cols.length > 16, every = dense ? Math.ceil(p.cols.length / 8) : 1;
  return <div class={`dc dc-${p.size ?? 'md'}`} data-ui="day-chart" style={{ '--n': String(p.cols.length) }}>
    <div class="dc-head">{p.title && <b>{p.title}</b>}<span class="grow" />
      {anyClip && <span class="dc-key"><i class="dc-sw clip" />above the scale</span>}
      {anySome && <span class="dc-key"><i class={`dc-sw some ${tone}`} />partly measured</span>}
      {anyPartial && <span class="dc-key"><i class={`dc-sw part ${tone}`} />part of the day</span>}
      {anyMissing && <span class="dc-key"><i class="dc-sw none" />not measured</span>}</div>
    <div class="dc-plot" role="group" aria-label={p.label}>
      <span class="dc-y mono" data-tip={p.capNote ? `Scale top: ${p.capNote}` : undefined}>{p.value(p.cap)}</span>
      {p.cols.map((c, i) => {
        const m = p.marks[i], on = p.selected === c.key, clipV = (m.v ?? 0) > p.cap, clipH = (m.hi ?? 0) > p.cap;
        return <button key={c.key} type="button" class={`dc-col${on ? ' on' : ''}${m.v === null ? ' none' : ''}${m.partial && m.v !== null ? ' part' : ''}${m.some && m.v !== null ? ' some' : ''}`}
          aria-pressed={pick ? on : undefined} aria-label={tipOf(c, m)} data-tip={tipOf(c, m)} tabIndex={pick ? 0 : -1} onClick={() => pick?.(c.key)}>
          <span class="dc-area">
            {m.v === null ? <i class="dc-none" />
              : p.kind === 'bar' ? <>
                <i class={`dc-bar ${tone}${clipV ? ' clip' : ''}`} style={{ height: `${pct(m.v, p.cap)}%` }} />
                {!dense && <span class="dc-lab mono" style={{ bottom: `${pct(m.v, p.cap)}%` }}>{p.value(m.v)}</span>}</>
              : <>
                {m.hi != null && <i class={`dc-hi ${tone}${clipH ? ' clip' : ''}`} style={{ height: `${pct(m.hi, p.cap)}%` }} />}
                <i class={`dc-bar ${tone}${clipV ? ' clip' : ''}`} style={{ height: `${pct(m.v, p.cap)}%` }} /></>}
          </span>
          {(clipH || clipV) && p.kind !== 'bar' && <span class="dc-true mono">{p.value(clipV ? m.v! : m.hi!)}</span>}
        </button>;
      })}
    </div>
    {p.values && !dense && <div class="dc-vals mono" aria-hidden="true">
      <span class="dc-g">{lo}</span>{p.marks.map((m, i) => <span key={i} class={`${m.partial || m.some ? 'muted' : ''}${p.selected === p.cols[i].key ? ' on' : ''}${over(m.v) ? ' alert' : ''}`}>{m.v === null ? '—' : p.value(m.v)}</span>)}
      {hi && <><span class="dc-g">{hi}</span>{p.marks.map((m, i) => <span key={'h' + i} class={`muted${over(m.hi) && m.v !== null ? ' alert' : ''}`}>{m.v === null || m.hi == null ? '' : p.value(m.hi)}</span>)}</>}
    </div>}
    {p.axis && <div class="dc-axis" aria-hidden="true"><span />{p.cols.map((c, i) => <span key={c.key} class={p.selected === c.key ? 'on' : undefined}>{i % every === 0 || p.selected === c.key ? c.label : ''}</span>)}</div>}
  </div>;
}

/* ---------- Share bar: one whole split into parts, with values on the parts (controls/share-bar.md) ---------- */

export type SharePart = { label: string; v: number; tone: DataTone; tip?: string };
/** A part narrower than this share of the bar carries no inline value; the legend under the bar always names every part with its value. */
const INLINE = 0.09;
export function ShareBar({ label, title, parts, value, note }: { label: string; title?: ComponentChildren; parts: SharePart[]; value: (v: number) => string; note?: ComponentChildren }) {
  const sum = parts.reduce((s, x) => s + x.v, 0) || 1;
  const share = (v: number) => Math.round(v / sum * 100);
  return <div class="sb" data-ui="share-bar">
    {title && <b class="sb-title">{title}</b>}
    <div class="sb-bar" role="img" aria-label={`${label}: ${parts.map(x => `${x.label} ${value(x.v)} (${share(x.v)}%)`).join(', ')}`}>
      {parts.filter(x => x.v > 0).map(x => <i key={x.label} class={x.tone} style={{ flexGrow: String(x.v) }} data-tip={x.tip ?? `${x.label}: ${value(x.v)}, ${share(x.v)}%`}>
        {x.v / sum >= INLINE && <span class="mono">{value(x.v)}</span>}</i>)}
    </div>
    <ul class="sb-legend" aria-hidden="true">
      {parts.map(x => <li key={x.label}><i class={`sb-key ${x.tone}`} />{x.label} <span class="mono">{value(x.v)}</span> <span class="muted mono">{share(x.v)}%</span></li>)}
      {note && <li class="muted grow-l">{note}</li>}
    </ul>
  </div>;
}

/* ---------- Norm bar: time in the current stage against that stage's norm (controls/norm-bar.md) ---------- */

/**
 * The track is two norms long (0 to 2 × P90), so the P90 tick sits in the middle of every row and rows compare at a glance.
 * The fill is the time so far: neutral within the norm, danger past P90; past the track's end it stops with a break.
 * Without a norm (a stage people work in) the track is empty and the caller says so.
 */
export function NormBar({ elapsed, p50, p90, label, value }: { elapsed: number; p50?: number | null; p90?: number | null; label: string; value: (v: number) => string }) {
  if (!p90) return <span class="nb none" data-ui="norm-bar" role="img" aria-label={`${label}: ${value(elapsed)}, no norm`} />;
  const over = elapsed > p90, w = Math.min(100, elapsed / (2 * p90) * 100), clip = elapsed > 2 * p90;
  return <span class={`nb${over ? ' over' : ''}${clip ? ' clip' : ''}`} data-ui="norm-bar" role="img"
    aria-label={`${label}: ${value(elapsed)}; norm P50 ${p50 ? value(p50) : 'unknown'}, P90 ${value(p90)}${over ? ', past the norm' : ''}`}>
    <i class="nb-fill" style={{ width: `${w}%` }} />
    {p50 ? <b class="nb-p50" style={{ left: `${Math.min(100, p50 / (2 * p90) * 100)}%` }} /> : null}
    <b class="nb-p90" />
  </span>;
}
