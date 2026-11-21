// Shared Lab use-case registry grouping, filtering, and slug helpers.
import type { LabUseCaseEntry } from '../../../contracts/generated/ops.ts';

export type RegistryUseCase = LabUseCaseEntry & { tags?: readonly string[] };

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

/** Format a concise picker label combining the use-case id and a short goal summary. */
export function useCaseOptionLabel(u: Pick<LabUseCaseEntry, 'id' | 'goal'>, maxGoal = 36): string {
  const goal = u.goal.trim();
  if (!goal) return u.id;
  const shortGoal = goal.length > maxGoal ? `${goal.slice(0, maxGoal - 1).trimEnd()}…` : goal;
  return `${u.id} · ${shortGoal}`;
}
