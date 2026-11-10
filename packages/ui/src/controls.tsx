// Fleet controls. Specs: docs/design-system/controls/*.md. Styling: styles.css with tokens only.
import type { ComponentChildren } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';
import { type IconName, type Family, type StateOf, type StateDef, type Tone, type Harness, type Stage, iconSvg, states, actions, harnesses, stages } from './registry.ts';
import { type Measured, type Kind, measured, age, bytes, exact, percent, duration, agentLabel } from './format.ts';
import { VirtualList, type VirtualListHandle } from './virtual-list.tsx';
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
  kbd?: string; busy?: boolean; disabledReason?: string; pressed?: boolean; onClick?: () => void;
};
export function Button(p: ButtonProps) {
  const disabled = p.busy || !!p.disabledReason;
  return (
    <button type="button" data-ui="button" class={`btn ${p.kind ?? 'default'}${p.iconOnly ? ' icon-only' : ''}${p.busy ? ' busy' : ''}`}
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
  question: string; askedBy: string; addressedTo: string; since: number; now: number; waits?: string;
  context?: ComponentChildren; options: { label: string; kbd?: string; primary?: boolean }[];
  answered?: { by: string; answer: string; at: number; delivered: boolean };
  onAnswer?: (answer: string) => void;
};
export function DecisionBlock(p: DecisionProps) {
  const [text, setText] = useState('');
  if (p.answered) {
    const a = p.answered;
    return <div class="decision answered" data-ui="decision-block" role="status"><Icon name={a.delivered ? 'check-check' : 'history'} />
      <span>{p.question} <b>{a.answer}</b> · {a.by} · {age(a.at, p.now).text}{a.delivered ? '' : ' · delivery pending'}</span></div>;
  }
  const send = (s: string) => s.trim() && p.onAnswer?.(s.trim());
  return (
    <div class="decision" data-ui="decision-block" role="group" aria-label={p.question}
      onKeyDown={e => { if ((e.target as HTMLElement).tagName === 'INPUT') return; const o = p.options.find(o => o.kbd === e.key); if (o) send(o.label); }}>
      <div class="dq"><Icon name="bell" /><b>{p.question}</b></div>
      <div class="meta">{p.askedBy}{p.addressedTo === 'you' ? '' : ` · for ${p.addressedTo}`} · <AgeChip t={p.since} now={p.now} />{p.waits && <> · {p.waits}</>}</div>
      {p.context && <div class="ctx">{p.context}</div>}
      <div class="answers">
        {p.options.map(o => <Button key={o.label} label={o.label} kbd={o.kbd} kind={o.primary ? 'primary' : 'default'} onClick={() => send(o.label)} />)}
        <input value={text} placeholder="Answer" aria-label="Typed answer" onInput={e => setText((e.target as HTMLInputElement).value)}
          onKeyDown={e => { if (e.key === 'Enter') send(text); }} />
      </div>
    </div>
  );
}

/* ---------- Result viewer shell ---------- */

type ResultProps = {
  name: string; type: string; size: number; source: string;
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
        <Button label="Download" icon={actions.download} iconOnly />
        <Button label="Share" icon={actions.share} iconOnly />
      </header>
      <div class="rv-b" role="tabpanel">{p.children}</div>
    </section>
  );
}

/* ---------- Setting field ---------- */

type SettingProps = {
  label: string; value: string; help: string; changed?: { by: string; at: number }; now: number;
  status: { kind: 'applied'; at: number } | { kind: 'pending' } | { kind: 'apply_error'; error: string };
  onSave?: (v: string) => void;
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
          ? <button type="button" id={id} class="value" onClick={() => setEdit(p.value)}>{p.value}</button>
          : <><input id={id} value={edit} onInput={e => setEdit((e.target as HTMLInputElement).value)}
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
  actions: { label: string; icon?: IconName; primary?: boolean }[];
}) {
  return (
    <div data-ui="problem-row" class={`problem ${severity}`} role="group" aria-label={`${severity === 'failure' ? 'Failure' : 'Warning'}: ${title}`}>
      <Icon name={severity === 'failure' ? 'octagon-x' : 'triangle-alert'} />
      <b>{title}</b><span class="facts" data-important="">{facts}</span>
      <span class="acts">{acts.map(a => <Button key={a.label} label={a.label} icon={a.icon} kind={a.primary ? 'primary' : 'default'} />)}</span>
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

/** Markdown as text: fenced code blocks get their own copy button, inline code is mono. Raw HTML is never rendered. */
// ponytail: fences and inline code only; headings, lists and links stay plain text until a renderer is chosen for the book.
export function Markdown({ text, onCopyFail }: { text: string; onCopyFail?: (e: unknown) => void }) {
  const parts = text.split(/^```[^\n]*\n([\s\S]*?)^```[ \t]*$/m);
  return <div class="md">{parts.map((part, i) => i % 2
    ? <div class="code" key={i}><pre><code>{part.replace(/\n$/, '')}</code></pre><span class="code-copy"><Button label="Copy code" icon={actions.copy} iconOnly kind="quiet" onClick={() => copyText(part.replace(/\n$/, '')).catch(e => onCopyFail?.(e))} /></span></div>
    : <p key={i}>{part.split(/(`[^`\n]+`)/).map((x, j) => j % 2 ? <code key={j}>{x.slice(1, -1)}</code> : x)}</p>)}</div>;
}

type Mark = { harness?: string | null; model?: string | null; effort?: string | null };
/** Agent mark (D4): harness icon and the short label; the model id on hover. */
export function AgentMark({ harness, model, effort }: Mark) {
  return <span class="agent-mark">{harness && harness in harnesses && <HarnessMark h={harness as Harness} />}<span class="lbl" data-tip={model ?? undefined}>{agentLabel(model, effort)}</span></span>;
}

/** An operator or agent reply: content first, the service zone (agent mark, copy) on the right (OR-21). */
export function TextRow({ who, markdown, mark, onCopyFail }: { who: 'operator' | 'agent'; markdown: string; mark?: Mark; onCopyFail?: (e: unknown) => void }) {
  return <div class={`th-row th-text ${who}`} data-ui="thread-row">
    <div class="th-main">{who === 'operator' && <span class="who">You</span>}<Markdown text={markdown} onCopyFail={onCopyFail} /></div>
    <span class="th-svc">{who === 'agent' && mark && <AgentMark {...mark} />}<Button label="Copy reply" icon={actions.copy} iconOnly kind="quiet" onClick={() => copyText(markdown).catch(e => onCopyFail?.(e))} /></span>
  </div>;
}

const fenced = (v: unknown) => '```\n' + (typeof v === 'string' ? v : JSON.stringify(v, null, 1)) + '\n```';
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
    body={<>{input != null && <Markdown text={fenced(input)} onCopyFail={onCopyFail} />}{output != null && <Markdown text={fenced(output)} onCopyFail={onCopyFail} />}</>} />;
}
export function ReasoningRow({ markdown, open, onToggle, onCopyFail }: { markdown: string; open: boolean; onToggle: () => void; onCopyFail?: (e: unknown) => void }) {
  return <Fold icon="bot" open={open} onToggle={onToggle} copy={markdown} onCopyFail={onCopyFail}
    head={<span class="th-line"><span>Reasoning</span><span class="svc ell">{markdown.split('\n')[0]}</span></span>} body={<Markdown text={markdown} onCopyFail={onCopyFail} />} />;
}
/** A question in the thread (DecisionBlock); answered, it collapses to one line with the answer and who gave it. */
export function QuestionRow({ text, options, state, answer, answeredBy, now, onAnswer }: { text: string; options: string[]; state: string; answer?: string; answeredBy?: string; now: number; onAnswer?: (a: string) => void }) {
  return <div class="th-row th-q" data-ui="thread-row"><DecisionBlock question={text} askedBy="agent" addressedTo="you" since={now} now={now}
    options={options.map((o, i) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
    answered={state === 'answered' && answer !== undefined ? { by: answeredBy ?? 'operator', answer, at: now, delivered: true } : undefined} onAnswer={onAnswer} /></div>;
}
/** Results of an execution end: artifact links (Stage 4 adds names, the delivery stage and the viewer). */
export function ResultRow({ artifactIds, stage }: { artifactIds: string[]; stage?: Stage }) {
  return <div class="th-row th-result" data-ui="thread-row"><Icon name="file-text" /><span class="th-line">{artifactIds.map(id => <a key={id} class="mono" href={`/api/results/${encodeURIComponent(id)}`} target="_blank" rel="noopener">{id.slice(0, 8)}</a>)}</span>{stage && <StageChip stage={stage} />}</div>;
}
/** An execution start: state, agent mark, duration chip, and Stop while it runs (CUJ 5). */
export function BoundaryRow({ executionId, state, mark, startedAt, endedAt, now, onStop, stopReason }: { executionId: string; state: StateOf<'execution'>; mark: Mark; startedAt?: number | null; endedAt?: number | null; now: number; onStop?: () => void; stopReason?: string }) {
  const secs = startedAt != null ? ((endedAt ?? now) - startedAt) / 1000 : null;
  return <div class="th-row th-boundary" data-ui="thread-row"><StateBadge family="execution" state={state} compact /><span class="id">{executionId.slice(0, 8)}</span><span class="grow" />
    <span class="th-svc"><AgentMark {...mark} />{secs !== null && <span class="chip-age">{duration(secs).text}</span>}{onStop && <Button label="Stop" icon="pause" iconOnly disabledReason={stopReason} onClick={onStop} />}</span></div>;
}

export type ThreadBlock = { kind: string; key: string; executionId?: string; [k: string]: any };
/**
 * A thread: one row per block in a measured VirtualList that follows the bottom, loads older rows upward (onStart) and
 * newer ones downward (onEnd), and jumps through `handle`. Open tool and reasoning rows are kept by key across the window.
 */
export function Thread({ blocks, label, now, marked, handle, onStart, onEnd, onStop, stopReason, onAnswer, onCopyFail }: {
  blocks: ThreadBlock[]; label: string; now: number; marked?: string | null; handle?: { current: VirtualListHandle | null };
  onStart?: () => void; onEnd?: () => void; onStop?: (executionId: string) => void; stopReason?: string;
  onAnswer?: (questionId: string, answer: string) => void; onCopyFail?: (e: unknown) => void;
}) {
  const [open, setOpen] = useState<Set<string>>(new Set());
  const toggle = (k: string) => setOpen(o => { const n = new Set(o); n.has(k) ? n.delete(k) : n.add(k); return n; });
  const markOf = (b: ThreadBlock): Mark => ({ harness: b.harness, model: b.model, effort: b.effort });
  const row = (b: ThreadBlock) => {
    switch (b.kind) {
      case 'text': return <TextRow who={b.role === 'operator' ? 'operator' : 'agent'} markdown={b.markdown ?? ''} mark={markOf(b)} onCopyFail={onCopyFail} />;
      case 'tool': return <ToolRow name={b.name} input={b.input} output={b.output} state={b.state} startedAt={b.startedAt} endedAt={b.endedAt} open={open.has(b.key)} onToggle={() => toggle(b.key)} onCopyFail={onCopyFail} />;
      case 'reasoning': return <ReasoningRow markdown={b.markdown ?? ''} open={open.has(b.key)} onToggle={() => toggle(b.key)} onCopyFail={onCopyFail} />;
      case 'question': return <QuestionRow text={b.text} options={b.options ?? []} state={b.state} answer={b.answer} answeredBy={b.answeredBy} now={now} onAnswer={onAnswer && (a => onAnswer(b.questionId, a))} />;
      case 'result': return <ResultRow artifactIds={b.artifactIds ?? []} stage={b.stage} />;
      case 'boundary': return <BoundaryRow executionId={b.executionId!} state={b.state} mark={markOf(b)} startedAt={b.startedAt} endedAt={b.endedAt} now={now} stopReason={stopReason}
        onStop={onStop && ['preparing', 'running', 'waiting'].includes(b.state) ? () => onStop(b.executionId!) : undefined} />;
      default: return null;
    }
  };
  return <div class="transcript" data-ui="transcript-viewer">
    <VirtualList class="tr-body" items={blocks} getKey={b => b.key} rowHeight={ROW} dynamic follow="bottom" label={label} handle={handle} onStart={onStart} onEnd={onEnd}
      rowClass={b => b.key === marked ? 'marked' : ''} render={row} />
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

/* ---------- State counters: quick filter by state with totals (state-counters.md) ---------- */

export function StateCounters<F extends Family>({ family, counts, active, onToggle, onAll, label, pending }: {
  family: F; counts: Partial<Record<StateOf<F>, number | null>>; active: StateOf<F>[]; onToggle: (s: StateOf<F>) => void;
  /** Σ button: show all (clear the state filter). Omit to hide the Σ button. */
  onAll?: () => void; label: string;
  /** A newer answer is loading: the last counts stay, dimmed (docs/designs/fleet-view-urls-and-list-path.md §2). */
  pending?: boolean;
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
        if (n === undefined) return null;
        const on = active.includes(s);
        const name = `${d.label}: ${n === null ? 'unknown' : n}`;
        return <button type="button" key={String(s)} class={`counter t-${d.tone}${on ? ' on' : ''}${n === 0 ? ' zero' : ''}`} aria-pressed={on} aria-label={name} data-tip={d.label} onClick={() => onToggle(s)}>
          <Icon name={d.icon} /><span class="num">{n === null ? '?' : n.toLocaleString('en')}</span></button>;
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
export function NavRail({ items, pinned, current, onPin, onGo }: {
  items: NavItem[]; pinned: string[]; current: string; onPin: (id: string, pin: boolean) => void; onGo: (id: string) => void;
}) {
  const [q, setQ] = useState('');
  const item = (it: NavItem) => {
    const isPinned = pinned.includes(it.id);
    return <li key={it.id} class={`nav-i${it.id === current ? ' cur' : ''}`}>
      <a href={`#/${it.id}`} aria-current={it.id === current ? 'page' : undefined} onClick={e => { e.preventDefault(); onGo(it.id); }}>
        <span data-tip={it.label} data-tip-narrow class="nav-ic"><Icon name={it.icon} /></span><span class="nm">{it.label}</span>
        {it.count && it.count.n > 0 && <span class={`nav-count t-${it.count.tone}`} aria-label={`${it.count.n} ${it.count.label}`}>{it.count.n}</span>}
      </a>
      <button type="button" class={`nav-pin${isPinned ? ' on' : ''}`} aria-pressed={isPinned} aria-label={`${isPinned ? 'Unpin' : 'Pin'} ${it.label}`} data-tip={isPinned ? 'Unpin' : 'Pin'} onClick={() => onPin(it.id, !isPinned)}>
        <Icon name={isPinned ? 'pin-off' : 'pin'} /></button>
    </li>;
  };
  const found = items.filter(i => i.label.toLowerCase().includes(q.toLowerCase()));
  const groups = [...new Set(found.filter(i => !pinned.includes(i.id)).map(i => i.group))];
  return (
    <nav class="rail" data-ui="nav-rail" aria-label="Sections">
      {pinned.length > 0 && <ul class="rail-g pinned" aria-label="Pinned">{pinned.map(id => items.find(i => i.id === id)).filter(Boolean).map(i => item(i!))}</ul>}
      <label class="search rail-search"><Icon name="search" /><input type="search" placeholder="Sections" aria-label="Find section" value={q} onInput={e => setQ((e.target as HTMLInputElement).value)} /></label>
      {groups.map(g => <ul key={g} class="rail-g" aria-label={g}><li class="rail-h">{g}</li>{found.filter(i => i.group === g && !pinned.includes(i.id)).map(item)}</ul>)}
    </nav>
  );
}

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
      <div class="outcome-meta"><span class={`chip-age${met < requirements.length ? '' : ' t-success alert'}`} aria-label={`${met} of ${requirements.length} requirements met`}>{met}/{requirements.length}</span>
        {inputs.map(i => <span key={i.label} class="chip in"><Icon name={i.icon} />{i.label}</span>)}</div>
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
