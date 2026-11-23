// Markdown block parser for agent replies (packages/ui/src/markdown.ts).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parse, SAFE_LINK, sliceText } from '../../packages/ui/src/markdown.ts';

test('blocks: headings, paragraphs, rules, fences with inner fences, quotes', () => {
  assert.deepEqual(parse('# Title\n\nOne\ntwo\n\n---\n\n````md\n```js\nx\n```\n````\n> quoted\n> more'), [
    { t: 'h', level: 1, text: 'Title' }, { t: 'p', text: 'One\ntwo' }, { t: 'hr' },
    { t: 'code', lang: 'md', text: '```js\nx\n```' }, { t: 'quote', blocks: [{ t: 'p', text: 'quoted\nmore' }] }]);
});

test('lists: bullets, ordered with start, nested by indent', () => {
  const [l] = parse('- a\n- b\n  - c\n- d') as any[];
  assert.equal(l.t, 'list'); assert.equal(l.ordered, false); assert.equal(l.items.length, 3);
  assert.deepEqual(l.items[1], [{ t: 'p', text: 'b' }, { t: 'list', ordered: false, start: 1, items: [[{ t: 'p', text: 'c' }]] }]);
  const [o] = parse('3. x\n4. y') as any[]; assert.equal(o.ordered, true); assert.equal(o.start, 3); assert.equal(o.items.length, 2);
});

test('tables with alignment and escaped pipes', () => {
  assert.deepEqual(parse('| a | b |\n|:--|--:|\n| 1 | x \\| y |'), [{ t: 'table', head: ['a', 'b'], align: ['left', 'right'], rows: [['1', 'x | y']] }]);
});

test('selection source positions survive quote, list, CRLF and escaped table syntax', () => {
  const source = '> - lead\r\n>   😀body\r\n\r\n| x \\| y |\r\n| --- |';
  const [quote, table] = parse(source, text => text);
  assert.equal(quote?.t, 'quote');
  const [list] = quote.blocks;
  assert.equal(list?.t, 'list');
  const [paragraph] = list.items[0];
  assert.equal(paragraph?.t, 'p');
  assert.equal(paragraph.text.text, 'lead\n😀body');
  assert.deepEqual(sliceText(paragraph.text, 'lead\n😀'.length).fragments, [{ at: source.indexOf('body'), text: 'body' }]);

  assert.equal(table?.t, 'table');
  const [cell] = table.head;
  assert.equal(cell.text, 'x | y');
  assert.deepEqual(sliceText(cell, 2, 3).fragments, [{ at: source.indexOf('| y'), text: '|' }]);
});

test('only http, https and mailto links are kept', () => {
  assert.ok(SAFE_LINK.test('https://x')); assert.ok(SAFE_LINK.test('mailto:a@b')); assert.ok(!SAFE_LINK.test('javascript:alert(1)')); assert.ok(!SAFE_LINK.test('data:text/html,x'));
});

test('deep nesting stops at a fixed depth instead of exhausting the stack', () => {
  let b: any = parse('>'.repeat(5000) + ' x'), d = 0;
  while (b[0]?.t === 'quote') { b = b[0].blocks; d++; }
  assert.equal(d, 16); assert.equal(b[0].t, 'p');
  assert.doesNotThrow(() => parse(Array.from({ length: 3000 }, (_, i) => ' '.repeat(i * 2) + '- x').join('\n')));
});
