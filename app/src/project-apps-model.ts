// The words of project apps on Setup › Apps and of a pipeline's Used by lines (docs/designs/fleet-project-apps.md,
// Screens; board drafts/project-apps.bl PA1, PA3, PA4): plain functions, so a test reads them without a page.
import { formatRowSpend, type CostFigureLike } from '../../packages/ui/src/format.ts';
import type { Ops } from './api.ts';

export type ProjectApp = Ops['project_apps']['reply']['apps'][number];

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? '' : 's'}`;

/** Runs, failed runs and cost over the window in one quiet line: "412 runs · 3 failed · $4.10"; "no runs" when none. */
export function usageLine(u: { runs: number; failed?: number; cost: readonly CostFigureLike[] }): string {
  if (u.runs === 0) return 'no runs';
  const spend = formatRowSpend(u.cost);
  return [plural(u.runs, 'run'), u.failed ? `${u.failed} failed` : '', spend.hasFigures ? [spend.primaryText, spend.suffix].filter(Boolean).join(' ') : '']
    .filter(Boolean).join(' · ');
}

/** The pipelines an app may run: "all pipelines" or their ids. */
export const pipelinesText = (a: Pick<ProjectApp, 'pipelines'>) => (a.pipelines.includes('*') ? 'all pipelines' : a.pipelines.join(', '));

/** Limits in words, as the approval card names them: "no limits", "500 runs a day". */
export function limitsText(l: ProjectApp['limits']): string {
  const parts = [
    l.runsPerDay ? `${l.runsPerDay} runs a day` : '',
    l.concurrentRuns ? `${l.concurrentRuns} runs at once` : '',
    l.spendPerMonth ? `${l.spendPerMonth.amount} ${l.spendPerMonth.currency} a month` : '',
  ].filter(Boolean);
  return parts.length ? parts.join(', ') : 'no limits';
}

/** An app row's facts: pipelines · use over 30 days (runs and cost) · limits; an asked or ended app without runs leaves use out. */
export function appRowText(a: Pick<ProjectApp, 'pipelines' | 'state' | 'use30d' | 'limits'>): string {
  const u = a.use30d ?? { runs: 0, cost: [] };
  const use = u.runs > 0 ? `${usageLine({ runs: u.runs, cost: u.cost })} · 30 d` : a.state === 'active' ? 'no runs in 30 d' : '';
  return [pipelinesText(a), use, limitsText(a.limits)].filter(Boolean).join(' · ');
}
