// Requirement: nav-rail.md Counters (OR-34): one nav_counts call gives every section's navigation counter for the person,
// counting the section's counter view (a person setting, default Running / Running / In flight / Last 24 h / Offline) with
// its list's own filters, split by the focus, ignored projects never counted; agents read it as a tool.
// Risk: a counter disagrees with the list it opens (another state set, focus or ignore rule), counts an ignored project,
// shows a stale or invalid counter view, or agents cannot read what the person sees.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {startCoordinator} from '../harness.ts';
import {SYSTEM_VIEWS, NAV_COUNTER_DEFAULTS} from '../../app/src/system-views.ts';

// R2, R7, OR-33, OR-34; risk: a navigation counter disagrees with its list, counts an ignored project, keeps a stale view or is closed to agents.
test('nav_counts: counter views by person setting, the focus split, ignored projects left out, equal to the lists, and an agent tool', {timeout: 60_000}, async t => {
  const c = await startCoordinator({name: 'navcounts'}); t.after(() => c.stop());
  const call = async (name: string, body: object, auth = 'Bearer ' + c.secret) => {
    const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: auth, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: JSON.stringify(body)});
    return {status: r.status, d: await r.json() as any};
  };
  const op = async (name: string, body: object = {}) => { const r = await call(name, body); assert.equal(r.status, 200, name + ' ' + JSON.stringify(r.d)); return r.d; };
  for (const id of ['alpha', 'beta', 'muted'])
    await op('project_create', {id, requestId: 'nav-' + id, content: {settings: {id, name: id, contextFiles: ['rules.md'], roles: {pm: 'pm'}}, contextFiles: [{path: 'rules.md', text: 'x'}]}});
  // 'stray' has rows but no project definition; NULL is work outside projects.
  c.sql(`INSERT INTO task(id,project_id,title,state) VALUES ('t-a1','alpha','A','queued'),('t-a2','alpha','B','in_progress'),('t-a3','alpha','C','completed'),
      ('t-b1','beta','D','queued'),('t-b2','beta','E','failed'),('t-m1','muted','F','queued'),('t-s1','stray','G','waiting'),('t-n1',NULL,'H','queued');
    INSERT INTO document(kind,id,project_id,data) VALUES ('session','s-a1','alpha','{}'),('session','s-b1','beta','{}'),('session','s-m1','muted','{}'),('session','s-n1',NULL,'{}'),('session','s-idle','alpha','{}');
    INSERT INTO execution(id,kind,state,project_id,session_id,data) VALUES ('e-a1','agent_run','running','alpha','s-a1','{}'),('e-b1','agent_run','failed','beta','s-b1','{}'),
      ('e-m1','agent_run','running','muted','s-m1','{}'),('e-n1','agent_run','waiting',NULL,'s-n1','{}');
    INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,state,created_at) VALUES ('a-a1','alpha','prod','1',1,'text/plain','a.txt','stored',now()),
      ('a-b1','beta','prod','2',1,'text/plain','b.txt','stored',now()),('a-m1','muted','prod','3',1,'text/plain','m.txt','stored',now()),
      ('a-old','alpha','prod','4',1,'text/plain','old.txt','stored',now()-interval '2 days');`);
  const counts = async () => (await op('nav_counts')).sections;
  // The list's own count of those states: its per-state counts (the state quick filter), summed.
  const total = async (name: 'task_page' | 'sessions_list', filter: {state: string[]} & object) => {
    const counts = (await op(name, {filter, limit: 1})).counts as Record<string, number>;
    return filter.state.reduce((n, x) => n + (counts[x] ?? 0), 0);
  };
  const results = async (body: object) => (await op('artifact_list', {...body, since: Date.now() - 864e5})).artifacts.length as number;
  const states = (list: 'tasks' | 'conversations' | 'delivery', name: string) => SYSTEM_VIEWS[list].find(v => v.name === name)!.view.state as string[];

  // No focus, nothing ignored: one number per section, equal to the list with the app's own preset (system-views.ts).
  let s = await counts(), r = await op('nav_counts');
  assert.equal(r.focused, false);
  assert.deepEqual(Object.keys(s).sort(), ['conversations', 'delivery', 'hosts', 'results', 'tasks']);
  assert.deepEqual([s.tasks.view, s.conversations.view, s.delivery.view, s.results.view, s.hosts.view], ['Running', 'Running', 'In flight', 'Last 24 h', 'Offline']);
  assert.deepEqual(Object.fromEntries(Object.entries(s).map(([k, v]: [string, any]) => [k, v.view])), NAV_COUNTER_DEFAULTS, "the app's defaults");
  assert.equal(s.tasks.inFocus, await total('task_page', {state: states('tasks', 'Running')})); assert.equal(s.tasks.inFocus, 6);
  assert.equal(s.conversations.inFocus, await total('sessions_list', {state: states('conversations', 'Running')})); assert.equal(s.conversations.inFocus, 3);
  assert.equal(s.results.inFocus, await results({})); assert.equal(s.results.inFocus, 3, 'only the last 24 hours');
  assert.equal(s.delivery.inFocus, (await op('delivery_status', {})).items.filter((i: any) => states('delivery', 'In flight').includes(i.stage)).length);
  const hosts = await op('hosts_list', {});
  assert.equal(s.hosts.inFocus, hosts.hosts.filter((h: any) => !h.lastSeenAt || hosts.serverNow - h.lastSeenAt >= hosts.staleAfterMs).length, 'offline as the Hosts list judges it');
  for (const x of Object.values(s) as any[]) assert.deepEqual([x.outside, x.split], [0, false], 'no focus: nothing outside, no split');

  // Ignored: never counted, also without a focus; work outside projects and an undefined project stay.
  await op('person_settings_set', {ignore: {project: 'muted', ignored: true}});
  s = await counts();
  assert.equal(s.tasks.inFocus, await total('task_page', {state: states('tasks', 'Running'), excludeProjectId: ['muted']})); assert.equal(s.tasks.inFocus, 5);
  assert.equal(s.conversations.inFocus, 2); assert.equal(s.results.inFocus, 2);

  // Focus alpha and work outside projects: the split; outside is every other project that is not ignored.
  await op('person_settings_set', {focus: ['alpha', '-']});
  r = await op('nav_counts'); s = r.sections;
  assert.equal(r.focused, true);
  assert.equal(s.tasks.inFocus, await total('task_page', {state: states('tasks', 'Running'), projectId: ['alpha', null]})); assert.equal(s.tasks.inFocus, 3);
  assert.equal(s.tasks.outside, 2, 'beta and stray; muted is ignored');
  assert.deepEqual([s.conversations.inFocus, s.conversations.outside], [2, 0]);
  assert.deepEqual([s.results.inFocus, s.results.outside], [1, 1]);
  assert.deepEqual([s.hosts.outside, s.hosts.split, s.tasks.split], [0, false, true], 'hosts have no project: no split');

  // The counter view is a person setting: another system preset, a saved view, off, and back to the default.
  await op('person_settings_set', {navCounters: {tasks: 'Needs me'}});
  s = await counts();
  assert.deepEqual([s.tasks.view, s.tasks.label, s.tasks.inFocus, s.tasks.outside], ['Needs me', 'need me', 0, 2], 'failed in beta and waiting in stray');
  await op('person_settings_set', {viewPut: {list: 'tasks', name: 'Mine', view: {view: 'tasks', state: ['queued'], q: 'A', project: ['alpha', 'muted']}}, navCounters: {tasks: 'Mine'}});
  s = await counts();
  assert.deepEqual([s.tasks.view, s.tasks.label, s.tasks.inFocus, s.tasks.outside], ['Mine', 'Mine', 1, 0], 'its states, search and projects; the ignored project it names is still left out');
  // A saved view that names its projects is that list whatever the focus: all of it in focus, as the list shows it.
  await op('person_settings_set', {viewPut: {list: 'tasks', name: 'Beta', view: {view: 'tasks', state: ['queued', 'failed'], project: ['beta']}}, navCounters: {tasks: 'Beta'}});
  s = await counts();
  assert.deepEqual([s.tasks.inFocus, s.tasks.outside, s.tasks.split], [await total('task_page', {state: ['queued', 'failed'], projectId: ['beta']}), 0, false]); assert.equal(s.tasks.inFocus, 2);
  // A saved view the list filters no longer take counts the default instead of failing every counter.
  c.sql(`UPDATE document SET data=jsonb_set(data,'{views}',(SELECT jsonb_agg(CASE WHEN v->>'name'='Beta' THEN jsonb_set(v,'{view,state}','["no_such_state"]') ELSE v END) FROM jsonb_array_elements(data->'views') v)) WHERE kind='person-setting'`);
  s = await counts();
  assert.equal(s.tasks.view, 'Running', 'a view the list refuses counts the default'); assert.equal(s.results.view, 'Last 24 h');
  await op('person_settings_set', {viewDelete: {list: 'tasks', name: 'Beta'}, navCounters: {tasks: 'Mine'}});
  await op('person_settings_set', {viewDelete: {list: 'tasks', name: 'Mine'}});
  assert.equal((await counts()).tasks.view, 'Running', 'a saved view since removed counts the default');
  await op('person_settings_set', {navCounters: {results: '-', conversations: 'Needs me'}});
  s = await counts();
  assert.equal(s.results, undefined, 'off: no counter'); assert.equal(s.conversations.view, 'Needs me');
  const set = await op('person_settings_set', {navCounters: {results: '', conversations: ''}});
  assert.deepEqual(set.navCounters, {}, "an empty value removes the choice, and a removed view's counter went with it");
  await op('person_settings_set', {viewPut: {list: 'tasks', name: 'Mine', view: {view: 'tasks'}}});
  assert.equal((await counts()).tasks.view, 'Running', 'a later view of the same name is not counted unchosen');
  await op('person_settings_set', {viewDelete: {list: 'tasks', name: 'Mine'}});
  assert.ok((await op('setting_changes', {target: 'person:owner@trial.test'})).changes.some((x: any) => x.field === 'navCounters'), 'audited, so other tabs reread');
  for (const bad of [{navCounters: {overview: 'Running'}}, {navCounters: {tasks: 'No such view'}}, {navCounters: {results: 'Running'}}, {navCounters: {hosts: 'Mine'}}])
    assert.equal((await call('person_settings_set', bad)).status, 400, JSON.stringify(bad));

  // Every system preset counts what its list shows with the app's own states: a task and a conversation in every state
  // inside the focus, so a state missing from either side's preset changes a count.
  c.sql(`INSERT INTO task(id,project_id,title,state,pause_reason) VALUES ('t-x-q','alpha','x','queued',NULL),('t-x-i','alpha','x','in_progress',NULL),
      ('t-x-w','alpha','x','waiting',NULL),('t-x-h','alpha','x','paused','held'),('t-x-p','alpha','x','paused',NULL),('t-x-c','alpha','x','completed',NULL),
      ('t-x-f','alpha','x','failed',NULL),('t-x-x','alpha','x','cancelled',NULL);
    INSERT INTO document(kind,id,project_id,data) SELECT 'session','s-x-'||s,'alpha','{}' FROM unnest(ARRAY['preparing','running','waiting','succeeded','failed','cancelled']) s;
    INSERT INTO execution(id,kind,state,project_id,session_id,data) SELECT 'e-x-'||s,'agent_run',s::execution_state,'alpha','s-x-'||s,'{}' FROM unnest(ARRAY['preparing','running','waiting','succeeded','failed','cancelled']) s;`);
  for (const list of ['tasks', 'conversations', 'delivery'] as const) for (const v of SYSTEM_VIEWS[list]) {
    await op('person_settings_set', {navCounters: {[list]: v.name}});
    const x = (await counts())[list], st = v.view.state as string[];
    assert.equal(x.view, v.name);
    const want = list === 'delivery' ? (await op('delivery_status', {projectIds: ['alpha', '-']})).items.filter((i: any) => st.includes(i.stage)).length
      : await total(list === 'tasks' ? 'task_page' : 'sessions_list', {state: st, projectId: ['alpha', null]});
    assert.equal(x.inFocus, want, `${list} ${v.name}`);
  }
  await op('person_settings_set', {navCounters: {tasks: '', conversations: '', delivery: ''}});

  // An agent reads its director's counters through the same operation, which agents get as a tool.
  await op('person_settings_set', {navCounters: {tasks: ''}});
  const host = await op('hosts_register', {id: 'h-nav', holder: 'owner@trial.test'});
  const poll = async () => (await (await fetch(c.base + '/api/hosts/poll', {method: 'POST', headers: {authorization: 'Bearer ' + host.secret, 'content-type': 'application/json'}, body: JSON.stringify({platform: 'darwin', capabilities: [], stores: [], problems: []})})).json()) as any;
  await poll();
  const sess = await op('session_start', {projectId: 'alpha', role: 'pm', requestId: randomUUID(), text: 'Count for me'});
  const cmd = (await poll()).commands.find((x: any) => x.sessionId === sess.sessionId && x.kind === 'start');
  assert.ok(cmd, 'start command issued');
  const a = await fetch(c.base + '/api/hosts/agent-tools', {method: 'POST', headers: {authorization: 'Bearer ' + host.secret, 'content-type': 'application/json'}, body: JSON.stringify({hostId: 'h-nav', commandId: cmd.id, executionId: cmd.executionId, agentToolKey: cmd.payload.agentToolKey,
    request: {jsonrpc: '2.0', id: 1, method: 'tools/call', params: {name: 'nav_counts', arguments: {}}}})});
  const body: any = await a.json();
  assert.equal(a.status, 200, JSON.stringify(body)); assert.ok(!body.error && !body.result?.isError, JSON.stringify(body));
  const agent = JSON.parse(body.result.content[0].text);
  const mine = await op('nav_counts');
  assert.equal(agent.focused, true); assert.deepEqual(agent.sections.tasks, mine.sections.tasks, "the director's counters");
});
