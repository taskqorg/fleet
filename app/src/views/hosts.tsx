// Hosts (docs/design-system/pages/hosts.md): health and workers; host detail as stacked sections (D11 C3).
// Staleness is decided on the client from lastSeenAt (no write happens when a host stops polling), re-evaluated every 5 s.
import { useEffect, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import { ListDetail, Icon, StateBadge, HarnessCounts, AgeChip, Num, FillBar, DetailSection, KvRow, Empty, OfflineMarker, Button } from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useDoc } from '../store.ts';

const useTick = (msTick: number) => { const [, set] = useState(0); useEffect(() => { const t = setInterval(() => set(n => n + 1), msTick); return () => clearInterval(t); }, [msTick]); };

export function Hosts({ id }: { id?: string }) {
  const [, go] = useLocation();
  useTick(5000);
  // Liveness comes from lastSeenAt, which polls move without a change row; re-read every 5 s (D11 CUJ 5) and judge age on the
  // server's clock (serverNow) so a skewed browser clock does not mark hosts offline.
  const { data, state } = useDoc<any>('hosts', async () => ({ ...(await op('hosts_list', {})), fetchedAt: Date.now() }), c => c.kind === 'host' || c.kind === 'execution' || c.kind === 'command', 5000);
  const now = data ? data.serverNow + (Date.now() - data.fetchedAt) : Date.now();
  // Limits (people-access §8): the latest reading per subscription and limit period, with its age; none is unknown, never zero.
  const limits = useDoc<any>('subscription-limits', async () => await op('subscription_limits', {}), c => c.kind === 'managed-account', 30000);
  if (!data) return <><header class="page-h"><h1>Hosts</h1></header><Empty text="Loading…" /></>;
  const fresh = (h: any) => !!h.lastSeenAt && now - h.lastSeenAt < data.staleAfterMs;
  const sel = data.hosts.find((h: any) => h.id === id);
  const list = <>
    <header class="page-h"><h1>Hosts</h1><span class="grow" /><OfflineMarker state={state} now={now} /></header>
    <section class="panel">
      <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) auto' }}>
        {data.hosts.map((h: any) => <div key={h.id} role="row" class={`vl-row static${h.id === id ? ' sel' : ''}`}>
          <span class="lead"><StateBadge family="host" state={fresh(h) ? 'online' : 'offline'} compact lastSeen={fresh(h) ? undefined : h.lastSeenAt ?? undefined} now={now} /></span>
          <span class="tt"><a class="title row-link" href={`/hosts/${h.id}`} onClick={e => { e.preventDefault(); go(`/hosts/${h.id}`); }}>{h.id}</a>
            {h.platform && !h.id.includes(h.platform.slice(0, 3)) && <span class="id">{h.platform}</span>}
            <HarnessCounts counts={h.runningByHarness} label={`${h.id} workers by harness`} />
            {h.queued > 0 && <span class="hc"><Icon name="list-ordered" label="Queued commands" /><Num m={{ value: h.queued, basis: 'measured' }} kind="count" /></span>}</span>
          <span>{h.lastSeenAt ? <AgeChip t={h.lastSeenAt} now={now} alertAfter={15 * 60} tone="attention" alertLabel="not seen for a while" /> : <span class="muted">never</span>}</span>
        </div>)}
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
  const detail = sel && <article class="detail" data-ui="detail" tabIndex={-1} aria-label={sel.id}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => go('/hosts')} /><h2 class="detail-title">{sel.id}</h2></header>
    <DetailSection title="State"><KvRow icon="activity" label="Last seen" meta={sel.lastSeenAt ? undefined : 'never'} value={sel.lastSeenAt ? <AgeChip t={sel.lastSeenAt} now={now} /> : undefined} />
      <KvRow icon="panels-top-left" label="Platform" meta={sel.platform ?? 'unknown'} /></DetailSection>
    <DetailSection title="Workers"><KvRow icon="activity" label="Running" value={<HarnessCounts counts={sel.runningByHarness} label="by harness" />} /><KvRow icon="list-ordered" label="Queued commands" value={<Num m={{ value: sel.queued, basis: 'measured' }} kind="count" />} /></DetailSection>
    {fresh(sel) && sel.problems?.length > 0 && <DetailSection title="Problems">{sel.problems.map((p: any) => <KvRow key={p.route} icon="activity" label={`Edge answers ${p.route} with ${p.status}`} meta={p.detail || p.location || undefined} />)}</DetailSection>}
    <DetailSection title="Capabilities">{sel.capabilities.length ? sel.capabilities.map((c: string) => <KvRow key={c} icon="check" label={c} />) : <span class="muted">none reported</span>}</DetailSection>
    <DetailSection title="Store"><KvRow icon="save" label="Store" value={<FillBar total={null} segments={[]} />} /></DetailSection>
  </article>;
  return <ListDetail list={list} detail={detail || undefined} detailKey={id} />;
}
