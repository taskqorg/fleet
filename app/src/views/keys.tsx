// API keys (docs/designs/fleet-connections.md §1, C0): connections with holder, provider, tools, keys (fingerprint and state
// only), access and intake. Key values go in, never come out: the add field is write-only and the answer is a fingerprint.
import { useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import { ListDetail, StateBadge, DetailSection, KvRow, Empty, Button, SettingField, AgeChip } from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useDoc } from '../store.ts';

const ids = (v: string) => v.split(',').map(x => x.trim()).filter(Boolean);

export function Keys({ id }: { id?: string }) {
  const [, go] = useLocation();
  const [error, setError] = useState<string | null>(null);
  const { data } = useDoc<any>('api-keys', () => op('connections_list', {}), c => c.kind === 'api-connection' || c.kind === 'connection-key' || c.kind === 'setting-change');
  const now = Date.now();
  if (!data) return <><header class="page-h"><h1>API keys</h1></header><Empty text="Loading…" /></>;
  const run = (name: string, body: any) => op(name, body).then(() => { setError(null); }, e => setError(String(e.message ?? e)));
  const sel = data.connections.find((c: any) => c.name === id);
  const list = <>
    <header class="page-h"><h1>API keys</h1></header>
    <section class="panel">
      <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) auto' }}>
        {data.connections.length === 0 && <Empty text="No API keys yet" />}
        {data.connections.map((c: any) => <div key={c.name} role="row" class={`vl-row static${c.name === id ? ' sel' : ''}`}>
          <span class="lead"><StateBadge family="intake" state={c.intake} compact /></span>
          <span class="tt"><a class="title row-link" href={`/keys/${c.name}`} onClick={e => { e.preventDefault(); go(`/keys/${c.name}`); }}>{c.name}</a><span class="id">{c.provider}</span><span class="muted">{c.holder}</span></span>
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
        value={<><StateBadge family="key" state={k.state} compact /><AgeChip t={k.addedAt} now={now} />
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
    </DetailSection>
  </article>;
  return <ListDetail list={list} detail={detail || undefined} detailKey={id} />;
}
