// Virtualized hierarchical catalogue tree (1–3 levels) with pinned hoisting and keyboard navigation.
// Specs: docs/design-system/controls/catalogue-tree.md (OR-9, OR-12, OR-22, OR-37, OR-45).
import type { ComponentChildren } from 'preact';
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks';
import { type Family, type IconName, actions, states } from '../registry.ts';
import { matchSearch } from '../search.ts';
import { VirtualList, type VirtualListHandle } from '../virtual-list.tsx';
import tokens from '../../tokens.json';
import { Icon, StateBadge, Empty } from './primitives.tsx';
import { RowCheck, RowPin } from './lists.tsx';

export type CatalogueTreeNodeState = {
  family: Family;
  state: string;
  reason?: string;
};

export type CatalogueTreeNode = {
  id: string;
  label: string;
  sub?: string;
  icon?: IconName;
  state?: CatalogueTreeNodeState;
  /** Right-aligned marker (OR-45 delta badge, metric score, or run count). */
  right?: ComponentChildren;
  /** Folder/collection children; when defined, the node is treated as a folder. */
  children?: CatalogueTreeNode[];
  /** Explicit folder item count (otherwise derived from descendant leaves). */
  count?: number;
  /** Folder starts collapsed when uncontrolled / absent from `collapsed` map. */
  defaultCollapsed?: boolean;
  /** Leaf is hoisted into the top Pinned section (OR-12). */
  pinned?: boolean;
  /** Additional searchable terms (tags, codes, OR-45 overrides). */
  searchText?: string;
  /** Optional href for row navigation. */
  href?: string;
  disabled?: boolean;
  disabledReason?: string;
  /** Marks the row with attention background (e.g. unconfirmed or problem state). */
  problem?: boolean;
};

export type FlattenCatalogueTreeOptions = {
  query?: string;
  collapsed?: Readonly<Record<string, boolean>>;
  pinnedId?: string;
  pinnedLabel?: string;
  match?: (node: CatalogueTreeNode, query: string) => boolean;
};

export type FlattenedCatalogueRow = {
  key: string;
  node: CatalogueTreeNode;
  depth: 0 | 1 | 2;
  isFolder: boolean;
  isPinnedSection?: boolean;
  isPinned?: boolean;
  collapsed: boolean;
  parentId: string | null;
  count: number;
};

export type CatalogueTreeProps = {
  nodes: CatalogueTreeNode[];
  label: string;
  selectedId?: string | null;
  selectedIds?: readonly string[];
  mode?: 'single' | 'multiple';
  onSelect?: (node: CatalogueTreeNode) => void;
  onToggleSelect?: (node: CatalogueTreeNode) => void;
  onConfirm?: (node: CatalogueTreeNode) => void;
  query?: string;
  collapsed?: Readonly<Record<string, boolean>>;
  onToggle?: (folderId: string, collapsed: boolean) => void;
  onPin?: (node: CatalogueTreeNode) => void;
  onRowMenu?: (node: CatalogueTreeNode, at: { x: number; y: number }) => void;
  rowHeight?: number;
  fill?: boolean;
  class?: string;
  emptyText?: string;
  handle?: { current: VirtualListHandle | null };
};

const DEFAULT_ROW_HEIGHT = parseInt(tokens.size.row);
const TOUCH_ROW_HEIGHT = parseInt(tokens.size['row-phone']);
const defaultRowHeight = () =>
  typeof matchMedia === 'function' && matchMedia('(pointer: coarse)').matches ? TOUCH_ROW_HEIGHT : DEFAULT_ROW_HEIGHT;

const isFolderNode = (node: CatalogueTreeNode): boolean => Array.isArray(node.children);

const clampDepth = (depth: number): 0 | 1 | 2 => (depth <= 0 ? 0 : depth >= 2 ? 2 : 1);

const defaultMatchNode = (node: CatalogueTreeNode, query: string): boolean => {
  const haystack = [node.label, node.id, node.sub, node.searchText].filter(Boolean).join(' ');
  return matchSearch(query, haystack, node.state ? { state: node.state.state } : {});
};

const hasCollapsedKey = (collapsed: Readonly<Record<string, boolean>> | undefined, id: string): boolean =>
  !!collapsed && Object.prototype.hasOwnProperty.call(collapsed, id);

const resolveCollapsed = (
  id: string,
  defaultCollapsed: boolean | undefined,
  collapsed: Readonly<Record<string, boolean>> | undefined,
): boolean => (hasCollapsedKey(collapsed, id) ? !!collapsed![id] : !!defaultCollapsed);

type FilteredNode = {
  node: CatalogueTreeNode;
  children: FilteredNode[];
  leafCount: number;
};

function collectPinnedLeaves(
  nodes: readonly CatalogueTreeNode[],
  query: string,
  matchNode: (node: CatalogueTreeNode, query: string) => boolean,
  out: CatalogueTreeNode[],
) {
  for (const node of nodes) {
    if (isFolderNode(node)) {
      collectPinnedLeaves(node.children!, query, matchNode, out);
    } else if (node.pinned && (!query || matchNode(node, query))) {
      out.push(node);
    }
  }
}

function countUnpinnedLeaves(nodes: readonly CatalogueTreeNode[]): number {
  let total = 0;
  for (const node of nodes) {
    if (isFolderNode(node)) total += countUnpinnedLeaves(node.children!);
    else if (!node.pinned) total++;
  }
  return total;
}

function buildAllUnpinned(nodes: readonly CatalogueTreeNode[]): FilteredNode[] {
  const out: FilteredNode[] = [];
  for (const node of nodes) {
    if (isFolderNode(node)) {
      const children = buildAllUnpinned(node.children!);
      const leafCount = children.reduce((acc, c) => acc + c.leafCount, 0);
      out.push({ node, children, leafCount });
    } else if (!node.pinned) {
      out.push({ node, children: [], leafCount: 1 });
    }
  }
  return out;
}

function filterNodesForQuery(
  nodes: readonly CatalogueTreeNode[],
  query: string,
  matchNode: (node: CatalogueTreeNode, query: string) => boolean,
): FilteredNode[] {
  const hasQuery = query.length > 0;
  const out: FilteredNode[] = [];
  for (const node of nodes) {
    if (!isFolderNode(node)) {
      if (!node.pinned && (!hasQuery || matchNode(node, query))) {
        out.push({ node, children: [], leafCount: 1 });
      }
      continue;
    }
    const rawChildren = node.children!;
    const hadOriginalChildren = rawChildren.length > 0;
    const filteredChildren = filterNodesForQuery(rawChildren, query, matchNode);
    if (!hasQuery) {
      // Omit folders whose children were all hoisted to Pinned, while preserving originally empty folders.
      if (hadOriginalChildren && filteredChildren.length === 0) continue;
      const leafCount = filteredChildren.reduce((acc, c) => acc + c.leafCount, 0);
      out.push({ node, children: filteredChildren, leafCount });
      continue;
    }
    if (filteredChildren.length > 0) {
      const leafCount = filteredChildren.reduce((acc, c) => acc + c.leafCount, 0);
      out.push({ node, children: filteredChildren, leafCount });
    } else if (matchNode(node, query) && countUnpinnedLeaves(rawChildren) > 0) {
      // Searching by folder/suite name keeps its unpinned children visible and expanded.
      const children = buildAllUnpinned(rawChildren);
      const leafCount = children.reduce((acc, c) => acc + c.leafCount, 0);
      out.push({ node, children, leafCount });
    }
  }
  return out;
}

/**
 * Flattens a 1–3 level catalogue tree into visible virtual-list rows.
 * Lifts `pinned` leaf nodes into a top `Pinned` section (OR-12), clamps depth to `0..2`,
 * and auto-expands matching non-empty folders when `query` is non-empty without mutating `collapsed`.
 */
export function flattenCatalogueTree(
  nodes: readonly CatalogueTreeNode[],
  opts: FlattenCatalogueTreeOptions = {},
): FlattenedCatalogueRow[] {
  const query = (opts.query ?? '').trim();
  const hasQuery = query.length > 0;
  const pinnedId = opts.pinnedId ?? '__pinned__';
  const pinnedLabel = opts.pinnedLabel ?? 'Pinned';
  const matchNode = opts.match ?? defaultMatchNode;
  const rows: FlattenedCatalogueRow[] = [];

  const pinnedLeaves: CatalogueTreeNode[] = [];
  collectPinnedLeaves(nodes, query, matchNode, pinnedLeaves);
  if (pinnedLeaves.length > 0) {
    const pinnedCollapsed = hasQuery ? false : resolveCollapsed(pinnedId, false, opts.collapsed);
    rows.push({
      key: pinnedId,
      node: {
        id: pinnedId,
        label: pinnedLabel,
        icon: 'pin',
        children: pinnedLeaves,
        count: pinnedLeaves.length,
      },
      depth: 0,
      isFolder: true,
      isPinnedSection: true,
      collapsed: pinnedCollapsed,
      parentId: null,
      count: pinnedLeaves.length,
    });
    if (!pinnedCollapsed) {
      for (const leaf of pinnedLeaves) {
        rows.push({
          key: leaf.id,
          node: leaf,
          depth: 1,
          isFolder: false,
          isPinned: true,
          collapsed: false,
          parentId: pinnedId,
          count: 0,
        });
      }
    }
  }

  const filtered = filterNodesForQuery(nodes, query, matchNode);
  const emit = (items: readonly FilteredNode[], rawDepth: number, parentId: string | null) => {
    const depth = clampDepth(rawDepth);
    for (const item of items) {
      const { node } = item;
      const folder = isFolderNode(node);
      if (!folder) {
        rows.push({
          key: node.id,
          node,
          depth,
          isFolder: false,
          isPinned: false,
          collapsed: false,
          parentId,
          count: 0,
        });
        continue;
      }
      const collapsed = hasQuery ? false : resolveCollapsed(node.id, node.defaultCollapsed, opts.collapsed);
      const count = hasQuery ? item.leafCount : (node.count ?? item.leafCount);
      rows.push({
        key: node.id,
        node,
        depth,
        isFolder: true,
        collapsed,
        parentId,
        count,
      });
      if (!collapsed) {
        emit(item.children, rawDepth + 1, node.id);
      }
    }
  };
  emit(filtered, 0, null);
  return rows;
}

export function CatalogueTree(props: CatalogueTreeProps) {
  const mode = props.mode ?? 'single';
  const rowHeight = props.rowHeight ?? defaultRowHeight();
  const [ownCollapsed, setOwnCollapsed] = useState<Record<string, boolean>>({});
  const collapsedMap = props.collapsed ?? ownCollapsed;

  const rows = useMemo(
    () => flattenCatalogueTree(props.nodes, { query: props.query, collapsed: collapsedMap }),
    [props.nodes, props.query, collapsedMap],
  );

  const selectedSet = useMemo(() => new Set(props.selectedIds ?? []), [props.selectedIds]);
  const [focusedKey, setFocusedKey] = useState<string | null>(props.selectedId ?? null);
  const treeRef = useRef<HTMLDivElement>(null);
  const ownHandle = useRef<VirtualListHandle | null>(null);
  const listHandle = props.handle ?? ownHandle;

  useEffect(() => {
    if (props.selectedId) setFocusedKey(props.selectedId);
  }, [props.selectedId]);

  const activeIndex = useMemo(() => {
    if (!rows.length) return -1;
    if (focusedKey) {
      const i = rows.findIndex(r => r.key === focusedKey);
      if (i >= 0) return i;
    }
    if (props.selectedId) {
      const i = rows.findIndex(r => r.node.id === props.selectedId);
      if (i >= 0) return i;
    }
    return 0;
  }, [rows, focusedKey, props.selectedId]);

  // Keep VirtualList's internal grid/row roles transparent inside role="tree" so assistive tech sees a single tree.
  useLayoutEffect(() => {
    const root = treeRef.current;
    if (!root) return;
    const syncRoles = () => {
      const vl = root.querySelector<HTMLElement>('.vl');
      if (vl) {
        if (vl.getAttribute('role') !== 'presentation') vl.setAttribute('role', 'presentation');
        if (vl.getAttribute('tabindex') !== '-1') vl.setAttribute('tabindex', '-1');
        if (vl.hasAttribute('aria-rowcount')) vl.removeAttribute('aria-rowcount');
      }
      for (const r of root.querySelectorAll<HTMLElement>('.vl-row')) {
        if (r.getAttribute('role') !== 'presentation') r.setAttribute('role', 'presentation');
        if (r.hasAttribute('aria-rowindex')) r.removeAttribute('aria-rowindex');
      }
    };
    syncRoles();
    const space = root.querySelector('.vl-space');
    if (!space || typeof MutationObserver === 'undefined') return;
    const obs = new MutationObserver(syncRoles);
    obs.observe(space, { childList: true });
    return () => obs.disconnect();
  });

  const toggleFolder = (row: FlattenedCatalogueRow, nextCollapsed = !row.collapsed) => {
    if (props.collapsed === undefined) {
      setOwnCollapsed(prev => ({ ...prev, [row.node.id]: nextCollapsed }));
    }
    props.onToggle?.(row.node.id, nextCollapsed);
  };

  const focusKey = (key: string, align: 'start' | 'center' | 'end' = 'center') => {
    setFocusedKey(key);
    listHandle.current?.scrollToKey(key, align);
    requestAnimationFrame(() => {
      const el = treeRef.current?.querySelector<HTMLElement>(`[data-tree-key="${CSS.escape(key)}"]`);
      el?.focus({ preventScroll: true });
    });
  };

  const handleRowActivate = (row: FlattenedCatalogueRow) => {
    if (row.node.disabled) return;
    if (mode === 'multiple' && !row.isFolder) {
      (props.onToggleSelect ?? props.onSelect)?.(row.node);
      return;
    }
    if (props.onSelect) {
      props.onSelect(row.node);
    } else if (row.isFolder) {
      toggleFolder(row);
    } else if (row.node.href) {
      location.assign(row.node.href);
    }
  };

  const onTreeKeyDown = (e: KeyboardEvent) => {
    if (e.altKey || e.ctrlKey || e.metaKey) return;
    const target = e.target as HTMLElement | null;
    if (target?.closest('input,textarea')) return;
    if (!rows.length || activeIndex < 0) return;
    const cur = rows[activeIndex];
    if (!cur) return;

    switch (e.key) {
      case 'ArrowDown':
      case 'j': {
        e.preventDefault();
        const next = rows[Math.min(rows.length - 1, activeIndex + 1)];
        if (next) focusKey(next.key, 'end');
        break;
      }
      case 'ArrowUp':
      case 'k': {
        e.preventDefault();
        const prev = rows[Math.max(0, activeIndex - 1)];
        if (prev) focusKey(prev.key, 'start');
        break;
      }
      case 'Home': {
        e.preventDefault();
        focusKey(rows[0].key, 'start');
        break;
      }
      case 'End': {
        e.preventDefault();
        focusKey(rows[rows.length - 1].key, 'end');
        break;
      }
      case 'ArrowRight': {
        if (!cur.isFolder) return;
        e.preventDefault();
        if (cur.collapsed) {
          toggleFolder(cur, false);
        } else {
          const child = rows[activeIndex + 1];
          if (child && child.parentId === cur.node.id) focusKey(child.key, 'end');
        }
        break;
      }
      case 'ArrowLeft': {
        e.preventDefault();
        if (cur.isFolder && !cur.collapsed) {
          toggleFolder(cur, true);
        } else if (cur.parentId) {
          const parent = rows.find(r => r.node.id === cur.parentId);
          if (parent) focusKey(parent.key, 'start');
        }
        break;
      }
      case 'Enter': {
        if (cur.node.disabled) return;
        e.preventDefault();
        if (props.onConfirm) props.onConfirm(cur.node);
        else handleRowActivate(cur);
        break;
      }
      case ' ': {
        if (cur.node.disabled) return;
        e.preventDefault();
        if (mode === 'multiple' && !cur.isFolder) {
          (props.onToggleSelect ?? props.onSelect)?.(cur.node);
        } else {
          handleRowActivate(cur);
        }
        break;
      }
    }
  };

  const leafCount = useMemo(() => rows.filter(r => !r.isFolder).length, [rows]);
  const hasQuery = !!(props.query && props.query.trim());
  const statusMessage = hasQuery ? `${leafCount} matching ${leafCount === 1 ? 'item' : 'items'}` : '';

  const renderRow = (row: FlattenedCatalogueRow, index: number) => {
    const { node } = row;
    const isActive = index === activeIndex;
    const isSelected = mode === 'multiple'
      ? (!row.isFolder && selectedSet.has(node.id)) || props.selectedId === node.id
      : props.selectedId === node.id;
    const st = node.state && node.state.state in states[node.state.family] ? node.state : undefined;

    return (
      <div
        role="treeitem"
        data-tree-key={row.key}
        data-node-id={node.id}
        aria-level={row.depth + 1}
        aria-expanded={row.isFolder ? !row.collapsed : undefined}
        aria-selected={isSelected}
        aria-disabled={node.disabled || undefined}
        data-tip={node.disabled ? node.disabledReason : undefined}
        tabIndex={isActive ? 0 : -1}
        class={`cat-row depth-${row.depth}${row.isFolder ? ' folder' : ' leaf'}${node.disabled ? ' disabled' : ''}`}
        onFocus={() => setFocusedKey(row.key)}
        onClick={() => handleRowActivate(row)}
      >
        {row.isFolder ? (
          <>
            <button
              type="button"
              class="tw cat-tw"
              tabIndex={-1}
              aria-label={row.collapsed ? `Expand ${node.label}` : `Collapse ${node.label}`}
              data-dir={row.collapsed ? 'right' : 'down'}
              onClick={e => {
                e.stopPropagation();
                toggleFolder(row);
              }}
            >
              <span class={row.collapsed ? 'chev' : 'chev open'}>
                <Icon name={actions.expand} />
              </span>
            </button>
            <Icon name={node.icon ?? 'folder-open'} />
            <span class="cat-label">{node.label}</span>
            {node.sub && <span class="cat-sub muted">{node.sub}</span>}
            <span class="cat-right">
              {node.right}
              <span class="cat-count num">{row.count.toLocaleString('en')}</span>
            </span>
          </>
        ) : (
          <>
            {mode === 'multiple' && (
              <RowCheck
                checked={selectedSet.has(node.id)}
                label={`Select ${node.label}`}
                onToggle={() => {
                  if (!node.disabled) (props.onToggleSelect ?? props.onSelect)?.(node);
                }}
              />
            )}
            {row.isPinned && props.onPin && (
              <RowPin label={node.label} onUnpin={() => props.onPin!(node)} />
            )}
            {st ? (
              <StateBadge family={st.family} state={st.state as never} reason={st.reason} compact />
            ) : node.icon ? (
              <span class="cat-ic"><Icon name={node.icon} /></span>
            ) : null}
            {st && node.icon && <span class="cat-ic"><Icon name={node.icon} /></span>}
            {node.href ? (
              <a
                class="cat-link"
                href={node.href}
                tabIndex={-1}
                onClick={e => {
                  if (node.disabled) { e.preventDefault(); return; }
                  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) {
                    e.stopPropagation();
                    return;
                  }
                  if (props.onSelect || mode === 'multiple') e.preventDefault();
                }}
              >
                {node.label}
              </a>
            ) : (
              <span class="cat-label">{node.label}</span>
            )}
            {node.sub && <span class="cat-sub muted">{node.sub}</span>}
            {node.right !== undefined && node.right !== null && <span class="cat-right">{node.right}</span>}
          </>
        )}
      </div>
    );
  };

  return (
    <div
      ref={treeRef}
      class={`cat-tree${props.fill ? ' fill' : ''}${props.class ? ' ' + props.class : ''}`}
      data-ui="catalogue-tree"
      role="tree"
      aria-label={props.label}
      aria-multiselectable={mode === 'multiple' || undefined}
      onKeyDown={onTreeKeyDown}
    >
      {rows.length === 0 ? (
        <Empty text={props.emptyText ?? (hasQuery ? 'No matching items.' : 'No items.')} />
      ) : (
        <VirtualList
          items={rows}
          fill={props.fill}
          getKey={r => r.key}
          rowHeight={rowHeight}
          follow="top"
          label={props.label}
          handle={listHandle}
          rowClass={r => {
            const sel = mode === 'multiple'
              ? !r.isFolder && selectedSet.has(r.node.id)
              : props.selectedId === r.node.id;
            return [
              r.isFolder ? 'cat-folder-row' : 'cat-leaf-row',
              sel ? (mode === 'multiple' ? 'checked' : 'sel') : '',
              r.node.problem ? 'cat-problem' : '',
            ].filter(Boolean).join(' ');
          }}
          onRowMenu={props.onRowMenu ? (r, at) => props.onRowMenu!(r.node, at) : undefined}
          render={renderRow}
        />
      )}
      <span class="sr-only" aria-live="polite">{statusMessage}</span>
    </div>
  );
}
