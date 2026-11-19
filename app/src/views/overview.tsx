// Overview (docs/design-system/pages/overview.md): what needs the operator and whether the fleet is healthy. The control
// column is the Needs-you inbox, one row per question or problem, grouped by cause; the work area answers the one picked,
// and shows the fleet's state while none is (2026-11-20 decision, Overview B).
import { useState } from 'preact/hooks';
import { useLocation, useSearch } from 'wouter-preact';
import {
  NeedsYou, OpsSummary, VirtualList, GroupHeader, catalogueRow, StatStrip, DecisionBlock, ProblemRow, StateBadge, HarnessCounts, Num, StateCounters, StageChip, Empty, OfflineMarker, Icon, ErrorBanner, Skeleton, Button,
  ListDetail, DetailSection, KvRow, AgeChip, Ago, keyConnection, actor, ShellStatus, type IconName,
} from '../../../packages/ui/src/index.ts';
import { op, online, addRequestedKey, writeOnce, useAlive } from '../api.ts';
import { useDoc } from '../store.ts';
import { scopeIds, noProject, prefsRead, prefs } from '../prefs.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';
import { idOf, touchesStatus } from './delivery.tsx';

type Overview = { questions: any[]; problems: any[]; counts: Record<string, number>; hosts: null; projects: { id: string; project: string | null; counts: Record<string, number> }[] };
// The focus and ignored projects (OR-34) narrow every part: questions, problems, counts and project rows.
async function fetchOverview(scope: ReturnType<typeof scopeIds>): Promise<Overview> {
  const { projectIds, excludeProjectIds } = scope;
  const [q, p, page] = await Promise.all([op('questions_list', { state: 'open', ...scope }), op('executions_list', { needsYou: true, ...scope }),
    op('task_page', { group: 'project', groupCounts: true, limit: 1, filter: { ...(projectIds && { projectId: projectIds.map(noProject) }), ...(excludeProjectIds && { excludeProjectId: excludeProjectIds.map(noProject) }) } })]);
  // One grouped call: each project's per-state counts (groupCounts); tasks without a project are their own group.
  const projects = (page.groups as any[]).map(g => ({ id: g.label ?? 'no project', project: g.label ?? null, counts: g.counts }));
  return { questions: q.questions, problems: p.executions, counts: page.counts, hosts: null, projects };
}

/** One Needs-you item: an open question or a run that needs a look. */
type Item = { id: string; q: any; e: any; rank: number; at: number; cause: string; title: string };

// The cause family names a group: the coordinator's problem causes (problem:delivery:…, problem:host:…) and question kinds.
const CAUSES: Record<string, string> = { delivery: 'Delivery got slower', host: 'Hosts', store: 'Stores', absence: 'Coordinator', bugsink: 'Errors' };
function causeOf(q: any): string {
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
  const rank = (q: any) => (q.kind === 'acceptance' ? 2 : 0);
  return [
    ...d.questions.map(q => { const cause = causeOf(q), t = headline(q.text);
      // A word once per region (OR-17): a title that repeats its group's name drops it ("Delivery got slower: queue P50 …").
      const title = t.toLowerCase().startsWith(cause.toLowerCase() + ':') ? t.slice(cause.length + 1).trim() : t;
      return { id: q.id, q, e: null as any, rank: rank(q), at: Date.parse(q.askedAt) || now, cause, title }; }),
    ...d.problems.map(e => ({ id: e.id, q: null as any, e, rank: 1, at: Date.parse(e.lastSeenAt ?? e.startedAt) || 0, cause: 'Runs', title: e.problem ?? 'Run needs a look' })),
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
  const { data, state, error } = useDoc<Overview>(prefsRead.value ? 'overview:' + JSON.stringify(scope) : null, () => fetchOverview(scope), c => ['task', 'question', 'execution'].includes(c.kind));
  const hostsRec = useDoc<any>('overview-hosts', async () => ({ ...(await op('hosts_list', {})), fetchedAt: Date.now() }), c => c.kind === 'host' || c.kind === 'execution' || c.kind === 'command', 5000);
  const now = Date.now();
  const hosts = hostsRec.data, hostNow = hosts ? hosts.serverNow + (now - hosts.fetchedAt) : now;
  if (!data) return <>{error ? <ErrorBanner text={`Overview did not load: ${error}`} /> : <Skeleton rows={6} />}</>;
  const keep = all ? '?all' : '';
  const open = (x: string | null) => go(x ? `/overview/${encodeURIComponent(x)}${keep}` : `/overview${keep}`);
  const items = needsYou(data, now), groups = groupsOf(items), sel = items.find(x => x.id === id);
  const rows: { g?: string; n?: number; x?: Item }[] = groups.flatMap(g => [{ g: g.cause, n: g.items.length }, ...(closed.includes(g.cause) ? [] : g.items.map(x => ({ x })))]);
  const fresh = (h: any) => !!h.lastSeenAt && hostNow - h.lastSeenAt < hosts.staleAfterMs;
  const hostList: any[] = hosts?.hosts ?? [], online_ = hostList.filter(fresh).length;
  const ops = ([
    { label: 'need you', icon: 'bell', value: { value: items.length, basis: 'measured' }, tone: items.length ? 'attention' : undefined },
    ...(hostList.length ? [{ label: 'hosts online', icon: 'panels-top-left', value: { value: online_, basis: 'measured' }, of: hostList.length, tone: online_ < hostList.length ? 'attention' : undefined }] : []),
    { label: 'failed', icon: 'octagon-x', value: { value: data.counts.failed ?? 0, basis: 'measured' }, tone: data.counts.failed ? 'danger' : undefined, href: '/tasks?state=failed' },
    { label: 'running', icon: 'activity', value: { value: data.counts.in_progress ?? 0, basis: 'measured' }, href: '/tasks?state=in_progress' },
    { label: 'queued', icon: 'list-ordered', value: { value: data.counts.queued ?? 0, basis: 'measured' }, href: '/tasks?state=queued' },
  ] as const).filter(x => x.value.value > 0);

  const list = <>
    <ShellStatus><OfflineMarker state={state} now={now} /></ShellStatus>
    {all && <header class="page-h"><span class="muted">Focus not applied</span><Button label="Apply the focus" icon="focus" iconOnly kind="quiet" onClick={() => go('/overview')} /></header>}
    <NeedsYou>
      {!items.length && <Empty text={!all && prefs.value.focus.length ? 'Nothing is waiting for you in the focused projects.' : 'Nothing is waiting for you.'} />}
      {/* One virtual list (OR-9): a group header per cause, then its items as one-line rows. */}
      {items.length > 0 && <VirtualList items={rows} getKey={r => r.g ? 'g:' + r.g : r.x!.id} rowHeight={catalogueRow()} follow="top" label="Needs you"
        columns="1.25rem minmax(0,1fr) auto" rowClass={r => r.g ? 'grp' : r.x!.id === id ? 'sel' : ''}
        render={r => r.g ? <GroupHeader icon={r.g === 'Runs' ? 'triangle-alert' : 'bell'} tone="attention" label={r.g} count={r.n!} open={!closed.includes(r.g)}
          onToggle={() => setClosed(c => c.includes(r.g!) ? c.filter(x => x !== r.g) : [...c, r.g!])} /> : <>
          <span class="lead ny-ic"><Icon name={(r.x!.q ? (r.x!.q.kind === 'acceptance' ? 'circle-check' : 'bell') : 'triangle-alert') as IconName} label={r.x!.q ? 'Question' : 'Problem'} /></span>
          <span class="tt"><a class="title row-link" href={`/overview/${encodeURIComponent(r.x!.id)}${keep}`} onClick={ev => { ev.preventDefault(); open(r.x!.id); }}>{r.x!.title}</a>
            <span class="id">{r.x!.q?.code ?? [r.x!.e?.hostId, r.x!.e?.harness].filter(Boolean).join(' · ')}</span></span>
          <Ago t={r.x!.at} />
        </>} />}
    </NeedsYou>
  </>;

  const detail = sel && <article class="detail" data-ui="detail" tabIndex={-1} aria-label={sel.title}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => open(null)} />
      {sel.q ? <StateBadge family="question" state="open" /> : <Icon name="triangle-alert" label="Problem" />}
      {sel.q?.code && <span class="mono muted">{sel.q.code}</span>}<span class="muted">{[sel.q?.projectId ?? sel.e?.projectId, sel.cause].filter(Boolean).join(' · ')}</span>
      <span class="grow" /><AgeChip t={sel.at} /></header>
    <h2 class="detail-title">{sel.title}</h2>
    {sel.q ? <DecisionBlock question={sel.q.text} askedBy={actor(sel.q.askedBy) || sel.q.kind} addressedTo="you" since={sel.at} now={now}
      waits={sel.q.taskId ? <a class="row-link" href={`/tasks/${encodeURIComponent(sel.q.taskId)}`} aria-label="Open task" data-tip="Open task" onClick={ev => { ev.preventDefault(); go(`/tasks/${encodeURIComponent(sel.q.taskId)}`); }}><Icon name="external-link" label="Open task" /></a> : undefined}
      options={(Array.isArray(sel.q.options) ? sel.q.options : sel.q.kind === 'acceptance' ? ['accepted', 'rejected'] : keyConnection(sel.q.options) ? ['decline'] : []).map((o: string, i: number) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
      onAnswer={a => { if (online.value) writeOnce('questions_answer', { questionId: sel.q.id, answer: a }, () => {}, { signal: alive }).then(() => open(null), () => { /* the question stays open */ }); }}
      onSecret={keyConnection(sel.q.options) ? v => addRequestedKey(sel.q.id, keyConnection(sel.q.options)!, v, alive) : undefined} />
      : <ProblemRow severity="warning" title={sel.e.problem} facts={[sel.e.hostId, sel.e.harness].filter(Boolean).join(' · ')}
        actions={sel.e.taskId ? [{ label: 'Open task', onClick: () => go(`/tasks/${encodeURIComponent(sel.e.taskId)}`) }] : []} />}
    {(() => { const same = groups.find(g => g.cause === sel.cause)!.items.filter(x => x.id !== sel.id);
      return same.length > 0 && <DetailSection title={`Same cause, ${same.length} more`}>
        {same.map(x => <KvRow key={x.id} icon={x.q ? 'bell' : 'triangle-alert'} label={x.title} meta={x.q?.code} value={<Button label="Open" kind="quiet" onClick={() => open(x.id)} />} />)}
      </DetailSection>; })()}
  </article>;

  // Nothing picked: the fleet's state, so the work area is never an empty "pick a row" (2026-11-20 decision).
  const idle = <article class="detail" aria-label="Fleet now">
    <OpsSummary>
    {ops.length > 0 && <StatStrip label="Fleet now" stats={ops as any} />}
    <div class="cols">
      <DetailSection title="Hosts">
        {hostList.map((h: any) => { const live = fresh(h); return <KvRow key={h.id} icon="panels-top-left" label={h.id}
          meta={[h.platform, h.version].filter(Boolean).join(' · ')}
          value={<><StateBadge family="host" state={live ? 'online' : 'offline'} lastSeen={live ? undefined : h.lastSeenAt ?? undefined} now={hostNow} compact />
            {live && h.running > 0 && <><Num m={{ value: h.running, basis: 'measured' }} kind="count" /><HarnessCounts counts={h.runningByHarness} label={`${h.id} workers by harness`} /></>}
            {live && h.queued > 0 && <span class="muted">{h.queued} queued</span>}</>} />; })}
      </DetailSection>
      <DetailSection title="Projects">
        {data.projects.map(p => { const counts = { waiting: p.counts.waiting ?? 0, failed: p.counts.failed ?? 0, in_progress: p.counts.in_progress ?? 0, queued: p.counts.queued ?? 0 };
          const total = Object.values(p.counts).reduce((n, v) => n + (v ?? 0), 0);
          return <KvRow key={p.id} icon="folder-open" label={p.id} meta={Object.values(counts).some(n => n > 0) ? undefined : `${total} tasks, none open`}
            value={Object.values(counts).some(n => n > 0) ? <StateCounters family="task" label={`${p.id} by state`} counts={counts} active={[]} hideZero
              onAll={() => go(formatView({ view: 'tasks', state: Object.keys(counts), project: [p.project] }))} onToggle={s => go(formatView({ view: 'tasks', state: [s], project: [p.project] }))} /> : undefined} />; })}
      </DetailSection>
    </div>
    <InFlight scope={scope} onOpen={go} />
    </OpsSummary>
  </article>;

  return <ListDetail list={list} detail={detail || undefined} detailKey={id} idle={idle} />;
}

/** Pull requests in flight: the Delivery page's own read (the same doc, so one fetch serves both), under the focus (OR-34). */
function InFlight({ scope, onOpen }: { scope: ReturnType<typeof scopeIds>; onOpen: (path: string) => void }) {
  const { data, error } = useDoc<any>(prefsRead.value ? 'delivery:' + JSON.stringify(scope) : null, () => op('delivery_status', scope), touchesStatus, 300000);
  const moving = (data?.items ?? []).filter((it: any) => it.stage !== 'installed' && it.stage !== 'merged');
  if (error && !data) return <DetailSection title="Delivery in flight"><ErrorBanner text={`Delivery did not load: ${error}`} /></DetailSection>;
  if (!moving.length) return null;
  return <DetailSection title="Delivery in flight">
    {moving.map((it: any) => { const path = `/delivery/${idOf(it)}`; return <KvRow key={it.repo + it.number} icon="git-pull-request" label={it.title}
      meta={<a class="row-link mono" href={path} onClick={e => { e.preventDefault(); onOpen(path); }}>#{it.number}</a>}
      value={<><StageChip stage={it.stage} /><AgeChip t={Date.parse(it.since)} /></>} />; })}
  </DetailSection>;
}
