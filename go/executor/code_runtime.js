'use strict';
// Code mode runtime (docs/designs/fleet-code-mode.md): one Node process per code server. Each `run` line is the body of
// an async function; globals (`state`, `fleet`, `conn`) persist between runs. Bridged calls go to the code server as
// `call` lines on stdout and come back as `reply` lines on stdin.
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

function plain(v) {
  if (v === undefined) return null;
  try {
    return JSON.parse(JSON.stringify(v));
  } catch {
    return util.inspect(v, { depth: 6 });
  }
}

const AsyncFunction = (async () => {}).constructor;
readline.createInterface({ input: process.stdin }).on('line', (line) => {
  const m = JSON.parse(line);
  if (m.type === 'tools') {
    globalThis.fleet = tools('fleet', m.fleet);
    globalThis.conn = Object.fromEntries(Object.entries(m.conns).map(([name, names]) => [name, tools('conn:' + name, names)]));
    return;
  }
  if (m.type === 'reply') {
    const p = pending.get(m.id);
    pending.delete(m.id);
    if (p) m.ok ? p.resolve(m.value) : p.reject(new FleetCallError({ ...m.error, job: p.owner }));
    return;
  }
  if (m.type !== 'run') return;
  job.run(m.job, async () => {
    try {
      const value = await new AsyncFunction(m.source)();
      send({ type: 'done', job: m.job, ok: true, value: plain(value) });
    } catch (e) {
      // Any thrown value ends the job, even one that cannot be turned into a string.
      const text = (f) => { try { const v = f(); return v == null ? null : String(v); } catch { return null; } };
      const error = { message: text(() => e?.message ?? e) ?? 'unprintable thrown value', stack: text(() => e?.stack) };
      if (e instanceof FleetCallError) Object.assign(error, { tool: e.tool, outcome: e.outcome, code: e.code ?? null, output: e.output ?? null });
      send({ type: 'done', job: m.job, ok: false, error });
    }
  });
});
