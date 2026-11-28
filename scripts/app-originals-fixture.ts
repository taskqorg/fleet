// Invented data derived from the operation boundary used in the normal-app
// diagnostic in C-FLT-1261. There are no artifact links, credentials or live IDs.
import type { Ops } from '../contracts/generated/ops.ts';
import { toPageParams } from '../packages/view-url/view-url.ts';

export type FixtureResponse = {
  [K in keyof Ops]: { op: K; request: Ops[K]['request']; reply: Ops[K]['reply']; required: boolean };
}[keyof Ops];

const response = <K extends keyof Ops>(op: K, request: Ops[NoInfer<K>]['request'], reply: Ops[NoInfer<K>]['reply'], required = false) =>
  ({ op, request, reply, required });
const session = 'c1111111-1111-4111-8111-111111111111';
const task = 'a1111111-1111-4111-8111-111111111111';
const project = 'fixture-project';
const date = '2026-11-01T00:00:00Z';
const clock = Date.parse(date) + 60_000;
const text = 'Does the proposed handoff have enough context to start?';
const me = { email: 'designer@example.invalid', name: 'Fixture designer', roles: ['operator'], owner: false,
  grantsVersion: 'synthetic', changeLogRetentionDays: 1 };
const settings: Ops['person_settings_get']['reply'] = {
  email: me.email, timezone: 'UTC', panels: {}, navCounters: {}, listGroups: {}, lastViews: {}, navCollapsed: false,
  railPinned: false, order: 'list', pins: [], views: [], focus: [], focusPresets: [], ignored: [], widths: {}, rowPins: {},
};
const agent = { profileId: null, harness: null, model: null, effort: null, mode: null, sandbox: null, tools: [], gitNetwork: null };
const question = { id: 'b1111111-1111-4111-8111-111111111111', sessionId: session, kind: 'agent_question' as const,
  text, options: ['Ready', 'Needs context'], executionId: null };
const taskTitle = 'Review the synthetic handoff';
const taskRef = { kind: 'task', id: task } as const;
const conversationRef = { kind: 'conversation', id: session } as const;
const projectRef = { kind: 'project', id: project } as const;
const taskEntity = { ...taskRef, code: 'T-TST-1', title: taskTitle, state: 'waiting' };
const conversationEntity = { ...conversationRef, code: 'C-TST-1', title: 'Synthetic design conversation', state: 'waiting' };
const projectEntity = { ...projectRef, code: 'TST', title: 'Synthetic project', state: null };
const emptyPage = { nextAfter: null, counts: {}, total: 0, groups: [] };
const changes = { changes: [], cursor: 0 };

const shared = [
  response('boot', {}, { person: 'known', me, settings, cursor: 0 }, true),
  response('me', {}, me),
  response('person_settings_get', {}, settings),
  response('nav_counts', {}, { focused: false, sections: {} }),
  response('project_list', {}, { projects: [{ id: project, code: 'TST', name: 'Synthetic project' }] }),
  response('projects_view', {}, { catalogue: [], lines: [], delivery: [], waiting: [], waitingTotal: 0 }),
  response('changes_since', {}, changes),
  response('changes_since', { cursor: 0, limit: 1000 }, changes),
  response('system_versions', {}, { coordinator: { version: '0.0.0', started: date },
    ui: { kind: 'bundled', version: '0.0.0', backendVersion: '0.0.0' }, hosts: [], serverNow: clock, staleAfterMs: 90_000 }),
] satisfies FixtureResponse[];

function board<K extends 'task' | 'session'>(scopeKind: K, scopeId: string) {
  return response('work_board_get', { scopeKind, scopeId }, { scopeKind, scopeId, projectId: scopeKind === 'task' ? project : null, roster: [], messages: [], headSeq: 0,
    summary: { threadsTotal: 0, threadsByStatus: {}, tasksTotal: 0, tasksByStatus: {}, membersTotal: 0, membersByState: {},
      messagesTotal: 0, blockersCount: 0, screenshotsCount: 0, harnessCounts: {} } });
}

const conversationResponses = [
  ...shared,
  board('session', session),
  response('sessions_list', { ...toPageParams({ view: 'conversations' }), limit: 100 },
    { sessions: [], origins: {}, hiddenCount: 0, ...emptyPage }, true),
  response('questions_list', { state: 'open', forPeople: true }, { questions: [] }),
  response('entity_refs', { refs: [{ kind: 'question', id: question.id }] },
    { items: [{ ref: 0, kind: 'question', id: question.id, code: 'Q-TST-1', title: text, state: 'open' }] }, true),
  response('session_view', { sessionId: session }, {
    head: { id: session, code: null, title: 'Synthetic design conversation', state: 'idle', activeExecutionId: null,
      projectId: null, projectCode: null, botId: null, archivedAt: null, createdAt: Date.parse(date), cost: [], agent },
    composer: { sessionId: session, activeExecutionId: null, reason: null }, questions: [question],
    triggers: { triggers: [], presets: [], canManage: false, passMs: 0 },
    context: { sessionId: session, swarm: [], tasks: [] }, executions: { rows: [], total: 0, nextBefore: null },
  }, true),
  response('thread_entries', { key: { session }, resolve: true, full: false, limit: 100 }, {
    key: { session }, entries: [], first: null, last: null, hasBefore: false, hasAfter: false, feedCursor: 0, sessionIds: [],
  }, true),
] satisfies FixtureResponse[];

const taskResponses = [
  ...shared,
  board('task', task),
  response('entity_resolve', { code: 'T-TST-1' }, { code: 'T-TST-1', currentCode: 'T-TST-1', id: task, type: 'T', superseded: false }, true),
  response('entity_card', { code: 'T-TST-1' }, { ...taskEntity, projectId: project, uuid: task, facts: [] }),
  response('entity_card', { kind: 'task', id: task }, { ...taskEntity, projectId: project, uuid: task, facts: [] }),
  response('publishing_status', { projectId: project }, { source: 'none', items: [], records: [], capabilities: [], samples: [], landing: 0 }),
  response('project_view', { projectId: project }, { conversations: [], conversationsTotal: 0, decisions: [], delivery: [], inWork: [], inWorkTotal: 0, lastMergedAt: null, merged: 0, pipelines: [], results: [], resultsTotal: 0, waiting: [], waitingTotal: 0 }),
  response('task_page', { ...toPageParams({ view: 'tasks', project: [project] }), limit: 100 }, { tasks: [], ...emptyPage }, true),
  response('task_get', { taskId: task }, {
    id: task, code: 'T-TST-1', projectId: project, title: taskTitle, expectedOutcome: 'Confirm that the synthetic source replays.',
    state: 'waiting', waitReason: null, priority: 'normal', completionRequirements: null, resultDestination: null,
    deliveryMethod: null, acceptedBy: null, acceptedAt: null, acceptedArtifactIds: null, createdBy: null,
    directedBy: null, requestedBy: null, takenFrom: null, createdAt: date, updatedAt: date, pauseReason: null,
    progress: { executions: 0, byState: {} }, cost: [], latestResult: null, openQuestions: 1, executions: [],
    dependencies: [], questions: [{ ...question, taskId: task, code: 'Q-TST-1', projectId: project, askedBy: 'Fixture agent',
      askedAt: date, answer: null, answeredBy: null, answeredAt: null, state: 'open' }],
  }, true),
  response('entity_refs', { refs: [projectRef] }, { items: [{ ref: 0, ...projectEntity }] }),
  response('entity_refs', { refs: [taskRef, projectRef] },
    { items: [{ ref: 0, ...taskEntity }, { ref: 1, ...projectEntity }] }),
  response('entity_refs', { refs: [projectRef, taskRef] },
    { items: [{ ref: 0, ...projectEntity }, { ref: 1, ...taskEntity }] }),
  response('entity_refs', { refs: [taskRef] }, { items: [{ ref: 0, ...taskEntity }] }),
  response('entity_refs', { refs: [conversationRef] }, { items: [{ ref: 0, ...conversationEntity }] }),
  response('entity_refs', { refs: [taskRef, conversationRef] },
    { items: [{ ref: 0, ...taskEntity }, { ref: 1, ...conversationEntity }] }),
  response('history_view', { taskId: task }, { entries: [], nextCursor: null, scopeTitle: taskTitle }),
] satisfies FixtureResponse[];

const conversationPath = '/conversations/' + session;
const taskPath = '/projects/TST/tasks/T-TST-1';
export const appOriginalsFixture = {
  version: 1, clock,
  cases: [
    { scopeKey: 'pages/conversations/app-shell:default', scenario: {
      start: conversationPath, target: conversationPath, flow: 'direct',
      landmark: '[data-ui=detail].session-view [data-ui=question-card]:has([aria-label*="Q-TST-1"])', responses: conversationResponses,
    } },
    { scopeKey: 'pages/tasks/app-shell:details', scenario: {
      start: taskPath, target: taskPath, flow: 'task-details',
      landmark: '[role=tabpanel][aria-label=Details] [data-ui=decision-block]:has([aria-label*="C-TST-1"])', responses: taskResponses,
    } },
  ],
};
