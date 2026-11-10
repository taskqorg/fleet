// Operation timing at scale (report only): 100,000 tasks and 5,000 sessions, median of 7 per case.
// Run: node tests/coordinator/bench-100k.ts [postgres-url]   (default PGlite in memory; a URL must name a disposable, migrated-or-empty database)
import { createStore } from '../../packages/coordinator/store.ts';
import { createApp } from '../../packages/coordinator/app.ts';
// Before the change Overview made one more task_page per project; now one grouped call carries every project's counts.
const url = process.argv[2] ?? 'memory://', N = 100000, S = 5000;
const store = createStore(url); await store.migrate();
const med = (a: number[]) => [...a].sort((x, y) => x - y)[a.length >> 1];
let t = performance.now();
await store.tx(async () => {
  for (let i = 0; i < N; i++) {
    const id = await store.tasks.create({ title: ['Render prop batch', 'Fix CI cache key', 'Rework hero copy', 'Write release notes', 'Compare 3D methods'][i % 5] + ' ' + i, projectId: ['dashq', 'game', 'fleet', 'cs', 'specq', 'lab'][i % 6], priority: ['urgent', 'normal', 'background'][i % 3] });
    if (i % 5 === 0) { await store.executions.createExecution({ id: 'run-' + i, kind: 'agent_run', taskId: id, projectId: ['dashq', 'game', 'fleet', 'cs', 'specq', 'lab'][i % 6], hostId: ['pds18', 'mac', 'windows'][i % 3], harness: ['codex', 'claude', 'antigravity'][i % 3], state: 'running' }); await store.tasks.transition(id, 'first_execution').catch(() => {}); }
  }
});
await store.query(`INSERT INTO document(kind,id,data,project_id) SELECT 'session','s'||g,jsonb_build_object('createdAt',now()),'fleet' FROM generate_series(1,${S}) g`);
await store.query(`INSERT INTO execution(id,kind,state,session_id,project_id,harness) SELECT 'se'||g||'-'||k,'agent_run',(CASE k WHEN 3 THEN 'succeeded' ELSE 'failed' END)::execution_state,'s'||g,'fleet','claude' FROM generate_series(1,${S}) g, generate_series(1,3) k`);
await store.query(`INSERT INTO document(kind,id,data,project_id) SELECT (CASE WHEN k<=5 THEN 'message' ELSE 'event' END),'m'||g||'-'||k,jsonb_build_object('sessionId','s'||g,'text','message '||k||' of s'||g),'fleet' FROM generate_series(1,${S}) g, generate_series(1,25) k`);
console.log(`fixture: ${N} tasks, ${S} sessions, ${((performance.now() - t) / 1000).toFixed(0)} s`);
const app: any = createApp({ store, database: 'memory://', browserSecret: 'b', machineSecret: 'm', hostId: 'pds18', nativeHostIds: [] });
await app.ready();
const CASES: [string, string, any][] = [
  ['task_page all, group priority', 'task_page', { filter: { state: [] }, group: 'priority', limit: 100 }],
  ['task_page group agent', 'task_page', { filter: { state: [] }, group: 'agent', limit: 100 }],
  ['task_page group project (Overview: per-project counts in one call)', 'task_page', { group: 'project', groupCounts: true, limit: 1 }],
  ['task_page search "hero"', 'task_page', { filter: { state: [], search: 'hero' }, group: 'priority', limit: 100 }],
  ['sessions_list first page', 'sessions_list', { limit: 100 }],
  ['sessions_list search', 'sessions_list', { filter: { search: 'message 1 of s49' }, limit: 100 }],
];
const call = (op: string, b: any) => app.inject({ method: 'POST', url: '/api/ops/' + op, headers: { authorization: 'Bearer b' }, payload: b });
for (const [name, op, b] of CASES) {
  const a: number[] = []; let st = 0;
  for (let i = 0; i < 7; i++) { const s = performance.now(); st = (await call(op, b)).statusCode; a.push(performance.now() - s); }
  console.log(`${name}: ${med(a).toFixed(0)} ms (${st})`);
}
await app.close(); await store.close();
