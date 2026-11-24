// Requirement: every earlier route lands on its target route of the seven-section rail, keeping the item
// (docs/designs/fleet-ia-and-journeys.md §1, route consolidation map; UR-69), and each target route opens the view that
// still speaks the earlier path (Library speaks its target routes). Unknown paths stay as they are, so the app's not-found page shows them.
import test from 'node:test';
import assert from 'node:assert/strict';
import { toInternal, toTarget } from '../../app/src/routes.ts';

const table: [earlier: string, target: string, internal: string][] = [
  ['/', '/home', '/overview'],
  ['/overview', '/home', '/overview'],
  ['/overview/Q-FLT-3', '/home/Q-FLT-3', '/overview/Q-FLT-3'],
  ['/tasks', '/projects/tasks', '/tasks'],
  ['/tasks/12', '/projects/tasks/12', '/tasks/12'],
  ['/projects/FLT/tasks/T-FLT-42', '/projects/tasks/T-FLT-42', '/tasks/T-FLT-42'],
  ['/results/r1', '/projects/results/r1', '/results/r1'],
  ['/delivery/o~r~3', '/projects/delivery/o~r~3', '/delivery/o~r~3'],
  ['/publishing', '/projects/delivery', '/delivery'],
  ['/publishing/o~r~3', '/projects/delivery/o~r~3', '/delivery/o~r~3'],
  ['/personal-lab', '/lab/mine', '/personal-lab'],
  ['/bots/new', '/library/bots/new', '/library/bots/new'],
  ['/bots/B-FLT-1', '/library/bots/B-FLT-1', '/library/bots/B-FLT-1'],
  ['/catalogue/c', '/library/methods/c', '/library/methods/c'],
  ['/agents/a', '/library/agents/a', '/library/agents/a'],
  ['/pipelines/p', '/library/pipelines/p', '/library/pipelines/p'],
  ['/changes', '/library/changes', '/library/changes'],
  ['/skills/s', '/library/skills/s', '/library/skills/s'],
  ['/agents/skill:s', '/library/skills/s', '/library/skills/s'],
  ['/library', '/library', '/library'],
  ['/hosts/pds18', '/system/hosts/pds18', '/hosts/pds18'],
  ['/hosts/limits', '/system/accounts', '/hosts/limits'],
  ['/runners', '/system/runners', '/runners'],
  ['/keys/tavily', '/system/connections/tavily', '/keys/tavily'],
  ['/spend', '/system/spend', '/spend'],
  ['/humans/new', '/system/people/new', '/humans/new'],
  ['/system', '/system', '/hosts'],
  ['/hosts', '/system', '/hosts'],
  ['/hosts/limits/acc-1', '/system/accounts/acc-1', '/hosts/limits/acc-1'],
  ['/projects/FLT', '/projects/FLT', '/projects/FLT'],
  ['/conversations/new', '/conversations/new', '/conversations/new'],
  ['/lab/E-FLT-7', '/lab/E-FLT-7', '/lab/E-FLT-7'],
  ['/live', '/live', '/live'],
  ['/nope/x', '/nope/x', '/nope/x'],
];

test('earlier routes redirect to their target route and open the same view', () => {
  for (const [earlier, target, internal] of table) {
    assert.equal(toTarget(earlier), target, earlier);
    assert.equal(toInternal(target), internal, target);
    assert.equal(toTarget(target), target, `${target} is stable`);
  }
});
