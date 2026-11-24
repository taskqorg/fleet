// Infrastructure (docs/designs/fleet-ia-and-journeys.md, docs/design-system/pages/{hosts,runners}.md):
// unified CatalogueTree (Hosts, Accounts & Limits, Connections & API keys, Runners) in the control column;
// System summary when idle on /hosts, HostDetail on /hosts/:id, Limits on /hosts/limits, Keys on /keys/:id?, Runners on /runners.
// Staleness is decided on the client from lastSeenAt (no write happens when a host stops polling), re-evaluated every 5 s.
import { createMemo, createSignal, For, Match, onSettled, Show, Switch, type Accessor } from 'solid-js';
import { useLocation } from '../router.ts';
import {
  AgeChip, Button, CatalogueTree, ContextMenu, DetailSection, Empty, EntityCode, ErrorBanner, Fact, HarnessCounts,
  KvRow, ListDetail, Num, OfflineMarker, ProblemRow, RowFacts, SearchField, ShellStatus, Skeleton, StateBadge,
  actions, bytes, copyText, matchSearch,
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
export function useClockOffset(read: { data: { serverNow: number } | null; restored: boolean }): Accessor<number> {
  let arrivedData: unknown = null;
  let arrivedAt = 0;
  return createMemo(() => {
    const data = read.restored ? null : read.data;
    if (data && arrivedData !== data) {
      arrivedData = data;
      arrivedAt = Date.now();
    }
    return data ? data.serverNow - arrivedAt : 0;
  });
}
const m = (value: number | null | undefined) => ({ value: value ?? null, basis: value == null ? 'unavailable' : 'measured' } as const);

const rawNodeId = (nodeId: string): string => {
  const idx = nodeId.indexOf(':');
  return idx >= 0 ? nodeId.slice(idx + 1) : nodeId;
};

export function Hosts(props: { section?: InfraSection; id?: string }) {
  const section = () => props.section ?? 'hosts';
  const [, go] = useLocation();
  const [q, setQ] = createSignal('');
  const [menu, setMenu] = createSignal<{ node: CatalogueTreeNode; at: { x: number; y: number } } | null>(null);
  const [tick, setTick] = createSignal(Date.now());
  onSettled(() => {
    const t = setInterval(() => setTick(Date.now()), 5000);
    return () => clearInterval(t);
  });

  const pins = usePins(INFRA_PINS);
  const treeFlipped = createMemo(() => listGroups(INFRA_TREE_GROUPS));

  // Liveness comes from lastSeenAt, which polls move without a change row; re-read every 5 s (D11 CUJ 5) and judge age on the
  // server's clock (serverNow) so a skewed browser clock does not mark hosts offline.
  const me = useOp('me', {});
  const hostsOp = useOp('hosts_list', {}, { every: 5000 });
  const connsOp = useOp('connections_list', {});
  const isLimits = createMemo(() => section() === 'hosts' && props.id === 'limits');
  const showSystem = createMemo(() => section() === 'hosts' && !props.id);
  const showHostDetail = createMemo(() => section() === 'hosts' && !!props.id && !isLimits());
  // Spans change without change rows: summary and findings are re-read every 30 s only while System or Host detail needs them.
  const tele = useOp('telemetry_summary', () => (showSystem() || showHostDetail() ? { window: '1h' } : null), { every: 30000 });
  const found = useOp('telemetry_findings', () => (showSystem() ? {} : null), { every: 30000 });

  const clockOffsetMs = useClockOffset(hostsOp);
  const now = createMemo(() => tick() + clockOffsetMs());
  const hostRows = createMemo(() => hostsOp.data?.hosts ?? []);
  const staleAfterMs = createMemo(() => hostsOp.data?.staleAfterMs ?? 60_000);
  const fresh = (h: HostRow) => !!h.lastSeenAt && now() - h.lastSeenAt < staleAfterMs();
  const conns = createMemo(() => (connsOp.data ? normalizeConnections(connsOp.data.connections) : []));

  const holders = createMemo(() => {
    const map = new Map<string, number>();
    for (const c of conns()) map.set(c.holder, (map.get(c.holder) ?? 0) + 1);
    return map;
  });
  const mainHolder = createMemo(() => [...holders()].toSorted((a, b) => b[1] - a[1])[0]?.[0]);

  const infraNodes = createMemo<CatalogueTreeNode[]>(() => {
    const curNow = now();
    const curStale = staleAfterMs();
    const curOffset = clockOffsetMs();
    const teleHosts = tele.data?.hosts ?? [];
    const holder = mainHolder();
    const hostLeaves: CatalogueTreeNode[] = hostRows().map(h => {
      const r = teleHosts.find(x => x.hostId === h.id);
      const isFresh = !!h.lastSeenAt && curNow - h.lastSeenAt < curStale;
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
                ? <AgeChip t={h.lastSeenAt} alertAfter={15 * 60} tone="attention" alertLabel="not seen for a while" clockOffsetMs={curOffset} />
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

    const keyLeaves: CatalogueTreeNode[] = conns().map(c => {
      const nodeId = `key:${c.name}`;
      const activeKeys = c.keys.filter(k => k.state === 'active').length;
      const liveKeys = c.keys.filter(k => k.state !== 'retired').length;
      const hasProblem = c.keys.some(k => k.state === 'disabled' || !!k.problem || (k.reconcile && !k.reconcile.unknown && (k.reconcile.difference ?? 0) > 0));
      return {
        id: nodeId,
        label: c.name,
        sub: [c.provider !== c.name ? c.provider : '', c.holder !== holder ? person(c.holder) : ''].filter(Boolean).join(' · ') || undefined,
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
      { id: ROOT_KEYS_ID, label: 'Connections & API keys', icon: 'shield', sub: holder ? `held by ${person(holder)}` : undefined, defaultCollapsed: false, href: '/keys', children: keyLeaves },
      { id: NAV_RUNNERS_ID, label: 'Runners', icon: 'play', pinned: pins.has(NAV_RUNNERS_ID), href: '/runners', searchText: 'runners executions spend ci' },
      ...(me.data?.owner ? [{ id: "nav:humans", label: "Humans", icon: "shield" as const, href: "/humans" }] : []),
    ];
  });

  const selectedTreeId = createMemo(() => {
    const sec = section();
    if (sec === 'runners') return NAV_RUNNERS_ID;
    if (sec === 'keys') return props.id ? `key:${props.id}` : ROOT_KEYS_ID;
    if (isLimits()) return NAV_LIMITS_ID;
    return props.id ? `host:${props.id}` : ROOT_HOSTS_ID;
  });

  const treeClosed = createMemo(() => closedGroups([], treeFlipped()));
  const collapsedMap = createMemo<Record<string, boolean>>(() => ({
    __pinned__: treeClosed().has('__pinned__'),
    [ROOT_HOSTS_ID]: treeClosed().has(ROOT_HOSTS_ID),
    [ROOT_KEYS_ID]: treeClosed().has(ROOT_KEYS_ID),
  }));

  const toggleTreeFolder = (folderId: string) => {
    setListGroups(INFRA_TREE_GROUPS, flipGroup(treeFlipped(), folderId));
  };

  const treeQuery = createMemo(() => q().replace(/\b(?:host|agent|project):(\S+)/gi, '$1'));
  const countMatchingLeaves = (nodes: readonly CatalogueTreeNode[]): number => {
    let total = 0;
    const tq = treeQuery();
    for (const n of nodes) {
      if (Array.isArray(n.children)) {
        total += countMatchingLeaves(n.children);
      } else if (!tq.trim() || matchSearch(tq, [n.label, n.id, n.sub, n.searchText].filter(Boolean).join(' '), n.state ? { state: n.state.state } : {})) {
        total++;
      }
    }
    return total;
  };
  const totalCount = createMemo(() => hostRows().length + conns().length + 2 + (me.data?.owner ? 1 : 0));
  const shownCount = createMemo(() => treeQuery().trim() ? countMatchingLeaves(infraNodes()) : totalCount());

  const initialLoading = createMemo(() => !hostsOp.data && !connsOp.data && !hostsOp.error && !connsOp.error);
  const loadError = createMemo(() =>
    (hostsOp.error && !hostsOp.data ? hostsOp.error : null)
    ?? (connsOp.error && !connsOp.data ? connsOp.error : null),
  );

  const listPane = (
    <>
      <ShellStatus><OfflineMarker state={hostsOp.state} now={now()} /></ShellStatus>
      <header class="page-h">
        <SearchField label="Find in infrastructure" value={q()} onInput={setQ} onSearch={() => {}} />
        <span class="grow" />
        <span class="muted mono" aria-label={`${shownCount()} of ${totalCount()} infrastructure items`}>
          {shownCount() < totalCount() ? `${shownCount()} of ${totalCount()}` : totalCount()}
        </span>
      </header>
      <Show when={loadError()}>{err => <ErrorBanner text={err()} onRetry={() => { void hostsOp.reload(); void connsOp.reload(); }} />}</Show>
      <section class="panel">
        <Show when={!initialLoading()} fallback={<Skeleton rows={8} />}>
          <CatalogueTree
            fill
            nodes={infraNodes()}
            label="Infrastructure"
            selectedId={selectedTreeId()}
            query={treeQuery()}
            collapsed={collapsedMap()}
            emptyText="No matching items."
            onToggle={toggleTreeFolder}
            onSelect={node => {
              if (Array.isArray(node.children)) {
                if (selectedTreeId() === node.id) {
                  toggleTreeFolder(node.id);
                } else {
                  if (collapsedMap()[node.id]) toggleTreeFolder(node.id);
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
        </Show>
        <Show when={menu()}>{m => (
          <ContextMenu
            at={m().at}
            label={m().node.label}
            onClose={() => setMenu(null)}
            items={[
              { label: 'Open', icon: actions.open, onClick: () => { const href = m().node.href; if (href) go(href); } },
              { label: pins.has(m().node.id) ? 'Unpin' : 'Pin', icon: pins.has(m().node.id) ? actions.unpin : actions.pin, onClick: () => pins.toggle(m().node.id) },
              { label: 'Copy id', icon: actions.copy, onClick: () => copyText(rawNodeId(m().node.id)) },
            ]}
          />
        )}</Show>
      </section>
    </>
  );

  const selHost = createMemo(() => (section() === 'hosts' && props.id && !isLimits() ? hostRows().find(h => h.id === props.id) : undefined));
  const selConn = createMemo(() => (section() === 'keys' && props.id ? conns().find(c => c.name === props.id) : undefined));
  const reading = (hid: string) => (tele.data?.hosts ?? []).find(r => r.hostId === hid);

  const detailKey = createMemo(() => {
    const sec = section();
    if (sec === 'runners') return 'runners';
    if (sec === 'keys') return props.id ? `key:${props.id}` : undefined;
    if (isLimits()) return 'limits';
    return props.id ? `host:${props.id}` : undefined;
  });

  return (
    <ListDetail
      list={listPane}
      detailKey={detailKey()}
      detail={
        detailKey() !== undefined ? (
          <Switch>
            <Match when={section() === 'runners'}><Runners onClose={() => go('/hosts')} /></Match>
            <Match when={section() === 'keys' && props.id}>
              <Show when={props.id} keyed>{keyId => (
                <KeyDetail id={keyId} conn={selConn()} loading={!connsOp.data && !connsOp.error} error={connsOp.error} onRetry={connsOp.reload} onClose={() => go('/keys')} />
              )}</Show>
            </Match>
            <Match when={isLimits()}><Limits onClose={() => go('/hosts')} /></Match>
            <Match when={section() === 'hosts' && props.id}>
              <Show when={selHost()?.id} keyed fallback={
                <article class="detail stack" data-ui="detail" tabindex={-1} aria-label={props.id}>
                  <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => go('/hosts')} /><h2 class="detail-title">{props.id}</h2></header>
                  <Show when={hostsOp.error}>{err => <ErrorBanner text={`Hosts did not load: ${err()}`} onRetry={hostsOp.reload} />}</Show>
                  <Show when={!hostsOp.data && !hostsOp.error} fallback={!hostsOp.error ? <Empty text={`Host "${props.id}" not found.`} /> : undefined}>
                    <Skeleton rows={6} />
                  </Show>
                </article>
              }>{hostId => (
                <HostDetail
                  host={selHost()!}
                  live={fresh(selHost()!)}
                  reading={reading(hostId)}
                  processes={(tele.data?.processes ?? []).filter(p => p.hostId === hostId)}
                  runs={(tele.data?.runs ?? []).filter(r => r.hostId === hostId)}
                  clockOffsetMs={clockOffsetMs()}
                  onGone={() => go('/hosts')}
                  onOpen={go}
                />
              )}</Show>
            </Match>
          </Switch>
        ) : undefined
      }
      idle={
        section() === 'keys' && !props.id ? (
          <KeysIdle conns={conns()} loading={!connsOp.data && !connsOp.error} error={connsOp.error} onRetry={connsOp.reload} onOpen={go} />
        ) : section() === 'hosts' && !props.id ? (
          <SystemPanels
            tele={tele.data}
            found={found.data}
            error={tele.error ?? found.error}
            onRetry={() => { void tele.reload(); void found.reload(); }}
            clockOffsetMs={clockOffsetMs()}
          />
        ) : undefined
      }
    />
  );
}

function HostDetail(props: { host: HostRow; live: boolean; reading: HostReading | undefined; processes: ProcessReading[]; runs: RunReading[]; clockOffsetMs: number; onGone: () => void; onOpen: (path: string) => void }) {
  const [preview, setPreview] = createSignal<Ops['hosts_remove']['reply'] | null>(null);
  const [err, setErr] = createSignal('');
  const remove = (confirm: boolean) => {
    setErr('');
    void op('hosts_remove', { id: props.host.id, preview: !confirm }).then(
      x => { if (confirm) props.onGone(); else setPreview(x); },
      e => setErr(String(e.message ?? e)),
    );
  };
  return <article class="detail" data-ui="detail" tabindex={-1} aria-label={props.host.id}>
    <header class="detail-h"><Button label="Close" icon="x" iconOnly onClick={() => props.onGone()} /><h2 class="detail-title">{props.host.id}</h2></header>
    <DetailSection title="State">
      {/* The coordinator answers this host's polls with an error: no commands, no update target (host_poll_health.go). */}
      <Show when={props.live && props.host.pollError}>{pe => (
        <ProblemRow title="Polls failing" severity="failure" actions={[]}
          facts={<><span class="mono">{pe().reason}</span> <span class="muted">{pe().status} since</span> <AgeChip t={pe().since} clockOffsetMs={props.clockOffsetMs} /> <span class="muted">{pe().message}</span></>} />
      )}</Show>
      <KvRow icon="activity" label="Last seen" meta={props.host.lastSeenAt ? undefined : 'never'} value={props.host.lastSeenAt ? <AgeChip t={props.host.lastSeenAt} clockOffsetMs={props.clockOffsetMs} /> : undefined} />
      <KvRow icon="panels-top-left" label="Platform" meta={props.host.platform ?? <span data-tip="Platform unknown">—</span>} />
      <Show when={props.reading?.version ?? props.host.version}>{v => <KvRow icon="info" label="Daemon version" meta={<span class="mono">{v()}</span>} />}</Show>
    </DetailSection>
    <DetailSection title="Daemon health">
      <Show when={props.reading} fallback={<span class="muted">No heartbeat recorded</span>}>{r => <>
        <KvRow icon="clock" label="Heartbeat" value={<AgeChip t={ms(r().at)} alertAfter={3 * 60} tone="danger" alertLabel="no heartbeat" clockOffsetMs={props.clockOffsetMs} />} />
        <KvRow icon="activity" label="Load (1 min)" value={<><Num m={m(r().load1)} kind="score" />{r().cpus != null && <span class="muted"> of {r().cpus} cores</span>}</>} />
        <KvRow icon="activity" label="Memory available" value={<><Num m={m(r().memFree)} kind="bytes" />{r().memTotal != null && <span class="muted"> of <Num m={m(r().memTotal)} kind="bytes" /></span>}</>} />
        <KvRow icon="save" label="Disk free" value={<Num m={m(r().diskFree)} kind="bytes" />} />
      </>}</Show>
    </DetailSection>
    <Show when={props.processes.length > 0}>
      <DetailSection title="Daemon processes (per beat)">
        <For each={props.processes}>{p => (
          <KvRow icon="activity" label={p.process}
            value={<RowFacts><Fact icon="gauge" tip="CPU per beat"><Num m={m(p.cpuSeconds)} kind="duration" /></Fact><Fact icon="save" tip="Resident memory"><Num m={m(p.rssBytes)} kind="bytes" /></Fact></RowFacts>} />
        )}</For>
      </DetailSection>
    </Show>
    <DetailSection title="Active & recent executions">
      <KvRow icon="activity" label="Running" value={<><HarnessCounts counts={props.host.runningByHarness} label="by harness" /> <a class="row-link" href="/runners" onClick={e => { e.preventDefault(); props.onOpen('/runners'); }}>Runners</a></>} />
      <KvRow icon="list-ordered" label="Queued commands" value={<Num m={{ value: props.host.queued, basis: 'measured' }} kind="count" />} />
      <For each={props.runs.slice(0, 10)}>{run => (
        <div class="kv-row">
          <EntityCode id={run.executionId} kind="execution" />
          {run.projectId && <EntityCode id={run.projectId} code={run.projectId} kind="project" cls="inline" />}
          <span class="grow" />
          <RowFacts>
            {run.durationMs != null && <Fact icon="clock" tip="Duration"><Num m={m(run.durationMs / 1000)} kind="duration" /></Fact>}
            <Fact icon="gauge" tip="CPU"><Num m={m(run.cpuSeconds)} kind="duration" /></Fact>
            <Fact icon="save" tip="Peak memory"><Num m={m(run.peakRssBytes)} kind="bytes" /></Fact>
            {(run.orphans ?? 0) > 0 && <span class="chip t-danger">{run.orphans} orphan</span>}
            <Fact tip="Finished"><AgeChip t={ms(run.at)} clockOffsetMs={props.clockOffsetMs} /></Fact>
          </RowFacts>
        </div>
      )}</For>
    </DetailSection>
    <Show when={props.live && props.host.problems.length > 0}>
      <DetailSection title="Problems">
        <For each={props.host.problems}>{p => <KvRow icon="activity" label={`Edge answers ${p.route} with ${p.status}`} meta={p.detail || p.location || undefined} />}</For>
      </DetailSection>
    </Show>
    <Show when={props.host.harnessVersions?.length}>
      <DetailSection title="Agent CLIs">
        <For each={props.host.harnessVersions}>{cli => (
          <KvRow icon="info" label={cli.harness} value={<span class="mono" data-tip={props.live && cli.observed ? undefined : 'Observation unavailable or stale'}>{props.live && cli.observed || '—'}</span>}
            meta={(!props.live || cli.observed !== cli.expected) ? `Expected ${cli.expected}` : undefined} />
        )}</For>
      </DetailSection>
    </Show>
    <DetailSection title="Capabilities">
      <Show when={props.host.capabilities.length} fallback={<span class="muted">none reported</span>}>
        <For each={props.host.capabilities}>{c => <KvRow icon="check" label={c} />}</For>
      </Show>
    </DetailSection>
    <DetailSection title="Store">
      <KvRow icon="save" label="Store checks" value={props.live && props.host.stores !== 'unknown' ? props.host.stores : <span class="muted" data-tip={props.live ? props.host.storesReason : 'Host observation stale'}>—</span>} meta={props.live ? props.host.storesReason : 'Host observation stale'} />
    </DetailSection>
    <DetailSection title="Remove">
      <Show when={err()}>{e => <ProblemRow title="Not removed" facts={e()} severity="failure" actions={[]} />}</Show>
      <Show when={preview()} fallback={<Button label="Remove host" icon="trash-2" onClick={() => remove(false)} />}>{p => <>
        <KvRow icon="activity" label="Running work cancelled" value={<Num m={{ value: p().runningExecutions, basis: 'measured' }} kind="count" />} />
        <KvRow icon="activity" label="Open sessions marked" value={<Num m={{ value: p().openSessions, basis: 'measured' }} kind="count" />} />
        <KvRow icon="triangle-alert" label="Artifacts lost (no other copy)" value={<Num m={{ value: p().unreplicatedArtifacts, basis: 'measured' }} kind="count" />} />
        <span class="acts"><Button label="Remove" icon="trash-2" kind="danger" onClick={() => remove(true)} /><Button label="Cancel" kind="quiet" onClick={() => setPreview(null)} /></span>
      </>}</Show>
    </DetailSection>
  </article>;
}

function SystemPanels(props: { tele: Ops['telemetry_summary']['reply'] | null; found: Ops['telemetry_findings']['reply'] | null; error: string | null; onRetry: () => void; clockOffsetMs: number }) {
  const [, go] = useLocation();
  const [purge, setPurge] = createSignal<Ops['system_purge']['reply'] | null>(null);
  const [purgeErr, setPurgeErr] = createSignal('');
  const [allOps, setAllOps] = createSignal(false);
  const runPurge = (preview: boolean) => { setPurgeErr(''); void op('system_purge', { preview, kinds: ['host'] }).then(setPurge, e => setPurgeErr(String(e.message ?? e))); };

  // Only what needs a look is listed (2026-11-20 decision): errors, refusals, or a P95 over a second; the healthy rest folds
  // into one line that opens the whole table.
  const why = (o: OperationStats) => [o.errors ? `${o.errors} ${o.errors === 1 ? 'error' : 'errors'}` : '', o.refused ? `${o.refused} refused` : '', o.p95Ms > 1000 ? 'slow' : ''].filter(Boolean).join(' · ');
  const sorted = createMemo(() => (props.tele?.operations ?? []).toSorted((a, b) => b.errors - a.errors || (b.refused ?? 0) - (a.refused ?? 0) || b.p95Ms - a.p95Ms));
  const notable = createMemo(() => sorted().filter(o => why(o)));
  const ops = createMemo(() => (allOps() ? sorted() : notable()));
  const healthy = createMemo(() => sorted().length - notable().length);

  return (
    <Show when={props.tele} fallback={
      <article class="detail stack" aria-label="System">
        <header class="detail-h"><h2 class="detail-title">System</h2></header>
        <Show when={props.error} fallback={<Skeleton rows={5} />}>{err => <ErrorBanner text={`System telemetry did not load: ${err()}`} onRetry={() => props.onRetry()} />}</Show>
      </article>
    }>{tele => (
      <Show when={tele().enabled} fallback={
        <article class="detail stack" aria-label="System">
          <header class="detail-h"><h2 class="detail-title">System</h2></header>
          <Empty text="Telemetry is off: no span store is configured." />
        </article>
      }>
        <article class="detail" aria-label="System">
          <header class="detail-h"><h2 class="detail-title">System</h2></header>
          <Show when={props.error}>{err => <ErrorBanner text={err()} onRetry={() => props.onRetry()} />}</Show>
          <Show when={tele().error}>{err => <ProblemRow title="Span store" facts={err()} severity="failure" actions={[]} />}</Show>
          <DetailSection title="Findings">
            <Show when={props.found?.stale}><ProblemRow title="Rules pass is stale" facts="Findings may be out of date." severity="warning" actions={[]} /></Show>
            <Show when={props.found?.findings.length} fallback={<span class="muted">none</span>}>
              <For each={props.found!.findings}>{f => (
                <KvRow icon={f.groupCause ? 'circle-alert' : 'triangle-alert'} label={f.text ?? f.cause}
                  meta={f.groupCause ? `part of ${f.groupCause}` : undefined} value={<AgeChip t={ms(f.since)} clockOffsetMs={props.clockOffsetMs} />} />
              )}</For>
            </Show>
          </DetailSection>
          <DetailSection title="Services, last hour">
            <div class="vl-wrap" style={{ '--cols': 'minmax(0,1fr) auto auto auto auto' }}>
              <For each={ops()}>{o => (
                <div role="row" class="vl-row static">
                  <span class="tt"><span class="title">{o.name}</span><span class="id">{o.service}</span></span>
                  <span class={o.errors ? 'danger' : 'muted'}>{why(o)}</span>
                  <span data-tip="Calls"><Num m={{ value: o.count, basis: 'measured' }} kind="count" /></span>
                  <span data-tip="Errors" class={o.errors ? 'danger' : 'muted'}><Num m={{ value: o.errors, basis: 'measured' }} kind="count" /></span>
                  <span data-tip="p95"><Num m={{ value: o.p95Ms / 1000, basis: 'measured' }} kind="duration" /></span>
                </div>
              )}</For>
            </div>
            <Show when={healthy() > 0}>
              <div class="res-bar">
                <span class="muted">{allOps() ? `${sorted().length} operations` : `${healthy()} more are healthy: no errors, none refused, P95 under 1 s.`}</span>
                <Button label={allOps() ? 'Show only what needs a look' : `Show all ${sorted().length}`} kind="quiet" onClick={() => setAllOps(v => !v)} />
              </div>
            </Show>
            <KvRow icon="circle-alert" label="Spans dropped" value={<Num m={{ value: tele().droppedSpans, basis: 'measured' }} kind="count" />} />
          </DetailSection>
          <DetailSection title="Active & recent executions">
            <div class="res-bar"><a class="row-link" href="/runners" onClick={e => { e.preventDefault(); go('/runners'); }}>Open Runners</a></div>
            <Show when={tele().runs.length} fallback={<span class="muted">none in the last hour</span>}>
              <For each={tele().runs.slice(0, 20)}>{r => (
                <div class="kv-row">
                  <EntityCode id={r.executionId} kind="execution" />
                  {r.hostId && <EntityCode id={r.hostId} code={r.hostId} kind="host" cls="inline" />}
                  {r.projectId && <EntityCode id={r.projectId} code={r.projectId} kind="project" cls="inline" />}
                  <span class="grow" />
                  <RowFacts>
                    {r.durationMs != null && <Fact icon="clock" tip="Duration"><Num m={m(r.durationMs / 1000)} kind="duration" /></Fact>}
                    <Fact icon="gauge" tip="CPU"><Num m={m(r.cpuSeconds)} kind="duration" /></Fact>
                    <Fact icon="save" tip="Peak memory"><Num m={m(r.peakRssBytes)} kind="bytes" /></Fact>
                    {(r.orphans ?? 0) > 0 && <span class="chip t-danger">{r.orphans} orphan</span>}
                    <Fact tip="Finished"><AgeChip t={ms(r.at)} clockOffsetMs={props.clockOffsetMs} /></Fact>
                  </RowFacts>
                </div>
              )}</For>
            </Show>
          </DetailSection>
          <DetailSection title="Purge">
            <Show when={purgeErr()}>{e => <ProblemRow title="Not purged" facts={e()} severity="failure" actions={[]} />}</Show>
            <Show when={purge()} fallback={<Button label="Find removed hosts to purge" icon="trash-2" onClick={() => runPurge(true)} />}>{p => <>
              <KvRow icon="trash-2" label="Removed hosts nothing references" value={<Num m={{ value: p().removed[0]?.count ?? 0, basis: 'measured' }} kind="count" />} />
              <KvRow icon="save" label="Removed hosts kept by history" value={<Num m={{ value: p().kept?.[0]?.count ?? 0, basis: 'measured' }} kind="count" />} />
              <Show when={(p().removed[0]?.count ?? 0) > 0 && !p().purged}><Button label="Purge" icon="trash-2" kind="danger" onClick={() => runPurge(false)} /></Show>
            </>}</Show>
          </DetailSection>
        </article>
      </Show>
    )}</Show>
  );
}
