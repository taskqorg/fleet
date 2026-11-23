// Spend breakdown and efficiency ("What it bought") screen (FLT-7, docs/designs/fleet-analytics.md).
import { useEffect, useMemo, useState } from 'preact/hooks';
import {
  Button, Choice, CostFigures, DataTable, DetailSection, Empty, EntityCode, ErrorBanner, GroupControl, KvRow, Num,
  ShareBar, ShellStatus, OfflineMarker, Skeleton, SortColHead, Totals, count, measured, money, percent,
  type CostFigureLike, type DataTone,
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
const SHARE_BASES = [
  { id: 'billed', label: 'Billed USD' },
  { id: 'subscription', label: 'Estimated subscription value' },
] as const;
type ShareBasis = typeof SHARE_BASES[number]['id'];

type SortCol = 'label' | 'runs' | 'failed' | 'tokens' | 'usd' | 'unpriced';

const usdOf = (cost: readonly CostFigureLike[]) =>
  cost.reduce((sum, c) => (c.basis !== 'unavailable' && c.currency === 'USD' && typeof c.value === 'number' ? sum + c.value : sum), 0);

function matchesShareBasis(figure: CostFigureLike, basis: ShareBasis): boolean {
  return figure.currency === 'USD' && (basis === 'subscription'
    ? figure.basis === 'subscription'
    : figure.basis === 'measured' || figure.basis === 'estimated');
}

function shareAmount(cost: readonly CostFigureLike[], basis: ShareBasis): number | null {
  let amount: number | null = null;
  for (const figure of cost) {
    if (figure.value != null && matchesShareBasis(figure, basis)) amount = (amount ?? 0) + figure.value;
  }
  return amount;
}

function shareText(share: number): string {
  if (share > 0 && share < 0.005) return '<1%';
  if (share >= 0.995 && share < 1) return '>99%';
  return percent(share, 1).text;
}

function sameAmount(amount: number, total: number, terms = 4): boolean {
  // Relative roundoff from accumulating the reported amounts and adding the cost figures.
  return Math.abs(amount - total) <= Math.abs(total) * Number.EPSILON * terms;
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

function ShareCell({ label, amount, total }: { label: string; amount: number | null; total: number | null }) {
  if (total == null || total <= 0 || !Number.isFinite(total)) {
    return <UnavailableCell label={label} reason="no positive USD total for this basis in the selected period and scope" />;
  }
  if (amount == null) {
    return <UnavailableCell label={label} reason="no USD amount is available for this row and basis" />;
  }
  if (amount < 0) {
    return <UnavailableCell label={label} reason="the recorded amount is negative" />;
  }
  if (amount > total && !sameAmount(amount, total)) {
    return <UnavailableCell label={label} reason="the recorded amount exceeds the selected period and scope total" />;
  }
  return <td class="num">{shareText(amount / total)}</td>;
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

  const sortedRows = useMemo(() => sortRows(data?.rows ?? []), [data, sortCol, sortDir]);

  const totalCost = (data?.totals.cost ?? []) as CostFigureLike[];
  const shareBases = SHARE_BASES.map(basis => ({ ...basis, total: shareAmount(totalCost, basis.id) }));
  const adjustedBases = data ? shareBases.filter(({ id, total }) =>
    total != null && total > 0 && Number.isFinite(total)
    && [data.rows, data.byPurpose].some(rows => rows.some(row => {
      const amount = shareAmount(row.cost as CostFigureLike[], id);
      return amount != null && amount < 0;
    }))) : [];
  const totalRuns = data ? measureNum(data.totals.measures, 'executions') : 0;
  const totalFailed = data ? measureNum(data.totals.measures, 'failed') : 0;
  const totalTokens = data ? measureNum(data.totals.measures, 'tokens_input') + measureNum(data.totals.measures, 'tokens_output') : 0;
  const totalUnpriced = data ? data.rows.reduce((s, r) => s + r.unpriced, 0) : 0;

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
        <p class="small-note">Each share uses its own USD total for this period and scope. Billed USD includes measured charges and tariff estimates. Subscription value is a list-price estimate.</p>
        {adjustedBases.map(({ id, label }) => (
          <p key={id} class="small-note">Refunds and other negative adjustments reduce the {label} total. Positive shares can add up to more than 100%.</p>
        ))}
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
                  {shareBases.map(({ id, label }) => <th key={id} scope="col" class="num">{label} share</th>)}
                  <SortColHead label="Runs" active={sortCol === 'runs'} dir={sortDir} numeric onSort={() => toggleSort('runs')} />
                  <SortColHead label="Failed" active={sortCol === 'failed'} dir={sortDir} numeric onSort={() => toggleSort('failed')} />
                  <SortColHead label="Tokens" active={sortCol === 'tokens'} dir={sortDir} numeric onSort={() => toggleSort('tokens')} />
                  <SortColHead label="Unpriced" active={sortCol === 'unpriced'} dir={sortDir} numeric onSort={() => toggleSort('unpriced')} />
                </tr>
              }>
              {sortedRows.map(r => {
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
                    {shareBases.map(({ id, label, total }) =>
                      <ShareCell key={id} label={`${label} share`} amount={shareAmount(cost, id)} total={total} />)}
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

        {data.byPurpose.length > 0 && (
          <DetailSection title="By purpose">
            <div class="stack">
              {shareBases.map(({ id, label, total }) => {
                const parts = data.byPurpose.flatMap((row, i) => {
                  const amount = shareAmount(row.cost as CostFigureLike[], id);
                  return amount == null ? [] : [{ label: PURPOSE_LABELS[row.key] ?? row.label, v: amount, tone: TONES[i % TONES.length] }];
                });
                if (!parts.length) {
                  return <p key={id} class="small-note">{label} share unavailable. No USD amounts by purpose.</p>;
                }
                const sum = parts.reduce((amount, part) => amount + part.v, 0);
                const canDraw = total != null && total > 0 && Number.isFinite(total)
                  && sameAmount(sum, total, parts.length + 4) && parts.every(part =>
                    part.v >= 0 && (part.v <= total || sameAmount(part.v, total)));
                if (!canDraw) {
                  return <div key={id} class="stack">
                    <b>{label}</b>
                    <p class="small-note">Shares use the selected period and scope total. A bar requires nonnegative purpose amounts that add up to that total.</p>
                    <DataTable label={`${label} by purpose`} head={<tr>
                      <th scope="col">Purpose</th><th scope="col" class="num">Amount</th><th scope="col" class="num">Share</th>
                    </tr>}>
                      {parts.map(part => <tr key={part.label}>
                        <th scope="row">{part.label}</th><td class="num">{money(part.v, 'USD').text}</td>
                        <ShareCell label={`${label} share`} amount={part.v} total={total} />
                      </tr>)}
                    </DataTable>
                  </div>;
                }
                return <div key={id} class="panel">
                  <ShareBar label={`${label} by purpose`} title={label} parts={parts}
                    value={v => money(v, 'USD').text} note="Rounded shares" />
                </div>;
              })}
            </div>
          </DetailSection>
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
