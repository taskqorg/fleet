// Live (docs/design-system/pages/live.md): what runs where right now, and what is broken. Status bar, six figures,
// one host band per host with its worker slots and 24-hour run strip, and the side column (projects, delivery, CI
// runners, just now). Wall mode (`/live?wall`) shows the same page with nothing to open. Reads: hosts_list (bands,
// gauges, slots in start order), executions_runners (strips, figures, slot details), executions_list (failures in
// plain words), task_page (task codes and projects, done today), delivery_status, executions_ci, project_list.
import { createMemo, For, Show } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { useLocation } from '../router.ts';
import {
  Button, Empty, Icon, ErrorBanner, FigureTile, HostBand, LiveRow, StatusPill, WorkFacts, bytes, causeOf, clock, count, duration, lifeOf, money, states, peerFence, useTick,
  type DataTone, type Life, type GaugeProps, type HostBandProps, type Measured, type Tone, type WorkerSlotProps,
  statesOf, openStates,
  timeOf, usdOf,
} from '../../../packages/ui/src/index.ts';
import { useOp } from '../store.ts';
import { prefs, scopeIds, noProject } from '../prefs.ts';
import { HistoryRows } from './history.tsx';
import { useClockOffset } from './hosts.tsx';
import { firstSentence, hostRecoveredAt } from './system-idle.tsx';
import { CHIP_STAGE } from './delivery-model.tsx';
import type { CostFigure, DeliveryStage, HostRow } from '../../../contracts/generated/ops.ts';

const MIN = 60_000, HOUR = 3_600_000, DAY = 24 * HOUR;
/** Gauge thresholds in percent until hosts carry their own (host-band.md). */
const WARN = 80, CRIT = 95;
/** A failure counts on Live while it is this recent: it names what fails now, not what failed this morning. */
const RECENT = HOUR;
/** Side-column feeds show their latest N (OR-30). */
const FEED = 5;
/** Free slots drawn one tile each up to this many; more are one tile with their count. */
const IDLE_TILES = 3;
const TONES: DataTone[] = ['d1', 'd2', 'd3', 'd4'];
const ACTIVE = new Set<string>(statesOf('execution', ['working', 'waiting']));
const OPEN_TASK = openStates('task');
/** In work: working first, then waiting, then queued (attention order of the one vocabulary). */
const WORK_ORDER: Record<string, number> = { in_progress: 0, waiting: 1, held: 1, queued: 2 };
/** Two starts closer than this are one moment (WorkFacts shows such a fact once). */
const SAME_MOMENT = MIN;

const m = (value: number | null | undefined, extra: Partial<Measured> = {}): Measured => ({ value: value ?? null, basis: value == null ? 'unavailable' : 'measured', ...extra });
const sum = (xs: readonly number[]) => xs.reduce((a, b) => a + b, 0);
const median = (xs: number[]) => { if (!xs.length) return null; const s = xs.toSorted((a, b) => a - b), i = s.length >> 1; return s.length % 2 ? s[i] : (s[i - 1] + s[i]) / 2; };
const startOfDay = (t: number) => { const d = new Date(t); d.setHours(0, 0, 0, 0); return d.getTime(); };
/** Times for sorting and comparing: a missing time is the oldest. */
const ms = (s: string | null | undefined) => timeOf(s, 0);
/** Times for work facts: a missing time is unknown (NaN), never the oldest. */
const timeAt = (s: string | null | undefined) => timeOf(s, NaN);
const fin = (t: number | undefined | null): t is number => t != null && Number.isFinite(t);
const lifeOfTask = (state: string): Life => (lifeOf.task as Record<string, Life>)[state] ?? 'queued';
const isTaskActive = (t: { state: string; activeState?: string | null }) =>
  t.activeState === 'running' || t.activeState === 'preparing';
const taskWorkLife = (t: { state: string; activeState?: string | null }): Life => {
  if (isTaskActive(t)) return 'working';
  // An in_progress task between runs has no active execution on any host, so keep its dot calm instead of pulsing working.
  if (t.state === 'in_progress') return 'waiting';
  return lifeOfTask(t.state);
};
const taskWorkLabel = (t: { state: string; activeState?: string | null }): string => {
  if (t.state === 'in_progress' && !isTaskActive(t)) return 'In progress · idle';
  return states.task[t.state as keyof typeof states.task]?.label ?? t.state;
};
/** Bars of a trend: `n` buckets of `size` ms ending now, each the sum of `value` over the items whose time falls in it. */
function buckets<T>(items: readonly T[], at: (x: T) => number | null, now: number, n = 12, size = 2 * HOUR, value: (x: T) => number = () => 1): number[] {
  const out = new Array<number>(n).fill(0), from = now - n * size;
  for (const x of items) { const t = at(x); if (t == null || t < from || t > now) continue; out[Math.min(n - 1, Math.floor((t - from) / size))] += value(x); }
  return out;
}
/** Delivery stages as the shared stage chip names them (registry stages). */
const STAGE = CHIP_STAGE;
const IN_FLIGHT = new Set<DeliveryStage>(['pr_ci', 'pr_failed', 'mq_wait', 'mq_ci', 'mq_failed', 'releasing', 'installing']);

/** One problem on Live: repeats of a cause on a host collapse into one with ×N (OR-58). */
type Problem = { host: string | null; cause: string; text: string; n: number; since: number; latest: number };

export function Live(props: { wall: boolean }) {
  const [, go] = useLocation();
  const open = (to: string) => (props.wall ? undefined : () => go(to));
  // Liveness and slots: hosts poll without change rows, so they are re-read every 5 s (as System › Hosts does).
  const hostsOp = useOp('hosts_list', {}, { every: 5000 });
  // Runs change many times a second (gateway calls): a timer only, as Runners reads them.
  const runnersOp = useOp('executions_runners', {}, { every: 15_000 });
  const failuresOp = useOp('executions_list', { needsYou: true }, { quiet: 30_000, every: 30_000 });
  const scope = createMemo(() => {
    const { projectIds, excludeProjectIds } = scopeIds();
    return projectIds ? { projectId: projectIds.map(noProject) } : excludeProjectIds ? { excludeProjectId: excludeProjectIds.map(noProject) } : {};
  });
  const openTasks = useOp('task_page', () => ({ filter: { state: OPEN_TASK, ...scope() }, group: 'project', groupCounts: true, limit: 200 }), { quiet: 15_000 });
  const doneTasks = useOp('task_page', () => ({ filter: { state: ['completed'], ...scope() }, sort: '-changed', limit: 200 }), { quiet: 30_000 });
  const projectsOp = useOp('project_list', {});
  const deliveryOp = useOp('delivery_status', () => scopeIds(), { every: 60_000 });
  const ciOp = useOp('executions_ci', {}, { every: 60_000 });

  const offset = useClockOffset(hostsOp);
  const tick = useTick(1000);
  const now = createMemo(() => tick() + offset());
  const stale = createMemo(() => hostsOp.state === 'offline' || hostsOp.state === 'signed-out');

  const runs = createMemo(() => runnersOp.data?.runs ?? []);
  const runById = createMemo(() => new Map(runs().map(r => [r.id, r])));
  const failures = createMemo(() => new Map((failuresOp.data?.executions ?? []).filter(e => e.failure).map(e => [e.id, e])));
  const taskById = createMemo(() => new Map([...(openTasks.data?.tasks ?? []), ...(doneTasks.data?.tasks ?? [])].map(t => [t.id, t])));
  // Peers for outliers (work-facts.md): the open tasks' attempts and times in work, so a task far past the rest stands out.
  const openList = createMemo(() => openTasks.data?.tasks ?? []);
  const attemptsFence = createMemo(() => peerFence(openList().map(x => x.executions)));
  const workFence = createMemo(() => peerFence(openList().map(x => (now() - timeAt(x.startedAt)) / 1000).filter(Number.isFinite)));
  const projectName = createMemo(() => new Map((projectsOp.data?.projects ?? []).map(p => [String(p.id), String(p.name ?? p.id)])));
  const projectTone = (id: string | null | undefined): DataTone | undefined => {
    if (!id) return undefined;
    const i = (projectsOp.data?.projects ?? []).findIndex(p => String(p.id) === id);
    return i < 0 ? undefined : TONES[i % TONES.length];
  };

  // Failures in the last hour by host and cause (OR-58): from the runs, with the cause in plain words where the
  // execution row carries it. A host failure counts only after the host's latest good start: once the host has started
  // work again it is history, not a current problem.
  const problems = createMemo<Problem[]>(() => {
    const t = now(), by = new Map<string, Problem>(), recovered = hostRecoveredAt(runs());
    const add = (host: string | null, cause: string, text: string, at: number) => {
      const k = `${host}\0${cause}`, p = by.get(k);
      if (p) { p.n++; p.since = Math.min(p.since, at); p.latest = Math.max(p.latest, at); }
      else by.set(k, { host, cause, text, n: 1, since: at, latest: at });
    };
    for (const r of runs()) {
      // A tool call row sums a connection's calls: a refused key shows on System as the connection's problem, and an
      // agent's bad call (unknown tool, invalid request) is its own turn's business, not Live's.
      if (r.state !== 'failed' || r.kind === 'tool_run') continue;
      const at = r.endedAt ?? r.startedAt;
      if (at == null || t - at > RECENT || (r.hostId && (r.startedAt ?? at) <= (recovered.get(r.hostId) ?? 0))) continue;
      const f = causeOf(failures().get(r.id)?.failure, r.problem);
      add(r.hostId, f?.id ?? 'other', f?.text ?? 'The run failed.', at);
    }
    return [...by.values()].toSorted((a, b) => b.n - a.n || b.latest - a.latest);
  });

  const fresh = (h: HostRow) => !!h.lastSeenAt && now() - h.lastSeenAt < (hostsOp.data?.staleAfterMs ?? 60_000);
  type Band = { id: string; props: HostBandProps; busy: number; slots: number; trouble: string | null };
  const bands = createMemo<Band[]>(() => {
    const t = now(), all = runs();
    return (hostsOp.data?.hosts ?? []).filter(h => !h.removedAt).map(h => {
      const offline = !fresh(h);
      const mine = all.filter(r => r.hostId === h.id);
      const failing = problems().filter(p => p.host === h.id);
      const hostProblem = h.pollError ? `Polls failing: ${h.pollError.reason}` : h.problems[0] ? h.problems[0].detail || h.problems[0].kind : null;
      const busy = h.running, limit = Math.max(h.slotLimit ?? 0, busy, (h.runs ?? []).length);
      // A host updating takes no new start (fleet-self-update.md, Executor drain): its free slots are not idle, and a
      // start handed to it before the drain began waits for the update.
      const updating = h.problems.find(p => p.kind === 'updating');
      // Slot tile i shows runs[i] (start order); a failure takes the first free tile, outlined (worker-slot.md).
      const slots: WorkerSlotProps[] = (h.runs ?? []).map((hr, i): WorkerSlotProps => {
        const r = runById().get(hr.id), task = r?.taskId ? taskById().get(r.taskId) : undefined;
        const started = r?.startedAt ?? (hr.startedAt ? ms(hr.startedAt) : undefined);
        const target = r?.taskId ? `/tasks/${r.taskId}` : r?.sessionId ? `/conversations/${r.sessionId}` : null;
        // The tile's own time is this attempt; the facts add the attempts and the time in work since the first one.
        const work = task ? timeAt(task.startedAt) - offset() : NaN, runStart = started != null ? started - offset() : NaN;
        return {
          slot: i + 1, state: 'busy', title: task?.title ?? r?.taskTitle ?? r?.sessionTitle ?? (updating && hr.state === 'preparing' ? 'Waits for the update' : 'Starting…'), code: task?.code ?? undefined,
          project: task?.projectId ? projectName().get(task.projectId) ?? task.projectId : undefined,
          color: projectTone(task?.projectId), tokens: r?.tokens != null ? m(r.tokens) : undefined,
          work: { attempts: task?.executions, attemptsFence: attemptsFence(), agent: r?.harness ?? task?.agent,
            workAt: fin(work) && !(fin(runStart) && Math.abs(work - runStart) < SAME_MOMENT) ? work : undefined, workFence: workFence() },
          started: started != null && Number.isFinite(started) ? started - offset() : undefined, now: stale() ? t - offset() : undefined,
          onOpen: target ? open(target) : undefined,
        };
      });
      // Free slots: one tile each up to IDLE_TILES, else one tile that counts them, so a 24-slot host stays one glance.
      const free = limit - slots.length;
      if (updating && free > 0) slots.push({ slot: slots.length + 1, state: 'failing', title: `${free} slot${free === 1 ? '' : 's'} take no new start`,
        cause: firstSentence(updating.detail)[0], onOpen: open(`/hosts/${encodeURIComponent(h.id)}`) });
      else if (free > IDLE_TILES) slots.push({ slot: slots.length + 1, state: 'idle', free });
      else for (let i = slots.length; i < limit; i++) slots.push({ slot: i + 1, state: 'idle' });
      const f = failing[0];
      if (f) {
        const i = slots.findIndex(s => s.state === 'idle');
        const tile: WorkerSlotProps = { slot: i < 0 ? slots.length + 1 : slots[i].slot, state: 'failing', title: firstSentence(f.text)[0], cause: firstSentence(f.text)[1] || undefined,
          work: { attempts: f.n, lastAt: f.latest - offset(), lastLabel: 'Latest failed start' },
          onOpen: open(`/hosts/${encodeURIComponent(h.id)}`) };
        if (i < 0) slots.push(tile);
        else if ((slots[i].free ?? 1) > 1) slots.splice(i, 1, tile, { ...slots[i], slot: slots[i].slot + 1, free: slots[i].free! - 1 });
        else slots[i] = tile;
      }
      const load = h.load;
      const cpu = load?.load1 != null && load.cpus ? Math.min(100, 100 * load.load1 / load.cpus) : null;
      const mem = load?.memTotal && load.memFree != null ? 100 * (1 - load.memFree / load.memTotal) : null;
      const gauges: GaugeProps[] = load ? [
        { label: 'CPU', name: 'CPU', value: cpu, warnAt: WARN, critAt: CRIT },
        { label: 'MEM', name: 'Memory', value: mem, warnAt: WARN, critAt: CRIT },
      ] : [];
      const day = mine.filter(r => (r.startedAt ?? 0) > t - DAY);
      const active = sum(day.map(r => r.wallMs ?? 0)) / 1000;
      const facts = [h.platform, load?.diskFree != null ? `${bytes(load.diskFree).text} free` : null].filter(Boolean).join(' · ');
      const trouble = offline ? `${h.id} is offline` : hostProblem ? `${h.id}: ${hostProblem}` : f ? `${h.id}: ${f.text}${f.n > 1 ? ` ×${f.n}` : ''}` : null;
      const still = busy > 0 ? `${busy} run${busy === 1 ? '' : 's'} still working.` : 'The host is online with no runs now.';
      const at = (t: number) => clock(t - offset()).slice(0, 5);
      const problemLine = hostProblem ?? (f ? `${f.n > 1 ? `${f.n} failed starts since ${at(f.since)}` : `Failed start at ${at(f.since)}`}: ${f.text.replace(/\.?$/, '.')} ${still}` : undefined);
      return {
        id: h.id, busy, slots: limit, trouble,
        props: {
          name: h.id, state: (offline ? 'offline' : trouble ? 'problem' : 'normal') as HostBandProps['state'], facts, gauges,
          day: day.length ? `${day.length} runs · ${duration(active).text}` : undefined,
          problem: problemLine, slots, seen: h.lastSeenAt ? h.lastSeenAt - offset() : undefined,
          strip: { now: t - offset(), runs: mine.filter(r => r.startedAt != null).map(r => ({ start: r.startedAt! - offset(), end: ACTIVE.has(r.state) ? null : (r.endedAt ?? r.startedAt! + (r.wallMs ?? 0)) - offset(), failed: r.state === 'failed' })) },
          // A red band leads to System, where the host's problem row names its cause and fix (J5); a healthy one opens the host.
          onOpen: open(trouble ? '/system' : `/hosts/${encodeURIComponent(h.id)}`),
        },
      };
    // Hosts with a problem first, then by busy slots (pages/live.md, Layout 3).
    }).toSorted((a, b) => Number(!!b.trouble) - Number(!!a.trouble) || b.busy - a.busy || a.id.localeCompare(b.id));
  });

  // The status pill: every host trouble and every failure off a host, first in words, the rest counted.
  const trouble = createMemo(() => [...bands().filter(b => b.trouble).map(b => ({ text: b.trouble!, host: b.id as string | null })),
    ...problems().filter(p => !p.host).map(p => ({ text: p.text + (p.n > 1 ? ` ×${p.n}` : ''), host: null }))]);
  const pill = createMemo(() => {
    if (!hostsOp.data) return { state: 'connecting' as const };
    if (stale()) return { state: 'stale' as const, at: hostsOp.data.serverNow - offset() };
    const t = trouble();
    return t.length ? { state: 'problem' as const, text: t[0].text, more: t.length - 1, host: t[0].host } : { state: 'working' as const };
  });

  // Figures (pages/live.md, Layout 2).
  const today = createMemo(() => (runnersOp.data?.spend ?? []).filter(s => s.period === 'today'));
  const month = createMemo(() => (runnersOp.data?.spend ?? []).filter(s => s.period === 'month'));
  const kindRuns = (k: string) => sum(today().filter(s => s.kind === k).map(s => s.runs));
  const tokensOf = (rows: typeof today extends () => infer R ? R : never) => rows.some(s => s.tokens != null) ? sum(rows.map(s => s.tokens ?? 0)) : null;
  const agentRuns = createMemo(() => runs().filter(r => r.kind === 'agent_run' || r.kind === 'child_agent'));
  const done = createMemo(() => { const from = startOfDay(now() - offset()); return (doneTasks.data?.tasks ?? []).filter(x => ms(x.stateAt) >= from); });
  // Starts on hosts (agent runs and host jobs), not tool calls: a provider refusing a call is not a failed start.
  const failedStarts = createMemo(() => runs().filter(r => r.state === 'failed' && !!r.hostId && (r.startedAt ?? r.endedAt ?? 0) > now() - 6 * HOUR));
  const figures = createMemo(() => {
    const t = now(), b = bands();
    const busy = sum(b.map(x => x.busy)), slots = sum(b.map(x => x.slots));
    const lead = median(done().flatMap(x => { const a = timeOf(x.createdAt), b = timeOf(x.stateAt); return a != null && b != null ? [b - a] : []; }));
    const hostJobs = kindRuns('host_job'), toolCalls = kindRuns('tool_run');
    const tokToday = tokensOf(today()), tokMonth = tokensOf(month());
    const usdToday = usdOf(today().flatMap(s => s.cost)), usdMonth = usdOf(month().flatMap(s => s.cost));
    const fs = failedStarts(), cause = problems().find(p => p.host);
    // Concurrency per bucket: runs active at the bucket's end.
    const working = Array.from({ length: 12 }, (_, i) => { const at = t - (11 - i) * 2 * HOUR; return agentRuns().filter(r => r.startedAt != null && r.startedAt <= at && (ACTIVE.has(r.state) || (r.endedAt ?? 0) >= at)).length; });
    return [
      { key: 'working', label: 'Agents working', value: m(busy), of: slots || undefined, sub: `of ${slots} slots`, trend: working, trendLabel: 'agents working, last 24 hours' },
      { key: 'runs', label: 'Agent runs today', value: m(kindRuns('agent_run') + kindRuns('child_agent')), sub: [hostJobs ? `+${hostJobs} host jobs` : '', toolCalls ? `${toolCalls} tool calls` : '', 'UTC day'].filter(Boolean).join(' · '), trend: buckets(agentRuns(), r => r.startedAt, t), trendLabel: 'agent runs started, last 24 hours' },
      { key: 'done', label: 'Tasks done today', value: m(done().length), sub: lead != null ? `lead time P50 ${duration(lead / 1000).text} · local day` : 'local day', trend: buckets(doneTasks.data?.tasks ?? [], x => ms(x.stateAt), t), trendLabel: 'tasks done, last 24 hours' },
      { key: 'tokens', label: 'Tokens today', value: m(tokToday), sub: [tokMonth != null ? `month ${count(tokMonth).text}` : '', 'UTC day'].filter(Boolean).join(' · '), trend: buckets(runs(), r => r.startedAt, t, 12, 2 * HOUR, r => r.tokens ?? 0), trendLabel: 'tokens, last 24 hours' },
      { key: 'spend', label: 'Spend today', value: m(usdToday, { currency: 'USD' }), kind: 'money' as const, sub: `month ${money(usdMonth).text} · UTC day`, trend: buckets(runs(), r => r.startedAt, t, 12, 2 * HOUR, r => usdOf(r.cost)), trendLabel: 'spend at list price, last 24 hours' },
      { key: 'failed', label: 'Failed starts 6h', value: m(fs.length), alert: !!cause, sub: cause?.text, trend: buckets(fs, r => r.startedAt ?? r.endedAt, t, 12, 30 * MIN), trendLabel: 'failed starts, last 6 hours' },
    ];
  });

  // Side column.
  const projectRows = createMemo(() => {
    const t = now(), d = done(), open = openList();
    const groups = openTasks.data?.groups ?? [];
    // g.counts includes terminal states (completed, cancelled); sum only OPEN_TASK states to check whether openList() is untruncated.
    const totalOpen = groups.reduce((acc, g) => acc + (g.counts?.in_progress ?? 0) + (g.counts?.waiting ?? 0) + (g.counts?.queued ?? 0) + (g.counts?.held ?? 0), 0);
    const exactActive = open.length >= totalOpen;
    const rows = groups.map(g => {
      const id = g.key === '~' ? null : g.key, c = g.counts ?? {};
      const doneHere = d.filter(x => (x.projectId ?? null) === id);
      const activeHere = exactActive ? open.filter(x => (x.projectId ?? null) === id && isTaskActive(x)).length : (c.in_progress ?? 0);
      const nonQueuedOpen = (c.in_progress ?? 0) + (c.waiting ?? 0);
      const idleOpen = Math.max(0, nonQueuedOpen - activeHere);
      return { id, name: id ? projectName().get(id) ?? g.label ?? id : 'Outside projects', running: activeHere, open: idleOpen, queued: (c.queued ?? 0) + (c.held ?? 0), done: doneHere.length,
        trend: buckets(doneHere, x => ms(x.stateAt), t) };
    });
    // Projects with only work done today have no open-task group.
    for (const id of new Set(d.map(x => x.projectId ?? null))) {
      if (rows.some(r => r.id === id)) continue;
      const doneHere = d.filter(x => (x.projectId ?? null) === id);
      rows.push({ id, name: id ? projectName().get(id) ?? id : 'Outside projects', running: 0, open: 0, queued: 0, done: doneHere.length, trend: buckets(doneHere, x => ms(x.stateAt), t) });
    }
    return rows.filter(r => r.running + r.open + r.queued + r.done > 0).toSorted((a, b) => b.running - a.running || b.open - a.open || b.queued - a.queued || a.name.localeCompare(b.name));
  });
  // In work: every open task with its work facts, actually running/preparing first, then in_progress/waiting/queued, longest in work first.
  const work = createMemo(() => openList().toSorted((a, b) => Number(isTaskActive(b)) - Number(isTaskActive(a))
    || (WORK_ORDER[a.state] ?? 3) - (WORK_ORDER[b.state] ?? 3)
    || (timeAt(a.startedAt) || Infinity) - (timeAt(b.startedAt) || Infinity) || a.title.localeCompare(b.title)));
  const delivery = createMemo(() => (deliveryOp.data?.items ?? []).filter(i => IN_FLIGHT.has(i.stage)).toSorted((a, b) => ms(b.since) - ms(a.since)));
  const ci = createMemo(() => {
    const d = ciOp.data;
    if (!d) return [];
    const rows: { key: string; name: string; fact: string; trouble: boolean }[] = d.minutesToday.filter(x => x.hosted).map(x => {
      const running = d.jobs.filter(j => j.hosted && j.runner === x.runner && j.status === 'in_progress').length;
      return { key: 'h:' + x.runner, name: x.runner, fact: running ? `${running} running · ${Math.round(x.minutes)} min today` : `${x.jobs} jobs · ${Math.round(x.minutes)} min today`, trouble: false };
    });
    for (const r of d.runners ?? []) rows.push({ key: 's:' + r.name, name: `self-hosted ${r.name}`, fact: r.status === 'online' ? (r.busy ? 'online · busy' : 'online') : r.status, trouble: r.status !== 'online' });
    return rows;
  });
  const recent = useOp('history_view', () => { const { projectIds, excludeProjectIds } = scopeIds(); return { section: 'work', limit: FEED, ...(projectIds?.length ? { projectIds } : {}), ...(excludeProjectIds?.length ? { excludeProjectIds } : {}) }; });
  const events = createMemo(() => recent.data?.entries ?? []);
  // Feeds are previews of their latest FEED rows, no toggle (OR-30, OR-65): the heading opens the full list where one exists.
  const feed = <T,>(xs: readonly T[]) => xs.slice(0, FEED);
  const head = (label: string, n: number, to?: string): JSX.Element => (
    <h2 class="live-h">
      <Show when={to && !props.wall} fallback={<>{label} <span class="mono">{n}</span></>}>
        <a class="live-open" href={to} onClick={e => { if (!e.metaKey && !e.ctrlKey) { e.preventDefault(); go(to!); } }}>{label} <span class="mono">{n}</span><Icon name="chevron-right" /></a>
      </Show>
    </h2>
  );

  const focus = createMemo(() => {
    const f = prefs.value.focus.filter(p => !prefs.value.ignored.includes(p));
    return f.length ? f.slice(0, 2).map(p => projectName().get(p) ?? p).join(', ') + (f.length > 2 ? ` +${f.length - 2}` : '') : 'all projects';
  });
  const wallClock = createMemo(() => clock(now() - offset()).slice(0, 5));

  return (
    <div class="live" data-ui="live" data-wall={props.wall ? '' : undefined} data-stale={stale() ? '' : undefined}>
      <header class="live-bar">
        <Show when={props.wall}><b class="brand">Fleet live</b></Show>
        <StatusPill state={pill().state} text={(pill() as { text?: string }).text} more={(pill() as { more?: number }).more} at={(pill() as { at?: number }).at}
          onOpen={(pill() as { host?: string | null }).host ? open('/system') : undefined} />
        <span class="grow" />
        <Show when={props.wall}>
          <span class="live-focus muted">focus: {focus()}</span>
          <time class="live-clock mono">{wallClock()}</time>
        </Show>
      </header>
      <Show when={hostsOp.error && !hostsOp.data}><ErrorBanner text={`Hosts did not load: ${hostsOp.error}`} onRetry={hostsOp.reload} /></Show>
      <section class="live-figures" aria-label="Figures">
        <For each={figures()}>{f => (
          <FigureTile label={f.label} value={f.value} kind={f.kind ?? 'count'} of={f.of} sub={f.sub} trend={f.trend} trendLabel={f.trendLabel} alert={f.alert} stale={stale() || !runnersOp.data} />
        )}</For>
      </section>
      <div class="live-cols">
        <section class="live-hosts" aria-label="Hosts">
          <h2 class="live-h">Hosts · worker slots · 24h runs <span class="mono">{bands().length || ''}</span></h2>
          <Show when={hostsOp.data} fallback={<><HostBand name="Loading" state="loading" /><HostBand name="Loading" state="loading" /></>}>
            <Show when={bands().length} fallback={<Empty text="No hosts yet. Register a host to run agents on it." action={props.wall ? undefined : <Button label="System › Hosts" icon="panels-top-left" onClick={() => go('/hosts')} />} />}>
              <For each={bands()} keyed={b => b.id}>{b => <HostBand {...b().props} />}</For>
            </Show>
          </Show>
        </section>
        <aside class="live-side" aria-label="Now">
          <Show when={projectRows().length}>
            <section aria-label="Projects now">
              {head('Projects · now', projectRows().length, '/projects')}
              <ul class="live-list">
                <For each={feed(projectRows())}>{p => <LiveRow kind="project" name={p.name} running={p.running} open={p.open} queued={p.queued} done={p.done} trend={p.trend} trendLabel="tasks done, last 24 hours" stale={stale()} onOpen={p.id ? open(`/projects/${encodeURIComponent(p.id)}`) : undefined} />}</For>
              </ul>
            </section>
          </Show>
          <Show when={work().length}>
            <section aria-label="In work">
              {head('In work', work().length, '/tasks')}
              <ul class="live-list">
                <For each={feed(work())} keyed={t => t.id}>{t => (
                  <LiveRow kind="task" state={taskWorkLife(t())} code={t().code ?? undefined} title={t().title} stale={stale()} onOpen={open(`/tasks/${t().code ?? t().id}`)}
                    facts={<WorkFacts attempts={t().executions} attemptsFence={attemptsFence()} lastAt={timeAt(t().lastStartedAt ?? t().activeStartedAt) - offset()}
                      workAt={timeAt(t().startedAt) - offset()} workFence={workFence()} agent={t().agent ?? t().lastAgent}
                      state={{ label: taskWorkLabel(t()), at: timeAt(t().stateAt) - offset() }} />} />
                )}</For>
              </ul>
            </section>
          </Show>
          <Show when={delivery().length}>
            <section aria-label="Delivery">
              {head('Delivery', delivery().length, '/delivery')}
              <ul class="live-list">
                <For each={feed(delivery())} keyed={d => `${d.repo}#${d.number}`}>{dd => { const d = dd(); return <LiveRow kind="delivery" tone={(states.delivery[d.stage]?.tone ?? 'neutral') as Tone} code={`#${d.number}`} title={d.title} stage={STAGE[d.stage] ?? 'pr_open'} trouble={d.stage.endsWith('failed')} stale={stale()} onOpen={open(`/delivery/${encodeURIComponent(`${d.repo.replace('/', '~')}~${d.number}`)}`)}
                  facts={<WorkFacts workAt={timeAt(d.opened) - offset()} state={{ label: states.delivery[d.stage]?.label ?? d.stage, at: timeAt(d.since) - offset() }} />} />; }}</For>
              </ul>
            </section>
          </Show>
          <Show when={ci().length}>
            <section aria-label="CI runners">
              <h2 class="live-h">CI runners <span class="mono">{ci().length}</span></h2>
              <ul class="live-list">
                <For each={ci()}>{r => <LiveRow kind="runner" name={r.name} fact={r.fact} trouble={r.trouble} stale={stale()} onOpen={open('/runners')} />}</For>
              </ul>
            </section>
          </Show>
          <Show when={events().length}>
            <section aria-label="Just now">
              {head('Just now', events().length, '/history')}
              <ul class="live-list"><HistoryRows entries={events()} still={props.wall} /></ul>
            </section>
          </Show>
        </aside>
      </div>
    </div>
  );
}

