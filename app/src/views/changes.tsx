// Library › Changes (FLT-7, docs/design-system/pages/changes.md): drift across repository, store, releases, and hosts.
import { createMemo, createSignal, For, Show } from 'solid-js';
import { useLocation } from '../router.ts';
import {
  Button, CopyButton, DataTable, DetailSection, DiffText, Empty, EntityCode, ErrorBanner, KvRow, Num, OfflineMarker,
  ProblemRow, ShellStatus, StateBadge, type StateOf,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useOp } from '../store.ts';
import type { DefinitionKey, DefinitionState, SystemChangeDefinition } from '../../../contracts/generated/ops.ts';

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
  const driftParts = createMemo(() => {
    const data = changesOp.data;
    if (!data) return [];
    return [
      data.repositoryChanged.length > 0 && `${data.repositoryChanged.length} repo changed`,
      data.storeOnly.length > 0 && `${data.storeOnly.length} store-only`,
      data.ahead.length > 0 && `${data.ahead.length} ahead`,
      behindHosts().length > 0 && `${behindHosts().length} hosts behind`,
      behindHarnesses().length > 0 && `${behindHarnesses().length} harness pins drifting`,
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

          <section class="panel">
            <header class="panel-h"><h2>Harness CLI pins vs hosts</h2></header>
            <Show when={data().harnessDrift.length} fallback={<Empty text="No harness CLI pins reported." />}>
              <DataTable
                label="Harness CLI pins"
                head={<tr><th scope="col">Host</th><th scope="col">Harness</th><th scope="col">Pinned</th><th scope="col">Observed</th><th scope="col">Status</th></tr>}>
                <For each={data().harnessDrift}>{r => (
                  <tr>
                    <th scope="row"><EntityCode code={r.hostId} id={r.hostId} kind="host" /></th>
                    <td>{r.harness}</td>
                    <td class="mono">{r.expected}</td>
                    <td class="mono">{r.observed ?? '—'}</td>
                    <td><StateBadge family="setting" state={r.behind ? 'apply_error' : 'applied'} reason={r.behind ? 'Drift' : 'Aligned'} /></td>
                  </tr>
                )}</For>
              </DataTable>
            </Show>
          </section>

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
