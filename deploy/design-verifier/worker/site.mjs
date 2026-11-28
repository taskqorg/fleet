// In-memory HTTP for the normal release bundle inside the browser network namespace.
// Only the operator's synthetic answers are served. There is no upstream.
import { createServer } from 'node:http';
import { imageBytes, isImageFlow } from './image-journey.mjs';

const mime = new Map([
  ['html', 'text/html; charset=utf-8'], ['js', 'text/javascript; charset=utf-8'], ['css', 'text/css; charset=utf-8'],
  ['png', 'image/png'], ['svg', 'image/svg+xml'], ['webmanifest', 'application/manifest+json'],
]);

const canonical = value => Array.isArray(value) ? value.map(canonical) : value && typeof value === 'object' ?
  Object.fromEntries(Object.keys(value).sort().map(key => [key, canonical(value[key])])) : value;
const key = (op, value) => op + ':' + JSON.stringify(canonical(value));

async function fixtureKey(op, request) {
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > 64 * 1024) return;
    chunks.push(chunk);
  }
  return key(op, JSON.parse(Buffer.concat(chunks).toString('utf8')));
}

export async function openSite(files, scenario) {
  const allowed = new Set([scenario.start, scenario.target]);
  const fixtures = new Map(scenario.responses.map(f => [key(f.op, f.request), f.reply]));
  if (fixtures.size !== scenario.responses.length) throw new Error('duplicate fixture request');
  const imageFlow = isImageFlow(scenario.flow);
  const media = new Map();
  if (imageFlow) for (const item of [scenario.image, ...(scenario.thumbnail ? [scenario.thumbnail] : [])]) {
    const bytes = imageBytes(item);
    if (media.has(item.path)) throw new Error('duplicate image fixture path');
    media.set(item.path, bytes);
  }
  const used = new Set(), missing = new Set();
  let origin, imageOpen = false;
  const server = createServer(async (request, response) => {
    const reply = (code, type, body) => {
      response.writeHead(code, { 'Content-Type': type, 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' });
      response.end(body);
    };
    try {
      const url = new URL(request.url, origin);
      if (url.origin !== origin) return reply(404, 'text/plain', '');
      if (imageFlow && url.pathname.startsWith('/api/results/')) {
        const bytes = request.method === 'GET' && media.get(request.url);
        if (!bytes) {
          missing.add('image fixture');
          return reply(404, 'text/plain', '');
        }
        if (scenario.thumbnail && !imageOpen && request.url === scenario.image.path) {
          missing.add('image original requested before opening');
          return reply(404, 'text/plain', '');
        }
        if (scenario.thumbnail && imageOpen && request.url === scenario.thumbnail.path && !used.has(request.url))
          return reply(404, 'text/plain', '');
        used.add(request.url);
        return reply(200, 'image/png', bytes);
      }
      if (request.method === 'POST' && /^\/api\/ops\/[a-z_]+$/.test(url.pathname) && !url.search) {
        const op = url.pathname.slice('/api/ops/'.length);
        let k;
        try { k = await fixtureKey(op, request); } catch { /* malformed input cannot match */ }
        if (!fixtures.has(k)) {
          missing.add(op);
          return reply(404, 'application/json', '{"error":"No protected fixture for request"}');
        }
        used.add(k);
        return reply(200, 'application/json', JSON.stringify(fixtures.get(k)));
      }
      if (request.method !== 'GET') return reply(405, 'text/plain', '');
      if (url.pathname === '/api/stream' && (!url.search || url.search === '?cursor=0')) {
        response.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store' });
        response.write('event: ready\ndata: {"cursor":0}\n\n');
        return;
      }
      const name = allowed.has(url.pathname + url.search) ? '/index.html' : url.pathname;
      const type = mime.get(name.split('.').at(-1));
      if (!type || !Object.hasOwn(files, name)) return reply(404, 'text/plain', '');
      used.add(name);
      reply(200, type, Buffer.from(files[name], 'base64'));
    } catch {
      if (!response.headersSent) reply(400, 'text/plain', '');
      else response.destroy();
    }
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  origin = 'http://127.0.0.1:' + server.address().port;
  return {
    origin,
    used,
    missing,
    beginImageOpen() {
      if (scenario.thumbnail && !used.has(scenario.thumbnail.path))
        missing.add('image thumbnail missing before opening');
      imageOpen = true;
    },
    unmet: () => [...scenario.responses.filter(f => f.required && !used.has(key(f.op, f.request))).map(f => f.op),
      ...[...media.keys()].filter(path => !used.has(path))],
    forbidden: () => scenario.forbiddenOps?.filter(op => [...used].some(k => k.startsWith(op + ':'))) ?? [],
    close() {
      return new Promise(resolve => {
        server.close(resolve);
        server.closeAllConnections();
      });
    },
  };
}
