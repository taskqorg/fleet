import type { StateOf } from '../../../../../src/index.ts';

export const PIPE_DEFS: readonly [string, boolean, StateOf<'definition'> | StateOf<'execution'>, string, string][] = [
  ['issue-to-pr', true, 'draft', '9 steps · 7 agents · shared', '2h'],
  ['render-props', false, 'failed', '3 steps · csgo', '4m'],
  ['web-research', false, 'succeeded', '4 steps · 1 agent · shared', '12m'],
  ['compare-3d-methods', false, 'running', '5 steps · 2 agents · csgo', 'now'],
  ['draft-examples', true, 'never', '2 steps · 1 agent · shared', 'never'],
];
