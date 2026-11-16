// Conversations (docs/design-system/pages/conversations.md): sessions list and one conversation with its live transcript.
// Actions use the same operations agents use: session_create, message_send, execution_stop, session_fork.
import { useCallback, useEffect, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  EntityCode, ListDetail, ListFrame, FilterBar, Presets, StateCounters, GroupHeader, GroupControl, SortControl, RowFacts, Fact, AgeChip, ContextMenu, SelectBar, SelectToggle, RowPin, ProblemRow, copyText, actions,
  RowCheck, VirtualList, StateBadge, HarnessMark, Button, Thread, Composer, DecisionBlock, SearchField, ActionMenu, DetailSection, type VirtualListHandle,
  Choice, Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses, type Harness, type StateOf, states as states_,
  writeClip, zone,
  keyConnection,
} from '../../../packages/ui/src/index.ts';
import tokens from '../../../packages/ui/tokens.json';
import { op, online, addRequestedKey, writeOnce, sendingId, prepareSending, authorizedDraftScope, createDraftPersistence, unknownOutcome, AccessChanged, OpError, useAlive, type Ops } from '../api.ts';
import { useList, useDoc } from '../store.ts';
import { chunks, compareKeys } from '../lists.ts';
import { listKey, formatView, DEFAULT_SESSION_GROUP, DEFAULT_SESSION_SORT, type View } from '../../../packages/view-url/view-url.ts';
import { useUrlView, useSearchBox, nextUrl } from '../url.ts';
import { focusIds, usePins, useViews } from '../prefs.ts';
import { useThread, type ThreadKey } from '../threads.ts';
import { click, all, inside, range, type Selection } from '../selection.ts';
import { ScopedRollupBar } from './resources.tsx';

const ROW = parseInt(tokens.size['row-2']); // two-line catalogue row
const GROUPS = [['project', 'Project'], ['state', 'State'], ['agent', 'Agent'], ['none', 'None']] as const;
const SORTS = [['activity', 'Activity'], ['created', 'Created']] as const;
type SessionRow = { id: string; code?: string | null; title: string | null; state: StateOf<'session'>; harness: string | null; openQuestions: number; activityAt?: string | null; executions?: number; projectId?: string | null; sortKey: (string | number)[] };
type Item = { kind: 'group'; key: string; label: string; count: number; icon?: 'pin' } | { kind: 'session'; s: SessionRow; pinned?: boolean };
const PINNED = '\u0000pinned'; // group key of the pinned rows; never a server group key
const isHarness = (h: string | null): h is Harness => !!h && h in harnesses;
const reqId = () => crypto.randomUUID();
const TERMINAL = ['succeeded', 'failed', 'cancelled'];
const titleOf = (s: SessionRow) => s.title ?? 'New conversation';

// System presets (OR-33): fixed, shown before the person's saved views. A system preset sets the state filter only;
// grouping, sort, projects and search stay as the person set them.
const SYSTEM_VIEWS = [{ name: 'Needs me', view: { view: 'conversations' as const, state: ['failed', 'waiting'] } }, { name: 'Running', view: { view: 'conversations' as const, state: ['preparing', 'running', 'waiting'] } }];

export function Conversations({ id }: { id?: string }) {
  const [, go] = useLocation();
  const { view, set, href } = useUrlView('conversations');
  const [query, setQuery] = useSearchBox(view.q, q => set({ q: q || undefined }, 'type'));
  const states = (view.state ?? []) as StateOf<'session'>[];
  const [failure, setFailure] = useState<string | null>(null);
  const [closed, setClosed] = useState<string[]>([]);
  const pins = usePins('conversations');
  const views = useViews('conversations', SYSTEM_VIEWS);
  const list = useList('sessions', view);
  const group = view.group ?? DEFAULT_SESSION_GROUP, sort = view.sort ?? DEFAULT_SESSION_SORT;
  // Rows on screen may still be the previous list (pending): headers follow that list's own grouping.
  const rowsGroup = list.shownView.group ?? DEFAULT_SESSION_GROUP;
  const rows = list.rows as SessionRow[];
  const sel = view.sel ?? [], toggleSel = (x: string) => set({ sel: sel.includes(x) ? sel.filter(i => i !== x) : [...sel, x] });
  // Selection mode (OR-37): a checkbox column left of the state, the action bar under the tools.
  const [selectOn, setSelectOn] = useState(false);
  const selecting = selectOn || sel.length > 0;
  const [confirmArchive, setConfirmArchive] = useState<SessionRow[] | null>(null);
  const [bulkError, setBulkError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const leave = useCallback(() => { setSelectOn(false); setConfirmArchive(null); setBulkError(null); set({ sel: undefined }); }, [set]);
  const [menu, setMenu] = useState<{ s: SessionRow; at: { x: number; y: number } } | null>(null);
  const closeMenu = useCallback(() => setMenu(null), []);
  // Pinned conversations stay whatever the filter, search or grouping (OR-12): read by id with an empty filter.
  const pinnedDoc = useDoc<SessionRow[]>(pins.ids.length ? JSON.stringify(['session-pins', pins.ids, sort]) : null,
    async () => {
      const found: SessionRow[] = [];
      for (const ids of chunks(pins.ids, 1000)) { // the operation takes at most 1,000 ids
        const reply = await op('list_membership', { ids, lists: [{ name: 'sessions', filter: {}, group: 'none', sort }] });
        found.push(...reply.lists[0].rows as unknown as SessionRow[]);
      }
      return found;
    }, c => (c.kind === 'session' && pins.has(c.id)) || (!!c.sessionId && pins.has(c.sessionId)));
  const byId = new Map((pinnedDoc.data ?? []).map(r => [r.id, r]));
  // The page supplies a just-pinned row while its own read is pending. Its grouping key is irrelevant here.
  const pinned = pins.ids.map(x => byId.get(x) ?? rows.find(r => r.id === x))
    .filter((r): r is SessionRow => !!r).sort((a, b) => compareKeys(a.sortKey.slice(1), b.sortKey.slice(1)));
  const rest = rows.filter(r => !pins.has(r.id)), displayed = [...pinned, ...rest];
  const items: Item[] = [];
  if (pinned.length) {
    items.push({ kind: 'group', key: PINNED, label: 'Pinned', count: pinned.length, icon: 'pin' });
    if (!closed.includes(PINNED)) for (const s of pinned) items.push({ kind: 'session', s, pinned: true });
  }
  let last: string | null = null;
  for (const s of rest) {
    const g = String(s.sortKey[0]);
    if (rowsGroup !== 'none' && g !== last) { const meta = list.groups.find(x => x.key === g); items.push({ kind: 'group', key: g, label: groupLabel(rowsGroup, meta?.label ?? null), count: meta?.count ?? 0 }); last = g; }
    if (!closed.includes(g)) items.push({ kind: 'session', s });
  }
  const chosen = displayed.filter(r => sel.includes(r.id));
  const canArchive = (s: SessionRow) => s.state === 'idle' || TERMINAL.includes(s.state);
  // Archive is one operation per conversation (session_archive); a conversation with a running execution refuses it.
  const archive = async (ss: SessionRow[]) => {
    if (busy || !ss.length) return;
    setBulkError(null); setBusy(true);
    const r = await Promise.allSettled(ss.map(s => op('session_archive', { sessionId: s.id })));
    setBusy(false);
    const failed = r.filter(x => x.status === 'rejected') as PromiseRejectedResult[];
    if (failed.length) setBulkError(`${failed.length} of ${ss.length} not archived: ${failed[0].reason?.message ?? failed[0].reason}`);
    else set({ sel: sel.filter(x => !ss.some(s => s.id === x)) });
  };
  const off = !online.value ? 'offline' : busy ? 'Working' : undefined;
  const pinAll = (on: boolean) => { for (const s of chosen) if (pins.has(s.id) !== on) pins.toggle(s.id); };
  const select = (s: SessionRow) => { setSelectOn(true); setConfirmArchive(null); toggleSel(s.id); };
  const rowMenu = (s: SessionRow): Parameters<typeof ContextMenu>[0]['items'] => [
    { label: 'Open', icon: actions.open, onClick: () => set({ focus: s.id }) },
    { label: pins.has(s.id) ? 'Unpin' : 'Pin', icon: pins.has(s.id) ? 'pin-off' : 'pin', onClick: () => pins.toggle(s.id) },
    { label: sel.includes(s.id) ? 'Deselect' : 'Select', icon: 'square', onClick: () => select(s) },
    { label: 'Copy code', icon: actions.copy, onClick: () => copyText(s.code ?? s.id) },
    { label: 'Copy link', icon: 'link', onClick: () => copyText(location.origin + formatView({ view: 'conversations', focus: s.id })) },
    ...(!view.archived ? [{ label: 'Archive', icon: 'archive' as const, disabledReason: off ?? (canArchive(s) ? undefined : 'Execution active'), onClick: () => setConfirmArchive([s]) }] : []),
  ];
  // A personal view matches the whole view and wins over a system preset with the same states; a system preset matches the
  // states alone (as on Tasks).
  const presetKey = (v: View) => listKey({ ...v, view: 'conversations' });
  const stateKey = (state?: string[]) => listKey({ view: 'conversations', state: state ?? [] });
  const activePreset = views.list.find(v => !v.system && presetKey(v.view) === listKey(view))?.name
    ?? (view.state?.length ? views.list.find(v => v.system && stateKey(v.view.state) === stateKey(view.state))?.name : undefined) ?? null;
  const pick = (n: string) => {
    const p = views.list.find(x => x.name === n)!;
    set(p.system ? { state: p.view.state ?? [], preset: n } : { state: p.view.state ?? [], group: p.view.group, sort: p.view.sort, project: p.view.project ?? [], q: p.view.q, preset: n });
  };
  const filtered = !!(states.length || view.q || view.project?.length);
  const now = Date.now();
  // ?new=<project> opens the form once; the key leaves the URL so a reload does not open it again.
  const [newConversation,setNewConversation]=useState(()=>!!new URLSearchParams(location.search).get('new'));
  useEffect(()=>{const u=new URL(location.href);if(u.searchParams.has('new')){u.searchParams.delete('new');history.replaceState(history.state,'',u.pathname+u.search);}},[]);
  const create=()=>setNewConversation(true);
  const pane = <>
    <header class="page-h"><h1>Conversations</h1><OfflineMarker state={list.state} now={now} /><span class="grow" />
      <Button label="New conversation" icon="plus" kind="primary" disabledReason={online.value ? undefined : 'offline'} onClick={create} /></header>
    {failure && <ErrorBanner text={failure} />}
    {newConversation&&<NewConversation onDone={sessionId=>{setNewConversation(false);set({focus:sessionId});}}/>}
    <section class="panel">
      <ListFrame pending={list.pending} bar={<FilterBar query={query} placeholder="Search conversations" onQuery={setQuery}
        shown={states.length ? states.reduce((n, s) => n + (list.counts[s] ?? 0), 0) : list.total} total={list.total}
        extra={<Presets system={views.list.filter(v => v.system).map(v => v.name)} presets={views.list.filter(v => !v.system).map(v => v.name)} active={activePreset} onPick={pick}
          onSave={n => views.save({ name: n, view: { view: 'conversations', state: view.state, group: view.group, sort: view.sort, project: view.project, q: view.q } })} onUnpin={views.remove} />}
        counters={<StateCounters family="session" label="Conversations by state" pending={list.pending} counts={list.counts as any} active={states} onAll={() => set({ state: [], preset: undefined })}
          onToggle={s => set({ state: states.includes(s) ? states.filter(x => x !== s) : [...states, s], preset: undefined })} />}
        tools={<>
          <SelectToggle on={selecting} onClick={() => selecting ? leave() : setSelectOn(true)} />
          <GroupControl value={group as typeof GROUPS[number][0]} options={GROUPS} onChange={g => set({ group: g, preset: undefined })} />
          <SortControl value={sort} fields={SORTS} onChange={v => set({ sort: v, preset: undefined })} />
        </>}
        selection={selecting && <SelectBar count={chosen.length} total={displayed.length} onAll={() => { setSelectOn(true); setConfirmArchive(null); set({ sel: displayed.map(r => r.id) }); }} onNone={() => { setSelectOn(true); setConfirmArchive(null); set({ sel: [] }); }} onExit={leave} actions={[
          { label: 'Pin', icon: 'pin', onClick: () => pinAll(true), disabledReason: chosen.length && chosen.every(s => pins.has(s.id)) ? 'All pinned' : undefined },
          { label: 'Unpin', icon: 'pin-off', onClick: () => pinAll(false), disabledReason: chosen.some(s => pins.has(s.id)) ? undefined : 'None pinned' },
          { label: 'Archive', icon: 'archive', danger: true, onClick: () => setConfirmArchive(chosen.filter(canArchive)), disabledReason: off ?? (view.archived ? 'Already archived' : chosen.some(canArchive) ? undefined : 'None can be archived') },
        ]} />} />}>
        {confirmArchive && <ProblemRow severity="warning" title={`Archive ${confirmArchive.length} ${confirmArchive.length === 1 ? 'conversation' : 'conversations'}?`} facts="Their worktrees are torn down; the owner can restore a conversation."
          actions={[{ label: 'Yes, archive', primary: true, onClick: () => { const ss = confirmArchive; setConfirmArchive(null); archive(ss); } }, { label: 'Keep', onClick: () => setConfirmArchive(null) }]} />}
        {bulkError && <ErrorBanner text={bulkError} />}
        {list.error && <ErrorBanner text={`Conversations did not load: ${list.error}`} onRetry={list.reload} />}
        {pinnedDoc.error && <ErrorBanner text={`Pinned conversations did not load: ${pinnedDoc.error}`} onRetry={pinnedDoc.reload} />}
        {!displayed.length && (list.shownKey === null || list.pending || (pins.ids.length > 0 && pinnedDoc.data === null && !pinnedDoc.error)) ? <Skeleton rows={8} />
          : !displayed.length ? (list.error || pinnedDoc.error ? null : <Empty text={filtered ? 'No conversations match these filters.' : focusIds() && !view.project?.length ? 'No conversations in the focused projects.' : 'No conversations yet.'}
            action={filtered ? <Button label="Clear filters" onClick={() => { setQuery(''); set({ state: [], q: undefined, project: [], preset: undefined }); }} /> : <Button label="New conversation" onClick={create} />} />)
          : <VirtualList items={items} getKey={i => i.kind === 'group' ? 'g:' + i.key : i.s.id} rowHeight={ROW} follow="top" label="Conversations" onEnd={list.more}
            columns={`${selecting ? '1.25rem ' : ''}1.25rem minmax(0,1fr) auto`} rowClass={i => i.kind === 'group' ? 'grp' : `${i.s.id === id ? 'sel' : ''}${sel.includes(i.s.id) ? ' checked' : ''}${i.pinned ? ' pin-row' : ''}`}
            onRowMenu={(i, at) => { if (i.kind === 'session') setMenu({ s: i.s, at }); }}
            render={i => i.kind === 'group'
              ? <GroupHeader icon={i.icon ?? (rowsGroup === 'state' ? 'activity' : rowsGroup === 'agent' ? 'bot' : 'folder-open')} label={i.label} count={i.count} open={!closed.includes(i.key)} onToggle={() => setClosed(c => c.includes(i.key) ? c.filter(x => x !== i.key) : [...c, i.key])} />
              : <>
                {selecting && <span class="lead"><RowCheck checked={sel.includes(i.s.id)} label={'Select ' + titleOf(i.s)} onToggle={() => select(i.s)} /></span>}
                {i.pinned && <span class="lead"><RowPin label={titleOf(i.s)} onUnpin={() => {
                  // In selection mode the pin selects like the rest of the row; otherwise it unpins and focus follows the row to its group.
                  if (selecting) return select(i.s);
                  pins.toggle(i.s.id); requestAnimationFrame(() => document.querySelector<HTMLElement>(`.vl-row[data-key="${CSS.escape(i.s.id)}"] .row-link`)?.focus({ preventScroll: true }));
                }} /></span>}
                <span class="lead"><StateBadge family="session" state={i.s.state} compact /></span>
                <span class="tt"><a class="title row-link" href={href({ focus: i.s.id })} aria-keyshortcuts="P X"
                  onClick={e => { e.preventDefault(); if (selecting || e.shiftKey) select(i.s); else set({ focus: i.s.id }); }}
                  onKeyDown={e => {
                    if (e.altKey || e.ctrlKey || e.metaKey) return;
                    if (e.key === 'p') { e.preventDefault(); pins.toggle(i.s.id); }
                    else if (e.key === 'x') { e.preventDefault(); select(i.s); }
                  }}>{titleOf(i.s)}</a><EntityCode code={i.s.code} id={i.s.id} /></span>
                <span class="rz"><SessionFacts s={i.s} now={now} /></span>
              </>} />}
      </ListFrame>
      {menu && <ContextMenu at={menu.at} label={titleOf(menu.s)} items={rowMenu(menu.s)} onClose={closeMenu} />}
    </section>
  </>;
  return <ListDetail list={pane} detail={id ? <Conversation key={id} id={id} onClose={() => set({ focus: undefined })} /> : undefined} detailKey={id} />;
}

/** Row facts of a conversation (OR-37): time since the last activity, open questions, executions, agent. */
function SessionFacts({ s, now }: { s: SessionRow; now: number }) {
  const at = s.activityAt ? Date.parse(s.activityAt) : NaN, n = s.executions ?? 0, q = s.openQuestions ?? 0;
  return <RowFacts>
    {q > 0 && <span class="chip-age t-attention alert" aria-label={q === 1 ? '1 open question' : `${q} open questions`}>{q}</span>}
    {n > 0 && <Fact icon={actions.runs} text={String(n)} tip={n === 1 ? '1 execution' : `${n} executions`} />}
    {!isNaN(at) && <Fact tip="Last activity"><AgeChip t={at} now={now} /></Fact>}
    {isHarness(s.harness) && <HarnessMark h={s.harness} />}
  </RowFacts>;
}

function groupLabel(group: string, label: string | null) {
  if (group === 'agent') return label && label in harnesses ? harnesses[label as Harness].label : label ?? 'No agent';
  if (group === 'state') return label && label in states_.session ? states_.session[label as StateOf<'session'>].label : label ?? '';
  return label ?? 'No project';
}

function Conversation({ id, onClose }: { id: string; onClose: () => void }) {
  const [, go] = useLocation();
  const [verifiedTarget, setVerifiedTarget] = useState<string | null>(null);
  const { data: s, error, state, restored } = useDoc<any>(`session:${id}`, () => op('session_get', { sessionId: id }).then(reply => { setVerifiedTarget(id); return reply; }), c => c.sessionId === id && c.kind !== 'event');
  // A session that got a task shows the task's thread (design "Thread scope"): the first page resolves it and returns the key.
  const thread = useThread({ session: id });
  const key: ThreadKey = thread.win?.key ?? { session: id };
  // A plain click marks where a selection starts (no tint, so reading stays quiet); Shift+click selects up to that row.
  const [sel, setSel] = useState<Selection>(null), pickFrom = useRef<string | null>(null);
  // Cmd+A selects the thread; Cmd+C copies the selected rows from the server as markdown and chat-safe HTML. The clipboard write starts inside the
  // key handler with a promise, so the browser keeps the user gesture while the export loads.
  const copySel = (s0: NonNullable<Selection>) => {
    try {
      // Both formats (formats.md "Copy"); times in the reader's own zone.
      const text = op('thread_export', { key, ...range(s0), tz: zone() }).then(r => r.markdown);
      writeClip(text).catch(e => copyFail(e, () => copySel(s0)));
    } catch (e) { copyFail(e, () => copySel(s0)); } // no async clipboard (plain http): say so instead of failing silently
  };
  // The key handler reads the latest selection through a ref: effects run after paint, and a key can come first.
  const live = useRef({ sel, copySel }); live.current = { sel, copySel };
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      const sel = live.current.sel, copySel = live.current.copySel;
      const t = e.target as HTMLElement;
      // Only inside the conversation (or with nothing focused); code, not key, so other keyboard layouts work too.
      if (!(e.metaKey || e.ctrlKey) || t?.closest?.('input,textarea') || !(t?.closest?.('.conversation') || t === document.body)) return;
      if (e.code === 'KeyA') { e.preventDefault(); setSel(all()); }
      // A native selection inside one row is the browser's own copy.
      if (e.code === 'KeyC' && sel && (getSelection()?.isCollapsed ?? true)) { e.preventDefault(); copySel(sel); }
    };
    addEventListener('keydown', on); return () => removeEventListener('keydown', on);
  }, []);
  const [answers, setAnswers] = useState<Record<string, Ops['questions_answer']['reply']>>({});
  async function answerQuestion(questionId: string, answer: string) {
    const row = await writeOnce('questions_answer', { questionId, answer }, unknown, { signal: alive.current.signal });
    setAnswers(current => ({ ...current, [row.id]: row }));
    return row;
  }
  const [failure, setFailure] = useState<{ text: string; retry?: () => void } | null>(null);
  // Writes in flight stop when the conversation closes.
  const alive = useRef(new AbortController()); useEffect(() => () => alive.current.abort(), []);
  const unknown = () => setFailure({ text: 'unknown, retrying' });
  const [panel, setPanel] = useState<'switch' | 'handoff' | null>(null);
  const [q, setQ] = useState(''), [hits, setHits] = useState<{ key: string; snippet: string }[] | null>(null), [hit, setHit] = useState(0), [marked, setMarked] = useState<string | null>(null);
  const handle = useRef<VirtualListHandle | null>(null);
  // The hit is scrolled to once the replaced window is on screen.
  const [jumpTo, setJumpTo] = useState<string | null>(null);
  useEffect(() => { if (jumpTo && thread.win?.blocks.some(b => b.key === jumpTo)) { handle.current?.scrollToKey(jumpTo, 'center'); setJumpTo(null); } }, [thread.win, jumpTo]);
  const now = Date.now(), off = !online.value;
  const run = async (fn: () => Promise<unknown>) => { setFailure(null); try { await fn(); } catch (e: any) { setFailure({ text: e.message, retry: () => run(fn) }); } };
  const copyFail = (e: unknown, retry?: () => void) => setFailure({ text: `Copy failed: ${(e as any)?.message ?? e}`, retry });
  // A jump replaces the window with entries around the hit, then puts the hit row in view and marks it.
  const open = async (list: { key: string }[], i: number) => { if (!list[i]) return; setHit(i); setMarked(list[i].key); await thread.jump(list[i].key); setJumpTo(list[i].key); };
  const search = () => run(async () => { if (!q.trim()) { setHits(null); setMarked(null); return; } const r = await op('thread_search', { key, q, limit: 100 }); setHits(r.hits); await open(r.hits, 0); });
  if (error && !s) return <article class="detail" data-ui="detail" tabIndex={-1}><ErrorBanner text={`Conversation did not load: ${error}`} /></article>;
  if (!s) return <article class="detail" data-ui="detail" tabIndex={-1}><Skeleton rows={6} /></article>;
  const running = (e: any) => !['succeeded', 'failed', 'cancelled'].includes(e.state) && e.observation !== 'unconfirmed';
  // Composer target: the newest running execution; with none, a new message starts one (design "Controls").
  const active = (s.executions ?? []).filter(running).at(-1);
  const openQs = (s.questions ?? []).filter((q: any) => (answers[q.id] ?? q).state === 'open');
  const w = thread.win;
  const lastMessage: string | undefined = (s.messages ?? []).at(-1)?.text;
  return <article class="detail conversation" data-ui="detail" tabIndex={-1} aria-label="Conversation">
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
      <EntityCode code={s.code} id={id} what="Conversation" />{s.agentConfig?.id && <span class="muted mono">{s.agentConfig.id}</span>}<span class="grow" />
      <SearchField label="Search thread" value={q} onInput={setQ} onSearch={search} />
      <span class="count" aria-live="polite">{hits ? `${hits.length ? hit + 1 : 0} of ${hits.length}` : w ? `${w.blocks.length.toLocaleString('en')} of ${w.total.toLocaleString('en')}` : ''}</span>
      <Button label="Previous hit" icon="arrow-up" iconOnly disabledReason={!hits || hit <= 0 ? 'no earlier hit' : undefined} onClick={() => hits && open(hits, hit - 1)} />
      <Button label="Next hit" icon="arrow-down" iconOnly disabledReason={!hits || hit >= hits.length - 1 ? 'no later hit' : undefined} onClick={() => hits && open(hits, hit + 1)} />
      <OfflineMarker state={state} now={now} />
      <Button label="Copy thread as markdown" icon="file-text" iconOnly disabledReason={off ? 'offline' : undefined} onClick={() => copySel(all()!)} />
      {/* Menu actions (design "Controls"): each calls the existing operation; the result shows as a new boundary in this thread. */}
      <ActionMenu label="Actions" disabledReason={off ? 'offline' : undefined} items={[
        { label: 'Retry last message', icon: 'rotate-ccw', disabledReason: lastMessage ? undefined : 'no message yet', onClick: () => { const requestId = reqId(); run(() => writeOnce('message_send', { sessionId: id, requestId, text: lastMessage }, unknown, { signal: alive.current.signal })); } },
        { label: 'Switch profile or model', icon: 'sliders-horizontal', onClick: () => setPanel('switch') },
        { label: 'Hand off to another agent', icon: 'bot', onClick: () => setPanel('handoff') },
        { label: 'Fork', icon: 'git-merge', onClick: () => { const requestId = reqId(); run(async () => { const f = await writeOnce('session_fork', { sessionId: id, requestId }, unknown, { signal: alive.current.signal }); go(nextUrl(location.pathname + location.search, 'conversations', { focus: f.sessionId })); }); } },
      ]} />
    </header>
    {panel && <SwitchPanel sessionId={id} handoff={panel === 'handoff'} current={s.agentConfig?.id} onDone={note => { setPanel(null); if (note) setFailure(null); }} run={run} />}
    <ScopedRollupBar rollup={s.resources} cost={s.cost} />
    <div class="status-line" role="status">{failure ? <ErrorBanner text={failure.text} onRetry={failure.retry} /> : thread.error ? <ErrorBanner text={`Thread did not load: ${thread.error}`} onRetry={thread.newest} /> : null}</div>
    <div class="pane">
      {!w ? <Skeleton rows={6} /> : <Thread blocks={w.blocks.map(b => b.kind === 'question' && answers[b.questionId] ? { ...b, state: answers[b.questionId].state, answer: answers[b.questionId].answer, answeredBy: answers[b.questionId].answeredBy ?? undefined, answeredAt: answers[b.questionId].answeredAt ? new Date(answers[b.questionId].answeredAt!).getTime() : null } : b)} label="Transcript" now={now} marked={marked} selected={k => inside(sel, k)} onRowClick={(k, shift) => { if (!shift) { pickFrom.current = k; setSel(null); return; } setSel(click(click(null, pickFrom.current ?? sel?.start.key ?? k, false), k, true)); pickFrom.current ??= k; }} showSession={!!key && 'task' in key} onOpenResult={r => go('/results/' + encodeURIComponent(r))} handle={handle} onStart={thread.older} onEnd={thread.newer}
        stopReason={off ? 'offline' : undefined} onStop={x => run(() => op('execution_stop', { executionId: x }))}
        onAnswer={(questionId, answer) => run(() => answerQuestion(questionId, answer))} onCopyFail={copyFail} onSecret={(q: string, c: string, v: string) => addRequestedKey(q, c, v, alive.current.signal)}
        pending={{ count: w.fresh, onJump: () => { setMarked(null); setHits(null); thread.newest(); } }} />}
      {/* D3: steer goes to the execution and does not answer; the open question stays in sight above the composer. */}
      {openQs.map((q: any) => <DecisionBlock key={q.id} question={q.question ?? q.text} askedBy="agent" addressedTo="you" since={q.askedAt ? Date.parse(q.askedAt) || now : now} now={now}
        options={(Array.isArray(q.options) ? q.options : keyConnection(q.options) ? ['decline'] : []).map((o: string, i: number) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
        onAnswer={a => run(() => answerQuestion(q.id, a))}
        onSecret={keyConnection(q.options) ? v => addRequestedKey(q.id, keyConnection(q.options)!, v, alive.current.signal) : undefined} />)}
      <div class="composer-bar">
        {active && <EntityCode id={active.id} what="Run" />}
        {active && <Button label="Stop" icon="pause" disabledReason={off ? 'offline' : undefined} onClick={() => run(() => op('execution_stop', { executionId: active.id }))} />}
      </div>
      <Composer draftPersistence={s && !restored && verifiedTarget === id ? createDraftPersistence(authorizedDraftScope({ kind: 'session', id })) : undefined} disabledReason={off ? 'offline' : verifiedTarget !== id ? 'Validating conversation' : undefined} placeholder={active ? 'Steer' : 'Message'}
        onSend={async text => {
          // One requestId per draft text until it is sent, kept next to the draft: a resend after a failure or a reload is the
          // same write, never a second one. A definite error (nothing stored) clears it.
          const scope = authorizedDraftScope({ kind: 'session', id });
          const request = await prepareSending(scope, 'message_send', { sessionId: id, text, ...(active ? { delivery: 'steer' } : {}) });
          setFailure(null);
          try { await writeOnce('message_send', request, unknown, { signal: alive.current.signal }); sendingId(scope, null, text); setFailure(null); }
          catch (e: any) { if (!(e instanceof AccessChanged) && !(e instanceof OpError && e.message === 'aborted')) { if (!unknownOutcome(e)) sendingId(scope, null, text); setFailure({ text: e.message }); } throw e; } }} /></div>
  </article>;
}

/**
 * Switch profile or model, or hand off to another agent (design "Menu actions"): session_transition decides reuse, a model
 * switch in the session, or a new session with a context handoff. An inline panel, never a floating form (OR-14).
 */
function SwitchPanel({ sessionId, handoff, current, onDone, run }: { sessionId: string; handoff: boolean; current?: string; onDone: (note?: string) => void; run: (fn: () => Promise<unknown>) => Promise<void> }) {
  const { data: profiles } = useDoc<any>('profiles', () => op('profiles_list', {}), () => false);
  const { data: accounts } = useDoc<any>(handoff ? 'accounts' : null, () => op('account_list', {}), c => c.kind === 'host');
  const [profile, setProfile] = useState<string | null>(null), [account, setAccount] = useState<string | null>(null), [done, setDone] = useState<string | null>(null), [next, setNext] = useState<string | null>(null), [, nav] = useLocation();
  const list: any[] = profiles?.profiles ?? [];
  const selectedProfile = list.find(p => p.id === profile);
  const named: any[] = (accounts?.accounts ?? []).filter((a: any) => a.intake === 'all' && selectedProfile && a.harness === (selectedProfile.harness ?? 'codex'));
  const go = () => run(async () => { const r = await op('session_transition', { sessionId, profileId: profile ?? undefined, ...(account ? { account } : {}) }); setDone(r.transition); setNext(r.sessionId && r.sessionId !== sessionId ? r.sessionId : null); });
  return <DetailSection title={handoff ? 'Hand off to another agent' : 'Switch profile or model'}>
    <div class="answers" role="group" aria-label="Profile">{list.map(p => <Button key={p.id} label={`${p.id} · ${p.harness ?? 'codex'}${p.model ? ` ${p.model}` : ''}`} pressed={profile === p.id} kind={p.id === current ? 'quiet' : 'default'} onClick={() => { setProfile(p.id); setAccount(null); }} />)}</div>
    {handoff && <div class="answers" role="group" aria-label="Account">{named.map(a => <Button key={a.id} label={`${a.name} · ${a.harness ?? 'codex'}`} pressed={account === a.name} onClick={() => setAccount(account === a.name ? null : a.name)} />)}</div>}
    <div class="answers"><Button label={handoff ? 'Hand off' : 'Switch'} kind="primary" disabledReason={profile ? undefined : 'pick a profile'} onClick={go} /><Button label="Close" kind="quiet" onClick={() => onDone(done ?? undefined)} />{done && <span class="muted" role="status">{done}</span>}{next && <a class="row-link" href={`/conversations/${encodeURIComponent(next)}`} onClick={e => { e.preventDefault(); onDone(done ?? undefined); nav(`/conversations/${encodeURIComponent(next)}`); }}>Open the new conversation</a>}</div>
  </DetailSection>;
}

function NewConversation({onDone}:{onDone:(sessionId:string)=>void}){
 const alive=useAlive();
 const [verifiedProject,setVerifiedProject]=useState<string|null>(null);
 const projects=useDoc<any>('conversation-projects',()=>op('project_list',{}),c=>c.kind==='definition');
 const [project,setProject]=useState(new URLSearchParams(location.search).get('new')??''),[role,setRole]=useState('pm'),[error,setError]=useState('');
 const detail=useDoc<any>(project?'conversation-project:'+project:null,()=>op('project_detail',{projectId:project}).catch(e=>{if(e instanceof OpError&&[404,503].includes(e.status))return op('project_context',{projectId:project});throw e;}).then(reply=>{setVerifiedProject(project);return reply;}),c=>c.kind==='definition');
 const selected=detail.restored||verifiedProject!==project?null:detail.data?.settings;
 const roles=Object.entries(selected?.roles??{}).map(([id,profile])=>({id,label:`${id} · ${String(profile)}`}));
 return <DetailSection title="New conversation"><Choice label="Project" value={project} onChange={p=>{setProject(p);setRole('pm');}} options={(projects.data?.projects??[]).map((p:any)=>({id:p.id,label:p.name}))}/>
 {project&&<Choice label="Role / configuration" value={role} onChange={setRole} options={roles}/>}
 {error&&<ErrorBanner text={error}/>}<Composer draftPersistence={selected&&roles.some(r=>r.id===role)?createDraftPersistence(authorizedDraftScope({kind:'project',id:project,roleId:role})):undefined} placeholder="Describe the work for this project" disabledReason={!project||!roles.some(r=>r.id===role)?'Choose a project and a mapped role':undefined} onSend={async text=>{const scope=authorizedDraftScope({kind:'project',id:project,roleId:role});try{setError('');const request=await prepareSending(scope,'session_start',{projectId:project,role,text});const r=await writeOnce('session_start',request,()=>setError('unknown, retrying'),{signal:alive});sendingId(scope,null,text);onDone(r.sessionId);}catch(e:any){if(!(e instanceof AccessChanged)&&!unknownOutcome(e)&&!(e instanceof OpError&&e.message==='aborted'))sendingId(scope,null,text);setError(e.message);throw e;}}}/>
 <Button label="Projectless conversation" icon="messages-square" onClick={()=>op('session_create',{}).then(r=>onDone(r.sessionId),e=>setError(e.message))}/></DetailSection>;
}
