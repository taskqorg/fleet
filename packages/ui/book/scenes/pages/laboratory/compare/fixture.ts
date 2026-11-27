import { type TaskRow } from '../../../../../src/index.ts';
import { tasks } from '../../../../fixtures.ts';

export const VARIANTS = [
  { v: 'A baseline', score: 0.71, judge: 0.68, tokens: 182000, time: 860, cost: 4.1, est: false, trials: 75, failed: 4 },
  { v: 'B new skill', score: 0.83, judge: 0.8, tokens: 151000, time: 790, cost: 3.6, est: true, trials: 75, failed: 2 },
  { v: 'C claude', score: 0.79, judge: null as number | null, tokens: 210000, time: 1020, cost: null as number | null, est: false, trials: 75, failed: 9 },
];
export const ALL: TaskRow[] = tasks(3000);
