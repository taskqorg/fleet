// Spend breakdown and efficiency ("What it bought") screen (FLT-7, docs/designs/fleet-analytics.md).
import { useEffect, useMemo, useState } from 'preact/hooks';
import {
  Button, Choice, CostFigures, DataTable, DetailSection, Empty, EntityCode, ErrorBanner, GroupControl, KvRow, Num,
  ShareBar, ShellStatus, OfflineMarker, Skeleton, SortColHead, Totals, count, measured, money, percent,
  type CostFigureLike, type DataTone, type SharePart,
} from '../../../packages/ui/src/index.ts';
import { scopeIds, prefsRead } from '../prefs.ts';
import { useOp } from '../store.ts';
import type { Ops } from '../api.ts';
import type { SpendGroupBy, SpendRow } from '../../../contracts/generated/ops.ts';

const WINDOWS = [
  { id: '1', label: 'UTC day' },
  { id: '7', label: '7d' },
  { id: '30', label: '30d' },
  { id: '90', label: '90d' },
] as const;

const GROUP_OPTIONS: readonly (readonly [SpendGroupBy, string])[] = [
  ['purpose', 'Purpose'],
  ['project', 'Project'],
  ['category', 'Category'],
  ['profile', 'Agent'],
  ['model', 'Model'],
  ['host', 'Host'],
];

const PURPOSE_LABELS: Record<string, string> = {
  task: 'Tasks',
  lab: 'Lab experiments',
  conversation: 'Conversations',
  system: 'System maintenance',
  other: 'Other',
};

const TONES: DataTone[] = ['d1', 'd2', 'd3', 'd4'];

type SortCol = 'label' | 'runs' | 'failed' | 'tokens' | 'usd' | 'unpriced';

const usdOf = (cost: readonly CostFigureLike[]) =>
  cost.reduce((sum, c) => (c.basis !== 'unavailable' && c.currency === 'USD' && typeof c.value === 'number' ? sum + c.value : sum), 0);

function spendShare(row: SpendRow, hasUsdTotal: boolean): number | null {
  if (row.share != null) return row.share;
  // The operation omits shares for zero USD as well as for unavailable USD.
  const hasUsdValue = row.cost.some(c => c.basis !== 'unavailable' && c.currency === 'USD' && typeof c.value === 'number');
  return hasUsdTotal && hasUsdValue && usdOf(row.cost as CostFigureLike[]) === 0 ? 0 : null;
}

function shareText(share: number): string {
  if (share > 0 && share < 0.005) return '<1%';
  if (share >= 0.995 && share < 1) return '>99%';
  return percent(share, 1).text;
}

function missingCostReason(cost: readonly CostFigureLike[]) {
  const figure = cost.find(c => (c.basis === 'unavailable' || c.value == null) && (c.reason || c.note));
  return figure?.reason || figure?.note;
}

function UnavailableCell({ label, reason }: { label: string; reason?: string }) {
  const value = measured({ value: null, basis: 'unavailable', reason }, 'money');
  const description = `${label} ${value.long}`;
  return <td class="num b-unavailable" aria-label={description} data-tip={description} tabIndex={0}>{value.text}</td>;
}

function CostCell({ cost, label }: { cost: readonly CostFigureLike[]; label: string }) {
  if (cost.some(c => c.value != null && c.basis !== 'unavailable')) {
    return <td class="num"><CostFigures cost={cost} /></td>;
  }
  return <UnavailableCell label={label} reason={missingCostReason(cost)} />;
}

function windowBounds(days: number): { from: string; to: string } {
  const now = new Date();
  const to = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + 1));
  const from = new Date(to.getTime() - days * 86_400_000);
  return { from: from.toISOString().slice(0, 10), to: to.toISOString().slice(0, 10) };
}

function measureNum(totals: Ops['spend_summary']['reply']['totals']['measures'], key: string): number {
  const m = totals[key];
  return m && typeof m.value === 'number' ? m.value : 0;
}

export function Spend() {
  const [days, setDays] = useState('30');
  const [by, setBy] = useState<SpendGroupBy>('purpose');
  const [sortCol, setSortCol] = useState<SortCol>('usd');
  const [sortDir, setSortDir] = useState<'ascending' | 'descending'>('descending');
  const [multiSupported, setMultiSupported] = useState(true);

  const bounds = useMemo(() => windowBounds(Number(days) || 30), [days]);
  const lastDate = new Date(Date.parse(bounds.to) - 86_400_000).toISOString().slice(0, 10);
  const periodDates = bounds.from === lastDate ? bounds.from : `${bounds.from} to ${lastDate}`;
  const scope = scopeIds();
  const singleProject = scope.projectIds?.length === 1 && scope.projectIds[0] !== '-' ? scope.projectIds[0] : undefined;
  const hasMultiScope = !singleProject && !!(scope.projectIds?.length || scope.excludeProjectIds?.length);
  const extraScope = {
    ...(scope.projectIds?.length ? { projectIds: scope.projectIds } : {}),
    ...(scope.excludeProjectIds?.length ? { excludeProjectIds: scope.excludeProjectIds } : {}),
  };

  const req: Ops['spend_summary']['request'] = {
    window: bounds,
    by,
    ...(singleProject
      ? { projectId: singleProject }
      : multiSupported && hasMultiScope
        ? (extraScope as unknown as Partial<Ops['spend_summary']['request']>)
        : {}),
  };
  const { data, error, status, state, reload } = useOp('spend_summary', prefsRead.value ? req : null, { quiet: 30_000 });

  useEffect(() => {
    if (hasMultiScope && multiSupported && status === 400 && error && /unknown field/i.test(error)) {
      setMultiSupported(false);
    }
  }, [hasMultiScope, multiSupported, status, error]);

  const sortRows = (rows: readonly SpendRow[]) => {
    const dir = sortDir === 'ascending' ? 1 : -1;
    return rows.toSorted((a, b) => {
      if (sortCol === 'label') return dir * a.label.localeCompare(b.label);
      if (sortCol === 'runs') return dir * (a.runs - b.runs);
      if (sortCol === 'failed') return dir * (a.failed - b.failed);
      if (sortCol === 'tokens') return dir * (a.tokens - b.tokens);
      if (sortCol === 'unpriced') return dir * (a.unpriced - b.unpriced);
      return dir * (usdOf(a.cost as CostFigureLike[]) - usdOf(b.cost as CostFigureLike[]));
    });
  };

  const toggleSort = (col: SortCol) => {
    if (sortCol === col) setSortDir(d => d === 'ascending' ? 'descending' : 'ascending');
    else { setSortCol(col); setSortDir(col === 'label' ? 'ascending' : 'descending'); }
  };

  const purposeParts: SharePart[] = useMemo(() => {
    if (!data) return [];
    const totalUsd = usdOf(data.totals.cost as CostFigureLike[]);
    const useCost = totalUsd > 0;
    return data.byPurpose.map((r, i) => ({
      label: PURPOSE_LABELS[r.key] ?? r.label,
      v: useCost ? usdOf(r.cost as CostFigureLike[]) : r.runs,
      tone: TONES[i % TONES.length],
    }));
  }, [data]);

  const sortedRows = useMemo(() => sortRows(data?.rows ?? []), [data, sortCol, sortDir]);

  const totalCost = (data?.totals.cost ?? []) as CostFigureLike[];
  const totalRuns = data ? measureNum(data.totals.measures, 'executions') : 0;
  const totalFailed = data ? measureNum(data.totals.measures, 'failed') : 0;
  const totalTokens = data ? measureNum(data.totals.measures, 'tokens_input') + measureNum(data.totals.measures, 'tokens_output') : 0;
  const totalUnpriced = data ? data.rows.reduce((s, r) => s + r.unpriced, 0) : 0;
  const useUsdShare = usdOf(totalCost) > 0;

  return (
    <article class="detail stack" aria-label="Spend">
      <ShellStatus><OfflineMarker state={state} now={Date.now()} /></ShellStatus>
      <header class="page-h">
        <b>Period</b>
        <Choice label="Period" value={days} options={[...WINDOWS]} onChange={setDays} />
        <span class="muted">{periodDates} UTC</span>
        <span class="grow" />
        <Button label="Refresh" icon="rotate-ccw" iconOnly kind="quiet" onClick={reload} />
      </header>

      {error && <ErrorBanner text={error} onRetry={reload} />}
      {!data && !error && <Skeleton rows={6} />}

      {data && (
        <DetailSection title="Total spend">
          <strong><CostFigures cost={totalCost} empty="No priced usage" /></strong>
          <p class="small-note">Amounts marked ≈ are estimates, including subscription list-price equivalents.</p>
          <Totals compact items={[
            { label: 'Runs', kind: 'count', m: { value: totalRuns, basis: 'measured' } },
            { label: 'Failed', kind: 'count', m: { value: totalFailed, basis: 'measured' } },
            { label: 'Tokens', kind: 'count', m: { value: totalTokens, basis: 'measured' } },
            ...(totalUnpriced > 0 ? [{
              label: 'Unpriced usage', kind: 'count' as const, m: { value: totalUnpriced, basis: 'measured' as const },
            }] : []),
          ]} />
          {totalUnpriced > 0 && <p class="small-note">Unpriced usage is excluded from dollar figures because a usage amount or price entry is missing.</p>}
          {data.hidden > 0 && <KvRow icon="shield" label="Outside access scope" meta="Leaf executions excluded by project access" value={<span class="mono">{count(data.hidden).text} hidden</span>} />}
        </DetailSection>

      )}
      <div class="page-h">
        <b>Breakdown</b>
        <GroupControl value={by} options={GROUP_OPTIONS} onChange={setBy} />
      </div>
      {data && <>
        <p class="small-note">Share is based on available USD spend for this period.</p>
        {by === 'category' && data.rows.some(r => r.key === 'unknown') &&
          <p class="small-note">Unknown category means no category is recorded for this usage.</p>}
        <section class="panel">
          {sortedRows.length ? (
            <DataTable
              label={`Spend breakdown by ${by}`}
              head={
                <tr>
                  <SortColHead label={GROUP_OPTIONS.find(([id]) => id === by)?.[1] ?? by} active={sortCol === 'label'} dir={sortDir} onSort={() => toggleSort('label')} />
                  <SortColHead label="Cost" active={sortCol === 'usd'} dir={sortDir} numeric onSort={() => toggleSort('usd')} />
                  <th scope="col" class="num">Share of spend</th>
                  <SortColHead label="Runs" active={sortCol === 'runs'} dir={sortDir} numeric onSort={() => toggleSort('runs')} />
                  <SortColHead label="Failed" active={sortCol === 'failed'} dir={sortDir} numeric onSort={() => toggleSort('failed')} />
                  <SortColHead label="Tokens" active={sortCol === 'tokens'} dir={sortDir} numeric onSort={() => toggleSort('tokens')} />
                  <SortColHead label="Unpriced" active={sortCol === 'unpriced'} dir={sortDir} numeric onSort={() => toggleSort('unpriced')} />
                </tr>
              }>
              {sortedRows.map(r => {
                const share = spendShare(r, useUsdShare);
                const cost = r.cost as CostFigureLike[];
                const label = by === 'purpose' ? PURPOSE_LABELS[r.key] ?? r.label
                  : by === 'category' && r.key === 'unknown' ? 'Unknown category' : r.label;
                return (
                  <tr key={r.key}>
                    <th scope="row">
                      {by === 'project' && r.key && r.key !== 'unknown' && r.key !== '-'
                        ? <EntityCode code={r.label || r.key} id={r.key} kind="project" />
                        : label}
                    </th>
                    <CostCell cost={cost} label="Cost" />
                    {share == null
                      ? <UnavailableCell label="Share of spend" reason={useUsdShare ? missingCostReason(cost) : 'no positive USD total in this period'} />
                      : <td class="num">{shareText(share)}</td>}
                    <td class="num"><Num m={{ value: r.runs, basis: 'measured' }} kind="count" /></td>
                    <td class="num"><Num m={{ value: r.failed, basis: 'measured' }} kind="count" /></td>
                    <td class="num"><Num m={{ value: r.tokens, basis: 'measured' }} kind="count" /></td>
                    <td class="num">{r.unpriced > 0 ? <Num m={{ value: r.unpriced, basis: 'measured' }} kind="count" /> : ''}</td>
                  </tr>
                );
              })}
            </DataTable>
          ) : <Empty text="No execution activity in this window." />}
        </section>

        {purposeParts.length > 0 && (
          <section class="panel">
            <ShareBar
              label="Spend by purpose"
              title="By purpose"
              parts={purposeParts}
              value={v => useUsdShare ? money(v, 'USD').text : count(Math.round(v)).text}
              note={useUsdShare ? 'USD spend by purpose · rounded shares' : 'Run counts (no positive USD total) · rounded shares'}
            />
          </section>
        )}

        <DetailSection title="What it bought">
          <KvRow icon="circle-check" label="Completed tasks" value={<Num m={{ value: data.bought.completedTasks, basis: 'measured' }} kind="count" />}
            meta={<CostFigures cost={data.bought.taskCost as CostFigureLike[]} empty="No priced task spend" />} />
          <KvRow icon="messages-square" label="Conversation runs" value={<Num m={{ value: data.bought.conversationRuns, basis: 'measured' }} kind="count" />}
            meta={<CostFigures cost={data.bought.conversationCost as CostFigureLike[]} empty="No priced conversation spend" />} />
          {data.bought.byProject.length > 0 && (
            <DataTable
              label="Completed tasks and cost per task by project"
              head={
                <tr>
                  <th scope="col">Project</th>
                  <th scope="col" class="num">Completed tasks</th>
                  <th scope="col" class="num">Task spend</th>
                  <th scope="col" class="num">Cost / task</th>
                  <th scope="col" class="num">Unpriced</th>
                </tr>
              }>
              {data.bought.byProject.map(p => (
                <tr key={p.projectId}>
                  <th scope="row">
                    {p.projectId && p.projectId !== 'unknown' && p.projectId !== '-'
                      ? <EntityCode code={p.projectId} id={p.projectId} kind="project" />
                      : 'No project'}
                  </th>
                  <td class="num"><Num m={{ value: p.completedTasks, basis: 'measured' }} kind="count" /></td>
                  <CostCell cost={p.taskCost as CostFigureLike[]} label="Task spend" />
                  {p.costPerTaskUsd != null
                    ? <td class="num"><Num m={{ value: p.costPerTaskUsd, basis: 'estimated', currency: 'USD' }} kind="money" /></td>
                    : <UnavailableCell label="Cost / task" reason={p.completedTasks === 0
                      ? 'no completed tasks'
                      : missingCostReason(p.taskCost as CostFigureLike[]) ?? 'no positive USD task spend'} />}
                  <td class="num">{p.unpriced > 0 ? <Num m={{ value: p.unpriced, basis: 'measured' }} kind="count" /> : ''}</td>
                </tr>
              ))}
            </DataTable>
          )}
        </DetailSection>
      </>}
    </article>
  );
}
