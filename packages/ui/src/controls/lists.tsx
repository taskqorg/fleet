// List filtering, state counters, saved views, grouping/sorting, row facts, and selection controls.
import type { ComponentChildren } from 'preact';
import { useCallback, useEffect, useRef, useState } from 'preact/hooks';
import { type IconName, type Family, type StateOf, type StateDef, type Tone, states, actions } from '../registry.ts';
import { isSubmitEnter } from '../keys.ts';
import { Icon, Button, SearchBox, popoverAttr, useAnchoredPopover } from './primitives.tsx';
import { ContextMenu, type MenuItem } from './overlays.tsx';

/* ---------- Filter bar: search with query syntax, applied-filter chips and the list's controls (§4, OR-31, OR-36) ---------- */

export type Chip = { field: string; value: string };
type FilterBarProps = {
  query: string; onQuery: (q: string) => void; placeholder: string;
  /** Filters applied from outside the query (a link from another screen); each removes on its own. */
  chips?: Chip[]; onRemove?: (c: Chip) => void; onClear?: () => void;
  /** After the search, on the same wrapping line: saved views. */
  extra?: ComponentChildren;
  /** The state quick filter (Σ and the state chips), on a line of its own. */
  counters?: ComponentChildren;
  /** List tools after the state quick filter, on a line of their own: the selection toggle, group and sort. */
  tools?: ComponentChildren;
  /** While selection mode is on: the selection bar, on its own line under the tools (nothing it shows covers them). */
  selection?: ComponentChildren;
  shown: number; total: number;
};
export function FilterBar(p: FilterBarProps) {
  const chips = p.chips ?? [];
  return (
    <div class="fbar" data-ui="filter-bar">
      <div class="fbar-line">
        <SearchBox label={p.placeholder} value={p.query} onInput={p.onQuery} />
        {p.extra}
        {chips.map(c => (
          <span class="chip" key={c.field + c.value}>{c.field}: {c.value}
            <button type="button" aria-label={`Remove filter ${c.field}: ${c.value}`} onClick={() => p.onRemove?.(c)}><Icon name={actions.clear} /></button></span>
        ))}
        {chips.length > 0 && p.onClear && <Button label="Clear" kind="quiet" onClick={p.onClear} />}
        <span class="count" aria-live="polite">{p.shown < p.total ? `${p.shown.toLocaleString('en')} of ${p.total.toLocaleString('en')}` : ''}</span>
      </div>
      {p.counters && <div class="fbar-line">{p.counters}</div>}
      {p.tools && <div class="fbar-line">{p.tools}</div>}
      {p.selection && <div class="fbar-line">{p.selection}</div>}
    </div>
  );
}

/* ---------- State counters: quick filter by state with totals (state-counters.md) ---------- */

export function StateCounters<F extends Family>({ family, counts, active, onToggle, onAll, label, pending, unknown, hideZero }: {
  family: F; counts: Partial<Record<StateOf<F>, number | null>>; active: StateOf<F>[]; onToggle: (s: StateOf<F>) => void;
  /** Σ button: show all (clear the state filter). Omit to hide the Σ button. */
  onAll?: () => void; label: string;
  /** A newer answer is loading: the last counts stay, dimmed (docs/designs/fleet-view-urls-and-list-path.md §2). */
  pending?: boolean;
  /** Whether the total has been confirmed by a read. */
  unknown?: boolean;
  /** A counter block, not a quick filter (OR-29): zero chips are hidden, and the caller hides an all-zero block. */
  hideZero?: boolean;
}) {
  const all = Object.keys(states[family]) as StateOf<F>[];
  const values = all.map(s => counts[s]).filter(n => n !== undefined) as (number | null)[];
  const total = unknown || values.includes(null) ? null : (values as number[]).reduce((a, b) => a + b, 0);
  return (
    <div class={`counters${pending ? ' pending' : ''}`} data-ui="state-counters" role="group" aria-label={label} aria-busy={pending || undefined}>
      {onAll && <button type="button" class={`counter t-neutral sigma${active.length === 0 ? ' on' : ''}`} aria-pressed={active.length === 0}
        aria-label={`All: ${total === null ? 'unknown' : total}`} onClick={onAll}><span class="sig" aria-hidden="true">Σ</span><span class="num">{total === null ? '?' : total.toLocaleString('en')}</span></button>}
      {/* OR-29: a chip with 0 shows only its icon; its accessible name still says 0. */}
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

/* ---------- Saved views: system presets and the person's own (OR-33) ---------- */

export function Presets({ system = [], systemTips, presets, active, onPick, onSave, onUnpin, counted, onCount }: {
  /** Views every person has (Tasks "Running"); they cannot be removed. */
  system?: string[];
  /** Optional tooltip and accessible description per system preset to clarify its scope. */
  systemTips?: Record<string, string>;
  presets: string[]; active: string | null; onPick: (name: string) => void; onSave: (name: string) => void; onUnpin: (name: string) => void;
  /**
   * The view the section's navigation counter counts (nav-rail.md, Counters), null when the counter is off. With onCount,
   * each view's menu (right click, Menu key or Shift+F10) offers to count it, or to stop counting it.
   */
  counted?: string | null; onCount?: (name: string | null) => void;
}) {
  const [naming, setNaming] = useState<string | null>(null);
  const [menu, setMenu] = useState<{ name: string; at: { x: number; y: number } } | null>(null);
  const closeMenu = useCallback(() => setMenu(null), []);
  // The view's menu: at the pointer, or under the chip from the keyboard.
  // '-' is the setting's off value, so a view of that name cannot be counted.
  const menuOn = (n: string) => !onCount || n === '-' ? {} : {
    'aria-keyshortcuts': 'Shift+F10',
    onContextMenu: (e: MouseEvent) => { e.preventDefault(); setMenu({ name: n, at: { x: e.clientX, y: e.clientY } }); },
    onKeyDown: (e: KeyboardEvent) => {
      if (e.key !== 'ContextMenu' && !(e.shiftKey && e.key === 'F10')) return;
      e.preventDefault(); const r = (e.currentTarget as HTMLElement).getBoundingClientRect(); setMenu({ name: n, at: { x: r.left, y: r.bottom } });
    },
  };
  return (
    <div class="presets" data-ui="saved-views" role="group" aria-label="Views">
      {system.map(n => {
        const tip = systemTips?.[n];
        return <span key={'s:' + n} class={`preset${n === active ? ' on' : ''}`}>
          <button type="button" aria-pressed={n === active} data-tip={tip} aria-description={tip} onClick={() => onPick(n)} {...menuOn(n)}>{n}</button></span>;
      })}
      {presets.map(n => <span key={n} class={`preset${n === active ? ' on' : ''}`}>
        <button type="button" aria-pressed={n === active} onClick={() => onPick(n)} {...menuOn(n)}><Icon name="pin" />{n}</button>
        <button type="button" class="x" aria-label={`Remove view ${n}`} data-tip="Remove" onClick={() => onUnpin(n)}><Icon name="x" /></button></span>)}
      {naming === null
        ? <Button label="Save view" icon="save" iconOnly kind="quiet" onClick={() => setNaming('')} />
        : <span class="naming"><input value={naming} placeholder="View name" aria-label="View name" ref={e => e?.focus()} onInput={e => setNaming((e.target as HTMLInputElement).value)}
            onKeyDown={e => { if (isSubmitEnter(e) && naming.trim()) { onSave(naming.trim()); setNaming(null); } if (e.key === 'Escape') setNaming(null); }} />
          <Button label="Save" kind="primary" onClick={() => { if (naming.trim()) onSave(naming.trim()); setNaming(null); }} />
          <Button label="Cancel" icon={actions.clear} iconOnly kind="quiet" onClick={() => setNaming(null)} /></span>}
      {menu && onCount && <ContextMenu at={menu.at} label={menu.name} onClose={closeMenu}
        items={[counted === menu.name
          ? { label: 'Stop counting in navigation', icon: 'circle-slash', onClick: () => onCount(null) }
          : { label: 'Count in navigation', icon: 'list-ordered', onClick: () => onCount(menu.name) }]} />}
    </div>
  );
}

/* ---------- Group and sort: shared list controls, part of a saved view (OR-35) ---------- */

/** A short choice menu under its button (OR-14: a short menu may float): the current choice is checked; Esc or a click outside closes it. */
type PickItem = { key: string; label: string; on: boolean; onPick: () => void; icon?: IconName } | 'sep' | { head: string };
function usePickMenu() {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null), menuRef = useRef<HTMLDivElement>(null);
  useAnchoredPopover(open, () => setOpen(false), triggerRef, menuRef);
  // Focus starts on the checked choice, so Enter keeps the current value.
  useEffect(() => { if (open) (menuRef.current?.querySelector<HTMLElement>('[aria-checked=true]') ?? menuRef.current?.querySelector<HTMLElement>('[role=menuitemradio]'))?.focus(); }, [open]);
  const move = (e: KeyboardEvent) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const all = [...menuRef.current!.querySelectorAll<HTMLElement>('[role=menuitemradio]')], i = all.indexOf(document.activeElement as HTMLElement);
    all[(i + (e.key === 'ArrowDown' ? 1 : all.length - 1)) % all.length]?.focus();
  };
  const menu = (label: string, items: PickItem[]) => open && <div ref={menuRef} {...popoverAttr('auto')} class="menu anchored-menu pick-menu" role="menu" aria-label={label} onKeyDown={move}>
    {items.map((it, i) => it === 'sep' ? <span key={i} class="menu-sep" role="separator" />
      : 'head' in it ? <span key={i} class="menu-h">{it.head}</span>
      : <button type="button" key={it.key} role="menuitemradio" aria-checked={it.on} onClick={() => { setOpen(false); it.onPick(); }}>
          {it.icon && <Icon name={it.icon} />}<span class="grow">{it.label}</span>{it.on && <Icon name="check" />}</button>)}
  </div>;
  return { open, setOpen, triggerRef, menu };
}

/**
 * Group by (OR-35): one chip with the registry icon and the current grouping; a click opens the choices in a short menu
 * and the line never changes. The cross removes the grouping in one click; with no grouping only the icon shows.
 */
export function GroupControl<G extends string>({ value, options, onChange, none = 'none' as G }: { value: G; options: readonly (readonly [G, string])[]; onChange: (g: G) => void;
  /** The option key that means no grouping; without it in the options there is no cross and no "No grouping". */ none?: G }) {
  const m = usePickMenu(), canNone = options.some(([g]) => g === none), on = !canNone || value !== none, cur = options.find(([g]) => g === value)?.[1] ?? value;
  return <span class="pick" data-ui="group-control">
    <button ref={m.triggerRef} type="button" class={`pick-b${on ? ' on' : ''}`} aria-haspopup="menu" aria-expanded={m.open} aria-label={on ? `Group by ${cur}` : 'Group by: none'} data-tip="Group by"
      onClick={() => m.setOpen(!m.open)}><Icon name={actions.group} />{on && <span>{cur}</span>}<Icon name="chevron-down" /></button>
    {on && canNone && <button type="button" class="pick-x" aria-label="Remove grouping" data-tip="No grouping" onClick={() => { m.setOpen(false); onChange(none); }}><Icon name="x" /></button>}
    {m.menu('Group by', [{ head: 'Group by' }, ...options.filter(([g]) => g !== none).map(([g, l]) => ({ key: g, label: l, on: g === value, onPick: () => onChange(g) })),
      ...(canNone ? ['sep' as const, { key: none, label: 'No grouping', on: !on, onPick: () => onChange(none) }] : [])])}
  </span>;
}

/**
 * Sort (OR-35): one chip led by the direction icon, a button that flips the direction in one click (as Group leads with
 * its icon), then the field, which opens the fields and the directions in a short menu. The value is the field, "-" before it for descending.
 * A field may name its directions (Newest first / Oldest first for times, the default).
 */
/** A sort field: value, name, direction names (descending, ascending; Newest first, Oldest first by default), and what the rows show:
 *  'age' (default: a moment shown as 4m, 3d) or 'value' (a number or text shown as it is). */
export type SortField = readonly [string, string] | readonly [string, string, readonly [string, string]] | readonly [string, string, readonly [string, string], 'age' | 'value'];
export function SortControl({ value, fields, onChange }: { value: string; fields: readonly SortField[]; onChange: (v: string) => void }) {
  const m = usePickMenu();
  const desc = value.startsWith('-'), field = desc ? value.slice(1) : value, f = fields.find(x => x[0] === field) ?? fields[0];
  const [dDesc, dAsc] = (f[2] ?? ['Newest first', 'Oldest first']) as readonly [string, string];
  // The icon's bars draw the values the rows show, top to bottom. A moment shows as an age (4m, 2h, 3d), so newest
  // first (descending) is small ages first: the narrow-to-wide icon, the reverse of a value sorted high first.
  const ages = (f[3] ?? 'age') === 'age', bigFirst = (d: boolean) => d !== ages, iconOf = (d: boolean) => bigFirst(d) ? actions.sortDesc : actions.sortAsc;
  return <span class="pick" data-ui="sort-control">
    <button type="button" class="pick-x pick-dir" aria-label={desc ? dDesc : dAsc} data-tip={`${desc ? dDesc : dAsc}; click for ${(desc ? dAsc : dDesc).toLowerCase()}`}
      onClick={() => { m.setOpen(false); onChange(desc ? f[0] : '-' + f[0]); }}><Icon name={iconOf(desc)} /></button>
    <button ref={m.triggerRef} type="button" class="pick-b on" aria-haspopup="menu" aria-expanded={m.open} aria-label={`Sort by ${f[1]}`} data-tip="Sort by" onClick={() => m.setOpen(!m.open)}>
      <span>{f[1]}</span><Icon name="chevron-down" /></button>
    {m.menu('Sort', [{ head: 'Sort by' }, ...fields.map(x => ({ key: x[0], label: x[1], on: x[0] === f[0], onPick: () => onChange((desc ? '-' : '') + x[0]) })), 'sep',
      { key: 'desc', label: dDesc, icon: iconOf(true), on: desc, onPick: () => onChange('-' + f[0]) },
      { key: 'asc', label: dAsc, icon: iconOf(false), on: !desc, onPick: () => onChange(f[0]) }])}
  </span>;
}

/* ---------- Row facts (OR-37) ---------- */

/** Live facts of a row in short form, in the row's right zone (OR-21). Each fact is icon + compact value with a tooltip. */
export const RowFacts = ({ children }: { children: ComponentChildren }) => <span class="row-facts" data-ui="row-facts">{children}</span>;
export const Fact = ({ icon, text, tip, label, children }: { icon?: IconName; text?: string; tip: string; label?: string; children?: ComponentChildren }) =>
  <span class="fact" data-tip={tip} aria-label={children ? undefined : label ?? tip}>{icon && <Icon name={icon} />}{text}{children}</span>;

/* ---------- Selection mode: a checkbox column and the action bar (OR-37) ---------- */

/**
 * The selection bar: its own line under the list tools while selection mode is on (FilterBar selection); group and sort
 * stay. The box selects every loaded row or none; actions apply to the selected rows; Esc or the cross leaves the mode.
 */
export function SelectBar({ count, total, onAll, onNone, actions: acts, onExit }: {
  count: number; total: number; onAll: () => void; onNone: () => void; actions: MenuItem[]; onExit: () => void;
}) {
  useEffect(() => {
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape' && !e.defaultPrevented && !(e.target as Element).closest?.('input,textarea,[role=menu]')) onExit(); };
    document.addEventListener('keydown', esc); return () => document.removeEventListener('keydown', esc);
  }, [onExit]);
  const all = total > 0 && count >= total;
  // Left to right: select all (in the row checkbox column), leave, the count; the actions on the right.
  return <div class="select-bar" data-ui="select-bar" role="toolbar" aria-label="Selection">
    <input type="checkbox" class="row-check" checked={all} ref={el => { if (el) el.indeterminate = count > 0 && !all; }}
      aria-label={all ? 'Select none' : 'Select all'} data-tip={all ? 'Select none' : 'Select all'} onChange={() => all ? onNone() : onAll()} />
    <Button label="Leave selection" icon="x" iconOnly kind="quiet" onClick={onExit} />
    <span aria-live="polite"><span class="num">{count.toLocaleString('en')}</span> selected</span>
    <span class="grow" />
    {acts.map(a => <Button key={a.label} label={a.label} icon={a.icon} iconOnly kind={a.danger ? 'danger' : 'quiet'}
      disabledReason={count === 0 ? 'Select rows first' : a.disabledReason} onClick={a.onClick} />)}
  </div>;
}

/** Turns selection mode on: a checkbox-shaped button at the left of the list tools, above the checkbox column it opens. */
export const SelectToggle = ({ on, onClick }: { on: boolean; onClick: () => void }) =>
  <span class="sel-toggle"><Button label="Select" icon="square" iconOnly kind="quiet" pressed={on} onClick={onClick} /></span>;

/* ---------- Row check: one checked row of a list, kept in the view URL as sel (fleet-view-urls-and-list-path.md §1) ---------- */

export const RowCheck = ({ checked, label, onToggle }: { checked: boolean; label: string; onToggle: () => void }) =>
  <input type="checkbox" class="row-check" data-ui="row-check" checked={checked} aria-label={label} onClick={e => e.stopPropagation()} onChange={onToggle} />;

/* ---------- Row pin: the pin mark of a pinned row, before its state icon (OR-12, OR-37) ---------- */

/**
 * Rendered only on pinned rows, as the first cell; the caller gives that row the class pin-row, which adds the one leading
 * column (styles.css), so other rows do not move. Both go together: the cell without the class shifts every cell.
 * A click unpins and the row leaves the Pinned group.
 */
export const RowPin = ({ label, onUnpin }: { label: string; onUnpin: () => void }) =>
  <button type="button" class="row-pin" data-ui="row-pin" aria-label={`Unpin ${label}`} data-tip="Unpin" onClick={e => { e.stopPropagation(); onUnpin(); }}><Icon name="pin" /></button>;
