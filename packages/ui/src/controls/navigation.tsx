// Navigation rail, global project focus control, and Spotlight search palette.
import { createEffect, createMemo, createSignal, createUniqueId, For, onSettled, Show, untrack } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { Input, Popover, PopoverContent, PopoverTrigger } from '@ui-sdk/solid';
import { type IconName, type Family, type StateDef, type Tone, states, actions } from '../registry.ts';
import { matchSearch, searchFields } from '../search.ts';
import { isSubmitEnter } from '../keys.ts';
import { Icon, Button, StateBadge, Ago, SearchBox, focusOnMount } from './primitives.tsx';
import { useReorder } from './reorder.tsx';

/* ---------- Focus: one project multi-select for the whole app, with presets and ignore (OR-34, controls/focus-control.md) ---------- */

export type FocusProject = { id: string; name: string };
export type FocusPreset = { name: string; projects: string[] };
const sameSet = (a: string[], b: string[]) => a.length === b.length && a.every(x => b.includes(x));

/** The focus presets as one-click chips: "All projects" (the system preset, an empty focus) and the saved ones. The
 *  empty Spotlight palette shows them. Unlike the focus chip panel, which keeps a preset of only ignored projects disabled
 *  so it can be removed, the palette offers only presets it can apply. Children follow on the same line. */
export function FocusPresets(p: {
  value: string[]; presets: FocusPreset[]; onChange: (ids: string[]) => void; onRemove?: (name: string) => void;
  /** Ignored projects (OR-34): left out of each preset; a preset of only ignored projects is not offered. */ ignored?: string[];
  /** Leads with the focus icon, where nothing else says what the chips are. */ icon?: boolean; children?: JSX.Element;
}) {
  const ignored = createMemo(() => p.ignored ?? []);
  const livePresets = createMemo(() =>
    p.presets
      .map(pr => ({ ...pr, projects: pr.projects.filter(x => !ignored().includes(x)) }))
      .filter(pr => pr.projects.length > 0),
  );
  return (
    <div class="presets" role="group" aria-label="Focus presets" data-ui="focus-presets">
      <Show when={p.icon}><span class="presets-h" data-tip="Focus"><Icon name="focus" label="Focus" /></span></Show>
      <span class={['preset', { on: p.value.length === 0 }]}>
        <button type="button" aria-pressed={p.value.length === 0 ? 'true' : 'false'} onClick={() => p.onChange([])}>All projects</button>
      </span>
      <For each={livePresets()}>
        {pr => {
          const active = createMemo(() => p.value.length > 0 && sameSet(pr.projects, p.value));
          return (
            <span class={['preset', { on: active() }]}>
              <button type="button" aria-pressed={active() ? 'true' : 'false'} onClick={() => p.onChange(pr.projects)}>{pr.name}</button>
              <Show when={p.onRemove} keyed>
                {rem => (
                  <button type="button" class="x" aria-label={`Remove focus preset ${pr.name}`} data-tip="Remove" onClick={() => rem(pr.name)}>
                    <Icon name="x" />
                  </button>
                )}
              </Show>
            </span>
          );
        }}
      </For>
      {p.children}
    </div>
  );
}

/**
 * The focus chip in the shell header (OR-43): the focused project names (two, then +N), toned when a focus is set, and a
 * panel anchored under it (an overlay that moves nothing, an OR-14 exception like Spotlight). The panel holds the presets
 * (system "All projects" and saved ones), the project list with an ignore action per row, and a folded "Ignored (n)"
 * section to restore them. Ignored projects are never offered for the focus. `outside` is the count of items that need the
 * person in projects outside the focus that are not ignored; it shows only when above 0.
 */
export function FocusControl(p: {
  projects: FocusProject[]; value: string[]; presets: { name: string; projects: string[] }[]; ignored?: string[];
  outside?: { n: number; onOpen: () => void };
  onChange: (ids: string[]) => void; onSavePreset: (name: string) => void; onRemovePreset: (name: string) => void; onIgnore?: (id: string, ignored: boolean) => void;
}) {
  const [open, setOpen] = createSignal(false);
  const [q, setQ] = createSignal('');
  const [naming, setNaming] = createSignal<string | null>(null);
  const [showIgnored, setShowIgnored] = createSignal(false);
  let wrap: HTMLDivElement | undefined;

  const ignored = createMemo(() => p.ignored ?? []);
  // '-' is work outside projects; it is listed when the focus or a preset holds it.
  const projects = createMemo(() =>
    p.value.includes('-') || p.presets.some(pr => pr.projects.includes('-'))
      ? [{ id: '-', name: 'No project' }, ...p.projects.filter(pr => pr.id !== '-')]
      : p.projects,
  );
  const name = (id: string) => id === '-' ? 'No project' : projects().find(pr => pr.id === id)?.name ?? id;
  const offered = createMemo(() => projects().filter(pr => !ignored().includes(pr.id)));
  const hidden = createMemo(() => ignored().map(id => ({ id, name: name(id) })));
  const names = createMemo(() => p.value.slice(0, 2).map(name));
  const more = createMemo(() => p.value.length - names().length);
  const label = createMemo(() => p.value.length ? names().join(', ') : 'All projects');
  const summary = createMemo(() => label() + (more() > 0 ? ` and ${more()} more` : ''));
  const same = (a: string[], b: string[]) => {
    const x = a.filter(id => !ignored().includes(id));
    return x.length === b.length && x.every(id => b.includes(id));
  };
  const shown = createMemo(() => {
    const needle = q().toLowerCase();
    return offered().filter(pr => `${pr.name} ${pr.id}`.toLowerCase().includes(needle));
  });
  const toggle = (id: string) => p.onChange(p.value.includes(id) ? p.value.filter(x => x !== id) : [...p.value, id]);
  const save = () => {
    const v = naming()?.trim();
    if (v) p.onSavePreset(v);
    setNaming(null);
  };

  return (
    <div ref={el => { wrap = el; }} class={['focus-ctl', { open: open(), set: p.value.length > 0 }]} data-ui="focus-control">
      <Show when={p.outside && p.outside.n > 0}>
        <button
          type="button"
          class="focus-outside t-attention"
          aria-label={`${p.outside!.n} need you outside focus: show every project`}
          data-tip="Show every project"
          onClick={() => { setOpen(false); p.outside!.onOpen(); }}
        >
          <Icon name="bell" />
          <span class="num">{p.outside!.n}</span>
          <span class="nm">need you outside focus</span>
        </button>
      </Show>
      <Popover
        open={open()}
        onOpenChange={next => {
          setOpen(next);
          if (!next) setNaming(null);
        }}
        container={wrap}
      >
        <PopoverTrigger class="focus-btn" aria-label={`Focus: ${summary()}`}>
          <span class="focus-ic" data-tip={`Focus: ${summary()}`} data-tip-narrow><Icon name="focus" /></span>
          <span class="nm">{label()}</span>
          <Show when={more() > 0}><span class="more">+{more()}</span></Show>
          <Icon name="chevron-down" />
        </PopoverTrigger>
        <PopoverContent class="focus-panel" side="bottom" align="end" aria-label="Focus projects" render={cp => <div {...cp} role="group" aria-labelledby={undefined} />}>
          <div class="presets">
            <span class={['preset', { on: p.value.length === 0 }]}>
              <button type="button" aria-pressed={p.value.length === 0 ? 'true' : 'false'} onClick={() => p.onChange([])}>All projects</button>
            </span>
            <For each={p.presets}>
              {pr => {
                const live = createMemo(() => pr.projects.filter(x => !ignored().includes(x)));
                const on = createMemo(() => p.value.length > 0 && same(pr.projects, p.value));
                return (
                  <span class={['preset', { on: on() }]}>
                    {/* A preset whose projects are all ignored would clear the focus: it cannot be picked until one is restored. */}
                    <button
                      type="button"
                      aria-pressed={on() ? 'true' : 'false'}
                      aria-disabled={!live().length ? 'true' : undefined}
                      data-tip={live().length ? undefined : 'Every project in it is ignored'}
                      onClick={() => { if (live().length) p.onChange(live()); }}
                    >
                      {pr.name}
                    </button>
                    <button type="button" class="x" aria-label={`Remove focus preset ${pr.name}`} data-tip="Remove" onClick={() => p.onRemovePreset(pr.name)}>
                      <Icon name="x" />
                    </button>
                  </span>
                );
              }}
            </For>
            <Show when={p.value.length > 0}>
              <Show
                when={naming() !== null}
                fallback={<Button label="Save focus preset" icon="save" iconOnly kind="quiet" onClick={() => setNaming('')} />}
              >
                <span class="naming">
                  <Input
                    value={naming() ?? ''}
                    placeholder="Preset name"
                    aria-label="Preset name"
                    ref={focusOnMount}
                    onValueChange={v => setNaming(v)}
                    onKeyDown={e => {
                      if (isSubmitEnter(e)) save();
                      if (e.key === 'Escape') setNaming(null);
                    }}
                  />
                  <Button label="Save" kind="primary" onClick={save} />
                </span>
              </Show>
            </Show>
          </div>
          <Show when={offered().length > 8}>
            <SearchBox label="Find project" value={q()} onInput={setQ} always />
          </Show>
          <ul class="focus-list" aria-label="Projects">
            <For each={shown()}>
              {pr => {
                const only = createMemo(() => p.value.length === 1 && p.value[0] === pr.id);
                return (
                  <li>
                    <label>
                      <input type="checkbox" class="row-check" checked={p.value.includes(pr.id)} onChange={() => toggle(pr.id)} />
                      <span class="nm">{pr.name}</span>
                    </label>
                    <Show when={p.onIgnore && pr.id !== '-'}>
                      <button
                        type="button"
                        class="focus-act"
                        aria-label={`Ignore ${pr.name}`}
                        aria-disabled={only() ? 'true' : undefined}
                        data-tip={only() ? 'The only focused project: focus another first' : 'Ignore: hide everywhere'}
                        onClick={() => { if (!only()) p.onIgnore!(pr.id, true); }}
                      >
                        <Icon name="circle-slash" />
                      </button>
                    </Show>
                  </li>
                );
              }}
            </For>
          </ul>
          <Show when={hidden().length > 0}>
            <div class="focus-ignored">
              <button type="button" class="group-h" aria-expanded={showIgnored() ? 'true' : 'false'} onClick={() => setShowIgnored(v => !v)}>
                <span class={['chev', { open: showIgnored() }]}><Icon name="chevron-down" /></span>
                <Icon name="circle-slash" />
                <b>Ignored</b>
                <span class="num">{hidden().length}</span>
              </button>
              <Show when={showIgnored()}>
                <ul class="focus-list" aria-label="Ignored projects">
                  <For each={hidden()}>
                    {pr => (
                      <li>
                        <span class="nm muted">{pr.name}</span>
                        <Show when={p.onIgnore}>
                          <button type="button" class="focus-act" aria-label={`Restore ${pr.name}`} data-tip="Restore" onClick={() => p.onIgnore!(pr.id, false)}>
                            <Icon name="undo-2" />
                          </button>
                        </Show>
                      </li>
                    )}
                  </For>
                </ul>
              </Show>
            </div>
          </Show>
        </PopoverContent>
      </Popover>
    </div>
  );
}

/* ---------- Navigation rail with pinned sections (nav-rail.md) ---------- */

/**
 * A section of the rail. count: its counter (nav-rail.md, Counters): n in the person's focus (every item without one), and
 * outside, the rest outside the focus (undefined without a focus); label names what is counted ("running").
 */
/** A rail section. `group` puts it under a small heading; the app's seven sections have none (OR-46). */
export type NavItem = { id: string; label: string; icon: IconName; group?: string; count?: NavCount };
export type NavCount = { n: number; outside?: number; tone: Tone; label: string };
/** The counter after a section name: `12·30` (in focus bright, outside dim), `·30` with none in focus, `12` without a focus; nothing at zero. */
function NavCounter(p: { c: NavCount }) {
  const out = () => p.c.outside ?? 0;
  const words = () => p.c.outside === undefined ? `${p.c.n} ${p.c.label}` : `${p.c.n > 0 ? p.c.n : 'none'} ${p.c.label} in focus, ${out()} outside`;
  return (
    <Show when={p.c.n > 0 || out() > 0}>
      <span class={['nav-count', `t-${p.c.tone}`, { 'out-only': p.c.n <= 0 }]} aria-label={words()}>
        <Show when={p.c.n > 0}><span class="nav-in">{p.c.n.toLocaleString('en')}</span></Show>
        <Show when={out() > 0}><span class="nav-out">·{out().toLocaleString('en')}</span></Show>
      </span>
    </Show>
  );
}
export function NavRail(p: {
  items: NavItem[]; current: string; onGo: (id: string) => void;
  /** The person's pinned ids in their order; onPins gets the whole new list (pin, unpin, reorder). */ pinned: string[]; onPins: (ids: string[]) => void;
  /** Collapsed to icons. */ mini?: boolean; /** Product name and need-you chip (app shell). */ head?: JSX.Element; /** The current user block, at the bottom. */ foot?: JSX.Element;
  /** The Pinned zone above the user block (RailPinned), when the person turned it on. */ zone?: JSX.Element;
  /** The person's Pinned-zone switch, offered in every item's context menu (nav-rail.md, Pinned). */ pinnedZone?: { on: boolean; set: (on: boolean) => void };
}) {
  // Ids of sections that no longer exist are dropped here, so they never take a position and leave on the next save.
  const pinned = createMemo(() => p.pinned.filter(id => p.items.some(i => i.id === id)));
  const [menu, setMenu] = createSignal<string | null>(null);
  let navRef: HTMLElement | undefined;

  const focusItem = (id: string, inPinned: boolean) => requestAnimationFrame(() =>
    navRef?.querySelector<HTMLElement>(`.rail-g${inPinned ? '.pinned' : ':not(.pinned)'} [data-id="${id}"] a`)?.focus(),
  );

  createEffect(menu, cur => {
    if (!cur) return;
    const close = (e: Event) => {
      if (e instanceof KeyboardEvent) { if (e.key !== 'Escape') return; e.stopPropagation(); focusItem(cur.slice(2), cur[0] === 'p'); }
      else if ((e.target as Element).closest?.('.rail-menu')) return;
      setMenu(null);
    };
    document.addEventListener('pointerdown', close);
    document.addEventListener('keydown', close, true);
    return () => {
      document.removeEventListener('pointerdown', close);
      document.removeEventListener('keydown', close, true);
    };
  });

  // An item unpinned from the pinned group leaves it: focus follows to the same item in its group.
  const toggle = (id: string, inPinned = false) => {
    const cur = pinned();
    p.onPins(cur.includes(id) ? cur.filter(x => x !== id) : [...cur, id]);
    if (inPinned) focusItem(id, false);
  };
  // Pinned sections reorder by drag (reorder.tsx; owner 26 Nov: no Move up / Move down).
  let pinnedRef: HTMLUListElement | undefined;
  const reorder = useReorder({
    keys: pinned, onOrder: p.onPins, root: () => pinnedRef, label: id => p.items.find(i => i.id === id)?.label ?? id,
    focus: id => pinnedRef?.querySelector<HTMLElement>(`[data-id="${CSS.escape(id)}"] a`)?.focus({ preventScroll: true }),
  });
  const act = (f: () => void) => () => { setMenu(null); f(); };

  const renderItem = (it: NavItem, inPinned: boolean) => {
    const isPinned = createMemo(() => pinned().includes(it.id));
    const idx = createMemo(() => pinned().indexOf(it.id));
    const menuKey = () => `${inPinned ? 'p' : 'g'}:${it.id}`;
    return (
      <li
        data-id={it.id}
        class={['nav-i', { cur: it.id === p.current, pinned: isPinned() }]}
        {...(inPinned ? reorder.item(it.id) : {})}
        onContextMenu={e => { e.preventDefault(); setMenu(menuKey()); }}
      >
        <a
          href={`#/${it.id}`}
          aria-current={it.id === p.current ? 'page' : undefined}
          aria-roledescription={inPinned ? 'draggable item' : undefined}
          aria-description={inPinned ? 'Space to reorder' : undefined}
          onClick={e => { e.preventDefault(); p.onGo(it.id); }}
          onKeyDown={e => {
            if (e.key === 'p' && !e.altKey && !e.ctrlKey && !e.metaKey) { e.preventDefault(); toggle(it.id, inPinned); }
            else if (e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) { e.preventDefault(); setMenu(menuKey()); }
          }}
        >
          <span data-tip={it.label} data-tip-narrow class="nav-ic"><Icon name={it.icon} /></span>
          <span class="nm">{it.label}</span>
          <Show when={it.count} keyed>{c => <NavCounter c={c} />}</Show>
        </a>
        <button
          type="button"
          class={['nav-pin', { on: isPinned() }]}
          aria-pressed={isPinned() ? 'true' : 'false'}
          aria-label={`${isPinned() ? 'Unpin' : 'Pin'} ${it.label}`}
          data-tip={isPinned() ? 'Unpin' : 'Pin'}
          onClick={() => toggle(it.id, inPinned)}
        >
          <Icon name="pin" />
        </button>
        <Show when={menu() === menuKey()}>
          <span class="menu rail-menu" role="menu" aria-label={it.label}>
            <button type="button" role="menuitem" ref={focusOnMount} onClick={act(() => toggle(it.id, inPinned))}>
              <Icon name={idx() < 0 ? 'pin' : 'pin-off'} />{idx() < 0 ? 'Pin' : 'Unpin'}
            </button>
            <Show when={p.pinnedZone} keyed>
              {b => (
                <button type="button" role="menuitem" onClick={act(() => b.set(!b.on))}>
                  <Icon name={b.on ? 'pin-off' : 'pin'} />{b.on ? 'Hide pinned in navigation' : 'Show pinned in navigation'}
                </button>
              )}
            </Show>
          </span>
        </Show>
      </li>
    );
  };

  const groups = createMemo(() => [...new Set(p.items.map(i => i.group))]);
  const pins = createMemo(() => reorder.order().map(id => p.items.find(i => i.id === id)).filter((x): x is NavItem => !!x));

  return (
    <nav class={['rail', { mini: !!p.mini }]} data-ui="nav-rail" aria-label="Sections" ref={el => { navRef = el; }}>
      <Show when={p.head} keyed>{h => <div class="rail-head">{h}</div>}</Show>
      <Show when={pins().length > 0}>
        <ul class="rail-g pinned" aria-label="Pinned" ref={el => { pinnedRef = el; }}>
          <For each={pins()}>{i => renderItem(i, true)}</For>
        </ul>
        {reorder.view()}
      </Show>
      <For each={groups()}>
        {g => (
          <ul class="rail-g" aria-label={g ?? 'All sections'}>
            <Show when={g}><li class="rail-h">{g}</li></Show>
            <For each={p.items.filter(i => i.group === g)}>{i => renderItem(i, false)}</For>
          </ul>
        )}
      </For>
      {p.zone}
      <Show when={p.foot} keyed>{f => <div class="rail-foot">{f}</div>}</Show>
    </nav>
  );
}

/**
 * The phone navigation (OR-60): below 760 px a bottom bar of five replaces the rail. The first four sections are tabs, the
 * fifth tab, More, opens a short menu above it (OR-14) with the other sections and `foot` (the user block). The tab of the
 * current section, or More when the current one is in its menu, is marked. Counters show as the in-focus badge.
 */
export function BottomBar(p: {
  items: NavItem[]; current: string; onGo: (id: string) => void; foot?: JSX.Element;
  /** First in the More menu: the Pinned zone when it is on (RailPinnedStrip); done closes the menu. */ extra?: (done: () => void) => JSX.Element;
}) {
  const [open, setOpen] = createSignal(false);
  const tabs = createMemo(() => p.items.slice(0, 4));
  const more = createMemo(() => p.items.slice(4));
  const inMore = createMemo(() => more().some(i => i.id === p.current));
  let ref: HTMLElement | undefined;
  createEffect(open, on => {
    if (!on) return;
    const close = (e: Event) => {
      if (e instanceof KeyboardEvent) { if (e.key !== 'Escape') return; e.stopPropagation(); ref?.querySelector<HTMLElement>('.tab-more')?.focus(); }
      else if (ref?.contains(e.target as Node)) return;
      setOpen(false);
    };
    document.addEventListener('pointerdown', close);
    document.addEventListener('keydown', close, true);
    return () => { document.removeEventListener('pointerdown', close); document.removeEventListener('keydown', close, true); };
  });
  const go = (id: string) => { setOpen(false); p.onGo(id); };
  return (
    <nav class="bottom-bar" data-ui="bottom-bar" aria-label="Sections" ref={el => { ref = el; }}>
      <For each={tabs()}>
        {it => (
          <a
            class={['tab-i', { cur: it.id === p.current }]}
            href={`#/${it.id}`}
            aria-current={it.id === p.current ? 'page' : undefined}
            onClick={e => { e.preventDefault(); go(it.id); }}
          >
            <span class="tab-ic"><Icon name={it.icon} /><Show when={it.count && it.count.n > 0 ? it.count : null} keyed>{c => <span class={['nav-count', 'tab-count', `t-${c.tone}`]} aria-label={`${c.n} ${c.label}`}>{c.n.toLocaleString('en')}</span>}</Show></span>
            <span class="nm">{it.label}</span>
          </a>
        )}
      </For>
      <Show when={more().length > 0 || p.foot}>
        <button
          type="button"
          class={['tab-i', 'tab-more', { cur: inMore() }]}
          aria-haspopup="menu"
          aria-expanded={open() ? 'true' : 'false'}
          aria-current={inMore() ? 'page' : undefined}
          onClick={() => setOpen(v => !v)}
        >
          <span class="tab-ic"><Icon name={actions.more} /></span>
          <span class="nm">More</span>
        </button>
        <Show when={open()}>
          <div class="menu tab-menu" role="menu" aria-label="More sections">
            {p.extra?.(() => setOpen(false))}
            <For each={more()}>
              {(it, i) => (
                <button type="button" role="menuitem" class={{ cur: it.id === p.current }} aria-current={it.id === p.current ? 'page' : undefined} ref={el => { if (i() === 0 && !p.extra) focusOnMount(el); }} onClick={() => go(it.id)}>
                  <Icon name={it.icon} />{it.label}
                </button>
              )}
            </For>
            <Show when={p.foot}><div class="tab-foot">{p.foot}</div></Show>
          </div>
        </Show>
      </Show>
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
export function Spotlight(p: {
  providers: SpotlightProvider[]; onClose: () => void;
  /** Kept between openings; without it the palette opens empty. */ memory?: SpotlightMemory;
  /** Shown while the query is empty, above the recent items. */ idle?: JSX.Element;
  /** Field terms suggested while the query is empty; a click adds one to the query. */ terms?: readonly string[];
}) {
  const mem = untrack(() => p.memory) ?? spotlightMemory();
  const shownOf = (queryText: string, groups: SpotGroup[]) =>
    queryText.trim() ? splitFocus(groups) : mem.recent.length ? [{ id: 'recent', label: 'Recent', items: mem.recent }] : [];

  const [q, setQ] = createSignal(mem.q);
  const [found, setFound] = createSignal<SpotGroup[]>(mem.found, { ownedWrite: true });
  const [at, setAt] = createSignal(
    Math.max(0, shownOf(mem.q, mem.found).flatMap(g => g.items).findIndex(i => i.key === mem.key)),
    { ownedWrite: true },
  );
  // keep: the highlighted row's key, found again when results change (a reopened palette, an action); typing clears it.
  // typed: the query changed by typing, so the old rows go; a reopen or a refresh keeps them until their provider answers.
  let keep = mem.key;
  let typed = false;
  const [refresh, setRefresh] = createSignal(0);
  // waiting: providers not answered yet (a kept query counts as asked from the start, so "Nothing found" never flickers); failed: one of them failed, so "nothing found" would be a guess.
  const [waiting, setWaiting] = createSignal(mem.q.trim() ? 1 : 0, { ownedWrite: true });
  const [failed, setFailed] = createSignal(false, { ownedWrite: true });
  // asked: the query the providers were last asked; until it is the field's query, results belong to an older one.
  const [asked, setAsked] = createSignal(mem.q, { ownedWrite: true });
  const [enter, setEnter] = createSignal(false, { ownedWrite: true });

  // Focus returns to where it was only when the palette closes without opening an item.
  const back = document.activeElement;
  let opened = false;
  let closed = false;
  let field: HTMLInputElement | undefined;
  let dialogRef: HTMLDialogElement | undefined;
  let inputText = mem.q;

  // Guard against a late native `close` event from dialogRef.close() firing after a newer Spotlight opens while a lazy route transition holds unmount.
  const notifyClose = () => {
    if (closed) return;
    closed = true;
    p.onClose();
  };

  onSettled(() => {
    const d = dialogRef;
    if (!d) return;
    if (!d.open) d.showModal();
    field?.focus();
    field?.select();
    // Solid 2 commits the <input value={q()}> property effect after onSettled, which resets selection if non-empty.
    queueMicrotask(() => {
      if (d.open && field && !typed) {
        field.focus();
        field.select();
      }
    });
    const close = () => notifyClose();
    d.addEventListener('close', close);
    return () => {
      closed = true;
      d.removeEventListener('close', close);
      if (d.open) d.close();
      if (!opened) (back as HTMLElement | null)?.focus?.();
    };
  });

  createEffect(() => [q(), refresh()] as const, ([curQ]) => {
    // Each provider's results show as they arrive, in provider order; local ones are not held back by the server.
    const c = new AbortController(), text = curQ.trim();
    const providers = untrack(() => p.providers);
    const got: (SpotlightItem[] | undefined)[] = providers.map(() => undefined);
    const restore = !typed, before = untrack(found);
    typed = false;
    const show = () => {
      if (c.signal.aborted) return;
      const next = providers.map((pr, i) => ({
        id: pr.id,
        label: pr.label,
        items: got[i] ?? (restore ? before.find(g => g.id === pr.id)?.items ?? [] : []),
      })).filter(g => g.items.length);
      setFound(next);
      const i = keep ? splitFocus(next).flatMap(g => g.items).findIndex(x => x.key === keep) : -1;
      if (i >= 0) setAt(i);
      else if (keep) { keep = undefined; setAt(0); } // the kept row is gone: the first row, never a neighbour by position
    };
    setFailed(false);
    if (!restore) setAt(0);
    setAsked(curQ);
    if (!text) { setFound([]); setWaiting(0); return; }
    let left = 0;
    providers.forEach((pr, i) => {
      const r = pr.find(text, c.signal);
      if (Array.isArray(r)) { got[i] = r; return; }
      left++;
      r.then(x => { got[i] = x; }, () => { got[i] = []; if (!c.signal.aborted) setFailed(true); })
        .then(() => { if (!c.signal.aborted) { setWaiting(--left); show(); } });
    });
    setWaiting(left);
    show();
    return () => c.abort();
  });

  const groups = createMemo(() => shownOf(q(), found()));
  const flat = createMemo(() => groups().flatMap(g => g.items));
  const cur = createMemo(() => Math.min(at(), flat().length - 1));

  // Kept for the next opening: results only of the query they answer (before its providers are asked, they are an older one's).
  createEffect(
    () => [q(), asked(), found(), cur(), flat()] as const,
    ([curQ, curAsked, curFound, curIdx, curFlat]) => {
      mem.q = curQ;
      mem.found = curAsked === curQ ? curFound : [];
      mem.key = curAsked === curQ ? curFlat[curIdx]?.key : undefined;
    },
  );

  const move = (k: number) => {
    setAt(k);
    keep = flat()[k]?.key;
  };
  const open = (i: SpotlightItem | undefined) => {
    if (!i) return;
    // A recent item is a way back, not a live row: its state and age at the time it was opened would be stale.
    if (!i.more) mem.recent = [{ ...i, outside: false, action: undefined, state: undefined, at: undefined, snippet: undefined }, ...mem.recent.filter(x => x.key !== i.key)].slice(0, RECENT);
    opened = true;
    closed = true;
    if (dialogRef?.open) dialogRef.close();
    p.onClose();
    i.run();
  };
  // An action changes what the results show (a project added to the focus): they are asked again, the row stays highlighted.
  const act = (i: SpotlightItem) => {
    i.action?.run();
    keep = i.key;
    setRefresh(n => n + 1);
    field?.focus();
  };
  // An unchanged value asks nothing (no effect runs), so it marks nothing as typed.
  const type = (v: string) => {
    if (v === inputText) return;
    inputText = v;
    typed = true;
    keep = undefined;
    setEnter(false);
    setQ(v);
  };
  const addTerm = (t: string) => {
    const curQ = q();
    type(curQ.trim() ? `${curQ.trimEnd()} ${t}:` : `${t}:`);
    field?.focus();
  };

  createEffect(
    () => [enter(), waiting(), asked(), q()] as const,
    ([isEnter, isWait, curAsked, curQ]) => {
      if (isEnter && !isWait && curAsked === curQ) {
        setEnter(false);
        open(flat()[cur()]);
      }
    },
  );

  const key = (e: KeyboardEvent) => {
    const items = flat(), idx = cur();
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); notifyClose(); }
    else if (e.key === 'Tab') e.preventDefault(); // aria-modal: focus stays on the field
    else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); if (items.length) move((idx + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length); }
    else if (e.key === 'Enter' && e.altKey && !e.isComposing && items[idx]?.action) { e.preventDefault(); act(items[idx]); }
    else if (e.key === 'Enter' && !e.isComposing && e.keyCode !== 229) { // 229: Safari's Enter that ends an IME composition
      e.preventDefault();
      // A key typed just before Enter may not have rendered yet: the field's own text tells, and the press waits for it.
      const typedNow = e.target instanceof HTMLInputElement ? e.target.value : q();
      if (waiting() || asked() !== q() || typedNow !== q()) setEnter(true); else open(items[idx]);
    }
  };

  const list = createUniqueId();
  const text = createMemo(() => q().trim());
  const terms = createMemo(() => p.terms ?? searchFields);
  const indexedGroups = createMemo(() => {
    let n = 0;
    return groups().map((g, gi) => ({
      ...g,
      showOutsideHeader: !!g.out && !groups()[gi - 1]?.out,
      indexedItems: g.items.map(item => ({ item, k: n++ })),
    }));
  });

  const row = (i: SpotlightItem, k: number) => {
    const st = i.state && (states[i.state.family] as Record<string, StateDef>)[i.state.state] ? i.state : undefined;
    return (
      <li
        id={`${list}-${k}`}
        role="option"
        aria-selected={k === cur() ? 'true' : 'false'}
        aria-keyshortcuts={i.action ? 'Alt+Enter' : undefined}
        aria-description={i.action ? `Alt+Enter: ${i.action.label}` : undefined}
        class={[{ cur: k === cur(), out: !!i.outside, more: !!i.more, snip: !!i.snippet }]}
        onPointerMove={() => { if (k !== cur()) move(k); }}
        onClick={() => open(i)}
      >
        {st ? <StateBadge family={st.family} state={st.state as never} compact /> : <Icon name={i.icon} />}
        <Show
          when={i.snippet}
          keyed
          fallback={<span class="title">{i.label}</span>}
        >
          {snip => <span class="txt"><span class="title">{i.label}</span><span class="snippet">{snip}</span></span>}
        </Show>
        <Show when={i.code} keyed>{cd => <span class="id">{cd}</span>}</Show>
        <span class="grow" />
        <Show when={i.sub} keyed>{s => <span class="sub">{s}</span>}</Show>
        <Show when={i.at !== undefined}><Ago t={i.at!} /></Show>
        <Show when={i.action} keyed>
          {action => (
            <button
              type="button"
              class="btn quiet icon-only"
              tabindex={-1}
              aria-pressed={action.pressed === undefined ? undefined : action.pressed ? 'true' : 'false'}
              aria-label={action.label}
              data-tip={`${action.label}  ⌥⏎`}
              onPointerDown={e => e.preventDefault()}
              onClick={e => { e.stopPropagation(); act(i); }}
            >
              <Icon name={action.icon} />
            </button>
          )}
        </Show>
        <Show when={i.more}><Icon name="arrow-right" /></Show>
      </li>
    );
  };

  return (
    <dialog ref={el => { dialogRef = el; }} closedby="any" class="spotlight" data-ui="spotlight" aria-label="Search" onKeyDown={key}>
      <label class="search">
        <Icon name={actions.search} />
        <input
          type="search"
          ref={el => { field = el; }}
          placeholder="Code, task, conversation, result, project, agent · agent:claude host:mac"
          aria-label="Search"
          role="combobox"
          list={undefined}
          aria-expanded={flat().length > 0 ? 'true' : 'false'}
          aria-controls={list}
          aria-activedescendant={flat()[cur()] ? `${list}-${cur()}` : undefined}
          value={q()}
          onInput={e => type(e.currentTarget.value)}
        />
      </label>
      <Show when={!text() && p.idle}>
        <div class="spot-idle" onClick={() => field?.focus()}>{p.idle}</div>
      </Show>
      <Show when={flat().length > 0}>
        <ul class="spot-list" id={list} role="listbox" aria-label={text() ? 'Results' : 'Recent'}>
          <For each={indexedGroups()}>
            {g => (
              <>
                <Show when={g.showOutsideHeader}>
                  <li class="rail-h spot-out" role="presentation"><Icon name="focus" />Outside focus</li>
                </Show>
                <li class="rail-h" role="presentation">{g.label}</li>
                <For each={g.indexedItems}>{entry => row(entry.item, entry.k)}</For>
              </>
            )}
          </For>
        </ul>
      </Show>
      <Show when={!text() && terms().length > 0}>
        <p class="spot-terms muted">
          <For each={terms()}>
            {t => <button type="button" tabindex={-1} class="id" onPointerDown={e => e.preventDefault()} onClick={() => addTerm(t)}>{t}:</button>}
          </For>
          <span>narrow the search</span>
        </p>
      </Show>
      <Show when={text() && !waiting() && asked() === q() && (failed() || !flat().length)}>
        <p class="muted spot-none" role="status">
          {failed() ? (flat().length ? 'Some kinds failed; try again' : 'Search failed; try again') : 'Nothing found'}
        </p>
      </Show>
    </dialog>
  );
}

/** Sections as a palette provider: the navigation items whose label holds the text. A field term (agent:x) finds none. */
export const sectionProvider = (items: NavItem[], onGo: (id: string) => void): SpotlightProvider => ({
  id: 'sections', label: 'Sections',
  find: q => items.filter(i => matchSearch(q, i.label)).map(i => ({ key: 's:' + i.id, icon: i.icon, label: i.label, run: () => onGo(i.id) })),
});
