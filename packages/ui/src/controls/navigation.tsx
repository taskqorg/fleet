// Navigation rail, global project focus control, and Spotlight search palette.
import { Fragment, type ComponentChildren } from 'preact';
import { useEffect, useId, useLayoutEffect, useRef, useState } from 'preact/hooks';
import { type IconName, type Family, type StateDef, type Tone, states, actions } from '../registry.ts';
import { matchSearch, searchFields } from '../search.ts';
import { isSubmitEnter } from '../keys.ts';
import { Icon, Button, StateBadge, Ago, SearchBox, useDismiss, focusOnMount } from './primitives.tsx';

/* ---------- Focus: one project multi-select for the whole app, with presets and ignore (OR-34, controls/focus-control.md) ---------- */

export type FocusProject = { id: string; name: string };
export type FocusPreset = { name: string; projects: string[] };
const sameSet = (a: string[], b: string[]) => a.length === b.length && a.every(x => b.includes(x));

/** The focus presets as one-click chips: "All projects" (the system preset, an empty focus) and the saved ones. The
 *  empty Spotlight palette shows them. Unlike the focus chip panel, which keeps a preset of only ignored projects disabled
 *  so it can be removed, the palette offers only presets it can apply. Children follow on the same line. */
export function FocusPresets({ value, presets, ignored = [], onChange, onRemove, icon, children }: {
  value: string[]; presets: FocusPreset[]; onChange: (ids: string[]) => void; onRemove?: (name: string) => void;
  /** Ignored projects (OR-34): left out of each preset; a preset of only ignored projects is not offered. */ ignored?: string[];
  /** Leads with the focus icon, where nothing else says what the chips are. */ icon?: boolean; children?: ComponentChildren;
}) {
  return <div class="presets" role="group" aria-label="Focus presets" data-ui="focus-presets">
    {icon && <span class="presets-h" data-tip="Focus"><Icon name="focus" label="Focus" /></span>}
    <span class={`preset${value.length === 0 ? ' on' : ''}`}><button type="button" aria-pressed={value.length === 0} onClick={() => onChange([])}>All projects</button></span>
    {presets.map(p => ({ ...p, projects: p.projects.filter(x => !ignored.includes(x)) })).filter(p => p.projects.length).map(p => <span key={p.name} class={`preset${sameSet(p.projects, value) && value.length ? ' on' : ''}`}>
      <button type="button" aria-pressed={!!value.length && sameSet(p.projects, value)} onClick={() => onChange(p.projects)}>{p.name}</button>
      {onRemove && <button type="button" class="x" aria-label={`Remove focus preset ${p.name}`} data-tip="Remove" onClick={() => onRemove(p.name)}><Icon name="x" /></button>}</span>)}
    {children}
  </div>;
}
/**
 * The focus chip in the shell header (OR-43): the focused project names (two, then +N), toned when a focus is set, and a
 * panel anchored under it (an overlay that moves nothing, an OR-14 exception like Spotlight). The panel holds the presets
 * (system "All projects" and saved ones), the project list with an ignore action per row, and a folded "Ignored (n)"
 * section to restore them. Ignored projects are never offered for the focus. `outside` is the count of items that need the
 * person in projects outside the focus that are not ignored; it shows only when above 0.
 */
export function FocusControl({ projects, value, presets, ignored = [], outside, onChange, onSavePreset, onRemovePreset, onIgnore }: {
  projects: FocusProject[]; value: string[]; presets: { name: string; projects: string[] }[]; ignored?: string[];
  outside?: { n: number; onOpen: () => void };
  onChange: (ids: string[]) => void; onSavePreset: (name: string) => void; onRemovePreset: (name: string) => void; onIgnore?: (id: string, ignored: boolean) => void;
}) {
  const [open, setOpen] = useState(false), [q, setQ] = useState(''), [naming, setNaming] = useState<string | null>(null), [showIgnored, setShowIgnored] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(open, ref, () => { setOpen(false); setNaming(null); }, '.focus-btn');
  // '-' is work outside projects; it is listed when the focus or a preset holds it.
  const name = (id: string) => id === '-' ? 'No project' : projects.find(p => p.id === id)?.name ?? id;
  if (value.includes('-') || presets.some(p => p.projects.includes('-'))) projects = [{ id: '-', name: 'No project' }, ...projects.filter(p => p.id !== '-')];
  const offered = projects.filter(p => !ignored.includes(p.id)), hidden = ignored.map(id => ({ id, name: name(id) }));
  const names = value.slice(0, 2).map(name), more = value.length - names.length;
  const label = value.length ? names.join(', ') : 'All projects', summary = label + (more > 0 ? ` and ${more} more` : '');
  const same = (a: string[], b: string[]) => { const x = a.filter(p => !ignored.includes(p)); return x.length === b.length && x.every(p => b.includes(p)); };
  const shown = offered.filter(p => `${p.name} ${p.id}`.toLowerCase().includes(q.toLowerCase()));
  const toggle = (id: string) => onChange(value.includes(id) ? value.filter(x => x !== id) : [...value, id]);
  const save = () => { if (naming?.trim()) onSavePreset(naming.trim()); setNaming(null); };
  return <div class={`focus-ctl${open ? ' open' : ''}${value.length ? ' set' : ''}`} data-ui="focus-control" ref={ref}>
    {outside && outside.n > 0 && <button type="button" class="focus-outside t-attention" aria-label={`${outside.n} need you outside focus: show every project`} data-tip="Show every project" onClick={() => { setOpen(false); outside.onOpen(); }}>
      <Icon name="bell" /><span class="num">{outside.n}</span><span class="nm">need you outside focus</span></button>}
    <button type="button" class="focus-btn" aria-expanded={open} aria-label={`Focus: ${summary}`} onClick={() => setOpen(!open)}>
      <span class="focus-ic" data-tip={`Focus: ${summary}`} data-tip-narrow><Icon name="focus" /></span>
      <span class="nm">{label}</span>{more > 0 && <span class="more">+{more}</span>}<Icon name="chevron-down" /></button>
    {open && <div class="focus-panel" role="group" aria-label="Focus projects">
      <div class="presets">
        <span class={`preset${value.length === 0 ? ' on' : ''}`}><button type="button" aria-pressed={value.length === 0} onClick={() => onChange([])}>All projects</button></span>
        {presets.map(p => { const live = p.projects.filter(x => !ignored.includes(x)); return <span key={p.name} class={`preset${value.length && same(p.projects, value) ? ' on' : ''}`}>
          {/* A preset whose projects are all ignored would clear the focus: it cannot be picked until one is restored. */}
          <button type="button" aria-pressed={!!value.length && same(p.projects, value)} aria-disabled={!live.length || undefined} data-tip={live.length ? undefined : 'Every project in it is ignored'}
            onClick={() => { if (live.length) onChange(live); }}>{p.name}</button>
          <button type="button" class="x" aria-label={`Remove focus preset ${p.name}`} data-tip="Remove" onClick={() => onRemovePreset(p.name)}><Icon name="x" /></button></span>; })}
        {value.length > 0 && (naming === null
          ? <Button label="Save focus preset" icon="save" iconOnly kind="quiet" onClick={() => setNaming('')} />
          : <span class="naming"><input value={naming} placeholder="Preset name" aria-label="Preset name" ref={focusOnMount} onInput={e => setNaming((e.target as HTMLInputElement).value)}
              onKeyDown={e => { if (isSubmitEnter(e)) save(); if (e.key === 'Escape') setNaming(null); }} />
              <Button label="Save" kind="primary" onClick={save} /></span>)}
      </div>
      {offered.length > 8 && <SearchBox label="Find project" value={q} onInput={setQ} always />}
      <ul class="focus-list" aria-label="Projects">{shown.map(p => { const only = value.length === 1 && value[0] === p.id; return <li key={p.id}><label><input type="checkbox" class="row-check" checked={value.includes(p.id)} onChange={() => toggle(p.id)} />
        <span class="nm">{p.name}</span></label>
        {onIgnore && p.id !== '-' && <button type="button" class="focus-act" aria-label={`Ignore ${p.name}`} aria-disabled={only || undefined}
          data-tip={only ? 'The only focused project: focus another first' : 'Ignore: hide everywhere'} onClick={() => { if (!only) onIgnore(p.id, true); }}><Icon name="circle-slash" /></button>}</li>; })}</ul>
      {hidden.length > 0 && <div class="focus-ignored">
        <button type="button" class="group-h" aria-expanded={showIgnored} onClick={() => setShowIgnored(!showIgnored)}>
          <span class={showIgnored ? 'chev open' : 'chev'}><Icon name="chevron-down" /></span><Icon name="circle-slash" /><b>Ignored</b><span class="num">{hidden.length}</span></button>
        {showIgnored && <ul class="focus-list" aria-label="Ignored projects">{hidden.map(p => <li key={p.id}><span class="nm muted">{p.name}</span>
          {onIgnore && <button type="button" class="focus-act" aria-label={`Restore ${p.name}`} data-tip="Restore" onClick={() => onIgnore(p.id, false)}><Icon name="undo-2" /></button>}</li>)}</ul>}
      </div>}
    </div>}
  </div>;
}

/* ---------- Navigation rail with pinned sections (nav-rail.md) ---------- */

/**
 * A section of the rail. count: its counter (nav-rail.md, Counters): n in the person's focus (every item without one), and
 * outside, the rest outside the focus (undefined without a focus); label names what is counted ("running").
 */
export type NavItem = { id: string; label: string; icon: IconName; group: string; count?: NavCount };
export type NavCount = { n: number; outside?: number; tone: Tone; label: string };
/** The counter after a section name: `12·30` (in focus bright, outside dim), `·30` with none in focus, `12` without a focus; nothing at zero. */
function NavCounter({ c }: { c: NavCount }) {
  const out = c.outside ?? 0;
  if (c.n <= 0 && out <= 0) return null;
  const words = c.outside === undefined ? `${c.n} ${c.label}` : `${c.n > 0 ? c.n : 'none'} ${c.label} in focus, ${out} outside`;
  return <span class={`nav-count t-${c.tone}${c.n > 0 ? '' : ' out-only'}`} aria-label={words}>
    {c.n > 0 && <span class="nav-in">{c.n.toLocaleString('en')}</span>}{out > 0 && <span class="nav-out">·{out.toLocaleString('en')}</span>}</span>;
}
export function NavRail({ items, pinned: stored, current, onPins, onGo, mini, head, foot }: {
  items: NavItem[]; current: string; onGo: (id: string) => void;
  /** The person's pinned ids in their order; onPins gets the whole new list (pin, unpin, reorder). */ pinned: string[]; onPins: (ids: string[]) => void;
  /** Collapsed to icons. */ mini?: boolean; /** Product name and need-you chip (app shell). */ head?: ComponentChildren; /** The current user block, at the bottom. */ foot?: ComponentChildren;
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
        {it.count && <NavCounter c={it.count} />}
      </a>
      <button type="button" class={`nav-pin${isPinned ? ' on' : ''}`} aria-pressed={isPinned} aria-label={`${isPinned ? 'Unpin' : 'Pin'} ${it.label}`} data-tip={isPinned ? 'Unpin' : 'Pin'} onClick={() => toggle(it.id, inPinned)}>
        <Icon name="pin" /></button>
      {menu === `${inPinned ? 'p' : 'g'}:${it.id}` && <span class="menu rail-menu" role="menu" aria-label={it.label}>
        <button type="button" role="menuitem" ref={focusOnMount} onClick={act(() => toggle(it.id, inPinned))}><Icon name={i < 0 ? 'pin' : 'pin-off'} />{i < 0 ? 'Pin' : 'Unpin'}</button>
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
      {groups.map(g => <ul key={g} class="rail-g" aria-label={g}><li class="rail-h">{g}</li>{items.filter(i => i.group === g).map(i => item(i, false))}</ul>)}
      {foot && <div class="rail-foot">{foot}</div>}
    </nav>
  );
}

/* ---------- Spotlight palette (OR-32, controls/spotlight.md) ---------- */

/**
 * One found item: run opens it. code is the human entity code (D17), shown mono after the label. state is the item's
 * registry state, shown instead of the kind icon where the kind has states; at is its last change (OR-2). outside: not in
 * the person's project focus (OR-34), listed after the focused results. action: a row button (also Alt+Enter) that keeps
 * the palette open. more: the "All N in <Section>" row that opens the section's list with the query.
 */
export type SpotlightItem = {
  key: string; icon: IconName; label: string; code?: string; sub?: string; run: () => void;
  /** The matching text where the label does not show it (a conversation found by its message): one muted line under the label. */ snippet?: string;
  state?: { family: Family; state: string }; at?: number; outside?: boolean; more?: boolean;
  action?: { label: string; icon: IconName; pressed?: boolean; run: () => void };
};
/** One kind of thing the palette finds. A new kind is one more provider; providers answer in their order. */
export type SpotlightProvider = { id: string; label: string; find: (q: string, signal: AbortSignal) => SpotlightItem[] | Promise<SpotlightItem[]> };
type SpotGroup = { id: string; label: string; items: SpotlightItem[]; out?: boolean };
/** What the palette keeps while it is closed: the query, its results, the highlighted row and the recently opened items.
 *  The shell holds one per tab and access generation, in memory only. */
export type SpotlightMemory = { q: string; found: SpotGroup[]; key?: string; recent: SpotlightItem[] };
export const spotlightMemory = (): SpotlightMemory => ({ q: '', found: [], recent: [] });
const RECENT = 6;

// The focused results of every kind first, then the ones outside the focus under one heading.
const splitFocus = (found: SpotGroup[]): SpotGroup[] => [
  ...found.map(g => ({ ...g, items: g.items.filter(i => !i.outside) })),
  ...found.map(g => ({ ...g, id: 'out:' + g.id, out: true, items: g.items.filter(i => i.outside) })),
].filter(g => g.items.length);

/** The palette: the only floating working tool (OR-32). Arrow keys move, Enter opens, Alt+Enter runs the row's action,
 *  Escape or a click outside closes. With an empty query it shows idle (the focus presets), recent items and the terms. */
export function Spotlight({ providers, onClose, memory, idle, terms = searchFields }: {
  providers: SpotlightProvider[]; onClose: () => void;
  /** Kept between openings; without it the palette opens empty. */ memory?: SpotlightMemory;
  /** Shown while the query is empty, above the recent items. */ idle?: ComponentChildren;
  /** Field terms suggested while the query is empty; a click adds one to the query. */ terms?: readonly string[];
}) {
  const mem = useRef(memory ?? spotlightMemory()).current;
  const [q, setQ] = useState(mem.q);
  const [found, setFound] = useState<SpotGroup[]>(mem.found);
  const shownOf = (q: string, found: SpotGroup[]) => q.trim() ? splitFocus(found) : mem.recent.length ? [{ id: 'recent', label: 'Recent', items: mem.recent }] : [];
  const [at, setAt] = useState(() => Math.max(0, shownOf(mem.q, mem.found).flatMap(g => g.items).findIndex(i => i.key === mem.key)));
  // keep: the highlighted row's key, found again when results change (a reopened palette, an action); typing clears it.
  // typed: the query changed by typing, so the old rows go; a reopen or a refresh keeps them until their provider answers.
  const keep = useRef(mem.key), typed = useRef(false), [refresh, setRefresh] = useState(0);
  // waiting: providers not answered yet (a kept query counts as asked from the start, so "Nothing found" never flickers); failed: one of them failed, so "nothing found" would be a guess.
  const [waiting, setWaiting] = useState(mem.q.trim() ? 1 : 0), [failed, setFailed] = useState(false);
  // asked: the query the providers were last asked; until it is the field's query, results belong to an older one.
  const [asked, setAsked] = useState(mem.q);
  // Focus returns to where it was only when the palette closes without opening an item.
  const back = useRef<Element | null>(document.activeElement), opened = useRef(false);
  // The field has focus with its text selected when the palette opens: typing replaces the query, arrows browse the old
  // results. It keeps focus while open (aria-modal): a page that focuses its own element as it loads behind the palette
  // gives it back. Declared before the focus return, so the trap is gone when focus goes back.
  const field = useRef<HTMLInputElement | null>(null), box = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => { // a layout effect: attached in the same commit as the field's focus, so no focus slips in between
    const trap = (e: FocusEvent) => { if (!box.current?.contains(e.target as Node)) field.current?.focus(); };
    document.addEventListener('focusin', trap); return () => document.removeEventListener('focusin', trap);
  }, []);
  useEffect(() => () => { if (!opened.current) (back.current as HTMLElement | null)?.focus?.(); }, []);
  const bind = useRef((el: HTMLInputElement | null) => { field.current = el; focusOnMount(el); el?.select(); }).current;
  useEffect(() => {
    // Each provider's results show as they arrive, in provider order; local ones are not held back by the server.
    const c = new AbortController(), text = q.trim(), got: (SpotlightItem[] | undefined)[] = providers.map(() => undefined);
    const restore = !typed.current, before = found;
    typed.current = false;
    const show = () => {
      if (c.signal.aborted) return;
      const next = providers.map((p, i) => ({ id: p.id, label: p.label, items: got[i] ?? (restore ? before.find(g => g.id === p.id)?.items ?? [] : []) })).filter(g => g.items.length);
      setFound(next);
      const i = keep.current ? splitFocus(next).flatMap(g => g.items).findIndex(x => x.key === keep.current) : -1;
      if (i >= 0) setAt(i);
      else if (keep.current) { keep.current = undefined; setAt(0); } // the kept row is gone: the first row, never a neighbour by position
    };
    setFailed(false);
    if (!restore) setAt(0);
    setAsked(q);
    if (!text) { setFound([]); setWaiting(0); return; }
    let left = 0;
    providers.forEach((p, i) => {
      const r = p.find(text, c.signal);
      if (Array.isArray(r)) { got[i] = r; return; }
      left++;
      r.then(x => { got[i] = x; }, () => { got[i] = []; if (!c.signal.aborted) setFailed(true); }).then(() => { if (!c.signal.aborted) { setWaiting(--left); show(); } });
    });
    setWaiting(left); show();
    return () => c.abort();
  }, [q, refresh]);
  const groups = shownOf(q, found), flat = groups.flatMap(g => g.items), cur = Math.min(at, flat.length - 1);
  // Kept for the next opening: results only of the query they answer (before its providers are asked, they are an older one's).
  mem.q = q; mem.found = asked === q ? found : []; mem.key = asked === q ? flat[cur]?.key : undefined;
  const move = (k: number) => { setAt(k); keep.current = flat[k]?.key; };
  const open = (i: SpotlightItem | undefined) => {
    if (!i) return;
    // A recent item is a way back, not a live row: its state and age at the time it was opened would be stale.
    if (!i.more) mem.recent = [{ ...i, outside: false, action: undefined, state: undefined, at: undefined, snippet: undefined }, ...mem.recent.filter(x => x.key !== i.key)].slice(0, RECENT);
    opened.current = true; onClose(); i.run();
  };
  // An action changes what the results show (a project added to the focus): they are asked again, the row stays highlighted.
  const act = (i: SpotlightItem) => { i.action?.run(); keep.current = i.key; setRefresh(n => n + 1); field.current?.focus(); };
  // An unchanged value asks nothing (no effect runs), so it marks nothing as typed.
  // Compared with the last input, not the render's q: two inputs can arrive before one render.
  const input = useRef(mem.q);
  const type = (v: string) => { if (v === input.current) return; input.current = v; typed.current = true; keep.current = undefined; setEnter(false); setQ(v); };
  const addTerm = (t: string) => { type(q.trim() ? `${q.trimEnd()} ${t}:` : `${t}:`); field.current?.focus(); };
  // Enter before every provider answered opens the first result once they have, not a section that happened to answer
  // first, and not a result of the query before the last keys (the providers are asked after the next paint).
  const [enter, setEnter] = useState(false);
  useEffect(() => { if (enter && !waiting && asked === q) { setEnter(false); open(flat[cur]); } }, [enter, waiting, asked]);
  const key = (e: KeyboardEvent) => {
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); onClose(); }
    else if (e.key === 'Tab') e.preventDefault(); // aria-modal: focus stays on the field
    else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); if (flat.length) move((cur + (e.key === 'ArrowDown' ? 1 : -1) + flat.length) % flat.length); }
    else if (e.key === 'Enter' && e.altKey && !e.isComposing && flat[cur]?.action) { e.preventDefault(); act(flat[cur]); }
    else if (e.key === 'Enter' && !e.isComposing && e.keyCode !== 229) { // 229: Safari's Enter that ends an IME composition
      e.preventDefault();
      // A key typed just before Enter may not have rendered yet: the field's own text tells, and the press waits for it.
      const typedNow = e.target instanceof HTMLInputElement ? e.target.value : q;
      if (waiting || asked !== q || typedNow !== q) setEnter(true); else open(flat[cur]);
    }
  };
  const list = useId(), text = q.trim(), now = Date.now();
  let n = -1;
  const row = (i: SpotlightItem) => {
    const k = ++n, st = i.state && (states[i.state.family] as Record<string, StateDef>)[i.state.state] ? i.state : undefined;
    return <li key={i.key} id={`${list}-${k}`} role="option" aria-selected={k === cur} aria-keyshortcuts={i.action ? "Alt+Enter" : undefined} aria-description={i.action ? `Alt+Enter: ${i.action.label}` : undefined} class={[k === cur && 'cur', i.outside && 'out', i.more && 'more', i.snippet && 'snip'].filter(Boolean).join(' ')}
      onPointerMove={() => k !== cur && move(k)} onClick={() => open(i)}>
      {st ? <StateBadge family={st.family} state={st.state as never} compact /> : <Icon name={i.icon} />}
      {i.snippet ? <span class="txt"><span class="title">{i.label}</span><span class="snippet">{i.snippet}</span></span> : <span class="title">{i.label}</span>}{i.code && <span class="id">{i.code}</span>}<span class="grow" />{i.sub && <span class="sub">{i.sub}</span>}
      {i.at !== undefined && <Ago t={i.at} />}
      {i.action && <button type="button" class="btn quiet icon-only" tabIndex={-1} aria-pressed={i.action.pressed} aria-label={i.action.label} data-tip={`${i.action.label}  ⌥⏎`}
        onPointerDown={e => e.preventDefault()} onClick={e => { e.stopPropagation(); act(i); }}><Icon name={i.action.icon} /></button>}
      {i.more && <Icon name="arrow-right" />}
    </li>;
  };
  return <div class="spot-back" onPointerDown={e => { if (e.target === e.currentTarget) onClose(); }}>
    <div class="spotlight" data-ui="spotlight" ref={box} role="dialog" aria-modal="true" aria-label="Search" onKeyDown={key}>
      <label class="search"><Icon name={actions.search} /><input type="search" ref={bind} placeholder="Code, task, conversation, result, project, agent · agent:claude host:mac" aria-label="Search"
        role="combobox" list={undefined} aria-expanded={flat.length > 0} aria-controls={list} aria-activedescendant={flat[cur] ? `${list}-${cur}` : undefined}
        value={q} onInput={e => type((e.target as HTMLInputElement).value)} /></label>
      {!text && idle && <div class="spot-idle" onClick={() => field.current?.focus()}>{idle}</div>}
      {flat.length > 0 && <ul class="spot-list" id={list} role="listbox" aria-label={text ? 'Results' : 'Recent'}>{groups.map((g, gi) => <Fragment key={g.id}>
        {g.out && !groups[gi - 1]?.out && <li class="rail-h spot-out" role="presentation"><Icon name="focus" />Outside focus</li>}
        <li class="rail-h" role="presentation">{g.label}</li>
        {g.items.map(row)}
      </Fragment>)}</ul>}
      {!text && terms.length > 0 && <p class="spot-terms muted">{terms.map(t => <button key={t} type="button" tabIndex={-1} class="id" onPointerDown={e => e.preventDefault()} onClick={() => addTerm(t)}>{t}:</button>)}<span>narrow the search</span></p>}
      {text && !waiting && asked === q && (failed || !flat.length) && <p class="muted spot-none" role="status">{failed ? flat.length ? 'Some kinds failed; try again' : 'Search failed; try again' : 'Nothing found'}</p>}
    </div>
  </div>;
}

/** Sections as a palette provider: the navigation items whose label holds the text. A field term (agent:x) finds none. */
export const sectionProvider = (items: NavItem[], onGo: (id: string) => void): SpotlightProvider => ({
  id: 'sections', label: 'Sections',
  find: q => items.filter(i => matchSearch(q, i.label)).map(i => ({ key: 's:' + i.id, icon: i.icon, label: i.label, run: () => onGo(i.id) })),
});
