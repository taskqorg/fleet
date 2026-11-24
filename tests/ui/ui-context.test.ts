// scripts/ui-context.ts: rule texts parse from both rule files, and a page's context names its canvas and cited rules.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { context, links, rules } from '../../scripts/ui-context.ts';

test('UC1 rules parse OR table rows and UR leads', () => {
  const m = rules('| **OR-7 No added arrows** | Never add an arrow. |\n', '* **UR-3 One home.** Every screen has one home section.\n');
  assert.equal(m.get('OR-7'), 'No added arrows: Never add an arrow.');
  assert.equal(m.get('UR-3'), 'One home. Every screen has one home section.');
});

test('UC2 links resolve relative to the file and drop anchors and URLs', () => {
  assert.deepEqual(links('[a](../controls/x.md#y) [b](https://e.com) [c](#z)', 'a/b/pages/p.md'), ['a/b/controls/x.md']);
});

test('UC3 the home context names its canvas and the rules its spec cites', () => {
  const c = context('home');
  assert.match(c, /docs\/design-system\/board\/screens\/home\.bl/);
  assert.match(c, /\*\*OR-50\*\* Home is sessions/);
  assert.doesNotMatch(c, /no rule with this ID/);
});
