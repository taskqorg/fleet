// System Changes screen (FLT-7, docs/designs/fleet-system-management.md): drift across repository, store, releases, and hosts.
import { useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  Button, DataTable, DetailSection, DiffText, Empty, ErrorBanner, KvRow, OfflineMarker, ProblemRow, ShellStatus, StateBadge,
} from '../../../packages/ui/src/index.ts';
import { op } from '../api.ts';
import { useOp } from '../store.ts';
import type { DefinitionKey, SystemChangeDefinition } from '../../../contracts/generated/ops.ts';

const defRoute = (d: SystemChangeDefinition) =>
  d.kind === 'agent' ? `/agents/${d.id}` : d.kind === 'pipeline' ? `/pipelines/${d.id}` : '/catalogue';

export function Changes() {
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

  if (error) return <article class="detail"><ErrorBanner text={error} onRetry={reload} /></article>;
  if (!data) return <article class="detail"><Empty text="Checking repository and host drift…" /></article>;

  const takeableKeys: DefinitionKey[] = data.repositoryChanged
    .filter(d => d.state === 'repository-changed')
    .map(d => ({ kind: d.kind, id: d.id }));
  const exportableKeys: DefinitionKey[] = [...data.ahead, ...data.storeOnly].map(d => ({ kind: d.kind, id: d.id }));
  const behindHosts = data.versions.hosts.filter(h => h.behind);
  const behindHarnesses = data.harnessDrift.filter(h => h.behind);

  return (
    <article class="detail stack" aria-label="Changes">
      <ShellStatus><OfflineMarker state={state} now={Date.now()} /></ShellStatus>
      <header class="page-h">
        <span class="muted">
          {data.repositoryChanged.length} repo changed · {data.storeOnly.length} store-only · {data.ahead.length} ahead · {behindHosts.length} hosts behind · {behindHarnesses.length} harness pins drifting
        </span>
        <span class="grow" />
        <Button label="Refresh" icon="rotate-ccw" kind="quiet" onClick={reload} />
      </header>

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
            {data.repositoryChanged.map(d => (
              <tr key={`${d.kind}/${d.id}`}>
                <td>{d.kind}</td>
                <th scope="row"><a href={defRoute(d)} onClick={e => { e.preventDefault(); go(defRoute(d)); }}>{d.id}</a></th>
                <td><span class="mono">{d.state}</span></td>
                <td class="num">r{d.head}</td>
                <td class="num">{d.runs30d}</td>
                <td>
                  <span class="acts">
                    <Button label="Open" icon="external-link" kind="quiet" onClick={() => go(defRoute(d))} />
                    {d.state === 'repository-changed' && (
                      <Button label="Take repo" icon="download" kind="quiet" busy={busy} onClick={() => void runTakeRepo([{ kind: d.kind, id: d.id }], true)} />
                    )}
                  </span>
                </td>
              </tr>
            ))}
          </DataTable>
        ) : <Empty text="All shipped definitions match the active store." />}
      </section>

      <DetailSection title="Release versions">
        <KvRow icon="panels-top-left" label="Coordinator" meta={data.versions.coordinator.commit?.slice(0, 7) ?? 'running'}
          value={<span class="mono">{data.versions.coordinator.version}{data.versions.coordinator.offered ? ` (offered ${data.versions.coordinator.offered})` : ''}</span>} />
        <KvRow icon="panels-top-left" label="Web UI" meta={`backend ${data.versions.ui.backendVersion}`}
          value={<span class="mono">{data.versions.ui.version} ({data.versions.ui.kind})</span>} />
        {data.versions.gateway && (
          <KvRow icon="network" label="Gateway" meta={data.versions.gateway.state ?? 'running'}
            value={<span class="mono">{data.versions.gateway.version ?? '—'}</span>} />
        )}
        {data.versions.hosts.map(h => (
          <KvRow key={h.id} icon="panels-top-left" label={`Host ${h.id}`}
            meta={h.pollError ? h.pollError.message : (h.behind ? 'behind offered release' : 'aligned')}
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
                <th scope="row">{r.hostId}</th>
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
            <Button label={exportKeys ? 'Refresh export patch' : `Export patch (${exportableKeys.length})`} icon="download"
              onClick={() => setExportKeys(exportKeys ? null : exportableKeys)} />
          )}
        </header>
        {exportableKeys.length ? (
          <DataTable
            label="Ahead and store-only definitions"
            head={<tr><th scope="col">Kind</th><th scope="col">ID</th><th scope="col">State</th><th scope="col" class="num">Store rev</th><th scope="col" class="num">30d runs</th><th scope="col">Path</th></tr>}>
            {[...data.storeOnly, ...data.ahead].map(d => (
              <tr key={`${d.kind}/${d.id}`}>
                <td>{d.kind}</td>
                <th scope="row"><a href={defRoute(d)} onClick={e => { e.preventDefault(); go(defRoute(d)); }}>{d.id}</a></th>
                <td><span class="mono">{d.state}</span></td>
                <td class="num">r{d.head}</td>
                <td class="num">{d.runs30d}</td>
                <td class="mono">{d.path}</td>
              </tr>
            ))}
          </DataTable>
        ) : <Empty text="No store-only or ahead definitions." />}
        {exportRead.data && <DetailSection title={`Git patch (${exportRead.data.files.length} files)`}><DiffText label="Exported git patch" text={exportRead.data.patch || 'Empty patch'} /></DetailSection>}
        {exportRead.error && <ErrorBanner text={exportRead.error} onRetry={exportRead.reload} />}
      </section>
    </article>
  );
}
