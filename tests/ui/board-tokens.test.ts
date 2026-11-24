// The design board's style file (docs/design-system/tokens.md "Design board") is generated from packages/ui/tokens.json:
// board drawings must resolve the same colours, type and sizes as the shipped controls.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
const { boardDs, BOARD_DS } = await import(new URL('../../packages/ui/scripts/board-tokens.mjs', import.meta.url).href) as { boardDs: () => string; BOARD_DS: URL };

test('BT1 board style file matches tokens.json (requirement: tokens.md "Design board"; risk: board colours drift from packages/ui)', () => {
  assert.equal(readFileSync(BOARD_DS, 'utf8'), boardDs(), 'run pnpm run board:tokens and commit docs/design-system/board/Styles/default.ds');
});

test('BT2 every Fleet colour has its light and dark value on the board', () => {
  const tokens = JSON.parse(readFileSync(new URL('../../packages/ui/tokens.json', import.meta.url), 'utf8'));
  const ds = boardDs();
  for (const [k, light] of Object.entries(tokens.color.light)) assert.ok(ds.includes(`fleet.${k}: color(${light}, dark: ${tokens.color.dark[k] ?? light})`), k);
});
