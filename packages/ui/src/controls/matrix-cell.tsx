// Matrix cell: one example × variant of a Lab experiment, how its repeats went (controls/matrix-cell.md).
import { createMemo, Show } from 'solid-js';
import type { IconName } from '../registry.ts';
import { type Measured, score } from '../format.ts';
import { Icon, Num, Button } from './primitives.tsx';

export type MatrixCellState = 'pass' | 'partial' | 'fail' | 'running' | 'excluded';
const LOOK: Record<MatrixCellState, { icon: IconName; word: string }> = {
  pass: { icon: 'circle-check', word: 'pass' },
  partial: { icon: 'bell', word: 'partial' },
  fail: { icon: 'octagon-x', word: 'failed' },
  running: { icon: 'activity', word: 'running' },
  excluded: { icon: 'circle-slash', word: 'excluded' },
};

// A share as a whole percent; a small non-zero share never reads as 0%.
const percentText = (r: number) => r <= 0 ? '0%' : r < 0.01 ? '<1%' : `${Math.round(r * 100)}%`;

export function MatrixCell(p: {
  state: MatrixCellState;
  /** "Example 3, variant codeMode": the start of the accessible name. */
  label: string;
  /** One evaluator's score; never combined across evaluator forms. */
  score?: number;
  /** Repeats that count, of all repeats. */
  k: number; n: number;
  /** Running: the step it is at, "3/6". */
  step?: string;
  time?: Measured; cost?: Measured;
  /** A comparison cell (Lab idle matrix): the share of trials that failed, 0 to 1. The line then reads "median 4m · 2% failed" and cost moves to Details (OR-54). */
  failRate?: number;
  /** Partial: why a repeat was left out. Fail: the reason. Excluded: defaults to "not in this comparison". */
  note?: string;
  /** Host, harness and model, in the tooltip (OR-59). */
  confounders?: string;
  onOpen?: () => void;
  onRetry?: () => void;
}) {
  const look = () => LOOK[p.state];
  const head = createMemo(() => p.state === 'running' ? `step ${p.step ?? ''}`.trim() : p.state === 'fail' ? 'failed' : p.state === 'excluded' ? 'excluded' : p.score !== undefined ? score(p.score).text : '—');
  const name = createMemo(() => `${p.label}: ${look().word}${p.score !== undefined && p.state !== 'fail' ? `, score ${score(p.score).text}` : ''}, ${p.k} of ${p.n} ${p.failRate !== undefined ? 'trials' : 'repeats'}${p.failRate !== undefined ? `, ${percentText(p.failRate)} failed` : ''}${p.note ? `, ${p.note}` : ''}`);
  return (
    <div class="mcell" data-state={p.state} data-ui="matrix-cell" data-tip={p.confounders}>
      <button type="button" class="mc-open" aria-label={name()} onClick={() => p.onOpen?.()}>
        <span class="mc-l1">
          <Icon name={look().icon} />
          <span class="mc-head">{head()}</span>
          <Show when={p.state !== 'excluded'}><span class="mc-k mono">{p.k}/{p.n}</span></Show>
        </span>
        <span class="mc-l2">
          <Show
            when={p.state === 'pass' || p.state === 'running' || (p.failRate !== undefined && p.state === 'partial')}
            fallback={<span class="mc-note">{p.note ?? (p.state === 'excluded' ? 'not in this comparison' : '')}</span>}
          >
            <Show when={p.failRate !== undefined} fallback={<>
              <Show when={p.time} keyed>{t => <Num m={t} kind="duration" />}</Show>
              <Show when={p.time && p.cost}> · </Show>
              <Show when={p.cost} keyed>{c => <Num m={c} kind="money" />}</Show>
            </>}>
              <Show when={p.time} keyed>{t => <>median <Num m={t} kind="duration" /> · </>}</Show>
              <span class="mono">{percentText(p.failRate!)}</span> failed
            </Show>
          </Show>
        </span>
      </button>
      <Show when={p.state === 'fail' && p.onRetry} keyed>
        {retry => <span class="mc-retry"><Button label="Retry" kind="quiet" onClick={retry} /></span>}
      </Show>
    </div>
  );
}
