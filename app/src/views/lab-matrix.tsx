// Lab comparison (docs/design-system/pages/laboratory.md#target, owner decision 2026-11-25): the Model × Pipeline matrix of
// a collection or a use case. Each cell carries score, median wall time and failure rate; tokens and cost are in the
// cell's Details (OR-54, OR-59). When the matrix does not fit its width the same cells become a ranked table (OR-66).
import { createMemo, createSignal, For, Show } from 'solid-js';
import {
  Button, DataTable, Empty, ErrorBanner, MatrixCell, Num, ProblemRow, Skeleton, TableName, TableRow, agentLabel, msMeasured,
  type MatrixCellState,
  money,
} from '../../../packages/ui/src/index.ts';
import { op, useAlive, type Ops } from '../api.ts';
import type { LabLeaderboardEntry } from '../../../contracts/generated/ops.ts';

/** One model × pipeline cell: every leaderboard group of that pair over the scope, on one score form. */
export type ComparisonCell = {
  row: string; col: string;
  trials: number; succeeded: number; failed: number;
  score: number | null; wallMs: number | null;
  tokens: number; costUsd: number | null; costBases: string[];
  useCases: number; groups: number;
  /** The group with the best score: Promote and the click open its latest experiment. */
  best: LabLeaderboardEntry;
};

const rowOf = (e: LabLeaderboardEntry) => (e.model ? agentLabel(e.model) : '') || e.profile || e.variantLabel || 'baseline';

// Weighted median of group medians (by trials): the cell's median wall time across the groups it joins.
function weightedMedian(xs: { v: number; w: number }[]): number | null {
  const s = xs.filter(x => x.w > 0).sort((a, b) => a.v - b.v);
  const total = s.reduce((n, x) => n + x.w, 0);
  if (!total) return null;
  let acc = 0;
  for (const x of s) { acc += x.w; if (acc * 2 >= total) return x.v; }
  return s[s.length - 1].v;
}

/**
 * Cells of the comparison. Scores are never combined across evaluator forms (controls/matrix-cell.md): the matrix uses
 * the form most trials were scored with; groups of other forms still count in trials and failures.
 */
export function comparisonCells(entries: readonly LabLeaderboardEntry[]): { form: string | null; rows: string[]; cols: string[]; cells: ComparisonCell[] } {
  const byForm = new Map<string, number>();
  for (const e of entries) if (e.scoreMean != null) byForm.set(e.scoreForm, (byForm.get(e.scoreForm) ?? 0) + e.succeededTrials);
  const form = [...byForm].sort((a, b) => b[1] - a[1])[0]?.[0] ?? null;
  const groups = new Map<string, LabLeaderboardEntry[]>();
  for (const e of entries) {
    const k = rowOf(e) + '\0' + e.pipelineId;
    groups.set(k, [...(groups.get(k) ?? []), e]);
  }
  const cells: ComparisonCell[] = [];
  for (const [k, es] of groups) {
    const [row, col] = k.split('\0');
    const scored = es.filter(e => e.scoreForm === form && e.scoreMean != null && e.succeededTrials > 0);
    const w = scored.reduce((n, e) => n + e.succeededTrials, 0);
    const score = w ? scored.reduce((n, e) => n + e.scoreMean! * e.succeededTrials, 0) / w : null;
    const priced = es.filter(e => e.costUsd != null);
    const best = (scored.length ? scored : es).toSorted((a, b) => (b.scoreMean ?? -1) - (a.scoreMean ?? -1) || b.trials - a.trials)[0];
    cells.push({
      row, col, best, groups: es.length,
      trials: es.reduce((n, e) => n + e.trials, 0),
      succeeded: es.reduce((n, e) => n + e.succeededTrials, 0),
      failed: es.reduce((n, e) => n + e.failedTrials, 0),
      score,
      wallMs: weightedMedian(es.filter(e => e.wallMsMedian != null).map(e => ({ v: e.wallMsMedian!, w: e.trials }))),
      tokens: es.reduce((n, e) => n + e.tokensTotal, 0),
      costUsd: priced.length ? priced.reduce((n, e) => n + e.costUsd!, 0) : null,
      costBases: [...new Set(es.map(e => e.costBasis).filter(Boolean))],
      useCases: new Set(es.map(e => e.useCaseId)).size,
    });
  }
  // Rows and columns by their best score, so the strongest model and pipeline read first.
  const bestOf = (key: (c: ComparisonCell) => string) => {
    const m = new Map<string, number>();
    for (const c of cells) m.set(key(c), Math.max(m.get(key(c)) ?? -1, c.score ?? -1));
    return [...m].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).map(([x]) => x);
  };
  return { form, rows: bestOf(c => c.row), cols: bestOf(c => c.col), cells };
}

const failRate = (c: ComparisonCell) => c.trials ? c.failed / c.trials : 0;
// Partial: some trials failed often enough to doubt the score (amber); fail: nothing succeeded (red, OR-56).
const cellState = (c: ComparisonCell): MatrixCellState => c.succeeded === 0 ? 'fail' : failRate(c) >= 0.1 ? 'partial' : 'pass';
const pct = (r: number) => r <= 0 ? '0%' : r < 0.01 ? '<1%' : `${Math.round(r * 100)}%`;

/** Details of a cell, in its tooltip (OR-59): tokens, cost with basis, how many groups and use cases it joins. */
function cellDetails(c: ComparisonCell, form: string | null): string {
  const cost = c.costUsd == null ? 'cost unavailable' : `cost ${money(c.costUsd).text}${c.costBases.length && !c.costBases.includes('measured') ? ` (${c.costBases.join(', ')})` : ''}`;
  return [
    `${c.succeeded} succeeded, ${c.failed} failed of ${c.trials} trials`,
    `tokens ${c.tokens.toLocaleString('en')}`, cost,
    `${c.useCases} use case${c.useCases === 1 ? '' : 's'}${c.groups > 1 ? `, median across ${c.groups} groups` : ''}`,
    form ? `score: ${form} evaluators` : '',
  ].filter(Boolean).join(' · ');
}

export function LabComparison(props: {
  /** The collection or the use case: the heading and the section's accessible name. */
  title: string;
  windowLabel: string;
  entries: readonly LabLeaderboardEntry[];
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  /** The project an experiment belongs to: Promote sets that project's recommendation. */
  projectOf: (experimentId: string) => string | undefined;
  onOpenExperiment: (id: string) => void;
  /** A use case's comparison has Run in Lab (J2). */
  onRun?: () => void;
}) {
  const data = createMemo(() => comparisonCells(props.entries));
  const at = createMemo(() => new Map(data().cells.map(c => [c.row + '\0' + c.col, c])));
  const ranked = createMemo(() => data().cells.toSorted((a, b) => (b.score ?? -1) - (a.score ?? -1) || failRate(a) - failRate(b)));
  const winner = createMemo(() => ranked().find(c => c.score != null && c.score > 0 && c.succeeded > 0) ?? null);
  const runs = createMemo(() => data().cells.reduce((n, c) => n + c.trials, 0));
  const useCases = createMemo(() => new Set(props.entries.map(e => e.useCaseId)).size);

  const [promoting, setPromoting] = createSignal(false);
  const [promoted, setPromoted] = createSignal<Ops['lab_promote']['reply'] | null>(null);
  const [promoteError, setPromoteError] = createSignal<string | null>(null);
  const alive = useAlive();
  const promote = async () => {
    const w = winner();
    if (!w || promoting()) return;
    setPromoting(true); setPromoteError(null);
    try {
      const reply = await op('lab_promote', { experimentId: w.best.latestExperimentId, variant: w.best.variantLabel }, { signal: alive });
      if (!alive.aborted) setPromoted(reply);
    } catch (x) {
      if (!alive.aborted) setPromoteError(x instanceof Error ? x.message : String(x));
    } finally {
      if (!alive.aborted) setPromoting(false);
    }
  };
  const scope = createMemo(() => { const w = winner(); return w ? props.projectOf(w.best.latestExperimentId) : undefined; });

  return (
    <section class="panel lab-cmp" aria-label={`Suite leaderboard: ${props.title}`} data-ui="lab-comparison">
      <header class="panel-h">
        <h2>{props.title}</h2>
        <span class="muted lab-cmp-meta">{useCases()} use case{useCases() === 1 ? '' : 's'} · {runs()} runs · {props.windowLabel}</span>
        <span class="grow" />
        <Show when={props.onRun} keyed>{run => <Button label="Run in Lab" icon="play" onClick={run} />}</Show>
        <Show when={winner()}>{w => <>
          <Show when={scope()}>{p => (
            <span class="lab-scope muted" data-tip="Promote sets this project's recommended method. A system-wide scope is not offered yet.">
              scope <span class="mono">{p()}</span>
            </span>
          )}</Show>
          <Button label={promoted() ? 'Promoted' : 'Promote best'} icon="check-check" kind="primary" busy={promoting()}
            tip={`Promote ${w().row} · ${w().col} (${w().best.variantLabel || 'baseline'}, use case ${w().best.useCaseId}) to the recommended method`} onClick={promote} />
        </>}</Show>
      </header>
      <Show when={props.error}>{err => <ErrorBanner text={err()} onRetry={() => props.onRetry()} />}</Show>
      <Show when={promoteError()}>{err => <ProblemRow title="Promote refused" facts={err()} severity="failure" actions={[]} />}</Show>
      <Show when={promoted()}>{p => (
        <p class="lab-note" role="status">Recommended method set to <span class="mono">{p().method.id ?? ''}</span>{p().catalogue ? <> · catalogue r{p().catalogue!.revision}</> : ''}{p().note ? ` · ${String(p().note)}` : ''}</p>
      )}</Show>
      <Show when={!(props.loading && !props.error)} fallback={<Skeleton rows={4} />}>
        <Show when={data().cells.length > 0} fallback={<Empty text={`No finished trials for ${props.title} in this window yet.`} />}>
          <div class="lab-cmp-body">
            <div class="lab-mx">
              <DataTable label={`Model × Pipeline: ${props.title}`} head={
                <tr>
                  <th scope="col" class="lab-mx-corner">Score · median · failed</th>
                  <For each={data().cols}>{c => <th scope="col">{c}</th>}</For>
                </tr>
              }>
                <For each={data().rows}>{r => (
                  <tr>
                    <th scope="row">{r}</th>
                    <For each={data().cols}>{col => (
                      <td>
                        <Show when={at().get(r + '\0' + col)} fallback={<span class="lab-mx-none" aria-label={`${r}, ${col}: no trials`}>—</span>}>{c => (
                          <MatrixCell state={cellState(c())} label={`${r}, ${col}`} score={c().score ?? undefined} k={c().succeeded} n={c().trials}
                            time={c().wallMs == null ? undefined : msMeasured(c().wallMs)} failRate={failRate(c())}
                            note={c().succeeded === 0 ? `all ${c().trials} failed` : undefined}
                            confounders={cellDetails(c(), data().form)} onOpen={() => props.onOpenExperiment(c().best.latestExperimentId)} />
                        )}</Show>
                      </td>
                    )}</For>
                  </tr>
                )}</For>
              </DataTable>
              <p class="lab-note muted">Tokens and cost of each cell are in its details (hover or focus).</p>
            </div>
            <div class="lab-rank">
              <DataTable label={`Ranked variants: ${props.title}`} head={
                <tr><th scope="col">Variant</th><th scope="col" class="num">Score</th><th scope="col" class="num">Median</th><th scope="col" class="num">Failed</th></tr>
              }>
                <For each={ranked()}>{c => (
                  <TableRow selected={c === winner()} onOpen={() => props.onOpenExperiment(c.best.latestExperimentId)}>
                    <TableName label={`${c.row} · ${c.col}`} onOpen={() => props.onOpenExperiment(c.best.latestExperimentId)} />
                    <td class="num"><Num m={{ value: c.score, basis: c.score == null ? 'unavailable' : 'measured' }} kind="score" /></td>
                    <td class="num"><Num m={msMeasured(c.wallMs)} kind="duration" /></td>
                    <td class="num mono" data-tip={cellDetails(c, data().form)}>{pct(failRate(c))}</td>
                  </TableRow>
                )}</For>
              </DataTable>
            </div>
          </div>
        </Show>
      </Show>
    </section>
  );
}
