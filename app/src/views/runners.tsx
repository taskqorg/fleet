// Runners (docs/design-system/pages/runners.md): everything that executes work, grouped by host, each run on a 24-hour
// time strip with its time and resources; spend today and this month per host and kind. Data: executions_runners.
import { useLocation } from 'wouter-preact';
import { StateBadge, HarnessMark, Num, CostFigures, Totals, EntityCode, AgeChip, TimeStrip, Empty, ErrorBanner, Skeleton, OfflineMarker, Icon, harnesses, type Harness, type Measured, type Kind, type TotalItem } from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useDoc } from '../store.ts';
import { mergeCosts } from './resources.tsx';

const DAY = 864e5;
const isActive = (r: any) => r.state === 'preparing' || r.state === 'running' || r.state === 'waiting';
const runsOf = (s: any) => s.runs;
const KINDS: Record<string, string> = { agent_run: 'Agent runs', child_agent: 'Child agents', host_job: 'Host jobs', tool_run: 'Tool calls' };
const m = (v: number | null | undefined, basis: Measured['basis'] = 'measured'): Measured => ({ value: v ?? null, basis: v == null ? 'unavailable' : basis });
/** A spend sum: partial when some runs in the group have no recorded totals. */
const sum = (s: any, v: number | null): Measured => ({ ...m(v), partial: v != null && s.measured < s.runs ? `${s.runs - s.measured} runs without totals` : undefined });
const hostName = (h: string | null) => h ?? 'coordinator';

function spendTotals(spend: any[]): TotalItem[] {
  const sumPeriod = (period: 'today' | 'month', label: string): TotalItem[] => {
    const rows = spend.filter((s: any) => s.period === period);
    if (!rows.length) return [];
    const runs = rows.reduce((n, r) => n + (r.runs ?? 0), 0);
    const measured = rows.reduce((n, r) => n + (r.measured ?? 0), 0);
    const tokens = rows.reduce((n, r) => n + (r.tokens ?? 0), 0);
    const partial = measured < runs ? `${runs - measured} runs without totals` : undefined;
    const cost = mergeCosts(rows.map((r: any) => r.cost ?? []));
    return [
      { label: `${label} runs`, m: { value: runs, basis: 'measured' }, kind: 'count' },
      { label: `${label} tokens`, m: { value: tokens, basis: 'measured', partial }, kind: 'count', note: partial },
      ...(cost.length ? [{ label: `${label} cost`, cost, showUnavailable: false }] : []),
    ];
  };
  return [...sumPeriod('today', 'Today'), ...sumPeriod('month', 'Month')];
}

export function Runners() {
  const [, go] = useLocation();
  const { data, state, error } = useDoc<any>('runners', async () => ({ ...(await op('executions_runners', {})), fetchedAt: Date.now() }), () => false, 15000); // timer only: gateway calls change executions many times a second
  if (!data) return <><header class="page-h"><h1>Runners</h1></header>{error ? <ErrorBanner text={`Runners did not load: ${error}`} /> : <Skeleton rows={6} />}</>;
  const now = data.serverNow + (Date.now() - data.fetchedAt), from = now - DAY;
  const hosts = [...new Set<string | null>([...data.runs.map((r: any) => r.hostId), ...data.spend.map((s: any) => s.hostId)])]
    .sort((a, b) => (a === null ? 1 : b === null ? -1 : a.localeCompare(b)));
  const cell = (s: any | undefined, k: Kind, v: (s: any) => number | null) => <td class="num">{s ? <Num m={v === runsOf ? m(v(s)) : sum(s, v(s))} kind={k} /> : <span class="muted">—</span>}</td>;
  const money = (s: any | undefined) => <td class="num"><CostFigures cost={s?.cost} showUnavailable={false} empty="—" /></td>;
  const open = (r: any) => r.taskId ? `/tasks/${r.taskId}` : r.sessionId ? `/conversations/${r.sessionId}` : null;
  const summary = spendTotals(data.spend);
  return <section class="runners" aria-label="Runners">
    <header class="page-h"><h1>Runners</h1><span class="grow" /><OfflineMarker state={state} now={now} /></header>
    <section class="panel" aria-label="Spend">
      <header class="panel-h"><h2>Spend</h2><span class="muted">UTC</span></header>
      {summary.length > 0 && <div class="stack-pad"><Totals items={summary} /></div>}
      <div class="table-frame"><table class="limit-table">
        <thead><tr><th scope="col">Host</th><th scope="col">Kind</th>
          <th scope="col">Runs today</th><th scope="col">Time today</th><th scope="col">Tokens today</th><th scope="col">Cost today</th>
          <th scope="col">Runs month</th><th scope="col">Time month</th><th scope="col">Tokens month</th><th scope="col">Cost month</th></tr></thead>
        <tbody>{hosts.flatMap(h => [...new Set<string>(data.spend.filter((s: any) => s.hostId === h).map((s: any) => s.kind))].sort().map((k, i) => {
          const t = data.spend.find((s: any) => s.hostId === h && s.kind === k && s.period === 'today'), mo = data.spend.find((s: any) => s.hostId === h && s.kind === k && s.period === 'month');
          return <tr key={`${h}:${k}`}><th scope="row">{i === 0 ? <span class="title">{hostName(h)}</span> : ''}</th><td>{KINDS[k] ?? k}</td>
            {cell(t, 'count', runsOf)}{cell(t, 'duration', s => s.wallMs == null ? null : s.wallMs / 1000)}{cell(t, 'count', s => s.tokens)}{money(t)}
            {cell(mo, 'count', runsOf)}{cell(mo, 'duration', s => s.wallMs == null ? null : s.wallMs / 1000)}{cell(mo, 'count', s => s.tokens)}{money(mo)}</tr>;
        }))}</tbody>
      </table></div>
    </section>
    {hosts.map(h => {
      const runs = data.runs.filter((r: any) => r.hostId === h);
      if (!runs.length) return null;
      const live = runs.filter(isActive).length;
      return <section key={hostName(h)} class="panel" aria-label={hostName(h)}>
        <header class="panel-h"><h2>{h ? <a class="row-link" href={`/hosts/${h}`} onClick={e => { e.preventDefault(); go(`/hosts/${h}`); }}>{h}</a> : hostName(h)}</h2>
          {live > 0 && <span class="hc" data-tip="Running"><Icon name="activity" label="Running" /><Num m={m(live)} kind="count" /></span>}<span class="grow" /><span class="muted">24h</span></header>
        <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) minmax(6rem,14rem) auto' }}>
          {runs.map((r: any) => {
            const href = open(r), start = r.startedAt ?? r.endedAt ?? now, title = r.taskTitle ?? KINDS[r.kind] ?? r.kind;
            // A finished run with no recorded end (a gateway call) ends at start plus its wall time, else at its start.
            const active = isActive(r), end = active ? null : r.endedAt ?? start + (r.wallMs ?? 0);
            // An abandoned run ended when given up: its length is unknown (the core leaves its wall time unavailable).
            const secs = r.wallMs != null ? r.wallMs / 1000 : r.endedAt != null && r.problem !== 'unknown:abandoned' ? (r.endedAt - start) / 1000 : null;
            return <div key={r.id} role="row" class="vl-row static">
              <span class="lead"><StateBadge family="execution" state={r.state} compact /></span>
              <span class="tt">{href ? <a class="title row-link" href={href} onClick={e => { e.preventDefault(); go(href); }}>{title}</a> : <span class="title">{title}</span>}
                {r.parentExecutionId && <EntityCode id={r.parentExecutionId} what="Parent execution" />}
                {r.harness && r.harness in harnesses && <HarnessMark h={r.harness as Harness} />}
                {r.calls != null && <span class="hc" data-tip="Tool calls"><Num m={m(r.calls)} kind="count" /></span>}
                {r.failedCalls > 0 && <span class="chip t-danger" data-tip="Failed calls"><Icon name="octagon-x" label="Failed calls" /><Num m={m(r.failedCalls)} kind="count" /></span>}
                {r.problem && <span class="chip t-danger" data-tip={r.problem}><Icon name="triangle-alert" label="Problem" /></span>}
                {r.tokens != null && <span class="hc" data-tip="Tokens"><Num m={m(r.tokens)} kind="count" /></span>}
                {r.cpuMs != null && <span class="hc" data-tip="CPU"><Icon name="gauge" label="CPU" /><Num m={m(r.cpuMs / 1000)} kind="duration" /></span>}
                {r.peakMemoryBytes != null && <span class="hc" data-tip="Peak memory"><Num m={m(r.peakMemoryBytes)} kind="bytes" /></span>}
                {r.cost?.length > 0 && <CostFigures cost={r.cost} />}</span>
              <TimeStrip from={from} to={now} start={start} end={end} now={now} state={r.state} label={`${r.state}, started ${new Date(start).toISOString()}`} />
              <span data-tip={active ? 'Running for' : 'Duration'}>{!active ? <Num m={m(secs)} kind="duration" /> : <AgeChip t={start} now={now} alertAfter={2 * 3600} tone="attention" alertLabel="running long" />}</span>
            </div>;
          })}
        </div>
      </section>;
    })}
    {!data.runs.length && <Empty text="Nothing ran in the last 24 hours." />}
    <Ci now={now} />
  </section>;
}

const JOB_STATE: Record<string, 'running' | 'preparing' | 'waiting'> = { in_progress: 'running', queued: 'preparing', waiting: 'waiting', pending: 'preparing', requested: 'preparing' };

/** GitHub Actions for taskqorg/fleet (executions_ci, cached on the coordinator): minutes today per runner type, the
 * self-hosted runners, and jobs in flight or queued on a 24-hour strip. */
function Ci({ now }: { now: number }) {
  const { data, error } = useDoc<any>('runners-ci', async () => await op('executions_ci', {}), () => false, 60000);
  const from = now - DAY;
  if (!data) return <section class="panel" aria-label="CI"><header class="panel-h"><h2>CI</h2></header>{error ? <ErrorBanner text={`CI did not load: ${error}`} /> : <Skeleton rows={3} />}</section>;
  const partial = data.runsUncounted > 0 ? `${data.runsUncounted} runs not read yet` : undefined;
  return <section class="panel" aria-label="CI">
    <header class="panel-h"><h2>CI</h2><Icon name="github" label="GitHub Actions" /><span class="id">taskqorg/fleet</span>
      {!data.keyed && <span class="chip" data-tip="No GitHub key: public reads, refreshed every 5 minutes; self-hosted runner state unknown">public</span>}
      {data.problems.length > 0 && <span class="chip t-danger" data-tip={data.problems.join('; ')}><Icon name="triangle-alert" label="GitHub problems" /></span>}
      <span class="grow" /><span class="muted">today UTC</span></header>
    <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) auto auto' }}>
      {data.minutesToday.map((x: any) => {
        const r = (data.runners ?? []).find((y: any) => y.name === x.runner);
        return <div key={x.runner} role="row" class="vl-row static">
          <span class="lead">{x.hosted ? <Icon name="github" label="GitHub-hosted" /> : <Icon name="panels-top-left" label="Self-hosted" />}</span>
          <span class="tt"><span class="title">{x.runner}</span>{!x.hosted && <span class="muted">{r ? `${r.status}${r.busy ? ', busy' : ''}` : data.runners ? 'removed runner' : 'state unknown'}</span>}</span>
          <span data-tip="Jobs"><Num m={m(x.jobs)} kind="count" /></span>
          <span data-tip="Minutes"><Num m={{ ...m(x.minutes * 60), partial }} kind="duration" /></span>
        </div>;
      })}
      {(data.runners ?? []).filter((r: any) => !data.minutesToday.some((x: any) => x.runner === r.name)).map((r: any) => <div key={r.name} role="row" class="vl-row static">
        <span class="lead"><Icon name="panels-top-left" label="Self-hosted" /></span><span class="tt"><span class="title">{r.name}</span><span class="muted">{r.status}{r.busy ? ', busy' : ''}</span></span><span /><span class="muted">idle today</span></div>)}
      {data.jobs.map((j: any) => { const start = j.startedAt ?? j.createdAt ?? now, st = JOB_STATE[j.status] ?? 'waiting';
        return <div key={j.id} role="row" class="vl-row static">
          <span class="lead"><StateBadge family="execution" state={st} compact /></span>
          <span class="tt"><a class="title row-link" href={j.url} target="_blank" rel="noopener">{j.name}</a><span class="id">{j.workflow}</span><span class="muted">{j.status === 'in_progress' ? j.runner : `not started · ${j.runner}`}</span></span>
          <TimeStrip from={from} to={now} start={start} end={null} now={now} state={st} label={`${j.status}, since ${new Date(start).toISOString()}`} />
          <span data-tip={j.status === 'queued' ? 'Queued for' : 'Running for'}><AgeChip t={start} now={now} alertAfter={3600} tone="attention" alertLabel="long" /></span>
        </div>; })}
    </div>
    {!data.jobs.length && !data.minutesToday.length && <Empty text="No CI jobs today." />}
  </section>;
}
