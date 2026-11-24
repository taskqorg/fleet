// The app's source of entity cards (packages/ui EntityCode and EntityCard; docs/design-system/controls/entity-card.md):
// one `entity_card` read per reference through useOp (memory and IndexedDB), read again when a change touches that entity,
// so a card opened twice shows at once and stays current.
import { createEffect, createMemo } from 'solid-js';
import type { EntityCardRead, EntityCardSource, EntityRef } from '../../packages/ui/src/index.ts';
import { readValue, type MaybeAccessor } from './signal.ts';
import { useOp } from './store.ts';

const NOT_FOUND = 'Not found, or not visible to you';
// Kinds whose state comes from runs, whose changes (totals) arrive many times a second: read every 5 s while open.
const TIMED = new Set<EntityRef['kind']>(['execution', 'conversation', 'host']);

function useCard(refInput: MaybeAccessor<EntityRef>): EntityCardRead {
  const ref = createMemo(() => readValue(refInput));
  const resolvedId = createMemo(() => ref().id);
  let learnedId: string | undefined;
  // The server names entity_card in the changes that can alter a card; touchedBy keeps those of this entity (by its id).
  // Execution changes are left out (totals flush many times a second): kinds whose state comes from runs read every 5 s.
  // A card asked by code learns its id from the reply (a legacy or alias id resolves to the one changes carry).
  const doc = useOp(
    'entity_card',
    () => {
      const r = ref();
      return r.id ? { kind: r.kind, id: r.id } : { code: r.code! };
    },
    {
      every: () => (TIMED.has(ref().kind) ? 5000 : undefined),
      scope: () => ({ kind: ref().kind, id: resolvedId() ?? learnedId }),
    },
  );
  createEffect(() => doc.data?.id, id => { if (id) learnedId = id; });
  // An entity now gone or hidden shows as such, never as its stored card.
  return {
    get data() { return doc.status === 404 ? null : doc.data; },
    get error() { return doc.status === 404 ? NOT_FOUND : doc.error; },
    retry: doc.reload,
  };
}

export const entityCards = (go: (path: string) => void): EntityCardSource => ({ useCard, open: go });
