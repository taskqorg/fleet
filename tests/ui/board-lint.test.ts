// The board lint (docs/design-system/README.md "Board layout") reads .bl rows and names what lies under what.
// It checks the lint itself on a small canvas, never the board's content: the lint is a report, not a gate.
import { test } from 'node:test';
import assert from 'node:assert/strict';
const { lint } = await import(new URL('../../packages/ui/scripts/board-lint.mjs', import.meta.url).href) as {
  lint: (text: string) => { kind: string; a: string; b: string }[];
};

const canvas = (rows: string[]) => ['bp:v1', 'canvas:', '---', ...rows].join('\n');

test('BL1 board lint names overlapping frames, tight gaps and children past their frame (requirement: README "Board layout"; risk: frames hide each other on the board)', () => {
  const kinds = lint(canvas([
    'aaaaaaaaaaaaaaa1 al(v) p(0,0) s(400,300) f[] "A"',
    '  bbbbbbbbbbbbbbb1 t("long",Inter,13) p(380,10) s(hug:60,hug:16) f[] "past the edge"',
    '  bbbbbbbbbbbbbbb2 al(v) p(300,10) s(200,100) f[] abs "menu"',
    'aaaaaaaaaaaaaaa2 al(v) p(300,0) s(400,300) f[] "B over A"',
    'aaaaaaaaaaaaaaa3 al(v) p(0,320) s(400,300) f[] "C under A"',
  ])).map(f => `${f.kind}:${f.a.split(' [')[0]}`);
  assert.deepEqual(kinds.sort(), ['outside:past the edge', 'overlap:A', 'tight:A', 'tight:B over A'].sort());
});

test('BL2 a clipping list and frames on the grid are clean', () => {
  assert.deepEqual(lint(canvas([
    'aaaaaaaaaaaaaaa1 al(v) p(0,0) s(400,300) f[] clip "list"',
    '  bbbbbbbbbbbbbbb1 al(h) p(0,280) s(400,40) f[] "row past the panel"',
    'aaaaaaaaaaaaaaa2 al(v) p(560,0) s(400,300) f[] "next"',
  ])), []);
});
