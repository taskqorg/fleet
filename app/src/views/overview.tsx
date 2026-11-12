// Overview (docs/design-system/pages/overview.md): what needs the operator, operational counts, workers per host, projects.
import { useLocation } from 'wouter-preact';
import { OpsSummary, NeedsYou, Latest, StatStrip, DecisionBlock, ProblemRow, StateBadge, HarnessCounts, Num, StateCounters, Empty, OfflineMarker, Icon, ErrorBanner, Skeleton, keyConnection, actor } from '../../../packages/ui/src/index.ts';
import { op, online, addRequestedKey, writeOnce, useAlive } from '../api.ts';
import { useDoc } from '../store.ts';
import { focusIds } from '../prefs.ts';
import { formatView } from '../../../packages/view-url/view-url.ts';

type Overview = { questions: any[]; problems: any[]; counts: Record<string, number>; hosts: null; projects: { id: string; project: string | null; counts: Record<string, number> }[] };
// The focus (OR-34) narrows every part: questions, problems, counts and project rows.
async function fetchOverview(projectIds?: string[]): Promise<Overview> {
  const [q, p, page] = await Promise.all([op('questions_list', { state: 'open', projectIds }), op('executions_list', { needsYou: true, projectIds }),
    op('task_page', { group: 'project', groupCounts: true, limit: 1, filter: projectIds ? { projectId: projectIds.map(x => x === '-' ? null : x) } : {} })]);
  // One grouped call: each project's per-state counts (groupCounts); tasks without a project are their own group.
  const projects = (page.groups as any[]).map(g => ({ id: g.label ?? 'no project', project: g.label ?? null, counts: g.counts }));
  return { questions: q.questions, problems: p.executions, counts: page.counts, hosts: null, projects };
}

export function Overview() {
  const focus = focusIds();
  const alive = useAlive();
  const [, go] = useLocation();
  const { data, state, error } = useDoc<Overview>('overview:' + (focus ?? []).join(','), () => fetchOverview(focus), c => ['task', 'question', 'execution'].includes(c.kind));
  const hostsRec = useDoc<any>('overview-hosts', async () => ({ ...(await op('hosts_list', {})), fetchedAt: Date.now() }), c => c.kind === 'host' || c.kind === 'execution' || c.kind === 'command', 5000);
  const now = Date.now();
  const hosts = hostsRec.data, hostNow = hosts ? hosts.serverNow + (now - hosts.fetchedAt) : now;
  if (!data) return <><header class="page-h"><h1>Overview</h1></header>{error ? <ErrorBanner text={`Overview did not load: ${error}`} /> : <Skeleton rows={6} />}</>;
  const ops = ([
        { label: 'failed', icon: 'octagon-x', value: { value: data.counts.failed ?? 0, basis: 'measured' }, tone: data.counts.failed ? 'danger' : undefined, href: '/tasks?state=failed' },
        { label: 'running', icon: 'activity', value: { value: data.counts.in_progress ?? 0, basis: 'measured' }, href: '/tasks?state=in_progress' },
        { label: 'queued', icon: 'list-ordered', value: { value: data.counts.queued ?? 0, basis: 'measured' }, href: '/tasks?state=queued' },
  ] as const).filter(x => x.value.value > 0);
  return <>
    <header class="page-h"><h1>Overview</h1><span class="grow" /><OfflineMarker state={state} now={now} /></header>
    <OpsSummary>
      {ops.length > 0 && <StatStrip label="Operations" stats={ops} />}
      <div class="cols">
        <NeedsYou>
          {!data.questions.length && !data.problems.length && <Empty text="Nothing is waiting for you." />}
          <Latest items={needsYou(data, now).map(x => x.q ? <DecisionBlock key={x.q.id} question={x.q.text} askedBy={actor(x.q.askedBy) || x.q.kind} addressedTo="you" since={x.at} now={now}
            waits={x.q.taskId ? <a class="row-link" href={`/tasks/${encodeURIComponent(x.q.taskId)}`} aria-label="Open task" data-tip="Open task" onClick={ev => { ev.preventDefault(); go(`/tasks/${encodeURIComponent(x.q.taskId)}`); }}><Icon name="external-link" /></a> : undefined}
            options={(Array.isArray(x.q.options) ? x.q.options : x.q.kind === 'acceptance' ? ['accepted', 'rejected'] : keyConnection(x.q.options) ? ['decline'] : []).map((o: string, i: number) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
            onAnswer={a => { if (online.value) writeOnce('questions_answer', { questionId: x.q.id, answer: a }, () => {}, { signal: alive }).catch(() => { /* the question stays open in the list */ }); }}
            onSecret={keyConnection(x.q.options) ? v => addRequestedKey(x.q.id, keyConnection(x.q.options)!, v, alive) : undefined} />
            : <ProblemRow key={x.e.id} severity="warning" title={x.e.problem} facts={[x.e.hostId, x.e.harness].filter(Boolean).join(' · ')}
              actions={x.e.taskId ? [{ label: 'Open task', onClick: () => go(`/tasks/${encodeURIComponent(x.e.taskId)}`) }] : []} />)} />
        </NeedsYou>
        <div class="stack">
          <section class="panel"><header class="panel-h"><h2>Workers per host</h2></header>
            <ul class="kv">{(hosts?.hosts ?? []).map((h: any) => { const fresh = h.lastSeenAt && hostNow - h.lastSeenAt < hosts.staleAfterMs; return <li key={h.id}>
              <StateBadge family="host" state={fresh ? 'online' : 'offline'} lastSeen={fresh ? undefined : h.lastSeenAt ?? undefined} now={hostNow} compact />
              <a class="k row-link" href={`/hosts/${h.id}`} onClick={ev => { ev.preventDefault(); go(`/hosts/${h.id}`); }}>{h.id}</a>
              {fresh && h.running > 0 && <><span class="v sum"><Num m={{ value: h.running, basis: 'measured' }} kind="count" /></span>
                <HarnessCounts counts={h.runningByHarness} label={`${h.id} workers by harness`} /></>}
              {fresh && h.queued > 0 && <span class="v muted">{h.queued} queued</span>}</li>; })}</ul>
          </section>
          <section class="panel"><header class="panel-h"><h2>Projects</h2></header>
            <ul class="kv">{data.projects.map(p => { const counts = { waiting: p.counts.waiting ?? 0, failed: p.counts.failed ?? 0, in_progress: p.counts.in_progress ?? 0, queued: p.counts.queued ?? 0 };
              return <li key={p.id}><Icon name="folder-open" /><span class="k">{p.id}</span>
                {Object.values(counts).some(n => n > 0) && <StateCounters family="task" label={`${p.id} by state`} counts={counts} active={[]} hideZero
                  onAll={() => go(formatView({ view: 'tasks', state: Object.keys(counts), project: [p.project] }))} onToggle={s => go(formatView({ view: 'tasks', state: [s], project: [p.project] }))} />}</li>; })}</ul>
          </section>
        </div>
      </div>
    </OpsSummary>
  </>;
}

/** Needs you order (OR-40): urgency first (a blocking question, then a problem, then an acceptance), then the most recent. */
function needsYou(d: Overview, now: number) {
  const rank = (q: any) => (q.kind === 'acceptance' ? 2 : 0);
  return [
    ...d.questions.map(q => ({ q, e: null as any, rank: rank(q), at: Date.parse(q.askedAt) || now })),
    ...d.problems.map(e => ({ q: null as any, e, rank: 1, at: Date.parse(e.lastSeenAt ?? e.startedAt) || 0 })),
  ].sort((a, b) => a.rank - b.rank || b.at - a.at);
}
