// Spend breakdown and efficiency ("What it bought") screen (FLT-7, docs/designs/fleet-analytics.md).
import { useEffect, useMemo, useState } from 'preact/hooks';
import {
  Button, Choice, CostFigures, DataTable, DetailSection, Empty, EntityCode, ErrorBanner, FillBar, KvRow, Num,
  ShareBar, ShellStatus, OfflineMarker, Skeleton, SortColHead, count, money,
  type CostFigureLike, type DataTone, type SharePart,
} from '../../../packages/ui/src/index.ts';
import { scopeIds, prefsRead } from '../prefs.ts';
import { useOp } from '../store.ts';
import type { Ops } from '../api.ts';
import type { SpendGroupBy, SpendRow } from '../../../contracts/generated/ops.ts';

const WINDOWS = [
  { id: '1', label: '24h' },
  { id: '7', label: '7d' },
  { id: '30', label: '30d' },
  { id: '90', label: '90d' },
] as const;

const GROUP_OPTIONS: { id: SpendGroupBy; label: string }[] = [
  { id: 'purpose', label: 'Purpose' },
  { id: 'project', label: 'Project' },
  { id: 'category', label: 'Category' },
  { id: 'profile', label: 'Agent' },
  { id: 'model', label: 'Model' },
  { id: 'host', label: 'Host' },
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

  const labelFor = (row: SpendRow, dim: SpendGroupBy) =>
    dim === 'purpose' ? (PURPOSE_LABELS[row.key] ?? row.label) : row.label;

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
        <Choice label="Window" value={days} options={[...WINDOWS]} onChange={setDays} />
        <Choice label="Breakdown" value={by} options={GROUP_OPTIONS} onChange={v => setBy(v as SpendGroupBy)} />
        <span class="grow" />
        <Button label="Refresh" icon="rotate-ccw" iconOnly kind="quiet" onClick={reload} />
      </header>

      {error && <ErrorBanner text={error} onRetry={reload} />}
      {!data && !error && <Skeleton rows={6} />}

      {data && <>
        <DetailSection title={`Window total (${bounds.from} to ${bounds.to})`}>
          <KvRow icon="gauge" label="Total spend" value={totalCost.length ? <CostFigures cost={totalCost} /> : <span class="muted">No priced usage</span>}
            meta={`${count(totalRuns).text} runs · ${count(totalFailed).text} failed · ${count(totalTokens).text} tokens`} />
          {totalUnpriced > 0 && <KvRow icon="circle-alert" label="Unpriced leaves" meta="Some runs had missing pricing or usage figures" value={<Num m={{ value: totalUnpriced, basis: 'measured' }} kind="count" />} />}
          {data.hidden > 0 && <KvRow icon="shield" label="Outside access scope" meta="Leaf executions excluded by project access" value={<span class="mono">{count(data.hidden).text} hidden</span>} />}
        </DetailSection>

        {purposeParts.length > 0 && (
          <section class="panel">
            <ShareBar
              label="Spend by purpose"
              title="By purpose"
              parts={purposeParts}
              value={v => useUsdShare ? money(v, 'USD').text : count(Math.round(v)).text}
              note={useUsdShare ? 'USD spend across purpose' : 'Run counts (no USD pricing in window)'}
            />
          </section>
        )}

        <section class="panel">
          {sortedRows.length ? (
            <DataTable
              label={`Spend breakdown by ${by}`}
              head={
                <tr>
                  <SortColHead label={GROUP_OPTIONS.find(g => g.id === by)?.label ?? by} active={sortCol === 'label'} dir={sortDir} onSort={() => toggleSort('label')} />
                  <SortColHead label="Runs" active={sortCol === 'runs'} dir={sortDir} numeric onSort={() => toggleSort('runs')} />
                  <SortColHead label="Failed" active={sortCol === 'failed'} dir={sortDir} numeric onSort={() => toggleSort('failed')} />
                  <SortColHead label="Tokens" active={sortCol === 'tokens'} dir={sortDir} numeric onSort={() => toggleSort('tokens')} />
                  <SortColHead label="Cost" active={sortCol === 'usd'} dir={sortDir} numeric onSort={() => toggleSort('usd')} />
                  <th scope="col">Share</th>
                  <SortColHead label="Unpriced" active={sortCol === 'unpriced'} dir={sortDir} numeric onSort={() => toggleSort('unpriced')} />
                </tr>
              }>
              {sortedRows.map(r => (
                <tr key={r.key}>
                  <th scope="row">
                    {by === 'project' && r.key && r.key !== 'unknown' && r.key !== '-'
                      ? <EntityCode code={r.label || r.key} id={r.key} kind="project" />
                      : labelFor(r, by)}
                  </th>
                  <td class="num"><Num m={{ value: r.runs, basis: 'measured' }} kind="count" /></td>
                  <td class="num"><Num m={{ value: r.failed, basis: 'measured' }} kind="count" /></td>
                  <td class="num"><Num m={{ value: r.tokens, basis: 'measured' }} kind="count" /></td>
                  <td class="num">{r.cost.length ? <CostFigures cost={r.cost as CostFigureLike[]} /> : '—'}</td>
                  <td>
                    {r.share != null
                      ? <span class="limit-cell"><FillBar total={100} segments={[{ label: labelFor(r, by), bytes: Math.min(100, Math.max(0, Math.round(r.share * 100))) }]} unit="percent" /></span>
                      : '—'}
                  </td>
                  <td class="num">{r.unpriced > 0 ? <Num m={{ value: r.unpriced, basis: 'measured' }} kind="count" /> : ''}</td>
                </tr>
              ))}
            </DataTable>
          ) : <Empty text="No execution activity in this window." />}
        </section>

        <DetailSection title="What it bought">
          <KvRow icon="circle-check" label="Completed tasks" value={<Num m={{ value: data.bought.completedTasks, basis: 'measured' }} kind="count" />}
            meta={data.bought.taskCost.length ? <CostFigures cost={data.bought.taskCost as CostFigureLike[]} /> : 'No priced task spend'} />
          <KvRow icon="messages-square" label="Conversation runs" value={<Num m={{ value: data.bought.conversationRuns, basis: 'measured' }} kind="count" />}
            meta={data.bought.conversationCost.length ? <CostFigures cost={data.bought.conversationCost as CostFigureLike[]} /> : 'No priced conversation spend'} />
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
                  <td class="num">{p.taskCost.length ? <CostFigures cost={p.taskCost as CostFigureLike[]} /> : '—'}</td>
                  <td class="num">
                    {p.costPerTaskUsd != null
                      ? <Num m={{ value: p.costPerTaskUsd, basis: 'estimated', currency: 'USD' }} kind="money" />
                      : '—'}
                  </td>
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
