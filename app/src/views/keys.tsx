// API keys (docs/designs/fleet-connections.md §1, C0): connections with holder, provider, tools, keys (fingerprint and state
// only), access and intake. Key values go in, never come out: the add field is write-only and the answer is a fingerprint.
import { useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import { ListDetail, StateBadge, DetailSection, KvRow, Empty, Button, SettingField, AgeChip, ProblemRow, bytes, duration } from '../../../packages/ui/src/index.ts';
import { op, type OpName, type Ops } from '../api.ts';
import { useDoc } from '../store.ts';

/** A person by name: the email's local part (formats.md, Person). */
const person = (h: string) => h.split('@')[0];
const ids = (v: string) => v.split(',').map(x => x.trim()).filter(Boolean);

// A connection's file limits with the gateway defaults filled in (fleet-connections.md §9): no size caps, 60 s stall, 120 s answer.
const fileLimits = (l: { fileBytes?: number; replyBytes?: number; stallMs?: number; answerMs?: number } | undefined) => [
  `upload ${l?.fileBytes ? bytes(l.fileBytes).text : 'no cap'}`, `download ${l?.replyBytes ? bytes(l.replyBytes).text : 'no cap'}`,
  `stall ${duration((l?.stallMs ?? 60_000) / 1000).text}`, `answer ${duration((l?.answerMs ?? 120_000) / 1000).text}`,
].join(' · ');

const fileTimeouts = (l?: { stallMs?: number; answerMs?: number }) =>
  `Stall ${l?.stallMs ?? 60_000} ms · Answer ${l?.answerMs ?? 120_000} ms`;

export function Keys({ id }: { id?: string }) {
  const [, go] = useLocation();
  const [error, setError] = useState<string | null>(null);
  const { data } = useDoc<any>('api-keys', () => op('connections_list', {}), c => c.kind === 'api-connection' || c.kind === 'connection-key' || c.kind === 'setting-change');
  const now = Date.now();
  if (!data) return <Empty text="Loading…" />;
  const run = <K extends OpName>(name: K, body: Ops[K]['request']) => op(name, body).then(() => { setError(null); }, e => setError(String(e.message ?? e)));
  // The reply omits empty lists (keys, tools, access lists): a connection without keys yet must still render.
  const conns = data.connections.map((c: any) => ({ ...c, keys: c.keys ?? [], tools: c.tools ?? [], access: { people: [], projects: [], experiments: [], ...c.access } }));
  const sel = conns.find((c: any) => c.name === id);
  // A word once per region (OR-17): the holder most connections share is named once above the list, a row names only another
  // holder; the provider shows only where it differs from the connection's name.
  const holders = new Map<string, number>(); for (const c of conns) holders.set(c.holder, (holders.get(c.holder) ?? 0) + 1);
  const mainHolder = [...holders].sort((a, b) => b[1] - a[1])[0]?.[0];
  const scope = (c: any) => `${c.access.projects.includes('*') ? 'all' : c.access.projects.join(', ') || 'none'}${c.access.experiments.length ? ' + Lab' : ''}`;
  const list = <>
    {mainHolder && <header class="page-h"><span class="muted">Held by {person(mainHolder)}{holders.size > 1 ? ', unless a row says otherwise' : ''}</span></header>}
    <section class="panel">
      <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) auto auto' }}>
        {conns.length === 0 && <Empty text="No API keys yet" />}
        {conns.map((c: any) => <div key={c.name} role="row" class={`vl-row static${c.name === id ? ' sel' : ''}`}>
          <span class="lead"><StateBadge family="intake" state={c.intake} compact /></span>
          <span class="tt"><a class="title row-link" href={`/keys/${c.name}`} onClick={e => { e.preventDefault(); go(`/keys/${c.name}`); }}>{c.name}</a>{c.provider !== c.name && <span class="id">{c.provider}</span>}{c.holder !== mainHolder && <span class="muted">{person(c.holder)}</span>}</span>
          <span class="mono muted" data-tip="Projects this connection serves">{scope(c)}</span>
          <span class="r mono" data-tip="Active keys of all keys in use">{c.keys.filter((k: any) => k.state === 'active').length}/{c.keys.filter((k: any) => k.state !== 'retired').length}</span>
        </div>)}
      </div>
    </section>
  </>;
  const detail = sel && <article class="detail" data-ui="detail" tabIndex={-1} aria-label={sel.name}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => go('/keys')} /><h2 class="detail-title">{sel.name}</h2></header>
    {error && <div class="error" role="alert">{error}</div>}
    <DetailSection title="Connection">
      <KvRow icon="bot" label="Provider" meta={sel.provider} />
      <KvRow icon="shield" label="Holder" meta={sel.holder} />
      <KvRow icon="list" label="Tools" meta={sel.tools.join(', ')} />
      <KvRow icon="activity" label="Spend" meta={sel.spend.basis} />
    </DetailSection>
    <DetailSection title="Keys">
      {sel.keys.map((k: any) => <KvRow key={k.id} icon="shield" label={k.fingerprint} meta={k.disabledReason ?? (k.problem ? `HTTP ${k.problem.status}: ${k.problem.message}` : undefined)}
        value={<><StateBadge family="key" state={k.state} compact /><AgeChip t={k.addedAt} />
          {k.reconcile && <span class={k.reconcile.difference > 0 ? 'attention' : 'muted'} data-tip="Provider usage since the first check, minus credits recorded through Fleet">{k.reconcile.unknown ? 'usage unknown' : k.reconcile.difference > 0 ? `+${k.reconcile.difference} elsewhere` : 'matches'}</span>}
          {k.state === 'active' && <Button label="Drain" icon="pause" iconOnly onClick={() => run('connections_key_state', { keyId: k.fingerprint, state: 'draining' })} />}
          {(k.state === 'draining' || k.state === 'disabled') && <Button label="Activate" icon="play" iconOnly onClick={() => run('connections_key_state', { keyId: k.fingerprint, state: 'active' })} />}
          {k.state !== 'retired' && <Button label="Retire" icon="trash-2" iconOnly kind="danger" onClick={() => run('connections_key_state', { keyId: k.fingerprint, state: 'retired' })} />}</>} />)}
      <SettingField label="Add key" value="" secret help="Owner only. Stored on the coordinator host; Fleet shows only its fingerprint." now={now} status={{ kind: 'applied', at: sel.createdAt }}
        onSave={v => { if (v.trim()) run('connections_key_add', { connection: sel.name, value: v.trim() }); }} />
    </DetailSection>
    <DetailSection title="Access">
      <SettingField label="Projects" value={sel.access.projects.join(', ')} help="Project ids, or * for every project" now={now} status={{ kind: 'applied', at: sel.updatedAt ?? sel.createdAt }}
        onSave={v => run('connections_set_access', { connection: sel.name, access: { ...sel.access, projects: ids(v) } })} />
      <SettingField label="People" value={sel.access.people.join(', ')} help="E-mails of people whose work may use it" now={now} status={{ kind: 'applied', at: sel.updatedAt ?? sel.createdAt }}
        onSave={v => run('connections_set_access', { connection: sel.name, access: { ...sel.access, people: ids(v) } })} />
      <SettingField label="Experiments" value={sel.access.experiments.join(', ')} help="Lab experiment ids" now={now} status={{ kind: 'applied', at: sel.updatedAt ?? sel.createdAt }}
        onSave={v => run('connections_set_access', { connection: sel.name, access: { ...sel.access, experiments: ids(v) } })} />
      <KvRow icon="activity" label="Intake" value={<Button label={sel.intake === 'all' ? 'Pause' : 'Resume'} icon={sel.intake === 'all' ? 'pause' : 'play'} onClick={() => run('connections_set_intake', { connection: sel.name, intake: sel.intake === 'all' ? 'paused' : 'all' })} />} />
      {/* File limits (fleet-connections.md §9) are set by the connections_set_limits operation; the screen shows them. */}
      <KvRow icon="gauge" label="Limits" value={<span data-tip={fileTimeouts(sel.limits)} aria-description={fileTimeouts(sel.limits)}>
        {fileLimits(sel.limits)}
      </span>} />
    </DetailSection>
  </article>;
  // Idle work area: what needs a look across connections (2026-11-20 decision): provider usage Fleet did not record, and retired keys.
  const unrecorded = conns.map((c: any) => ({ c, keys: c.keys.filter((k: any) => k.reconcile && !k.reconcile.unknown && k.reconcile.difference > 0) })).filter((x: any) => x.keys.length);
  const retired = conns.map((c: any) => ({ c, n: c.keys.filter((k: any) => k.state === 'retired').length })).filter((x: any) => x.n);
  const limited = conns.filter((c: any) => !c.access.projects.includes('*'));
  const idle = <article class="detail" aria-label="API keys">
    {unrecorded.map(({ c, keys }: any) => <ProblemRow key={c.name} severity="warning" title={`${c.name} counts ${keys.reduce((n: number, k: any) => n + k.reconcile.difference, 0)} calls Fleet did not record`}
      facts={`${keys.length} of its ${c.keys.length} keys: ${keys.map((k: any) => `${k.reconcile.providerDelta} at the provider against ${k.reconcile.recorded} recorded`).join(', ')}`}
      actions={[{ label: `Open ${c.name}`, primary: true, onClick: () => go(`/keys/${c.name}`) }]} />)}
    {limited.length > 0 && <DetailSection title="Limited to some projects">{limited.map((c: any) => <KvRow key={c.name} icon="shield" label={c.name} meta={`${c.provider} · ${c.keys.filter((k: any) => k.state === 'active').length} active`} value={scope(c)} />)}</DetailSection>}
    {retired.length > 0 && <DetailSection title="Retired keys">{retired.map(({ c, n }: any) => <KvRow key={c.name} icon="circle-slash" label={c.name} value={`${n} retired`} />)}</DetailSection>}
    {!unrecorded.length && !limited.length && !retired.length && <Empty text="Every connection serves every project; no key needs a look." />}
  </article>;
  return <ListDetail list={list} detail={detail || undefined} detailKey={id} idle={idle} />;
}
