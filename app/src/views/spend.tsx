// Spend breakdown and efficiency ("What it bought") screen (FLT-7, docs/designs/fleet-analytics.md).
import { For, Match, Show, Switch, createEffect, createMemo, createSignal } from 'solid-js';
import {
  Button, Choice, CostFigures, DataTable, DetailSection, Empty, EntityCode, ErrorBanner, FigureTile, GroupControl, KvRow, Num,
  ShareBar, ShellStatus, OfflineMarker, Skeleton, SortColHead, count, measured, money, percent,
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
  ['bot', 'Bot'],
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

export const usdOf = (cost: readonly CostFigureLike[]) =>
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

function UnavailableCell(props: { label: string; reason?: string }) {
  const value = createMemo(() => measured({ value: null, basis: 'unavailable', reason: props.reason }, 'money'));
  const description = () => `${props.label} ${value().long}`;
  return <td class="num b-unavailable" aria-label={description()} data-tip={description()} tabindex={0}>{value().text}</td>;
}

function ShareCell(props: { label: string; amount: number | null; total: number | null }) {
  return (
    <Switch fallback={<td class="num">{shareText(props.amount! / props.total!)}</td>}>
      <Match when={props.total == null || props.total <= 0 || !Number.isFinite(props.total)}>
        <UnavailableCell label={props.label} reason="no positive USD total for this basis in the selected period and scope" />
      </Match>
      <Match when={props.amount == null}>
        <UnavailableCell label={props.label} reason="no USD amount is available for this row and basis" />
      </Match>
      <Match when={props.amount! < 0}>
        <UnavailableCell label={props.label} reason="the recorded amount is negative" />
      </Match>
      <Match when={props.amount! > props.total! && !sameAmount(props.amount!, props.total!)}>
        <UnavailableCell label={props.label} reason="the recorded amount exceeds the selected period and scope total" />
      </Match>
    </Switch>
  );
}

function CostCell(props: { cost: readonly CostFigureLike[]; label: string }) {
  return (
    <Show
      when={props.cost.some(c => c.value != null && c.basis !== 'unavailable')}
      fallback={<UnavailableCell label={props.label} reason={missingCostReason(props.cost)} />}
    >
      <td class="num"><CostFigures cost={props.cost} /></td>
    </Show>
  );
}

export function windowBounds(days: number): { from: string; to: string } {
  const now = new Date();
  const to = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + 1));
  const from = new Date(to.getTime() - days * 86_400_000);
  return { from: from.toISOString().slice(0, 10), to: to.toISOString().slice(0, 10) };
}

/** One value per UTC day of the last `days` days, oldest first, from a spend_summary by:'day' reply (row key YYYY-MM-DD):
 *  a covered day without runs is 0; no reply yet is an empty series (the tile shows no sparkline). */
export function dailySeries(reply: Ops['spend_summary']['reply'] | null | undefined, days: number,
  pick: (row: Ops['spend_summary']['reply']['rows'][number]) => number): number[] {
  if (!reply) return [];
  const byKey = new Map(reply.rows.map(r => [r.key, r]));
  const { to } = windowBounds(days);
  const end = Date.parse(to + 'T00:00:00Z');
  return Array.from({ length: days }, (_, i) => {
    const row = byKey.get(new Date(end - (days - i) * 86_400_000).toISOString().slice(0, 10));
    return row ? pick(row) : 0;
  });
}

function measureNum(totals: Ops['spend_summary']['reply']['totals']['measures'], key: string): number {
  const m = totals[key];
  return m && typeof m.value === 'number' ? m.value : 0;
}

/** The spend_summary request Spend sends for a window, in the person's focus (System's idle figures send the same, so
 *  they share one read with Spend). */
export function spendRequest(days: number, by: SpendGroupBy = 'purpose'): Ops['spend_summary']['request'] {
  const sc = scopeIds();
  const sp = sc.projectIds?.length === 1 && sc.projectIds[0] !== '-' ? sc.projectIds[0] : undefined;
  return {
    window: windowBounds(days), by,
    ...(sp ? { projectId: sp } : {
      ...(sc.projectIds?.length ? { projectIds: sc.projectIds } : {}),
      ...(sc.excludeProjectIds?.length ? { excludeProjectIds: sc.excludeProjectIds } : {}),
    } as unknown as Partial<Ops['spend_summary']['request']>),
  };
}

export function Spend() {
  const [days, setDays] = createSignal('30');
  const [by, setBy] = createSignal<SpendGroupBy>('purpose');
  const [sortCol, setSortCol] = createSignal<SortCol>('usd');
  const [sortDir, setSortDir] = createSignal<'ascending' | 'descending'>('descending');
  const [multiSupported, setMultiSupported] = createSignal(true, { ownedWrite: true });

  const bounds = createMemo(() => windowBounds(Number(days()) || 30));
  const periodDates = createMemo(() => {
    const b = bounds();
    const lastDate = new Date(Date.parse(b.to) - 86_400_000).toISOString().slice(0, 10);
    return b.from === lastDate ? b.from : `${b.from} to ${lastDate}`;
  });
  const scope = createMemo(() => scopeIds());
  const singleProject = createMemo(() => {
    const sc = scope();
    return sc.projectIds?.length === 1 && sc.projectIds[0] !== '-' ? sc.projectIds[0] : undefined;
  });
  const hasMultiScope = createMemo(() => {
    const sc = scope();
    return !singleProject() && !!(sc.projectIds?.length || sc.excludeProjectIds?.length);
  });
  const req = createMemo<Ops['spend_summary']['request']>(() => {
    const sc = scope();
    const sp = singleProject();
    const extraScope = {
      ...(sc.projectIds?.length ? { projectIds: sc.projectIds } : {}),
      ...(sc.excludeProjectIds?.length ? { excludeProjectIds: sc.excludeProjectIds } : {}),
    };
    return {
      window: bounds(),
      by: by(),
      ...(sp
        ? { projectId: sp }
        : multiSupported() && hasMultiScope()
          ? (extraScope as unknown as Partial<Ops['spend_summary']['request']>)
          : {}),
    };
  });

  const summary = useOp('spend_summary', () => (prefsRead.value ? req() : null), { quiet: 30_000 });

  createEffect(
    () => [hasMultiScope(), multiSupported(), summary.status, summary.error] as const,
    ([hms, ms, status, error]) => {
      if (hms && ms && status === 400 && error && /unknown field/i.test(error)) {
        setMultiSupported(false);
      }
    },
  );

  const toggleSort = (col: SortCol) => {
    if (sortCol() === col) setSortDir(d => (d === 'ascending' ? 'descending' : 'ascending'));
    else {
      setSortCol(col);
      setSortDir(col === 'label' ? 'ascending' : 'descending');
    }
  };

  const sortedRows = createMemo<readonly SpendRow[]>(() => {
    const rows = summary.data?.rows ?? [];
    const dir = sortDir() === 'ascending' ? 1 : -1;
    const col = sortCol();
    return rows.toSorted((a, b) => {
      if (col === 'label') return dir * a.label.localeCompare(b.label);
      if (col === 'runs') return dir * (a.runs - b.runs);
      if (col === 'failed') return dir * (a.failed - b.failed);
      if (col === 'tokens') return dir * (a.tokens - b.tokens);
      if (col === 'unpriced') return dir * (a.unpriced - b.unpriced);
      return dir * (usdOf(a.cost as CostFigureLike[]) - usdOf(b.cost as CostFigureLike[]));
    });
  });

  const totalCost = createMemo(() => (summary.data?.totals.cost ?? []) as CostFigureLike[]);
  const shareBases = createMemo(() => SHARE_BASES.map(basis => ({ ...basis, total: shareAmount(totalCost(), basis.id) })));
  const adjustedBases = createMemo(() => {
    const d = summary.data;
    if (!d) return [];
    return shareBases().filter(({ id, total }) =>
      total != null && total > 0 && Number.isFinite(total)
      && [d.rows, d.byPurpose].some(rows => rows.some(row => {
        const amount = shareAmount(row.cost as CostFigureLike[], id);
        return amount != null && amount < 0;
      })),
    );
  });
  const totalRuns = createMemo(() => (summary.data ? measureNum(summary.data.totals.measures, 'executions') : 0));
  const totalFailed = createMemo(() => (summary.data ? measureNum(summary.data.totals.measures, 'failed') : 0));
  const totalTokens = createMemo(() =>
    summary.data ? measureNum(summary.data.totals.measures, 'tokens_input') + measureNum(summary.data.totals.measures, 'tokens_output') : 0,
  );
  const totalUnpriced = createMemo(() => (summary.data ? summary.data.rows.reduce((s, r) => s + r.unpriced, 0) : 0));
  // One bar per day of the period (spend_summary by day, canvas board/screens/system.bl state 3); Tasks done has no daily read.
  const daily = useOp('spend_summary', () => (prefsRead.value ? spendRequest(Number(days()) || 30, 'day') : null), { quiet: 30_000 });
  const n = () => Number(days()) || 30;
  const costByDay = createMemo(() => dailySeries(daily.data, n(), r => usdOf(r.cost as CostFigureLike[])));
  const runsByDay = createMemo(() => dailySeries(daily.data, n(), r => r.runs));
  const unpricedByDay = createMemo(() => dailySeries(daily.data, n(), r => r.unpriced));

  return (
    <article class="detail stack" aria-label="Spend">
      <ShellStatus><OfflineMarker state={summary.state} now={Date.now()} /></ShellStatus>
      <header class="page-h">
        <b>Period</b>
        <Choice label="Period" value={days()} options={[...WINDOWS]} onChange={setDays} />
        <span class="muted">{periodDates()} UTC</span>
        <span class="grow" />
        <Button label="Refresh" icon="rotate-ccw" iconOnly kind="quiet" onClick={summary.reload} />
      </header>

      <Show when={summary.error}>{err => <ErrorBanner text={err()} onRetry={summary.reload} />}</Show>
      <Show when={!summary.data && !summary.error}><Skeleton rows={6} /></Show>

      <Show when={summary.data} keyed>
        {data => (
          <DetailSection title="Total spend">
            {/* Period and total first (spend.md; canvas board/screens/system.bl state 3): figures, then the breakdown. */}
            <div class="sys-figures">
              <FigureTile label={`Total, ${days() === '1' ? 'UTC day' : `${days()} days`}`} kind="money" value={{ value: usdOf(totalCost()), basis: 'measured' }}
                sub={[shareAmount(totalCost(), 'billed') != null ? `billed ${money(shareAmount(totalCost(), 'billed')!).text}` : '', shareAmount(totalCost(), 'subscription') != null ? `subscription value ${money(shareAmount(totalCost(), 'subscription')!).text}` : ''].filter(Boolean).join(' · ') || undefined}
                trend={costByDay()} trendLabel={`Spend by day, last ${n()} days`} />
              <FigureTile label="Runs" value={{ value: totalRuns(), basis: 'measured' }} sub={totalFailed() ? `${count(totalFailed()).text} failed · ${count(totalTokens()).text} tokens` : `${count(totalTokens()).text} tokens`}
                trend={runsByDay()} trendLabel={`Runs by day, last ${n()} days`} />
              <FigureTile label="Tasks done" value={{ value: data.bought.completedTasks, basis: 'measured' }}
                sub={data.bought.completedTasks ? `${money(usdOf(data.bought.taskCost as CostFigureLike[]) / data.bought.completedTasks).text} each` : undefined}
                trend={[]} trendLabel="Tasks done by day" />
              <Show when={totalUnpriced() > 0}>
                <FigureTile label="Unpriced usage" value={{ value: totalUnpriced(), basis: 'measured' }} sub="no price entry" alert trend={unpricedByDay()} trendLabel={`Unpriced usage by day, last ${n()} days`} />
              </Show>
            </div>
            <p class="small-note">Amounts marked ≈ are estimates, including subscription list-price equivalents: <CostFigures cost={totalCost()} empty="no priced usage" /></p>
            <Show when={totalUnpriced() > 0}>
              <p class="small-note">Unpriced usage is excluded from dollar figures because a usage amount or price entry is missing.</p>
            </Show>
            <Show when={data.hidden > 0}>
              <KvRow icon="shield" label="Outside access scope" meta="Leaf executions excluded by project access" value={<span class="mono">{count(data.hidden).text} hidden</span>} />
            </Show>
          </DetailSection>
        )}
      </Show>

      <div class="page-h">
        <b>Breakdown</b>
        <GroupControl value={by()} options={GROUP_OPTIONS} onChange={setBy} />
      </div>

      <Show when={summary.data} keyed>
        {data => (
          <>
            <p class="small-note">Each share uses its own USD total for this period and scope. Billed USD includes measured charges and tariff estimates. Subscription value is a list-price estimate.</p>
            <For each={adjustedBases()}>
              {b => (
                <p class="small-note">Refunds and other negative adjustments reduce the {b.label} total. Positive shares can add up to more than 100%.</p>
              )}
            </For>
            <Show when={by() === 'category' && data.rows.some(r => r.key === 'unknown')}>
              <p class="small-note">Unknown category means no category is recorded for this usage.</p>
            </Show>
            <section class="panel">
              <Show when={sortedRows().length > 0} fallback={<Empty text="No execution activity in this window." />}>
                <DataTable
                  label={`Spend breakdown by ${by()}`}
                  head={
                    <tr>
                      <SortColHead label={GROUP_OPTIONS.find(([id]) => id === by())?.[1] ?? by()} active={sortCol() === 'label'} dir={sortDir()} onSort={() => toggleSort('label')} />
                      <SortColHead label="Cost" active={sortCol() === 'usd'} dir={sortDir()} numeric onSort={() => toggleSort('usd')} />
                      <For each={shareBases()}>{b => <th scope="col" class="num">{b.label} share</th>}</For>
                      <SortColHead label="Runs" active={sortCol() === 'runs'} dir={sortDir()} numeric onSort={() => toggleSort('runs')} />
                      <SortColHead label="Failed" active={sortCol() === 'failed'} dir={sortDir()} numeric onSort={() => toggleSort('failed')} />
                      <SortColHead label="Tokens" active={sortCol() === 'tokens'} dir={sortDir()} numeric onSort={() => toggleSort('tokens')} />
                      <SortColHead label="Unpriced" active={sortCol() === 'unpriced'} dir={sortDir()} numeric onSort={() => toggleSort('unpriced')} />
                    </tr>
                  }
                >
                  <For each={sortedRows()}>
                    {r => {
                      const cost = r.cost as CostFigureLike[];
                      const label = () => (by() === 'purpose' ? PURPOSE_LABELS[r.key] ?? r.label : by() === 'category' && r.key === 'unknown' ? 'Unknown category' : r.label);
                      return (
                        <tr>
                          <th scope="row">
                            <Show
                              when={by() === 'project' && r.key && r.key !== 'unknown' && r.key !== '-'}
                              fallback={label()}
                            >
                              <EntityCode code={r.label || r.key} id={r.key} kind="project" />
                            </Show>
                          </th>
                          <CostCell cost={cost} label="Cost" />
                          <For each={shareBases()}>
                            {b => <ShareCell label={`${b.label} share`} amount={shareAmount(cost, b.id)} total={b.total} />}
                          </For>
                          <td class="num"><Num m={{ value: r.runs, basis: 'measured' }} kind="count" /></td>
                          <td class="num"><Num m={{ value: r.failed, basis: 'measured' }} kind="count" /></td>
                          <td class="num"><Num m={{ value: r.tokens, basis: 'measured' }} kind="count" /></td>
                          <td class="num">
                            <Show when={r.unpriced > 0} fallback="">
                              <Num m={{ value: r.unpriced, basis: 'measured' }} kind="count" />
                            </Show>
                          </td>
                        </tr>
                      );
                    }}
                  </For>
                </DataTable>
              </Show>
            </section>

            <Show when={data.byPurpose.length > 0}>
              <DetailSection title="By purpose">
                <div class="stack">
                  <For each={shareBases()}>
                    {b => {
                      const parts = data.byPurpose.flatMap((row, i) => {
                        const amount = shareAmount(row.cost as CostFigureLike[], b.id);
                        return amount == null ? [] : [{ label: PURPOSE_LABELS[row.key] ?? row.label, v: amount, tone: TONES[i % TONES.length] }];
                      });
                      const sum = parts.reduce((amount, part) => amount + part.v, 0);
                      const canDraw = b.total != null && b.total > 0 && Number.isFinite(b.total)
                        && sameAmount(sum, b.total, parts.length + 4) && parts.every(part =>
                          part.v >= 0 && (part.v <= b.total! || sameAmount(part.v, b.total!)));
                      return (
                        <Switch
                          fallback={
                            <div class="panel">
                              <ShareBar
                                label={`${b.label} by purpose`}
                                title={b.label}
                                parts={parts}
                                value={v => money(v, 'USD').text}
                                note="Rounded shares"
                              />
                            </div>
                          }
                        >
                          <Match when={!parts.length}>
                            <p class="small-note">{b.label} share unavailable. No USD amounts by purpose.</p>
                          </Match>
                          <Match when={!canDraw}>
                            <div class="stack">
                              <b>{b.label}</b>
                              <p class="small-note">Shares use the selected period and scope total. A bar requires nonnegative purpose amounts that add up to that total.</p>
                              <DataTable
                                label={`${b.label} by purpose`}
                                head={
                                  <tr>
                                    <th scope="col">Purpose</th>
                                    <th scope="col" class="num">Amount</th>
                                    <th scope="col" class="num">Share</th>
                                  </tr>
                                }
                              >
                                <For each={parts}>
                                  {part => (
                                    <tr>
                                      <th scope="row">{part.label}</th>
                                      <td class="num">{money(part.v, 'USD').text}</td>
                                      <ShareCell label={`${b.label} share`} amount={part.v} total={b.total} />
                                    </tr>
                                  )}
                                </For>
                              </DataTable>
                            </div>
                          </Match>
                        </Switch>
                      );
                    }}
                  </For>
                </div>
              </DetailSection>
            </Show>

            <DetailSection title="What it bought">
              <KvRow
                icon="circle-check"
                label="Completed tasks"
                value={<Num m={{ value: data.bought.completedTasks, basis: 'measured' }} kind="count" />}
                meta={<CostFigures cost={data.bought.taskCost as CostFigureLike[]} empty="No priced task spend" />}
              />
              <KvRow
                icon="messages-square"
                label="Conversation runs"
                value={<Num m={{ value: data.bought.conversationRuns, basis: 'measured' }} kind="count" />}
                meta={<CostFigures cost={data.bought.conversationCost as CostFigureLike[]} empty="No priced conversation spend" />}
              />
              <Show when={data.bought.byProject.length > 0}>
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
                  }
                >
                  <For each={data.bought.byProject}>
                    {p => (
                      <tr>
                        <th scope="row">
                          <Show
                            when={p.projectId && p.projectId !== 'unknown' && p.projectId !== '-'}
                            fallback="No project"
                          >
                            <EntityCode code={p.projectId} id={p.projectId} kind="project" />
                          </Show>
                        </th>
                        <td class="num"><Num m={{ value: p.completedTasks, basis: 'measured' }} kind="count" /></td>
                        <CostCell cost={p.taskCost as CostFigureLike[]} label="Task spend" />
                        <Show
                          when={p.costPerTaskUsd != null}
                          fallback={
                            <UnavailableCell
                              label="Cost / task"
                              reason={p.completedTasks === 0
                                ? 'no completed tasks'
                                : missingCostReason(p.taskCost as CostFigureLike[]) ?? 'no positive USD task spend'}
                            />
                          }
                        >
                          <td class="num"><Num m={{ value: p.costPerTaskUsd!, basis: 'estimated', currency: 'USD' }} kind="money" /></td>
                        </Show>
                        <td class="num">
                          <Show when={p.unpriced > 0} fallback="">
                            <Num m={{ value: p.unpriced, basis: 'measured' }} kind="count" />
                          </Show>
                        </td>
                      </tr>
                    )}
                  </For>
                </DataTable>
              </Show>
            </DetailSection>
          </>
        )}
      </Show>
    </article>
  );
}
