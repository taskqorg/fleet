// Conversations (docs/design-system/pages/conversations.md): sessions list and one conversation with its live transcript.
// Actions use the same operations agents use: session_start (conversation-new.tsx), message_send, execution_stop, session_fork.
import { For, Match, Show, Switch, createEffect, createMemo, createSignal, onSettled, untrack } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { useLocation, useSearch } from '../router.ts';
import {
  EntityCode, EntityPicker, ListDetail, RowFacts, Fact, AgeChip, ProblemRow, actions,
  StateBadge, StateCounters, HarnessMark, Button, Thread, Composer, DecisionBlock, SearchField, ActionMenu, DetailSection,
  OpsSummary, StatStrip, KvRow, Num, TabBar, type VirtualListHandle, type CatalogueTreeNode, type IconName, type Stat,
  Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses, type Harness, type StateOf, states as states_, TextField,
  writeClip, zone, DropZone, agentLabel, InlineForm, type ComposerChip,
  keyConnection, ShellStatus, ShellActions,
} from '../../../packages/ui/src/index.ts';
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
import { RowSpend, useRowSpend, ScopedRollupBar, ScopedRollupDetails } from './resources.tsx';
import { SwarmPanel, hasSwarmBoard, useSwarmBoard } from './swarm-panel.tsx';

const GROUPS = [['project', 'Project'], ['state', 'State'], ['agent', 'Agent'], ['none', 'None']] as const;
const SORTS = [['activity', 'Activity'], ['created', 'Created']] as const;
/** The contract's session row. */
type SessionRow = ContractSessionRow & { state: StateOf<'session'> };
const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));
const rec = (v: unknown): Record<string, unknown> => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {});
const str = (v: unknown) => (typeof v === 'string' ? v : undefined);
const isHarness = (h: string | null | undefined): h is Harness => !!h && h in harnesses;
const reqId = () => crypto.randomUUID();
const TERMINAL = ['succeeded', 'failed', 'cancelled'];
const titleOf = (s: SessionRow) => s.title ?? 'New conversation';

export function Conversations(props: { id?: string }) {
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
  const { set } = page;
  const search = useSearch();
  const spend = useRowSpend('sessions', () => page.displayed);
  const archived = () => !!page.view.archived;
  const canArchive = (s: SessionRow) => s.state === 'idle' || TERMINAL.includes(s.state);
  // Archive and Unarchive are one operation per conversation. session_archive is refused while an execution is active;
  // session_unarchive (owner or full access) returns the conversation to the active list and records a reason.
  const archive = (ss: SessionRow[]) => page.bulk(ss, s => op('session_archive', { sessionId: s.id }), 'not archived');
  const unarchive = (ss: SessionRow[]) => page.bulk(ss, s => op('session_unarchive', { sessionId: s.id, reason: 'Unarchived from the Conversations list' }), 'not unarchived');
  const toggleArchived = () => set({ archived: archived() ? undefined : true, sel: undefined, preset: undefined });
  const now = () => Date.now();
  // New conversation (fleet-conversations.md "Start a conversation") is its own URL in the work area, /conversations/new,
  // with the list view kept; ?in=<project> preselects a project. The old ?new=<project> link opens it once.
  const isNew = () => props.id === 'new';
  const initial = createMemo(() => new URLSearchParams(search()));
  // On the new page already, + goes to its composer (the draft and the choices stay).
  // The field when it takes input, else the page itself (presets loading, offline): never a dead click.
  const create = () => {
    if (!untrack(isNew)) return set({ focus: 'new', extra: { ...untrack(() => page.view.extra), in: undefined, role: undefined } });
    const detail = document.querySelector<HTMLElement>('[data-ui=detail][aria-label="New conversation"]'), field = detail?.querySelector<HTMLTextAreaElement>('[data-ui=composer] textarea');
    (field && !field.disabled ? field : detail)?.focus();
  };
  onSettled(() => {
    const p = untrack(initial).get('new');
    if (p !== null && !untrack(isNew)) set({ focus: 'new', extra: { ...untrack(() => page.view.extra), new: undefined, in: p || undefined } });
  });
  const pane = (
    <>
      <ShellStatus><OfflineMarker state={page.list.state} now={now()} /></ShellStatus>
      <ShellActions><Button label="New conversation" icon="plus" iconOnly kind="primary" pressed={isNew()} disabledReason={online.value ? undefined : 'offline'} onClick={create} /></ShellActions>
      <ListPagePanel
        page={page}
        noun="Conversations"
        placeholder="Search conversations"
        family="session"
        groups={GROUPS}
        sorts={SORTS}
        focused={isNew() ? undefined : props.id}
        tools={<Button label="Archived" icon="archive" iconOnly kind="quiet" pressed={archived()} onClick={toggleArchived} />}
        actions={() => [
          archived()
            ? { label: 'Unarchive', icon: 'archive-restore', onClick: () => unarchive(page.chosen), disabledReason: page.off }
            : { label: 'Archive', icon: 'archive', danger: true, onClick: () => page.setConfirm(page.chosen.filter(canArchive)), disabledReason: page.off ?? (page.chosen.some(canArchive) ? undefined : 'None can be archived') },
        ]}
        menu={s => [
          archived()
            ? { label: 'Unarchive', icon: 'archive-restore', disabledReason: page.off, onClick: () => unarchive([s]) }
            : { label: 'Archive', icon: 'archive', disabledReason: page.off ?? (canArchive(s) ? undefined : 'Execution active'), onClick: () => page.setConfirm([s]) },
        ]}
        confirm={page.confirm ? (
          <ProblemRow
            severity="warning"
            title={`Archive ${page.confirm.length} ${page.confirm.length === 1 ? 'conversation' : 'conversations'}?`}
            facts="Their worktrees are torn down; the owner can restore a conversation."
            actions={[
              { label: 'Yes, archive', primary: true, onClick: () => { const ss = page.confirm!; page.setConfirm(null); archive(ss); } },
              { label: 'Keep', onClick: () => page.setConfirm(null) },
            ]}
          />
        ) : undefined}
        empty={
          <Empty
            text={page.filtered ? `No ${archived() ? 'archived ' : ''}conversations match these filters.` : prefs.value.focus.length && !page.view.project?.length ? `No ${archived() ? 'archived ' : ''}conversations in the focused projects.` : archived() ? 'No archived conversations.' : 'No conversations yet.'}
            action={page.filtered ? <Button label="Clear filters" onClick={page.clearFilters} /> : archived() ? <Button label="Show active" onClick={toggleArchived} /> : <Button label="New conversation" onClick={create} />}
          />
        }
        listLabel={archived() ? 'Archived conversations' : 'Conversations'}
        title={titleOf}
        groupIcon={g => (g === 'state' ? 'activity' : g === 'agent' ? 'bot' : 'folder-open')}
        state={s => <StateBadge family="session" state={s.state} compact />}
        facts={s => (
          <SessionFacts
            s={s}
            spend={
              <RowSpend
                cost={spend.cost.get(s.id)}
                stale={!!spend.error}
                name={s.code ?? titleOf(s)}
                href={page.href({ focus: s.id })}
                open={extend => (page.selecting || extend ? page.select(s) : set({ focus: s.id }))}
              />
            }
          />
        )}
      />
    </>
  );
  return (
    <ListDetail
      list={pane}
      detail={
        isNew() ? (
          <NewConversation
            initialProject={initial().get('in')}
            initialRole={initial().get('role')}
            onClose={() => set({ focus: undefined, extra: { ...page.view.extra, in: undefined, role: undefined } })}
            onStarted={sessionId => set({ focus: sessionId, extra: { ...page.view.extra, in: undefined, role: undefined } })}
          />
        ) : props.id ? (
          <Show when={props.id} keyed>
            {id => <Conversation id={id} onClose={() => set({ focus: undefined })} />}
          </Show>
        ) : undefined
      }
      idle={<ConversationsSummary page={page} filtered={page.filtered} archived={archived()} />}
      detailKey={props.id}
    />
  );
}

function ConversationsSummary(props: { page: ListPage<SessionRow>; filtered: boolean; archived: boolean }) {
  const counts = () => props.page.list.counts as Partial<Record<StateOf<'session'>, number>>;
  const activeStats = createMemo<Stat[]>(() => {
    const c = counts();
    return ([
      { label: props.archived ? 'archived' : 'total', icon: props.archived ? 'archive' : 'messages-square', value: { value: props.page.list.total, basis: 'measured' } },
      { label: 'waiting', icon: states_.session.waiting.icon, value: { value: c.waiting ?? 0, basis: 'measured' }, tone: (c.waiting ?? 0) > 0 ? 'attention' : undefined },
      { label: 'failed', icon: states_.session.failed.icon, value: { value: c.failed ?? 0, basis: 'measured' }, tone: (c.failed ?? 0) > 0 ? 'danger' : undefined },
      { label: 'running', icon: states_.session.running.icon, value: { value: c.running ?? 0, basis: 'measured' } },
      { label: 'preparing', icon: states_.session.preparing.icon, value: { value: c.preparing ?? 0, basis: 'measured' } },
      { label: 'idle', icon: states_.session.idle.icon, value: { value: c.idle ?? 0, basis: 'measured' } },
    ] satisfies Stat[]).filter(s => (s.value.value ?? 0) > 0);
  });
  const nonZeroGroups = createMemo(() => props.page.list.groups.filter(g => g.count > 0));
  const groupTitle = createMemo(() => GROUPS.find(g => g[0] === props.page.rowsGroup)?.[1] ?? props.page.rowsGroup);
  const hasCounts = createMemo(() => Object.values(counts()).some(n => (n ?? 0) > 0));

  return (
    <article class="detail" aria-label="Conversations summary">
      <OpsSummary>
        <Show when={!(props.page.list.pending && !props.page.list.total)} fallback={<Skeleton rows={3} />}>
          <Show when={activeStats().length > 0}>
            <StatStrip label="Conversations" stats={activeStats()} />
          </Show>
          <Switch fallback={<Empty text={props.filtered ? 'No matching conversations to summarize.' : props.archived ? 'Archived summary is empty.' : 'No conversations to summarize.'} />}>
            <Match when={nonZeroGroups().length > 0 && props.page.rowsGroup !== 'none'}>
              <DetailSection title={`By ${groupTitle().toLowerCase()}`}>
                <For each={nonZeroGroups()}>
                  {g => (
                    <KvRow
                      icon={props.page.rowsGroup === 'state' ? 'activity' : props.page.rowsGroup === 'agent' ? 'bot' : 'folder-open'}
                      label={groupLabel(props.page.rowsGroup, g.label) || g.key}
                      value={<Num m={{ value: g.count, basis: 'measured' }} kind="count" />}
                    />
                  )}
                </For>
              </DetailSection>
            </Match>
            <Match when={hasCounts()}>
              <DetailSection title="By state">
                <StateCounters
                  family="session"
                  label="Conversations by state"
                  counts={counts()}
                  active={(props.page.view.state ?? []) as StateOf<'session'>[]}
                  hideZero
                  onAll={() => props.page.set({ state: [] })}
                  onToggle={s => {
                    const cur = (props.page.view.state ?? []) as StateOf<'session'>[];
                    props.page.set({ state: cur.includes(s) ? cur.filter(x => x !== s) : [...cur, s] });
                  }}
                />
              </DetailSection>
            </Match>
          </Switch>
        </Show>
      </OpsSummary>
    </article>
  );
}

/** Row facts of a conversation (OR-37): open questions, executions from two, time since the last activity, agent. */
function SessionFacts(props: { s: SessionRow; spend: JSX.Element }) {
  const at = () => (props.s.activityAt ? Date.parse(props.s.activityAt) : NaN);
  const n = () => props.s.executions ?? 0;
  const q = () => props.s.openQuestions ?? 0;
  return (
    <RowFacts>
      <Show when={q() > 0}>
        <span class="chip-age t-attention alert" aria-label={q() === 1 ? '1 open question' : `${q()} open questions`}>{q()}</span>
      </Show>
      <Show when={n() > 1}>
        <Fact icon={actions.runs} text={String(n())} tip={`${n()} executions`} />
      </Show>
      <Show when={!isNaN(at())}>
        <Fact tip="Last activity"><AgeChip t={at()} /></Fact>
      </Show>
      <Show when={isHarness(props.s.harness)}><HarnessMark h={props.s.harness!} /></Show>
      {props.spend}
    </RowFacts>
  );
}

function groupLabel(group: string, label: string | null) {
  if (group === 'agent') return label && label in harnesses ? harnesses[label as Harness].label : label ?? 'No agent';
  if (group === 'state') return label && label in states_.session ? states_.session[label as StateOf<'session'>].label : label ?? '';
  return label ?? 'No project';
}

function Conversation(props: { id: string; onClose: () => void }) {
  const [, go] = useLocation();
  const [verifiedTarget, setVerifiedTarget] = createSignal<string | null>(null, { ownedWrite: true });
  const sDoc = useOp('session_get', () => ({ sessionId: props.id }));
  const targetDoc = useOp('thread_composer', () => ({ conversationId: props.id }));
  // A fresh answer (not the stored copy) for this conversation validates the composer's target.
  createEffect(
    () => [sDoc.data, sDoc.restored, props.id] as const,
    ([s, restored, id]) => { if (s && !restored) setVerifiedTarget(id); },
  );
  const projects = useOp('project_list', () => (sDoc.data?.projectId ? {} : null));
  let previousCode: string | null | undefined;
  createEffect(
    () => sDoc.data?.code,
    code => {
      // Renaming a project reissues its conversation codes in the same transaction.
      if (previousCode && code && previousCode !== code) projects.reload();
      previousCode = code;
    },
  );
  const project = createMemo(() => projects.data?.projects.find(p => rec(p).id === sDoc.data?.projectId));
  // A session that got a task shows the task's thread (design "Thread scope"): the first page resolves it and returns the key.
  const thread = useThread(() => ({ session: props.id }));
  const key = createMemo<ThreadKey>(() => thread.win?.key ?? { session: props.id });
  // A plain click marks where a selection starts (no tint, so reading stays quiet); Shift+click selects up to that row.
  const [selection, renderSelection] = createSignal<{ scope: string; range: Selection } | null>(null);
  let pickFrom: string | null = null;
  const scope = createMemo(() => JSON.stringify(key()));
  const sel = createMemo(() => (selection()?.scope === scope() ? selection()!.range : null));
  const setSel = (rangeVal: Selection) => {
    renderSelection({ scope: untrack(scope), range: rangeVal });
  };
  createEffect(() => scope(), () => { pickFrom = null; });
  // Writes in flight stop when the conversation closes.
  const alive = useAlive();
  const [failure, setFailure] = createSignal<{ text: string; retry?: () => void } | null>(null);
  const unknown = () => setFailure({ text: 'unknown, retrying' });
  const copyFail = (e: unknown, retry?: () => void) => setFailure({ text: `Copy failed: ${errText(e)}`, retry });
  // Cmd+A selects the thread; Cmd+C copies the selected rows from the server as markdown and chat-safe HTML. The clipboard write starts inside the
  // key handler with a promise, so the browser keeps the user gesture while the export loads.
  const copySel = (s0: NonNullable<Selection>) => {
    try {
      // Both formats (formats.md "Copy"); times in the reader's own zone.
      const text = op('thread_export', { key: untrack(key), ...range(s0), tz: zone() }, { signal: alive }).then(r => r.markdown);
      writeClip(text).catch(e => copyFail(e, () => copySel(s0)));
    } catch (e) { copyFail(e, () => copySel(s0)); } // no async clipboard (plain http): say so instead of failing silently
  };
  onSettled(() => {
    const on = (e: KeyboardEvent) => {
      const curSel = untrack(sel);
      const t = e.target as HTMLElement;
      // Only inside the conversation (or with nothing focused); code, not key, so other keyboard layouts work too.
      if (!(e.metaKey || e.ctrlKey) || t?.closest?.('input,textarea,[contenteditable]') || !(t?.closest?.('.conversation') || t === document.body)) return;
      if (e.code === 'KeyA') { e.preventDefault(); getSelection()?.removeAllRanges(); setSel(all()); }
      // A native selection inside one row is the browser's own copy.
      if (e.code === 'KeyC' && curSel && (getSelection()?.isCollapsed ?? true)) { e.preventDefault(); copySel(curSel); }
    };
    addEventListener('keydown', on);
    return () => removeEventListener('keydown', on);
  });
  const [answers, setAnswers] = createSignal<Record<string, Ops['questions_answer']['reply']>>({});
  const blocks = createMemo(() => {
    const ans = answers();
    return thread.win?.blocks.map(block => {
      const answer = block.kind === 'question' ? ans[block.questionId] : undefined;
      if (!answer || block.kind !== 'question') return block;
      return {
        ...block,
        state: answer.state,
        answer: answer.answer,
        answeredBy: answer.answeredBy ?? undefined,
        answeredAt: answer.answeredAt ? Date.parse(answer.answeredAt) : null,
      };
    }) ?? [];
  });
  async function answerQuestion(questionId: string, answer: string) {
    const row = await writeOnce('questions_answer', { questionId, answer }, unknown, { signal: alive });
    setAnswers(current => ({ ...current, [row.id]: row }));
    return row;
  }
  const [panel, setPanel] = createSignal<'rename' | { session: SessionGetReply; handoff: boolean } | null>(null);
  const switchPanel = createMemo(() => { const current = panel(); return typeof current === 'object' ? current : null; });
  const [q, setQ] = createSignal('');
  const [hits, setHits] = createSignal<{ key: string; snippet: string }[] | null>(null);
  const [hit, setHit] = createSignal(0);
  const [marked, setMarked] = createSignal<string | null>(null);
  const handle: { current: VirtualListHandle | null } = { current: null };
  const board = useSwarmBoard('session', () => props.id);
  const hasSwarm = createMemo(() => hasSwarmBoard(board.data));
  const [convTab, setConvTab] = createSignal<'transcript' | 'swarm'>('transcript');
  const activeConvTab = () => (hasSwarm() ? convTab() : 'transcript');
  const [showRelated, setShowRelated] = createSignal(false);
  const related = () => sDoc.data?.resources;
  const hasRelated = createMemo(() => {
    const r = related();
    return !!r && (r.delegated.executions > 0 || r.spawned.executions > 0 || r.spawnedTasks.length > 0 || r.hidden > 0 || r.total.tools.byTool.length > 0);
  });
  const inRelated = () => showRelated() && hasRelated();
  let reading: ReturnType<VirtualListHandle['getAnchor']> = null;
  const toggleRelated = () => {
    if (activeConvTab() === 'swarm') {
      setConvTab('transcript');
      setShowRelated(true);
      return;
    }
    if (!untrack(inRelated)) reading = handle.current?.getAnchor() ?? null;
    setShowRelated(!untrack(inRelated));
  };
  createEffect(
    () => [inRelated(), activeConvTab()] as const,
    ([rel, tab]) => {
      const anchor = reading;
      if (rel || tab === 'swarm' || !anchor || !handle.current) return;
      reading = null;
      handle.current.restoreAnchor(anchor.key, anchor.offsetPx);
    },
  );
  // Files for the next message: uploaded as they are added (picker, paste, drop over the conversation).
  const taskUpload = createMemo(() => !!targetDoc.data?.key.task);
  const uploadProject = createMemo(() => sDoc.data?.projectId);
  const uploadTarget = createMemo(() => taskUpload() ? { projectId: uploadProject() } : { sessionId: props.id });
  const files = useAttachments(uploadTarget);
  // The hit is scrolled to once the replaced window is on screen.
  const [jumpTo, setJumpTo] = createSignal<string | null>(null, { ownedWrite: true });
  createEffect(
    () => [thread.win, jumpTo()] as const,
    ([win, jt]) => {
      if (jt && win?.blocks.some(b => b.key === jt)) {
        handle.current?.scrollToKey(jt, 'center');
        setJumpTo(null);
      }
    },
  );
  const now = () => Date.now();
  const off = () => !online.value;
  const run = async (fn: () => Promise<unknown>) => { setFailure(null); try { await fn(); } catch (e) { setFailure({ text: errText(e), retry: () => run(fn) }); } };
  // A jump replaces the window with entries around the hit, then puts the hit row in view and marks it.
  const open = async (list: { key: string }[], i: number) => {
    const hitKey = list[i]?.key;
    if (!hitKey) return;
    setShowRelated(false);
    reading = null;
    setHit(i);
    setMarked(hitKey);
    // Land on the new window before its old scroll position can trigger another edge load.
    await thread.jump(hitKey, () => {
      if (handle.current) handle.current.scrollToKey(hitKey, 'center');
      else setJumpTo(hitKey);
    });
  };
  const search = () => run(async () => {
    const curQ = untrack(q);
    if (!curQ.trim()) { setHits(null); setMarked(null); return; }
    const r = await op('thread_search', { key: untrack(key), q: curQ, limit: 100 });
    setHits(r.hits);
    await open(r.hits, 0);
  });

  const s = () => sDoc.data;
  const active = createMemo(() => targetDoc.data?.active);
  const targetID = createMemo(() => targetDoc.data?.sessionId);
  const targetSessionDoc = useOp('session_get', () => {
    const sid = targetID();
    return sid && sid !== props.id ? { sessionId: sid } : null;
  });
  const targetSession = () => targetID() === props.id ? s() : targetSessionDoc.data;
  const targetChips = createMemo(() => {
    const session = targetSession();
    return session ? composerChips(session, active(), () => setPanel({ session, handoff: false })) : [];
  });
  const composerReason = () => {
    if (off()) return 'offline';
    if (targetDoc.error || targetSessionDoc.error) return 'Conversation target unavailable';
    if (!targetDoc.data || verifiedTarget() !== props.id) return 'Validating conversation';
    if (!targetID()) return 'No session can receive messages';
  };
  const stopComposer = createMemo(() => {
    const executionId = active()?.id;
    return executionId ? () => run(() => op('execution_stop', { executionId })) : undefined;
  });
  const latest = createMemo(() => s()?.executions.at(-1));
  // Older coordinator replies and retained detail snapshots carry only the run list.
  const sessionState = () => (s()?.state ?? latest()?.state ?? 'idle') as StateOf<'session'>;
  const unconfirmed = () => latest()?.observation === 'unconfirmed';
  const lastSeen = createMemo(() => {
    const l = latest();
    return unconfirmed() && l ? (typeof l.lastSeenAt === 'number' ? l.lastSeenAt : Date.parse(String(l.lastSeenAt))) : NaN;
  });
  const openQs = createMemo(() => {
    const ans = answers();
    const questions = new Map((s()?.questions ?? []).map(q => [q.id, q]));
    for (const q of targetSession()?.questions ?? []) questions.set(q.id, q);
    return [...questions.values()].filter(q => (ans[q.id] ?? q).state === 'open');
  });
  const last = () => s()?.messages.at(-1);
  const lastMessage = () => last()?.text;
  const lastFiles = createMemo<string[]>(() => (last()?.attachments ?? []).map(a => str(rec(a).artifactId)).filter((x): x is string => !!x));
  // The stored title (set by a person or an agent, or generated after the first reply); until then the first message,
  // as the list shows it.
  const title = createMemo(() => {
    const session = s();
    if (!session) return 'New conversation';
    const fallback = (session.messages[0]?.text ?? '').replace(new RegExp(titleUUID.source, 'gi'), '').trim();
    return session.title || [...fallback].slice(0, 200).join('') || 'New conversation';
  });
  const draftPersistence = createMemo(() =>
    s() && !sDoc.restored && verifiedTarget() === props.id ? createDraftPersistence(authorizedDraftScope({ kind: 'session', id: props.id })) : undefined,
  );

  return (
    <Switch
      fallback={
        <article class="detail conversation" data-ui="detail" tabindex={-1} aria-label="Conversation">
          <header class="detail-h">
            <Button label="Close" icon="x" iconOnly onClick={props.onClose} />
            <h2 class="detail-title conv-title" data-tip={title()}>{title()}</h2>
            <EntityCode code={s()!.code} id={props.id} kind="conversation" />
            <span class="grow" />
            <SearchField label="Search thread" value={q()} onInput={setQ} onSearch={search} />
            <Show when={hits() !== null || (!!thread.win && thread.win.blocks.length < thread.win.total)}>
              <div class="conv-search-results" data-active={String(!!hits())} data-partial={String(!!thread.win && thread.win.blocks.length < thread.win.total)}>
                <span class="count" aria-live="polite">
                  {hits() ? `${hits()!.length ? hit() + 1 : 0} of ${hits()!.length}` : `${thread.win!.blocks.length.toLocaleString('en')} of ${thread.win!.total.toLocaleString('en')}`}
                </span>
                <Show when={hits()} keyed>
                  {hs => (
                    <>
                      <Button label="Previous hit" icon="arrow-up" iconOnly disabledReason={hit() <= 0 ? 'no earlier hit' : undefined} onClick={() => open(hs, hit() - 1)} />
                      <Button label="Next hit" icon="arrow-down" iconOnly disabledReason={hit() >= hs.length - 1 ? 'no later hit' : undefined} onClick={() => open(hs, hit() + 1)} />
                    </>
                  )}
                </Show>
              </div>
            </Show>
            <Button label="Copy thread as markdown" icon="file-text" iconOnly disabledReason={off() ? 'offline' : undefined} onClick={() => copySel(all()!)} />
            {/* Menu actions (design "Controls"): each calls the existing operation; the result shows as a new boundary in this thread. */}
            <ActionMenu
              label="Actions"
              disabledReason={off() ? 'offline' : undefined}
              items={[
                { label: 'Rename', icon: 'type', onClick: () => setPanel('rename') },
                {
                  label: 'Retry last message',
                  icon: 'rotate-ccw',
                  disabledReason: lastMessage() || lastFiles().length ? undefined : 'no message yet',
                  onClick: () => {
                    const requestId = reqId(), msg = lastMessage(), fs = lastFiles();
                    run(() => writeOnce('message_send', { sessionId: props.id, requestId, text: msg ?? '', ...(fs.length ? { attachments: fs } : {}) }, unknown, { signal: alive }));
                  },
                },
                { label: 'Switch profile or model', icon: 'sliders-horizontal', onClick: () => setPanel({ session: s()!, handoff: false }) },
                { label: 'Hand off to another agent', icon: 'bot', onClick: () => setPanel({ session: s()!, handoff: true }) },
                {
                  label: 'Fork',
                  icon: 'git-merge',
                  onClick: () => {
                    const requestId = reqId();
                    run(async () => {
                      const f = await writeOnce('session_fork', { sessionId: props.id, requestId }, unknown, { signal: alive });
                      go(nextUrl(location.pathname + location.search, 'conversations', { focus: f.sessionId }));
                    });
                  },
                },
              ]}
            />
          </header>
          <Show when={panel() === 'rename'}>
            <RenamePanel
              sessionId={props.id}
              current={s()!.title ?? ''}
              onDone={() => setPanel(null)}
              onSaved={() => { setFailure(null); sDoc.reload(); }}
              run={run}
              onUnknown={() => { setFailure({ text: 'Rename outcome unknown. Check the current title.' }); sDoc.reload(); }}
            />
          </Show>
          <Show when={switchPanel()} keyed>{action => (
            <SwitchPanel
              sessionId={action.session.sessionId}
              handoff={action.handoff}
              current={str(rec(action.session.agentConfig).id)}
              onDone={note => { setPanel(null); if (note) setFailure(null); }}
              run={run}
            />
          )}</Show>
          <div class="state-line">
            <Show when={sessionState() in states_.session}>
              <StateBadge
                family="session"
                state={sessionState()}
                unconfirmed={unconfirmed()}
                lastSeen={Number.isFinite(lastSeen()) ? lastSeen() : undefined}
              />
            </Show>
            <Show when={s()!.projectId} keyed>
              {pid => (
                <>
                  <Show when={project()?.name}>{n => <span class="muted">{n()}</span>}</Show>
                  <EntityCode code={str(rec(project()).code)} id={pid} kind="project" />
                </>
              )}
            </Show>
            <Show when={hasRelated()}>
              <Button label="Related work" icon="folder-open" kind="quiet" pressed={inRelated()} onClick={toggleRelated} />
            </Show>
          </div>
          <ScopedRollupBar rollup={s()!.resources} cost={s()!.cost} inlineDetails={false} />
          <Show when={hasSwarm()}>
            <TabBar
              cls="detail-tabs"
              label="Conversation views"
              tabs={[
                { id: 'transcript', label: 'Transcript' },
                { id: 'swarm', label: 'Swarm', count: board.data!.summary.messagesTotal || board.data!.roster.length || undefined },
              ]}
              value={activeConvTab()}
              onChange={nextTab => {
                if (nextTab === 'swarm' && !untrack(inRelated)) reading = handle.current?.getAnchor() ?? null;
                if (nextTab === 'swarm') setShowRelated(false);
                setConvTab(nextTab);
              }}
            />
          </Show>
          <div class="status-line" role="status">
            <Show
              when={failure()}
              keyed
              fallback={
                <Show when={targetDoc.error || targetSessionDoc.error} fallback={
                  <Show when={thread.error}>
                    {err => <ErrorBanner text={`Thread did not load: ${err()}`} onRetry={thread.newest} />}
                  </Show>
                }>
                  {err => <ErrorBanner text={`Conversation target did not load: ${err()}`} onRetry={() => { targetDoc.reload(); targetSessionDoc.reload(); }} />}
                </Show>
              }
            >
              {f => <ErrorBanner text={f.text} onRetry={f.retry} />}
            </Show>
          </div>
          <Show
            when={activeConvTab() === 'swarm'}
            fallback={
              <div class="pane" data-related-work={String(inRelated())}>
                <DropZone onFiles={files.add} disabledReason={composerReason()}>
                  <Show when={!!thread.win} fallback={<Skeleton rows={6} />}>
                    <Show when={!!scope()}>
                      <Thread
                        blocks={blocks()}
                        label="Transcript"
                        now={now()}
                        marked={marked()}
                        selected={k => inside(sel(), k) && !textBounds(sel(), k)}
                        textBounds={k => textBounds(sel(), k)}
                        onTextRange={(anchor, focus) => { pickFrom = anchor.key; setSel(drag(anchor, focus)); }}
                        onRowClick={(k, shift) => {
                          if (!shift) { pickFrom = k; setSel(null); return; }
                          setSel(click(click(null, pickFrom ?? untrack(sel)?.start.key ?? k, false), k, true));
                          pickFrom ??= k;
                        }}
                        showSession={!!key() && 'task' in key()}
                        onOpenResult={r => go('/results/' + encodeURIComponent(r))}
                        resultSrc={r => '/api/results/' + encodeURIComponent(r)}
                        handle={handle}
                        onStart={inRelated() ? undefined : thread.older}
                        onEnd={inRelated() ? undefined : thread.newer}
                        stopReason={off() ? 'offline' : undefined}
                        onStop={x => run(() => op('execution_stop', { executionId: x }))}
                        onAnswer={(questionId, answer) => run(() => answerQuestion(questionId, answer))}
                        onCopyFail={copyFail}
                        onSecret={(qId: string, c: string, v: string) => addRequestedKey(qId, c, v, alive)}
                        pending={{ count: thread.win?.fresh ?? 0, onJump: () => { setMarked(null); setHits(null); thread.newest(); } }}
                      />
                    </Show>
                  </Show>
                  <Show when={inRelated()}>
                    <section class="related-work" aria-label="Related work" tabindex={0}>
                      <ScopedRollupDetails rollup={related()!} />
                    </section>
                  </Show>
                  {/* D3: steer goes to the execution and does not answer; the open question stays in sight above the composer. */}
                  <For each={openQs()}>
                    {oq => (
                      <DecisionBlock
                        question={oq.question ?? oq.text ?? ''}
                        askedBy="agent"
                        addressedTo="you"
                        since={oq.askedAt ? Date.parse(String(oq.askedAt)) || now() : now()}
                        now={now()}
                        options={(Array.isArray(oq.options) ? oq.options as unknown[] : keyConnection(oq.options) ? ['decline'] : []).map((o, i) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
                        onAnswer={a => run(() => answerQuestion(oq.id, a))}
                        onSecret={keyConnection(oq.options) ? v => addRequestedKey(oq.id, keyConnection(oq.options)!, v, alive) : undefined}
                      />
                    )}
                  </For>
                  <Show when={active()} keyed fallback={
                    <Show when={targetID() && targetID() !== props.id}>
                      <div class="composer-bar"><EntityCode id={targetID()!} kind="conversation" /></div>
                    </Show>
                  }>
                    {act => <div class="composer-bar"><EntityCode id={act.id} kind="execution" /></div>}
                  </Show>
                  <Composer
                    draftPersistence={draftPersistence()}
                    disabledReason={composerReason()}
                    placeholder={active() ? 'Steer' : 'Message'}
                    onAttach={files.add}
                    attachments={files.items}
                    onRemoveAttachment={files.remove}
                    chips={targetChips()}
                    onStop={stopComposer()}
                    stopReason={composerReason()}
                    onSend={async text => {
                      const sid = untrack(targetID), executionId = untrack(active)?.id ?? null;
                      if (!sid || untrack(composerReason)) throw Error('Conversation target unavailable');
                      // Files still uploading finish first; a failed one fails the send and stays on its chip.
                      let attachments: string[];
                      try { attachments = await files.ready(); } catch (e) { setFailure({ text: `Attachment not uploaded: ${errText(e)}` }); throw e; }
                      // One requestId per draft text and files until it is sent, kept next to the draft with the complete body: a resend
                      // of the same text and files after a failure or a reload is the same write, never a second one; other files make
                      // a new message (submissionKey). A definite error (nothing stored) clears it. A message with files is queued
                      // while the agent runs (its files are staged before a turn), so it is never a steer.
                      const dScope = authorizedDraftScope({ kind: 'session', id: props.id });
                      const body = { sessionId: sid, text, composer: { conversationId: props.id, executionId },
                        ...(attachments.length ? { attachments } : executionId ? { delivery: 'steer' } : {}) };
                      const request = await prepareSending(dScope, 'message_send', body), sKey = submissionKey(body);
                      setFailure(null);
                      try {
                        await writeOnce('message_send', request, unknown, { signal: alive });
                        sendingId(dScope, null, sKey);
                        files.clear(attachments);
                        setFailure(null);
                      } catch (e) {
                        if (!(e instanceof AccessChanged) && !(e instanceof OpError && e.message === 'aborted')) {
                          if (!unknownOutcome(e)) sendingId(dScope, null, sKey);
                          if (e instanceof OpError && e.status === 409) targetDoc.reload();
                          setFailure({ text: errText(e) });
                        }
                        throw e;
                      }
                    }}
                  />
                </DropZone>
              </div>
            }
          >
            <div class="pane" role="tabpanel" aria-label="Swarm">
              <section class="related-work" aria-label="Swarm" tabindex={0}>
                <SwarmPanel scopeKind="session" scopeId={props.id} board={board} />
              </section>
            </div>
          </Show>
        </article>
      }
    >
      <Match when={sDoc.error && !s()}>
        <article class="detail" data-ui="detail" tabindex={-1}><ErrorBanner text={`Conversation did not load: ${sDoc.error}`} /></article>
      </Match>
      <Match when={!s()}>
        <article class="detail" data-ui="detail" tabindex={-1}><Skeleton rows={6} /></article>
      </Match>
    </Switch>
  );
}

/** Rename (session_rename): an inline field, never a floating form (OR-14). The title it sets is kept; a generated one
 *  never replaces it. */
const titleUUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;

function RenamePanel(props: { sessionId: string; current: string; onDone: () => void; onSaved: () => void; run: (fn: () => Promise<unknown>) => Promise<void>; onUnknown: () => void }) {
  // A rename still retrying stops when the panel closes (Cancel, Escape, another panel), so it never lands later.
  const signal = useAlive();
  const [v, setV] = createSignal(untrack(() => props.current));
  const [busy, setBusy] = createSignal(false);
  const clean = () => v().trim().replace(/\s+/g, ' ');
  const chars = () => [...clean()].length;
  // The server's checks (session_titles.go sessionRename), said before sending.
  const reason = createMemo(() => {
    const c = clean(), n = chars();
    return !online.value ? 'offline' : !c ? 'type a title' : n > 200 ? 'at most 200 characters' : !/[\p{L}\p{N}]/u.test(c) ? 'a title holds a word'
      : titleUUID.test(c) ? 'no UUID in a title' : undefined;
  });
  const save = () => {
    if (untrack(busy)) return;
    setBusy(true);
    const title = untrack(clean);
    props.run(async () => {
      try {
        await op('session_rename', { sessionId: props.sessionId, title }, { signal });
        props.onDone();
        props.onSaved();
      } catch (error) {
        if (signal.aborted || error instanceof AccessChanged) return;
        if (!unknownOutcome(error)) throw error;
        props.onDone();
        props.onUnknown();
      } finally { setBusy(false); }
    });
  };
  return (
    <DetailSection title="Rename">
      <InlineForm
        cls="answers"
        onSubmit={e => { e.preventDefault(); if (!reason()) save(); }}
        onKeyDown={e => { if (e.key === 'Escape') props.onDone(); }}
      >
        <TextField label="Title" value={v()} onChange={setV} />
        <Button label="Save" kind="primary" busy={busy()} disabledReason={reason()} onClick={save} />
        <Button label="Cancel" kind="quiet" onClick={props.onDone} />
      </InlineForm>
    </DetailSection>
  );
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
  chips.push({
    icon: 'shield',
    label: [mode ?? 'Workspace', network ? 'network' : null].filter(Boolean).join(' · '),
    tip: `Permissions of the profile: ${tools.length ? `tools ${tools.join(', ')}` : 'the harness tools'}${network ? ', git network' : cfg.gitNetwork === false ? ', no git network' : ', git network as the project grants'}${settings.sandbox === false ? ', no sandbox' : ''}`,
  });
  return chips;
}

/**
 * Switch profile or model, or hand off to another agent (design "Menu actions"): session_transition decides reuse, a model
 * switch in the session, or a new session with a context handoff. An inline panel, never a floating form (OR-14).
 */
function SwitchPanel(props: { sessionId: string; handoff: boolean; current?: string; onDone: (note?: string) => void; run: (fn: () => Promise<unknown>) => Promise<void> }) {
  const profilesDoc = useOp('profiles_list', {});
  const accountsDoc = useOp('account_list', () => (props.handoff ? {} : null));
  const [profile, setProfile] = createSignal<string | null>(null);
  const [account, setAccount] = createSignal<string | null>(null);
  const [done, setDone] = createSignal<string | null>(null);
  const [next, setNext] = createSignal<string | null>(null);
  const [, nav] = useLocation();
  // Listed profiles are open records (contracts/profile.schema.json): their id, harness and model.
  const list = createMemo(() => (profilesDoc.data?.profiles ?? []).flatMap(p => (typeof p.id === 'string' ? [{ id: p.id, harness: str(p.harness), model: str(p.model) }] : [])));
  const selectedProfile = createMemo(() => list().find(p => p.id === profile()));
  const named = createMemo(() => {
    const sp = selectedProfile();
    return (accountsDoc.data?.accounts ?? []).filter(a => a.intake === 'all' && sp && a.harness === (sp.harness ?? 'codex'));
  });
  const submit = () => props.run(async () => {
    const prof = untrack(profile), acct = untrack(account);
    const r = await op('session_transition', { sessionId: props.sessionId, profileId: prof ?? undefined, ...(acct ? { account: acct } : {}) });
    setDone(r.transition);
    setNext(r.sessionId && r.sessionId !== props.sessionId ? r.sessionId : null);
  });
  const profileNodes = createMemo<CatalogueTreeNode[]>(() => list().map(p => ({
    id: p.id,
    label: p.id,
    sub: [p.harness ?? 'codex', p.model].filter(Boolean).join(' · '),
    icon: (isHarness(p.harness) ? harnesses[p.harness].icon : 'bot') as IconName,
    right: p.id === props.current ? <span class="muted">current</span> : undefined,
    searchText: `${p.id} ${p.harness ?? ''} ${p.model ?? ''}`,
  })));
  const accountNodes = createMemo<CatalogueTreeNode[]>(() => named().map(a => ({
    id: a.name,
    label: a.name,
    sub: a.harness ?? 'codex',
    icon: (isHarness(a.harness) ? harnesses[a.harness].icon : 'bot') as IconName,
    searchText: `${a.name} ${a.harness ?? ''}`,
  })));
  return (
    <DetailSection title={props.handoff ? 'Hand off to another agent' : 'Switch profile or model'}>
      <div class="stack">
        <EntityPicker
          label="Profile"
          nodes={profileNodes()}
          value={profile()}
          placeholder="Pick a profile"
          searchPlaceholder="Find profile"
          loading={!profilesDoc.data}
          onChange={v => { setProfile(v); setAccount(null); }}
        />
        <Show when={props.handoff}>
          <EntityPicker
            label="Account"
            nodes={accountNodes()}
            value={account()}
            placeholder={profile() ? 'Auto account' : 'Pick a profile first'}
            searchPlaceholder="Find account"
            loading={!accountsDoc.data}
            disabledReason={profile() ? undefined : 'Pick a profile first'}
            onChange={setAccount}
          />
        </Show>
        <div class="answers">
          <Button label={props.handoff ? 'Hand off' : 'Switch'} kind="primary" disabledReason={profile() ? undefined : 'pick a profile'} onClick={submit} />
          <Button label="Close" kind="quiet" onClick={() => props.onDone(done() ?? undefined)} />
          <Show when={done()}>{d => <span class="muted" role="status">{d()}</span>}</Show>
          <Show when={next()} keyed>
            {nx => (
              <a class="row-link" href={`/conversations/${encodeURIComponent(nx)}`} onClick={e => { e.preventDefault(); props.onDone(done() ?? undefined); nav(`/conversations/${encodeURIComponent(nx)}`); }}>
                Open the new conversation
              </a>
            )}
          </Show>
        </div>
      </div>
    </DetailSection>
  );
}
