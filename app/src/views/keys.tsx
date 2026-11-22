// API keys (docs/designs/fleet-connections.md §1, C0): connections with holder, provider, tools, keys (fingerprint and state
// only), access and intake. Key values go in, never come out: the add field is write-only and the answer is a fingerprint.
import { useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import { AgeChip, Button, Choice, DetailSection, Disclosure, Empty, ErrorBanner, KvRow, ProblemRow, SettingField, Skeleton, StateBadge, bytes, duration, type StateOf } from '../../../packages/ui/src/index.ts';
import { op, type OpName, type Ops } from '../api.ts';
import { useOp } from '../store.ts';
import type { ConnectionView, ConnectionKey } from '../../../contracts/generated/ops.ts';
import type { GatewayKeyStrategy } from '../../../contracts/generated/gateway.ts';

/** A connection as the screen shows it: the lists the reply omits when empty filled in. */
export type Conn = Omit<ConnectionView, 'keys' | 'tools' | 'access'> & {
  keys: ConnectionKey[];
  tools: string[];
  access: { people: string[]; projects: string[]; experiments: string[] };
};

/** A person by name: the email's local part (formats.md, Person). */
export const person = (h: string) => h.split('@')[0];
const ids = (v: string) => v.split(',').map(x => x.trim()).filter(Boolean);

export const normalizeConnections = (list: readonly ConnectionView[]): Conn[] =>
  list.map(c => ({
    ...c,
    keys: c.keys ?? [],
    tools: c.tools ?? [],
    access: {
      people: c.access.people ?? [],
      projects: c.access.projects ?? [],
      experiments: c.access.experiments ?? [],
    },
  }));

export const connectionScope = (c: Conn): string =>
  `${c.access.projects.includes('*') ? 'all' : c.access.projects.join(', ') || 'none'}${c.access.experiments.length ? ' + Lab' : ''}`;

type LimitKey = 'fileBytes' | 'replyBytes' | 'stallMs' | 'answerMs';
const DEFAULT_STALL_MS = 60_000;
const DEFAULT_ANSWER_MS = 120_000;
const DEFAULT_COOLDOWN_S = 60;
const DEFAULT_QUOTA_COOLDOWN_S = 3600;
const KEY_STRATEGY_OPTIONS: { id: GatewayKeyStrategy; label: string }[] = [
  { id: 'sticky', label: 'Sticky' },
  { id: 'round_robin', label: 'Round robin' },
  { id: 'least_used', label: 'Least used' },
];

type LimitFieldDef = {
  key: LimitKey;
  label: string;
  value: string;
  help: string;
  diff: boolean;
};

function connectionLimitFields(limits: ConnectionView['limits'] | undefined): LimitFieldDef[] {
  const fileBytes = limits?.fileBytes;
  const replyBytes = limits?.replyBytes;
  const stallMs = limits?.stallMs;
  const answerMs = limits?.answerMs;
  return [
    {
      key: 'fileBytes',
      label: 'Upload cap',
      value: fileBytes != null ? String(fileBytes) : '',
      help: fileBytes != null ? `${bytes(fileBytes).text} · bytes; clear to restore no cap` : 'Bytes; default: no cap',
      diff: fileBytes != null,
    },
    {
      key: 'replyBytes',
      label: 'Download cap',
      value: replyBytes != null ? String(replyBytes) : '',
      help: replyBytes != null ? `${bytes(replyBytes).text} · bytes; clear to restore no cap` : 'Bytes; default: no cap',
      diff: replyBytes != null,
    },
    {
      key: 'stallMs',
      label: 'Stall timeout',
      value: String(stallMs ?? DEFAULT_STALL_MS),
      help: stallMs != null && stallMs !== DEFAULT_STALL_MS
        ? `${duration(stallMs / 1000).text} · ms; default ${duration(DEFAULT_STALL_MS / 1000).text} (${DEFAULT_STALL_MS} ms)`
        : `${duration(DEFAULT_STALL_MS / 1000).text} (${DEFAULT_STALL_MS} ms)`,
      diff: stallMs != null && stallMs !== DEFAULT_STALL_MS,
    },
    {
      key: 'answerMs',
      label: 'Answer timeout',
      value: String(answerMs ?? DEFAULT_ANSWER_MS),
      help: answerMs != null && answerMs !== DEFAULT_ANSWER_MS
        ? `${duration(answerMs / 1000).text} · ms; default ${duration(DEFAULT_ANSWER_MS / 1000).text} (${DEFAULT_ANSWER_MS} ms)`
        : `${duration(DEFAULT_ANSWER_MS / 1000).text} (${DEFAULT_ANSWER_MS} ms)`,
      diff: answerMs != null && answerMs !== DEFAULT_ANSWER_MS,
    },
  ];
}

export function KeyDetail({
  id,
  conn: sel,
  loading,
  error: readError,
  onRetry,
  onClose,
}: {
  id: string;
  conn: Conn | undefined;
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  onClose: () => void;
}) {
  const [actionError, setActionError] = useState<string | null>(null);
  const now = Date.now();
  const run = <K extends OpName>(name: K, body: Ops[K]['request']) =>
    op(name, body).then(() => { setActionError(null); }, e => setActionError(e instanceof Error ? e.message : String(e)));

  if (!sel) {
    return (
      <article class="detail stack" data-ui="detail" tabIndex={-1} aria-label={id}>
        <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /><h2 class="detail-title">{id}</h2></header>
        {readError && <ErrorBanner text={`Connections did not load: ${readError}`} onRetry={onRetry} />}
        {loading ? <Skeleton rows={5} /> : !readError && <Empty text={`Connection "${id}" not found.`} />}
      </article>
    );
  }

  const limitFields = connectionLimitFields(sel.limits);
  const diffLimits = limitFields.filter(f => f.diff);
  const inheritedLimits = limitFields.filter(f => !f.diff);
  const updatedAt = sel.updatedAt ?? sel.createdAt;

  const saveLimit = (key: LimitKey, raw: string) => {
    const trimmed = raw.trim();
    const next: NonNullable<ConnectionView['limits']> = { ...(sel.limits ?? {}) };
    if (!trimmed) {
      delete next[key];
    } else {
      const n = Number(trimmed);
      if (!Number.isInteger(n) || n < 1) {
        setActionError(`${key} must be a positive integer.`);
        return;
      }
      if ((key === 'stallMs' && n === DEFAULT_STALL_MS) || (key === 'answerMs' && n === DEFAULT_ANSWER_MS)) {
        delete next[key];
      } else {
        next[key] = n;
      }
    }
    void run('connections_set_limits', { connection: sel.name, limits: next });
  };

  const kp = sel.keyPolicy;
  const strategy: GatewayKeyStrategy = kp?.strategy ?? 'sticky';
  const saveKeyPolicy = (patch: Partial<NonNullable<ConnectionView['keyPolicy']>>) => {
    const next: NonNullable<ConnectionView['keyPolicy']> = { ...(kp ?? {}), ...patch };
    if (next.strategy === 'sticky' || !next.strategy) delete next.strategy;
    if (!next.failoverStatuses?.length) delete next.failoverStatuses;
    if (next.cooldownSeconds == null || next.cooldownSeconds === DEFAULT_COOLDOWN_S) delete next.cooldownSeconds;
    if (next.quotaCooldownSeconds == null || next.quotaCooldownSeconds === DEFAULT_QUOTA_COOLDOWN_S) delete next.quotaCooldownSeconds;
    void run('connections_set_key_policy', { connection: sel.name, keyPolicy: next });
  };
  const saveFailoverStatuses = (raw: string) => {
    const parts = raw.split(',').map(s => s.trim()).filter(Boolean);
    const seen = new Set<number>();
    const parsed: number[] = [];
    for (const p of parts) {
      const n = Number(p);
      if (!Number.isInteger(n) || n < 400 || n > 599) {
        setActionError('Failover HTTP statuses must be integers between 400 and 599 (e.g. 429, 432).');
        return;
      }
      if (!seen.has(n)) {
        seen.add(n);
        parsed.push(n);
      }
    }
    saveKeyPolicy({ failoverStatuses: parsed.length ? parsed : undefined });
  };
  const saveCooldown = (field: 'cooldownSeconds' | 'quotaCooldownSeconds', raw: string) => {
    const trimmed = raw.trim();
    if (!trimmed) {
      saveKeyPolicy({ [field]: undefined });
      return;
    }
    const n = Number(trimmed);
    if (!Number.isInteger(n) || n < 1) {
      setActionError(`${field} must be a positive integer.`);
      return;
    }
    saveKeyPolicy({ [field]: n });
  };

  return (
    <article class="detail stack" data-ui="detail" tabIndex={-1} aria-label={sel.name}>
      <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onClose} /><h2 class="detail-title">{sel.name}</h2></header>
      {readError && <ErrorBanner text={`Connections did not load: ${readError}`} onRetry={onRetry} />}
      {actionError && <ErrorBanner text={actionError} />}
      <DetailSection title="Connection">
        {sel.provider !== sel.name && <KvRow icon="bot" label="Provider" meta={sel.provider} />}
        <KvRow icon="shield" label="Holder" meta={sel.holder} />
        <KvRow icon="list" label="Tools" meta={sel.tools.length ? sel.tools.join(', ') : '—'} />
        <KvRow icon="activity" label="Spend" meta={sel.spend?.basis && sel.spend.basis !== 'unavailable' ? sel.spend.basis : <span data-tip="Spend unavailable">—</span>} />
      </DetailSection>
      <DetailSection title="Keys">
        {sel.keys.map(k => (
          <KvRow
            key={k.id}
            icon="shield"
            label={k.fingerprint}
            meta={k.disabledReason ?? (k.problem ? `HTTP ${k.problem.status}: ${k.problem.message}` : undefined)}
            value={<>
              <StateBadge family="key" state={k.state as StateOf<'key'>} compact />
              <AgeChip t={k.addedAt} />
              {k.reconcile && (
                <span class={(k.reconcile.difference ?? 0) > 0 ? 'attention' : 'muted'} data-tip={k.reconcile.unknown ? `Usage unknown: ${k.reconcile.unknown}` : 'Provider usage since the first check, minus credits recorded through Fleet'}>
                  {k.reconcile.unknown ? '—' : (k.reconcile.difference ?? 0) > 0 ? `+${k.reconcile.difference} elsewhere` : 'matches'}
                </span>
              )}
              {k.state === 'active' && <Button label="Drain" icon="pause" iconOnly onClick={() => run('connections_key_state', { keyId: k.fingerprint, state: 'draining' })} />}
              {(k.state === 'draining' || k.state === 'disabled') && <Button label="Activate" icon="play" iconOnly onClick={() => run('connections_key_state', { keyId: k.fingerprint, state: 'active' })} />}
              {k.state !== 'retired' && <Button label="Retire" icon="trash-2" iconOnly kind="danger" onClick={() => run('connections_key_state', { keyId: k.fingerprint, state: 'retired' })} />}
            </>}
          />
        ))}
        <SettingField label="Add key" value="" secret help="Owner only. Stored on the coordinator host; Fleet shows only its fingerprint." now={now} status={{ kind: 'applied', at: sel.createdAt }}
          onSave={v => { if (v.trim()) void run('connections_key_add', { connection: sel.name, value: v.trim() }); }} />
      </DetailSection>
      <DetailSection title="Access">
        <SettingField label="Projects" value={sel.access.projects.join(', ')} help="Project ids, or * for every project" now={now} status={{ kind: 'applied', at: updatedAt }}
          onSave={v => run('connections_set_access', { connection: sel.name, access: { ...sel.access, projects: ids(v) } })} />
        <SettingField label="People" value={sel.access.people.join(', ')} help="E-mails of people whose work may use it" now={now} status={{ kind: 'applied', at: updatedAt }}
          onSave={v => run('connections_set_access', { connection: sel.name, access: { ...sel.access, people: ids(v) } })} />
        <SettingField label="Experiments" value={sel.access.experiments.join(', ')} help="Lab experiment ids" now={now} status={{ kind: 'applied', at: updatedAt }}
          onSave={v => run('connections_set_access', { connection: sel.name, access: { ...sel.access, experiments: ids(v) } })} />
        <KvRow icon="activity" label="Intake" value={<Button label={sel.intake === 'all' ? 'Pause' : 'Resume'} icon={sel.intake === 'all' ? 'pause' : 'play'} onClick={() => run('connections_set_intake', { connection: sel.name, intake: sel.intake === 'all' ? 'paused' : 'all' })} />} />
      </DetailSection>
      <DetailSection title="Key balancing & failover">
        <KvRow
          icon="activity"
          label="Strategy"
          meta="Sticky keeps one key; round robin rotates per call; least used prefers lowest recorded usage"
          value={<Choice label="Key balancing strategy" options={KEY_STRATEGY_OPTIONS} value={strategy} onChange={id => saveKeyPolicy({ strategy: id as GatewayKeyStrategy })} />}
        />
        <SettingField
          label="Failover statuses"
          value={(kp?.failoverStatuses ?? []).join(', ')}
          help="Comma-separated HTTP 400–599 codes that cool down the key and fail over (e.g. 429, 432); empty disables"
          now={now}
          status={{ kind: 'applied', at: updatedAt }}
          onSave={saveFailoverStatuses}
        />
        <SettingField
          label="Cooldown (s)"
          value={String(kp?.cooldownSeconds ?? DEFAULT_COOLDOWN_S)}
          help={`Seconds before retrying a failed-over key; default ${duration(DEFAULT_COOLDOWN_S).text} (${DEFAULT_COOLDOWN_S}s)`}
          now={now}
          status={{ kind: 'applied', at: updatedAt }}
          onSave={v => saveCooldown('cooldownSeconds', v)}
        />
        <SettingField
          label="Quota cooldown (s)"
          value={String(kp?.quotaCooldownSeconds ?? DEFAULT_QUOTA_COOLDOWN_S)}
          help={`Seconds before retrying a key after HTTP 432; default ${duration(DEFAULT_QUOTA_COOLDOWN_S).text} (${DEFAULT_QUOTA_COOLDOWN_S}s)`}
          now={now}
          status={{ kind: 'applied', at: updatedAt }}
          onSave={v => saveCooldown('quotaCooldownSeconds', v)}
        />
      </DetailSection>
      <DetailSection title="Limits">
        {diffLimits.length > 0 ? diffLimits.map(f => (
          <SettingField key={f.key} label={f.label} value={f.value} help={f.help} now={now} status={{ kind: 'applied', at: updatedAt }}
            onSave={v => saveLimit(f.key, v)} />
        )) : (
          <KvRow icon="circle-check" label="Matches gateway defaults" meta="No size caps · 1m stall · 2m answer" />
        )}
        {inheritedLimits.length > 0 && (
          <Disclosure summary={`Inherited defaults (${inheritedLimits.length})`} summaryClass="muted">
            {inheritedLimits.map(f => (
              <SettingField key={f.key} label={f.label} value={f.value} help={f.help} now={now} status={{ kind: 'applied', at: updatedAt }}
                onSave={v => saveLimit(f.key, v)} />
            ))}
          </Disclosure>
        )}
      </DetailSection>
    </article>
  );
}

export function KeysIdle({
  conns,
  loading,
  error,
  onRetry,
  onOpen,
}: {
  conns: readonly Conn[];
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  onOpen: (path: string) => void;
}) {
  if (loading && !conns.length) {
    return (
      <article class="detail stack" aria-label="Connections & API keys">
        <header class="detail-h"><h2 class="detail-title">Connections & API keys</h2></header>
        <Skeleton rows={4} />
      </article>
    );
  }
  const unrecorded = conns.map(c => ({ c, keys: c.keys.filter(k => k.reconcile && !k.reconcile.unknown && (k.reconcile.difference ?? 0) > 0) })).filter(x => x.keys.length);
  const retired = conns.map(c => ({ c, n: c.keys.filter(k => k.state === 'retired').length })).filter(x => x.n);
  const limited = conns.filter(c => !c.access.projects.includes('*'));
  return (
    <article class="detail stack" aria-label="Connections & API keys">
      <header class="detail-h"><h2 class="detail-title">Connections & API keys</h2></header>
      {error && <ErrorBanner text={`Connections did not load: ${error}`} onRetry={onRetry} />}
      {unrecorded.map(({ c, keys }) => (
        <ProblemRow
          key={c.name}
          severity="warning"
          title={`${c.name} counts ${keys.reduce((n, k) => n + (k.reconcile?.difference ?? 0), 0)} calls Fleet did not record`}
          facts={`${keys.length} of its ${c.keys.length} keys: ${keys.map(k => `${k.reconcile?.providerDelta} at the provider against ${k.reconcile?.recorded} recorded`).join(', ')}`}
          actions={[{ label: `Open ${c.name}`, primary: true, onClick: () => onOpen(`/keys/${encodeURIComponent(c.name)}`) }]}
        />
      ))}
      {limited.length > 0 && (
        <DetailSection title="Limited to some projects">
          {limited.map(c => (
            <KvRow
              key={c.name}
              icon="shield"
              label={c.name}
              meta={[c.provider !== c.name ? c.provider : '', `${c.keys.filter(k => k.state === 'active').length} active`].filter(Boolean).join(' · ')}
              value={connectionScope(c)}
            />
          ))}
        </DetailSection>
      )}
      {retired.length > 0 && (
        <DetailSection title="Retired keys">
          {retired.map(({ c, n }) => <KvRow key={c.name} icon="circle-slash" label={c.name} value={`${n} retired`} />)}
        </DetailSection>
      )}
      {!error && !unrecorded.length && !limited.length && !retired.length && <Empty text="Every connection serves every project; no key needs a look." />}
    </article>
  );
}

export function Keys({ id }: { id?: string }) {
  const [, go] = useLocation();
  const { data, error, reload } = useOp('connections_list', {});
  const conns = data ? normalizeConnections(data.connections) : [];
  const sel = id ? conns.find(c => c.name === id) : undefined;
  return id
    ? <KeyDetail id={id} conn={sel} loading={!data && !error} error={error} onRetry={reload} onClose={() => go('/keys')} />
    : <KeysIdle conns={conns} loading={!data && !error} error={error} onRetry={reload} onOpen={go} />;
}
