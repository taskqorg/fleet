// Floating overlay layers and entity references: tooltips, entity codes/cards, and context menus.
import { createContext, type ComponentChildren, type TargetedMouseEvent } from 'preact';
import { useContext, useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks';
import { type IconName, type Harness, type EntityKind, entityKinds, codeKind, ENTITY_CODE, states, actions, harnesses } from '../registry.ts';
import { bytes, count, duration } from '../format.ts';
import type { EntityCardReply } from '../../../../contracts/generated/ops.ts';
import tokens from '../../tokens.json';
import { Icon, Button, StateBadge, HarnessMark, Ago, CopyButton, popoverAttr, type PopoverElement } from './primitives.tsx';

/* ---------- Tooltip (OR-20, modern-web-guidance: hint popover + visualViewport edge clamping) ---------- */

/** One small tooltip for the whole app, for elements with data-tip. Shows after a delay on hover or focus,
 *  about that element only, never over a field being typed in, hidden on any key press or scroll. Mount once. */
export function TooltipLayer({ delay = 600 }: { delay?: number }) {
  const [tip, setTip] = useState<{ text: string; anchor: HTMLElement } | null>(null);
  const box = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = box.current as PopoverElement | null;
    if (!tip || !el) return;
    if (!el.matches(':popover-open')) el.showPopover({ source: tip.anchor });
    const anchor = tip.anchor.getBoundingClientRect();
    const gap = parseInt(tokens.space[2]);
    const viewport = window.visualViewport;
    const viewWidth = viewport?.width ?? innerWidth;
    const viewHeight = viewport?.height ?? innerHeight;
    const x = viewport?.offsetLeft ?? 0;
    const y = viewport?.offsetTop ?? 0;
    el.style.maxWidth = `min(var(--size-tip), ${Math.max(0, viewWidth - 2 * gap)}px)`;
    const { width, height } = el.getBoundingClientRect();
    const left = Math.max(x + gap, Math.min(anchor.left + (anchor.width - width) / 2, x + viewWidth - gap - width));
    const above = anchor.top - height - gap;
    const top = above >= y + gap ? above : Math.max(y + gap, Math.min(anchor.bottom + gap, y + viewHeight - gap - height));
    el.style.left = `${left}px`;
    el.style.top = `${top}px`;
    return () => { if (el.matches(':popover-open')) el.hidePopover(); };
  }, [tip]);
  useEffect(() => {
    let timer = 0;
    const hide = () => { clearTimeout(timer); setTip(null); };
    const show = (e: Event) => {
      if (e instanceof PointerEvent && e.pointerType === 'touch') return;
      const el = (e.target as HTMLElement).closest?.('[data-tip]') as HTMLElement | null;
      hide();
      if (!el || document.activeElement?.matches('input,textarea,[contenteditable]')) return;
      // data-tip-narrow: only when the visible label next to it is hidden (icon-only rail).
      if (el.hasAttribute('data-tip-narrow') && el.nextElementSibling?.getClientRects().length) return;
      timer = window.setTimeout(() => { if (el.isConnected && el.dataset.tip) setTip({ text: el.dataset.tip, anchor: el }); }, delay);
    };
    const opts = { capture: true, passive: true };
    document.addEventListener('pointerover', show, opts); document.addEventListener('focusin', show, opts);
    window.addEventListener('resize', hide);
    window.visualViewport?.addEventListener('resize', hide);
    window.visualViewport?.addEventListener('scroll', hide);
    for (const ev of ['pointerout', 'focusout', 'keydown', 'scroll', 'input', 'pointerdown']) document.addEventListener(ev, hide, opts);
    return () => { hide(); document.removeEventListener('pointerover', show, opts); document.removeEventListener('focusin', show, opts);
      window.removeEventListener('resize', hide);
      window.visualViewport?.removeEventListener('resize', hide);
      window.visualViewport?.removeEventListener('scroll', hide);
      for (const ev of ['pointerout', 'focusout', 'keydown', 'scroll', 'input', 'pointerdown']) document.removeEventListener(ev, hide, opts); };
  }, [delay]);
  return tip && <div ref={box} {...popoverAttr('hint')} class="tooltip" role="tooltip">{tip.text}</div>;
}

/* ---------- Entity code and card (controls/entity-card.md; D17, OR-38) ---------- */

export type EntityCardData = EntityCardReply;
/** A reference: a kind with its id, or a D17 code alone (the card resolves it). */
export type EntityRef = { kind: EntityKind; id?: string; code?: string };
export type EntityCardRead = { data: EntityCardData | null; error: string | null; retry?: () => void };
/** Where cards come from: the app reads `entity_card` through its store; the book serves fixtures. `useCard` is a hook,
 *  called once per open card; `open` goes to an in-app path (default: a full page load). */
export type EntityCardSource = { useCard: (ref: EntityRef) => EntityCardRead; open?: (path: string) => void };
export const EntityCards = createContext<EntityCardSource | null>(null);

// The one open card (one at a time): which reference, the code it hangs from, and whether a key opened it.
type Shown = { ref: EntityRef; anchor: HTMLElement; keyboard: boolean };
let shown: Shown | null = null;
const watchers = new Set<() => void>();
const showCard = (s: Shown | null) => { shown = s; for (const w of watchers) w(); };
/** The open card; with `mine`, re-renders only when that element's own card opens or closes (not on every other code's). */
function useShownCard(mine?: { current: HTMLElement | null }) {
  const [, set] = useState(0);
  useEffect(() => {
    let was = !!mine && shown?.anchor === mine.current;
    const w = () => { const is = !!mine && shown?.anchor === mine.current; if (!mine || is !== was) { was = is; set(n => n + 1); } };
    watchers.add(w); return () => { watchers.delete(w); };
  }, []);
  return shown;
}
const CARD_ID = 'entity-card';

/** The one formatter of an entity reference (D17, OR-38). A human code (`T-FLT-42`, a pull request's number, `v1.2.3`) in mono after the
 *  name; a kind without a code (run, result, experiment) as its kind icon. A click, tap, Enter or Space opens its
 *  EntityCard and never the row (OR-26); the same again closes it. The UUID is never shown: the card copies it. */
export function EntityCode({ code, id, kind, cls, label }: { code?: string | null; id?: string | null; kind?: EntityKind; cls?: string; label?: boolean }) {
  const el = useRef<HTMLButtonElement>(null);
  const cur = useShownCard(el);
  // A code that leaves the page (its row recycled by a virtual list, the view changed) takes its card with it.
  const last = useRef<HTMLButtonElement | null>(null);
  useEffect(() => { last.current = el.current; });
  useEffect(() => () => { if (last.current && shown?.anchor === last.current) showCard(null); }, []);
  const k = kind ?? (code ? codeKind[code[0]] : undefined);
  if (!k || !id && !code) return null;
  const open = !!cur && !!el.current && cur.anchor === el.current;
  const toggle = (e: TargetedMouseEvent<HTMLButtonElement>) => {
    e.preventDefault(); e.stopPropagation();
    // The open card is read now, not from the last render: a second click before a re-render still closes it. A click
    // from the keyboard (Enter, Space) has no pointer detail: focus then moves into the card.
    showCard(shown?.anchor === e.currentTarget ? null : { ref: id ? { kind: k, id, ...(code ? { code } : {}) } : { kind: k, code: code! }, anchor: e.currentTarget, keyboard: e.detail === 0 });
  };
  const what = entityKinds[k].label;
  const trigger = <button ref={el} type="button" class={`${code ? 'id ec-code' : label ? 'btn quiet ec-code ec-label' : 'btn quiet icon-only ec-code'}${cls ? ' ' + cls : ''}`} data-ui="entity-code" aria-haspopup="dialog" aria-expanded={open}
    aria-controls={open ? CARD_ID : undefined} aria-label={`${what}${code ? ' ' + code : ''}: show details`} data-tip={open ? undefined : 'Show details'} onClick={toggle}>
    {code ?? <><Icon name={entityKinds[k].icon} />{label && what}</>}</button>;
  if (!code) return trigger;
  return <span class={cls === 'inline' ? 'ec-reference inline' : 'ec-reference'}>{trigger}<span class="ec-copy" onClick={e => e.stopPropagation()}>
    <CopyButton label={`Copy ${code}`} value={code} iconOnly />
  </span></span>;
}

/** D17 codes in a text as references (agent messages, OR-38): every other part stays text. */
export function linkCodes(text: string, key: number): ComponentChildren {
  const out: ComponentChildren[] = []; let last = 0;
  for (const m of text.matchAll(ENTITY_CODE)) {
    if (m.index! > last) out.push(text.slice(last, m.index));
    out.push(<EntityCode key={`${key}:${m.index}`} code={m[0]} cls="inline" />);
    last = m.index! + m[0].length;
  }
  if (!out.length) return text;
  if (last < text.length) out.push(text.slice(last));
  return out;
}

/** The floating card of the open EntityCode: mount once (AppShell does). A non-modal dialog (OR-14 informational, OR-16
 *  surface): under the code, above it when there is no room, kept inside the window. Escape, a click outside, a scroll,
 *  focus leaving it or the code again close it; Escape and the Open action give focus back to the code. */
export function EntityCardLayer() {
  const cur = useShownCard();
  const box = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = box.current as PopoverElement | null;
    if (!cur || !el) return;
    const anchor = cur.anchor;
    const place = () => {
      const a = anchor.getBoundingClientRect(), r = el.getBoundingClientRect(), gap = 4, edge = 8;
      const below = a.bottom + gap + r.height <= innerHeight - edge || a.top - gap - r.height < edge && innerHeight - a.bottom > a.top;
      el.style.top = `${Math.max(edge, Math.min(innerHeight - edge - r.height, below ? a.bottom + gap : a.top - gap - r.height))}px`;
      el.style.left = `${Math.max(edge, Math.min(a.left, innerWidth - edge - r.width))}px`;
      el.dataset.side = below ? 'below' : 'above';
    };
    const close = (refocus: boolean) => { showCard(null); if (refocus && anchor.isConnected) anchor.focus({ preventScroll: true }); };
    const inside = (n: EventTarget | null) => n instanceof Node && (el.contains(n) || anchor.contains(n));
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { const mine = inside(document.activeElement); if (mine) { e.stopPropagation(); e.preventDefault(); } close(mine); return; }
      if (e.key !== 'Tab' || !el.contains(document.activeElement)) return;
      const all = [...el.querySelectorAll<HTMLElement>('a[tabindex],a[href],button')], at = all.indexOf(document.activeElement as HTMLElement);
      if (e.shiftKey ? at <= 0 : at === all.length - 1) { e.preventDefault(); close(true); }
    };
    const down = (e: PointerEvent) => { if (!inside(e.target)) close(false); };
    const out = (e: FocusEvent) => { if (e.relatedTarget && !inside(e.relatedTarget)) close(false); };
    const sized = new ResizeObserver(place); sized.observe(el);
    const seen = new IntersectionObserver(es => { if (es.some(x => !x.isIntersecting)) close(el.contains(document.activeElement)); });
    seen.observe(anchor);
    document.addEventListener('pointerdown', down, true);
    document.addEventListener('focusout', out, true);
    document.addEventListener('keydown', key, true);
    addEventListener('scroll', place, true); addEventListener('resize', place);
    if (!el.matches(':popover-open')) el.showPopover({ source: anchor });
    place();
    return () => {
      sized.disconnect(); seen.disconnect();
      document.removeEventListener('pointerdown', down, true);
      document.removeEventListener('focusout', out, true);
      document.removeEventListener('keydown', key, true);
      removeEventListener('scroll', place, true); removeEventListener('resize', place);
      if (el.matches(':popover-open')) el.hidePopover();
    };
  }, [cur]);
  // Opened by a key: focus goes to Open, which stays the same element while the card loads.
  useEffect(() => {
    const el = box.current, open = el?.querySelector<HTMLElement>('.ec-open');
    if (!cur?.keyboard || !el || !open) return;
    open.focus({ preventScroll: true });
    const reveal = () => {
      if (document.activeElement !== open) return;
      const b = open.getBoundingClientRect(), r = el.getBoundingClientRect();
      if (b.bottom > r.bottom) el.scrollTop += b.bottom - r.bottom;
    };
    reveal();
    const layout = new ResizeObserver(reveal);
    layout.observe(el);
    if (open.parentElement) layout.observe(open.parentElement);
    return () => layout.disconnect();
  }, [cur]);
  if (!cur) return null;
  const k = entityKinds[cur.ref.kind];
  // The card takes its code's theme where a region sets one (the book's light and dark frames).
  const theme = cur.anchor.closest('.th-dark') ? ' th-dark' : cur.anchor.closest('.th-light') ? ' th-light' : '';
  return <div ref={box} id={CARD_ID} {...popoverAttr('manual')} class={`ecard${theme}`} data-ui="entity-card" role="dialog" aria-label={`${k.label}${cur.ref.code ? ' ' + cur.ref.code : ''}`}>
    <CardBody key={JSON.stringify(cur.ref)} r={cur.ref} onOpen={() => { const a = cur.anchor; showCard(null); if (a.isConnected) a.focus({ preventScroll: true }); }} /></div>;
}

function CardBody({ r, onOpen }: { r: EntityRef; onOpen: () => void }) {
  const source = useContext(EntityCards);
  const read: EntityCardRead = source ? source.useCard(r) : { data: null, error: 'Details are not available here' };
  const k = entityKinds[r.kind], c = read.error ? null : read.data, now = Date.now();
  // Before it loads, a release names itself by its tag (the reference may show a label such as UI 1.2.5).
  const code = c ? c.code : r.kind === 'release' ? r.id : r.code;
  // Copy actions: the code (a pull request: owner/repo#n), the UUID, the id of a kind with neither, a result's hash.
  const copies: [string, string][] = c ? [
    ...(c.code ? [[c.kind === 'pull_request' ? 'Copy reference' : 'Copy code', c.kind === 'pull_request' ? c.id : c.code] as [string, string]] : []),
    ...(c.uuid ? [['Copy UUID', c.uuid] as [string, string]] : []),
    ...(!c.code && !c.uuid ? [['Copy id', c.id] as [string, string]] : []),
    ...(c.hash ? [['Copy SHA-256', c.hash] as [string, string]] : [])] : r.kind === 'pull_request' && r.id ? [['Copy reference', r.id.replace(/^([^~]+)~([^~]+)~(\d+)$/, '$1/$2#$3')]] : code ? [['Copy code', code]] : [];
  const at = c?.at ? Date.parse(c.at) : NaN;
  const go = (e: MouseEvent) => {
    if (!c?.path) { e.preventDefault(); return; }
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault(); onOpen();
    if (source?.open) source.open(c.path); else location.assign(c.path);
  };
  const family = k.family, state = c?.state && family && c.state in states[family] ? c.state : null;
  return <>
    <div class="ec-h"><Icon name={k.icon} label={k.label} />{code && <span class="id">{code}</span>}<span class="grow" />
      {state && family && <StateBadge family={family} state={state as never} />}</div>
    {c ? <>
      <p class="ec-t">{c.title}</p>
      {(c.projectId || c.harness || Number.isFinite(at)) && <p class="ec-m">{c.projectId && <span class="mono">{c.projectId}</span>}
        {c.harness && c.harness in harnesses && <HarnessMark h={c.harness as Harness} />}
        {Number.isFinite(at) && <span>{c.atLabel ? c.atLabel + ' ' : ''}<Ago t={at} /></span>}</p>}
      {c.facts.length > 0 && <dl class="ec-f">{c.facts.map(f => <div key={f.label}><dt>{f.label}</dt><dd class={f.type === 'text' || f.type === 'time' ? undefined : 'mono'}>{factText(f)}</dd></div>)}</dl>}
    </> : read.error ? <div class="ec-err" role="alert"><Icon name="circle-alert" /><span>{read.error}</span>{read.retry && <Button label="Retry" icon={actions.retry} iconOnly kind="quiet" onClick={read.retry} />}</div>
      : <div class="ec-sk" aria-busy="true" aria-label="Loading"><i /><i /><i /></div>}
    <div class="ec-a">
      {/* One element whether or not the path is known yet, so focus on it survives the load. */}
      <a class="btn primary ec-open" data-ui="button" href={c?.path ?? undefined} tabIndex={0} role={c?.path ? undefined : 'link'} aria-disabled={c?.path ? undefined : true}
        data-tip={c?.path ? undefined : read.error ? 'Not available' : 'Loading'} onClick={go}><Icon name="arrow-right" /><span>Open</span></a>
      {copies.map(([label, value]) => <CopyButton key={label} label={label} value={value} />)}
      {c?.url && <a class="btn quiet icon-only" data-ui="button" href={c.url} target="_blank" rel="noopener noreferrer" aria-label="Open on GitHub" data-tip="Open on GitHub"><Icon name="github" /></a>}
    </div>
  </>;
}

function factText(f: EntityCardData['facts'][number]): ComponentChildren {
  const n = Number(f.value);
  switch (f.type) {
    case 'time': { const t = Date.parse(f.value); return Number.isFinite(t) ? <Ago t={t} /> : f.value; }
    case 'count': return Number.isFinite(n) ? count(n).text : f.value;
    case 'bytes': return Number.isFinite(n) ? bytes(n).text : f.value;
    case 'duration': return Number.isFinite(n) ? duration(n).text : f.value;
    default: return f.value;
  }
}

/* ---------- Row menu: the right-click menu of a list row (OR-12, OR-14) ---------- */

export type MenuItem = { label: string; icon: IconName; onClick: () => void; disabledReason?: string; danger?: boolean };
/**
 * A short menu at the pointer: right click, long press, or the Menu key / Shift+F10 on a focused row. A list page mounts one
 * for the whole list (rows are virtual). Esc, a click outside or a scroll closes it; arrows move between items.
 */
export function ContextMenu({ at, label, items, onClose }: { at: { x: number; y: number }; label: string; items: MenuItem[]; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  // Focus goes back where it was (the row) when the menu closes, unless the person moved it elsewhere.
  const back = useRef(document.activeElement as HTMLElement | null);
  useEffect(() => () => { const a = document.activeElement; if (!a || a === document.body || ref.current?.contains(a) || !a.isConnected) back.current?.focus?.({ preventScroll: true }); }, []);
  useEffect(() => {
    const close = (e: Event) => {
      if (e instanceof KeyboardEvent) { if (e.key !== 'Escape') return; e.stopPropagation(); }
      else if (e.type === 'pointerdown' && ref.current?.contains(e.target as Node)) return;
      onClose();
    };
    document.addEventListener('pointerdown', close, true); document.addEventListener('keydown', close, true); document.addEventListener('scroll', close, true); addEventListener('blur', close);
    return () => { document.removeEventListener('pointerdown', close, true); document.removeEventListener('keydown', close, true); document.removeEventListener('scroll', close, true); removeEventListener('blur', close); };
  }, [onClose]);
  // Keep the menu inside the window: it opens to the left or upward when the pointer is near an edge.
  useLayoutEffect(() => {
    const el = ref.current; if (!el) return;
    const r = el.getBoundingClientRect();
    el.style.left = `${Math.max(4, at.x + r.width > innerWidth - 4 ? at.x - r.width : at.x)}px`;
    el.style.top = `${Math.max(4, at.y + r.height > innerHeight - 4 ? at.y - r.height : at.y)}px`;
    el.querySelector<HTMLElement>('[role=menuitem]')?.focus();
  }, [at.x, at.y]);
  const move = (e: KeyboardEvent) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const all = [...ref.current!.querySelectorAll<HTMLElement>('[role=menuitem]')], i = all.indexOf(document.activeElement as HTMLElement);
    all[(i + (e.key === 'ArrowDown' ? 1 : all.length - 1)) % all.length]?.focus();
  };
  return <div ref={ref} class="menu ctx-menu" data-ui="context-menu" role="menu" aria-label={label} style={{ left: at.x, top: at.y }} onKeyDown={move}>
    {items.map(a => <button type="button" role="menuitem" key={a.label} class={a.danger ? 't-danger' : undefined} aria-disabled={!!a.disabledReason || undefined} data-tip={a.disabledReason}
      onClick={() => { if (a.disabledReason) return; onClose(); a.onClick(); }}><Icon name={a.icon} />{a.label}</button>)}
  </div>;
}
