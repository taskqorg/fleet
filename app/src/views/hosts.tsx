// System (docs/design-system/pages/system.md, canvas board/screens/system.bl): one CatalogueTree with the roots Hosts,
// Accounts & limits, Connections & keys, Runners, Spend and People (owner only) in the control column; the work area
// shows the readiness and spend summary when idle, or the opened host, account, connection, person, Runners or Spend.
// The screens keep their earlier internal paths (/hosts, /hosts/limits, /keys, /runners, /spend, /humans); the address
// bar shows /system/... (app/src/routes.ts). Staleness is decided on the client from lastSeenAt (no write happens when a
// host stops polling), re-evaluated every 5 s.
import { createMemo, createSignal, For, Match, onSettled, Show, Switch, type Accessor } from 'solid-js';
import { useLocation } from '../router.ts';
import {
  AgeChip, Button, CatalogueTree, ContextMenu, DataTable, DetailHead, DetailSection, Empty, ErrorBanner, Fact, HostBand,
  KvRow, ListDetail, Num, OfflineMarker, ProblemRow, QuestionCard, RowFacts, SearchField, ShellStatus, Skeleton, StateBadge,
  actions, bytes, copyText, duration, harnesses, matchSearch, money,
  type CatalogueTreeNode, type CostFigureLike, type GaugeProps, type WorkerSlotProps,
} from '../../../packages/ui/src/index.ts';
import { op, type Ops } from '../api.ts';
import { closedGroups, flipGroup } from '../lists.ts';
import { listGroups, setListGroups, usePins, prefsRead } from '../prefs.ts';
import { useOp } from '../store.ts';
import type { HostRow, HostReading, ProcessReading, RunnerRun } from '../../../contracts/generated/ops.ts';
import { Limits } from './limits.tsx';
import { AccountDetail, accountHarness, accountName, nearLimit, tightest, useAccountLimits } from './accounts.tsx';
import { KeyDetail, KeysIdle, connectionScope, normalizeConnections, person } from './keys.tsx';
import { Runners } from './runners.tsx';
import { Spend, spendRequest, usdOf } from './spend.tsx';
import { Humans } from './humans.tsx';
import { SystemIdle, hostFailures } from './system-idle.tsx';

export type InfraSection = 'hosts' | 'limits' | 'keys' | 'runners' | 'spend' | 'people';

const INFRA_PINS = 'infrastructure';
const INFRA_TREE_GROUPS = 'infrastructure:tree';
const ROOT_HOSTS_ID = 'root:hosts';
const ROOT_ACCOUNTS_ID = 'root:accounts';
const ROOT_KEYS_ID = 'root:keys';
const ROOT_PEOPLE_ID = 'root:people';
const NAV_RUNNERS_ID = 'nav:runners';
const NAV_SPEND_ID = 'nav:spend';
const DAY = 864e5;

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

/** Percent of cores busy and of memory used, from the host's latest load reading. */
const cpuPct = (l: HostRow['load']) => l?.load1 != null && l.cpus ? Math.min(100, 100 * l.load1 / l.cpus) : null;
const memPct = (l: HostRow['load']) => l?.memFree != null && l.memTotal ? 100 * (1 - l.memFree / l.memTotal) : null;

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
  const limits = useAccountLimits();
  const people = useOp('people_list', () => (me.data?.owner ? { manage: true } : null));
  const today = useOp('spend_summary', () => (prefsRead.value ? spendRequest(1) : null), { quiet: 30_000 });

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
    const curOffset = clockOffsetMs();
    const holder = mainHolder();
    // A host leaf (system.md): state icon, name, and on the right running of slots and last seen; a failing host names
    // its failure instead of its slots.
    const hostLeaves: CatalogueTreeNode[] = hostRows().map(h => {
      const isFresh = fresh(h);
      const limit = h.slotLimit ?? (typeof h.slots === 'number' ? h.slots : null);
      const nodeId = `host:${h.id}`;
      return {
        id: nodeId,
        label: h.id,
        state: {
          family: 'host',
          state: !isFresh ? 'offline' : h.pollError ? 'failing' : 'online',
          reason: h.pollError?.reason,
        },
        right: (
          <RowFacts>
            {isFresh && h.pollError ? <Fact tip={h.pollError.message}><span class="t-danger">polls failing</span></Fact>
              : limit != null ? <Fact tip={`${h.running} of ${limit} slots busy`}><span>{h.running} of {limit}</span></Fact>
                : h.running > 0 ? <Fact tip={`${h.running} busy`}><span>{h.running}</span></Fact> : null}
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

    // An account leaf: state icon, harness mark, name, and left of capacity with the reset on the right.
    const accountLeaves: CatalogueTreeNode[] = (limits.data?.subscriptions ?? []).map(s => {
      const nodeId = `account:${s.id}`;
      const p = tightest(s);
      const h = accountHarness(s.harness);
      const serverNow = limits.data?.serverNow ?? Date.now();
      return {
        id: nodeId,
        label: accountName(s),
        icon: h ? harnesses[h].icon : undefined,
        state: { family: 'subscription', state: s.intake === 'paused' ? 'paused' : 'all' },
        right: (
          <RowFacts>
            {p && p.left !== null && <Fact tip={`${p.period}: ${p.left}% left`}><span class={nearLimit(p) ? 't-attention' : ''}>{p.left}% left</span></Fact>}
            {p?.resetsAt && <Fact tip="Resets in"><span class="muted">{duration(Math.max(0, (p.resetsAt - serverNow) / 1000)).text}</span></Fact>}
          </RowFacts>
        ),
        pinned: pins.has(nodeId),
        href: `/hosts/limits/${encodeURIComponent(s.id)}`,
        problem: nearLimit(p),
        searchText: [accountName(s), s.harness, String(s.holder ?? '')].join(' '),
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
        state: { family: 'intake', state: c.intake as never },
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

    const personLeaves: CatalogueTreeNode[] = [
      ...(people.data?.roster ?? []).map(p => ({
        id: `person:${p.email}`, label: p.name, icon: p.machine ? 'bot' as const : undefined,
        sub: p.owner ? 'owner' : p.machine ? undefined : p.roles.join(', '),
        href: `/humans/${encodeURIComponent(p.email)}`, searchText: [p.name, p.email, ...p.roles].join(' '),
      })),
      ...(people.data?.cloudflareRemovals ?? []).map(p => ({
        id: `person:${p.email}`, label: p.email, sub: 'revoked', problem: true,
        href: `/humans/${encodeURIComponent(p.email)}`, searchText: p.email,
      })),
    ];
    const spendToday = today.data ? usdOf(today.data.totals.cost as CostFigureLike[]) : null;

    return [
      { id: ROOT_HOSTS_ID, label: 'Hosts', icon: 'panels-top-left', defaultCollapsed: false, href: '/hosts', children: hostLeaves },
      { id: ROOT_ACCOUNTS_ID, label: 'Accounts & limits', icon: 'gauge', defaultCollapsed: false, href: '/hosts/limits', children: accountLeaves },
      { id: ROOT_KEYS_ID, label: 'Connections & keys', icon: 'shield', sub: holder ? `held by ${person(holder)}` : undefined, defaultCollapsed: true, href: '/keys', children: keyLeaves },
      // Runners and Spend are roots without children: a click opens their page; no state slot (open item of PR 844).
      { id: NAV_RUNNERS_ID, label: 'Runners', icon: 'play', root: true, href: '/runners', searchText: 'runners executions ci' },
      { id: NAV_SPEND_ID, label: 'Spend', icon: 'chart-no-axes-column', root: true, href: '/spend', searchText: 'spend cost money',
        right: spendToday != null ? <Fact tip="Spend today (UTC)"><span class="muted">{money(spendToday).text} today</span></Fact> : undefined },
      ...(me.data?.owner ? [{ id: ROOT_PEOPLE_ID, label: 'People', icon: 'shield' as const, defaultCollapsed: true, href: '/humans', children: personLeaves }] : []),
    ];
  });

  const selectedTreeId = createMemo(() => {
    const sec = section();
    if (sec === 'runners') return NAV_RUNNERS_ID;
    if (sec === 'spend') return NAV_SPEND_ID;
    if (sec === 'keys') return props.id ? `key:${props.id}` : ROOT_KEYS_ID;
    if (sec === 'limits') return props.id ? `account:${props.id}` : ROOT_ACCOUNTS_ID;
    if (sec === 'people') return props.id ? `person:${props.id}` : ROOT_PEOPLE_ID;
    return props.id ? `host:${props.id}` : ROOT_HOSTS_ID;
  });

  const treeClosed = createMemo(() => closedGroups([ROOT_KEYS_ID, ROOT_PEOPLE_ID], treeFlipped()));
  const collapsedMap = createMemo<Record<string, boolean>>(() => ({
    __pinned__: treeClosed().has('__pinned__'),
    [ROOT_HOSTS_ID]: treeClosed().has(ROOT_HOSTS_ID),
    [ROOT_ACCOUNTS_ID]: treeClosed().has(ROOT_ACCOUNTS_ID),
    [ROOT_KEYS_ID]: treeClosed().has(ROOT_KEYS_ID),
    [ROOT_PEOPLE_ID]: treeClosed().has(ROOT_PEOPLE_ID),
  }));

  const toggleTreeFolder = (folderId: string) => {
    setListGroups(INFRA_TREE_GROUPS, flipGroup(treeFlipped(), folderId));
  };

  const treeQuery = createMemo(() => q().replace(/\b(?:host|agent|project):(\S+)/gi, '$1'));
  const countLeaves = (nodes: readonly CatalogueTreeNode[], tq: string): number => {
    let total = 0;
    for (const n of nodes) {
      if (Array.isArray(n.children)) total += countLeaves(n.children, tq);
      else if (!tq.trim() || matchSearch(tq, [n.label, n.id, n.sub, n.searchText].filter(Boolean).join(' '), n.state ? { state: n.state.state } : {})) total++;
    }
    return total;
  };
  const totalCount = createMemo(() => countLeaves(infraNodes(), ''));
  const shownCount = createMemo(() => treeQuery().trim() ? countLeaves(infraNodes(), treeQuery()) : totalCount());

  const initialLoading = createMemo(() => !hostsOp.data && !connsOp.data && !hostsOp.error && !connsOp.error);
  const loadError = createMemo(() =>
    (hostsOp.error && !hostsOp.data ? hostsOp.error : null)
    ?? (connsOp.error && !connsOp.data ? connsOp.error : null),
  );

  const listPane = (
    <>
      <ShellStatus><OfflineMarker state={hostsOp.state} now={now()} /></ShellStatus>
      <header class="page-h">
        <SearchField label="Find in System" value={q()} onInput={setQ} onSearch={() => {}} />
        <span class="grow" />
        <span class="muted mono" aria-label={`${shownCount()} of ${totalCount()} items`}>
          {shownCount() < totalCount() ? `${shownCount()} of ${totalCount()}` : totalCount()}
        </span>
      </header>
      <Show when={loadError()}>{err => <ErrorBanner text={err()} onRetry={() => { void hostsOp.reload(); void connsOp.reload(); }} />}</Show>
      <section class="panel">
        <Show when={!initialLoading()} fallback={<Skeleton rows={8} />}>
          <CatalogueTree
            fill
            nodes={infraNodes()}
            label="System"
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
              if (!Array.isArray(node.children) && !node.root) setMenu({ node, at });
            }}
          />
        </Show>
        <Show when={menu()}>{mn => (
          <ContextMenu
            at={mn().at}
            label={mn().node.label}
            onClose={() => setMenu(null)}
            items={[
              { label: 'Open', icon: actions.open, onClick: () => { const href = mn().node.href; if (href) go(href); } },
              { label: pins.has(mn().node.id) ? 'Unpin' : 'Pin', icon: pins.has(mn().node.id) ? actions.unpin : actions.pin, onClick: () => pins.toggle(mn().node.id) },
              { label: 'Copy id', icon: actions.copy, onClick: () => copyText(rawNodeId(mn().node.id)) },
            ]}
          />
        )}</Show>
      </section>
    </>
  );

  const selHost = createMemo(() => (section() === 'hosts' && props.id ? hostRows().find(h => h.id === props.id) : undefined));
  const selConn = createMemo(() => (section() === 'keys' && props.id ? conns().find(c => c.name === props.id) : undefined));

  // The work area: Runners, Spend, Limits and People are pages of their own; a host, account or connection is an item.
  const detailKey = createMemo(() => {
    const sec = section();
    if (sec === 'runners' || sec === 'spend') return sec;
    if (sec === 'people') return `people:${props.id ?? ''}`;
    if (sec === 'limits') return props.id ? `account:${props.id}` : 'limits';
    if (sec === 'keys') return props.id ? `key:${props.id}` : undefined;
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
            <Match when={section() === 'spend'}><Spend /></Match>
            <Match when={section() === 'people'}><Humans id={props.id} /></Match>
            <Match when={section() === 'limits' && !props.id}><Limits onClose={() => go('/hosts')} /></Match>
            <Match when={section() === 'limits' && props.id}>
              <Show when={props.id} keyed>{id => <AccountDetail id={id} onClose={() => go('/hosts/limits')} onOpen={go} />}</Show>
            </Match>
            <Match when={section() === 'keys' && props.id}>
              <Show when={props.id} keyed>{keyId => (
                <KeyDetail id={keyId} conn={selConn()} loading={!connsOp.data && !connsOp.error} error={connsOp.error} onRetry={connsOp.reload} onClose={() => go('/keys')} />
              )}</Show>
            </Match>
            <Match when={section() === 'hosts' && props.id}>
              <Show when={selHost()?.id} keyed fallback={
                <article class="detail stack" data-ui="detail" tabindex={-1} aria-label={props.id}>
                  <DetailHead crumb="system · host" title={props.id} onClose={() => go('/hosts')} />
                  <Show when={hostsOp.error}>{err => <ErrorBanner text={`Hosts did not load: ${err()}`} onRetry={hostsOp.reload} />}</Show>
                  <Show when={!hostsOp.data && !hostsOp.error} fallback={!hostsOp.error ? <Empty text={`Host "${props.id}" not found.`} /> : undefined}>
                    <Skeleton rows={6} />
                  </Show>
                </article>
              }>{_hostId => (
                <HostDetail host={selHost()!} live={fresh(selHost()!)} now={now()} clockOffsetMs={clockOffsetMs()} onGone={() => go('/hosts')} onOpen={go} />
              )}</Show>
            </Match>
          </Switch>
        ) : undefined
      }
      idle={
        section() === 'keys' ? (
          <KeysIdle conns={conns()} loading={!connsOp.data && !connsOp.error} error={connsOp.error} onRetry={connsOp.reload} onOpen={go} />
        ) : (
          <SystemIdle hosts={hostRows()} fresh={fresh} now={now()} clockOffsetMs={clockOffsetMs()} conns={conns()}
            loading={!hostsOp.data || !connsOp.data} error={loadError()} onRetry={() => { void hostsOp.reload(); void connsOp.reload(); }} onOpen={go} />
        )
      }
    />
  );
}

/** A host's slots in fill order (hosts_list runs), titled from the day's runs; free slots up to its limit. */
function hostSlots(h: HostRow, runs: readonly RunnerRun[], onOpen: (path: string) => void): WorkerSlotProps[] {
  const byId = new Map(runs.map(r => [r.id, r]));
  const busy = (h.runs ?? []).map((r, i): WorkerSlotProps => {
    const row = byId.get(r.id);
    const hn = row?.harness && row.harness in harnesses ? harnesses[row.harness as keyof typeof harnesses].label : row?.harness ?? undefined;
    return {
      slot: i + 1, state: 'busy', title: row?.taskTitle ?? (r.state === 'preparing' ? 'Preparing' : row?.sessionId ? 'Conversation' : 'Run'),
      harness: hn, started: r.startedAt ? ms(r.startedAt) : undefined,
      tokens: row?.tokens != null ? { value: row.tokens, basis: 'measured' } : undefined,
      onOpen: row?.taskId ? () => onOpen(`/tasks/${encodeURIComponent(row.taskId!)}`) : row?.sessionId ? () => onOpen(`/conversations/${encodeURIComponent(row.sessionId!)}`) : undefined,
    };
  });
  // Free capacity shows as one idle tile: the head already says how many of the slots are busy, and a 24-slot host would
  // otherwise fill the work area with empty tiles.
  const free = Math.max(0, (h.slotLimit ?? 0) - busy.length);
  return free > 0 ? [...busy, { slot: busy.length + 1, state: 'idle' }] : busy;
}

function HostDetail(props: { host: HostRow; live: boolean; now: number; clockOffsetMs: number; onGone: () => void; onOpen: (path: string) => void }) {
  // Spans change without change rows: readings and processes are re-read every 30 s; the day's runs on a timer, like Runners.
  const tele = useOp('telemetry_summary', { window: '1h' }, { every: 30000 });
  const found = useOp('telemetry_findings', {}, { every: 30000 });
  const runners = useOp('executions_runners', {}, { every: 15000 });
  const [confirm, setConfirm] = createSignal<Ops['hosts_remove']['reply'] | null>(null);
  const [removing, setRemoving] = createSignal<string | undefined>();
  const [err, setErr] = createSignal('');
  const h = () => props.host;
  const reading = createMemo<HostReading | undefined>(() => (tele.data?.hosts ?? []).find(r => r.hostId === h().id));
  const processes = createMemo<ProcessReading[]>(() => (tele.data?.processes ?? []).filter(p => p.hostId === h().id));
  // Tool-call rows (one per caller) are not runs of this host's slots.
  const dayRuns = createMemo(() => (runners.data?.runs ?? []).filter(r => r.hostId === h().id && r.calls == null));
  const failures = createMemo(() => hostFailures(dayRuns(), props.now).get(h().id) ?? []);
  const findings = createMemo(() => (found.data?.findings ?? []).filter(f => f.hostId === h().id));
  const preview = () => {
    setErr('');
    void op('hosts_remove', { id: h().id, preview: true }).then(setConfirm, e => setErr(String(e.message ?? e)));
  };
  const answer = (a: string) => {
    if (a !== 'Remove') { setConfirm(null); return; }
    setRemoving(a);
    void op('hosts_remove', { id: h().id, preview: false }).then(() => props.onGone(), e => { setErr(String(e.message ?? e)); setRemoving(undefined); });
  };
  const load = () => h().load;
  const gauges = createMemo<GaugeProps[]>(() => props.live ? [
    // Host settings have no resource thresholds yet: the Hosts spec's 80 and 95 percent stand in.
    { label: 'CPU', name: 'CPU load', value: cpuPct(load()), warnAt: 80, critAt: 95 },
    { label: 'MEM', name: 'Memory', value: memPct(load()), warnAt: 80, critAt: 95 },
  ] : []);
  const active = createMemo(() => dayRuns().reduce((s, r) => s + (r.wallMs ?? ((r.endedAt ?? props.now) - (r.startedAt ?? props.now))), 0));
  const problemLine = createMemo(() => {
    if (!props.live) return undefined;
    if (h().pollError) return `Polls failing: coordinator answers ${h().pollError!.status} (${h().pollError!.reason}).`;
    const f = failures()[0];
    return f ? `${f.text.split('. ')[0]}${f.n > 1 ? ` ×${f.n}` : ''} in the last hour.` : undefined;
  });
  const stateLine = () => !props.live
    ? <><StateBadge family="host" state="offline" compact lastSeen={h().lastSeenAt ?? undefined} /> <span class="muted">{h().running > 0 ? 'running · unconfirmed, last seen' : h().lastSeenAt ? 'last seen' : 'never polled'}</span> {h().lastSeenAt && <AgeChip t={h().lastSeenAt!} clockOffsetMs={props.clockOffsetMs} />}</>
    : h().pollError
      ? <><StateBadge family="host" state="failing" compact reason={h().pollError!.reason} /> <span class="muted">polls failing since</span> <AgeChip t={h().pollError!.since} clockOffsetMs={props.clockOffsetMs} /></>
      : <><StateBadge family="host" state="online" compact /> <span class="muted">{h().running} of {h().slotLimit ?? '?'} slots busy{h().queued > 0 ? ` · ${h().queued} queued` : ''}</span></>;

  return <article class="detail" data-ui="detail" tabindex={-1} aria-label={h().id}>
    <DetailHead crumb="system · host" title={h().id} state={stateLine()} onClose={props.onGone}
      actions={<Button label="Show on Live" kind="quiet" onClick={() => props.onOpen('/live')} />} />
    <Show when={props.live && h().pollError}>{pe => (
      <ProblemRow title="Polls failing" severity="failure" actions={[{ label: 'Remove host', onClick: preview }]}
        facts={<><span>{pe().message || `coordinator answers ${pe().status}`}</span> <span class="muted mono">{pe().reason}</span> <span class="muted">since</span> <AgeChip t={pe().since} clockOffsetMs={props.clockOffsetMs} /></>} />
    )}</Show>
    <For each={props.live ? failures() : []}>{f => (
      <ProblemRow title={f.text.split('. ')[0]} count={f.n} severity="failure" actions={[{ label: 'Open Runners', onClick: () => props.onOpen('/runners') }]}
        facts={<>{f.text.split('. ').slice(1).join('. ')} <AgeChip t={f.at} clockOffsetMs={props.clockOffsetMs} /></>} />
    )}</For>
    <For each={props.live ? h().problems : []}>{p => (
      <ProblemRow title={`Edge answers ${p.route} with ${p.status}`} severity="failure" actions={[]} facts={p.detail || p.location || ''} />
    )}</For>
    <For each={findings()}>{f => <ProblemRow title={f.text ?? f.cause} severity="warning" actions={[]} facts={<AgeChip t={ms(f.since)} clockOffsetMs={props.clockOffsetMs} />} />}</For>
    <HostBand name={h().id}
      state={!props.live ? 'offline' : problemLine() ? 'problem' : 'normal'}
      facts={[h().platform, load()?.diskFree != null ? `${bytes(load()!.diskFree!).text} free` : ''].filter(Boolean).join(' · ')}
      gauges={gauges()} problem={problemLine()} seen={h().lastSeenAt ?? undefined}
      day={runners.data ? `${dayRuns().length} runs · ${duration(active() / 1000).text}` : undefined}
      slots={hostSlots(h(), dayRuns(), props.onOpen)}
      strip={runners.data ? { now: props.now, runs: dayRuns().filter(r => r.startedAt != null && props.now - r.startedAt < DAY).map(r => ({ start: r.startedAt!, end: r.endedAt, failed: r.state === 'failed' })) } : undefined} />
    <Show when={h().harnessVersions?.length}>
      <DetailSection title="Agent CLIs">
        <DataTable label="Agent CLIs" head={<tr><th scope="col">CLI</th><th scope="col" class="num">Seen</th><th scope="col" class="num">Expected</th></tr>}>
          <For each={h().harnessVersions}>{cli => (
            <tr>
              <th scope="row">{cli.harness}</th>
              <td class={['num mono', { 't-attention': props.live && !!cli.observed && cli.observed !== cli.expected }]} data-tip={props.live && cli.observed ? undefined : 'Observation unavailable or stale'}>{props.live && cli.observed || '—'}</td>
              <td class="num mono">{cli.expected}</td>
            </tr>
          )}</For>
        </DataTable>
      </DetailSection>
    </Show>
    <DetailSection title="Readings">
      <Show when={reading()} fallback={<span class="muted">No heartbeat recorded</span>}>{r => <>
        <KvRow icon="clock" label="Heartbeat" value={<AgeChip t={ms(r().at)} alertAfter={3 * 60} tone="danger" alertLabel="no heartbeat" clockOffsetMs={props.clockOffsetMs} />} />
        <KvRow icon="activity" label="Load (1 min)" value={<><Num m={m(r().load1)} kind="score" />{r().cpus != null && <span class="muted"> of {r().cpus} cores</span>}</>} />
        <KvRow icon="activity" label="Memory available" value={<><Num m={m(r().memFree)} kind="bytes" />{r().memTotal != null && <span class="muted"> of <Num m={m(r().memTotal)} kind="bytes" /></span>}</>} />
        <KvRow icon="save" label="Disk free" value={<Num m={m(r().diskFree)} kind="bytes" />} />
      </>}</Show>
      <For each={processes()}>{p => (
        <KvRow icon="activity" label={p.process}
          value={<RowFacts><Fact icon="gauge" tip="CPU per beat"><Num m={m(p.cpuSeconds)} kind="duration" /></Fact><Fact icon="save" tip="Resident memory"><Num m={m(p.rssBytes)} kind="bytes" /></Fact></RowFacts>} />
      )}</For>
      <Show when={h().version}>{v => <KvRow icon="info" label="Daemon version" meta={<span class="mono">{v()}</span>} />}</Show>
    </DetailSection>
    <DetailSection title="Capabilities">
      <Show when={h().capabilities.length} fallback={<span class="muted">none reported</span>}>
        <p class="sys-caps">{[...new Set(h().capabilities)].join(' · ')}</p>
      </Show>
    </DetailSection>
    <DetailSection title="Store">
      <KvRow icon="save" label="Store checks" value={props.live && h().stores !== 'unknown' ? h().stores : <span class="muted" data-tip={props.live ? h().storesReason : 'Host observation stale'}>—</span>} meta={props.live ? h().storesReason : 'Host observation stale'} />
    </DetailSection>
    <Show when={err()}>{e => <ProblemRow title="Not removed" facts={e()} severity="failure" actions={[]} />}</Show>
    {/* Risky action (OR-61): the confirmation card names the scope and the way back. */}
    <Show when={confirm()} fallback={<div class="res-bar"><Button label="Remove host" icon="trash-2" kind="quiet" onClick={preview} /></div>}>{c => (
      <QuestionCard tone="danger" answering={removing()}
        question={`Remove ${h().id}? ${c().runningExecutions} running ${c().runningExecutions === 1 ? 'run is' : 'runs are'} cancelled, ${c().openSessions} open ${c().openSessions === 1 ? 'session is' : 'sessions are'} marked, ${c().unreplicatedArtifacts} ${c().unreplicatedArtifacts === 1 ? 'result has' : 'results have'} no other copy. Way back: register the host again.`}
        choices={[{ label: 'Remove', primary: true }, { label: 'Keep' }]} onAnswer={answer} />
    )}</Show>
  </article>;
}
