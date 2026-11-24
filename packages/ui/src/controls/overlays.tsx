// Floating overlay layers and entity references: tooltips, entity codes/cards, and context menus.
import { createContext, createEffect, createMemo, createSignal, For, onSettled, Show, untrack, useContext } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { type IconName, type Harness, type EntityKind, entityKinds, codeKind, ENTITY_CODE, states, actions, harnesses } from '../registry.ts';
import { bytes, count, duration } from '../format.ts';
import type { EntityCardReply } from '../../../../contracts/generated/ops.ts';
import tokens from '../../tokens.json';
import { Icon, Button, StateBadge, HarnessMark, Ago, CopyButton, popoverAttr, type PopoverElement } from './primitives.tsx';

/* ---------- Tooltip (OR-20, modern-web-guidance: hint popover + visualViewport edge clamping) ---------- */

function TooltipBox(p: { text: string; anchor: HTMLElement }) {
  let box: HTMLDivElement | undefined;
  onSettled(() => {
    const el = box as PopoverElement | undefined;
    if (!el) return;
    if (!el.matches(':popover-open')) el.showPopover({ source: p.anchor });
    const anchor = p.anchor.getBoundingClientRect();
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
  });
  return <div ref={el => { box = el; }} {...popoverAttr('hint')} class="tooltip" role="tooltip">{p.text}</div>;
}

/** One small tooltip for the whole app, for elements with data-tip. Shows after a delay on hover or focus,
 *  about that element only, never over a field being typed in, hidden on any key press or scroll. Mount once. */
export function TooltipLayer(p: { delay?: number }) {
  const [tip, setTip] = createSignal<{ text: string; anchor: HTMLElement } | null>(null, { ownedWrite: true });
  createEffect(() => p.delay ?? 600, delay => {
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
    return () => {
      hide();
      document.removeEventListener('pointerover', show, opts);
      document.removeEventListener('focusin', show, opts);
      window.removeEventListener('resize', hide);
      window.visualViewport?.removeEventListener('resize', hide);
      window.visualViewport?.removeEventListener('scroll', hide);
      for (const ev of ['pointerout', 'focusout', 'keydown', 'scroll', 'input', 'pointerdown']) document.removeEventListener(ev, hide, opts);
    };
  });
  return <Show when={tip()} keyed>{t => <TooltipBox text={t.text} anchor={t.anchor} />}</Show>;
}

/* ---------- Entity code and card (controls/entity-card.md; D17, OR-38) ---------- */

export type EntityCardData = EntityCardReply;
/** A reference: a kind with its id, or a D17 code alone (the card resolves it). */
export type EntityRef = { kind: EntityKind; id?: string; code?: string };
export type EntityCardRead = { data: EntityCardData | null; error: string | null; retry?: () => void };
/** Where cards come from: the app reads `entity_card` through its store; the book serves fixtures. `useCard` is called once per open card; `open` goes to an in-app path (default: a full page load). */
export type EntityCardSource = { useCard: (ref: EntityRef) => EntityCardRead; open?: (path: string) => void };
export const EntityCards = createContext<EntityCardSource | null>(null);

// The one open card (one at a time): which reference, the code it hangs from, and whether a key opened it.
type Shown = { ref: EntityRef; anchor: HTMLElement; keyboard: boolean };
const [shown, setShown] = createSignal<Shown | null>(null, { ownedWrite: true });
const showCard = (s: Shown | null) => setShown(s);
const CARD_ID = 'entity-card';

/** The one formatter of an entity reference (D17, OR-38). A human code (`T-FLT-42`, a pull request's number, `v1.2.3`) in mono after the
 *  name; a kind without a code (run, result, experiment) as its kind icon. A click, tap, Enter or Space opens its
 *  EntityCard and never the row (OR-26); the same again closes it. The UUID is never shown: the card copies it. */
export function EntityCode(p: { code?: string | null; id?: string | null; kind?: EntityKind; cls?: string; label?: boolean }) {
  let el: HTMLButtonElement | undefined;
  onSettled(() => () => { if (el && untrack(shown)?.anchor === el) showCard(null); });
  const k = createMemo(() => p.kind ?? (p.code ? codeKind[p.code[0]] : undefined));
  const open = createMemo(() => { const s = shown(); return !!s && !!el && s.anchor === el; });
  const toggle = (e: MouseEvent & { currentTarget: HTMLButtonElement }) => {
    e.preventDefault();
    e.stopPropagation();
    const kind = k();
    if (!kind) return;
    // The open card is read now, not from the last render: a second click before a re-render still closes it. A click
    // from the keyboard (Enter, Space) has no pointer detail: focus then moves into the card.
    showCard(untrack(shown)?.anchor === e.currentTarget ? null : {
      ref: p.id ? { kind, id: p.id, ...(p.code ? { code: p.code } : {}) } : { kind, code: p.code! },
      anchor: e.currentTarget,
      keyboard: e.detail === 0,
    });
  };
  return (
    <Show when={k() && (p.id || p.code)}>
      {(() => {
        const what = () => entityKinds[k()!].label;
        const trigger = (
          <button
            ref={node => { el = node; }}
            type="button"
            class={[p.code ? 'id ec-code' : p.label ? 'btn quiet ec-code ec-label' : 'btn quiet icon-only ec-code', p.cls]}
            data-ui="entity-code"
            aria-haspopup="dialog"
            aria-expanded={open() ? 'true' : 'false'}
            aria-controls={open() ? CARD_ID : undefined}
            aria-label={`${what()}${p.code ? ' ' + p.code : ''}: show details`}
            data-tip={open() ? undefined : 'Show details'}
            onClick={toggle}
          >
            {p.code ?? <><Icon name={entityKinds[k()!].icon} />{p.label && what()}</>}
          </button>
        );
        return (
          <Show when={p.code} fallback={trigger}>
            <span class={p.cls === 'inline' ? 'ec-reference inline' : 'ec-reference'}>
              {trigger}
              <span class="ec-copy" onClick={e => e.stopPropagation()}>
                <CopyButton label={`Copy ${p.code}`} value={p.code!} iconOnly />
              </span>
            </span>
          </Show>
        );
      })()}
    </Show>
  );
}

/** D17 codes in a text as references (agent messages, OR-38): every other part stays text. */
export function linkCodes(text: string, _key: number, literal = (value: string, _start: number): JSX.Element => value): JSX.Element {
  const out: JSX.Element[] = []; let last = 0;
  for (const m of text.matchAll(ENTITY_CODE)) {
    if (m.index! > last) out.push(literal(text.slice(last, m.index), last));
    out.push(<EntityCode code={m[0]} cls="inline" />);
    last = m.index! + m[0].length;
  }
  if (!out.length) return literal(text, 0);
  if (last < text.length) out.push(literal(text.slice(last), last));
  return out;
}

function EntityCardDialog(p: { cur: Shown }) {
  let box: HTMLDivElement | undefined;
  onSettled(() => {
    const el = box as PopoverElement | undefined;
    if (!el) return;
    const anchor = p.cur.anchor;
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

    // Opened by a key: focus goes to Open, which stays the same element while the card loads.
    const openEl = el.querySelector<HTMLElement>('.ec-open');
    let focusLayout: ResizeObserver | undefined;
    if (p.cur.keyboard && openEl) {
      openEl.focus({ preventScroll: true });
      const reveal = () => {
        if (document.activeElement !== openEl) return;
        const b = openEl.getBoundingClientRect(), r = el.getBoundingClientRect();
        if (b.bottom > r.bottom) el.scrollTop += b.bottom - r.bottom;
      };
      reveal();
      focusLayout = new ResizeObserver(reveal);
      focusLayout.observe(el);
      if (openEl.parentElement) focusLayout.observe(openEl.parentElement);
    }

    return () => {
      sized.disconnect(); seen.disconnect(); focusLayout?.disconnect();
      document.removeEventListener('pointerdown', down, true);
      document.removeEventListener('focusout', out, true);
      document.removeEventListener('keydown', key, true);
      removeEventListener('scroll', place, true); removeEventListener('resize', place);
      if (el.matches(':popover-open')) el.hidePopover();
    };
  });
  const k = () => entityKinds[p.cur.ref.kind];
  // The card takes its code's theme where a region sets one (the book's light and dark frames).
  const theme = () => p.cur.anchor.closest('.th-dark') ? 'th-dark' : p.cur.anchor.closest('.th-light') ? 'th-light' : '';
  return (
    <div
      ref={el => { box = el; }}
      id={CARD_ID}
      {...popoverAttr('manual')}
      class={['ecard', theme()]}
      data-ui="entity-card"
      role="dialog"
      aria-label={`${k().label}${p.cur.ref.code ? ' ' + p.cur.ref.code : ''}`}
    >
      <CardBody
        r={p.cur.ref}
        onOpen={() => {
          const a = p.cur.anchor;
          showCard(null);
          if (a.isConnected) a.focus({ preventScroll: true });
        }}
      />
    </div>
  );
}

/** The floating card of the open EntityCode: mount once (AppShell does). A non-modal dialog (OR-14 informational, OR-16
 *  surface): under the code, above it when there is no room, kept inside the window. Escape, a click outside, a scroll,
 *  focus leaving it or the code again close it; Escape and the Open action give focus back to the code. */
export function EntityCardLayer() {
  return <Show when={shown()} keyed>{c => <EntityCardDialog cur={c} />}</Show>;
}

function CardBody(p: { r: EntityRef; onOpen: () => void }) {
  const source = useContext(EntityCards);
  const read: EntityCardRead = source ? source.useCard(untrack(() => p.r)) : { data: null, error: 'Details are not available here' };
  const k = createMemo(() => entityKinds[p.r.kind]);
  const c = createMemo(() => read.error ? null : read.data);
  // Before it loads, a release names itself by its tag (the reference may show a label such as UI 1.2.5).
  const code = createMemo(() => c() ? c()!.code : p.r.kind === 'release' ? p.r.id : p.r.code);
  // Copy actions: the code (a pull request: owner/repo#n), the UUID, the id of a kind with neither, a result's hash.
  const copies = createMemo<[string, string][]>(() => {
    const data = c(), cd = code();
    if (data) {
      return [
        ...(data.code ? [[data.kind === 'pull_request' ? 'Copy reference' : 'Copy code', data.kind === 'pull_request' ? data.id : data.code] as [string, string]] : []),
        ...(data.uuid ? [['Copy UUID', data.uuid] as [string, string]] : []),
        ...(!data.code && !data.uuid ? [['Copy id', data.id] as [string, string]] : []),
        ...(data.hash ? [['Copy SHA-256', data.hash] as [string, string]] : []),
      ];
    }
    if (p.r.kind === 'pull_request' && p.r.id) return [['Copy reference', p.r.id.replace(/^([^~]+)~([^~]+)~(\d+)$/, '$1/$2#$3')]];
    return cd ? [['Copy code', cd]] : [];
  });
  const at = createMemo(() => c()?.at ? Date.parse(c()!.at!) : NaN);
  const go = (e: MouseEvent) => {
    const path = c()?.path;
    if (!path) { e.preventDefault(); return; }
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    p.onOpen();
    if (source?.open) source.open(path); else location.assign(path);
  };
  const family = () => k().family;
  const state = createMemo(() => {
    const fam = family(), st = c()?.state;
    return st && fam && st in states[fam] ? st : null;
  });
  return (
    <>
      <div class="ec-h">
        <Icon name={k().icon} label={k().label} />
        <Show when={code()} keyed>{cd => <span class="id">{cd}</span>}</Show>
        <span class="grow" />
        <Show when={state() && family()}>
          <StateBadge family={family()!} state={state() as never} />
        </Show>
      </div>
      <Show
        when={c()}
        keyed
        fallback={
          <Show
            when={read.error}
            keyed
            fallback={<div class="ec-sk" aria-busy="true" aria-label="Loading"><i /><i /><i /></div>}
          >
            {err => (
              <div class="ec-err" role="alert">
                <Icon name="circle-alert" />
                <span>{err}</span>
                <Show when={read.retry} keyed>{retry => <Button label="Retry" icon={actions.retry} iconOnly kind="quiet" onClick={retry} />}</Show>
              </div>
            )}
          </Show>
        }
      >
        {data => (
          <>
            <p class="ec-t">{data.title}</p>
            <Show when={data.projectId || data.harness || Number.isFinite(at())}>
              <p class="ec-m">
                <Show when={data.projectId} keyed>{pid => <span class="mono">{pid}</span>}</Show>
                <Show when={data.harness && data.harness in harnesses}><HarnessMark h={data.harness as Harness} /></Show>
                <Show when={Number.isFinite(at())}><span>{data.atLabel ? data.atLabel + ' ' : ''}<Ago t={at()} /></span></Show>
              </p>
            </Show>
            <Show when={data.facts.length > 0}>
              <dl class="ec-f">
                <For each={data.facts}>
                  {f => (
                    <div>
                      <dt>{f.label}</dt>
                      <dd class={f.type === 'text' || f.type === 'time' ? undefined : 'mono'}>{factText(f)}</dd>
                    </div>
                  )}
                </For>
              </dl>
            </Show>
          </>
        )}
      </Show>
      <div class="ec-a">
        {/* One element whether or not the path is known yet, so focus on it survives the load. */}
        <a
          class="btn primary ec-open"
          data-ui="button"
          href={c()?.path ?? undefined}
          tabindex={0}
          role={c()?.path ? undefined : 'link'}
          aria-disabled={c()?.path ? undefined : 'true'}
          data-tip={c()?.path ? undefined : read.error ? 'Not available' : 'Loading'}
          onClick={go}
        >
          <Icon name="arrow-right" /><span>Open</span>
        </a>
        <For each={copies()}>{([label, value]) => <CopyButton label={label} value={value} />}</For>
        <Show when={c()?.url} keyed>
          {url => (
            <a class="btn quiet icon-only" data-ui="button" href={url} target="_blank" rel="noopener noreferrer" aria-label="Open on GitHub" data-tip="Open on GitHub">
              <Icon name="github" />
            </a>
          )}
        </Show>
      </div>
    </>
  );
}

function factText(f: EntityCardData['facts'][number]): JSX.Element {
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
export function ContextMenu(p: { at: { x: number; y: number }; label: string; items: MenuItem[]; onClose: () => void }) {
  let ref: HTMLDivElement | undefined;
  // Focus goes back where it was (the row) when the menu closes, unless the person moved it elsewhere.
  const back = document.activeElement as HTMLElement | null;
  onSettled(() => {
    const close = (e: Event) => {
      if (e instanceof KeyboardEvent) { if (e.key !== 'Escape') return; e.stopPropagation(); }
      else if (e.type === 'pointerdown' && ref?.contains(e.target as Node)) return;
      p.onClose();
    };
    document.addEventListener('pointerdown', close, true);
    document.addEventListener('keydown', close, true);
    document.addEventListener('scroll', close, true);
    addEventListener('blur', close);
    return () => {
      document.removeEventListener('pointerdown', close, true);
      document.removeEventListener('keydown', close, true);
      document.removeEventListener('scroll', close, true);
      removeEventListener('blur', close);
      const a = document.activeElement;
      if (!a || a === document.body || ref?.contains(a) || !a.isConnected) back?.focus?.({ preventScroll: true });
    };
  });
  // Keep the menu inside the window: it opens to the left or upward when the pointer is near an edge.
  createEffect(() => [p.at.x, p.at.y] as const, ([x, y]) => {
    const el = ref;
    if (!el) return;
    const r = el.getBoundingClientRect();
    el.style.left = `${Math.max(4, x + r.width > innerWidth - 4 ? x - r.width : x)}px`;
    el.style.top = `${Math.max(4, y + r.height > innerHeight - 4 ? y - r.height : y)}px`;
    el.querySelector<HTMLElement>('[role=menuitem]')?.focus();
  });
  const move = (e: KeyboardEvent) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const all = [...(ref?.querySelectorAll<HTMLElement>('[role=menuitem]') ?? [])];
    const i = all.indexOf(document.activeElement as HTMLElement);
    if (all.length) all[(i + (e.key === 'ArrowDown' ? 1 : all.length - 1)) % all.length]?.focus();
  };
  return (
    <div
      ref={el => { ref = el; }}
      class="menu ctx-menu"
      data-ui="context-menu"
      role="menu"
      aria-label={p.label}
      style={{ left: `${p.at.x}px`, top: `${p.at.y}px` }}
      onKeyDown={move}
    >
      <For each={p.items}>
        {a => (
          <button
            type="button"
            role="menuitem"
            class={a.danger ? 't-danger' : undefined}
            aria-disabled={a.disabledReason ? 'true' : undefined}
            data-tip={a.disabledReason}
            onClick={() => { if (a.disabledReason) return; p.onClose(); a.onClick(); }}
          >
            <Icon name={a.icon} />
            {a.label}
          </button>
        )}
      </For>
    </div>
  );
}
