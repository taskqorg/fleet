// Synthetic fixture v1. SQL generation and expected enumeration deliberately use separate paths.
import { createHash } from 'node:crypto';
import { writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

export const kinds = ['agent_run', 'agent_run', 'tool_run', 'pipeline_run', 'pipeline_step', 'child_agent'] as const;
export const tools = ['read', 'search', 'write'] as const;
export const marker = '!qualification-progress';
export type Fixture = { version: 1; seed: 1; history: number; projects: number; cardinality: number };
export const projectIDs = (f: Fixture) => Array.from({ length: f.projects }, (_, i) => `interface-seed1-${i + 1}`);
export const literal = (s: string) => "'" + s.replaceAll("'", "''") + "'";
export function fixture(history = 618, projects = 1, cardinality = 3): Fixture {
  if (!Number.isSafeInteger(history) || history < 1 || history > 1_000_000 || ![1, 3, 5].includes(projects) || !Number.isSafeInteger(cardinality) || cardinality < 3 || cardinality > 100_000) throw Error('invalid fixture dimensions');
  return { version: 1, seed: 1, history, projects, cardinality };
}

type Metric = { sum: number; present: number; expected: number; bases: Record<string, number> };
type Expected = { executions: number; missingTotals: number; measuredZeroTotals: number; metrics: Record<string, Metric>; tools: Record<string, { calls: number; errors: number }>; directCostsCents: Record<string, number>; estimatedEURMicro: number };
// This enumerator never reads generated SQL, production reducers, contribution tables or expected
// output computed by the implementation. Integers retain exact precision across the full ladder.
export function expected(f: Fixture) {
  const result: Record<string, Expected> = Object.fromEntries(projectIDs(f).map(p => [p, { executions: 0, missingTotals: 0, measuredZeroTotals: 0, metrics: {}, tools: {}, directCostsCents: {}, estimatedEURMicro: 0 }]));
  const add = (p: Expected, name: string, value: number | null, basis: string, required: boolean) => {
    const m = p.metrics[name] ??= { sum: 0, present: 0, expected: 0, bases: {} };
    if (required) m.expected++;
    if (value !== null) { m.sum += value; m.present++; m.bases[basis] = (m.bases[basis] ?? 0) + 1; }
  };
  const tool = (p: Expected, name: string, calls: number, errors: number) => { const t = p.tools[name] ??= { calls: 0, errors: 0 }; t.calls += calls; t.errors += errors; };
  for (let i = 0; i < f.history; i++) {
    const kind = kinds[i % 6], child = kind === 'child_agent', agent = kind === 'agent_run' || child;
    const hasTotals = (i + 1) % 7 !== 0 && (!child || Math.floor(i / 6) % 2 === 0);
    if (kind === 'pipeline_run' || kind === 'pipeline_step' || (child && !hasTotals)) continue;
    const p = result[projectIDs(f)[i % f.projects]]; p.executions++;
    if (!hasTotals) p.missingTotals++;
    const v = (i + 1) % 11 === 0 ? 0 : i % 97;
    if (hasTotals && (i + 1) % 11 === 0) p.measuredZeroTotals++;
    const basis = i % 3 === 1 ? 'estimated' : 'measured';
    for (const [name, n] of Object.entries({ input: v, output: v * 2, cacheRead: v % 5, cacheWrite: v % 3, reasoning: v % 7 })) add(p, name, hasTotals && agent ? n : null, basis, agent);
    for (const [name, n] of Object.entries({ wallMs: v * 10, activeMs: v * 8, apiMs: v * 2, cpuMs: v * 3, peakMemoryBytes: v * 1024, turns: v % 4 })) {
      const required = !['cpuMs', 'peakMemoryBytes'].includes(name) || agent;
      add(p, name, hasTotals && required ? n : null, basis, required);
    }
    let calls: number | null = null, errors: number | null = null;
    if (kind === 'tool_run') { calls = 1; errors = i % 13 === 0 ? 1 : 0; tool(p, tools[i % 3], calls, errors); }
    else if (hasTotals) { calls = v % 5; errors = calls > 0 && i % 17 === 0 ? 1 : 0; tool(p, `tool-${String(i % f.cardinality).padStart(6, '0')}`, calls, errors); }
    add(p, 'toolCalls', calls, kind === 'tool_run' && !hasTotals ? 'measured' : basis, true);
    add(p, 'toolErrors', errors, kind === 'tool_run' && !hasTotals ? 'measured' : basis, true);
    if (hasTotals && i % 3 !== 1) {
      const key = i % 3 === 2 ? 'GBP/subscription' : 'USD/measured';
      p.directCostsCents[key] = (p.directCostsCents[key] ?? 0) + v;
    } else if (hasTotals && agent) p.estimatedEURMicro += v + v * 2 + v % 5 + v % 3;
  }
  return result;
}

export function manifest(f: Fixture) {
  const content = { ...f, synthetic: true, referenceCounts: [618, 18794], kinds, fixedTools: tools, currencies: ['USD', 'EUR', 'GBP'], bases: ['measured', 'estimated', 'subscription'], expected: expected(f), longExecution: { acceptedUsageEvents: 100_000, eventFixture: 'separate; public host acceptance and dedup/reorder are measured by runner' } };
  return { ...content, sha256: createHash('sha256').update(JSON.stringify(content)).digest('hex') };
}

// generate_series keeps seed payload/memory bounded at one million rows. Batch endpoints are
// fixture-owned, literal SQL integers only; never a supplied database URL or production project.
export function seedSQL(f: Fixture, first: number, last: number) {
  if (!Number.isInteger(first) || !Number.isInteger(last) || first < 0 || last < first || last >= f.history) throw Error('invalid seed range');
  return `BEGIN;
  CREATE TEMP TABLE fixture_rows ON COMMIT DROP AS
  SELECT i, 'interface-seed1-e-'||lpad(i::text,8,'0') AS id,
   'interface-seed1-'||(1+i%${f.projects}) AS project,
   (ARRAY['agent_run','agent_run','tool_run','pipeline_run','pipeline_step','child_agent'])[1+i%6] AS kind,
   ((i+1)%7<>0 AND (i%6<>5 OR (i/6)%2=0)) AS totals,
   CASE WHEN (i+1)%11=0 THEN 0 ELSE i%97 END AS v,
   CASE WHEN i%3=1 THEN 'estimated' ELSE 'measured' END AS basis
  FROM generate_series(${first},${last}) i;
  INSERT INTO execution(id,kind,state,project_id,model,started_at,ended_at,data)
  SELECT id,kind::execution_kind,CASE WHEN i%13=0 THEN 'failed' ELSE 'succeeded' END::execution_state,project,'interface-seed1-model-'||(i%${f.cardinality}),
   '2024-01-01'::timestamptz+i*interval '1 second','2024-01-01'::timestamptz+i*interval '1 second'+interval '1 second',
   jsonb_build_object('tool',(ARRAY['read','search','write'])[1+i%3]) FROM fixture_rows;
  INSERT INTO execution_totals(execution_id,project_id,tokens_input,tokens_input_basis,tokens_output,tokens_output_basis,tokens_cache_read,tokens_cache_read_basis,tokens_cache_write,tokens_cache_write_basis,tokens_reasoning,tokens_reasoning_basis,wall_ms,wall_ms_basis,active_ms,active_ms_basis,api_ms,api_ms_basis,cpu_ms,cpu_ms_basis,peak_memory_bytes,peak_memory_bytes_basis,turn_count,turn_count_basis,tool_calls,tool_calls_basis,connection_cost,connection_cost_basis,billing_basis)
  SELECT id,project,CASE WHEN kind<>'tool_run' THEN v END,basis,CASE WHEN kind<>'tool_run' THEN v*2 END,basis,
   CASE WHEN kind<>'tool_run' THEN v%5 END,basis,CASE WHEN kind<>'tool_run' THEN v%3 END,basis,CASE WHEN kind<>'tool_run' THEN v%7 END,basis,
   v*10,basis,v*8,basis,v*2,basis,CASE WHEN kind<>'tool_run' THEN v*3 END,basis,CASE WHEN kind<>'tool_run' THEN v*1024 END,basis,v%4,basis,
   CASE WHEN kind<>'tool_run' THEN jsonb_build_object('byTool',jsonb_build_object('tool-'||lpad((i%${f.cardinality})::text,6,'0'),jsonb_build_object('calls',v%5,'errors',CASE WHEN v%5>0 AND i%17=0 THEN 1 ELSE 0 END))) END,basis,
   CASE WHEN i%3<>1 THEN jsonb_build_object('currency',CASE WHEN i%3=2 THEN 'GBP' ELSE 'USD' END,'amount',(v::numeric/100)::text) END,'measured',CASE WHEN i%3=2 THEN 'subscription' ELSE 'measured' END
  FROM fixture_rows WHERE totals AND kind NOT IN ('pipeline_run','pipeline_step');
  COMMIT;`;
}

export function pricesSQL(cardinality = 3) {
  fixture(1, 1, cardinality);
  return `INSERT INTO price_entry(id,subject,unit,price,currency,source,observed_at,effective_from) SELECT 'interface-seed1-price-'||i||'-'||u,'interface-seed1-model-'||i,u,0.000001,'EUR','owner','2023-01-01','2023-01-01' FROM generate_series(0,${cardinality - 1}) i CROSS JOIN unnest(ARRAY['input_tokens','output_tokens','cache_read_tokens','cache_write_tokens']) u;`;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const args = Object.fromEntries(process.argv.slice(2).map(a => { const [k, v] = a.replace(/^--/, '').split('='); return [k, v]; }));
  const f = fixture(Number(args.history ?? 618), Number(args.projects ?? 1), Number(args.cardinality ?? 3));
  if (args.sql) writeFileSync(args.sql, pricesSQL(f.cardinality) + '\n' + seedSQL(f, 0, f.history - 1));
  const output = JSON.stringify(manifest(f), null, 2) + '\n';
  if (args.out) writeFileSync(args.out, output); else process.stdout.write(output);
}
