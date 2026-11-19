// Patterns: compositions of controls for recurring jobs. Specs: docs/design-system/patterns/*.md.
import { createContext, render, type ComponentChildren } from 'preact';
import { useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks';
import { Button, Icon, Ago, EntityCode, Empty, StateBadge, Num, AgeChip, FilterBar, Presets, StateCounters, GroupHeader, GroupControl, NavRail, ContextMenu, SelectBar, useTick, SelectToggle, RowCheck, RowPin, copyText, TooltipLayer, EntityCardLayer, HarnessMark, StageChip, Spotlight, sectionProvider, spotlightMemory, type SpotlightProvider, type Chip, type NavItem } from './controls.tsx';
import { VirtualList } from './virtual-list.tsx';
import { matchSearch } from './search.ts';
import { states, actions, harnesses, type StateOf, type StateDef, type Harness, type Stage, type IconName, type Tone } from './registry.ts';
import tokens from '../tokens.json';

const ROW = parseInt(tokens.size.row);
export const ROW2 = parseInt(tokens.size['row-2']);
/** A catalogue row in the control column: one line, the title then its code (2026-11-20 decision); 44 px on touch (principles: narrow work areas). */
export const catalogueRow = () => typeof matchMedia === 'function' && matchMedia('(pointer: coarse)').matches ? parseInt(tokens.size['row-phone']) : ROW;

/* ---------- Task list view (patterns/list-view.md) ---------- */

export type TaskRow = {
  id: number; title: string; project: string; state: StateOf<'task'>; reason?: string; agent: Harness; host: string;
  started: number; cost: number | null; est: boolean; pinned?: boolean; priority: 0 | 1 | 2; stage?: Stage;
};
export type GroupBy = 'priority' | 'state' | 'project' | 'agent' | 'host' | 'none';
/** The list view's system presets (Tasks): they set the states only. */
const SYSTEM_PRESETS = ['Needs me', 'Running'];
type View = { name: string; chips: Chip[]; states: StateOf<'task'>[]; group: GroupBy };
type Row = { kind: 'group'; key: string; label: string; icon: IconName; tone: Tone; count: number } | { kind: 'task'; t: TaskRow };
const GROUPS: { id: GroupBy; label: string }[] = [
  { id: 'priority', label: 'Priority' }, { id: 'state', label: 'State' }, { id: 'project', label: 'Project' },
  { id: 'agent', label: 'Agent' }, { id: 'host', label: 'Host' }, { id: 'none', label: 'None' }];

/** Filter bar + views + state counters + grouping + virtual list. View state lives here; a real page keeps it in the URL. */
export function TaskListView({ items, now, onPin, onOpen, selected, height, row = ROW }: {
  items: TaskRow[]; now: number; onPin: (id: number) => void; onOpen?: (t: TaskRow) => void; selected?: number; height?: string;
  /** Row height: ROW2 for a catalogue in the control column. */
  row?: number;
}) {
  const [views, setViews] = useState<View[]>([
    { name: 'Needs me', chips: [], states: ['waiting', 'failed'], group: 'project' },
    { name: 'Running', chips: [], states: ['in_progress', 'queued'], group: 'project' },
    { name: 'cs', chips: [{ field: 'Project', value: 'cs' }], states: [], group: 'state' },
  ]);
  const [view, setView] = useState<View>({ name: '', chips: [], states: [], group: 'project' });
  const [active, setActive] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [closed, setClosed] = useState<string[]>(['completed', 'cancelled']);
  // Selection mode (OR-37): a checkbox column left of the state; the action bar replaces group and sort.
  const [selectOn, setSelectOn] = useState(false), [sel, setSel] = useState<number[]>([]);
  const selecting = selectOn || sel.length > 0;
  const leave = () => { setSelectOn(false); setSel([]); };
  const toggle = (id: number) => { setSelectOn(true); setSel(x => x.includes(id) ? x.filter(i => i !== id) : [...x, id]); };
  const [menu, setMenu] = useState<{ t: TaskRow; at: { x: number; y: number } } | null>(null);
  const edit = (v: Partial<View>) => { setView({ ...view, ...v }); setActive(null); };

  const byChips = items.filter(t => view.chips.every(c => c.field === 'Project' ? t.project === c.value : c.field === 'Agent' ? t.agent === c.value : t.host === c.value)
    && matchSearch(query, `${t.title} T-FLT-${t.id}`, { agent: t.agent, host: t.host, project: t.project, state: t.state }));
  // ponytail: counts and grouping computed client-side over loaded rows; server aggregates when lists are paged.
  const counts = useMemo(() => { const c: Partial<Record<StateOf<'task'>, number>> = {}; for (const s of Object.keys(states.task) as StateOf<'task'>[]) c[s] = 0; for (const t of byChips) c[t.state]!++; return c; }, [byChips]);
  const shown = view.states.length ? byChips.filter(t => view.states.includes(t.state)) : byChips;
  // Pinned tasks stay whatever the filter or search (OR-12); the filter narrows the rest.
  const pinned = items.filter(t => t.pinned), unpinned = shown.filter(t => !t.pinned);

  const rows: Row[] = useMemo(() => {
    // Pinned rows come first as a group of their own (OR-12); each shows its pin before the state (RowPin).
    const top: Row[] = pinned.length ? [{ kind: 'group', key: 'g-\u0000pinned', label: 'Pinned', icon: 'pin', tone: 'neutral', count: pinned.length }, ...(closed.includes('\u0000pinned') ? [] : pinned.map(t => ({ kind: 'task' as const, t })))] : [];
    if (view.group === 'none') return [...top, ...unpinned.map(t => ({ kind: 'task' as const, t }))];
    const g = view.group;
    const keyOf = (t: TaskRow) => String(g === 'priority' ? t.priority : g === 'state' ? t.state : g === 'project' ? t.project : g === 'agent' ? t.agent : t.host);
    const order = g === 'priority' ? ['0', '1', '2'] : g === 'state' ? ['waiting', 'failed', 'in_progress', 'queued', 'held', 'paused', 'completed', 'cancelled'] : [...new Set(unpinned.map(keyOf))].sort();
    const head = (k: string): { label: string; icon: IconName; tone: Tone } => {
      if (g === 'priority') return { label: `Priority ${k}`, icon: k === '0' ? 'triangle-alert' : 'list-ordered', tone: k === '0' ? 'danger' : 'neutral' };
      if (g === 'state') { const d = states.task[k as StateOf<'task'>] as StateDef; return { label: d.label, icon: d.icon, tone: d.tone }; }
      if (g === 'agent') return { label: harnesses[k as Harness].label, icon: harnesses[k as Harness].icon, tone: 'neutral' };
      return { label: k, icon: g === 'host' ? 'panels-top-left' : 'folder-open', tone: 'neutral' };
    };
    const out: Row[] = [...top];
    for (const k of order) {
      const items = unpinned.filter(t => keyOf(t) === k);
      if (!items.length) continue;
      out.push({ kind: 'group', key: `g-${k}`, count: items.length, ...head(k) });
      if (!closed.includes(k)) for (const t of items) out.push({ kind: 'task', t });
    }
    return out;
  }, [pinned, unpinned, view.group, closed]);

  return (
    <div class="list-view" data-ui="list-view" style={height ? { '--list-height': height } : undefined}>
      <FilterBar query={query} onQuery={setQuery} placeholder="Search tasks" chips={view.chips}
        onRemove={c => edit({ chips: view.chips.filter(x => x.field !== c.field || x.value !== c.value) })} onClear={() => edit({ chips: [], states: [] })}
        shown={shown.length} total={items.length}
        extra={<>
          <Presets system={SYSTEM_PRESETS} presets={views.map(v => v.name).filter(n => !SYSTEM_PRESETS.includes(n))} active={active}
            // A system preset sets the states only (OR-33); a personal view restores all it saved.
            onPick={n => { const v = views.find(x => x.name === n)!; setView(SYSTEM_PRESETS.includes(n) ? { ...view, name: n, states: v.states } : v); setActive(n); }}
            onSave={n => { const v = { ...view, name: n }; setViews([...views.filter(x => x.name !== n), v]); setActive(n); }}
            onUnpin={n => { setViews(views.filter(v => v.name !== n)); if (active === n) setActive(null); }} />
        </>}
        counters={
          <StateCounters family="task" label="Tasks by state" counts={counts} active={view.states} onAll={() => edit({ states: [] })}
            onToggle={s => edit({ states: view.states.includes(s) ? view.states.filter(x => x !== s) : [...view.states, s] })} />}
        tools={<><SelectToggle on={selecting} onClick={() => selecting ? leave() : setSelectOn(true)} />
          <GroupControl value={view.group} options={GROUPS.map(g => [g.id, g.label] as const)} onChange={g => edit({ group: g })} /></>}
        selection={selecting && <SelectBar count={sel.length} total={shown.length} onAll={() => setSel(shown.map(t => t.id))} onNone={() => setSel([])} onExit={leave} actions={[
              { label: 'Pin', icon: 'pin', onClick: () => sel.forEach(id => !items.find(t => t.id === id)?.pinned && onPin(id)) },
              { label: 'Unpin', icon: 'pin-off', onClick: () => sel.forEach(id => items.find(t => t.id === id)?.pinned && onPin(id)) },
              { label: 'Pause', icon: 'pause', onClick: () => {} },
              { label: 'Continue', icon: 'rotate-cw', onClick: () => {} },
              { label: 'Cancel', icon: 'circle-slash', danger: true, onClick: () => {} }]} />} />
      <VirtualList items={rows} getKey={r => r.kind === 'group' ? r.key : String(r.t.id)} rowHeight={row} follow="top" label="Tasks"
        columns={(selecting ? '1.25rem ' : '') + (row === ROW ? '1.25rem minmax(0,1fr) 1.25rem 1.25rem 2.75rem 4.5rem' : '1.25rem minmax(0,1fr) 2.75rem 4.5rem')}
        rowClass={r => r.kind === 'group' ? 'grp' : `${r.t.id === selected ? 'sel' : ''}${sel.includes(r.t.id) ? ' checked' : ''}${r.t.pinned ? ' pin-row' : ''}`}
        onRowMenu={(r, at) => { if (r.kind === 'task') setMenu({ t: r.t, at }); }}
        header={row === ROW ? <>{selecting && <span />}<span /><span>Task</span><span /><span /><span class="r">Age</span><span class="r">Cost</span></> : undefined}
        render={r => r.kind === 'group'
          ? <GroupHeader icon={r.icon} tone={r.tone} label={r.label} count={r.count}
              open={!closed.includes(r.key.slice(2))} onToggle={() => { const k = r.key.slice(2); setClosed(closed.includes(k) ? closed.filter(x => x !== k) : [...closed, k]); }} />
          : <TaskCells t={r.t} now={now} onOpen={onOpen} showPriority={view.group !== 'priority'} two={row !== ROW}
              check={selecting ? sel.includes(r.t.id) : undefined} onSelect={() => toggle(r.t.id)} selecting={selecting} onPin={() => onPin(r.t.id)}
              />} />
      {menu && <ContextMenu at={menu.at} label={menu.t.title} onClose={() => setMenu(null)} items={[
        { label: 'Open', icon: actions.open, onClick: () => onOpen?.(menu.t) },
        { label: menu.t.pinned ? 'Unpin' : 'Pin', icon: menu.t.pinned ? 'pin-off' : 'pin', onClick: () => onPin(menu.t.id) },
        { label: sel.includes(menu.t.id) ? 'Deselect' : 'Select', icon: 'square', onClick: () => toggle(menu.t.id) },
        { label: 'Copy code', icon: actions.copy, onClick: () => copyText(`T-FLT-${menu.t.id}`) },
        { label: 'Copy link', icon: 'link', onClick: () => copyText(`#/tasks/${menu.t.id}`) },
        { label: 'Pause', icon: 'pause', onClick: () => {} }]} />}
    </div>
  );
}

function TaskCells({ t, now, onPin, onOpen, showPriority, two, check, selecting, onSelect }: { t: TaskRow; now: number; onPin: () => void; onOpen?: (t: TaskRow) => void; showPriority: boolean; two: boolean;
  check?: boolean; selecting: boolean; onSelect: () => void }) {
  // OR-22: state icon leftmost; ID beside the title; agent mark only while running; delivery stage as a chip.
  // A two-line catalogue row keeps the marks on the second line, so the title has the whole first line (OR-21).
  const marks = <>{t.state === 'in_progress' && <HarnessMark h={t.agent} />}{t.stage && <StageChip stage={t.stage} />}</>;
  return <>
    {check !== undefined && <span class="lead"><RowCheck checked={check} label={'Select ' + t.title} onToggle={onSelect} /></span>}
    {t.pinned && <span class="lead"><RowPin label={t.title} onUnpin={selecting ? onSelect : onPin} /></span>}
    <span class="lead"><StateBadge family="task" state={t.state} reason={t.reason} compact /></span>
    <span class="tt">{showPriority && t.priority === 0 && <span class="p0" aria-label="priority 0">p0</span>}<a class="title row-link" href={`#/tasks/${t.id}`} aria-keyshortcuts="P X"
      onClick={e => { if (selecting || e.shiftKey) { e.preventDefault(); onSelect(); } else if (onOpen) { e.preventDefault(); onOpen(t); } }}
      onKeyDown={e => {
        if (e.altKey || e.ctrlKey || e.metaKey) return;
        if (e.key === 'p') { e.preventDefault(); onPin(); } else if (e.key === 'x') { e.preventDefault(); onSelect(); }
      }}>{t.title}</a><EntityCode code={`T-FLT-${t.id}`} id={`00000000-0000-4000-8000-${String(t.id).padStart(12, '0')}`} />{two && marks}</span>
    {!two && <><span>{t.state === 'in_progress' && <HarnessMark h={t.agent} />}</span><span>{t.stage && <StageChip stage={t.stage} />}</span></>}
    <span class="r"><AgeChip t={t.started} alertAfter={t.state === 'in_progress' ? 2 * 3600 : undefined} alertLabel="running too long" /></span>
    <span class="r"><Num m={t.cost === null ? { value: null, basis: 'unavailable', reason: 'no meter' } : { value: t.cost, basis: t.est ? 'estimated' : 'measured' }} kind="money" rise="bad" /></span>
  </>;
}

/* ---------- App shell (patterns/app-shell.md) ---------- */

const StatusSlot = createContext<HTMLElement | null>(null);
/**
 * A page's status (its offline marker, a data-age chip) in the shell header after the section title (OR-43), so a page
 * needs no head row of its own for it and nothing moves when it appears (OR-23). One page at a time fills it.
 */
export function ShellStatus({ children }: { children: ComponentChildren }) { return useHeaderSlot(StatusSlot, children); }
/** Renders children into a shell-header slot while the page is mounted. */
function useHeaderSlot(ctx: typeof StatusSlot, children: ComponentChildren) {
  const slot = useContext(ctx);
  useLayoutEffect(() => { if (slot) render(<>{children}</>, slot); });
  useLayoutEffect(() => () => { if (slot) render(null, slot); }, [slot]);
  return null;
}
const ActionsSlot = createContext<HTMLElement | null>(null);
/**
 * A section's own actions (Conversations: New conversation) in the shell header, right after the section title (OR-43),
 * so the control column needs no head row for them. Icon buttons; at phone width they stay. One page at a time fills it.
 */
export function ShellActions({ children }: { children: ComponentChildren }) { return useHeaderSlot(ActionsSlot, children); }

export function AppShell({ nav, current, onGo, waiting, children, pins, onPins, search = [], searchIdle, user, widths: stored, onWidth, collapsed, onCollapsed, ready = true, focus, title, privateReady = true, privateGeneration = 0 }: {
  nav: NavItem[]; current: string; onGo: (id: string) => void; waiting: number; children: ComponentChildren;
  /** Spotlight providers after the built-in sections one (OR-32): codes, tasks, projects, hosts in the app. */ search?: SpotlightProvider[];
  /** What the palette shows while its query is empty, above the recent items (the app's focus presets). */ searchIdle?: ComponentChildren;
  /** The project focus chip (OR-34), on the right of the shell header (OR-43). */ focus?: ComponentChildren;
  /** The section title in the shell header; default: the current section's label. */ title?: string;
  /** The person's pinned sections (person settings); without them the shell keeps its own demo pins. */ pins?: string[]; onPins?: (ids: string[]) => void;
  /** The signed-in person, shown at the bottom of the navigation with a Logout menu, and the installed version. */ user?: { name: string; onLogout: () => void; version?: VersionInfo };
  /** Column widths in px by key (`nav`, `control`) and the collapsed navigation: person settings (OR-41, OR-42). onWidth(key, 0) resets.
   *  Without them the shell keeps its own (book and mockups). */
  privateReady?: boolean; privateGeneration?: number;
  widths?: Record<string, number>; onWidth?: (key: string, px: number) => void; collapsed?: boolean; onCollapsed?: (on: boolean) => void;
  /** False until those settings are read: the shell stays hidden, so nothing jumps when they arrive (OR-23). */ ready?: boolean;
}) {
  const [own, setOwn] = useState<string[]>(['tasks', 'hosts']);
  const pinned = pins ?? own, setPinned = onPins ?? setOwn;
  const [ownMini, setOwnMini] = useState(false), [ownWidths, setOwnWidths] = useState<Record<string, number>>({});
  const mini = collapsed ?? ownMini, widths = stored ?? ownWidths;
  const ownSet = useRef((k: string, px: number) => setOwnWidths(w => { const n = { ...w }; if (px) n[k] = px; else delete n[k]; return n; })).current;
  const setWidth = onWidth ?? ownSet;
  const [spot, setSpot] = useState<number | null>(null), [slot, setSlot] = useState<HTMLElement | null>(null), [acts, setActs] = useState<HTMLElement | null>(null);
  // The palette's query, results, highlighted row and recent items, kept while it is closed: per tab and access generation, in memory only.
  const spotMem = useRef({ gen: privateGeneration, m: spotlightMemory() });
  if (spotMem.current.gen !== privateGeneration) spotMem.current = { gen: privateGeneration, m: spotlightMemory() };
  useEffect(() => {
    const k = (e: KeyboardEvent) => { if (privateReady && (e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && !e.repeat && (e.key.toLowerCase() === 'k' || (e.code === 'KeyK' && !/^[a-z]$/i.test(e.key)))) { e.preventDefault(); setSpot(s => s === privateGeneration ? null : privateGeneration); } };
    addEventListener('keydown', k); return () => removeEventListener('keydown', k);
  }, [privateReady, privateGeneration]);
  const toggle = () => (onCollapsed ?? setOwnMini)(!mini);
  // Stored widths are clamped on the way in, so a value an agent or another device wrote never breaks the layout.
  const vars = Object.fromEntries(Object.entries(COLS).filter(([, c]) => typeof widths[c.key] === 'number' && widths[c.key] > 0).map(([n, c]) => [`--size-${n}`, Math.max(c.lo, Math.min(c.hi, widths[c.key])) + 'px']));
  // OR-26: a click anywhere on a row follows the row's link. Controls in the row keep their own action; a text selection is not a click.
  const openRow = (e: MouseEvent) => {
    const t = e.target as HTMLElement, link = t.closest?.('.vl-row, .kv li')?.querySelector<HTMLElement>('.row-link');
    if (!link || t.closest('a,button,input,textarea,select,label') || !(getSelection()?.isCollapsed ?? true)) return;
    link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, metaKey: e.metaKey, ctrlKey: e.ctrlKey, shiftKey: e.shiftKey }));
  };
  return (
    <Widths.Provider value={setWidth}><div class={`shell${ready ? '' : ' pending'}`} data-ui="app-shell" onClick={openRow} style={vars}>
      <a class="skip" href="#main" onClick={e => { e.preventDefault(); document.getElementById('main')?.focus(); }}>Skip to content</a>
      <NavRail items={nav} pinned={pinned} current={current} onGo={onGo} mini={mini} onPins={setPinned}
        head={<><span class="brand-mark" role="img" aria-label="Fleet" /><b class="brand">Fleet</b><span class="grow" />
          {waiting > 0 && current !== 'overview' && <a class="nav-count t-attention waiting" href="#/overview" onClick={e => { e.preventDefault(); onGo('overview'); }}>{waiting} need you</a>}</>}
        foot={user && <UserBlock {...user} />} />
      <div class="shell-col">
        {!mini && <Divider name="rail" />}
        {/* OR-43: one header over the control column and the work area, whatever page is below: the collapse toggle (OR-42),
            the section title and the section's actions (ShellActions) on the left; the focus chip and search on the right. A right-panel toggle goes last, after
            search, once a right panel exists (app-shell.md); until then nothing is rendered for it. */}
        <header class="shell-h" data-ui="shell-header">
          <span class="nav-toggle"><Button label={mini ? 'Expand navigation' : 'Collapse navigation'} icon={mini ? 'panel-left-open' : 'panel-left-close'} iconOnly pressed={mini} onClick={toggle} /></span>
          <h1 class="shell-title">{title ?? nav.find(n => n.id === current)?.label ?? ''}</h1>
          <span class="shell-actions" ref={setActs} />
          <span class="shell-status" ref={setSlot} />
          <span class="grow" />
          {focus}
          <button type="button" class="btn quiet icon-only shell-search" aria-label="Search" aria-keyshortcuts="Meta+K Control+K" data-tip="Search ⌘K" aria-disabled={!privateReady || undefined}
            onClick={() => { if (privateReady) setSpot(privateGeneration); }}><Icon name={actions.search} /></button>
        </header>
        <main class="shell-main" id="main" tabIndex={-1}><ActionsSlot.Provider value={acts}><StatusSlot.Provider value={slot}>{children}</StatusSlot.Provider></ActionsSlot.Provider></main>
      </div>
      {spot === privateGeneration && privateReady && <Spotlight key={privateGeneration} providers={[sectionProvider(nav, onGo), ...search]} memory={spotMem.current.m} idle={searchIdle} onClose={() => setSpot(null)} />}
      <TooltipLayer />
      <EntityCardLayer />
    </div></Widths.Provider>
  );
}

/** The resizable columns: CSS variable name, person-setting key and limits in px. */
const COLS: Record<'rail' | 'ctl', { key: string; lo: number; hi: number }> = { rail: { key: 'nav', lo: 160, hi: 400 }, ctl: { key: 'control', lo: 280, hi: 720 } };
const Widths = createContext<(key: string, px: number) => void>(() => {});
/** A column's drag divider (OR-41): drag or arrow keys resize the column on its left, double-click resets it. */
function Divider({ name }: { name: 'rail' | 'ctl' }) {
  const prop = `--size-${name}`, { key, lo, hi } = COLS[name], save = useContext(Widths);
  // The upper limit is also what CSS shows (min(width, 25vw) for the rail, half the split for the list), so nothing unseen is saved.
  const clamp = (el: HTMLElement, w: number) => Math.round(Math.max(lo, Math.min(hi, name === 'rail' ? innerWidth / 4 : el.parentElement!.getBoundingClientRect().width / 2, w)));
  const width = (el: HTMLElement) => (el.previousElementSibling ?? el.parentElement!.previousElementSibling)!.getBoundingClientRect().width;
  const live = (el: HTMLElement, w: number) => el.closest<HTMLElement>('.shell')?.style.setProperty(prop, w + 'px');
  return <div class={`divider divider-${name}`} role="separator" aria-orientation="vertical" aria-label={name === 'rail' ? 'Resize navigation' : 'Resize list'} tabIndex={0}
    onPointerDown={e => {
      if (e.button !== 0) return;
      const el = e.currentTarget as HTMLElement, x0 = e.clientX, w0 = width(el), start = clamp(el, w0);
      let w = start;
      el.setPointerCapture(e.pointerId); e.preventDefault();
      // The drag moves the column live; only its end is saved.
      el.onpointermove = m => { w = clamp(el, w0 + m.clientX - x0); live(el, w); };
      // A press without a move keeps the default width; a cancelled drag keeps where it got to.
      el.onpointerup = el.onpointercancel = () => { el.onpointermove = el.onpointerup = el.onpointercancel = null; if (w !== start) save(key, w); };
    }}
    onDblClick={e => { (e.currentTarget as HTMLElement).closest<HTMLElement>('.shell')?.style.removeProperty(prop); save(key, 0); }}
    // Arrow keys move it live; the release saves, so a held key writes once.
    onKeyDown={e => { if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return; e.preventDefault(); const el = e.currentTarget as HTMLElement; live(el, clamp(el, width(el) + (e.key === 'ArrowLeft' ? -16 : 16))); }}
    onKeyUp={e => { if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return; const el = e.currentTarget as HTMLElement; save(key, clamp(el, width(el))); }} />;
}

/** One part of Fleet in the version overview: the tab, the site's UI, the coordinator, the gateway, each host. */
export type VersionRow = { key: string; part: string; icon: IconName; version: string | null; state: string; tone: Tone; note?: string;
  /** Moments shown in compact form with the exact time on hover (OR-2), e.g. { label: 'last seen', at }; `now` is the present they count from (default: the panel's). */ times?: { label: string; at: number; now?: number }[] };
/**
 * The installed version beside the person (nav-rail.md, Version): the Fleet release, toned when a part needs a look (OR-18).
 * A click shows the overview of every part. When the site serves a newer app than this tab, Update reloads into it at once.
 */
export type VersionInfo = { current: string | null; tone: Tone;
  /** Says in words what the tone shows (WCAG 1.4.1), e.g. "1 part needs a look"; absent when nothing does. */ summary?: string; updateReady: boolean; onUpdate: () => void; updating?: boolean; rows: VersionRow[]; onOpen?: () => void;
  /** The same info as of a moment: while the overview is open it is recounted on a timer, so the mark, states and ages move on together. */ at?: (now: number) => VersionInfo };
/** How often the open version overview recounts its ages and states. */
const VERSION_TICK = 15_000;

/** A floating layer fixed above its opener (OR-14 informational, OR-16): Esc, a click outside, a rail scroll or a resize closes it. */
function useFloating(ref: { current: HTMLElement | null }, focusBack: string) {
  const [open, setOpen] = useState<{ left: number; bottom: number } | null>(null);
  useEffect(() => {
    if (!open) return;
    ref.current?.querySelector<HTMLElement>('.float [role=menuitem], .float button')?.focus();
    // Focus goes back to the first opener that is shown (the version is hidden on the collapsed rail; the avatar is not).
    const back = () => [...(ref.current?.querySelectorAll<HTMLElement>(focusBack) ?? [])].find(e => e.offsetParent !== null)?.focus();
    const close = (e: Event) => {
      if (e instanceof KeyboardEvent) { if (e.key !== 'Escape') return; back(); }
      else if (ref.current?.contains(e.target as Node)) return;
      setOpen(null);
    };
    // Only the rail's own scroll moves the opener; lists scrolling elsewhere (a live feed keeping its anchor) leave it open.
    const gone = () => setOpen(null), rail = ref.current?.closest('.rail');
    document.addEventListener('pointerdown', close); document.addEventListener('keydown', close); rail?.addEventListener('scroll', gone); addEventListener('resize', gone);
    return () => { document.removeEventListener('pointerdown', close); document.removeEventListener('keydown', close); rail?.removeEventListener('scroll', gone); removeEventListener('resize', gone); };
  }, [!!open]);
  // Fixed, so the scrolling rail never clips it (the collapsed rail is narrower than the layer).
  const toggle = (e: Event) => { e.preventDefault(); const r = ((e.currentTarget as HTMLElement).closest('.rail-user') ?? e.currentTarget as HTMLElement).getBoundingClientRect(); setOpen(open ? null : { left: r.left, bottom: innerHeight - r.top + 4 }); };
  return { open, setOpen, toggle };
}

/** The signed-in person at the bottom of the navigation: avatar and name (avatar only when collapsed); a click opens Logout. The version follows. */
function UserBlock({ name, onLogout, version }: { name: string; onLogout: () => void; version?: VersionInfo }) {
  const ref = useRef<HTMLDivElement>(null);
  const menu = useFloating(ref, '.rail-user-b'), panel = useFloating(ref, '.rail-ver, .rail-user-b');
  // One present for the mark and every row (OR-2), recounted every VERSION_TICK while the overview is open; closing clears the timer.
  useTick(VERSION_TICK, !!panel.open);
  const now = Date.now(), v = version?.at ? version.at(now) : version, attention = !!v && (v.updateReady || v.tone !== 'neutral');
  return <div class="rail-user" ref={ref}>
    <button type="button" class="rail-user-b" aria-label={name} aria-haspopup="menu" aria-expanded={!!menu.open} onClick={e => { panel.setOpen(null); menu.toggle(e); }} onContextMenu={e => { panel.setOpen(null); menu.toggle(e); }}>
      <span class={`avatar${attention ? ` dot t-${v!.updateReady ? 'running' : v!.tone}` : ''}`} aria-hidden="true" data-tip={name} data-tip-narrow>{name.slice(0, 1).toUpperCase()}</span><span class="nm">{name}</span>
    </button>
    {v && <button type="button" class={`rail-ver t-${v.tone}`} aria-expanded={!!panel.open} aria-label={`Version ${v.current ?? 'unknown'}${v.summary ? `, ${v.summary}` : ''}: show every part`} data-tip={v.summary ? `Versions: ${v.summary}` : 'Versions'}
      onClick={e => { menu.setOpen(null); if (!panel.open) v.onOpen?.(); panel.toggle(e); }}>{v.current ?? '…'}</button>}
    {v?.updateReady && <span class="rail-upd"><Button label={v.updating ? 'Updating' : 'Update to the new version'} icon="rotate-cw" iconOnly kind="primary" busy={v.updating} onClick={v.onUpdate} /></span>}
    {menu.open && <span class="menu rail-menu up float" role="menu" aria-label={name} style={{ left: menu.open.left + 'px', bottom: menu.open.bottom + 'px' }}>
      {v?.updateReady && <button type="button" role="menuitem" onClick={() => { menu.setOpen(null); v.onUpdate(); }}><Icon name="rotate-cw" />Update</button>}
      {v && <button type="button" role="menuitem" class="menu-ver" onClick={() => { const at = menu.open!; menu.setOpen(null); v.onOpen?.(); panel.setOpen(at); }}><Icon name="info" />Versions</button>}
      <button type="button" role="menuitem" onClick={() => { menu.setOpen(null); onLogout(); }}><Icon name="log-out" />Logout</button>
    </span>}
    {v && panel.open && <VersionPanel v={v} at={panel.open} now={now} />}
  </div>;
}

/** The open version overview; every row counts from the same present. */
function VersionPanel({ v, at, now }: { v: VersionInfo; at: { left: number; bottom: number }; now: number }) {
  return <div class="ver-panel float" data-ui="version-panel" role="group" aria-label="Versions" style={{ left: at.left + 'px', bottom: at.bottom + 'px', '--ver-bottom': at.bottom + 'px' }}>
    {v.updateReady && <div class="ver-update"><span>A newer app is on the site.</span><Button label={v.updating ? 'Updating' : 'Update'} icon="rotate-cw" kind="primary" busy={v.updating} onClick={v.onUpdate} /></div>}
    <table class="ver-table"><tbody>{v.rows.map(r => <tr key={r.key}>
      <th scope="row"><Icon name={r.icon} /><span>{r.part}</span></th>
      <td class="mono">{r.version ?? '—'}</td>
      <td><span class={`ver-state t-${r.tone}`}>{r.state}</span>{r.times?.map(x => <span class="muted" key={x.label}> {x.label} <Ago t={x.at} clockOffsetMs={x.now !== undefined ? x.now - now : 0} /></span>)}{r.note && <span class="muted"> {r.note}</span>}</td>
    </tr>)}</tbody></table>
  </div>;
}

/* ---------- Layout patterns used by pages ---------- */

/** Control column and work area (patterns/list-detail.md). With nothing open the work area says so; when narrow only one of the two shows. */
export function ListDetail({ list, detail, detailKey, idle }: {
  list: ComponentChildren; detail?: ComponentChildren; detailKey?: string | number;
  /** What the work area shows while nothing is open, in place of the default line (the section's own reference page). */
  idle?: ComponentChildren;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => { if (detailKey !== undefined) ref.current?.querySelector<HTMLElement>('[data-ui=detail]')?.focus({ preventScroll: true }); }, [detailKey]);
  return <div ref={ref} data-ui="list-detail" class={`split-view${detail ? ' has-detail' : ''}`}><div class="split-list">{list}</div><Divider name="ctl" />{detail ?? <div class={`work-idle${idle ? '' : ' work-empty'}`}>{idle ?? <Empty text="Pick a row to open it here." />}</div>}</div>;
}
/** Everything waiting for a person, answered in place (patterns/needs-you.md). */
export const NeedsYou = ({ children }: { children: ComponentChildren }) => (
  <section data-ui="needs-you" class="panel wide"><header class="panel-h"><h2>Needs you</h2></header><div class="stack">{children}</div></section>
);
/** A long feed (OR-30): the first `n` items, then a `+K` button that shows the rest in place. Items arrive already ordered. */
export function Latest({ items, n = 3 }: { items: ComponentChildren[]; n?: number }) {
  const [all, setAll] = useState(false);
  const k = items.length - n;
  return <>{all || k <= 0 ? items : items.slice(0, n)}{k > 0 && <Button label={all ? 'Show less' : `+${k}`} kind="quiet" onClick={() => setAll(!all)} />}</>;
}
/** Operational counts and capacity (patterns/ops-summary.md): a stat strip plus side panels. */
export const OpsSummary = ({ children }: { children: ComponentChildren }) => <div data-ui="ops-summary" class="stack">{children}</div>;
/** Generic list frame: filter bar over a virtual list (patterns/list-view.md), for lists other than tasks. */
/** pending: a newer answer is loading; the rows stay, dimmed, under a thin progress bar (fleet-view-urls-and-list-path.md §2). The bar always keeps its height, so the rows never move (OR-23). */
export const ListFrame = ({ bar, children, pending }: { bar: ComponentChildren; children: ComponentChildren; pending?: boolean }) =>
  <div class={`list-view${pending ? ' pending' : ''}`} data-ui="list-view" aria-busy={pending || undefined}>{bar}<div class="list-progress" role={pending ? 'progressbar' : undefined} aria-label={pending ? 'Updating' : undefined} aria-hidden={pending ? undefined : true} />{children}</div>;

/** The work area for a definition kept in git: path and revision, name, tabs, then the tab's sections (patterns/definition-editor.md). */
export function DefinitionEditor({ label, crumb, title, tabs, tab, onTab, onClose, actions, children }: {
  label: string; crumb: string; title: ComponentChildren; tabs: string[]; tab: string; onTab: (t: string) => void; onClose?: () => void;
  actions?: ComponentChildren; children: ComponentChildren;
}) {
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={label}>
    <header class="detail-h">{onClose && <Button label="Close" icon="x" iconOnly onClick={onClose} />}<span class="muted mono">{crumb}</span><span class="grow" />{actions}</header>
    <h2 class="detail-title">{title}</h2>
    <div class="tabs detail-tabs" role="tablist">{tabs.map(x => <button type="button" role="tab" key={x} aria-selected={tab === x} onClick={() => onTab(x)}>{x}</button>)}</div>
    <div class="stack" data-ui="definition-editor" role="tabpanel">{children}</div>
  </article>;
}
