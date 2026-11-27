// Floating overlay layers and entity references: tooltips, entity codes/cards, and context menus.
import { ProjectTile, projectIdentity } from './identity.tsx';
import { createContext, createEffect, createMemo, createSignal, For, onSettled, Show, untrack, useContext } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { type IconName, type Harness, type EntityKind, entityKinds, codeKind, ENTITY_CODE, states, actions, harnesses } from '../registry.ts';
import { bytes, count, duration } from '../format.ts';
import { APP_URL_REF, HASH_REF, PR_REF, UUID_REF } from '../markdown.ts';
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
/** The live state of a rendered reference: undefined while it loads, null when the entity is unknown or not visible. */
export type EntityLive = { kind: EntityKind; id: string; code: string | null; state: string | null; title: string };
/** Where cards come from: the app reads `entity_card` through its store; the book serves fixtures. `useCard` is called once per open card; `open` goes to an in-app path (default: a full page load).
 *  `live` holds a rendered reference's live state (one batched registry, entity-card.md "Live state of many references"); `src` is the bytes URL of a result preview. */
export type EntityCardSource = {
  useCard: (ref: EntityRef) => EntityCardRead;
  open?: (path: string) => void;
  live?: (ref: EntityRef | { id: string; kind?: undefined }) => { read: () => EntityLive | null | undefined; release: () => void };
  src?: (resultId: string) => string;
};
export const EntityCards = createContext<EntityCardSource | null>(null);
/** The forms of a reference (entity-card.md "Forms"): code and copy; icon in its state's tone and code (default); icon, code, title and state. */
export type EntityForm = 'code' | 'chip' | 'full';

// The one shown card (one at a time): which reference, the code it hangs from, and whether a key opened it.
// Opens on click, tap, Enter, or Space only (OR-44, OR-23, OR-37).
type Shown = { ref: EntityRef; anchor: HTMLElement; keyboard: boolean };
const [shown, setShown] = createSignal<Shown | null>(null, { ownedWrite: true });
const showCard = (s: Shown | null) => setShown(s);
const CARD_ID = 'entity-card';

/** The state word and tone of a reference's live state, null for kinds without a state or a state the registry lacks. */
function stateOf(kind: EntityKind, state: string | null | undefined): { label: string; tone: string; state: string } | null {
  const fam = entityKinds[kind].family;
  if (!fam || !state || !(state in states[fam])) return null;
  const d = (states[fam] as Record<string, { label: string; tone: string }>)[state];
  return { label: d.label, tone: d.tone, state };
}

/** The one formatter of an entity reference (D17, OR-38, OR-44), in three forms (`form`): `code` (the code in mono and
 *  the copy slot), `chip` (the kind icon in its live state's tone and the code; the default) and `full` (icon, code, title
 *  and state badge). A kind without a code shows its kind icon (and the kind's word in `chip`). Its state is live (the
 *  source's registry) and updates in place, tinting briefly (OR-6). Click, tap, Enter or Space opens its card (never
 *  hover); the click never opens the row (OR-26). The UUID is never shown. */
export function EntityCode(p: {
  code?: string | null; id?: string | null; kind?: EntityKind; cls?: string; label?: boolean; form?: EntityForm;
  /** The title the parent already has (`full`), shown until the live state names it. */ title?: string | null;
  /** The state the parent already has, shown until the live state arrives. */ state?: string | null;
}) {
  let el: HTMLButtonElement | undefined;
  const source = useContext(EntityCards);
  // The kind the reference names, else the one its live state learns (a bare id from agent text, `id` alone).
  const k0 = createMemo(() => p.kind ?? (p.code ? codeKind[p.code[0]] : undefined));
  const reg = createMemo<EntityRef | { id: string } | null>(() => {
    const kind = k0();
    if (!kind) return p.id && !p.code ? { id: p.id } : null;
    if (!(p.id || p.code)) return null;
    return p.id ? { kind, id: p.id, ...(p.code ? { code: p.code } : {}) } : { kind, code: p.code! };
  });
  // One registration per mounted reference and entity; released when it unmounts or names another entity.
  const holder = createMemo(() => {
    const r = reg();
    return r && source?.live ? untrack(() => source.live!(r)) : null;
  });
  const k = createMemo(() => k0() ?? holder()?.read()?.kind);
  const ref = createMemo<EntityRef | null>(() => {
    const kind = k(), r = reg();
    if (!kind || !r) return null;
    return 'kind' in r && r.kind ? r : { kind, id: (r as { id: string }).id };
  });
  createEffect(holder, h => () => h?.release());
  onSettled(() => () => { if (el && untrack(shown)?.anchor === el) showCard(null); });
  const live = createMemo(() => holder()?.read());
  const form = () => p.form ?? 'chip';
  const st = createMemo(() => stateOf(k()!, live() === null ? null : live()?.state ?? p.state));
  const code = () => live()?.code ?? p.code ?? undefined;
  const title = () => live()?.title ?? p.title ?? undefined;
  // OR-6: a state that changes while shown tints the reference briefly; success and failure in their tones, others neutral.
  const [flash, setFlash] = createSignal<string | null>(null);
  // Only a change between two loaded live values tints: the first reply after mount is not a change.
  let lastState: string | null | undefined, flashTimer: ReturnType<typeof setTimeout> | undefined;
  createEffect(() => live() === undefined ? undefined : st()?.state ?? null, now => {
    if (now === undefined) return;
    if (lastState !== undefined && now !== lastState) {
      const tone = st()?.tone;
      setFlash(tone === 'success' || tone === 'danger' ? tone : 'neutral');
      clearTimeout(flashTimer); flashTimer = setTimeout(() => setFlash(null), parseInt(tokens.motion.highlight, 10));
    }
    lastState = now;
  });
  const open = createMemo(() => { const s = shown(); return !!s && !!el && s.anchor === el; });
  const toggle = (e: MouseEvent & { currentTarget: HTMLButtonElement }) => {
    e.preventDefault();
    e.stopPropagation();
    const r = ref();
    if (!r) return;
    // The open card is read now, not from the last render: a second click before a re-render still closes it.
    // A click from the keyboard (Enter, Space) has no pointer detail: focus then moves into the card.
    const cur = untrack(shown);
    showCard(cur?.anchor === e.currentTarget ? null : { ref: r, anchor: e.currentTarget, keyboard: e.detail === 0 });
  };
  const what = () => entityKinds[k()!].label;
  const name = () => `${what()}${code() ? ' ' + code() : ''}${st() ? ', ' + st()!.label.toLowerCase() : ''}${live() === null ? ', not visible' : ''}: show details`;
  // A project shows its tile in its identity colour (OR-72); the other kinds their icon in their state's tone.
  const icon = () => k() === 'project' && code()
    ? <span class="ec-ico"><ProjectTile code={code()!} color={projectIdentity(code()!)?.color} pattern={projectIdentity(code()!)?.pattern} size="xs" /></span>
    : <span class={['ec-ico', st() ? `t-${st()!.tone}` : 't-neutral']}><Icon name={entityKinds[k()!].icon} /></span>;
  return (
    <Show when={ref()} fallback={
      <Show when={!k0() && p.id}>
        {/* A bare id (agent text) never shows itself (OR-38): a placeholder while its kind loads, then Copy id when it names no visible entity. */}
        <Show when={!holder() || holder()!.read() === null} fallback={<span class="ec-pending muted" aria-busy="true">…</span>}>
          <CopyButton label="Copy id" value={p.id!} iconOnly />
        </Show>
      </Show>
    }>
      {(() => {
        const inner = () => {
          switch (form()) {
            case 'code': return code() ?? <>{icon()}{p.label && what()}</>;
            case 'full': return (
              <>
                {icon()}
                <Show when={code()}><span class="ec-cd">{code()}</span></Show>
                <Show when={(title() ?? what()) !== code()}><span class="ec-title">{title() ?? what()}</span></Show>
                <Show when={st()} keyed>{s => <span class={['badge', `t-${s.tone}`]}>{s.label}</span>}</Show>
              </>
            );
            default: return <>{icon()}{code() ? <span class="ec-cd">{code()}</span> : <span class="ec-word">{what()}</span>}</>;
          }
        };
        const trigger = (
          <button
            ref={node => { el = node; }}
            type="button"
            class={[
              'ec-code', `ec-${form()}`,
              form() === 'code' ? (code() ? 'id' : p.label ? 'btn quiet ec-label' : 'btn quiet icon-only') : '',
              flash() ? `ec-flash t-${flash()}` : '', p.cls,
            ]}
            data-ui="entity-code"
            data-state={st()?.state}
            aria-haspopup="dialog"
            aria-expanded={open() ? 'true' : 'false'}
            aria-controls={open() ? CARD_ID : undefined}
            aria-label={name()}
            onClick={toggle}
          >
            {inner()}
          </button>
        );
        return (
          <Show when={code() && form() !== 'full'} fallback={trigger}>
            <span class={p.cls === 'inline' ? 'ec-reference inline' : 'ec-reference'}>
              {trigger}
              <span class="ec-copy" onClick={e => e.stopPropagation()}>
                <CopyButton label={`Copy ${code()}`} value={code()!} iconOnly />
              </span>
            </span>
          </Show>
        );
      })()}
    </Show>
  );
}

/** D17 codes, pull requests (a GitHub URL, owner/repo#n) and UUIDs in a text as references (agent messages, OR-38,
 *  OR-44); uuidOnly (inline code) links UUIDs alone. Every other part stays text. */
export function linkCodes(text: string, _key: number, literal = (value: string, _start: number): JSX.Element => value, uuidOnly = false): JSX.Element {
  const out: JSX.Element[] = []; let last = 0;
  const found = [
    ...[...(uuidOnly ? [] : text.matchAll(ENTITY_CODE))].map(m => ({ at: m.index!, end: m.index! + m[0].length, el: () => <EntityCode code={m[0]} cls="inline" /> })),
    ...[...text.matchAll(APP_URL_REF)].map(m => ({ at: m.index!, end: m.index! + m[0].length, el: () => m[1] ? <EntityCode id={m[1].toLowerCase()} cls="inline" /> : <EntityCode code={m[2]} cls="inline" /> })),
    ...[...text.matchAll(UUID_REF)].map(m => ({ at: m.index!, end: m.index! + m[0].length, el: () => <EntityCode id={m[0].toLowerCase()} cls="inline" /> })),
    ...[...(uuidOnly ? [] : text.matchAll(HASH_REF))].map(m => ({
      at: m.index!,
      end: m.index! + m[0].length,
      el: () => <CopyButton label={m[0].length === 64 ? 'digest' : 'commit'} tip={`Copy ${m[0].length === 64 ? 'SHA-256' : 'commit'}: ${m[0]}`} value={m[0]} cls="ec-hash inline" />,
    })),
    ...[...(uuidOnly ? [] : text.matchAll(PR_REF))].map(m => {
      const owner = m[1] ?? m[4], repo = m[2] ?? m[5], n = m[3] ?? m[6];
      return { at: m.index!, end: m.index! + m[0].length, el: () => <EntityCode kind="pull_request" id={`${owner}/${repo}#${n}`} code={'#' + n} cls="inline" /> };
    }),
  ].sort((a, b) => a.at - b.at);
  for (const f of found) {
    if (f.at < last) continue;
    if (f.at > last) out.push(literal(text.slice(last, f.at), last));
    out.push(f.el());
    last = f.end;
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
      data-ui="entity-card" role="dialog"
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
            <Show when={data.preview && source?.src ? data.preview : null} keyed>
              {pv => (
                <a class="ec-pv" href={data.path ?? undefined} onClick={go} aria-label={`Open ${data.title}`}>
                  <img src={source!.src!(pv.resultId)} alt={data.title} loading="lazy" decoding="async" />
                  <Show when={pv.view === 'video'}><Icon name="play" /></Show>
                </a>
              )}
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
