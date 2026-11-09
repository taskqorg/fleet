// Tasks list and detail (docs/design-system/pages/tasks.md). Server-ordered list with views, state counters, grouping and
// search (list-view pattern); the detail shows outcome, totals, open decisions and the execution tree.
import { useState } from 'preact/hooks';
import {
  ListDetail, ListFrame, FilterBar, Presets, StateCounters, GroupHeader, VirtualList, StateBadge, HarnessMark, AgeChip, Button,
  SplitButton, RowCheck, Totals, OutcomeBlock, DecisionBlock, ExecTree, Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses,
  type TreeNode, type Harness, type StateOf,
} from '../../../packages/ui/src/index';
import tokens from '../../../packages/ui/tokens.json';
import { op, online } from '../api';
import { useList, useDoc } from '../store';
import { listKey, type View } from '../../../packages/view-url/view-url';
import { useUrlView, useSearchBox } from '../url';
import { usePins, useViews } from '../prefs';

const ROW = parseInt(tokens.size.row);
const GROUPS = [['priority', 'Priority'], ['state', 'State'], ['project', 'Project'], ['agent', 'Agent'], ['host', 'Host'], ['none', 'None']] as const;
type TaskRow = { id: string; title: string; state: StateOf<'task'>; waitReason: string | null; priority: 'urgent' | 'normal' | 'background'; agent: string | null; host: string | null; startedAt: string | null; createdAt: string; projectId: string | null; openQuestions: number; sortKey: (string | number)[] };
type Item = { kind: 'group'; key: string; label: string; count: number } | { kind: 'task'; t: TaskRow };
const ms = (v: string | number | null | undefined) => (v === null || v === undefined ? null : typeof v === 'number' ? v : Date.parse(v));
const isHarness = (h: string | null): h is Harness => !!h && h in harnesses;

export function Tasks({ id }: { id?: string }) {
  const { view, set, href } = useUrlView('tasks');
  const [closed, setClosed] = useState<string[]>([]);
  const [query, setQuery] = useSearchBox(view.q, q => set({ q: q || undefined }, 'type'));
  const pins = usePins('tasks');
  const views = useViews('tasks', [{ name: 'Needs me', view: { view: 'tasks', state: ['failed', 'waiting'] } }, { name: 'Running', view: { view: 'tasks', state: ['in_progress', 'queued'], group: 'agent' } }]);
  const list = useList('tasks', view);
  const group = view.group ?? 'priority', states = (view.state ?? []) as StateOf<'task'>[];
  // Rows on screen may still be the previous list (pending): headers and chips follow that list's own grouping.
  const rowsGroup = list.shownView.group ?? 'priority';
  const rows = list.rows as TaskRow[];
  const sel = view.sel ?? [], toggleSel = (x: string) => set({ sel: sel.includes(x) ? sel.filter(i => i !== x) : [...sel, x] });
  const pinned = rows.filter(r => pins.has(r.id)), rest = rows.filter(r => !pins.has(r.id));
  const groupOf = (r: TaskRow) => String(r.sortKey[0]);
  const items: Item[] = [];
  for (const t of pinned) items.push({ kind: 'task', t });
  let last: string | null = null;
  for (const t of rest) {
    const g = groupOf(t);
    if (rowsGroup !== 'none' && g !== last) { const meta = list.groups.find(x => x.key === g); items.push({ kind: 'group', key: g, label: groupLabel(rowsGroup, meta?.label ?? null), count: meta?.count ?? 0 }); last = g; }
    if (!closed.includes(g)) items.push({ kind: 'task', t });
  }
  const shown = states.length ? states.reduce((n, s) => n + (list.counts[s] ?? 0), 0) : list.total;
  const now = Date.now();
  const filtered = !!(states.length || view.q || view.project?.length);
  const presetKey = (v: View) => listKey({ ...v, view: 'tasks' });
  const activePreset = views.list.find(v => presetKey(v.view) === listKey(view))?.name ?? null;
  const listPane = <>
    <header class="page-h"><h1>Tasks</h1><OfflineMarker state={list.state} since={list.at || undefined} now={now} /></header>
    <section class="panel">
      <ListFrame pending={list.pending} bar={<FilterBar query={query} placeholder="Search tasks" onQuery={setQuery} chips={[]} fields={[]} onAdd={() => {}} onRemove={() => {}} onClear={() => { setQuery(''); set({ state: [], q: undefined, project: [], preset: undefined }); }}
        shown={shown} total={list.total}
        extra={<>
          <Presets presets={views.list.map(v => v.name)} active={activePreset}
            onPick={n => { const p = views.list.find(x => x.name === n)!; set({ state: p.view.state ?? [], group: p.view.group, project: p.view.project ?? [], q: p.view.q, preset: n }); }}
            onSave={n => views.save({ name: n, view: { view: 'tasks', state: view.state, group: view.group, project: view.project, q: view.q } })} onUnpin={views.remove} />
          <span class="grow" />
          <StateCounters family="task" label="Tasks by state" pending={list.pending} counts={list.counts as any} active={states} onAll={() => set({ state: [], preset: undefined })}
            onToggle={s => set({ state: states.includes(s) ? states.filter(x => x !== s) : [...states, s], preset: undefined })} />
          <span class="seg-group" role="group" aria-label="Group by"><span class="seg-label">Group</span>
            {GROUPS.map(([g, l]) => <button type="button" key={g} class={`seg${group === g ? ' on' : ''}`} aria-pressed={group === g} onClick={() => set({ group: g, preset: undefined })}>{l}</button>)}</span>
        </>} />}>
        {list.error && !rows.length ? <ErrorBanner text={`Tasks did not load: ${list.error}`} onRetry={list.reload} />
          : list.shownKey === null ? <Skeleton rows={8} />
          : !rows.length ? (list.pending ? <Skeleton rows={8} /> : <Empty text={filtered ? 'No tasks match these filters.' : 'No tasks yet.'} action={filtered ? <Button label="Clear filters" onClick={() => { setQuery(''); set({ state: [], q: undefined, project: [] }); }} /> : undefined} />)
          : <VirtualList items={items} getKey={i => i.kind === 'group' ? 'g:' + i.key : i.t.id} rowHeight={ROW} follow="top" label="Tasks" onEnd={list.more}
            columns="1.25rem minmax(0,1fr) 1.25rem 2.75rem 1.75rem 1.25rem" rowClass={i => i.kind === 'group' ? 'grp' : i.t.id === id ? 'sel' : ''}
            header={<><span /><span>Task</span><span /><span class="r">Age</span><span /><span /></>}
            render={i => i.kind === 'group'
              ? <GroupHeader icon={rowsGroup === 'priority' ? 'list-ordered' : 'folder-open'} label={i.label} count={i.count} open={!closed.includes(i.key)} onToggle={() => setClosed(c => c.includes(i.key) ? c.filter(x => x !== i.key) : [...c, i.key])} />
              : <>
                <span class="lead"><StateBadge family="task" state={i.t.state} reason={i.t.waitReason ?? undefined} compact /></span>
                <span class="tt">{rowsGroup !== 'priority' && <PriorityChip p={i.t.priority} />}<a class="title row-link" href={href({ focus: i.t.id })} onClick={e => { e.preventDefault(); set({ focus: i.t.id }); }}>{i.t.title}</a><span class="id">{i.t.id.slice(0, 8)}</span></span>
                <span>{i.t.state === 'in_progress' && isHarness(i.t.agent) && <HarnessMark h={i.t.agent} />}</span>
                <span class="r">{ms(i.t.startedAt ?? i.t.createdAt) && <AgeChip t={ms(i.t.startedAt ?? i.t.createdAt)!} now={now} alertAfter={i.t.state === 'in_progress' ? 2 * 3600 : undefined} alertLabel="running too long" />}</span>
                <Button label={pins.has(i.t.id) ? 'Unpin' : 'Pin'} icon={pins.has(i.t.id) ? 'pin-off' : 'pin'} iconOnly pressed={pins.has(i.t.id)} onClick={() => pins.toggle(i.t.id)} />
                <RowCheck checked={sel.includes(i.t.id)} label={'Select ' + i.t.title} onToggle={() => toggleSel(i.t.id)} />
              </>} />}
      </ListFrame>
    </section>
  </>;
  return <ListDetail list={listPane} detail={id ? <TaskDetail id={id} onClose={() => set({ focus: undefined })} /> : undefined} detailKey={id} />;
}

/** Priority class chip (owner decision): urgent red, background grey, normal none (OR-17). */
function PriorityChip({ p }: { p: TaskRow['priority'] }) {
  if (p === 'normal') return null;
  return <span class={`chip-age${p === 'urgent' ? ' t-danger alert' : ''}`} aria-label={p === 'urgent' ? 'urgent priority' : 'background priority'}>{p === 'urgent' ? 'urgent' : 'background'}</span>;
}

function groupLabel(group: string, label: string | null) {
  if (group === 'priority') return label === 'urgent' ? 'Urgent' : label === 'background' ? 'Background' : 'Normal';
  if (group === 'agent') return label && label in harnesses ? harnesses[label as Harness].label : 'No agent';
  if (group === 'state') return label ?? '';
  return label ?? 'None';
}

const EXEC_STATE: Record<string, StateOf<'execution'>> = { completed: 'succeeded', stopped: 'cancelled', unknown: 'running', started: 'running', accepted: 'preparing', pending: 'preparing' };
const execState = (s: string): StateOf<'execution'> => (EXEC_STATE[s] ?? (['preparing', 'running', 'waiting', 'succeeded', 'failed', 'cancelled'].includes(s) ? s : 'running')) as StateOf<'execution'>;

function TaskDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const { data: t, error, state } = useDoc<any>(`task:${id}`, () => op('task_get', { taskId: id }), c => c.taskId === id || c.id === id);
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
    time: e.startedAt && (e.endedAt ?? e.finishedAt) ? { value: (ms(e.endedAt ?? e.finishedAt)! - ms(e.startedAt)!) / 1000, basis: 'measured' } : { value: null, basis: 'unavailable', reason: 'not finished' } }));
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
