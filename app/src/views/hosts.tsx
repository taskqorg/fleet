// Hosts (docs/design-system/pages/hosts.md): health and workers; host detail as stacked sections (D11 C3).
// Staleness is decided on the client from lastSeenAt (no write happens when a host stops polling), re-evaluated every 5 s.
import { useEffect, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import { ListDetail, StatStrip, StateBadge, HarnessCounts, AgeChip, Num, FillBar, DetailSection, KvRow, Empty, OfflineMarker, Button } from '../../../packages/ui/src/index';
import { op } from '../api';
import { useLiveRecord } from '../live';

const useTick = (msTick: number) => { const [, set] = useState(0); useEffect(() => { const t = setInterval(() => set(n => n + 1), msTick); return () => clearInterval(t); }, [msTick]); };

export function Hosts({ id }: { id?: string }) {
  const [, go] = useLocation();
  useTick(5000);
  // Liveness comes from lastSeenAt, which polls move without a change row; re-read every 5 s (D11 CUJ 5) and judge age on the
  // server's clock (serverNow) so a skewed browser clock does not mark hosts offline.
  const { data, state } = useLiveRecord<any>('hosts', async () => ({ ...(await op('hosts_list', {})), fetchedAt: Date.now() }), c => c.kind === 'host' || c.kind === 'execution' || c.kind === 'command', 5000);
  const now = data ? data.serverNow + (Date.now() - data.fetchedAt) : Date.now();
  if (!data) return <><header class="page-h"><h1>Hosts</h1></header><Empty text="Loading…" /></>;
  const fresh = (h: any) => !!h.lastSeenAt && now - h.lastSeenAt < data.staleAfterMs;
  const sel = data.hosts.find((h: any) => h.id === id);
  const list = <>
    <header class="page-h"><h1>Hosts</h1><span class="grow" /><OfflineMarker state={state} now={now} /></header>
    <StatStrip label="Workers per host" stats={data.hosts.map((h: any) => ({ label: h.id, icon: 'panels-top-left' as const, value: { value: h.running, basis: 'measured' as const, partial: fresh(h) ? undefined : 'last known' }, harness: h.runningByHarness }))} />
    <section class="panel"><header class="panel-h"><h2>Hosts and stores</h2></header>
      <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(6rem,1fr) 3rem 3.5rem 7rem 3rem minmax(0,10rem)' }}>
        <div class="vl-head" role="row"><span /><span>Host</span><span>Seen</span><span class="r">Workers</span><span /><span class="r">Queue</span><span>Store</span></div>
        {data.hosts.map((h: any) => <div key={h.id} role="row" class={`vl-row static${h.id === id ? ' sel' : ''}`}>
          <StateBadge family="host" state={fresh(h) ? 'online' : 'offline'} compact lastSeen={fresh(h) ? undefined : h.lastSeenAt ?? undefined} now={now} />
          <a class="title row-link" href={`/hosts/${h.id}`} onClick={e => { e.preventDefault(); go(`/hosts/${h.id}`); }}>{h.id}{h.platform && !h.id.includes(h.platform.slice(0, 3)) && <span class="id">{h.platform}</span>}</a>
          <span>{h.lastSeenAt ? <AgeChip t={h.lastSeenAt} now={now} alertAfter={15 * 60} tone="attention" alertLabel="not seen for a while" /> : <span class="muted">never</span>}</span>
          <span class="r"><Num m={{ value: h.running, basis: 'measured', partial: fresh(h) ? undefined : 'last known' }} kind="count" /></span>
          <HarnessCounts counts={h.runningByHarness} label={`${h.id} workers by harness`} />
          <span class="r"><Num m={{ value: h.queued, basis: 'measured' }} kind="count" /></span>
          <FillBar total={null} segments={[]} />
        </div>)}
      </div>
    </section>
  </>;
  const detail = sel && <article class="detail" data-ui="detail" tabIndex={-1} aria-label={sel.id}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => go('/hosts')} /><h2 class="detail-title">{sel.id}</h2></header>
    <DetailSection title="State"><KvRow icon="activity" label="Last seen" meta={sel.lastSeenAt ? undefined : 'never'} value={sel.lastSeenAt ? <AgeChip t={sel.lastSeenAt} now={now} /> : undefined} />
      <KvRow icon="panels-top-left" label="Platform" meta={sel.platform ?? 'unknown'} /></DetailSection>
    <DetailSection title="Workers"><KvRow icon="activity" label="Running" value={<HarnessCounts counts={sel.runningByHarness} label="by harness" />} /><KvRow icon="list-ordered" label="Queued commands" value={<Num m={{ value: sel.queued, basis: 'measured' }} kind="count" />} /></DetailSection>
    {fresh(sel) && sel.problems?.length > 0 && <DetailSection title="Problems">{sel.problems.map((p: any) => <KvRow key={p.route} icon="activity" label={`Edge answers ${p.route} with ${p.status}`} meta={p.location || undefined} />)}</DetailSection>}
    <DetailSection title="Capabilities">{sel.capabilities.length ? sel.capabilities.map((c: string) => <KvRow key={c} icon="check" label={c} />) : <span class="muted">none reported</span>}</DetailSection>
    <DetailSection title="Store"><KvRow icon="save" label="Store" value={<FillBar total={null} segments={[]} />} /></DetailSection>
  </article>;
  return <ListDetail list={list} detail={detail || undefined} detailKey={id} />;
}
