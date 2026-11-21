// System Changes screen (FLT-7, docs/designs/fleet-system-management.md): drift across repository, store, releases, and hosts.
import { useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  Button, CopyButton, DataTable, DetailSection, DiffText, Empty, EntityCode, ErrorBanner, KvRow, Num, OfflineMarker,
  ProblemRow, ShellStatus, StateBadge, type StateOf,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useOp } from '../store.ts';
import type { DefinitionKey, DefinitionState, SystemChangeDefinition } from '../../../contracts/generated/ops.ts';

const defRoute = (d: SystemChangeDefinition) =>
  d.kind === 'agent' ? `/agents/${encodeURIComponent(d.id)}` : d.kind === 'pipeline' ? `/pipelines/${encodeURIComponent(d.id)}` : `/catalogue/${encodeURIComponent(d.id)}`;

const changeState = (s: DefinitionState): { state: StateOf<'source'>; reason?: string } => {
  switch (s) {
    case 'synced': return { state: 'current' };
    case 'repository-changed': return { state: 'changed', reason: 'Repository' };
    case 'ahead': return { state: 'changed', reason: 'Ahead' };
    case 'store-only': return { state: 'unavailable', reason: 'Store only' };
    case 'repository-deleted': return { state: 'missing', reason: 'Repo deleted' };
  }
};

export function Changes({ onClose }: { onClose?: () => void } = {}) {
  const [, go] = useLocation();
  const { data, error, state, reload } = useOp('system_changes', {});
  const [previewTake, setPreviewTake] = useState<DefinitionKey[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<{ severity: 'warning' | 'failure'; title: string; facts: string } | null>(null);
  const [exportKeys, setExportKeys] = useState<DefinitionKey[] | null>(null);

  const exportRead = useOp('definition_export', exportKeys ? { keys: exportKeys } : null);

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
        await reload();
      }
    } catch (e) {
      setNotice({ severity: 'failure', title: 'Repository sync failed', facts: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  const takeableKeys: DefinitionKey[] = (data?.repositoryChanged ?? [])
    .filter(d => d.state === 'repository-changed')
    .map(d => ({ kind: d.kind, id: d.id }));
  const exportableKeys: DefinitionKey[] = data ? [...data.ahead, ...data.storeOnly].map(d => ({ kind: d.kind, id: d.id })) : [];
  const behindHosts = data?.versions.hosts.filter(h => h.behind) ?? [];
  const behindHarnesses = data?.harnessDrift.filter(h => h.behind) ?? [];
  const driftParts = data ? [
    data.repositoryChanged.length > 0 && `${data.repositoryChanged.length} repo changed`,
    data.storeOnly.length > 0 && `${data.storeOnly.length} store-only`,
    data.ahead.length > 0 && `${data.ahead.length} ahead`,
    behindHosts.length > 0 && `${behindHosts.length} hosts behind`,
    behindHarnesses.length > 0 && `${behindHarnesses.length} harness pins drifting`,
  ].filter((x): x is string => !!x) : [];

  return (
    <article class="detail stack" data-ui="detail" tabIndex={-1} aria-label="Changes">
      <ShellStatus><OfflineMarker state={state} now={Date.now()} /></ShellStatus>
      <header class="detail-h">
        {onClose && <Button label="Close" icon="x" iconOnly onClick={onClose} />}
        <span class="muted">{data ? (driftParts.length ? driftParts.join(' · ') : 'All aligned') : 'Checking repository and host drift…'}</span>
        <span class="grow" />
        <Button label="Refresh" icon="rotate-ccw" iconOnly kind="quiet" onClick={reload} />
      </header>

      {error ? <ErrorBanner text={error} onRetry={reload} /> : !data ? <Empty text="Checking repository and host drift…" /> : <>
        {notice && (
          <ProblemRow
            severity={notice.severity}
            title={notice.title}
            facts={notice.facts}
            actions={previewTake
              ? [
                  { label: `Apply ${previewTake.length}`, primary: true, onClick: () => void runTakeRepo(previewTake, false) },
                  { label: 'Dismiss', onClick: () => { setPreviewTake(null); setNotice(null); } },
                ]
              : [{ label: 'Dismiss', onClick: () => setNotice(null) }]}
          />
        )}

        <section class="panel">
          <header class="panel-h">
            <h2>Shipped definitions changed in repository</h2>
            <span class="grow" />
            {takeableKeys.length > 0 && (
              <Button label={`Preview take all (${takeableKeys.length})`} icon="download" busy={busy} onClick={() => void runTakeRepo(takeableKeys, true)} />
            )}
          </header>
          {data.repositoryChanged.length ? (
            <DataTable
              label="Repository changed definitions"
              head={<tr><th scope="col">Kind</th><th scope="col">ID</th><th scope="col">State</th><th scope="col" class="num">Store rev</th><th scope="col" class="num">30d runs</th><th scope="col">Action</th></tr>}>
              {data.repositoryChanged.map(d => {
                const badge = changeState(d.state);
                return (
                  <tr key={`${d.kind}/${d.id}`}>
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
                          <Button label="Take repo" icon="download" kind="quiet" busy={busy} onClick={() => void runTakeRepo([{ kind: d.kind, id: d.id }], true)} />
                        )}
                      </span>
                    </td>
                  </tr>
                );
              })}
            </DataTable>
          ) : <Empty text="All shipped definitions match the active store." />}
        </section>

        <DetailSection title="Release versions">
          <KvRow icon="panels-top-left" label="Coordinator" meta="running"
            value={<span class="acts">
              <span class="mono">{data.versions.coordinator.version}{data.versions.coordinator.offered ? ` (offered ${data.versions.coordinator.offered})` : ''}</span>
              {data.versions.coordinator.commit && <CopyButton label={`Copy commit ${data.versions.coordinator.commit}`} value={data.versions.coordinator.commit} iconOnly />}
            </span>} />
          <KvRow icon="panels-top-left" label="Web UI" meta={`backend ${data.versions.ui.backendVersion}`}
            value={<span class="mono">{data.versions.ui.version} ({data.versions.ui.kind})</span>} />
          {data.versions.gateway && (
            <KvRow icon="network" label="Gateway" meta={data.versions.gateway.state ?? 'running'}
              value={<span class="mono">{data.versions.gateway.version ?? '—'}</span>} />
          )}
          {data.versions.hosts.map(h => (
            <KvRow key={h.id} icon="panels-top-left" label="Host"
              meta={<span class="acts"><EntityCode code={h.id} id={h.id} kind="host" cls="inline" /><span>{h.pollError ? h.pollError.message : (h.behind ? 'behind offered release' : 'aligned')}</span></span>}
              value={<span class="acts"><span class="mono">{h.version ?? '—'}</span>
                <StateBadge family="host" state={h.pollError ? 'failing' : h.behind ? 'offline' : 'online'} compact /></span>} />
          ))}
        </DetailSection>

        <section class="panel">
          <header class="panel-h"><h2>Harness CLI pins vs hosts</h2></header>
          {data.harnessDrift.length ? (
            <DataTable
              label="Harness CLI pins"
              head={<tr><th scope="col">Host</th><th scope="col">Harness</th><th scope="col">Pinned</th><th scope="col">Observed</th><th scope="col">Status</th></tr>}>
              {data.harnessDrift.map(r => (
                <tr key={`${r.hostId}/${r.harness}`}>
                  <th scope="row"><EntityCode code={r.hostId} id={r.hostId} kind="host" /></th>
                  <td>{r.harness}</td>
                  <td class="mono">{r.expected}</td>
                  <td class="mono">{r.observed ?? '—'}</td>
                  <td><StateBadge family="setting" state={r.behind ? 'apply_error' : 'applied'} reason={r.behind ? 'Drift' : 'Aligned'} /></td>
                </tr>
              ))}
            </DataTable>
          ) : <Empty text="No harness CLI pins reported." />}
        </section>

        <section class="panel">
          <header class="panel-h">
            <h2>Made in Fleet, not in repository</h2>
            <span class="grow" />
            {exportableKeys.length > 0 && (
              <span class="acts">
                {exportKeys && <Button label="Refresh export patch" icon="rotate-cw" iconOnly kind="quiet" onClick={() => { setExportKeys(exportableKeys); exportRead.reload(); }} />}
                <Button label={exportKeys ? 'Hide patch' : `Export patch (${exportableKeys.length})`} icon={exportKeys ? 'x' : 'download'} kind={exportKeys ? 'quiet' : 'default'}
                  onClick={() => setExportKeys(exportKeys ? null : exportableKeys)} />
              </span>
            )}
          </header>
          {exportableKeys.length ? (
            <DataTable
              label="Ahead and store-only definitions"
              head={<tr><th scope="col">Kind</th><th scope="col">ID</th><th scope="col">State</th><th scope="col" class="num">Store rev</th><th scope="col" class="num">30d runs</th><th scope="col">Path</th></tr>}>
              {[...data.storeOnly, ...data.ahead].map(d => {
                const badge = changeState(d.state);
                return (
                  <tr key={`${d.kind}/${d.id}`}>
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
              })}
            </DataTable>
          ) : <Empty text="No store-only or ahead definitions." />}
          {exportRead.data && <DetailSection title={`Git patch (${exportRead.data.files.length} files)`}><DiffText label="Exported git patch" text={exportRead.data.patch || 'Empty patch'} /></DetailSection>}
          {exportRead.error && <ErrorBanner text={exportRead.error} onRetry={exportRead.reload} />}
        </section>
      </>}
    </article>
  );
}
