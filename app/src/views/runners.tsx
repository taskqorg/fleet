// Runners (docs/design-system/pages/runners.md): everything that executes work, grouped by host, each run on a 24-hour
// time strip with its time and resources; spend today and this month per host and kind. Data: executions_runners.
import { useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  AgeChip, Button, Choice, CostFigures, DataTable, Empty, EntityCode, ErrorBanner, Fact, HarnessMark, Icon, Num,
  OfflineMarker, ProblemRow, RowFacts, ShellStatus, Skeleton, StateBadge, TimeStrip, Totals, age, duration, harnesses,
  type Harness, type Kind, type Measured, type TotalItem,
} from '../../../packages/ui/src/index.ts';
import { useOp } from '../store.ts';
import { useClockOffset } from './hosts.tsx';
import type { RunnerRun, RunnerSpend } from '../../../contracts/generated/ops.ts';
import { mergeCosts } from './resources.tsx';

const DAY = 864e5;
const isActive = (r: RunnerRun) => r.state === 'preparing' || r.state === 'running' || r.state === 'waiting';
const runsOf = (s: RunnerSpend) => s.runs;
const KINDS: Record<string, string> = { agent_run: 'Agent runs', child_agent: 'Child agents', host_job: 'Host jobs', tool_run: 'Tool calls' };
const m = (v: number | null | undefined, basis: Measured['basis'] = 'measured'): Measured => ({ value: v ?? null, basis: v == null ? 'unavailable' : basis });
/** A spend sum: partial when some runs in the group have no recorded totals. */
const sum = (s: RunnerSpend, v: number | null): Measured => ({ ...m(v), partial: v != null && s.measured < s.runs ? `${s.runs - s.measured} runs without totals` : undefined });
const hostName = (h: string | null) => h ?? 'coordinator';

function spendTotals(spend: RunnerSpend[]): TotalItem[] {
  const sumPeriod = (period: 'today' | 'month', label: string): TotalItem[] => {
    const rows = spend.filter(s => s.period === period);
    if (!rows.length) return [];
    const runs = rows.reduce((n, r) => n + (r.runs ?? 0), 0);
    const measured = rows.reduce((n, r) => n + (r.measured ?? 0), 0);
    const hasTok = rows.some(r => r.tokens != null);
    const tokens = hasTok ? rows.reduce((n, r) => n + (r.tokens ?? 0), 0) : null;
    const partial = hasTok && measured < runs ? `${runs - measured} runs without totals` : undefined;
    const cost = mergeCosts(rows.map(r => r.cost));
    return [
      { label: `${label} runs`, m: { value: runs, basis: 'measured' }, kind: 'count' },
      { label: `${label} tokens`, m: { value: tokens, basis: tokens == null ? 'unavailable' : 'measured', partial }, kind: 'count', note: partial },
      ...(cost.length ? [{ label: `${label} cost`, cost, showUnavailable: false }] : []),
    ];
  };
  return [...sumPeriod('today', 'Today'), ...sumPeriod('month', 'Month')];
}

export function Runners({ onClose }: { onClose?: () => void } = {}) {
  const [, go] = useLocation();
  const [period, setPeriod] = useState<'today' | 'month'>('today');
  const runners = useOp('executions_runners', {}, { every: 15000 }), { data, state, error, reload } = runners; // timer only: gateway calls change executions many times a second
  const clockOffsetMs = useClockOffset(runners);

  if (!data) {
    return (
      <article class="detail stack" data-ui="detail" tabIndex={-1} aria-label="Runners">
        <header class="detail-h">
          {onClose && <Button label="Close" icon="x" iconOnly onClick={onClose} />}
          <h2 class="detail-title">Runners</h2>
        </header>
        {error ? <ErrorBanner text={`Runners did not load: ${error}`} onRetry={reload} /> : <Skeleton rows={6} />}
      </article>
    );
  }

  const now = Date.now() + clockOffsetMs, from = now - DAY;
  const hosts = [...new Set<string | null>([...data.runs.map(r => r.hostId), ...data.spend.map(s => s.hostId)])]
    .sort((a, b) => (a === null ? 1 : b === null ? -1 : a.localeCompare(b)));
  const cell = (s: RunnerSpend | undefined, k: Kind, v: (s: RunnerSpend) => number | null) =>
    <td class="num">{s ? <Num m={v === runsOf ? m(v(s)) : sum(s, v(s))} kind={k} /> : <span class="muted">—</span>}</td>;
  const money = (s: RunnerSpend | undefined) => <td class="num"><CostFigures cost={s?.cost} showUnavailable={false} empty="—" /></td>;
  const open = (r: RunnerRun) => r.taskId ? `/tasks/${r.taskId}` : r.sessionId ? `/conversations/${r.sessionId}` : null;
  const summary = spendTotals(data.spend);

  return (
    <article class="detail stack" data-ui="detail" tabIndex={-1} aria-label="Runners">
      <ShellStatus><OfflineMarker state={state} now={now} /></ShellStatus>
      <header class="detail-h">
        {onClose && <Button label="Close" icon="x" iconOnly onClick={onClose} />}
        <h2 class="detail-title">Runners</h2>
      </header>
      <section class="panel" aria-label="Spend">
        <header class="panel-h">
          <h2>Spend</h2>
          <span class="muted">UTC</span>
          <span class="grow" />
          <Choice label="Spend period" options={[{ id: 'today', label: 'Today' }, { id: 'month', label: 'Month' }]} value={period} onChange={v => setPeriod(v as 'today' | 'month')} />
        </header>
        {summary.length > 0 && <div class="stack-pad"><Totals items={summary} /></div>}
        <DataTable label="Spend" head={<tr><th scope="col">Host</th><th scope="col">Kind</th><th scope="col" class="num">Runs</th><th scope="col" class="num">Time</th><th scope="col" class="num">Tokens</th><th scope="col" class="num">Cost</th></tr>}>
          {hosts.flatMap(h => [...new Set<string>(data.spend.filter(s => s.hostId === h).map(s => s.kind))].sort().map((k, i) => {
            const row = data.spend.find(s => s.hostId === h && s.kind === k && s.period === period);
            return (
              <tr key={`${h}:${k}`}>
                <th scope="row">{i === 0 ? (h ? <EntityCode id={h} code={h} kind="host" cls="inline" /> : <span class="title">{hostName(h)}</span>) : ''}</th>
                <td>{KINDS[k] ?? k}</td>
                {cell(row, 'count', runsOf)}
                {cell(row, 'duration', s => s.wallMs == null ? null : s.wallMs / 1000)}
                {cell(row, 'count', s => s.tokens)}
                {money(row)}
              </tr>
            );
          }))}
        </DataTable>
      </section>
      {hosts.map(h => {
        const runs = data.runs.filter(r => r.hostId === h);
        if (!runs.length) return null;
        const live = runs.filter(isActive).length;
        return (
          <section key={hostName(h)} class="panel" aria-label={hostName(h)}>
            <header class="panel-h">
              <h2>{h ? <EntityCode id={h} code={h} kind="host" cls="inline" /> : hostName(h)}</h2>
              {live > 0 && <span class="hc" data-tip="Running"><Icon name="activity" label="Running" /><Num m={m(live)} kind="count" /></span>}
              <span class="grow" />
              <span class="muted">24h</span>
            </header>
            <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) minmax(6rem,12rem) auto' }}>
              {runs.map(r => {
                // Gateway calls are one run per connection (`calls:/conn:<name>`): the connection names the row, not the kind (OR-17).
                const conn = r.kind === 'tool_run' && typeof r.id === 'string' ? /^calls:\/conn:(.+)$/.exec(r.id)?.[1] : undefined;
                const href = open(r), start = r.startedAt ?? r.endedAt ?? now, title = r.taskTitle ?? (conn ? `${conn} calls` : KINDS[r.kind] ?? r.kind);
                // A finished run with no recorded end (a gateway call) ends at start plus its wall time, else at its start.
                const active = isActive(r), end = active ? null : r.endedAt ?? start + (r.wallMs ?? 0);
                // An abandoned run ended when given up: its length is unknown (the core leaves its wall time unavailable).
                const secs = r.wallMs != null ? r.wallMs / 1000 : r.endedAt != null && r.problem !== 'unknown:abandoned' ? (r.endedAt - start) / 1000 : null;
                return (
                  <div key={r.id} role="row" class="vl-row static">
                    <span class="lead"><StateBadge family="execution" state={r.state} compact /></span>
                    <span class="tt">
                      {href ? <a class="title row-link" href={href} onClick={e => { e.preventDefault(); go(href); }}>{title}</a> : <span class="title">{title}</span>}
                      {!conn && <EntityCode id={r.id} kind="execution" />}
                      {r.parentExecutionId && <EntityCode id={r.parentExecutionId} kind="execution" />}
                      {r.harness && r.harness in harnesses && <HarnessMark h={r.harness as Harness} />}
                      {r.problem && <span class="chip t-danger" data-tip={r.problem} role="img" aria-label={r.problem}><Icon name="triangle-alert" /></span>}
                    </span>
                    <TimeStrip from={from} to={now} start={start} end={end} now={now} state={r.state} label={`${r.state}, started ${age(start, now).long}`} />
                    <span class="rz">
                      <RowFacts>
                        {r.calls != null && <Fact icon="terminal" tip="Tool calls"><Num m={m(r.calls)} kind="count" /></Fact>}
                        {(r.failedCalls ?? 0) > 0 && <Fact icon="octagon-x" tip="Failed calls"><span class="t-danger"><Num m={m(r.failedCalls)} kind="count" /></span></Fact>}
                        {r.tokens != null && <Fact icon="layers-2" tip="Tokens"><Num m={m(r.tokens)} kind="count" /></Fact>}
                        {r.cpuMs != null && <Fact icon="gauge" tip="CPU"><Num m={m(r.cpuMs / 1000)} kind="duration" /></Fact>}
                        {r.peakMemoryBytes != null && <Fact icon="save" tip="Peak memory"><Num m={m(r.peakMemoryBytes)} kind="bytes" /></Fact>}
                        {r.cost.length > 0 && <Fact tip="Cost"><CostFigures cost={r.cost} /></Fact>}
                        <Fact icon="clock" tip={active ? 'Running for' : 'Duration'}>
                          {!active ? <Num m={m(secs)} kind="duration" /> : <AgeChip t={start} clockOffsetMs={clockOffsetMs} alertAfter={2 * 3600} tone="attention" alertLabel="running long" />}
                        </Fact>
                      </RowFacts>
                    </span>
                  </div>
                );
              })}
            </div>
          </section>
        );
      })}
      {!data.runs.length && <Empty text="Nothing ran in the last 24 hours." />}
      <Ci now={now} clockOffsetMs={clockOffsetMs} />
    </article>
  );
}

const JOB_STATE: Record<string, 'running' | 'preparing' | 'waiting'> = { in_progress: 'running', queued: 'preparing', waiting: 'waiting', pending: 'preparing', requested: 'preparing' };

/** GitHub Actions for taskqorg/fleet (executions_ci, cached on the coordinator): minutes today per runner type, the
 * self-hosted runners, and jobs in flight or queued on a 24-hour strip. */
function Ci({ now, clockOffsetMs }: { now: number; clockOffsetMs: number }) {
  const { data, error, reload } = useOp('executions_ci', {}, { every: 60000 });
  const from = now - DAY;
  if (!data) {
    return (
      <section class="panel" aria-label="CI">
        <header class="panel-h"><h2>CI</h2></header>
        {error ? <ErrorBanner text={`CI did not load: ${error}`} onRetry={reload} /> : <Skeleton rows={3} />}
      </section>
    );
  }
  const partial = data.runsUncounted > 0 ? `${data.runsUncounted} runs not read yet` : undefined;
  // The finding first (2026-11-20 decision): GitHub-hosted minutes on a day the self-hosted runners stood free.
  const hosted = data.minutesToday.filter(x => x.hosted), self = data.minutesToday.filter(x => !x.hosted);
  const hostedJobs = hosted.reduce((n, x) => n + x.jobs, 0), hostedMin = hosted.reduce((n, x) => n + x.minutes, 0);
  const selfJobs = self.reduce((n, x) => n + x.jobs, 0), idleSelf = (data.runners ?? []).filter(r => r.status === 'online' && !r.busy).length;
  const idleRunners = (data.runners ?? []).filter(r => !data.minutesToday.some(x => x.runner === r.name));
  return (
    <section class="panel" aria-label="CI">
      {hostedJobs > 0 && idleSelf > 0 && (
        <ProblemRow
          severity="warning"
          title="CI ran on GitHub-hosted machines today"
          facts={`${hostedJobs} of ${hostedJobs + selfJobs} jobs took ${duration(hostedMin * 60).text} on ${hosted.map(x => x.runner).join(', ')}; ${idleSelf} self-hosted ${idleSelf === 1 ? 'runner is' : 'runners are'} online and free`}
          actions={[]}
        />
      )}
      <header class="panel-h">
        <h2>CI</h2>
        <Icon name="github" label="GitHub Actions" />
        <span class="id">taskqorg/fleet</span>
        {!data.keyed && <span class="chip" data-tip="No GitHub key: public reads, refreshed every 5 minutes; self-hosted runner state unknown">public</span>}
        {data.problems.length > 0 && <span class="chip t-danger" data-tip={data.problems.join('; ')} role="img" aria-label={data.problems.join('; ')}><Icon name="triangle-alert" /></span>}
        <span class="grow" />
        <span class="muted">today UTC</span>
      </header>
      {(data.minutesToday.length > 0 || idleRunners.length > 0) && (
        <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) auto' }}>
          {data.minutesToday.map(x => {
            const r = (data.runners ?? []).find(y => y.name === x.runner);
            const lead = x.hosted
              ? <Icon name="github" label="GitHub-hosted" />
              : !data.runners
                ? <StateBadge family="host" state="offline" unconfirmed reason="state unknown" compact />
                : !r
                  ? <StateBadge family="host" state="offline" reason="removed runner" compact />
                  : r.busy
                    ? <StateBadge family="execution" state="running" reason={`${r.status}, busy`} compact />
                    : <StateBadge family="host" state={r.status === 'online' ? 'online' : 'offline'} compact />;
            return (
              <div key={x.runner} role="row" class="vl-row static">
                <span class="lead">{lead}</span>
                <span class="tt"><span class="title">{x.runner}</span></span>
                <span class="rz">
                  <RowFacts>
                    <Fact icon="repeat" tip="Jobs"><Num m={m(x.jobs)} kind="count" /></Fact>
                    <Fact icon="clock" tip="Minutes"><Num m={{ ...m(x.minutes * 60), partial }} kind="duration" /></Fact>
                  </RowFacts>
                </span>
              </div>
            );
          })}
          {idleRunners.map(r => (
            <div key={r.name} role="row" class="vl-row static">
              <span class="lead">
                {r.busy
                  ? <StateBadge family="execution" state="running" reason={`${r.status}, busy`} compact />
                  : <StateBadge family="host" state={r.status === 'online' ? 'online' : 'offline'} compact />}
              </span>
              <span class="tt"><span class="title">{r.name}</span></span>
              <span class="rz">
                <RowFacts>
                  <Fact icon="circle-dashed" tip="Idle today"><span class="muted">—</span></Fact>
                </RowFacts>
              </span>
            </div>
          ))}
        </div>
      )}
      {data.jobs.length > 0 && (
        <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) minmax(6rem,12rem) auto' }}>
          {data.jobs.map(j => {
            const start = j.startedAt ?? j.createdAt ?? now, st = JOB_STATE[j.status] ?? 'waiting';
            return (
              <div key={j.id} role="row" class="vl-row static">
                <span class="lead"><StateBadge family="execution" state={st} reason={j.status !== 'in_progress' ? j.status : undefined} compact /></span>
                <span class="tt"><a class="title row-link" href={j.url} target="_blank" rel="noopener">{j.name}</a><span class="id">{j.workflow}</span></span>
                <TimeStrip from={from} to={now} start={start} end={null} now={now} state={st} label={`${j.status}, since ${age(start, now).long}`} />
                <span class="rz">
                  <RowFacts>
                    <Fact icon="panels-top-left" text={j.runner} tip="Runner" />
                    <Fact icon="clock" tip={j.status === 'queued' ? 'Queued for' : 'Running for'}>
                      <AgeChip t={start} clockOffsetMs={clockOffsetMs} alertAfter={3600} tone="attention" alertLabel="long" />
                    </Fact>
                  </RowFacts>
                </span>
              </div>
            );
          })}
        </div>
      )}
      {!data.jobs.length && !data.minutesToday.length && !idleRunners.length && <Empty text="No CI jobs today." />}
    </section>
  );
}
