// The app's source of entity cards (packages/ui EntityCode and EntityCard; docs/design-system/controls/entity-card.md):
// one `entity_card` read per reference through useOp (memory and IndexedDB), read again when a change touches that entity,
// so a card opened twice shows at once and stays current.
import type { EntityCardRead, EntityCardSource, EntityRef } from '../../packages/ui/src/index.ts';
import { useOp } from './store.ts';

const NOT_FOUND = 'Not found, or not visible to you';
// Kinds whose state comes from runs, whose changes (totals) arrive many times a second: read every 5 s while open.
const TIMED = new Set<EntityRef['kind']>(['execution', 'conversation', 'host']);

function useCard(ref: EntityRef): EntityCardRead {
  // The server names entity_card in the changes that can alter a card; touchedBy keeps those of this entity (by its id).
  // Execution changes are left out (totals flush many times a second): kinds whose state comes from runs read every 5 s.
  const doc = useOp('entity_card', ref.id ? { kind: ref.kind, id: ref.id } : { code: ref.code! }, { every: TIMED.has(ref.kind) ? 5000 : undefined });
  // An entity now gone or hidden shows as such, never as its stored card.
  const gone = doc.status === 404;
  return { data: gone ? null : doc.data, error: gone ? NOT_FOUND : doc.error, retry: doc.reload };
}

export const entityCards = (go: (path: string) => void): EntityCardSource => ({ useCard, open: go });
