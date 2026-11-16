// The list-view pattern shared by Tasks and Conversations (docs/design-system/patterns/list-view.md): saved views (OR-33),
// pins read by id (OR-12), grouping with collapse, selection mode and the row menu (OR-37). A page keeps its rows, facts,
// actions, server kind and fields; useListPage holds the state and ListPagePanel renders the list section.
import type { ComponentChildren } from 'preact';
import { useCallback, useEffect, useRef, useState } from 'preact/hooks';
import {
  ListFrame, FilterBar, Presets, StateCounters, GroupHeader, GroupControl, SortControl, VirtualList, ContextMenu, SelectBar, SelectToggle, RowPin, RowCheck, copyText, actions,
  EntityCode, ErrorBanner, Skeleton, type IconName,
} from '../../packages/ui/src/index.ts';
import tokens from '../../packages/ui/tokens.json';
import { op, online } from './api.ts';
import { useList, useDoc, type Kind } from './store.ts';
import { chunks, compareKeys } from './lists.ts';
import { listKey, formatView, type View, type ViewName } from '../../packages/view-url/view-url.ts';
import { useUrlView, useSearchBox } from './url.ts';
import { usePins, useViews, type SavedView } from './prefs.ts';
import type { Change } from './feed.ts';

/** A server list row: its id, code and the server sort key whose first part is the group key. */
export type ListRow = { id: string; code?: string | null; sortKey: (string | number)[] };
export type ListItem<R> = { kind: 'group'; key: string; label: string; count: number; icon?: 'pin' } | { kind: 'row'; r: R; pinned?: boolean };
type MenuItems = Parameters<typeof ContextMenu>[0]['items'];
type BarActions = Parameters<typeof SelectBar>[0]['actions'];
const ROW = parseInt(tokens.size['row-2']); // two-line catalogue row
const PINNED = '\u0000pinned'; // group key of the pinned rows; never a server group key

export type ListPageOptions = {
  /** URL view name; also the pins and saved-views list name. */
  view: ViewName;
  /** Server list kind (`useList`, `list_membership`). */
  kind: Kind;
  defaultGroup: string;
  defaultSort: string;
  /** System presets (OR-33): fixed, shown before the person's views; each sets the state filter only. */
  system: SavedView[];
  /** Doc name prefix of the pin read. */
  pinDoc: string;
  /** The pin read's filter, and the parts of the view it depends on (added to the doc name). */
  pinRead: (v: View) => { key: unknown[]; filter: Record<string, unknown> };
  /** Whether a change touches a pinned row. */
  pinTouches: (c: Change, pinned: (id: string) => boolean) => boolean;
  groupLabel: (group: string, label: string | null) => string;
  /** Whether a search also clears the active preset. */
  searchClearsPreset?: boolean;
  /** A saved personal view as it is matched and restored (e.g. a default for a key older views lack). */
  savedView?: (v: View) => View;
  /** The grouping saved with a view when the URL names none. */
  saveGroup?: string;
  /** Further view keys a personal view saves. */
  saveExtra?: (v: View) => Partial<View>;
  /** Further view keys a personal view restores, given the current view. */
  pickExtra?: (saved: View, current: View) => Partial<View>;
  /** Rows a bulk action finished leave the selection. */
  dropDone?: boolean;
  /** A change of this value is a switch to another list: it ends selection mode and a pending confirmation. */
  listId?: (v: View) => unknown;
};

export function useListPage<R extends ListRow>(o: ListPageOptions) {
  const { view, set, href } = useUrlView(o.view);
  const [closed, setClosed] = useState<string[]>([]);
  const [query, setQuery] = useSearchBox(view.q, q => set(o.searchClearsPreset ? { q: q || undefined, preset: undefined } : { q: q || undefined }, 'type'));
  const pins = usePins(o.view);
  const views = useViews(o.view, o.system);
  const list = useList(o.kind, view);
  const group = view.group ?? o.defaultGroup, sort = view.sort ?? o.defaultSort, states = view.state ?? [];
  // Rows on screen may still be the previous list (pending): headers and chips follow that list's own grouping.
  const rowsGroup = list.shownView.group ?? o.defaultGroup;
  const rows = list.rows as unknown as R[];
  const sel = view.sel ?? [], selNow = useRef(sel), toggleSel = (x: string) => set({ sel: sel.includes(x) ? sel.filter(i => i !== x) : [...sel, x] });
  selNow.current = sel;
  // Selection mode (OR-37): a checkbox column left of the state, the action bar under the tools.
  const [selectOn, setSelectOn] = useState(false);
  const selecting = selectOn || sel.length > 0;
  const [confirm, setConfirm] = useState<R[] | null>(null);
  const [bulkError, setBulkError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const leave = useCallback(() => { setSelectOn(false); setConfirm(null); setBulkError(null); set({ sel: undefined }); }, [set]);
  const [menu, setMenu] = useState<{ r: R; at: { x: number; y: number } } | null>(null);
  const closeMenu = useCallback(() => setMenu(null), []);
  // A switch to another list by any path ends selection mode and a pending confirmation.
  const firstList = useRef(true);
  useEffect(() => { if (firstList.current) { firstList.current = false; return; } setSelectOn(false); setConfirm(null); setBulkError(null); }, [o.listId?.(view)]);
  // Pinned rows stay in the list whatever the filter, search or grouping (OR-12): they are read by id with the page's pin
  // filter and live through row changes; the filtered page supplies the rest.
  const pinRead = o.pinRead(view);
  const pinnedDoc = useDoc<R[]>(pins.ids.length ? JSON.stringify([o.pinDoc, pins.ids, sort, ...pinRead.key]) : null,
    async () => {
      const found: R[] = [];
      // The operation accepts at most 1,000 IDs; sequential batches avoid a request burst for a large pin collection.
      for (const ids of chunks(pins.ids, 1000)) {
        const reply = await op('list_membership', { ids, lists: [{ name: o.kind, filter: pinRead.filter, group: 'none', sort }] });
        found.push(...reply.lists[0].rows as unknown as R[]);
      }
      return found;
    }, c => o.pinTouches(c, pins.has));
  const byId = new Map((pinnedDoc.data ?? []).map(r => [r.id, r]));
  // The page supplies a just-pinned row while its independent read is pending. Its grouping key is irrelevant here.
  const pinned = pins.ids.map(x => byId.get(x) ?? rows.find(r => r.id === x))
    .filter((r): r is R => !!r).sort((a, b) => compareKeys(a.sortKey.slice(1), b.sortKey.slice(1)));
  const rest = rows.filter(r => !pins.has(r.id)), displayed = [...pinned, ...rest];
  const items: ListItem<R>[] = [];
  // Pinned rows come first, as a group of their own (OR-12); each shows its pin before the state (RowPin).
  if (pinned.length) {
    items.push({ kind: 'group', key: PINNED, label: 'Pinned', count: pinned.length, icon: 'pin' });
    if (!closed.includes(PINNED)) for (const r of pinned) items.push({ kind: 'row', r, pinned: true });
  }
  let last: string | null = null;
  for (const r of rest) {
    const g = String(r.sortKey[0]);
    if (rowsGroup !== 'none' && g !== last) { const meta = list.groups.find(x => x.key === g); items.push({ kind: 'group', key: g, label: o.groupLabel(rowsGroup, meta?.label ?? null), count: meta?.count ?? 0 }); last = g; }
    if (!closed.includes(g)) items.push({ kind: 'row', r });
  }
  const toggleGroup = (key: string) => setClosed(c => c.includes(key) ? c.filter(x => x !== key) : [...c, key]);
  const chosen = displayed.filter(r => sel.includes(r.id));
  // One operation per row; failures are counted in one banner. With dropDone, the rows that succeeded leave the selection
  // as it is now (not when the operation began), whatever the others did.
  const bulk = async (rs: R[], run: (r: R) => Promise<unknown>, failText: string) => {
    if (busy || !rs.length) return;
    setBulkError(null); setBusy(true);
    const r = await Promise.allSettled(rs.map(run));
    setBusy(false);
    const failed = r.filter(x => x.status === 'rejected') as PromiseRejectedResult[];
    if (failed.length) setBulkError(`${failed.length} of ${rs.length} ${failText}: ${failed[0].reason?.message ?? failed[0].reason}`);
    if (o.dropDone) {
      const done = rs.filter((_, i) => r[i].status === 'fulfilled').map(x => x.id);
      if (done.length) set({ sel: selNow.current.filter(x => !done.includes(x)) });
    }
  };
  const off = !online.value ? 'offline' : busy ? 'Working' : undefined;
  const pinAll = (on: boolean) => { for (const r of chosen) if (pins.has(r.id) !== on) pins.toggle(r.id); };
  const select = (r: R) => { setSelectOn(true); setConfirm(null); toggleSel(r.id); };
  // A personal view matches the whole view and wins over a system preset with the same states; a system preset matches the
  // states alone.
  const savedView = o.savedView ?? ((v: View) => v);
  const presetKey = (v: View) => listKey({ ...v, view: o.view });
  const stateKey = (state?: string[]) => listKey({ view: o.view, state: state ?? [] });
  const activePreset = views.list.find(v => !v.system && presetKey(savedView(v.view)) === listKey(view))?.name
    ?? (view.state?.length ? views.list.find(v => v.system && stateKey(v.view.state) === stateKey(view.state))?.name : undefined) ?? null;
  // A system preset sets the states only; a personal view restores everything it saved.
  const pick = (n: string) => {
    const p = views.list.find(x => x.name === n)!, sv = savedView(p.view);
    set(p.system ? { state: p.view.state ?? [], preset: n } : { state: sv.state ?? [], group: sv.group, sort: sv.sort, project: sv.project ?? [], q: sv.q, ...o.pickExtra?.(sv, view), preset: n });
  };
  const save = (n: string) => views.save({ name: n, view: { view: o.view, state: view.state, group: view.group ?? o.saveGroup, sort: view.sort, project: view.project, q: view.q, ...o.saveExtra?.(view) } });
  const shown = states.length ? states.reduce((n, s) => n + (list.counts[s] ?? 0), 0) : list.total;
  return {
    view, set, href, query, setQuery, pins, views, list, group, sort, states, rowsGroup, rows, items, displayed, chosen, closed, toggleGroup,
    sel, selecting, setSelectOn, select, leave, confirm, setConfirm, bulkError, busy, bulk, off, pinAll, menu, setMenu, closeMenu,
    pinnedDoc, activePreset, pick, save, shown,
  };
}
export type ListPage<R extends ListRow> = ReturnType<typeof useListPage<R>>;

type Options = readonly (readonly [string, string])[];

/** The list section: filter bar (search, saved views, state counters, tools, selection bar), banners, the rows and the row menu. */
export function ListPagePanel<R extends ListRow>(p: {
  page: ListPage<R>;
  /** Plural noun of the error banners: "Tasks did not load", "Pinned tasks did not load". */
  noun: string;
  placeholder: string;
  family: 'task' | 'session';
  groups: Options;
  sorts: Options;
  /** Tools after Select, Group and Sort. */
  tools?: ComponentChildren;
  /** Selection bar actions after Pin and Unpin. */
  actions: BarActions;
  /** Row menu items after Open, Pin, Select, Copy code and Copy link. */
  menu: (r: R) => MenuItems;
  /** A pending confirmation, shown above the banners. */
  confirm?: ComponentChildren;
  /** The empty state when nothing loaded and nothing failed. */
  empty: ComponentChildren;
  listLabel: string;
  title: (r: R) => string;
  groupIcon: (group: string) => IconName;
  /** The state icon cell. */
  state: (r: R) => ComponentChildren;
  /** Content before the title link. */
  before?: (r: R) => ComponentChildren;
  facts: (r: R) => ComponentChildren;
  focused?: string;
}) {
  const { page: g } = p, { view, set, list, pins, views, sel, selecting, chosen, displayed, pinnedDoc } = g;
  const rowMenu = (r: R): MenuItems => [
    { label: 'Open', icon: actions.open, onClick: () => set({ focus: r.id }) },
    { label: pins.has(r.id) ? 'Unpin' : 'Pin', icon: pins.has(r.id) ? 'pin-off' : 'pin', onClick: () => pins.toggle(r.id) },
    { label: sel.includes(r.id) ? 'Deselect' : 'Select', icon: 'square', onClick: () => g.select(r) },
    { label: 'Copy code', icon: actions.copy, onClick: () => copyText(r.code ?? r.id) },
    { label: 'Copy link', icon: 'link', onClick: () => copyText(location.origin + formatView({ view: view.view, focus: r.id })) },
    ...p.menu(r),
  ];
  return <section class="panel">
    <ListFrame pending={list.pending} bar={<FilterBar query={g.query} placeholder={p.placeholder} onQuery={g.setQuery} shown={g.shown} total={list.total}
      extra={<Presets system={views.list.filter(v => v.system).map(v => v.name)} presets={views.list.filter(v => !v.system).map(v => v.name)} active={g.activePreset} onPick={g.pick}
        onSave={g.save} onUnpin={views.remove} />}
      counters={<StateCounters family={p.family} label={`${p.noun} by state`} pending={list.pending} counts={list.counts as any} active={g.states as any} onAll={() => set({ state: [], preset: undefined })}
        onToggle={(s: string) => set({ state: g.states.includes(s) ? g.states.filter(x => x !== s) : [...g.states, s], preset: undefined })} />}
      tools={<>
        <SelectToggle on={selecting} onClick={() => selecting ? g.leave() : g.setSelectOn(true)} />
        <GroupControl value={g.group} options={p.groups} onChange={x => set({ group: x, preset: undefined })} />
        <SortControl value={g.sort} fields={p.sorts} onChange={v => set({ sort: v, preset: undefined })} />
        {p.tools}
      </>}
      selection={selecting && <SelectBar count={chosen.length} total={displayed.length} onAll={() => { g.setSelectOn(true); g.setConfirm(null); set({ sel: displayed.map(r => r.id) }); }} onNone={() => { g.setSelectOn(true); g.setConfirm(null); set({ sel: [] }); }} onExit={g.leave} actions={[
        { label: 'Pin', icon: 'pin', onClick: () => g.pinAll(true), disabledReason: chosen.length && chosen.every(r => pins.has(r.id)) ? 'All pinned' : undefined },
        { label: 'Unpin', icon: 'pin-off', onClick: () => g.pinAll(false), disabledReason: chosen.some(r => pins.has(r.id)) ? undefined : 'None pinned' },
        ...p.actions,
      ]} />} />}>
      {p.confirm}
      {g.bulkError && <ErrorBanner text={g.bulkError} />}
      {list.error && <ErrorBanner text={`${p.noun} did not load: ${list.error}`} onRetry={list.reload} />}
      {pinnedDoc.error && <ErrorBanner text={`Pinned ${p.noun.toLowerCase()} did not load: ${pinnedDoc.error}`} onRetry={pinnedDoc.reload} />}
      {!displayed.length && (list.shownKey === null || list.pending || (pins.ids.length > 0 && pinnedDoc.data === null && !pinnedDoc.error)) ? <Skeleton rows={8} />
        : !displayed.length ? (list.error || pinnedDoc.error ? null : p.empty)
        : <VirtualList items={g.items} getKey={i => i.kind === 'group' ? 'g:' + i.key : i.r.id} rowHeight={ROW} follow="top" label={p.listLabel} onEnd={list.more}
          columns={`${selecting ? '1.25rem ' : ''}1.25rem minmax(0,1fr) auto`} rowClass={i => i.kind === 'group' ? 'grp' : `${i.r.id === p.focused ? 'sel' : ''}${sel.includes(i.r.id) ? ' checked' : ''}${i.pinned ? ' pin-row' : ''}`}
          onRowMenu={(i, at) => { if (i.kind === 'row') g.setMenu({ r: i.r, at }); }}
          render={i => i.kind === 'group'
            ? <GroupHeader icon={i.icon ?? p.groupIcon(g.rowsGroup)} label={i.label} count={i.count} open={!g.closed.includes(i.key)} onToggle={() => g.toggleGroup(i.key)} />
            : <>
              {selecting && <span class="lead"><RowCheck checked={sel.includes(i.r.id)} label={'Select ' + p.title(i.r)} onToggle={() => g.select(i.r)} /></span>}
              {i.pinned && <span class="lead"><RowPin label={p.title(i.r)} onUnpin={() => {
                // In selection mode the pin selects like the rest of the row; otherwise it unpins and focus follows the row to its group.
                if (selecting) return g.select(i.r);
                pins.toggle(i.r.id); requestAnimationFrame(() => document.querySelector<HTMLElement>(`.vl-row[data-key="${CSS.escape(i.r.id)}"] .row-link`)?.focus({ preventScroll: true }));
              }} /></span>}
              <span class="lead">{p.state(i.r)}</span>
              <span class="tt">{p.before?.(i.r)}<a class="title row-link" href={g.href({ focus: i.r.id })} aria-keyshortcuts="P X"
                onClick={e => { e.preventDefault(); if (selecting || e.shiftKey) g.select(i.r); else set({ focus: i.r.id }); }}
                onKeyDown={e => {
                  if (e.altKey || e.ctrlKey || e.metaKey) return;
                  if (e.key === 'p') { e.preventDefault(); pins.toggle(i.r.id); }
                  else if (e.key === 'x') { e.preventDefault(); g.select(i.r); }
                  // The Menu key and Shift+F10 fire contextmenu on the row: onRowMenu opens the menu.
                }}>{p.title(i.r)}</a><EntityCode code={i.r.code} id={i.r.id} /></span>
              <span class="rz">{p.facts(i.r)}</span>
            </>} />}
    </ListFrame>
    {g.menu && <ContextMenu at={g.menu.at} label={p.title(g.menu.r)} items={rowMenu(g.menu.r)} onClose={g.closeMenu} />}
  </section>;
}
