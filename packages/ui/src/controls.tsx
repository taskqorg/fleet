// Fleet controls. Specs: docs/design-system/controls/*.md. Styling: styles.css with tokens only.
import type { ComponentChildren } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';
import { type IconName, type Family, type StateOf, type StateDef, type Tone, iconSvg, states, actions } from './registry';
import { type Measured, type Kind, measured, age, exact, bytes, clock } from './format';
import { VirtualList } from './virtual-list';
import tokens from '../tokens.json';

const ROW = parseInt(tokens.size.row);

/* ---------- Icon and button ---------- */

/** Registry icon. With `label` it is meaningful (tooltip + accessible name); without, decorative. */
export function Icon({ name, label }: { name: IconName; label?: string }) {
  return <span class="icon" role={label ? 'img' : undefined} aria-label={label} aria-hidden={label ? undefined : true} title={label}
    dangerouslySetInnerHTML={{ __html: iconSvg(name) }} />;
}

type ButtonProps = {
  label: string; icon?: IconName; iconOnly?: boolean; kind?: 'primary' | 'default' | 'quiet' | 'danger';
  kbd?: string; busy?: boolean; disabledReason?: string; pressed?: boolean; onClick?: () => void;
};
export function Button(p: ButtonProps) {
  const disabled = p.busy || !!p.disabledReason;
  return (
    <button type="button" class={`btn ${p.kind ?? 'default'}${p.iconOnly ? ' icon-only' : ''}${p.busy ? ' busy' : ''}`}
      aria-label={p.iconOnly ? p.label : undefined} title={p.disabledReason ?? (p.iconOnly ? p.label : undefined)}
      aria-disabled={disabled || undefined} aria-pressed={p.pressed} onClick={disabled ? undefined : p.onClick}>
      {p.icon && <Icon name={p.icon} />}
      {!p.iconOnly && <span>{p.label}</span>}
      {p.kbd && <kbd>{p.kbd}</kbd>}
    </button>
  );
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
    <span class="state">
      <span class={`badge t-${d.tone}${unconf ? ' unconf' : ''}${p.compact ? ' compact' : ''}`} role="img" aria-label={name} title={name}>
        <Icon name={d.icon} />{!p.compact && <span>{d.label}</span>}
      </span>
      {(unconf || p.reason) && <span class={`reason${unconf ? ' unconf' : ''}`} aria-hidden="true">{unconf ? `seen ${seen!.text}` : ''}{unconf && p.reason ? ' · ' : ''}{p.reason}</span>}
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
  return <span class={`num b-${m.basis}${m.partial ? ' partial' : ''} ${tint}`} title={f.long} aria-label={f.long} tabIndex={m.basis === 'measured' && !m.partial ? undefined : 0}>{f.text}</span>;
}

export function Totals({ items }: { items: { label: string; m: Measured; kind: Kind; note?: string }[] }) {
  return (
    <dl class="totals">
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
export function AgeChip({ t, now, alertAfter, tone = 'danger' }: { t: number; now: number; alertAfter?: number; tone?: Tone }) {
  const a = age(t, now);
  const alert = alertAfter !== undefined && now - t >= alertAfter * 1000;
  return <span class={`chip-age${alert ? ` t-${tone} alert` : ''}`} title={`${a.long} · ${exact(t)}`} aria-label={a.long}>{a.text}</span>;
}

/* ---------- Fill bar (W9–W11) ---------- */

type FillProps = { total: number | null; segments: { label: string; bytes: number }[]; warnAt?: number; stale?: number; now?: number };
export function FillBar(p: FillProps) {
  if (p.total === null) return <span class="fill-wrap"><span class="fill" role="img" aria-label="unavailable" /><span class="legend muted">— unavailable</span></span>;
  const used = p.segments.reduce((s, x) => s + x.bytes, 0), free = p.total - used;
  const state = free <= 0 ? 'full' : p.warnAt !== undefined && free < p.warnAt ? 'warn' : '';
  const legend = p.segments.map(s => `${s.label} ${Math.round(s.bytes / p.total! * 100)}%`).join(' · ');
  const freeText = free <= 0 ? 'full' : `${bytes(free).text} free${state ? `, warning at ${bytes(p.warnAt!).text}` : ''}`;
  const staleText = p.stale !== undefined ? ` · ${age(p.stale, p.now ?? Date.now()).text} old` : '';
  return (
    <span class="fill-wrap">
      <span class={`fill ${state}${p.stale !== undefined ? ' stale' : ''}`} role="img" aria-label={`${legend}; ${freeText}${staleText}`}>
        {p.segments.map((s, i) => <i key={s.label} class={`d${i % 4 + 1}`} style={{ width: `${s.bytes / p.total! * 100}%` }} title={`${s.label} ${bytes(s.bytes).text}`} />)}
        {p.warnAt !== undefined && <b class="mark" style={{ left: `${(1 - p.warnAt / p.total) * 100}%` }} />}
      </span>
      <span class={`legend${state ? ` t-${state === 'full' ? 'danger' : 'attention'}` : ''}`}>{legend} · {freeText}{staleText}</span>
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
  saved: { name: string; active?: boolean; pinned?: boolean }[]; onSaved: (name: string) => void;
  shown: number; total: number;
};
export function FilterBar(p: FilterBarProps) {
  const [open, setOpen] = useState<string | null>(null); // inline filter row, never a popup (OR-14)
  const field = p.fields.find(f => f.field === open);
  return (
    <div class="fbar">
      <div class="fbar-line">
        <label class="search"><Icon name={actions.search} /><input type="search" value={p.query} placeholder={p.placeholder} aria-label={p.placeholder}
          onInput={e => p.onQuery((e.target as HTMLInputElement).value)} /></label>
        <Button label="Filter" icon={actions.filter} kind="quiet" kbd="f" pressed={open !== null} onClick={() => setOpen(open === null ? p.fields[0]?.field ?? null : null)} />
        {p.chips.map(c => (
          <span class="chip" key={c.field + c.value}>{c.field}: {c.value}
            <button type="button" aria-label={`Remove filter ${c.field}: ${c.value}`} onClick={() => p.onRemove(c)}><Icon name={actions.clear} /></button></span>
        ))}
        {p.chips.length > 0 && <Button label="Clear" kind="quiet" onClick={p.onClear} />}
        <span class="count" aria-live="polite">{p.shown.toLocaleString('en')} of {p.total.toLocaleString('en')}</span>
      </div>
      <div class="fbar-line saved" role="group" aria-label="Saved filters">
        {p.saved.filter(s => s.pinned).map(s => (
          <button type="button" key={s.name} class={`saved-chip${s.active ? ' on' : ''}`} aria-pressed={!!s.active} onClick={() => p.onSaved(s.name)}>
            <Icon name={actions.pin} />{s.name}</button>
        ))}
      </div>
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
  return <ul class="tree" role="tree" aria-label={label}>{nodes.map(n => row(n, 1))}</ul>;
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
    return <div class="decision answered" role="status"><Icon name={a.delivered ? 'check-check' : 'history'} />
      <span>{p.question} <b>{a.answer}</b> · {a.by} · {age(a.at, p.now).text}{a.delivered ? '' : ' · delivery pending'}</span></div>;
  }
  const send = (s: string) => s.trim() && p.onAnswer?.(s.trim());
  return (
    <div class="decision" role="group" aria-label={p.question}
      onKeyDown={e => { if ((e.target as HTMLElement).tagName === 'INPUT') return; const o = p.options.find(o => o.kbd === e.key); if (o) send(o.label); }}>
      <div class="dq"><Icon name="bell" /><b>{p.question}</b></div>
      <div class="meta">{p.askedBy} · for {p.addressedTo} · <AgeChip t={p.since} now={p.now} />{p.waits && <> · {p.waits}</>}</div>
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
    <section class="rv" aria-label={p.name}>
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
    <div class="setting">
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
    <div class={`problem ${severity}`} role="group" aria-label={`${severity === 'failure' ? 'Failure' : 'Warning'}: ${title}`}>
      <Icon name={severity === 'failure' ? 'octagon-x' : 'triangle-alert'} />
      <b>{title}</b><span class="facts">{facts}</span>
      <span class="acts">{acts.map(a => <Button key={a.label} label={a.label} icon={a.icon} kind={a.primary ? 'primary' : 'default'} />)}</span>
    </div>
  );
}

/* ---------- Empty, loading, error, stale (§8) ---------- */

export const Skeleton = ({ rows }: { rows: number }) => (
  <div class="skeleton" aria-busy="true" aria-label="Loading">{Array.from({ length: rows }, (_, i) => <div key={i} class="sk-row"><i style={{ width: `${55 + (i * 17) % 35}%` }} /><i /></div>)}</div>
);
export const Empty = ({ text, action }: { text: string; action?: ComponentChildren }) => <div class="empty"><p>{text}</p>{action}</div>;
export const ErrorBanner = ({ text, onRetry }: { text: string; onRetry?: () => void }) => (
  <div class="err-banner" role="alert"><Icon name="circle-alert" /><span>{text}</span><span class="grow" /><Button label="Retry" icon={actions.retry} onClick={onRetry} /></div>
);
export const Stale = ({ at, now, onRefresh }: { at: number; now: number; onRefresh?: () => void }) => (
  <span class="stale" title={exact(at)}>updated {age(at, now).text} <Button label="Refresh" icon={actions.retry} iconOnly onClick={onRefresh} /></span>
);

/* ---------- Live transcript (transcript-viewer.md) ---------- */

export type Entry = { id: string; t: number; who?: string; text?: string; call?: { name: string; state: string; secs: number }; gap?: string };
export function Transcript({ entries, label }: { entries: Entry[]; label: string }) {
  const [q, setQ] = useState('');
  const shown = q ? entries.filter(e => (e.text ?? e.call?.name ?? '').toLowerCase().includes(q.toLowerCase())) : entries;
  return (
    <div class="transcript">
      <div class="tr-tool"><label class="search"><Icon name={actions.search} /><input type="search" placeholder="Search transcript" aria-label="Search transcript" value={q}
        onInput={e => setQ((e.target as HTMLInputElement).value)} /></label><span class="count">{shown.length} of {entries.length}</span></div>
      <VirtualList class="tr-body" items={shown} getKey={e => e.id} rowHeight={ROW} dynamic follow="bottom" label={label} render={e => (
        e.gap ? <div class="tr-gap"><Icon name="triangle-alert" />{e.gap}</div>
          : <div class="tr-e"><time title={exact(e.t)}>{clock(e.t)}</time>{e.call
            ? <span class="call"><Icon name="square" />{e.call.name} · {e.call.state} · {e.call.secs}s</span>
            : <span><b>{e.who}</b> {e.text}</span>}</div>
      )} />
    </div>
  );
}
