// Run strip: the time strip's form for one host's runs over a window, default 24 hours (controls/time-strip.md#run-strip).
import { createMemo, For } from 'solid-js';
import { exact } from '../format.ts';

export type StripRun = { start: number; end: number | null; failed?: boolean };
export type RunStripProps = { runs: readonly StripRun[]; now: number; hours?: number };

export function RunStrip(p: RunStripProps) {
  const hours = () => p.hours ?? 24;
  const from = () => p.now - hours() * 3_600_000;
  const x = (t: number) => Math.min(100, Math.max(0, (t - from()) / (p.now - from()) * 100));
  const failed = createMemo(() => p.runs.filter(r => r.failed).length);
  const marks = createMemo(() => [4, 3, 2, 1].map(q => `−${hours() * q / 4}h`).concat('now'));
  return (
    <div class="rstrip" data-ui="run-strip" role="img" aria-label={`${hours()} hours: ${p.runs.length} runs, ${failed()} failed`}>
      <span class="rs-track">
        <For each={p.runs}>
          {r => {
            const a = () => x(r.start), b = () => x(r.end ?? p.now);
            return <i class={{ failed: !!r.failed, live: r.end === null }} data-tip={`${r.failed ? 'Failed' : 'Run'} ${exact(r.start)}`} style={{ left: `${a()}%`, width: `max(2px, ${b() - a()}%)` }} />;
          }}
        </For>
      </span>
      <span class="rs-marks" aria-hidden="true"><For each={marks()}>{m => <span>{m}</span>}</For></span>
    </div>
  );
}
