// State families and the one state vocabulary (OR-55, controls/state-badge.md): what each state is called, its tone and
// icon, the mapping to the vocabulary and the open and finished states. No JSON import, so Node tests and pure app
// modules (task-facts.ts, bot-view.ts) read it directly; registry.ts re-exports it.
import type { IconName } from './registry.ts';

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
  /** An account or key on System › Access (access_status): whether it works and what it needs from its owner. */
  access: {
    works: { label: 'Works', tone: 'success', icon: 'circle-check' },
    failing: { label: 'Failing', tone: 'danger', icon: 'octagon-x' },
    'needs-login': { label: 'Needs a login', tone: 'attention', icon: 'log-out' },
    'needs-key': { label: 'Needs a key', tone: 'attention', icon: 'shield' },
    'not-added': { label: 'Not added', tone: 'attention', icon: 'plus' },
    paused: { label: 'Paused', tone: 'neutral', icon: 'circle-pause' },
    unknown: { label: 'Not checked', tone: 'neutral', icon: 'circle-dashed' },
  },
  /** A project server Fleet's provisioner reaches (access_grants servers, docs/designs/fleet-project-access.md). */
  accessServer: {
    enrolled: { label: 'Enrolled', tone: 'success', icon: 'circle-check' },
    'not-enrolled': { label: 'Not enrolled', tone: 'attention', icon: 'circle-dashed' },
    failing: { label: 'Failing', tone: 'danger', icon: 'octagon-x' },
  },
  /** A project resource grant (access_grants grants): asked, provisioned and probed, or ended. */
  accessGrant: {
    asked: { label: 'Asked', tone: 'attention', icon: 'message-circle-question' },
    provisioning: { label: 'Provisioning', tone: 'running', icon: 'history' },
    ready: { label: 'Ready', tone: 'success', icon: 'circle-check' },
    failed: { label: 'Failed', tone: 'danger', icon: 'octagon-x' },
    declined: { label: 'Declined', tone: 'neutral', icon: 'circle-slash' },
    withdrawn: { label: 'Withdrawn', tone: 'neutral', icon: 'circle-slash' },
    revoking: { label: 'Revoking', tone: 'running', icon: 'history' },
    revoked: { label: 'Revoked', tone: 'neutral', icon: 'circle-dashed' },
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
    // Library (pages/library.md): the repository has a newer version than the store; Take repository version.
    repository: { label: 'Waits for repository', tone: 'attention', icon: 'bell' },
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

/** A family's state in the one vocabulary (OR-55); a state the registry does not know yet is Asleep, never Working. */
export const life = <F extends LifeFamily>(family: F, state: string | null | undefined): Life =>
  ((lifeOf[family] as Record<string, Life>)[state ?? ''] ?? 'asleep');
/** The states of a family that read as any of `lives`, in the registry's order (a filter for a list read). */
export const statesOf = <F extends LifeFamily>(family: F, lives: readonly Life[]): StateOf<F>[] =>
  (Object.entries(lifeOf[family]) as [StateOf<F>, Life][]).filter(([, l]) => lives.includes(l)).map(([s]) => s);
/** The states a record of a family ends in: no further work happens on it. A paused task is Stopped but not finished. */
export const finishedStates = {
  task: ['completed', 'failed', 'cancelled'],
  execution: ['succeeded', 'failed', 'cancelled'],
  botRun: ['succeeded', 'failed', 'cancelled', 'skipped'],
  session: ['succeeded', 'failed', 'cancelled'],
  experiment: ['stopped', 'done'],
} as const satisfies { [F in LifeFamily | 'experiment']: readonly StateOf<F>[] };
export const isFinished = (family: keyof typeof finishedStates, state: string | null | undefined): boolean =>
  (finishedStates[family] as readonly string[]).includes(state ?? '');
/** The states of a family that are not finished: open work, including a paused task and an idle session. */
export const openStates = <F extends LifeFamily>(family: F): StateOf<F>[] =>
  (Object.keys(lifeOf[family]) as StateOf<F>[]).filter(s => !isFinished(family, s as string));
