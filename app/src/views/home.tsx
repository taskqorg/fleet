// Home (docs/design-system/pages/home.md, OR-50): who works for me and what they need from me. A greeting and the pulse
// line, a composer that picks who to talk to, the active sessions as cards in attention order (Waiting for you, Failed,
// Working, Queued) with their questions answered in place, and the asleep bots with Wake. A quiet Home says what happened
// since the last visit. A card opens the session at /home/<code>. Replaces the Overview (2026-11-25, FLT-48).
// One read, home_view (docs/designs/fleet-data-layer.md): the server counts and groups; only the cards' rows arrive.
import { For, Match, Show, Switch, createEffect, createMemo, createSignal, lazy, onSettled, untrack, type Accessor } from 'solid-js';
import type { JSX } from '@solidjs/web';
import {
  PulseLine, Composer, TargetPicker, SessionCard, SessionCardSkeleton, QuestionCard, OpenQuestion, BotTile, QuietSummary, GroupHeader, Ago,
  OfflineMarker, ErrorBanner, Button, ShellStatus, actor, life, statesOf, states, stamp, day, dayStart, causeOf,
  type Blocker, type PulseProblems, type Target, type Life, type AvatarMark,
  timeOf, plural, WorkFacts, peerFence, Fact,
} from '../../../packages/ui/src/index.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';
import { useLocation, useSearch } from '../router.ts';
import { op, useAlive } from '../api.ts';
import { useAnswers } from '../answers.ts';
import { useOp, useView } from '../store.ts';
import { scopeIds, prefsRead, prefs } from '../prefs.ts';
import type { SessionRow, QuestionRow, HomeBlocker } from '../../../contracts/generated/ops.ts';
import type { BotListItem } from '../../../contracts/generated/ops-bots.ts';
import { botPicture, botColor, reasonLabel } from '../bot-view.ts';

/** Chromium's deferred fetch (Fetch spec, deferred fetching): the browser sends it when the page goes away. Not in TypeScript's DOM lib yet. */
declare function fetchLater(input: string, init?: RequestInit & { activateAfter?: number }): { readonly activated: boolean };

const SessionView = lazy(() => import('./session.tsx'), { export: 'SessionView' });

const TARGET_KEY = 'fleet:home-target';

/** The card groups in attention order: a session is in the group of its state's word (registry `life`, OR-55). */
type Group = 'waiting' | 'failed' | 'working' | 'queued';
// A group's Conversations filter: Queued holds preparing sessions whose run has not started (lifeOfCard); Working also
// holds sessions waiting on other work, not on this person.
const groupStates = (id: Group): string[] =>
  id === 'queued' ? ['preparing'] : id === 'working' ? [...statesOf('session', ['working']), 'waiting'] : statesOf('session', [id]) as string[];
const GROUPS = (['waiting', 'failed', 'working', 'queued'] as const).map(id => ({ id, ...states.life[id], states: groupStates(id) }));
/** Failed sessions that share a plain cause (OR-58): the cause, how many, their hosts. */
type Cause = { id: string; text: string; n: number; hosts: string[] };
/**
 * One card: a session (with its oldest open question), a question no listed session asks (a task's acceptance), or
 * failed sessions of one cause collapsed into one card with `×N`.
 */
type Card = { key: string; group: Group; at: number; s: SessionRow | null; qs: QuestionRow[]; cause?: Cause };

/** Times for sorting and comparing: a missing time is the oldest. */
const ms = (t: string | number | null | undefined) => timeOf(t, 0);
/** A question's first sentence without links, for a card's "now" line and a blocker chip. */
function headline(text: string): string {
  const t = String(text ?? '').replace(/https?:\/\/\S+/g, '').replace(/\s+/g, ' ').trim();
  const m = /^(.+?[.!?])(\s|$)/.exec(t);
  return (m ? m[1] : t).replace(/[.:]\s*$/, '') || 'Question';
}

// Blocker chips (OR-56, OR-58): home_view groups failed runs that need a look by plain cause and host (contracts/
// vocabulary.json `causes`); here they get their words. Problems are the pulse line's own count (pulse-line.md,
// OR-71): quiet with their owner, loud only when stuck or without an owner, so problem blockers are left out.
const FAMILY: Record<string, string> = { bugsink: 'New errors in Bugsink', 'error-spike': 'Errors spiked', 'client-error': 'Browser errors', delivery: 'Delivery got slower', store: 'A store needs a look' };
const clause = (text: string) => (/^[^:.]+/.exec(text)?.[0] ?? text).trim();
function blockers(groups: HomeBlocker[]): Blocker[] {
  return groups.filter(g => g.kind === 'run').map(g => {
    const host = g.hostId ?? '';
    if (g.kind === 'run') {
      const what = g.text ? clause(g.text) : 'A run needs a look';
      return { cause: host && !what.includes(host) ? `${what} on ${host}` : what, n: g.n,
        href: host ? `/live?host=${encodeURIComponent(host)}` : `/live?cause=${encodeURIComponent(g.cause)}` };
    }
    const family = g.cause.split(':')[1] ?? 'unknown';
    return { cause: FAMILY[family] ?? headline(g.text), n: g.n, href: host ? `/live?host=${encodeURIComponent(host)}` : `/live?cause=${encodeURIComponent(family)}` };
  });
}

/** Who a session is: its bot, its project's PM, or the work it does. */
function who(s: SessionRow, bot: BotListItem | undefined): { name: string; avatar: AvatarMark } {
  if (bot) return { name: bot.name, avatar: { letter: bot.name, tone: 'success', picture: botPicture(bot), color: botColor(bot) } };
  if (s.profileId === 'pm') return { name: `PM · ${s.projectId ?? 'Fleet'}`, avatar: { letter: 'P' } };
  return { name: s.title || s.code || 'Session', avatar: s.harness ? { harness: s.harness } : { letter: (s.title || 'S').trim() } };
}
const lifeOfSession = (state: string): Life => life('session', state);
/** A card's life: a preparing session whose run has not started waits for a host, so it is Queued, not Working (live
 * 2026-11-26: 32 working while hosts ran 3 during an update drain). */
const lifeOfCard = (s: { state: string; runStartedAt?: string | null }): Life =>
  s.state === 'preparing' && !s.runStartedAt ? 'queued' : lifeOfSession(s.state);
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

function HomePage() {
  const [, go] = useLocation();
  const search = useSearch();
  const alive = useAlive();
  // ?all: the focus not applied; the header's "need you outside focus" opens it. Ignore is full silence (OR-34).
  const all = createMemo(() => new URLSearchParams(search()).has('all'));
  const scope = createMemo(() => scopeIds(!all()));
  const inScope = (project: string | null | undefined) => {
    const { projectIds, excludeProjectIds } = scope(), p = project ?? '-';
    return projectIds ? projectIds.includes(p) : !excludeProjectIds?.includes(p);
  };

  const me = useOp('me', {});
  // The day's start for "done today", fixed for this visit (a new day reads anew on the next open).
  const today = dayStart(Date.now());
  const view = useView('home_view', () => (prefsRead.value ? { ...scope(), dayStart: today } : null), { quiet: 2000 });
  const projects = useOp('project_list', {});

  // The last visit (home_view counts from it) is recorded when the person leaves Home, so it stays put while Home is
  // open. Leaving for another page records it at once; closing or reloading the tab sends the record the browser holds
  // (fetchLater, sent when the page goes away, bfcache-safe unlike an unload handler).
  onSettled(() => {
    const later = new AbortController();
    try {
      fetchLater('/api/ops/person_visit', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ page: 'home' }), signal: later.signal });
    } catch { /* over the deferred-fetch quota: the in-app leave still records */ }
    return () => { later.abort(); void op('person_visit', { page: 'home' }).catch(() => {}); };
  });
  const since = (): number | null | undefined => (view.data ? view.data.counts.visitAt : undefined);

  const now = () => Date.now();
  const scopedBots = createMemo(() => view.data?.bots ?? []);
  const botById = createMemo(() => new Map(scopedBots().map(b => [b.id, b])));
  const botOf = (s: { botId?: string | null }) => (s.botId ? botById().get(s.botId) : undefined);

  // The newest failed run of each failed session, with its plain cause.
  const failureOf = createMemo(() => new Map((view.data?.failures ?? []).map(f => [f.sessionId, { cause: f.cause, text: f.text, hostId: f.hostId, at: f.at }])));
  // Questions to the person, oldest first (problem questions are blockers): each sits on its session's card.
  const asked = createMemo<QuestionRow[]>(() => view.data?.questions ?? []);
  const cards = createMemo<Card[]>(() => {
    const rows = view.data?.sessions ?? [], byId = new Map(rows.map(s => [s.id, s]));
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
      const l = lifeOfCard(s);
      // Waiting for you holds only a question to this person (home.md); a session waiting on other work (its task, a
      // worker, a review) is in work, so it is Working, and its card still says what it waits for.
      const group = mine.length ? 'waiting' : l === 'waiting' ? 'working' : GROUPS.find(g => g.id === l)?.id;
      if (!group) continue;
      // A bot's failed conversation is that bot's problem (run-failed:<bot>:<cause>), not a card here: Failed lists the
      // person's own sessions; an aggregated failure shows only as the pulse line's chip.
      if (group === 'failed' && s.botId) continue;
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
  // Waiting shows every card: a question no session asks is answered only here. The other groups preview one row of
  // cards and link to their Conversations filter (OR-65).
  const shown = (g: { id: Group; cards: Card[] }) => (g.id === 'waiting' ? g.cards : g.cards.slice(0, cols().cards));
  const loaded = () => !!view.data;
  // A grid that appears later (the first cards, the asleep row) is measured once it is in the page.
  createEffect(() => [loaded(), view.data?.bots] as const, () => { requestAnimationFrame(measure); });
  const quiet = createMemo(() => loaded() && !grouped().some(g => (g.id === 'waiting' || g.id === 'failed') && g.cards.length > 0));

  // Pulse: the Working and Queued groups' card counts (one source with the groups below), done today, then problems.
  const rows = () => view.data?.sessions ?? [];
  const chips = createMemo(() => blockers(view.data?.blockers ?? []));
  const problems = createMemo((): PulseProblems | undefined => {
    const p = view.data?.problems;
    if (!p) return undefined;
    const pills: PulseProblems['pills'] = [];
    if (p.unowned > 0) pills.push({ text: `${p.unowned} ${p.unowned === 1 ? 'problem' : 'problems'} without an owner`, tone: 'danger', href: '/system/problems' });
    if (p.stuck > 0) pills.push({ text: `${p.stuck} stuck${p.firstStuck ? `: ${headline(p.firstStuck.text)}` : ''}`, tone: 'attention', href: p.firstStuck ? `/system/problems/${p.firstStuck.code}` : '/system/problems' });
    return { n: p.open, owner: p.owners.length === 1 ? p.owners[0].name : undefined, href: '/system/problems', pills };
  });
  const stateHref = (l: Life) => formatView({ view: 'conversations', state: GROUPS.find(g => g.id === l)?.states ?? statesOf('session', [l]) });

  // Quiet summary: since the last visit, or today on a first one (home_view counts).
  const quietFacts = createMemo(() => {
    const c = view.data?.counts;
    if (!c) return '';
    return [plural(c.doneSince, 'session') + ' done', plural(c.mergedSince, 'pull request') + ' merged', plural(c.failedSince, 'failure')].join(', ');
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
      r => { if (r.run && r.run.state === 'failed') { const run = r.run; setWaking(m => ({ ...m, [b.id]: { state: 'failed', reason: reasonLabel(run.reason) ?? causeOf(run.failure, run.problem)?.text ?? 'not admitted', at } })); } },
      e => setWaking(m => ({ ...m, [b.id]: { state: 'failed', reason: e instanceof Error ? e.message : String(e), at } })),
    );
  };
  const asleep = createMemo(() => scopedBots().filter(b => !b.activeRunId).sort((a, b) => ms(b.lastRunAt) - ms(a.lastRunAt)));

  // Composer targets: each project's PM, then the bots (target-picker); the last one talked to from Home first.
  const pmSession = (project: string) =>
    rows().filter(s => s.profileId === 'pm' && s.projectId === project && !s.botId).sort((a, b) => ms(b.activityAt) - ms(a.activityAt))[0];
  const targets = createMemo<Target[]>(() => [
    ...(projects.data?.projects ?? []).map(p => String(p.id)).filter(p => inScope(p) && !prefs.value.ignored.includes(p)).map(p => {
      const s = pmSession(p);
      return { id: 'pm:' + p, kind: 'pm' as const, name: `PM · ${p}`, about: s?.now ?? 'Project manager', project: p, avatar: { letter: 'P' }, state: s ? lifeOfSession(s.state) : 'asleep' };
    }),
    ...scopedBots().map(b => ({ id: 'bot:' + b.id, kind: 'bot' as const, name: b.name, about: b.purpose, project: b.projectId, avatar: { letter: b.name, tone: 'success' as const, picture: botPicture(b), color: botColor(b) }, state: (b.activeRunId ? 'working' : 'asleep') as Life })),
  ]);
  const [picked, setPicked] = createSignal<string | null>(typeof localStorage === 'undefined' ? null : localStorage.getItem(TARGET_KEY));
  const target = createMemo(() => targets().find(t => t.id === picked())?.id ?? targets()[0]?.id ?? '');
  const pick = (id: string) => { setPicked(id); try { localStorage.setItem(TARGET_KEY, id); } catch { /* private mode: the pick lasts this visit */ } };
  const [sendError, setSendError] = createSignal<string | null>(null);
  /** Sends the first message and opens that session: a bot's Home (waking it when asleep), or the project's PM session. */
  const send = async (text: string, to = target()) => {
    const t = to;
    setSendError(null);
    try {
      if (t.startsWith('bot:')) {
        const b = scopedBots().find(x => 'bot:' + x.id === t);
        if (!b) throw new Error('that bot is no longer listed');
        // bot_run with the text wakes it with its memory in its conversation without a subject; a busy one queues it.
        const r = await op('bot_run', { botId: b.id, requestId: newRequestId(), text }, { signal: alive });
        go(sessionPath({ id: r.sessionId }));
        return;
      }
      const project = t.slice('pm:'.length), open = pmSession(project);
      if (open) {
        await op('message_send', { sessionId: open.id, text, requestId: newRequestId() }, { signal: alive });
        go(sessionPath(open));
        return;
      }
      // A PM with no open session starts from the project's PM preset, read when the message is sent.
      const preset = (await op('session_presets', { projectId: project }, { signal: alive })).presets.find(p => p.role === 'pm' && !p.unavailable);
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
  const state = createMemo(() => view.state);

  // Work facts (controls/work-facts.md): the time a card waits, is in work or failed, against the cards of its group
  // (peerFence), never a fixed number. No agent mark on Home: the avatar and the name say who.
  const fin = (t: number) => (t > 0 ? t : undefined);
  const waitOf = (c: Card) => fin(c.s ? (c.qs.length ? ms(c.qs[0].askedAt) : ms(c.s.waitingSince)) : ms(c.qs[0]?.askedAt));
  const workOf = (c: Card) => (c.s && (c.s.state === 'running' || c.s.state === 'preparing') ? fin(ms(c.s.runStartedAt)) : undefined);
  const startsOf = (c: Card) => (c.cause ? c.cause.n : c.s?.state === 'failed' ? c.s.failedStarts ?? 0 : 0);
  const fences = createMemo(() => {
    const t = now(), all = cards(), secs = (v: number | undefined) => (v === undefined ? NaN : (t - v) / 1000);
    return {
      wait: peerFence(all.filter(c => c.group === 'waiting').map(c => secs(waitOf(c)))),
      work: peerFence(all.map(c => secs(workOf(c))).filter(Number.isFinite)),
      starts: peerFence(all.filter(c => c.group === 'failed').map(startsOf)),
    };
  });
  const cardFacts = (c: Card) => {
    const wait = c.group === 'waiting' ? waitOf(c) : undefined, work = workOf(c), n = startsOf(c);
    const activity = c.s ? fin(ms(c.s.activityAt)) : undefined;
    const last = c.cause ? c.at : c.s?.state === 'failed' ? fin(ms(c.s.lastStartedAt)) : undefined;
    // A card with no time and no repeats has no work facts: no empty meta line.
    if (n < 2 && wait === undefined && work === undefined && last === undefined && activity === undefined) return undefined;
    return (
      <WorkFacts
        attempts={n > 1 ? n : undefined}
        attemptsFence={fences().starts}
        attemptsTip={c.cause ? `${n} sessions failed this way` : `${n} failed starts`}
        waitAt={wait}
        waitFence={fences().wait}
        workAt={wait === undefined ? work : undefined}
        workFence={fences().work}
        lastAt={wait === undefined && work === undefined ? last : undefined}
        lastLabel={c.cause ? 'Latest failure' : 'Latest failed start'}
        time={wait === undefined && work === undefined && last === undefined && activity !== undefined ? <Fact tip="Last activity"><Ago t={activity} /></Fact> : undefined}
      />
    );
  };

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
          meta={f().hosts.length ? [f().hosts.join(', ')] : []}
          facts={cardFacts(c())}
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
          facts={cardFacts(c())}
          question={question()}
          onOpen={() => {
            const y = x();
            if (y.taskId) go(`/tasks/${encodeURIComponent(y.taskId)}`);
            else if (y.sessionId) go(sessionPath({ id: y.sessionId }));
          }}
        />
      );
    }
    const s = () => c().s!, w = createMemo(() => who(s(), botOf(s())));
    return (
      <SessionCard
        name={w().name}
        project={s().projectId ?? undefined}
        avatar={w().avatar}
        state={c().qs.length && s().state !== 'failed' ? 'waiting' : lifeOfCard(s())}
        now={(s().state === 'failed' ? failureOf().get(s().id)?.text : undefined) || s().now || (w().name === s().title ? 'No message yet' : s().title || 'No message yet')}
        facts={cardFacts(c())}
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
          working={grouped().find(g => g.id === 'working')?.cards.length ?? 0}
          queued={grouped().find(g => g.id === 'queued')?.cards.length ?? 0}
          done={view.data?.counts.doneToday ?? 0}
          blockers={chips()}
          problems={problems()}
          countHref={stateHref}
          liveHref="/live"
        />
      </header>
      <Composer
        placeholder="Talk to anyone…"
        draftKey="home:talk"
        onSend={text => send(text)}
        disabledReason={!targets().length ? 'Nobody to talk to yet' : undefined}
        target={<TargetPicker targets={targets()} value={target()} onPick={pick} onNew={() => go('/conversations/new')} />}
      />
      <Show when={sendError()}>{e => <ErrorBanner text={e()} />}</Show>
      <Switch>
        <Match when={view.error && !view.data}>
          <ErrorBanner text={`Home did not load: ${view.error}`} onRetry={() => view.reload()} />
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
                      {/* A preview, no toggle (OR-65): the rest is in Conversations under the group's filter. */}
                      <a class="home-more" href={formatView({ view: 'conversations', state: g.states })}
                        onClick={e => { e.preventDefault(); go(formatView({ view: 'conversations', state: g.states })); }}>
                        All {g.cards.length} in Conversations ›
                      </a>
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
                    onOpen={b().conversationId ? () => go(sessionPath({ id: b().conversationId! })) : undefined}
                  />
                )}
              </For>
            </div>
            <Show when={asleep().length > cols().tiles}>
              <a class="home-more" href="/bots" onClick={e => { e.preventDefault(); go('/bots'); }}>All {asleep().length} bots ›</a>
            </Show>
          </Show>
        </section>
      </Show>
    </article>
  );
}
