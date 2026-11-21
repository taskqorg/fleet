// Infrastructure (docs/designs/fleet-ia-and-journeys.md, docs/design-system/pages/{hosts,runners}.md):
// unified CatalogueTree (Hosts, Accounts & Limits, Connections & API keys, Runners) in the control column;
// System summary when idle on /hosts, HostDetail on /hosts/:id, Limits on /hosts/limits, Keys on /keys/:id?, Runners on /runners.
// Staleness is decided on the client from lastSeenAt (no write happens when a host stops polling), re-evaluated every 5 s.
import { useMemo, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  AgeChip, Button, CatalogueTree, ContextMenu, DetailSection, Empty, EntityCode, ErrorBanner, Fact, HarnessCounts,
  KvRow, ListDetail, Num, OfflineMarker, ProblemRow, RowFacts, SearchField, ShellStatus, Skeleton, StateBadge,
  actions, bytes, copyText, matchSearch, useTick,
  type CatalogueTreeNode, type StateOf,
} from '../../../packages/ui/src/index.ts';
import { op, type Ops } from '../api.ts';
import { closedGroups, flipGroup } from '../lists.ts';
import { listGroups, setListGroups, usePins } from '../prefs.ts';
import { useOp } from '../store.ts';
import type { HostRow, HostReading, ProcessReading, RunReading, OperationStats } from '../../../contracts/generated/ops.ts';
import { Limits } from './limits.tsx';
import { KeyDetail, KeysIdle, connectionScope, normalizeConnections, person } from './keys.tsx';
import { Runners } from './runners.tsx';

export type InfraSection = 'hosts' | 'keys' | 'runners';

const INFRA_PINS = 'infrastructure';
const INFRA_TREE_GROUPS = 'infrastructure:tree';
const ROOT_HOSTS_ID = 'root:hosts';
const NAV_LIMITS_ID = 'nav:limits';
const ROOT_KEYS_ID = 'root:keys';
const NAV_RUNNERS_ID = 'nav:runners';

const ms = (s: string) => Date.parse(s);
/**
 * The server's clock minus this browser's, from serverNow of the reply and the moment it arrived (judge ages on the server's
 * clock). A copy restored from the browser's store arrived at an unknown time: no offset, so its hosts age from then.
 */
export function useClockOffset(read: { data: { serverNow: number } | null; restored: boolean }): number {
  const arrived = useRef<{ data: unknown; at: number }>({ data: null, at: 0 });
  const data = read.restored ? null : read.data;
  if (data && arrived.current.data !== data) arrived.current = { data, at: Date.now() };
  return data ? data.serverNow - arrived.current.at : 0;
}
const m = (value: number | null | undefined) => ({ value: value ?? null, basis: value == null ? 'unavailable' : 'measured' } as const);

const rawNodeId = (nodeId: string): string => {
  const idx = nodeId.indexOf(':');
  return idx >= 0 ? nodeId.slice(idx + 1) : nodeId;
};

export function Hosts({ section = 'hosts', id }: { section?: InfraSection; id?: string }) {
  const [, go] = useLocation();
  const [q, setQ] = useState('');
  const [menu, setMenu] = useState<{ node: CatalogueTreeNode; at: { x: number; y: number } } | null>(null);

  const pins = usePins(INFRA_PINS);
  const treeFlipped = listGroups(INFRA_TREE_GROUPS);

  useTick(5000);
  // Liveness comes from lastSeenAt, which polls move without a change row; re-read every 5 s (D11 CUJ 5) and judge age on the
  // server's clock (serverNow) so a skewed browser clock does not mark hosts offline.
  const hostsOp = useOp('hosts_list', {}, { every: 5000 });
  const connsOp = useOp('connections_list', {});
  const isLimits = section === 'hosts' && id === 'limits';
  const showSystem = section === 'hosts' && !id;
  const showHostDetail = section === 'hosts' && !!id && !isLimits;
  // Spans change without change rows: summary and findings are re-read every 30 s only while System or Host detail needs them.
  const tele = useOp('telemetry_summary', showSystem || showHostDetail ? { window: '1h' } : null, { every: 30000 });
  const found = useOp('telemetry_findings', showSystem ? {} : null, { every: 30000 });

  const clockOffsetMs = useClockOffset(hostsOp);
  const now = Date.now() + clockOffsetMs;
  const hostRows = hostsOp.data?.hosts ?? [];
  const staleAfterMs = hostsOp.data?.staleAfterMs ?? 60_000;
  const fresh = (h: HostRow) => !!h.lastSeenAt && now - h.lastSeenAt < staleAfterMs;
  const conns = useMemo(
    () => (connsOp.data ? normalizeConnections(connsOp.data.connections) : []),
    [connsOp.data],
  );

  const holders = useMemo(() => {
    const map = new Map<string, number>();
    for (const c of conns) map.set(c.holder, (map.get(c.holder) ?? 0) + 1);
    return map;
  }, [conns]);
  const mainHolder = useMemo(() => [...holders].toSorted((a, b) => b[1] - a[1])[0]?.[0], [holders]);

  const infraNodes = useMemo<CatalogueTreeNode[]>(() => {
    const hostLeaves: CatalogueTreeNode[] = hostRows.map(h => {
      const r = (tele.data?.hosts ?? []).find(x => x.hostId === h.id);
      const isFresh = !!h.lastSeenAt && now - h.lastSeenAt < staleAfterMs;
      const slots = typeof h.slots === 'number' ? h.slots : null;
      const nodeId = `host:${h.id}`;
      return {
        id: nodeId,
        label: h.id,
        sub: h.platform && !h.id.includes(h.platform.slice(0, 3)) ? h.platform : undefined,
        state: {
          family: 'host',
          state: !isFresh ? 'offline' : h.pollError ? 'failing' : 'online',
          reason: h.pollError?.reason,
        },
        right: (
          <RowFacts>
            {slots != null
              ? <Fact icon="activity" text={`${h.running}/${slots}`} tip={`${h.running} of ${slots} workers busy`} />
              : h.running > 0 ? <Fact icon="activity" text={String(h.running)} tip={`${h.running} workers busy`} /> : null}
            <HarnessCounts counts={h.runningByHarness} label={`${h.id} workers by harness`} />
            {h.queued > 0 && <Fact icon="list-ordered" tip="Queued commands"><Num m={{ value: h.queued, basis: 'measured' }} kind="count" /></Fact>}
            {r && isFresh && r.diskFree != null && <Fact icon="save" tip={`Disk free: ${bytes(r.diskFree).long}`}><Num m={m(r.diskFree)} kind="bytes" /></Fact>}
            <Fact tip="Last seen">
              {h.lastSeenAt
                ? <AgeChip t={h.lastSeenAt} alertAfter={15 * 60} tone="attention" alertLabel="not seen for a while" clockOffsetMs={clockOffsetMs} />
                : <span class="muted">—</span>}
            </Fact>
          </RowFacts>
        ),
        pinned: pins.has(nodeId),
        href: `/hosts/${encodeURIComponent(h.id)}`,
        problem: !isFresh || !!h.pollError || h.problems.length > 0,
        searchText: [h.id, h.platform, h.version, ...h.capabilities].filter(Boolean).join(' '),
      };
    });

    const keyLeaves: CatalogueTreeNode[] = conns.map(c => {
      const nodeId = `key:${c.name}`;
      const activeKeys = c.keys.filter(k => k.state === 'active').length;
      const liveKeys = c.keys.filter(k => k.state !== 'retired').length;
      const hasProblem = c.keys.some(k => k.state === 'disabled' || !!k.problem || (k.reconcile && !k.reconcile.unknown && (k.reconcile.difference ?? 0) > 0));
      return {
        id: nodeId,
        label: c.name,
        sub: [c.provider !== c.name ? c.provider : '', c.holder !== mainHolder ? person(c.holder) : ''].filter(Boolean).join(' · ') || undefined,
        state: { family: 'intake', state: c.intake as StateOf<'intake'> },
        right: (
          <RowFacts>
            <Fact tip="Projects this connection serves"><span class="muted">{connectionScope(c)}</span></Fact>
            <Fact icon="shield" tip="Active keys of all keys in use"><span>{activeKeys}/{liveKeys}</span></Fact>
          </RowFacts>
        ),
        pinned: pins.has(nodeId),
        href: `/keys/${encodeURIComponent(c.name)}`,
        problem: hasProblem,
        searchText: [c.name, c.provider, c.holder, ...c.tools, ...c.access.projects].filter(Boolean).join(' '),
      };
    });

    return [
      { id: ROOT_HOSTS_ID, label: 'Hosts', icon: 'panels-top-left', defaultCollapsed: false, href: '/hosts', children: hostLeaves },
      { id: NAV_LIMITS_ID, label: 'Accounts & Limits', icon: 'gauge', pinned: pins.has(NAV_LIMITS_ID), href: '/hosts/limits', searchText: 'accounts limits subscriptions quota' },
      { id: ROOT_KEYS_ID, label: 'Connections & API keys', icon: 'shield', sub: mainHolder ? `held by ${person(mainHolder)}` : undefined, defaultCollapsed: false, href: '/keys', children: keyLeaves },
      { id: NAV_RUNNERS_ID, label: 'Runners', icon: 'play', pinned: pins.has(NAV_RUNNERS_ID), href: '/runners', searchText: 'runners executions spend ci' },
    ];
  }, [hostRows, tele.data, conns, mainHolder, now, staleAfterMs, clockOffsetMs, pins.ids]);

  const selectedTreeId = section === 'runners'
    ? NAV_RUNNERS_ID
    : section === 'keys'
      ? (id ? `key:${id}` : ROOT_KEYS_ID)
      : isLimits
        ? NAV_LIMITS_ID
        : id
          ? `host:${id}`
          : ROOT_HOSTS_ID;

  const treeClosed = closedGroups([], treeFlipped);
  const collapsedMap = useMemo<Record<string, boolean>>(() => ({
    __pinned__: treeClosed.has('__pinned__'),
    [ROOT_HOSTS_ID]: treeClosed.has(ROOT_HOSTS_ID),
    [ROOT_KEYS_ID]: treeClosed.has(ROOT_KEYS_ID),
  }), [treeClosed]);

  const toggleTreeFolder = (folderId: string) => {
    setListGroups(INFRA_TREE_GROUPS, flipGroup(treeFlipped, folderId));
  };

  const treeQuery = useMemo(() => q.replace(/\b(?:host|agent|project):(\S+)/gi, '$1'), [q]);
  const countMatchingLeaves = (nodes: readonly CatalogueTreeNode[]): number => {
    let total = 0;
    for (const n of nodes) {
      if (Array.isArray(n.children)) {
        total += countMatchingLeaves(n.children);
      } else if (!treeQuery.trim() || matchSearch(treeQuery, [n.label, n.id, n.sub, n.searchText].filter(Boolean).join(' '), n.state ? { state: n.state.state } : {})) {
        total++;
      }
    }
    return total;
  };
  const totalCount = hostRows.length + conns.length + 2;
  const shownCount = treeQuery.trim() ? countMatchingLeaves(infraNodes) : totalCount;

  const initialLoading = !hostsOp.data && !connsOp.data && !hostsOp.error && !connsOp.error;
  const loadError = (hostsOp.error && !hostsOp.data ? hostsOp.error : null)
    ?? (connsOp.error && !connsOp.data ? connsOp.error : null);

  const listPane = (
    <>
      <ShellStatus><OfflineMarker state={hostsOp.state} now={now} /></ShellStatus>
      <header class="page-h">
        <SearchField label="Find in infrastructure" value={q} onInput={setQ} onSearch={() => {}} />
        <span class="grow" />
        <span class="muted mono" aria-label={`${shownCount} of ${totalCount} infrastructure items`}>
          {shownCount < totalCount ? `${shownCount} of ${totalCount}` : totalCount}
        </span>
      </header>
      {loadError && <ErrorBanner text={loadError} onRetry={() => { void hostsOp.reload(); void connsOp.reload(); }} />}
      <section class="panel">
        {initialLoading ? <Skeleton rows={8} /> : (
          <CatalogueTree
            fill
            nodes={infraNodes}
            label="Infrastructure"
            selectedId={selectedTreeId}
            query={treeQuery}
            collapsed={collapsedMap}
            emptyText="No matching items."
            onToggle={toggleTreeFolder}
            onSelect={node => {
              if (Array.isArray(node.children)) {
                if (selectedTreeId === node.id) {
                  toggleTreeFolder(node.id);
                } else {
                  if (collapsedMap[node.id]) toggleTreeFolder(node.id);
                  if (node.href) go(node.href);
                }
              } else if (node.href) {
                go(node.href);
              }
            }}
            onPin={node => pins.toggle(node.id)}
            onRowMenu={(node, at) => {
              if (!Array.isArray(node.children)) setMenu({ node, at });
            }}
          />
        )}
        {menu && (
          <ContextMenu
            at={menu.at}
            label={menu.node.label}
            onClose={() => setMenu(null)}
            items={[
              { label: 'Open', icon: actions.open, onClick: () => { if (menu.node.href) go(menu.node.href); } },
              { label: pins.has(menu.node.id) ? 'Unpin' : 'Pin', icon: pins.has(menu.node.id) ? actions.unpin : actions.pin, onClick: () => pins.toggle(menu.node.id) },
              { label: 'Copy id', icon: actions.copy, onClick: () => copyText(rawNodeId(menu.node.id)) },
            ]}
          />
        )}
      </section>
    </>
  );

  const selHost = section === 'hosts' && id && !isLimits ? hostRows.find(h => h.id === id) : undefined;
  const selConn = section === 'keys' && id ? conns.find(c => c.name === id) : undefined;
  const reading = (hid: string) => (tele.data?.hosts ?? []).find(r => r.hostId === hid);

  let detail: ReturnType<typeof Limits> | undefined;
  let detailKey: string | undefined;
  let idle: ReturnType<typeof SystemPanels> | undefined;

  if (section === 'runners') {
    detail = <Runners onClose={() => go('/hosts')} />;
    detailKey = 'runners';
  } else if (section === 'keys') {
    if (id) {
      detail = <KeyDetail key={id} id={id} conn={selConn} loading={!connsOp.data && !connsOp.error} error={connsOp.error} onRetry={connsOp.reload} onClose={() => go('/keys')} />;
      detailKey = `key:${id}`;
    } else {
      idle = <KeysIdle conns={conns} loading={!connsOp.data && !connsOp.error} error={connsOp.error} onRetry={connsOp.reload} onOpen={go} />;
    }
  } else if (isLimits) {
    detail = <Limits onClose={() => go('/hosts')} />;
    detailKey = 'limits';
  } else if (id) {
    detailKey = `host:${id}`;
    if (selHost) {
      detail = (
        <HostDetail
          key={selHost.id}
          host={selHost}
          live={fresh(selHost)}
          reading={reading(selHost.id)}
          processes={(tele.data?.processes ?? []).filter(p => p.hostId === selHost.id)}
          runs={(tele.data?.runs ?? []).filter(r => r.hostId === selHost.id)}
          clockOffsetMs={clockOffsetMs}
          onGone={() => go('/hosts')}
          onOpen={go}
        />
      );
    } else {
      detail = (
        <article class="detail stack" data-ui="detail" tabIndex={-1} aria-label={id}>
          <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => go('/hosts')} /><h2 class="detail-title">{id}</h2></header>
          {hostsOp.error && <ErrorBanner text={`Hosts did not load: ${hostsOp.error}`} onRetry={hostsOp.reload} />}
          {!hostsOp.data && !hostsOp.error ? <Skeleton rows={6} /> : !hostsOp.error && <Empty text={`Host "${id}" not found.`} />}
        </article>
      );
    }
  } else {
    idle = (
      <SystemPanels
        tele={tele.data}
        found={found.data}
        error={tele.error ?? found.error}
        onRetry={() => { void tele.reload(); void found.reload(); }}
        clockOffsetMs={clockOffsetMs}
      />
    );
  }

  return <ListDetail list={listPane} detail={detail} detailKey={detailKey} idle={idle} />;
}

function HostDetail({ host: sel, live, reading: r, processes, runs, clockOffsetMs, onGone, onOpen }: { host: HostRow; live: boolean; reading: HostReading | undefined; processes: ProcessReading[]; runs: RunReading[]; clockOffsetMs: number; onGone: () => void; onOpen: (path: string) => void }) {
  const [preview, setPreview] = useState<Ops['hosts_remove']['reply'] | null>(null);
  const [err, setErr] = useState('');
  const remove = (confirm: boolean) => (setErr(''), op('hosts_remove', { id: sel.id, preview: !confirm }).then(x => { if (confirm) onGone(); else setPreview(x); }, e => setErr(String(e.message ?? e))));
  return <article class="detail" data-ui="detail" tabIndex={-1} aria-label={sel.id}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={onGone} /><h2 class="detail-title">{sel.id}</h2></header>
    <DetailSection title="State">
      {/* The coordinator answers this host's polls with an error: no commands, no update target (host_poll_health.go). */}
      {live && sel.pollError && <ProblemRow title="Polls failing" severity="failure" actions={[]}
        facts={<><span class="mono">{sel.pollError.reason}</span> <span class="muted">{sel.pollError.status} since</span> <AgeChip t={sel.pollError.since} clockOffsetMs={clockOffsetMs} /> <span class="muted">{sel.pollError.message}</span></>} />}
      <KvRow icon="activity" label="Last seen" meta={sel.lastSeenAt ? undefined : 'never'} value={sel.lastSeenAt ? <AgeChip t={sel.lastSeenAt} clockOffsetMs={clockOffsetMs} /> : undefined} />
      <KvRow icon="panels-top-left" label="Platform" meta={sel.platform ?? <span data-tip="Platform unknown">—</span>} />
      {(r?.version ?? sel.version) && <KvRow icon="info" label="Daemon version" meta={<span class="mono">{r?.version ?? sel.version}</span>} />}
    </DetailSection>
    <DetailSection title="Daemon health">{r ? <>
      <KvRow icon="clock" label="Heartbeat" value={<AgeChip t={ms(r.at)} alertAfter={3 * 60} tone="danger" alertLabel="no heartbeat" clockOffsetMs={clockOffsetMs} />} />
      <KvRow icon="activity" label="Load (1 min)" value={<><Num m={m(r.load1)} kind="score" />{r.cpus != null && <span class="muted"> of {r.cpus} cores</span>}</>} />
      <KvRow icon="activity" label="Memory available" value={<><Num m={m(r.memFree)} kind="bytes" />{r.memTotal != null && <span class="muted"> of <Num m={m(r.memTotal)} kind="bytes" /></span>}</>} />
      <KvRow icon="save" label="Disk free" value={<Num m={m(r.diskFree)} kind="bytes" />} />
    </> : <span class="muted">No heartbeat recorded</span>}</DetailSection>
    {processes.length > 0 && <DetailSection title="Daemon processes (per beat)">{processes.map(p => <KvRow key={p.process} icon="activity" label={p.process}
      value={<RowFacts><Fact icon="gauge" tip="CPU per beat"><Num m={m(p.cpuSeconds)} kind="duration" /></Fact><Fact icon="save" tip="Resident memory"><Num m={m(p.rssBytes)} kind="bytes" /></Fact></RowFacts>} />)}</DetailSection>}
    <DetailSection title="Active & recent executions">
      <KvRow icon="activity" label="Running" value={<><HarnessCounts counts={sel.runningByHarness} label="by harness" /> <a class="row-link" href="/runners" onClick={e => { e.preventDefault(); onOpen('/runners'); }}>Runners</a></>} />
      <KvRow icon="list-ordered" label="Queued commands" value={<Num m={{ value: sel.queued, basis: 'measured' }} kind="count" />} />
      {runs.slice(0, 10).map(run => (
        <div key={run.executionId} class="kv-row">
          <EntityCode id={run.executionId} kind="execution" />
          {run.projectId && <EntityCode id={run.projectId} code={run.projectId} kind="project" cls="inline" />}
          <span class="grow" />
          <RowFacts>
            {run.durationMs != null && <Fact icon="clock" tip="Duration"><Num m={m(run.durationMs / 1000)} kind="duration" /></Fact>}
            <Fact icon="gauge" tip="CPU"><Num m={m(run.cpuSeconds)} kind="duration" /></Fact>
            <Fact icon="save" tip="Peak memory"><Num m={m(run.peakRssBytes)} kind="bytes" /></Fact>
            {(run.orphans ?? 0) > 0 && <span class="chip t-danger">{run.orphans} orphan</span>}
            <Fact tip="Finished"><AgeChip t={ms(run.at)} clockOffsetMs={clockOffsetMs} /></Fact>
          </RowFacts>
        </div>
      ))}
    </DetailSection>
    {live && sel.problems.length > 0 && <DetailSection title="Problems">{sel.problems.map(p => <KvRow key={p.route} icon="activity" label={`Edge answers ${p.route} with ${p.status}`} meta={p.detail || p.location || undefined} />)}</DetailSection>}
    {sel.harnessVersions?.length ? <DetailSection title="Agent CLIs">{sel.harnessVersions.map(cli =>
      <KvRow key={cli.harness} icon="info" label={cli.harness} value={<span class="mono" data-tip={live && cli.observed ? undefined : 'Observation unavailable or stale'}>{live && cli.observed || '—'}</span>}
        meta={(!live || cli.observed !== cli.expected) ? `Expected ${cli.expected}` : undefined} />)}</DetailSection> : null}
    <DetailSection title="Capabilities">{sel.capabilities.length ? sel.capabilities.map(c => <KvRow key={c} icon="check" label={c} />) : <span class="muted">none reported</span>}</DetailSection>
    <DetailSection title="Store"><KvRow icon="save" label="Store checks" value={live && sel.stores !== 'unknown' ? sel.stores : <span class="muted" data-tip={live ? sel.storesReason : 'Host observation stale'}>—</span>} meta={live ? sel.storesReason : 'Host observation stale'} /></DetailSection>
    <DetailSection title="Remove">
      {err && <ProblemRow title="Not removed" facts={err} severity="failure" actions={[]} />}
      {!preview ? <Button label="Remove host" icon="trash-2" onClick={() => remove(false)} /> : <>
        <KvRow icon="activity" label="Running work cancelled" value={<Num m={{ value: preview.runningExecutions, basis: 'measured' }} kind="count" />} />
        <KvRow icon="activity" label="Open sessions marked" value={<Num m={{ value: preview.openSessions, basis: 'measured' }} kind="count" />} />
        <KvRow icon="triangle-alert" label="Artifacts lost (no other copy)" value={<Num m={{ value: preview.unreplicatedArtifacts, basis: 'measured' }} kind="count" />} />
        <span class="acts"><Button label="Remove" icon="trash-2" kind="danger" onClick={() => remove(true)} /><Button label="Cancel" kind="quiet" onClick={() => setPreview(null)} /></span>
      </>}
    </DetailSection>
  </article>;
}

function SystemPanels({ tele, found, error, onRetry, clockOffsetMs }: { tele: Ops['telemetry_summary']['reply'] | null; found: Ops['telemetry_findings']['reply'] | null; error: string | null; onRetry: () => void; clockOffsetMs: number }) {
  const [, go] = useLocation();
  const [purge, setPurge] = useState<Ops['system_purge']['reply'] | null>(null);
  const [purgeErr, setPurgeErr] = useState('');
  const [allOps, setAllOps] = useState(false);
  const runPurge = (preview: boolean) => { setPurgeErr(''); op('system_purge', { preview, kinds: ['host'] }).then(setPurge, e => setPurgeErr(String(e.message ?? e))); };
  if (!tele) {
    return (
      <article class="detail stack" aria-label="System">
        <header class="detail-h"><h2 class="detail-title">System</h2></header>
        {error ? <ErrorBanner text={`System telemetry did not load: ${error}`} onRetry={onRetry} /> : <Skeleton rows={5} />}
      </article>
    );
  }
  if (!tele.enabled) {
    return (
      <article class="detail stack" aria-label="System">
        <header class="detail-h"><h2 class="detail-title">System</h2></header>
        <Empty text="Telemetry is off: no span store is configured." />
      </article>
    );
  }
  // Only what needs a look is listed (2026-11-20 decision): errors, refusals, or a P95 over a second; the healthy rest folds
  // into one line that opens the whole table.
  const why = (o: OperationStats) => [o.errors ? `${o.errors} ${o.errors === 1 ? 'error' : 'errors'}` : '', o.refused ? `${o.refused} refused` : '', o.p95Ms > 1000 ? 'slow' : ''].filter(Boolean).join(' · ');
  const sorted = tele.operations.toSorted((a, b) => b.errors - a.errors || (b.refused ?? 0) - (a.refused ?? 0) || b.p95Ms - a.p95Ms);
  const notable = sorted.filter(o => why(o)), ops = allOps ? sorted : notable, healthy = sorted.length - notable.length;
  return <article class="detail" aria-label="System">
    <header class="detail-h"><h2 class="detail-title">System</h2></header>
    {error && <ErrorBanner text={error} onRetry={onRetry} />}
    {tele.error && <ProblemRow title="Span store" facts={tele.error} severity="failure" actions={[]} />}
    <DetailSection title="Findings">
      {found?.stale && <ProblemRow title="Rules pass is stale" facts="Findings may be out of date." severity="warning" actions={[]} />}
      {found?.findings.length ? found.findings.map(f => <KvRow key={f.cause} icon={f.groupCause ? 'circle-alert' : 'triangle-alert'} label={f.text ?? f.cause}
        meta={f.groupCause ? `part of ${f.groupCause}` : undefined} value={<AgeChip t={ms(f.since)} clockOffsetMs={clockOffsetMs} />} />) : <span class="muted">none</span>}
    </DetailSection>
    <DetailSection title="Services, last hour">
      <div class="vl-wrap" style={{ '--cols': 'minmax(0,1fr) auto auto auto auto' }}>
        {ops.map(o => <div key={o.service + o.name} role="row" class="vl-row static">
          <span class="tt"><span class="title">{o.name}</span><span class="id">{o.service}</span></span>
          <span class={o.errors ? 'danger' : 'muted'}>{why(o)}</span>
          <span data-tip="Calls"><Num m={{ value: o.count, basis: 'measured' }} kind="count" /></span>
          <span data-tip="Errors" class={o.errors ? 'danger' : 'muted'}><Num m={{ value: o.errors, basis: 'measured' }} kind="count" /></span>
          <span data-tip="p95"><Num m={{ value: o.p95Ms / 1000, basis: 'measured' }} kind="duration" /></span>
        </div>)}
      </div>
      {healthy > 0 && <div class="res-bar"><span class="muted">{allOps ? `${sorted.length} operations` : `${healthy} more are healthy: no errors, none refused, P95 under 1 s.`}</span>
        <Button label={allOps ? 'Show only what needs a look' : `Show all ${sorted.length}`} kind="quiet" onClick={() => setAllOps(!allOps)} /></div>}
      <KvRow icon="circle-alert" label="Spans dropped" value={<Num m={{ value: tele.droppedSpans, basis: 'measured' }} kind="count" />} />
    </DetailSection>
    <DetailSection title="Active & recent executions">
      <div class="res-bar"><a class="row-link" href="/runners" onClick={e => { e.preventDefault(); go('/runners'); }}>Open Runners</a></div>
      {tele.runs.length ? tele.runs.slice(0, 20).map(r => (
        <div key={r.executionId} class="kv-row">
          <EntityCode id={r.executionId} kind="execution" />
          {r.hostId && <EntityCode id={r.hostId} code={r.hostId} kind="host" cls="inline" />}
          {r.projectId && <EntityCode id={r.projectId} code={r.projectId} kind="project" cls="inline" />}
          <span class="grow" />
          <RowFacts>
            {r.durationMs != null && <Fact icon="clock" tip="Duration"><Num m={m(r.durationMs / 1000)} kind="duration" /></Fact>}
            <Fact icon="gauge" tip="CPU"><Num m={m(r.cpuSeconds)} kind="duration" /></Fact>
            <Fact icon="save" tip="Peak memory"><Num m={m(r.peakRssBytes)} kind="bytes" /></Fact>
            {(r.orphans ?? 0) > 0 && <span class="chip t-danger">{r.orphans} orphan</span>}
            <Fact tip="Finished"><AgeChip t={ms(r.at)} clockOffsetMs={clockOffsetMs} /></Fact>
          </RowFacts>
        </div>
      )) : <span class="muted">none in the last hour</span>}
    </DetailSection>
    <DetailSection title="Purge">
      {purgeErr && <ProblemRow title="Not purged" facts={purgeErr} severity="failure" actions={[]} />}
      {!purge ? <Button label="Find removed hosts to purge" icon="trash-2" onClick={() => runPurge(true)} /> : <>
        <KvRow icon="trash-2" label="Removed hosts nothing references" value={<Num m={{ value: purge.removed[0]?.count ?? 0, basis: 'measured' }} kind="count" />} />
        <KvRow icon="save" label="Removed hosts kept by history" value={<Num m={{ value: purge.kept?.[0]?.count ?? 0, basis: 'measured' }} kind="count" />} />
        {(purge.removed[0]?.count ?? 0) > 0 && !purge.purged && <Button label="Purge" icon="trash-2" kind="danger" onClick={() => runPurge(false)} />}
      </>}
    </DetailSection>
  </article>;
}
