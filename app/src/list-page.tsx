// The list-view pattern shared by Tasks and Conversations (docs/design-system/patterns/list-view.md): saved views (OR-33),
// pins read by id (OR-12), grouping with collapse, selection mode and the row menu (OR-37). A page keeps its rows, facts,
// actions, server kind and fields; useListPage holds the state and ListPagePanel renders the list section.
import { createEffect, createMemo, createSignal, onSettled, Show, untrack, type Accessor } from 'solid-js';
import type { JSX } from '@solidjs/web';
import {
  ListFrame, FilterBar, Presets, StateCounters, GroupHeader, GroupControl, SortControl, VirtualList, ContextMenu, SelectBar, SelectToggle, RowPin, RowCheck, copyText, actions,
  EntityCode, ErrorBanner, type EntityKind, Skeleton, catalogueRow, type IconName, type Family, type StateOf, type Chip, type Tone, type SortField,
} from '../../packages/ui/src/index.ts';
import { online } from './api.ts';
import { useList, useOp, type Kind } from './store.ts';
import { compareKeys, closedGroups, flipGroup } from './lists.ts';
import { listKey, formatView, parseView, type View, type ViewName } from '../../packages/view-url/view-url.ts';
import { useUrlView, useSearchBox } from './url.ts';
import { usePins, useViews, listGroups, setListGroups, setNavCounter, type SavedView } from './prefs.ts';
import { navCounted } from './nav-counts.ts';
import { NAV_COUNTER_DEFAULTS } from './system-views.ts';

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
const PINNED = '\u0000pinned'; // group key of the pinned rows; never a server group key

export type ListPageOptions = {
  /** URL view name; also the pins and saved-views list name. */
  view: ViewName;
  /** The kind of entity a row is: its code opens that entity's card (EntityCode). */
  entity: EntityKind;
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
  /** The pin read's filter (the request names the read). */
  pinRead?: (v: View) => { filter: Record<string, unknown> };
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
  const url = useUrlView(o.view);
  // A new query ends the preset that was active when the person typed it. A preset picked while the debounced query waits
  // stays: the commit reads the URL as it is then (url.ts) and leaves a different preset alone.
  let typedPreset: string | undefined;
  const [query, setText] = useSearchBox(() => url.view.q, q => {
    const now = parseView(location.pathname + location.search)?.preset;
    url.set({ q: q || undefined, ...(now === typedPreset ? { preset: undefined } : {}) }, 'type');
  });
  const setQuery = (t: string) => { typedPreset = untrack(() => url.view.preset); setText(t); };
  const pins = usePins(o.view);
  const views = useViews(o.view, o.system);
  // A page passes `local` for its whole life or never.
  const localMemo = o.local ? createMemo(() => o.local!(url.view)) : null;
  const list = localMemo ? localList(localMemo, () => url.view) : useList(o.kind!, () => url.view);
  const group = createMemo(() => url.view.group ?? o.defaultGroup);
  const sort = createMemo(() => url.view.sort ?? o.defaultSort);
  const states = createMemo(() => url.view.state ?? []);
  // Rows on screen may still be the previous list (pending): headers and chips follow that list's own grouping.
  const rowsGroup = createMemo(() => list.shownView.group ?? o.defaultGroup);
  const rows = createMemo(() => list.rows as unknown as R[]);
  const sel = createMemo(() => url.view.sel ?? []);
  const toggleSel = (x: string) => {
    const cur = untrack(sel);
    url.set({ sel: cur.includes(x) ? cur.filter(i => i !== x) : [...cur, x] });
  };
  // Selection mode (OR-37): a checkbox column left of the state, the action bar under the tools.
  const [selectOn, setSelectOn] = createSignal(false, { ownedWrite: true });
  const selecting = createMemo(() => selectOn() || sel().length > 0);
  const [confirm, setConfirm] = createSignal<R[] | null>(null, { ownedWrite: true });
  const [bulkError, setBulkError] = createSignal<string | null>(null, { ownedWrite: true });
  const [busy, setBusy] = createSignal(false);
  const leave = () => { setSelectOn(false); setConfirm(null); setBulkError(null); url.set({ sel: undefined }); };
  const [menu, setMenu] = createSignal<{ r: R; at: { x: number; y: number } } | null>(null);
  const closeMenu = () => setMenu(null);
  // A switch to another list by any path ends selection mode and a pending confirmation.
  createEffect(() => o.listId?.(url.view), () => { setSelectOn(false); setConfirm(null); setBulkError(null); }, { defer: true });
  // Pinned rows stay in the list whatever the filter, search or grouping (OR-12): they are read by id with the page's pin
  // filter and live through row changes; the filtered page supplies the rest.
  const pinnedDoc = useOp('list_membership', () => {
    if (!pins.ids.length || localMemo) return null;
    const pinRead = o.pinRead?.(url.view) ?? { filter: {} };
    return { ids: pins.ids.slice(0, 1000), lists: [{ name: o.kind!, filter: pinRead.filter, group: 'none', sort: sort() }] };
  });
  const byId = createMemo(() => {
    const pinRows = pinnedDoc.data?.lists[0]?.rows as R[] | undefined;
    return new Map(((localMemo?.().all as R[] | undefined) ?? pinRows ?? []).map(r => [r.id, r]));
  });
  // The page supplies a just-pinned row while its independent read is pending. Its grouping key is irrelevant here.
  const pinned = createMemo(() =>
    pins.ids.map(x => byId().get(x) ?? rows().find(r => r.id === x))
      .filter((r): r is R => !!r).sort((a, b) => compareKeys(a.sortKey.slice(1), b.sortKey.slice(1))),
  );
  const rest = createMemo(() => rows().filter(r => !pins.has(r.id)));
  const displayed = createMemo(() => [...pinned(), ...rest()]);
  // Collapsed groups: the page's defaults for the grouping on screen with the person's flips (prefs listGroups), read in
  // render, so the first frame after the preferences load already has them. The Pinned group is one list per view.
  const grouping = createMemo(() => {
    const rg = rowsGroup();
    const context = o.groupsContext?.(list.shownView);
    return context ? `${rg}-${context}` : rg;
  });
  const groupsList = createMemo(() => `${o.view}:${grouping()}`);
  const pinList = `${o.view}:pinned`;
  const closed = createMemo(() => {
    const c = closedGroups(o.closedGroups?.[grouping()] ?? [], listGroups(groupsList()));
    if (listGroups(pinList).includes('pinned')) c.add(PINNED);
    return c;
  });
  const toggleGroup = (key: string) => key === PINNED ? setListGroups(pinList, flipGroup(listGroups(pinList), 'pinned'))
    : setListGroups(untrack(groupsList), flipGroup(listGroups(untrack(groupsList)), key));
  // A project group is labelled with the project's name, as the focus control names it; its key stays the project id.
  const projects = useOp('project_list', () => (rowsGroup() === 'project' ? {} : null));
  const names = createMemo(() => new Map<string, string>((projects.data?.projects ?? []).map(p => [String(p.id), String(p.name ?? p.id)])));
  const label = (key: string, meta: string | null) => o.groupLabel(rowsGroup(), rowsGroup() === 'project' ? names().get(key) ?? meta : meta);
  const layout = createMemo(() => {
    const out: ListItem<R>[] = [];
    const pinRows = pinned(), restRows = rest(), cl = closed(), rg = rowsGroup();
    // Pinned rows come first, as a group of their own (OR-12); each shows its pin before the state (RowPin).
    if (pinRows.length) {
      out.push({ kind: 'group', key: PINNED, label: 'Pinned', count: pinRows.length, icon: 'pin' });
      if (!cl.has(PINNED)) for (const r of pinRows) out.push({ kind: 'row', r, pinned: true });
    }
    let last: string | null = null;
    const shownGroups = new Set<string>();
    for (const r of restRows) {
      const g = String(r.sortKey[0]);
      if (rg !== 'none' && g !== last) {
        shownGroups.add(g);
        const meta = list.groups.find(x => x.key === g);
        out.push({ kind: 'group', key: g, label: label(g, meta?.label ?? null), count: meta?.count ?? 0 });
        last = g;
      }
      if (!cl.has(g)) out.push({ kind: 'row', r });
    }
    // Loaded rows that end inside a collapsed group show no new items, so the list end would stop asking for pages. The
    // collapsed groups after it (the server's groups, in row order) show their headers without reading their rows, up to and
    // with the first open group, whose rows each loaded page keeps asking for (endKey). Collapsed groups at the end (Completed, Cancelled)
    // are not read through.
    const tail = restRows.length && rg !== 'none' ? String(restRows[restRows.length - 1].sortKey[0]) : null;
    let endKey = '';
    if (tail !== null && cl.has(tail)) {
      const at = list.groups.findIndex(x => x.key === tail);
      if (at < 0) endKey = String(rows().length); // the group counts do not know the tail yet: keep loading
      for (const meta of at < 0 ? [] : list.groups.slice(at + 1)) {
        if (shownGroups.has(meta.key)) continue; // a live row placed it already
        out.push({ kind: 'group', key: meta.key, label: label(meta.key, meta.label ?? null), count: meta.count });
        if (!cl.has(meta.key)) { endKey = String(rows().length); break; }
      }
    }
    return { items: out, endKey };
  });
  const chosen = createMemo(() => {
    const s = sel();
    return displayed().filter(r => s.includes(r.id));
  });
  // One operation per row; failures are counted in one banner. The rows that succeeded leave the selection as it is now
  // (not when the operation began), whatever the others did; failed rows stay selected.
  // A bulk action that settles after the page closed leaves the URL alone: set() would write into whatever view is open then.
  let mounted = true;
  onSettled(() => () => { mounted = false; });
  const bulk = async (rs: R[], run: (r: R) => Promise<unknown>, failText: string) => {
    if (untrack(busy) || !rs.length) return;
    setBulkError(null); setBusy(true);
    const r = await Promise.allSettled(rs.map(run));
    setBusy(false);
    const failed = r.filter(x => x.status === 'rejected') as PromiseRejectedResult[];
    if (failed.length) setBulkError(`${failed.length} of ${rs.length} ${failText}: ${failed[0].reason?.message ?? failed[0].reason}`);
    const done = rs.filter((_, i) => r[i].status === 'fulfilled').map(x => x.id);
    if (done.length && mounted) url.set({ sel: untrack(sel).filter(x => !done.includes(x)) });
  };
  const off = createMemo(() => (!online.value ? 'offline' : busy() ? 'Working' : undefined));
  const pinAll = (on: boolean) => { for (const r of untrack(chosen)) if (pins.has(r.id) !== on) pins.toggle(r.id); };
  const select = (r: R) => { setSelectOn(true); setConfirm(null); toggleSel(r.id); };
  // A personal view matches the whole view and wins over a system preset with the same states; a system preset matches the
  // states alone.
  const savedView = o.savedView ?? ((v: View) => v);
  const presetKey = (v: View) => listKey({ ...v, view: o.view });
  const stateKey = (st?: string[]) => listKey({ view: o.view, state: st ?? [] });
  const activePreset = createMemo(() => {
    const v = url.view;
    return views.list.find(x => !x.system && presetKey(savedView(x.view)) === listKey(v))?.name
      ?? (v.state?.length ? views.list.find(x => x.system && stateKey(x.view.state) === stateKey(v.state))?.name : undefined) ?? null;
  });
  // A system preset sets the states only; a personal view restores everything it saved.
  const pick = (n: string) => {
    const v = untrack(() => url.view);
    const p = views.list.find(x => x.name === n)!, sv = savedView(p.view);
    url.set(p.system ? { state: p.view.state ?? [], preset: n } : { state: sv.state ?? [], group: sv.group, sort: sv.sort, project: sv.project ?? [], q: sv.q, ...o.pickExtra?.(sv), preset: n, ...(o.listId && o.listId(sv) !== o.listId(v) ? { sel: undefined } : {}) });
  };
  const save = (n: string) => {
    const v = untrack(() => url.view);
    views.save({ name: n, view: { view: o.view, state: v.state, group: v.group ?? o.saveGroup, sort: v.sort, project: v.project, q: v.q, ...o.saveExtra?.(v) } });
  };
  // The empty state's Clear filters: states, search, projects and the active preset.
  // setQuery('') also drops a typed query not yet committed, which the URL sync alone would leave in the box.
  const filtered = createMemo(() => !!(states().length || url.view.q || url.view.project?.length));
  const clearFilters = () => { setQuery(''); url.set({ state: [], q: undefined, project: [], preset: undefined }); };
  const shown = createMemo(() => {
    if (localMemo) return localMemo().rows.length;
    const st = states();
    return st.length ? st.reduce((n, s) => n + (list.counts[s] ?? 0), 0) : list.total;
  });
  return {
    get view() { return url.view; },
    set: url.set,
    href: url.href,
    get query() { return query(); },
    setQuery,
    pins,
    views,
    list,
    get group() { return group(); },
    get sort() { return sort(); },
    get states() { return states(); },
    get rowsGroup() { return rowsGroup(); },
    get rows() { return rows(); },
    get items() { return layout().items; },
    get endKey() { return layout().endKey; },
    get displayed() { return displayed(); },
    get chosen() { return chosen(); },
    get closed() { return closed(); },
    toggleGroup,
    get sel() { return sel(); },
    get selecting() { return selecting(); },
    setSelectOn,
    select,
    leave,
    get confirm() { return confirm(); },
    setConfirm,
    get bulkError() { return bulkError(); },
    get busy() { return busy(); },
    bulk,
    get off() { return off(); },
    pinAll,
    get menu() { return menu(); },
    setMenu,
    closeMenu,
    pinnedDoc,
    get activePreset() { return activePreset(); },
    pick,
    save,
    get filtered() { return filtered(); },
    clearFilters,
    get shown() { return shown(); },
    isLocal: !!localMemo,
    entity: o.entity,
  };
}
export type ListPage<R extends ListRow> = ReturnType<typeof useListPage<R>>;

/** A page-built list in the shape useList returns: always loaded, one page, counts from the page. */
function localList(l: Accessor<LocalList<ListRow>>, view: Accessor<View>) {
  return {
    get rows() { return l().rows as unknown as ReturnType<typeof useList>['rows']; },
    get counts() { return l().counts; },
    get total() { return l().total; },
    get groups() { return l().groups; },
    at: 0,
    get error() { return l().error; },
    get pending() { return l().pending; },
    get shownKey() { const cur = l(); return cur.pending && !cur.rows.length ? null : listKey(view()); },
    get shownView() { return view(); },
    more: () => Promise.resolve(),
    reload: () => { l().reload(); return Promise.resolve(); },
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
  tools?: JSX.Element;
  /** Filters applied from outside the query (Delivery: the picked pace day), each removable. */
  chips?: Chip[]; onRemoveChip?: (c: Chip) => void;
  /** Optional tooltip / accessible description per system preset name. */
  systemTips?: Record<string, string>;
  /** The state counters' accessible name; `<noun> by state` by default. */
  countersLabel?: string;
  /** Counts per state that are unknown (`?`, never 0): some source data did not load. */
  countsUnknown?: boolean;
  /** Selection bar actions after Pin and Unpin; built only in selection mode. */
  actions: () => BarActions;
  /** Row menu items after Open, Pin, Select, Copy code and Copy link. */
  menu: (r: R) => MenuItems;
  /** A pending confirmation, shown above the banners. */
  confirm?: JSX.Element;
  /** The empty state when nothing loaded and nothing failed. */
  empty: JSX.Element;
  listLabel: string;
  title: (r: R) => string;
  /** A group header's icon, from the grouping and the group key. */
  groupIcon: (group: string, key: string) => IconName;
  /** A group header's tone (Delivery: the stage's); neutral by default. */
  groupTone?: (group: string, key: string) => Tone;
  /** The state icon cell. */
  state: (r: R) => JSX.Element;
  /** Content before the title link. */
  before?: (r: R) => JSX.Element;
  facts: (r: R) => JSX.Element;
  focused?: string;
}) {
  const g = p.page;
  const rowMenu = (r: R): MenuItems => [
    { label: 'Open', icon: actions.open, onClick: () => g.set({ focus: r.id }) },
    { label: g.pins.has(r.id) ? 'Unpin' : 'Pin', icon: g.pins.has(r.id) ? 'pin-off' : 'pin', onClick: () => g.pins.toggle(r.id) },
    { label: g.sel.includes(r.id) ? 'Deselect' : 'Select', icon: 'square', onClick: () => g.select(r) },
    { label: 'Copy code', icon: actions.copy, onClick: () => copyText(r.code ?? r.id) },
    { label: 'Copy link', icon: 'link', onClick: () => copyText(location.origin + formatView({ view: g.view.view, focus: r.id })) },
    ...p.menu(r),
  ];
  return <section class="panel">
    <ListFrame pending={g.list.pending} bar={<FilterBar query={g.query} placeholder={p.placeholder} onQuery={g.setQuery} shown={g.shown} total={g.list.total} chips={p.chips} onRemove={p.onRemoveChip}
      extra={<Presets system={g.views.list.filter(v => v.system).map(v => v.name)} systemTips={p.systemTips} presets={g.views.list.filter(v => !v.system).map(v => v.name)} active={g.activePreset} onPick={g.pick}
        onSave={g.save} onUnpin={g.views.remove} counted={navCounted(g.view.view, g.views.list.map(v => v.name))} onCount={g.view.view in NAV_COUNTER_DEFAULTS ? n => setNavCounter(g.view.view, n) : undefined} />}
      counters={<StateCounters family={p.family} label={p.countersLabel ?? `${p.noun} by state`} pending={g.list.pending} unknown={!g.isLocal && !g.list.at} counts={(p.countsUnknown ? Object.fromEntries(Object.keys(g.list.counts).map(k => [k, null])) : g.list.counts) as Partial<Record<StateOf<F>, number | null>>} active={g.states as StateOf<F>[]} onAll={() => g.set({ state: [], preset: undefined })}
        onToggle={(x: StateOf<F>) => { const s = String(x); g.set({ state: g.states.includes(s) ? g.states.filter(y => y !== s) : [...g.states, s], preset: undefined }); }} />}
      tools={<>
        <SelectToggle on={g.selecting} onClick={() => g.selecting ? g.leave() : g.setSelectOn(true)} />
        <GroupControl value={g.group as G} options={p.groups} onChange={x => g.set({ group: x, preset: undefined })} />
        <SortControl value={g.sort} fields={p.sorts} onChange={v => g.set({ sort: v, preset: undefined })} />
        {p.tools}
      </>}
      selection={g.selecting && <SelectBar count={g.chosen.length} total={g.displayed.length} onAll={() => { g.setSelectOn(true); g.setConfirm(null); g.set({ sel: g.displayed.map(r => r.id) }); }} onNone={() => { g.setSelectOn(true); g.setConfirm(null); g.set({ sel: [] }); }} onExit={g.leave} actions={[
        { label: 'Pin', icon: 'pin', onClick: () => g.pinAll(true), disabledReason: g.chosen.length && g.chosen.every(r => g.pins.has(r.id)) ? 'All pinned' : undefined },
        { label: 'Unpin', icon: 'pin-off', onClick: () => g.pinAll(false), disabledReason: g.chosen.some(r => g.pins.has(r.id)) ? undefined : 'None pinned' },
        ...p.actions(),
      ]} />} />}>
      {p.confirm}
      {g.bulkError && <ErrorBanner text={g.bulkError} />}
      {g.list.error && <ErrorBanner text={g.list.at ? `${p.noun} may be out of date: ${g.list.error}` : `${p.noun} did not load: ${g.list.error}`} onRetry={g.list.reload} />}
      {g.pinnedDoc.error && <ErrorBanner text={`Pinned ${p.noun.toLowerCase()} did not load: ${g.pinnedDoc.error}`} onRetry={g.pinnedDoc.reload} />}
      {!g.displayed.length && (g.list.shownKey === null || g.list.pending || (!g.isLocal && g.pins.ids.length > 0 && g.pinnedDoc.data === null && !g.pinnedDoc.error)) ? <Skeleton rows={8} />
        : !g.displayed.length ? (g.list.error || g.pinnedDoc.error ? null : p.empty)
        : <VirtualList keepRowFocus items={g.items} getKey={i => i.kind === 'group' ? 'g:' + i.key : i.r.id} rowHeight={catalogueRow()} follow="top" label={p.listLabel} onEnd={g.list.more} endKey={g.endKey}
          columns={`${g.selecting ? '1.25rem ' : ''}1.25rem minmax(0,1fr) auto`} rowClass={i => i.kind === 'group' ? 'grp' : `${i.r.id === p.focused ? 'sel' : ''}${g.sel.includes(i.r.id) ? ' checked' : ''}${i.pinned ? ' pin-row' : ''}`}
          onRowMenu={(i, at) => { if (i.kind === 'row') g.setMenu({ r: i.r, at }); }}
          render={i => i.kind === 'group'
            ? <GroupHeader icon={i.icon ?? p.groupIcon(g.rowsGroup, i.key)} tone={i.icon ? undefined : p.groupTone?.(g.rowsGroup, i.key)} label={i.label} count={i.count} open={!g.closed.has(i.key)} onToggle={() => g.toggleGroup(i.key)} />
            : <>
              <Show when={g.selecting}><span class="lead"><RowCheck checked={g.sel.includes(i.r.id)} label={'Select ' + p.title(i.r)} onToggle={() => g.select(i.r)} /></span></Show>
              <Show when={i.pinned}><span class="lead"><RowPin label={p.title(i.r)} onUnpin={() => {
                // In selection mode the pin selects like the rest of the row; otherwise it unpins and focus follows the row to its group.
                if (g.selecting) return g.select(i.r);
                g.pins.toggle(i.r.id); requestAnimationFrame(() => document.querySelector<HTMLElement>(`.vl-row[data-key="${CSS.escape(i.r.id)}"] .row-link`)?.focus({ preventScroll: true }));
              }} /></span></Show>
              <span class="lead">{p.state(i.r)}</span>
              <span class="tt">{p.before?.(i.r)}<a class="title row-link" href={g.href({ focus: i.r.id })} aria-keyshortcuts="P X"
                onClick={e => { e.preventDefault(); if (g.selecting || e.shiftKey) g.select(i.r); else g.set({ focus: i.r.id }); }}
                onKeyDown={e => {
                  if (e.altKey || e.ctrlKey || e.metaKey) return;
                  if (e.key === 'p') { e.preventDefault(); g.pins.toggle(i.r.id); }
                  else if (e.key === 'x') { e.preventDefault(); g.select(i.r); }
                  // The Menu key and Shift+F10 fire contextmenu on the row: onRowMenu opens the menu.
                }}>{p.title(i.r)}</a><EntityCode code={i.r.code} id={i.r.id} kind={g.entity} /></span>
              <span class="rz">{p.facts(i.r)}</span>
            </>} />}
    </ListFrame>
    <Show when={g.menu} keyed>{m => <ContextMenu at={m.at} label={p.title(m.r)} items={rowMenu(m.r)} onClose={g.closeMenu} />}</Show>
  </section>;
}
