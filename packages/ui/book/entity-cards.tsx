// Entity references and their cards in the book: one fixture card per kind (synthetic data), a reference that stays
// loading and one that fails, served through EntityCards as the app serves entity_card.
import type { ComponentChildren } from 'preact';
import { EntityCode, EntityCards, type EntityCardData, type EntityCardSource, type EntityRef, Markdown } from '../src/index.ts';

const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
const PR = '#' + 643; // built: a literal reads as a colour to check-design
const uuid = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;

export const CARDS: EntityCardData[] = [
  { kind: 'task', id: uuid(42), uuid: uuid(42), code: 'T-FLT-42', title: 'Entity card: a standard control for references', state: 'in_progress', projectId: 'fleet', harness: 'claude', at: ago(1), atLabel: 'changed', path: '/tasks/' + uuid(42),
    facts: [{ label: 'Runs', type: 'count', value: '3' }, { label: 'Open questions', type: 'count', value: '1' }, { label: 'Host', type: 'code', value: 'mac-mini' }] },
  { kind: 'question', id: uuid(7), uuid: uuid(7), code: 'Q-FLT-7', title: 'Open the card on click or on hover? Click keeps the pointer free to read rows.', state: 'open', projectId: 'fleet', at: ago(12), atLabel: 'asked', path: '/tasks/' + uuid(42),
    facts: [{ label: 'Asked by', type: 'text', value: 'claude-worker' }, { label: 'Task', type: 'code', value: 'T-FLT-42' }] },
  { kind: 'conversation', id: uuid(311), uuid: uuid(311), code: 'C-FLT-311', title: 'Plan the delivery page: stages, norms and the release tail', state: 'running', projectId: 'fleet', harness: 'codex', at: ago(0.2), atLabel: 'active', path: '/conversations/' + uuid(311),
    facts: [{ label: 'Profile', type: 'code', value: 'codex-designer' }, { label: 'Task', type: 'code', value: 'T-FLT-40' }, { label: 'Runs', type: 'count', value: '5' }] },
  { kind: 'execution', id: uuid(900), uuid: uuid(900), title: 'Entity card: a standard control for references', state: 'failed', projectId: 'fleet', harness: 'claude', at: ago(25), atLabel: 'ended', path: '/tasks/' + uuid(42),
    facts: [{ label: 'Problem', type: 'text', value: 'exit 1: browser check failed' }, { label: 'Task', type: 'code', value: 'T-FLT-42' }, { label: 'Host', type: 'code', value: 'pds18' }, { label: 'Duration', type: 'duration', value: '754' }] },
  { kind: 'result', id: uuid(55), uuid: uuid(55), hash: 'ab'.repeat(32), title: 'entity-card-dark-1440.png', state: 'stored', projectId: 'fleet', at: ago(90), atLabel: 'stored', path: '/results/' + uuid(55),
    facts: [{ label: 'Task', type: 'code', value: 'T-FLT-42' }, { label: 'Type', type: 'code', value: 'image/png' }, { label: 'Size', type: 'bytes', value: '284312' }] },
  { kind: 'experiment', id: uuid(3), uuid: uuid(3), title: 'Does code mode cut researcher tokens?', state: 'running', projectId: 'fleet', at: ago(300), atLabel: 'launched', path: '/lab/' + uuid(3),
    facts: [{ label: 'Pipeline', type: 'code', value: 'web-research' }, { label: 'Trials', type: 'count', value: '24' }, { label: 'Variants', type: 'count', value: '2' }] },
  { kind: 'project', id: 'fleet', code: 'FLT', title: 'Fleet', at: ago(60 * 26), atLabel: 'changed', path: '/projects/fleet',
    facts: [{ label: 'Open tasks', type: 'count', value: '38' }, { label: 'Running', type: 'count', value: '6' }, { label: 'Open questions', type: 'count', value: '2' }] },
  { kind: 'host', id: 'pds18', title: 'pds18', state: 'online', at: ago(0.1), atLabel: 'seen', path: '/hosts/pds18',
    facts: [{ label: 'Version', type: 'code', value: '1.261117.4' }, { label: 'Running', type: 'count', value: '4' }, { label: 'Queued', type: 'count', value: '1' }, { label: 'Platform', type: 'text', value: 'linux' }] },
  { kind: 'agent', id: 'claude-worker', code: 'claude-worker', title: 'Claude worker', harness: 'claude', at: ago(60 * 50), atLabel: 'changed', path: '/agents/claude-worker',
    facts: [{ label: 'Model', type: 'code', value: 'sonnet' }, { label: 'Revision', type: 'count', value: '12' }] },
  { kind: 'pipeline', id: 'web-research', title: 'web-research', at: ago(60 * 75), atLabel: 'changed', path: '/pipelines/web-research',
    facts: [{ label: 'Steps', type: 'count', value: '4' }, { label: 'Revision', type: 'count', value: '3' }] },
  { kind: 'pull_request', id: 'taskqorg/fleet' + PR, code: PR, title: 'feat(config): claude-researcher runs its tools through code mode', state: 'merged', projectId: 'fleet', at: ago(1), atLabel: 'since',
    url: 'https://github.com/taskqorg/fleet/pull/643', path: '/delivery/taskqorg~fleet~643',
    facts: [{ label: 'Repository', type: 'code', value: 'taskqorg/fleet' }, { label: 'Release', type: 'code', value: 'v1.261117.4' }, { label: 'Opened', type: 'time', value: ago(95) }, { label: 'Merged', type: 'time', value: ago(1) }] },
  { kind: 'release', id: 'v1.261117.4', code: 'v1.261117.4', title: 'Release 1.261117.4', state: 'installing', path: '/delivery?q=v1.261117.4',
    facts: [{ label: 'Hosts', type: 'text', value: '2 of 3' }, { label: 'Pull requests', type: 'count', value: '4' }] },
];

const key = (r: EntityRef) => r.id ?? r.code;
const find = (r: EntityRef) => CARDS.find(c => c.kind === r.kind && (c.id === r.id || c.code === r.code || c.id === key(r)));

/** The book's card source: fixtures; `slow` stays loading, anything unknown fails like a 404. */
export const bookCards: EntityCardSource = {
  useCard: r => key(r) === 'slow' ? { data: null, error: null } : { data: find(r) ?? null, error: find(r) ? null : 'Not found, or not visible to you', retry: () => {} },
  open: () => {},
};

export const CardsProvider = ({ children }: { children: ComponentChildren }) => <EntityCards.Provider value={bookCards}>{children}</EntityCards.Provider>;

/** Every kind as a reference, with its card on click, Enter or Space; and codes inside an agent message. */
export function EntityCardDemo() {
  return <div class="stack">
    <div class="ec-demo">{CARDS.map(c => <span key={c.kind} class="ec-demo-i"><span class="muted">{c.kind.replace('_', ' ')}</span><EntityCode kind={c.kind} id={c.id} code={c.code ?? (c.uuid ? undefined : c.id)} /></span>)}
      <span class="ec-demo-i"><span class="muted">loading</span><EntityCode kind="task" id="slow" code="T-FLT-1" /></span>
      <span class="ec-demo-i"><span class="muted">no access</span><EntityCode kind="task" id="gone" code="T-ABC-9" /></span></div>
    <Markdown text={'Done with **T-FLT-42**; the open decision is Q-FLT-7, discussed in C-FLT-311. `T-FLT-42` in code stays text.'} />
  </div>;
}
