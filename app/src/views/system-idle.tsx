// System idle (docs/design-system/pages/system.md, "Work area, idle"; canvas board/screens/system.bl state 1): readiness
// first (hosts with a problem, accounts near their limit or with no host to run on, connections with failing keys), each
// one problem row with its fix action and repeats as ×N (OR-58); then today's and the month's spend; then services with
// errors in the last hour. A healthy System says so in one line. Every fact comes from the operations agents read too
// (hosts_list, telemetry_*, executions_runners, subscription_limits, account_list, connections_list, spend_summary).
import { createMemo, createSignal, For, Show } from 'solid-js';
import {
  AgeChip, Button, DetailSection, ErrorBanner, FigureTile, Icon, KvRow, Num, ProblemRow, Skeleton, cause, duration, money,
  type CostFigureLike,
} from '../../../packages/ui/src/index.ts';
import type { HostRow, RunnerRun } from '../../../contracts/generated/ops.ts';
import { op, type Ops } from '../api.ts';
import { useOp } from '../store.ts';
import { prefsRead } from '../prefs.ts';
import { accountHosts, accountName, nearLimit, readinessText, tightest, useAccountLimits } from './accounts.tsx';
import type { Conn } from './keys.tsx';
import { spendRequest, usdOf } from './spend.tsx';

const HOUR = 36e5;
/** A telemetry finding without its sentence (every rule writes one): the generic words, never the cause code. */
export const FINDING = 'A health check found something unusual';
/** A provider's refusal can be a whole JSON body: the row keeps its first 140 characters; the connection has the rest. */
const clip = (t: string) => t.length > 140 ? t.slice(0, 139) + '…' : t;
const hostPath = (id: string) => `/hosts/${encodeURIComponent(id)}`;

export type Problem = {
  key: string; title: string; facts: string; severity: 'warning' | 'failure'; count: number;
  /** Newest occurrence (ms), shown as an age. */
  at?: number;
  open: { label: string; path: string };
};

/** First sentence of a plain cause: the row title; the rest is the fix. */
export const firstSentence = (t: string) => { const i = t.indexOf('. '); return i < 0 ? [t.replace(/\.$/, ''), ''] : [t.slice(0, i), t.slice(i + 2)]; };

/** Failed runs of the last hour by host and cause (OR-58): one problem per pair, with how many and the newest. */
export function hostFailures(runs: readonly RunnerRun[], now: number): Map<string, { cause: string; text: string; n: number; at: number }[]> {
  const out = new Map<string, Map<string, { cause: string; text: string; n: number; at: number }>>();
  for (const r of runs) {
    if (r.state !== 'failed' || !r.hostId || !r.problem) continue;
    const at = r.endedAt ?? r.lastSeenAt ?? r.startedAt ?? 0;
    if (now - at > HOUR) continue;
    const c = cause(r.problem);
    const byCause = out.get(r.hostId) ?? new Map();
    const cur = byCause.get(c.id) ?? { cause: c.id, text: c.text, n: 0, at: 0 };
    cur.n++; cur.at = Math.max(cur.at, at);
    byCause.set(c.id, cur); out.set(r.hostId, byCause);
  }
  return new Map([...out].map(([h, m]) => [h, [...m.values()].toSorted((a, b) => b.n - a.n)]));
}

/** Readiness problems over the shared reads; failures first, then warnings, newest first within each. */
export function readiness(input: {
  hosts: readonly HostRow[]; fresh: (h: HostRow) => boolean; now: number;
  findings: Ops['telemetry_findings']['reply']['findings']; runs: readonly RunnerRun[];
  subs: Ops['subscription_limits']['reply']['subscriptions']; accounts: readonly Record<string, unknown>[];
  conns: readonly Conn[];
}): Problem[] {
  const out: Problem[] = [];
  const failures = hostFailures(input.runs, input.now);
  for (const h of input.hosts) {
    const open = { label: 'Open host', path: hostPath(h.id) };
    if (!input.fresh(h)) {
      out.push({ key: `host:${h.id}:seen`, severity: 'warning', count: 1, open, at: h.lastSeenAt ?? undefined,
        title: `${h.id}: not seen`, facts: h.lastSeenAt ? (h.running > 0 ? 'running · unconfirmed, last seen' : 'last seen') : 'never polled' });
      continue;
    }
    if (h.pollError) out.push({ key: `host:${h.id}:poll`, severity: 'failure', count: 1, open, at: h.pollError.since,
      title: `${h.id}: polls failing`, facts: `coordinator answers ${h.pollError.status} (${h.pollError.reason}) since` });
    for (const p of h.problems) out.push({ key: `host:${h.id}:edge:${p.route}`, severity: 'failure', count: 1, open,
      title: `${h.id}: edge answers ${p.route} with ${p.status}`, facts: p.detail || p.location || '' });
    for (const f of failures.get(h.id) ?? []) {
      const [title, fix] = firstSentence(f.text);
      out.push({ key: `host:${h.id}:cause:${f.cause}`, severity: 'failure', count: f.n, open, at: f.at, title: `${h.id}: ${title}`, facts: fix });
    }
    const outdated = (h.harnessVersions ?? []).filter(v => v.observed && v.observed !== v.expected);
    if (outdated.length) out.push({ key: `host:${h.id}:cli`, severity: 'warning', count: 1, open,
      title: `${h.id}: agent CLI drift`, facts: outdated.map(v => `${v.harness} ${v.observed}, expected ${v.expected}`).join(' · ') });
  }
  for (const f of input.findings) {
    const open = f.hostId ? { label: 'Open host', path: hostPath(f.hostId) } : { label: 'Open Runners', path: '/runners' };
    out.push({ key: `finding:${f.cause}`, severity: 'warning', count: 1, open, at: Date.parse(f.since), title: f.text ?? FINDING, facts: f.groupCause ? 'Part of a wider problem shown above' : '' });
  }
  for (const s of input.subs) {
    const p = tightest(s);
    if (s.intake !== 'paused' && nearLimit(p)) out.push({ key: `account:${s.id}:limit`, severity: 'warning', count: 1,
      open: { label: 'Open account', path: `/hosts/limits/${encodeURIComponent(s.id)}` }, at: p!.readingAt ?? undefined,
      title: `${accountName(s)}: ${p!.left ?? '?'}% left`, facts: `${s.harness} · ${p!.period}${p!.low ? ' · below its Low mark' : ''}` });
  }
  // J5: an account no host can start runs for; dispatch would skip it, so it shows before work waits on it.
  for (const a of input.accounts) {
    if ((a as { intake?: string }).intake === 'paused') continue;
    const hosts = accountHosts(a as never);
    if (hosts.some(c => c.readiness === 'ready')) continue;
    const id = String(a.id), name = String(a.name ?? a.id);
    out.push({ key: `account:${id}:hosts`, severity: 'warning', count: 1,
      open: { label: 'Open account', path: `/hosts/limits/${encodeURIComponent(id)}` },
      title: hosts.length ? `${name}: no host can run it` : `${name}: no host has a login`,
      facts: hosts.map(c => `${c.hostId}: ${readinessText(c)}`).join(' · ') || `${String(a.harness ?? '')} account without a host binding` });
  }
  for (const c of input.conns) {
    const bad = c.keys.filter(k => k.state === 'disabled' || !!k.problem);
    if (!bad.length) continue;
    const k = bad[0];
    out.push({ key: `conn:${c.name}:keys`, severity: 'failure', count: bad.length,
      open: { label: 'Open connection', path: `/keys/${encodeURIComponent(c.name)}` },
      at: typeof k.problem?.at === 'number' ? k.problem.at : k.disabledAt,
      title: `${c.name}: key ${k.state === 'disabled' ? 'disabled' : `refused (${k.problem?.status})`}`,
      facts: clip(k.problem?.message ?? k.disabledReason ?? '') });
  }
  const rank = (p: Problem) => (p.severity === 'failure' ? 0 : 1);
  return out.toSorted((a, b) => rank(a) - rank(b) || (b.at ?? 0) - (a.at ?? 0));
}

export function SystemIdle(props: {
  hosts: readonly HostRow[]; fresh: (h: HostRow) => boolean; now: number; clockOffsetMs: number;
  conns: readonly Conn[]; loading: boolean; error: string | null; onRetry: () => void; onOpen: (path: string) => void;
}) {
  const tele = useOp('telemetry_summary', { window: '1h' }, { every: 30000 });
  const found = useOp('telemetry_findings', {}, { every: 30000 });
  // Timer only, like Runners: gateway calls change executions many times a second.
  const runners = useOp('executions_runners', {}, { every: 15000 });
  const limits = useAccountLimits();
  const accounts = useOp('account_list', {});
  const today = useOp('spend_summary', () => (prefsRead.value ? spendRequest(1) : null), { quiet: 30_000 });
  const month = useOp('spend_summary', () => (prefsRead.value ? spendRequest(30) : null), { quiet: 30_000 });
  const [purge, setPurge] = createSignal<Ops['system_purge']['reply'] | null>(null);
  const [purgeErr, setPurgeErr] = createSignal('');
  const runPurge = (preview: boolean) => { setPurgeErr(''); void op('system_purge', { preview, kinds: ['host'] }).then(setPurge, e => setPurgeErr(String(e.message ?? e))); };

  const problems = createMemo(() => readiness({
    hosts: props.hosts, fresh: props.fresh, now: props.now, conns: props.conns,
    findings: found.data?.findings ?? [], runs: runners.data?.runs ?? [],
    subs: limits.data?.subscriptions ?? [], accounts: (accounts.data?.accounts ?? []) as Record<string, unknown>[],
  }));
  const services = createMemo(() => (tele.data?.operations ?? []).filter(o => o.errors > 0).toSorted((a, b) => b.errors - a.errors));
  // Host facts come from hosts_list alone: show what has arrived; a read that failed has its banner above.
  const ready = createMemo(() => !props.loading && (!!runners.data || !!runners.error) && (!!limits.data || !!limits.error) && (!!accounts.data || !!accounts.error));
  const cost = (d: Ops['spend_summary']['reply'] | null) => d ? usdOf(d.totals.cost as CostFigureLike[]) : null;
  const perTask = createMemo(() => {
    const b = month.data?.bought;
    return b && b.completedTasks > 0 ? usdOf(b.taskCost as CostFigureLike[]) / b.completedTasks : null;
  });
  const unpriced = createMemo(() => month.data ? month.data.rows.reduce((s, r) => s + r.unpriced, 0) : null);
  const errorText = () => props.error ?? runners.error ?? limits.error ?? accounts.error;

  return (
    <article class="detail" data-ui="detail" aria-label="System">
      <Show when={errorText()}>{e => <ErrorBanner text={e()} onRetry={() => { props.onRetry(); void runners.reload(); void limits.reload(); void accounts.reload(); }} />}</Show>
      <Show when={ready()} fallback={<Skeleton rows={4} />}>
        <Show when={problems().length} fallback={<p class="sys-healthy"><Icon name="circle-check" /> Hosts, accounts and connections are healthy.</p>}>
          <DetailSection title={`Needs a look ${problems().length}`}>
            <div class="sys-problems">
              <For each={problems()}>{p => (
                <ProblemRow severity={p.severity} title={p.title} count={p.count}
                  facts={<>{p.facts}<Show when={p.at}>{at => <> <AgeChip t={at()} clockOffsetMs={props.clockOffsetMs} /></>}</Show></>}
                  actions={[{ label: p.open.label, primary: true, onClick: () => props.onOpen(p.open.path) }]} />
              )}</For>
            </div>
          </DetailSection>
        </Show>
      </Show>
      <DetailSection title="Spend">
        <Show when={today.error ?? month.error}>{e => <ErrorBanner text={e()} onRetry={() => { void today.reload(); void month.reload(); }} />}</Show>
        <div class="sys-figures">
          <FigureTile label="Spend today" kind="money" value={{ value: cost(today.data), basis: today.data ? 'measured' : 'unavailable', reason: 'Loading' }}
            sub={cost(month.data) != null ? `${money(cost(month.data)!).text} in 30 days` : undefined}
            trend={[]} trendLabel="Spend by day" onOpen={() => props.onOpen('/spend')} />
          <FigureTile label="Cost per task done" kind="money" value={{ value: perTask(), basis: perTask() == null ? 'unavailable' : 'measured', reason: 'No task done in 30 days' }}
            sub={month.data ? `${month.data.bought.completedTasks} tasks, 30 days` : undefined}
            trend={[]} trendLabel="Cost per task by day" onOpen={() => props.onOpen('/spend')} />
          <Show when={(unpriced() ?? 0) > 0}>
            <FigureTile label="Unpriced runs" value={{ value: unpriced(), basis: 'measured' }} sub="no price entry" alert
              trend={[]} trendLabel="Unpriced runs by day" onOpen={() => props.onOpen('/spend')} />
          </Show>
        </div>
      </DetailSection>
      <Show when={services().length > 0}>
        <DetailSection title={`Services with errors, last hour ${services().length}`}>
          <For each={services()}>{o => (
            <KvRow icon="circle-alert" label={o.name} meta={o.service}
              value={<span class="mono"><span class="t-danger">{o.errors}</span> errors of <Num m={{ value: o.count, basis: 'measured' }} kind="count" /> · p95 {duration(o.p95Ms / 1000).text}</span>} />
          )}</For>
          <Show when={(tele.data?.droppedSpans ?? 0) > 0}>
            <KvRow icon="circle-alert" label="Spans dropped" value={<Num m={{ value: tele.data!.droppedSpans, basis: 'measured' }} kind="count" />} />
          </Show>
        </DetailSection>
      </Show>
      <DetailSection title="Removed hosts">
        <Show when={purgeErr()}>{e => <ProblemRow title="Not purged" facts={e()} severity="failure" actions={[]} />}</Show>
        <Show when={purge()} fallback={<Button label="Find removed hosts to purge" icon="trash-2" kind="quiet" onClick={() => runPurge(true)} />}>{p => <>
          <KvRow icon="trash-2" label="Nothing references them" value={<Num m={{ value: p().removed[0]?.count ?? 0, basis: 'measured' }} kind="count" />} />
          <KvRow icon="save" label="Kept by history" value={<Num m={{ value: p().kept?.[0]?.count ?? 0, basis: 'measured' }} kind="count" />} />
          <Show when={(p().removed[0]?.count ?? 0) > 0 && !p().purged}><Button label="Purge" icon="trash-2" kind="danger" onClick={() => runPurge(false)} /></Show>
        </>}</Show>
      </DetailSection>
    </article>
  );
}
