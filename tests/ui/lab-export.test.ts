// Project export version 1 to Lab examples (scripts/lab-export.ts, contracts/lab-export.schema.json).
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readdirSync, readFileSync} from 'node:fs';
import {join} from 'node:path';
import {examplesFromExport, parseExport} from '../../scripts/lab-export.ts';
import {checkExample} from '../../scripts/lab-examples.ts';
import {useCaseIds} from '../../scripts/lab-example-format.ts';

const useCases = useCaseIds('config/lab');
const exported = {schema: 1, project: 'cs', stepId: 'translate-check', exportedAt: '2026-11-11T00:00:00Z', rows: [
  {rowId: 'r1', input: 'Text: "Добро пожаловать". Question: is this Russian?', label: 'yes', tag: 'ru', productionCost: {value: 0.002, currency: 'USD', basis: 'measured'}, split: 'tune'},
  {rowId: 'r2', input: 'Text: "Welcome". Question: is this Russian?', label: 'no', tag: 'en', split: 'held_out'}]};

test('X1 every row of a version-1 project export becomes a Lab example whose checks pass on its label alone, and only version 1 is read (requirement: docs/designs/fleet-lab.md, Project data in examples and staging 0; risk: project rows imported without a usable label check, or a changed export format read as version 1)', () => {
  const examples = examplesFromExport(exported, 'fleet:UC5');
  assert.deepEqual(examples.map(e => [e.id, e.origin, e.split]), [['cs-translate-check-r1', 'export:cs', 'tune'], ['cs-translate-check-r2', 'export:cs', 'held_out']]);
  for (const e of examples) assert.deepEqual(checkExample(e, useCases), [], e.id);
  assert.throws(() => parseExport({...exported, schema: 2}), /not version 1/);
  assert.throws(() => parseExport({...exported, rows: [{...exported.rows[0], split: 'test'}]}), /split/);
  assert.throws(() => parseExport({...exported, rows: [exported.rows[0], exported.rows[0]]}), /duplicate rowId/);
  assert.throws(() => parseExport({...exported, rows: [{...exported.rows[0], label: 'maybe'}]}), /yes or no/);
  assert.throws(() => examplesFromExport({...exported, rows: [{...exported.rows[0], rowId: 'a b'}, {...exported.rows[1], rowId: 'a/b'}]}, 'fleet:UC5'), /same example id/);
});

test('X2 every seed example passes its checks with no agent (requirement: docs/designs/fleet-lab.md, examples; risk: a seed whose checks pass on nothing or fail on its own reference)', () => {
  for (const f of readdirSync('config/lab/examples').filter(f => f.endsWith('.json'))) assert.deepEqual(checkExample(JSON.parse(readFileSync(join('config/lab/examples', f), 'utf8')), useCases), [], f);
});
