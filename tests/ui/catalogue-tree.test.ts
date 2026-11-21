// CatalogueTree flatten invariants (docs/design-system/controls/catalogue-tree.md, OR-12, OR-33).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { build } from 'esbuild';
import type { CatalogueTreeNode, FlattenCatalogueTreeOptions, FlattenedCatalogueRow } from '../../packages/ui/src/controls/catalogue-tree.tsx';

const bundle = await build({
  stdin: {
    contents: "export { flattenCatalogueTree } from './packages/ui/src/controls/catalogue-tree.tsx';",
    resolveDir: process.cwd(),
    loader: 'ts',
  },
  bundle: true,
  write: false,
  platform: 'node',
  format: 'esm',
  jsx: 'automatic',
  jsxImportSource: 'preact',
  loader: { '.json': 'json' },
});
const mod = await import('data:text/javascript;base64,' + Buffer.from(bundle.outputFiles[0].text).toString('base64')) as {
  flattenCatalogueTree: (nodes: readonly CatalogueTreeNode[], opts?: FlattenCatalogueTreeOptions) => FlattenedCatalogueRow[];
};
const { flattenCatalogueTree } = mod;

test('flattenCatalogueTree hoists pinned leaves, clamps depth to 0..2, respects collapsed vs defaultCollapsed, and auto-expands search without mutating caller state (requirement: catalogue-tree.md; risk: pinned items stay buried inside collapsed folders, deep nesting overflows 3 levels, or search filtering mutates persisted collapse preferences)', () => {
  const tree: CatalogueTreeNode[] = [
    {
      id: 'suite-a',
      label: 'Suite A',
      defaultCollapsed: true,
      children: [
        { id: 'uc-pinned', label: 'Pinned Use Case', pinned: true },
        {
          id: 'group-b',
          label: 'Group B',
          children: [
            {
              id: 'sub-c',
              label: 'Sub C',
              children: [
                { id: 'deep-leaf', label: 'Deep Leaf', searchText: 'buried-needle' },
              ],
            },
          ],
        },
      ],
    },
    {
      id: 'suite-empty',
      label: 'Empty Suite',
      children: [],
    },
  ];

  // 1. Default collapsed hides non-pinned children, yet pinned leaves hoist to top Pinned section.
  const collapsedByDefault = flattenCatalogueTree(tree);
  assert.deepEqual(
    collapsedByDefault.map(r => [r.key, r.depth, r.isFolder]),
    [
      ['__pinned__', 0, true],
      ['uc-pinned', 1, false],
      ['suite-a', 0, true],
      ['suite-empty', 0, true],
    ],
  );
  assert.equal(collapsedByDefault[0].count, 1);
  assert.equal(collapsedByDefault[2].collapsed, true);

  // 2. Controlled collapsed overrides defaultCollapsed and clamps any depth > 2 down to 2.
  const callerCollapsed: Record<string, boolean> = { 'suite-a': false };
  const snapshot = { ...callerCollapsed };
  const expanded = flattenCatalogueTree(tree, { collapsed: callerCollapsed });
  assert.deepEqual(
    expanded.map(r => [r.key, r.depth, r.isFolder]),
    [
      ['__pinned__', 0, true],
      ['uc-pinned', 1, false],
      ['suite-a', 0, true],
      ['group-b', 1, true],
      ['sub-c', 2, true],
      ['deep-leaf', 2, false],
      ['suite-empty', 0, true],
    ],
  );
  for (const row of expanded) {
    assert.ok(row.depth >= 0 && row.depth <= 2, `depth ${row.depth} out of 0..2`);
  }

  // 3. Non-empty search auto-expands matching folders, hides empty/non-matching folders,
  //    clamps depth <= 2, and never mutates caller's collapsed map.
  const callerDefaultCollapsed: Record<string, boolean> = { 'suite-a': true, 'group-b': true, '__pinned__': true };
  const beforeSearch = { ...callerDefaultCollapsed };
  const searched = flattenCatalogueTree(tree, { query: 'buried-needle', collapsed: callerDefaultCollapsed });
  assert.deepEqual(
    searched.map(r => [r.key, r.depth, r.collapsed]),
    [
      ['suite-a', 0, false],
      ['group-b', 1, false],
      ['sub-c', 2, false],
      ['deep-leaf', 2, false],
    ],
  );
  assert.deepEqual(callerDefaultCollapsed, beforeSearch);

  // 4. Search matching a pinned leaf auto-expands Pinned section without mutating caller state.
  const searchedPinned = flattenCatalogueTree(tree, { query: 'pinned', collapsed: callerDefaultCollapsed });
  assert.deepEqual(
    searchedPinned.map(r => [r.key, r.depth, r.collapsed]),
    [
      ['__pinned__', 0, false],
      ['uc-pinned', 1, false],
    ],
  );
  assert.deepEqual(callerDefaultCollapsed, beforeSearch);
  assert.deepEqual(callerCollapsed, snapshot);
});

test('CatalogueTree fill modifier uses cat-tree-fill and 100% width instead of .fill capacity bar class (requirement: catalogue-tree.md; risk: .cat-tree.fill matches .fill { width: var(--size-fill) } and locks tree width to 160px)', () => {
  const tsx = readFileSync('packages/ui/src/controls/catalogue-tree.tsx', 'utf8');
  const css = readFileSync('packages/ui/src/styles/controls.css', 'utf8');

  assert.match(tsx, /class=\{`cat-tree\$\{props\.fill \? ' cat-tree-fill' : ''\}/);
  assert.doesNotMatch(tsx, /class=\{`cat-tree\$\{props\.fill \? ' fill' : ''\}/);

  assert.match(css, /\.cat-tree\s*\{[^}]*\bwidth:\s*100%/);
  assert.match(css, /\.cat-tree\.cat-tree-fill,\s*\.cat-tree\.cat-tree-fill\s+\.vl-wrap\s*\{[^}]*\bwidth:\s*100%/);
  assert.doesNotMatch(css, /\.cat-tree\.fill\b/);
});
