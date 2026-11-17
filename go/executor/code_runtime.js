'use strict';
// Code mode runtime (docs/designs/fleet-code-mode.md): one Node process per code server. Each `run` line is the body of
// an async function; globals (`state`, `fleet`, `conn`) persist between runs. Bridged calls go to the code server as
// `call` lines on stdout and come back as `reply` lines on stdin.
const readline = require('node:readline');
const util = require('node:util');
const { AsyncLocalStorage } = require('node:async_hooks');

const out = process.stdout.write.bind(process.stdout);
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
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject });
    send({ type: 'call', job: job.getStore() ?? null, id, server, tool, args: args ?? {} });
  });
}

// Names that built-ins probe (`await`, JSON.stringify, String(), util.inspect) stay undefined, so probing sends no call;
// tool names are snake_case. `$tools` lists the tools; no granted tool name starts with `$`.
const probed = new Set(['then', 'toJSON', 'toString', 'valueOf', 'constructor', 'inspect', 'asymmetricMatch', 'nodeType']);
const tools = (server) => new Proxy({}, { get: (_, tool) => (typeof tool === 'string' && !probed.has(tool) ? (args) => call(server, tool, args) : undefined) });
const print = (...xs) => send({ type: 'print', job: job.getStore() ?? null, text: util.format(...xs) + '\n' });

globalThis.fleet = tools('fleet');
globalThis.conn = new Proxy({}, { get: (_, name) => (typeof name === 'string' && !probed.has(name) ? tools('conn:' + name) : undefined) });
globalThis.state = {};
globalThis.print = print;
globalThis.require = require;
globalThis.FleetCallError = FleetCallError;
for (const k of ['log', 'info', 'warn', 'error', 'debug']) console[k] = print;
process.stdout.write = (chunk, encoding, callback) => {
  print(String(chunk).replace(/\n$/, ''));
  const done = typeof encoding === 'function' ? encoding : callback;
  if (typeof done === 'function') process.nextTick(done);
  return true;
};
// An unawaited rejection or a throw in a timer is reported, not fatal: the runtime, its other jobs and `state` survive.
process.on('unhandledRejection', (e) => print('Unhandled rejection:', e));
process.on('uncaughtException', (e) => print('Uncaught exception:', e));

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
  if (m.type === 'reply') {
    const p = pending.get(m.id);
    pending.delete(m.id);
    if (p) m.ok ? p.resolve(m.value) : p.reject(new FleetCallError(m.error));
    return;
  }
  if (m.type !== 'run') return;
  job.run(m.job, async () => {
    try {
      const value = await new AsyncFunction(m.source)();
      send({ type: 'done', job: m.job, ok: true, value: plain(value) });
    } catch (e) {
      const error = { message: String(e?.message ?? e), stack: e?.stack ?? null };
      if (e instanceof FleetCallError) Object.assign(error, { tool: e.tool, outcome: e.outcome, code: e.code ?? null, output: e.output ?? null });
      send({ type: 'done', job: m.job, ok: false, error });
    }
  });
});
