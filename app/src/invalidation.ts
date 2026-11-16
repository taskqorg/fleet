// Domain predicates over authoritative feed metadata; access/resync and legacy fallbacks stay conservative.
import type { Change } from '../../contracts/generated/ops.ts';

/** A writer's actor is not the person whose preferences it changed. */
export function preferencesTouched(changes: Change[], identityKey: string | undefined): boolean {
  return changes.some(change => {
    if (change.kind !== 'setting-change') return false;
    const target = change.settingTarget;
    if (!target || !identityKey) return true;
    if (target === 'person:') return true; // malformed/unknown personal target: reread safely
    return target === 'person:' + identityKey;
  });
}

/** Other definition kinds can affect project dependencies; only a known other project is irrelevant. */
export function projectDefinitionTouched(changes: Change[], projectId: string | undefined): boolean {
  if (!projectId) return false;
  return changes.some(change => change.kind === 'definition' &&
    (!change.id.startsWith('project/') || change.id === 'project/' || change.id === 'project/' + projectId));
}
