// Task-owned transport for the isolated interface runner, never a production proxy.
import net, { type Socket } from 'node:net';
import { spawn, type ChildProcess } from 'node:child_process';
import { pathToFileURL } from 'node:url';

type Options = {
  host: string; sourcePort: number; listenPort: number; legPorts: readonly [number, number];
  signal?: AbortSignal; log?: (event: Record<string, unknown>) => void;
};
type Leg = { child: ChildProcess; port: number; exited: boolean; closed: Promise<void> };
type Launch = (args: string[]) => ChildProcess;
const port = (n: number, zero = false) => Number.isInteger(n) && n >= (zero ? 0 : 1) && n <= 65535;
const aborted = () => Object.assign(new Error('interface relay startup cancelled'), { name: 'AbortError' });

export async function startInterfaceRelay(o: Options, launch: Launch = args => spawn('ssh', args, { stdio: ['ignore', 'ignore', 'pipe'] })) {
  if (!/^[a-zA-Z0-9][a-zA-Z0-9._-]*$/.test(o.host) || !port(o.sourcePort) || !port(o.listenPort, true) ||
      o.legPorts.length !== 2 || o.legPorts.some(n => !port(n)) || new Set(o.legPorts).size !== 2 || o.legPorts.includes(o.listenPort)) {
    throw Error('use an SSH alias and distinct loopback listen/two leg ports');
  }
  if (o.signal?.aborted) throw aborted();
  const legs: Leg[] = [], flows = new Map<number, { down: Socket; up: Socket; closed: Promise<void> }>();
  let nextLeg = 0, nextFlow = 0, stopping = false, failure: Error | undefined, shutdown: Promise<void> | undefined;
  let resolveClosed!: () => void, rejectClosed!: (e: Error) => void;
  const closed = new Promise<void>((resolve, reject) => { resolveClosed = resolve; rejectClosed = reject; });
  void closed.catch(() => {}); // Startup can fail before a caller receives the runtime promise.
  const emit = (type: string, fields: Record<string, unknown> = {}) => {
    try { o.log?.({ type, wall: Date.now(), pid: process.pid, ...fields }); }
    catch (e) { void finish(e instanceof Error ? e : Error(String(e))); }
  };
  const server = net.createServer({ allowHalfOpen: true }, down => {
    if (stopping) { down.destroy(); return; }
    const leg = nextLeg++ % 2, id = ++nextFlow;
    down.pause();
    const up = net.createConnection({ host: '127.0.0.1', port: o.legPorts[leg], allowHalfOpen: true });
    let resolveFlow!: () => void;
    flows.set(id, { down, up, closed: new Promise(resolve => { resolveFlow = resolve; }) });
    emit('flow-open', { id, leg, active: flows.size });
    const fail = (side: string, e: Error & { code?: string }) => {
      emit('flow-error', { id, leg, side, code: e.code ?? e.name });
      down.destroy(); up.destroy();
    };
    down.on('error', e => fail('browser', e)); up.on('error', e => fail('source', e));
    up.once('connect', () => {
      if (stopping || down.destroyed) { up.destroy(); return; }
      down.pipe(up); up.pipe(down); down.resume();
    });
    let downClosed = false, upClosed = false;
    const complete = () => {
      if (!downClosed || !upClosed) return;
      flows.delete(id);
      emit('flow-close', { id, leg, fromBrowserBytes: down.bytesRead, toBrowserBytes: down.bytesWritten, active: flows.size });
      resolveFlow();
    };
    down.once('close', () => { downClosed = true; up.destroy(); complete(); });
    up.once('close', () => {
      upClosed = true;
      // A normal source FIN already ends down through pipe; let queued writes drain.
      if (!up.readableEnded) down.destroy();
      complete();
    });
  });
  server.on('error', e => { void finish(e); });
  const onAbort = () => { void finish(); };
  o.signal?.addEventListener('abort', onAbort, { once: true });

  function finish(e?: Error): Promise<void> {
    if (shutdown) { failure ??= e; return shutdown; }
    stopping = true; failure = e;
    shutdown = Promise.resolve().then(async () => {
      emit('stopping', { failed: !!failure, reason: failure?.message, active: flows.size });
      o.signal?.removeEventListener('abort', onAbort);
      const listenerClosed = new Promise<void>(resolve => { server.close(() => resolve()); });
      const streamsClosed = Promise.all([...flows.values()].map(flow => flow.closed));
      for (const { down, up } of flows.values()) { down.destroy(); up.destroy(); }
      await Promise.all(legs.map(async leg => {
        if (leg.exited) return;
        leg.child.kill('SIGTERM');
        const force = setTimeout(() => { if (!leg.exited) leg.child.kill('SIGKILL'); }, 2000);
        force.unref();
        await leg.closed; clearTimeout(force);
      }));
      await listenerClosed;
      await streamsClosed;
      emit('stopped', { failed: !!failure, active: flows.size });
      if (failure) rejectClosed(failure); else resolveClosed();
    });
    return shutdown;
  }

  try {
    const listening: Promise<void>[] = [];
    for (const [index, legPort] of o.legPorts.entries()) {
      if (stopping) throw failure ?? aborted();
      const child = launch(['-N', '-T', '-o', 'BatchMode=yes', '-o', 'ExitOnForwardFailure=yes',
        '-o', 'ControlMaster=no', '-o', 'ControlPath=none', '-o', 'ControlPersist=no', '-o', 'LogLevel=DEBUG1',
        '-L', `127.0.0.1:${legPort}:127.0.0.1:${o.sourcePort}`, o.host]);
      let resolveExit!: () => void, resolveReady!: () => void, rejectReady!: (e: Error) => void;
      const leg: Leg = { child, port: legPort, exited: false, closed: new Promise(resolve => { resolveExit = resolve; }) };
      legs.push(leg);
      const ready = new Promise<void>((resolve, reject) => { resolveReady = resolve; rejectReady = reject; });
      void ready.catch(() => {}); listening.push(ready);
      const fail = (e: Error) => { rejectReady(e); if (!stopping) void finish(e); };
      child.once('error', fail);
      child.once('close', (code, signal) => {
        leg.exited = true; resolveExit();
        emit('leg-exit', { leg: index, childPid: child.pid, code, signal });
        fail(Error(`interface source leg ${index} exited (${code ?? signal})`));
      });
      if (!child.stderr) throw Error('SSH diagnostics pipe unavailable');
      let tail = '', confirmed = false;
      child.stderr.on('data', (chunk: Buffer) => {
        // Authentication diagnostics are neither retained nor emitted. Bound even a long line.
        const lines = (tail + chunk.toString()).split('\n'); tail = lines.pop()!.slice(-8192);
        for (const line of lines) {
          if (!confirmed && line.trim() === `debug1: Local forwarding listening on 127.0.0.1 port ${legPort}.`) {
            confirmed = true; emit('leg-listening', { leg: index, childPid: child.pid, port: legPort }); resolveReady();
          }
        }
      });
    }
    await Promise.all(listening);
    if (stopping) throw failure ?? aborted();
    await new Promise<void>((resolve, reject) => {
      const onClose = () => reject(failure ?? aborted());
      server.once('error', reject); server.once('close', onClose);
      server.listen(o.listenPort, '127.0.0.1', () => {
        server.removeListener('error', reject); server.removeListener('close', onClose); resolve();
      });
    });
    if (stopping) throw failure ?? aborted();
    const address = server.address();
    if (!address || typeof address === 'string') throw Error('loopback listener unavailable');
    emit('ready', { host: o.host, sourcePort: o.sourcePort, listenPort: address.port, legs: legs.map(l => ({ port: l.port, childPid: l.child.pid })) });
    return { port: address.port, closed, stop: () => finish() };
  } catch (e) {
    // Child termination is expected when an operator cancels incomplete startup.
    await finish(failure ?? (o.signal?.aborted ? undefined : e instanceof Error ? e : Error(String(e))));
    if (!failure && o.signal?.aborted) throw aborted();
    throw failure ?? e;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const controller = new AbortController();
  const stop = () => controller.abort();
  const outputError = () => { process.exitCode = 1; controller.abort(); };
  process.once('SIGTERM', stop); process.once('SIGINT', stop);
  process.stdout.once('error', outputError);
  try {
    const args: Record<string, string> = {};
    for (const arg of process.argv.slice(2)) {
      const match = /^--(host|source-port|listen-port|leg-ports)=(.+)$/.exec(arg);
      if (!match || match[1] in args) throw Error('expected --host, --source-port, --listen-port and --leg-ports');
      args[match[1]] = match[2];
    }
    const legPorts = (args['leg-ports'] ?? '19494,19495').split(',').map(Number);
    if (legPorts.length !== 2) throw Error('exactly two source legs required');
    const relay = await startInterfaceRelay({ host: args.host ?? '', sourcePort: Number(args['source-port']),
      listenPort: Number(args['listen-port'] ?? 19493), legPorts: legPorts as [number, number], signal: controller.signal,
      log: event => console.log(JSON.stringify(event)) });
    await relay.closed;
  } catch (e) {
    if (!(controller.signal.aborted && e instanceof Error && e.name === 'AbortError')) {
      console.error(JSON.stringify({ type: 'relay-failed', error: e instanceof Error ? e.message : String(e) })); process.exitCode = 1;
    }
  } finally {
    process.removeListener('SIGTERM', stop); process.removeListener('SIGINT', stop);
    process.stdout.removeListener('error', outputError);
  }
}
