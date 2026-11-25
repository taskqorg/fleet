// Home (docs/design-system/pages/home.md, OR-50): who works for me and what they need from me. A greeting and the pulse
// line, a composer that picks who to talk to, the active sessions as cards in attention order (Waiting for you, Failed,
// Working, Queued) with their questions answered in place, and the asleep bots with Wake. A quiet Home says what happened
// since the last visit. A card opens the session at /home/<code>. Replaces the Overview (2026-11-25, FLT-48).
import { For, Match, Show, Switch, createEffect, createMemo, createSignal, lazy, onSettled, untrack, type Accessor } from 'solid-js';
import type { JSX } from '@solidjs/web';
import {
  PulseLine, Composer, TargetPicker, SessionCard, SessionCardSkeleton, QuestionCard, OpenQuestion, BotTile, QuietSummary, GroupHeader, Ago,
  OfflineMarker, ErrorBanner, Button, ShellStatus, actor, lifeOf, stamp, day, dayStart, causeOf,
  type Blocker, type Target, type Life, type AvatarMark, type IconName, type Tone,
} from '../../../packages/ui/src/index.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';
import { useLocation, useSearch } from '../router.ts';
import { op, useAlive } from '../api.ts';
import { useAnswers } from '../answers.ts';
import { useOp } from '../store.ts';
import { scopeIds, noProject, prefsRead, prefs } from '../prefs.ts';
import type { SessionRow, QuestionRow, ExecutionRow } from '../../../contracts/generated/ops.ts';
import type { BotListItem } from '../../../contracts/generated/ops-bots.ts';
import { botPicture, reasonLabel } from '../bot-view.ts';

const SessionView = lazy(() => import('./session.tsx'), { export: 'SessionView' });

/** Session states Home shows; idle sessions only feed the composer's targets. */
const ACTIVE = ['waiting', 'failed', 'running', 'preparing', 'idle'];
const TARGET_KEY = 'fleet:home-target';

type Group = 'waiting' | 'failed' | 'working' | 'queued';
const GROUPS: { id: Group; label: string; icon: IconName; tone: Tone; states: string[] }[] = [
  { id: 'waiting', label: 'Waiting for you', icon: 'bell', tone: 'attention', states: ['waiting'] },
  { id: 'failed', label: 'Failed', icon: 'octagon-x', tone: 'danger', states: ['failed'] },
  { id: 'working', label: 'Working', icon: 'activity', tone: 'running', states: ['running'] },
  { id: 'queued', label: 'Queued', icon: 'list-ordered', tone: 'neutral', states: ['preparing'] },
];
/** Failed sessions that share a plain cause (OR-58): the cause, how many, their hosts. */
type Cause = { id: string; text: string; n: number; hosts: string[] };
/**
 * One card: a session (with its oldest open question), a question no listed session asks (a task's acceptance), or
 * failed sessions of one cause collapsed into one card with `×N`.
 */
type Card = { key: string; group: Group; at: number; s: SessionRow | null; qs: QuestionRow[]; cause?: Cause };

const ms = (t: string | number | null | undefined) => (typeof t === 'number' ? t : t ? Date.parse(t) || 0 : 0);
const isProblem = (q: QuestionRow) => typeof q.cause === 'string' && q.cause.startsWith('problem:');
/** A question's first sentence without links, for a card's "now" line and a blocker chip. */
function headline(text: string): string {
  const t = String(text ?? '').replace(/https?:\/\/\S+/g, '').replace(/\s+/g, ' ').trim();
  const m = /^(.+?[.!?])(\s|$)/.exec(t);
  return (m ? m[1] : t).replace(/[.:]\s*$/, '') || 'Question';
}

// Blocker chips (OR-56, OR-58): failed runs that need a look, grouped by plain cause and host (contracts/vocabulary.json
// `causes`, served as `failure`), and the coordinator's problem questions grouped by what they are about.
const FAMILY: Record<string, string> = { bugsink: 'New errors in Bugsink', 'error-spike': 'Errors spiked', 'client-error': 'Browser errors', delivery: 'Delivery got slower', store: 'A store needs a look' };
const clause = (text: string) => (/^[^:.]+/.exec(text)?.[0] ?? text).trim();
function blockers(problems: ExecutionRow[], questions: QuestionRow[]): Blocker[] {
  const by = new Map<string, Blocker & { at: number }>();
  const add = (key: string, cause: string, href: string, at: number) => {
    const b = by.get(key);
    if (b) { b.n++; b.at = Math.max(b.at, at); } else by.set(key, { cause, n: 1, href, at });
  };
  for (const e of problems) {
    const f = causeOf(e.failure, e.problem), host = e.hostId ?? '';
    const what = f ? clause(f.text) : 'A run needs a look';
    add(`run:${f?.id ?? 'run'}:${host}`, host && !what.includes(host) ? `${what} on ${host}` : what,
      host ? `/live?host=${encodeURIComponent(host)}` : `/live?cause=${encodeURIComponent(f?.id ?? 'run')}`, ms(e.lastSeenAt ?? e.startedAt));
  }
  for (const q of questions) {
    const parts = String(q.cause).split(':'), family = parts[1] ?? 'unknown';
    // A host problem names its host and kind (problem:host:owner-mac:saturated); the others group by family.
    const key = family === 'host' ? parts.slice(0, 4).join(':') : `problem:${family}`;
    const href = family === 'host' && parts[2] ? `/live?host=${encodeURIComponent(parts[2])}` : `/live?cause=${encodeURIComponent(family)}`;
    add(key, FAMILY[family] ?? headline(q.text), href, ms(q.askedAt));
  }
  return [...by.values()].sort((a, b) => b.at - a.at).map(({ at: _, ...b }) => b);
}

/** Who a session is: its bot, its project's PM, or the work it does. */
function who(s: SessionRow, bot: BotListItem | undefined): { name: string; avatar: AvatarMark } {
  if (bot) return { name: bot.name, avatar: { letter: bot.name, tone: 'success', picture: botPicture(bot) } };
  if (s.profileId === 'pm') return { name: `PM · ${s.projectId ?? 'Fleet'}`, avatar: { letter: 'P' } };
  return { name: s.title || s.code || 'Session', avatar: s.harness ? { harness: s.harness } : { letter: (s.title || 'S').trim() } };
}
// A preparing session waits for a slot: Home shows it as Queued (pages/home.md), where the registry's session map says Working.
const lifeOfSession = (state: string): Life => (state === 'preparing' ? 'queued' : (lifeOf.session as Record<string, Life>)[state] ?? 'working');
/** The address of a session on Home: its human code (OR-38), else its id. */
const sessionPath = (s: { id: string; code?: string | null }) => `/home/${encodeURIComponent(s.code ?? s.id)}`;
const newRequestId = () => crypto.randomUUID();

export function Home(props: { id?: string }) {
  return (
    <Show when={props.id} keyed fallback={<HomePage />}>
      {id => <HomeSession code={id} />}
    </Show>
  );
}

/** The session a card opens, at /home/<code>: the one session view (OR-51), full width with Home highlighted. */
function HomeSession(props: { code: string }) {
  return <SessionView frame="home" id={props.code} />;
}

function greeting(now: Date) {
  const h = now.getHours();
  return h < 5 ? 'Good night' : h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening';
}
/** The span a quiet summary covers, in words: "Overnight", "Since 14:20", "Since 12 Nov", or "Today" on a first visit. */
function spanOf(since: number | null, now: Date): string {
  if (!since) return 'Today';
  const midnight = new Date(now); midnight.setHours(0, 0, 0, 0);
  if (since < midnight.getTime() && now.getHours() < 12 && midnight.getTime() - since < 18 * 3600_000) return 'Overnight';
  // OR-2 formats: the time today ("14:20", the end of a stamp), else the day ("12 Nov").
  if (since >= midnight.getTime()) return `Since ${stamp(since, now.getTime()).slice(-5)}`;
  return `Since ${day(since, now.getTime())}`;
}
const plural = (n: number, one: string, many = one + 's') => `${n.toLocaleString('en')} ${n === 1 ? one : many}`;

function HomePage() {
  const [, go] = useLocation();
  const search = useSearch();
  const alive = useAlive();
  // ?all: the focus not applied; the header's "need you outside focus" opens it. Ignore is full silence (OR-34).
  const all = createMemo(() => new URLSearchParams(search()).has('all'));
  const scope = createMemo(() => scopeIds(!all()));
  const listScope = createMemo(() => {
    const { projectIds, excludeProjectIds } = scope();
    return { ...(projectIds && { projectId: projectIds.map(noProject) }), ...(excludeProjectIds && { excludeProjectId: excludeProjectIds.map(noProject) }) };
  });
  const inScope = (project: string | null | undefined) => {
    const { projectIds, excludeProjectIds } = scope(), p = project ?? '-';
    return projectIds ? projectIds.includes(p) : !excludeProjectIds?.includes(p);
  };

  const me = useOp('me', {});
  const active = useOp('sessions_list', () => (prefsRead.value ? { filter: { state: ACTIVE, ...listScope() }, sort: '-activity', limit: 200 } : null));
  const finished = useOp('sessions_list', () => (prefsRead.value ? { filter: { state: ['succeeded'], ...listScope() }, sort: '-activity', limit: 200 } : null), { quiet: 30_000 });
  const questions = useOp('questions_list', () => (prefsRead.value ? { state: 'open', ...scope() } : null));
  const problems = useOp('executions_list', () => (prefsRead.value ? { needsYou: true, ...scope() } : null), { quiet: 30_000 });
  // Failed runs with their plain cause (failure, contracts/vocabulary.json): the newest one of each failed session names
  // the cause its card collapses by.
  const failedRuns = useOp('executions_list', () => (prefsRead.value ? { problem: true, limit: 500, ...scope() } : null), { quiet: 30_000 });
  const bots = useOp('bot_list', {});
  const projects = useOp('project_list', {});
  const delivery = useOp('publishing_status', () => (prefsRead.value ? scope() : null), { every: 300_000 });

  // The visit before this one (person_visit records this one). Read once per mount: a reread would move it to now.
  const [since, setSince] = createSignal<number | null | undefined>(undefined);
  onSettled(() => {
    op('person_visit', { page: 'home' }, { signal: alive }).then(r => setSince(r.previousAt ?? null), () => setSince(null));
  });

  const now = () => Date.now();
  const botBySession = createMemo(() => new Map((bots.data?.bots ?? []).filter(b => b.homeSessionId).map(b => [b.homeSessionId!, b])));
  const scopedBots = createMemo(() => (bots.data?.bots ?? []).filter(b => inScope(b.projectId)));

  // The newest failed run of each session, with its plain cause.
  const failureOf = createMemo(() => {
    const by = new Map<string, { cause: string; text: string; hostId: string | null; at: number }>();
    for (const e of failedRuns.data?.executions ?? []) {
      if (!e.sessionId || !e.failure) continue;
      const at = ms(e.endedAt ?? e.lastSeenAt ?? e.startedAt), had = by.get(e.sessionId);
      if (!had || at > had.at) by.set(e.sessionId, { cause: e.failure.cause, text: e.failure.text, hostId: e.hostId, at });
    }
    return by;
  });
  // Questions to the person: a problem is a blocker chip; any other one sits on its session's card, oldest first.
  const asked = createMemo(() => (questions.data?.questions ?? []).filter(q => !isProblem(q)).sort((a, b) => ms(a.askedAt) - ms(b.askedAt)));
  const cards = createMemo<Card[]>(() => {
    const rows = active.data?.sessions ?? [], byId = new Map(rows.map(s => [s.id, s]));
    const qs = new Map<string, QuestionRow[]>();
    const loose: Card[] = [];
    for (const q of asked()) {
      if (q.sessionId && byId.has(q.sessionId)) qs.set(q.sessionId, [...(qs.get(q.sessionId) ?? []), q]);
      else loose.push({ key: 'q:' + q.id, group: 'waiting', at: ms(q.askedAt), s: null, qs: [q] });
    }
    const out: Card[] = [...loose];
    const failedBy = new Map<string, Card[]>();
    for (const s of rows) {
      const mine = qs.get(s.id) ?? [];
      const group = mine.length || s.state === 'waiting' ? 'waiting' : GROUPS.find(g => g.states.includes(s.state))?.id;
      if (!group) continue;
      const card: Card = { key: s.id, group, at: group === 'waiting' && mine.length ? ms(mine[0].askedAt) : ms(s.activityAt), s, qs: mine };
      const f = group === 'failed' ? failureOf().get(s.id) : undefined;
      if (f) failedBy.set(f.cause, [...(failedBy.get(f.cause) ?? []), card]);
      else out.push(card);
    }
    // Repeats aggregate (OR-58): failed sessions of one known cause are one card with ×N; a lone one stays itself.
    for (const [id, xs] of failedBy) {
      if (xs.length === 1) { out.push(xs[0]); continue; }
      const f = failureOf().get(xs[0].s!.id)!;
      const hosts = [...new Set(xs.map(c => failureOf().get(c.s!.id)?.hostId).filter((h): h is string => !!h))];
      out.push({ key: 'cause:' + id, group: 'failed', at: Math.max(...xs.map(c => c.at)), s: null, qs: [], cause: { id, text: f.text, n: xs.length, hosts } });
    }
    return out;
  });
  const grouped = createMemo(() =>
    GROUPS.map(g => {
      const xs = cards().filter(c => c.group === g.id);
      // Waiting: the oldest question first; the others: the most recent activity first.
      xs.sort((a, b) => (g.id === 'waiting' ? a.at - b.at : b.at - a.at));
      return { ...g, cards: xs };
    }),
  );
  const [closed, setClosed] = createSignal<(Group | 'asleep')[]>([]);
  // One row per group (OR-30): as many cards (or tiles) as the grid's columns hold at this width, then +K. The grids
  // lay out with auto-fill, so their column count is the row's capacity whatever the number of items.
  let page: HTMLElement | undefined;
  const [cols, setCols] = createSignal({ cards: 3, tiles: 4 });
  const measure = () => {
    const count = (sel: string) => { const g = page?.querySelector(sel); return g ? getComputedStyle(g).gridTemplateColumns.split(' ').filter(Boolean).length : 0; };
    const next = { cards: count('.home-cards') || untrack(cols).cards, tiles: count('.home-tiles') || untrack(cols).tiles };
    if (next.cards !== untrack(cols).cards || next.tiles !== untrack(cols).tiles) setCols(next);
  };
  onSettled(() => {
    const ro = new ResizeObserver(measure);
    if (page) ro.observe(page);
    return () => ro.disconnect();
  });
  // Waiting opened past its row by its +K (OR-30 expander); the other groups stay one row.
  const [openWaiting, setOpenWaiting] = createSignal(false);
  const shown = (g: { id: Group; cards: Card[] }) => (g.id === 'waiting' && openWaiting() ? g.cards : g.cards.slice(0, cols().cards));
  const loaded = () => !!active.data && !!questions.data;
  // A grid that appears later (the first cards, the asleep row) is measured once it is in the page.
  createEffect(() => [loaded(), bots.data] as const, () => { requestAnimationFrame(measure); });
  const quiet = createMemo(() => loaded() && !grouped().some(g => (g.id === 'waiting' || g.id === 'failed') && g.cards.length > 0));

  // Pulse: working and queued sessions, sessions done today, then blockers.
  const today = () => dayStart(Date.now());
  const rows = () => active.data?.sessions ?? [];
  // At most one page (200 rows, newest first): a day with more finished sessions shows 200.
  const doneSince = (t: number) => (finished.data?.sessions ?? []).filter(s => ms(s.activityAt) >= t).length;
  const chips = createMemo(() => blockers(problems.data?.executions ?? [], (questions.data?.questions ?? []).filter(isProblem)));
  const stateHref = (life: Life) => formatView({ view: 'conversations', state: life === 'working' ? ['running'] : life === 'queued' ? ['preparing'] : ['succeeded'] });

  // Quiet summary: since the last visit, or today on a first one.
  const quietFacts = createMemo(() => {
    const s = since();
    const from = s ?? today();
    const done = doneSince(from);
    const merged = (delivery.data?.items ?? []).filter(i => i.merged && ms(i.merged) >= from).length;
    // Failed runs that still need a look; failed sessions would show as cards, so a quiet Home has none of those.
    const failures = (problems.data?.executions ?? []).filter(e => e.state === 'failed' && ms(e.lastSeenAt ?? e.startedAt) >= from).length;
    return [plural(done, 'session') + ' done', plural(merged, 'pull request') + ' merged', plural(failures, 'failure')].join(', ');
  });
  // The bot that keeps watch: one running now (the most recent). An asleep bot watches nothing, so none is named.
  const watcher = createMemo(() => scopedBots().filter(b => b.activeRunId).sort((a, b) => ms(b.lastRunAt) - ms(a.lastRunAt))[0]);

  // Answers in place (OR-53, OpenQuestion): the chosen label is busy until the write returns; then the card shows the
  // answered line until the question leaves the read, and the next open question of that session takes its place.
  const answers = useAnswers();

  // Wake: busy until the bot's run shows in the read; a refusal shows its reason and Retry.
  const [waking, setWaking] = createSignal<Record<string, { state: 'waking' | 'failed'; reason?: string; at: number }>>({});
  // A wake shows until the bot's run appears (a newer lastRunAt): then the tile leaves and comes back asleep later.
  const wakeOf = (b: BotListItem) => { const w = waking()[b.id]; return w && !(w.state === 'waking' && ms(b.lastRunAt) >= w.at) ? w : undefined; };
  const wake = (b: BotListItem) => {
    const at = Date.now() - 5_000;
    setWaking(m => ({ ...m, [b.id]: { state: 'waking', at } }));
    op('bot_run', { botId: b.id, requestId: newRequestId() }, { signal: alive }).then(
      r => { if (!r.admitted) setWaking(m => ({ ...m, [b.id]: { state: 'failed', reason: reasonLabel(r.run.reason) ?? causeOf(r.run.failure, r.run.problem)?.text ?? 'not admitted', at } })); },
      e => setWaking(m => ({ ...m, [b.id]: { state: 'failed', reason: e instanceof Error ? e.message : String(e), at } })),
    );
  };
  const asleep = createMemo(() => scopedBots().filter(b => !b.activeRunId).sort((a, b) => ms(b.lastRunAt) - ms(a.lastRunAt)));

  // Composer targets: each project's PM, then the bots (target-picker); the last one talked to from Home first.
  const pmSession = (project: string) =>
    rows().filter(s => s.profileId === 'pm' && s.projectId === project && !botBySession().has(s.id)).sort((a, b) => ms(b.activityAt) - ms(a.activityAt))[0];
  const targets = createMemo<Target[]>(() => [
    ...(projects.data?.projects ?? []).map(p => String(p.id)).filter(p => inScope(p) && !prefs.value.ignored.includes(p)).map(p => {
      const s = pmSession(p);
      return { id: 'pm:' + p, kind: 'pm' as const, name: `PM · ${p}`, about: s?.now ?? 'Project manager', project: p, avatar: { letter: 'P' }, state: s ? lifeOfSession(s.state) : 'asleep' };
    }),
    ...scopedBots().map(b => ({ id: 'bot:' + b.id, kind: 'bot' as const, name: b.name, about: b.purpose, project: b.projectId, avatar: { letter: b.name, tone: 'success' as const, picture: botPicture(b) }, state: (b.activeRunId ? 'working' : 'asleep') as Life })),
  ]);
  const [picked, setPicked] = createSignal<string | null>(typeof localStorage === 'undefined' ? null : localStorage.getItem(TARGET_KEY));
  const target = createMemo(() => targets().find(t => t.id === picked())?.id ?? targets()[0]?.id ?? '');
  const pick = (id: string) => { setPicked(id); try { localStorage.setItem(TARGET_KEY, id); } catch { /* private mode: the pick lasts this visit */ } };
  // The picked PM's project presets, read ahead: a PM with no open session starts from the project's PM preset.
  const presets = useOp('session_presets', () => (target().startsWith('pm:') && !pmSession(target().slice(3)) ? { projectId: target().slice(3) } : null));
  const [sendError, setSendError] = createSignal<string | null>(null);
  /** Sends the first message and opens that session: a bot's Home (waking it when asleep), or the project's PM session. */
  const send = async (text: string, to = target()) => {
    const t = to;
    setSendError(null);
    try {
      if (t.startsWith('bot:')) {
        const b = scopedBots().find(x => 'bot:' + x.id === t);
        if (!b) throw new Error('that bot is no longer listed');
        // Asleep: bot_run with the text wakes it with its memory. Running: the message joins its Home conversation.
        if (!b.activeRunId || !b.homeSessionId) {
          const r = await op('bot_run', { botId: b.id, requestId: newRequestId(), text }, { signal: alive });
          if (!r.admitted) throw new Error(r.run.reason ?? 'the bot did not take the message');
          go(sessionPath({ id: r.run.sessionId ?? b.homeSessionId ?? '' }));
        } else {
          await op('message_send', { sessionId: b.homeSessionId, text, requestId: newRequestId() }, { signal: alive });
          go(sessionPath({ id: b.homeSessionId }));
        }
        return;
      }
      const project = t.slice('pm:'.length), open = pmSession(project);
      if (open) {
        await op('message_send', { sessionId: open.id, text, requestId: newRequestId() }, { signal: alive });
        go(sessionPath(open));
        return;
      }
      const preset = presets.data?.presets.find(p => p.role === 'pm' && !p.unavailable);
      const r = await op('session_start', { projectId: project, requestId: newRequestId(), text, ...(preset ? { preset: preset.id } : { role: 'pm' }) }, { signal: alive });
      go(sessionPath({ id: r.sessionId }));
    } catch (e) {
      setSendError(`Not sent: ${e instanceof Error ? e.message : String(e)}`);
      throw e;
    }
  };
  const askWatcher = () => {
    const b = watcher(), span = spanOf(since() ?? null, new Date()).toLowerCase();
    if (!b) return;
    pick('bot:' + b.id);
    void send(`What happened ${span}?`, 'bot:' + b.id).catch(() => {});
  };

  const name = createMemo(() => (me.data?.name ?? '').split(/\s+/)[0]);
  const state = createMemo(() => [active.state, questions.state].find(s => s !== 'current') ?? 'current');

  /** One card; `c` follows its row as reads update, so a typed answer survives them (keyed by session or question). */
  const renderCard = (c: Accessor<Card>): JSX.Element => {
    const q = createMemo(() => c().qs.find(x => !answers.answered(x.id)) ?? null);
    const lastAnswered = createMemo(() => c().qs.map(x => answers.answered(x.id)).filter(Boolean).at(-1));
    const question = () => {
      const x = q();
      if (!x) {
        const a = lastAnswered();
        return a ? <QuestionCard question="" choices={[]} answered={a} /> : undefined;
      }
      return <OpenQuestion q={x} answers={answers} />;
    };
    if (untrack(c).cause) {
      // Failed sessions of one cause (OR-58): the cause in words, how many, where; it opens Conversations' Failed list.
      const f = () => c().cause!;
      return (
        <SessionCard
          name={clause(f().text)}
          avatar={{ letter: '!', tone: 'danger' }}
          state="failed"
          now={f().text}
          meta={[<b class="num">×{f().n}</b>, ...(f().hosts.length ? [f().hosts.join(', ')] : []), <Ago t={c().at} />]}
          onOpen={() => go(formatView({ view: 'conversations', state: ['failed'] }))}
        />
      );
    }
    if (!untrack(c).s) {
      // A question no active session asks: the task it is about, or the asker.
      const x = () => c().qs[0];
      return (
        <SessionCard
          name={x().kind === 'acceptance' ? 'Acceptance' : actor(x().askedBy) || 'Question'}
          project={x().projectId ?? undefined}
          avatar={{ letter: x().kind === 'acceptance' ? 'A' : 'Q', tone: 'attention' }}
          state="waiting"
          now={x().kind === 'acceptance' ? 'Asks you to accept a result.' : 'Asks you to decide.'}
          meta={[<Ago t={ms(x().askedAt)} />]}
          question={question()}
          onOpen={() => {
            const y = x();
            if (y.taskId) go(`/tasks/${encodeURIComponent(y.taskId)}`);
            else if (y.sessionId) go(sessionPath({ id: y.sessionId }));
          }}
        />
      );
    }
    const s = () => c().s!, w = createMemo(() => who(s(), botBySession().get(s().id)));
    return (
      <SessionCard
        name={w().name}
        project={s().projectId ?? undefined}
        avatar={w().avatar}
        state={c().qs.length && s().state !== 'failed' ? 'waiting' : lifeOfSession(s().state)}
        now={(s().state === 'failed' ? failureOf().get(s().id)?.text : undefined) || s().now || (w().name === s().title ? 'No message yet' : s().title || 'No message yet')}
        meta={s().activityAt ? [<Ago t={ms(s().activityAt)} />] : []}
        question={question()}
        onOpen={() => go(sessionPath(s()))}
      />
    );
  };

  return (
    <article ref={el => { page = el; }} class="detail home" data-ui="home" aria-label="Home">
      <ShellStatus><OfflineMarker state={state()} now={now()} /></ShellStatus>
      <header class="home-head">
        <h2 class="home-hello">{greeting(new Date())}{name() ? `, ${name()}` : ''}</h2>
        <Show when={all()}>
          <span class="muted">Focus not applied</span>
          <Button label="Apply the focus" icon="focus" iconOnly kind="quiet" onClick={() => go('/overview')} />
        </Show>
        <PulseLine
          working={rows().filter(s => s.state === 'running').length}
          queued={rows().filter(s => s.state === 'preparing').length}
          done={doneSince(today())}
          blockers={chips()}
          countHref={stateHref}
          liveHref="/live"
        />
      </header>
      <Composer
        placeholder="Talk to anyone…"
        onSend={text => send(text)}
        disabledReason={!targets().length ? 'Nobody to talk to yet' : undefined}
        target={<TargetPicker targets={targets()} value={target()} onPick={pick} onNew={() => go('/conversations/new')} />}
      />
      <Show when={sendError()}>{e => <ErrorBanner text={e()} />}</Show>
      <Switch>
        <Match when={active.error && !active.data}>
          <ErrorBanner text={`Sessions did not load: ${active.error}`} onRetry={() => active.reload()} />
        </Match>
        <Match when={!loaded()}>
          <section class="home-group" aria-busy="true" aria-label="Loading sessions">
            <div class="home-cards"><SessionCardSkeleton /><SessionCardSkeleton /><SessionCardSkeleton /></div>
          </section>
        </Match>
        <Match when={true}>
          <Show when={quiet()}>
            <QuietSummary
              span={spanOf(since() ?? null, new Date())}
              facts={quietFacts()}
              watcher={watcher()?.name}
              loading={since() === undefined}
              onAsk={askWatcher}
              onLive={() => go('/live')}
            />
          </Show>
          <For each={grouped()}>
            {g => (
              <Show when={g.cards.length > 0}>
                <section class="home-group" aria-label={g.label}>
                  <GroupHeader icon={g.icon} tone={g.tone} label={g.label} count={g.cards.length} open={!closed().includes(g.id)}
                    onToggle={() => setClosed(c => (c.includes(g.id) ? c.filter(x => x !== g.id) : [...c, g.id]))} />
                  <Show when={!closed().includes(g.id)}>
                    <div class="home-cards">
                      <For each={shown(g)} keyed={c => c.key}>{c => renderCard(c)}</For>
                    </div>
                    <Show when={g.cards.length > shown(g).length}>
                      {/* Waiting expands in place: a question no session asks is answered only here, so its +K never
                          leaves Home. The other groups link to their Conversations filter. */}
                      <Show when={g.id === 'waiting'} fallback={
                        <a class="home-more" href={formatView({ view: 'conversations', state: g.states })}
                          onClick={e => { e.preventDefault(); go(formatView({ view: 'conversations', state: g.states })); }}>
                          +{g.cards.length - cols().cards} · Conversations ›
                        </a>
                      }>
                        <Button label={`+${g.cards.length - cols().cards} more`} kind="quiet" onClick={() => setOpenWaiting(true)} />
                      </Show>
                    </Show>
                  </Show>
                </section>
              </Show>
            )}
          </For>
        </Match>
      </Switch>
      <Show when={asleep().length > 0}>
        <section class="home-group" aria-label="Asleep">
          <GroupHeader icon="bot" tone="neutral" label="Asleep" count={asleep().length} open={!closed().includes('asleep')}
            onToggle={() => setClosed(c => (c.includes('asleep') ? c.filter(x => x !== 'asleep') : [...c, 'asleep']))} />
          <Show when={!closed().includes('asleep')}>
            <div class="home-tiles">
              <For each={asleep().slice(0, cols().tiles)} keyed={b => b.id}>
                {b => (
                  <BotTile
                    name={b().name}
                    about={b().purpose}
                    avatar={{ letter: b().name, tone: 'success', picture: botPicture(b()) }}
                    lastUsed={b().lastRunAt ? ms(b().lastRunAt) : undefined}
                    state={wakeOf(b())?.state ?? 'asleep'}
                    reason={wakeOf(b())?.reason}
                    onWake={() => wake(b())}
                    onOpen={b().homeSessionId ? () => go(sessionPath({ id: b().homeSessionId! })) : undefined}
                  />
                )}
              </For>
            </div>
            <Show when={asleep().length > cols().tiles}>
              <a class="home-more" href="/bots" onClick={e => { e.preventDefault(); go('/bots'); }}>+{asleep().length - cols().tiles} · All bots ›</a>
            </Show>
          </Show>
        </section>
      </Show>
    </article>
  );
}
