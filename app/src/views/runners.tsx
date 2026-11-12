// Runners (docs/design-system/pages/runners.md): everything that executes work, grouped by host, each run on a 24-hour
// time strip with its time and resources; spend today and this month per host and kind. Data: executions_runners.
import { useLocation } from 'wouter-preact';
import { StateBadge, HarnessMark, Num, AgeChip, TimeStrip, Empty, ErrorBanner, Skeleton, OfflineMarker, Icon, harnesses, type Harness, type Measured, type Kind } from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useDoc } from '../store.ts';

const DAY = 864e5;
const runsOf = (s: any) => s.runs;
const KINDS: Record<string, string> = { agent_run: 'Agent runs', child_agent: 'Child agents', host_job: 'Host jobs', tool_run: 'Tool calls' };
const m = (v: number | null | undefined, basis: Measured['basis'] = 'measured'): Measured => ({ value: v ?? null, basis: v == null ? 'unavailable' : basis });
/** The money of an estimateCost list (USD first, else the first priced currency): estimated when priced, unavailable when no
 * usage has a price, none when nothing was used. ponytail: a second currency is not shown; split the column when one appears. */
const costOf = (cost: any[]): Measured | null => {
  if (!cost.length) return null;
  const priced = cost.find(c => c.currency === 'USD' && c.value != null) ?? cost.find(c => c.value != null), unknown = cost.some(c => c.value == null);
  return priced ? { value: priced.value, basis: 'estimated', currency: priced.currency, partial: unknown ? 'some usage has no price' : undefined } : { value: null, basis: 'unavailable', reason: 'no price entry' };
};
/** A spend sum: partial when some runs in the group have no recorded totals. */
const sum = (s: any, v: number | null): Measured => ({ ...m(v), partial: v != null && s.measured < s.runs ? `${s.runs - s.measured} runs without totals` : undefined });
const hostName = (h: string | null) => h ?? 'coordinator';

export function Runners() {
  const [, go] = useLocation();
  const { data, state, error } = useDoc<any>('runners', async () => ({ ...(await op('executions_runners', {})), fetchedAt: Date.now() }), c => c.kind === 'execution', 15000);
  if (!data) return <><header class="page-h"><h1>Runners</h1></header>{error ? <ErrorBanner text={`Runners did not load: ${error}`} /> : <Skeleton rows={6} />}</>;
  const now = data.serverNow + (Date.now() - data.fetchedAt), from = now - DAY;
  const hosts = [...new Set<string | null>([...data.runs.map((r: any) => r.hostId), ...data.spend.map((s: any) => s.hostId)])]
    .sort((a, b) => (a === null ? 1 : b === null ? -1 : a.localeCompare(b)));
  const cell = (s: any | undefined, k: Kind, v: (s: any) => number | null) => <td class="num">{s ? <Num m={v === runsOf ? m(v(s)) : sum(s, v(s))} kind={k} /> : <span class="muted">—</span>}</td>;
  const money = (s: any | undefined) => { const c = s && costOf(s.cost); return <td class="num">{c ? <Num m={c} kind="money" /> : <span class="muted">—</span>}</td>; };
  const open = (r: any) => r.taskId ? `/tasks/${r.taskId}` : r.sessionId ? `/conversations/${r.sessionId}` : null;
  return <section class="runners" aria-label="Runners">
    <header class="page-h"><h1>Runners</h1><span class="grow" /><OfflineMarker state={state} now={now} /></header>
    <section class="panel" aria-label="Spend">
      <header class="panel-h"><h2>Spend</h2><span class="muted">UTC</span></header>
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
      const live = runs.filter((r: any) => !r.endedAt).length;
      return <section key={hostName(h)} class="panel" aria-label={hostName(h)}>
        <header class="panel-h"><h2>{h ? <a class="row-link" href={`/hosts/${h}`} onClick={e => { e.preventDefault(); go(`/hosts/${h}`); }}>{h}</a> : hostName(h)}</h2>
          {live > 0 && <span class="hc" data-tip="Running"><Icon name="activity" label="Running" /><Num m={m(live)} kind="count" /></span>}<span class="grow" /><span class="muted">24h</span></header>
        <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) minmax(6rem,14rem) auto' }}>
          {runs.map((r: any) => {
            const href = open(r), start = r.startedAt ?? r.endedAt ?? now, cost = costOf(r.cost), title = r.taskTitle ?? KINDS[r.kind] ?? r.kind;
            const secs = r.wallMs != null ? r.wallMs / 1000 : ((r.endedAt ?? now) - start) / 1000;
            return <div key={r.id} role="row" class="vl-row static">
              <span class="lead"><StateBadge family="execution" state={r.state} compact /></span>
              <span class="tt">{href ? <a class="title row-link" href={href} onClick={e => { e.preventDefault(); go(href); }}>{title}</a> : <span class="title">{title}</span>}
                <span class="id">{r.id.slice(0, 12)}</span>
                {r.harness && r.harness in harnesses && <HarnessMark h={r.harness as Harness} />}
                {r.problem && <span class="chip t-danger" data-tip={r.problem}><Icon name="triangle-alert" label="Problem" /></span>}
                {r.tokens != null && <span class="hc" data-tip="Tokens"><Num m={m(r.tokens)} kind="count" /></span>}
                {r.cpuMs != null && <span class="hc" data-tip="CPU"><Icon name="gauge" label="CPU" /><Num m={m(r.cpuMs / 1000)} kind="duration" /></span>}
                {r.peakMemoryBytes != null && <span class="hc" data-tip="Peak memory"><Num m={m(r.peakMemoryBytes)} kind="bytes" /></span>}
                {cost?.value != null && <span class="hc"><Num m={cost} kind="money" /></span>}</span>
              <TimeStrip from={from} to={now} start={start} end={r.endedAt} now={now} state={r.state} label={`${r.state}, started ${new Date(start).toISOString()}`} />
              <span data-tip={r.endedAt ? 'Duration' : 'Running for'}>{r.endedAt ? <Num m={m(secs)} kind="duration" /> : <AgeChip t={start} now={now} alertAfter={2 * 3600} tone="attention" alertLabel="running long" />}</span>
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
          <span class="tt"><span class="title">{x.runner}</span>{!x.hosted && <span class="muted">{r ? `${r.status}${r.busy ? ', busy' : ''}` : 'state unknown'}</span>}</span>
          <span data-tip="Jobs"><Num m={m(x.jobs)} kind="count" /></span>
          <span data-tip="Minutes"><Num m={{ ...m(x.minutes * 60), partial }} kind="duration" /></span>
        </div>;
      })}
      {(data.runners ?? []).filter((r: any) => !data.minutesToday.some((x: any) => x.runner === r.name)).map((r: any) => <div key={r.name} role="row" class="vl-row static">
        <span class="lead"><Icon name="panels-top-left" label="Self-hosted" /></span><span class="tt"><span class="title">{r.name}</span><span class="muted">{r.status}{r.busy ? ', busy' : ''}</span></span><span /><span class="muted">idle today</span></div>)}
      {data.jobs.map((j: any) => { const start = j.startedAt ?? j.createdAt ?? now, st = JOB_STATE[j.status] ?? 'waiting';
        return <div key={j.id} role="row" class="vl-row static">
          <span class="lead"><StateBadge family="execution" state={st} compact /></span>
          <span class="tt"><a class="title row-link" href={j.url} target="_blank" rel="noopener">{j.name}</a><span class="id">{j.workflow}</span><span class="muted">{j.runner}</span></span>
          <TimeStrip from={from} to={now} start={start} end={null} now={now} state={st} label={`${j.status}, since ${new Date(start).toISOString()}`} />
          <span data-tip={j.status === 'queued' ? 'Queued for' : 'Running for'}><AgeChip t={start} now={now} alertAfter={3600} tone="attention" alertLabel="long" /></span>
        </div>; })}
    </div>
    {!data.jobs.length && !data.minutesToday.length && <Empty text="No CI jobs today." />}
  </section>;
}
