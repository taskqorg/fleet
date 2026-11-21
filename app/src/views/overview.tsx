// Overview (docs/design-system/pages/overview.md): what needs the operator and whether the fleet is healthy. The control
// column is the Needs-you inbox, one row per question or problem, grouped by cause; the work area answers the one picked,
// and shows the fleet's state while none is (2026-11-20 decision, Overview B).
import { useState } from 'preact/hooks';
import { useLocation, useSearch } from 'wouter-preact';
import {
  NeedsYou, OpsSummary, VirtualList, GroupHeader, catalogueRow, StatStrip, DecisionBlock, ProblemRow, StateBadge, HarnessCounts, Num, StateCounters, Empty, OfflineMarker, Icon, ErrorBanner, Skeleton, Button,
  ListDetail, DetailSection, KvRow, AgeChip, Ago, EntityCode, keyConnection, actor, ShellStatus, type IconName, type Stat,
} from '../../../packages/ui/src/index.ts';
import { op, online, addRequestedKey, writeOnce, useAlive } from '../api.ts';
import { useOp, type Freshness } from '../store.ts';
import type { QuestionRow, ExecutionRow, HostRow } from '../../../contracts/generated/ops.ts';
import { scopeIds, noProject, prefsRead, prefs } from '../prefs.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';
import { idOf } from './delivery.tsx';
import { useClockOffset } from './hosts.tsx';

/** The Overview's reads joined: open questions, runs that need a look, task counts and each project's counts. */
type Overview = { questions: QuestionRow[]; problems: ExecutionRow[]; counts: Record<string, number>; projects: { id: string; project: string | null; counts: Record<string, number> }[] };
// The focus and ignored projects (OR-34) narrow every part: questions, problems, counts and project rows.
function useOverview(scope: ReturnType<typeof scopeIds>) {
  const ready = prefsRead.value, { projectIds, excludeProjectIds } = scope;
  const q = useOp('questions_list', ready ? { state: 'open', ...scope } : null);
  const p = useOp('executions_list', ready ? { needsYou: true, ...scope } : null, { quiet: 30_000 });
  // One grouped call: each project's per-state counts (groupCounts); tasks without a project are their own group.
  const page = useOp('task_page', ready ? { group: 'project', groupCounts: true, limit: 1, filter: { ...(projectIds && { projectId: projectIds.map(noProject) }), ...(excludeProjectIds && { excludeProjectId: excludeProjectIds.map(noProject) }) } } : null);
  const data: Overview | null = q.data && p.data && page.data ? {
    questions: q.data.questions, problems: p.data.executions, counts: page.data.counts,
    projects: page.data.groups.map(g => ({ id: g.label ?? 'no project', project: g.label ?? null, counts: g.counts ?? {} })),
  } : null;
  const restored = q.restored || p.restored || page.restored;
  const state: Freshness = [q.state, p.state, page.state].find(x => x !== 'current') ?? q.state;
  return { data, error: q.error ?? p.error ?? page.error, state, restored };
}

/** One Needs-you item: an open question or a run that needs a look. */
type Item = { id: string; rank: number; at: number; cause: string; title: string } & ({ q: QuestionRow; e: null } | { q: null; e: ExecutionRow });

// The cause family names a group: the coordinator's problem causes (problem:delivery:…, problem:host:…) and question kinds.
const CAUSES: Record<string, string> = { delivery: 'Delivery got slower', host: 'Hosts', store: 'Stores', absence: 'Coordinator', bugsink: 'Errors' };
function causeOf(q: QuestionRow): string {
  const c = typeof q.cause === 'string' ? q.cause.split(':') : [];
  if (c[0] === 'problem' && c[1]) return CAUSES[c[1]] ?? c[1];
  return q.kind === 'acceptance' ? 'Acceptance' : keyConnection(q.options) ? 'Keys' : 'Decisions';
}
/** A question's headline: its first sentence, links left for the detail (raw URLs used to run past the column). */
export function headline(text: string): string {
  const t = String(text ?? '').replace(/https?:\/\/\S+/g, '').replace(/\s+/g, ' ').trim();
  const m = /^(.+?[.!?])(\s|$)/.exec(t);
  return (m ? m[1] : t).replace(/[.:]\s*$/, '') || 'Question';
}

/** Needs you order (OR-40): urgency first (a blocking question, then a problem, then an acceptance), then the most recent. */
function needsYou(d: Overview, now: number): Item[] {
  const rank = (q: QuestionRow) => (q.kind === 'acceptance' ? 2 : 0);
  return [
    ...d.questions.map(q => { const cause = causeOf(q), t = headline(q.text);
      // A word once per region (OR-17): a title that repeats its group's name drops it ("Delivery got slower: queue P50 …").
      const title = t.toLowerCase().startsWith(cause.toLowerCase() + ':') ? t.slice(cause.length + 1).trim() : t;
      return { id: q.id, q, e: null, rank: rank(q), at: Date.parse(q.askedAt) || now, cause, title }; }),
    ...d.problems.map(e => ({ id: e.id, q: null, e, rank: 1, at: Date.parse(e.lastSeenAt ?? e.startedAt ?? '') || 0, cause: 'Runs', title: e.problem ?? 'Run needs a look' })),
  ].sort((a, b) => a.rank - b.rank || b.at - a.at);
}
/** Groups in the order of their most urgent item; items keep their order inside a group. */
function groupsOf(items: Item[]) {
  const g = new Map<string, Item[]>();
  for (const x of items) g.set(x.cause, [...(g.get(x.cause) ?? []), x]);
  return [...g].map(([cause, xs]) => ({ cause, items: xs }));
}

export function Overview({ id }: { id?: string }) {
  // ?all: the focus not applied; the header's "need you outside focus" opens it (app-shell.md).
  const all = new URLSearchParams(useSearch()).has('all');
  // ?all drops the focus, not the ignore: ignore is full silence (OR-34).
  const scope = scopeIds(!all);
  const alive = useAlive();
  const [, go] = useLocation();
  // Collapsed cause groups, for this visit only: an inbox empties as it is answered.
  const [closed, setClosed] = useState<string[]>([]);
  const { data, state, error } = useOverview(scope);
  // The Hosts page's read (one key, so one fetch serves both); liveness moves without a change row: every 5 s.
  const hostsRead = useOp('hosts_list', {}, { every: 5000 }), hosts = hostsRead.data, offset = useClockOffset(hostsRead);
  const now = Date.now();
  const hostNow = now + offset;
  const keep = all ? '?all' : '';
  const open = (x: string | null) => go(x ? `/overview/${encodeURIComponent(x)}${keep}` : `/overview${keep}`);
  const items = data ? needsYou(data, now) : [], groups = groupsOf(items), sel = items.find(x => x.id === id);
  const rows: { g?: string; n?: number; x?: Item }[] = groups.flatMap(g => [{ g: g.cause, n: g.items.length }, ...(closed.includes(g.cause) ? [] : g.items.map(x => ({ x })))]);
  const fresh = (h: HostRow) => !!h.lastSeenAt && !!hosts && hostNow - h.lastSeenAt < hosts.staleAfterMs;
  const hostList = hosts?.hosts ?? [], online_ = hostList.filter(fresh).length;
  const openProjects = (data?.projects ?? []).filter(p => (p.counts.waiting ?? 0) + (p.counts.failed ?? 0) + (p.counts.in_progress ?? 0) + (p.counts.queued ?? 0) > 0);
  const stats: Stat[] = data ? [
    { label: 'need you', icon: 'bell', value: { value: items.length, basis: 'measured' }, tone: items.length ? 'attention' : undefined },
    ...(hostList.length ? [{ label: 'hosts online', icon: 'panels-top-left', value: { value: online_, basis: 'measured' }, of: hostList.length, tone: online_ < hostList.length ? 'attention' : undefined } satisfies Stat] : []),
    { label: 'failed', icon: 'octagon-x', value: { value: data.counts.failed ?? 0, basis: 'measured' }, tone: data.counts.failed ? 'danger' : undefined, href: '/tasks?state=failed' },
    { label: 'running', icon: 'activity', value: { value: data.counts.in_progress ?? 0, basis: 'measured' }, href: '/tasks?state=in_progress' },
    { label: 'queued', icon: 'list-ordered', value: { value: data.counts.queued ?? 0, basis: 'measured' }, href: '/tasks?state=queued' },
  ] : [];
  const ops = stats.filter(x => (x.value.value ?? 0) > 0);

  const list = <>
    <ShellStatus><OfflineMarker state={state} now={now} /></ShellStatus>
    {all && <header class="page-h"><span class="muted">Focus not applied</span><Button label="Apply the focus" icon="focus" iconOnly kind="quiet" onClick={() => go('/overview')} /></header>}
    <NeedsYou>
      {error && !data ? <ErrorBanner text={`Overview did not load: ${error}`} /> : !data ? <Skeleton rows={6} /> : !items.length ? (
        <Empty text={!all && prefs.value.focus.length ? 'Nothing is waiting for you in the focused projects.' : 'Nothing is waiting for you.'} />
      ) : (
        /* One virtual list (OR-9): a group header per cause, then its items as one-line rows. */
        <VirtualList items={rows} getKey={r => r.g ? 'g:' + r.g : r.x!.id} rowHeight={catalogueRow()} follow="top" label="Needs you"
          columns="1.25rem minmax(0,1fr) auto" rowClass={r => r.g ? 'grp' : r.x!.id === id ? 'sel' : ''}
          render={r => r.g ? <GroupHeader icon={r.g === 'Runs' ? 'triangle-alert' : 'bell'} tone="attention" label={r.g} count={r.n!} open={!closed.includes(r.g)}
            onToggle={() => setClosed(c => c.includes(r.g!) ? c.filter(x => x !== r.g) : [...c, r.g!])} /> : <>
            <span class="lead ny-ic"><Icon name={(r.x!.q ? (r.x!.q.kind === 'acceptance' ? 'circle-check' : 'bell') : 'triangle-alert') as IconName} label={r.x!.q ? 'Question' : 'Problem'} /></span>
            <span class="tt"><a class="title row-link" href={`/overview/${encodeURIComponent(r.x!.id)}${keep}`} onClick={ev => { ev.preventDefault(); open(r.x!.id); }}>{r.x!.title}</a>
              {r.x!.q ? <EntityCode code={r.x!.q.code} id={r.x!.q.id} kind="question" /> : <span class="id">{[r.x!.e.hostId, r.x!.e.harness].filter(Boolean).join(' · ')}</span>}</span>
            <Ago t={r.x!.at} />
          </>} />
      )}
    </NeedsYou>
  </>;

  const detail = !data && id ? (
    <article class="detail" data-ui="detail" tabIndex={-1}>
      <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => open(null)} /></header>
      {error ? <ErrorBanner text={`Overview did not load: ${error}`} /> : <Skeleton rows={6} />}
    </article>
  ) : sel && <article class="detail" data-ui="detail" tabIndex={-1} aria-label={sel.title}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => open(null)} />
      {sel.q ? <StateBadge family="question" state="open" /> : <Icon name="triangle-alert" label="Problem" />}
      {sel.q && <EntityCode code={sel.q.code} id={sel.q.id} kind="question" />}<span class="muted">{[(sel.q ?? sel.e).projectId, sel.cause].filter(Boolean).join(' · ')}</span>
      <span class="grow" /><AgeChip t={sel.at} /></header>
    <h2 class="detail-title">{sel.title}</h2>
    {sel.q ? <DecisionBlock question={sel.q.text} askedBy={actor(sel.q.askedBy) || sel.q.kind} addressedTo="you" since={sel.at} now={now}
      waits={sel.q.taskId ? <EntityCode id={sel.q.taskId} kind="task" /> : undefined}
      options={(Array.isArray(sel.q.options) ? sel.q.options as unknown[] : sel.q.kind === 'acceptance' ? ['accepted', 'rejected'] : keyConnection(sel.q.options) ? ['decline'] : []).map((o, i) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
      onAnswer={a => { if (online.value) writeOnce('questions_answer', { questionId: sel.q!.id, answer: a }, () => {}, { signal: alive }).then(() => open(null), () => { /* the question stays open */ }); }}
      onSecret={keyConnection(sel.q.options) ? v => addRequestedKey(sel.q!.id, keyConnection(sel.q!.options)!, v, alive) : undefined} />
      : <ProblemRow severity="warning" title={sel.e.problem ?? 'Run needs a look'} facts={[sel.e.hostId, sel.e.harness].filter(Boolean).join(' · ')}
        actions={sel.e.taskId ? [{ label: 'Open task', onClick: () => go(`/tasks/${encodeURIComponent(sel.e!.taskId!)}`) }] : []} />}
    {(() => { const same = groups.find(g => g.cause === sel.cause)!.items.filter(x => x.id !== sel.id);
      return same.length > 0 && <DetailSection title={`Same cause, ${same.length} more`}>
        <ul class="kv">
          {same.map(x => (
            <li key={x.id}>
              <Icon name={x.q ? 'bell' : 'triangle-alert'} />
              <a class="row-link" href={`/overview/${encodeURIComponent(x.id)}${keep}`} onClick={ev => { ev.preventDefault(); open(x.id); }}>{x.title}</a>
              {x.q && <EntityCode code={x.q.code} id={x.q.id} kind="question" />}
              <span class="grow" />
              <AgeChip t={x.at} />
            </li>
          ))}
        </ul>
      </DetailSection>; })()}
  </article>;

  // Nothing picked: the fleet's state, so the work area is never an empty "pick a row" (2026-11-20 decision).
  const idle = <article class="detail" aria-label="Fleet now">
    <OpsSummary>
      {error && !data ? <ErrorBanner text={`Overview did not load: ${error}`} /> : !data ? <Skeleton rows={6} /> : (
        <>
          {ops.length > 0 && <StatStrip label="Fleet now" stats={ops} />}
          {(hostList.length > 0 || openProjects.length > 0) && (
            <div class="cols">
              {hostList.length > 0 && (
                <DetailSection title="Hosts">
                  {hostList.map(h => { const live = fresh(h); return <KvRow key={h.id} icon="panels-top-left" label={h.id}
                    meta={[h.platform, h.version].filter(Boolean).join(' · ')}
                    value={<><StateBadge family="host" state={live ? 'online' : 'offline'} lastSeen={live ? undefined : h.lastSeenAt ?? undefined} now={hostNow} compact />
                      {live && h.running > 0 && <><Num m={{ value: h.running, basis: 'measured' }} kind="count" /><HarnessCounts counts={h.runningByHarness} label={`${h.id} workers by harness`} /></>}
                      {live && h.queued > 0 && <span class="hc" data-tip="Queued commands"><Icon name="list-ordered" label="Queued commands" /><Num m={{ value: h.queued, basis: 'measured' }} kind="count" /></span>}</>} />; })}
                </DetailSection>
              )}
              {openProjects.length > 0 && (
                <DetailSection title="Projects">
                  {openProjects.map(p => { const counts = { waiting: p.counts.waiting ?? 0, failed: p.counts.failed ?? 0, in_progress: p.counts.in_progress ?? 0, queued: p.counts.queued ?? 0 };
                    return <KvRow key={p.id} icon="folder-open" label={p.id}
                      value={<StateCounters family="task" label={`${p.id} by state`} counts={counts} active={[]} hideZero
                        onAll={() => go(formatView({ view: 'tasks', state: Object.keys(counts), project: [p.project] }))} onToggle={s => go(formatView({ view: 'tasks', state: [s], project: [p.project] }))} />} />; })}
                </DetailSection>
              )}
            </div>
          )}
          <InFlight scope={scope} />
        </>
      )}
    </OpsSummary>
  </article>;

  return <ListDetail list={list} detail={detail || undefined} detailKey={id} idle={idle} />;
}

/** Pull requests in flight: the Delivery page's own read (the same doc, so one fetch serves both), under the focus (OR-34). */
function InFlight({ scope }: { scope: ReturnType<typeof scopeIds> }) {
  const { data, error } = useOp('delivery_status', prefsRead.value ? scope : null, { every: 300000 });
  const moving = (data?.items ?? []).filter(it => it.stage !== 'installed' && it.stage !== 'merged');
  if (error && !data) return <DetailSection title="Delivery in flight"><ErrorBanner text={`Delivery did not load: ${error}`} /></DetailSection>;
  if (!moving.length) return null;
  return <DetailSection title="Delivery in flight">
    {moving.map(it => <KvRow key={it.repo + it.number} icon="git-pull-request" label={it.title}
      meta={<EntityCode code={`#${it.number}`} id={idOf(it)} kind="pull_request" />}
      value={<><StateBadge family="delivery" state={it.stage} compact /><AgeChip t={Date.parse(it.since)} /></>} />)}
  </DetailSection>;
}
