// Tasks list and detail (docs/design-system/pages/tasks.md). Server-ordered list with views, state counters, grouping and
// search (list-view pattern); the detail shows outcome, totals, open decisions and the execution tree.
import { useMemo, useState } from 'preact/hooks';
import { useLocation, useSearch } from 'wouter-preact';
import {
  ListDetail, ListFrame, FilterBar, Presets, StateCounters, GroupHeader, VirtualList, StateBadge, HarnessMark, AgeChip, Button,
  SplitButton, Totals, OutcomeBlock, DecisionBlock, ExecTree, Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses,
  type TreeNode, type Harness, type StateOf,
} from '../../../packages/ui/src/index';
import tokens from '../../../packages/ui/tokens.json';
import { op, online } from '../api';
import { useLiveList, useLiveRecord } from '../live';
import { usePins, useViews, type SavedView } from '../prefs';

const ROW = parseInt(tokens.size.row);
const GROUPS = [['priority', 'Priority'], ['state', 'State'], ['project', 'Project'], ['agent', 'Agent'], ['host', 'Host'], ['none', 'None']] as const;
type TaskRow = { id: string; title: string; state: StateOf<'task'>; waitReason: string | null; priority: number; agent: string | null; host: string | null; startedAt: string | null; createdAt: string; projectId: string | null; openQuestions: number; sortKey: (string | number)[] };
type Item = { kind: 'group'; key: string; label: string; count: number } | { kind: 'task'; t: TaskRow };
const ms = (v: string | number | null | undefined) => (v === null || v === undefined ? null : typeof v === 'number' ? v : Date.parse(v));
const isHarness = (h: string | null): h is Harness => !!h && h in harnesses;

function useView() {
  const search = useSearch(), [, go] = useLocation();
  const q = new URLSearchParams(search);
  const view = { states: (q.get('state') ?? '').split(',').filter(Boolean) as StateOf<'task'>[], group: q.get('group') ?? 'priority', search: q.get('q') ?? '' };
  const set = (v: Partial<typeof view>, path = location.pathname) => {
    const n = { ...view, ...v }, p = new URLSearchParams();
    if (n.states.length) p.set('state', n.states.join(',')); if (n.group !== 'priority') p.set('group', n.group); if (n.search) p.set('q', n.search);
    go(path + (p.toString() ? '?' + p : ''), { replace: true });
  };
  return { view, set };
}

export function Tasks({ id }: { id?: string }) {
  const { view, set } = useView();
  const [, go] = useLocation();
  const [closed, setClosed] = useState<string[]>([]);
  const [query, setQuery] = useState(view.search);
  const pins = usePins('tasks');
  const views = useViews('tasks', [{ name: 'Needs me', states: ['waiting', 'failed'], group: 'priority' }, { name: 'Running', states: ['in_progress', 'queued'], group: 'agent' }]);
  const params = useMemo(() => ({ filter: { state: view.states, search: view.search || undefined }, group: view.group }), [view.states.join(), view.group, view.search]);
  const list = useLiveList('tasks', params);
  const rows = list.rows as TaskRow[];
  const pinned = rows.filter(r => pins.has(r.id)), rest = rows.filter(r => !pins.has(r.id));
  const groupOf = (r: TaskRow) => String(r.sortKey[0]);
  const items: Item[] = [];
  for (const t of pinned) items.push({ kind: 'task', t });
  let last: string | null = null;
  for (const t of rest) {
    const g = groupOf(t);
    if (view.group !== 'none' && g !== last) { const meta = list.groups.find(x => x.key === g); items.push({ kind: 'group', key: g, label: groupLabel(view.group, meta?.label ?? null), count: meta?.count ?? 0 }); last = g; }
    if (!closed.includes(g)) items.push({ kind: 'task', t });
  }
  const shown = view.states.length ? view.states.reduce((n, s) => n + (list.counts[s] ?? 0), 0) : list.total;
  const now = Date.now();
  const listPane = <>
    <header class="page-h"><h1>Tasks</h1><OfflineMarker state={list.state} since={list.at || undefined} now={now} /></header>
    <section class="panel">
      <ListFrame bar={<FilterBar query={query} placeholder="Search tasks" onQuery={v => { setQuery(v); set({ search: v }); }} chips={[]} fields={[]} onAdd={() => {}} onRemove={() => {}} onClear={() => set({ states: [], search: '' })}
        shown={shown} total={list.total}
        extra={<>
          <Presets presets={views.list.map(v => v.name)} active={views.list.find(v => v.states.join() === view.states.join() && v.group === view.group)?.name ?? null}
            onPick={n => { const v = views.list.find(x => x.name === n)!; set({ states: v.states as StateOf<'task'>[], group: v.group }); }}
            onSave={n => views.save({ name: n, states: view.states, group: view.group } as SavedView)} onUnpin={views.remove} />
          <span class="grow" />
          <StateCounters family="task" label="Tasks by state" counts={list.counts as any} active={view.states} onAll={() => set({ states: [] })}
            onToggle={s => set({ states: view.states.includes(s) ? view.states.filter(x => x !== s) : [...view.states, s] })} />
          <span class="seg-group" role="group" aria-label="Group by"><span class="seg-label">Group</span>
            {GROUPS.map(([g, l]) => <button type="button" key={g} class={`seg${view.group === g ? ' on' : ''}`} aria-pressed={view.group === g} onClick={() => set({ group: g })}>{l}</button>)}</span>
        </>} />}>
        {list.error && !rows.length ? <ErrorBanner text={`Tasks did not load: ${list.error}`} onRetry={list.reload} />
          : !list.at ? <Skeleton rows={8} />
          : !rows.length ? <Empty text={view.states.length || view.search ? 'No tasks match these filters.' : 'No tasks yet.'} action={view.states.length || view.search ? <Button label="Clear filters" onClick={() => set({ states: [], search: '' })} /> : undefined} />
          : <VirtualList items={items} getKey={i => i.kind === 'group' ? 'g:' + i.key : i.t.id} rowHeight={ROW} follow="top" label="Tasks" onEnd={list.more}
            columns="1.25rem minmax(0,1fr) 1.25rem 2.75rem 1.75rem" rowClass={i => i.kind === 'group' ? 'grp' : i.t.id === id ? 'sel' : ''}
            header={<><span /><span>Task</span><span /><span class="r">Age</span><span /></>}
            render={i => i.kind === 'group'
              ? <GroupHeader icon={view.group === 'priority' ? 'list-ordered' : 'folder-open'} label={i.label} count={i.count} open={!closed.includes(i.key)} onToggle={() => setClosed(c => c.includes(i.key) ? c.filter(x => x !== i.key) : [...c, i.key])} />
              : <>
                <span class="lead"><StateBadge family="task" state={i.t.state} reason={i.t.waitReason ?? undefined} compact /></span>
                <span class="tt"><a class="title row-link" href={`/tasks/${i.t.id}${location.search}`} onClick={e => { e.preventDefault(); go(`/tasks/${i.t.id}${location.search}`); }}>{i.t.title}</a><span class="id">{i.t.id.slice(0, 8)}</span></span>
                <span>{i.t.state === 'in_progress' && isHarness(i.t.agent) && <HarnessMark h={i.t.agent} />}</span>
                <span class="r">{ms(i.t.startedAt ?? i.t.createdAt) && <AgeChip t={ms(i.t.startedAt ?? i.t.createdAt)!} now={now} alertAfter={i.t.state === 'in_progress' ? 2 * 3600 : undefined} alertLabel="running too long" />}</span>
                <Button label={pins.has(i.t.id) ? 'Unpin' : 'Pin'} icon={pins.has(i.t.id) ? 'pin-off' : 'pin'} iconOnly pressed={pins.has(i.t.id)} onClick={() => pins.toggle(i.t.id)} />
              </>} />}
      </ListFrame>
    </section>
  </>;
  return <ListDetail list={listPane} detail={id ? <TaskDetail id={id} onClose={() => go('/tasks' + location.search)} /> : undefined} detailKey={id} />;
}

function groupLabel(group: string, label: string | null) {
  if (group === 'priority') return `Priority ${label ?? 0}`;
  if (group === 'agent') return label && label in harnesses ? harnesses[label as Harness].label : 'No agent';
  if (group === 'state') return label ?? '';
  return label ?? 'None';
}

const EXEC_STATE: Record<string, StateOf<'execution'>> = { completed: 'succeeded', stopped: 'cancelled', unknown: 'running', started: 'running', accepted: 'preparing', pending: 'preparing' };
const execState = (s: string): StateOf<'execution'> => (EXEC_STATE[s] ?? (['preparing', 'running', 'waiting', 'succeeded', 'failed', 'cancelled'].includes(s) ? s : 'running')) as StateOf<'execution'>;

function TaskDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const { data: t, error, state } = useLiveRecord<any>(`task:${id}`, () => op('task_get', { taskId: id }), c => c.taskId === id || c.id === id);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  const now = Date.now(), off = !online.value;
  const act = async (action: string) => { setBusy(action); setFailure(null); try { await op('task_update', { taskId: id, action }); } catch (e: any) { setFailure(e.message); } finally { setBusy(null); setConfirming(false); } };
  if (error && !t) return <article class="detail" data-ui="detail" tabIndex={-1}><ErrorBanner text={`Task did not load: ${error}`} /></article>;
  if (!t) return <article class="detail" data-ui="detail" tabIndex={-1}><Skeleton rows={6} /></article>;
  const req = t.completionRequirements && typeof t.completionRequirements === 'object' ? Object.entries(t.completionRequirements).map(([k, v]) => ({ text: typeof v === 'string' ? `${k}: ${v}` : k, met: false })) : [];
  const open = (t.questions ?? []).filter((q: any) => q.state === 'open');
  const tokensIn = t.cost?.tokens_input, tokensOut = t.cost?.tokens_output;
  const tok = tokensIn === null || tokensIn === undefined ? null : Number(tokensIn) + Number(tokensOut ?? 0);
  const nodes: TreeNode[] = (t.executions ?? []).map((e: any) => ({ id: e.id, name: [e.kind ?? 'run', e.harness ?? e.agentConfig?.harness, e.hostId].filter(Boolean).join(' · '), state: execState(e.state),
    lastSeen: e.observation === 'unconfirmed' ? ms(e.lastSeenAt) ?? undefined : undefined,
    time: e.startedAt && e.finishedAt ? { value: (ms(e.finishedAt)! - ms(e.startedAt)!) / 1000, basis: 'measured' } : { value: null, basis: 'unavailable', reason: 'not finished' } }));
  const disabled = off ? 'offline' : undefined;
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={t.title}>
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
      <span class="muted mono">{t.projectId ?? 'no project'} · {t.id.slice(0, 8)}</span><span class="grow" />
      <OfflineMarker state={state} now={now} />
      {t.state === 'paused'
        ? <Button label="Continue" icon="rotate-cw" busy={busy === 'resume'} disabledReason={disabled} onClick={() => act('resume')} />
        : !['completed', 'failed', 'cancelled'].includes(t.state) && <SplitButton main={{ label: 'Pause', icon: 'pause', onClick: () => !off && act('pause') }} alternatives={[{ label: 'Cancel task', icon: 'circle-slash', onClick: () => setConfirming(true) }]} />}
    </header>
    {confirming && <div class="problem warning" role="group" aria-label="Confirm cancel"><b>Cancel this task?</b><span class="facts">Running work is asked to stop; finished external effects stay.</span>
      <span class="acts"><Button label="Yes, cancel" kind="danger" busy={busy === 'cancel'} disabledReason={off ? 'offline' : undefined} onClick={() => act('cancel')} /><Button label="Keep" onClick={() => setConfirming(false)} /></span></div>}
    {failure && <ErrorBanner text={failure} />}
    <h2 class="detail-title">{t.title}</h2>
    <div class="state-line"><StateBadge family="task" state={t.state} reason={t.waitReason ?? undefined} />{ms(t.createdAt) && <AgeChip t={ms(t.createdAt)!} now={now} />}</div>
    {t.expectedOutcome && <OutcomeBlock outcome={t.expectedOutcome} requirements={req} inputs={[]} depends={t.dependencies?.length ? `Depends on ${t.dependencies.map((d: any) => d.id.slice(0, 8)).join(', ')}` : undefined} />}
    <Totals items={[
      { label: 'Executions', m: { value: t.progress?.executions ?? 0, basis: 'measured' }, kind: 'count' },
      { label: 'Tokens', m: tok === null ? { value: null, basis: 'unavailable', reason: 'no meter yet' } : { value: tok, basis: 'measured' }, kind: 'count' },
      { label: 'Cost', m: { value: null, basis: 'unavailable', reason: 'no cost meter yet' }, kind: 'money', note: 'no cost meter yet' },
    ]} />
    {open.map((q: any) => <DecisionBlock key={q.id} question={q.text} askedBy={q.askedBy ?? q.kind} addressedTo="you" since={ms(q.askedAt) ?? now} now={now}
      options={(Array.isArray(q.options) ? q.options : q.kind === 'acceptance' ? ['accepted', 'rejected'] : []).map((o: string, i: number) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
      onAnswer={a => { if (!off) op('questions_answer', { questionId: q.id, answer: a }).catch(e => setFailure(e.message)); }} />)}
    <h3 class="sub-h">Executions</h3>
    {nodes.length ? <ExecTree nodes={nodes} now={now} label="Executions" /> : <Empty text="No executions yet." />}
  </article>;
}
