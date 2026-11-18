// Shared resource rollup and recursive execution tree helpers (docs/design-system/pages/{tasks,conversations,projects,definitions,runners,hosts}.md).
import { useState, useRef, useLayoutEffect } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  StatusLine, AgeChip, Button, SearchField, VirtualList, Empty, ROW2, type VirtualListHandle, CostFigures, DetailSection, EntityCode, Icon, Num, StateBadge, Totals, CopyButton,
  msMeasured, numMeasured, splitCostFigures,
  type CostFigureLike, type Measured, type StateOf, type TotalItem, type TreeNode,
} from '../../../packages/ui/src/index.ts';
import { op, OpError } from '../api.ts';
import { usePreparedDoc, preparedKey } from '../store.ts';
import type { ResourceItem, ResourceSortKey, ResourceItemsPage, ProjectResourceItemsRequest, ResourceCategory, ResourceGroup, PreparedHeaderV1, ResourceProgress } from '../../../contracts/generated/ops.ts';
import type { CostFigure, ExecutionRow, ResourceSummary, ScopedResourceRollup } from '../../../contracts/generated/ops.ts';

const ms = (v: string | number | null | undefined) => (v === null || v === undefined ? null : typeof v === 'number' ? v : Date.parse(v));

export const EXEC_STATE: Record<string, StateOf<'execution'>> = {
  completed: 'succeeded', stopped: 'cancelled', unknown: 'running', started: 'running', accepted: 'preparing', pending: 'preparing',
};

export const execState = (s: string): StateOf<'execution'> =>
  (EXEC_STATE[s] ?? (['preparing', 'running', 'waiting', 'succeeded', 'failed', 'cancelled'].includes(s) ? s : 'running')) as StateOf<'execution'>;

/** Merges multiple CostFigure arrays strictly within identical (currency, basis) pairs, never summing metered and subscription. */
export function mergeCosts(lists: readonly (readonly CostFigure[] | null | undefined)[]): CostFigure[] {
  const byKey = new Map<string, CostFigure>();
  for (const list of lists) {
    for (const c of list ?? []) {
      const k = `${c.currency ?? ''}\0${c.basis}`;
      const prev = byKey.get(k);
      if (!prev) {
        byKey.set(k, { ...c });
        continue;
      }
      if (prev.value == null || c.value == null) prev.value = prev.value ?? c.value;
      else prev.value = +(prev.value + c.value).toFixed(6);
      if (c.usages != null) prev.usages = (prev.usages ?? 0) + c.usages;
      if (!prev.reason && c.reason) prev.reason = c.reason;
      if (!prev.note && c.note) prev.note = c.note;
    }
  }
  return [...byKey.values()];
}

function costNote(cost: readonly CostFigureLike[]): string | undefined {
  const { metered, subscription } = splitCostFigures(cost);
  const parts: string[] = [];
  if (metered.some(c => c.value != null)) parts.push('metered');
  if (subscription.length) parts.push(subscription.some(c => c.value != null) ? 'subscription list-price sep.' : 'subscription');
  return parts.length > 1 ? parts.join(' + ') : undefined;
}

/** Converts a ResourceSummary into Totals items, optionally hiding unavailable fields (OR-39). */
function resNum(v: number | null | undefined, basis: Measured['basis'], scale = 1): Measured {
  if (v == null || (basis === 'unavailable' && v <= 0)) return { value: null, basis: 'unavailable' };
  if (basis === 'unavailable') return { value: v * scale, basis: 'measured', partial: 'incomplete telemetry' };
  return { value: v * scale, basis };
}

export function resourceTotals(
  r: ResourceSummary | null | undefined,
  opts?: { extraCost?: readonly CostFigure[]; hideUnavailable?: boolean },
): TotalItem[] {
  const hide = opts?.hideUnavailable ?? false;
  const items: TotalItem[] = [];
  if (r) {
    const { elapsedMs, executionWallMs, cpuMs, measuredCpuRuns, totalRuns, basis } = r.durations;
    const elapsedM = resNum(elapsedMs, basis, 0.001);
    const wallM = resNum(executionWallMs, basis, 0.001);
    if (elapsedM.basis !== 'unavailable' || !hide) items.push({ label: 'Elapsed', m: elapsedM, kind: 'duration' });
    if (wallM.basis !== 'unavailable' || (!hide && elapsedM.basis === 'unavailable')) {
      items.push({ label: 'Execution wall', m: wallM, kind: 'duration' });
    }
    if (cpuMs != null) {
      items.push({
        label: 'CPU',
        m: msMeasured(cpuMs, 'measured'),
        kind: 'duration',
        note: measuredCpuRuns > 0 && measuredCpuRuns < totalRuns ? `${measuredCpuRuns} of ${totalRuns} measured` : undefined,
      });
    }
    const peakM = resNum(r.peakMemoryBytes.value, r.peakMemoryBytes.basis);
    if (peakM.basis !== 'unavailable' || !hide) {
      items.push({ label: 'Peak memory', m: { ...peakM, reason: r.peakMemoryBytes.reason }, kind: 'bytes' });
    }
    const tokM = resNum(r.tokens.total, r.tokens.basis);
    if (tokM.basis !== 'unavailable' || !hide) {
      const { input, output, cacheRead } = r.tokens;
      const parts = [
        input != null ? `${input.toLocaleString('en')} in` : '',
        output != null ? `${output.toLocaleString('en')} out` : '',
        cacheRead ? `${cacheRead.toLocaleString('en')} cache` : '',
      ].filter(Boolean);
      items.push({
        label: 'Tokens',
        m: tokM,
        kind: 'count',
        note: tokM.basis !== 'unavailable' && parts.length ? parts.join(' · ') : undefined,
      });
    }
    const toolM = r.tools.totalCalls > 0 ? resNum(r.tools.totalCalls, r.tools.basis) : { value: 0, basis: r.tools.basis };
    if (r.tools.totalCalls > 0 || (!hide && r.tools.turns > 0 && r.tools.basis !== 'unavailable')) {
      items.push({
        label: 'Tools',
        m: toolM,
        kind: 'count',
        note: r.tools.failedCalls > 0 ? `${r.tools.failedCalls} failed` : r.tools.turns > 0 ? `${r.tools.turns} turns` : undefined,
      });
    }
  }
  const cost = r?.cost?.length ? r.cost : opts?.extraCost ?? [];
  if (cost.length && (!hide || cost.some(c => (c.value !== null && c.basis !== 'unavailable') || c.basis === 'subscription'))) {
    items.push({ label: 'Cost', cost, showUnavailable: !hide, note: costNote(cost) });
  }
  return items;
}

function ResourceCells({ s }: { s: ResourceSummary }) {
  return (
    <>
      <td class="num"><Num m={{ value: s.executions, basis: 'measured' }} kind="count" /></td>
      <td class="num"><Num m={resNum(s.durations.elapsedMs, s.durations.basis, 0.001)} kind="duration" /></td>
      <td class="num"><Num m={resNum(s.durations.executionWallMs, s.durations.basis, 0.001)} kind="duration" /></td>
      <td class="num">{s.durations.cpuMs != null ? <Num m={msMeasured(s.durations.cpuMs, 'measured')} kind="duration" /> : <span class="muted">—</span>}</td>
      <td class="num">{s.peakMemoryBytes.value != null ? <Num m={resNum(s.peakMemoryBytes.value, s.peakMemoryBytes.basis)} kind="bytes" /> : <span class="muted">—</span>}</td>
      <td class="num"><Num m={resNum(s.tokens.total, s.tokens.basis)} kind="count" /></td>
      <td class="num">{s.tools.totalCalls > 0 ? <><Num m={resNum(s.tools.totalCalls, s.tools.basis)} kind="count" />{s.tools.failedCalls > 0 && <span class="chip t-danger">{s.tools.failedCalls} err</span>}</> : <span class="muted">—</span>}</td>
      <td class="num"><CostFigures cost={s.cost} showUnavailable empty="—" /></td>
    </>
  );
}

/** Displays ScopedResourceRollup breakdown (own, delegated, spawned, total), spawned task links, and hidden count. */
export function ScopedRollupDetails({
  rollup,
  cost,
  title = 'Resource breakdown',
}: {
  rollup?: ScopedResourceRollup | null;
  cost?: readonly CostFigure[];
  title?: string;
}) {
  const [, go] = useLocation();
  if (!rollup) return null;
  const total = rollup.total;
  const rows: { label: string; s: ResourceSummary }[] = [
    { label: 'Own', s: rollup.own },
    ...(rollup.delegated.executions > 0 ? [{ label: 'Delegated', s: rollup.delegated }] : []),
    ...(rollup.spawned.executions > 0 || rollup.spawnedTasks.length > 0 ? [{ label: 'Spawned', s: rollup.spawned }] : []),
    ...(rollup.delegated.executions > 0 || rollup.spawned.executions > 0 ? [{ label: 'Total', s: rollup.total }] : []),
  ];
  const hasDetail = total.executions > 0 || total.tokens.total != null || rollup.spawnedTasks.length > 0 || rollup.hidden > 0;
  if (!hasDetail) return null;
  const topTools = total?.tools.byTool.slice(0, 6) ?? [];
  return (
    <DetailSection title={title}>
      {rows.length > 0 && (
        <div class="table-frame">
          <table class="limit-table">
            <thead>
              <tr>
                <th scope="col">Scope</th>
                <th scope="col" class="num">Runs</th>
                <th scope="col" class="num">Elapsed</th>
                <th scope="col" class="num">Execution wall</th>
                <th scope="col" class="num">CPU</th>
                <th scope="col" class="num">Peak mem</th>
                <th scope="col" class="num">Tokens</th>
                <th scope="col" class="num">Tools</th>
                <th scope="col" class="num">Cost</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(r => (
                <tr key={r.label}>
                  <th scope="row">{r.label}</th>
                  <ResourceCells s={r.s} />
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {topTools.length > 0 && (
        <div class="res-bar">
          <span>Top tools:</span>
          {topTools.map(t => (
            <span key={t.name} class="chip" data-tip={t.connection ? `connection: ${t.connection}` : undefined}>
              <Icon name="terminal" label="Tool" />
              <span class="mono">{t.name}</span> · {t.calls}{t.errors > 0 ? ` (${t.errors} err)` : ''}
            </span>
          ))}
          {(total?.tools.other ?? 0) > 0 && <span class="muted">+{total!.tools.other} other calls</span>}
        </div>
      )}
      {(rollup?.spawnedTasks.length ?? 0) > 0 && (
        <div class="stack">
          {rollup!.spawnedTasks.map(st => (
            <div key={st.taskId} class="kv-row">
              <StateBadge family="task" state={(st.state as StateOf<'task'>) || 'queued'} compact />
              <EntityCode code={st.code} id={st.taskId} kind="task" />
              <a class="row-link" href={`/tasks/${encodeURIComponent(st.taskId)}`} onClick={e => { e.preventDefault(); go(`/tasks/${encodeURIComponent(st.taskId)}`); }}>
                {st.title}
              </a>
              <span class="grow" />
              <span class="res-bar">
                {(() => {
                  const stWall = resNum(st.resources.durations.executionWallMs, st.resources.durations.basis, 0.001);
                  const stTok = resNum(st.resources.tokens.total, st.resources.tokens.basis);
                  return <>
                    {stWall.basis !== 'unavailable' && <Num m={stWall} kind="duration" />}
                    {stTok.basis !== 'unavailable' && <Num m={stTok} kind="count" />}
                  </>;
                })()}
                <CostFigures cost={st.resources.cost} />
              </span>
            </div>
          ))}
        </div>
      )}
      {(rollup?.hidden ?? 0) > 0 && (
        <div class="res-bar">
          <span class="chip" data-tip="Spawned or delegated records omitted due to project access limits">
            <Icon name="circle-alert" label="Hidden" />
            <Num m={{ value: rollup!.hidden, basis: 'measured' }} kind="count" /> hidden (restricted project)
          </span>
        </div>
      )}
    </DetailSection>
  );
}

/** Compact one-line summary bar + expandable ScopedRollupDetails for space-constrained headers like Conversations (OR-25). */
export function ScopedRollupBar({ rollup, cost }: { rollup?: ScopedResourceRollup | null; cost?: readonly CostFigure[] }) {
  const [open, setOpen] = useState(false);
  const t = rollup?.total;
  const effCost = t?.cost?.length ? t.cost : cost ?? [];
  const elapsedM = t ? resNum(t.durations?.elapsedMs, t.durations?.basis ?? 'unavailable', 0.001) : null;
  const wallM = t ? resNum(t.durations?.executionWallMs, t.durations?.basis ?? 'unavailable', 0.001) : null;
  const tokM = t ? resNum(t.tokens?.total, t.tokens?.basis ?? 'unavailable') : null;
  const calls = t?.tools?.totalCalls ?? 0;
  const callM = calls > 0 && t ? resNum(calls, t.tools.basis) : null;
  const hidden = rollup?.hidden ?? 0;
  const hasCost = effCost.some(c => (c.value !== null && c.basis !== 'unavailable') || c.basis === 'subscription');
  const hasAny = (elapsedM && elapsedM.basis !== 'unavailable') || (wallM && wallM.basis !== 'unavailable') || (tokM && tokM.basis !== 'unavailable') || !!callM || hasCost || hidden > 0 || (rollup?.spawnedTasks.length ?? 0) > 0;
  if (!hasAny) return null;
  return (
    <>
      <div class="res-bar" data-ui="resource-bar">
        {elapsedM && elapsedM.basis !== 'unavailable' && (
          <span class="hc" data-tip="Elapsed span (first start to last end)">
            <Icon name="clock" label="Elapsed" /><Num m={elapsedM} kind="duration" />
          </span>
        )}
        {wallM && wallM.basis !== 'unavailable' && (elapsedM?.value == null || Math.abs((wallM.value ?? 0) - elapsedM.value) >= 1) && (
          <span class="hc" data-tip="Active execution wall time">
            <Icon name="activity" label="Execution wall" /><Num m={wallM} kind="duration" />
          </span>
        )}
        {tokM && tokM.basis !== 'unavailable' && (
          <span class="hc" data-tip="Total tokens">
            <Num m={tokM} kind="count" /> tok
          </span>
        )}
        {callM && (
          <span class="hc" data-tip={`${calls} tool invocations${t!.tools.failedCalls ? `, ${t!.tools.failedCalls} failed` : ''}`}>
            <Icon name="terminal" label="Tools" /><Num m={callM} kind="count" />
          </span>
        )}
        {effCost.length > 0 && <CostFigures cost={effCost} showUnavailable={false} />}
        {hidden > 0 && (
          <span class="chip" data-tip="Hidden descendant resources due to project access">
            <Icon name="circle-alert" label="Hidden" /><Num m={{ value: hidden, basis: 'measured' }} kind="count" /> hidden
          </span>
        )}
        <span class="grow" />
        <Button
          label={open ? 'Hide breakdown' : 'Resources'}
          icon={open ? 'arrow-up' : 'chevron-down'}
          kind="quiet"
          pressed={open}
          onClick={() => setOpen(v => !v)}
        />
      </div>
      {open && (
        <div class="res-drawer">
          <Totals items={resourceTotals(t, { extraCost: cost, hideUnavailable: false })} />
          <ScopedRollupDetails rollup={rollup} cost={cost} title="Scoped resources" />
        </div>
      )}
    </>
  );
}

/** Builds recursive TreeNode[] from ExecutionRow.parentExecutionId, grouping tool_run siblings cleanly (CA-09). */
export function buildExecTree(executions: readonly ExecutionRow[]): TreeNode[] {
  const byId = new Map<string, ExecutionRow>();
  for (const e of executions) byId.set(e.id, e);
  const childrenByParent = new Map<string | null, ExecutionRow[]>();
  for (const e of executions) {
    const pid = e.parentExecutionId && byId.has(e.parentExecutionId) && e.parentExecutionId !== e.id ? e.parentExecutionId : null;
    const list = childrenByParent.get(pid) ?? [];
    list.push(e);
    childrenByParent.set(pid, list);
  }
  const seen = new Set<string>();
  const leafTime = (e: ExecutionRow) => {
    const a = ms(e.startedAt), b = ms(e.endedAt);
    return a && b && b >= a ? { value: (b - a) / 1000, basis: 'measured' as const } : { value: null, basis: 'unavailable' as const, reason: ['succeeded', 'failed', 'cancelled'].includes(e.state) ? 'terminal timing unavailable' : 'not finished' };
  };
  const labelOf = (e: ExecutionRow) => {
    const cfg = e.configRef && typeof e.configRef === 'object' ? (e.configRef as Record<string, unknown>) : null;
    const h = e.harness ?? (typeof cfg?.harness === 'string' ? cfg.harness : null);
    return [e.kind ?? 'run', h, e.model, e.hostId ?? cfg?.executorService].filter(Boolean).join(' · ');
  };
  const visit = (parentId: string | null): TreeNode[] => {
    const list = childrenByParent.get(parentId) ?? [];
    const regular: ExecutionRow[] = [];
    const tools: ExecutionRow[] = [];
    for (const e of list) {
      if (seen.has(e.id)) continue;
      if (e.kind === 'tool_run') tools.push(e);
      else regular.push(e);
    }
    const out: TreeNode[] = regular.map(e => {
      seen.add(e.id);
      const kids = visit(e.id);
      return {
        id: e.id,
        name: labelOf(e),
        note: e.problem ?? (e.configRef && typeof e.configRef === 'object' && 'publicationAuthority' in e.configRef ? `Publication authority: ${String(e.configRef.publicationAuthority)} (configured role)` : e.sessionId ? 'session' : undefined),
        state: execState(e.state),
        lastSeen: e.observation === 'unconfirmed' ? ms(e.lastSeenAt) ?? undefined : undefined,
        time: leafTime(e),
        children: kids.length ? kids : undefined,
      };
    });
    if (tools.length > 0) {
      let totalMs = 0;
      let anyMs = false;
      let failed = 0;
      let running = false;
      const toolLeaves: TreeNode[] = tools.map(e => {
        seen.add(e.id);
        const st = execState(e.state);
        if (st === 'failed') failed++;
        if (st === 'running' || st === 'preparing' || st === 'waiting') running = true;
        const a = ms(e.startedAt), b = ms(e.endedAt);
        if (a && b && b >= a) { totalMs += b - a; anyMs = true; }
        const kids = visit(e.id);
        return {
          id: e.id,
          name: labelOf(e),
          note: e.problem ?? undefined,
          state: st,
          lastSeen: e.observation === 'unconfirmed' ? ms(e.lastSeenAt) ?? undefined : undefined,
          time: leafTime(e),
          children: kids.length ? kids : undefined,
        };
      });
      out.push({
        id: `${parentId ?? 'root'}:tools`,
        name: 'Tool calls',
        note: `${tools.length}${failed ? ` · ${failed} failed` : ''}`,
        state: running ? 'running' : failed ? 'failed' : 'succeeded',
        time: anyMs ? { value: totalMs / 1000, basis: 'measured' } : undefined,
        children: toolLeaves,
      });
    }
    return out;
  };
  return visit(null);
}

/** Captured positions are opaque when supplied; never decode/rebuild packed tool keys. */
export function resourcePosition(item: ResourceItem): ResourceSortKey {
  if (item.kind === 'tool') return item.position ?? { kind: 'tool', name: item.name, connection: item.connection };
  if (item.kind === 'cost') return { kind: 'cost', currency: item.currency, basis: item.basis };
  return { kind: 'price', priceId: item.priceId, observedAt: item.observedAt };
}
export function resourcePositionKey(key: ResourceSortKey): string {
  return JSON.stringify(key.kind === 'tool-position' ? [key.kind, key.encoded] : key.kind === 'tool' ? [key.kind, key.name, key.connection] : key.kind === 'cost' ? [key.kind, key.currency, key.basis] : [key.kind, key.priceId, key.observedAt]);
}
export const resourceItemKey = (item: ResourceItem) => resourcePositionKey(resourcePosition(item));
export function useProjectResources(projectId: string | undefined, enabled: boolean) {
  return usePreparedDoc(projectId && enabled ? preparedKey('project_resources', projectId) : null,
    ({ signal, knownVersion }) => op('project_resources', { projectId: projectId!, knownVersion }, { signal }),
    { touches: c => c.kind === 'resource_summary' && c.id === projectId });
}
type ProjectResourceState = ReturnType<typeof useProjectResources>;
const preparedTotals = (header: PreparedHeaderV1 | null): TotalItem[] => {
  const unknown: Measured = { value: null, basis: 'unavailable' };
  const metric = (value: number | null | undefined, basis: Measured['basis'] | undefined, scale = 1) => header ? resNum(value, basis ?? 'unavailable', scale) : unknown;
  return [
    { label: 'Executions', m: header ? { value: header.executions, basis: 'measured' } : unknown, kind: 'count' },
    { label: 'Tokens', m: metric(header?.tokens.total, header?.tokens.basis), kind: 'count' },
    { label: 'Reasoning', m: metric(header?.tokens.reasoning, header?.tokens.basis), kind: 'count' },
    { label: 'Elapsed', m: metric(header?.durations.elapsedMs, header?.durations.basis, 0.001), kind: 'duration' },
    { label: 'Execution wall', m: metric(header?.durations.executionWallMs, header?.durations.basis, 0.001), kind: 'duration' },
    { label: 'Active', m: metric(header?.durations.activeMs, header?.durations.basis, 0.001), kind: 'duration' },
    { label: 'API', m: metric(header?.durations.apiMs, header?.durations.basis, 0.001), kind: 'duration' },
    { label: 'CPU', m: { ...metric(header?.durations.cpuMs, header?.durations.basis, 0.001), ...(header && header.durations.measuredCpuRuns < header.durations.totalRuns ? { partial: `${header.durations.measuredCpuRuns} of ${header.durations.totalRuns} measured` } : {}) }, kind: 'duration' },
    { label: 'Peak memory', m: { ...metric(header?.peakMemoryBytes.value, header?.peakMemoryBytes.basis), reason: header?.peakMemoryBytes.reason }, kind: 'bytes' },
    { label: 'Tools', m: metric(header?.tools.totalCalls, header?.tools.basis), kind: 'count' },
    { label: 'Credits', m: metric(header?.credits, header?.credits == null ? 'unavailable' : 'measured'), kind: 'count' },
  ];
};
function ResourceProgressLine({ progress, error }: { progress: ResourceProgress | null; error: string | null }) {
  const now = Date.now();
  return <StatusLine>{error ? 'Refresh failed: ' + error : progress ? <>
    {progress.state === 'current' ? 'Confirmed' : progress.state}{progress.errorClass ? ' · ' + progress.errorClass : ''}
    {' · checked '}<AgeChip t={Date.parse(progress.validatedAt)} now={now} />
    {progress.pending && <> · Pending {progress.oldestPendingAt ? <AgeChip t={Date.parse(progress.oldestPendingAt)} now={now} /> : 'age unknown'}</>}
  </> : 'Preparing confirmed resources…'}</StatusLine>;
}
export function PreparedProjectResources({ projectId, summary, category, onOpen, onClose }: { projectId: string; summary: ProjectResourceState; category?: 'tools' | 'costs'; onOpen?: (category: 'tools' | 'costs') => void; onClose?: () => void }) {
  const reply = summary.data, header = reply && 'header' in reply ? reply.header : null;
  if (category) return <PreparedResourceWindow key={projectId + category} projectId={projectId} category={category} onClose={onClose!} />;
  return <section data-resource-project={projectId} data-resource-version={reply?.version ?? ''} data-resource-state={summary.progress?.state ?? 'initializing'}>
    <ResourceProgressLine progress={summary.progress} error={summary.error} />
    <Totals items={preparedTotals(header)} /><div class="answers">
      <Button label={`Tools · ${header?.itemCounts.tools ?? '—'}`} icon="terminal" disabledReason={!header ? 'Preparing confirmed figures' : undefined} onClick={() => onOpen?.('tools')} />
      <Button label={`Costs · ${header?.itemCounts.costs ?? '—'}`} icon="chart-no-axes-column" disabledReason={!header ? 'Preparing confirmed figures' : undefined} onClick={() => onOpen?.('costs')} />
    </div><p class="muted">Latest confirmed facts. Underlying observation time is unavailable; checked time is not measurement time.</p>
    <Button label="Refresh resources" icon="rotate-cw" iconOnly onClick={() => summary.refresh('retry')} />
  </section>;
}
function PreparedResourceWindow({ projectId, category, group, onClose }: { projectId: string; category: ResourceCategory; group?: ResourceGroup; onClose: () => void }) {
  const handle = useRef<VirtualListHandle | null>(null);
  const [query, setQuery] = useState('');
  const [request, setRequest] = useState<Omit<ProjectResourceItemsRequest, 'projectId'>>({ category, group, limit: 50, anchor: 'start' });
  const [rows, setRows] = useState<ResourceItem[]>([]), [page, setPage] = useState<ResourceItemsPage | null>(null);
  const [prices, setPrices] = useState<ResourceGroup | null>(null);
  const frame = useRef({ rows, page }); frame.current = { rows, page };
  const movement = useRef<'replace' | 'append' | 'prepend'>('replace');
  const firstRequest = useRef<string | null>(null);
  const responseContext = useRef(new WeakMap<object, { requestKey: string; movement: 'replace' | 'append' | 'prepend'; jump?: 'start' | 'end'; anchor: { key: string; offsetPx: number } | null }>());
  const restore = useRef<{ key: string; offsetPx: number; alignEnd?: boolean } | null>(null);
  const [loadingWindow, setLoadingWindow] = useState(false);
  const captured = useRef<{ key: string; offsetPx: number } | null>(null);
  const last = useRef<unknown>(null);
  const windowPages = useRef<ResourceItemsPage[]>([]);
  const retainedPosition = useRef<ResourceSortKey | null>(null);
  const requestKey = preparedKey('project_resource_items', projectId, request);
  const result = usePreparedDoc(requestKey, async ({ signal }) => {
    const first = firstRequest.current !== requestKey;
    const context = { requestKey, movement: first ? movement.current : 'replace' as const, jump: first ? request.anchor : undefined, anchor: null as { key: string; offsetPx: number } | null };
    const anchor = handle.current?.getAnchor() ?? null; if (anchor) captured.current = anchor;
    const item = anchor ? frame.current.rows.find(row => resourceItemKey(row) === anchor.key) : null;
    const position = item ? resourcePosition(item) : retainedPosition.current;
    if (position) retainedPosition.current = position;
    context.anchor = captured.current;
    const base = { projectId, category, group, ...(category === 'tools' ? { query: request.query ?? '' } : {}), limit: 50 };
    let next: ProjectResourceItemsRequest = first ? { ...base, ...request } : position ? { ...base, around: { key: position, direction: 'atOrAfter' } } : { ...base, anchor: 'start' };
    let value;
    try { value = await op('project_resource_items', next, { signal }); }
    catch (e) { if (!(e instanceof OpError) || e.status !== 409) throw e; next = position ? { ...base, around: { key: position, direction: 'atOrAfter' } } : { ...base, anchor: 'start' }; value = await op('project_resource_items', next, { signal }); }
    responseContext.current.set(value, context); return value;
  }, { touches: c => c.kind === 'resource_summary' && c.id === projectId });
  useLayoutEffect(() => {
    const value = result.data;
    if (result.state === 'preparing') {
      windowPages.current = []; last.current = null; setLoadingWindow(false);
      setRows([]); setPage(null); restore.current = { key: '', offsetPx: 0 }; return;
    }
    if (!value || !('items' in value) || last.current === value) return;
    last.current = value;
    const context = responseContext.current.get(value);
    // A preparing/error reply must not consume the requested jump or edge transition.
    if (context) firstRequest.current = context.requestKey;
    const direction = context?.movement ?? 'replace';
    const sameEpoch = !frame.current.page || frame.current.page.version.split(':')[0] === value.version.split(':')[0];
    const segments = sameEpoch && direction === 'append' ? [...windowPages.current, value] : sameEpoch && direction === 'prepend' ? [value, ...windowPages.current] : [value];
    windowPages.current = direction === 'prepend' ? segments.slice(0, 3) : segments.slice(-3);
    const bounded = [...new Map(windowPages.current.flatMap(segment => segment.items).map(item => [resourceItemKey(item), item])).values()];
    // Cursors belong to the actual retained edges, not the most recently fetched page.
    const edges = { previousCursor: windowPages.current[0]?.previousCursor ?? null, nextCursor: windowPages.current.at(-1)?.nextCursor ?? null };
    const a = context?.anchor;
    if (context?.jump === 'end' && bounded.length) restore.current = { key: resourceItemKey(bounded.at(-1)!), offsetPx: 0, alignEnd: true };
    else if (context?.jump === 'start') restore.current = { key: bounded.length ? resourceItemKey(bounded[0]) : '', offsetPx: 0 };
    else if (a && bounded.some(row => resourceItemKey(row) === a.key)) restore.current = a;
    else if (value.resolvedAnchor) restore.current = { key: resourcePositionKey(value.resolvedAnchor), offsetPx: 0 };
    else restore.current = { key: '', offsetPx: 0 };
    setRows(bounded); setPage({ ...value, ...edges });
  }, [result.data, result.state]);
  useLayoutEffect(() => { if (restore.current) { if (restore.current.alignEnd) handle.current?.scrollToKey(restore.current.key, 'end'); else handle.current?.restoreAnchor(restore.current.key, restore.current.offsetPx); restore.current = null; setLoadingWindow(false); } }, [rows]);
  const move = (position: Pick<ProjectResourceItemsRequest, 'anchor' | 'cursor'>, kind: typeof movement.current = 'replace') => { if (position.anchor) { retainedPosition.current = null; captured.current = null; } setLoadingWindow(true); firstRequest.current = null; movement.current = kind; const next = { category, group, query: category === 'tools' ? query : undefined, limit: 50, ...position }; if (preparedKey('project_resource_items', projectId, next) === requestKey) { result.refresh('jump'); } else setRequest(next); };
  if (prices) return <PreparedResourceWindow projectId={projectId} category="prices" group={prices} onClose={() => setPrices(null)} />;
  return <article class="detail conversation" data-ui="detail" data-resource-window={category} data-window-first={rows.length ? resourceItemKey(rows[0]) : ''} data-window-last={rows.length ? resourceItemKey(rows.at(-1)!) : ''} data-resource-version={page?.version ?? ''}>
    <header class="detail-h"><h2>{category === 'prices' ? 'Price basis' : category === 'tools' ? 'Tools' : 'Costs'}</h2><span class="grow" /><Button label="Close resources" icon="x" iconOnly onClick={onClose} /></header>
    <ResourceProgressLine progress={result.progress} error={result.error} />
    {page && <Totals items={preparedTotals(page.header)} />}
    <div class="answers">{category === 'tools' && <SearchField label="Search tools" value={query} onInput={value => { setLoadingWindow(true); setQuery(value.slice(0, 200)); retainedPosition.current = null; captured.current = null; firstRequest.current = null; movement.current = 'replace'; setRequest({ category, query: value.slice(0, 200), limit: 50, anchor: 'start' }); }} onSearch={() => {}} />}
      <span>{rows.length} of {page?.total ?? '—'}</span><Button label="Jump to start" icon="arrow-up" iconOnly onClick={() => move({ anchor: 'start' })} /><Button label="Jump to end" icon="arrow-down" iconOnly onClick={() => move({ anchor: 'end' })} />
      {result.error && <Button label="Retry resources" icon="rotate-cw" onClick={() => result.refresh('retry')} />}</div>
    <VirtualList fill handle={handle} items={rows} getKey={resourceItemKey} label={`Project ${category}`} rowHeight={ROW2} follow="top" columns="1.25rem minmax(0,1fr) auto"
      onEnd={!loadingWindow && page?.nextCursor ? () => move({ cursor: page.nextCursor! }, 'append') : undefined}
      onStart={!loadingWindow && page?.previousCursor ? () => move({ cursor: page.previousCursor! }, 'prepend') : undefined}
      render={item => <><StateBadge family="source" state="current" compact />{item.kind === 'tool' ? <><span>{item.name}</span><span class="num">{item.calls} · {item.errors} failed</span></> : item.kind === 'cost' ? <><CostFigures cost={[{ ...item, reason: item.reason ?? undefined, note: item.note ?? undefined, usages: item.usages ?? undefined }]} showUnavailable /><Button label={`Price basis · ${item.priceCount}`} onClick={() => setPrices({ currency: item.currency, basis: item.basis })} /></> : <><span><CopyButton label="Copy price id" value={item.priceId} iconOnly /> Recorded rate</span><AgeChip t={item.observedAt} now={Date.now()} /></>}</>} />
    {!rows.length && page && <Empty text="No matching confirmed items." />}
  </article>;
}
