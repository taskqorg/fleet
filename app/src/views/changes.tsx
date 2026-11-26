// Library › Changes (FLT-7, docs/design-system/pages/changes.md): drift across repository, store, releases, and hosts.
import { createMemo, createSignal, For, Show } from 'solid-js';
import { useLocation } from '../router.ts';
import {
  Button, ChangeRow, DataTable, DetailSection, Empty, ErrorBanner, OfflineMarker,
  Icon, LiveRow, ProblemRow, ShellStatus, timeOf, type IconName, type Life, type Tone,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useOp } from '../store.ts';
import type {
  ComponentKind, DeliveryStage, PinsStage, SystemChangeComponent, SystemChangeComponentHost, SystemChangeDefinition,
  SystemChangeKind, SystemChangesReply, SystemPinsProposal, SystemVersionsReply,
} from '../../../contracts/generated/ops.ts';
import type { UpdateResult } from '../../../contracts/generated/host-protocol.ts';
import { AgentEnvironment, agentEnvironmentDrift } from './agent-environment.tsx';

const defRoute = (d: SystemChangeDefinition) =>
  `/library/${d.kind === 'agent' ? 'agents' : d.kind === 'pipeline' ? 'pipelines' : 'methods'}/${encodeURIComponent(d.id)}`;
const taskRoute = (id: string) => `/tasks/${encodeURIComponent(id)}`;
const ms = (t?: string) => t ? Date.parse(t) : undefined;
const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/** Where a Fleet edit's pull request stands (the publish task's latest pull request, DeliveryStage). */
const PR_STAGE: Record<DeliveryStage, { text: string; tone: Tone } | 'failed'> = {
  pr_open: { text: 'open', tone: 'neutral' },
  pr_ci: { text: 'CI running', tone: 'running' },
  pr_failed: 'failed',
  mq_wait: { text: 'waits to merge', tone: 'running' },
  mq_ci: { text: 'merging', tone: 'running' },
  mq_failed: 'failed',
  merged: { text: 'merged · waits for release', tone: 'success' },
  releasing: { text: 'merged · releasing', tone: 'success' },
  installing: { text: 'merged · installing', tone: 'success' },
  installed: { text: 'released', tone: 'success' },
};
const TASK_STATE: Record<string, { text: string; tone: Tone }> = {
  queued: { text: 'queued', tone: 'neutral' },
  in_progress: { text: 'worker on it', tone: 'running' },
  waiting: { text: 'waits', tone: 'attention' },
  paused: { text: 'paused', tone: 'attention' },
  completed: { text: 'done', tone: 'success' },
};

/** A definition row's look: the frame's on-its-way, conflict, failed and made-in-Fleet rows (pages/changes.md). */
type RowView = { summary?: string; chip: { text: string; tone: Tone; tip?: string }; tone?: 'attention' | 'danger'; need: boolean; onWay: boolean;
  actions: { label: string; primary?: boolean; act: 'compare' | 'keep' | 'take' | 'task' | 'publish' }[] };
const rowView = (d: SystemChangeDefinition): RowView => {
  const runs = d.runs30d > 0 ? `${d.runs30d} runs in 30 d` : '';
  const said = [d.summary && `"${d.summary}"`, runs].filter(Boolean).join(' · ') || undefined;
  if (d.state === 'repository-changed') return { tone: 'attention', need: true, onWay: false, chip: { text: 'Conflict', tone: 'attention' },
    summary: `the repository changed it under your edit r${d.head}`,
    actions: [{ label: 'Compare', act: 'compare' }, { label: 'Keep mine', act: 'keep' }, { label: 'Take repository', primary: true, act: 'take' }] };
  if (d.state === 'repository-deleted') return { tone: 'attention', need: true, onWay: false, chip: { text: 'Deleted in repository', tone: 'attention' },
    summary: 'the repository removed its file; open it to delete or keep it', actions: [] };
  const pub = d.publish;
  if (pub?.pr) {
    const st = PR_STAGE[pub.pr.stage] ?? { text: pub.pr.stage, tone: 'neutral' };
    if (st === 'failed') return { tone: 'danger', need: true, onWay: false, chip: { text: 'Failed', tone: 'danger' },
      summary: `PR #${pub.pr.number} failed ${pub.pr.stage === 'mq_failed' ? 'in the merge queue' : 'its checks'}`, actions: [{ label: 'Open task', primary: true, act: 'task' }] };
    return { need: false, onWay: true, chip: { text: `PR #${pub.pr.number} · ${st.text}`, tone: st.tone }, summary: said, actions: [] };
  }
  if (pub) {
    const st = TASK_STATE[pub.state];
    if (!st) return { tone: 'danger', need: true, onWay: false, chip: { text: 'Failed', tone: 'danger' },
      summary: `${pub.code || 'its task'} ${pub.state || 'ended'} without a pull request`, actions: [{ label: 'Open task', primary: true, act: 'task' }] };
    return { need: false, onWay: true, chip: { text: `${pub.code} · ${st.text}`, tone: st.tone }, summary: said, actions: [] };
  }
  if (d.state === 'store-only') return { need: false, onWay: false, chip: { text: 'New in Fleet', tone: 'neutral' },
    summary: [d.summary && `"${d.summary}"`, 'made in Fleet, not named by any experiment'].filter(Boolean).join(' · '), actions: [{ label: 'Publish', primary: true, act: 'publish' }] };
  return { need: false, onWay: false, chip: { text: 'Edited in Fleet', tone: 'neutral' }, summary: said, actions: [{ label: 'Publish', primary: true, act: 'publish' }] };
};

/** The rows that need a person: conflicts and failed pull requests (the Library catalogue's Changes counter). */
export const changesNeedingYou = (c: SystemChangesReply) =>
  [...c.repositoryChanged, ...c.ahead, ...c.storeOnly].filter(d => !d.experiment && rowView(d).need).length;

export function Changes(props: { onClose?: () => void } = {}) {
  const [, go] = useLocation();
  // Host observations touch system_changes every few seconds; reread it at most every 30 s (FLT-38).
  const changesOp = useOp('system_changes', {}, { quiet: 30_000 });
  const [busy, setBusy] = createSignal<string | null>(null);
  const [notice, setNotice] = createSignal<{ severity: 'warning' | 'failure'; title: string; facts: string } | null>(null);

  // One row's action; the change stream rereads the view.
  const act = async (d: SystemChangeDefinition, a: RowView['actions'][number]['act']) => {
    const key = { kind: d.kind, id: d.id };
    if (a === 'compare') return go(defRoute(d));
    if (a === 'task') return d.publish && go(taskRoute(d.publish.taskId));
    setBusy(`${d.kind}/${d.id}`);
    setNotice(null);
    try {
      const items = a === 'take'
        ? (await op('definition_take_repository', { keys: [key], preview: false, message: 'Take repository version from Changes' })).items
        : (await op('definition_publish', { keys: [key], preview: false, keep: a === 'keep' })).items;
      const bad = items.filter(i => i.status !== 'applied');
      if (bad.length) setNotice({ severity: 'warning', title: `${d.name} was not changed`, facts: bad.map(i => i.reason ?? i.status).join(' · ') });
      await changesOp.reload();
    } catch (e) {
      setNotice({ severity: 'failure', title: `${d.name} could not be changed`, facts: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(null);
    }
  };

  const definitions = createMemo(() => {
    const c = changesOp.data;
    if (!c) return [];
    const rows = [...c.repositoryChanged, ...c.ahead, ...c.storeOnly].filter(d => !d.experiment).map(d => ({ d, v: rowView(d) }));
    // On their way first, then what needs a person, then what is made in Fleet; newest first in each.
    const rank = (v: RowView) => v.onWay ? 0 : v.need ? 1 : 2;
    return rows.toSorted((a, b) => rank(a.v) - rank(b.v) || (ms(b.d.at) ?? 0) - (ms(a.d.at) ?? 0));
  });
  const variantsOf = (exp: string) => changesOp.data?.storeOnly.filter(d => d.experiment === exp) ?? [];

  const behindHosts = createMemo(() => changesOp.data?.versions.hosts.filter(h => h.behind) ?? []);
  const behindHarnesses = createMemo(() => changesOp.data?.harnessDrift.filter(h => h.behind) ?? []);
  const failedProposals = createMemo(() => changesOp.data?.components.filter(k => k.proposal?.stage === 'failed').length ?? 0);
  const offered = () => changesOp.data ? changesOp.data.versions.coordinator.offered ?? changesOp.data.versions.coordinator.version : '';
  const installing = createMemo(() => (changesOp.data?.versions.hosts ?? []).filter(h => updateOf(h.update)?.state === 'pending' && updateOf(h.update)?.version === offered()));
  const installingBehind = createMemo(() => behindHosts().filter(h => installing().includes(h)).length);
  const reverting = createMemo(() => changesOp.data?.components.filter(k => k.proposal?.stage === 'reverting').length ?? 0);
  const envDrift = createMemo(() => changesOp.data ? agentEnvironmentDrift(changesOp.data) : 0);
  // The summary line: the definitions first (on their way, need you, Lab experiments), then the hosts, each in its tone.
  const driftParts = createMemo<{ text: string; tone: string }[]>(() => {
    const data = changesOp.data;
    if (!data) return [];
    const onWay = definitions().filter(r => r.v.onWay).length, need = definitions().filter(r => r.v.need).length;
    const hosts = [
      installing().length > 0 && { text: `${plural(installing().length, 'host', 'hosts')} installing`, tone: 't-running' },
      behindHosts().length > installingBehind() && { text: `${plural(behindHosts().length - installingBehind(), 'host', 'hosts')} behind`, tone: 't-attention' },
      failedProposals() > 0 && { text: `${failedProposals()} qualification failed`, tone: 't-danger' },
      reverting() > 0 && { text: `${plural(reverting(), 'revert', 'reverts')} in flight`, tone: 't-attention' },
      behindHarnesses().length > 0 && { text: `${behindHarnesses().length} harness pins drifting`, tone: 't-attention' },
    ].filter((x): x is { text: string; tone: string } => !!x);
    return [
      onWay > 0 && { text: `${plural(onWay, 'definition', 'definitions')} on ${onWay === 1 ? 'its' : 'their'} way to the repository`, tone: 'muted' },
      need > 0 && { text: `${need} need${need === 1 ? 's' : ''} you`, tone: 't-attention' },
      data.experiments.length > 0 && { text: plural(data.experiments.length, 'Lab experiment', 'Lab experiments'), tone: 'muted' },
      ...(hosts.length ? hosts : [{ text: 'hosts aligned', tone: 'muted' }]),
      envDrift() > 0 && { text: `${plural(envDrift(), 'agent environment drift', 'agent environment drifts')}`, tone: 't-attention' },
    ].filter((x): x is { text: string; tone: string } => !!x);
  });

  return (
    <article class="detail stack" data-ui="detail" tabindex={-1} aria-label="Changes">
      <ShellStatus><OfflineMarker state={changesOp.state} now={Date.now()} /></ShellStatus>
      <header class="detail-h">
        <Show when={props.onClose}>{onClose => <Button label="Close" icon="x" iconOnly onClick={onClose()} />}</Show>
        <span class="pins-summary">{changesOp.data
          ? <For each={driftParts()}>{(p, i) => <><Show when={i() > 0}><span class="muted"> · </span></Show><span class={['pins-tone', p.tone]}>{p.text}</span></>}</For>
          : <span class="muted">Checking repository and host drift…</span>}</span>
        <span class="grow" />
        <Button label="Refresh" icon="rotate-ccw" iconOnly kind="quiet" onClick={changesOp.reload} />
      </header>

      <Show when={changesOp.error} fallback={
        <Show when={changesOp.data} fallback={<Empty text="Checking repository and host drift…" />}>{data => <>
          <Show when={notice()}>{n => (
            <ProblemRow severity={n().severity} title={n().title} facts={n().facts} actions={[{ label: 'Dismiss', onClick: () => setNotice(null) }]} />
          )}</Show>

          <section class="panel" data-ui="agent-environment" aria-label="Agent environment">
            <header class="panel-h">
              <h2>Agent environment</h2>
              <span class={envDrift() ? 't-attention' : 'muted'}>{envDrift() ? plural(envDrift(), 'drift', 'drifts') : 'what every future agent gets, in sync'}</span>
            </header>
            <AgentEnvironment data={data()} onOpen={go} />
          </section>

          <Show when={definitions().length}>
            <DetailSection title="Definitions" count={definitions().length} action={<span class="muted">a save in Fleet opens its pull request; unedited ones follow each release</span>}>
              <ul class="change-rows" aria-label="Definitions">
                <For each={definitions()}>{r => (
                  <ChangeRow name={r.d.name} kind={r.d.kind} summary={r.v.summary} chip={r.v.chip} tone={r.v.tone} at={ms(r.d.at)}
                    href={defRoute(r.d)} onOpen={() => go(defRoute(r.d))}
                    actions={r.v.actions.map(a => ({ label: a.label, primary: a.primary, busy: busy() === `${r.d.kind}/${r.d.id}`, onClick: () => void act(r.d, a.act) }))} />
                )}</For>
              </ul>
            </DetailSection>
          </Show>

          <Show when={data().experiments.length}>
            <DetailSection title="Lab variants" count={data().experiments.length} action={<span class="muted">they leave when their experiment is decided</span>}>
              <ul class="change-rows" aria-label="Lab variants">
                <For each={data().experiments}>{x => {
                  const ids = () => variantsOf(x.id).map(d => d.id);
                  return <ChangeRow name={x.title} chip={{ text: x.state[0].toUpperCase() + x.state.slice(1), tone: x.state === 'running' ? 'running' : 'neutral' }}
                    summary={`${plural(ids().length, 'variant', 'variants')}: ${ids().join(', ')}`} at={ms(x.at)}
                    href={`/lab/${encodeURIComponent(x.id)}`} onOpen={() => go(`/lab/${encodeURIComponent(x.id)}`)} />;
                }}</For>
              </ul>
            </DetailSection>
          </Show>

          <ReleaseRow versions={data().versions} onHost={go} />

          <For each={data().components}>{k => <ComponentGroup group={k} onHost={go} />}</For>
        </>}</Show>
      }>{err => <ErrorBanner text={`Changes could not be read: ${err()}`} onRetry={changesOp.reload} />}</Show>
    </article>
  );
}

// Release-managed components (docs/designs/fleet-host-updates.md, frame 1A of board/screens/host-updates.bl): one group per
// kind, its open pin proposal above, a row per component with the pin, the proposal and a cell per host.
const hostPath = (id: string) => `/hosts/${encodeURIComponent(id)}`;
const KIND_TITLE: Record<ComponentKind, string> = { cli: 'Agent CLIs', tool: 'Host tools' };
const STAGE: Record<PinsStage, { life: Life; text: string; tone: string }> = {
  offline: { life: 'working', text: 'offline checks', tone: 't-running' },
  hosts: { life: 'working', text: 'on hosts', tone: 't-running' },
  merging: { life: 'working', text: 'merging', tone: 't-running' },
  blocked: { life: 'waiting', text: 'blocked', tone: 't-attention' },
  landed: { life: 'done', text: 'landed', tone: 't-success' },
  reverting: { life: 'waiting', text: 'reverting', tone: 't-attention' },
  failed: { life: 'failed', text: 'failed', tone: 't-danger' },
};

/** "codex 0.170.0, claude 2.1.331": what the proposal changes. */
const proposalTitle = (g: SystemChangeKind) =>
  g.components.filter(c => c.proposed).map(c => `${c.name} ${c.proposed}`).join(', ') || 'pin proposal';

function ProposalRow(p: { group: SystemChangeKind; proposal: SystemPinsProposal }) {
  const stage = () => STAGE[p.proposal.stage];
  const trials = () => {
    const t = p.proposal.trials;
    if (!t || t.total === 0) return '';
    return t.failed > 0 ? `${t.passed} of ${t.total} trials · ${t.failed} failed` : `${t.passed} of ${t.total} trials passed`;
  };
  return <LiveRow kind="event" state={stage().life} at={timeOf(p.proposal.at, Date.now())}
    text={`#${p.proposal.number} ${proposalTitle(p.group)}`}
    facts={<span class={['lr-fact', 'pins-tone', stage().tone]} data-tip={p.proposal.failures.join('; ') || undefined}>{[stage().text, p.proposal.blocked, trials()].filter(Boolean).join(' · ')}</span>}
    onOpen={() => window.open(p.proposal.url, '_blank', 'noopener')} />;
}

const CELL: Record<SystemChangeComponentHost['state'], { icon?: IconName; tone: string }> = {
  pinned: { icon: 'check', tone: 't-success' },
  behind: { tone: 't-attention' },
  installing: { icon: 'circle-dashed', tone: 't-running' },
  refused: { icon: 'circle-slash', tone: 't-danger' },
  failing: { icon: 'triangle-alert', tone: 't-danger' },
  own: { tone: 'muted' },
  unknown: { tone: 'muted' },
};

/** One host's cell: on the pin, behind <version>, installing <pin>, refused <reason>, N of M runs failed, own. */
function HostCell(p: { cell: SystemChangeComponentHost; href: string; onOpen: () => void }) {
  const text = () => {
    const c = p.cell;
    switch (c.state) {
      case 'pinned': return '';
      case 'behind': return `behind ${c.observed ?? ''}`;
      case 'installing': return `installing ${p.cell.pinned ?? ''}`;
      case 'refused': return `refused ${(c.reason ?? '').split(':')[0]}`;
      case 'failing': return `${c.failed} of ${c.total} runs failed`;
      case 'own': return c.observed ? `own ${c.observed}` : 'own';
      case 'unknown': return '—';
    }
  };
  const label = () => text() || `on ${p.cell.pinned ?? 'the pin'}`;
  return <a class={['pins-cell', CELL[p.cell.state].tone]} href={p.href} aria-label={`${p.cell.hostId}: ${label()}, open its updates`}
    data-tip={p.cell.reason} onClick={e => { if (!e.metaKey && !e.ctrlKey) { e.preventDefault(); p.onOpen(); } }}>
    <Show when={CELL[p.cell.state].icon}>{i => <Icon name={i()} />}</Show>
    <span>{text()}</span>
  </a>;
}

function ProposalCell(p: { c: SystemChangeComponent; proposal?: SystemPinsProposal }) {
  const failed = () => p.c.failures.length > 0;
  const tone = (stage: PinsStage) => failed() ? 't-danger' : stage === 'reverting' || stage === 'blocked' ? 't-attention' : stage === 'landed' || stage === 'merging' ? 't-success' : 'muted';
  return <Show when={p.c.proposed} fallback={<span class="muted">—</span>}>{v => <>
    <span class="mono">{v()}</span>
    <Show when={p.proposal}>{pr => (
      <span class={['pins-sub', tone(pr().stage)]} data-tip={p.c.failures.join('; ') || undefined}>
        {failed() ? p.c.failures[0] : STAGE[pr().stage].text}
      </span>
    )}</Show>
  </>}</Show>;
}

function ComponentGroup(p: { group: SystemChangeKind; onHost: (path: string) => void }) {
  const hosts = createMemo(() => p.group.components[0]?.hosts.map(h => h.hostId) ?? []);
  return <section class={['panel', { 'pins-off': !p.group.enabled }]} data-ui="component-group" aria-label={KIND_TITLE[p.group.kind]}>
    <header class="panel-h">
      <h2>{KIND_TITLE[p.group.kind]} <span class="dsec-n">{p.group.components.length}</span></h2>
      <Show when={!p.group.enabled}><span class="muted">pins off · hosts keep their own</span></Show>
    </header>
    <Show when={p.group.proposal}>{pr => <ul class="live-list"><ProposalRow group={p.group} proposal={pr()} /></ul>}</Show>
    <DataTable label={`${KIND_TITLE[p.group.kind]} pins and hosts`}
      head={<tr><th scope="col">{p.group.kind === 'cli' ? 'CLI' : 'Tool'}</th><th scope="col">Pinned</th><th scope="col">Proposal</th>
        <For each={hosts()}>{h => <th scope="col">{h}</th>}</For></tr>}>
      <For each={p.group.components}>{c => (
        <tr>
          <th scope="row">{c.name}</th>
          <td class="mono">{c.pinned ?? '—'}</td>
          <td><ProposalCell c={c} proposal={p.group.proposal} /></td>
          <For each={c.hosts}>{cell => <td><HostCell cell={cell} href={hostPath(cell.hostId)} onOpen={() => p.onHost(hostPath(cell.hostId))} /></td>}</For>
        </tr>
      )}</For>
    </DataTable>
  </section>;
}

/** A host's reported update result (HostVersion.update is open JSON). */
const updateOf = (u: unknown): UpdateResult | undefined =>
  u && typeof u === 'object' && typeof (u as UpdateResult).state === 'string' ? u as UpdateResult : undefined;

const names = (ids: string[]) => ids.length <= 2 ? ids.join(' and ') : `${ids.slice(0, -1).join(', ')} and ${ids.at(-1)}`;

/** The Release row of frame 1A: the release hosts take, and where it stands on them; it opens the first host it waits on. */
function ReleaseRow(p: { versions: SystemVersionsReply; onHost: (path: string) => void }) {
  const release = () => p.versions.coordinator.offered ?? p.versions.coordinator.version;
  const pending = () => p.versions.hosts.filter(h => updateOf(h.update)?.state === 'pending' && updateOf(h.update)?.version === release());
  const failing = () => p.versions.hosts.filter(h => ['refused', 'failed', 'error'].includes(updateOf(h.update)?.state ?? '') && updateOf(h.update)?.version === release());
  const why = (h: { update: unknown }) => updateOf(h.update)?.error ? ` · ${updateOf(h.update)!.error}` : '';
  const behind = () => p.versions.hosts.filter(h => h.behind && !pending().includes(h));
  const row = () => {
    const f = failing(), w = pending(), b = behind();
    if (f.length) return { life: 'failed' as Life, tone: 't-danger', text: `${names(f.map(h => h.id))} ${new Set(f.map(h => updateOf(h.update)!.state)).size > 1 ? 'failed' : ({ refused: 'refused', failed: 'rolled back', error: 'update error' } as Record<string, string>)[updateOf(f[0].update)!.state]}${why(f[0])}`, host: f[0], at: updateOf(f[0].update)?.at };
    if (w.length) return { life: 'working' as Life, tone: 't-running', text: `${names(w.map(h => h.id))} installing${why(w[0])}`, host: w[0], at: updateOf(w[0].update)?.at };
    if (b.length) return { life: 'waiting' as Life, tone: 't-attention', text: `${names(b.map(h => h.id))} behind`, host: b[0], at: undefined };
    return { life: 'done' as Life, tone: 'muted', text: 'every host runs it', host: undefined, at: undefined };
  };
  return <ul class="live-list" aria-label="Release">
    <LiveRow kind="event" state={row().life} at={timeOf(row().at ?? p.versions.coordinator.started, Date.now())}
      text={`Release ${release()}`}
      facts={<span class={['lr-fact', 'pins-tone', row().tone]}>{row().text}</span>}
      onOpen={row().host ? () => p.onHost(hostPath(row().host!.id)) : undefined} />
  </ul>;
}
