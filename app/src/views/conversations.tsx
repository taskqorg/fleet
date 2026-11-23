// Conversations (docs/design-system/pages/conversations.md): sessions list and one conversation with its live transcript.
// Actions use the same operations agents use: session_start (conversation-new.tsx), message_send, execution_stop, session_fork.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  EntityCode, EntityPicker, ListDetail, RowFacts, Fact, AgeChip, ProblemRow, actions,
  StateBadge, StateCounters, HarnessMark, Button, Thread, Composer, DecisionBlock, SearchField, ActionMenu, DetailSection,
  OpsSummary, StatStrip, KvRow, Num, TabBar, type VirtualListHandle, type CatalogueTreeNode, type IconName, type Stat,
  Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses, type Harness, type StateOf, states as states_, TextField,
  writeClip, zone, DropZone, agentLabel, InlineForm, type ComposerChip,
  keyConnection, ShellStatus, ShellActions } from '../../../packages/ui/src/index.ts';
import { op, online, addRequestedKey, writeOnce, sendingId, prepareSending, submissionKey, authorizedDraftScope, createDraftPersistence, unknownOutcome, AccessChanged, OpError, useAlive, type Ops } from '../api.ts';
import { NewConversation } from './conversation-new.tsx';
import { useAttachments } from '../uploads.ts';
import { useOp } from '../store.ts';
import type { SessionRow as ContractSessionRow, SessionGetReply, SessionExecution } from '../../../contracts/generated/ops.ts';
import { DEFAULT_SESSION_GROUP, DEFAULT_SESSION_SORT } from '../../../packages/view-url/view-url.ts';
import { nextUrl } from '../url.ts';
import { prefs } from '../prefs.ts';
import { useListPage, ListPagePanel, type ListPage } from '../list-page.tsx';
import { SYSTEM_VIEWS } from '../system-views.ts';
import { useThread, type ThreadKey } from '../threads.ts';
import { click, all, drag, inside, range, textBounds, type Selection } from '../selection.ts';
import { ScopedRollupBar, ScopedRollupDetails } from './resources.tsx';
import { SwarmPanel, hasSwarmBoard, useSwarmBoard } from './swarm-panel.tsx';

const GROUPS = [['project', 'Project'], ['state', 'State'], ['agent', 'Agent'], ['none', 'None']] as const;
const SORTS = [['activity', 'Activity'], ['created', 'Created']] as const;
/** The contract's session row. */
type SessionRow = ContractSessionRow & { state: StateOf<'session'> };
const errText = (e: unknown) => e instanceof Error ? e.message : String(e);
const rec = (v: unknown): Record<string, unknown> => v && typeof v === 'object' && !Array.isArray(v) ? v as Record<string, unknown> : {};
const str = (v: unknown) => typeof v === 'string' ? v : undefined;
const isHarness = (h: string | null | undefined): h is Harness => !!h && h in harnesses;
const reqId = () => crypto.randomUUID();
const TERMINAL = ['succeeded', 'failed', 'cancelled'];
const titleOf = (s: SessionRow) => s.title ?? 'New conversation';

export function Conversations({ id }: { id?: string }) {
  // Pinned conversations stay whatever the filter, search or grouping (OR-12): read by id with no filter but the archived
  // list the view shows, so an archived pin never shows among active conversations or the other way round. Active and
  // archived conversations are two lists: a switch by any path (the toggle, a saved view, Back) ends selection mode and a
  // pending confirmation; the toggle and a saved view also drop the checked rows. The rows a bulk action finished leave
  // the selection (list-page.tsx).
  const page = useListPage<SessionRow>({
    view: 'conversations', entity: 'conversation', kind: 'sessions', defaultGroup: DEFAULT_SESSION_GROUP, defaultSort: DEFAULT_SESSION_SORT, system: SYSTEM_VIEWS.conversations, groupLabel,
    pinRead: v => ({ filter: { archived: !!v.archived } }),
    saveExtra: v => ({ archived: v.archived }), pickExtra: sv => ({ archived: sv.archived }),
    listId: v => !!v.archived,
  });
  const { view, set, chosen, off, confirm } = page;
  const archived = !!view.archived;
  const canArchive = (s: SessionRow) => s.state === 'idle' || TERMINAL.includes(s.state);
  // Archive and Unarchive are one operation per conversation. session_archive is refused while an execution is active;
  // session_unarchive (owner or full access) returns the conversation to the active list and records a reason.
  const archive = (ss: SessionRow[]) => page.bulk(ss, s => op('session_archive', { sessionId: s.id }), 'not archived');
  const unarchive = (ss: SessionRow[]) => page.bulk(ss, s => op('session_unarchive', { sessionId: s.id, reason: 'Unarchived from the Conversations list' }), 'not unarchived');
  const toggleArchived = () => set({ archived: archived ? undefined : true, sel: undefined, preset: undefined });
  const { filtered } = page;
  const now = Date.now();
  // New conversation (fleet-conversations.md "Start a conversation") is its own URL in the work area, /conversations/new,
  // with the list view kept; ?in=<project> preselects a project. The old ?new=<project> link opens it once.
  const isNew = id === 'new', initial = new URLSearchParams(location.search);
  // On the new page already, + goes to its composer (the draft and the choices stay).
  // The field when it takes input, else the page itself (presets loading, offline): never a dead click.
  const create = () => {
    if (!isNew) return set({ focus: 'new', extra: { ...view.extra, in: undefined, role: undefined } });
    const page = document.querySelector<HTMLElement>('[data-ui=detail][aria-label="New conversation"]'), field = page?.querySelector<HTMLTextAreaElement>('[data-ui=composer] textarea');
    (field && !field.disabled ? field : page)?.focus();
  };
  useEffect(() => { const p = initial.get('new'); if (p !== null && !isNew) set({ focus: 'new', extra: { ...view.extra, new: undefined, in: p || undefined } }); }, []);
  const pane = <>
    <ShellStatus><OfflineMarker state={page.list.state} now={now} /></ShellStatus>
    <ShellActions><Button label="New conversation" icon="plus" iconOnly kind="primary" pressed={isNew} disabledReason={online.value ? undefined : 'offline'} onClick={create} /></ShellActions>
    <ListPagePanel page={page} noun="Conversations" placeholder="Search conversations" family="session" groups={GROUPS} sorts={SORTS} focused={isNew ? undefined : id}
      tools={<Button label="Archived" icon="archive" iconOnly kind="quiet" pressed={archived} onClick={toggleArchived} />}
      actions={() => [
        archived ? { label: 'Unarchive', icon: 'archive-restore', onClick: () => unarchive(chosen), disabledReason: off }
          : { label: 'Archive', icon: 'archive', danger: true, onClick: () => page.setConfirm(chosen.filter(canArchive)), disabledReason: off ?? (chosen.some(canArchive) ? undefined : 'None can be archived') },
      ]}
      menu={s => [
        archived ? { label: 'Unarchive', icon: 'archive-restore', disabledReason: off, onClick: () => unarchive([s]) }
          : { label: 'Archive', icon: 'archive', disabledReason: off ?? (canArchive(s) ? undefined : 'Execution active'), onClick: () => page.setConfirm([s]) },
      ]}
      confirm={confirm && <ProblemRow severity="warning" title={`Archive ${confirm.length} ${confirm.length === 1 ? 'conversation' : 'conversations'}?`} facts="Their worktrees are torn down; the owner can restore a conversation."
        actions={[{ label: 'Yes, archive', primary: true, onClick: () => { page.setConfirm(null); archive(confirm); } }, { label: 'Keep', onClick: () => page.setConfirm(null) }]} />}
      empty={<Empty text={filtered ? `No ${archived ? 'archived ' : ''}conversations match these filters.` : prefs.value.focus.length && !view.project?.length ? `No ${archived ? 'archived ' : ''}conversations in the focused projects.` : archived ? 'No archived conversations.' : 'No conversations yet.'}
        action={filtered ? <Button label="Clear filters" onClick={page.clearFilters} />
          : archived ? <Button label="Show active" onClick={toggleArchived} /> : <Button label="New conversation" onClick={create} />} />}
      listLabel={archived ? 'Archived conversations' : 'Conversations'} title={titleOf} groupIcon={g => g === 'state' ? 'activity' : g === 'agent' ? 'bot' : 'folder-open'}
      state={s => <StateBadge family="session" state={s.state} compact />}
      facts={s => <SessionFacts s={s} />} />
  </>;
  const detail = isNew ? <NewConversation key="new" initialProject={initial.get('in')} initialRole={initial.get('role')} onClose={() => set({ focus: undefined, extra: { ...view.extra, in: undefined, role: undefined } })} onStarted={sessionId => set({ focus: sessionId, extra: { ...view.extra, in: undefined, role: undefined } })} />
    : id ? <Conversation key={id} id={id} onClose={() => set({ focus: undefined })} /> : undefined;
  return <ListDetail list={pane} detail={detail} idle={<ConversationsSummary page={page} filtered={filtered} archived={archived} />} detailKey={id} />;
}

function ConversationsSummary({ page, filtered, archived }: { page: ListPage<SessionRow>; filtered: boolean; archived: boolean }) {
  const { list, view, set, rowsGroup } = page;
  const counts = list.counts as Partial<Record<StateOf<'session'>, number>>;
  const activeStats: Stat[] = ([
    { label: archived ? 'archived' : 'total', icon: archived ? 'archive' : 'messages-square', value: { value: list.total, basis: 'measured' } },
    { label: 'waiting', icon: states_.session.waiting.icon, value: { value: counts.waiting ?? 0, basis: 'measured' }, tone: (counts.waiting ?? 0) > 0 ? 'attention' : undefined },
    { label: 'failed', icon: states_.session.failed.icon, value: { value: counts.failed ?? 0, basis: 'measured' }, tone: (counts.failed ?? 0) > 0 ? 'danger' : undefined },
    { label: 'running', icon: states_.session.running.icon, value: { value: counts.running ?? 0, basis: 'measured' } },
    { label: 'preparing', icon: states_.session.preparing.icon, value: { value: counts.preparing ?? 0, basis: 'measured' } },
    { label: 'idle', icon: states_.session.idle.icon, value: { value: counts.idle ?? 0, basis: 'measured' } },
  ] satisfies Stat[]).filter(s => (s.value.value ?? 0) > 0);
  const nonZeroGroups = list.groups.filter(g => g.count > 0);
  const groupTitle = GROUPS.find(g => g[0] === rowsGroup)?.[1] ?? rowsGroup;
  const hasCounts = Object.values(counts).some(n => (n ?? 0) > 0);

  return (
    <article class="detail" aria-label="Conversations summary">
      <OpsSummary>
        {list.pending && !list.total ? <Skeleton rows={3} /> : (
          <>
            {activeStats.length > 0 && <StatStrip label="Conversations" stats={activeStats} />}
            {nonZeroGroups.length > 0 && rowsGroup !== 'none' ? (
              <DetailSection title={`By ${groupTitle.toLowerCase()}`}>
                {nonZeroGroups.map(g => (
                  <KvRow
                    key={g.key}
                    icon={rowsGroup === 'state' ? 'activity' : rowsGroup === 'agent' ? 'bot' : 'folder-open'}
                    label={groupLabel(rowsGroup, g.label) || g.key}
                    value={<Num m={{ value: g.count, basis: 'measured' }} kind="count" />}
                  />
                ))}
              </DetailSection>
            ) : hasCounts ? (
              <DetailSection title="By state">
                <StateCounters
                  family="session"
                  label="Conversations by state"
                  counts={counts}
                  active={(view.state ?? []) as StateOf<'session'>[]}
                  hideZero
                  onAll={() => set({ state: [] })}
                  onToggle={s => {
                    const cur = (view.state ?? []) as StateOf<'session'>[];
                    set({ state: cur.includes(s) ? cur.filter(x => x !== s) : [...cur, s] });
                  }}
                />
              </DetailSection>
            ) : (
              <Empty text={filtered ? 'No matching conversations to summarize.' : archived ? 'Archived summary is empty.' : 'No conversations to summarize.'} />
            )}
          </>
        )}
      </OpsSummary>
    </article>
  );
}

/** Row facts of a conversation (OR-37): open questions, executions from two, time since the last activity, agent. */
function SessionFacts({ s }: { s: SessionRow }) {
  const at = s.activityAt ? Date.parse(s.activityAt) : NaN, n = s.executions ?? 0, q = s.openQuestions ?? 0;
  return <RowFacts>
    {q > 0 && <span class="chip-age t-attention alert" aria-label={q === 1 ? '1 open question' : `${q} open questions`}>{q}</span>}
    {n > 1 && <Fact icon={actions.runs} text={String(n)} tip={`${n} executions`} />}
    {!isNaN(at) && <Fact tip="Last activity"><AgeChip t={at} /></Fact>}
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
  const { data: s, error, restored, reload: reloadSession } = useOp('session_get', { sessionId: id });
  // A fresh answer (not the stored copy) for this conversation validates the composer's target.
  useEffect(() => { if (s && !restored) setVerifiedTarget(id); }, [s, restored, id]);
  const projects = useOp('project_list', s?.projectId ? {} : null);
  const previousCode = useRef(s?.code);
  useEffect(() => {
    // Renaming a project reissues its conversation codes in the same transaction.
    if (previousCode.current && s?.code && previousCode.current !== s.code) projects.reload();
    previousCode.current = s?.code;
  }, [s?.code]);
  const project = projects.data?.projects.find(p => rec(p).id === s?.projectId);
  // A session that got a task shows the task's thread (design "Thread scope"): the first page resolves it and returns the key.
  const thread = useThread({ session: id });
  const key: ThreadKey = thread.win?.key ?? { session: id };
  // A plain click marks where a selection starts (no tint, so reading stays quiet); Shift+click selects up to that row.
  const [selection, renderSelection] = useState<{ scope: string; range: Selection } | null>(null);
  const pickFrom = useRef<string | null>(null);
  const scope = JSON.stringify(key);
  const sel = selection?.scope === scope ? selection.range : null;
  const setSel = (range: Selection) => {
    live.current.sel = range;
    renderSelection({ scope, range });
  };
  useEffect(() => { pickFrom.current = null; }, [scope]);
  // Cmd+A selects the thread; Cmd+C copies the selected rows from the server as markdown and chat-safe HTML. The clipboard write starts inside the
  // key handler with a promise, so the browser keeps the user gesture while the export loads.
  const copySel = (s0: NonNullable<Selection>) => {
    try {
      // Both formats (formats.md "Copy"); times in the reader's own zone.
      const text = op('thread_export', { key, ...range(s0), tz: zone() }, { signal: alive.current.signal }).then(r => r.markdown);
      writeClip(text).catch(e => copyFail(e, () => copySel(s0)));
    } catch (e) { copyFail(e, () => copySel(s0)); } // no async clipboard (plain http): say so instead of failing silently
  };
  // The key handler reads the latest selection through a ref: effects run after paint, and a key can come first.
  const live = useRef({ sel, setSel, copySel }); live.current = { sel, setSel, copySel };
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      const { sel, setSel, copySel } = live.current;
      const t = e.target as HTMLElement;
      // Only inside the conversation (or with nothing focused); code, not key, so other keyboard layouts work too.
      if (!(e.metaKey || e.ctrlKey) || t?.closest?.('input,textarea,[contenteditable]') || !(t?.closest?.('.conversation') || t === document.body)) return;
      if (e.code === 'KeyA') { e.preventDefault(); getSelection()?.removeAllRanges(); setSel(all()); }
      // A native selection inside one row is the browser's own copy.
      if (e.code === 'KeyC' && sel && (getSelection()?.isCollapsed ?? true)) { e.preventDefault(); copySel(sel); }
    };
    addEventListener('keydown', on); return () => removeEventListener('keydown', on);
  }, []);
  const [answers, setAnswers] = useState<Record<string, Ops['questions_answer']['reply']>>({});
  const threadBlocks = thread.win?.blocks;
  const blocks = useMemo(() => threadBlocks?.map(block => {
    const answer = block.kind === 'question' ? answers[block.questionId] : undefined;
    if (!answer || block.kind !== 'question') return block;
    return { ...block, state: answer.state, answer: answer.answer, answeredBy: answer.answeredBy ?? undefined,
      answeredAt: answer.answeredAt ? Date.parse(answer.answeredAt) : null };
  }) ?? [], [threadBlocks, answers]);
  async function answerQuestion(questionId: string, answer: string) {
    const row = await writeOnce('questions_answer', { questionId, answer }, unknown, { signal: alive.current.signal });
    setAnswers(current => ({ ...current, [row.id]: row }));
    return row;
  }
  const [failure, setFailure] = useState<{ text: string; retry?: () => void } | null>(null);
  // Writes in flight stop when the conversation closes.
  const alive = useRef(new AbortController()); useEffect(() => () => alive.current.abort(), []);
  const unknown = () => setFailure({ text: 'unknown, retrying' });
  const [panel, setPanel] = useState<'switch' | 'handoff' | 'rename' | null>(null);
  const [q, setQ] = useState(''), [hits, setHits] = useState<{ key: string; snippet: string }[] | null>(null), [hit, setHit] = useState(0), [marked, setMarked] = useState<string | null>(null);
  const handle = useRef<VirtualListHandle | null>(null);
  const board = useSwarmBoard('session', id);
  const hasSwarm = hasSwarmBoard(board.data);
  const [convTab, setConvTab] = useState<'transcript' | 'swarm'>('transcript');
  const activeConvTab = hasSwarm ? convTab : 'transcript';
  const [showRelated, setShowRelated] = useState(false);
  const related = s?.resources;
  const hasRelated = !!related && (related.delegated.executions > 0 || related.spawned.executions > 0 || related.spawnedTasks.length > 0 || related.hidden > 0 || related.total.tools.byTool.length > 0);
  const inRelated = showRelated && hasRelated;
  const reading = useRef<ReturnType<VirtualListHandle['getAnchor']>>(null);
  const toggleRelated = () => {
    if (activeConvTab === 'swarm') {
      setConvTab('transcript');
      setShowRelated(true);
      return;
    }
    if (!inRelated) reading.current = handle.current?.getAnchor() ?? null;
    setShowRelated(!inRelated);
  };
  useLayoutEffect(() => {
    const anchor = reading.current;
    if (inRelated || activeConvTab === 'swarm' || !anchor || !handle.current) return;
    reading.current = null;
    handle.current.restoreAnchor(anchor.key, anchor.offsetPx);
  }, [inRelated, activeConvTab]);
  // Files for the next message: uploaded as they are added (picker, paste, drop over the conversation).
  const files = useAttachments({ sessionId: id });
  // The hit is scrolled to once the replaced window is on screen.
  const [jumpTo, setJumpTo] = useState<string | null>(null);
  useEffect(() => { if (jumpTo && thread.win?.blocks.some(b => b.key === jumpTo)) { handle.current?.scrollToKey(jumpTo, 'center'); setJumpTo(null); } }, [thread.win, jumpTo]);
  const now = Date.now(), off = !online.value;
  const run = async (fn: () => Promise<unknown>) => { setFailure(null); try { await fn(); } catch (e) { setFailure({ text: errText(e), retry: () => run(fn) }); } };
  const copyFail = useCallback((e: unknown, retry?: () => void) => setFailure({ text: `Copy failed: ${errText(e)}`, retry }), []);
  // A jump replaces the window with entries around the hit, then puts the hit row in view and marks it.
  const open = async (list: { key: string }[], i: number) => {
    const key = list[i]?.key;
    if (!key) return;
    setShowRelated(false);
    reading.current = null;
    setHit(i);
    setMarked(key);
    // Land on the new window before its old scroll position can trigger another edge load.
    await thread.jump(key, () => {
      if (handle.current) handle.current.scrollToKey(key, 'center');
      else setJumpTo(key);
    });
  };
  const search = () => run(async () => { if (!q.trim()) { setHits(null); setMarked(null); return; } const r = await op('thread_search', { key, q, limit: 100 }); setHits(r.hits); await open(r.hits, 0); });
  if (error && !s) return <article class="detail" data-ui="detail" tabIndex={-1}><ErrorBanner text={`Conversation did not load: ${error}`} /></article>;
  if (!s) return <article class="detail" data-ui="detail" tabIndex={-1}><Skeleton rows={6} /></article>;
  const running = (e: SessionExecution) => !['succeeded', 'failed', 'cancelled'].includes(e.state) && e.observation !== 'unconfirmed';
  // Composer target: the newest running execution; with none, a new message starts one (design "Controls").
  const active = s.executions.filter(running).at(-1);
  const latest = s.executions.at(-1);
  // Older coordinator replies and retained detail snapshots carry only the run list.
  const sessionState = (s.state ?? latest?.state ?? 'idle') as StateOf<'session'>;
  const unconfirmed = latest?.observation === 'unconfirmed';
  const lastSeen = unconfirmed ? (typeof latest.lastSeenAt === 'number' ? latest.lastSeenAt : Date.parse(String(latest.lastSeenAt))) : NaN;
  const openQs = s.questions.filter(q => (answers[q.id] ?? q).state === 'open');
  const w = thread.win;
  const last = s.messages.at(-1), lastMessage: string | undefined = last?.text;
  const lastFiles: string[] = (last?.attachments ?? []).map(a => str(rec(a).artifactId)).filter((x): x is string => !!x);
  // The stored title (set by a person or an agent, or generated after the first reply); until then the first message,
  // as the list shows it.
  const fallback = (s.messages[0]?.text ?? '').replace(new RegExp(titleUUID.source, 'gi'), '').trim();
  const title: string = s.title || [...fallback].slice(0, 200).join('') || 'New conversation';
  return <article class="detail conversation" data-ui="detail" tabIndex={-1} aria-label="Conversation">
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
      <h2 class="detail-title conv-title" data-tip={title}>{title}</h2>
      <EntityCode code={s.code} id={id} kind="conversation" /><span class="grow" />
      <SearchField label="Search thread" value={q} onInput={setQ} onSearch={search} />
      {(hits !== null || (!!w && w.blocks.length < w.total)) && (
        <div class="conv-search-results" data-active={!!hits} data-partial={!!w && w.blocks.length < w.total}>
          <span class="count" aria-live="polite">{hits ? `${hits.length ? hit + 1 : 0} of ${hits.length}` : `${w!.blocks.length.toLocaleString('en')} of ${w!.total.toLocaleString('en')}`}</span>
          {hits && (
            <>
              <Button label="Previous hit" icon="arrow-up" iconOnly disabledReason={hit <= 0 ? 'no earlier hit' : undefined} onClick={() => open(hits, hit - 1)} />
              <Button label="Next hit" icon="arrow-down" iconOnly disabledReason={hit >= hits.length - 1 ? 'no later hit' : undefined} onClick={() => open(hits, hit + 1)} />
            </>
          )}
        </div>
      )}
      <Button label="Copy thread as markdown" icon="file-text" iconOnly disabledReason={off ? 'offline' : undefined} onClick={() => copySel(all()!)} />
      {/* Menu actions (design "Controls"): each calls the existing operation; the result shows as a new boundary in this thread. */}
      <ActionMenu label="Actions" disabledReason={off ? 'offline' : undefined} items={[
        { label: 'Rename', icon: 'type', onClick: () => setPanel('rename') },
        { label: 'Retry last message', icon: 'rotate-ccw', disabledReason: lastMessage || lastFiles.length ? undefined : 'no message yet', onClick: () => { const requestId = reqId(); run(() => writeOnce('message_send', { sessionId: id, requestId, text: lastMessage ?? '', ...(lastFiles.length ? { attachments: lastFiles } : {}) }, unknown, { signal: alive.current.signal })); } },
        { label: 'Switch profile or model', icon: 'sliders-horizontal', onClick: () => setPanel('switch') },
        { label: 'Hand off to another agent', icon: 'bot', onClick: () => setPanel('handoff') },
        { label: 'Fork', icon: 'git-merge', onClick: () => { const requestId = reqId(); run(async () => { const f = await writeOnce('session_fork', { sessionId: id, requestId }, unknown, { signal: alive.current.signal }); go(nextUrl(location.pathname + location.search, 'conversations', { focus: f.sessionId })); }); } },
      ]} />
    </header>
    {panel === 'rename' && <RenamePanel sessionId={id} current={s.title ?? ''} onDone={() => setPanel(null)} onSaved={() => { setFailure(null); reloadSession(); }} run={run} onUnknown={() => { setFailure({ text: 'Rename outcome unknown. Check the current title.' }); reloadSession(); }} />}
    {panel && panel !== 'rename' && <SwitchPanel sessionId={id} handoff={panel === 'handoff'} current={str(rec(s.agentConfig).id)} onDone={note => { setPanel(null); if (note) setFailure(null); }} run={run} />}
    <div class="state-line">
      {sessionState in states_.session && <StateBadge family="session" state={sessionState}
        unconfirmed={unconfirmed} lastSeen={Number.isFinite(lastSeen) ? lastSeen : undefined} />}
      {s.projectId && <>{project?.name && <span class="muted">{project.name}</span>}
        <EntityCode code={str(rec(project).code)} id={s.projectId} kind="project" /></>}
      {hasRelated && <Button label="Related work" icon="folder-open" kind="quiet" pressed={inRelated} onClick={toggleRelated} />}
    </div>
    <ScopedRollupBar rollup={s.resources} cost={s.cost} inlineDetails={false} />
    {hasSwarm && (
      <TabBar
        cls="detail-tabs"
        label="Conversation views"
        tabs={[
          { id: 'transcript', label: 'Transcript' },
          { id: 'swarm', label: 'Swarm', count: board.data!.summary.messagesTotal || board.data!.roster.length || undefined },
        ]}
        value={activeConvTab}
        onChange={nextTab => {
          if (nextTab === 'swarm' && !inRelated) reading.current = handle.current?.getAnchor() ?? null;
          if (nextTab === 'swarm') setShowRelated(false);
          setConvTab(nextTab);
        }}
      />
    )}
    <div class="status-line" role="status">{failure ? <ErrorBanner text={failure.text} onRetry={failure.retry} /> : thread.error ? <ErrorBanner text={`Thread did not load: ${thread.error}`} onRetry={thread.newest} /> : null}</div>
    {activeConvTab === 'swarm' ? (
      <div class="pane" role="tabpanel" aria-label="Swarm">
        <section class="related-work" aria-label="Swarm" tabIndex={0}>
          <SwarmPanel scopeKind="session" scopeId={id} board={board} />
        </section>
      </div>
    ) : (
    <div class="pane" data-related-work={inRelated}><DropZone onFiles={files.add} disabledReason={off ? 'offline' : undefined}>
      {!w ? <Skeleton rows={6} /> : <Thread key={scope} blocks={blocks} label="Transcript" now={now} marked={marked} selected={k => inside(sel, k) && !textBounds(sel, k)} textBounds={k => textBounds(sel, k)}
        onTextRange={(anchor, focus) => { pickFrom.current = anchor.key; setSel(drag(anchor, focus)); }}
        onRowClick={(k, shift) => { if (!shift) { pickFrom.current = k; setSel(null); return; } setSel(click(click(null, pickFrom.current ?? sel?.start.key ?? k, false), k, true)); pickFrom.current ??= k; }} showSession={!!key && 'task' in key} onOpenResult={r => go('/results/' + encodeURIComponent(r))} resultSrc={r => '/api/results/' + encodeURIComponent(r)} handle={handle} onStart={inRelated ? undefined : thread.older} onEnd={inRelated ? undefined : thread.newer}
        stopReason={off ? 'offline' : undefined} onStop={x => run(() => op('execution_stop', { executionId: x }))}
        onAnswer={(questionId, answer) => run(() => answerQuestion(questionId, answer))} onCopyFail={copyFail} onSecret={(q: string, c: string, v: string) => addRequestedKey(q, c, v, alive.current.signal)}
        pending={{ count: w.fresh, onJump: () => { setMarked(null); setHits(null); thread.newest(); } }} />}
      {inRelated && <section class="related-work" aria-label="Related work" tabIndex={0}><ScopedRollupDetails rollup={related} /></section>}
      {/* D3: steer goes to the execution and does not answer; the open question stays in sight above the composer. */}
      {openQs.map(q => <DecisionBlock key={q.id} question={q.question ?? q.text ?? ''} askedBy="agent" addressedTo="you" since={q.askedAt ? Date.parse(String(q.askedAt)) || now : now} now={now}
        options={(Array.isArray(q.options) ? q.options as unknown[] : keyConnection(q.options) ? ['decline'] : []).map((o, i) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
        onAnswer={a => run(() => answerQuestion(q.id, a))}
        onSecret={keyConnection(q.options) ? v => addRequestedKey(q.id, keyConnection(q.options)!, v, alive.current.signal) : undefined} />)}
      {active && <div class="composer-bar"><EntityCode id={active.id} kind="execution" /></div>}
      <Composer draftPersistence={s && !restored && verifiedTarget === id ? createDraftPersistence(authorizedDraftScope({ kind: 'session', id })) : undefined} disabledReason={off ? 'offline' : verifiedTarget !== id ? 'Validating conversation' : undefined} placeholder={active ? 'Steer' : 'Message'}
        onAttach={files.add} attachments={files.items} onRemoveAttachment={files.remove} chips={composerChips(s, active, () => setPanel('switch'))}
        onStop={active ? () => run(() => op('execution_stop', { executionId: active.id })) : undefined} stopReason={off ? 'offline' : undefined}
        onSend={async text => {
          // Files still uploading finish first; a failed one fails the send and stays on its chip.
          let attachments: string[];
          try { attachments = await files.ready(); } catch (e) { setFailure({ text: `Attachment not uploaded: ${errText(e)}` }); throw e; }
          // One requestId per draft text and files until it is sent, kept next to the draft with the complete body: a resend
          // of the same text and files after a failure or a reload is the same write, never a second one; other files make
          // a new message (submissionKey). A definite error (nothing stored) clears it. A message with files is queued
          // while the agent runs (its files are staged before a turn), so it is never a steer.
          const scope = authorizedDraftScope({ kind: 'session', id });
          const body = { sessionId: id, text, ...(attachments.length ? { attachments } : active ? { delivery: 'steer' } : {}) };
          const request = await prepareSending(scope, 'message_send', body), key = submissionKey(body);
          setFailure(null);
          try { await writeOnce('message_send', request, unknown, { signal: alive.current.signal }); sendingId(scope, null, key); files.clear(attachments); setFailure(null); }
          catch (e) { if (!(e instanceof AccessChanged) && !(e instanceof OpError && e.message === 'aborted')) { if (!unknownOutcome(e)) sendingId(scope, null, key); setFailure({ text: errText(e) }); } throw e; } }} />
    </DropZone></div>
    )}
  </article>;
}

/** Rename (session_rename): an inline field, never a floating form (OR-14). The title it sets is kept; a generated one
 *  never replaces it. */
const titleUUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;

function RenamePanel({ sessionId, current, onDone, onSaved, run, onUnknown }: { sessionId: string; current: string; onDone: () => void; onSaved: () => void; run: (fn: () => Promise<unknown>) => Promise<void>; onUnknown: () => void }) {
  // A rename still retrying stops when the panel closes (Cancel, Escape, another panel), so it never lands later.
  const signal = useAlive();
  const [v, setV] = useState(current), [busy, setBusy] = useState(false), off = !online.value, clean = v.trim().replace(/\s+/g, ' '), chars = [...clean].length;
  // The server's checks (session_titles.go sessionRename), said before sending.
  const reason = off ? 'offline' : !clean ? 'type a title' : chars > 200 ? 'at most 200 characters' : !/[\p{L}\p{N}]/u.test(clean) ? 'a title holds a word'
    : titleUUID.test(clean) ? 'no UUID in a title' : undefined;
  const save = () => {
    if (busy) return;
    setBusy(true);
    run(async () => {
      try {
        await op('session_rename', { sessionId, title: clean }, { signal });
        onDone();
        onSaved();
      } catch (error) {
        if (signal.aborted || error instanceof AccessChanged) return;
        if (!unknownOutcome(error)) throw error;
        onDone();
        onUnknown();
      } finally { setBusy(false); }
    });
  };
  return <DetailSection title="Rename">
    <InlineForm cls="answers" onSubmit={e => { e.preventDefault(); if (!reason) save(); }} onKeyDown={e => { if (e.key === 'Escape') onDone(); }}>
      <TextField label="Title" value={v} onChange={setV} />
      <Button label="Save" kind="primary" busy={busy} disabledReason={reason} onClick={save} />
      <Button label="Cancel" kind="quiet" onClick={onDone} /></InlineForm>
  </DetailSection>;
}

/**
 * The composer's facts (controls/composer.md): the agent profile with its model and effort (a click opens Switch profile
 * or model), and the permissions the profile grants, shown only.
 */
function composerChips(s: SessionGetReply, active: SessionExecution | undefined, onSwitch: () => void): ComposerChip[] {
  const cfg = rec(s.agentConfig), settings = rec(cfg.settings), harness: string | null = str(cfg.harness) ?? active?.harness ?? null;
  const mark = agentLabel(active?.model ?? str(cfg.model), active?.effort ?? str(cfg.effort));
  const chips: ComposerChip[] = [{ harness, icon: 'bot', label: mark || str(cfg.id) || 'Agent', tip: `${cfg.id ? `Profile ${str(cfg.id)}` : 'No profile'} · switch profile or model`, onClick: onSwitch }];
  const tools: string[] = Array.isArray(cfg.tools) ? cfg.tools.filter((t): t is string => typeof t === 'string') : [];
  const network = cfg.gitNetwork === true, mode = str(settings.mode) ?? null;
  chips.push({ icon: 'shield', label: [mode ?? 'Workspace', network ? 'network' : null].filter(Boolean).join(' · '),
    tip: `Permissions of the profile: ${tools.length ? `tools ${tools.join(', ')}` : 'the harness tools'}${network ? ', git network' : cfg.gitNetwork === false ? ', no git network' : ', git network as the project grants'}${settings.sandbox === false ? ', no sandbox' : ''}` });
  return chips;
}

/**
 * Switch profile or model, or hand off to another agent (design "Menu actions"): session_transition decides reuse, a model
 * switch in the session, or a new session with a context handoff. An inline panel, never a floating form (OR-14).
 */
function SwitchPanel({ sessionId, handoff, current, onDone, run }: { sessionId: string; handoff: boolean; current?: string; onDone: (note?: string) => void; run: (fn: () => Promise<unknown>) => Promise<void> }) {
  const { data: profiles } = useOp('profiles_list', {});
  const { data: accounts } = useOp('account_list', handoff ? {} : null);
  const [profile, setProfile] = useState<string | null>(null), [account, setAccount] = useState<string | null>(null), [done, setDone] = useState<string | null>(null), [next, setNext] = useState<string | null>(null), [, nav] = useLocation();
  // Listed profiles are open records (contracts/profile.schema.json): their id, harness and model.
  const list = (profiles?.profiles ?? []).flatMap(p => typeof p.id === 'string' ? [{ id: p.id, harness: str(p.harness), model: str(p.model) }] : []);
  const selectedProfile = list.find(p => p.id === profile);
  const named = (accounts?.accounts ?? []).filter(a => a.intake === 'all' && selectedProfile && a.harness === (selectedProfile.harness ?? 'codex'));
  const go = () => run(async () => { const r = await op('session_transition', { sessionId, profileId: profile ?? undefined, ...(account ? { account } : {}) }); setDone(r.transition); setNext(r.sessionId && r.sessionId !== sessionId ? r.sessionId : null); });
  const profileNodes: CatalogueTreeNode[] = list.map(p => ({
    id: p.id,
    label: p.id,
    sub: [p.harness ?? 'codex', p.model].filter(Boolean).join(' · '),
    icon: (isHarness(p.harness) ? harnesses[p.harness].icon : 'bot') as IconName,
    right: p.id === current ? <span class="muted">current</span> : undefined,
    searchText: `${p.id} ${p.harness ?? ''} ${p.model ?? ''}`,
  }));
  const accountNodes: CatalogueTreeNode[] = named.map(a => ({
    id: a.name,
    label: a.name,
    sub: a.harness ?? 'codex',
    icon: (isHarness(a.harness) ? harnesses[a.harness].icon : 'bot') as IconName,
    searchText: `${a.name} ${a.harness ?? ''}`,
  }));
  return <DetailSection title={handoff ? 'Hand off to another agent' : 'Switch profile or model'}>
    <div class="stack">
      <EntityPicker
        label="Profile"
        nodes={profileNodes}
        value={profile}
        placeholder="Pick a profile"
        searchPlaceholder="Find profile"
        loading={!profiles}
        onChange={v => { setProfile(v); setAccount(null); }}
      />
      {handoff && (
        <EntityPicker
          label="Account"
          nodes={accountNodes}
          value={account}
          placeholder={profile ? 'Auto account' : 'Pick a profile first'}
          searchPlaceholder="Find account"
          loading={!accounts}
          disabledReason={profile ? undefined : 'Pick a profile first'}
          onChange={setAccount}
        />
      )}
      <div class="answers"><Button label={handoff ? 'Hand off' : 'Switch'} kind="primary" disabledReason={profile ? undefined : 'pick a profile'} onClick={go} /><Button label="Close" kind="quiet" onClick={() => onDone(done ?? undefined)} />{done && <span class="muted" role="status">{done}</span>}{next && <a class="row-link" href={`/conversations/${encodeURIComponent(next)}`} onClick={e => { e.preventDefault(); onDone(done ?? undefined); nav(`/conversations/${encodeURIComponent(next)}`); }}>Open the new conversation</a>}</div>
    </div>
  </DetailSection>;
}
