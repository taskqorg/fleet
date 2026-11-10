// One catalogue of icons, state names and navigation anchors (OR-1, OR-3, OR-8).
// Icons are the TaskQ set: Lucide static 1.17.0 (ISC) from taskq/src/ui-icons.json, plus the harness and
// provider marks from taskq/src/project-blocks.js (attribution: taskq/docs/design/visual-style.md in the taskq repository).
import icons from '../icons.json';

export type IconName = keyof typeof icons;
export const iconSvg = (name: IconName): string => (icons as Record<string, string>)[name] ?? '';

/** Colour roles from tokens; colour always comes with an icon and a word (D7). */
export type Tone = 'attention' | 'danger' | 'running' | 'success' | 'neutral';
export type StateDef = { label: string; tone: Tone; icon: IconName };

export const states = {
  task: {
    queued: { label: 'Queued', tone: 'neutral', icon: 'list-ordered' },
    in_progress: { label: 'In progress', tone: 'running', icon: 'activity' },
    waiting: { label: 'Waiting', tone: 'attention', icon: 'bell' },
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
  /** A Lab trial (pipeline run): unknown is an outcome nobody can prove (never replayed). */
  trial: {
    running: { label: 'Running', tone: 'running', icon: 'activity' },
    completed: { label: 'Completed', tone: 'success', icon: 'circle-check' },
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

/** Workspaces and their navigation anchors (OR-3), in nav order: the one list of screens. `app: true` marks a screen the
 * operator app has built; the app nav and routes, the build (manifests, service worker) and the coordinator's app routes
 * all follow it, so setting it (with the view in app/src/main.tsx) is all a new screen needs. */
export const workspaces = [
  { id: 'overview', label: 'Overview', icon: 'house', group: 'Operator', app: true },
  { id: 'tasks', label: 'Tasks', icon: 'list', group: 'Operator', app: true },
  { id: 'conversations', label: 'Conversations', icon: 'messages-square', group: 'Operator', app: true },
  { id: 'results', label: 'Results', icon: 'file-text', group: 'Operator', app: true },
  { id: 'projects', label: 'Projects', icon: 'folder-open', group: 'Operator', app: false },
  { id: 'agents', label: 'Agents', icon: 'bot', group: 'Architect', app: false },
  { id: 'pipelines', label: 'Pipelines', icon: 'network', group: 'Architect', app: false },
  { id: 'catalogue', label: 'Catalogue', icon: 'rows-3', group: 'Architect', app: false },
  { id: 'hosts', label: 'Hosts', icon: 'panels-top-left', group: 'Architect', app: true },
  { id: 'lab', label: 'Lab', icon: 'chart-no-axes-column', group: 'Architect', app: true },
  { id: 'keys', label: 'API keys', icon: 'shield', group: 'Architect', app: true },
  { id: 'access', label: 'Access', icon: 'shield', group: 'Architect', app: false },
] as const satisfies readonly { id: string; label: string; icon: IconName; group: string; app: boolean }[];
export type AppScreen = Extract<(typeof workspaces)[number], { app: true }>['id'];

/** What performs a pipeline step (fleet.md: executors are agents, tools, programs or pipelines). */
export const stepKinds = {
  agent: { label: 'Agent', icon: 'bot' },
  tool: { label: 'Tool', icon: 'settings' },
  program: { label: 'Program', icon: 'terminal' },
  pipeline: { label: 'Pipeline', icon: 'network' },
} as const satisfies Record<string, { label: string; icon: IconName }>;
export type StepKind = keyof typeof stepKinds;

/** Action icons used by controls. One meaning per icon. */
export const actions = {
  pin: 'pin', unpin: 'pin-off', filter: 'list-filter', search: 'search', clear: 'x', retry: 'rotate-ccw',
  open: 'external-link', copy: 'copy', download: 'download', settings: 'settings', more: 'ellipsis',
  expand: 'chevron-down', back: 'arrow-left', share: 'paperclip', save: 'save', remove: 'trash-2', info: 'info',
} as const satisfies Record<string, IconName>;

/** Harnesses (agents' programs), shown as their mark (OR-1). */
export const harnesses = {
  codex: { label: 'Codex', icon: 'openai' },
  claude: { label: 'Claude', icon: 'claude' },
  antigravity: { label: 'Antigravity', icon: 'antigravity' },
} as const satisfies Record<string, { label: string; icon: IconName }>;
export type Harness = keyof typeof harnesses;

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
