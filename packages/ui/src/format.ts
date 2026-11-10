// The only place that formats time, numbers, sizes and money for the UI (OR-2).
// Each formatter returns the short text and the long form for tooltips and accessible names.

export type Basis = 'measured' | 'estimated' | 'unavailable';
/** A number with its measurement basis. `partial` marks a lower bound ("≥"). Unknown is never 0. */
export type Measured = { value: number | null; basis: Basis; reason?: string; partial?: string };
export type Formatted = { text: string; long: string };

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const pad = (n: number) => String(n).padStart(2, '0');
const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`;

/** Exact local time, for tooltips: "12 Nov 2026 10:42". */
export function exact(t: number): string {
  const d = new Date(t);
  return `${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Moment of a message in a copied or pasted text: "11 Nov 14:03", with the year when it is not this year (formats.md "Copy"). */
export function stamp(t: number, now: number): string {
  const d = new Date(t);
  return `${d.getDate()} ${MONTHS[d.getMonth()]}${d.getFullYear() === new Date(now).getFullYear() ? '' : ' ' + d.getFullYear()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
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

/** Duration in seconds: 40s, 14m, 1h 02m, 3d 4h. */
export function duration(sec: number): Formatted {
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
export function money(n: number): Formatted {
  const text = n >= 1000 ? `$${count(n).text}` : `$${n.toFixed(2)}`;
  return { text, long: `${n.toFixed(2)} dollars` };
}

/** Scores 0..1 with two fixed decimals: 0.80, 0.68. */
export function score(n: number): Formatted { return { text: n.toFixed(2), long: n.toFixed(2) }; }

export type Kind = 'money' | 'count' | 'duration' | 'bytes' | 'score';
const BY_KIND: Record<Kind, (n: number) => Formatted> = { money, count, duration, bytes, score };

/** A measured, estimated, partial or unavailable number (principles §2). */
export function measured(m: Measured, kind: Kind): Formatted {
  if (m.basis === 'unavailable' || m.value === null) {
    return { text: '—', long: `unavailable${m.reason ? `: ${m.reason}` : ''}` };
  }
  const f = BY_KIND[kind](m.value);
  if (m.basis === 'estimated') return { text: `≈ ${f.text}`, long: `about ${f.long}, estimated${m.reason ? ` from ${m.reason}` : ''}` };
  if (m.partial) return { text: `≥ ${f.text}`, long: `at least ${f.long}, ${m.partial}` };
  return f;
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
