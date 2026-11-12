// Patterns: compositions of controls for recurring jobs. Specs: docs/design-system/patterns/*.md.
import type { ComponentChildren } from 'preact';
import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { Button, Empty, StateBadge, Num, AgeChip, FilterBar, Presets, StateCounters, GroupHeader, NavRail, TooltipLayer, HarnessMark, StageChip, type Chip, type FilterField, type NavItem } from './controls.tsx';
import { VirtualList } from './virtual-list.tsx';
import { states, actions, harnesses, type StateOf, type StateDef, type Harness, type Stage, type IconName, type Tone } from './registry.ts';
import tokens from '../tokens.json';

const ROW = parseInt(tokens.size.row);
export const ROW2 = parseInt(tokens.size['row-2']);

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
export function TaskListView({ items, now, onPin, onOpen, selected, height, row = ROW }: {
  items: TaskRow[]; now: number; onPin: (id: number) => void; onOpen?: (t: TaskRow) => void; selected?: number; height?: string;
  /** Row height: ROW2 for a catalogue in the control column. */
  row?: number;
}) {
  const [views, setViews] = useState<View[]>([
    { name: 'Needs me', chips: [], states: ['waiting', 'failed'], group: 'priority' },
    { name: 'Running', chips: [], states: ['in_progress', 'queued'], group: 'agent' },
    { name: 'cs', chips: [{ field: 'Project', value: 'cs' }], states: [], group: 'state' },
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
    const order = g === 'priority' ? ['0', '1', '2'] : g === 'state' ? ['waiting', 'failed', 'in_progress', 'queued', 'held', 'paused', 'completed', 'cancelled'] : [...new Set(pinnedFirst.map(keyOf))].sort();
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
      <VirtualList items={rows} getKey={r => r.kind === 'group' ? r.key : String(r.t.id)} rowHeight={row} follow="top" label="Tasks"
        columns={row === ROW ? '1.25rem minmax(0,1fr) 1.25rem 1.25rem 2.75rem 4.5rem 1.75rem' : '1.25rem minmax(0,1fr) 2.75rem 4.5rem 1.75rem'}
        rowClass={r => r.kind === 'group' ? 'grp' : r.t.id === selected ? 'sel' : ''}
        header={row === ROW ? <><span /><span>Task</span><span /><span /><span class="r">Age</span><span class="r">Cost</span><span /></> : undefined}
        render={r => r.kind === 'group'
          ? <GroupHeader icon={r.icon} tone={r.tone} label={r.label} count={r.count}
              open={!closed.includes(r.key.slice(2))} onToggle={() => { const k = r.key.slice(2); setClosed(closed.includes(k) ? closed.filter(x => x !== k) : [...closed, k]); }} />
          : <TaskCells t={r.t} now={now} onPin={onPin} onOpen={onOpen} showPriority={view.group !== 'priority'} two={row !== ROW} />} />
    </div>
  );
}

function TaskCells({ t, now, onPin, onOpen, showPriority, two }: { t: TaskRow; now: number; onPin: (id: number) => void; onOpen?: (t: TaskRow) => void; showPriority: boolean; two: boolean }) {
  // OR-22: state icon leftmost; ID beside the title; agent mark only while running; delivery stage as a chip.
  // A two-line catalogue row keeps the marks on the second line, so the title has the whole first line (OR-21).
  const marks = <>{t.state === 'in_progress' && <HarnessMark h={t.agent} />}{t.stage && <StageChip stage={t.stage} />}</>;
  return <>
    <span class="lead"><StateBadge family="task" state={t.state} reason={t.reason} compact /></span>
    <span class="tt">{showPriority && t.priority === 0 && <span class="p0" aria-label="priority 0">p0</span>}<a class="title row-link" href={`#/tasks/${t.id}`} onClick={e => { if (onOpen) { e.preventDefault(); onOpen(t); } }}>{t.title}</a><span class="id">T-{t.id}</span>{two && marks}</span>
    {!two && <><span>{t.state === 'in_progress' && <HarnessMark h={t.agent} />}</span><span>{t.stage && <StageChip stage={t.stage} />}</span></>}
    <span class="r"><AgeChip t={t.started} now={now} alertAfter={t.state === 'in_progress' ? 2 * 3600 : undefined} alertLabel="running too long" /></span>
    <span class="r"><Num m={t.cost === null ? { value: null, basis: 'unavailable', reason: 'no meter' } : { value: t.cost, basis: t.est ? 'estimated' : 'measured' }} kind="money" rise="bad" /></span>
    <Button label={t.pinned ? 'Unpin' : 'Pin'} icon={t.pinned ? actions.unpin : actions.pin} iconOnly pressed={t.pinned} onClick={() => onPin(t.id)} />
  </>;
}

/* ---------- App shell (patterns/app-shell.md) ---------- */

export function AppShell({ nav, current, onGo, waiting, children, pins, onPins }: {
  nav: NavItem[]; current: string; onGo: (id: string) => void; waiting: number; children: ComponentChildren;
  /** The person's pinned sections (person settings); without them the shell keeps its own demo pins. */ pins?: string[]; onPins?: (ids: string[]) => void;
}) {
  const [own, setOwn] = useState<string[]>(['tasks', 'hosts']);
  const pinned = pins ?? own, setPinned = onPins ?? setOwn;
  // The collapsed rail is a per-device convenience; storage may be unavailable.
  const [mini, setMini] = useState(() => { try { return localStorage.getItem('fleet.nav.mini') === '1'; } catch { return false; } });
  const toggle = () => { setMini(!mini); try { localStorage.setItem('fleet.nav.mini', mini ? '0' : '1'); } catch { /* not remembered */ } };
  // OR-26: a click anywhere on a row follows the row's link. Controls in the row keep their own action; a text selection is not a click.
  const openRow = (e: MouseEvent) => {
    const t = e.target as HTMLElement, link = t.closest?.('.vl-row, .kv li')?.querySelector<HTMLElement>('.row-link');
    if (!link || t.closest('a,button,input,textarea,select,label') || !(getSelection()?.isCollapsed ?? true)) return;
    link.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, metaKey: e.metaKey, ctrlKey: e.ctrlKey, shiftKey: e.shiftKey }));
  };
  return (
    <div class="shell" data-ui="app-shell" onClick={openRow}>
      <a class="skip" href="#main" onClick={e => { e.preventDefault(); document.getElementById('main')?.focus(); }}>Skip to content</a>
      <NavRail items={nav} pinned={pinned} current={current} onGo={onGo} mini={mini} onPins={setPinned}
        head={<><span class="brand-mark" role="img" aria-label="Fleet" /><b class="brand">Fleet</b><span class="grow" />
          {waiting > 0 && current !== 'overview' && <a class="nav-count t-attention waiting" href="#/overview" onClick={e => { e.preventDefault(); onGo('overview'); }}>{waiting} need you</a>}
          <Button label={mini ? 'Expand navigation' : 'Collapse navigation'} icon="panel-left-close" iconOnly pressed={mini} onClick={toggle} /></>} />
      <main class="shell-main" id="main" tabIndex={-1}>{children}</main>
      <TooltipLayer />
    </div>
  );
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
  return <div ref={ref} data-ui="list-detail" class={`split-view${detail ? ' has-detail' : ''}`}><div class="split-list">{list}</div>{detail ?? <div class={`work-idle${idle ? '' : ' work-empty'}`}>{idle ?? <Empty text="Pick a row to open it here." />}</div>}</div>;
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
