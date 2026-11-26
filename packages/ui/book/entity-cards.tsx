// Entity references and their cards in the book: one fixture card per kind (synthetic data), a reference that stays
// loading and one that fails, served through EntityCards as the app serves entity_card.
import { createSignal, For } from 'solid-js';
import type { JSX } from '@solidjs/web';
import { Button, EntityCode, EntityCards, entityKinds, type EntityCardData, type EntityCardSource, type EntityRef, type EntityForm, Markdown } from '../src/index.ts';

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
  { kind: 'result', id: uuid(55), uuid: uuid(55), hash: 'ab'.repeat(32), code: 'R-FLT-9', preview: { resultId: 'cover', view: 'image' }, title: 'entity-card-dark-1440.png', state: 'stored', projectId: 'fleet', at: ago(90), atLabel: 'stored', path: '/results/' + uuid(55),
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
// The book's live states: the fixture's state, which the demo's Merge button moves (one signal: every reference updates).
const [moved, setMoved] = createSignal<Record<string, string>>({}, { ownedWrite: true });
// A picture for the result preview, drawn inline (no network in the book).
const COVER = 'data:image/svg+xml,' + encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" width="120" height="160"><defs><linearGradient id="g" x1="0" y1="0" x2=".4" y2="1"><stop offset="0" stop-color="#c9a46a"/><stop offset=".45" stop-color="#6b4a2b"/><stop offset="1" stop-color="#1d1a2e"/></linearGradient></defs><rect width="120" height="160" fill="url(#g)"/></svg>');
export const bookCards: EntityCardSource = {
  live: r => ({
    read: () => {
      if (key(r) === 'gone') return null;
      if (key(r) === 'slow') return undefined;
      const c = find(r);
      return c ? { kind: c.kind, id: c.id, code: c.code ?? null, state: moved()[c.id] ?? c.state ?? null, title: c.title } : undefined;
    },
    release: () => {},
  }),
  src: () => COVER,
  useCard: r => key(r) === 'slow' ? { data: null, error: null } : key(r) === 'gone' ? { data: null, error: 'Not found, or not visible to you', retry: () => {} }
    // A mockup's code without a fixture: its kind and code only.
    : { data: find(r) ?? { kind: r.kind, id: key(r)!, code: r.code ?? null, title: `${entityKinds[r.kind].label} ${r.code ?? ''}`.trim(), facts: [], path: null }, error: null },
  open: () => {},
};

export const CardsProvider = (props: { children: JSX.Element }) => <EntityCards value={bookCards}>{props.children}</EntityCards>;

const FORMS: EntityForm[] = ['code', 'chip', 'full'];
const pr = () => CARDS.find(c => c.kind === 'pull_request')!;

/** Every kind in the three forms, its card on hover (500 ms) or pinned on click, Enter or Space; a pull request merging
 *  live; and codes inside an agent message. */
export function EntityCardDemo() {
  const merged = () => (moved()[pr().id] ?? pr().state) === 'merged';
  return <div class="stack">
    <div class="ec-forms" role="table" aria-label="Reference forms">
      <For each={CARDS}>{c => <div class="ec-forms-r" role="row"><span class="muted" role="cell">{c.kind.replace('_', ' ')}</span>
        <For each={FORMS}>{f => <span role="cell"><EntityCode kind={c.kind} id={c.id} code={c.code ?? (c.uuid ? undefined : c.id)} form={f} /></span>}</For></div>}</For>
    </div>
    <div class="ec-demo">
      <Button label={merged() ? 'Reopen #643' : 'Merge #643'} kind="quiet" onClick={() => setMoved({ ...moved(), [pr().id]: merged() ? 'pr_open' : 'merged' })} />
      <span class="muted">Every reference to the pull request on this page changes in place.</span>
    </div>
    <div class="ec-demo"><For each={CARDS}>{c => <span class="ec-demo-i"><span class="muted">{c.kind.replace('_', ' ')}</span><EntityCode kind={c.kind} id={c.id} code={c.code ?? (c.uuid ? undefined : c.id)} /></span>}</For>
      <span class="ec-demo-i"><span class="muted">loading</span><EntityCode kind="task" id="slow" code="T-FLT-1" /></span>
      <span class="ec-demo-i"><span class="muted">no access</span><EntityCode kind="task" id="gone" code="T-ABC-9" /></span></div>
    <Markdown text={'Done with **T-FLT-42**; the open decision is Q-FLT-7, discussed in C-FLT-311. `T-FLT-42` in code stays text.'} />
  </div>;
}
