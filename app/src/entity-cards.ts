// The app's source of entity cards (packages/ui EntityCode and EntityCard; docs/design-system/controls/entity-card.md):
// one `entity_card` read per reference, kept in the doc store (memory and IndexedDB) under `entity-card:<kind>:<id>` and
// read again when the change feed touches that entity, so a card opened twice shows at once and stays current.
import type { EntityCardRead, EntityCardSource, EntityRef } from '../../packages/ui/src/index.ts';
import type { Change, EntityCardReply } from '../../contracts/generated/ops.ts';
import { op, OpError } from './api.ts';
import { useRef } from 'preact/hooks';
import { useDoc } from './store.ts';

/** Whether a change can alter the card of this entity (its id once read: a code-only reference learns it from the reply). */
export function cardTouched(kind: EntityRef['kind'], id: string | undefined, c: Change): boolean {
  if (!id) return false;
  switch (kind) {
    case 'task': return (c.id === id || c.taskId === id) && c.kind !== 'event';
    case 'question': return c.kind === 'question' && c.id === id;
    // Not every streamed event: a turn shows as its execution and messages' session changes.
    case 'conversation': return c.sessionId === id && c.kind !== 'event';
    case 'execution': return c.kind === 'execution' && c.id === id;
    case 'result': return c.kind === 'artifact' && c.id === id;
    // Its trials are executions, whose changes name no experiment: Trials is read again when the card opens (holdDoc).
    case 'experiment': return c.kind === 'experiment' && c.id === id;
    // Its observation, and the runs and commands on it (Running, Queued).
    case 'host': return c.hostId === id || c.kind === 'host' && c.id === id;
    // A change names no project, so counts are read again when the card opens (holdDoc), not on every task change.
    case 'project': return c.kind === 'definition' && c.id === 'project/' + id;
    case 'agent': case 'pipeline': return c.kind === 'definition' && c.id === `${kind}/${id}`;
    case 'pull_request': return c.kind === 'delivery';
    case 'release': return c.kind === 'delivery' || c.kind === 'host';
  }
}

function useCard(ref: EntityRef): EntityCardRead {
  const name = `entity-card:${ref.kind}:${ref.id ?? ref.code}`;
  const known = useRef<string | undefined>(ref.id);
  const doc = useDoc<EntityCardReply>(name, async () => {
    try { return await op('entity_card', ref.id ? { kind: ref.kind, id: ref.id } : { code: ref.code! }); }
    catch (e) { throw e instanceof OpError && e.status === 404 ? Error('Not found, or not visible to you') : e; }
  }, c => cardTouched(ref.kind, known.current, c));
  known.current = doc.data?.id ?? ref.id; // the reply's id: a legacy or alias id resolves to the one changes carry
  return { data: doc.data, error: doc.error, retry: doc.reload };
}

export const entityCards = (go: (path: string) => void): EntityCardSource => ({ useCard, open: go });
