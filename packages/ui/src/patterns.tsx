// Patterns: compositions of controls for recurring jobs. Specs: docs/design-system/patterns/*.md.
import type { ComponentChildren } from 'preact';
import { useMemo, useState } from 'preact/hooks';
import { Button, StateBadge, Num, AgeChip, FilterBar, Presets, StateCounters, GroupHeader, NavRail, TooltipLayer, type Chip, type FilterField, type NavItem } from './controls';
import { VirtualList } from './virtual-list';
import { states, actions, type StateOf, type StateDef } from './registry';
import tokens from '../tokens.json';

const ROW = parseInt(tokens.size.row);

/* ---------- Task list view (patterns/list-view.md) ---------- */

export type TaskRow = {
  id: number; title: string; project: string; state: StateOf<'task'>; reason?: string; agent: string; host: string;
  started: number; cost: number | null; est: boolean; pinned?: boolean;
};
type View = { name: string; chips: Chip[]; states: StateOf<'task'>[]; group: 'state' | 'project' | 'none' };
type Row = { kind: 'group'; key: string; label: string; state?: StateOf<'task'>; count: number } | { kind: 'task'; t: TaskRow };

/** Filter bar + views + state counters + grouping + virtual list. View state lives here; a real page keeps it in the URL. */
export function TaskListView({ items, now, onPin, onOpen, selected, height }: {
  items: TaskRow[]; now: number; onPin: (id: number) => void; onOpen?: (t: TaskRow) => void; selected?: number; height?: string;
}) {
  const [views, setViews] = useState<View[]>([
    { name: 'Needs me', chips: [], states: ['waiting', 'failed'], group: 'state' },
    { name: 'Running', chips: [], states: ['in_progress', 'queued'], group: 'project' },
    { name: 'dashq', chips: [{ field: 'Project', value: 'dashq' }], states: [], group: 'state' },
  ]);
  const [view, setView] = useState<View>({ name: '', chips: [], states: [], group: 'state' });
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
    const keyOf = (t: TaskRow) => view.group === 'state' ? t.state : t.project;
    const order = view.group === 'state' ? ['waiting', 'failed', 'in_progress', 'queued', 'paused', 'completed', 'cancelled'] : [...new Set(pinnedFirst.map(keyOf))];
    const out: Row[] = [];
    for (const k of order) {
      const g = pinnedFirst.filter(t => keyOf(t) === k);
      if (!g.length) continue;
      out.push({ kind: 'group', key: `g-${k}`, label: view.group === 'state' ? (states.task[k as StateOf<'task'>] as StateDef).label : k, state: view.group === 'state' ? k as StateOf<'task'> : undefined, count: g.length });
      if (!closed.includes(k)) for (const t of g) out.push({ kind: 'task', t });
    }
    return out;
  }, [pinnedFirst, view.group, closed]);

  const fields: FilterField[] = [
    { field: 'Project', values: [...new Set(items.map(t => t.project))].map(v => ({ value: v, count: items.filter(t => t.project === v).length })) },
    { field: 'Agent', values: [...new Set(items.map(t => t.agent))].map(v => ({ value: v, count: items.filter(t => t.agent === v).length })) },
    { field: 'Host', values: [...new Set(items.map(t => t.host))].map(v => ({ value: v, count: items.filter(t => t.host === v).length })) },
  ];

  return (
    <div class="list-view" style={height ? { '--list-height': height } : undefined}>
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
          <StateCounters family="task" label="Tasks by state" counts={counts} active={view.states}
            onToggle={s => edit({ states: view.states.includes(s) ? view.states.filter(x => x !== s) : [...view.states, s] })} />
          <span class="seg-group" role="group" aria-label="Group by">
            {(['state', 'project', 'none'] as const).map(g => <button type="button" key={g} class={`seg${view.group === g ? ' on' : ''}`} aria-pressed={view.group === g} onClick={() => edit({ group: g })}>{g === 'none' ? 'No groups' : `By ${g}`}</button>)}
          </span>
        </>} />
      <VirtualList items={rows} getKey={r => r.kind === 'group' ? r.key : String(r.t.id)} rowHeight={ROW} follow="top" label="Tasks"
        columns="minmax(0,1fr) 4.5rem 1.25rem 4rem 2.75rem 4.5rem 1.75rem"
        rowClass={r => r.kind === 'group' ? 'grp' : r.t.id === selected ? 'sel' : ''}
        header={<><span>Task</span><span>ID</span><span /><span>Agent</span><span class="r">Age</span><span class="r">Cost</span><span /></>}
        render={r => r.kind === 'group'
          ? <GroupHeader icon={r.state ? (states.task[r.state] as StateDef).icon : 'folder-open'} tone={r.state ? (states.task[r.state] as StateDef).tone : 'neutral'} label={r.label} count={r.count}
              open={!closed.includes(r.key.slice(2))} onToggle={() => { const k = r.key.slice(2); setClosed(closed.includes(k) ? closed.filter(x => x !== k) : [...closed, k]); }} />
          : <TaskCells t={r.t} now={now} onPin={onPin} onOpen={onOpen} />} />
    </div>
  );
}

function TaskCells({ t, now, onPin, onOpen }: { t: TaskRow; now: number; onPin: (id: number) => void; onOpen?: (t: TaskRow) => void }) {
  return <>
    <a class="title row-link" href={`#/tasks/${t.id}`} onClick={e => { if (onOpen) { e.preventDefault(); onOpen(t); } }}>{t.title}</a>
    <span class="id svc">T-{t.id}</span>
    <StateBadge family="task" state={t.state} reason={t.reason} compact />
    <span class="svc">{t.agent}</span>
    <span class="r"><AgeChip t={t.started} now={now} alertAfter={t.state === 'in_progress' ? 2 * 3600 : undefined} /></span>
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
    <div class="shell">
      <header class="topbar">
        <b class="brand">Fleet</b>
        <span class="grow" />
        {waiting > 0 && <a class="nav-count t-attention waiting" href="#/overview" onClick={e => { e.preventDefault(); onGo('overview'); }}>{waiting} need you</a>}
      </header>
      <div class="shell-body">
        <NavRail items={nav} pinned={pinned} current={current} onGo={onGo} onPin={(id, p) => setPinned(p ? [...pinned, id] : pinned.filter(x => x !== id))} />
        <main class="shell-main">{children}</main>
      </div>
      <TooltipLayer />
    </div>
  );
}
