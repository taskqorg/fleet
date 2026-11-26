// Requirement: every earlier route lands on its target route of the eight-section rail, keeping the item
// (docs/designs/fleet-ia-and-journeys.md §1, route consolidation map; UR-69), and each target route opens the view that
// still speaks the earlier path (Library speaks its target routes). Unknown paths stay as they are, so the app's not-found page shows them.
import test from 'node:test';
import assert from 'node:assert/strict';
import { toInternal, toTarget, setProjectCodes, projectCodesPending } from '../../app/src/routes.ts';

const table: [earlier: string, target: string, internal: string][] = [
  ['/', '/home', '/overview'],
  ['/overview', '/home', '/overview'],
  ['/overview/Q-FLT-3', '/home/Q-FLT-3', '/overview/Q-FLT-3'],
  ['/tasks', '/projects/tasks', '/tasks'],
  ['/tasks/12', '/projects/tasks/12', '/tasks/12'],
  ['/projects/FLT/tasks/T-FLT-42', '/projects/FLT/tasks/T-FLT-42', '/tasks/T-FLT-42?project=FLT'],
  ['/tasks?project=fleet&state=failed', '/projects/fleet/tasks?state=failed', '/tasks?state=failed&project=fleet'],
  ['/tasks?project=a,b', '/projects/tasks?project=a,b', '/tasks?project=a,b'],
  ['/projects/FLT/results/R-FLT-3', '/projects/FLT/results/R-FLT-3', '/results/R-FLT-3?project=FLT'],
  ['/projects/FLT/delivery', '/projects/FLT/delivery', '/delivery?project=FLT'],
  ['/projects/FLT/setup', '/projects/FLT/setup', '/projects/FLT?facet=setup'],
  ['/projects/FLT/rules', '/projects/FLT/setup', '/projects/FLT?facet=setup'],
  ['/projects/FLT/access?grant=g1', '/projects/FLT/setup?grant=g1', '/projects/FLT?grant=g1&facet=setup'],
  ['/projects/FLT?facet=rules', '/projects/FLT/setup', '/projects/FLT?facet=setup'],
  ['/projects/FLT?facet=access&grant=g1', '/projects/FLT/setup?grant=g1', '/projects/FLT?grant=g1&facet=setup'],
  ['/projects/DAM/pipelines', '/projects/DAM/pipelines', '/projects/DAM?facet=pipelines'],
  ['/projects/DAM/pipelines/doc-recognize-openai?tab=definition', '/projects/DAM/pipelines/doc-recognize-openai?tab=definition', '/projects/DAM?tab=definition&facet=pipelines&pipeline=doc-recognize-openai'],
  ['/projects/DAM/history', '/projects/DAM/history', '/projects/DAM?facet=history'],
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

test('a screen that names a project waits for the codes, so a code is never read as an id', () => {
  assert.equal(projectCodesPending('/projects/DAM'), true);
  assert.equal(projectCodesPending('/projects/DAM/tasks?state=x'), true);
  assert.equal(projectCodesPending('/projects/tasks'), false);
  assert.equal(projectCodesPending('/projects'), false);
  assert.equal(projectCodesPending('/home'), false);
  setProjectCodes([]); // a failed project_list: ids stand in, nothing waits
  assert.equal(projectCodesPending('/projects/DAM'), false);
});

test('a project facet names the project by its code once codes are known; the id still opens it', () => {
  setProjectCodes([{ id: 'fleet', code: 'FLT' }]);
  assert.equal(toTarget('/tasks/T-FLT-42?project=fleet'), '/projects/FLT/tasks/T-FLT-42');
  assert.equal(toInternal('/projects/flt/tasks/T-FLT-42'), '/tasks/T-FLT-42?project=fleet');
  assert.equal(toTarget('/projects/fleet'), '/projects/FLT');
  assert.equal(toTarget('/projects/fleet/rules'), '/projects/FLT/setup');
  // The facet replaces a project filter the address carried in its query.
  assert.equal(toInternal('/projects/FLT/tasks?project=other&q=x'), '/tasks?q=x&project=fleet');
});
