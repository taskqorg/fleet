// One catalogue of icons, state names and navigation anchors (OR-1, OR-3, OR-8).
// Icons are the TaskQ set: Lucide static 1.17.0 (ISC) from taskq/src/ui-icons.json, plus the harness and
// provider marks from taskq/src/project-blocks.js (attribution: taskq docs/design/visual-style.md).
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
  host: {
    online: { label: 'Online', tone: 'success', icon: 'circle-check' },
    offline: { label: 'Offline', tone: 'danger', icon: 'circle-slash' },
  },
} as const satisfies Record<string, Record<string, StateDef>>;

export type Family = keyof typeof states;
export type StateOf<F extends Family> = keyof (typeof states)[F];

/** Workspaces and their navigation anchors (OR-3). */
export const workspaces = [
  { id: 'overview', label: 'Overview', icon: 'house' },
  { id: 'sessions', label: 'Sessions', icon: 'messages-square' },
  { id: 'tasks', label: 'Tasks', icon: 'list' },
  { id: 'results', label: 'Results', icon: 'file-text' },
  { id: 'projects', label: 'Projects', icon: 'folder-open' },
  { id: 'agents', label: 'Agents', icon: 'bot' },
  { id: 'hosts', label: 'Hosts', icon: 'panels-top-left' },
  { id: 'laboratory', label: 'Laboratory', icon: 'chart-no-axes-column' },
  { id: 'access', label: 'Access', icon: 'shield' },
] as const satisfies readonly { id: string; label: string; icon: IconName }[];

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
