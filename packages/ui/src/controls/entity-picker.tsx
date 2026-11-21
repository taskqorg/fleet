// Inline single/multiple entity picker combining a chip trigger with a searchable CatalogueTree.
// Specs: docs/design-system/controls/entity-picker.md (OR-9, OR-14, OR-38, OR-45).
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks';
import { actions } from '../registry.ts';
import { Icon, Button, ErrorBanner, Skeleton } from './primitives.tsx';
import { SearchField } from './thread.tsx';
import { CatalogueTree, flattenCatalogueTree, type CatalogueTreeNode } from './catalogue-tree.tsx';

export type EntityPickerPreset = {
  id: string;
  label: string;
  ids: string[];
};

export type EntityPickerBaseProps = {
  label: string;
  nodes: CatalogueTreeNode[];
  /** Maximum visible chips before collapsing overflow into `+N more` (default 4). */
  maxChips?: number;
  presets?: EntityPickerPreset[];
  placeholder?: string;
  searchPlaceholder?: string;
  disabledReason?: string;
  loading?: boolean;
  error?: string | null;
  onRetry?: () => void;
  /** Optional archived entries revealed only when `Include archived` is toggled on. */
  archivedNodes?: CatalogueTreeNode[];
  collapsed?: Readonly<Record<string, boolean>>;
  onToggleFolder?: (folderId: string, collapsed: boolean) => void;
  onPin?: (node: CatalogueTreeNode) => void;
  class?: string;
};

export type EntityPickerProps =
  | (EntityPickerBaseProps & { mode?: 'single'; value: string | null; onChange: (value: string | null) => void })
  | (EntityPickerBaseProps & { mode: 'multiple'; value: readonly string[]; onChange: (value: string[]) => void });

const DEFAULT_MAX_CHIPS = 4;

const sameIdSet = (a: readonly string[], b: readonly string[]) =>
  a.length === b.length && a.every(id => b.includes(id));

function indexNodes(nodes: readonly CatalogueTreeNode[], map: Map<string, CatalogueTreeNode>) {
  for (const n of nodes) {
    map.set(n.id, n);
    if (n.children) indexNodes(n.children, map);
  }
}

export function EntityPicker(props: EntityPickerProps) {
  const mode = props.mode ?? 'single';
  const maxChips = props.maxChips ?? DEFAULT_MAX_CHIPS;
  const disabled = !!props.disabledReason;

  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [includeArchived, setIncludeArchived] = useState(false);
  const [ownCollapsed, setOwnCollapsed] = useState<Record<string, boolean>>({});

  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const baseId = useId();
  const labelId = `${baseId}-label`;
  const triggerId = `${baseId}-trigger`;
  const panelId = `${baseId}-panel`;

  const selectedIds = useMemo<readonly string[]>(() => {
    if (props.mode === 'multiple') return props.value;
    return props.value ? [props.value] : [];
  }, [props.mode, props.value]);

  const nodeMap = useMemo(() => {
    const map = new Map<string, CatalogueTreeNode>();
    indexNodes(props.nodes, map);
    if (props.archivedNodes) indexNodes(props.archivedNodes, map);
    return map;
  }, [props.nodes, props.archivedNodes]);

  const activeNodes = useMemo(() => {
    if (!includeArchived || !props.archivedNodes?.length) return props.nodes;
    return [...props.nodes, ...props.archivedNodes];
  }, [props.nodes, props.archivedNodes, includeArchived]);

  const collapsedMap = props.collapsed ?? ownCollapsed;

  const closeAndRefocus = () => {
    setOpen(false);
    setQuery('');
    requestAnimationFrame(() => triggerRef.current?.focus());
  };

  useLayoutEffect(() => {
    if (!open) return;
    panelRef.current?.querySelector<HTMLInputElement>('.search input')?.focus();
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) {
        setOpen(false);
        setQuery('');
      }
    };
    document.addEventListener('pointerdown', onPointerDown);
    return () => document.removeEventListener('pointerdown', onPointerDown);
  }, [open]);

  const setSingle = (id: string | null) => {
    if (props.mode !== 'multiple') props.onChange(id);
  };

  const setMultiple = (ids: string[]) => {
    if (props.mode === 'multiple') props.onChange(ids);
  };

  const removeId = (id: string) => {
    if (disabled) return;
    if (mode === 'multiple') setMultiple(selectedIds.filter(x => x !== id));
    else setSingle(null);
  };

  const toggleLeaf = (node: CatalogueTreeNode) => {
    if (disabled || node.disabled) return;
    if (mode === 'single') {
      setSingle(node.id);
      closeAndRefocus();
      return;
    }
    const next = selectedIds.includes(node.id)
      ? selectedIds.filter(x => x !== node.id)
      : [...selectedIds, node.id];
    setMultiple(next);
  };

  const toggleFolder = (folderId: string, nextCollapsed: boolean) => {
    if (props.collapsed === undefined) {
      setOwnCollapsed(prev => ({ ...prev, [folderId]: nextCollapsed }));
    }
    props.onToggleFolder?.(folderId, nextCollapsed);
  };

  const handleTreeSelect = (node: CatalogueTreeNode) => {
    if (Array.isArray(node.children)) {
      const current = collapsedMap[node.id] ?? node.defaultCollapsed ?? false;
      toggleFolder(node.id, !current);
      return;
    }
    toggleLeaf(node);
  };

  const handleTreeConfirm = (node: CatalogueTreeNode) => {
    if (Array.isArray(node.children)) {
      const current = collapsedMap[node.id] ?? node.defaultCollapsed ?? false;
      toggleFolder(node.id, !current);
      return;
    }
    if (mode === 'single') {
      toggleLeaf(node);
    } else {
      closeAndRefocus();
    }
  };

  const handlePreset = (preset: EntityPickerPreset) => {
    if (disabled) return;
    if (mode === 'single') {
      setSingle(preset.ids[0] ?? null);
      closeAndRefocus();
      return;
    }
    setMultiple(sameIdSet(selectedIds, preset.ids) ? [] : [...preset.ids]);
  };

  const handleSearchConfirm = () => {
    if (mode !== 'single') return;
    const flat = flattenCatalogueTree(activeNodes, { query, collapsed: collapsedMap });
    const firstLeaf = flat.find(r => !r.isFolder && !r.node.disabled);
    if (firstLeaf) toggleLeaf(firstLeaf.node);
  };

  const onPanelKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      closeAndRefocus();
      return;
    }
    const target = e.target as HTMLElement | null;
    if (e.key === 'Backspace' && mode === 'multiple' && query === '' && selectedIds.length > 0) {
      if (!target?.closest('input,textarea') || target.matches('.search input')) {
        e.preventDefault();
        setMultiple(selectedIds.slice(0, -1));
        return;
      }
    }
    if (target?.matches('.search input') && e.key === 'ArrowDown') {
      const firstTreeItem = panelRef.current?.querySelector<HTMLElement>('[role="treeitem"][tabindex="0"]')
        ?? panelRef.current?.querySelector<HTMLElement>('[role="treeitem"]');
      if (firstTreeItem) {
        e.preventDefault();
        firstTreeItem.focus();
      }
    }
  };

  const visibleChips = selectedIds.slice(0, maxChips);
  const overflowCount = Math.max(0, selectedIds.length - visibleChips.length);

  return (
    <div ref={rootRef} class={`ep${props.class ? ' ' + props.class : ''}`} data-ui="entity-picker">
      <span id={labelId} class="ep-label">{props.label}</span>
      <div
        class={`ep-trigger${open ? ' open' : ''}${disabled ? ' disabled' : ''}`}
        aria-disabled={disabled || undefined}
        data-tip={disabled ? props.disabledReason : undefined}
        onClick={e => {
          if (disabled) return;
          if ((e.target as HTMLElement).closest('button')) return;
          if (open) closeAndRefocus();
          else setOpen(true);
        }}
      >
        <div class="ep-chips" role="list" aria-label={`Selected ${props.label}`}>
          {visibleChips.length === 0 && (
            <span class="ep-placeholder muted">{props.placeholder ?? 'None selected'}</span>
          )}
          {visibleChips.map(id => {
            const node = nodeMap.get(id);
            const chipLabel = node?.label ?? id;
            return (
              <span key={id} role="listitem" class="chip ep-chip">
                {node?.icon && <Icon name={node.icon} />}
                <span class="ep-chip-label">{chipLabel}</span>
                {node?.sub && <span class="ep-chip-sub">{node.sub}</span>}
                {!disabled && (
                  <button
                    type="button"
                    aria-label={`Remove ${chipLabel}`}
                    onClick={e => {
                      e.stopPropagation();
                      removeId(id);
                    }}
                  >
                    <Icon name={actions.clear} />
                  </button>
                )}
              </span>
            );
          })}
          {overflowCount > 0 && (
            <span class="ep-more muted num" aria-label={`${overflowCount} more selected`}>
              +{overflowCount} more
            </span>
          )}
        </div>
        <button
          ref={triggerRef}
          id={triggerId}
          type="button"
          class="ep-btn"
          aria-expanded={open}
          aria-controls={panelId}
          aria-labelledby={`${labelId} ${triggerId}`}
          aria-disabled={disabled || undefined}
          data-tip={disabled ? props.disabledReason : undefined}
          onClick={() => {
            if (disabled) return;
            if (open) closeAndRefocus();
            else setOpen(true);
          }}
        >
          {(selectedIds.length === 0 || mode === 'multiple') && (
            <>
              <Icon name="plus" />
              <span>Select</span>
            </>
          )}
          <span class={open ? 'chev open' : 'chev'}>
            <Icon name={actions.expand} />
          </span>
        </button>
      </div>

      {open && (
        <div
          ref={panelRef}
          id={panelId}
          class="ep-panel"
          role="region"
          aria-label={`${props.label} picker`}
          onKeyDown={onPanelKeyDown}
        >
          <div class="ep-bar">
            <SearchField
              always
              label={props.searchPlaceholder ?? `Search ${props.label.toLowerCase()}`}
              value={query}
              onInput={setQuery}
              onSearch={handleSearchConfirm}
            />
            {props.archivedNodes && props.archivedNodes.length > 0 && (
              <label class="ep-arch">
                <input
                  type="checkbox"
                  class="row-check"
                  checked={includeArchived}
                  onChange={() => setIncludeArchived(v => !v)}
                />
                <span>Include archived</span>
              </label>
            )}
            {mode === 'multiple' && (
              <Button label="Done" kind="quiet" onClick={closeAndRefocus} />
            )}
          </div>

          {props.presets && props.presets.length > 0 && (
            <div class="ep-presets" role="group" aria-label="Preset shortcuts">
              {props.presets.map(preset => {
                const active = mode === 'multiple'
                  ? sameIdSet(selectedIds, preset.ids)
                  : selectedIds[0] === preset.ids[0];
                return (
                  <button
                    key={preset.id}
                    type="button"
                    class={`seg${active ? ' on' : ''}`}
                    aria-pressed={active}
                    onClick={() => handlePreset(preset)}
                  >
                    {preset.label}
                  </button>
                );
              })}
            </div>
          )}

          {props.error ? (
            <ErrorBanner text={props.error} onRetry={props.onRetry} />
          ) : props.loading ? (
            <Skeleton rows={4} />
          ) : (
            <CatalogueTree
              class="ep-tree"
              nodes={activeNodes}
              label={props.label}
              mode={mode}
              selectedId={props.mode !== 'multiple' ? props.value : null}
              selectedIds={mode === 'multiple' ? selectedIds : undefined}
              query={query}
              collapsed={collapsedMap}
              onToggle={toggleFolder}
              onSelect={handleTreeSelect}
              onToggleSelect={toggleLeaf}
              onConfirm={handleTreeConfirm}
              onPin={props.onPin}
            />
          )}
        </div>
      )}
    </div>
  );
}
