// Primitive controls, badges, numbers, age clocks, harness marks, status states, copy, and search input.
import type { ComponentChildren, TargetedMouseEvent } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';
import { type IconName, type Family, type StateOf, type StateDef, type Tone, type Harness, type Stage, iconSvg, states, actions, harnesses, stages } from '../registry.ts';
import { type Measured, type Kind, type CostFigureLike, costMeasured, splitCostFigures, measured, age, exact } from '../format.ts';
import { isSubmitEnter } from '../keys.ts';

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

/* ---------- State badge (OR-1, D7) ---------- */

type BadgeProps<F extends Family> = {
  family: F; state: StateOf<F>; reason?: string;
  /** Last contact time when the observation is unconfirmed. */
  lastSeen?: number; unconfirmed?: boolean; now?: number;
  /** Icon only, word in tooltip: for dense columns. */
  compact?: boolean;
};
export function StateBadge<F extends Family>(p: BadgeProps<F>) {
  const d = states[p.family][p.state] as StateDef;
  const unconf = p.unconfirmed || p.lastSeen !== undefined;
  const seen = p.lastSeen !== undefined ? exact(p.lastSeen) : null;
  const name = [d.label, unconf && 'unconfirmed', seen && `last seen ${seen}`, p.reason].filter(Boolean).join(', ');
  return (
    <span class="state" data-ui="state-badge">
      <span class={`badge t-${d.tone}${unconf ? ' unconf' : ''}${p.compact ? ' compact' : ''}`} role="img" aria-label={name} data-tip={p.compact || unconf ? name : undefined}>
        <Icon name={d.icon} />{!p.compact && <span>{d.label}</span>}
      </span>
      {(unconf || p.reason) && !p.compact && <span class={`reason${unconf ? ' unconf' : ''}`} aria-hidden="true">{unconf && <>Unconfirmed{p.lastSeen !== undefined && <> · seen <Ago t={p.lastSeen} /></>}</>}{unconf && p.reason ? ' · ' : ''}{p.reason}</span>}
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
  const tip = m.basis === 'measured' && !m.partial ? undefined : f.long;
  return <span data-ui="number" class={`num b-${m.basis}${m.value === null ? ' null' : ''}${m.partial ? ' partial' : ''} ${tint}`} data-tip={tip} aria-label={f.long} tabIndex={tip ? 0 : undefined}>
    {m.basis === 'subscription' && <Icon name="rotate-cw" />}{f.text}</span>;
}

export type TotalItem =
  | { label: string; m: Measured; kind: Kind; note?: string; cost?: never }
  | { label: string; cost: readonly CostFigureLike[]; note?: string; showUnavailable?: boolean; m?: never; kind?: never };

export function Totals({ items, compact }: { items: TotalItem[]; compact?: boolean }) {
  return (
    <dl class={compact ? "totals compact" : "totals"} data-ui="number">
      {items.map(i => (
        <div key={i.label}>
          <dt>{i.label}</dt>
          <dd>{i.cost !== undefined ? <CostFigures cost={i.cost} showUnavailable={i.showUnavailable} empty="—" /> : <Num m={i.m} kind={i.kind} />}</dd>
          {i.note && <dd class="note">{i.note}</dd>}
        </div>
      ))}
    </dl>
  );
}

/** Renders multi-currency CostFigure[] while keeping metered spend and subscription equivalents separate. */
export function CostFigures({ cost, showUnavailable = false, empty }: { cost: readonly CostFigureLike[] | null | undefined; showUnavailable?: boolean; empty?: string }) {
  const { metered, subscription } = splitCostFigures(cost);
  const visMetered = showUnavailable ? metered : metered.filter(c => c.value !== null && c.basis !== 'unavailable');
  if (!visMetered.length && !subscription.length) {
    return empty ? <span class="muted">{empty}</span> : null;
  }
  return (
    <span class="cost-figs" data-ui="number">
      {visMetered.map((c, i) => <Num key={`m:${c.currency ?? i}:${i}`} m={costMeasured(c)} kind="money" />)}
      {subscription.map((c, i) => <Num key={`s:${c.currency ?? i}:${i}`} m={costMeasured(c)} kind="money" />)}
    </span>
  );
}

/** Re-render every ms while active (open layers whose ages count on; views that judge liveness by the clock). */
export function useTick(ms: number, active = true) {
  const [, set] = useState(0);
  useEffect(() => { if (!active) return; const t = setInterval(() => set(n => n + 1), ms); return () => clearInterval(t); }, [ms, active]);
}

const ageListeners = new Set<(now: number) => void>();
let ageTimer: ReturnType<typeof setInterval> | undefined;

function useAgeClock(t: number, alertAfter: number | undefined, offset: number) {
  const [, refresh] = useState(0);
  const now = Date.now() + offset;
  useEffect(() => {
    if (!Number.isFinite(t)) return;
    const updateKey = (now: number) => `${age(t, now).text}:${alertAfter !== undefined && now - t >= alertAfter * 1000}`;
    let previous = updateKey(now);
    const update = (clock: number) => {
      const next = updateKey(clock + offset);
      if (next === previous) return;
      previous = next;
      refresh(n => n + 1);
    };
    update(Date.now());
    ageListeners.add(update);
    ageTimer ??= setInterval(() => {
      const clock = Date.now();
      ageListeners.forEach(listener => listener(clock));
    }, 1000);
    return () => {
      ageListeners.delete(update);
      if (!ageListeners.size) {
        clearInterval(ageTimer);
        ageTimer = undefined;
      }
    };
  }, [t, alertAfter, offset]);
  return now;
}

type AgeProps = { t: number; clockOffsetMs?: number; alertAfter?: number; tone?: Tone; alertLabel?: string };

/** Compact live age with the original timestamp available on hover. */
export function Ago({ t, chip, clockOffsetMs = 0, alertAfter, tone = 'danger', alertLabel = 'needs a look' }: AgeProps & { chip?: boolean }) {
  const now = useAgeClock(t, alertAfter, clockOffsetMs);
  const cls = chip ? 'chip-age' : 'when';
  if (!Number.isFinite(t)) return <span class={cls} aria-label="Time unavailable">—</span>;
  const a = age(t, now);
  const alert = alertAfter !== undefined && now - t >= alertAfter * 1000;
  return <time class={cls + (alert ? ` t-${tone} alert` : '')} data-ui={chip ? 'number' : undefined}
    dateTime={new Date(t).toISOString()} data-tip={alert ? `${alertLabel} · ${exact(t)}` : exact(t)}
    aria-label={alert ? `${a.long}, ${alertLabel}` : a.long}>
    {alert && <Icon name="triangle-alert" />}{a.text}</time>;
}

export function AgeChip(props: AgeProps) {
  return <Ago {...props} chip />;
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

/* ---------- Empty, loading, error, stale (§8) ---------- */

export const Skeleton = ({ rows }: { rows: number }) => (
  <div class="skeleton" data-ui="status-states" aria-busy="true" aria-label="Loading">{Array.from({ length: rows }, (_, i) => <div key={i} class="sk-row"><i style={{ width: `${55 + (i * 17) % 35}%` }} /><i /></div>)}</div>
);
export const Empty = ({ text, action }: { text: string; action?: ComponentChildren }) => <div class="empty" data-ui="status-states"><p>{text}</p>{action}</div>;
export const ErrorBanner = ({ text, onRetry }: { text: string; onRetry?: () => void }) => (
  <div class="err-banner" data-ui="status-states" role="alert"><Icon name="circle-alert" /><span>{text}</span><span class="grow" />{onRetry && <Button label="Retry" icon={actions.retry} onClick={onRetry} />}</div>
);
export const Stale = ({ at, onRefresh }: { at: number; now: number; onRefresh?: () => void }) => (
  <span class="stale" data-ui="status-states">updated <Ago t={at} /> <Button label="Refresh" icon={actions.retry} iconOnly onClick={onRefresh} /></span>
);

/* ---------- Offline marker (controls/offline-marker.md) ---------- */

/** Says whether shown data is current: offline, updating (stale but online) or signed out; hidden when current. */
export function OfflineMarker({ state, since }: { state: 'current' | 'offline' | 'updating' | 'signed-out'; since?: number; now: number }) {
  if (state === 'current') return null;
  const lastUpdate = since ? exact(since) : null;
  const word = state === 'offline' ? 'offline' : state === 'updating' ? 'updating' : 'signed out';
  const long = state === 'signed-out' ? 'Session expired. Sign in again.' : `${word}${lastUpdate ? `, last updated ${lastUpdate}` : ''}`;
  return <span class={`offline-marker t-${state === 'signed-out' ? 'danger' : state === 'offline' ? 'attention' : 'neutral'}`} data-ui="offline-marker" role="status" aria-label={long}>
    <Icon name={state === 'updating' ? 'rotate-cw' : state === 'offline' ? 'circle-slash' : 'circle-alert'} />{word}{since && state !== 'signed-out' ? <> · <Ago t={since} /></> : ''}
    {state === 'signed-out' && <a href={location.pathname}>Sign in again</a>}
  </span>;
}

/** Reserved two-line feedback region; updates never displace adjacent controls. */
export const StatusLine = ({ children }: { children?: ComponentChildren }) => <div role="status" style={{ minHeight: '2lh' }}>{children}</div>;

/** Writes text to the clipboard; the caller shows a failure (status line with retry). */
export const copyText = (text: string) => navigator.clipboard.writeText(text);

/** A copy action: its icon turns into a check (a cross when the copy failed) for a moment and the result is announced;
 *  the button keeps its width (OR-23). */
export function CopyButton({ label, value, iconOnly }: { label: string; value: string; iconOnly?: boolean }) {
  const [done, setDone] = useState<boolean | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const show = (ok: boolean) => { setDone(ok); clearTimeout(timer.current); timer.current = setTimeout(() => setDone(null), 1500); };
  return <><Button label={label} icon={done === null ? actions.copy : done ? 'check' : 'circle-x'} kind="quiet" iconOnly={iconOnly} onClick={() => copyText(value).then(() => show(true), () => show(false))} />
    <span class="sr-only" role="status">{done === null ? '' : done ? `${label.replace(/^Copy /, '')} copied` : 'Copy failed'}</span></>;
}

/** Focuses an element when it mounts. Preact does not do this for `autoFocus`, and browsers honour the attribute
 *  only while the page loads, so a dialog or menu opened later kept focus on its opener. One stable function, so it runs once. */
export const focusOnMount = (el: HTMLElement | null) => el?.focus();

/* ---------- Menu dismissal shared by the short menus under a button ---------- */

/**
 * Closes an open menu on a pointer press outside `ref`, on Escape (focus back to `back` inside ref), and when focus
 * leaves `ref` (Tab away). One rule for SplitButton, ActionMenu and the group and sort chips.
 */
export function useDismiss(open: boolean, ref: { current: HTMLElement | null }, close: () => void, back?: string) {
  useEffect(() => {
    if (!open) return;
    const press = (e: Event) => { if (!ref.current?.contains(e.target as Node)) close(); };
    const key = (e: KeyboardEvent) => { if (e.key !== 'Escape') return; e.stopPropagation(); close(); if (back) ref.current?.querySelector<HTMLElement>(back)?.focus(); };
    const blur = (e: FocusEvent) => { const to = e.relatedTarget as Node | null; if (to && !ref.current?.contains(to)) close(); };
    const el = ref.current;
    document.addEventListener('pointerdown', press); document.addEventListener('keydown', key, true); el?.addEventListener('focusout', blur);
    return () => { document.removeEventListener('pointerdown', press); document.removeEventListener('keydown', key, true); el?.removeEventListener('focusout', blur); };
  }, [open]);
}

/* ---------- Split button (split-button.md) ---------- */

/** Default action plus a short menu of alternatives for the same place (OR-14 allows short action menus). */
export function SplitButton({ main, alternatives, kind = 'default' }: {
  main: { label: string; icon?: IconName; onClick?: () => void };
  alternatives: { label: string; icon?: IconName; onClick?: () => void }[]; kind?: 'primary' | 'default';
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLSpanElement>(null);
  useDismiss(open, ref, () => setOpen(false), '.split-more');
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
  useDismiss(open, ref, () => setOpen(false), '.btn');
  return <span class="split action-menu" data-ui="split-button" ref={ref}>
    <button type="button" class="btn default icon-only" aria-haspopup="menu" aria-expanded={open} aria-label={label} data-tip={disabledReason ?? label} aria-disabled={!!disabledReason || undefined}
      onClick={() => !disabledReason && setOpen(!open)}><Icon name={actions.more} /></button>
    {open && <span class="menu" role="menu">{items.map(a =>
      <button type="button" role="menuitem" key={a.label} aria-disabled={!!a.disabledReason || undefined} data-tip={a.disabledReason} onClick={() => { if (a.disabledReason) return; setOpen(false); a.onClick(); }}><Icon name={a.icon} />{a.label}</button>)}</span>}
  </span>;
}

/**
 * Search as an icon (OR-31): the field shows only while focused or not empty. Filters are typed in it (OR-36), e.g.
 * `agent:claude host:mac`; the list's server reads the terms.
 */
export function SearchBox({ label, value, onInput, onEnter, always }: { label: string; value: string; onInput: (v: string) => void; onEnter?: () => void;
  /** A picker whose job is typing keeps the field shown. */ always?: boolean }) {
  const [focused, setOpen] = useState(false), open = focused || !!always;
  // grab: focus the field once, right after the icon was clicked; closing waits for the click that moved focus (OR-23).
  const grab = useRef(false), closing = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  if (!open && !value) return <Button label={label} icon={actions.search} iconOnly kind="quiet" onClick={() => { grab.current = true; setOpen(true); }} />;
  return <label class="search" data-ui="search"><Icon name={actions.search} /><input type="search" value={value} placeholder={label} aria-label={label}
    ref={e => { if (grab.current && e) { grab.current = false; e.focus(); } }}
    onFocus={() => { clearTimeout(closing.current); setOpen(true); }} onBlur={() => { closing.current = setTimeout(() => setOpen(false), 250); }}
    onKeyDown={e => { if (isSubmitEnter(e)) onEnter?.(); if (e.key === 'Escape') { onInput(''); (e.target as HTMLInputElement).blur(); } }}
    onInput={e => onInput((e.target as HTMLInputElement).value)} /></label>;
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

/* ---------- Detail sections (D11, C3) ---------- */

export const DetailSection = ({ title, children }: { title: string; children: ComponentChildren }) =>
  <section class="dsec" data-ui="detail-section"><h3>{title}</h3>{children}</section>;
export const KvRow = ({ icon, label, meta, value }: { icon: IconName; label: string; meta?: ComponentChildren; value?: ComponentChildren }) =>
  <div class="kv-row"><Icon name={icon} /><b>{label}</b>{meta && <span class="muted">{meta}</span>}<span class="grow" />{value}</div>;
