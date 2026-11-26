// One catalogue of icons, state names and navigation anchors (OR-1, OR-3, OR-8).
// Icons are the TaskQ set: Lucide static 1.17.0 (ISC) from taskq/src/ui-icons.json, plus the harness and
// provider marks from taskq/src/project-blocks.js (attribution: taskq/docs/design/visual-style.md in the taskq repository).
import icons from '../icons.json';
import vocabulary from '../../../contracts/vocabulary.json' with { type: 'json' };

export type IconName = keyof typeof icons;
export const iconSvg = (name: IconName): string => (icons as Record<string, string>)[name] ?? '';

export * from './states.ts';
import { states, type Tone, type Family, type StateOf, type StateDef } from './states.ts';

/** Workspaces and their navigation anchors (OR-3): the one list of screens. `nav: true` marks the seven rail sections in
 * rail order (OR-46, docs/designs/fleet-ia-and-journeys.md §1); `root` is the screen a section opens. Every other entry is
 * a screen inside one `section` (its own page, a facet or a tree root), which stays highlighted while it is open (OR-57)
 * and which Spotlight lists as a page. The address bar shows target routes (app/src/routes.ts); inside the app a screen's
 * id is the first segment of the path its view speaks, which the coordinator's view links share (contracts/vocabulary.json
 * views). The manifests are the sections' (app/build.mjs). */
export const workspaces = [
  { id: 'home', label: 'Home', icon: 'house', section: 'home', root: 'overview', nav: true },
  { id: 'live', label: 'Live', icon: 'activity', section: 'live', root: 'live', nav: true },
  { id: 'history', label: 'History', icon: 'history', section: 'history', root: 'history', nav: true },
  { id: 'conversations', label: 'Conversations', icon: 'messages-square', section: 'conversations', root: 'conversations', nav: true },
  { id: 'projects', label: 'Projects', icon: 'folder-open', section: 'projects', root: 'projects', nav: true },
  { id: 'lab', label: 'Lab', icon: 'chart-no-axes-column', section: 'lab', root: 'lab', nav: true },
  { id: 'library', label: 'Library', icon: 'rows-3', section: 'library', root: 'library', nav: true },
  { id: 'system', label: 'System', icon: 'settings', section: 'system', root: 'hosts', nav: true },
  { id: 'overview', label: 'Home', icon: 'house', section: 'home', nav: false },
  { id: 'tasks', label: 'Tasks', icon: 'list', section: 'projects', nav: false },
  { id: 'results', label: 'Results', icon: 'file-text', section: 'projects', nav: false },
  { id: 'delivery', label: 'Delivery', icon: 'git-merge', section: 'projects', nav: false },
  { id: 'personal-lab', label: 'Personal Lab', icon: 'chart-no-axes-column', section: 'lab', nav: false },
  { id: 'agents', label: 'Agents', icon: 'bot', section: 'library', nav: false },
  { id: 'bots', label: 'Bots', icon: 'bot', section: 'library', nav: false },
  { id: 'pipelines', label: 'Pipelines', icon: 'network', section: 'library', nav: false },
  { id: 'catalogue', label: 'Methods', icon: 'rows-3', section: 'library', nav: false },
  { id: 'changes', label: 'Changes', icon: 'git-pull-request', section: 'library', nav: false },
  { id: 'hosts', label: 'Hosts', icon: 'panels-top-left', section: 'system', nav: false },
  { id: 'agent-gaps', label: 'Agents', icon: 'bot', section: 'system', nav: false },
  { id: 'problems', label: 'Problems', icon: 'circle-alert', section: 'system', nav: false },
  { id: 'runners', label: 'Runners', icon: 'play', section: 'system', nav: false },
  { id: 'keys', label: 'Connections and keys', icon: 'shield', section: 'system', nav: false },
  { id: 'spend', label: 'Spend', icon: 'gauge', section: 'system', nav: false },
  { id: 'humans', label: 'People', icon: 'shield', section: 'system', nav: false },
] as const satisfies readonly { id: string; label: string; icon: IconName; section: string; root?: string; nav: boolean }[];
export type AppScreen = Extract<(typeof workspaces)[number], { nav: false }>['id'];
/** The phone bottom bar's four section tabs (OR-60, patterns/app-shell.md); More holds the other sections. */
export const barTabs = ['home', 'live', 'conversations', 'projects'] as const satisfies readonly Extract<(typeof workspaces)[number], { nav: true }>['id'][];

/** What an entity reference names (EntityCode, EntityCard; the coordinator's EntityKind): its word, icon and the state
 *  family of its card's state. A D17 code's letter names T task, Q question, C conversation. */
export const entityKinds = {
  task: { label: 'Task', icon: 'list', family: 'task' },
  question: { label: 'Question', icon: 'message-circle-question', family: 'question' },
  conversation: { label: 'Conversation', icon: 'messages-square', family: 'session' },
  execution: { label: 'Run', icon: 'play', family: 'execution' },
  result: { label: 'Result', icon: 'file-text', family: 'artifact' },
  experiment: { label: 'Experiment', icon: 'chart-no-axes-column', family: 'experiment' },
  project: { label: 'Project', icon: 'folder-open', family: null },
  host: { label: 'Host', icon: 'panels-top-left', family: 'host' },
  agent: { label: 'Agent', icon: 'bot', family: null },
  pipeline: { label: 'Pipeline', icon: 'network', family: null },
  pull_request: { label: 'Pull request', icon: 'git-pull-request', family: 'delivery' },
  release: { label: 'Release', icon: 'archive', family: 'delivery' },
} as const satisfies Record<string, { label: string; icon: IconName; family: Family | null }>;
export type EntityKind = keyof typeof entityKinds;
export const codeKind: Record<string, EntityKind> = { T: 'task', Q: 'question', C: 'conversation', R: 'result' };
/** D17 codes of entities (tasks, questions, conversations) in free text; spec rules (S-) are not entities. */
export const ENTITY_CODE = /\b[TQCR]-[A-Z][A-Z0-9]{1,4}-[1-9][0-9]*\b/g;

/** What performs a pipeline step (fleet.md: executors are agents, tools, programs or pipelines). */
export const stepKinds = {
  agent: { label: 'Agent', icon: 'bot' },
  tool: { label: 'Tool', icon: 'settings' },
  program: { label: 'Program', icon: 'terminal' },
  pipeline: { label: 'Pipeline', icon: 'network' },
} as const satisfies Record<string, { label: string; icon: IconName }>;
export type StepKind = keyof typeof stepKinds;

/** Delivery stages in attention order (the list's stage groups, Moving now): failed first, then the merge queue and
 *  checks, then the release tail. */
export const DELIVERY_ORDER: StateOf<'delivery'>[] = ['pr_failed', 'mq_failed', 'mq_ci', 'mq_wait', 'pr_ci', 'pr_open', 'releasing', 'installing', 'installed', 'merged'];

/** Row facts as glyphs (OR-73, controls/fact-count.md): one glyph and tone per fact everywhere, in this order (needs
 *  the person first, then trouble, then work). The label is the tooltip and the accessible name. */
export const factKinds = {
  setup: { label: 'Needs setup', icon: 'sliders-horizontal', tone: 'attention' },
  questions: { label: 'Open questions', icon: 'message-circle-question', tone: 'attention' },
  failed: { label: 'Failed', icon: 'octagon-x', tone: 'danger' },
  working: { label: 'Sessions working', icon: 'activity', tone: 'running' },
  inFlight: { label: 'Tasks in flight', icon: 'list', tone: 'neutral' },
} as const satisfies Record<string, { label: string; icon: IconName; tone: Tone }>;
export type FactKind = keyof typeof factKinds;

/** Action icons used by controls. One meaning per icon. */
export const actions = {
  pin: 'pin', unpin: 'pin-off', filter: 'list-filter', search: 'search', clear: 'x', retry: 'rotate-ccw',
  open: 'external-link', copy: 'copy', download: 'download', settings: 'settings', more: 'ellipsis',
  expand: 'chevron-down', back: 'arrow-left', share: 'paperclip', save: 'save', remove: 'trash-2', info: 'info',
  // sortDesc bars put big values first, sortAsc small values first; SortControl picks by what the rows show.
  group: 'layers-2', sortDesc: 'arrow-down-wide-narrow', sortAsc: 'arrow-up-narrow-wide', runs: 'repeat',
} as const satisfies Record<string, IconName>;

/** Harnesses (agents' programs), shown as their mark (OR-1). */
// One list with the coordinator: contracts/vocabulary.json (accounts, host versions and the profiles read the same file).
export const harnesses: Record<string, { label: string; icon: IconName }> = Object.fromEntries(vocabulary.harnesses.map(h => [h.id, { label: h.label, icon: h.icon as IconName }]));
export type Harness = string;

/** Delivery stages shown on a task alongside its state (fleet.md: review, merge and deployment as stages). */
export const stages = {
  pr_open: { label: 'PR open', tone: 'neutral', icon: 'git-pull-request' },
  checks: { label: 'Checks running', tone: 'running', icon: 'circle-dashed' },
  checks_failed: { label: 'Checks failed', tone: 'danger', icon: 'circle-x' },
  merging: { label: 'Merging', tone: 'running', icon: 'git-merge' },
  merged: { label: 'Merged', tone: 'success', icon: 'check-check' },
  deploying: { label: 'Deploying', tone: 'running', icon: 'rotate-cw' },
} as const satisfies Record<string, StateDef>;
export type Stage = keyof typeof stages;
