// OR-36: the browser lists use the server's search syntax (go/coordinator/search.go); risk: lists drift from the server.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { matchSearch, searchFields } from '../../packages/ui/src/search.ts';

// R2 (OR-36); risk: a field term is ignored in the browser, or the field list drifts from the server's.
test('matchSearch: every word, field terms, unknown field is text, field lists equal', () => {
  assert.ok(matchSearch('deploy fix', 'Fix flaky deploy'));
  assert.ok(!matchSearch('deploy docs', 'Fix flaky deploy'));
  assert.ok(matchSearch('agent:clau', 'x', { agent: 'claude' }));
  assert.ok(!matchSearch('agent:claude', 'x'), 'a field the item lacks matches nothing');
  assert.ok(matchSearch('state:running', 'x', { state: 'running' }) && !matchSearch('state:run', 'x', { state: 'running' }));
  assert.ok(matchSearch('http://a', 'see http://a'), 'an unknown word:rest stays text');
  assert.ok(matchSearch('agent:claude agent:codex', 'x', { agent: 'codex' }), 'a repeated term keeps its last value, as on the server');
  const go = readFileSync(new URL('../../go/coordinator/search.go', import.meta.url), 'utf8').match(/searchFields = \[\]string\{([^}]*)\}/)![1];
  assert.deepEqual(go.split(',').map(s => s.trim().replace(/"/g, '')), [...searchFields]);
});
