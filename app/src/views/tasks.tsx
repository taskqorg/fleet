// Tasks list and detail (docs/design-system/pages/tasks.md). Server-ordered list with views, state counters, grouping and
// search (list-view pattern); the detail shows outcome, totals, open decisions and the execution tree.
import { useCallback, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  ListDetail, ListFrame, FilterBar, Presets, StateCounters, GroupHeader, GroupControl, SortControl, RowFacts, Fact, VirtualList, ContextMenu, SelectBar, SelectToggle, RowPin, ProblemRow, copyText, StateBadge, HarnessMark, AgeChip, Button,
  SplitButton, RowCheck, Totals, Icon, DetailSection, Markdown, actions, OutcomeBlock, DecisionBlock, ExecTree, Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses,
  type TreeNode, type Harness, type StateOf, states as states_,
  keyConnection, EntityCode, actor, fileLabel, viewOf,
} from '../../../packages/ui/src/index.ts';
import tokens from '../../../packages/ui/tokens.json';
import { op, online, addRequestedKey, writeOnce, useAlive, type Ops } from '../api.ts';
import { useList, useDoc } from '../store.ts';
import { chunks, compareKeys } from '../lists.ts';
import { listKey, formatView, DEFAULT_TASK_SORT, DEFAULT_TASK_GROUP, type View } from '../../../packages/view-url/view-url.ts';
import { useUrlView, useSearchBox } from '../url.ts';
import { usePins, useViews, withFocus, setFocus } from '../prefs.ts';
import { ScopedRollupDetails, buildExecTree, resourceTotals } from './resources.tsx';

const ROW = parseInt(tokens.size['row-2']); // two-line catalogue row
const GROUPS = [['priority', 'Priority'], ['state', 'State'], ['project', 'Project'], ['agent', 'Agent'], ['host', 'Host'], ['none', 'None']] as const;
const SORTS = [['changed', 'Changed'], ['created', 'Created']] as const;
type TaskRow = { id: string; code?: string | null; title: string; state: StateOf<'task'>; pauseReason?: string | null; waitReason: string | null; priority: 'urgent' | 'normal' | 'background'; agent: string | null; host: string | null; startedAt: string | null; createdAt: string; projectId: string | null; openQuestions: number;
  stateAt?: string; executions?: number; lastAgent?: string | null; sortKey: (string | number)[] };
type Item = { kind: 'group'; key: string; label: string; count: number; icon?: 'pin' } | { kind: 'task'; t: TaskRow; pinned?: boolean };
const PINNED = '\u0000pinned'; // group key of the pinned rows; never a server group key
const ms = (v: string | number | null | undefined) => (v === null || v === undefined ? null : typeof v === 'number' ? v : Date.parse(v));
const isHarness = (h: string | null): h is Harness => !!h && h in harnesses;

// System presets (P5): fixed, shown before the person's saved views. Running: every non-terminal state.
// A system preset sets the state filter only; grouping, sort, projects and search stay as the person set them.
const SYSTEM_VIEWS = [{ name: 'Needs me', view: { view: 'tasks' as const, state: ['failed', 'waiting'] } }, { name: 'Running', view: { view: 'tasks' as const, state: ['queued', 'in_progress', 'waiting', 'held', 'paused'] } }];

export function Tasks({ id }: { id?: string }) {
  const { view, set, href } = useUrlView('tasks');
  const [closed, setClosed] = useState<string[]>([]);
  const [query, setQuery] = useSearchBox(view.q, q => set({ q: q || undefined, preset: undefined }, 'type'));
  const pins = usePins('tasks');
  const views = useViews('tasks', SYSTEM_VIEWS);
  const list = useList('tasks', view);
  const group = view.group ?? DEFAULT_TASK_GROUP, states = (view.state ?? []) as StateOf<'task'>[];
  // Rows on screen may still be the previous list (pending): headers and chips follow that list's own grouping.
  const rowsGroup = list.shownView.group ?? DEFAULT_TASK_GROUP;
  const rows = list.rows as TaskRow[];
  const sel = view.sel ?? [], toggleSel = (x: string) => set({ sel: sel.includes(x) ? sel.filter(i => i !== x) : [...sel, x] });
  // Selection mode (OR-37): a checkbox column left of the state, the action bar in place of group and sort.
  const [selectOn, setSelectOn] = useState(false);
  const selecting = selectOn || sel.length > 0;
  const [confirmCancel, setConfirmCancel] = useState<TaskRow[] | null>(null);
  const [bulkError, setBulkError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const leave = useCallback(() => { setSelectOn(false); setConfirmCancel(null); setBulkError(null); set({ sel: undefined }); }, [set]);
  const [menu, setMenu] = useState<{ t: TaskRow; at: { x: number; y: number } } | null>(null);
  const closeMenu = useCallback(() => setMenu(null), []);
  // Pinned tasks stay in the list whatever the filter, search or grouping (OR-12): they are read by id, unfiltered,
  // and live through task changes; the filtered page supplies the rest.
  const pinSort = view.sort ?? DEFAULT_TASK_SORT;
  const pinnedDoc = useDoc<TaskRow[]>(pins.ids.length ? JSON.stringify(['task-pins', pins.ids, pinSort]) : null,
    async () => {
      const found: TaskRow[] = [];
      // The operation accepts at most 1,000 IDs; sequential batches avoid a request burst for a large pin collection.
      for (const ids of chunks(pins.ids, 1000)) {
        const reply = await op('list_membership', { ids, lists: [{ name: 'tasks', filter: {}, group: 'none', sort: pinSort }] });
        found.push(...reply.lists[0].rows as unknown as TaskRow[]);
      }
      return found;
    }, c => c.kind === 'task' && pins.has(c.id) || !!c.taskId && pins.has(c.taskId));
  const byId = new Map((pinnedDoc.data ?? []).map(r => [r.id, r]));
  // The page supplies a just-pinned row while its independent read is pending. Its grouping key is irrelevant here.
  const pinned = pins.ids.map(id => byId.get(id) ?? rows.find(r => r.id === id))
    .filter((r): r is TaskRow => !!r).sort((a, b) => compareKeys(a.sortKey.slice(1), b.sortKey.slice(1)));
  const rest = rows.filter(r => !pins.has(r.id)), displayed = [...pinned, ...rest];
  const groupOf = (r: TaskRow) => String(r.sortKey[0]);
  const items: Item[] = [];
  // Pinned rows come first, as a group of their own (OR-12); each shows its pin before the state (RowPin).
  if (pinned.length) {
    items.push({ kind: 'group', key: PINNED, label: 'Pinned', count: pinned.length, icon: 'pin' });
    if (!closed.includes(PINNED)) for (const t of pinned) items.push({ kind: 'task', t, pinned: true });
  }
  let last: string | null = null;
  for (const t of rest) {
    const g = groupOf(t);
    if (rowsGroup !== 'none' && g !== last) { const meta = list.groups.find(x => x.key === g); items.push({ kind: 'group', key: g, label: groupLabel(rowsGroup, meta?.label ?? null), count: meta?.count ?? 0 }); last = g; }
    if (!closed.includes(g)) items.push({ kind: 'task', t });
  }
  const chosen = displayed.filter(r => sel.includes(r.id));
  const canPause = (t: TaskRow) => !TERMINAL.includes(t.state) && t.state !== 'paused', canResume = (t: TaskRow) => t.state === 'paused';
  const bulk = async (action: 'pause' | 'resume' | 'cancel', ts: TaskRow[]) => {
    if (busy || !ts.length) return;
    setBulkError(null); setBusy(true);
    const r = await Promise.allSettled(ts.map(t => op('task_update', { taskId: t.id, action })));
    setBusy(false);
    const failed = r.filter(x => x.status === 'rejected') as PromiseRejectedResult[];
    if (failed.length) setBulkError(`${failed.length} of ${ts.length} did not ${action}: ${failed[0].reason?.message ?? failed[0].reason}`);
  };
  const off = !online.value ? 'offline' : busy ? 'Working' : undefined;
  const pinAll = (on: boolean) => { for (const t of chosen) if (pins.has(t.id) !== on) pins.toggle(t.id); };
  const select = (t: TaskRow) => { setSelectOn(true); setConfirmCancel(null); toggleSel(t.id); };
  const rowMenu = (t: TaskRow): Parameters<typeof ContextMenu>[0]['items'] => [
    { label: 'Open', icon: actions.open, onClick: () => set({ focus: t.id }) },
    { label: pins.has(t.id) ? 'Unpin' : 'Pin', icon: pins.has(t.id) ? 'pin-off' : 'pin', onClick: () => pins.toggle(t.id) },
    { label: sel.includes(t.id) ? 'Deselect' : 'Select', icon: 'square', onClick: () => select(t) },
    { label: 'Copy code', icon: actions.copy, onClick: () => copyText(t.code ?? t.id) },
    { label: 'Copy link', icon: 'link', onClick: () => copyText(location.origin + formatView({ view: 'tasks', focus: t.id })) },
    ...(canResume(t) ? [{ label: t.pauseReason === 'held' ? 'Release' : 'Continue', icon: 'rotate-cw' as const, disabledReason: off, onClick: () => bulk('resume', [t]) }] : []),
    ...(canPause(t) ? [{ label: 'Pause', icon: 'pause' as const, disabledReason: off, onClick: () => bulk('pause', [t]) }] : []),
  ];
  const shown = states.length ? states.reduce((n, s) => n + (list.counts[s] ?? 0), 0) : list.total;
  const now = Date.now();
  const filtered = !!(states.length || view.q || view.project?.length), focused = !view.project?.length && !!withFocus(view).project?.length;
  const presetKey = (v: View) => listKey({ ...v, view: 'tasks' });
  // A personal view matches the whole view and wins over a system preset with the same states; a system preset matches the
  // states alone. Saved views from before the project default kept no group: they were grouped by priority.
  const stateKey = (state?: string[]) => listKey({ view: 'tasks', state: state ?? [] });
  const savedView = (v: View): View => v.group ? v : { ...v, group: 'priority' };
  const mine = views.list.filter(v => !v.system), system = views.list.filter(v => v.system);
  const activePreset = mine.find(v => presetKey(savedView(v.view)) === listKey(view))?.name
    ?? (view.state?.length ? system.find(v => stateKey(v.view.state) === stateKey(view.state))?.name : undefined) ?? null;
  const pick = (n: string) => {
    const p = views.list.find(x => x.name === n)!, sv = savedView(p.view);
    set(p.system ? { state: p.view.state ?? [], preset: n } : { state: sv.state ?? [], group: sv.group, sort: sv.sort, project: sv.project ?? [], q: sv.q, preset: n });
  };
  const listPane = <>
    <header class="page-h"><h1>Tasks</h1><OfflineMarker state={list.state} since={list.at || undefined} now={now} /></header>
    <section class="panel">
      <ListFrame pending={list.pending} bar={<FilterBar query={query} placeholder="Search tasks, agent:claude host:mac" onQuery={setQuery} shown={shown} total={list.total}
        extra={<>
          <Presets system={views.list.filter(v => v.system).map(v => v.name)} presets={views.list.filter(v => !v.system).map(v => v.name)} active={activePreset} onPick={pick}
            onSave={n => views.save({ name: n, view: { view: 'tasks', state: view.state, group: view.group ?? DEFAULT_TASK_GROUP, sort: view.sort, project: view.project, q: view.q } })} onUnpin={views.remove} />
        </>}
        counters={
          <StateCounters family="task" label="Tasks by state" pending={list.pending} counts={list.counts as any} active={states} onAll={() => set({ state: [], preset: undefined })}
            onToggle={s => set({ state: states.includes(s) ? states.filter(x => x !== s) : [...states, s], preset: undefined })} />}
        tools={<>
          <SelectToggle on={selecting} onClick={() => selecting ? leave() : setSelectOn(true)} />
          <GroupControl value={group as typeof GROUPS[number][0]} options={GROUPS} onChange={g => set({ group: g, preset: undefined })} />
          <SortControl value={view.sort ?? DEFAULT_TASK_SORT} fields={SORTS} onChange={v => set({ sort: v, preset: undefined })} />
        </>}
        selection={selecting && <SelectBar count={chosen.length} total={displayed.length} onAll={() => { setSelectOn(true); setConfirmCancel(null); set({ sel: displayed.map(r => r.id) }); }} onNone={() => { setSelectOn(true); setConfirmCancel(null); set({ sel: [] }); }} onExit={leave} actions={[
              { label: 'Pin', icon: 'pin', onClick: () => pinAll(true), disabledReason: chosen.length && chosen.every(t => pins.has(t.id)) ? 'All pinned' : undefined },
              { label: 'Unpin', icon: 'pin-off', onClick: () => pinAll(false), disabledReason: chosen.some(t => pins.has(t.id)) ? undefined : 'None pinned' },
              { label: 'Pause', icon: 'pause', onClick: () => bulk('pause', chosen.filter(canPause)), disabledReason: off ?? (chosen.some(canPause) ? undefined : 'None can pause') },
              { label: 'Continue', icon: 'rotate-cw', onClick: () => bulk('resume', chosen.filter(canResume)), disabledReason: off ?? (chosen.some(canResume) ? undefined : 'None paused') },
              { label: 'Cancel', icon: 'circle-slash', danger: true, onClick: () => setConfirmCancel(chosen.filter(t => !TERMINAL.includes(t.state))), disabledReason: off ?? (chosen.some(t => !TERMINAL.includes(t.state)) ? undefined : 'None can cancel') },
            ]} />} />}>
        {confirmCancel && <ProblemRow severity="warning" title={`Cancel ${confirmCancel.length} ${confirmCancel.length === 1 ? 'task' : 'tasks'}?`} facts="Running work is asked to stop; finished external effects stay."
          actions={[{ label: 'Yes, cancel', primary: true, onClick: () => { const ts = confirmCancel; setConfirmCancel(null); bulk('cancel', ts); } }, { label: 'Keep', onClick: () => setConfirmCancel(null) }]} />}
        {bulkError && <ErrorBanner text={bulkError} />}
        {list.error && <ErrorBanner text={`Tasks did not load: ${list.error}`} onRetry={list.reload} />}
        {pinnedDoc.error && <ErrorBanner text={`Pinned tasks did not load: ${pinnedDoc.error}`} onRetry={pinnedDoc.reload} />}
        {!displayed.length && (list.shownKey === null || list.pending || (pins.ids.length > 0 && pinnedDoc.data === null && !pinnedDoc.error)) ? <Skeleton rows={8} />
          : !displayed.length ? (list.error || pinnedDoc.error ? null : <Empty text={filtered ? 'No tasks match these filters.' : focused ? 'No tasks in the focused projects.' : 'No tasks yet.'} action={filtered ? <Button label="Clear filters" onClick={() => { setQuery(''); set({ state: [], q: undefined, project: [] }); }} /> : focused ? <Button label="Show all projects" onClick={() => setFocus([])} /> : undefined} />)
          : <VirtualList items={items} getKey={i => i.kind === 'group' ? 'g:' + i.key : i.t.id} rowHeight={ROW} follow="top" label="Tasks" onEnd={list.more}
            columns={`${selecting ? '1.25rem ' : ''}1.25rem minmax(0,1fr) auto`} rowClass={i => i.kind === 'group' ? 'grp' : `${i.t.id === id ? 'sel' : ''}${sel.includes(i.t.id) ? ' checked' : ''}${i.pinned ? ' pin-row' : ''}`}
            onRowMenu={(i, at) => { if (i.kind === 'task') setMenu({ t: i.t, at }); }}
            render={i => i.kind === 'group'
              ? <GroupHeader icon={i.icon ?? (rowsGroup === 'priority' ? 'list-ordered' : 'folder-open')} label={i.label} count={i.count} open={!closed.includes(i.key)} onToggle={() => setClosed(c => c.includes(i.key) ? c.filter(x => x !== i.key) : [...c, i.key])} />
              : <>
                {selecting && <span class="lead"><RowCheck checked={sel.includes(i.t.id)} label={'Select ' + i.t.title} onToggle={() => select(i.t)} /></span>}
                {i.pinned && <span class="lead"><RowPin label={i.t.title} onUnpin={() => {
                  // In selection mode the pin selects like the rest of the row; otherwise it unpins and focus follows the row to its group.
                  if (selecting) return select(i.t);
                  pins.toggle(i.t.id); requestAnimationFrame(() => document.querySelector<HTMLElement>(`.vl-row[data-key="${CSS.escape(i.t.id)}"] .row-link`)?.focus({ preventScroll: true }));
                }} /></span>}
                <span class="lead"><StateBadge family="task" state={i.t.pauseReason === 'held' ? 'held' : i.t.state} reason={i.t.waitReason ?? undefined} compact /></span>
                <span class="tt">{rowsGroup !== 'priority' && <PriorityChip p={i.t.priority} />}<a class="title row-link" href={href({ focus: i.t.id })} aria-keyshortcuts="P X"
                  onClick={e => { e.preventDefault(); if (selecting || e.shiftKey) select(i.t); else set({ focus: i.t.id }); }}
                  onKeyDown={e => {
                    if (e.altKey || e.ctrlKey || e.metaKey) return;
                    if (e.key === 'p') { e.preventDefault(); pins.toggle(i.t.id); }
                    else if (e.key === 'x') { e.preventDefault(); select(i.t); }
                    // The Menu key and Shift+F10 fire contextmenu on the row: onRowMenu opens the menu.
                  }}>{i.t.title}</a><EntityCode code={i.t.code} id={i.t.id} /></span>
                <span class="rz"><TaskFacts t={i.t} now={now} /></span>
              </>} />}
      </ListFrame>
      {menu && <ContextMenu at={menu.at} label={menu.t.title} items={rowMenu(menu.t)} onClose={closeMenu} />}
    </section>
  </>;
  return <ListDetail list={listPane} detail={id ? <TaskDetail id={id} onClose={() => set({ focus: undefined })} /> : undefined} detailKey={id} />;
}

const TERMINAL = ['completed', 'failed', 'cancelled'];
/** Row facts of a task (OR-37): time since taken into work, executions, time in the current state, agent, host. */
function TaskFacts({ t, now }: { t: TaskRow; now: number }) {
  const taken = ms(t.startedAt), since = ms(t.stateAt), agent = t.lastAgent ?? t.agent, n = t.executions ?? 0;
  const label = states_.task[t.pauseReason === 'held' ? 'held' : t.state].label;
  return <RowFacts>
    {taken !== null && !TERMINAL.includes(t.state) && <Fact icon="clock" tip="Since taken into work"><AgeChip t={taken} now={now} alertAfter={t.state === 'in_progress' ? 2 * 3600 : undefined} alertLabel="running too long" /></Fact>}
    {n > 0 && <Fact icon={actions.runs} text={String(n)} tip={n === 1 ? '1 execution' : `${n} executions`} />}
    {since !== null && <Fact tip={`${label} for`}><AgeChip t={since} now={now} /></Fact>}
    {isHarness(agent) && <HarnessMark h={agent} />}
    {t.host && <Fact icon="panels-top-left" tip={`Host ${t.host}`} />}
  </RowFacts>;
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

/** Dependencies by code (OR-38); ones the viewer cannot see are only counted. */
function dependsOn(ds: { code?: string | null }[]) {
  const codes = ds.flatMap(d => d.code ? [d.code] : []), hidden = ds.length - codes.length;
  return `Depends on ${[...codes, ...(hidden ? [`${hidden} other task${hidden > 1 ? 's' : ''}`] : [])].join(', ')}`;
}

function TaskDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const [, go] = useLocation();
  const { data: t, error, state } = useDoc<any>(`task:${id}`, () => op('task_get', { taskId: id }), c => c.taskId === id || c.id === id);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const alive = useAlive();
  const [confirming, setConfirming] = useState(false);
  const pins = usePins('tasks');
  const now = Date.now(), off = !online.value;
  const act = async (action: string) => { setBusy(action); setFailure(null); try { await op('task_update', { taskId: id, action }); } catch (e: any) { setFailure(e.message); } finally { setBusy(null); setConfirming(false); } };
  if (error && !t) return <article class="detail" data-ui="detail" tabIndex={-1}><ErrorBanner text={`Task did not load: ${error}`} /></article>;
  if (!t) return <article class="detail" data-ui="detail" tabIndex={-1}><Skeleton rows={6} /></article>;
  const req = t.completionRequirements && typeof t.completionRequirements === 'object' ? Object.entries(t.completionRequirements).map(([k, v]) => ({ text: typeof v === 'string' ? `${k}: ${v}` : k, met: false })) : [];
  const open = (t.questions ?? []).filter((q: any) => q.state === 'open');
  const rollup = (t as Ops['task_get']['reply']).resources;
  const nodes: TreeNode[] = buildExecTree(t.executions ?? []);
  const disabled = off ? 'offline' : undefined;
  // OR-39: statement, lifecycle, result, decision; a total is shown only when it is known (no empty token or cost fields).
  // The execution count is the tree below (OR-17: no second 'Executions').
  const totals = resourceTotals(rollup?.total, { extraCost: t.cost, hideUnavailable: true });
  const latestExecution = t.executions?.at(-1); // task_get preserves execution sequence.
  const reported = (t.executions ?? []).filter((e: any) => e.report).sort((a: any, b: any) => b.report.at - a.report.at)[0];
  const decided = (t.questions ?? []).filter((q: any) => q.state === 'answered').sort((a: any, b: any) => (ms(b.answeredAt) ?? 0) - (ms(a.answeredAt) ?? 0)).slice(0, 3);
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={t.title}>
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
      <span class="muted mono">{t.projectId ?? 'no project'}</span><span class="grow" />
      <OfflineMarker state={state} now={now} />
      <Button label={pins.has(id) ? 'Unpin' : 'Pin'} icon={pins.has(id) ? 'pin-off' : 'pin'} iconOnly pressed={pins.has(id)} onClick={() => pins.toggle(id)} />
      {t.state === 'paused'
        ? <Button label={t.pauseReason === 'held' ? 'Release' : 'Continue'} icon="rotate-cw" busy={busy === 'resume'} disabledReason={disabled} onClick={() => act('resume')} />
        : !['completed', 'failed', 'cancelled'].includes(t.state) && <SplitButton main={{ label: 'Pause', icon: 'pause', onClick: () => !off && act('pause') }} alternatives={[{ label: 'Cancel task', icon: 'circle-slash', onClick: () => setConfirming(true) }]} />}
    </header>
    {confirming && <div class="problem warning" role="group" aria-label="Confirm cancel"><b>Cancel this task?</b><span class="facts">Running work is asked to stop; finished external effects stay.</span>
      <span class="acts"><Button label="Yes, cancel" kind="danger" busy={busy === 'cancel'} disabledReason={off ? 'offline' : undefined} onClick={() => act('cancel')} /><Button label="Keep" onClick={() => setConfirming(false)} /></span></div>}
    {failure && <ErrorBanner text={failure} />}
    <h2 class="detail-title">{t.title}<EntityCode code={t.code} id={t.id} /></h2>
    <div class="state-line"><StateBadge family="task" state={t.pauseReason === 'held' ? 'held' : t.state} reason={t.waitReason ?? undefined} />{t.createdBy && <span class="muted">by {actor(t.createdBy)}</span>}{ms(t.createdAt) && <AgeChip t={ms(t.createdAt)!} now={now} />}</div>
    {t.latestResult?.id && <p class="state-line"><Icon name={viewOf(t.latestResult.mediaType ?? '') === 'download' ? 'archive' : 'file-text'} label={t.latestResult.mediaType ?? 'Result'} />
      <a class="row-link" href={`/results/${encodeURIComponent(t.latestResult.id)}`} onClick={ev => { ev.preventDefault(); go(`/results/${encodeURIComponent(t.latestResult.id)}`); }}>{fileLabel(t.latestResult.path ?? t.latestResult.name ?? '') || 'Result'}</a>
      <EntityCode id={t.latestResult.sha256 ?? t.latestResult.id} what={t.latestResult.sha256 ? 'sha256' : 'Result'} /></p>}
    {open.map((q: any) => <DecisionBlock key={q.id} question={q.text} askedBy={actor(q.askedBy) || q.kind} addressedTo="you" since={ms(q.askedAt) ?? now} now={now}
      options={(Array.isArray(q.options) ? q.options : q.kind === 'acceptance' ? ['accepted', 'rejected'] : keyConnection(q.options) ? ['decline'] : []).map((o: string, i: number) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
      onAnswer={a => { if (!off) writeOnce('questions_answer', { questionId: q.id, answer: a }, () => setFailure('unknown, retrying'), { signal: alive }).then(() => setFailure(null), e => setFailure(e.message)); }}
      onSecret={keyConnection(q.options) ? v => addRequestedKey(q.id, keyConnection(q.options)!, v, alive) : undefined} />)}
    {latestExecution?.problem && <DetailSection title="Latest execution problem">
      <ErrorBanner text={latestExecution.problem} />
    </DetailSection>}
    {reported && <DetailSection title="Latest available worker report">
      <AgeChip t={reported.report.at} now={now} />
      <StateBadge family="execution" state={reported.report.outcome === 'succeeded' ? 'succeeded' : 'failed'} />
      {reported.report.summary ? <Markdown text={reported.report.summary} /> : <p class="muted">No report summary provided.</p>}
    </DetailSection>}
    {t.expectedOutcome && <OutcomeBlock outcome={t.expectedOutcome} requirements={req} inputs={[]} depends={t.dependencies?.length ? dependsOn(t.dependencies) : undefined} />}
    {totals.length > 0 && <Totals items={totals} />}
    <ScopedRollupDetails rollup={rollup} cost={t.cost} title="Resource breakdown" />
    {decided.length > 0 && <DetailSection title="Previous answers">
    {decided.map((q: any) => <p key={q.id} class="state-line"><Icon name={q.answer === 'accepted' ? 'circle-check' : q.answer === 'rejected' ? 'circle-x' : 'message-circle-question'} />
      <span>{q.kind === 'acceptance' ? q.answer : `${q.text}: ${q.answer}`}</span>{q.answeredBy && <span class="muted">by {actor(q.answeredBy)}</span>}{ms(q.answeredAt) && <AgeChip t={ms(q.answeredAt)!} now={now} />}</p>)}
    </DetailSection>}
    <h3 class="sub-h">Executions</h3>
    {nodes.length ? <ExecTree nodes={nodes} now={now} label="Executions" /> : <Empty text="No executions yet." />}
  </article>;
}
