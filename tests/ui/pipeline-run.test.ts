// Requirement: Run with files on a pipeline page (docs/design-system/pages/projects.md, Run on files): every chosen file
// reaches the method under a path the server accepts, and a result's records show as a table of their plain fields.
// Risk: a repeated or unsafe file name refuses the whole run (400), or the table shows empty or nested columns.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { inputPaths, recordColumns, resultRecords, stepErrorText } from '../../app/src/pipeline-run-model.ts';

test('input paths are distinct plain names the server accepts', () => {
  assert.deepEqual(inputPaths(['a.jpg', 'a.jpg', 'a.jpg', 'scan', 'scan', '../x.pdf', 'c:\\d.png', '']), ['a.jpg', 'a-2.jpg', 'a-3.jpg', 'scan', 'scan-2', 'x.pdf', 'c--d.png', 'file-8']);
});

test('a result shows its first list of records with the plain fields some record fills', () => {
  const out = { summary: 'S · 2 files', files: 2, model: { aliases: [] }, documents: [
    { file: 'e1.jpg', type: 'passport', alias: null, extra: { n: 1 } },
    { file: 'e2.jpg', type: 'model_release', alias: null, signed: false },
  ] };
  const rows = resultRecords(out);
  assert.equal(rows?.length, 2);
  assert.deepEqual(recordColumns(rows!), ['file', 'type', 'signed']);
  assert.equal(resultRecords({ summary: 'x', files: [] }), null);
  assert.equal(resultRecords([{ a: 1 }]), null);
});

test('a step error shows its message, not the JSON of the error object', () => {
  assert.equal(stepErrorText({ code: 'infra:inputs: Unavailable or foreign pipeline input', message: 'Unavailable or foreign pipeline input' }), 'Unavailable or foreign pipeline input');
  assert.equal(stepErrorText({ code: 'timeout' }), 'timeout');
  assert.equal(stepErrorText('plain'), 'plain');
  assert.equal(stepErrorText(null), undefined);
});
