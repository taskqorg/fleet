import { test } from 'node:test';
import assert from 'node:assert/strict';
import net, { type Server, type Socket } from 'node:net';
import { spawn, type ChildProcess } from 'node:child_process';
import { once } from 'node:events';
import { createHash } from 'node:crypto';
import { startInterfaceRelay } from '../performance/relay-interface.ts';

const listen = async (server: Server) => {
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const address = server.address(); assert.ok(address && typeof address !== 'string'); return address.port;
};
const close = (server: Server) => new Promise<void>(resolve => server.close(() => resolve()));
async function legPorts(): Promise<[number, number]> {
  const servers = [net.createServer(), net.createServer()];
  const ports = await Promise.all(servers.map(listen)); await Promise.all(servers.map(close));
  return ports as [number, number];
}
const dial = async (port: number) => {
  const socket = net.createConnection({ host: '127.0.0.1', port, allowHalfOpen: true });
  await once(socket, 'connect'); return socket;
};
const hash = (data: Buffer) => createHash('sha256').update(data).digest('hex');

// Real child listeners stand in for SSH; dedicated-host regressions still exercise actual SSH.
const childProgram = String.raw`
import net from 'node:net';
const [port, source, mode, marker] = process.argv.slice(1); const sockets = new Set();
process.on('message', message => { if (message === 'fail') process.exit(23); });
process.on('SIGTERM', () => { for (const socket of sockets) socket.destroy(); process.exit(0); });
if (mode !== 'standby') {
 process.stderr.write('debug1: Local forwarding listening on 127.0.0.1 port '+port+'.\n');
 const server = net.createServer({allowHalfOpen:true}, down => {
  const up = net.createConnection({host:'127.0.0.1',port:Number(source),allowHalfOpen:true});
  sockets.add(down); sockets.add(up); down.pipe(up); up.pipe(down);
  down.on('error',()=>up.destroy()); up.on('error',()=>down.destroy());
  down.on('close',()=>{sockets.delete(down);up.destroy();});
  up.on('close',()=>{sockets.delete(up);if(!up.readableEnded)down.destroy();});
 });
 server.listen(Number(port),'127.0.0.1',()=>{
  process.stderr.write('debug1: Authentication diagnostic PRIVATE_SENTINEL\n');
  process.stdout.write(marker+'\n');
  process.send({ready:true});
 });
}`;
function launcher(mode: 'normal' | 'partial' | 'standby' = 'normal') {
  const children: ChildProcess[] = []; let firstListening = false;
  const failSecond = () => { if (firstListening && children[1]?.connected) children[1].send('fail'); };
  const launch = (args: string[]) => {
    const forward = args[args.indexOf('-L') + 1].split(':');
    const index = children.length;
    const marker = /FLEET_INTERFACE_READY_[a-f0-9]+/.exec(args.find(a => a.startsWith('LocalCommand=')) ?? '')?.[0];
    assert.ok(marker);
    const child = spawn(process.execPath, ['--input-type=module', '-e', childProgram, forward[1], forward[3],
      mode === 'standby' || mode === 'partial' && index === 1 ? 'standby' : 'normal', marker], { stdio: ['ignore', 'pipe', 'pipe', 'ipc'] });
    children.push(child);
    if (mode === 'partial' && index === 0) child.on('message', () => {
      // Observe the first real listener before intentionally failing the second child.
      void dial(Number(forward[1])).then(socket => { socket.destroy(); firstListening = true; failSecond(); });
    });
    failSecond(); return child;
  };
  return { launch, children, firstListening: () => firstListening };
}
function source(handler: (socket: Socket) => void) {
  const sockets = new Set<Socket>();
  const server = net.createServer({ allowHalfOpen: true }, socket => {
    sockets.add(socket); socket.on('error', () => {}); socket.on('close', () => sockets.delete(socket)); handler(socket);
  });
  return { server, stop: async () => { for (const socket of sockets) socket.destroy(); await close(server); } };
}
const assertChildrenGone = (children: ChildProcess[]) => {
  assert.equal(children.length, 2);
  for (const child of children) assert.ok(child.exitCode !== null || child.signalCode !== null, `child ${child.pid} still running`);
};

// Requirement: the transport preserves byte identities, backpressure and request/reply half-close across two legs.
// Risk: a FIN or slow browser truncates a reply, or one source SSH process still receives every connection.
test('interface relay balances real streams and drains replies after request FIN', { timeout: 10000 }, async t => {
  const reply = Buffer.alloc(2 * 1024 * 1024, 0x71), events: Record<string, unknown>[] = [];
  const upstream = source(socket => {
    const sha = createHash('sha256'); let bytes = 0;
    socket.on('data', chunk => { sha.update(chunk); bytes += chunk.length; });
    socket.on('end', () => socket.end(Buffer.concat([Buffer.from(JSON.stringify({ bytes, sha: sha.digest('hex') }) + '\n'), reply])));
  });
  const sourcePort = await listen(upstream.server); t.after(upstream.stop);
  const fake = launcher(), ports = await legPorts();
  const relay = await startInterfaceRelay({ host: 'isolated-test', sourcePort, listenPort: 0, legPorts: ports, log: e => events.push(e) }, fake.launch);
  t.after(relay.stop);
  await Promise.all(Array.from({ length: 4 }, async (_, index) => {
    const request = Buffer.alloc(1024 * 1024, index), socket = await dial(relay.port), chunks: Buffer[] = [];
    socket.pause(); socket.on('data', chunk => chunks.push(chunk));
    const ended = once(socket, 'end'); socket.end(request);
    await new Promise(resolve => setTimeout(resolve, 30)); socket.resume(); await ended;
    const response = Buffer.concat(chunks), split = response.indexOf(10);
    assert.deepEqual(JSON.parse(response.subarray(0, split).toString()), { bytes: request.length, sha: hash(request) });
    assert.equal(response.length - split - 1, reply.length); assert.equal(hash(response.subarray(split + 1)), hash(reply));
    socket.destroy();
  }));
  await relay.stop(); await relay.closed;
  assert.deepEqual(events.filter(e => e.type === 'flow-open').map(e => e.leg), [0, 1, 0, 1]);
  assert.equal(events.filter(e => e.type === 'flow-close').length, 4);
  assert.equal(events.at(-1)?.active, 0); assertChildrenGone(fake.children);
  assert.ok(!JSON.stringify(events).includes('PRIVATE_SENTINEL'));
});

// Requirement: a source-first FIN must not discard the browser request still queued toward a slow source.
// Risk: closing the finished browser socket destroys the source socket before its pending writes flush.
test('interface relay drains the whole request after an early source FIN and slow source reads', { timeout: 10000 }, async t => {
  const request = Buffer.alloc(8 * 1024 * 1024, 0x37);
  let requestEnded = false, resolveReceived!: (r: { bytes: number; sha: string }) => void;
  const received = new Promise<{ bytes: number; sha: string }>(resolve => { resolveReceived = resolve; });
  const upstream = source(socket => {
    const sha = createHash('sha256'); let bytes = 0;
    socket.pause(); socket.end('source-ended-first');
    let resume = setTimeout(() => socket.resume(), 50);
    socket.on('data', chunk => {
      sha.update(chunk); bytes += chunk.length; socket.pause();
      resume = setTimeout(() => socket.resume(), 1);
    });
    socket.on('end', () => { requestEnded = true; });
    socket.on('close', () => { clearTimeout(resume); resolveReceived({ bytes, sha: sha.digest('hex') }); });
  });
  const sourcePort = await listen(upstream.server); t.after(upstream.stop);
  const fake = launcher();
  const relay = await startInterfaceRelay({ host: 'isolated-test', sourcePort, listenPort: 0, legPorts: await legPorts() }, fake.launch);
  t.after(relay.stop);
  const socket = await dial(relay.port); t.after(() => socket.destroy());
  const chunks: Buffer[] = []; socket.on('data', chunk => chunks.push(chunk));
  const sourceFIN = once(socket, 'end'); socket.end(request); await sourceFIN;
  assert.equal(Buffer.concat(chunks).toString(), 'source-ended-first');
  assert.equal(requestEnded, false, 'source must finish its response before receiving the request FIN');
  assert.deepEqual(await received, { bytes: request.length, sha: hash(request) });
  assert.ok(requestEnded); await relay.stop(); assertChildrenGone(fake.children);
});

// Requirement: an SSH leg exit fails the transport and stops the other owned leg without replaying a flow.
// Risk: failure becomes an invisible retry on another source connection and corrupts qualification evidence.
test('interface relay exposes a leg exit and cancels its active streams without replay', { timeout: 10000 }, async t => {
  let sourceConnections = 0;
  const upstream = source(socket => { sourceConnections++; socket.pipe(socket); });
  const sourcePort = await listen(upstream.server); t.after(upstream.stop);
  const fake = launcher(), events: Record<string, unknown>[] = [];
  const relay = await startInterfaceRelay({ host: 'isolated-test', sourcePort, listenPort: 0, legPorts: await legPorts(), log: e => events.push(e) }, fake.launch);
  t.after(relay.stop);
  const socket = await dial(relay.port); t.after(() => socket.destroy());
  const received = once(socket, 'data'); socket.write('already-sent');
  assert.equal(String((await received)[0]), 'already-sent');
  const rejected = assert.rejects(relay.closed, /source leg 0 exited/);
  fake.children[0].kill('SIGTERM'); await rejected;
  assert.equal(sourceConnections, 1); assert.equal(events.filter(e => e.type === 'flow-open').length, 1);
  assertChildrenGone(fake.children);
  await assert.rejects(dial(relay.port), { code: 'ECONNREFUSED' });
});

// Requirement: partial startup releases the first leg and never accepts browser streams before both legs are ready.
// Risk: a failed second SSH process leaves an orphan listener or a misleading ready relay.
test('interface relay cleans a real first listener when its second child fails during startup', { timeout: 10000 }, async t => {
  const upstream = source(socket => socket.end()); const sourcePort = await listen(upstream.server); t.after(upstream.stop);
  const fake = launcher('partial'), ports = await legPorts(), events: Record<string, unknown>[] = [];
  await assert.rejects(startInterfaceRelay({ host: 'isolated-test', sourcePort, listenPort: 0, legPorts: ports, log: e => events.push(e) }, fake.launch), /source leg 1 exited/);
  assert.ok(fake.firstListening()); assertChildrenGone(fake.children);
  assert.ok(!events.some(e => e.type === 'ready'));
  for (const p of ports) await assert.rejects(dial(p), { code: 'ECONNREFUSED' });
});

// Requirement: cancellation and a busy bridge port stop only owned SSH children, preserving existing listeners.
// Risk: startup hangs or cleanup kills a foreign service to obtain a successful bind.
test('interface relay cancels pending startup and preserves a busy unrelated listener', { timeout: 10000 }, async t => {
  const existing = net.createServer(socket => socket.end('existing'));
  const occupied = await listen(existing); t.after(() => close(existing));
  const fake = launcher('standby'), controller = new AbortController();
  const pending = startInterfaceRelay({ host: 'isolated-test', sourcePort: occupied, listenPort: 0, legPorts: await legPorts(), signal: controller.signal }, fake.launch);
  controller.abort(); await assert.rejects(pending, { name: 'AbortError' }); assertChildrenGone(fake.children);
  const second = launcher();
  await assert.rejects(startInterfaceRelay({ host: 'isolated-test', sourcePort: occupied, listenPort: occupied, legPorts: await legPorts() }, second.launch), { code: 'EADDRINUSE' });
  assertChildrenGone(second.children); assert.ok(existing.listening);
  const socket = await dial(occupied); const data = once(socket, 'data'); assert.equal(String((await data)[0]), 'existing'); socket.destroy();
});

// Requirement: readiness comes after forwarding creation, never from OpenSSH's early pre-bind diagnostic.
// Risk: a busy source-leg port is falsely ready and forwards a browser flow to an unrelated listener.
test('interface relay rejects a busy leg despite its pre-bind listening log without contacting that port', { timeout: 10000 }, async t => {
  let contacted = 0;
  const existing = net.createServer(socket => { contacted++; socket.end('foreign-leg'); });
  const occupied = await listen(existing); t.after(() => close(existing));
  const ports = await legPorts(), fake = launcher(), events: Record<string, unknown>[] = [];
  await assert.rejects(startInterfaceRelay({ host: 'isolated-test', sourcePort: occupied, listenPort: 0,
    legPorts: [ports[0], occupied], log: e => events.push(e) }, fake.launch), /source leg 1 exited/);
  assert.ok(!events.some(e => e.type === 'ready' || e.type === 'flow-open' || e.type === 'leg-listening' && e.leg === 1));
  assert.equal(contacted, 0); assertChildrenGone(fake.children); assert.ok(existing.listening);
  const socket = await dial(occupied); const data = once(socket, 'data');
  assert.equal(String((await data)[0]), 'foreign-leg'); socket.destroy();
});
