// Overview (docs/design-system/pages/overview.md): what needs the operator, operational counts, workers per host, projects.
import { useLocation } from 'wouter-preact';
import { OpsSummary, NeedsYou, StatStrip, DecisionBlock, ProblemRow, StateBadge, HarnessCounts, Num, StateCounters, Empty, OfflineMarker, Icon, ErrorBanner, Skeleton } from '../../../packages/ui/src/index.ts';
import { op, online } from '../api.ts';
import { useDoc } from '../store.ts';

type Overview = { questions: any[]; problems: any[]; counts: Record<string, number>; hosts: null; projects: { id: string; counts: Record<string, number> }[] };
async function fetchOverview(): Promise<Overview> {
  const [q, p, page] = await Promise.all([op('questions_list', { state: 'open' }), op('executions_list', { problem: true }), op('task_page', { group: 'project', groupCounts: true, limit: 1 })]);
  // One grouped call: each project's per-state counts (groupCounts); tasks without a project are their own group.
  const projects = (page.groups as any[]).map(g => ({ id: g.label ?? 'no project', counts: g.counts }));
  return { questions: q.questions, problems: p.executions, counts: page.counts, hosts: null, projects };
}

export function Overview() {
  const [, go] = useLocation();
  const { data, state, error } = useDoc<Overview>('overview', fetchOverview, c => ['task', 'question', 'execution'].includes(c.kind));
  const hostsRec = useDoc<any>('overview-hosts', async () => ({ ...(await op('hosts_list', {})), fetchedAt: Date.now() }), c => c.kind === 'host' || c.kind === 'execution' || c.kind === 'command', 5000);
  const now = Date.now();
  const hosts = hostsRec.data, hostNow = hosts ? hosts.serverNow + (now - hosts.fetchedAt) : now;
  if (!data) return <><header class="page-h"><h1>Overview</h1></header>{error ? <ErrorBanner text={`Overview did not load: ${error}`} /> : <Skeleton rows={6} />}</>;
  return <>
    <header class="page-h"><h1>Overview</h1><span class="grow" /><OfflineMarker state={state} now={now} /></header>
    <OpsSummary>
      <StatStrip label="Operations" stats={[
        { label: 'failed', icon: 'octagon-x', value: { value: data.counts.failed ?? 0, basis: 'measured' }, tone: data.counts.failed ? 'danger' : undefined, href: '/tasks?state=failed' },
        { label: 'running', icon: 'activity', value: { value: data.counts.in_progress ?? 0, basis: 'measured' }, href: '/tasks?state=in_progress' },
        { label: 'queued', icon: 'list-ordered', value: { value: data.counts.queued ?? 0, basis: 'measured' }, href: '/tasks?state=queued' },
      ]} />
      <div class="cols">
        <NeedsYou>
          {!data.questions.length && !data.problems.length && <Empty text="Nothing is waiting for you." />}
          {data.questions.map(q => <DecisionBlock key={q.id} question={q.text} askedBy={q.askedBy ?? q.kind} addressedTo="you" since={Date.parse(q.askedAt) || now} now={now}
            waits={q.taskId ? `task ${String(q.taskId).slice(0, 8)}` : undefined}
            options={(Array.isArray(q.options) ? q.options : q.kind === 'acceptance' ? ['accepted', 'rejected'] : []).map((o: string, i: number) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
            onAnswer={a => { if (online.value) op('questions_answer', { questionId: q.id, answer: a }); }} />)}
          {data.problems.map(e => <ProblemRow key={e.id} severity="warning" title={e.problem} facts={`execution ${String(e.id).slice(0, 8)}${e.taskId ? ` · task ${String(e.taskId).slice(0, 8)}` : ''}`}
            actions={e.taskId ? [{ label: 'Open task' }] : []} />)}
        </NeedsYou>
        <div class="stack">
          <section class="panel"><header class="panel-h"><h2>Workers per host</h2></header>
            <ul class="kv">{(hosts?.hosts ?? []).map((h: any) => { const fresh = h.lastSeenAt && hostNow - h.lastSeenAt < hosts.staleAfterMs; return <li key={h.id}>
              <StateBadge family="host" state={fresh ? 'online' : 'offline'} lastSeen={fresh ? undefined : h.lastSeenAt ?? undefined} now={hostNow} compact />
              <a class="k row-link" href={`/hosts/${h.id}`} onClick={ev => { ev.preventDefault(); go(`/hosts/${h.id}`); }}>{h.id}</a>
              <span class="v"><Num m={{ value: h.running, basis: 'measured', partial: fresh ? undefined : 'last known' }} kind="count" /></span>
              <HarnessCounts counts={h.runningByHarness} label={`${h.id} workers by harness`} />
              <span class="v muted">{h.queued ? `${h.queued} queued` : ''}</span></li>; })}</ul>
          </section>
          <section class="panel"><header class="panel-h"><h2>Projects</h2></header>
            <ul class="kv">{data.projects.map(p => <li key={p.id}><Icon name="folder-open" /><span class="k">{p.id}</span>
              <StateCounters family="task" label={`${p.id} by state`} counts={{ waiting: p.counts.waiting ?? 0, failed: p.counts.failed ?? 0, in_progress: p.counts.in_progress ?? 0, queued: p.counts.queued ?? 0 }} active={[]}
                onToggle={s => go(`/tasks?state=${s}`)} /></li>)}</ul>
          </section>
        </div>
      </div>
    </OpsSummary>
  </>;
}
