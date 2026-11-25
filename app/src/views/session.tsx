// The one session view (docs/design-system/pages/session.md, OR-51): every bot, PM and task run opens here, from Home
// (`/home/C-…`, full width) and from Conversations (`/conversations/C-…`, the work area). Light chat: messages as prose,
// tool calls folded into one line each (OR-52), questions as cards answered in place (OR-53), no metrics header, model
// badge or cost (OR-54); the context panel is a side panel (side-panel.md): a column on the right, a sheet on a phone.
import { For, Match, Show, Switch, createEffect, createMemo, createSignal, onSettled, untrack } from 'solid-js';
import { currentUrl, useLocation } from '../router.ts';
import {
  Avatar, ProjectChip, StateDot, QuestionCard, Thread, ContextPanel, SidePanel, Composer,
  Button, ActionMenu, SearchField, ErrorBanner, Skeleton, DropZone, InlineForm, TextField, DetailSection, Disclosure, KvRow,
  EntityCode, EntityPicker, DecisionBlock, keyConnection, agentLabel, harnesses, writeClip, zone, lifeOf,
  type VirtualListHandle, type CatalogueTreeNode, type IconName, type Harness, type ComposerChip, type Life, type AvatarMark,
  type Timer, type Member, type PlanStep, type Automation, type RelatedTask,
} from '../../../packages/ui/src/index.ts';
import { op, online, addRequestedKey, writeOnce, prepareSending, submissionKey, sendingId, authorizedDraftScope, createDraftPersistence, unknownOutcome, AccessChanged, OpError, useAlive, type Ops } from '../api.ts';
import { useAttachments } from '../uploads.ts';
import { useOp } from '../store.ts';
import type { SessionGetReply } from '../../../contracts/generated/ops.ts';
import { nextUrl } from '../url.ts';
import { useThread, type ThreadKey } from '../threads.ts';
import { click, all, drag, inside, range, textBounds, type Selection } from '../selection.ts';
import { ScopedRollupBar, ScopedRollupDetails } from './resources.tsx';
import { SwarmPanel, hasSwarmBoard, useSwarmBoard } from './swarm-panel.tsx';

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));
const rec = (v: unknown): Record<string, unknown> => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {});
const str = (v: unknown) => (typeof v === 'string' ? v : undefined);
const isHarness = (h: string | null | undefined): h is Harness => !!h && h in harnesses;
const reqId = () => crypto.randomUUID();
const titleUUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;
/** A session's human code (D17): `C-FLT-12`. */
export const isSessionCode = (s: string) => /^C-[A-Z0-9]+-\d+$/i.test(s);
const lifeOfSession = (s: string): Life => (lifeOf.session as Record<string, Life>)[s] ?? 'asleep';
const ACTIVE = ['preparing', 'running', 'waiting'];

/** Where the view sits: Home opens it full width right of the navigation, Conversations in its work area (session.md, Frames). */
export type SessionFrame = 'home' | 'conversations';

/**
 * The one session view. `id` is the session's code (`C-FLT-12`) or its id; Home mounts it with `frame="home"`, Conversations
 * with `frame="conversations"` in the work area. `onClose` shows a Close icon in the head (the Conversations frame);
 * `onTitle` receives the session's name (Home titles the shell header with it).
 */
export function SessionView(props: { id: string; frame: SessionFrame; onClose?: () => void; onTitle?: (title: string) => void }) {
  const code = () => (isSessionCode(props.id) ? props.id : null);
  const resolved = useOp('entity_resolve', () => (code() ? { code: code()! } : null));
  const sessionId = createMemo(() => (code() ? (resolved.data?.type === 'C' ? resolved.data.id : undefined) : props.id));
  return (
    <Switch fallback={<article class="detail session-view" data-ui="detail" tabindex={-1} aria-label="Session"><Skeleton rows={6} /></article>}>
      <Match when={code() && resolved.error}>
        <article class="detail session-view" data-ui="detail" tabindex={-1} aria-label="Session">
          <ErrorBanner text={`${props.id} not found: ${resolved.error}`} onRetry={resolved.reload} />
        </article>
      </Match>
      <Match when={sessionId()} keyed>
        {id => <Session id={id} frame={props.frame} onClose={props.onClose} onTitle={props.onTitle} />}
      </Match>
    </Switch>
  );
}

function Session(props: { id: string; frame: SessionFrame; onClose?: () => void; onTitle?: (title: string) => void }) {
  const [, go] = useLocation();
  const alive = useAlive();
  const sDoc = useOp('session_get', () => ({ sessionId: props.id }));
  const targetDoc = useOp('thread_composer', () => ({ conversationId: props.id }));
  // quiet: a running session's execution changes many times a second; the panel rereads at most once a second.
  const ctxDoc = useOp('session_context', () => ({ sessionId: props.id }), { quiet: 1000 });
  const projects = useOp('project_list', () => (sDoc.data?.projectId ? {} : null));
  const [verifiedTarget, setVerifiedTarget] = createSignal<string | null>(null, { ownedWrite: true });
  createEffect(() => [sDoc.data, sDoc.restored, props.id] as const, ([s, restored, id]) => { if (s && !restored) setVerifiedTarget(id); });
  let previousCode: string | null | undefined;
  createEffect(() => sDoc.data?.code, c => { if (previousCode && c && previousCode !== c) projects.reload(); previousCode = c; });
  const project = createMemo(() => projects.data?.projects.find(p => rec(p).id === sDoc.data?.projectId));
  const s = () => sDoc.data;

  // Thread: a session that got a task shows the task's thread; Thread quiet folds its blocks (tool-fold.ts).
  const thread = useThread(() => ({ session: props.id }));
  const key = createMemo<ThreadKey>(() => thread.win?.key ?? { session: props.id });
  const [answers, setAnswers] = createSignal<Record<string, Ops['questions_answer']['reply']>>({});
  const [answering, setAnswering] = createSignal<Record<string, string>>({});
  const execStates = createMemo(() => new Map((s()?.executions ?? []).map(e => [e.id, String(e.state)])));
  // An answer given here shows in its place in the transcript at once (UR-58).
  const blocks = createMemo(() => {
    const ans = answers();
    return (thread.win?.blocks ?? []).map(b => {
      const a = b.kind === 'question' ? ans[b.questionId] : undefined;
      return a && b.kind === 'question' ? { ...b, state: a.state, answer: a.answer, answeredBy: a.answeredBy ?? undefined, answeredAt: a.answeredAt ? Date.parse(a.answeredAt) : null } : b;
    });
  });
  // Selection: a plain click marks where it starts; Shift+click selects up to that row; Cmd+A selects the thread and
  // Cmd+C copies the selected rows from the server as markdown (thread_export, started inside the key handler).
  const [selection, renderSelection] = createSignal<{ scope: string; range: Selection } | null>(null);
  let pickFrom: string | null = null;
  const scope = createMemo(() => JSON.stringify(key()));
  const sel = createMemo(() => (selection()?.scope === scope() ? selection()!.range : null));
  const setSel = (r: Selection) => renderSelection({ scope: untrack(scope), range: r });
  createEffect(() => scope(), () => { pickFrom = null; });

  const [failure, setFailure] = createSignal<{ text: string; retry?: () => void } | null>(null);
  const unknown = () => setFailure({ text: 'unknown, retrying' });
  const run = async (fn: () => Promise<unknown>) => { setFailure(null); try { await fn(); } catch (e) { setFailure({ text: errText(e), retry: () => run(fn) }); } };
  const off = () => !online.value;

  async function answerQuestion(questionId: string, answer: string) {
    setAnswering(a => ({ ...a, [questionId]: answer }));
    try {
      const row = await writeOnce('questions_answer', { questionId, answer }, unknown, { signal: alive });
      setAnswers(current => ({ ...current, [row.id]: row }));
    } finally { setAnswering(a => { const n = { ...a }; delete n[questionId]; return n; }); }
  }

  // Composer target (thread_composer): the session that receives the next message and its running execution.
  const active = createMemo(() => targetDoc.data?.active);
  const targetID = createMemo(() => targetDoc.data?.sessionId);
  const targetSessionDoc = useOp('session_get', () => { const sid = targetID(); return sid && sid !== props.id ? { sessionId: sid } : null; });
  const targetSession = () => (targetID() === props.id ? s() : targetSessionDoc.data);
  const composerReason = () => {
    if (off()) return 'offline';
    if (targetDoc.error || targetSessionDoc.error) return 'Session target unavailable';
    if (!targetDoc.data || verifiedTarget() !== props.id) return 'Validating session';
    if (!targetID()) return 'No session can receive messages';
  };
  const latest = createMemo(() => s()?.executions.at(-1));
  const runError = () => { const r = rec(latest()); return str(r.error) ?? str(r.errorMessage) ?? str(r.reason); };
  const state = () => String(s()?.state ?? latest()?.state ?? 'idle');
  const life = () => lifeOfSession(state());
  const openQs = createMemo(() => {
    const ans = answers();
    const qs = new Map((s()?.questions ?? []).map(q => [q.id, q]));
    for (const q of targetSession()?.questions ?? []) qs.set(q.id, q);
    return [...qs.values()].filter(q => (ans[q.id] ?? q).state === 'open');
  });
  // A question's choices: the stored record in session_get may not carry them; its thread block does.
  const blockOptions = createMemo(() => new Map((thread.win?.blocks ?? []).flatMap(b => (b.kind === 'question' ? [[b.questionId, b.options] as const] : []))));
  const optionsOf = (q: { id: string; options?: unknown }) => q.options ?? blockOptions().get(q.id);
  const last = () => s()?.messages.at(-1);
  const lastFiles = createMemo<string[]>(() => (last()?.attachments ?? []).map(a => str(rec(a).artifactId)).filter((x): x is string => !!x));
  const retryLast = () => {
    const requestId = reqId(), msg = last()?.text, fs = lastFiles();
    run(() => writeOnce('message_send', { sessionId: props.id, requestId, text: msg ?? '', ...(fs.length ? { attachments: fs } : {}) }, unknown, { signal: alive }));
  };
  const title = createMemo(() => {
    const session = s();
    if (!session) return 'Session';
    const fallback = (session.messages[0]?.text ?? '').replace(new RegExp(titleUUID.source, 'gi'), '').trim();
    return session.title || [...fallback].slice(0, 200).join('') || 'New conversation';
  });
  createEffect(() => (s() ? title() : null), t => { if (t) props.onTitle?.(t); });
  onSettled(() => () => props.onTitle?.(''));
  const avatar = createMemo<AvatarMark>(() => {
    const h = str(rec(s()?.agentConfig).harness) ?? latest()?.harness;
    return s()?.role === 'pm' ? { letter: 'P' } : isHarness(h) ? { harness: h } : { letter: title().slice(0, 1).toUpperCase() };
  });
  const projectName = () => str(rec(project()).name) ?? str(rec(project()).code);
  const stop = createMemo(() => { const x = active()?.id; return x ? () => run(() => op('execution_stop', { executionId: x })) : undefined; });

  // The URL a link to this session takes, in the frame it is open in.
  const here = (c: string) => `/${props.frame === 'home' ? 'home' : 'conversations'}/${encodeURIComponent(c)}`;
  // A new session (a fork, a handoff) opens in the same frame: Home keeps Home highlighted.
  const openSession = (sid: string) => (props.frame === 'home' ? go(here(sid)) : go(nextUrl(currentUrl(), 'conversations', { focus: sid })));

  // Search in the transcript (thread_search): an icon until used (OR-31); a hit replaces the window around it.
  const handle: { current: VirtualListHandle | null } = { current: null };
  const [q, setQ] = createSignal('');
  const [hits, setHits] = createSignal<{ key: string; snippet: string }[] | null>(null);
  const [hit, setHit] = createSignal(0);
  const [marked, setMarked] = createSignal<string | null>(null);
  const [jumpTo, setJumpTo] = createSignal<string | null>(null, { ownedWrite: true });
  // The hit is scrolled to once the replaced window is on screen; a hit inside a folded line marks that line (Thread quiet).
  createEffect(() => [thread.win, jumpTo()] as const, ([win, jt]) => {
    if (jt && win?.blocks.some(b => b.key === jt)) { handle.current?.scrollToKey(jt, 'center'); setJumpTo(null); }
  });
  const openHit = async (list: { key: string }[], i: number) => {
    const hitKey = list[i]?.key;
    if (!hitKey) return;
    setHit(i);
    setMarked(hitKey);
    await thread.jump(hitKey, () => { if (handle.current) handle.current.scrollToKey(hitKey, 'center'); else setJumpTo(hitKey); });
  };
  const search = () => run(async () => {
    const curQ = untrack(q);
    if (!curQ.trim()) { setHits(null); setMarked(null); return; }
    const r = await op('thread_search', { key: untrack(key), q: curQ, limit: 100 });
    setHits(r.hits);
    await openHit(r.hits, 0);
  });
  const copyFail = (e: unknown, retry?: () => void) => setFailure({ text: `Copy failed: ${errText(e)}`, retry });
  const copySel = (s0: NonNullable<Selection>) => {
    try {
      // Both formats (formats.md "Copy"); times in the reader's own zone.
      const text = op('thread_export', { key: untrack(key), ...range(s0), tz: zone() }, { signal: alive }).then(r => r.markdown);
      writeClip(text).catch(e => copyFail(e, () => copySel(s0)));
    } catch (e) { copyFail(e, () => copySel(s0)); }
  };
  const copyThread = () => copySel(all()!);

  // Panels that open in place under the head (OR-14): rename, start fresh, switch or hand off, details, timer edit, swarm board.
  const [panel, setPanel] = createSignal<'rename' | 'fresh' | 'details' | 'swarm' | { timer: number; paused: boolean } | { session: SessionGetReply; handoff: boolean } | null>(null);
  const switchPanel = createMemo(() => { const p = panel(); return p && typeof p === 'object' && 'session' in p ? p : null; });
  const timerPanel = createMemo(() => { const p = panel(); return p && typeof p === 'object' && 'timer' in p ? p : null; });
  const [nextInstruction, setNextInstruction] = createSignal('');
  const [forking, setForking] = createSignal(false);
  const fork = (text?: string) => {
    if (untrack(forking)) return;
    const requestId = reqId(), sid = props.id;
    run(async () => {
      setForking(true);
      try {
        const f = await writeOnce('session_fork', { sessionId: sid, requestId, ...(text ? { strategy: 'context', text } : {}) }, unknown, { signal: alive });
        setPanel(null);
        openSession(f.sessionId);
      } finally { setForking(false); }
    });
  };
  const board = useSwarmBoard('session', () => props.id);
  const archived = () => s()?.archivedAt != null;
  const canArchive = () => !ACTIVE.includes(state());
  const archive = () => run(async () => {
    await (archived() ? op('session_unarchive', { sessionId: props.id, reason: 'Unarchived from the session view' }) : op('session_archive', { sessionId: props.id }));
    sDoc.reload();
  });
  const copyLink = () => { const c = s()?.code; writeClip(location.origin + here(c ?? props.id)).catch(e => setFailure({ text: `Copy failed: ${errText(e)}` })); };

  // Context panel (session_context): plan, timers, automations, swarm, tasks. A side panel (side-panel.md): open or closed and
  // its width are the person's settings (session-context), the same for every session; at phone width it opens as a sheet.
  const ctx = () => ctxDoc.data;
  const plan = createMemo<PlanStep[]>(() => (ctx()?.plan?.steps ?? []).map(st => ({ text: st.title, done: st.done })));
  const timers = createMemo<Timer[]>(() => (ctx()?.timers ?? []).map(t => ({
    id: t.id, text: `Check in every ${every(t.everyMs)}`, paused: t.paused, readOnly: !t.canManage,
    when: t.paused ? `paused · every ${every(t.everyMs)}` : t.nextAt ? `${inTime(t.nextAt)} · every ${every(t.everyMs)}` : `every ${every(t.everyMs)}`,
  })));
  const automations = createMemo<Automation[]>(() => (ctx()?.automations ?? []).map(a => {
    const set = rec(a.settings);
    const event = str(set.event);
    return { rule: str(set.name) ?? 'Automation', when: set.enabled === false ? 'paused' : str(set.scheduleTime) || (event && event !== 'none' ? `on ${event}` : 'on request') };
  }));
  // A member's progress: the plan steps assigned to it that are done.
  const progress = (member: string) => {
    const mine = (ctx()?.plan?.steps ?? []).filter(st => member && st.owner === member);
    return mine.length ? `${mine.filter(st => st.done).length} of ${mine.length} steps` : undefined;
  };
  const swarm = createMemo<Member[]>(() => (ctx()?.swarm ?? []).flatMap(m => !m.sessionId ? [] : [{
    id: m.sessionId, name: [m.memberName, m.role].filter(Boolean).join(' · ') || m.sessionCode || 'Member', progress: progress(m.memberName),
    avatar: { letter: (m.memberName || m.role || '?').slice(0, 1).toUpperCase(), tone: lifeOfSession(String(m.state)) === 'done' ? 'success' : 'running' },
    state: lifeOfSession(String(m.state)), href: here(m.sessionCode ?? m.sessionId),
  }]));
  const tasks = createMemo<RelatedTask[]>(() => (ctx()?.tasks ?? []).map(t => ({
    id: t.id, code: t.code, title: t.title, state: (lifeOf.task as Record<string, Life>)[t.state] ?? 'queued', href: `/tasks/${encodeURIComponent(t.id)}`,
  })));
  // Showing or hiding the column changes the transcript's width and so its wrapped rows: keep the row being read where it
  // was (a list at its newest row keeps following). The composer and its draft stay mounted.
  const keepRow = () => {
    const view = handle.current, anchor = view && !view.isAtEdge() ? view.getAnchor() : null;
    return anchor ? () => handle.current?.restoreAnchor(anchor.key, anchor.offsetPx) : undefined;
  };
  // Timer changes (watch_timer_set) touch only the timer: the watch's event subscriptions and cursor stay. The base is
  // the timer on screen; a timer changed meanwhile answers 409 and the panel rereads.
  // A failure has no Retry: after a reread the base may differ, so the person acts again on what the panel shows.
  const timerChange = async (t: { everyMs: number; paused: boolean }, action: 'edit' | 'pause' | 'resume' | 'cancel', timerMs?: number) => {
    setFailure(null);
    try {
      await op('watch_timer_set', { sessionId: props.id, action, baseTimerMs: t.everyMs, basePaused: t.paused, ...(timerMs ? { timerMs } : {}) });
      setPanel(null);
    } catch (e) { setFailure({ text: errText(e) }); } finally { ctxDoc.reload(); }
  };
  const onTimer = (id: string, action: 'pause' | 'edit' | 'cancel', closeSheet: () => void) => {
    const t = ctx()?.timers.find(x => x.id === id);
    if (!t) return;
    if (action === 'edit') { closeSheet(); setPanel({ timer: t.everyMs, paused: t.paused }); return; }
    void timerChange(t, action === 'pause' ? (t.paused ? 'resume' : 'pause') : 'cancel');
  };
  // The base is the timer the panel opened on, so a timer changed meanwhile answers 409 instead of being overwritten.
  const saveTimer = (minutes: number) => {
    const t = timerPanel();
    if (t) void timerChange({ everyMs: t.timer, paused: t.paused }, 'edit', Math.round(minutes * 60_000));
  };
  // In the sheet the panel has its own Hide; the column is toggled from the shell header only, like the navigation.
  const contextPanel = (where: 'column' | 'sheet', close: () => void) => (
    <ContextPanel loading={!ctx() && !ctxDoc.error} plan={plan()} timers={timers()} automations={automations()} swarm={swarm()} tasks={tasks()}
      onHide={where === 'sheet' ? close : undefined} onTimer={(id, a) => onTimer(id, a, close)} onOpen={href => { close(); go(href); }} />
  );

  // Files for the next message: uploaded as they are added (picker, paste, drop over the session).
  const taskUpload = createMemo(() => !!targetDoc.data?.key.task);
  const uploadTarget = createMemo(() => (taskUpload() ? { projectId: s()?.projectId ?? undefined } : { sessionId: props.id }));
  const files = useAttachments(uploadTarget);
  const chips = createMemo<ComposerChip[]>(() => {
    const ts = targetSession();
    return ts ? composerChips(ts, active(), () => setPanel({ session: ts, handoff: false })) : [];
  });
  const draftPersistence = createMemo(() => (s() && !sDoc.restored && verifiedTarget() === props.id ? createDraftPersistence(authorizedDraftScope({ kind: 'session', id: props.id })) : undefined));
  const send = async (text: string) => {
    const sid = untrack(targetID), executionId = untrack(active)?.id ?? null;
    if (!sid || untrack(composerReason)) throw Error('Session target unavailable');
    // Files still uploading finish first; a failed one fails the send and stays on its chip.
    let attachments: string[];
    try { attachments = await files.ready(); } catch (e) { setFailure({ text: `Attachment not uploaded: ${errText(e)}` }); throw e; }
    // One requestId per draft text and files until it is sent (prepareSending): a resend after a failure or a reload is the
    // same write. A message with files is queued while the agent runs; without files it steers the run.
    const dScope = authorizedDraftScope({ kind: 'session', id: props.id });
    const body = { sessionId: sid, text, composer: { conversationId: props.id, executionId }, ...(attachments.length ? { attachments } : executionId ? { delivery: 'steer' } : {}) };
    const request = await prepareSending(dScope, 'message_send', body), sKey = submissionKey(body);
    setFailure(null);
    try {
      await writeOnce('message_send', request, unknown, { signal: alive });
      sendingId(dScope, null, sKey);
      files.clear(attachments);
    } catch (e) {
      if (!(e instanceof AccessChanged) && !(e instanceof OpError && e.message === 'aborted')) {
        if (!unknownOutcome(e)) sendingId(dScope, null, sKey);
        if (e instanceof OpError && e.status === 409) targetDoc.reload();
        setFailure({ text: errText(e) });
      }
      throw e;
    }
  };

  const now = () => Date.now();

  onSettled(() => {
    const on = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      // Escape in an open panel closes it; the sheet closes itself (PanelSheet).
      if (e.key === 'Escape' && untrack(panel) && !t?.closest?.('dialog')) { setPanel(null); return; }
      // Only inside the session (or with nothing focused); code, not key, so other keyboard layouts work too.
      if (!(e.metaKey || e.ctrlKey) || t?.closest?.('input,textarea,[contenteditable]') || !(t?.closest?.('.session-view') || t === document.body)) return;
      const cur = untrack(sel);
      if (e.code === 'KeyA') { e.preventDefault(); getSelection()?.removeAllRanges(); setSel(all()); }
      // A native selection inside one row is the browser's own copy.
      if (e.code === 'KeyC' && cur && (getSelection()?.isCollapsed ?? true)) { e.preventDefault(); copySel(cur); }
    };
    addEventListener('keydown', on);
    return () => removeEventListener('keydown', on);
  });

  return (
    <Switch
      fallback={
        <article class="detail session-view" data-ui="detail" data-frame={props.frame} tabindex={-1} aria-label={title()}>
          <div class="sv-main">
            <header class="sv-head">
              <Avatar {...avatar()} />
              <h2 class="sv-name" data-tip={title()}>{title()}</h2>
              <Show when={projectName()}>{n => <ProjectChip name={n()} />}</Show>
              <StateDot state={life()} />
              <span class="grow" />
              <SearchField label="Search thread" value={q()} onInput={setQ} onSearch={search} />
              <Show when={hits()} keyed>
                {hs => (
                  <span class="sv-hits">
                    <span class="num" aria-live="polite">{hs.length ? hit() + 1 : 0} of {hs.length}</span>
                    <Button label="Previous hit" icon="arrow-up" iconOnly kind="quiet" disabledReason={hit() <= 0 ? 'no earlier hit' : undefined} onClick={() => openHit(hs, hit() - 1)} />
                    <Button label="Next hit" icon="arrow-down" iconOnly kind="quiet" disabledReason={hit() >= hs.length - 1 ? 'no later hit' : undefined} onClick={() => openHit(hs, hit() + 1)} />
                  </span>
                )}
              </Show>
              <Show when={stop()} keyed>{st => <Button label="Stop" icon="square" iconOnly kind="quiet" disabledReason={off() ? 'offline' : undefined} onClick={st} />}</Show>
              <Button label="Fork" icon="git-merge" iconOnly kind="quiet" busy={forking()} disabledReason={off() ? 'offline' : undefined} onClick={() => fork()} />
              <ActionMenu
                label="Actions"
                disabledReason={off() ? 'offline' : undefined}
                items={[
                  { label: 'Details', icon: 'info', onClick: () => setPanel(p => (p === 'details' ? null : 'details')) },
                  { label: 'Copy link', icon: 'link', onClick: copyLink },
                  { label: 'Copy as markdown', icon: 'file-text', onClick: copyThread },
                  { label: 'Rename', icon: 'type', onClick: () => setPanel('rename') },
                  { label: 'Retry last message', icon: 'rotate-ccw', disabledReason: last()?.text || lastFiles().length ? undefined : 'no message yet', onClick: retryLast },
                  { label: 'Switch profile or model', icon: 'sliders-horizontal', onClick: () => setPanel({ session: s()!, handoff: false }) },
                  { label: 'Hand off to another agent', icon: 'bot', onClick: () => setPanel({ session: s()!, handoff: true }) },
                  { label: 'Start fresh with context', icon: 'git-merge', disabledReason: latest() ? undefined : 'Send a message first', onClick: () => setPanel('fresh') },
                  ...(hasSwarmBoard(board.data) ? [{ label: 'Swarm board', icon: 'messages-square' as IconName, onClick: () => setPanel(p => (p === 'swarm' ? null : 'swarm')) }] : []),
                  archived()
                    ? { label: 'Unarchive', icon: 'archive-restore' as IconName, onClick: archive }
                    : { label: 'Archive', icon: 'archive' as IconName, disabledReason: canArchive() ? undefined : 'Execution active', onClick: archive },
                ]}
              />
              <Show when={props.onClose}><Button label="Close" icon="x" iconOnly kind="quiet" onClick={() => props.onClose?.()} /></Show>
            </header>

            <Show when={panel() === 'details'}>
              <section class="sv-panel" aria-label="Details">
                <ScopedRollupBar rollup={s()!.resources} cost={s()!.cost} inlineDetails={false} />
                <KvRow icon="messages-square" label="Session" value={<EntityCode code={s()!.code} id={props.id} kind="conversation" />} />
                <Show when={latest()} keyed>{e => <KvRow icon="activity" label="Latest run" value={<EntityCode id={e.id} kind="execution" />} />}</Show>
                <ForkDetails session={s()!} open={openSession} />
                <Show when={s()!.resources}>{r => <Disclosure summary="Related work"><ScopedRollupDetails rollup={r()} /></Disclosure>}</Show>
              </section>
            </Show>
            <Show when={panel() === 'rename'}>
              <RenamePanel sessionId={props.id} current={s()!.title ?? ''} onDone={() => setPanel(null)} onSaved={() => { setFailure(null); sDoc.reload(); }} run={run}
                onUnknown={() => { setFailure({ text: 'Rename outcome unknown. Check the current title.' }); sDoc.reload(); }} />
            </Show>
            <Show when={panel() === 'fresh'}>
              <DetailSection title="Start fresh with context">
                <InlineForm cls="stack" onSubmit={e => { e.preventDefault(); const text = nextInstruction().trim(); if (!off() && text) fork(text); }}>
                  <TextField label="Next instruction" value={nextInstruction()} multiline onChange={setNextInstruction} />
                  <div class="answers">
                    <Button label="Start fresh" kind="primary" busy={forking()} disabledReason={off() ? 'offline' : nextInstruction().trim() ? undefined : 'Enter the next instruction'} onClick={() => fork(nextInstruction().trim())} />
                    <Button label="Cancel" kind="quiet" onClick={() => setPanel(null)} />
                  </div>
                </InlineForm>
              </DetailSection>
            </Show>
            <Show when={switchPanel()} keyed>
              {a => <SwitchPanel sessionId={a.session.sessionId} handoff={a.handoff} current={str(rec(a.session.agentConfig).id)} onDone={note => { setPanel(null); if (note) setFailure(null); }} run={run} />}
            </Show>
            <Show when={timerPanel()} keyed>{t => <TimerPanel everyMs={t.timer} onSave={saveTimer} onDone={() => setPanel(null)} />}</Show>

            {/* The open question, pinned above the transcript (OR-53); a failed run asks to retry (UR-63). */}
            <For each={openQs().filter(oq => !keyConnection(optionsOf(oq)))}>
              {oq => (
                <QuestionCard
                  question={oq.question ?? oq.text ?? ''}
                  choices={(Array.isArray(optionsOf(oq)) ? (optionsOf(oq) as unknown[]) : []).map((o, i) => ({ label: String(o), primary: i === 0 }))}
                  answering={answering()[oq.id]}
                  onAnswer={a => run(() => answerQuestion(oq.id, a))}
                />
              )}
            </For>
            {/* A key request: the owner's value goes to the connection, never into the transcript. */}
            <For each={openQs().filter(oq => keyConnection(optionsOf(oq)))}>
              {oq => (
                <DecisionBlock
                  question={oq.question ?? oq.text ?? ''} askedBy="agent" addressedTo="you" since={oq.askedAt ? Date.parse(String(oq.askedAt)) || now() : now()} now={now()}
                  options={[{ label: 'decline', kbd: '1' }]}
                  onAnswer={a => run(() => answerQuestion(oq.id, a))}
                  onSecret={v => addRequestedKey(oq.id, keyConnection(optionsOf(oq))!, v, alive)}
                />
              )}
            </For>
            <Show when={life() === 'failed' && !openQs().length}>
              <QuestionCard tone="danger" question={`The last run failed${runError() ? `: ${runError()}` : ''}`} choices={[{ label: 'Retry', primary: true }]} onAnswer={retryLast} />
            </Show>

            <div class="sv-status" role="status">
              <Show when={failure()} keyed fallback={
                <Show when={targetDoc.error || targetSessionDoc.error} fallback={<Show when={thread.error}>{err => <ErrorBanner text={`Transcript did not load: ${err()}`} onRetry={thread.newest} />}</Show>}>
                  {err => <ErrorBanner text={`Session target did not load: ${err()}`} onRetry={() => { targetDoc.reload(); targetSessionDoc.reload(); }} />}
                </Show>
              }>
                {f => <ErrorBanner text={f.text} onRetry={f.retry} />}
              </Show>
            </div>

            <DropZone onFiles={files.add} disabledReason={composerReason()}>
              <Show when={panel() === 'swarm'} fallback={
                <div class="sv-transcript">
                  <Show when={!!thread.win} fallback={<Skeleton rows={6} />}>
                    <Thread
                      quiet
                      executions={execStates()}
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
                      showSession={'task' in key()}
                      onOpenResult={r => go('/results/' + encodeURIComponent(r))}
                      resultSrc={r => '/api/results/' + encodeURIComponent(r)}
                      handle={handle}
                      onStart={thread.older}
                      onEnd={thread.newer}
                      onCopyFail={copyFail}
                      pending={{ count: thread.win?.fresh ?? 0, onJump: () => { setMarked(null); setHits(null); thread.newest(); } }}
                    />
                  </Show>
                </div>
              }>
                <section class="sv-transcript related-work" aria-label="Swarm board" tabindex={0}>
                  <SwarmPanel scopeKind="session" scopeId={props.id} board={board} />
                </section>
              </Show>
              <Composer
                draftPersistence={draftPersistence()}
                disabledReason={composerReason()}
                placeholder={active() ? 'Steer' : 'Message'}
                onAttach={files.add}
                attachments={files.items}
                onRemoveAttachment={files.remove}
                chips={chips()}
                onStop={stop()}
                stopReason={composerReason()}
                onSend={send}
              />
            </DropZone>
          </div>
          <SidePanel setting="session-context" name="context" label="Session context" onLayout={keepRow}>{contextPanel}</SidePanel>
        </article>
      }
    >
      <Match when={sDoc.error && !s()}>
        <article class="detail session-view" data-ui="detail" tabindex={-1} aria-label="Session"><ErrorBanner text={`Session did not load: ${sDoc.error}`} onRetry={sDoc.reload} /></article>
      </Match>
      <Match when={!s()}>
        <article class="detail session-view" data-ui="detail" tabindex={-1} aria-label="Session"><Skeleton rows={6} /></article>
      </Match>
    </Switch>
  );
}

const every = (ms: number) => (ms % 3_600_000 === 0 ? `${ms / 3_600_000}h` : ms % 60_000 === 0 ? `${ms / 60_000}m` : `${Math.round(ms / 1000)}s`);
const inTime = (at: number) => { const m = Math.round((at - Date.now()) / 60_000); return m < 1 ? 'due now' : m < 60 ? `in ${m}m` : `in ${Math.round(m / 60)}h`; };

/** Edit a timer: its period in minutes (watch_timer_set edit). */
function TimerPanel(p: { everyMs: number; onSave: (minutes: number) => void; onDone: () => void }) {
  const [v, setV] = createSignal(String(Math.max(1, Math.round(untrack(() => p.everyMs) / 60_000))));
  const n = () => Number(v());
  const reason = () => (!online.value ? 'offline' : !(n() >= 1) ? 'at least 1 minute' : undefined);
  return (
    <DetailSection title="Timer">
      <InlineForm cls="answers" onSubmit={e => { e.preventDefault(); if (!reason()) p.onSave(n()); }}>
        <TextField label="Every, minutes" value={v()} onChange={setV} />
        <Button label="Save" kind="primary" disabledReason={reason()} onClick={() => p.onSave(n())} />
        <Button label="Cancel" kind="quiet" onClick={p.onDone} />
      </InlineForm>
    </DetailSection>
  );
}

/**
 * The composer's facts (controls/composer.md): the agent profile with its model and effort (a click opens Switch profile
 * or model), and the permissions the profile grants, shown only.
 */
function composerChips(s: SessionGetReply, active: { harness?: string; model?: string; effort?: string } | undefined, onSwitch: () => void): ComposerChip[] {
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

const forkReasons: Record<string, string> = {
  fresh_session_requested: 'Start fresh with context selected',
  source_execution_open: 'Source execution still open',
  native_context_missing: 'No native session recorded',
  harness_fork_unsupported: 'Harness cannot fork the native session',
  host_capability_missing: 'Host has not reported native fork support',
  native_context_available: 'Native session and fork support available',
};

function ForkDetails(props: { session: SessionGetReply; open: (id: string) => void }) {
  const source = () => rec(props.session.forkOf);
  const parent = () => str(source().sessionId);
  const strategy = () => str(source().strategy);
  const reason = () => str(source().reason);
  const packet = () => props.session.handoffs?.find(h => h.strategy === 'fork_summary');
  const recipient = () => props.session.executions.find(e => e.id === packet()?.executionId) ?? props.session.executions[0];
  const child = () => str(props.session.forks?.at(-1)?.sessionId);
  const packetDoc = useOp('session_handoffs', () => (packet() ? { sessionId: props.session.id } : null));
  const text = () => str(packetDoc.data?.handoffs.find(h => h.id === packet()?.id)?.text);
  return (
    <>
      <Show when={parent()} keyed>
        {id => (
          <DetailSection title="Session fork">
            <KvRow icon="git-merge" label="Routing decision" value={strategy() === 'native' ? 'Native fork' : strategy() === 'context_transfer' ? 'Fresh session with context transfer' : 'Unknown'} />
            <KvRow icon="activity" label="Trigger" value={forkReasons[reason() ?? ''] ?? 'Unknown'} />
            <KvRow icon="messages-square" label="Source conversation" value={<Button label="Open source" kind="quiet" onClick={() => props.open(id)} />} />
            <Show when={str(source().executionId)} keyed>{executionId => <KvRow icon="activity" label="Source execution" value={<EntityCode id={executionId} kind="execution" />} />}</Show>
            <Show when={strategy() === 'context_transfer'}>
              <KvRow icon="file-text" label="Handoff packet" value={packet() ? str(packet()?.state) ?? 'Unknown' : 'No retained turns'} />
              <Show when={packet()}>
                <Disclosure summary="Read the handoff packet">
                  <Show when={packetDoc.error} keyed>{error => <ErrorBanner text={error} onRetry={packetDoc.reload} />}</Show>
                  <Show when={text()} keyed fallback={<Show when={!packetDoc.error}><p>{packetDoc.data ? 'Packet text unavailable' : 'Loading packet…'}</p></Show>}>
                    {body => <pre tabindex={0} role="region" aria-label="Handoff packet text" class="sv-packet">{body}</pre>}
                  </Show>
                </Disclosure>
              </Show>
              <KvRow icon="activity" label="Fresh execution" value={
                <Show when={recipient()} keyed fallback="Waiting for the first message">
                  {e => <>{e.observation === 'unconfirmed' ? 'Unconfirmed' : e.state} <EntityCode id={e.id} kind="execution" /></>}
                </Show>
              } />
            </Show>
          </DetailSection>
        )}
      </Show>
      <Show when={child()} keyed>{id => <Button label="Open latest child conversation" icon="git-merge" kind="quiet" onClick={() => props.open(id)} />}</Show>
    </>
  );
}

/** Rename (session_rename): an inline field, never a floating form (OR-14). */
function RenamePanel(props: { sessionId: string; current: string; onDone: () => void; onSaved: () => void; run: (fn: () => Promise<unknown>) => Promise<void>; onUnknown: () => void }) {
  const signal = useAlive();
  const [v, setV] = createSignal(untrack(() => props.current));
  const [busy, setBusy] = createSignal(false);
  const clean = () => v().trim().replace(/\s+/g, ' ');
  // The server's checks (session_titles.go sessionRename), said before sending.
  const reason = createMemo(() => {
    const c = clean(), n = [...c].length;
    return !online.value ? 'offline' : !c ? 'type a title' : n > 200 ? 'at most 200 characters' : !/[\p{L}\p{N}]/u.test(c) ? 'a title holds a word' : titleUUID.test(c) ? 'no UUID in a title' : undefined;
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
      <InlineForm cls="answers" onSubmit={e => { e.preventDefault(); if (!reason()) save(); }}>
        <TextField label="Title" value={v()} onChange={setV} />
        <Button label="Save" kind="primary" busy={busy()} disabledReason={reason()} onClick={save} />
        <Button label="Cancel" kind="quiet" onClick={props.onDone} />
      </InlineForm>
    </DetailSection>
  );
}

/**
 * Switch profile or model, or hand off to another agent: session_transition decides reuse, a model switch in the session,
 * or a new session with a context handoff. An inline panel, never a floating form (OR-14).
 */
function SwitchPanel(props: { sessionId: string; handoff: boolean; current?: string; onDone: (note?: string) => void; run: (fn: () => Promise<unknown>) => Promise<void> }) {
  const profilesDoc = useOp('profiles_list', {});
  const accountsDoc = useOp('account_list', () => (props.handoff ? {} : null));
  const [profile, setProfile] = createSignal<string | null>(null);
  const [account, setAccount] = createSignal<string | null>(null);
  const [done, setDone] = createSignal<string | null>(null);
  const [next, setNext] = createSignal<string | null>(null);
  const [, nav] = useLocation();
  const list = createMemo(() => (profilesDoc.data?.profiles ?? []).flatMap(p => (typeof p.id === 'string' ? [{ id: p.id, harness: str(p.harness), model: str(p.model) }] : [])));
  const selectedProfile = createMemo(() => list().find(p => p.id === profile()));
  const named = createMemo(() => { const sp = selectedProfile(); return (accountsDoc.data?.accounts ?? []).filter(a => a.intake === 'all' && sp && a.harness === (sp.harness ?? 'codex')); });
  const submit = () => props.run(async () => {
    const prof = untrack(profile), acct = untrack(account);
    const r = await op('session_transition', { sessionId: props.sessionId, profileId: prof ?? undefined, ...(acct ? { account: acct } : {}) });
    setDone(r.transition);
    setNext(r.sessionId && r.sessionId !== props.sessionId ? r.sessionId : null);
  });
  const profileNodes = createMemo<CatalogueTreeNode[]>(() => list().map(p => ({
    id: p.id, label: p.id, sub: [p.harness ?? 'codex', p.model].filter(Boolean).join(' · '),
    icon: (isHarness(p.harness) ? harnesses[p.harness].icon : 'bot') as IconName,
    right: p.id === props.current ? <span class="muted">current</span> : undefined,
    searchText: `${p.id} ${p.harness ?? ''} ${p.model ?? ''}`,
  })));
  const accountNodes = createMemo<CatalogueTreeNode[]>(() => named().map(a => ({
    id: a.name, label: a.name, sub: a.harness ?? 'codex', icon: (isHarness(a.harness) ? harnesses[a.harness].icon : 'bot') as IconName, searchText: `${a.name} ${a.harness ?? ''}`,
  })));
  const openNext = (nx: string) => { props.onDone(done() ?? undefined); nav(`/conversations/${encodeURIComponent(nx)}`); };
  return (
    <DetailSection title={props.handoff ? 'Hand off to another agent' : 'Switch profile or model'}>
      <div class="stack">
        <EntityPicker label="Profile" nodes={profileNodes()} value={profile()} placeholder="Pick a profile" searchPlaceholder="Find profile" loading={!profilesDoc.data} onChange={v => { setProfile(v); setAccount(null); }} />
        <Show when={props.handoff}>
          <EntityPicker label="Account" nodes={accountNodes()} value={account()} placeholder={profile() ? 'Auto account' : 'Pick a profile first'} searchPlaceholder="Find account" loading={!accountsDoc.data} disabledReason={profile() ? undefined : 'Pick a profile first'} onChange={setAccount} />
        </Show>
        <div class="answers">
          <Button label={props.handoff ? 'Hand off' : 'Switch'} kind="primary" disabledReason={profile() ? undefined : 'pick a profile'} onClick={submit} />
          <Button label="Close" kind="quiet" onClick={() => props.onDone(done() ?? undefined)} />
          <Show when={done()}>{d => <span class="muted" role="status">{d()}</span>}</Show>
          <Show when={next()} keyed>{nx => <Button label="Open the new conversation" kind="quiet" onClick={() => openNext(nx)} />}</Show>
        </div>
      </div>
    </DetailSection>
  );
}
