// Shared resource rollup and recursive execution tree helpers (docs/design-system/pages/{tasks,conversations,projects,definitions,runners,hosts}.md).
import { useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  Button, CostFigures, DetailSection, EntityCode, Icon, Num, StateBadge, Totals,
  msMeasured, numMeasured, splitCostFigures,
  type CostFigureLike, type Measured, type StateOf, type TotalItem, type TreeNode,
} from '../../../packages/ui/src/index.ts';
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
              <EntityCode code={st.code} id={st.taskId} />
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
    return a && b && b >= a ? { value: (b - a) / 1000, basis: 'measured' as const } : { value: null, basis: 'unavailable' as const, reason: 'not finished' };
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
