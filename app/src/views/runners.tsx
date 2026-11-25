// Runners (docs/design-system/pages/runners.md): everything that executes work, grouped by host, each run on a 24-hour
// time strip with its time and resources. Cost is shown only on Spend (OR-54). Data: executions_runners.
import { createMemo, For, Show } from 'solid-js';
import { useLocation } from '../router.ts';
import {
  AgeChip, Button, CostFigures, Empty, EntityCode, ErrorBanner, Fact, Icon, Num,
  OfflineMarker, ProblemRow, RowFacts, ShellStatus, Skeleton, StateBadge, TimeStrip, WorkFacts, age, cause, duration, harnesses, peerFence,
  type Harness, type Measured,
} from '../../../packages/ui/src/index.ts';
import { useOp } from '../store.ts';
import { useClockOffset } from './hosts.tsx';
import type { RunnerRun } from '../../../contracts/generated/ops.ts';

const DAY = 864e5;
const isActive = (r: RunnerRun) => r.state === 'preparing' || r.state === 'running' || r.state === 'waiting';
const KINDS: Record<string, string> = { agent_run: 'Agent runs', child_agent: 'Child agents', host_job: 'Host jobs', tool_run: 'Tool calls' };
const m = (v: number | null | undefined, basis: Measured['basis'] = 'measured'): Measured => ({ value: v ?? null, basis: v == null ? 'unavailable' : basis });
const hostName = (h: string | null) => h ?? 'coordinator';

export function Runners(props: { onClose?: () => void } = {}) {
  const [, go] = useLocation();
  const runners = useOp('executions_runners', {}, { every: 15000 }); // timer only: gateway calls change executions many times a second
  const clockOffsetMs = useClockOffset(runners);

  const now = createMemo(() => { void runners.data; return Date.now() + clockOffsetMs(); });
  const from = createMemo(() => now() - DAY);
  const hosts = createMemo(() => {
    const data = runners.data;
    if (!data) return [];
    return [...new Set<string | null>(data.runs.map(r => r.hostId))]
      .sort((a, b) => (a === null ? 1 : b === null ? -1 : a.localeCompare(b)));
  });
  // Work facts: a long run or a much-tried task stands out against the page's runs (peerFence), never a fixed limit.
  const fences = createMemo(() => {
    const runs = runners.data?.runs ?? [];
    return {
      attempts: peerFence(runs.map(r => r.attempt ?? 0)),
      work: peerFence(runs.filter(isActive).map(r => (now() - (r.startedAt ?? r.endedAt ?? now())) / 1000)),
    };
  });
  const open = (r: RunnerRun) => r.taskId ? `/tasks/${r.taskId}` : r.sessionId ? `/conversations/${r.sessionId}` : null;

  return (
    <Show when={runners.data} fallback={
      <article class="detail stack" data-ui="detail" tabindex={-1} aria-label="Runners">
        <header class="detail-h">
          <Show when={props.onClose}>{onClose => <Button label="Close" icon="x" iconOnly onClick={onClose()} />}</Show>
          <h2 class="detail-title">Runners</h2>
        </header>
        <Show when={runners.error} fallback={<Skeleton rows={6} />}>{err => <ErrorBanner text={`Runners did not load: ${err()}`} onRetry={runners.reload} />}</Show>
      </article>
    }>{data => (
      <article class="detail stack" data-ui="detail" tabindex={-1} aria-label="Runners">
        <ShellStatus><OfflineMarker state={runners.state} now={now()} /></ShellStatus>
        <header class="detail-h">
          <Show when={props.onClose}>{onClose => <Button label="Close" icon="x" iconOnly onClick={onClose()} />}</Show>
          <h2 class="detail-title">Runners</h2>
        </header>
        <For each={hosts()}>{h => {
          const runs = createMemo(() => data().runs.filter(r => r.hostId === h));
          const live = createMemo(() => runs().filter(isActive).length);
          return (
            <Show when={runs().length > 0}>
              <section class="panel" aria-label={hostName(h)}>
                <header class="panel-h">
                  <h2>{h ? <EntityCode id={h} code={h} kind="host" cls="inline" /> : hostName(h)}</h2>
                  <Show when={live() > 0}><span class="hc" data-tip="Running"><Icon name="activity" label="Running" /><Num m={m(live())} kind="count" /></span></Show>
                  <span class="grow" />
                  <span class="muted">24h</span>
                </header>
                <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) minmax(6rem,12rem) auto' }}>
                  <For each={runs()}>{r => {
                    // Gateway calls are one run per connection (`calls:/conn:<name>`): the connection names the row, not the kind (OR-17).
                    const conn = r.kind === 'tool_run' && typeof r.id === 'string' ? /^calls:\/conn:(.+)$/.exec(r.id)?.[1] : undefined;
                    const href = open(r);
                    const title = r.taskTitle ?? (conn ? `${conn} calls` : KINDS[r.kind] ?? r.kind);
                    const start = () => r.startedAt ?? r.endedAt ?? now();
                    // A finished run with no recorded end (a gateway call) ends at start plus its wall time, else at its start.
                    const active = isActive(r);
                    const end = () => active ? null : r.endedAt ?? start() + (r.wallMs ?? 0);
                    // An abandoned run ended when given up: its length is unknown (the core leaves its wall time unavailable).
                    const secs = () => r.wallMs != null ? r.wallMs / 1000 : r.endedAt != null && r.problem !== 'unknown:abandoned' ? (r.endedAt - start()) / 1000 : null;
                    return (
                      <div role="row" class="vl-row static">
                        <span class="lead"><StateBadge family="execution" state={r.state} compact /></span>
                        <span class="tt">
                          {href ? <a class="title row-link" href={href} onClick={e => { e.preventDefault(); go(href); }}>{title}</a> : <span class="title">{title}</span>}
                          {!conn && <EntityCode id={r.id} kind="execution" />}
                          {r.parentExecutionId && <EntityCode id={r.parentExecutionId} kind="execution" />}
                          {r.problem && <span class="chip t-danger" data-tip={`${cause(r.problem).text} Code: ${r.problem}`} role="img" aria-label={cause(r.problem).text}><Icon name="triangle-alert" /></span>}
                        </span>
                        <TimeStrip from={from()} to={now()} start={start()} end={end()} now={now()} state={r.state} label={`${r.state}, started ${age(start(), now()).long}`} />
                        <span class="rz">
                          <WorkFacts
                            before={<>
                              {r.calls != null && <Fact icon="terminal" tip="Tool calls"><Num m={m(r.calls)} kind="count" /></Fact>}
                              {(r.failedCalls ?? 0) > 0 && <Fact icon="octagon-x" tip="Failed calls"><span class="t-danger"><Num m={m(r.failedCalls)} kind="count" /></span></Fact>}
                              {r.tokens != null && <Fact icon="layers-2" tip="Tokens"><Num m={m(r.tokens)} kind="count" /></Fact>}
                              {r.cpuMs != null && <Fact icon="gauge" tip="CPU"><Num m={m(r.cpuMs / 1000)} kind="duration" /></Fact>}
                              {r.peakMemoryBytes != null && <Fact icon="save" tip="Peak memory"><Num m={m(r.peakMemoryBytes)} kind="bytes" /></Fact>}
                              {r.cost.length > 0 && <Fact tip="Cost"><CostFigures cost={r.cost} /></Fact>}
                            </>}
                            attempts={r.attempt ?? undefined}
                            attemptsFence={fences().attempts}
                            attemptsTip={`Attempt ${(r.attempt ?? 0).toLocaleString('en')} of its task`}
                            agent={r.harness && r.harness in harnesses ? (r.harness as Harness) : null}
                            workAt={active ? start() : undefined}
                            workFence={fences().work}
                            workTip="Running for"
                            clockOffsetMs={clockOffsetMs()}
                            time={active ? undefined : <Fact icon="clock" tip="Duration"><Num m={m(secs())} kind="duration" /></Fact>}
                          />
                        </span>
                      </div>
                    );
                  }}</For>
                </div>
              </section>
            </Show>
          );
        }}</For>
        <Show when={!data().runs.length}><Empty text="Nothing ran in the last 24 hours." /></Show>
        <Ci now={now()} clockOffsetMs={clockOffsetMs()} />
      </article>
    )}</Show>
  );
}

const JOB_STATE: Record<string, 'running' | 'preparing' | 'waiting'> = { in_progress: 'running', queued: 'preparing', waiting: 'waiting', pending: 'preparing', requested: 'preparing' };

/** GitHub Actions for taskqorg/fleet (executions_ci, cached on the coordinator): minutes today per runner type, the
 * self-hosted runners, and jobs in flight or queued on a 24-hour strip. */
function Ci(props: { now: number; clockOffsetMs: number }) {
  const ciOp = useOp('executions_ci', {}, { every: 60000 });
  const from = createMemo(() => props.now - DAY);
  const partial = createMemo(() => (ciOp.data?.runsUncounted ?? 0) > 0 ? `${ciOp.data!.runsUncounted} runs not read yet` : undefined);
  // The finding first (2026-11-20 decision): GitHub-hosted minutes on a day the self-hosted runners stood free.
  const hosted = createMemo(() => ciOp.data?.minutesToday.filter(x => x.hosted) ?? []);
  const self = createMemo(() => ciOp.data?.minutesToday.filter(x => !x.hosted) ?? []);
  const hostedJobs = createMemo(() => hosted().reduce((n, x) => n + x.jobs, 0));
  const hostedMin = createMemo(() => hosted().reduce((n, x) => n + x.minutes, 0));
  const selfJobs = createMemo(() => self().reduce((n, x) => n + x.jobs, 0));
  const idleSelf = createMemo(() => (ciOp.data?.runners ?? []).filter(r => r.status === 'online' && !r.busy).length);
  const idleRunners = createMemo(() => (ciOp.data?.runners ?? []).filter(r => !ciOp.data?.minutesToday.some(x => x.runner === r.name)));

  return (
    <Show when={ciOp.data} fallback={
      <section class="panel" aria-label="CI">
        <header class="panel-h"><h2>CI</h2></header>
        <Show when={ciOp.error} fallback={<Skeleton rows={3} />}>{err => <ErrorBanner text={`CI did not load: ${err()}`} onRetry={ciOp.reload} />}</Show>
      </section>
    }>{data => (
      <section class="panel" aria-label="CI">
        <Show when={hostedJobs() > 0 && idleSelf() > 0}>
          <ProblemRow
            severity="warning"
            title="CI ran on GitHub-hosted machines today"
            facts={`${hostedJobs()} of ${hostedJobs() + selfJobs()} jobs took ${duration(hostedMin() * 60).text} on ${hosted().map(x => x.runner).join(', ')}; ${idleSelf()} self-hosted ${idleSelf() === 1 ? 'runner is' : 'runners are'} online and free`}
            actions={[]}
          />
        </Show>
        <header class="panel-h">
          <h2>CI</h2>
          <Icon name="github" label="GitHub Actions" />
          <span class="id">taskqorg/fleet</span>
          <Show when={!data().keyed}><span class="chip" data-tip="No GitHub key: public reads, refreshed every 5 minutes; self-hosted runner state unknown">public</span></Show>
          <Show when={data().problems.length > 0}><span class="chip t-danger" data-tip={data().problems.join('; ')} role="img" aria-label={data().problems.join('; ')}><Icon name="triangle-alert" /></span></Show>
          <span class="grow" />
          <span class="muted">today UTC</span>
        </header>
        <Show when={data().minutesToday.length > 0 || idleRunners().length > 0}>
          <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) auto' }}>
            <For each={data().minutesToday}>{x => {
              const r = createMemo(() => (data().runners ?? []).find(y => y.name === x.runner));
              const lead = () => {
                if (x.hosted) return <Icon name="github" label="GitHub-hosted" />;
                if (!data().runners) return <StateBadge family="host" state="offline" unconfirmed reason="state unknown" compact />;
                const cur = r();
                if (!cur) return <StateBadge family="host" state="offline" reason="removed runner" compact />;
                return cur.busy
                  ? <StateBadge family="execution" state="running" reason={`${cur.status}, busy`} compact />
                  : <StateBadge family="host" state={cur.status === 'online' ? 'online' : 'offline'} compact />;
              };
              return (
                <div role="row" class="vl-row static">
                  <span class="lead">{lead()}</span>
                  <span class="tt"><span class="title">{x.runner}</span></span>
                  <span class="rz">
                    <RowFacts>
                      <Fact icon="repeat" tip="Jobs"><Num m={m(x.jobs)} kind="count" /></Fact>
                      <Fact icon="clock" tip="Minutes"><Num m={{ ...m(x.minutes * 60), partial: partial() }} kind="duration" /></Fact>
                    </RowFacts>
                  </span>
                </div>
              );
            }}</For>
            <For each={idleRunners()}>{r => (
              <div role="row" class="vl-row static">
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
            )}</For>
          </div>
        </Show>
        <Show when={data().jobs.length > 0}>
          <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) minmax(6rem,12rem) auto' }}>
            <For each={data().jobs}>{j => {
              const start = j.startedAt ?? j.createdAt ?? props.now;
              const st = JOB_STATE[j.status] ?? 'waiting';
              return (
                <div role="row" class="vl-row static">
                  <span class="lead"><StateBadge family="execution" state={st} reason={j.status !== 'in_progress' ? j.status : undefined} compact /></span>
                  <span class="tt"><a class="title row-link" href={j.url} target="_blank" rel="noopener">{j.name}</a><span class="id">{j.workflow}</span></span>
                  <TimeStrip from={from()} to={props.now} start={start} end={null} now={props.now} state={st} label={`${j.status}, since ${age(start, props.now).long}`} />
                  <span class="rz">
                    <RowFacts>
                      <Fact icon="panels-top-left" text={j.runner} tip="Runner" />
                      <Fact icon="clock" tip={j.status === 'queued' ? 'Queued for' : 'Running for'}>
                        <AgeChip t={start} clockOffsetMs={props.clockOffsetMs} alertAfter={3600} tone="attention" alertLabel="long" />
                      </Fact>
                    </RowFacts>
                  </span>
                </div>
              );
            }}</For>
          </div>
        </Show>
        <Show when={!data().jobs.length && !data().minutesToday.length && !idleRunners().length}><Empty text="No CI jobs today." /></Show>
      </section>
    )}</Show>
  );
}
