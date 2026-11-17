// Conversations (docs/design-system/pages/conversations.md): sessions list and one conversation with its live transcript.
// Actions use the same operations agents use: session_start (conversation-new.tsx), message_send, execution_stop, session_fork.
import { useEffect, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  EntityCode, ListDetail, RowFacts, Fact, AgeChip, ProblemRow, actions,
  StateBadge, HarnessMark, Button, Thread, Composer, DecisionBlock, SearchField, ActionMenu, DetailSection, type VirtualListHandle,
  Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses, type Harness, type StateOf, states as states_, TextField,
  writeClip, zone, DropZone, agentLabel, type ComposerChip,
  keyConnection, ShellStatus, ShellActions } from '../../../packages/ui/src/index.ts';
import { op, online, addRequestedKey, writeOnce, sendingId, prepareSending, submissionKey, authorizedDraftScope, createDraftPersistence, unknownOutcome, AccessChanged, OpError, useAlive, type Ops } from '../api.ts';
import { NewConversation } from './conversation-new.tsx';
import { useAttachments } from '../uploads.ts';
import { useDoc } from '../store.ts';
import { DEFAULT_SESSION_GROUP, DEFAULT_SESSION_SORT } from '../../../packages/view-url/view-url.ts';
import { nextUrl } from '../url.ts';
import { prefs } from '../prefs.ts';
import { useListPage, ListPagePanel } from '../list-page.tsx';
import { SYSTEM_VIEWS } from '../system-views.ts';
import { useThread, type ThreadKey } from '../threads.ts';
import { click, all, inside, range, type Selection } from '../selection.ts';
import { ScopedRollupBar } from './resources.tsx';

const GROUPS = [['project', 'Project'], ['state', 'State'], ['agent', 'Agent'], ['none', 'None']] as const;
const SORTS = [['activity', 'Activity'], ['created', 'Created']] as const;
type SessionRow = { id: string; code?: string | null; title: string | null; state: StateOf<'session'>; harness: string | null; openQuestions: number; activityAt?: string | null; executions?: number; projectId?: string | null; sortKey: (string | number)[] };
const isHarness = (h: string | null): h is Harness => !!h && h in harnesses;
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
    view: 'conversations', kind: 'sessions', defaultGroup: DEFAULT_SESSION_GROUP, defaultSort: DEFAULT_SESSION_SORT, system: SYSTEM_VIEWS.conversations, groupLabel,
    pinDoc: 'session-pins', pinRead: v => ({ key: [!!v.archived], filter: { archived: !!v.archived } }),
    pinTouches: (c, has) => (c.kind === 'session' && has(c.id)) || (!!c.sessionId && has(c.sessionId)),
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
      facts={s => <SessionFacts s={s} now={now} />} />
  </>;
  const detail = isNew ? <NewConversation key="new" initialProject={initial.get('in')} initialRole={initial.get('role')} onStarted={sessionId => set({ focus: sessionId, extra: { ...view.extra, in: undefined, role: undefined } })} />
    : id ? <Conversation key={id} id={id} onClose={() => set({ focus: undefined })} /> : undefined;
  return <ListDetail list={pane} detail={detail} detailKey={id} />;
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
  const { data: s, error, state, restored, reload: reloadSession } = useDoc<any>(`session:${id}`, () => op('session_get', { sessionId: id }).then(reply => { setVerifiedTarget(id); return reply; }), c => c.sessionId === id && c.kind !== 'event');
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
  const [panel, setPanel] = useState<'switch' | 'handoff' | 'rename' | null>(null);
  const [q, setQ] = useState(''), [hits, setHits] = useState<{ key: string; snippet: string }[] | null>(null), [hit, setHit] = useState(0), [marked, setMarked] = useState<string | null>(null);
  const handle = useRef<VirtualListHandle | null>(null);
  // Files for the next message: uploaded as they are added (picker, paste, drop over the conversation).
  const files = useAttachments({ sessionId: id });
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
  const last = (s.messages ?? []).at(-1), lastMessage: string | undefined = last?.text;
  const lastFiles: string[] = (last?.attachments ?? []).map((a: any) => a.artifactId).filter(Boolean);
  // The stored title (set by a person or an agent, or generated after the first reply); until then the first message,
  // as the list shows it.
  const fallback = ((s.messages ?? [])[0]?.text ?? '').replace(new RegExp(titleUUID.source, 'gi'), '').trim();
  const title: string = s.title || [...fallback].slice(0, 200).join('') || 'New conversation';
  return <article class="detail conversation" data-ui="detail" tabIndex={-1} aria-label="Conversation">
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
      <h2 class="detail-title conv-title" data-tip={title}>{title}</h2>
      <EntityCode code={s.code} id={id} what="Conversation" />{s.agentConfig?.id && <span class="muted mono conv-profile">{s.agentConfig.id}</span>}<span class="grow" />
      <SearchField label="Search thread" value={q} onInput={setQ} onSearch={search} />
      <span class="count" aria-live="polite">{hits ? `${hits.length ? hit + 1 : 0} of ${hits.length}` : w ? `${w.blocks.length.toLocaleString('en')} of ${w.total.toLocaleString('en')}` : ''}</span>
      <Button label="Previous hit" icon="arrow-up" iconOnly disabledReason={!hits || hit <= 0 ? 'no earlier hit' : undefined} onClick={() => hits && open(hits, hit - 1)} />
      <Button label="Next hit" icon="arrow-down" iconOnly disabledReason={!hits || hit >= hits.length - 1 ? 'no later hit' : undefined} onClick={() => hits && open(hits, hit + 1)} />
      <OfflineMarker state={state} now={now} />
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
    {panel && panel !== 'rename' && <SwitchPanel sessionId={id} handoff={panel === 'handoff'} current={s.agentConfig?.id} onDone={note => { setPanel(null); if (note) setFailure(null); }} run={run} />}
    <ScopedRollupBar rollup={s.resources} cost={s.cost} />
    <div class="status-line" role="status">{failure ? <ErrorBanner text={failure.text} onRetry={failure.retry} /> : thread.error ? <ErrorBanner text={`Thread did not load: ${thread.error}`} onRetry={thread.newest} /> : null}</div>
    <div class="pane"><DropZone onFiles={files.add} disabledReason={off ? 'offline' : undefined}>
      {!w ? <Skeleton rows={6} /> : <Thread blocks={w.blocks.map(b => b.kind === 'question' && answers[b.questionId] ? { ...b, state: answers[b.questionId].state, answer: answers[b.questionId].answer, answeredBy: answers[b.questionId].answeredBy ?? undefined, answeredAt: answers[b.questionId].answeredAt ? new Date(answers[b.questionId].answeredAt!).getTime() : null } : b)} label="Transcript" now={now} marked={marked} selected={k => inside(sel, k)} onRowClick={(k, shift) => { if (!shift) { pickFrom.current = k; setSel(null); return; } setSel(click(click(null, pickFrom.current ?? sel?.start.key ?? k, false), k, true)); pickFrom.current ??= k; }} showSession={!!key && 'task' in key} onOpenResult={r => go('/results/' + encodeURIComponent(r))} resultSrc={r => '/api/results/' + encodeURIComponent(r)} handle={handle} onStart={thread.older} onEnd={thread.newer}
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
      </div>
      <Composer draftPersistence={s && !restored && verifiedTarget === id ? createDraftPersistence(authorizedDraftScope({ kind: 'session', id })) : undefined} disabledReason={off ? 'offline' : verifiedTarget !== id ? 'Validating conversation' : undefined} placeholder={active ? 'Steer' : 'Message'}
        onAttach={files.add} attachments={files.items} onRemoveAttachment={files.remove} chips={composerChips(s, active, () => setPanel('switch'))}
        onStop={active ? () => run(() => op('execution_stop', { executionId: active.id })) : undefined} stopReason={off ? 'offline' : undefined}
        onSend={async text => {
          // Files still uploading finish first; a failed one fails the send and stays on its chip.
          let attachments: string[];
          try { attachments = await files.ready(); } catch (e: any) { setFailure({ text: `Attachment not uploaded: ${e?.message ?? e}` }); throw e; }
          // One requestId per draft text and files until it is sent, kept next to the draft with the complete body: a resend
          // of the same text and files after a failure or a reload is the same write, never a second one; other files make
          // a new message (submissionKey). A definite error (nothing stored) clears it. A message with files is queued
          // while the agent runs (its files are staged before a turn), so it is never a steer.
          const scope = authorizedDraftScope({ kind: 'session', id });
          const body = { sessionId: id, text, ...(attachments.length ? { attachments } : active ? { delivery: 'steer' } : {}) };
          const request = await prepareSending(scope, 'message_send', body), key = submissionKey(body);
          setFailure(null);
          try { await writeOnce('message_send', request, unknown, { signal: alive.current.signal }); sendingId(scope, null, key); files.clear(attachments); setFailure(null); }
          catch (e: any) { if (!(e instanceof AccessChanged) && !(e instanceof OpError && e.message === 'aborted')) { if (!unknownOutcome(e)) sendingId(scope, null, key); setFailure({ text: e.message }); } throw e; } }} />
    </DropZone></div>
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
    <form class="answers" onSubmit={e => { e.preventDefault(); if (!reason) save(); }} onKeyDown={e => { if (e.key === 'Escape') onDone(); }}>
      <TextField label="Title" value={v} onChange={setV} />
      <Button label="Save" kind="primary" busy={busy} disabledReason={reason} onClick={save} />
      <Button label="Cancel" kind="quiet" onClick={onDone} /></form>
  </DetailSection>;
}

/**
 * The composer's facts (controls/composer.md): the agent profile with its model and effort (a click opens Switch profile
 * or model), and the permissions the profile grants, shown only.
 */
function composerChips(s: any, active: any, onSwitch: () => void): ComposerChip[] {
  const cfg = s.agentConfig ?? {}, harness: string | null = cfg.harness ?? active?.harness ?? s.harness ?? null;
  const mark = agentLabel(active?.model ?? cfg.model, active?.effort ?? cfg.effort);
  const chips: ComposerChip[] = [{ harness, icon: 'bot', label: mark || cfg.id || 'Agent', tip: `${cfg.id ? `Profile ${cfg.id}` : 'No profile'} · switch profile or model`, onClick: onSwitch }];
  const tools: string[] = Array.isArray(cfg.tools) ? cfg.tools : [];
  const network = cfg.gitNetwork === true, mode = typeof cfg.settings?.mode === 'string' ? cfg.settings.mode : null;
  chips.push({ icon: 'shield', label: [mode ?? 'Workspace', network ? 'network' : null].filter(Boolean).join(' · '),
    tip: `Permissions of the profile: ${tools.length ? `tools ${tools.join(', ')}` : 'the harness tools'}${network ? ', git network' : cfg.gitNetwork === false ? ', no git network' : ', git network as the project grants'}${cfg.settings?.sandbox === false ? ', no sandbox' : ''}` });
  return chips;
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
