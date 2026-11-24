// Shared Lab use-case registry grouping, filtering, tree mapping, and slug helpers.
import type { JSX } from '@solidjs/web';
import type { CatalogueTreeNode, IconName } from '../../../packages/ui/src/index.ts';
import type { LabExampleEntry, LabUseCaseEntry } from '../../../contracts/generated/ops.ts';

export type RegistryUseCase = LabUseCaseEntry & { tags?: readonly string[]; expected_outcome?: string };
export type ExampleUseCaseCandidate = Pick<LabExampleEntry, 'artifactId' | 'name'> & { useCaseId?: string | null };

export interface UseCaseCollection {
  key: 'benchmarks' | 'real-work' | 'fleet-projects';
  title: string;
  items: RegistryUseCase[];
}

export interface GroupedUseCases {
  active: RegistryUseCase[];
  collections: UseCaseCollection[];
  archived: RegistryUseCase[];
}

/** True when a registry entry is obsolete or superseded by another entry. */
export const isArchivedUseCase = (u: RegistryUseCase): boolean =>
  u.status === 'obsolete' || u.status === 'duplicate';

/** True when an active registry entry can be evaluated in Lab. */
export const isLabEligibleUseCase = (u: RegistryUseCase): boolean =>
  !isArchivedUseCase(u) && u.lab_fit !== 'none';

const isBenchmarkUseCase = (u: RegistryUseCase): boolean =>
  u.id.startsWith('bench:') || (u.tags ?? []).includes('llm-code-benchmarks');

const isRealWorkUseCase = (u: RegistryUseCase): boolean =>
  u.id.startsWith('real:') || (u.tags ?? []).includes('real-work');

/** Partition registry entries into active source collections and archived entries. */
export function groupUseCases(useCases: readonly RegistryUseCase[]): GroupedUseCases {
  const active: RegistryUseCase[] = [];
  const archived: RegistryUseCase[] = [];
  const benchmarks: RegistryUseCase[] = [];
  const realWork: RegistryUseCase[] = [];
  const fleetAndProjects: RegistryUseCase[] = [];

  for (const u of useCases) {
    if (isArchivedUseCase(u)) {
      archived.push(u);
      continue;
    }
    active.push(u);
    if (isBenchmarkUseCase(u)) benchmarks.push(u);
    else if (isRealWorkUseCase(u)) realWork.push(u);
    else fleetAndProjects.push(u);
  }

  const collections: UseCaseCollection[] = (
    [
      { key: 'benchmarks', title: 'LLM Code Benchmarks', items: benchmarks },
      { key: 'real-work', title: 'Real Work', items: realWork },
      { key: 'fleet-projects', title: 'Fleet & Projects', items: fleetAndProjects },
    ] as const
  ).filter(c => c.items.length > 0);

  return { active, collections, archived };
}

/** Normalize a use-case id (e.g. `bench:bug-finding`) to a clean slug (`bench-bug-finding`). */
export function useCaseSlug(id: string): string {
  return id.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
}

/** Match an approved example name against a use-case slug on alphanumeric boundaries. */
export function matchesUseCaseSlug(exampleName: string, slug: string): boolean {
  if (!slug) return false;
  const escaped = slug.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return new RegExp(`(?:^|[^a-z0-9])${escaped}(?:[^a-z0-9]|$)`).test(exampleName.toLowerCase());
}

const EXAMPLE_TOKEN_ALIASES: Record<string, string> = {
  py: 'python',
  ts: 'typescript',
  js: 'javascript',
};

const tokenizeForUseCaseMatch = (raw: string): string[] =>
  raw
    .toLowerCase()
    .replace(/\buc-?0*(\d+)\b/g, 'uc$1')
    .split(/[^a-z0-9]+/)
    .filter(Boolean)
    .map(t => EXAMPLE_TOKEN_ALIASES[t] ?? t);

/**
 * Match an approved example to a use case by explicit API `useCaseId` first,
 * falling back to slug boundary matching and symmetric token-set matching.
 */
export function matchesUseCaseExample(ex: ExampleUseCaseCandidate, useCaseId: string): boolean {
  const explicit = (ex as { useCaseId?: string | null }).useCaseId?.trim();
  if (explicit) return explicit === useCaseId;

  const slug = useCaseSlug(useCaseId);
  if (matchesUseCaseSlug(ex.name, slug)) return true;

  const cleanName = ex.name.trim().replace(/^examples\//i, '').replace(/(?:@[^.]+)?\.json$/i, '');
  const ucTokens = tokenizeForUseCaseMatch(useCaseId);
  const exTokens = tokenizeForUseCaseMatch(cleanName);
  if (ucTokens.length < 2 || exTokens.length < 2) return false;

  const prefix = ucTokens[0];
  const ucSet = new Set(ucTokens);
  const exSet = new Set(exTokens);
  if (!exSet.has(prefix)) return false;

  const sharedNonPrefix = ucTokens.slice(1).filter(t => exSet.has(t));
  if (sharedNonPrefix.length === 0) return false;

  if (ucTokens.every(t => exSet.has(t))) return true;
  return sharedNonPrefix.length >= 2 && exTokens.every(t => ucSet.has(t));
}

/** Return approved examples matching the given use-case ID. */
export function matchingExamplesForUseCase<T extends ExampleUseCaseCandidate>(
  examples: readonly T[],
  useCaseId: string,
): T[] {
  return examples.filter(ex => matchesUseCaseExample(ex, useCaseId));
}

/** Format a concise picker label combining the use-case id and a short goal summary. */
export function useCaseOptionLabel(u: Pick<LabUseCaseEntry, 'id' | 'goal'>, maxGoal = 36): string {
  const goal = u.goal.trim();
  if (!goal) return u.id;
  const shortGoal = goal.length > maxGoal ? `${goal.slice(0, maxGoal - 1).trimEnd()}…` : goal;
  return `${u.id} · ${shortGoal}`;
}

export const USE_CASE_COLLECTION_PREFIX = 'col:';
export const ARCHIVED_COLLECTION_ID = 'col:archived';

const COLLECTION_TITLES: Record<UseCaseCollection['key'] | 'archived', string> = {
  benchmarks: 'LLM Code Benchmarks',
  'real-work': 'Real Work',
  'fleet-projects': 'Fleet & Projects',
  archived: 'Archived',
};

const COLLECTION_ICONS: Record<UseCaseCollection['key'] | 'archived', IconName> = {
  benchmarks: 'chart-no-axes-column',
  'real-work': 'list',
  'fleet-projects': 'folder-open',
  archived: 'archive',
};

export type SuiteKey = UseCaseCollection['key'] | 'archived';

/** Resolve a URL suite segment or folder id (`benchmarks`, `col:benchmarks`, or title) to a canonical SuiteKey. */
export function resolveSuiteKey(raw: string | null | undefined): SuiteKey | null {
  if (!raw) return null;
  const trimmed = raw.trim();
  const stripped = trimmed.startsWith(USE_CASE_COLLECTION_PREFIX)
    ? trimmed.slice(USE_CASE_COLLECTION_PREFIX.length)
    : trimmed;
  if (stripped in COLLECTION_TITLES) return stripped as SuiteKey;
  const lower = stripped.toLowerCase();
  for (const [key, title] of Object.entries(COLLECTION_TITLES)) {
    if (title.toLowerCase() === lower) return key as SuiteKey;
  }
  if (lower === 'archived / obsolete') return 'archived';
  return null;
}

/** Return the registry icon for a collection or suite key. */
export function collectionIconOf(key: SuiteKey): IconName {
  return COLLECTION_ICONS[key];
}

/** Resolve a CatalogueTree folder node id (`col:<key>`) or suite key back to its collection title, or null if unknown. */
export function collectionTitleOf(folderId: string | null | undefined): string | null {
  if (!folderId || !folderId.startsWith(USE_CASE_COLLECTION_PREFIX)) return null;
  const key = resolveSuiteKey(folderId);
  return key ? COLLECTION_TITLES[key] : null;
}

export type UseCaseTreeOptions = {
  /** Pinned use-case IDs hoisted into the top Pinned group (OR-12). */
  pinnedIds?: readonly string[];
  /** Optional right-aligned marker for each leaf node (e.g. score or run count). */
  rightOf?: (u: RegistryUseCase) => JSX.Element;
  /** Optional right-aligned marker for each collection folder (e.g. best score). */
  collectionRightOf?: (c: UseCaseCollection) => JSX.Element;
  /** Optional href for each leaf node. */
  hrefOf?: (u: RegistryUseCase) => string;
  /** Return archived entries in `archivedNodes` (for EntityPicker) instead of folding them at the bottom of `nodes`. */
  separateArchived?: boolean;
  /** Mark `lab_fit === 'none'` entries as disabled (e.g. when picking runnable Lab suites). */
  eligibleOnly?: boolean;
};

/** Map grouped use cases into 2-level `CatalogueTreeNode[]` (<= 3 levels, `OR-12`, `OR-22`). */
export function buildUseCaseTree(
  useCases: readonly RegistryUseCase[],
  opts: UseCaseTreeOptions = {},
): { nodes: CatalogueTreeNode[]; archivedNodes: CatalogueTreeNode[] } {
  const { collections, archived } = groupUseCases(useCases);
  const pinnedSet = new Set(opts.pinnedIds ?? []);

  const leafOf = (u: RegistryUseCase): CatalogueTreeNode => {
    const disabled = !!opts.eligibleOnly && !isLabEligibleUseCase(u);
    return {
      id: u.id,
      label: u.goal,
      sub: u.id,
      state: {
        family: 'source',
        state: u.source_status || 'current',
        reason: u.source ? `${u.source.repo}/${u.source.path}#${u.source.anchor}` : undefined,
      },
      pinned: pinnedSet.has(u.id),
      right: opts.rightOf?.(u),
      href: opts.hrefOf?.(u),
      disabled: disabled || undefined,
      disabledReason: disabled ? 'Not runnable in Lab (lab_fit: none)' : undefined,
      searchText: [u.id, u.goal, u.status, u.lab_fit, u.maturity, ...(u.tags ?? []), ...(u.maps_to ?? [])]
        .filter(Boolean)
        .join(' '),
    };
  };

  const nodes: CatalogueTreeNode[] = collections.map(c => ({
    id: `${USE_CASE_COLLECTION_PREFIX}${c.key}`,
    label: c.title,
    icon: COLLECTION_ICONS[c.key],
    defaultCollapsed: false,
    right: opts.collectionRightOf?.(c),
    children: c.items.map(leafOf),
  }));

  const archivedFolder: CatalogueTreeNode | null = archived.length
    ? {
        id: ARCHIVED_COLLECTION_ID,
        label: 'Archived',
        icon: 'archive',
        defaultCollapsed: !opts.separateArchived,
        children: archived.map(leafOf),
      }
    : null;

  const archivedNodes = archivedFolder ? [archivedFolder] : [];
  if (archivedFolder && !opts.separateArchived) {
    nodes.push(archivedFolder);
  }
  return { nodes, archivedNodes };
}
