// The one session view (docs/design-system/pages/session.md, OR-51): every bot, PM and task run opens here, from Home
// (`/home/C-…`, full width) and from Conversations (`/conversations/C-…`, the work area). Light chat: messages as prose,
// tool calls folded into one line each (OR-52), questions as cards answered in place (OR-53), no metrics header, model
// badge or cost (OR-54); the context panel is a side panel (side-panel.md): a column on the right, a sheet on a phone.
import { For, Match, Show, Switch, createEffect, createMemo, createSignal, onSettled, untrack } from 'solid-js';
import { currentUrl, useLocation } from '../router.ts';
import {
  Avatar, ProjectChip, StateDot, QuestionCard, OpenQuestion, Thread, ContextPanel, SidePanel, Composer, TimerChip, CommandPreview,
  Button, ActionMenu, SearchField, ErrorBanner, Skeleton, DropZone, InlineForm, TextField, DetailSection, Disclosure, KvRow,
  EntityCode, EntityPicker, agentLabel, harnesses, writeClip, zone, life as lifeIn, statesOf, timeOf, plural, clock,
  type VirtualListHandle, type CatalogueTreeNode, type IconName, type Harness, type ComposerChip, type Life, type AvatarMark,
  type Timer, type Member, type PlanStep, type Automation, type RelatedTask,
} from '../../../packages/ui/src/index.ts';
import { op, online, writeOnce, prepareSending, submissionKey, sendingId, authorizedDraftScope, createDraftPersistence, unknownOutcome, AccessChanged, OpError, useAlive, type Ops } from '../api.ts';
import { useAnswers } from '../answers.ts';
import { access } from '../access.ts';
import { useAttachments } from '../uploads.ts';
import { useOp, useView } from '../store.ts';
import type { SessionAgent, SessionGetReply } from '../../../contracts/generated/ops.ts';
import { nextUrl } from '../url.ts';
import { useThread, type ThreadKey } from '../threads.ts';
import { botAvatarState, botPicture } from '../bot-view.ts';
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
const lifeOfSession = (s: string): Life => lifeIn('session', s);
const ACTIVE: string[] = statesOf('session', ['working', 'waiting']);

/** Where the view sits: Home opens it full width right of the navigation, Conversations in its work area (session.md, Frames). */
export type SessionFrame = 'home' | 'conversations';

/**
 * The one session view. `id` is the session's code (`C-FLT-12`) or its id; Home mounts it with `frame="home"`, Conversations
 * with `frame="conversations"` in the work area. `onClose` shows a Close icon in the head (the Conversations frame);
 * `onTitle` receives the session's name (Home titles the shell header with it).
 */
export function SessionView(props: { id: string; frame: SessionFrame; thread?: string; onClose?: () => void; onTitle?: (title: string) => void }) {
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
        {id => <Session id={id} frame={props.frame} thread={props.thread} onClose={props.onClose} onTitle={props.onTitle} />}
      </Match>
    </Switch>
  );
}

function Session(props: { id: string; frame: SessionFrame; thread?: string; onClose?: () => void; onTitle?: (title: string) => void }) {
  const [, go] = useLocation();
  const alive = useAlive();
  // One read for the screen (session_view, docs/designs/fleet-conversations.md "Session view"): the head, the composer
  // target, open questions, threads, triggers, the context panel and the newest turns as summaries. quiet: a running
  // session's execution changes many times a second; the view rereads at most once a second.
  const sDoc = useView('session_view', () => ({ sessionId: props.id }), { quiet: 1000 });
  const [verifiedTarget, setVerifiedTarget] = createSignal<string | null>(null, { ownedWrite: true });
  createEffect(() => [sDoc.data, sDoc.restored, props.id] as const, ([s, restored, id]) => { if (s && !restored) setVerifiedTarget(id); });
  const s = () => sDoc.data?.head;
  const view = () => sDoc.data;
  // The project's name for the head chip: project_list is one read every page shares.
  const projects = useOp('project_list', () => (s()?.projectId ? {} : null));
  const project = createMemo(() => projects.data?.projects.find(p => rec(p).id === s()?.projectId));

  // Work-item threads (fleet-conversations.md#work-item-threads; Conversations v2, owner pick B): the Conversations list
  // shows a bot's threads under its row and opens one by ?thread=<key>; the head names it after the bot ("HAL0 › Main").
  // Without a pick the shown thread is the newest Open one, else Main; the composer writes into the shown one.
  const threadItems = createMemo(() => view()?.threads ?? []);
  const hasThreads = createMemo(() => threadItems().length > 1);
  const shownThread = createMemo(() => {
    const ts = threadItems(), p = props.thread;
    if (p !== undefined && ts.some(t => t.threadKey === p)) return p;
    return ts.filter(t => t.open).sort((a, b) => (b.lastActivityAt ?? 0) - (a.lastActivityAt ?? 0))[0]?.threadKey ?? '';
  });
  const shownTitle = createMemo(() => { const k = shownThread(); return k === '' ? 'Main' : threadItems().find(t => t.threadKey === k)?.title || k; });
  // The shown thread counts as read (person_visit page thread; it writes no change row, so session_view rereads after it)
  // once it has something unread; one mark per thread and unread count, so a failed mark is not repeated in a loop.
  let readMark = '';
  createEffect(() => [hasThreads(), shownThread(), threadItems().find(t => t.threadKey === shownThread())?.unread ?? 0] as const, ([on, th, unread]) => {
    const mark = `${th}:${unread}`;
    if (!on || unread === 0 || mark === readMark) return;
    readMark = mark;
    op('person_visit', { page: 'thread', thread: { sessionId: props.id, thread: th } }, { signal: alive }).then(() => sDoc.reload(), () => {});
  });
  // Thread: a session that got a task shows the task's thread; Thread quiet folds its blocks (tool-fold.ts).
  const thread = useThread(() => (hasThreads() ? { session: props.id, thread: shownThread() } : { session: props.id }));
  const key = createMemo<ThreadKey>(() => thread.win?.key ?? { session: props.id });
  const answers = useAnswers();
  const execStates = createMemo(() => new Map((view()?.executions.rows ?? []).map(e => [e.id, e.state])));
  // An answer given here shows in its place in the transcript at once (UR-58).
  const blocks = createMemo(() => {
    return (thread.win?.blocks ?? []).map(b => {
      const a = b.kind === 'question' ? answers.answered(b.questionId)?.reply : undefined;
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

  // Composer target (session_view composer): the session that receives the next message and its running execution.
  const composer = () => view()?.composer;
  const active = createMemo(() => composer()?.activeExecutionId ?? undefined);
  const targetID = createMemo(() => composer()?.sessionId ?? undefined);
  const composerReason = () => {
    if (off()) return 'offline';
    if (sDoc.error && !view()) return 'Session target unavailable';
    if (!view() || verifiedTarget() !== props.id) return 'Validating session';
    if (!targetID()) return composer()?.reason ?? 'No session can receive messages';
  };
  // The newest turn of the session (session_view lists turns newest first).
  const latest = createMemo(() => view()?.executions.rows.find(e => e.kind === 'agent_run') ?? view()?.executions.rows[0]);
  const runError = () => s()?.error ?? undefined;
  const state = () => s()?.state ?? 'idle';
  const life = () => lifeOfSession(state());
  const openQs = createMemo(() => (view()?.questions ?? []).filter(q => !answers.answered(q.id)));
  // A question's choices: the stored record may not carry them; its thread block does.
  const blockOptions = createMemo(() => new Map((thread.win?.blocks ?? []).flatMap(b => (b.kind === 'question' ? [[b.questionId, b.options] as const] : []))));
  const optionsOf = (q: { id: string; options?: string[] }) => (q.options?.length ? q.options : blockOptions().get(q.id));
  const last = () => view()?.lastMessage;
  const lastFiles = createMemo<string[]>(() => last()?.attachments ?? []);
  const retryLast = () => {
    const requestId = reqId(), msg = last()?.text, fs = lastFiles();
    run(() => writeOnce('message_send', { sessionId: props.id, requestId, text: msg ?? '', ...(fs.length ? { attachments: fs } : {}) }, unknown, { signal: alive }));
  };
  // The set or generated title, else the first message's start (the server resolves it), never a UUID.
  const title = createMemo(() => (s() ? s()!.title.replace(new RegExp(titleUUID.source, 'gi'), '').trim() || 'New conversation' : 'Session'));
  createEffect(() => (s() ? title() : null), t => { if (t) props.onTitle?.(t); });
  onSettled(() => () => props.onTitle?.(''));
  // A bot's conversation shows the bot itself as the speaker: its own avatar (picture or letter) with its activity state, as
  // the rail and Home do (owner, 2026-11-26); the harness mark belongs to agent runs.
  const bots = useOp('bot_list', () => (s()?.botId ? {} : null));
  const bot = createMemo(() => (bots.data?.bots ?? []).find(b => b.id === s()?.botId));
  const avatar = createMemo<AvatarMark>(() => {
    const b = bot();
    if (b) return { letter: b.name.slice(0, 1).toUpperCase(), picture: botPicture(b) };
    const h = s()?.agent.harness ?? latest()?.harness ?? undefined;
    return s()?.role === 'pm' ? { letter: 'P' } : isHarness(h) ? { harness: h } : { letter: title().slice(0, 1).toUpperCase() };
  });
  const avatarState = createMemo(() => { const b = bot(); return b ? botAvatarState(b) : undefined; });
  const projectName = () => str(rec(project()).name) ?? s()?.projectCode ?? undefined;
  const stop = createMemo(() => { const x = active(); return x ? () => run(() => op('execution_stop', { executionId: x })) : undefined; });

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

  // Panels that open in place under the head (OR-14): rename, start fresh, switch or hand off, details, swarm board.
  const [panel, setPanel] = createSignal<'rename' | 'fresh' | 'details' | 'swarm' | { sessionId: string; profileId: string | null; handoff: boolean } | null>(null);
  const switchPanel = createMemo(() => { const p = panel(); return p && typeof p === 'object' && 'sessionId' in p ? p : null; });
  // Details (session_get: forks, handoffs, the resource rollup) is read only while the panel is open.
  const detailsDoc = useOp('session_get', () => (panel() === 'details' ? { sessionId: props.id } : null));
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

  // Context panel (session_view context and triggers): plan, timers, automations, swarm, tasks. A side panel (side-panel.md):
  // open or closed and its width are the person's settings (session-context), the same for every session; at phone width it
  // opens as a sheet.
  const ctx = () => view()?.context;
  const trigDoc = { get data() { return view()?.triggers; }, reload: () => sDoc.reload() };
  const plan = createMemo<PlanStep[]>(() => (ctx()?.plan?.steps ?? []).map(st => ({ text: st.title, done: st.done })));
  // Timers: the session's timed triggers. One that checks every pass (passMs, the server's FLEET_TRIGGER_TICK_MS, or less) follows changes, not the clock, and
  // is not shown, as in session_context.
  const timed = createMemo(() => { const pass = trigDoc.data?.passMs ?? 0; return (trigDoc.data?.triggers ?? []).filter(t => t.settings.when === 'daily' || (t.settings.when === 'every' && (t.settings.everyMs ?? 0) > pass)); });
  const [editing, setEditing] = createSignal<{ id: string; revision: number } | null>(null);
  const timers = createMemo<Timer[]>(() => timed().map(t => ({
    id: t.id, schedule: scheduleOf(t.settings), text: t.settings.text, paused: !t.settings.enabled, readOnly: !trigDoc.data?.canManage,
    nextAt: timeOf(t.nextAt) ?? undefined, selected: editing()?.id === t.id,
    quiet: t.quietChecks > 0 ? `${plural(t.quietChecks, 'quiet check')}${t.quietSince ? ` since ${clock(Date.parse(t.quietSince))}` : ''}` : undefined,
  })));
  // The head's timer chip (owner pick A): the first active timer, else the first paused one; a click opens Context.
  const headTimer = createMemo(() => { const ts = timed(); const t = ts.find(x => x.settings.enabled) ?? ts[0]; return t ? { t, more: ts.length - 1 } : null; });
  const [reveal, setReveal] = createSignal(0);
  // The automations of the session's bot: its triggers, one line each (name and when).
  const botTrigDoc = { get data() { return view()?.botTriggers; } };
  const automations = createMemo<Automation[]>(() => (botTrigDoc.data?.triggers ?? []).map(t => ({
    rule: t.settings.name, when: !t.settings.enabled ? 'paused' : whenOf(t.settings, botTrigDoc.data!.passMs),
  })));
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
    id: t.id, code: t.code, title: t.title, state: lifeIn('task', t.state), href: `/tasks/${encodeURIComponent(t.id)}`,
  })));
  // Showing or hiding the column changes the transcript's width and so its wrapped rows: keep the row being read where it
  // was (a list at its newest row keeps following). The composer and its draft stay mounted.
  const keepRow = () => {
    const view = handle.current, anchor = view && !view.isAtEdge() ? view.getAnchor() : null;
    return anchor ? () => handle.current?.restoreAnchor(anchor.key, anchor.offsetPx) : undefined;
  };
  // Pause, Resume and Cancel act at the revision on screen: a trigger changed meanwhile answers 409 and the list rereads.
  // Edit puts the trigger's own command in the composer (composer.md#commands); the composer previews and applies it.
  const [fill, setFill] = createSignal<{ text: string; seq: number } | undefined>(undefined);
  const editTimer = async (id: string) => {
    const r = await op('session_command_preview', { sessionId: props.id, format: id });
    setEditing(r.trigger ? { id, revision: r.trigger.revision } : null);
    setFill(f => ({ text: r.text, seq: (f?.seq ?? 0) + 1 }));
  };
  const onTimer = (id: string, action: 'pause' | 'edit' | 'cancel', closeSheet: () => void) => {
    const t = timed().find(x => x.id === id);
    if (!t) return;
    setFailure(null);
    if (action === 'edit') closeSheet();
    const act: Promise<unknown> = action === 'edit' ? editTimer(id)
      : action === 'pause' ? op('trigger_save', { id, baseRevision: t.revision, settings: { enabled: !t.settings.enabled } })
      : op('trigger_delete', { id, baseRevision: t.revision });
    act.catch(e => setFailure({ text: errText(e) })).finally(() => { if (action !== 'edit') trigDoc.reload(); });
  };
  // A composer text that starts with /trigger is a command: previewed (session_command_preview) a moment after typing
  // stops and again when a trigger changes, applied on send at the revision Edit opened, so a trigger changed meanwhile
  // shows the conflict, not a save.
  const [draft, setDraft] = createSignal('');
  const isCommand = createMemo(() => commandText(draft()));
  const [asked, setAsked] = createSignal<string | null>(null);
  const [applyError, setApplyError] = createSignal<{ status: number | null; message: string } | null>(null);
  const baseOf = (e: { id: string; revision: number } | null) => (e ? { triggerId: e.id, baseRevision: e.revision } : {});
  createEffect(() => (isCommand() ? draft() : null), text => {
    setApplyError(null);
    if (text === null) { setAsked(null); setEditing(null); return; }
    const t = setTimeout(() => setAsked(text), 300);
    return () => clearTimeout(t);
  });
  const previewDoc = useOp('session_command_preview', () => { const text = asked(); return text === null ? null : { sessionId: props.id, text, ...baseOf(editing()) }; });
  const commandError = (e: unknown) => setApplyError({ status: e instanceof OpError ? e.status : null, message: errText(e) });
  const loadCurrent = () => { const e = untrack(editing); if (e) editTimer(e.id).catch(commandError); };
  const applyCommand = async (text: string) => {
    try {
      await op('session_command', { sessionId: props.id, text, ...baseOf(untrack(editing)) });
    } catch (e) { commandError(e); throw e; }
    setEditing(null); trigDoc.reload();
  };
  const commandAbove = createMemo(() => {
    if (!isCommand()) return undefined;
    const fault = applyError() ?? (previewDoc.error ? { status: previewDoc.status, message: previewDoc.error } : null);
    if (fault?.status === 409) return <CommandPreview kind="conflict" message={fault.message} onLoad={editing() ? loadCurrent : undefined} />;
    if (fault) return <CommandPreview kind="error" message={fault.message} title={fault.status === 400 ? undefined : 'Cannot apply'} />;
    const reply = previewDoc.data;
    if (!reply) return undefined;
    const changes = reply.changes.map(c => ({ label: c.field, before: c.from, after: c.to }));
    return <CommandPreview kind="change" icon="clock" title={reply.title ?? 'Command'} changes={changes} note={changes.length ? undefined : 'Nothing changes'} />;
  });
  // In the sheet the panel has its own Hide; the column is toggled from the shell header only, like the navigation.
  const contextPanel = (where: 'column' | 'sheet', close: () => void) => (
    <ContextPanel loading={!ctx() && !sDoc.error} plan={plan()} timers={timers()} automations={automations()} swarm={swarm()} tasks={tasks()}
      onHide={where === 'sheet' ? close : undefined} onTimer={(id, a) => onTimer(id, a, close)} onOpen={href => { close(); go(href); }} />
  );

  // Files for the next message: uploaded as they are added (picker, paste, drop over the session).
  const taskUpload = createMemo(() => { const k = thread.win?.key; return !!k && 'task' in k; });
  const uploadTarget = createMemo(() => (taskUpload() ? { projectId: s()?.projectId ?? undefined } : { sessionId: props.id }));
  const files = useAttachments(uploadTarget);
  const chips = createMemo<ComposerChip[]>(() => {
    const a = composer()?.agent, sid = targetID();
    const running = view()?.executions.rows.find(e => e.id === active());
    return a && sid ? composerChips(a, running ? { harness: running.harness ?? undefined, model: running.model ?? undefined } : undefined, () => setPanel({ sessionId: sid, profileId: a.profileId, handoff: false })) : [];
  });
  // The draft belongs to the person and the session from the first keystroke, before the target is verified (sending
  // waits for that): a reload or remount restores it, and the mirror-to-server swap never resets the box.
  const draftPersistence = createMemo(() => (access.value.identity ? createDraftPersistence(authorizedDraftScope({ kind: 'session', id: props.id })) : undefined));
  const send = async (text: string) => {
    const sid = untrack(targetID), executionId = untrack(active) ?? null;
    if (!sid || untrack(composerReason)) throw Error('Session target unavailable');
    // Files still uploading finish first; a failed one fails the send and stays on its chip.
    let attachments: string[];
    try { attachments = await files.ready(); } catch (e) { setFailure({ text: `Attachment not uploaded: ${errText(e)}` }); throw e; }
    // One requestId per draft text and files until it is sent (prepareSending): a resend after a failure or a reload is the
    // same write. A message with files is queued while the agent runs; without files it steers the run.
    const dScope = authorizedDraftScope({ kind: 'session', id: props.id });
    const th = untrack(hasThreads) ? untrack(shownThread) : '';
    const body = { sessionId: sid, text, composer: { conversationId: props.id, executionId }, ...(th ? { thread: th } : {}), ...(attachments.length ? { attachments } : executionId ? { delivery: 'steer' } : {}) };
    const request = await prepareSending(dScope, 'message_send', body), sKey = submissionKey(body);
    setFailure(null);
    try {
      await writeOnce('message_send', request, unknown, { signal: alive });
      sendingId(dScope, null, sKey);
      files.clear(attachments);
    } catch (e) {
      if (!(e instanceof AccessChanged) && !(e instanceof OpError && e.message === 'aborted')) {
        if (!unknownOutcome(e)) sendingId(dScope, null, sKey);
        if (e instanceof OpError && e.status === 409) sDoc.reload();
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
              <Avatar {...avatar()} state={avatarState()} tip={bot()?.name} />
              <h2 class="sv-name" data-tip={title()}>{title()}<Show when={hasThreads()}> <span class="sv-crumb-sep" aria-hidden="true">›</span> <span class="sv-crumb" data-tip={shownTitle()}>{shownTitle()}</span></Show></h2>
              <Show when={projectName()}>{n => <ProjectChip name={n()} />}</Show>
              <StateDot state={life()} />
              <Show when={headTimer()} keyed>
                {h => <TimerChip every={scheduleOf(h.t.settings).replace(/^Every /, '')} nextAt={timeOf(h.t.nextAt) ?? undefined} paused={!h.t.settings.enabled} more={h.more || undefined} onOpen={() => setReveal(n => n + 1)} />}
              </Show>
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
                  { label: 'Switch profile or model', icon: 'sliders-horizontal', onClick: () => setPanel({ sessionId: props.id, profileId: s()!.agent.profileId, handoff: false }) },
                  { label: 'Hand off to another agent', icon: 'bot', onClick: () => setPanel({ sessionId: props.id, profileId: s()!.agent.profileId, handoff: true }) },
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
                <KvRow icon="messages-square" label="Session" value={<EntityCode code={s()!.code} id={props.id} kind="conversation" />} />
                <Show when={latest()} keyed>{e => <KvRow icon="activity" label="Latest run" value={<EntityCode id={e.id} kind="execution" />} />}</Show>
                <Show when={detailsDoc.data} keyed fallback={<Show when={detailsDoc.error} fallback={<Skeleton rows={3} />}>{err => <ErrorBanner text={`Details did not load: ${err()}`} onRetry={detailsDoc.reload} />}</Show>}>
                  {d => <>
                    <ScopedRollupBar rollup={d.resources} cost={d.cost} inlineDetails={false} />
                    <ForkDetails session={d} open={openSession} />
                    <Disclosure summary="Related work"><ScopedRollupDetails rollup={d.resources} /></Disclosure>
                  </>}
                </Show>
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
              {a => <SwitchPanel sessionId={a.sessionId} handoff={a.handoff} current={a.profileId ?? undefined} onDone={note => { setPanel(null); if (note) setFailure(null); }} run={run} />}
            </Show>

            {/* The open question, pinned above the transcript (OR-53); a failed run asks to retry (UR-63). */}
            {/* Keyed by question: a live reread updates the card in place (its typed answer and focus stay). */}
            <For each={openQs()} keyed={q => q.id}>
              {oq => <OpenQuestion q={{ ...oq(), options: optionsOf(oq()) }} answers={answers} />}
            </For>
            <Show when={life() === 'failed' && !openQs().length}>
              <QuestionCard tone="danger" question={`The last run failed${runError() ? `: ${runError()}` : ''}`} choices={[{ label: 'Retry', primary: true }]} onAnswer={retryLast} />
            </Show>

            <div class="sv-status" role="status">
              <Show when={failure()} keyed fallback={
                <Show when={view() ? null : sDoc.error} fallback={<Show when={thread.error}>{err => <ErrorBanner text={`Transcript did not load: ${err()}`} onRetry={thread.newest} />}</Show>}>
                  {err => <ErrorBanner text={`Session target did not load: ${err()}`} onRetry={() => sDoc.reload()} />}
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
                      loadToolCall={callId => op('tool_output', { callId }, { signal: alive })}
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
                onSend={text => (commandText(text) ? applyCommand(text) : send(text))}
                fill={fill()}
                onInput={setDraft}
                above={commandAbove()}
                command={isCommand() ? { label: 'Apply' } : undefined}
              />
            </DropZone>
          </div>
          <SidePanel setting="session-context" name="context" label="Session context" onLayout={keepRow} reveal={reveal()}>{contextPanel}</SidePanel>
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

/** When a trigger wakes, in a line: on a change it checks every pass, on request, else its schedule. */
const whenOf = (t: { when: string; everyMs?: number; dailyTime?: string; dailyZone?: string; ifChanged: string[] }, passMs: number) =>
  t.when === 'manual' ? 'on request' : t.ifChanged.length && t.when === 'every' && (t.everyMs ?? 0) <= passMs ? `on ${t.ifChanged.join(', ')}` : scheduleOf(t).toLowerCase();

/** A composer text that is a command (composer.md#commands), not a message. */
const commandText = (text: string) => /^\s*\/trigger(\s|$)/.test(text);

/** A trigger's schedule as the timer row shows it: Every 5m, Daily 09:00 UTC. */
const scheduleOf = (t: { when: string; everyMs?: number; dailyTime?: string; dailyZone?: string }) =>
  t.when === 'every' ? `Every ${every(t.everyMs ?? 0)}` : t.when === 'daily' ? `Daily ${t.dailyTime ?? ''} ${t.dailyZone ?? ''}`.trim() : 'Manual';

/**
 * The composer's facts (controls/composer.md): the agent profile with its model and effort (a click opens Switch profile
 * or model), and the permissions the profile grants, shown only.
 */
function composerChips(a: SessionAgent, active: { harness?: string; model?: string; effort?: string } | undefined, onSwitch: () => void): ComposerChip[] {
  const harness: string | null = a.harness ?? active?.harness ?? null;
  const mark = agentLabel(active?.model ?? a.model ?? undefined, active?.effort ?? a.effort ?? undefined);
  const chips: ComposerChip[] = [{ harness, icon: 'bot', label: mark || a.profileId || 'Agent', tip: `${a.profileId ? `Profile ${a.profileId}` : 'No profile'} · switch profile or model`, onClick: onSwitch }];
  const tools = a.tools, network = a.gitNetwork === true, mode = a.mode ?? null;
  chips.push({
    icon: 'shield',
    label: [mode ?? 'Workspace', network ? 'network' : null].filter(Boolean).join(' · '),
    tip: `Permissions of the profile: ${tools.length ? `tools ${tools.join(', ')}` : 'the harness tools'}${network ? ', git network' : a.gitNetwork === false ? ', no git network' : ', git network as the project grants'}${a.sandbox === false ? ', no sandbox' : ''}`,
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
