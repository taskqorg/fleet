// The list-view pattern shared by Tasks and Conversations (docs/design-system/patterns/list-view.md): saved views (OR-33),
// pins read by id (OR-12), grouping with collapse, selection mode and the row menu (OR-37). A page keeps its rows, facts,
// actions, server kind and fields; useListPage holds the state and ListPagePanel renders the list section.
import type { ComponentChildren } from 'preact';
import { useCallback, useEffect, useRef, useState } from 'preact/hooks';
import {
  ListFrame, FilterBar, Presets, StateCounters, GroupHeader, GroupControl, SortControl, VirtualList, ContextMenu, SelectBar, SelectToggle, RowPin, RowCheck, copyText, actions,
  EntityCode, ErrorBanner, Skeleton, type IconName, type Family, type StateOf, type Chip, type Tone, type SortField,
} from '../../packages/ui/src/index.ts';
import tokens from '../../packages/ui/tokens.json';
import { op, online } from './api.ts';
import { useList, useDoc, type Kind } from './store.ts';
import { chunks, compareKeys, closedGroups, flipGroup } from './lists.ts';
import { listKey, formatView, parseView, type View, type ViewName } from '../../packages/view-url/view-url.ts';
import { useUrlView, useSearchBox } from './url.ts';
import { usePins, useViews, listGroups, setListGroups, saveListGroups, type SavedView } from './prefs.ts';
import type { Change } from './feed.ts';

/** A server list row: its id, code and the server sort key whose first part is the group key. */
export type ListRow = { id: string; code?: string | null; sortKey: (string | number)[] };
/**
 * A list the page reads whole and filters, groups and sorts itself (Delivery: delivery_status holds every pull request), in
 * place of a server list kind. `rows` are in group order, each row's sortKey[0] its group key; `all` is unfiltered (pins).
 */
export type LocalList<R> = { rows: R[]; all: R[]; counts: Record<string, number>; groups: { key: string; label: string; count: number }[];
  /** Rows before the search and the state filter: the "of M" of the count. */ total: number;
  pending: boolean; error: string | null; reload: () => void };
export type ListItem<R> = { kind: 'group'; key: string; label: string; count: number; icon?: 'pin' } | { kind: 'row'; r: R; pinned?: boolean };
type MenuItems = Parameters<typeof ContextMenu>[0]['items'];
type BarActions = Parameters<typeof SelectBar>[0]['actions'];
const ROW = parseInt(tokens.size['row-2']); // two-line catalogue row
const PINNED = '\u0000pinned'; // group key of the pinned rows; never a server group key

export type ListPageOptions = {
  /** URL view name; also the pins and saved-views list name. */
  view: ViewName;
  /** Server list kind (`useList`, `list_membership`); a page with `local` has none. */
  kind?: Kind;
  /** The list built by the page from the view, in place of a server list; fixed for the page's lifetime. */
  local?: (v: View) => LocalList<ListRow>;
  defaultGroup: string;
  defaultSort: string;
  /** System presets (OR-33): fixed, shown before the person's views; each sets the state filter only. */
  system: SavedView[];
  /** Group keys collapsed by default, by grouping (Tasks: completed and cancelled under state; Delivery: installed under
   * stage). The person's own toggles are remembered on the server per `<view>:<grouping>` (`listGroups`) and win. */
  closedGroups?: Partial<Record<string, readonly string[]>>;
  /** A context of the view with defaults and remembered groups of its own, kept as `<grouping>-<context>` (Delivery:
   * `merged` for a picked day or a filter to merged rows, where Installed is open by default). A default never changes
   * inside one context, so a remembered flip means the same every time. */
  groupsContext?: (v: View) => string | undefined;
  /** Doc name prefix of the pin read (server lists; a local list finds pinned rows in its own rows). */
  pinDoc?: string;
  /** The pin read's filter, and the parts of the view it depends on (added to the doc name). */
  pinRead?: (v: View) => { key: unknown[]; filter: Record<string, unknown> };
  /** Whether a change touches a pinned row. */
  pinTouches?: (c: Change, pinned: (id: string) => boolean) => boolean;
  groupLabel: (group: string, label: string | null) => string;
  /** A saved personal view as it is matched and restored (e.g. a default for a key older views lack). */
  savedView?: (v: View) => View;
  /** The grouping saved with a view when the URL names none. */
  saveGroup?: string;
  /** Further view keys a personal view saves. */
  saveExtra?: (v: View) => Partial<View>;
  /** Further view keys a personal view restores. */
  pickExtra?: (saved: View) => Partial<View>;
  /** A change of this value is a switch to another list: it ends selection mode and a pending confirmation; a saved view
   * that switches lists also drops the checked rows. */
  listId?: (v: View) => unknown;
};

export function useListPage<R extends ListRow>(o: ListPageOptions) {
  const { view, set, href } = useUrlView(o.view);
  // Unsent group toggles leave with the section (the tab hiding sends them too: main.tsx).
  useEffect(() => saveListGroups, []);
  // A new query ends the preset that was active when the person typed it. A preset picked while the debounced query waits
  // stays: the commit reads the URL as it is then (url.ts) and leaves a different preset alone.
  const typedPreset = useRef<string | undefined>(undefined);
  const [query, setText] = useSearchBox(view.q, q => {
    const now = parseView(location.pathname + location.search)?.preset;
    set({ q: q || undefined, ...(now === typedPreset.current ? { preset: undefined } : {}) }, 'type');
  });
  const setQuery = (t: string) => { typedPreset.current = view.preset; setText(t); };
  const pins = usePins(o.view);
  const views = useViews(o.view, o.system);
  // A page passes `local` for its whole life or never, so the hook order never changes between renders.
  const local = o.local?.(view);
  const list = local ? localList(local, view) : useList(o.kind!, view);
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
  const leave = () => { setSelectOn(false); setConfirm(null); setBulkError(null); set({ sel: undefined }); };
  const [menu, setMenu] = useState<{ r: R; at: { x: number; y: number } } | null>(null);
  const closeMenu = useCallback(() => setMenu(null), []);
  // A switch to another list by any path ends selection mode and a pending confirmation.
  const firstList = useRef(true);
  useEffect(() => { if (firstList.current) { firstList.current = false; return; } setSelectOn(false); setConfirm(null); setBulkError(null); }, [o.listId?.(view)]);
  // Pinned rows stay in the list whatever the filter, search or grouping (OR-12): they are read by id with the page's pin
  // filter and live through row changes; the filtered page supplies the rest.
  const pinRead = o.pinRead?.(view) ?? { key: [], filter: {} };
  const pinnedDoc = useDoc<R[]>(pins.ids.length && !local ? JSON.stringify([o.pinDoc, pins.ids, sort, ...pinRead.key]) : null,
    async () => {
      const found: R[] = [];
      // The operation accepts at most 1,000 IDs; sequential batches avoid a request burst for a large pin collection.
      for (const ids of chunks(pins.ids, 1000)) {
        const reply = await op('list_membership', { ids, lists: [{ name: o.kind!, filter: pinRead.filter, group: 'none', sort }] });
        found.push(...reply.lists[0].rows as unknown as R[]);
      }
      return found;
    }, c => !!o.pinTouches?.(c, pins.has));
  const byId = new Map(((local?.all as R[] | undefined) ?? pinnedDoc.data ?? []).map(r => [r.id, r]));
  // The page supplies a just-pinned row while its independent read is pending. Its grouping key is irrelevant here.
  const pinned = pins.ids.map(x => byId.get(x) ?? rows.find(r => r.id === x))
    .filter((r): r is R => !!r).sort((a, b) => compareKeys(a.sortKey.slice(1), b.sortKey.slice(1)));
  const rest = rows.filter(r => !pins.has(r.id)), displayed = [...pinned, ...rest];
  // Collapsed groups: the page's defaults for the grouping on screen with the person's flips (prefs listGroups), read in
  // render, so the first frame after the preferences load already has them. The Pinned group is one list per view.
  const context = o.groupsContext?.(list.shownView), grouping = context ? `${rowsGroup}-${context}` : rowsGroup;
  const groupsList = `${o.view}:${grouping}`, pinList = `${o.view}:pinned`;
  const closed = closedGroups(o.closedGroups?.[grouping] ?? [], listGroups(groupsList));
  if (listGroups(pinList).includes('pinned')) closed.add(PINNED);
  const toggleGroup = (key: string) => key === PINNED ? setListGroups(pinList, flipGroup(listGroups(pinList), 'pinned'))
    : setListGroups(groupsList, flipGroup(listGroups(groupsList), key));
  const items: ListItem<R>[] = [];
  // Pinned rows come first, as a group of their own (OR-12); each shows its pin before the state (RowPin).
  if (pinned.length) {
    items.push({ kind: 'group', key: PINNED, label: 'Pinned', count: pinned.length, icon: 'pin' });
    if (!closed.has(PINNED)) for (const r of pinned) items.push({ kind: 'row', r, pinned: true });
  }
  let last: string | null = null;
  const shownGroups = new Set<string>();
  for (const r of rest) {
    const g = String(r.sortKey[0]);
    if (rowsGroup !== 'none' && g !== last) { shownGroups.add(g); const meta = list.groups.find(x => x.key === g); items.push({ kind: 'group', key: g, label: o.groupLabel(rowsGroup, meta?.label ?? null), count: meta?.count ?? 0 }); last = g; }
    if (!closed.has(g)) items.push({ kind: 'row', r });
  }
  // Loaded rows that end inside a collapsed group show no new items, so the list end would stop asking for pages. The
  // collapsed groups after it (the server's groups, in row order) show their headers without reading their rows, up to and
  // with the first open group, whose rows each loaded page keeps asking for (endKey). Collapsed groups at the end (Completed, Cancelled)
  // are not read through.
  const tail = rest.length && rowsGroup !== 'none' ? String(rest[rest.length - 1].sortKey[0]) : null;
  let endKey = '';
  if (tail !== null && closed.has(tail)) {
    const at = list.groups.findIndex(x => x.key === tail);
    if (at < 0) endKey = String(rows.length); // the group counts do not know the tail yet: keep loading
    for (const meta of at < 0 ? [] : list.groups.slice(at + 1)) {
      if (shownGroups.has(meta.key)) continue; // a live row placed it already
      items.push({ kind: 'group', key: meta.key, label: o.groupLabel(rowsGroup, meta.label ?? null), count: meta.count });
      if (!closed.has(meta.key)) { endKey = String(rows.length); break; }
    }
  }
  const chosen = displayed.filter(r => sel.includes(r.id));
  // One operation per row; failures are counted in one banner. The rows that succeeded leave the selection as it is now
  // (not when the operation began), whatever the others did; failed rows stay selected.
  // A bulk action that settles after the page closed leaves the URL alone: set() would write into whatever view is open then.
  const mounted = useRef(true);
  useEffect(() => () => { mounted.current = false; }, []);
  const bulk = async (rs: R[], run: (r: R) => Promise<unknown>, failText: string) => {
    if (busy || !rs.length) return;
    setBulkError(null); setBusy(true);
    const r = await Promise.allSettled(rs.map(run));
    setBusy(false);
    const failed = r.filter(x => x.status === 'rejected') as PromiseRejectedResult[];
    if (failed.length) setBulkError(`${failed.length} of ${rs.length} ${failText}: ${failed[0].reason?.message ?? failed[0].reason}`);
    const done = rs.filter((_, i) => r[i].status === 'fulfilled').map(x => x.id);
    if (done.length && mounted.current) set({ sel: selNow.current.filter(x => !done.includes(x)) });
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
    set(p.system ? { state: p.view.state ?? [], preset: n } : { state: sv.state ?? [], group: sv.group, sort: sv.sort, project: sv.project ?? [], q: sv.q, ...o.pickExtra?.(sv), preset: n, ...(o.listId && o.listId(sv) !== o.listId(view) ? { sel: undefined } : {}) });
  };
  const save = (n: string) => views.save({ name: n, view: { view: o.view, state: view.state, group: view.group ?? o.saveGroup, sort: view.sort, project: view.project, q: view.q, ...o.saveExtra?.(view) } });
  // The empty state's Clear filters: states, search, projects and the active preset.
  // setQuery('') also drops a typed query not yet committed, which the URL sync alone would leave in the box.
  const filtered = !!(states.length || view.q || view.project?.length);
  const clearFilters = () => { setQuery(''); set({ state: [], q: undefined, project: [], preset: undefined }); };
  const shown = local ? local.rows.length : states.length ? states.reduce((n, s) => n + (list.counts[s] ?? 0), 0) : list.total;
  return {
    view, set, href, query, setQuery, pins, views, list, group, sort, states, rowsGroup, rows, items, endKey, displayed, chosen, closed, toggleGroup,
    sel, selecting, setSelectOn, select, leave, confirm, setConfirm, bulkError, busy, bulk, off, pinAll, menu, setMenu, closeMenu,
    pinnedDoc, activePreset, pick, save, filtered, clearFilters, shown, isLocal: !!local,
  };
}
export type ListPage<R extends ListRow> = ReturnType<typeof useListPage<R>>;

/** A page-built list in the shape useList returns: always loaded, one page, counts from the page. */
function localList(l: LocalList<ListRow>, view: View) {
  return {
    rows: l.rows as unknown as ReturnType<typeof useList>['rows'], counts: l.counts, total: l.total, groups: l.groups, at: 0, error: l.error,
    pending: l.pending, shownKey: l.pending && !l.rows.length ? null : listKey(view), shownView: view, more: () => Promise.resolve(), reload: () => { l.reload(); return Promise.resolve(); },
    state: 'current' as const,
  };
}


/** The list section: filter bar (search, saved views, state counters, tools, selection bar), banners, the rows and the row menu. */
export function ListPagePanel<R extends ListRow, F extends Family, G extends string>(p: {
  page: ListPage<R>;
  /** Plural noun of the error banners: "Tasks did not load", "Pinned tasks did not load". */
  noun: string;
  placeholder: string;
  family: F;
  groups: readonly (readonly [G, string])[];
  sorts: readonly SortField[];
  /** Tools after Select, Group and Sort. */
  tools?: ComponentChildren;
  /** Filters applied from outside the query (Delivery: the picked pace day), each removable. */
  chips?: Chip[]; onRemoveChip?: (c: Chip) => void;
  /** The state counters' accessible name; `<noun> by state` by default. */
  countersLabel?: string;
  /** Counts per state that are unknown (`?`, never 0): some source data did not load. */
  countsUnknown?: boolean;
  /** Selection bar actions after Pin and Unpin; built only in selection mode. */
  actions: () => BarActions;
  /** Row menu items after Open, Pin, Select, Copy code and Copy link. */
  menu: (r: R) => MenuItems;
  /** A pending confirmation, shown above the banners. */
  confirm?: ComponentChildren;
  /** The empty state when nothing loaded and nothing failed. */
  empty: ComponentChildren;
  listLabel: string;
  title: (r: R) => string;
  /** A group header's icon, from the grouping and the group key. */
  groupIcon: (group: string, key: string) => IconName;
  /** A group header's tone (Delivery: the stage's); neutral by default. */
  groupTone?: (group: string, key: string) => Tone;
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
    <ListFrame pending={list.pending} bar={<FilterBar query={g.query} placeholder={p.placeholder} onQuery={g.setQuery} shown={g.shown} total={list.total} chips={p.chips} onRemove={p.onRemoveChip}
      extra={<Presets system={views.list.filter(v => v.system).map(v => v.name)} presets={views.list.filter(v => !v.system).map(v => v.name)} active={g.activePreset} onPick={g.pick}
        onSave={g.save} onUnpin={views.remove} />}
      counters={<StateCounters family={p.family} label={p.countersLabel ?? `${p.noun} by state`} pending={list.pending} counts={(p.countsUnknown ? Object.fromEntries(Object.keys(list.counts).map(k => [k, null])) : list.counts) as any} active={g.states as StateOf<F>[]} onAll={() => set({ state: [], preset: undefined })}
        onToggle={(x: StateOf<F>) => { const s = String(x); set({ state: g.states.includes(s) ? g.states.filter(y => y !== s) : [...g.states, s], preset: undefined }); }} />}
      tools={<>
        <SelectToggle on={selecting} onClick={() => selecting ? g.leave() : g.setSelectOn(true)} />
        <GroupControl value={g.group as G} options={p.groups} onChange={x => set({ group: x, preset: undefined })} />
        <SortControl value={g.sort} fields={p.sorts} onChange={v => set({ sort: v, preset: undefined })} />
        {p.tools}
      </>}
      selection={selecting && <SelectBar count={chosen.length} total={displayed.length} onAll={() => { g.setSelectOn(true); g.setConfirm(null); set({ sel: displayed.map(r => r.id) }); }} onNone={() => { g.setSelectOn(true); g.setConfirm(null); set({ sel: [] }); }} onExit={g.leave} actions={[
        { label: 'Pin', icon: 'pin', onClick: () => g.pinAll(true), disabledReason: chosen.length && chosen.every(r => pins.has(r.id)) ? 'All pinned' : undefined },
        { label: 'Unpin', icon: 'pin-off', onClick: () => g.pinAll(false), disabledReason: chosen.some(r => pins.has(r.id)) ? undefined : 'None pinned' },
        ...p.actions(),
      ]} />} />}>
      {p.confirm}
      {g.bulkError && <ErrorBanner text={g.bulkError} />}
      {list.error && <ErrorBanner text={`${p.noun} did not load: ${list.error}`} onRetry={list.reload} />}
      {pinnedDoc.error && <ErrorBanner text={`Pinned ${p.noun.toLowerCase()} did not load: ${pinnedDoc.error}`} onRetry={pinnedDoc.reload} />}
      {!displayed.length && (list.shownKey === null || list.pending || (!g.isLocal && pins.ids.length > 0 && pinnedDoc.data === null && !pinnedDoc.error)) ? <Skeleton rows={8} />
        : !displayed.length ? (list.error || pinnedDoc.error ? null : p.empty)
        : <VirtualList items={g.items} getKey={i => i.kind === 'group' ? 'g:' + i.key : i.r.id} rowHeight={ROW} follow="top" label={p.listLabel} onEnd={list.more} endKey={g.endKey}
          columns={`${selecting ? '1.25rem ' : ''}1.25rem minmax(0,1fr) auto`} rowClass={i => i.kind === 'group' ? 'grp' : `${i.r.id === p.focused ? 'sel' : ''}${sel.includes(i.r.id) ? ' checked' : ''}${i.pinned ? ' pin-row' : ''}`}
          onRowMenu={(i, at) => { if (i.kind === 'row') g.setMenu({ r: i.r, at }); }}
          render={i => i.kind === 'group'
            ? <GroupHeader icon={i.icon ?? p.groupIcon(g.rowsGroup, i.key)} tone={i.icon ? undefined : p.groupTone?.(g.rowsGroup, i.key)} label={i.label} count={i.count} open={!g.closed.has(i.key)} onToggle={() => g.toggleGroup(i.key)} />
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
