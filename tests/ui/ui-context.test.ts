// scripts/ui-context.ts: rule texts parse from both rule files, and a page's context names its canvas and cited rules.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { cited, context, homes, links, rules, section } from '../../scripts/ui-context.ts';

test('UC1 rules parse OR table rows and UR leads', () => {
  const m = rules('| **OR-7 No added arrows** | Never add an arrow. |\n', '* **UR-3 One home.** Every screen has one home section.\n');
  assert.equal(m.get('OR-7'), 'No added arrows: Never add an arrow.');
  assert.equal(m.get('UR-3'), 'One home. Every screen has one home section.');
  const t = rules('', '| **UR-70.** | An unknown route says so. | Browser: open `/x`. |\n');
  assert.equal(t.get('UR-70'), 'An unknown route says so.');
});

test('UC7 moved rules parse to their home: a spec, a check or the rule they merged into', () => {
  const owner = '## Rules kept by a component or a check\n\n| Rules | Fate | Where it lives now |\n|---|---|---|\n' +
    '| OR-24 Three columns, OR-25 One scroll per column | component | [patterns/app-shell.md](patterns/app-shell.md#rules-kept-here) |\n' +
    '| OR-7 No added arrows | check | `check-design.mjs` OR-7. The owner\'s words: "Never." |\n| OR-10 System first | merged | into OR-4 in [rules](#rules) |\n';
  const h = homes(owner, '## Rules kept elsewhere\n\n| UR-11 | merged | into [OR-6](owner-rules.md#rules) |\n');
  assert.deepEqual(h.get('OR-25'), { name: 'One scroll per column', fate: 'component', where: 'docs/design-system/patterns/app-shell.md#rules-kept-here' });
  assert.equal(h.get('OR-7')?.where, '`check-design.mjs` OR-7');
  assert.equal(h.get('OR-10')?.into, 'OR-4');
  assert.equal(h.get('UR-11')?.into, 'OR-6');
});

test('UC2 links resolve relative to the file and drop anchors and URLs', () => {
  assert.deepEqual(links('[a](../controls/x.md#y) [b](https://e.com) [c](#z)', 'a/b/pages/p.md'), ['a/b/controls/x.md']);
});

test('UC4 a rule range counts every ID in it', () => {
  assert.deepEqual(cited('OR-41 to OR-43, UR-2').sort(), ['OR-41', 'OR-42', 'OR-43', 'UR-2']);
});

test('UC5 section takes one heading by name or slug, up to the next heading of its level', () => {
  const t = '# A\n## Card one\nOR-1\n### Sub\nOR-2\n## Next\nOR-3';
  assert.equal(section(t, 'card-one'), '## Card one\nOR-1\n### Sub\nOR-2');
  assert.equal(section(t, 'missing'), t);
});

test('UC3 the home context names its canvas and the rules its spec cites', () => {
  const c = context('home');
  assert.match(c, /docs\/design-system\/board\/screens\/home\.bl/);
  assert.match(c, /\*\*OR-50\*\* Home is sessions/);
  assert.doesNotMatch(c, /no rule with this ID/);
});

test('UC6 every page context starts with the owner principles card', () => {
  const c = context('home');
  assert.ok(c.indexOf('## Principles card') < c.indexOf('Read next:'));
  assert.match(c, /Useful empty pane/);
});
