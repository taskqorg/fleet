// One catalogue of icons, state names and navigation anchors (OR-1, OR-3, OR-8).
// Icons are the TaskQ set: Lucide static 1.17.0 (ISC) from taskq/src/ui-icons.json, plus the harness and
// provider marks from taskq/src/project-blocks.js (attribution: taskq/docs/design/visual-style.md in the taskq repository).
import icons from '../icons.json';
import vocabulary from '../../../contracts/vocabulary.json' with { type: 'json' };

export type IconName = keyof typeof icons;
export const iconSvg = (name: IconName): string => (icons as Record<string, string>)[name] ?? '';

/** Colour roles from tokens; colour always comes with an icon and a word (D7). */
export type Tone = 'attention' | 'danger' | 'running' | 'success' | 'neutral';
export type StateDef = { label: string; tone: Tone; icon: IconName };

export const states = {
  /** The one state vocabulary of sessions, bots, tasks and runs (OR-55, controls/state-badge.md#dot-and-word); lifeOf maps the families below to it. */
  life: {
    working: { label: 'Working', tone: 'running', icon: 'activity' },
    waiting: { label: 'Waiting for you', tone: 'attention', icon: 'bell' },
    queued: { label: 'Queued', tone: 'neutral', icon: 'list-ordered' },
    done: { label: 'Done', tone: 'success', icon: 'circle-check' },
    failed: { label: 'Failed', tone: 'danger', icon: 'octagon-x' },
    stopped: { label: 'Stopped', tone: 'neutral', icon: 'circle-slash' },
    asleep: { label: 'Asleep', tone: 'neutral', icon: 'circle-dashed' },
  },
  task: {
    queued: { label: 'Queued', tone: 'neutral', icon: 'list-ordered' },
    in_progress: { label: 'In progress', tone: 'running', icon: 'activity' },
    waiting: { label: 'Waiting', tone: 'attention', icon: 'bell' },
    held: { label: 'Held', tone: 'attention', icon: 'circle-pause' },
    paused: { label: 'Paused', tone: 'neutral', icon: 'pause' },
    completed: { label: 'Completed', tone: 'success', icon: 'circle-check' },
    failed: { label: 'Failed', tone: 'danger', icon: 'octagon-x' },
    cancelled: { label: 'Cancelled', tone: 'neutral', icon: 'circle-slash' },
  },
  execution: {
    preparing: { label: 'Preparing', tone: 'running', icon: 'rotate-cw' },
    running: { label: 'Running', tone: 'running', icon: 'activity' },
    waiting: { label: 'Waiting', tone: 'attention', icon: 'bell' },
    succeeded: { label: 'Succeeded', tone: 'success', icon: 'circle-check' },
    failed: { label: 'Failed', tone: 'danger', icon: 'octagon-x' },
    cancelled: { label: 'Cancelled', tone: 'neutral', icon: 'circle-slash' },
  },
  botRun: {
    queued: { label: 'Queued', tone: 'neutral', icon: 'list-ordered' },
    running: { label: 'Running', tone: 'running', icon: 'activity' },
    waiting: { label: 'Waiting', tone: 'attention', icon: 'bell' },
    succeeded: { label: 'Done', tone: 'success', icon: 'circle-check' },
    failed: { label: 'Failed', tone: 'danger', icon: 'octagon-x' },
    cancelled: { label: 'Stopped', tone: 'neutral', icon: 'circle-slash' },
    skipped: { label: 'Skipped', tone: 'neutral', icon: 'circle-slash' },
  },
  // Delivery stage of a pull request (delivery_status DeliveryStage, docs/designs/fleet-delivery-pipeline.md).
  delivery: {
    pr_open: { label: 'Open', tone: 'neutral', icon: 'git-pull-request' },
    pr_ci: { label: 'PR checks', tone: 'running', icon: 'activity' },
    pr_failed: { label: 'Checks failed', tone: 'danger', icon: 'octagon-x' },
    mq_wait: { label: 'Waiting in queue', tone: 'neutral', icon: 'list-ordered' },
    mq_ci: { label: 'Queue checks', tone: 'running', icon: 'git-merge' },
    mq_failed: { label: 'Queue failed', tone: 'danger', icon: 'circle-x' },
    releasing: { label: 'Releasing', tone: 'running', icon: 'archive' },
    installing: { label: 'Installing', tone: 'running', icon: 'download' },
    installed: { label: 'Installed', tone: 'success', icon: 'circle-check' },
    merged: { label: 'Merged', tone: 'success', icon: 'check-check' },
  },
  copy: {
    stored: { label: 'Stored', tone: 'success', icon: 'save' },
    placed: { label: 'Placed', tone: 'success', icon: 'check-check' },
    pending: { label: 'Pending', tone: 'running', icon: 'history' },
    unavailable: { label: 'Unavailable', tone: 'danger', icon: 'circle-alert' },
  },
  setting: {
    applied: { label: 'Applied', tone: 'success', icon: 'check' },
    pending: { label: 'Pending', tone: 'neutral', icon: 'history' },
    apply_error: { label: 'Apply error', tone: 'danger', icon: 'triangle-alert' },
  },
  session: {
    idle: { label: 'Idle', tone: 'neutral', icon: 'messages-square' },
    preparing: { label: 'Preparing', tone: 'running', icon: 'rotate-cw' },
    running: { label: 'Running', tone: 'running', icon: 'activity' },
    waiting: { label: 'Waiting', tone: 'attention', icon: 'bell' },
    succeeded: { label: 'Done', tone: 'success', icon: 'circle-check' },
    failed: { label: 'Failed', tone: 'danger', icon: 'octagon-x' },
    cancelled: { label: 'Stopped', tone: 'neutral', icon: 'circle-slash' },
  },
  experiment: {
    draft: { label: 'Draft', tone: 'neutral', icon: 'circle-dashed' },
    running: { label: 'Running', tone: 'running', icon: 'activity' },
    paused: { label: 'Paused', tone: 'attention', icon: 'circle-pause' },
    stopped: { label: 'Stopped', tone: 'neutral', icon: 'circle-slash' },
    done: { label: 'Done', tone: 'success', icon: 'circle-check' },
  },
  /** Eligibility in a pipeline plan, independent of execution state. */
  activation: {
    pending: { label: 'Pending', tone: 'neutral', icon: 'circle-dashed' },
    selected: { label: 'Selected', tone: 'running', icon: 'check' },
    not_selected: { label: 'Not selected', tone: 'neutral', icon: 'circle-slash' },
    blocked: { label: 'Blocked', tone: 'attention', icon: 'triangle-alert' },
  },
  /** A Lab trial (pipeline run, execution states): unknown is a failed run whose outcome nobody can prove (observation unconfirmed, never replayed). */
  trial: {
    preparing: { label: 'Preparing', tone: 'running', icon: 'rotate-cw' },
    running: { label: 'Running', tone: 'running', icon: 'activity' },
    waiting: { label: 'Waiting', tone: 'attention', icon: 'circle-pause' },
    succeeded: { label: 'Completed', tone: 'success', icon: 'circle-check' },
    failed: { label: 'Failed', tone: 'danger', icon: 'octagon-x' },
    unknown: { label: 'Unknown', tone: 'attention', icon: 'circle-question-mark' },
    cancelled: { label: 'Cancelled', tone: 'neutral', icon: 'circle-slash' },
  },
  /** Use-case registry drift (scripts/lab-drift.ts). */
  source: {
    current: { label: 'Current', tone: 'success', icon: 'check' },
    changed: { label: 'Changed', tone: 'attention', icon: 'triangle-alert' },
    missing: { label: 'Missing', tone: 'danger', icon: 'circle-x' },
    unavailable: { label: 'Unavailable', tone: 'neutral', icon: 'circle-dashed' },
  },
  key: {
    active: { label: 'Active', tone: 'success', icon: 'circle-check' },
    draining: { label: 'Draining', tone: 'attention', icon: 'circle-pause' },
    disabled: { label: 'Disabled', tone: 'danger', icon: 'circle-slash' },
    retired: { label: 'Retired', tone: 'neutral', icon: 'circle-dashed' },
  },
  intake: {
    all: { label: 'Open', tone: 'success', icon: 'circle-check' },
    paused: { label: 'Paused', tone: 'attention', icon: 'circle-pause' },
  },
  /** A question to a person (Spotlight results): open needs an answer; answered and withdrawn are done. */
  question: {
    open: { label: 'Needs you', tone: 'attention', icon: 'message-circle-question' },
    answered: { label: 'Answered', tone: 'success', icon: 'circle-check' },
    withdrawn: { label: 'Withdrawn', tone: 'neutral', icon: 'circle-slash' },
  },
  /** A stored result or other artifact (Results page, OR-22). */
  artifact: {
    stored: { label: 'Stored', tone: 'success', icon: 'save' },
    pending: { label: 'Uploading', tone: 'running', icon: 'history' },
    failed: { label: 'Failed', tone: 'danger', icon: 'octagon-x' },
    deleted: { label: 'Deleted', tone: 'neutral', icon: 'trash-2' },
  },
  host: {
    online: { label: 'Online', tone: 'success', icon: 'circle-check' },
    offline: { label: 'Offline', tone: 'danger', icon: 'circle-slash' },
    /** It polls, but the coordinator answers its polls with an error: no commands, no update target. */
    failing: { label: 'Polls failing', tone: 'danger', icon: 'octagon-x' },
  },
  // A subscription's intake (people-access §7): it accepts new work, or its holder paused it.
  subscription: {
    all: { label: 'Accepting work', tone: 'success', icon: 'circle-check' },
    paused: { label: 'Paused', tone: 'neutral', icon: 'pause' },
  },
  /** A definition kept in git (agent, pipeline, catalogue entry): its draft and qualification state (patterns/definition-editor.md). */
  definition: {
    draft: { label: 'Draft', tone: 'attention', icon: 'file-plus-2' },
    ready: { label: 'Ready', tone: 'success', icon: 'circle-check' },
    unchecked: { label: 'Not checked', tone: 'neutral', icon: 'circle-dashed' },
    incompatible: { label: 'Incompatible', tone: 'danger', icon: 'circle-x' },
    never: { label: 'Never run', tone: 'neutral', icon: 'circle-dashed' },
  },
} as const satisfies Record<string, Record<string, StateDef>>;

export type Family = keyof typeof states;
export type StateOf<F extends Family> = keyof (typeof states)[F];
export type Life = StateOf<'life'>;

/** OR-55: today's task, execution, bot-run and session states in the one vocabulary. Synonyms never show: Succeeded and
 *  Completed are Done, Cancelled is Stopped, Idle is Asleep, Preparing and Running are Working. A held task waits for a
 *  person and a paused one was stopped by one. */
export const lifeOf = {
  task: { queued: 'queued', in_progress: 'working', waiting: 'waiting', held: 'waiting', paused: 'stopped', completed: 'done', failed: 'failed', cancelled: 'stopped' },
  execution: { preparing: 'working', running: 'working', waiting: 'waiting', succeeded: 'done', failed: 'failed', cancelled: 'stopped' },
  botRun: { queued: 'queued', running: 'working', waiting: 'waiting', succeeded: 'done', failed: 'failed', cancelled: 'stopped', skipped: 'stopped' },
  session: { idle: 'asleep', preparing: 'working', running: 'working', waiting: 'waiting', succeeded: 'done', failed: 'failed', cancelled: 'stopped' },
} as const satisfies { [F in 'task' | 'execution' | 'botRun' | 'session']: Record<StateOf<F>, Life> };
export type LifeFamily = keyof typeof lifeOf;

/** Workspaces and their navigation anchors (OR-3): the one list of screens. `nav: true` marks the seven rail sections in
 * rail order (OR-46, docs/designs/fleet-ia-and-journeys.md §1); `root` is the screen a section opens. Every other entry is
 * a screen owned by one `section`, which stays highlighted while it is open (OR-57). `app: true` marks a screen the operator
 * app has built and routes to (app/src/main.tsx); the manifests are the sections' (app/build.mjs). The address bar shows
 * target routes (app/src/routes.ts); screens keep their earlier paths inside the app until wave 2 moves them. */
export const workspaces = [
  { id: 'home', label: 'Home', icon: 'house', section: 'home', root: 'overview', app: false, nav: true },
  { id: 'live', label: 'Live', icon: 'activity', section: 'live', root: 'live', app: true, nav: true },
  { id: 'conversations', label: 'Conversations', icon: 'messages-square', section: 'conversations', root: 'conversations', app: true, nav: true },
  { id: 'projects', label: 'Projects', icon: 'folder-open', section: 'projects', root: 'projects', app: true, nav: true },
  { id: 'lab', label: 'Lab', icon: 'chart-no-axes-column', section: 'lab', root: 'lab', app: true, nav: true },
  { id: 'library', label: 'Library', icon: 'rows-3', section: 'library', root: 'agents', app: false, nav: true },
  { id: 'system', label: 'System', icon: 'settings', section: 'system', root: 'hosts', app: false, nav: true },
  { id: 'overview', label: 'Home', icon: 'house', section: 'home', app: true, nav: false },
  { id: 'tasks', label: 'Tasks', icon: 'list', section: 'projects', app: true, nav: false },
  { id: 'results', label: 'Results', icon: 'file-text', section: 'projects', app: true, nav: false },
  { id: 'delivery', label: 'Delivery', icon: 'git-merge', section: 'projects', app: true, nav: false },
  { id: 'personal-lab', label: 'Personal Lab', icon: 'chart-no-axes-column', section: 'lab', app: true, nav: false },
  { id: 'agents', label: 'Agents', icon: 'bot', section: 'library', app: true, nav: false },
  { id: 'bots', label: 'Bots', icon: 'bot', section: 'library', app: true, nav: false },
  { id: 'pipelines', label: 'Pipelines', icon: 'network', section: 'library', app: true, nav: false },
  { id: 'catalogue', label: 'Methods', icon: 'rows-3', section: 'library', app: true, nav: false },
  { id: 'changes', label: 'Changes', icon: 'git-pull-request', section: 'library', app: true, nav: false },
  { id: 'hosts', label: 'Hosts', icon: 'panels-top-left', section: 'system', app: true, nav: false },
  { id: 'runners', label: 'Runners', icon: 'play', section: 'system', app: true, nav: false },
  { id: 'keys', label: 'Connections and keys', icon: 'shield', section: 'system', app: true, nav: false },
  { id: 'spend', label: 'Spend', icon: 'gauge', section: 'system', app: true, nav: false },
  { id: 'humans', label: 'People', icon: 'shield', section: 'system', app: true, nav: false },
  { id: 'access', label: 'Access', icon: 'shield', section: 'system', app: false, nav: false },
] as const satisfies readonly { id: string; label: string; icon: IconName; section: string; root?: string; app: boolean; nav: boolean }[];
export type AppScreen = Extract<(typeof workspaces)[number], { app: true }>['id'];

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
export const codeKind: Record<string, EntityKind> = { T: 'task', Q: 'question', C: 'conversation' };
/** D17 codes of entities (tasks, questions, conversations) in free text; spec rules (S-) are not entities. */
export const ENTITY_CODE = /\b[TQC]-[A-Z][A-Z0-9]{1,4}-[1-9][0-9]*\b/g;

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
