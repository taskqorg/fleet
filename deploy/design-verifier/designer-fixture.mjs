import { imageScenarioOK, resolveImages, validateImages } from './worker/image-journey.mjs';

const maxFixture = 256 * 1024;
export const maxDesignerCases = 8;
const scope = /^[a-z0-9-]+\/[a-z0-9-]+\/[a-z0-9-]+:[a-z0-9-]+$/;
const states = new Set(['preparing','running','waiting','succeeded','failed','cancelled']);
const exactKeys = (value, keys) => value && Object.keys(value).sort().join(',') === keys;

function appRoute(path, prefix) {
  return typeof path === 'string' && path.startsWith(prefix) && path.length <= 240 &&
    !/[\\#\x00-\x20]/.test(path) && new URL(path, 'http://fleet.invalid').href === 'http://fleet.invalid' + path;
}

// Synthetic database model, not a claim about production. These records model
// ThreadMembersOfTask, ThreadComposerSessions and SessionViewExecutions.
function modeledComposer(proof, id, project, task, sessionIds) {
  if (!exactKeys(proof, 'runs,sessions') || !Array.isArray(proof.sessions) || !Array.isArray(proof.runs) ||
      proof.sessions.length > 64 || proof.runs.length > 256) throw new Error('Conversation needs complete modeled sessions and runs');
  const sessions = new Map();
  for (const s of proof.sessions) {
    if (!exactKeys(s, 'archivedAt,createdAt,firstTaskId,id,projectId,taskId') || typeof s.id !== 'string' || !s.id ||
        sessions.has(s.id) || !Number.isSafeInteger(s.createdAt) ||
        !(s.archivedAt === null || Number.isSafeInteger(s.archivedAt))) throw new Error('Invalid modeled session');
    sessions.set(s.id, s);
  }
  const current = sessions.get(id);
  if (!current || current.projectId !== project || (!task && (current.taskId !== null || current.firstTaskId !== null)) ||
      (task && current.taskId !== task && !(current.taskId === null && current.firstTaskId === task)))
    throw new Error('Modeled current session is outside the thread');
  const seen = new Set(); let last = -1;
  for (const r of proof.runs) {
    if (!exactKeys(r, 'id,kind,observation,projectId,seq,sessionId,state,taskId') || r.kind !== 'agent_run' ||
        typeof r.id !== 'string' || !r.id || seen.has(r.id) || !sessions.has(r.sessionId) || !states.has(r.state) ||
        !['confirmed','unconfirmed'].includes(r.observation) || !Number.isSafeInteger(r.seq) || r.seq <= last)
      throw new Error('Invalid modeled agent run');
    const owner = sessions.get(r.sessionId);
    if (task ? !(r.taskId === task || r.taskId === null && (owner.taskId === task || owner.taskId === null && owner.firstTaskId === task)) : r.sessionId !== id || r.taskId !== null)
      throw new Error('Modeled run is outside the thread');
    seen.add(r.id); last = r.seq;
  }
  const memberSessions = new Set(proof.runs.map(r => r.sessionId));
  if (new Set(sessionIds).size !== sessionIds.length || sessionIds.some(x => !memberSessions.has(x)) ||
      memberSessions.size !== sessionIds.length) throw new Error('Thread run membership differs from modeled records');
  const eligible = task ? [...sessions.values()].filter(s => s.projectId === project && s.archivedAt === null &&
    (s.taskId === task || s.taskId === null && (s.id === id || memberSessions.has(s.id))))
    .sort((a,b) => b.createdAt - a.createdAt || b.id.localeCompare(a.id)) : [sessions.get(id)].filter(Boolean);
  const ids = new Set(eligible.map(s => s.id));
  const active = [...proof.runs].reverse().find(r => ids.has(r.sessionId) && r.observation === 'confirmed' &&
    !['succeeded','failed','cancelled'].includes(r.state) && (!task || r.state === 'running' && r.projectId === project));
  return { target: active?.sessionId ?? eligible[0]?.id ?? null, active: active?.id ?? null,
    latest: [...proof.runs].reverse().find(r => r.sessionId === id) };
}

export function checkFixture(fixture) {
  if (fixture?.version === 2) {
    checkImageFixture(fixture);
    return;
  }
  if (!exactKeys(fixture, 'cases,clock,version') || fixture.version !== 1 ||
      !Number.isSafeInteger(fixture.clock) ||
      fixture.clock < 946684800000 || fixture.clock > 4102444800000 ||
      !Array.isArray(fixture.cases) || !fixture.cases.length || fixture.cases.length > 2 ||
      new Set(fixture.cases.map(c => c?.scopeKey)).size !== fixture.cases.length ||
      Buffer.byteLength(JSON.stringify(fixture)) > maxFixture)
    throw new Error('Expected a bounded version 1 synthetic fixture');

  for (const item of fixture.cases) {
    const scenario = item?.scenario;
    if (!exactKeys(item, 'scenario,scopeKey') || typeof item.scopeKey !== 'string' || !scope.test(item.scopeKey) ||
        !(exactKeys(scenario, 'flow,landmark,responses,start,target') || exactKeys(scenario, 'flow,landmark,proof,responses,start,target')) ||
        typeof scenario.landmark !== 'string' || !scenario.landmark.trim() || scenario.landmark.length > 240 ||
        /[\u0000-\u001f\u2028\u2029]/.test(scenario.landmark) ||
        !Array.isArray(scenario.responses) || scenario.responses.length > 48 ||
        !scenario.responses.every(f => exactKeys(f, 'op,reply,request,required') && typeof f.required === 'boolean' &&
          typeof f.op === 'string' && /^[a-z_]{1,60}$/.test(f.op) && f.request && typeof f.request === 'object' && !Array.isArray(f.request) &&
          Buffer.byteLength(JSON.stringify(f.request)) <= 64 * 1024))
      throw new Error('Invalid normal app case or operation replies');
    const task = scenario.flow === 'task-details';
    if (!task && scenario.flow !== 'direct' ||
        ![scenario.start, scenario.target].every(path => task ?
          appRoute(path, '/projects/') && /^\/projects\/[A-Za-z0-9_-]+\/tasks\/T-[A-Za-z0-9-]+$/.test(path) :
          appRoute(path, '/conversations/')) ||
        scenario.start !== scenario.target ||
        !scenario.responses.some(r => r.required && r.op === (task ? 'task_get' : 'session_view')) ||
        !scenario.responses.some(r => r.required && r.op === 'boot'))
      throw new Error('The case must prove a Conversation or Task Details read at its normal route');
    if (!task) {
      const match = /^\/conversations\/([A-Za-z0-9_-]+)$/.exec(scenario.target);
      if (!match) throw new Error('Conversation route must name a single conversation');
      const code = /^C-[A-Z0-9]+-\d+$/i.test(match[1]) ? match[1] : null;
      const resolved = code && scenario.responses.find(r => r.required && r.op === 'entity_resolve' && String(r.request.code).toUpperCase() === code.toUpperCase());
      if (code && (!resolved || String(resolved.reply?.code).toUpperCase() !== code.toUpperCase() || resolved.reply?.type !== 'C'))
        throw new Error('Conversation code must resolve to the displayed conversation');
      const id = code ? resolved.reply.id : match[1];
      const view = scenario.responses.find(r => r.required && r.op === 'session_view' && r.request.sessionId === id);
      if (typeof id !== 'string' || !id || !view || view.reply?.head?.id !== id)
        throw new Error('Conversation route, request and returned head must identify the same conversation');
      const reply = view.reply, target = reply.composer?.sessionId;
      if (!reply.composer || (target != null && (typeof target !== 'string' || !target)) ||
          reply.context?.sessionId !== id || !Array.isArray(reply.questions) ||
          reply.questions.some(q => typeof q?.sessionId !== 'string' || !q.sessionId || (q.sessionId !== id && q.sessionId !== target)))
        throw new Error('Conversation context and questions must belong to the visible session or its composer target');
      const entries = scenario.responses.find(r => r.required && r.op === 'thread_entries' &&
        r.request.key?.session === id && r.request.resolve === true &&
        (r.reply?.key?.session === id || typeof r.reply?.key?.task === 'string'));
      if (!entries || !Array.isArray(entries.reply.sessionIds))
        throw new Error('The conversation must include its actual thread key');
      const threadTask = entries.reply.key.task;
      const rows = reply.executions?.rows;
      if (!Array.isArray(rows) || !Number.isSafeInteger(reply.executions?.total) || reply.executions.total < rows.length ||
          view.request.executionsBefore != null || !rows.every(r => Number.isSafeInteger(r.seq)))
        throw new Error('Conversation needs its latest visible execution page');
      const proof = scenario.proof;
      let model;
      if (!proof && !threadTask && reply.executions.total === 0 && entries.reply.sessionIds.length === 0) {
        model = { target: id, active: null, latest: undefined };
      } else {
        model = modeledComposer(proof, id, reply.head.projectId, threadTask, entries.reply.sessionIds);
      }
      if ((target ?? null) !== model.target || (reply.composer.activeExecutionId ?? null) !== model.active)
        throw new Error('Composer target or active run differs from modeled thread records');
      const visibleAgent = rows.find(r => r.kind === 'agent_run');
      if (visibleAgent && (!model.latest || visibleAgent.id !== model.latest.id || visibleAgent.state !== model.latest.state || visibleAgent.seq !== model.latest.seq))
        throw new Error('Visible latest agent differs from modeled records');
      if (!visibleAgent && model.latest && (!rows.length || !rows.every(row => row.seq > model.latest.seq) || reply.executions.total <= rows.length))
        throw new Error('Modeled latest agent is not behind the page boundary');
      const expectedState = model.latest?.state ?? 'idle';
      const expectedActive = visibleAgent && ['preparing','running','waiting'].includes(visibleAgent.state) ? visibleAgent.id : null;
      if (reply.head.state !== expectedState || (reply.head.activeExecutionId ?? null) !== expectedActive)
        throw new Error('Conversation state must match its newest modeled agent run');
    }
    if (item.scopeKey !== (task ? 'pages/tasks/app-shell:details' : 'pages/conversations/app-shell:default'))
      throw new Error('Case scope must match the normal route');
    if (task) {
      const [, , projectCode, , taskCode] = scenario.target.split('/');
      const resolved = scenario.responses.find(r => r.required && r.op === 'entity_resolve' && r.request.code === taskCode);
      const details = scenario.responses.find(r => r.required && r.op === 'task_get' && r.request.taskId === resolved?.reply?.id);
      const projectList = scenario.responses.find(r => r.op === 'project_list' &&
        r.reply?.projects?.some(p => p.code === projectCode && p.id === details?.reply?.projectId));
      if (!resolved || resolved.reply.code !== taskCode || resolved.reply.type !== 'T' ||
          !details || details.reply.id !== resolved.reply.id || !projectList)
        throw new Error('Task route, resolved code, project and Details fixture must name the same task');
    }
  }
}

function imageScenario(item) {
  const { start, target, responses, flow } = item.scenario;
  return { start, target, responses, ...item.journey, surface: flow === 'task-details' ? 'task' : 'conversation' };
}

function checkImageFixture(fixture) {
  if (!exactKeys(fixture, 'cases,clock,images,version') || !Array.isArray(fixture.cases) ||
      fixture.cases.length !== maxDesignerCases ||
      new Set(fixture.cases.map(item => item?.scopeKey)).size !== fixture.cases.length ||
      Buffer.byteLength(JSON.stringify(fixture)) > maxFixture)
    throw new Error('Expected the eight-case version 2 normal-route image fixture');
  validateImages(fixture.images);
  for (const item of fixture.cases) {
    if (!exactKeys(item, 'journey,scenario,scopeKey') || !item.scenario ||
        !(exactKeys(item.journey, 'entry,flow,image,resultCode,resultTitle') ||
          exactKeys(item.journey, 'entry,flow,image,resultCode,resultTitle,thumbnail')))
      throw new Error('Invalid fixed image journey');
    const image = imageScenario(item);
    const area = image.surface === 'task' ? 'tasks' : 'conversations';
    const baseScope = 'pages/' + area + '/app-shell:' + (area === 'tasks' ? 'details' : 'default');
    checkFixture({ version: 1, clock: fixture.clock, cases: [{ scopeKey: baseScope, scenario: item.scenario }] });
    if (!imageScenarioOK(image) || item.scopeKey !== 'pages/' + area + '/image-viewer:' + image.flow.slice('image-'.length))
      throw new Error('Image scope must match its normal route and fixed gesture');
    const media = resolveImages(image, fixture.images);
    const resultID = media.image.path.split('/').at(-1);
    const thumbnailID = media.thumbnail?.path.split('/').at(-1);
    if (image.surface === 'conversation' && !image.responses.some(row => row.op === 'session_view' && row.required &&
        row.request.includeQuestionArtifactIds === true && row.reply?.questions?.some(q => q.artifactIds?.includes(resultID))))
      throw new Error('Image fixture must opt in to Conversation question artifact IDs');

    const matchesResult = result => result?.kind === 'result' && result.id === resultID &&
      result.code === image.resultCode && result.title === image.resultTitle &&
      result.state === 'stored' && result.mediaType === 'image/png' &&
      (thumbnailID ? result.preview?.view === 'image' && result.preview.resultId === thumbnailID :
        !result.preview || result.preview.resultId === resultID);
    const matchesRef = ref => ref?.id === resultID || ref?.kind === 'result' && ref.code === image.resultCode;
    if (!image.responses.some(row => row.op === 'entity_refs' && row.required &&
        Array.isArray(row.reply?.items) && row.reply.items.some(result =>
          matchesResult(result) && Number.isInteger(result.ref) && matchesRef(row.request.refs?.[result.ref]))) ||
        image.entry === 'entity' && !image.responses.some(row => row.op === 'entity_card' && row.required &&
          matchesRef(row.request) && matchesResult(row.reply)))
      throw new Error('Image fixture must resolve the selected stored result and exact preview through normal operations');
  }
}

export function designerRenderScenario(item, fixture) {
  if (fixture.version === 1) return item.scenario;
  const scenario = imageScenario(item);
  return { ...scenario, ...resolveImages(scenario, fixture.images), landmark: item.scenario.landmark };
}
