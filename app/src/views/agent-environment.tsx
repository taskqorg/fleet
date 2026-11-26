// Library › Changes › Agent environment (docs/design-system/pages/changes.md, board drafts/agent-garden frames 3 and 6):
// what every future agent gets, each setting with its drift state and last change, or the open agent gap finding about
// it (agent_gaps, System › Agents). Reads: system_changes and the agent_gaps view.
import { createMemo, For, Show } from 'solid-js';
import { DetailSection, DriftRow, day, moment, type Tone } from '../../../packages/ui/src/index.ts';
import type { AgentGapClass, GrantDrift, SystemChangesReply } from '../../../contracts/generated/ops.ts';
import { useView } from '../store.ts';

type Row = { key: string; name: string; detail?: string; chip: { text: string; tone: Tone; tip?: string }; fact?: string; factTip?: string; href?: string };
const ok = (text = 'In sync'): Row['chip'] => ({ text, tone: 'success' });
const names = (xs: string[]) => xs.length <= 2 ? xs.join(' and ') : `${xs.slice(0, -1).join(', ')} and ${xs.at(-1)}`;
const few = (xs: string[]) => xs.length <= 2 ? xs.join(', ') : `${xs[0]} and ${xs.length - 1} more`;
/** A person by the part before @ (site-agents, alex); the coordinator as Fleet; an agent execution id as agent. */
const who = (actor: string) => actor === 'fleet' ? 'Fleet' : /^[0-9a-f-]{20,}$|^(exec|execution)[:_-]/i.test(actor) ? 'agent' : actor.split('@')[0];
const hostPath = (id: string) => `/hosts/${encodeURIComponent(id)}`;

/** "+6 granted 26 Nov · alex": the connection's last audited change. */
const lastChange = (c: GrantDrift, now: number) => {
  const l = c.lastChange;
  if (!l) return {};
  const what = l.field === 'tools' ? 'tools changed' : l.field === 'keyPolicy' ? 'key policy changed' : `${l.field} changed`;
  return { fact: `${what} ${day(l.at, now)} · ${who(l.actor)}`, factTip: `${moment(l.at, now)} · ${l.actor} · ${l.op}` };
};

function connectionRows(d: SystemChangesReply, now: number): Row[] {
  return d.agentEnvironment.grants.connections.filter(c => c.needed.length || c.stale.length).map(c => {
    const missing = c.missing.map(m => m.tool);
    const stale = c.stale.length ? ` · ${names(c.stale)} no longer in the catalogue` : '';
    return {
      key: c.name, name: c.name, ...lastChange(c, now),
      detail: missing.length
        ? `${c.granted.length} granted · lacks ${names(missing)}`
        : `${c.granted.length} granted · all ${c.needed.length} that code and profiles use${stale}`,
      chip: missing.length
        ? { text: `${missing.length} missing`, tone: 'attention', tip: c.missing.map(m => `${m.tool}: ${m.users.join('; ')}`).join(' · ') + ' · fix: connections_set_tools' }
        : ok(),
    };
  });
}

function keyRows(d: SystemChangesReply, now: number): Row[] {
  return d.agentEnvironment.grants.connections.filter(c => c.keys.total > 1 || c.keyPolicy).map(c => {
    const p = c.keyPolicy, fail = p?.failoverStatuses ?? [];
    const strategy = p?.strategy === 'least_used' ? 'least used' : 'sticky';
    return {
      key: c.name, name: c.name, ...lastChange(c, now),
      detail: [`${c.keys.active} of ${c.keys.total} keys active`, strategy, fail.length ? `next key on ${fail.join(', ')}` : 'no next key'].join(' · '),
      chip: c.keys.active === 0 ? { text: 'No active key', tone: 'danger' }
        : c.keys.active > 1 && !fail.length ? { text: 'No failover', tone: 'attention', tip: 'connections_set_key_policy sets failoverStatuses' }
        : ok('Ready'),
    };
  });
}

function hostToolRows(d: SystemChangesReply): Row[] {
  const kind = d.components.find(k => k.kind === 'tool');
  if (!kind) return [];
  return kind.components.map(c => {
    // A host that has not reported (unknown) is neither on the pin nor drifting.
    const off = c.hosts.filter(h => h.state === 'behind' || h.state === 'installing' || h.state === 'refused' || h.state === 'failing');
    const known = c.hosts.filter(h => h.state !== 'unknown').length, on = known - off.length;
    return {
      key: c.name, name: c.name, href: off[0] && hostPath(off[0].hostId),
      detail: !kind.enabled ? 'pins off · hosts keep their own'
        : c.pinned ? `${c.pinned} · ${on === known ? 'pinned on every host' : `on ${on} of ${known} hosts`}` : 'not pinned',
      chip: off.length
        ? { text: `${off.some(h => h.state === 'refused' || h.state === 'failing') ? 'Failing' : 'Missing'} on ${names(off.map(h => h.hostId))}`, tone: off.some(h => h.state === 'installing') ? 'running' : 'attention',
          tip: off.map(h => `${h.hostId}: ${h.state}${h.reason ? ` (${h.reason})` : h.observed ? ` (${h.observed})` : ''}`).join(' · ') }
        : ok(),
    };
  });
}

function sandboxRows(d: SystemChangesReply): Row[] {
  const hosts = d.versions.hosts, behind = hosts.filter(h => h.behind).map(h => h.id);
  const release = d.versions.coordinator.offered ?? d.versions.coordinator.version;
  return d.agentEnvironment.sandbox.map(g => ({
    key: `${g.harness} ${g.mode} ${g.network}`, name: `${g.harness[0].toUpperCase()}${g.harness.slice(1)}: ${few(g.profiles)}`,
    detail: `${g.mode} · network ${g.network ? 'on' : 'off'}`,
    chip: behind.length ? { text: `Behind on ${names(behind)}`, tone: 'attention', tip: `the executor of release ${release} applies it` }
      : ok(`Applied on ${hosts.length} host${hosts.length === 1 ? '' : 's'}`),
    fact: `release ${release}`, factTip: g.profiles.join(', '),
  }));
}

function profileRows(d: SystemChangesReply): Row[] {
  const drift = [...d.repositoryChanged, ...d.ahead, ...d.storeOnly].filter(x => x.kind === 'agent' && !x.experiment);
  const synced = Number(d.agentEnvironment.profiles) - drift.length;
  const rows: Row[] = drift.map(x => ({
    key: x.id, name: x.name, href: `/library/agents/${encodeURIComponent(x.id)}`,
    detail: x.state === 'repository-changed' ? 'the repository changed it under a Fleet edit'
      : x.state === 'repository-deleted' ? 'the repository removed its file'
      : x.publish?.pr ? `on its way to the repository · PR #${x.publish.pr.number}` : 'edited in Fleet',
    chip: x.state === 'repository-changed' ? { text: 'Conflict', tone: 'attention' }
      : x.state === 'repository-deleted' ? { text: 'Deleted in repository', tone: 'attention' }
      : { text: x.publish ? 'On its way' : 'Edited in Fleet', tone: 'neutral' },
    fact: x.at ? day(Date.parse(x.at)) : undefined,
  }));
  if (synced > 0) rows.push({ key: 'synced', name: `${synced} profile${synced === 1 ? '' : 's'}`, detail: 'in sync with config/agents', chip: ok() });
  return rows;
}

const drifting = (rows: Row[]) => rows.filter(r => r.chip.tone !== 'success' && r.chip.tone !== 'neutral').length;

/** The Agent environment groups of Changes; the count of drifting settings goes to the page summary. */
export function agentEnvironmentDrift(d: SystemChangesReply) {
  const now = Date.now();
  return [connectionRows(d, now), keyRows(d, now), hostToolRows(d), sandboxRows(d), profileRows(d)].reduce((n, rows) => n + drifting(rows), 0);
}

type Group = 'connection' | 'keys' | 'host-tool' | 'sandbox' | 'profile';
const LIVE_FINDING = ['open', 'investigating', 'linked', 'watching', 'still-failing'];
const connectionOf = (c: AgentGapClass) => {
  const arg = c.class.split(':')[1] ?? '';
  return arg.includes('/') ? arg.split('/')[0] : c.tools[0]?.key.split('/')[0] ?? '';
};

/** The agent gap finding about a row (board drafts/agent-garden frame 3): its class names the row's setting. */
function findingFor(classes: readonly AgentGapClass[], group: Group, key: string): AgentGapClass | undefined {
  return classes.find(c => {
    if (!c.finding || !LIVE_FINDING.includes(c.finding.state)) return false;
    const arg = c.class.split(':')[1] ?? '';
    switch (group) {
      case 'host-tool': return (c.family === 'missing-command' || c.family === 'missing-module') && key.split(/[ ,]+/).includes(arg);
      case 'keys': return c.family === 'gateway-quota' && connectionOf(c) === key;
      case 'connection': return (c.family === 'mcp-refused' || c.family === 'gateway-unknown-tool' || c.family === 'gateway-rejected') && connectionOf(c) === key;
      case 'sandbox': return c.family.startsWith('sandbox-') && c.harnesses.some(h => h.key === key.split(' ')[0]);
      case 'profile': return (c.family === 'tool-unavailable' || c.family === 'model-unsupported') && c.profiles.some(x => x.key === key);
    }
  });
}

/** A row with its finding as its last fact, opening System › Agents when the row opens nothing else. */
function withFinding(r: Row, c: AgentGapClass | undefined): Row {
  if (!c?.finding) return r;
  const state = c.finding.state === 'open' ? '' : ` · ${c.finding.state.replace('-', ' ')}`;
  return { ...r, fact: `finding: ${c.label}${state}`, factTip: [`${c.count} failures in ${c.executions} runs`, c.setting.text, r.fact].filter(Boolean).join(' · '), href: r.href ?? `/agent-gaps/${encodeURIComponent(c.class)}` };
}

export function AgentEnvironment(p: { data: SystemChangesReply; onOpen: (path: string) => void }) {
  const gaps = useView('agent_gaps', {}, { quiet: 30_000 });
  const groups = createMemo(() => {
    const now = Date.now();
    const classes = gaps.data?.classes ?? [];
    const mark = (group: Group, rows: Row[]) => rows.map(r => withFinding(r, findingFor(classes, group, r.key)));
    return [
      { title: 'Connection tools', rows: mark('connection', connectionRows(p.data, now)) },
      { title: 'Key pools', rows: mark('keys', keyRows(p.data, now)) },
      { title: 'Host tools', rows: mark('host-tool', hostToolRows(p.data)) },
      { title: 'Sandbox policy', rows: mark('sandbox', sandboxRows(p.data)) },
      { title: 'Profiles', rows: mark('profile', profileRows(p.data)) },
    ].filter(g => g.rows.length);
  });
  return <For each={groups()}>{g => {
    const n = () => drifting(g.rows);
    return <DetailSection title={g.title} count={g.rows.length}
      action={<span class={n() ? 't-attention' : 'muted'}>{n() ? `${n()} drift` : 'in sync'}</span>}>
      <ul class="drift-rows" aria-label={g.title}>
        <For each={g.rows}>{r => (
          <DriftRow name={r.name} detail={r.detail} chip={r.chip} fact={r.fact} factTip={r.factTip}
            href={r.href} onOpen={r.href ? () => p.onOpen(r.href!) : undefined} />
        )}</For>
      </ul>
    </DetailSection>;
  }}</For>;
}
