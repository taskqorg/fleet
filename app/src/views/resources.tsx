// Shared resource rollup and recursive execution tree helpers (docs/design-system/pages/{tasks,conversations,projects,definitions,runners,hosts}.md).
import { For, Match, Show, Switch, createEffect, createMemo, createSignal, onSettled, untrack } from 'solid-js';
import { useLocation } from '../router.ts';
import { readValue, type MaybeAccessor } from '../signal.ts';
import {
  StatusLine, AgeChip, Button, DataTable, Disclosure, SearchBox, SearchField, VirtualList, Empty, ROW2, type VirtualListHandle, CostFigures, DetailSection, EntityCode, Icon, Num, StateBadge, Totals, CopyButton,
  cause, formatRowSpend, msMeasured, numMeasured, splitCostFigures,
  type CostFigureLike, type Measured, type StateOf, type TotalItem, type TreeNode,
  isFinished,
} from '../../../packages/ui/src/index.ts';
import { op, OpError } from '../api.ts';
import { useOp, usePreparedOp, preparedKey } from '../store.ts';
import { accessGeneration } from '../access.ts';
import type { ResourceItem, ResourceSortKey, ResourceItemsPage, ProjectResourceItemsRequest, ResourceCategory, ResourceGroup, PreparedHeaderV1, ResourceProgress } from '../../../contracts/generated/ops.ts';
import type { CostFigure, ExecutionRow, ListSpendRequest, ResourceSummary, ScopedResourceRollup } from '../../../contracts/generated/ops.ts';

const ms = (v: string | number | null | undefined) => (v === null || v === undefined ? null : typeof v === 'number' ? v : Date.parse(v));

/** A single scoped read for the loaded rows, independent of list membership refresh. */
export function useRowSpend(kind: ListSpendRequest['kind'], rowsInput: MaybeAccessor<readonly { id: string }[]>) {
  const rowIds = createMemo(() => Array.from(new Set(readValue(rowsInput).map(row => row.id))).sort());
  const read = useOp('list_spend', () => (rowIds().length ? { kind, rowIds: rowIds() } : null), { quiet: 1000 });
  let retained: { generation: number; rows: ReadonlyMap<string, CostFigure[]> } = { generation: -1, rows: new Map() };
  const cost = createMemo(() => {
    const ids = rowIds();
    const generation = accessGeneration();
    const previous = retained.generation === generation ? retained.rows : new Map<string, CostFigure[]>();
    const data = read.data;
    const current = new Map<string, CostFigure[]>();
    for (const id of ids) {
      const figures = data ? (Object.hasOwn(data.rows, id) ? data.rows[id] : undefined) : previous.get(id);
      if (figures) current.set(id, figures);
    }
    retained = { generation, rows: current };
    return current;
  });
  return {
    get cost() { return cost(); },
    get error() { return read.error; },
  };
}

export function RowSpend(props: {
  cost?: readonly CostFigure[]; href: string; open: (extend: boolean) => void; stale: boolean; name?: string;
}) {
  const fmt = createMemo(() => formatRowSpend(props.cost, props.stale, props.name));
  return (
    <a
      class="fact row-spend"
      href={props.href}
      data-tip={fmt().tip}
      aria-label={fmt().ariaLabel}
      onClick={e => {
        if (e.metaKey || e.ctrlKey || e.altKey) return;
        e.preventDefault();
        props.open(e.shiftKey);
      }}
    >
      <Show when={fmt().hasFigures} fallback={fmt().primaryText}>
        <span class="spend-primary">
          <Show when={fmt().subscription}><Icon name="rotate-cw" /></Show>
          {fmt().primaryText}
        </span>
        <Show when={fmt().suffix}>{s => <span>{s()}</span>}</Show>
      </Show>
    </a>
  );
}

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

function ResourceCells(props: { s: ResourceSummary }) {
  return (
    <>
      <td class="num"><Num m={{ value: props.s.executions, basis: 'measured' }} kind="count" /></td>
      <td class="num"><Num m={resNum(props.s.durations.elapsedMs, props.s.durations.basis, 0.001)} kind="duration" /></td>
      <td class="num"><Num m={resNum(props.s.durations.executionWallMs, props.s.durations.basis, 0.001)} kind="duration" /></td>
      <td class="num">
        <Show when={props.s.durations.cpuMs != null} fallback={<span class="muted">—</span>}>
          <Num m={msMeasured(props.s.durations.cpuMs!, 'measured')} kind="duration" />
        </Show>
      </td>
      <td class="num">
        <Show when={props.s.peakMemoryBytes.value != null} fallback={<span class="muted">—</span>}>
          <Num m={resNum(props.s.peakMemoryBytes.value, props.s.peakMemoryBytes.basis)} kind="bytes" />
        </Show>
      </td>
      <td class="num"><Num m={resNum(props.s.tokens.total, props.s.tokens.basis)} kind="count" /></td>
      <td class="num">
        <Show when={props.s.tools.totalCalls > 0} fallback={<span class="muted">—</span>}>
          <Num m={resNum(props.s.tools.totalCalls, props.s.tools.basis)} kind="count" />
          <Show when={props.s.tools.failedCalls > 0}>
            <span class="chip t-danger">{props.s.tools.failedCalls} err</span>
          </Show>
        </Show>
      </td>
      <td class="num"><CostFigures cost={props.s.cost} showUnavailable empty="—" /></td>
    </>
  );
}

/** Displays ScopedResourceRollup breakdown (own, delegated, spawned, total), spawned task links, and hidden count. */
export function ScopedRollupDetails(props: { rollup: ScopedResourceRollup; foldTools?: boolean }) {
  const [, go] = useLocation();
  const [taskQuery, setTaskQuery] = createSignal('');
  const tasks = createMemo(() => {
    const term = taskQuery().trim().toLowerCase();
    return props.rollup.spawnedTasks.filter(task => !term || task.title.toLowerCase().includes(term) || task.code?.toLowerCase().includes(term));
  });
  const shown = createMemo(() => tasks().slice(0, 8));
  const rows = createMemo(() => {
    const r = props.rollup;
    const out: { label: string; s: ResourceSummary }[] = [
      { label: 'Own', s: r.own },
      ...(r.delegated.executions > 0 ? [{ label: 'Delegated', s: r.delegated }] : []),
      ...(r.spawned.executions > 0 || r.spawnedTasks.length > 0 ? [{ label: 'Spawned', s: r.spawned }] : []),
    ];
    return out;
  });
  const tools = () => props.rollup.total.tools;
  const topTools = createMemo(() => tools().byTool.slice(0, 6));
  const renderToolBreakdown = () => (
    <div class="res-bar">
      <For each={topTools()}>
        {tool => (
          <span class="chip" data-tip={tool.connection ? `connection: ${tool.connection}` : undefined}>
            <Icon name="terminal" label="Tool" />
            <span class="mono">{tool.name}</span> · {tool.calls}{tool.errors > 0 ? ` (${tool.errors} err)` : ''}
          </span>
        )}
      </For>
      <Show when={(tools().other ?? 0) > 0}>
        <span class="muted">+{tools().other} other calls</span>
      </Show>
    </div>
  );

  return (
    <Show when={rows().length > 1 || props.rollup.hidden > 0 || topTools().length > 0}>
      <DetailSection title="Resource breakdown">
        <Show when={rows().length > 1}>
          <DataTable
            label="Resource breakdown"
            head={
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
            }
          >
            <For each={rows()}>
              {r => (
                <tr>
                  <th scope="row">{r.label}</th>
                  <ResourceCells s={r.s} />
                </tr>
              )}
            </For>
          </DataTable>
        </Show>
        <Show when={topTools().length > 0}>
          <Show when={props.foldTools} fallback={<><h4 class="sub-h">Top tools</h4>{renderToolBreakdown()}</>}>
            <Disclosure summary="Top tools">{renderToolBreakdown()}</Disclosure>
          </Show>
        </Show>
        <Show when={props.rollup.spawnedTasks.length > 0}>
          <div class="stack spawned-tasks">
            <Show when={props.rollup.spawnedTasks.length > 8}>
              <SearchBox label="Find spawned tasks" value={taskQuery()} onInput={setTaskQuery} always />
            </Show>
            <Show when={shown().length < tasks().length}>
              <span class="muted">{shown().length} of {tasks().length} tasks</span>
            </Show>
            <Show when={!shown().length}>
              <Empty text="No spawned tasks match." />
            </Show>
            <For each={shown()}>
              {st => {
                const stWall = resNum(st.resources.durations.executionWallMs, st.resources.durations.basis, 0.001);
                const stTok = resNum(st.resources.tokens.total, st.resources.tokens.basis);
                return (
                  <div class="kv-row">
                    <StateBadge family="task" state={(st.state as StateOf<'task'>) || 'queued'} compact />
                    <EntityCode code={st.code} id={st.taskId} kind="task" />
                    <a
                      class="row-link"
                      href={`/tasks/${encodeURIComponent(st.taskId)}`}
                      onClick={e => { e.preventDefault(); go(`/tasks/${encodeURIComponent(st.taskId)}`); }}
                    >
                      {st.title}
                    </a>
                    <span class="grow" />
                    <span class="res-bar">
                      <Show when={stWall.basis !== 'unavailable'}><Num m={stWall} kind="duration" /></Show>
                      <Show when={stTok.basis !== 'unavailable'}><Num m={stTok} kind="count" /></Show>
                      <CostFigures cost={st.resources.cost} />
                    </span>
                  </div>
                );
              }}
            </For>
          </div>
        </Show>
        <Show when={props.rollup.hidden > 0}>
          <div class="res-bar">
            <span class="chip" data-tip="Spawned or delegated records omitted due to project access limits">
              <Icon name="circle-alert" label="Hidden" />
              <Num m={{ value: props.rollup.hidden, basis: 'measured' }} kind="count" /> hidden (restricted project)
            </span>
          </div>
        </Show>
      </DetailSection>
    </Show>
  );
}

export function ScopedRollupBar(props: { rollup?: ScopedResourceRollup | null; cost?: readonly CostFigure[]; inlineDetails?: boolean }) {
  const inlineDetails = () => props.inlineDetails ?? true;
  const totals = createMemo(() => resourceTotals(props.rollup?.total, { extraCost: props.cost, hideUnavailable: true }));
  const hidden = createMemo(() => (inlineDetails() ? 0 : props.rollup?.hidden ?? 0));
  return (
    <>
      <Show when={totals().length > 0 || hidden() > 0}>
        <div data-ui="resource-bar" aria-label="Resources">
          <Show when={totals().length > 0}><Totals items={totals()} compact /></Show>
          <Show when={hidden() > 0}><span class="res-bar"><Icon name="circle-alert" />{hidden()} hidden (restricted project)</span></Show>
        </div>
      </Show>
      <Show when={inlineDetails() && props.rollup} keyed>
        {r => <ScopedRollupDetails rollup={r} foldTools />}
      </Show>
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
    return a && b && b >= a ? { value: (b - a) / 1000, basis: 'measured' as const } : { value: null, basis: 'unavailable' as const, reason: isFinished('execution', e.state) ? 'terminal timing unavailable' : 'not finished' };
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
        note: (e.problem ? cause(e.problem).text : undefined) ?? (e.configRef && typeof e.configRef === 'object' && 'publicationAuthority' in e.configRef ? `Publication authority: ${String(e.configRef.publicationAuthority)} (configured role)` : e.sessionId ? 'session' : undefined),
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
          note: e.problem ? cause(e.problem).text : undefined,
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
export function useProjectResources(projectId: MaybeAccessor<string | undefined>, enabled: MaybeAccessor<boolean>) {
  return usePreparedOp('project_resources', () => {
    const id = readValue(projectId);
    return id && readValue(enabled) ? { projectId: id } : null;
  });
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
function ResourceProgressLine(props: { progress: ResourceProgress | null; error: string | null }) {
  return (
    <StatusLine>
      <Show
        when={!props.error}
        fallback={<>{'Refresh failed: ' + props.error}</>}
      >
        <Show when={props.progress} keyed fallback="Preparing confirmed resources…">
          {p => (
            <>
              {p.state === 'current' ? 'Confirmed' : p.state}{p.errorClass ? ' · ' + p.errorClass : ''}
              {' · checked '}<AgeChip t={Date.parse(p.validatedAt)} />
              <Show when={p.pending}>
                {' · Pending '}
                <Show when={p.oldestPendingAt} fallback="age unknown">
                  {at => <AgeChip t={Date.parse(at())} />}
                </Show>
              </Show>
            </>
          )}
        </Show>
      </Show>
    </StatusLine>
  );
}
export function PreparedProjectResources(props: { projectId: string; summary: ProjectResourceState; category?: 'tools' | 'costs'; onOpen?: (category: 'tools' | 'costs') => void; onClose?: () => void }) {
  const reply = () => props.summary.data;
  const header = createMemo(() => {
    const r = reply();
    return r && 'header' in r ? r.header : null;
  });
  return (
    <Show
      when={props.category}
      keyed
      fallback={
        <section
          data-resource-project={props.projectId}
          data-resource-version={reply()?.version ?? ''}
          data-resource-state={props.summary.progress?.state ?? 'initializing'}
        >
          <ResourceProgressLine progress={props.summary.progress} error={props.summary.error} />
          <Totals items={preparedTotals(header())} />
          <div class="answers">
            <Button
              label={`Tools · ${header()?.itemCounts.tools ?? '—'}`}
              icon="terminal"
              disabledReason={!header() ? 'Preparing confirmed figures' : undefined}
              onClick={() => props.onOpen?.('tools')}
            />
            <Button
              label={`Costs · ${header()?.itemCounts.costs ?? '—'}`}
              icon="chart-no-axes-column"
              disabledReason={!header() ? 'Preparing confirmed figures' : undefined}
              onClick={() => props.onOpen?.('costs')}
            />
          </div>
          <p class="muted">Latest confirmed facts. Underlying observation time is unavailable; checked time is not measurement time.</p>
          <Button label="Refresh resources" icon="rotate-cw" iconOnly onClick={() => props.summary.refresh('retry')} />
        </section>
      }
    >
      {cat => <PreparedResourceWindow projectId={props.projectId} category={cat} onClose={props.onClose!} />}
    </Show>
  );
}
function PreparedResourceWindow(props: { projectId: string; category: ResourceCategory; group?: ResourceGroup; onClose: () => void }) {
  const handle: { current: VirtualListHandle | null } = { current: null };
  const [query, setQuery] = createSignal('');
  const [request, setRequest] = createSignal<Omit<ProjectResourceItemsRequest, 'projectId'>>({
    category: untrack(() => props.category),
    group: untrack(() => props.group),
    limit: 50,
    anchor: 'start',
  });
  const [rows, setRows] = createSignal<ResourceItem[]>([], { ownedWrite: true });
  const [page, setPage] = createSignal<ResourceItemsPage | null>(null, { ownedWrite: true });
  const [prices, setPrices] = createSignal<ResourceGroup | null>(null);
  let frameRows: ResourceItem[] = [];
  let framePage: ResourceItemsPage | null = null;
  let movement: 'replace' | 'append' | 'prepend' = 'replace';
  let firstRequest: string | null = null;
  const responseContext = new WeakMap<object, { requestKey: string; movement: 'replace' | 'append' | 'prepend'; jump?: 'start' | 'end'; anchor: { key: string; offsetPx: number } | null }>();
  let restore: { key: string; offsetPx: number; alignEnd?: boolean } | null = null;
  const [loadingWindow, setLoadingWindow] = createSignal(false, { ownedWrite: true });
  let captured: { key: string; offsetPx: number } | null = null;
  let last: unknown = null;
  let windowPages: ResourceItemsPage[] = [];
  let retainedPosition: ResourceSortKey | null = null;
  const requestKey = createMemo(() => preparedKey('project_resource_items', props.projectId, request()));
  const result = usePreparedOp(
    'project_resource_items',
    () => ({ projectId: props.projectId, ...request() }),
    {
      read: async ({ signal }) => {
        const req = untrack(request);
        const rk = untrack(requestKey);
        const pid = untrack(() => props.projectId);
        const cat = untrack(() => props.category);
        const grp = untrack(() => props.group);
        const first = firstRequest !== rk;
        const context = { requestKey: rk, movement: first ? movement : 'replace' as const, jump: first ? req.anchor : undefined, anchor: null as { key: string; offsetPx: number } | null };
        const anchor = handle.current?.getAnchor() ?? null; if (anchor) captured = anchor;
        const item = anchor ? frameRows.find(row => resourceItemKey(row) === anchor.key) : null;
        const position = item ? resourcePosition(item) : retainedPosition;
        if (position) retainedPosition = position;
        context.anchor = captured;
        const base = { projectId: pid, category: cat, group: grp, ...(cat === 'tools' ? { query: req.query ?? '' } : {}), limit: 50 };
        let next: ProjectResourceItemsRequest = first ? { ...base, ...req } : position ? { ...base, around: { key: position, direction: 'atOrAfter' } } : { ...base, anchor: 'start' };
        let value;
        try { value = await op('project_resource_items', next, { signal }); }
        catch (e) { if (!(e instanceof OpError) || e.status !== 409) throw e; next = position ? { ...base, around: { key: position, direction: 'atOrAfter' } } : { ...base, anchor: 'start' }; value = await op('project_resource_items', next, { signal }); }
        responseContext.set(value, context); return value;
      },
    },
  );
  createEffect(
    () => [result.data, result.state] as const,
    ([value, state]) => {
      if (state === 'preparing') {
        windowPages = []; last = null; setLoadingWindow(false);
        frameRows = []; framePage = null;
        setRows([]); setPage(null); restore = { key: '', offsetPx: 0 }; return;
      }
      if (!value || !('items' in value) || last === value) return;
      last = value;
      const context = responseContext.get(value);
      // A preparing/error reply must not consume the requested jump or edge transition.
      if (context) firstRequest = context.requestKey;
      const direction = context?.movement ?? 'replace';
      const sameEpoch = !framePage || framePage.version.split(':')[0] === value.version.split(':')[0];
      const segments = sameEpoch && direction === 'append' ? [...windowPages, value] : sameEpoch && direction === 'prepend' ? [value, ...windowPages] : [value];
      windowPages = direction === 'prepend' ? segments.slice(0, 3) : segments.slice(-3);
      const bounded = [...new Map(windowPages.flatMap(segment => segment.items).map(item => [resourceItemKey(item), item])).values()];
      // Cursors belong to the actual retained edges, not the most recently fetched page.
      const edges = { previousCursor: windowPages[0]?.previousCursor ?? null, nextCursor: windowPages.at(-1)?.nextCursor ?? null };
      const a = context?.anchor;
      if (context?.jump === 'end' && bounded.length) restore = { key: resourceItemKey(bounded.at(-1)!), offsetPx: 0, alignEnd: true };
      else if (context?.jump === 'start') restore = { key: bounded.length ? resourceItemKey(bounded[0]) : '', offsetPx: 0 };
      else if (a && bounded.some(row => resourceItemKey(row) === a.key)) restore = a;
      else if (value.resolvedAnchor) restore = { key: resourcePositionKey(value.resolvedAnchor), offsetPx: 0 };
      else restore = { key: '', offsetPx: 0 };
      const nextPage = { ...value, ...edges };
      frameRows = bounded; framePage = nextPage;
      setRows(bounded); setPage(nextPage);
    },
  );
  createEffect(
    () => rows(),
    () => {
      if (restore) {
        if (restore.alignEnd) handle.current?.scrollToKey(restore.key, 'end');
        else handle.current?.restoreAnchor(restore.key, restore.offsetPx);
        restore = null;
        setLoadingWindow(false);
      }
    },
  );
  const move = (position: Pick<ProjectResourceItemsRequest, 'anchor' | 'cursor'>, kind: typeof movement = 'replace') => {
    if (position.anchor) { retainedPosition = null; captured = null; }
    setLoadingWindow(true); firstRequest = null; movement = kind;
    const next = { category: props.category, group: props.group, query: props.category === 'tools' ? query() : undefined, limit: 50, ...position };
    if (preparedKey('project_resource_items', props.projectId, next) === requestKey()) { result.refresh('jump'); } else setRequest(next);
  };
  onSettled(() => {
    const r = restore;
    if (r) {
      if (r.alignEnd) handle.current?.scrollToKey(r.key, 'end');
      else handle.current?.restoreAnchor(r.key, r.offsetPx);
      restore = null;
      setLoadingWindow(false);
    }
  });
  return (
    <Show
      when={prices()}
      keyed
      fallback={
        <article
          class="detail conversation"
          data-ui="detail"
          data-resource-window={props.category}
          data-window-first={rows().length ? resourceItemKey(rows()[0]) : ''}
          data-window-last={rows().length ? resourceItemKey(rows().at(-1)!) : ''}
          data-resource-version={page()?.version ?? ''}
        >
          <header class="detail-h">
            <h2>{props.category === 'prices' ? 'Price basis' : props.category === 'tools' ? 'Tools' : 'Costs'}</h2>
            <span class="grow" />
            <Button label="Close resources" icon="x" iconOnly onClick={props.onClose} />
          </header>
          <ResourceProgressLine progress={result.progress} error={result.error} />
          <Show when={page()} keyed>{pg => <Totals items={preparedTotals(pg.header)} />}</Show>
          <div class="answers">
            <Show when={props.category === 'tools'}>
              <SearchField
                label="Search tools"
                value={query()}
                onInput={value => {
                  setLoadingWindow(true);
                  setQuery(value.slice(0, 200));
                  retainedPosition = null; captured = null; firstRequest = null; movement = 'replace';
                  setRequest({ category: props.category, query: value.slice(0, 200), limit: 50, anchor: 'start' });
                }}
                onSearch={() => {}}
              />
            </Show>
            <span>{rows().length} of {page()?.total ?? '—'}</span>
            <Button label="Jump to start" icon="arrow-up" iconOnly onClick={() => move({ anchor: 'start' })} />
            <Button label="Jump to end" icon="arrow-down" iconOnly onClick={() => move({ anchor: 'end' })} />
            <Show when={result.error}><Button label="Retry resources" icon="rotate-cw" onClick={() => result.refresh('retry')} /></Show>
          </div>
          <VirtualList
            fill
            handle={handle}
            items={rows()}
            getKey={resourceItemKey}
            label={`Project ${props.category}`}
            rowHeight={ROW2}
            follow="top"
            columns="1.25rem minmax(0,1fr) auto"
            onEnd={!loadingWindow() && page()?.nextCursor ? () => move({ cursor: page()!.nextCursor! }, 'append') : undefined}
            onStart={!loadingWindow() && page()?.previousCursor ? () => move({ cursor: page()!.previousCursor! }, 'prepend') : undefined}
            render={item => (
              <>
                <StateBadge family="source" state="current" compact />
                <Switch
                  fallback={
                    <>
                      <span><CopyButton label="Copy price id" value={(item as Extract<ResourceItem, { kind: 'price' }>).priceId} iconOnly /> Recorded rate</span>
                      <AgeChip t={(item as Extract<ResourceItem, { kind: 'price' }>).observedAt} />
                    </>
                  }
                >
                  <Match when={item.kind === 'tool' ? item : null} keyed>
                    {t => <><span>{t.name}</span><span class="num">{t.calls} · {t.errors} failed</span></>}
                  </Match>
                  <Match when={item.kind === 'cost' ? item : null} keyed>
                    {c => (
                      <>
                        <CostFigures cost={[{ ...c, reason: c.reason ?? undefined, note: c.note ?? undefined, usages: c.usages ?? undefined }]} showUnavailable />
                        <Button label={`Price basis · ${c.priceCount}`} onClick={() => setPrices({ currency: c.currency, basis: c.basis })} />
                      </>
                    )}
                  </Match>
                </Switch>
              </>
            )}
          />
          <Show when={!rows().length && page()}><Empty text="No matching confirmed items." /></Show>
        </article>
      }
    >
      {g => <PreparedResourceWindow projectId={props.projectId} category="prices" group={g} onClose={() => setPrices(null)} />}
    </Show>
  );
}
