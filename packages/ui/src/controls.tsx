// Fleet controls. Specs: docs/design-system/controls/*.md. Styling: styles.css with tokens only.
import { Fragment, type ComponentChildren, type TargetedMouseEvent } from 'preact';
import { useEffect, useId, useRef, useState } from 'preact/hooks';
import { type IconName, type Family, type StateOf, type StateDef, type Tone, type Harness, type Stage, iconSvg, states, actions, harnesses, stages, stepKinds, type StepKind } from './registry.ts';
import { type Measured, type Kind, measured, age, bytes, exact, stamp, percent, duration, agentLabel, actor } from './format.ts';
import { matchSearch } from './search.ts';
import { VirtualList, type VirtualListHandle } from './virtual-list.tsx';
import { parse, renderBlocks } from './markdown.tsx';
import { author, clipMessage, writeClip } from './clip.ts';
import tokens from '../tokens.json';

const ROW = parseInt(tokens.size.row);

/* ---------- Icon and button ---------- */

/** Registry icon. With `label` it is meaningful (tooltip + accessible name); without, decorative. */
export function Icon({ name, label }: { name: IconName; label?: string }) {
  return <span class="icon" role={label ? 'img' : undefined} aria-label={label} aria-hidden={label ? undefined : true}
    dangerouslySetInnerHTML={{ __html: iconSvg(name) }} />;
}

type ButtonProps = {
  label: string; icon?: IconName; iconOnly?: boolean; kind?: 'primary' | 'default' | 'quiet' | 'danger';
  kbd?: string; rowLink?: boolean; busy?: boolean; disabledReason?: string; pressed?: boolean; onClick?: (event: TargetedMouseEvent<HTMLButtonElement>) => void;
};
export function Button(p: ButtonProps) {
  const disabled = p.busy || !!p.disabledReason;
  return (
    <button type="button" data-ui="button" class={`btn ${p.kind ?? 'default'}${p.iconOnly ? ' icon-only' : ''}${p.rowLink ? ' row-link' : ''}${p.busy ? ' busy' : ''}`}
      aria-label={p.iconOnly ? p.label : undefined} data-tip={p.disabledReason ?? (p.iconOnly ? p.label : undefined)}
      aria-disabled={disabled || undefined} aria-pressed={p.pressed} onClick={disabled ? undefined : p.onClick}>
      {p.icon && <Icon name={p.icon} />}
      {!p.iconOnly && <span>{p.label}</span>}
      {p.kbd && <kbd>{p.kbd}</kbd>}
    </button>
  );
}

/* ---------- Tooltip (OR-20) ---------- */

/** One small tooltip for the whole app, for elements with data-tip. Shows after a delay on hover or focus,
 *  about that element only, never over a field being typed in, hidden on any key press or scroll. Mount once. */
export function TooltipLayer({ delay = 600 }: { delay?: number }) {
  const [tip, setTip] = useState<{ text: string; x: number; y: number; below: boolean } | null>(null);
  useEffect(() => {
    let timer = 0;
    const hide = () => { clearTimeout(timer); setTip(null); };
    const show = (e: Event) => {
      const el = (e.target as HTMLElement).closest?.('[data-tip]') as HTMLElement | null;
      hide();
      if (!el || document.activeElement?.matches('input,textarea,[contenteditable]')) return;
      // data-tip-narrow: only when the visible label next to it is hidden (icon-only rail).
      if (el.hasAttribute('data-tip-narrow') && el.nextElementSibling?.getClientRects().length) return;
      timer = window.setTimeout(() => {
        const r = el.getBoundingClientRect(), below = r.top < 48;
        setTip({ text: el.dataset.tip!, x: r.left + r.width / 2, y: below ? r.bottom : r.top, below });
      }, delay);
    };
    const opts = { capture: true, passive: true };
    document.addEventListener('pointerover', show, opts); document.addEventListener('focusin', show, opts);
    for (const ev of ['pointerout', 'focusout', 'keydown', 'scroll', 'input', 'pointerdown']) document.addEventListener(ev, hide, opts);
    return () => { hide(); document.removeEventListener('pointerover', show, opts); document.removeEventListener('focusin', show, opts);
      for (const ev of ['pointerout', 'focusout', 'keydown', 'scroll', 'input', 'pointerdown']) document.removeEventListener(ev, hide, opts); };
  }, [delay]);
  return tip && <div class={`tooltip${tip.below ? ' below' : ''}`} role="tooltip" style={{ left: `${tip.x}px`, top: `${tip.y}px` }}>{tip.text}</div>;
}

/* ---------- State badge (OR-1, D7) ---------- */

type BadgeProps<F extends Family> = {
  family: F; state: StateOf<F>; reason?: string;
  /** Last contact time when the observation is unconfirmed. */
  lastSeen?: number; now?: number;
  /** Icon only, word in tooltip: for dense columns. */
  compact?: boolean;
};
export function StateBadge<F extends Family>(p: BadgeProps<F>) {
  const d = states[p.family][p.state] as StateDef;
  const unconf = p.lastSeen !== undefined;
  const seen = unconf ? age(p.lastSeen!, p.now ?? Date.now()) : null;
  const name = [d.label, unconf && `unconfirmed, last seen ${seen!.long}`, p.reason].filter(Boolean).join(', ');
  return (
    <span class="state" data-ui="state-badge">
      <span class={`badge t-${d.tone}${unconf ? ' unconf' : ''}${p.compact ? ' compact' : ''}`} role="img" aria-label={name} data-tip={p.compact || unconf ? name : undefined}>
        <Icon name={d.icon} />{!p.compact && <span>{d.label}</span>}
      </span>
      {(unconf || p.reason) && !p.compact && <span class={`reason${unconf ? ' unconf' : ''}`} aria-hidden="true">{unconf ? `seen ${seen!.text}` : ''}{unconf && p.reason ? ' · ' : ''}{p.reason}</span>}
    </span>
  );
}

/* ---------- Numbers with basis, change tint (§2, OR-6, OR-7) ---------- */

type NumProps = {
  m: Measured; kind: Kind;
  /** What a rise means: tint green/red only when known (OR-6). */
  rise?: 'good' | 'bad' | 'neutral';
};
export function Num({ m, kind, rise = 'neutral' }: NumProps) {
  const f = measured(m, kind);
  const last = useRef(m.value);
  const [tint, setTint] = useState('');
  useEffect(() => {
    const before = last.current; last.current = m.value;
    if (before === null || m.value === null || before === m.value) return;
    const up = m.value > before;
    setTint(rise === 'neutral' ? 'tint-neutral' : (up === (rise === 'good')) ? 'tint-good' : 'tint-bad');
    const t = setTimeout(() => setTint(''), 2000);
    return () => clearTimeout(t);
  }, [m.value]);
  return <span data-ui="number" class={`num b-${m.basis}${m.partial ? ' partial' : ''} ${tint}`} data-tip={m.basis === 'measured' && !m.partial ? undefined : f.long} aria-label={f.long} tabIndex={m.basis === 'measured' && !m.partial ? undefined : 0}>{f.text}</span>;
}

export function Totals({ items }: { items: { label: string; m: Measured; kind: Kind; note?: string }[] }) {
  return (
    <dl class="totals" data-ui="number">
      {items.map(i => (
        <div key={i.label}>
          <dt>{i.label}</dt>
          <dd><Num m={i.m} kind={i.kind} /></dd>
          {i.note && <dd class="note">{i.note}</dd>}
        </div>
      ))}
    </dl>
  );
}

/** Age chip with visual salience (OR-2, OR-18): turns to the given tone past the caller's threshold. */
export function AgeChip({ t, now, alertAfter, tone = 'danger', alertLabel = 'needs a look' }: { t: number; now: number; alertAfter?: number; tone?: Tone; alertLabel?: string }) {
  const a = age(t, now);
  const alert = alertAfter !== undefined && now - t >= alertAfter * 1000;
  // D14/M3: the alert is never colour only: icon + meaning in the name and tooltip.
  return <span data-ui="number" class={`chip-age${alert ? ` t-${tone} alert` : ''}`} aria-label={alert ? `${a.long}, ${alertLabel}` : a.long} data-tip={alert ? alertLabel : undefined}>
    {alert && <Icon name="triangle-alert" />}{a.text}</span>;
}

/* ---------- Fill bar (W9–W11) ---------- */

type FillProps = { total: number | null; segments: { label: string; bytes: number }[]; warnAt?: number; stale?: number; now?: number;
  /** Units of total and segments: bytes (stores) or percent (quota windows). */
  unit?: 'bytes' | 'percent' };
export function FillBar(p: FillProps) {
  if (p.total === null) return <span class="fill-wrap" data-ui="fill-bar"><span class="fill" role="img" aria-label="unavailable" /><span class="legend muted">— unavailable</span></span>;
  const used = p.segments.reduce((s, x) => s + x.bytes, 0), free = p.total - used;
  const state = free <= 0 ? 'full' : p.warnAt !== undefined && free < p.warnAt ? 'warn' : '';
  const legend = p.segments.map(s => `${s.label} ${Math.round(s.bytes / p.total! * 100)}%`).join(' · ');
  const amount = (n: number) => p.unit === 'percent' ? percent(n, p.total!).text : bytes(n).text;
  const freeText = free <= 0 ? 'full' : `${amount(free)} ${p.unit === 'percent' ? 'left' : 'free'}${state ? `, warning at ${amount(p.warnAt!)}` : ''}`;
  const staleText = p.stale !== undefined ? ` · ${age(p.stale, p.now ?? Date.now()).text} old` : '';
  return (
    <span class="fill-wrap" data-ui="fill-bar">
      <span class={`fill ${state}${p.stale !== undefined ? ' stale' : ''}`} role="img" aria-label={`${legend}; ${freeText}${staleText}`}>
        {p.segments.map((s, i) => <i key={s.label} class={`d${i % 4 + 1}`} style={{ width: `${s.bytes / p.total! * 100}%` }} data-tip={`${s.label} ${p.unit === 'percent' ? percent(s.bytes, p.total!).text : bytes(s.bytes).text}`} />)}
        {p.warnAt !== undefined && <b class="mark" style={{ left: `${(1 - p.warnAt / p.total) * 100}%` }} />}
      </span>
      <span class={`legend${state ? ` t-${state === 'full' ? 'danger' : 'attention'}` : ''}`} data-important={state ? '' : undefined}>{state ? `${freeText} · ${legend}` : `${legend} · ${freeText}`}{staleText}</span>
    </span>
  );
}

/* ---------- Filter bar with inline filters, saved filters and search (§4, OR-12, OR-14) ---------- */

export type Chip = { field: string; value: string };
export type FilterField = { field: string; values: { value: string; count: number }[] };
type FilterBarProps = {
  query: string; onQuery: (q: string) => void; placeholder: string;
  chips: Chip[]; onRemove: (c: Chip) => void; onAdd: (c: Chip) => void; onClear: () => void;
  fields: FilterField[];
  /** Second line: presets, state counters, grouping. */
  extra?: ComponentChildren;
  shown: number; total: number;
};
export function FilterBar(p: FilterBarProps) {
  const [open, setOpen] = useState<string | null>(null); // inline filter row, never a popup (OR-14)
  const field = p.fields.find(f => f.field === open);
  return (
    <div class="fbar" data-ui="filter-bar">
      <div class="fbar-line">
        <label class="search" data-ui="search"><Icon name={actions.search} /><input type="search" value={p.query} placeholder={p.placeholder} aria-label={p.placeholder}
          onInput={e => p.onQuery((e.target as HTMLInputElement).value)} /></label>
        <Button label="Filter" icon={actions.filter} kind="quiet" kbd="f" pressed={open !== null} onClick={() => setOpen(open === null ? p.fields[0]?.field ?? null : null)} />
        {p.chips.map(c => (
          <span class="chip" key={c.field + c.value}>{c.field}: {c.value}
            <button type="button" aria-label={`Remove filter ${c.field}: ${c.value}`} onClick={() => p.onRemove(c)}><Icon name={actions.clear} /></button></span>
        ))}
        {p.chips.length > 0 && <Button label="Clear" kind="quiet" onClick={p.onClear} />}
        <span class="count" aria-live="polite">{p.shown < p.total ? `${p.shown.toLocaleString('en')} of ${p.total.toLocaleString('en')}` : ''}</span>
      </div>
      {p.extra && <div class="fbar-line">{p.extra}</div>}
      {open !== null && (
        <div class="fbar-line inline-filter" role="group" aria-label="Add filter">
          {p.fields.map(f => <button type="button" key={f.field} class={`seg${f.field === open ? ' on' : ''}`} aria-pressed={f.field === open} onClick={() => setOpen(f.field)}>{f.field}</button>)}
          <span class="sep" />
          {field?.values.map(v => {
            const on = p.chips.some(c => c.field === field.field && c.value === v.value);
            return <button type="button" key={v.value} class={`seg${on ? ' on' : ''}`} aria-pressed={on}
              onClick={() => on ? p.onRemove({ field: field.field, value: v.value }) : p.onAdd({ field: field.field, value: v.value })}>
              {v.value} <span class="muted">{v.count}</span></button>;
          })}
        </div>
      )}
    </div>
  );
}

/* ---------- Execution tree (§ execution-tree.md) ---------- */

export type TreeNode = {
  id: string; name: string; state: StateOf<'execution'>; lastSeen?: number; note?: string;
  time?: Measured; tokens?: Measured; cost?: Measured; children?: TreeNode[];
};
export function ExecTree({ nodes, now, label }: { nodes: TreeNode[]; now: number; label: string }) {
  const openByDefault = (n: TreeNode): boolean => n.state !== 'succeeded' && n.state !== 'cancelled' || !!n.children?.some(openByDefault);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [sel, setSel] = useState<string | null>(null);
  const row = (n: TreeNode, level: number): ComponentChildren => {
    const isOpen = open[n.id] ?? openByDefault(n);
    return (
      <li key={n.id} role="treeitem" aria-level={level} aria-expanded={n.children ? isOpen : undefined} aria-selected={sel === n.id}>
        <div class="ti" onClick={() => setSel(n.id)}>
          {n.children ? <button type="button" class="tw" aria-label={isOpen ? 'Collapse' : 'Expand'} onClick={e => { e.stopPropagation(); setOpen({ ...open, [n.id]: !isOpen }); }}>
            <span class={isOpen ? 'chev open' : 'chev'}><Icon name={actions.expand} /></span></button> : <span class="tw" />}
          <StateBadge family="execution" state={n.state} lastSeen={n.lastSeen} now={now} compact />
          <span class="nm">{n.name}{n.note && <span class="muted"> · {n.note}</span>}</span>
          <span class="m">{n.time && <Num m={n.time} kind="duration" />}{n.tokens && <Num m={n.tokens} kind="count" />}{n.cost && <Num m={n.cost} kind="money" />}</span>
        </div>
        {n.children && isOpen && <ul role="group">{n.children.map(c => row(c, level + 1))}</ul>}
      </li>
    );
  };
  return <ul class="tree" data-ui="execution-tree" role="tree" aria-label={label}>{nodes.map(n => row(n, 1))}</ul>;
}

/* ---------- Decision block (§6) ---------- */

type DecisionProps = {
  question: string; askedBy: string; addressedTo: string; since: number; now: number; waits?: ComponentChildren;
  context?: ComponentChildren; options: { label: string; kbd?: string; primary?: boolean }[];
  answered?: { by: string; answer: string; at: number; delivered: boolean };
  onAnswer?: (answer: string) => void;
  /** A key request: a write-only password field replaces the typed answer; the value goes to onSecret only, never to onAnswer. */
  onSecret?: (value: string) => Promise<unknown>;
};
export function DecisionBlock(p: DecisionProps) {
  const [text, setText] = useState('');
  const [failed, setFailed] = useState<string | null>(null);
  if (p.answered) {
    const a = p.answered;
    return <div class="decision answered" data-ui="decision-block" role="status"><Icon name={a.delivered ? 'check-check' : 'history'} />
      <span>{p.question} <b>{a.answer}</b> · {a.by} · {age(a.at, p.now).text}{a.delivered ? '' : ' · delivery pending'}</span></div>;
  }
  const send = (s: string) => s.trim() && p.onAnswer?.(s.trim());
  // The field clears once the key is stored; a refusal shows its reason, never the value.
  const secret = () => { const v = text.trim(); if (!v || !p.onSecret) return; setFailed(null); p.onSecret(v).then(() => setText(''), e => setFailed(String(e?.message ?? e))); };
  return (
    <div class="decision" data-ui="decision-block" role="group" aria-label={p.question}
      onKeyDown={e => { if ((e.target as HTMLElement).tagName === 'INPUT') return; const o = p.options.find(o => o.kbd === e.key); if (o) send(o.label); }}>
      <div class="dq"><Icon name="bell" /><b>{p.question}</b></div>
      <div class="meta">{p.askedBy}{p.addressedTo === 'you' ? '' : ` · for ${p.addressedTo}`} · <AgeChip t={p.since} now={p.now} />{p.waits && <> · {p.waits}</>}</div>
      {p.context && <div class="ctx">{p.context}</div>}
      <div class="answers">
        {p.options.map(o => <Button key={o.label} label={o.label} kbd={o.kbd} kind={o.primary ? 'primary' : 'default'} onClick={() => send(o.label)} />)}
        {p.onSecret ? <>
          <input type="password" autocomplete="off" spellcheck={false} value={text} placeholder="Paste key" aria-label="Key value"
            onInput={e => setText((e.target as HTMLInputElement).value)} onKeyDown={e => { if (e.key === 'Enter') secret(); }} />
          <Button label="Add key" icon="shield" kind="primary" disabledReason={text.trim() ? undefined : 'paste the key first'} onClick={secret} />
        </> : <input value={text} placeholder="Answer" aria-label="Typed answer" onInput={e => setText((e.target as HTMLInputElement).value)}
          onKeyDown={e => { if (e.key === 'Enter') send(text); }} />}
      </div>
      {failed && <div class="error" role="alert">{failed}</div>}
    </div>
  );
}

/* ---------- Result viewer shell ---------- */

type ResultProps = {
  name: string; type: string; size: number; source: string; href?: string;
  copies: { where: string; state: StateOf<'copy'> }[];
  tabs: string[]; tab: string; onTab: (t: string) => void; children: ComponentChildren;
};
export function ResultViewer(p: ResultProps) {
  return (
    <section class="rv" data-ui="result-viewer" aria-label={p.name}>
      <header class="rv-h">
        <Icon name="file-text" /><b class="nm">{p.name}</b>
        <span class="muted">{p.type} · {bytes(p.size).text} · {p.source}</span>
        {p.copies.map(c => <StateBadge key={c.where} family="copy" state={c.state} reason={c.where} compact />)}
        <span class="grow" />
        <span class="tabs" role="tablist">{p.tabs.map(t => <button type="button" role="tab" key={t} aria-selected={t === p.tab} onClick={() => p.onTab(t)}>{t}</button>)}</span>
        <Button label="Compare" icon="list" iconOnly kbd="c" />
        {p.href ? <a class="btn default icon-only" data-ui="button" href={p.href} download={p.name} aria-label="Download" data-tip="Download"><Icon name={actions.download} /></a> : <Button label="Download" icon={actions.download} iconOnly />}
        <Button label="Share" icon={actions.share} iconOnly />
      </header>
      <div class="rv-b" role="tabpanel">{p.children}</div>
    </section>
  );
}

/* ---------- Artifact view: the body of the result viewer (result-viewer.md) ---------- */

export type ArtifactMeta = { id: string; name: string; mediaType: string; size: number };
const TEXTUAL = /^(text\/|application\/(json|xml|x-ndjson|javascript|x-yaml|yaml|toml|sql|x-sh))/;
/** Which view a stored media type gets: image, video, text, or download only. HTML and SVG are text with a rendered preview. */
export const viewOf = (mediaType: string): 'image' | 'video' | 'text' | 'download' =>
  /^image\/(png|jpeg|gif|webp|avif|bmp)$/.test(mediaType) ? 'image' : /^video\//.test(mediaType) ? 'video' : TEXTUAL.test(mediaType) || mediaType === 'image/svg+xml' ? 'text' : 'download';
/** The rendered view a text format has, if any. YAML has none: its source is already its readable form. */
export const renderOf = (mediaType: string, name: string): 'html' | 'svg' | 'markdown' | 'json' | 'csv' | null =>
  /^text\/html/.test(mediaType) ? 'html' : mediaType === 'image/svg+xml' ? 'svg' : /^text\/markdown/.test(mediaType) || /\.md$/i.test(name) ? 'markdown'
    : /^application\/json/.test(mediaType) ? 'json' : /^text\/csv/.test(mediaType) ? 'csv' : null;
/** CSV rows (RFC 4180: quoted fields, doubled quotes, line breaks inside quotes). */
export function parseCsv(t: string): string[][] {
  const rows: string[][] = []; let row: string[] = [], f = '', q = false;
  for (let i = 0; i < t.length; i++) {
    const c = t[i];
    if (q) { if (c === '"') { if (t[i + 1] === '"') { f += '"'; i++; } else q = false; } else f += c; }
    else if (c === '"') q = true; else if (c === ',') { row.push(f); f = ''; }
    else if (c === '\n' || c === '\r') { if (c === '\r' && t[i + 1] === '\n') i++; row.push(f); rows.push(row); row = []; f = ''; }
    else f += c;
  }
  if (f || row.length) { row.push(f); rows.push(row); }
  return rows;
}
const pretty = (t: string) => { try { return JSON.stringify(JSON.parse(t), null, 2); } catch { return t; /* not complete yet, or not JSON: shown as is */ } };
// The chosen view per format lasts for the session (sessionStorage; a blocked store just falls back to the preview).
const modeOf = (f: string) => { try { return sessionStorage.getItem('fleet.av.' + f) === 'source' ? 'source' : 'preview'; } catch { return 'preview'; } };
const PAGE_BYTES = 65536;
/**
 * One stored artifact by media type: image and video in a fixed-ratio frame (no jump when they load, OR-23), text read in
 * Range pages as the reader scrolls (no cap). A format with a renderer opens rendered, with a Source/Preview switch:
 * HTML runs in a sandboxed frame without same-origin access (scripts on an opaque origin, no network), SVG as an image
 * (no scripts), Markdown as nodes, JSON indented, CSV as a table. Copy and download always take the source. Bytes come from `src`.
 */
export function ArtifactView({ a, src }: { a: ArtifactMeta; src: string }) {
  const kind = viewOf(a.mediaType), [text, setText] = useState(''), [next, setNext] = useState(0), [busy, setBusy] = useState(false), [err, setErr] = useState<string | null>(null);
  const fmt = kind === 'text' ? renderOf(a.mediaType, a.name) : null, [mode, setMode] = useState(() => fmt ? modeOf(fmt) : 'source'), decoder = useRef(new TextDecoder());
  const more = async () => {
    if (busy || next >= a.size) return; setBusy(true);
    try { const r = await fetch(src, { headers: { range: `bytes=${next}-${Math.min(a.size, next + PAGE_BYTES) - 1}` }, credentials: 'same-origin' });
      if (!r.ok) throw new Error(`${r.status}`); const b = new Uint8Array(await r.arrayBuffer());
      setText(t => t + decoder.current.decode(b, { stream: next + b.length < a.size })); /* one decoder per artifact: a character split across pages stays whole */ setNext(n => n + b.length); setErr(null); }
    catch (e: any) { setErr(e.message); } finally { setBusy(false); }
  };
  useEffect(() => { setText(''); setNext(0); setMode(fmt ? modeOf(fmt) : 'source'); decoder.current = new TextDecoder(); }, [a.id]);
  useEffect(() => { if (kind === 'text' && next === 0 && a.size > 0) more(); }, [a.id, next]);
  const flip = () => { const m = mode === 'preview' ? 'source' : 'preview'; setMode(m); try { sessionStorage.setItem('fleet.av.' + fmt, m); } catch { /* session-only memory is optional */ } };
  const scroller = (body: ComponentChildren) => <div class="av-text" onScroll={e => { const el = e.currentTarget; if (el.scrollHeight - el.scrollTop - el.clientHeight < 400) more(); }}>{body}
    {next < a.size && <div class="muted type-sm">{bytes(next).text} of {bytes(a.size).text}{busy ? ' · loading' : ''}</div>}</div>;
  const rendered = () => {
    if (fmt === 'html') return <div class="av-frame page" data-tip="Agent output running in a sandbox: not part of Fleet"><iframe src={`${src}/preview`} sandbox="allow-scripts allow-forms allow-modals" title={a.name} referrerpolicy="no-referrer" /></div>;
    if (fmt === 'svg') return <div class="av-frame"><img src={src} alt={a.name} /></div>;
    if (fmt === 'markdown') return scroller(<Markdown text={text} />);
    if (fmt === 'json') return scroller(<pre><code>{next < a.size ? text : pretty(text)}</code></pre>);
    const all = parseCsv(text), [head, ...rows] = next < a.size ? all.slice(0, -1) : all; // the last row of a partial page may be cut
    return scroller(<table class="av-table"><thead><tr>{(head ?? []).map((h, i) => <th key={i}>{h}</th>)}</tr></thead><tbody>{rows.map((r, i) => <tr key={i}>{r.map((c, j) => <td key={j}>{c}</td>)}</tr>)}</tbody></table>);
  };
  return <div class="av" data-ui="artifact-view">
    {kind === 'image' && <div class="av-frame"><img src={src} alt={a.name} loading="lazy" /></div>}
    {kind === 'video' && <div class="av-frame video"><video src={src} controls preload="metadata" /></div>}
    {kind === 'text' && <>
      <div class="bar">
        {fmt && <Button label={mode === 'preview' ? 'Source' : 'Preview'} icon={mode === 'preview' ? 'type' : 'panels-top-left'} onClick={flip} />}
        <Button label="Copy source" icon={actions.copy} iconOnly kind="quiet" onClick={() => (next < a.size ? fetch(src, { credentials: 'same-origin' }).then(r => r.text()) : Promise.resolve(text)).then(copyText).catch(e => setErr(e.message))} />
      </div>
      {fmt && mode === 'preview' ? rendered() : scroller(<pre><code>{text}</code></pre>)}
    </>}
    {kind === 'download' && <Empty text={`${a.mediaType} has no view here.`} />}
    {err && <ErrorBanner text={`Did not load: ${err}`} onRetry={more} />}
  </div>;
}

/** An unsaved form value; it never claims that a setting was applied (controls/text-field.md). */
export function TextField({ label, value, onChange, multiline, type = 'text', help, error, disabledReason }: {
  label: string; value: string; onChange: (value: string) => void; multiline?: boolean; type?: 'text' | 'number';
  help?: string; error?: string; disabledReason?: string;
}) {
  const id = useId(), note = error || disabledReason || help;
  const props = { id, value, disabled: !!disabledReason, 'aria-invalid': error ? true : undefined,
    'aria-describedby': note ? id + '-note' : undefined, onInput: (e: Event) => onChange((e.target as HTMLInputElement).value) };
  return <div class="text-field" data-ui="text-field"><label for={id}>{label}</label>
    {multiline ? <textarea {...props} rows={3} /> : type === 'number' ? <input {...props} type="number" /> : <input {...props} type="text" />}
    {note && <span id={id + '-note'} class={error ? 't-danger' : 'muted'} role={error ? 'alert' : undefined}>{note}</span>}
  </div>;
}

/* ---------- Setting field ---------- */

type SettingProps = {
  label: string; value: string; help: string; changed?: { by: string; at: number }; now: number;
  status: { kind: 'applied'; at: number } | { kind: 'pending' } | { kind: 'apply_error'; error: string };
  onSave?: (v: string) => void;
  /** A write-only value (a key): typed into a password field, never shown; the field starts empty. */
  secret?: boolean;
};
export function SettingField(p: SettingProps) {
  const [edit, setEdit] = useState<string | null>(null);
  const id = `set-${p.label.replace(/\W+/g, '-')}-${p.value.replace(/\W+/g, '')}`;
  const s = p.status;
  return (
    <div class="setting" data-ui="setting-field">
      <label for={id}>{p.label}</label>
      <div class="val">
        {edit === null
          ? <button type="button" id={id} class="value" onClick={() => setEdit(p.value)}>{p.value || (p.secret ? 'Paste' : '—')}</button>
          : <><input id={id} value={edit} type={(p.secret ? 'password' : 'text') as 'text'} autocomplete={p.secret ? 'off' : undefined} onInput={e => setEdit((e.target as HTMLInputElement).value)}
              onKeyDown={e => { if (e.key === 'Enter') { p.onSave?.(edit); setEdit(null); } if (e.key === 'Escape') setEdit(null); }} />
            <Button label="Save" kind="primary" onClick={() => { p.onSave?.(edit); setEdit(null); }} /><Button label="Cancel" kind="quiet" onClick={() => setEdit(null)} /></>}
        <StateBadge family="setting" state={s.kind} compact reason={s.kind === 'applied' ? age(s.at, p.now).text : s.kind === 'apply_error' ? s.error : undefined} />
        {s.kind === 'apply_error' && <Button label="Retry" icon={actions.retry} iconOnly />}
        {p.changed && <span class="muted">{p.changed.by} · {age(p.changed.at, p.now).text}</span>}
      </div>
      <div class="help">{p.help}</div>
    </div>
  );
}

/* ---------- Problem row ---------- */

export function ProblemRow({ title, facts, severity, actions: acts }: {
  title: string; facts: ComponentChildren; severity: 'warning' | 'failure';
  actions: { label: string; icon?: IconName; primary?: boolean; onClick?: () => void }[];
}) {
  return (
    <div data-ui="problem-row" class={`problem ${severity}`} role="group" aria-label={`${severity === 'failure' ? 'Failure' : 'Warning'}: ${title}`}>
      <Icon name={severity === 'failure' ? 'octagon-x' : 'triangle-alert'} />
      <b>{title}</b><span class="facts" data-important="">{facts}</span>
      <span class="acts">{acts.map(a => <Button key={a.label} label={a.label} icon={a.icon} kind={a.primary ? 'primary' : 'default'} onClick={a.onClick} />)}</span>
    </div>
  );
}

/* ---------- Empty, loading, error, stale (§8) ---------- */

export const Skeleton = ({ rows }: { rows: number }) => (
  <div class="skeleton" data-ui="status-states" aria-busy="true" aria-label="Loading">{Array.from({ length: rows }, (_, i) => <div key={i} class="sk-row"><i style={{ width: `${55 + (i * 17) % 35}%` }} /><i /></div>)}</div>
);
export const Empty = ({ text, action }: { text: string; action?: ComponentChildren }) => <div class="empty" data-ui="status-states"><p>{text}</p>{action}</div>;
export const ErrorBanner = ({ text, onRetry }: { text: string; onRetry?: () => void }) => (
  <div class="err-banner" data-ui="status-states" role="alert"><Icon name="circle-alert" /><span>{text}</span><span class="grow" /><Button label="Retry" icon={actions.retry} onClick={onRetry} /></div>
);
export const Stale = ({ at, now, onRefresh }: { at: number; now: number; onRefresh?: () => void }) => (
  <span class="stale" data-ui="status-states">updated {age(at, now).text} <Button label="Refresh" icon={actions.retry} iconOnly onClick={onRefresh} /></span>
);

/* ---------- Thread rows (transcript-viewer.md; docs/designs/fleet-conversations.md) ---------- */

/** Writes text to the clipboard; the caller shows a failure (status line with retry). */
export const copyText = (text: string) => navigator.clipboard.writeText(text);

/** The one formatter of an entity id (D17, OR-38). With a human code: `T-FLT-42` in mono after the name. Without one
 *  (conversation, run, experiment) and with `what`: a copy icon; otherwise nothing. The UUID is never shown as text:
 *  it is the tooltip, and a click or Enter copies it without opening the row (OR-26). */
export function EntityCode({ code, id, cls, what }: { code?: string | null; id: string; cls?: string; what?: string }) {
  const [tip, setTip] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  useEffect(() => setTip(null), [id]);
  if (!code && !what) return null;
  const copy = (e: Event) => {
    e.preventDefault(); e.stopPropagation();
    const show = (t: string) => { setTip(t); clearTimeout(timer.current); timer.current = setTimeout(() => setTip(null), 1500); };
    copyText(id).then(() => show('UUID copied'), () => show('Copy failed'));
  };
  const label = `${code ?? what}, copy UUID`, dataTip = tip ?? `${what ? what + ' ' : ''}${id} · click to copy`;
  if (!code) return <button type="button" class={`btn quiet icon-only${cls ? ' ' + cls : ''}`} data-ui="entity-code" aria-label={label} data-tip={dataTip} onClick={copy}><Icon name={actions.copy} /></button>;
  return <span class={`id code${cls ? ' ' + cls : ''}`} data-ui="entity-code" role="button" tabIndex={0} aria-label={label} data-tip={dataTip}
    onClick={copy} onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') copy(e); }}>{code}</span>;
}

/** Markdown (markdown.tsx): built as nodes, never as HTML; each fenced code block has its own copy button. */
export function Markdown({ text, onCopyFail }: { text: string; onCopyFail?: (e: unknown) => void }) {
  return <div class="md">{renderBlocks(parse(text), (code, _lang, k) => <div class="code" key={k}><pre><code>{code}</code></pre><span class="code-copy"><Button label="Copy code" icon={actions.copy} iconOnly kind="quiet" onClick={() => copyText(code).catch(e => onCopyFail?.(e))} /></span></div>)}</div>;
}

type Mark = { harness?: string | null; model?: string | null; effort?: string | null };
/** Agent mark (D4): harness icon and the short label; the model id on hover. */
export function AgentMark({ harness, model, effort }: Mark) {
  return <span class="agent-mark">{harness && harness in harnesses && <HarnessMark h={harness as Harness} />}<span class="lbl" data-tip={model ?? undefined}>{agentLabel(model, effort) || harness /* no model recorded: the harness still names the author */}</span></span>;
}

/** An operator or agent message: a head line with who wrote it and when (OR-2), then the content; copy on the right (OR-21).
 *  Copy writes the head line and the body as plain markdown and as chat-safe HTML (clip.ts, formats.md "Copy"). */
export function TextRow({ who, markdown, mark, by, at, now, onCopyFail }: { who: 'operator' | 'agent'; markdown: string; mark?: Mark; by?: string | null; at?: number | null; now?: number; onCopyFail?: (e: unknown) => void }) {
  const t = now ?? Date.now(), name = author({ role: who, by, ...mark });
  return <div class={`th-row th-text ${who}`} data-ui="thread-row">
    <div class="th-main">
      <div class="th-who">{who === 'operator' ? <span class="who" data-tip={by ?? undefined}>{name}</span> : <AgentMark {...(mark ?? {})} />}
        {at ? <span class="when" data-tip={exact(at)} aria-label={age(at, t).long}>{age(at, t).text}</span> : null}</div>
      <Markdown text={markdown} onCopyFail={onCopyFail} /></div>
    <span class="th-svc"><Button label="Copy message" icon={actions.copy} iconOnly kind="quiet" onClick={() => writeClip(clipMessage(name, at, t, markdown)).catch(e => onCopyFail?.(e))} /></span>
  </div>;
}

const raw = (v: unknown) => typeof v === 'string' ? v : JSON.stringify(v, null, 1) ?? '';
const fenced = (v: unknown) => { const t = raw(v), ticks = '`'.repeat(Math.max(3, ...[...t.matchAll(/`+/g)].map(m => m[0].length + 1))); return ticks + '\n' + t + '\n' + ticks; };
/** Raw tool input or output: preformatted as is (never parsed as markdown), with its own copy. */
function CodeBlock({ text, onCopyFail }: { text: string; onCopyFail?: (e: unknown) => void }) {
  return <div class="md"><div class="code"><pre><code>{text}</code></pre><span class="code-copy"><Button label="Copy code" icon={actions.copy} iconOnly kind="quiet" onClick={() => copyText(text).catch(e => onCopyFail?.(e))} /></span></div></div>;
}
/** A collapsed one-line row that opens downward on click; the clicked header stays where it is (OR-23). */
function Fold({ icon, head, body, open, onToggle, copy, onCopyFail }: { icon: IconName; head: ComponentChildren; body: ComponentChildren; open: boolean; onToggle: () => void; copy: string; onCopyFail?: (e: unknown) => void }) {
  return <div class={`th-row th-fold${open ? ' open' : ''}`} data-ui="thread-row">
    <button type="button" class="th-head" aria-expanded={open} onClick={onToggle}><span class={`chev${open ? ' open' : ''}`}><Icon name="chevron-down" /></span><Icon name={icon} />{head}</button>
    <span class="th-svc"><Button label="Copy" icon={actions.copy} iconOnly kind="quiet" onClick={() => copyText(copy).catch(e => onCopyFail?.(e))} /></span>
    {open && <div class="th-body">{body}</div>}
  </div>;
}
export function ToolRow({ name, input, output, state, startedAt, endedAt, open, onToggle, onCopyFail }: { name: string; input: unknown; output: unknown; state: string; startedAt?: number | null; endedAt?: number | null; open: boolean; onToggle: () => void; onCopyFail?: (e: unknown) => void }) {
  const secs = startedAt != null && endedAt != null ? duration((endedAt - startedAt) / 1000).text : null;
  const copy = [`Tool ${name} (${state})`, input != null ? fenced(input) : '', output != null ? fenced(output) : ''].filter(Boolean).join('\n\n');
  return <Fold icon="terminal" open={open} onToggle={onToggle} copy={copy} onCopyFail={onCopyFail}
    head={<span class="th-line"><span class="mono">{String(name)}</span><span class={`svc${['failed', 'unfinished'].includes(state) ? ' t-danger' : ''}`}>{state}</span>{secs && <span class="svc">{secs}</span>}</span>}
    body={<>{input != null && <CodeBlock text={raw(input)} onCopyFail={onCopyFail} />}{output != null && <CodeBlock text={raw(output)} onCopyFail={onCopyFail} />}</>} />;
}
export function ReasoningRow({ markdown, open, onToggle, onCopyFail }: { markdown: string; open: boolean; onToggle: () => void; onCopyFail?: (e: unknown) => void }) {
  return <Fold icon="bot" open={open} onToggle={onToggle} copy={markdown} onCopyFail={onCopyFail}
    head={<span class="th-line"><span>Reasoning</span><span class="svc ell">{markdown.split('\n')[0]}</span></span>} body={<Markdown text={markdown} onCopyFail={onCopyFail} />} />;
}
/** A question in the thread (DecisionBlock); answered, it collapses to one line with the answer and who gave it. */
export function QuestionRow({ text, options, state, answer, answeredBy, askedAt, answeredAt, now, onAnswer, onSecret }: { text: string; options: unknown[]; state: string; answer?: string; answeredBy?: string; askedAt?: number | null; answeredAt?: number | null; now: number; onAnswer?: (a: string) => void; onSecret?: (value: string) => Promise<unknown> }) {
  return <div class="th-row th-q" data-ui="thread-row"><DecisionBlock question={text} askedBy="agent" addressedTo="you" since={askedAt ?? now} now={now}
    options={options.map((o, i) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
    answered={state === 'answered' && answer !== undefined ? { by: actor(answeredBy) || 'operator', answer, at: answeredAt ?? now, delivered: true } : undefined} onAnswer={onAnswer} onSecret={onSecret} /></div>;
}
/** The connection a key request (a secret question) asks a key for: its options are {connection}. */
export const keyConnection = (options: unknown): string | undefined => {
  const c = options && !Array.isArray(options) && typeof options === 'object' ? (options as { connection?: unknown }).connection : undefined;
  return typeof c === 'string' ? c : undefined;
};
/** Results of an execution end: artifact names linking to their Results page entry. */
export function ResultRow({ artifacts, onOpen }: { artifacts: ArtifactMeta[]; onOpen?: (id: string) => void }) {
  return <div class="th-row th-result" data-ui="thread-row"><Icon name="file-text" /><span class="th-line">{artifacts.map(a =>
    <a key={a.id} href={`/results/${encodeURIComponent(a.id)}`} data-tip={`${a.mediaType} · ${bytes(a.size).text}`} onClick={e => { if (onOpen && !e.metaKey && !e.ctrlKey) { e.preventDefault(); onOpen(a.id); } }}>{a.name}</a>)}</span></div>;
}
/** An execution start: state, agent mark, duration chip, and Stop while it runs (CUJ 5). */
export function BoundaryRow({ session, executionId, state, mark, startedAt, endedAt, now, onStop, stopReason }: { session?: string; executionId: string; state: StateOf<'execution'>; mark: Mark; startedAt?: number | null; endedAt?: number | null; now: number; onStop?: () => void; stopReason?: string }) {
  const secs = startedAt != null ? ((endedAt ?? now) - startedAt) / 1000 : null;
  return <div class="th-row th-boundary" data-ui="thread-row"><StateBadge family="execution" state={state} compact />{session && <EntityCode id={session} what="Conversation" />}<EntityCode id={executionId} what="Run" /><span class="grow" />
    <span class="th-svc"><AgentMark {...mark} />{secs !== null && <span class="chip-age">{duration(secs).text}</span>}{onStop && <Button label="Stop" icon="pause" iconOnly disabledReason={stopReason} onClick={onStop} />}</span></div>;
}

/** A search field for one scope (a thread): Enter searches. */
export function SearchField({ label, value, onInput, onSearch }: { label: string; value: string; onInput: (v: string) => void; onSearch: () => void }) {
  return <label class="search"><Icon name={actions.search} /><input type="search" placeholder={label} aria-label={label} value={value}
    onInput={e => onInput((e.target as HTMLInputElement).value)} onKeyDown={e => { if (e.key === 'Enter') onSearch(); }} /></label>;
}
import type { Block as ThreadBlock } from '../../../contracts/generated/ops.ts';
export type { ThreadBlock };
/**
 * A thread: one row per block in a measured VirtualList that follows the bottom, loads older rows upward (onStart) and
 * newer ones downward (onEnd), and jumps through `handle`. Open tool and reasoning rows are kept by key across the window.
 */
export function Thread({ blocks, label, now, marked, selected, onRowClick, showSession, onOpenResult, handle, pending, onStart, onEnd, onStop, stopReason, onAnswer, onCopyFail, onSecret }: {
  blocks: ThreadBlock[]; label: string; now: number; marked?: string | null;
  /** Cross-message selection: rows in it are tinted; a click on a row (Shift extends) selects it, buttons and links excepted. */
  selected?: (key: string) => boolean; onRowClick?: (key: string, shift: boolean) => void; showSession?: boolean; onOpenResult?: (id: string) => void; handle?: { current: VirtualListHandle | null }; pending?: { count: number; onJump: () => void };
  onStart?: () => void; onEnd?: () => void; onStop?: (executionId: string) => void; stopReason?: string;
  onAnswer?: (questionId: string, answer: string) => void; onCopyFail?: (e: unknown) => void;
  /** A key request row (its options name a connection): the owner's value goes here, never to onAnswer. */
  onSecret?: (questionId: string, connection: string, value: string) => Promise<unknown>;
}) {
  const [open, setOpen] = useState<Set<string>>(new Set());
  const toggle = (k: string) => setOpen(o => { const n = new Set(o); n.has(k) ? n.delete(k) : n.add(k); return n; });
  const markOf = (b: Mark): Mark => ({ harness: b.harness, model: b.model, effort: b.effort });
  const row = (b: ThreadBlock) => {
    switch (b.kind) {
      case 'text': return <TextRow who={b.role === 'operator' ? 'operator' : 'agent'} markdown={b.markdown ?? ''} mark={markOf(b)} by={b.by} at={b.at} now={now} onCopyFail={onCopyFail} />;
      case 'tool': return <ToolRow name={b.name} input={b.input} output={b.output} state={b.state} startedAt={b.startedAt} endedAt={b.endedAt} open={open.has(b.key)} onToggle={() => toggle(b.key)} onCopyFail={onCopyFail} />;
      case 'reasoning': return <ReasoningRow markdown={b.markdown ?? ''} open={open.has(b.key)} onToggle={() => toggle(b.key)} onCopyFail={onCopyFail} />;
      case 'question': { const conn = keyConnection(b.options); return <QuestionRow text={b.text} options={Array.isArray(b.options) ? b.options : conn ? ['decline'] : []} state={b.state} answer={b.answer === undefined || typeof b.answer === 'string' ? b.answer : JSON.stringify(b.answer)} answeredBy={b.answeredBy} askedAt={b.askedAt} answeredAt={b.answeredAt} now={now} onAnswer={onAnswer && (a => onAnswer(b.questionId, a))} onSecret={conn && onSecret ? v => onSecret(b.questionId, conn, v) : undefined} />; }
      case 'result': return <ResultRow artifacts={b.artifacts} onOpen={onOpenResult} />;
      case 'boundary': return <BoundaryRow session={showSession ? b.sessionId ?? undefined : undefined} executionId={b.executionId} state={b.state} mark={markOf(b)} startedAt={b.startedAt} endedAt={b.endedAt} now={now} stopReason={stopReason}
        onStop={onStop && ['preparing', 'running', 'waiting'].includes(b.state) ? () => onStop(b.executionId) : undefined} />;
      default: return null;
    }
  };
  return <div class="transcript" data-ui="transcript-viewer">
    <VirtualList class="tr-body" items={blocks} getKey={b => b.key} rowHeight={ROW} dynamic follow="bottom" label={label} handle={handle} pending={pending} onStart={onStart} onEnd={onEnd}
      rowClass={b => [b.key === marked ? 'marked' : '', selected?.(b.key) ? 'picked' : ''].join(' ')}
      render={b => <div class="th-pick" style={{ display: 'contents' }} onClick={e => { if (!onRowClick || (e.target as HTMLElement).closest('button,a,input') || !(getSelection()?.isCollapsed ?? true) && !e.shiftKey) return; if (e.shiftKey) getSelection()?.removeAllRanges(); ((e.currentTarget as HTMLElement).closest('.vl') as HTMLElement | null)?.focus({ preventScroll: true }); onRowClick(b.key, e.shiftKey); }}>{row(b)}</div>} />
  </div>;
}

/* ---------- Split button (split-button.md) ---------- */

/** Default action plus a short menu of alternatives for the same place (OR-14 allows short action menus). */
export function SplitButton({ main, alternatives, kind = 'default' }: {
  main: { label: string; icon?: IconName; onClick?: () => void };
  alternatives: { label: string; icon?: IconName; onClick?: () => void }[]; kind?: 'primary' | 'default';
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: Event) => { if (!(e instanceof KeyboardEvent) || e.key === 'Escape') if (!ref.current?.contains(e.target as Node) || e instanceof KeyboardEvent) setOpen(false); };
    document.addEventListener('pointerdown', close); document.addEventListener('keydown', close);
    return () => { document.removeEventListener('pointerdown', close); document.removeEventListener('keydown', close); };
  }, [open]);
  return (
    <span class={`split ${kind}`} data-ui="split-button" ref={ref}>
      <Button label={main.label} icon={main.icon} kind={kind} onClick={main.onClick} />
      <button type="button" class={`btn ${kind} split-more`} aria-haspopup="menu" aria-expanded={open} aria-label={`More actions for ${main.label}`} onClick={() => setOpen(!open)}><Icon name="chevron-down" /></button>
      {open && <span class="menu" role="menu">{alternatives.map(a =>
        <button type="button" role="menuitem" key={a.label} onClick={() => { setOpen(false); a.onClick?.(); }}>{a.icon && <Icon name={a.icon} />}{a.label}</button>)}</span>}
    </span>
  );
}

/* ---------- Action menu: one icon button, a short left-aligned menu with icons (OR-13, OR-14) ---------- */

export function ActionMenu({ label, items, disabledReason }: { label: string; items: { label: string; icon: IconName; onClick: () => void; disabledReason?: string }[]; disabledReason?: string }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: Event) => { if (e instanceof KeyboardEvent ? e.key === 'Escape' : !ref.current?.contains(e.target as Node)) setOpen(false); };
    document.addEventListener('pointerdown', close); document.addEventListener('keydown', close);
    return () => { document.removeEventListener('pointerdown', close); document.removeEventListener('keydown', close); };
  }, [open]);
  return <span class="split action-menu" data-ui="split-button" ref={ref}>
    <button type="button" class="btn default icon-only" aria-haspopup="menu" aria-expanded={open} aria-label={label} data-tip={disabledReason ?? label} aria-disabled={!!disabledReason || undefined}
      onClick={() => !disabledReason && setOpen(!open)}><Icon name={actions.more} /></button>
    {open && <span class="menu" role="menu">{items.map(a =>
      <button type="button" role="menuitem" key={a.label} aria-disabled={!!a.disabledReason || undefined} data-tip={a.disabledReason} onClick={() => { if (a.disabledReason) return; setOpen(false); a.onClick(); }}><Icon name={a.icon} />{a.label}</button>)}</span>}
  </span>;
}

/* ---------- State counters: quick filter by state with totals (state-counters.md) ---------- */

export function StateCounters<F extends Family>({ family, counts, active, onToggle, onAll, label, pending, hideZero }: {
  family: F; counts: Partial<Record<StateOf<F>, number | null>>; active: StateOf<F>[]; onToggle: (s: StateOf<F>) => void;
  /** Σ button: show all (clear the state filter). Omit to hide the Σ button. */
  onAll?: () => void; label: string;
  /** A newer answer is loading: the last counts stay, dimmed (docs/designs/fleet-view-urls-and-list-path.md §2). */
  pending?: boolean;
  /** A counter block, not a quick filter (OR-29): zero chips are hidden, and the caller hides an all-zero block. */
  hideZero?: boolean;
}) {
  const all = Object.keys(states[family]) as StateOf<F>[];
  const values = all.map(s => counts[s]).filter(n => n !== undefined) as (number | null)[];
  const total = values.includes(null) ? null : (values as number[]).reduce((a, b) => a + b, 0);
  return (
    <div class={`counters${pending ? ' pending' : ''}`} data-ui="state-counters" role="group" aria-label={label} aria-busy={pending || undefined}>
      {onAll && <button type="button" class={`counter t-neutral sigma${active.length === 0 ? ' on' : ''}`} aria-pressed={active.length === 0}
        aria-label={`All: ${total === null ? 'unknown' : total}`} onClick={onAll}><span class="sig" aria-hidden="true">Σ</span><span class="num">{total === null ? '?' : total.toLocaleString('en')}</span></button>}
      {all.map(s => {
        const d = states[family][s] as StateDef, n = counts[s];
        if (n === undefined || (hideZero && n === 0)) return null;
        const on = active.includes(s);
        const name = `${d.label}: ${n === null ? 'unknown' : n}`;
        return <button type="button" key={String(s)} class={`counter t-${d.tone}${on ? ' on' : ''}${n === 0 ? ' zero' : ''}`} aria-pressed={on} aria-label={name} data-tip={d.label} onClick={() => onToggle(s)}>
          <Icon name={d.icon} />{n !== 0 && <span class="num">{n === null ? '?' : n.toLocaleString('en')}</span>}</button>;
      })}
    </div>
  );
}

/* ---------- Group header row for grouped lists ---------- */

export function GroupHeader({ icon, label, count, open, onToggle, tone = 'neutral' }: { icon: IconName; label: string; count: number; open: boolean; onToggle: () => void; tone?: Tone }) {
  return <button type="button" class={`group-h t-${tone}`} aria-expanded={open} onClick={onToggle}>
    <span class={open ? 'chev open' : 'chev'}><Icon name="chevron-down" /></span><Icon name={icon} /><b>{label}</b><span class="num">{count.toLocaleString('en')}</span></button>;
}

/* ---------- Presets: saved views per person (filter-bar.md) ---------- */

export function Presets({ presets, active, onPick, onSave, onUnpin }: {
  presets: string[]; active: string | null; onPick: (name: string) => void; onSave: (name: string) => void; onUnpin: (name: string) => void;
}) {
  const [naming, setNaming] = useState<string | null>(null);
  return (
    <div class="presets" data-ui="filter-bar" role="group" aria-label="Views">
      {presets.map(n => <span key={n} class={`preset${n === active ? ' on' : ''}`}>
        <button type="button" aria-pressed={n === active} onClick={() => onPick(n)}><Icon name="pin" />{n}</button>
        <button type="button" class="x" aria-label={`Unpin view ${n}`} data-tip="Unpin" onClick={() => onUnpin(n)}><Icon name="x" /></button></span>)}
      {naming === null
        ? <Button label="Save view" icon="save" kind="quiet" onClick={() => setNaming('')} />
        : <span class="naming"><input value={naming} placeholder="View name" aria-label="View name" ref={e => e?.focus()} onInput={e => setNaming((e.target as HTMLInputElement).value)}
            onKeyDown={e => { if (e.key === 'Enter' && naming.trim()) { onSave(naming.trim()); setNaming(null); } if (e.key === 'Escape') setNaming(null); }} />
          <Button label="Save" kind="primary" onClick={() => { if (naming.trim()) onSave(naming.trim()); setNaming(null); }} /></span>}
    </div>
  );
}

/* ---------- Navigation rail with pinned sections (nav-rail.md) ---------- */

export type NavItem = { id: string; label: string; icon: IconName; group: string; count?: { n: number; tone: Tone; label: string } };
export function NavRail({ items, pinned: stored, current, onPins, onGo, mini, head, onSearch, foot }: {
  items: NavItem[]; current: string; onGo: (id: string) => void;
  /** The person's pinned ids in their order; onPins gets the whole new list (pin, unpin, reorder). */ pinned: string[]; onPins: (ids: string[]) => void;
  /** Collapsed to icons. */ mini?: boolean; /** Product name and need-you chip (app shell). */ head?: ComponentChildren; /** The current user block, at the bottom. */ foot?: ComponentChildren;
  /** Opens the Spotlight palette (OR-31, OR-32); the rail has no search field of its own. */ onSearch?: () => void;
}) {
  // Ids of sections that no longer exist are dropped here, so they never take a position and leave on the next save.
  const pinned = stored.filter(id => items.some(i => i.id === id));
  const [menu, setMenu] = useState<string | null>(null);
  const [drag, setDrag] = useState<string | null>(null);
  const navRef = useRef<HTMLElement>(null);
  useEffect(() => {
    if (!menu) return;
    const close = (e: Event) => {
      if (e instanceof KeyboardEvent) { if (e.key !== 'Escape') return; e.stopPropagation(); focusItem(menu.slice(2), menu[0] === 'p'); }
      else if ((e.target as Element).closest?.('.rail-menu')) return;
      setMenu(null);
    };
    document.addEventListener('pointerdown', close); document.addEventListener('keydown', close, true);
    return () => { document.removeEventListener('pointerdown', close); document.removeEventListener('keydown', close, true); };
  }, [menu]);
  const focusItem = (id: string, inPinned: boolean) => requestAnimationFrame(() =>
    navRef.current?.querySelector<HTMLElement>(`.rail-g${inPinned ? '.pinned' : ':not(.pinned)'} [data-id="${id}"] a`)?.focus());
  // An item unpinned from the pinned group leaves it: focus follows to the same item in its group.
  const toggle = (id: string, inPinned = false) => { onPins(pinned.includes(id) ? pinned.filter(x => x !== id) : [...pinned, id]); if (inPinned) focusItem(id, false); };
  // move: the pinned item to position `to` (clamped); keeps focus on the moved item for keyboard reorder.
  const move = (id: string, to: number, refocus = false) => {
    const rest = pinned.filter(x => x !== id), at = Math.max(0, Math.min(rest.length, to));
    if (pinned.indexOf(id) === at) return;
    onPins([...rest.slice(0, at), id, ...rest.slice(at)]);
    if (refocus) focusItem(id, true);
  };
  const item = (it: NavItem, inPinned: boolean) => {
    const isPinned = pinned.includes(it.id), i = pinned.indexOf(it.id);
    return <li key={it.id} data-id={it.id} class={`nav-i${it.id === current ? ' cur' : ''}${isPinned ? ' pinned' : ''}${drag === it.id && inPinned ? ' dragging' : ''}`}
      draggable={inPinned} onDragStart={inPinned ? e => { setDrag(it.id); e.dataTransfer?.setData('text/plain', it.id); } : undefined}
      onDragOver={inPinned && drag ? e => e.preventDefault() : undefined}
      onDrop={inPinned && drag ? e => { e.preventDefault(); const r = (e.currentTarget as HTMLElement).getBoundingClientRect(); const after = e.clientY > r.top + r.height / 2;
        if (drag !== it.id) move(drag, pinned.filter(x => x !== drag).indexOf(it.id) + (after ? 1 : 0)); setDrag(null); } : undefined}
      onDragEnd={() => setDrag(null)}
      onContextMenu={e => { e.preventDefault(); setMenu(`${inPinned ? 'p' : 'g'}:${it.id}`); }}>
      <a href={`#/${it.id}`} aria-current={it.id === current ? 'page' : undefined} aria-keyshortcuts={inPinned ? 'Alt+ArrowUp Alt+ArrowDown' : undefined} onClick={e => { e.preventDefault(); onGo(it.id); }}
        onKeyDown={e => {
          if (e.key === 'p' && !e.altKey && !e.ctrlKey && !e.metaKey) { e.preventDefault(); toggle(it.id, inPinned); }
          else if (inPinned && e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) { e.preventDefault(); move(it.id, i + (e.key === 'ArrowUp' ? -1 : 1), true); }
          else if (e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) { e.preventDefault(); setMenu(`${inPinned ? 'p' : 'g'}:${it.id}`); }
        }}>
        <span data-tip={it.label} data-tip-narrow class="nav-ic"><Icon name={it.icon} /></span><span class="nm">{it.label}</span>
        {it.count && it.count.n > 0 && <span class={`nav-count t-${it.count.tone}`} aria-label={`${it.count.n} ${it.count.label}`}>{it.count.n}</span>}
      </a>
      <button type="button" class={`nav-pin${isPinned ? ' on' : ''}`} aria-pressed={isPinned} aria-label={`${isPinned ? 'Unpin' : 'Pin'} ${it.label}`} data-tip={isPinned ? 'Unpin' : 'Pin'} onClick={() => toggle(it.id, inPinned)}>
        <Icon name="pin" /></button>
      {menu === `${inPinned ? 'p' : 'g'}:${it.id}` && <span class="menu rail-menu" role="menu" aria-label={it.label}>
        <button type="button" role="menuitem" autoFocus onClick={act(() => toggle(it.id, inPinned))}><Icon name={i < 0 ? 'pin' : 'pin-off'} />{i < 0 ? 'Pin' : 'Unpin'}</button>
        {i > 0 && <button type="button" role="menuitem" onClick={act(() => move(it.id, i - 1, true))}><Icon name="arrow-up" />Move up</button>}
        {i >= 0 && i < pinned.length - 1 && <button type="button" role="menuitem" onClick={act(() => move(it.id, i + 1, true))}><Icon name="arrow-down" />Move down</button>}
      </span>}
    </li>;
  };
  const groups = [...new Set(items.map(i => i.group))];
  const pins = pinned.map(id => items.find(i => i.id === id)).filter(Boolean) as NavItem[];
  const act = (f: () => void) => () => { setMenu(null); f(); };
  return (
    <nav class={`rail${mini ? ' mini' : ''}`} data-ui="nav-rail" aria-label="Sections" ref={navRef}>
      {head && <div class="rail-head">{head}</div>}
      {pins.length > 0 && <ul class="rail-g pinned" aria-label="Pinned">{pins.map(i => item(i, true))}</ul>}
      {onSearch && <ul class="rail-g"><li class="nav-i"><a href="#search" aria-keyshortcuts="Meta+K Control+K" onClick={e => { e.preventDefault(); onSearch(); }}>
        <span data-tip="Search  ⌘K" data-tip-narrow class="nav-ic"><Icon name={actions.search} /></span><span class="nm">Search</span></a></li></ul>}
      {groups.map(g => <ul key={g} class="rail-g" aria-label={g}><li class="rail-h">{g}</li>{items.filter(i => i.group === g).map(i => item(i, false))}</ul>)}
      {foot && <div class="rail-foot">{foot}</div>}
    </nav>
  );
}

/* ---------- Spotlight palette (OR-32, controls/spotlight.md) ---------- */

/** One found item: run opens it. code is the human entity code (D17), shown mono after the label. */
export type SpotlightItem = { key: string; icon: IconName; label: string; code?: string; sub?: string; run: () => void };
/** One kind of thing the palette finds. A new kind is one more provider; providers answer in their order. */
export type SpotlightProvider = { id: string; label: string; find: (q: string, signal: AbortSignal) => SpotlightItem[] | Promise<SpotlightItem[]> };

/** The palette: the only floating working tool (OR-32). Arrow keys move, Enter opens, Escape or a click outside closes. */
export function Spotlight({ providers, onClose }: { providers: SpotlightProvider[]; onClose: () => void }) {
  const [q, setQ] = useState('');
  const [found, setFound] = useState<{ p: SpotlightProvider; items: SpotlightItem[] }[]>([]);
  const [at, setAt] = useState(0);
  // waiting: providers not answered yet; failed: one of them failed, so "nothing found" would be a guess.
  const [waiting, setWaiting] = useState(0), [failed, setFailed] = useState(false);
  // Focus returns to where it was only when the palette closes without opening an item.
  const back = useRef<Element | null>(document.activeElement), opened = useRef(false);
  useEffect(() => () => { if (!opened.current) (back.current as HTMLElement | null)?.focus?.(); }, []);
  useEffect(() => {
    // Each provider's results show as they arrive, in provider order; local ones are not held back by the server.
    const c = new AbortController(), text = q.trim(), got: (SpotlightItem[] | undefined)[] = providers.map(() => undefined);
    const show = () => { if (!c.signal.aborted) setFound(providers.map((p, i) => ({ p, items: got[i] ?? [] })).filter(g => g.items.length)); };
    setFailed(false); setAt(0); setEnter(false);
    if (!text) { setFound([]); setWaiting(0); return; }
    let left = 0;
    providers.forEach((p, i) => {
      const r = p.find(text, c.signal);
      if (Array.isArray(r)) { got[i] = r; return; }
      left++;
      r.then(x => { got[i] = x; }, () => { if (!c.signal.aborted) setFailed(true); }).then(() => { if (!c.signal.aborted) { setWaiting(--left); show(); } });
    });
    setWaiting(left); show();
    return () => c.abort();
  }, [q]);
  const flat = found.flatMap(g => g.items);
  const open = (i: SpotlightItem | undefined) => { if (i) { opened.current = true; onClose(); i.run(); } };
  // Enter before every provider answered opens the first result once they have, not a section that happened to answer first.
  const [enter, setEnter] = useState(false);
  useEffect(() => { if (enter && !waiting) { setEnter(false); open(flat[at]); } }, [enter, waiting]);
  const key = (e: KeyboardEvent) => {
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); onClose(); }
    else if (e.key === 'Tab') e.preventDefault(); // aria-modal: focus stays on the field
    else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); if (flat.length) setAt((at + (e.key === 'ArrowDown' ? 1 : -1) + flat.length) % flat.length); }
    else if (e.key === 'Enter' && !e.isComposing) { e.preventDefault(); if (waiting) setEnter(true); else open(flat[at]); }
  };
  const list = useId();
  let n = -1;
  return <div class="spot-back" onPointerDown={e => { if (e.target === e.currentTarget) onClose(); }}>
    <div class="spotlight" data-ui="spotlight" role="dialog" aria-modal="true" aria-label="Search" onKeyDown={key}>
      <label class="search"><Icon name={actions.search} /><input type="search" autoFocus placeholder="Code, task, section, project, host · agent:claude host:mac" aria-label="Search"
        role="combobox" list={undefined} aria-expanded={flat.length > 0} aria-controls={list} aria-activedescendant={flat[at] ? `${list}-${at}` : undefined}
        value={q} onInput={e => setQ((e.target as HTMLInputElement).value)} /></label>
      {flat.length > 0 && <ul class="spot-list" id={list} role="listbox" aria-label="Results">{found.map(g => <Fragment key={g.p.id}>
        <li key={g.p.id} class="rail-h" role="presentation">{g.p.label}</li>
        {g.items.map(i => { const k = ++n; return <li key={i.key} id={`${list}-${k}`} role="option" aria-selected={k === at} class={k === at ? 'cur' : ''}
          onPointerMove={() => k !== at && setAt(k)} onClick={() => open(i)}>
          <Icon name={i.icon} /><span class="title">{i.label}</span>{i.code && <span class="id">{i.code}</span>}{i.sub && <span class="muted">{i.sub}</span>}</li>; })}
      </Fragment>)}</ul>}
      {q.trim() && !flat.length && !waiting && <p class="muted spot-none" role="status">{failed ? 'Search failed; try again' : 'Nothing found'}</p>}
    </div>
  </div>;
}

/** Sections as a palette provider: the navigation items whose label holds the text. A field term (agent:x) finds none. */
export const sectionProvider = (items: NavItem[], onGo: (id: string) => void): SpotlightProvider => ({
  id: 'sections', label: 'Sections',
  find: q => items.filter(i => matchSearch(q, i.label)).map(i => ({ key: 's:' + i.id, icon: i.icon, label: i.label, run: () => onGo(i.id) })),
});

/* ---------- Stat strip: operational counts (stat-strip.md) ---------- */

export type Stat = { label: string; icon: IconName; value: Measured; kind?: Kind; of?: number; tone?: Tone; href?: string; harness?: Partial<Record<Harness, number>> };
export function StatStrip({ stats, label }: { stats: Stat[]; label: string }) {
  return (
    <div class="stats" data-ui="stat-strip" role="list" aria-label={label}>
      {stats.map(s => {
        const body = <><Icon name={s.icon} /><span class="sv"><Num m={s.value} kind={s.kind ?? 'count'} />{s.of !== undefined && <span class="of">/{s.of}</span>}</span><span class="sl">{s.label}</span>{s.harness && <HarnessCounts counts={s.harness} label={`${s.label} by harness`} />}</>;
        return <span role="listitem" key={s.label} class={`stat${s.tone ? ` t-${s.tone} toned` : ''}`}>{s.href ? <a href={s.href}>{body}</a> : body}</span>;
      })}
    </div>
  );
}

/* ---------- Harness mark, harness counts, stage chip (D11) ---------- */

/** Which harness runs it: the mark only, name in the tooltip (OR-1, OR-20). */
export const HarnessMark = ({ h }: { h: Harness }) =>
  <span class="harness" data-ui="harness-counts" data-tip={harnesses[h].label} role="img" aria-label={harnesses[h].label}><Icon name={harnesses[h].icon} /></span>;

/** Counts per harness as mark + number; zero counts are omitted. */
export function HarnessCounts({ counts, label }: { counts: Partial<Record<Harness, number>>; label: string }) {
  const parts = (Object.keys(harnesses) as Harness[]).filter(h => (counts[h] ?? 0) > 0);
  return <span class="harness-counts" data-ui="harness-counts" role="group" aria-label={label}>
    {parts.map(h => <span key={h} class="hc" data-tip={harnesses[h].label} aria-label={`${harnesses[h].label} ${counts[h]}`}><Icon name={harnesses[h].icon} /><span class="num">{counts[h]}</span></span>)}
  </span>;
}

/** Delivery stage as a compact chip: icon, plus the short name only where the icon is not enough. */
export function StageChip({ stage }: { stage: Stage }) {
  const d = stages[stage] as StateDef;
  return <span class={`badge compact t-${d.tone}`} data-ui="stage-chip" role="img" aria-label={d.label} data-tip={d.label}><Icon name={d.icon} /></span>;
}

/* ---------- Outcome block (D11, B2) ---------- */

export function OutcomeBlock({ outcome, requirements, inputs, depends }: {
  outcome: string; requirements: { text: string; met: boolean }[]; inputs: { icon: IconName; label: string }[]; depends?: string;
}) {
  const [open, setOpen] = useState(false);
  const met = requirements.filter(r => r.met).length;
  return <div class="outcome" data-ui="outcome-block">
    <button type="button" class="tw" aria-expanded={open} aria-label={open ? 'Collapse outcome' : 'Expand outcome'} onClick={() => setOpen(!open)}><span class={open ? 'chev open' : 'chev'}><Icon name="chevron-down" /></span></button>
    <div class="outcome-b">
      <p class={open ? '' : 'clamp'}>{outcome}</p>
      {(requirements.length > 0 || inputs.length > 0) && <div class="outcome-meta">{requirements.length > 0 && <span class={`chip-age${met < requirements.length ? '' : ' t-success alert'}`} aria-label={`${met} of ${requirements.length} requirements met`}>{met}/{requirements.length}</span>}
        {inputs.map(i => <span key={i.label} class="chip in"><Icon name={i.icon} />{i.label}</span>)}</div>}
      {open && <ul class="reqs">{requirements.map(r => <li key={r.text}><Icon name={r.met ? 'circle-check' : 'square'} />{r.text}</li>)}</ul>}
      {open && depends && <p class="muted">{depends}</p>}
    </div>
  </div>;
}

/* ---------- Detail sections (D11, C3) ---------- */

export const DetailSection = ({ title, children }: { title: string; children: ComponentChildren }) =>
  <section class="dsec" data-ui="detail-section"><h3>{title}</h3>{children}</section>;
export const KvRow = ({ icon, label, meta, value }: { icon: IconName; label: string; meta?: ComponentChildren; value?: ComponentChildren }) =>
  <div class="kv-row"><Icon name={icon} /><b>{label}</b>{meta && <span class="muted">{meta}</span>}<span class="grow" />{value}</div>;

/* ---------- Composer (controls/composer.md) ---------- */

/** Message input for a conversation: Enter sends, Shift+Enter adds a line; the draft survives updates and is kept per session. */
export function Composer({ onSend, disabledReason, placeholder = 'Message', draftKey }: { onSend: (text: string) => Promise<void> | void; disabledReason?: string; placeholder?: string; draftKey?: string }) {
  const read = () => { try { return draftKey ? localStorage.getItem('draft:' + draftKey) ?? '' : ''; } catch { return ''; } };
  const [text, setText] = useState(read);
  const [busy, setBusy] = useState(false);
  useEffect(() => { setText(read()); }, [draftKey]);
  const keep = (v: string) => { setText(v); try { if (draftKey) localStorage.setItem('draft:' + draftKey, v); } catch { /* storage unavailable */ } };
  const send = async () => {
    const v = text.trim(); if (!v || busy || disabledReason) return;
    // The draft is cleared only after a successful send; on failure it stays for another try.
    setBusy(true); try { await onSend(v); keep(''); } catch { /* caller shows the error */ } finally { setBusy(false); }
  };
  return (
    <div class="composer" data-ui="composer">
      <textarea value={text} placeholder={placeholder} aria-label={placeholder} rows={2} disabled={!!disabledReason}
        onInput={e => keep((e.target as HTMLTextAreaElement).value)}
        onKeyDown={e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); } }} />
      <Button label="Send" kind="primary" busy={busy} disabledReason={disabledReason} onClick={send} />
    </div>
  );
}

/* ---------- Code editor: a definition's text, edited in place (controls/code-editor.md) ---------- */

/** A monospace text area for a definition file; problems are listed under it. The caller keeps the text (a draft). */
export function CodeEditor({ label, value, onChange, problems = [], disabledReason }: {
  label: string; value: string; onChange: (text: string) => void; problems?: { path: string; message: string }[]; disabledReason?: string;
}) {
  return <div class="code-editor" data-ui="code-editor">
    <textarea class="mono" spellcheck={false} aria-label={label} value={value} disabled={!!disabledReason}
      onInput={e => onChange((e.target as HTMLTextAreaElement).value)} aria-invalid={problems.length > 0 || undefined} />
    {problems.map(p => <div key={p.path + p.message} class="problem-line t-danger" role="alert"><Icon name="circle-alert" />{p.path && <span class="mono">{p.path}</span>}{p.message}</div>)}
  </div>;
}

/** A unified diff, its added and removed lines tinted (controls/code-editor.md). */
export const DiffText = ({ text, label }: { text: string; label: string }) =>
  <pre class="diff block" data-ui="diff-text" aria-label={label}>{text === '' ? <span class="muted">No differences</span> : text.split('\n').map((l, i) =>
    <span key={i} class={l.startsWith('+') && !l.startsWith('+++') ? 'add' : l.startsWith('-') && !l.startsWith('---') ? 'del' : undefined}>{l}{'\n'}</span>)}</pre>;

/* ---------- Offline marker (controls/offline-marker.md) ---------- */

/** Says whether shown data is current: offline, updating (stale but online) or signed out; hidden when current. */
export function OfflineMarker({ state, since, now }: { state: 'current' | 'offline' | 'updating' | 'signed-out'; since?: number; now: number }) {
  if (state === 'current') return null;
  const a = since ? age(since, now) : null;
  const word = state === 'offline' ? 'offline' : state === 'updating' ? 'updating' : 'signed out';
  const long = state === 'signed-out' ? 'Session expired. Sign in again.' : `${word}${a ? `, last updated ${a.long}` : ''}`;
  return <span class={`offline-marker t-${state === 'signed-out' ? 'danger' : state === 'offline' ? 'attention' : 'neutral'}`} data-ui="offline-marker" role="status" aria-label={long}>
    <Icon name={state === 'updating' ? 'rotate-cw' : state === 'offline' ? 'circle-slash' : 'circle-alert'} />{word}{a && state !== 'signed-out' ? ` · ${a.text}` : ''}
    {state === 'signed-out' && <a href={location.pathname}>Sign in again</a>}
  </span>;
}

/* ---------- Row check: one checked row of a list, kept in the view URL as sel (fleet-view-urls-and-list-path.md §1) ---------- */

export const RowCheck = ({ checked, label, onToggle }: { checked: boolean; label: string; onToggle: () => void }) =>
  <input type="checkbox" class="row-check" data-ui="row-check" checked={checked} aria-label={label} onClick={e => e.stopPropagation()} onChange={onToggle} />;

/* ---------- Choice: one or several of a few fixed options (controls/choice.md) ---------- */

export function Choice({ label, options, value, onChange }: {
  label: string; options: { id: string; label: string; icon?: IconName }[]; value: string | string[]; onChange?: (id: string) => void;
}) {
  const on = (id: string) => Array.isArray(value) ? value.includes(id) : value === id;
  return <span class="seg-group" data-ui="choice" role="group" aria-label={label}>{options.map(o =>
    <button type="button" key={o.id} class={`seg${on(o.id) ? ' on' : ''}`} aria-pressed={on(o.id)} onClick={() => onChange?.(o.id)}>{o.icon && <Icon name={o.icon} />}{o.label}</button>)}</span>;
}

/* ---------- Draft bar: a definition that differs from its saved revision (controls/draft-bar.md) ---------- */

export function DraftBar({ changes, base, note, actions: acts }: {
  changes: number; base: string; note: string; actions: { label: string; icon?: IconName; primary?: boolean; onClick?: () => void }[];
}) {
  return <div class="problem draft" data-ui="draft-bar" role="group" aria-label="Draft">
    <Icon name="file-plus-2" /><b>Draft</b><span class="facts">{changes} {changes === 1 ? 'change' : 'changes'} against <span class="mono">{base}</span>. {note}</span>
    <span class="acts">{acts.map(a => <Button key={a.label} label={a.label} icon={a.icon} kind={a.primary ? 'primary' : 'default'} onClick={a.onClick} />)}</span>
  </div>;
}

/* ---------- Step list and flow strip: a pipeline's steps, with a run's state on top (controls/step-list.md) ---------- */

export type StepItem =
  | { band: 'loop' | 'parallel'; label: string; inset?: boolean }
  | { id: string; kind: StepKind; executor: ComponentChildren; reads: string; writes: string; inset?: boolean;
      /** The step in one run: shown in place of what it reads and writes. */
      run?: { state: StateOf<'execution'>; reason?: string; note?: string; time: Measured; cost: Measured; error?: string } };

/** The one place a pipeline is edited (D20). `children` is the editor of the open step, shown under its row (OR-14). */
export function StepList({ items, open, onOpen, label, children }: { items: StepItem[]; open?: string; onOpen?: (id: string) => void; label: string; children?: ComponentChildren }) {
  let n = 0;
  return <div class="steps" data-ui="step-list" role="list" aria-label={label}>{items.map((s, i) => {
    if ('band' in s) return <div key={i} class={`step-band${s.inset ? ' inset' : ''}`}><Icon name={s.band === 'loop' ? 'rotate-cw' : 'rows-3'} /><span>{s.label}</span></div>;
    n++;
    const k = stepKinds[s.kind];
    return <div key={s.id} role="listitem" class={`step-item${s.inset ? ' inset' : ''}`}>
      <button type="button" class={`step${open === s.id ? ' open' : ''}`} aria-expanded={onOpen ? open === s.id : undefined} onClick={() => onOpen?.(s.id)}>
        <span class="no">{String(n).padStart(2, '0')}</span>
        {s.run ? <StateBadge family="execution" state={s.run.state} reason={s.run.reason} compact /> : <span class="lead" data-tip={k.label}><Icon name={k.icon} label={k.label} /></span>}
        <b class="nm">{s.id}</b><span class="ex">{s.executor}</span>
        {s.run ? <span class="svc">{s.run.note && <span class="chip-age">{s.run.note}</span>}<Num m={s.run.time} kind="duration" /><Num m={s.run.cost} kind="money" /></span>
          : <span class="io">reads {s.reads} · writes <span class="w">{s.writes}</span></span>}
      </button>
      {s.run?.error && <div class="step-error" data-important="">{s.run.error}</div>}
      {open === s.id && children && <div class="step-edit">{children}</div>}
    </div>;
  })}</div>;
}

export type FlowNode =
  | { id: string; kind: StepKind; state?: StateOf<'execution'>; current?: boolean; optional?: boolean }
  | { loop: string; nodes: FlowNode[] } | { parallel: FlowNode[] };

/** A read-only picture of the steps: wires for order, a bracket for parallel steps, a dashed frame for a loop. */
export function FlowStrip({ nodes, label }: { nodes: FlowNode[]; label: string }) {
  const row = (ns: FlowNode[]): ComponentChildren => ns.map((n, i) => <>{i > 0 && <span class="flow-w" />}{one(n, i)}</>);
  const one = (n: FlowNode, i: number): ComponentChildren =>
    'loop' in n ? <span key={i} class="flow-loop"><span class="flow-cap"><Icon name="rotate-cw" />{n.loop}</span>{row(n.nodes)}</span>
    : 'parallel' in n ? <span key={i} class="flow-par">{n.parallel.map(one)}</span>
    : <span key={n.id} class={`flow-n${n.current ? ' cur' : ''}${n.optional ? ' opt' : ''}`}>{n.state ? <StateBadge family="execution" state={n.state} compact /> : <Icon name={stepKinds[n.kind].icon} />}{n.id}</span>;
  return <div class="flow" data-ui="flow-strip" role="img" aria-label={label}>{row(nodes)}</div>;
}

/* ---------- Area chart (controls/area-chart.md): recorded and projected lines over a time range, CSS only (OR-8) ---------- */

export type ChartPoint = { at: number; v: number };
/** pattern: stripes over the tone, for a fifth and later series (tokens.md: never new hues). */
export type ChartSeries = { label: string; points: ChartPoint[]; tone: 'd1' | 'd2' | 'd3' | 'd4'; projected?: boolean; step?: boolean; pattern?: boolean };
type AreaChartProps = { label: string; series: ChartSeries[]; from: number; to: number; now: number; max: number;
  /** Layers on top of each other, first series at the bottom (points share their times); with a legend of last values. */
  stacked?: boolean;
  /** Text of a value for the summary and the axis, e.g. `40%` or `12/h`. */
  value: (v: number) => string };
export function AreaChart(p: AreaChartProps) {
  // Stacked: each layer is drawn as the sum of itself and the layers under it, top layer first, all opaque.
  const drawn = p.stacked ? p.series.map((s, i) => ({ ...s, points: s.points.map((q, j) => ({ at: q.at, v: p.series.slice(0, i + 1).reduce((n, t) => n + (t.points[j]?.v ?? 0), 0) })) })).reverse() : p.series;
  const span = Math.max(1, p.to - p.from);
  const x = (t: number) => Math.min(100, Math.max(0, (t - p.from) / span * 100));
  const y = (v: number) => 100 - Math.min(100, Math.max(0, v / (p.max || 1) * 100));
  const shape = (s: ChartSeries) => {
    const pts = s.points.filter(q => q.at <= p.to).map(q => ({ at: Math.max(q.at, p.from), v: q.v }));
    if (pts.length === 0) return '';
    const xy: string[] = [];
    pts.forEach((q, i) => {
      if (s.step && i > 0) xy.push(`${x(q.at)}% ${y(pts[i - 1].v)}%`);
      xy.push(`${x(q.at)}% ${y(q.v)}%`);
    });
    return `polygon(${x(pts[0].at)}% 100%, ${xy.join(', ')}, ${x(pts[pts.length - 1].at)}% 100%)`;
  };
  const last = (s: ChartSeries) => s.points.length ? p.value(s.points[s.points.length - 1].v) : 'none';
  const summary = p.series.map(s => `${s.label}${s.projected && !s.label.includes('projected') ? ' projected' : ''}: ${last(s)}`).join('; ');
  const days = (from: number, to: number) => { const out: number[] = []; const d = new Date(from); d.setHours(24, 0, 0, 0); for (let t = d.getTime(); t < to && out.length < 31; t += 864e5) out.push(t); return out; };
  return <div data-ui="area-chart" class="area-chart" role="img" aria-label={`${p.label}: ${summary}`}>
    <div class="ac-plot">
      {drawn.filter(s => shape(s)).map(s => <i key={s.label + (s.projected ? '-p' : '')} class={`ac-area ${s.tone}${s.projected ? ' projected' : ''}${p.stacked ? ' stacked' : ''}${s.pattern ? ' pattern' : ''}`} style={{ clipPath: shape(s) }} />)}
      {days(p.from, p.to).map(t => <b key={t} class="ac-day" style={{ left: `${x(t)}%` }} />)}
      {p.now >= p.from && p.now <= p.to && <b class="ac-now" style={{ left: `${x(p.now)}%` }} data-tip={exact(p.now)} />}
      <span class="ac-max">{p.value(p.max)}</span>
    </div>
    <div class="ac-axis"><time>{stamp(p.from, p.now)}</time><span class="grow" /><time>{stamp(p.to, p.now)}</time></div>
    {p.stacked && <ul class="ac-legend" aria-hidden="true">{p.series.map(s => <li key={s.label}><i class={`ac-key ${s.tone}${s.pattern ? ' pattern' : ''}`} />{s.label} <span class="num">{last(s)}</span></li>)}</ul>}
  </div>;
}

/* ---------- Time strip: where a run sits in a time window (Runners), toned by its state ---------- */

/** One run as a bar from its start to its end (to now while it runs) inside [from, to]; a run older than the window starts at its left edge. */
export function TimeStrip({ from, to, start, end, now, state, label }: { from: number; to: number; start: number; end: number | null; now: number; state: StateOf<'execution'>; label: string }) {
  const x = (t: number) => Math.min(100, Math.max(0, (t - from) / (to - from) * 100));
  const a = x(start), b = x(end ?? now);
  return <span class="tstrip" data-ui="time-strip" role="img" aria-label={label}>
    <i class={`t-${states.execution[state].tone}${end === null ? ' live' : ''}`} style={{ left: `${a}%`, width: `max(2px, ${b - a}%)` }} /></span>;
}
