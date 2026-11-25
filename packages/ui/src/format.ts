// The only place that formats time, numbers, sizes and money for the UI (OR-2).
// Each formatter returns the short text and the long form for tooltips and accessible names.
import vocabulary from '../../../contracts/vocabulary.json' with { type: 'json' };

export type Basis = 'measured' | 'estimated' | 'subscription' | 'unavailable';
/** A number with its measurement basis. `partial` marks a lower bound ("≥"). Unknown is never 0. */
export type Measured = { value: number | null; basis: Basis; reason?: string; partial?: string; currency?: string; note?: string };
export type Formatted = { text: string; long: string };

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const pad = (n: number) => String(n).padStart(2, '0');
/** A count with its word: "1 session", "3 sessions", "1,240 tasks"; `many` for an irregular plural. */
export const plural = (n: number, one: string, many = one + 's') => `${n.toLocaleString('en')} ${n === 1 ? one : many}`;

/**
 * A stored time (ISO text or epoch ms) as epoch ms. Missing or unreadable is `missing`: null by default, never a silent
 * 0 or NaN; a caller that sorts or compares passes the value that means "oldest" for it (formats.md, Time).
 */
export function timeOf(t: string | number | null | undefined): number | null;
export function timeOf<M extends number | null | undefined>(t: string | number | null | undefined, missing: M): number | M;
export function timeOf(t: string | number | null | undefined, missing: number | null | undefined = null): number | null | undefined {
  if (typeof t === 'number') return Number.isFinite(t) ? t : missing;
  const v = t ? Date.parse(t) : NaN;
  return Number.isFinite(v) ? v : missing;
}

/** Exact local time, for tooltips: "12 Nov 2026 10:42". */
export function exact(t: number): string {
  const d = new Date(t);
  return `${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function zonedTime(t: number, zone: string): string {
  const options: Intl.DateTimeFormatOptions = {
    day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
  };
  try {
    return new Intl.DateTimeFormat('en-GB', { ...options, timeZone: zone }).format(t) + ' · ' + zone;
  } catch {
    return new Intl.DateTimeFormat('en-GB', { ...options, timeZone: 'UTC' }).format(t) + ' · UTC';
  }
}

/** Moment of a message in a copied or pasted text: "11 Nov 14:03", with the year when it is not this year (formats.md "Copy"). */
export function stamp(t: number, now: number): string {
  const d = new Date(t);
  return `${d.getDate()} ${MONTHS[d.getMonth()]}${d.getFullYear() === new Date(now).getFullYear() ? '' : ' ' + d.getFullYear()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Calendar day in the viewer's zone: "16 Nov" (the year when it is not the year of `now`). */
export function day(t: number, now = Date.now()): string {
  const d = new Date(t);
  return `${d.getDate()} ${MONTHS[d.getMonth()]}${d.getFullYear() === new Date(now).getFullYear() ? '' : ' ' + d.getFullYear()}`;
}

/** Start of the calendar day of `t` in the viewer's zone, `n` days later (negative: earlier). */
export function dayStart(t: number, n = 0): number {
  // The date moves first: in a zone whose clocks skip midnight, the day then starts at its first real hour.
  const d = new Date(t);
  d.setHours(12, 0, 0, 0);
  d.setDate(d.getDate() + n);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

/** Start of the local slot of `hours` (a divisor of 24) that holds `t`, `n` slots later; local clock hours, so a daylight-saving change keeps the labels. */
export function slotStart(t: number, hours: number, n = 0): number {
  const d = new Date(t);
  d.setHours(d.getHours() - d.getHours() % hours + n * hours, 0, 0, 0);
  return d.getTime();
}

/** A link key of a local calendar day ("2026-11-16") or, with `hour`, of a local hour slot ("2026-11-16T14"): the same
 *  calendar day or hour in every zone, and stable while the page stays open. */
export function dateKey(t: number, hour = false): string {
  const d = new Date(t);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}${hour ? 'T' + pad(d.getHours()) : ''}`;
}

/** Hour of the day in the viewer's zone, for buckets shorter than a day: "14:00". */
export function hourLabel(t: number): string {
  const d = new Date(t);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Time left until a future moment (a reset countdown): 40s, 4m, 2h, 3d, then the date; "now" when it has passed. */
export function countdown(t: number, now: number): Formatted {
  const s = Math.floor((t - now) / 1000);
  if (s < 5) return { text: 'now', long: 'now' };
  if (s < 60) return { text: `${s}s`, long: `in ${plural(s, 'second')}` };
  const m = Math.floor(s / 60);
  if (m < 60) return { text: `${m}m`, long: `in ${plural(m, 'minute')}` };
  const h = Math.floor(m / 60);
  if (h < 24) return { text: `${h}h`, long: `in ${plural(h, 'hour')}` };
  const d = Math.floor(h / 24);
  if (d < 7) return { text: `${d}d`, long: `in ${plural(d, 'day')}` };
  return { text: exact(t).replace(/ \d\d:\d\d$/, ''), long: exact(t) };
}

/** Age of a past moment: now, 40s, 4m, 2h, 3d, then 12 Nov, then 12 Nov 2025. */
export function age(t: number, now: number): Formatted {
  const s = Math.max(0, Math.floor((now - t) / 1000));
  if (s < 5) return { text: 'now', long: 'just now' };
  if (s < 60) return { text: `${s}s`, long: `${plural(s, 'second')} ago` };
  const m = Math.floor(s / 60);
  if (m < 60) return { text: `${m}m`, long: `${plural(m, 'minute')} ago` };
  const h = Math.floor(m / 60);
  if (h < 24) return { text: `${h}h`, long: `${plural(h, 'hour')} ago` };
  const d = Math.floor(h / 24);
  if (d < 7) return { text: `${d}d`, long: `${plural(d, 'day')} ago` };
  const a = new Date(t), b = new Date(now);
  const text = `${a.getDate()} ${MONTHS[a.getMonth()]}` + (a.getFullYear() === b.getFullYear() ? '' : ` ${a.getFullYear()}`);
  return { text, long: exact(t) };
}

/** Duration in seconds: 120ms (under a second: request latency), 40s, 14m, 1h 02m, 3d 4h. */
export function duration(sec: number): Formatted {
  if (sec > 0 && sec < 1) { const ms = Math.max(1, Math.round(sec * 1000)); return { text: `${ms}ms`, long: plural(ms, 'millisecond') }; }
  const s = Math.round(sec);
  if (s < 60) return { text: `${s}s`, long: plural(s, 'second') };
  const m = Math.floor(s / 60);
  if (m < 60) return { text: `${m}m`, long: plural(m, 'minute') };
  const h = Math.floor(m / 60);
  if (h < 24) return { text: `${h}h ${pad(m % 60)}m`, long: `${plural(h, 'hour')} ${plural(m % 60, 'minute')}` };
  const d = Math.floor(h / 24);
  return { text: `${d}d ${h % 24}h`, long: `${plural(d, 'day')} ${plural(h % 24, 'hour')}` };
}

/** Bytes in decimal units: 950 B, 4.2 KB, 18 GB, 1 TB. */
export function bytes(n: number): Formatted {
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0, v = n;
  while (v >= 1000 && i < u.length - 1) { v /= 1000; i++; }
  const text = `${v < 10 && i ? v.toFixed(1).replace(/\.0$/, '') : Math.round(v)} ${u[i]}`;
  return { text, long: `${n} bytes` };
}

/** Counts: 950, 12.4k, 182k, 1.2M, 3.4B. */
export function count(n: number): Formatted {
  const steps: [number, string][] = [[1e9, 'B'], [1e6, 'M'], [1e3, 'k']];
  for (const [d, s] of steps) if (Math.abs(n) >= d) {
    const v = n / d;
    return { text: `${v < 100 ? v.toFixed(1).replace(/\.0$/, '') : Math.round(v)}${s}`, long: String(n) };
  }
  return { text: String(n), long: String(n) };
}

/** Money in dollars: $0.42, $12.80, $1.2k. */
export function money(n: number, currency = 'USD'): Formatted {
  // Below a cent (one provider call): two significant digits, never exponent notation, so an estimate never reads as 0.00.
  const v = n > 0 && n < 0.01 ? n.toLocaleString('en-US', { maximumSignificantDigits: 2, useGrouping: false }) : n >= 1000 ? count(n).text : n.toFixed(2);
  return currency === 'USD' ? { text: `$${v}`, long: `${v} dollars` } : { text: `${v} ${currency}`, long: `${v} ${currency}` };
}

/** Scores 0..1 with two fixed decimals: 0.80, 0.68. */
export function score(n: number): Formatted { return { text: n.toFixed(2), long: n.toFixed(2) }; }

export type Kind = 'money' | 'count' | 'duration' | 'bytes' | 'score';
const BY_KIND: Record<Kind, (n: number) => Formatted> = { money, count, duration, bytes, score };

/** A measured, estimated, subscription, partial or unavailable number (principles §2). */
export function measured(m: Measured, kind: Kind): Formatted {
  if (m.basis === 'unavailable' || m.value === null) {
    if (m.basis === 'subscription') {
      const why = m.reason ?? m.note;
      return { text: '—', long: why ? `included in subscription: ${why}` : 'included in subscription' };
    }
    return { text: '—', long: `unavailable${m.reason ? `: ${m.reason}` : ''}` };
  }
  const f = kind === 'money' ? money(m.value, m.currency) : BY_KIND[kind](m.value);
  if (m.basis === 'subscription') {
    return {
      text: `≈ ${f.text}`,
      long: `about ${f.long}, subscription list-price equivalent${m.partial ? `, ${m.partial}` : ''}${m.reason ? ` (${m.reason})` : ''}${m.note ? ` · ${m.note}` : ''}`,
    };
  }
  if (m.basis === 'estimated') return { text: `≈ ${f.text}`, long: `about ${f.long}, estimated${m.reason ? ` from ${m.reason}` : ''}${m.partial ? `, ${m.partial}` : ''}` };
  if (m.partial) return { text: `≥ ${f.text}`, long: `at least ${f.long}, ${m.partial}` };
  return f;
}

export type CostFigureLike = {
  value: number | null;
  currency: string | null;
  basis: Basis;
  reason?: string;
  note?: string;
  usages?: number;
};

/** A stored cost basis. An unknown one is unavailable: Fleet never claims an estimate it cannot vouch for (formats.md, Money). */
export const basisOf = (b: string | null | undefined): Basis =>
  b === 'measured' || b === 'estimated' || b === 'subscription' || b === 'unavailable' ? b : 'unavailable';
/** The USD total of cost figures: an unavailable figure or one without a currency is never summed, even with a value (formats.md, Money). */
export const usdOf = (cost: readonly CostFigureLike[] | null | undefined): number =>
  (cost ?? []).reduce((sum, c) => (c.basis !== 'unavailable' && c.currency === 'USD' && typeof c.value === 'number' ? sum + c.value : sum), 0);

/** Converts one CostFigure into a money Measured entry without altering currency or basis. */
export function costMeasured(c: CostFigureLike): Measured {
  return { value: c.value, basis: c.basis, currency: c.currency ?? 'USD', reason: c.reason, note: c.note };
}

/** Separates metered/unavailable figures from subscription list-price equivalents, preserving every currency. */
export function splitCostFigures(all: readonly CostFigureLike[] | null | undefined): { metered: CostFigureLike[]; subscription: CostFigureLike[] } {
  const metered: CostFigureLike[] = [];
  const subscription: CostFigureLike[] = [];
  for (const c of all ?? []) {
    if (c.basis === 'subscription') subscription.push(c);
    else metered.push(c);
  }
  return { metered, subscription };
}

/** Formats row-level cumulative spend while keeping currencies, bases, partial coverage, and unknown states distinct. */
export function formatRowSpend(cost?: readonly CostFigureLike[], stale = false, name?: string) {
  const known = (cost ?? []).filter(figure => figure.value !== null && figure.basis !== 'unavailable');
  const partial = known.length > 0 && (cost?.some(figure => figure.basis === 'unavailable') ?? false);
  const unpricedSub = known.length === 0 && (cost?.some(figure => figure.basis === 'subscription') ?? false);
  const figures = known.map(figure => measured(costMeasured(figure), 'money'));
  const names = figures.map(figure => figure.long).join(', ');
  const detail = figures.length ? names + (partial ? ', partial' : '') : unpricedSub ? 'included in subscription' : 'unknown';
  const primaryText = figures.length ? figures[0].text : 'Spend —';
  const suffix = figures.length ? (partial ? 'partial' : figures.length > 1 ? `+${figures.length - 1}` : '') : '';
  const subscription = figures.length > 0 ? known[0].basis === 'subscription' : unpricedSub;
  const visible = suffix ? `${primaryText} ${suffix}` : primaryText;
  const tip = `Cumulative spend: ${detail}${stale && cost ? ', last confirmed' : ''}. Open resource breakdown.`;
  const ariaLabel = `${name ? `${name}: ` : ''}${visible}. ${tip}`;
  return { primaryText, suffix, subscription, hasFigures: figures.length > 0, tip, ariaLabel };
}

/** Adapts a contract MeasuredNumber/MeasuredInt into UI Measured with optional unit scaling (e.g. 0.001 for ms to seconds). */
export function numMeasured(n: { value: number | null; basis: Basis; reason?: string; partial?: string; note?: string } | null | undefined, scale = 1): Measured {
  if (!n) return { value: null, basis: 'unavailable' };
  const basis: Basis = n.basis === 'unavailable' && n.partial && n.value != null ? 'measured' : n.basis;
  return { value: n.value === null ? null : n.value * scale, basis, reason: n.reason, partial: n.partial, note: n.note };
}

/** Adapts millisecond durations into UI second Measured values. */
export function msMeasured(ms: number | null | undefined, basis: Basis = 'measured', reason?: string): Measured {
  return ms == null ? { value: null, basis: 'unavailable', reason } : { value: ms / 1000, basis, reason };
}

/** Clock time for transcripts: 10:31:05. */
export function clock(t: number): string {
  const d = new Date(t);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** Share of a whole: 3%, 41%, 97%. */
export function percent(part: number, whole: number): Formatted {
  const v = Math.round(part / whole * 100);
  return { text: `${v}%`, long: `${v} percent` };
}

/** Short agent label from the stored model id and effort: claude-opus-5-5 + high -> "opus-5.5 · high" (design D4). */
export function agentLabel(model: string | null | undefined, effort?: string | null): string {
  const m = (model ?? '').replace(/^claude-/, '').replace(/-(\d+)-(\d+)$/, '-$1.$2');
  return [m, effort].filter(Boolean).join(' · ');
}

/** The reader's IANA time zone, for times the server writes into copied text (formats.md "Copy"). */
export const zone = () => Intl.DateTimeFormat().resolvedOptions().timeZone;

/** An actor as people read it (OR-38): an agent run (`execution:<uuid>`) is "agent", a person is their email. */
export const actor = (a: string | null | undefined): string => !a ? '' : a.startsWith('execution:') ? 'agent' : a;

/**
 * A file's human name (OR-38): the last path segment without UUIDs or long hex hashes, extension kept; '' when nothing
 * readable is left (the caller then names the kind). `mcp-lifecycle-5eb2…-16e7c862-….jsonl` reads `mcp-lifecycle.jsonl`.
 */
export function fileLabel(path: string): string {
  const base = path.split('/').pop() ?? '', dot = base.lastIndexOf('.');
  const stem = dot > 0 ? base.slice(0, dot) : base, ext = dot > 0 ? base.slice(dot) : '';
  const s = stem.replace(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi, '').replace(/(^|[-_.])[0-9a-f]{12,}(?=$|[-_.])/gi, '$1')
    .replace(/[-_.]{2,}/g, '-').replace(/^[-_.]+|[-_.]+$/g, '');
  return s ? s + ext : '';
}

/** A run failure cause in plain words: `id` the cause (`abandoned`), `text` its sentence, `long` the stored code for the tooltip and copy. */
export type Cause = Formatted & { id: string };
/** A served failure (`failure` on execution and bot run rows), as the coordinator sends it. */
export type FailureLike = { cause: string; text: string; code: string };

/**
 * A stored failure cause code (`unknown:abandoned`, `step:provider-429`) in plain words: the first contracts/vocabulary.json
 * `causes` entry whose match the code starts with, the same mapping as the coordinator's contract.PlainCause. An unknown
 * code gets the generic sentence; the code itself is never the text, only `long`.
 */
export function cause(code: string): Cause {
  const all = vocabulary.causes;
  const c = all.find(x => x.match.some(m => code.startsWith(m))) ?? all[all.length - 1];
  return { id: c.id, text: c.text, long: code };
}

/** A row's cause: its served `failure` when there is one, else its stored `problem` code mapped here; nothing when neither. */
export function causeOf(failure: FailureLike | null | undefined, code?: string | null): Cause | undefined {
  if (failure) return { id: failure.cause, text: failure.text, long: failure.code };
  return code ? cause(code) : undefined;
}
