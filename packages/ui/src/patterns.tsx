// Patterns: compositions of controls for recurring jobs. Specs: docs/design-system/patterns/*.md.
import type { ComponentChildren } from 'preact';
import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { Button, StateBadge, Num, AgeChip, FilterBar, Presets, StateCounters, GroupHeader, NavRail, TooltipLayer, HarnessMark, StageChip, type Chip, type FilterField, type NavItem } from './controls.tsx';
import { VirtualList } from './virtual-list.tsx';
import { states, actions, harnesses, type StateOf, type StateDef, type Harness, type Stage, type IconName, type Tone } from './registry.ts';
import tokens from '../tokens.json';

const ROW = parseInt(tokens.size.row);

/* ---------- Task list view (patterns/list-view.md) ---------- */

export type TaskRow = {
  id: number; title: string; project: string; state: StateOf<'task'>; reason?: string; agent: Harness; host: string;
  started: number; cost: number | null; est: boolean; pinned?: boolean; priority: 0 | 1 | 2; stage?: Stage;
};
export type GroupBy = 'priority' | 'state' | 'project' | 'agent' | 'host' | 'none';
type View = { name: string; chips: Chip[]; states: StateOf<'task'>[]; group: GroupBy };
type Row = { kind: 'group'; key: string; label: string; icon: IconName; tone: Tone; count: number } | { kind: 'task'; t: TaskRow };
const GROUPS: { id: GroupBy; label: string }[] = [
  { id: 'priority', label: 'Priority' }, { id: 'state', label: 'State' }, { id: 'project', label: 'Project' },
  { id: 'agent', label: 'Agent' }, { id: 'host', label: 'Host' }, { id: 'none', label: 'None' }];

/** Filter bar + views + state counters + grouping + virtual list. View state lives here; a real page keeps it in the URL. */
export function TaskListView({ items, now, onPin, onOpen, selected, height }: {
  items: TaskRow[]; now: number; onPin: (id: number) => void; onOpen?: (t: TaskRow) => void; selected?: number; height?: string;
}) {
  const [views, setViews] = useState<View[]>([
    { name: 'Needs me', chips: [], states: ['waiting', 'failed'], group: 'priority' },
    { name: 'Running', chips: [], states: ['in_progress', 'queued'], group: 'agent' },
    { name: 'dashq', chips: [{ field: 'Project', value: 'dashq' }], states: [], group: 'state' },
  ]);
  const [view, setView] = useState<View>({ name: '', chips: [], states: [], group: 'priority' });
  const [active, setActive] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [closed, setClosed] = useState<string[]>(['completed', 'cancelled']);
  const edit = (v: Partial<View>) => { setView({ ...view, ...v }); setActive(null); };

  const q = query.toLowerCase();
  const byChips = items.filter(t => view.chips.every(c => c.field === 'Project' ? t.project === c.value : c.field === 'Agent' ? t.agent === c.value : t.host === c.value)
    && (!q || t.title.toLowerCase().includes(q) || `t-${t.id}`.includes(q)));
  // ponytail: counts and grouping computed client-side over loaded rows; server aggregates when lists are paged.
  const counts = useMemo(() => { const c: Partial<Record<StateOf<'task'>, number>> = {}; for (const s of Object.keys(states.task) as StateOf<'task'>[]) c[s] = 0; for (const t of byChips) c[t.state]!++; return c; }, [byChips]);
  const shown = view.states.length ? byChips.filter(t => view.states.includes(t.state)) : byChips;
  const pinnedFirst = [...shown.filter(t => t.pinned), ...shown.filter(t => !t.pinned)];

  const rows: Row[] = useMemo(() => {
    if (view.group === 'none') return pinnedFirst.map(t => ({ kind: 'task', t }));
    const g = view.group;
    const keyOf = (t: TaskRow) => String(g === 'priority' ? t.priority : g === 'state' ? t.state : g === 'project' ? t.project : g === 'agent' ? t.agent : t.host);
    const order = g === 'priority' ? ['0', '1', '2'] : g === 'state' ? ['waiting', 'failed', 'in_progress', 'queued', 'paused', 'completed', 'cancelled'] : [...new Set(pinnedFirst.map(keyOf))].sort();
    const head = (k: string): { label: string; icon: IconName; tone: Tone } => {
      if (g === 'priority') return { label: `Priority ${k}`, icon: k === '0' ? 'triangle-alert' : 'list-ordered', tone: k === '0' ? 'danger' : 'neutral' };
      if (g === 'state') { const d = states.task[k as StateOf<'task'>] as StateDef; return { label: d.label, icon: d.icon, tone: d.tone }; }
      if (g === 'agent') return { label: harnesses[k as Harness].label, icon: harnesses[k as Harness].icon, tone: 'neutral' };
      return { label: k, icon: g === 'host' ? 'panels-top-left' : 'folder-open', tone: 'neutral' };
    };
    const out: Row[] = [];
    for (const k of order) {
      const items = pinnedFirst.filter(t => keyOf(t) === k);
      if (!items.length) continue;
      out.push({ kind: 'group', key: `g-${k}`, count: items.length, ...head(k) });
      if (!closed.includes(k)) for (const t of items) out.push({ kind: 'task', t });
    }
    return out;
  }, [pinnedFirst, view.group, closed]);

  const fields: FilterField[] = [
    { field: 'Project', values: [...new Set(items.map(t => t.project))].map(v => ({ value: v, count: items.filter(t => t.project === v).length })) },
    { field: 'Agent', values: [...new Set(items.map(t => t.agent))].map(v => ({ value: v, count: items.filter(t => t.agent === v).length })) },
    { field: 'Host', values: [...new Set(items.map(t => t.host))].map(v => ({ value: v, count: items.filter(t => t.host === v).length })) },
  ];

  return (
    <div class="list-view" data-ui="list-view" style={height ? { '--list-height': height } : undefined}>
      <FilterBar query={query} onQuery={setQuery} placeholder="Search tasks" chips={view.chips} fields={fields}
        onRemove={c => edit({ chips: view.chips.filter(x => x.field !== c.field || x.value !== c.value) })}
        onAdd={c => edit({ chips: [...view.chips, c] })} onClear={() => edit({ chips: [], states: [] })}
        shown={shown.length} total={items.length}
        extra={<>
          <Presets presets={views.map(v => v.name)} active={active}
            onPick={n => { const v = views.find(x => x.name === n)!; setView(v); setActive(n); }}
            onSave={n => { const v = { ...view, name: n }; setViews([...views.filter(x => x.name !== n), v]); setActive(n); }}
            onUnpin={n => { setViews(views.filter(v => v.name !== n)); if (active === n) setActive(null); }} />
          <span class="grow" />
          <StateCounters family="task" label="Tasks by state" counts={counts} active={view.states} onAll={() => edit({ states: [] })}
            onToggle={s => edit({ states: view.states.includes(s) ? view.states.filter(x => x !== s) : [...view.states, s] })} />
          <span class="seg-group" role="group" aria-label="Group by"><span class="seg-label">Group</span>
            {GROUPS.map(g => <button type="button" key={g.id} class={`seg${view.group === g.id ? ' on' : ''}`} aria-pressed={view.group === g.id} onClick={() => edit({ group: g.id })}>{g.label}</button>)}
          </span>
        </>} />
      <VirtualList items={rows} getKey={r => r.kind === 'group' ? r.key : String(r.t.id)} rowHeight={ROW} follow="top" label="Tasks"
        columns="1.25rem minmax(0,1fr) 1.25rem 1.25rem 2.75rem 4.5rem 1.75rem"
        rowClass={r => r.kind === 'group' ? 'grp' : r.t.id === selected ? 'sel' : ''}
        header={<><span /><span>Task</span><span /><span /><span class="r">Age</span><span class="r">Cost</span><span /></>}
        render={r => r.kind === 'group'
          ? <GroupHeader icon={r.icon} tone={r.tone} label={r.label} count={r.count}
              open={!closed.includes(r.key.slice(2))} onToggle={() => { const k = r.key.slice(2); setClosed(closed.includes(k) ? closed.filter(x => x !== k) : [...closed, k]); }} />
          : <TaskCells t={r.t} now={now} onPin={onPin} onOpen={onOpen} showPriority={view.group !== 'priority'} />} />
    </div>
  );
}

function TaskCells({ t, now, onPin, onOpen, showPriority }: { t: TaskRow; now: number; onPin: (id: number) => void; onOpen?: (t: TaskRow) => void; showPriority: boolean }) {
  // OR-22: state icon leftmost; ID beside the title; agent mark only while running; delivery stage as a chip.
  return <>
    <span class="lead"><StateBadge family="task" state={t.state} reason={t.reason} compact /></span>
    <span class="tt">{showPriority && t.priority === 0 && <span class="p0" aria-label="priority 0">p0</span>}<a class="title row-link" href={`#/tasks/${t.id}`} onClick={e => { if (onOpen) { e.preventDefault(); onOpen(t); } }}>{t.title}</a><span class="id">T-{t.id}</span></span>
    <span>{t.state === 'in_progress' && <HarnessMark h={t.agent} />}</span>
    <span>{t.stage && <StageChip stage={t.stage} />}</span>
    <span class="r"><AgeChip t={t.started} now={now} alertAfter={t.state === 'in_progress' ? 2 * 3600 : undefined} alertLabel="running too long" /></span>
    <span class="r"><Num m={t.cost === null ? { value: null, basis: 'unavailable', reason: 'no meter' } : { value: t.cost, basis: t.est ? 'estimated' : 'measured' }} kind="money" rise="bad" /></span>
    <Button label={t.pinned ? 'Unpin' : 'Pin'} icon={t.pinned ? actions.unpin : actions.pin} iconOnly pressed={t.pinned} onClick={() => onPin(t.id)} />
  </>;
}

/* ---------- App shell (patterns/app-shell.md) ---------- */

export function AppShell({ nav, current, onGo, waiting, children }: {
  nav: NavItem[]; current: string; onGo: (id: string) => void; waiting: number; children: ComponentChildren;
}) {
  const [pinned, setPinned] = useState<string[]>(['tasks', 'hosts']);
  return (
    <div class="shell" data-ui="app-shell">
      <a class="skip" href="#main" onClick={e => { e.preventDefault(); document.getElementById('main')?.focus(); }}>Skip to content</a>
      <header class="topbar">
        <b class="brand">Fleet</b>
        <span class="grow" />
        {waiting > 0 && current !== 'overview' && <a class="nav-count t-attention waiting" href="#/overview" onClick={e => { e.preventDefault(); onGo('overview'); }}>{waiting} need you</a>}
      </header>
      <div class="shell-body">
        <NavRail items={nav} pinned={pinned} current={current} onGo={onGo} onPin={(id, p) => setPinned(p ? [...pinned, id] : pinned.filter(x => x !== id))} />
        <main class="shell-main" id="main" tabIndex={-1}>{children}</main>
      </div>
      <TooltipLayer />
    </div>
  );
}

/* ---------- Layout patterns used by pages ---------- */

/** List on the left, detail on the right; stacks when narrow (patterns/list-detail.md). */
// D14/M1: when narrow, the detail comes first (CSS order) and takes focus, so opening an item always shows it.
export function ListDetail({ list, detail, detailKey }: { list: ComponentChildren; detail?: ComponentChildren; detailKey?: string | number }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (detailKey === undefined) return;
    const d = ref.current?.querySelector<HTMLElement>('[data-ui=detail]');
    if (!d) return;
    d.focus({ preventScroll: true });
    if (d.getBoundingClientRect().top > window.innerHeight * 0.5 || d.getBoundingClientRect().top < 0) d.scrollIntoView({ block: 'start' });
  }, [detailKey]);
  return <div ref={ref} data-ui="list-detail" class={`split-view${detail ? ' has-detail' : ''}`}><div class="split-list">{list}</div>{detail}</div>;
}
/** Everything waiting for a person, answered in place (patterns/needs-you.md). */
export const NeedsYou = ({ children }: { children: ComponentChildren }) => (
  <section data-ui="needs-you" class="panel wide"><header class="panel-h"><h2>Needs you</h2></header><div class="stack">{children}</div></section>
);
/** Operational counts and capacity (patterns/ops-summary.md): a stat strip plus side panels. */
export const OpsSummary = ({ children }: { children: ComponentChildren }) => <div data-ui="ops-summary" class="stack">{children}</div>;
/** Generic list frame: filter bar over a virtual list (patterns/list-view.md), for lists other than tasks. */
/** pending: a newer answer is loading; the rows stay, dimmed, under a thin progress bar (fleet-view-urls-and-list-path.md §2). The bar always keeps its height, so the rows never move (OR-23). */
export const ListFrame = ({ bar, children, pending }: { bar: ComponentChildren; children: ComponentChildren; pending?: boolean }) =>
  <div class={`list-view${pending ? ' pending' : ''}`} data-ui="list-view" aria-busy={pending || undefined}>{bar}<div class="list-progress" role={pending ? 'progressbar' : undefined} aria-label={pending ? 'Updating' : undefined} aria-hidden={pending ? undefined : true} />{children}</div>;
