// Library › Changes (FLT-7, docs/design-system/pages/changes.md): drift across repository, store, releases, and hosts.
import { createMemo, createSignal, For, Show } from 'solid-js';
import { useLocation } from '../router.ts';
import {
  Button, CopyButton, DataTable, DetailSection, DiffText, Empty, EntityCode, ErrorBanner, KvRow, Num, OfflineMarker,
  Icon, LiveRow, ProblemRow, ShellStatus, StateBadge, timeOf, type IconName, type Life, type StateOf,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useOp } from '../store.ts';
import type {
  ComponentKind, DefinitionKey, DefinitionState, PinsStage, SystemChangeComponent, SystemChangeComponentHost, SystemChangeDefinition,
  SystemChangeKind, SystemPinsProposal,
} from '../../../contracts/generated/ops.ts';

const defRoute = (d: SystemChangeDefinition) =>
  `/library/${d.kind === 'agent' ? 'agents' : d.kind === 'pipeline' ? 'pipelines' : 'methods'}/${encodeURIComponent(d.id)}`;

const changeState = (s: DefinitionState): { state: StateOf<'source'>; reason?: string } => {
  switch (s) {
    case 'synced': return { state: 'current' };
    case 'repository-changed': return { state: 'changed', reason: 'Repository' };
    case 'ahead': return { state: 'changed', reason: 'Ahead' };
    case 'store-only': return { state: 'unavailable', reason: 'Store only' };
    case 'repository-deleted': return { state: 'missing', reason: 'Repo deleted' };
  }
};

export function Changes(props: { onClose?: () => void } = {}) {
  const [, go] = useLocation();
  // Host observations touch system_changes every few seconds; reread it at most every 30 s (FLT-38).
  const changesOp = useOp('system_changes', {}, { quiet: 30_000 });
  const [previewTake, setPreviewTake] = createSignal<DefinitionKey[] | null>(null);
  const [busy, setBusy] = createSignal(false);
  const [notice, setNotice] = createSignal<{ severity: 'warning' | 'failure'; title: string; facts: string } | null>(null);
  const [exportKeys, setExportKeys] = createSignal<DefinitionKey[] | null>(null);

  const exportRead = useOp('definition_export', () => {
    const keys = exportKeys();
    return keys ? { keys } : null;
  });

  const runTakeRepo = async (keys: DefinitionKey[], preview: boolean) => {
    setBusy(true);
    setNotice(null);
    try {
      const r = await op('definition_take_repository', { keys, preview, message: 'Take repository version from Changes view' });
      if (preview) {
        setPreviewTake(keys);
        const readyCount = r.items.filter(i => i.status === 'would-apply').length;
        setNotice({ severity: 'warning', title: `Preview: take repository version for ${readyCount} of ${keys.length} definitions`, facts: r.items.map(i => `${i.kind}/${i.id}: ${i.status}${i.reason ? ` (${i.reason})` : ''}`).join(' · ') });
      } else {
        setPreviewTake(null);
        setNotice({ severity: 'warning', title: `Applied ${r.applied} repository update${r.applied === 1 ? '' : 's'}`, facts: r.items.map(i => `${i.kind}/${i.id}: ${i.status}${i.revision ? ` r${i.revision}` : ''}`).join(' · ') });
        await changesOp.reload();
      }
    } catch (e) {
      setNotice({ severity: 'failure', title: 'Repository sync failed', facts: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  const takeableKeys = createMemo<DefinitionKey[]>(() =>
    (changesOp.data?.repositoryChanged ?? []).filter(d => d.state === 'repository-changed').map(d => ({ kind: d.kind, id: d.id })),
  );
  const exportableKeys = createMemo<DefinitionKey[]>(() =>
    changesOp.data ? [...changesOp.data.ahead, ...changesOp.data.storeOnly].map(d => ({ kind: d.kind, id: d.id })) : [],
  );
  const behindHosts = createMemo(() => changesOp.data?.versions.hosts.filter(h => h.behind) ?? []);
  const behindHarnesses = createMemo(() => changesOp.data?.harnessDrift.filter(h => h.behind) ?? []);
  const failedProposals = createMemo(() => changesOp.data?.components.filter(k => k.proposal?.stage === 'failed').length ?? 0);
  const driftParts = createMemo(() => {
    const data = changesOp.data;
    if (!data) return [];
    return [
      data.repositoryChanged.length > 0 && `${data.repositoryChanged.length} repo changed`,
      data.storeOnly.length > 0 && `${data.storeOnly.length} store-only`,
      data.ahead.length > 0 && `${data.ahead.length} ahead`,
      behindHosts().length > 0 && `${behindHosts().length} hosts behind`,
      behindHarnesses().length > 0 && `${behindHarnesses().length} harness pins drifting`,
      failedProposals() > 0 && `${failedProposals()} qualification failed`,
    ].filter((x): x is string => !!x);
  });

  return (
    <article class="detail stack" data-ui="detail" tabindex={-1} aria-label="Changes">
      <ShellStatus><OfflineMarker state={changesOp.state} now={Date.now()} /></ShellStatus>
      <header class="detail-h">
        <Show when={props.onClose}>{onClose => <Button label="Close" icon="x" iconOnly onClick={onClose()} />}</Show>
        <span class="muted">{changesOp.data ? (driftParts().length ? driftParts().join(' · ') : 'All aligned') : 'Checking repository and host drift…'}</span>
        <span class="grow" />
        <Button label="Refresh" icon="rotate-ccw" iconOnly kind="quiet" onClick={changesOp.reload} />
      </header>

      <Show when={changesOp.error} fallback={
        <Show when={changesOp.data} fallback={<Empty text="Checking repository and host drift…" />}>{data => <>
          <Show when={notice()}>{n => (
            <ProblemRow
              severity={n().severity}
              title={n().title}
              facts={n().facts}
              actions={previewTake()
                ? [
                    { label: `Apply ${previewTake()!.length}`, primary: true, onClick: () => void runTakeRepo(previewTake()!, false) },
                    { label: 'Dismiss', onClick: () => { setPreviewTake(null); setNotice(null); } },
                  ]
                : [{ label: 'Dismiss', onClick: () => setNotice(null) }]}
            />
          )}</Show>

          <section class="panel">
            <header class="panel-h">
              <h2>Shipped definitions changed in repository</h2>
              <span class="grow" />
              <Show when={takeableKeys().length > 0}>
                <Button label={`Preview take all (${takeableKeys().length})`} icon="download" busy={busy()} onClick={() => void runTakeRepo(takeableKeys(), true)} />
              </Show>
            </header>
            <Show when={data().repositoryChanged.length} fallback={<Empty text="All shipped definitions match the active store." />}>
              <DataTable
                label="Repository changed definitions"
                head={<tr><th scope="col">Kind</th><th scope="col">ID</th><th scope="col">State</th><th scope="col" class="num">Store rev</th><th scope="col" class="num">30d runs</th><th scope="col">Action</th></tr>}>
                <For each={data().repositoryChanged}>{d => {
                  const badge = changeState(d.state);
                  return (
                    <tr>
                      <td>{d.kind}</td>
                      <th scope="row">
                        {d.kind === 'agent' || d.kind === 'pipeline'
                          ? <EntityCode code={d.id} id={d.id} kind={d.kind} />
                          : <a class="row-link" href={defRoute(d)} onClick={e => { e.preventDefault(); go(defRoute(d)); }}>{d.id}</a>}
                      </th>
                      <td><StateBadge family="source" state={badge.state} reason={badge.reason} /></td>
                      <td class="num mono">r{d.head}</td>
                      <td class="num"><Num m={{ value: d.runs30d, basis: 'measured' }} kind="count" /></td>
                      <td>
                        <span class="acts">
                          <Button label="Open" icon="external-link" kind="quiet" onClick={() => go(defRoute(d))} />
                          {d.state === 'repository-changed' && (
                            <Button label="Take repo" icon="download" kind="quiet" busy={busy()} onClick={() => void runTakeRepo([{ kind: d.kind, id: d.id }], true)} />
                          )}
                        </span>
                      </td>
                    </tr>
                  );
                }}</For>
              </DataTable>
            </Show>
          </section>

          <DetailSection title="Release versions">
            <KvRow icon="panels-top-left" label="Coordinator" meta="running"
              value={<span class="acts">
                <span class="mono">{data().versions.coordinator.version}{data().versions.coordinator.offered ? ` (offered ${data().versions.coordinator.offered})` : ''}</span>
                <Show when={data().versions.coordinator.commit}>{commit => <CopyButton label={`Copy commit ${commit()}`} value={commit()} iconOnly />}</Show>
              </span>} />
            <KvRow icon="panels-top-left" label="Web UI" meta={`backend ${data().versions.ui.backendVersion}`}
              value={<span class="mono">{data().versions.ui.version} ({data().versions.ui.kind})</span>} />
            <Show when={data().versions.gateway}>{gw => (
              <KvRow icon="network" label="Gateway" meta={gw().state ?? 'running'}
                value={<span class="mono">{gw().version ?? '—'}</span>} />
            )}</Show>
            <For each={data().versions.hosts}>{h => (
              <KvRow icon="panels-top-left" label="Host"
                meta={<span class="acts"><EntityCode code={h.id} id={h.id} kind="host" cls="inline" /><span>{h.pollError ? h.pollError.message : (h.behind ? 'behind offered release' : 'aligned')}</span></span>}
                value={<span class="acts"><span class="mono">{h.version ?? '—'}</span>
                  <StateBadge family="host" state={h.pollError ? 'failing' : h.behind ? 'offline' : 'online'} compact /></span>} />
            )}</For>
          </DetailSection>

          <For each={data().components}>{k => <ComponentGroup group={k} onHost={go} />}</For>

          <section class="panel">
            <header class="panel-h">
              <h2>Made in Fleet, not in repository</h2>
              <span class="grow" />
              <Show when={exportableKeys().length > 0}>
                <span class="acts">
                  <Show when={exportKeys()}><Button label="Refresh export patch" icon="rotate-cw" iconOnly kind="quiet" onClick={() => { setExportKeys(exportableKeys()); exportRead.reload(); }} /></Show>
                  <Button label={exportKeys() ? 'Hide patch' : `Export patch (${exportableKeys().length})`} icon={exportKeys() ? 'x' : 'download'} kind={exportKeys() ? 'quiet' : 'default'}
                    onClick={() => setExportKeys(exportKeys() ? null : exportableKeys())} />
                </span>
              </Show>
            </header>
            <Show when={exportableKeys().length} fallback={<Empty text="No store-only or ahead definitions." />}>
              <DataTable
                label="Ahead and store-only definitions"
                head={<tr><th scope="col">Kind</th><th scope="col">ID</th><th scope="col">State</th><th scope="col" class="num">Store rev</th><th scope="col" class="num">30d runs</th><th scope="col">Path</th></tr>}>
                <For each={[...data().storeOnly, ...data().ahead]}>{d => {
                  const badge = changeState(d.state);
                  return (
                    <tr>
                      <td>{d.kind}</td>
                      <th scope="row">
                        {d.kind === 'agent' || d.kind === 'pipeline'
                          ? <EntityCode code={d.id} id={d.id} kind={d.kind} />
                          : <a class="row-link" href={defRoute(d)} onClick={e => { e.preventDefault(); go(defRoute(d)); }}>{d.id}</a>}
                      </th>
                      <td><StateBadge family="source" state={badge.state} reason={badge.reason} /></td>
                      <td class="num mono">r{d.head}</td>
                      <td class="num"><Num m={{ value: d.runs30d, basis: 'measured' }} kind="count" /></td>
                      <td class="mono">{d.path}</td>
                    </tr>
                  );
                }}</For>
              </DataTable>
            </Show>
            <Show when={exportRead.data}>{exp => <DetailSection title={`Git patch (${exp().files.length} files)`}><DiffText label="Exported git patch" text={exp().patch || 'Empty patch'} /></DetailSection>}</Show>
            <Show when={exportRead.error}>{err => <ErrorBanner text={err()} onRetry={exportRead.reload} />}</Show>
          </section>
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
    facts={<span class={['lr-fact', 'pins-tone', stage().tone]} data-tip={p.proposal.failures.join('; ') || undefined}>{[stage().text, trials()].filter(Boolean).join(' · ')}</span>}
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
      case 'own': return 'own';
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
  const tone = (stage: PinsStage) => failed() ? 't-danger' : stage === 'reverting' ? 't-attention' : stage === 'landed' || stage === 'merging' ? 't-success' : 'muted';
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
