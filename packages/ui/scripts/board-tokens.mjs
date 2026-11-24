// Turns tokens.json into the design board's style file (docs/design-system/board/Styles/default.ds, Brilliant DSL),
// so board drawings resolve the same colours, sizes and type as packages/ui. Run: pnpm run board:tokens.
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

export const BOARD_DS = new URL('../../../docs/design-system/board/Styles/default.ds', import.meta.url);
const px = v => Number(String(v).replace(/px$/, ''));

// Brilliant's semantic roles, read by its own blocks and by $color.* rows, mapped onto Fleet colour tokens.
const ROLES = [
  ['color.surface', 'bg'], ['color.surface.container', 'surface'], ['color.surface.container.high', 'surface-sunken'],
  ['color.on-surface', 'fg'], ['color.surface.hover', 'hover'], ['color.surface.pressed', 'selected'],
  ['color.surface.selected', 'selected'], ['color.on-surface.selected', 'fg'],
  ['color.outline', 'line'], ['color.outline.variant', 'line-soft'],
  ['color.text.primary', 'fg'], ['color.text.secondary', 'fg-muted'], ['color.text.disabled', 'fg-faint'],
  ['color.text.display', 'fg'], ['color.text.display.alt', 'fg-muted'],
  ['color.primary', 'accent'], ['color.on-primary', 'on-accent'], ['color.primary.container', 'selected'],
  ['color.success', 'success-fg'], ['color.on-success', 'bg'], ['color.success.container', 'success-bg'],
  ['color.error', 'danger-fg'], ['color.on-error', 'bg'], ['color.error.container', 'danger-bg'],
  ['color.warning', 'attention-fg'], ['color.on-warning', 'bg'], ['color.warning.container', 'attention-bg'],
  ['color.info', 'running-fg'], ['color.on-info', 'bg'], ['color.info.container', 'running-bg'],
];

export function boardDs(t = JSON.parse(readFileSync(new URL('../tokens.json', import.meta.url)))) {
  const { light, dark } = t.color;
  const L = [];
  L.push('ds:v1');
  L.push('// Fleet design tokens for the design board. GENERATED from packages/ui/tokens.json by');
  L.push('// packages/ui/scripts/board-tokens.mjs (pnpm run board:tokens); edit tokens.json, never this file.');
  L.push('// Each colour is one Fleet token with its light and dark value. An unstamped frame resolves light;');
  L.push('// board frames start with ds(, theme(dark)), since the owner works in the dark theme.');
  L.push('');
  L.push('modes {');
  L.push('  theme: [dark, light]');
  L.push('}');
  L.push('');
  L.push('// Fleet colour tokens: fleet.<name> is var(--<name>) in packages/ui. Seeds only; read them bare ($fleet.bg).');
  for (const k of Object.keys(light)) L.push(`fleet.${k}: color(${light[k]}, dark: ${dark[k] ?? light[k]})`);
  L.push('');
  L.push("// Brilliant's semantic roles mapped onto Fleet tokens, so $color.* rows draw in Fleet colours.");
  for (const [role, k] of ROLES) L.push(`${role}: fleet.${k}`);
  L.push('');
  L.push('// Type. packages/ui uses the platform font (system-ui); the board draws with Inter, the closest bundled face.');
  L.push('font.family:      Inter');
  L.push('font.family.sans: font.family');
  L.push('font.family.mono: "JetBrains Mono"');
  const f = t.font;
  L.push(`font.size:        tshirt(number([${['size-xs', 'size-sm', 'size-md', 'size-lg', 'size-xl', 'size-2xl'].map(k => px(f[k])).join(', ')}]), transforms: none)`);
  L.push(`font.lineHeight:  ${f.line}`);
  L.push(`font.weight.regular: ${f['weight-regular']}`);
  L.push(`font.weight.medium:  ${f['weight-medium']}`);
  L.push(`font.weight.bold:    ${f['weight-bold']}`);
  L.push('');
  L.push('// Spacing: space.1 to space.7 of packages/ui, as xs, sm, md, lg, xl, 2xl, 3xl.');
  L.push(`spacing: tshirt(number([${Object.values(t.space).map(px).join(', ')}]), min: { none: 0 }, transforms: none)`);
  L.push('');
  L.push('// Strokes: 1 px borders (line, line-soft) and the focus ring.');
  L.push('stroke.width.subtle: 1');
  L.push(`stroke.width.mid: ${px(t.size['focus-ring'])}`);
  L.push('');
  L.push('// Radius and fixed sizes: Fleet is square-cornered and dense.');
  for (const [k, v] of Object.entries(t.radius)) L.push(`fleet.radius.${k}: ${px(v)}`);
  for (const [k, v] of Object.entries(t.size)) L.push(`fleet.size.${k}: ${px(v)}`);
  L.push('');
  return L.join('\n');
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  writeFileSync(BOARD_DS, boardDs());
  console.log(`wrote ${fileURLToPath(BOARD_DS)}`);
}
