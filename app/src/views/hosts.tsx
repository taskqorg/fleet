// Hosts (docs/design-system/pages/hosts.md): health and workers; host detail as stacked sections (D11 C3). With no host
// open, the work area shows the system: services (RED from spans), findings of the rules pass, recent runs with their CPU
// and memory, and purge (docs/designs/fleet-telemetry.md §8, fleet-system-management.md System screen).
// Staleness is decided on the client from lastSeenAt (no write happens when a host stops polling), re-evaluated every 5 s.
import { useEffect, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import { ListDetail, Icon, StateBadge, HarnessCounts, AgeChip, Num, FillBar, DetailSection, KvRow, Empty, OfflineMarker, Button, ProblemRow } from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useDoc } from '../store.ts';

const useTick = (msTick: number) => { const [, set] = useState(0); useEffect(() => { const t = setInterval(() => set(n => n + 1), msTick); return () => clearInterval(t); }, [msTick]); };
const ms = (s: string) => Date.parse(s);
const m = (value: number | null | undefined) => ({ value: value ?? null, basis: value == null ? 'unavailable' : 'measured' } as const);

export function Hosts({ id }: { id?: string }) {
  const [, go] = useLocation();
  useTick(5000);
  // Liveness comes from lastSeenAt, which polls move without a change row; re-read every 5 s (D11 CUJ 5) and judge age on the
  // server's clock (serverNow) so a skewed browser clock does not mark hosts offline.
  const { data, state } = useDoc<any>('hosts', async () => ({ ...(await op('hosts_list', {})), fetchedAt: Date.now() }), c => c.kind === 'host' || c.kind === 'execution' || c.kind === 'command', 5000);
  const now = data ? data.serverNow + (Date.now() - data.fetchedAt) : Date.now();
  // Limits (people-access §8): the latest reading per subscription and limit period, with its age; none is unknown, never zero.
  const limits = useDoc<any>('subscription-limits', async () => await op('subscription_limits', {}), c => c.kind === 'managed-account', 30000);
  // Spans change without change rows: summary and findings are re-read every 30 s.
  const tele = useDoc<any>('telemetry-summary', async () => await op('telemetry_summary', { window: '1h' }), () => false, 30000);
  const found = useDoc<any>('telemetry-findings', async () => await op('telemetry_findings', {}), () => false, 30000);
  if (!data) return <><header class="page-h"><h1>Hosts</h1></header><Empty text="Loading…" /></>;
  const fresh = (h: any) => !!h.lastSeenAt && now - h.lastSeenAt < data.staleAfterMs;
  const reading = (hid: string) => (tele.data?.hosts ?? []).find((r: any) => r.hostId === hid);
  const sel = data.hosts.find((h: any) => h.id === id);
  const list = <>
    <header class="page-h"><h1>Hosts</h1><span class="grow" /><OfflineMarker state={state} now={now} /></header>
    <section class="panel">
      <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) auto' }}>
        {data.hosts.map((h: any) => { const r = reading(h.id); return <div key={h.id} role="row" class={`vl-row static${h.id === id ? ' sel' : ''}`}>
          <span class="lead"><StateBadge family="host" state={fresh(h) ? 'online' : 'offline'} compact lastSeen={fresh(h) ? undefined : h.lastSeenAt ?? undefined} now={now} /></span>
          <span class="tt"><a class="title row-link" href={`/hosts/${h.id}`} onClick={e => { e.preventDefault(); go(`/hosts/${h.id}`); }}>{h.id}</a>
            {h.platform && !h.id.includes(h.platform.slice(0, 3)) && <span class="id">{h.platform}</span>}
            <HarnessCounts counts={h.runningByHarness} label={`${h.id} workers by harness`} />
            {h.queued > 0 && <span class="hc"><Icon name="list-ordered" label="Queued commands" /><Num m={{ value: h.queued, basis: 'measured' }} kind="count" /></span>}
            {r && <span class="hc" data-tip="Load (1 min) and memory free"><Icon name="activity" label="Load" /><Num m={m(r.load1)} kind="score" /> <Num m={m(r.memFree)} kind="bytes" /></span>}</span>
          <span>{h.lastSeenAt ? <AgeChip t={h.lastSeenAt} now={now} alertAfter={15 * 60} tone="attention" alertLabel="not seen for a while" /> : <span class="muted">never</span>}</span>
        </div>; })}
      </div>
    </section>
    <section class="panel"><header class="panel-h"><h2>Subscription limits</h2></header>
      <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr)' }}>
        {(limits.data?.subscriptions ?? []).map((a: any) => <div key={a.id} role="row" class="vl-row static">
          <span class="lead"><StateBadge family="subscription" state={a.intake === 'paused' ? 'paused' : 'all'} compact /></span>
          <span class="tt"><span class="title">{a.name}</span><span class="id">{a.harness}</span>
            {a.readings.length ? a.readings.map((r: any) => <span key={r.period} class={r.state === 'unknown' ? 'muted' : undefined}>{r.period} {r.state === 'unknown' ? 'unknown' : r.pct !== null ? <><Num m={{ value: r.pct, basis: 'measured' }} kind="count" />%</> : r.status} <AgeChip t={r.at} now={now} /> </span>) : <span class="muted">unknown</span>}</span>
        </div>)}
      </div>
    </section>
  </>;
  const detail = sel && <HostDetail key={sel.id} host={sel} live={fresh(sel)} reading={reading(sel.id)} processes={(tele.data?.processes ?? []).filter((p: any) => p.hostId === sel.id)} now={now} onGone={() => go('/hosts')} />;
  return <ListDetail list={list} detail={detail || undefined} detailKey={id} idle={<SystemPanels tele={tele.data} found={found.data} now={now} />} />;
}

function HostDetail({ host: sel, live, reading: r, processes, now, onGone }: { host: any; live: boolean; reading: any; processes: any[]; now: number; onGone: () => void }) {
  const [preview, setPreview] = useState<any>(null);
  const [err, setErr] = useState('');
  const remove = (confirm: boolean) => op('hosts_remove', { id: sel.id, preview: !confirm }).then(x => { if (confirm) onGone(); else setPreview(x); }, e => setErr(String(e.message ?? e)));
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={sel.id}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onGone} /><h2 class="detail-title">{sel.id}</h2></header>
    <DetailSection title="State"><KvRow icon="activity" label="Last seen" meta={sel.lastSeenAt ? undefined : 'never'} value={sel.lastSeenAt ? <AgeChip t={sel.lastSeenAt} now={now} /> : undefined} />
      <KvRow icon="panels-top-left" label="Platform" meta={sel.platform ?? 'unknown'} /></DetailSection>
    <DetailSection title="Workers"><KvRow icon="activity" label="Running" value={<HarnessCounts counts={sel.runningByHarness} label="by harness" />} /><KvRow icon="list-ordered" label="Queued commands" value={<Num m={{ value: sel.queued, basis: 'measured' }} kind="count" />} /></DetailSection>
    <DetailSection title="Readings">{r ? <>
      <KvRow icon="clock" label="Heartbeat" value={<AgeChip t={ms(r.at)} now={now} alertAfter={3 * 60} tone="danger" alertLabel="no heartbeat" />} />
      <KvRow icon="activity" label="Load (1 min)" value={<><Num m={m(r.load1)} kind="score" />{r.cpus != null && <span class="muted"> of {r.cpus} cores</span>}</>} />
      <KvRow icon="activity" label="Memory free" value={<><Num m={m(r.memFree)} kind="bytes" />{r.memTotal != null && <span class="muted"> of <Num m={m(r.memTotal)} kind="bytes" /></span>}</>} />
      <KvRow icon="save" label="Disk free" value={<Num m={m(r.diskFree)} kind="bytes" />} />
    </> : <span class="muted">no heartbeat recorded</span>}</DetailSection>
    {processes.length > 0 && <DetailSection title="Processes">{processes.map(p => <KvRow key={p.process} icon="activity" label={p.process}
      value={<><Num m={m(p.cpuSeconds)} kind="duration" /> <span class="muted">CPU per beat</span> <Num m={m(p.rssBytes)} kind="bytes" /></>} />)}</DetailSection>}
    {live && sel.problems?.length > 0 && <DetailSection title="Problems">{sel.problems.map((p: any) => <KvRow key={p.route} icon="activity" label={`Edge answers ${p.route} with ${p.status}`} meta={p.detail || p.location || undefined} />)}</DetailSection>}
    <DetailSection title="Capabilities">{sel.capabilities.length ? sel.capabilities.map((c: string) => <KvRow key={c} icon="check" label={c} />) : <span class="muted">none reported</span>}</DetailSection>
    <DetailSection title="Store"><KvRow icon="save" label="Store" value={<FillBar total={null} segments={[]} />} /></DetailSection>
    <DetailSection title="Remove">
      {err && <ProblemRow title="Not removed" facts={err} severity="failure" actions={[]} />}
      {!preview ? <Button label="Remove host" icon="trash-2" onClick={() => remove(false)} /> : <>
        <KvRow icon="activity" label="Running work cancelled" value={<Num m={{ value: preview.runningExecutions, basis: 'measured' }} kind="count" />} />
        <KvRow icon="activity" label="Open sessions marked" value={<Num m={{ value: preview.openSessions, basis: 'measured' }} kind="count" />} />
        <KvRow icon="triangle-alert" label="Artifacts lost (no other copy)" value={<Num m={{ value: preview.unreplicatedArtifacts, basis: 'measured' }} kind="count" />} />
        <span class="acts"><Button label="Remove" icon="trash-2" kind="danger" onClick={() => remove(true)} /><Button label="Cancel" kind="quiet" onClick={() => setPreview(null)} /></span>
      </>}
    </DetailSection>
  </article>;
}

function SystemPanels({ tele, found, now }: { tele: any; found: any; now: number }) {
  const [purge, setPurge] = useState<any>(null);
  if (!tele) return <Empty text="Loading…" />;
  if (!tele.enabled) return <Empty text="Telemetry is off: no span store is configured." />;
  const ops = [...tele.operations].sort((a: any, b: any) => b.errors - a.errors || b.count - a.count);
  return <article class="detail" aria-label="System">
    <header class="detail-h"><h2 class="detail-title">System</h2></header>
    {tele.error && <ProblemRow title="Span store" facts={tele.error} severity="failure" actions={[]} />}
    <DetailSection title="Findings">
      {found?.stale && <ProblemRow title="Rules pass is stale" facts="Findings may be out of date." severity="warning" actions={[]} />}
      {(found?.findings ?? []).length ? found.findings.map((f: any) => <KvRow key={f.cause} icon={f.groupCause ? 'circle-alert' : 'triangle-alert'} label={f.text ?? f.cause}
        meta={f.groupCause ? `part of ${f.groupCause}` : undefined} value={<AgeChip t={ms(f.since)} now={now} />} />) : <span class="muted">none</span>}
    </DetailSection>
    <DetailSection title="Services, last hour">
      <div class="vl-wrap" style={{ '--cols': 'minmax(0,1fr) auto auto auto' }}>
        {ops.map((o: any) => <div key={o.service + o.name} role="row" class="vl-row static">
          <span class="tt"><span class="title">{o.name}</span><span class="id">{o.service}</span></span>
          <span data-tip="Calls"><Num m={{ value: o.count, basis: 'measured' }} kind="count" /></span>
          <span data-tip="Errors" class={o.errors ? 'danger' : 'muted'}><Num m={{ value: o.errors, basis: 'measured' }} kind="count" /></span>
          <span data-tip="p95"><Num m={{ value: o.p95Ms / 1000, basis: 'measured' }} kind="duration" /></span>
        </div>)}
      </div>
      <KvRow icon="circle-alert" label="Spans dropped" value={<Num m={{ value: tele.droppedSpans, basis: 'measured' }} kind="count" />} />
    </DetailSection>
    <DetailSection title="Recent runs">
      {tele.runs.length ? tele.runs.slice(0, 20).map((r: any) => <KvRow key={r.executionId} icon="activity" label={r.executionId}
        meta={r.hostId} value={<><Num m={m(r.cpuSeconds)} kind="duration" /> <span class="muted">CPU</span> <Num m={m(r.peakRssBytes)} kind="bytes" /> <span class="muted">peak</span> <AgeChip t={ms(r.at)} now={now} /></>} />) : <span class="muted">none in the last hour</span>}
    </DetailSection>
    <DetailSection title="Purge">
      {!purge ? <Button label="Find removed hosts to purge" icon="trash-2" onClick={() => op('system_purge', { preview: true }).then(setPurge)} /> : <>
        <KvRow icon="trash-2" label="Removed hosts nothing references" value={<Num m={{ value: purge.removed[0]?.count ?? 0, basis: 'measured' }} kind="count" />} />
        <KvRow icon="save" label="Removed hosts kept by history" value={<Num m={{ value: purge.kept[0]?.count ?? 0, basis: 'measured' }} kind="count" />} />
        {(purge.removed[0]?.count ?? 0) > 0 && !purge.purged && <Button label="Purge" icon="trash-2" kind="danger" onClick={() => op('system_purge', { preview: false }).then(setPurge)} />}
      </>}
    </DetailSection>
  </article>;
}
