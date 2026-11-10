// Copy format (formats.md "Copy"): header line per message, code stays code, HTML keeps only tags chat apps keep.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { author, clipMessage, clipHtml } from '../../packages/ui/src/clip.ts';

test('author line', () => {
  assert.equal(author({ role: 'operator', by: 'alex@x.io' }), 'alex');
  assert.equal(author({ role: 'agent', harness: 'codex', model: 'gpt-6.1-sol', effort: 'low' }), 'codex gpt-6.1-sol low');
  assert.equal(author({ role: 'agent', harness: 'claude', model: 'claude-opus-5-5', effort: 'high' }), 'claude opus-5.5 high');
});

test('message and html', () => {
  const now = new Date(2026, 10, 11, 15).getTime(), at = new Date(2026, 10, 11, 14, 3).getTime();
  const md = clipMessage('alex', at, now, 'Run `npm test` **now** <x>\n```sh\na < b\n```\nok');
  assert.equal(md.split('\n')[0], '**alex · 11 Nov 14:03**');
  assert.equal(clipHtml(md), '<b>alex · 11 Nov 14:03</b><br>Run <code>npm test</code> <b>now</b> &lt;x&gt;<pre>a &lt; b</pre>ok');
  assert.equal(clipMessage('a', new Date(2025, 0, 2, 9, 5).getTime(), now, 'x').split('\n')[0], '**a · 2 Jan 2025 09:05**');
});
