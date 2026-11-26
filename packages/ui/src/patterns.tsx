// Patterns: compositions of controls for recurring jobs. Specs: docs/design-system/patterns/*.md.
import { createContext, createEffect, createMemo, createSignal, For, onSettled, Show, untrack, useContext, type Accessor } from 'solid-js';
import { Portal, type JSX } from '@solidjs/web';
import { SidePanels, ownPanelStore, clampWidth, PanelToggle, ResizeDivider, type SidePanelStore } from './controls/side-panel.tsx';
import { Button, Icon, Ago, EntityCode, Empty, StateBadge, Num, AgeChip, FilterBar, Presets, StateCounters, GroupHeader, GroupControl, NavRail, BottomBar, ContextMenu, SelectBar, useTick, SelectToggle, RowCheck, RowPin, copyText, TooltipLayer, EntityCardLayer, HarnessMark, StageChip, Spotlight, TabBar, sectionProvider, spotlightMemory, type SpotlightProvider, type Chip, type NavItem, DetailHead, RailPinned, RailPinnedStrip, type RailPinnedProps } from './controls.tsx';
import { VirtualList } from './virtual-list.tsx';
import { matchSearch } from './search.ts';
import { states, statesOf, actions, harnesses, barTabs, type StateOf, type StateDef, type Harness, type Stage, type IconName, type Tone } from './registry.ts';
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
export function TaskListView(p: {
  items: TaskRow[]; now: number; onPin: (id: number) => void; onOpen?: (t: TaskRow) => void; selected?: number; height?: string;
  /** Row height: ROW2 for a catalogue in the control column. */
  row?: number;
}) {
  const [views, setViews] = createSignal<View[]>([
    { name: 'Needs me', chips: [], states: ['waiting', 'failed'], group: 'project' },
    { name: 'Running', chips: [], states: statesOf('task', ['working', 'queued']), group: 'project' },
    { name: 'cs', chips: [{ field: 'Project', value: 'cs' }], states: [], group: 'state' },
  ]);
  const [view, setView] = createSignal<View>({ name: '', chips: [], states: [], group: 'project' });
  const [active, setActive] = createSignal<string | null>(null);
  const [query, setQuery] = createSignal('');
  const [closed, setClosed] = createSignal<string[]>(['completed', 'cancelled']);
  // Selection mode (OR-37): a checkbox column left of the state; the action bar replaces group and sort.
  const [selectOn, setSelectOn] = createSignal(false);
  const [sel, setSel] = createSignal<number[]>([]);
  const selecting = createMemo(() => selectOn() || sel().length > 0);
  const leave = () => { setSelectOn(false); setSel([]); };
  const toggle = (id: number) => { setSelectOn(true); setSel(x => x.includes(id) ? x.filter(i => i !== id) : [...x, id]); };
  const [menu, setMenu] = createSignal<{ t: TaskRow; at: { x: number; y: number } } | null>(null);
  const edit = (v: Partial<View>) => { setView(cur => ({ ...cur, ...v })); setActive(null); };
  const rowHeight = () => p.row ?? ROW;

  const byChips = createMemo(() => {
    const v = view(), q = query();
    return p.items.filter(t =>
      v.chips.every(c => c.field === 'Project' ? t.project === c.value : c.field === 'Agent' ? t.agent === c.value : t.host === c.value)
      && matchSearch(q, `${t.title} T-FLT-${t.id}`, { agent: t.agent, host: t.host, project: t.project, state: t.state }),
    );
  });
  // ponytail: counts and grouping computed client-side over loaded rows; server aggregates when lists are paged.
  const counts = createMemo(() => {
    const c: Partial<Record<StateOf<'task'>, number>> = {};
    for (const s of Object.keys(states.task) as StateOf<'task'>[]) c[s] = 0;
    for (const t of byChips()) c[t.state]!++;
    return c;
  });
  const shown = createMemo(() => {
    const st = view().states;
    return st.length ? byChips().filter(t => st.includes(t.state)) : byChips();
  });
  // Pinned tasks stay whatever the filter or search (OR-12); the filter narrows the rest.
  const pinned = createMemo(() => p.items.filter(t => t.pinned));
  const unpinned = createMemo(() => shown().filter(t => !t.pinned));

  const rows = createMemo<Row[]>(() => {
    const pinList = pinned(), rest = unpinned(), cl = closed(), g = view().group;
    // Pinned rows come first as a group of their own (OR-12); each shows its pin before the state (RowPin).
    const top: Row[] = pinList.length
      ? [{ kind: 'group', key: 'g-\u0000pinned', label: 'Pinned', icon: 'pin', tone: 'neutral', count: pinList.length },
         ...(cl.includes('\u0000pinned') ? [] : pinList.map(t => ({ kind: 'task' as const, t })))]
      : [];
    if (g === 'none') return [...top, ...rest.map(t => ({ kind: 'task' as const, t }))];
    const keyOf = (t: TaskRow) => String(g === 'priority' ? t.priority : g === 'state' ? t.state : g === 'project' ? t.project : g === 'agent' ? t.agent : t.host);
    const order = g === 'priority' ? ['0', '1', '2'] : g === 'state' ? ['waiting', 'failed', 'in_progress', 'queued', 'held', 'paused', 'completed', 'cancelled'] : [...new Set(rest.map(keyOf))].sort();
    const head = (k: string): { label: string; icon: IconName; tone: Tone } => {
      if (g === 'priority') return { label: `Priority ${k}`, icon: k === '0' ? 'triangle-alert' : 'list-ordered', tone: k === '0' ? 'danger' : 'neutral' };
      if (g === 'state') { const d = states.task[k as StateOf<'task'>] as StateDef; return { label: d.label, icon: d.icon, tone: d.tone }; }
      if (g === 'agent') return { label: harnesses[k as Harness].label, icon: harnesses[k as Harness].icon, tone: 'neutral' };
      return { label: k, icon: g === 'host' ? 'panels-top-left' : 'folder-open', tone: 'neutral' };
    };
    const out: Row[] = [...top];
    for (const k of order) {
      const groupItems = rest.filter(t => keyOf(t) === k);
      if (!groupItems.length) continue;
      out.push({ kind: 'group', key: `g-${k}`, count: groupItems.length, ...head(k) });
      if (!cl.includes(k)) for (const t of groupItems) out.push({ kind: 'task', t });
    }
    return out;
  });

  return (
    <div class="list-view" data-ui="list-view" style={p.height ? { '--list-height': p.height } : undefined}>
      <FilterBar
        query={query()}
        onQuery={setQuery}
        placeholder="Search tasks"
        chips={view().chips}
        onRemove={c => edit({ chips: view().chips.filter(x => x.field !== c.field || x.value !== c.value) })}
        onClear={() => edit({ chips: [], states: [] })}
        shown={shown().length}
        total={p.items.length}
        extra={
          <Presets
            system={SYSTEM_PRESETS}
            presets={views().map(v => v.name).filter(n => !SYSTEM_PRESETS.includes(n))}
            active={active()}
            // A system preset sets the states only (OR-33); a personal view restores all it saved.
            onPick={n => {
              const v = views().find(x => x.name === n)!;
              setView(SYSTEM_PRESETS.includes(n) ? { ...view(), name: n, states: v.states } : v);
              setActive(n);
            }}
            onSave={n => {
              const v = { ...view(), name: n };
              setViews(vs => [...vs.filter(x => x.name !== n), v]);
              setActive(n);
            }}
            onUnpin={n => {
              setViews(vs => vs.filter(v => v.name !== n));
              if (active() === n) setActive(null);
            }}
          />
        }
        counters={
          <StateCounters
            family="task"
            label="Tasks by state"
            counts={counts()}
            active={view().states}
            onAll={() => edit({ states: [] })}
            onToggle={s => edit({ states: view().states.includes(s) ? view().states.filter(x => x !== s) : [...view().states, s] })}
          />
        }
        tools={
          <>
            <SelectToggle on={selecting()} onClick={() => selecting() ? leave() : setSelectOn(true)} />
            <GroupControl value={view().group} options={GROUPS.map(g => [g.id, g.label] as const)} onChange={g => edit({ group: g })} />
          </>
        }
        selection={
          selecting() ? (
            <SelectBar
              count={sel().length}
              total={shown().length}
              onAll={() => setSel(shown().map(t => t.id))}
              onNone={() => setSel([])}
              onExit={leave}
              actions={[
                { label: 'Pin', icon: 'pin', onClick: () => sel().forEach(id => !p.items.find(t => t.id === id)?.pinned && p.onPin(id)) },
                { label: 'Unpin', icon: 'pin-off', onClick: () => sel().forEach(id => p.items.find(t => t.id === id)?.pinned && p.onPin(id)) },
                { label: 'Pause', icon: 'pause', onClick: () => {} },
                { label: 'Continue', icon: 'rotate-cw', onClick: () => {} },
                { label: 'Cancel', icon: 'circle-slash', danger: true, onClick: () => {} },
              ]}
            />
          ) : undefined
        }
      />
      <VirtualList
        items={rows()}
        getKey={r => r.kind === 'group' ? r.key : String(r.t.id)}
        rowHeight={rowHeight()}
        follow="top"
        label="Tasks"
        columns={(selecting() ? '1.25rem ' : '') + (rowHeight() === ROW ? '1.25rem minmax(0,1fr) 1.25rem 1.25rem 2.75rem 4.5rem' : '1.25rem minmax(0,1fr) 2.75rem 4.5rem')}
        rowClass={r => r.kind === 'group' ? 'grp' : `${r.t.id === p.selected ? 'sel' : ''}${sel().includes(r.t.id) ? ' checked' : ''}${r.t.pinned ? ' pin-row' : ''}`}
        onRowMenu={(r, at) => { if (r.kind === 'task') setMenu({ t: r.t, at }); }}
        header={rowHeight() === ROW ? <>{selecting() && <span />}<span /><span>Task</span><span /><span /><span class="r">Age</span><span class="r">Cost</span></> : undefined}
        render={r => r.kind === 'group'
          ? (
            <GroupHeader
              icon={r.icon}
              tone={r.tone}
              label={r.label}
              count={r.count}
              open={!closed().includes(r.key.slice(2))}
              onToggle={() => {
                const k = r.key.slice(2);
                setClosed(cl => cl.includes(k) ? cl.filter(x => x !== k) : [...cl, k]);
              }}
            />
          )
          : (
            <TaskCells
              t={r.t}
              now={p.now}
              onOpen={p.onOpen}
              showPriority={view().group !== 'priority'}
              two={rowHeight() !== ROW}
              check={selecting() ? sel().includes(r.t.id) : undefined}
              onSelect={() => toggle(r.t.id)}
              selecting={selecting()}
              onPin={() => p.onPin(r.t.id)}
            />
          )}
      />
      <Show when={menu()} keyed>
        {m => (
          <ContextMenu
            at={m.at}
            label={m.t.title}
            onClose={() => setMenu(null)}
            items={[
              { label: 'Open', icon: actions.open, onClick: () => p.onOpen?.(m.t) },
              { label: m.t.pinned ? 'Unpin' : 'Pin', icon: m.t.pinned ? 'pin-off' : 'pin', onClick: () => p.onPin(m.t.id) },
              { label: sel().includes(m.t.id) ? 'Deselect' : 'Select', icon: 'square', onClick: () => toggle(m.t.id) },
              { label: 'Copy code', icon: actions.copy, onClick: () => copyText(`T-FLT-${m.t.id}`) },
              { label: 'Copy link', icon: 'link', onClick: () => copyText(`#/tasks/${m.t.id}`) },
              { label: 'Pause', icon: 'pause', onClick: () => {} },
            ]}
          />
        )}
      </Show>
    </div>
  );
}

function TaskCells(p: {
  t: TaskRow; now: number; onPin: () => void; onOpen?: (t: TaskRow) => void; showPriority: boolean; two: boolean;
  check?: boolean; selecting: boolean; onSelect: () => void;
}) {
  // OR-22: state icon leftmost; ID beside the title; agent mark only while running; delivery stage as a chip.
  // A two-line catalogue row keeps the marks on the second line, so the title has the whole first line (OR-21).
  const marks = () => (
    <>
      <Show when={p.t.state === 'in_progress'}><HarnessMark h={p.t.agent} /></Show>
      <Show when={p.t.stage} keyed>{st => <StageChip stage={st} />}</Show>
    </>
  );
  return (
    <>
      <Show when={p.check !== undefined}>
        <span class="lead"><RowCheck checked={p.check!} label={'Select ' + p.t.title} onToggle={p.onSelect} /></span>
      </Show>
      <Show when={p.t.pinned}>
        <span class="lead"><RowPin label={p.t.title} onToggle={() => (p.selecting ? p.onSelect : p.onPin)()} /></span>
      </Show>
      <span class="lead"><StateBadge family="task" state={p.t.state} reason={p.t.reason} compact /></span>
      <span class="tt">
        <Show when={p.showPriority && p.t.priority === 0}><span class="p0" aria-label="priority 0">p0</span></Show>
        <a
          class="title row-link"
          href={`#/tasks/${p.t.id}`}
          aria-keyshortcuts="P X"
          onClick={e => {
            if (p.selecting || e.shiftKey) { e.preventDefault(); p.onSelect(); }
            else if (p.onOpen) { e.preventDefault(); p.onOpen(p.t); }
          }}
          onKeyDown={e => {
            if (e.altKey || e.ctrlKey || e.metaKey) return;
            if (e.key === 'p') { e.preventDefault(); p.onPin(); }
            else if (e.key === 'x') { e.preventDefault(); p.onSelect(); }
          }}
        >
          {p.t.title}
        </a>
        <EntityCode code={`T-FLT-${p.t.id}`} id={`00000000-0000-4000-8000-${String(p.t.id).padStart(12, '0')}`} />
        <Show when={p.two}>{marks()}</Show>
      </span>
      <Show when={!p.two}>
        <span><Show when={p.t.state === 'in_progress'}><HarnessMark h={p.t.agent} /></Show></span>
        <span><Show when={p.t.stage} keyed>{st => <StageChip stage={st} />}</Show></span>
      </Show>
      <span class="r"><AgeChip t={p.t.started} alertAfter={p.t.state === 'in_progress' ? 2 * 3600 : undefined} alertLabel="running too long" /></span>
      <span class="r">
        <Num
          m={p.t.cost === null ? { value: null, basis: 'unavailable', reason: 'no meter' } : { value: p.t.cost, basis: p.t.est ? 'estimated' : 'measured' }}
          kind="money"
          rise="bad"
        />
      </span>
    </>
  );
}

/* ---------- App shell (patterns/app-shell.md) ---------- */

const StatusSlot = createContext<Accessor<HTMLElement | null>>(() => null);
/**
 * A page's status (its offline marker, a data-age chip) in the shell header after the section title (OR-43), so a page
 * needs no head row of its own for it and nothing moves when it appears (OR-23). One page at a time fills it.
 */
export function ShellStatus(p: { children: JSX.Element }) {
  const slot = useContext(StatusSlot);
  return <Show when={slot()} keyed>{el => <Portal mount={el}>{p.children}</Portal>}</Show>;
}

const ActionsSlot = createContext<Accessor<HTMLElement | null>>(() => null);
/**
 * A section's own actions (Conversations: New conversation) in the shell header, right after the section title (OR-43),
 * so the control column needs no head row for them. Icon buttons; at phone width they stay. One page at a time fills it.
 */
export function ShellActions(p: { children: JSX.Element }) {
  const slot = useContext(ActionsSlot);
  return <Show when={slot()} keyed>{el => <Portal mount={el}>{p.children}</Portal>}</Show>;
}

export function AppShell(p: {
  nav: NavItem[]; current: string; onGo: (id: string) => void; waiting: number; children: JSX.Element;
  /** Spotlight providers after the built-in sections one (OR-32): codes, tasks, projects, hosts in the app. */ search?: SpotlightProvider[];
  /** What the palette shows while its query is empty, above the recent items (the app's focus presets). */ searchIdle?: JSX.Element;
  /** The project focus chip (OR-34), on the right of the shell header (OR-43). */ focus?: JSX.Element;
  /** The section title in the shell header; default: the current section's label. */ title?: string;
  /** The person's pinned sections (person settings); without them the shell keeps its own demo pins. */ pins?: string[]; onPins?: (ids: string[]) => void;
  /** The signed-in person, shown at the bottom of the navigation with a Logout menu, and the installed version. */ user?: { name: string; onLogout: () => void; version?: VersionInfo };
  /** Column and side-panel widths in px by key (`nav`, `control`, `session-context`) and which side panels are open by key
   *  (`nav` is the navigation, open when expanded): person settings (OR-41, OR-42, side-panel.md). onWidth(key, 0) resets.
   *  Without them the shell keeps its own (book and mockups). */
  privateReady?: boolean; privateGeneration?: number;
  widths?: Record<string, number>; onWidth?: (key: string, px: number) => void; panels?: Record<string, boolean>; onPanel?: (key: string, open: boolean) => void;
  /** False until those settings are read: the shell stays hidden, so nothing jumps when they arrive (OR-23). */ ready?: boolean;
  /** Wall mode (Live on a TV, pages/live.md): no navigation and no shell header; the page fills the screen. */ wall?: boolean;
  /** The Pinned zone (nav-rail.md, Pinned): the person's switch and, while it is on, the zone's pins and actions. */
  pinnedZone?: { on: boolean; set: (on: boolean) => void; zone: RailPinnedProps };
}) {
  const [own, setOwn] = createSignal<string[]>([]);
  const pinned = createMemo(() => p.pins ?? own());
  const setPinned = (ids: string[]) => (p.onPins ?? setOwn)(ids);
  // The Pinned zone's switch (user menu, every rail item's menu); read live, so a menu shows the current state.
  const zoneSwitch = () => (p.pinnedZone ? { get on() { return p.pinnedZone!.on; }, set: p.pinnedZone.set } : undefined);
  // Side panels (side-panel.md): the navigation (`nav`) and every right panel share one store of open state and widths.
  const [panelSlot, setPanelSlot] = createSignal<HTMLElement | null>(null, { ownedWrite: true });
  const ownPanels = ownPanelStore(panelSlot);
  const panels: SidePanelStore = {
    isOpen: (k, d) => (p.panels ? p.panels[k] ?? d : ownPanels.isOpen(k, d)),
    setOpen: (k, on) => (p.onPanel ? p.onPanel(k, on) : ownPanels.setOpen(k, on)),
    width: k => (p.widths ? p.widths[k] : ownPanels.width(k)),
    setWidth: (k, px) => (p.onWidth ? p.onWidth(k, px) : ownPanels.setWidth(k, px)),
    slot: panelSlot,
  };
  const mini = createMemo(() => !panels.isOpen('nav', true));

  let spotSeq = 0;
  const [spot, setSpot] = createSignal<{ gen: number; seq: number } | null>(null);
  const [slot, setSlot] = createSignal<HTMLElement | null>(null, { ownedWrite: true });
  const [acts, setActs] = createSignal<HTMLElement | null>(null, { ownedWrite: true });

  // The palette's query, results, highlighted row and recent items, kept while it is closed: per tab and access generation, in memory only.
  let spotMem = { gen: untrack(() => p.privateGeneration ?? 0), m: spotlightMemory() };
  const memoryFor = (gen: number) => {
    if (spotMem.gen !== gen) spotMem = { gen, m: spotlightMemory() };
    return spotMem.m;
  };

  onSettled(() => {
    const k = (e: KeyboardEvent) => {
      const ready = p.privateReady ?? true;
      const gen = p.privateGeneration ?? 0;
      if (ready && (e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && !e.repeat && (e.key.toLowerCase() === 'k' || (e.code === 'KeyK' && !/^[a-z]$/i.test(e.key)))) {
        e.preventDefault();
        setSpot(s => s?.gen === gen ? null : { gen, seq: ++spotSeq });
      }
    };
    addEventListener('keydown', k);
    return () => removeEventListener('keydown', k);
  });

  const toggle = () => panels.setOpen('nav', mini());
  // Stored widths are clamped on the way in, so a value an agent or another device wrote never breaks the layout.
  const vars = createMemo(() => Object.fromEntries(
    Object.entries(COLS).flatMap(([n, c]) => { const w = panels.width(c.key); return typeof w === 'number' && w > 0 ? [[`--size-${n}`, clampWidth(w, c.lo, c.hi) + 'px']] : []; }),
  ));
  // OR-26: a click anywhere on a row follows the row's link. Controls in the row keep their own action; a text selection is not a click.
  const openRow = (e: MouseEvent) => {
    const t = e.target as HTMLElement, link = t.closest?.('.vl-row, .kv li')?.querySelector<HTMLElement>('.row-link');
    if (!link || t.closest('a,button,input,textarea,select,label') || !(getSelection()?.isCollapsed ?? true)) return;
    link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, metaKey: e.metaKey, ctrlKey: e.ctrlKey, shiftKey: e.shiftKey }));
  };

  const openSpot = createMemo(() => {
    const gen = p.privateGeneration ?? 0;
    const s = spot();
    return s && s.gen === gen && (p.privateReady ?? true) ? s : null;
  }, { equals: (a, b) => a?.gen === b?.gen && a?.seq === b?.seq });

  const privateReady = () => p.privateReady ?? true;
  const ready = () => p.ready ?? true;

  return (
    <SidePanels value={panels}>
      <div class={['shell', { pending: !ready() }]} data-ui="app-shell" data-wall={p.wall ? '' : undefined} onClick={openRow} style={vars()}>
        <a class="skip" href="#main" onClick={e => { e.preventDefault(); document.getElementById('main')?.focus(); }}>Skip to content</a>
        <NavRail
          items={p.nav}
          pinned={pinned()}
          current={p.current}
          onGo={p.onGo}
          mini={mini()}
          onPins={setPinned}
          head={
            <>
              <span class="brand-mark" role="img" aria-label="Fleet" />
              <b class="brand">Fleet</b>
              <span class="grow" />
              <Show when={p.waiting > 0 && p.current !== 'home'}>
                <a class="nav-count t-attention waiting" href="#/home" onClick={e => { e.preventDefault(); p.onGo('home'); }}>
                  {p.waiting} need you
                </a>
              </Show>
            </>
          }
          foot={<Show when={p.user}>{u => <UserBlock name={u().name} onLogout={u().onLogout} version={u().version} pinned={zoneSwitch()} />}</Show>}
          zone={<Show when={p.pinnedZone?.on}><RailPinned {...p.pinnedZone!.zone} /></Show>}
          pinnedZone={zoneSwitch()}
        />
        {/* OR-60: below 760 px the bottom bar of five replaces the rail (CSS shows one of them). */}
        <BottomBar items={p.nav} tabs={barTabs} current={p.current} onGo={p.onGo} extra={p.pinnedZone?.on ? done => <RailPinnedStrip {...p.pinnedZone!.zone} onDone={done} /> : undefined} foot={<Show when={p.user}>{u => <UserBlock name={u().name} onLogout={u().onLogout} version={u().version} pinned={zoneSwitch()} />}</Show>} />
        <div class="shell-col">
          <Show when={!mini()}><Divider name="rail" /></Show>
          {/* OR-43: one header over the control column and the work area, whatever page is below: the collapse toggle (OR-42),
              the section title and the section's actions (ShellActions) on the left; the focus chip and search on the right. */}
          <header class="shell-h" data-ui="shell-header">
            <PanelToggle side="left" class="nav-toggle" open={!mini()} label={mini() ? 'Expand navigation' : 'Collapse navigation'} onToggle={toggle} />
            <h1 class="shell-title">{p.title ?? p.nav.find(n => n.id === p.current)?.label ?? ''}</h1>
            <span class="shell-actions" ref={el => setActs(el)} />
            <span class="shell-status" ref={el => setSlot(el)} />
            <span class="grow" />
            {p.focus}
            <button
              type="button"
              class="btn quiet icon-only shell-search"
              aria-label="Search"
              aria-keyshortcuts="Meta+K Control+K"
              data-tip="Search ⌘K"
              aria-disabled={!privateReady() ? 'true' : undefined}
              onClick={() => { if (privateReady()) setSpot({ gen: p.privateGeneration ?? 0, seq: ++spotSeq }); }}
            >
              <Icon name={actions.search} />
            </button>
            {/* Right side-panel toggles (side-panel.md): after search, a reserved place, so nothing moves when a page brings one. */}
            <span class="shell-panels" ref={el => setPanelSlot(el)} />
          </header>
          <main class="shell-main" id="main" tabindex={-1}>
            <ActionsSlot value={acts}>
              <StatusSlot value={slot}>
                {p.children}
              </StatusSlot>
            </ActionsSlot>
          </main>
        </div>
        <Show when={openSpot()} keyed>
          {s => (
            <Spotlight
              providers={[sectionProvider(p.nav, p.onGo), ...(p.search ?? [])]}
              memory={memoryFor(s.gen)}
              idle={p.searchIdle}
              onClose={() => setSpot(cur => cur?.seq === s.seq ? null : cur)}
            />
          )}
        </Show>
        <TooltipLayer />
        <EntityCardLayer />
      </div>
    </SidePanels>
  );
}

/** The resizable columns: CSS variable name, person-setting key and limits in px. */
const COLS: Record<'rail' | 'ctl', { key: string; lo: number; hi: number }> = { rail: { key: 'nav', lo: 160, hi: 400 }, ctl: { key: 'control', lo: 280, hi: 720 } };
/** A column's drag divider (OR-41), the shared side-panel divider: drag or arrow keys resize the column on its left, double-click resets it. */
function Divider(p: { name: 'rail' | 'ctl' }) {
  const spec = () => COLS[p.name];
  // The upper limit is also what CSS shows (min(width, 25vw) for the rail, half the split for the list), so nothing unseen is saved.
  return (
    <ResizeDivider
      class={`divider-${p.name}`}
      label={p.name === 'rail' ? 'Resize navigation' : 'Resize list'}
      setting={spec().key}
      side="left"
      lo={spec().lo}
      hi={spec().hi}
      prop={`--size-${p.name}`}
      host={el => el.closest<HTMLElement>('.shell')}
      panel={el => (p.name === 'rail' ? el.closest('.shell')!.querySelector<HTMLElement>('[data-ui=nav-rail]')! : el.previousElementSibling as HTMLElement)}
      cap={el => (p.name === 'rail' ? innerWidth / 4 : el.parentElement!.getBoundingClientRect().width / 2)}
    />
  );
}

/** One part of Fleet in the version overview: the tab, the site's UI, the coordinator, the gateway, each host. */
export type VersionRow = {
  key: string; part: string; icon: IconName; version: string | null; state: string; tone: Tone; note?: string;
  /** Moments shown in compact form with the exact time on hover (OR-2), e.g. { label: 'last seen', at }; `now` is the present they count from (default: the panel's). */
  times?: { label: string; at: number; now?: number }[];
  /** Opens the part's own page (a host), shown as "Open host" on rows that need a look. */
  open?: () => void;
};
/**
 * The installed version beside the person (nav-rail.md, Version; board screens/shell): the Fleet release, toned only by a
 * confirmed problem (OR-18). A click shows the overview of every part. When the site serves a newer app than this tab,
 * an update strip above the user row names it and reloads into it.
 */
export type VersionInfo = {
  current: string | null;
  /** attention or danger only for a confirmed problem; anything else is neutral. */ tone: Tone;
  /** Says in words what the tone shows (WCAG 1.4.1), e.g. "1 part needs a look"; absent when nothing does. */ summary?: string;
  updateReady: boolean; onUpdate: () => void; updating?: boolean; rows: VersionRow[]; onOpen?: () => void;
  /** The version the site serves when an update is ready. */ newVersion?: string | null;
  /** Why the last update did not finish; the strip offers Retry. */ updateError?: string | null;
  /** Seconds left before the tab updates itself (it counts while nobody uses the tab); null when not counting. */ updateIn?: number | null;
  /** What holds the count: unsent input, an open dialog, a request still in flight. */ updateHold?: 'draft' | 'dialog' | 'save' | null;
  /** When the versions were last read, and why the latest read failed (shown in the overview, never as a tone). */
  checkedAt?: number | null; checkError?: string | null;
  /** The same info as of a moment: while the overview is open it is recounted on a timer, so the mark, states and ages move on together. */
  at?: (now: number) => VersionInfo;
};
/** How often the open version overview recounts its ages and states. */
const VERSION_TICK = 15_000;
const problem = (t: Tone) => t === 'attention' || t === 'danger';

/** A floating layer fixed above its opener (OR-14 informational, OR-16): Esc, a click outside, a rail scroll or a resize closes it. */
function useFloating(ref: () => HTMLElement | undefined, focusBack: string) {
  const [open, setOpen] = createSignal<{ left: number; bottom: number } | null>(null);
  createEffect(() => !!open(), isOpen => {
    if (!isOpen) return;
    queueMicrotask(() => ref()?.querySelector<HTMLElement>('.float [role=menuitem], .float button')?.focus());
    // Focus goes back to the first opener that is shown (the version is hidden on the collapsed rail; the avatar is not).
    const back = () => [...(ref()?.querySelectorAll<HTMLElement>(focusBack) ?? [])].find(e => e.offsetParent !== null)?.focus();
    const close = (e: Event) => {
      if (e instanceof KeyboardEvent) { if (e.key !== 'Escape') return; back(); }
      else if (ref()?.contains(e.target as Node)) return;
      setOpen(null);
    };
    // Only the rail's own scroll moves the opener; lists scrolling elsewhere (a live feed keeping its anchor) leave it open.
    const gone = () => setOpen(null), rail = ref()?.closest('.rail');
    document.addEventListener('pointerdown', close);
    document.addEventListener('keydown', close);
    rail?.addEventListener('scroll', gone);
    addEventListener('resize', gone);
    return () => {
      document.removeEventListener('pointerdown', close);
      document.removeEventListener('keydown', close);
      rail?.removeEventListener('scroll', gone);
      removeEventListener('resize', gone);
    };
  });
  // Fixed, so the scrolling rail never clips it (the collapsed rail is narrower than the layer).
  const toggle = (e: Event) => {
    e.preventDefault();
    const cur = e.currentTarget as HTMLElement;
    const r = (cur.closest('.rail-user') ?? cur).getBoundingClientRect();
    setOpen(curOpen => curOpen ? null : { left: r.left, bottom: innerHeight - r.top + 4 });
  };
  return { open, setOpen, toggle };
}

/** The full count of the idle update, for the strip's bar. */
const UPDATE_IDLE_S = 30;
const HOLD_TEXT = { draft: 'Waits for your draft', dialog: 'Waits for the open dialog', save: 'Waits for a save' } as const;

/**
 * The update strip (board screens/shell): when a new app is ready it counts down the seconds of no activity, with a bar
 * that empties, and reloads at 0; activity puts it back to the full count. Update reloads at once. Unsent input, an open
 * dialog or a save in flight hold it ("Waits for your draft"), with an icon button to update anyway. Reloading until the
 * page goes; a failure with Retry. On the collapsed rail the same row, at the same height (OR-42), holds one button: the
 * seconds, pause while held, the spinner, or Retry. The seconds are not announced; the live region says what happens once.
 */
function UpdateStrip(p: { v: VersionInfo; quiet?: boolean }) {
  const failed = () => !!p.v.updateError && !p.v.updating;
  const hold = () => !p.v.updating && !failed() ? p.v.updateHold ?? null : null;
  const left = () => !p.v.updating && !failed() && !hold() && p.v.updateIn != null ? p.v.updateIn : null;
  const version = () => p.v.newVersion ?? '';
  const title = () => p.v.updating ? 'Reloading into' : hold() ? HOLD_TEXT[hold()!] : left() != null ? `Updating in ${left()}s` : 'New app ready';
  const fullLabel = () => p.v.updating ? 'Reloading' : failed() ? 'Retry' : left() != null ? 'Update' : 'Reload';
  const tip = () => p.v.updating ? `Reloading into ${version() || 'the new app'}`
    : failed() ? `Update failed: ${p.v.updateError}. Retry`
    : hold() ? `${HOLD_TEXT[hold()!].replace('Waits', 'Update waits')}. Update now`
    : left() != null ? `Updating to ${version() || 'the new app'} in ${left()} s. Update now`
    : `Reload into ${version() || 'the new app'}`;
  return (
    <div class={['upd-strip', { failed: failed(), counting: left() != null }]} data-ui="update-strip" role={p.quiet ? undefined : 'status'}
      aria-busy={p.v.updating ? 'true' : undefined} style={left() != null ? { '--upd-left': String(left()! / UPDATE_IDLE_S) } : undefined}>
      <span class="upd-text">
        <Show when={failed()} fallback={<><b aria-hidden={left() != null ? 'true' : undefined}>{title()}</b><span class="mono">{version()}</span></>}>
          <b>Update failed</b><span>{p.v.updateError}</span>
        </Show>
        <Show when={left() != null}><span class="sr-only">New app ready: it updates itself after {UPDATE_IDLE_S} seconds without activity</span></Show>
      </span>
      <span class="upd-full">
        <Show when={hold()} fallback={<Button label={fullLabel()} icon="rotate-cw" kind={failed() ? 'default' : 'primary'} busy={p.v.updating} onClick={p.v.onUpdate} />}>
          <Button label="Update now" icon="rotate-cw" iconOnly onClick={p.v.onUpdate} />
        </Show>
      </span>
      <span class="upd-icon">
        <Show when={left() != null} fallback={<Button label={tip()} icon={hold() ? 'pause' : 'rotate-cw'} iconOnly kind={failed() || hold() ? 'default' : 'primary'} busy={p.v.updating} onClick={p.v.onUpdate} />}>
          <button type="button" class="btn primary icon-only upd-count" aria-label={tip()} data-tip={tip()} onClick={() => p.v.onUpdate()}>{left()}</button>
        </Show>
      </span>
    </div>
  );
}

/**
 * The signed-in person at the bottom of the navigation (board screens/shell): the update strip when a newer app is ready,
 * then the avatar and name, then the version. The avatar never changes. Collapsed, the strip keeps its row with only its
 * icon button, and the problem the hidden version mark shows floats above the block, so no row moves (OR-42).
 */
function UserBlock(p: { name: string; onLogout: () => void; version?: VersionInfo; pinned?: { on: boolean; set: (on: boolean) => void } }) {
  let ref: HTMLDivElement | undefined;
  const menu = useFloating(() => ref, '.rail-user-b');
  const panel = useFloating(() => ref, '.rail-ver, .rail-prob, .rail-user-b');
  // One present for the mark and every row (OR-2), recounted every VERSION_TICK while the overview is open; closing clears the timer.
  const now = useTick(VERSION_TICK, () => !!panel.open());
  const v = createMemo(() => p.version?.at ? p.version.at(now()) : p.version);
  const offer = () => !!v() && (v()!.updateReady || !!v()!.updateError);
  const openPanel = (e: Event) => { menu.setOpen(null); if (!panel.open()) v()?.onOpen?.(); panel.toggle(e); };
  return (
    <div class="rail-user" ref={el => { ref = el; }}>
      <Show when={offer()}><UpdateStrip v={v()!} /></Show>
      <Show when={v() && problem(v()!.tone)}>
        <button type="button" class={['rail-prob', `t-${v()!.tone}`]} aria-label={`Versions: ${v()!.summary ?? ''}`} data-tip={`Versions: ${v()!.summary ?? ''}`} onClick={openPanel}>
          <Icon name={v()!.tone === 'danger' ? 'octagon-x' : 'triangle-alert'} />
        </button>
      </Show>
      <div class="rail-user-row">
        <button
          type="button"
          class="rail-user-b"
          aria-label={p.name}
          aria-haspopup="menu"
          aria-expanded={menu.open() ? 'true' : 'false'}
          onClick={e => { panel.setOpen(null); menu.toggle(e); }}
          onContextMenu={e => { panel.setOpen(null); menu.toggle(e); }}
        >
          <span class="rail-av" aria-hidden="true" data-tip={p.name} data-tip-narrow>{p.name.slice(0, 1).toUpperCase()}</span>
          <span class="nm">{p.name}</span>
        </button>
        <Show when={v()}>
          <button
            type="button"
            class={['rail-ver', `t-${problem(v()!.tone) ? v()!.tone : 'neutral'}`]}
            aria-expanded={panel.open() ? 'true' : 'false'}
            aria-label={`Version ${v()!.current ?? 'not read yet'}${v()!.summary ? `, ${v()!.summary}` : ''}: show every part`}
            data-tip={v()!.summary ? `Versions: ${v()!.summary}` : 'Versions'}
            onClick={openPanel}
          >
            <Show when={problem(v()!.tone)}><Icon name={v()!.tone === 'danger' ? 'octagon-x' : 'triangle-alert'} /></Show>
            {v()!.current ?? ''}
          </button>
        </Show>
      </div>
      <Show when={menu.open()}>
        <span class="menu rail-menu up float" role="menu" aria-label={p.name} style={{ left: menu.open()!.left + 'px', bottom: menu.open()!.bottom + 'px' }}>
          <Show when={v()}>
            <button type="button" role="menuitem" class="menu-ver" onClick={() => { const pos = menu.open(); menu.setOpen(null); v()?.onOpen?.(); panel.setOpen(pos); }}>
              <Icon name="info" />Versions
            </button>
          </Show>
          <Show when={p.pinned}>
            {b => (
              <button type="button" role="menuitem" onClick={() => { menu.setOpen(null); b().set(!b().on); }}>
                <Icon name={b().on ? 'pin-off' : 'pin'} />{b().on ? 'Hide pinned in navigation' : 'Show pinned in navigation'}
              </button>
            )}
          </Show>
          <button type="button" role="menuitem" onClick={() => { menu.setOpen(null); p.onLogout(); }}><Icon name="log-out" />Logout</button>
        </span>
      </Show>
      <Show when={v() && panel.open()}>
        <VersionPanel v={v()!} at={panel.open()!} now={now()} />
      </Show>
    </div>
  );
}

/** The open version overview; every row counts from the same present. It stays open while its values change in place. */
function VersionPanel(p: { v: VersionInfo; at: { left: number; bottom: number }; now: number }) {
  return (
    <div
      class="ver-panel float"
      data-ui="version-panel"
      role="group"
      aria-label="Versions"
      style={{ left: p.at.left + 'px', bottom: p.at.bottom + 'px', '--ver-bottom': p.at.bottom + 'px' }}
    >
      <div class="ver-head">
        <b>Versions</b>
        <span class="muted">
          <Show when={p.v.checkedAt != null} fallback={p.v.checkError ? `could not check: ${p.v.checkError}` : 'checking…'}>
            {p.v.checkError ? 'could not check · last checked ' : 'checked '}<Ago t={p.v.checkedAt!} />
          </Show>
        </span>
      </div>
      {/* The rail's strip is the live region; this copy is not announced again. */}
      <Show when={p.v.updateReady || p.v.updateError}><UpdateStrip v={p.v} quiet /></Show>
      <table class="ver-table">
        <tbody>
          <For each={p.v.rows} keyed={(row: VersionRow) => row.key}>
            {r => (
              <tr>
                <th scope="row"><Icon name={r().icon} /><span>{r().part}</span></th>
                <td class="mono">{r().version ?? '—'}</td>
                <td>
                  <span class={['ver-state', `t-${r().tone}`]}>{r().state}</span>
                  <For each={r().times ?? []}>
                    {x => <span class="muted"> {x.label} <Ago t={x.at} clockOffsetMs={x.now !== undefined ? x.now! - p.now : 0} /></span>}
                  </For>
                  <Show when={r().note}><span class="muted"> {r().note}</span></Show>
                </td>
                <td class="ver-open">
                  <Show when={r().open}><button type="button" onClick={() => r().open!()}>Open host<Icon name="arrow-right" /></button></Show>
                </td>
              </tr>
            )}
          </For>
        </tbody>
      </table>
    </div>
  );
}

/* ---------- Layout patterns used by pages ---------- */

/** Control column and work area (patterns/list-detail.md). With nothing open the work area says so; when narrow only one of the two shows. */
export function ListDetail(p: {
  list: JSX.Element; detail?: JSX.Element; detailKey?: string | number;
  /** What the work area shows while nothing is open, in place of the default line (the section's own reference page). */
  idle?: JSX.Element;
}) {
  let ref: HTMLDivElement | undefined;
  const detail = createMemo(() => p.detail);
  // The idle page is built only while nothing is open: it is a page of its own with its own reads (System's readiness,
  // spend and runners), which an open item must not pay for.
  const idle = createMemo(() => (detail() ? undefined : p.idle));
  createEffect(() => p.detailKey, key => {
    if (key !== undefined) queueMicrotask(() => ref?.querySelector<HTMLElement>('[data-ui=detail]')?.focus({ preventScroll: true }));
  });
  return (
    <div ref={el => { ref = el; }} data-ui="list-detail" class={['split-view', { 'has-detail': !!detail() }]}>
      <div class="split-list">{p.list}</div>
      <Divider name="ctl" />
      {detail() ?? <div class={['work-idle', { 'work-empty': !idle() }]}>{idle() ?? <Empty text="Pick a row to open it here." />}</div>}
    </div>
  );
}

/** Everything waiting for a person, answered in place (patterns/needs-you.md). */
export function NeedsYou(p: { children: JSX.Element }) {
  return <section data-ui="needs-you" class="panel wide"><header class="panel-h"><h2>Needs you</h2></header><div class="stack">{p.children}</div></section>;
}

/** A short preview inside a card (OR-30, OR-65): the first `n` items and nothing else, no toggle. The section title
 *  (DetailSection with `onOpen`) opens the full list, which loads on scroll. Items arrive already ordered. */
export function Preview(p: { items: JSX.Element[]; n?: number }) {
  return <>{p.items.slice(0, p.n ?? 3)}</>;
}

/** Operational counts and capacity (patterns/ops-summary.md): a stat strip plus side panels. */
export function OpsSummary(p: { children: JSX.Element }) {
  return <div data-ui="ops-summary" class="stack">{p.children}</div>;
}

/** Generic list frame: filter bar over a virtual list (patterns/list-view.md), for lists other than tasks. */
/** pending: first or operator-requested load. Quiet reads keep the reserved progress strip idle. */
export function ListFrame(p: { bar: JSX.Element; children: JSX.Element; pending?: boolean }) {
  return (
    <div class={['list-view', { pending: !!p.pending }]} data-ui="list-view" aria-busy={p.pending ? 'true' : undefined}>
      {p.bar}
      <progress class="list-progress" aria-label={p.pending ? 'Updating' : undefined} aria-hidden={p.pending ? undefined : 'true'} />
      {p.children}
    </div>
  );
}

/** The work area for a definition kept in git: path and revision, name, tabs, then the tab's sections (patterns/definition-editor.md). */
export function DefinitionEditor(p: {
  label: string; crumb: string; title: JSX.Element; tabs: string[]; tab: string; onTab: (t: string) => void; onClose?: () => void;
  actions?: JSX.Element; state?: JSX.Element; children: JSX.Element;
}) {
  return (
    <article class="detail" data-ui="detail" tabindex={-1} aria-label={p.label}>
      <DetailHead crumb={p.crumb} title={p.title} state={p.state} actions={p.actions} onClose={p.onClose} />
      <TabBar cls="detail-tabs" label={`${p.label} sections`} tabs={p.tabs.map(x => ({ id: x, label: x }))} value={p.tab} onChange={p.onTab} />
      <div class="stack" data-ui="definition-editor" role="tabpanel">{p.children}</div>
    </article>
  );
}
