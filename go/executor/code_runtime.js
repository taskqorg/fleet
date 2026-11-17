'use strict';
// Code mode runtime (docs/designs/fleet-code-mode.md): one Node process per code server. Each `run` line is the body of
// an async function; globals (`state`, `fleet`, `conn`) persist between runs. Bridged calls go to the code server as
// `call` lines on stdout and come back as `reply` lines on stdin.
const readline = require('node:readline');
const util = require('node:util');
const { AsyncLocalStorage } = require('node:async_hooks');

// No network: bridged calls go through the code server, so the runtime needs none, and a harness without network
// (Codex without gitNetwork) gets none through code. Node 22's permission model has no network rule; this hook refuses
// the network modules for require and import, and --permission already refuses process.binding.
const offline = new Set(['net', 'http', 'https', 'http2', 'tls', 'dgram', 'dns', 'dns/promises', 'inspector', 'inspector/promises']);
require('node:module').registerHooks({
  resolve(specifier, context, next) {
    if (offline.has(specifier.replace(/^node:/, ''))) throw Object.assign(new Error(`${specifier}: no network in code mode`), { code: 'ERR_ACCESS_DENIED' });
    return next(specifier, context);
  },
});
for (const k of ['fetch', 'WebSocket', 'EventSource']) delete globalThis[k];

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

// `then` stays undefined so `await fleet` or `await conn.x` is not taken for a promise.
const tools = (server) => new Proxy({}, { get: (_, tool) => (typeof tool === 'string' && tool !== 'then' ? (args) => call(server, tool, args) : undefined) });
const print = (...xs) => send({ type: 'print', job: job.getStore() ?? null, text: util.format(...xs) + '\n' });

globalThis.fleet = tools('fleet');
globalThis.conn = new Proxy({}, { get: (_, name) => (typeof name === 'string' && name !== 'then' ? tools('conn:' + name) : undefined) });
globalThis.state = {};
globalThis.print = print;
globalThis.require = require;
globalThis.FleetCallError = FleetCallError;
for (const k of ['log', 'info', 'warn', 'error', 'debug']) console[k] = print;
process.stdout.write = (chunk) => (print(String(chunk).replace(/\n$/, '')), true);
// An unawaited rejection is reported, not fatal: the runtime and its `state` survive it.
process.on('unhandledRejection', (e) => print('Unhandled rejection:', e));

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
