// Requirement: the h2 measurement origin gives Chromium the production HTTP/2 transport, pins the fixture's own key,
// forwards ordinary JSON and EventSource streams unchanged, and drops only connection-specific headers.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import http2 from 'node:http2';
import { createHash, X509Certificate } from 'node:crypto';
import { existsSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { ephemeralCertificate, proxyRequest } from '../performance/interface.ts';

async function origin() {
  const seen: http.IncomingHttpHeaders[] = [], errors: Error[] = [];
  let streamsClosed = 0;
  const upstream = http.createServer((req, res) => {
    seen.push(req.headers);
    if (req.url === '/api/slow') { res.on('close', () => streamsClosed++); return; } // never answers
    if (req.url === '/api/stream') { res.on('close', () => streamsClosed++); res.writeHead(200, { 'content-type': 'text/event-stream', connection: 'keep-alive, x-hop' , 'x-hop': 'connection-only' }); res.write('event: ready\ndata: {"cursor":7}\n\n'); return; }
    res.writeHead(200, { 'content-type': 'application/json', connection: 'keep-alive' }); res.end(JSON.stringify({ path: req.url }));
  });
  await new Promise<void>(r => upstream.listen(0, '127.0.0.1', r));
  const tls = ephemeralCertificate(mkdtempSync(join(tmpdir(), 'h2-origin-')));
  const server = http2.createSecureServer({ key: tls.key, cert: tls.cert, allowHTTP1: true }, (req, res) => proxyRequest(req as any, res as any, 'http://127.0.0.1:' + (upstream.address() as any).port, { 'x-fleet-person': 'owner@trial.test' }, e => errors.push(e)));
  await new Promise<void>(r => server.listen(0, '127.0.0.1', r));
  return { seen, errors, streamsClosed: () => streamsClosed, tls, url: 'https://127.0.0.1:' + (server.address() as any).port, close: () => { server.close(); upstream.closeAllConnections(); upstream.close(); } };
}

test('h2 origin forwards JSON and an open event stream, dropping only connection-specific headers', async () => {
  const o = await origin(), session = http2.connect(o.url, { ca: o.tls.cert });
  const get = (path: string, untilReady = false) => new Promise<{ headers: http2.IncomingHttpHeaders; body: string }>((ok, fail) => {
    const r = session.request({ ':path': path, ':method': 'POST', 'content-type': 'application/json' }); let body = '', headers: http2.IncomingHttpHeaders = {};
    r.on('response', h => { headers = h; }); r.on('data', c => { body += c; if (untilReady && body.includes('\n\n')) { r.close(); ok({ headers, body }); } }); r.on('end', () => ok({ headers, body })); r.on('error', fail); r.end('{}');
  });
  try {
    const json = await get('/api/ops/project_list'), stream = await get('/api/stream', true);
    assert.equal(json.headers[':status'], 200); assert.deepEqual(JSON.parse(json.body), { path: '/api/ops/project_list' });
    assert.equal(stream.headers['content-type'], 'text/event-stream'); assert.equal(stream.body, 'event: ready\ndata: {"cursor":7}\n\n');
    assert.equal(json.headers.connection, undefined);
    assert.equal(o.seen.length, 2);
    for (const h of o.seen) { assert.equal(h['x-fleet-person'], 'owner@trial.test'); assert.equal(h['content-type'], 'application/json'); assert.ok(!Object.keys(h).some(k => k.startsWith(':'))); }
    assert.equal(stream.headers['x-hop'], undefined, 'a Connection-listed header is connection-specific');
    for (let i = 0; i < 100 && o.streamsClosed() < 1; i++) await new Promise(r => setTimeout(r, 10));
    assert.equal(o.streamsClosed(), 1, 'closing the client stream ends the coordinator subscription');
    const slow = session.request({ ':path': '/api/slow', ':method': 'GET' }); slow.on('error', () => {}); slow.end();
    for (let i = 0; i < 100 && o.seen.length < 3; i++) await new Promise(r => setTimeout(r, 10));
    slow.close(http2.constants.NGHTTP2_CANCEL);
    for (let i = 0; i < 100 && o.streamsClosed() < 2; i++) await new Promise(r => setTimeout(r, 10));
    assert.equal(o.streamsClosed(), 2, 'a stream reset before the coordinator answered also ends its request');
    await new Promise(r => setTimeout(r, 50));
    assert.deepEqual(o.errors, []);
    assert.equal(new X509Certificate(o.tls.cert).fingerprint256, o.tls.fingerprint256);
  } finally { session.close(); o.close(); }
});

const { chromium } = await import('playwright');
test('Chromium uses HTTP/2 with only the pinned fixture key', { skip: !existsSync(chromium.executablePath()) && 'Chromium not installed', timeout: 60000 }, async () => {
  const o = await origin();
  const spki = createHash('sha256').update(new X509Certificate(o.tls.cert).publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
  const pinned = await chromium.launch({ headless: true, args: ['--ignore-certificate-errors-spki-list=' + spki] });
  const strict = await chromium.launch({ headless: true });
  try {
    const page = await (await pinned.newContext()).newPage();
    await page.goto(o.url + '/start');
    const result = await page.evaluate(async () => {
      const json = await (await fetch('/api/ops/project_list', { method: 'POST', body: '{}' })).json();
      const ready = await new Promise<string>(ok => { const es = new EventSource('/api/stream'); es.addEventListener('ready', e => { es.close(); ok((e as MessageEvent).data); }); });
      const protocols = performance.getEntriesByType('resource').map(e => (e as PerformanceResourceTiming).nextHopProtocol);
      return { json, ready, protocols, navigation: (performance.getEntriesByType('navigation')[0] as PerformanceResourceTiming).nextHopProtocol };
    });
    assert.deepEqual(result.json, { path: '/api/ops/project_list' });
    assert.equal(result.ready, '{"cursor":7}');
    assert.equal(result.navigation, 'h2');
    assert.ok(result.protocols.length > 0 && result.protocols.every(p => p === 'h2'), JSON.stringify(result.protocols));
    await assert.rejects((await (await strict.newContext()).newPage()).goto(o.url + '/start'), /ERR_CERT/, 'an unpinned browser still rejects the certificate');
  } finally { await pinned.close(); await strict.close(); o.close(); }
});
