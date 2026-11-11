// TypeScript types from contracts/*.schema.json (fleet-stack L3). Run by `pnpm run typecheck` and `pnpm run build`; the
// output in contracts/generated/ is not committed. go/gen reads the same files for Go. Both accept only
// the keyword subset in contracts/README.md and fail on anything else, so a schema never yields a silently weaker type.
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';

const dir = new URL('./', import.meta.url);
const KEYS = new Set(['$comment', 'type', 'properties', 'required', 'additionalProperties', 'items', 'enum', '$ref', 'minimum', 'const', 'oneOf']);
const SCALAR = { string: 'string', integer: 'number', number: 'number', boolean: 'boolean', null: 'null' };

function type(s, at) {
  for (const k of Object.keys(s)) if (!KEYS.has(k)) throw Error(`${at}: unsupported keyword ${k}`);
  if (s.$ref) {
    const m = /^(?:([a-z-]+)\.schema\.json)?#\/\$defs\/(\w+)$/.exec(s.$ref);
    if (!m) throw Error(`${at}: $ref must be [<file>.schema.json]#/$defs/<Name>`);
    if (m[1]) imports.add(`import type { ${m[2]} } from './${m[1]}.ts';`);
    return m[2];
  }
  if (s.oneOf) {
    if (/[.[]/.test(at) || s.oneOf.some((x) => !x.$ref || Object.keys(x).length !== 1)) throw Error(`${at}: oneOf must be a $defs entry of $refs`);
    return s.oneOf.map((x) => type(x, at)).join(' | ');
  }
  if (s.const !== undefined) {
    if (typeof s.const !== 'string' || (s.type !== undefined && s.type !== 'string')) throw Error(`${at}: const must be a string, with no other type`);
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

let imports = new Set();
const domains = [];
const seen = new Map(); // operation or planned name -> file: one name, one file across all ops files
mkdirSync(new URL('generated/', dir), { recursive: true });
for (const f of readdirSync(dir).filter((f) => f.endsWith('.schema.json'))) {
  const root = JSON.parse(readFileSync(new URL(f, dir), 'utf8'));
  imports = new Set();
  const out = Object.entries(root.$defs).map(([name, s]) => `export type ${name} = ${type(s, name)};`);
  // operations: each name maps to its <Name>Request and <Name>Reply (contracts/README.md, Operations).
  // Each ops-<domain> file exports Ops<Domain>; ops.ts re-exports every domain file and joins them into Ops (below).
  const opsName = 'Ops' + f.replace(/^ops-?|\.schema\.json$/g, '').split('-').filter(Boolean).map((p) => p[0].toUpperCase() + p.slice(1)).join('');
  if (/^ops-/.test(f)) domains.push([f, opsName]);
  else if (root.operations || root.planned) throw Error(`${f}: operations and planned belong in ops-<domain>.schema.json`);
  for (const n of [...Object.keys(root.operations ?? {}), ...Object.keys(root.planned ?? {})]) {
    if (seen.has(n)) throw Error(`${f}: operation ${n} is also in ${seen.get(n)} (built or planned): keep one`);
    seen.set(n, f);
  }
  if (root.operations) { out.push(`export type ${opsName} = {\n${Object.keys(root.operations).map((n) => {
    const t = n.split(/[_.-]/).filter(Boolean).map((p) => p[0].toUpperCase() + p.slice(1)).join('');
    for (const d of [t + 'Request', t + 'Reply']) if (!root.$defs[d]) throw Error(`${f}: operation ${n} has no $defs ${d}`);
    return `  ${JSON.stringify(n)}: { request: ${t}Request; reply: ${t}Reply };`;
  }).join('\n')}\n};`); }
  for (const n of Object.keys(root.planned ?? {})) {
    if (root.operations?.[n]) throw Error(`${f}: operation ${n} is both planned and built`);
    const t = n.split(/[_.-]/).filter(Boolean).map((p) => p[0].toUpperCase() + p.slice(1)).join('');
    for (const d of [t + 'Request', t + 'Reply']) if (!root.$defs[d]) throw Error(`${f}: planned operation ${n} has no $defs ${d}`);
  }
  writeFileSync(new URL(`generated/${f.replace('.schema.json', '.ts')}`, dir), `// Generated from contracts/${f} by contracts/gen-ts.mjs. Do not edit.\n\n${imports.size ? [...imports].join('\n') + '\n\n' : ''}${out.join('\n\n')}\n`);
}
// ops.ts: the shared types, every domain file re-exported, and Ops over all of them, so callers import from one file.
if (domains.length) {
  const ops = new URL('generated/ops.ts', dir);
  const files = domains.map(([f]) => './' + f.replace('.schema.json', '.ts'));
  const built = domains.filter(([f]) => JSON.parse(readFileSync(new URL(f, dir), 'utf8')).operations);
  writeFileSync(ops, readFileSync(ops, 'utf8') + `\n${files.map((m) => `export type * from '${m}';`).join('\n')}\n${built.map(([f, n]) => `import type { ${n} } from './${f.replace('.schema.json', '.ts')}';`).join('\n')}\n\nexport type Ops = ${built.map(([, n]) => n).join(' & ')};\n`);
}
