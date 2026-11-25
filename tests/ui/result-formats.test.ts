// Readable forms of stored results (docs/design-system/controls/result-viewer.md, FLT-39): JSON and JSON lines as
// key/value records, unified diffs as files with numbered lines.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { jsonRecords, parseDiff, kvOf } from '../../packages/ui/src/result-formats.ts';

const RECORD = '{"phase":"socket","returnedError":null,"status":{"outcome":"unknown","stage":"unknown"},"tries":[1,true]}';

test('a JSON-lines record reads as keys and values, null as none, nested objects under their key', () => {
  const [r] = jsonRecords(RECORD + '\n', true, true)!;
  if (!('entries' in r)) throw Error('a record is an object');
  assert.deepEqual(r.entries.map(e => e.key), ['phase', 'returnedError', 'status', 'tries']);
  assert.deepEqual(r.entries[1].node, { kind: 'null', text: 'none' });
  const status = r.entries[2].node;
  assert.ok('entries' in status && status.entries.length === 2, 'status keeps its two fields');
  assert.deepEqual(kvOf([1, true]), { kind: 'array', entries: [{ key: '1', node: { kind: 'number', text: '1' } }, { key: '2', node: { kind: 'boolean', text: 'true' } }] });
});

test('a large integer reads as stored, not rounded', () => {
  const [r] = jsonRecords('{"ts":1795578213107123456,"f":1.50}', false, true)!;
  assert.ok('entries' in r);
  assert.deepEqual(r.entries.map(e => e.node), [{ kind: 'number', text: '1795578213107123456' }, { kind: 'number', text: '1.50' }]);
});

test('while pages are still coming, only whole JSON lines show and a JSON document waits', () => {
  assert.equal(jsonRecords(RECORD + '\n{"phase":"so', true, false)!.length, 1);
  assert.equal(jsonRecords('{"a":', false, false), null);
  assert.equal(jsonRecords('not json', false, true), null, 'not JSON: the viewer falls back to the source');
  assert.equal(jsonRecords('{"a":1}\n\n{"b":2}', true, true)!.length, 2, 'blank lines are no records');
});

test('a git diff splits into files with old and new line numbers', () => {
  const files = parseDiff([
    'diff --git a/app/x.ts b/app/x.ts', 'index 1..2 100644', '--- a/app/x.ts', '+++ b/app/x.ts',
    '@@ -10,3 +10,3 @@ function x', ' keep', '-old', '--- a comment removed', '+new', ' tail',
    'diff --git a/new.md b/new.md', 'new file mode 100644', '--- /dev/null', '+++ b/new.md', '@@ -0,0 +1 @@', '+hello', '\\ No newline at end of file', '',
  ].join('\n'));
  assert.deepEqual(files.map(f => [f.path, f.added, f.removed]), [['app/x.ts', 1, 2], ['new.md', 1, 0]]);
  const [h] = files[0].hunks;
  assert.deepEqual(h.lines.map(l => [l.sign, l.old, l.new]), [[' ', 10, 10], ['-', 11, null], ['-', 12, null], ['+', null, 11], [' ', 13, 12]]);
  assert.equal(h.lines[2].text, '-- a comment removed', 'a removed "-- " line inside a hunk is no file header');
});

test('a plain diff -u names its file; text without hunks is no diff', () => {
  assert.deepEqual(parseDiff('--- a.txt\t2026\n+++ a.txt\t2026\n@@ -1 +1 @@\n-a\n+b\n').map(f => f.path), ['a.txt']);
  assert.deepEqual(parseDiff('just text\n- a list\n+ more'), []);
});
