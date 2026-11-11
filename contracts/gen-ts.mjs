// TypeScript types from contracts/*.schema.json (fleet-stack L3). Run by `pnpm run typecheck` and `pnpm run build`; the
// output in contracts/generated/ is not committed. go/gen reads the same files for Go. Both accept only
// the keyword subset in contracts/README.md and fail on anything else, so a schema never yields a silently weaker type.
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';

const dir = new URL('./', import.meta.url);
const KEYS = new Set(['$comment', 'type', 'properties', 'required', 'additionalProperties', 'items', 'enum', '$ref', 'minimum', 'const', 'oneOf']);
const SCALAR = { string: 'string', integer: 'number', number: 'number', boolean: 'boolean', null: 'null' };

function type(s, at) {
  for (const k of Object.keys(s)) if (!KEYS.has(k)) throw Error(`${at}: unsupported keyword ${k}`);
  if (s.$ref) return s.$ref.replace(/^#\/\$defs\//, '');
  if (s.oneOf) {
    if (/[.[]/.test(at) || s.oneOf.some((x) => !x.$ref)) throw Error(`${at}: oneOf must be a $defs entry of $refs`);
    return s.oneOf.map((x) => type(x, at)).join(' | ');
  }
  if (s.const !== undefined) {
    if (typeof s.const !== 'string') throw Error(`${at}: const must be a string`);
    return JSON.stringify(s.const);
  }
  if (s.enum && /[.[]/.test(at)) throw Error(`${at}: an enum must be a $defs entry (Go needs its name)`);
  if (s.enum) return s.enum.map((v) => JSON.stringify(v)).join(' | ');
  if (s.type === undefined) return 'unknown';
  if (Array.isArray(s.type)) return s.type.map((t) => type({ ...s, type: t }, at)).join(' | ');
  if (s.type === 'array') return `Array<${type(s.items ?? {}, at + '[]')}>`;
  if (s.type === 'object') {
    const props = Object.entries(s.properties ?? {});
    if (!props.length && typeof s.additionalProperties === 'object') return `Record<string, ${type(s.additionalProperties, at + '.*')}>`;
    if (!props.length) return 'Record<string, unknown>';
    if (/[.[]/.test(at)) throw Error(`${at}: an object with properties must be a $defs entry (Go needs its name)`);
    const req = new Set(s.required ?? []);
    return `{\n${props.map(([k, v]) => `  ${JSON.stringify(k)}${req.has(k) ? '' : '?'}: ${type(v, `${at}.${k}`).replace(/\n/g, '\n  ')};`).join('\n')}\n}`;
  }
  if (SCALAR[s.type]) return SCALAR[s.type];
  throw Error(`${at}: unsupported type ${s.type}`);
}

mkdirSync(new URL('generated/', dir), { recursive: true });
for (const f of readdirSync(dir).filter((f) => f.endsWith('.schema.json'))) {
  const root = JSON.parse(readFileSync(new URL(f, dir), 'utf8'));
  const out = Object.entries(root.$defs).map(([name, s]) => `export type ${name} = ${type(s, name)};`);
  // operations: each name maps to its <Name>Request and <Name>Reply (contracts/README.md, Operations).
  if (root.operations) out.push(`export type Ops = {\n${Object.keys(root.operations).map((n) => {
    const t = n.split(/[_.-]/).filter(Boolean).map((p) => p[0].toUpperCase() + p.slice(1)).join('');
    for (const d of [t + 'Request', t + 'Reply']) if (!root.$defs[d]) throw Error(`${f}: operation ${n} has no $defs ${d}`);
    return `  ${JSON.stringify(n)}: { request: ${t}Request; reply: ${t}Reply };`;
  }).join('\n')}\n};`);
  writeFileSync(new URL(`generated/${f.replace('.schema.json', '.ts')}`, dir), `// Generated from contracts/${f} by contracts/gen-ts.mjs. Do not edit.\n\n${out.join('\n\n')}\n`);
}
