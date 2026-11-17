'use strict';
// Code mode runtime (docs/designs/fleet-code-mode.md): one Node process per code server. Each `run` line is the body of
// an async function; globals (`state`, `fleet`, `conn`) persist between runs. Bridged calls go to the code server as
// `call` lines on stdout and come back as `reply` lines on stdin.
// One closure: user code (built with the AsyncFunction constructor, global scope) sees only what is put on globalThis.
(() => {
const readline = require('node:readline');
const util = require('node:util');
const { AsyncLocalStorage } = require('node:async_hooks');

// The protocol pipe is fd 3, so fd 1 stays free for raw writes.
const fs = require('node:fs');
const out = (text) => fs.writeSync(3, text);
const send = (m) => out(JSON.stringify(m) + '\n');
const job = new AsyncLocalStorage();
const pending = new Map();
let nextId = 1;

class FleetCallError extends Error {
  constructor(d) {
    super(`${d.tool}: ${d.outcome}${d.code ? ' ' + d.code : ''}`);
    this.name = 'FleetCallError';
    Object.assign(this, d);
  }
}

function call(server, tool, args) {
  const id = nextId++;
  const owner = job.getStore() ?? null;
  return new Promise((resolve, reject) => {
    let line;
    try {
      line = JSON.stringify({ type: 'call', job: owner, id, server, tool, args: args ?? {} });
    } catch (e) {
      const label = server.startsWith('conn:') ? server.slice(5) + '.' + tool : tool;
      return reject(new FleetCallError({ tool: label, outcome: 'error', code: 'bad-arguments', output: String(e.message), job: owner }));
    }
    pending.set(id, { resolve, reject, owner });
    out(line + '\n');
  });
}

// `fleet` and `conn` hold one function per granted name (the code server's first `tools` line), so a typo or a probe
// such as `then` or `toJSON` is undefined, not a call. `$tools()` lists names and schemas; no granted name starts with
// `$`.
const tools = (server, names) => {
  const o = { $tools: () => call(server, '$tools', {}) };
  for (const n of names) o[n] = (args) => call(server, n, args);
  return o;
};
const emit = (text) => send({ type: 'print', job: job.getStore() ?? null, text });
const print = (...xs) => emit(util.format(...xs) + '\n');

// Host functions (fleet-code-mode.md, host functions). read, glob and grep run here, within the read roots; write, edit
// and sh are bridged: the code server checks them against the execution's grant.
const path = require('node:path');
let root = process.cwd(); // the worktree, as the code server names it (the tools line): the read roots use that spelling
const resolve = (p) => path.resolve(root, String(p));
const shown = (abs) => { const r = path.relative(root, abs); return r.startsWith('..') || path.isAbsolute(r) ? abs : r; };
// Asynchronous, so a walk or grep inside Promise.all lets other calls and jobs run between its reads.
async function walk(dir, out) {
  for (const d of await fs.promises.readdir(dir, { withFileTypes: true })) {
    const p = path.join(dir, d.name);
    if (d.isDirectory()) {
      if (d.name !== '.git' && d.name !== 'node_modules') await walk(p, out);
    } else if (d.isFile()) out.push(p); // symlinks are not followed
  }
  return out;
}
// Each entry relative to the walk's start, as a glob pattern sees it; returned relative to the worktree.
async function files(opts, pattern) {
  const start = resolve(opts?.path ?? '.');
  const dir = (await fs.promises.stat(start)).isDirectory();
  const all = dir ? await walk(start, []) : [start];
  // A file given as path matches by its name.
  return all.filter((f) => pattern == null || path.matchesGlob(dir ? path.relative(start, f) : path.basename(f), pattern));
}
// A file's text, or null when its first 8 KiB hold a NUL byte (binary): then the rest is not read.
async function textOf(f) {
  const h = await fs.promises.open(f);
  try {
    const head = Buffer.alloc(8192);
    const { bytesRead } = await h.read(head, 0, head.length, 0);
    if (head.subarray(0, bytesRead).includes(0)) return null;
    return (await h.readFile()).toString('utf8'); // a positional read leaves the position at 0
  } finally {
    await h.close();
  }
}
// A rejected bad-arguments call when an argument is not a string, else undefined.
const strings = (tool, ...xs) => xs.every((x) => typeof x === 'string') ? undefined :
  Promise.reject(new FleetCallError({ tool, outcome: 'error', code: 'bad-arguments', output: 'path and text must be strings', job: job.getStore() ?? null }));
const host = {
  read: (p) => fs.promises.readFile(resolve(p), 'utf8'),
  glob: async (pattern, opts) => (await files(opts, pattern)).map(shown),
  grep: async (pattern, opts) => {
    const flags = (pattern instanceof RegExp ? pattern.flags.replace(/[gyi]/g, '') : '') + (opts?.ignoreCase || pattern?.flags?.includes('i') ? 'i' : '');
    const re = new RegExp(pattern instanceof RegExp ? pattern.source : String(pattern), flags);
    const hits = [];
    // 32 files at a time, in walk order; a file that cannot be read as text (too large for a string) is skipped.
    // A glob without a slash matches file names at any depth, as ripgrep's.
    const g = opts?.glob;
    const list = await files(opts, g != null && !String(g).includes('/') ? '**/' + g : g);
    for (let i = 0; i < list.length; i += 32) {
      const texts = await Promise.all(list.slice(i, i + 32).map((f) => textOf(f).catch(() => null)));
      texts.forEach((t, k) => t?.split('\n').forEach((text, n) => { if (re.test(text)) hits.push({ file: shown(list[i + k]), line: n + 1, text }); }));
    }
    return hits;
  },
  // A missing or non-string text is an error, never the file contents "undefined".
  write: (p, text) => strings('write', p, text) ?? call('host', 'write', { path: p, text }),
  edit: (p, old, text, opts) => strings('edit', p, old, text) ?? call('host', 'edit', { path: p, old, new: text, all: opts?.all === true }),
  sh: (cmd, ...rest) => {
    let opts = {};
    if (Array.isArray(cmd) && Array.isArray(cmd.raw)) {
      // A tagged template: each interpolated value is one shell word, an array several.
      const word = (v) => Array.isArray(v) ? v.map(word).join(' ') : "'" + String(v).replaceAll("'", "'\\''") + "'";
      cmd = cmd.raw.reduce((a, s, i) => a + word(rest[i - 1]) + s);
    } else {
      opts = rest[0] ?? {};
    }
    return call('host', 'sh', { command: String(cmd), cwd: opts.cwd, timeoutMs: opts.timeoutMs }).then((r) => new ShResult(r));
  },
};
class ShResult {
  constructor(r) { Object.assign(this, r); }
  text() { return this.stdout.trimEnd(); }
  json() { return JSON.parse(this.stdout); }
  lines() { return this.stdout.split('\n').filter((l) => l !== ''); }
}

globalThis.fleet = tools('fleet', []);
globalThis.conn = {};
globalThis.state = {};
globalThis.print = print;
globalThis.require = require;
globalThis.FleetCallError = FleetCallError;
for (const k of ['log', 'info', 'warn', 'error', 'debug']) console[k] = print;
// Raw writes keep their bytes: no added newline, typed arrays as text.
process.stdout.write = (chunk, encoding, callback) => {
  const bytes = typeof chunk === 'string' ? Buffer.from(chunk, typeof encoding === 'string' ? encoding : 'utf8') : Buffer.from(chunk.buffer, chunk.byteOffset, chunk.byteLength);
  emit(bytes.toString('utf8'));
  const done = typeof encoding === 'function' ? encoding : callback;
  if (typeof done === 'function') process.nextTick(done);
  return true;
};
// An unawaited rejection or a throw in a timer is reported, not fatal: the runtime, its other jobs and `state` survive.
// The report goes to the job that made the failed call (FleetCallError carries it), else to the job in context.
const report = (what, e) => send({ type: 'print', job: e?.job ?? job.getStore() ?? null, text: util.format(what, e) + '\n' });
process.on('unhandledRejection', (e) => report('Unhandled rejection:', e));
process.on('uncaughtException', (e) => report('Uncaught exception:', e));

// The returned value as JSON text, serialized once: undefined or a function is null, a value JSON cannot hold (a cycle,
// a BigInt) its util.inspect text.
function valueJSON(v) {
  try {
    return JSON.stringify(v) ?? 'null';
  } catch {
    return JSON.stringify(util.inspect(v, { depth: 6 }));
  }
}

const AsyncFunction = (async () => {}).constructor;
const compiles = (body) => { try { new AsyncFunction(body); return true; } catch { return false; } };
// Without an explicit return, the last expression statement is the result: the shortest tail after a newline or `;` that
// compiles as one expression after a prefix that compiles on its own. A line that starts with an operator or a dot
// continues the line before it, so it is never split off. A line that starts with `(`, `[` or a template starts the
// result, unlike plain JavaScript, so a last line `({a, b})` returns the object instead of calling the line before.
// `!` alone starts a new statement (a semicolon is inserted before it); `!=` continues.
const continues = /^\s*([-+*/%<>=&|^?:,.)\]}]|!=)/;
const declaration = /^\s*((async\s+)?function\b|class\b)/;
function body(source) {
  if (!compiles(source)) return source; // its own syntax error, unchanged
  for (let i = source.length; i >= 0; i--) {
    if (i > 0 && source[i - 1] !== '\n' && source[i - 1] !== ';') continue;
    const tail = source.slice(i).replace(/[\s;]+$/, '');
    if (tail.trim() === '' || i > 0 && source[i - 1] === '\n' && continues.test(tail)) continue;
    if (declaration.test(tail)) continue; // `return (function f() {})` would no longer hoist f
    // The tail alone first: most tails fail at their first tokens, so a script without a last expression costs about
    // one parse, not one per line.
    if (!compiles('return (' + tail + '\n)')) continue;
    const prefix = source.slice(0, i);
    // A `;` inside a trailing line comment is no statement end: there `prefix (` still compiles, the `(` commented out.
    if (source[i - 1] === ';' && compiles(prefix + ' (')) continue;
    const wrapped = prefix + '\nreturn (' + tail + '\n)';
    if (compiles(wrapped) && compiles(prefix)) return wrapped;
  }
  return source;
}

readline.createInterface({ input: process.stdin }).on('line', (line) => {
  const m = JSON.parse(line);
  if (m.type === 'tools') {
    globalThis.fleet = tools('fleet', m.fleet);
    globalThis.conn = Object.fromEntries(Object.entries(m.conns).map(([name, names]) => [name, tools('conn:' + name, names)]));
    root = m.dir ?? root;
    for (const n of m.host ?? []) globalThis[n] = host[n];
    return;
  }
  if (m.type === 'reply') {
    const p = pending.get(m.id);
    pending.delete(m.id);
    if (p) m.ok ? p.resolve(m.value) : p.reject(new FleetCallError({ ...m.error, job: p.owner }));
    // After the promise callbacks (microtasks) ran: a call they chained is already sent when the server sees this.
    setImmediate(() => out(JSON.stringify({ type: 'settled', id: m.id }) + '\n'));
    return;
  }
  if (m.type !== 'run') return;
  job.run(m.job, async () => {
    try {
      const value = await new AsyncFunction(body(m.source))();
      out(`{"type":"done","job":${JSON.stringify(m.job)},"ok":true,"value":${valueJSON(value)}}\n`);
    } catch (e) {
      // Any thrown value ends the job, even one that cannot be turned into a string.
      const text = (f) => { try { const v = f(); return v == null ? null : String(v); } catch { return null; } };
      const error = { message: text(() => e?.message ?? e) ?? 'unprintable thrown value', stack: text(() => e?.stack) };
      if (e instanceof FleetCallError) Object.assign(error, { tool: e.tool, outcome: e.outcome, code: e.code ?? null, output: e.output ?? null });
      send({ type: 'done', job: m.job, ok: false, error });
    }
  });
});
})();
