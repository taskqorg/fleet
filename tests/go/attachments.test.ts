// Outside tests of message attachments (docs/designs/fleet-conversations.md, Attachments): an operator upload into the
// artifact store, message_send with attachments, the start command that stages them, and the thread that shows them.
// HTTP only, against FLEET_TRIAL_URL; an in-process fake S3 plays the store. Run:
//   FLEET_STORE_ENDPOINT_HOSTS=127.0.0.1 FLEET_DEFAULT_ARTIFACT_LOCATIONS=trial-host FLEET_STORE_SECRETS=$(mktemp -d) \
//   FLEET_TRIAL_TESTS=tests/go/attachments.test.ts FLEET_TRIAL_SERVER=go/bin/fleet-coordinator node tests/trial/run.ts
// Each test names its requirement and the risk it guards.
import {after, test} from 'node:test';
import assert from 'node:assert/strict';
import {createHash, randomUUID} from 'node:crypto';
import {fakeS3} from '../s3-fake.ts';
import {hostCommand} from '../harness.ts';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
type Reply = {status: number, body: any};
const as = (person: string) => ({authorization: 'Bearer ' + SECRET, 'x-fleet-person': person});
const owner = as('owner@trial.test'), outsider = as('outsider@trial.test');
async function call(method: string, path: string, body?: unknown, headers: Record<string, string> = owner): Promise<Reply> {
  const raw = body instanceof Uint8Array;
  const r = await fetch(BASE + path, {method, headers: {...(body === undefined || raw ? {} : {'content-type': 'application/json'}), ...headers}, body: body === undefined ? undefined : raw ? body : JSON.stringify(body)});
  const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed};
}
const ok = (r: Reply) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };
const op = (name: string, b: object = {}, h = owner) => call('POST', '/api/ops/' + name, b, h);
const old = {platform: 'darwin', capabilities: [], stores: [], problems: []}, poll = {...old, capabilities: ['attachments']};

const {port: s3port, objects, uploads, close: closeS3} = await fakeS3();
after(closeS3);
const {secret} = ok(await op('hosts_register', {id: 'trial-host'}));
const h = (path: string, body: unknown) => call('POST', path, body, {authorization: 'Bearer ' + secret});
ok(await h('/api/hosts/stores', {store: {id: 'trial-store', bucket: 'fleet', endpoint: `http://127.0.0.1:${s3port}`, accessKeyId: 'k', secretAccessKey: 's'}}));
ok(await h('/api/hosts/poll', poll));

/** Upload bytes in parts of at most `part` bytes, as the app does; returns the last reply (the stored artifact). */
async function upload(bytes: Buffer, name: string, mediaType: string, at: {sessionId?: string, projectId?: string | null} = {}, headers = owner) {
  const begun = ok(await op('artifact_upload_begin', {name, size: bytes.length, mediaType, ...at}, headers));
  let last: Reply = {status: 0, body: null};
  for (let first = 0; first < bytes.length || first === 0; first += begun.partBytes) {
    const chunk = bytes.subarray(first, Math.min(bytes.length, first + begun.partBytes));
    last = await call('PUT', begun.url, new Uint8Array(chunk), {...headers, 'content-range': bytes.length ? `bytes ${first}-${first + chunk.length - 1}/${bytes.length}` : 'bytes */0'});
    if (!bytes.length) break;
  }
  return {begun, last};
}

test('An operator upload becomes a stored attachment artifact, part by part and resumable (requirement: docs/designs/fleet-conversations.md Attachments, server; risk: a pasted file never reaches the store, a retried part duplicates bytes, a part out of order corrupts the file, or a failed finish cannot be retried)', {timeout: 60_000}, async () => {
  const {sessionId} = ok(await call('POST', '/api/ops/session_create', {}));
  // Two parts: a full part on the grid and a short last one; the last bytes mark the end.
  const bytes = Buffer.alloc((8 << 20) + 4096, 7); bytes.write('heliotrope', bytes.length - 10);
  const begun = ok(await op('artifact_upload_begin', {name: '../notes/\nword.bin', size: bytes.length, mediaType: 'application/octet-stream', sessionId}));
  assert.equal(begun.partBytes, 8 << 20, 'parts are transfer-sized (over the S3 minimum part), not a file cap');
  const P = begun.partBytes;
  const put = (first: number, last: number, headers = owner) => call('PUT', begun.url, new Uint8Array(bytes.subarray(first, last + 1)), {...headers, 'content-range': `bytes ${first}-${last}/${bytes.length}`});
  const gap = await put(P, bytes.length - 1);
  assert.equal(gap.status, 409, 'a part after a gap is refused'); assert.equal(gap.body.received, 0);
  assert.equal((await put(0, 99)).status, 400, 'a part off the grid is refused');
  const cut = await call('PUT', begun.url, new Uint8Array(bytes.subarray(0, 1000)), {...owner, 'content-range': `bytes 0-${P - 1}/${bytes.length}`});
  assert.equal(cut.status, 409, 'a cut-off part is sent again, not a failure'); assert.equal(cut.body.received, 0);
  assert.deepEqual(ok(await put(0, P - 1)), {received: P});
  assert.deepEqual(ok(await put(0, P - 1)), {received: P}, 'a retried part is not appended twice');
  assert.equal((await put(P, bytes.length - 1, outsider)).status, 404, 'another person cannot write into the upload');
  const stored = ok(await put(P, bytes.length - 1));
  assert.equal(stored.state, 'stored'); assert.equal(stored.role, 'attachment'); assert.equal(stored.name, 'word.bin', 'the base name, without control characters');
  assert.equal(stored.size, bytes.length); assert.equal(stored.sha256, createHash('sha256').update(bytes).digest('hex'), 'the running hash over both parts');
  assert.ok([...objects.entries()].some(([k, o]) => k.endsWith('/' + stored.sha256) && o.equals(bytes)), 'the content key holds the whole file');
  assert.ok(![...objects.keys()].some(k => k.includes('.pending/upload-')), 'the staging object is removed');
  assert.equal(ok(await put(P, bytes.length - 1)).id, stored.id, 'a retried last part answers the same artifact');
  assert.equal(ok(await call('PUT', begun.url, new Uint8Array(0), {...owner, 'content-range': `bytes */${bytes.length}`})).id, stored.id, 'a bodyless probe answers the artifact');
  const small = Buffer.from('the secret word is heliotrope\n');
  const one = ok((await upload(small, 'word.txt', 'text/plain', {sessionId})).last);
  assert.equal((await call('GET', one.url)).body, small.toString(), 'readable through Fleet');
  assert.equal(ok((await upload(Buffer.alloc(0), 'empty.txt', 'text/plain', {sessionId})).last).size, 0, 'an empty file');
});

test('Concurrent parts of one upload: one multipart upload, each part once, one artifact (requirement: docs/designs/fleet-conversations.md Attachments, concurrent finishes record one artifact; risk: racing retries orphan store multipart uploads, record a part twice, or store two artifacts)', {timeout: 60_000}, async () => {
  const {sessionId} = ok(await call('POST', '/api/ops/session_create', {}));
  const bytes = Buffer.alloc((8 << 20) + 777, 3); bytes.write('race', 0);
  const open = uploads.size;
  const begun = ok(await op('artifact_upload_begin', {name: 'race.bin', size: bytes.length, sessionId}));
  const P = begun.partBytes;
  const put = (first: number, last: number) => call('PUT', begun.url, new Uint8Array(bytes.subarray(first, last + 1)), {...owner, 'content-range': `bytes ${first}-${last}/${bytes.length}`});
  const firsts = await Promise.all([0, 1, 2, 3].map(() => put(0, P - 1)));
  assert.ok(firsts.every(r => r.status === 200 || r.status === 409), JSON.stringify(firsts.map(r => [r.status, r.body])));
  assert.equal(uploads.size, open + 1, 'one multipart upload on the store, losers aborted');
  const lasts = await Promise.all([0, 1, 2].map(() => put(P, bytes.length - 1)));
  const ids = new Set(lasts.filter(r => r.status === 200 && r.body.id).map(r => r.body.id));
  assert.ok(lasts.every(r => r.status === 200 || r.status === 409), JSON.stringify(lasts.map(r => [r.status, r.body])));
  const stored = ok(await put(P, bytes.length - 1));
  assert.ok(ids.size <= 1 && (ids.size === 0 || ids.has(stored.id)), 'one artifact for the upload');
  assert.equal(stored.sha256, createHash('sha256').update(bytes).digest('hex'), 'each part counted once');
  assert.equal(uploads.size, open, 'no multipart upload left open');
  const rows = ok(await op('artifact_list', {q: 'race.bin'})).artifacts.filter((a: any) => a.name === 'race.bin');
  assert.equal(rows.length, 1, 'one stored artifact row');
});

test('Uploads follow the project grant (requirement: docs/designs/fleet-conversations.md Attachments, access is the session project grant; risk: a person uploads into, or attaches files of, a project outside their grant)', async () => {
  assert.equal((await op('artifact_upload_begin', {name: 'x.txt', size: 1, projectId: 'hello-world'}, outsider)).status, 404);
  assert.equal((await op('artifact_upload_begin', {name: 'x.txt', size: 1}, outsider)).status, 404, 'no project needs the architect role');
  const {sessionId} = ok(await call('POST', '/api/ops/session_create', {}));
  assert.equal((await op('artifact_upload_begin', {name: 'x.txt', size: 1, sessionId}, outsider)).status, 404, 'an unseen session is unknown');
  const other = ok((await upload(Buffer.from('other project'), 'o.txt', 'text/plain', {projectId: 'hello-world'})).last);
  const sent = await op('message_send', {sessionId, requestId: randomUUID(), text: 'see file', attachments: [other.id]});
  assert.equal(sent.status, 400, 'a file of another project is not attached'); assert.match(sent.body.message, /another project/);
  assert.equal((await op('message_send', {sessionId, requestId: randomUUID(), text: 'x', attachments: ['nope']})).status, 400);
  const mine = ok((await upload(Buffer.from('mine'), 'm.txt', 'text/plain', {sessionId})).last);
  const {sessionId: other2} = ok(await call('POST', '/api/ops/session_create', {}));
  const cross = await op('message_send', {sessionId: other2, requestId: randomUUID(), text: 'x', attachments: [mine.id]});
  assert.equal(cross.status, 400, "an upload of one conversation is not sent in another"); assert.match(cross.body.message, /another conversation/);
  // A repeated request answers what was sent, even when its file is gone meanwhile (a lost reply is retried).
  const goneBytes = Buffer.from('purged later ' + randomUUID());
  const gone = ok((await upload(goneBytes, 'g.txt', 'text/plain', {sessionId})).last);
  const rid = randomUUID(), first = ok(await op('message_send', {sessionId, requestId: rid, text: 'with g', attachments: [gone.id]}));
  ok(await op('artifact_purge', {contentHash: gone.sha256, reason: 'attachments test'}));
  assert.equal(ok(await op('message_send', {sessionId, requestId: rid, text: 'with g', attachments: [gone.id]})).id, first.id, 'the retry answers the message already sent');
  const refused = (await upload(goneBytes, 'g2.txt', 'text/plain', {sessionId})).last;
  assert.equal(refused.status, 409, 'purged content is not stored again by an upload'); assert.match(refused.body.message, /purged/);
  assert.equal((await op('message_send', {sessionId, requestId: randomUUID(), text: 'x', attachments: 'nope'})).status, 400);
});

test('message_send with attachments: the message and the start command carry them, the host may read them, the thread shows them (requirement: docs/designs/fleet-conversations.md Attachments, delivery and transcript; risk: the agent never receives the files, or the operator cannot see what was sent)', {timeout: 30_000}, async () => {
  const {sessionId} = ok(await call('POST', '/api/ops/session_create', {}));
  const text = ok((await upload(Buffer.from('word: heliotrope'), 'note.txt', 'text/plain', {sessionId})).last);
  const png = ok((await upload(Buffer.from('89504e470d0a1a0a', 'hex'), 'dot.png', 'image/png')).last);
  ok(await h('/api/hosts/poll', old)); // the host runs an executor from before attachments
  const requestId = randomUUID();
  const e = ok(await op('message_send', {sessionId, requestId, text: 'Quote the word.', attachments: [text.id, png.id, text.id]}));
  assert.deepEqual(ok(await op('message_send', {sessionId, requestId, text: 'Quote the word.', attachments: [text.id, png.id]})).id, e.id, 'the same request is the same write');
  assert.equal((await op('message_send', {sessionId, requestId, text: 'Quote the word.', attachments: [png.id]})).status, 409, 'other attachments under one request id conflict');
  const view = ok(await op('artifact_get', {artifactId: png.id}));
  assert.equal(view.state, 'stored');
  assert.ok(!ok(await h('/api/hosts/poll', old)).commands.some((x: any) => x.executionId === e.id), 'an executor without attachments support gets no turn with files');
  const c = await hostCommand(async q => ok(await h('/api/hosts/poll' + q, poll)), (x: any) => x.executionId === e.id && x.kind === 'start');
  assert.ok(c, 'start command issued');
  assert.deepEqual(c.payload.attachments.map((a: any) => [a.artifactId, a.path, a.mediaType, a.size]),
    [[text.id, `attachments/${e.id}/note.txt`, 'text/plain', 16], [png.id, `attachments/${e.id}/dot.png`, 'image/png', 8]]);
  assert.deepEqual(c.payload.inputs, c.payload.attachments.map((a: any) => ({artifactId: a.artifactId, path: a.path})), 'each attachment is staged as an input');
  for (const a of [text, png]) {
    const got = ok(await h('/api/hosts/artifacts/get', {executionId: e.id, artifactId: a.id}));
    assert.ok(got.url, 'the executing host may download the attachment');
  }
  const {entries} = ok(await op('thread_entries', {key: {session: sessionId}, limit: 50}));
  const message = entries.find((b: any) => b.kind === 'text' && b.role === 'operator');
  assert.deepEqual(message.attachments.map((a: any) => [a.id, a.name, a.mediaType]), [[text.id, 'note.txt', 'text/plain'], [png.id, 'dot.png', 'image/png']]);
  const {markdown} = ok(await op('thread_export', {key: {session: sessionId}}));
  assert.ok(markdown.includes('*Attachment:* `dot.png`'), 'the name is a literal code span');
  // A file alone is a message, and a message with files while a turn runs is queued (its files are staged before a turn).
  const queued = ok(await op('message_send', {sessionId, requestId: randomUUID(), attachments: [png.id], delivery: 'steer'}));
  assert.equal(queued.queued, true); assert.equal(queued.state, 'queued');
  assert.equal((await op('message_send', {sessionId, requestId: randomUUID(), text: ''})).status, 400, 'no text and no files is nothing to send');
});

test('An agent attaches its own stored output through the MCP message_send tool (requirement: docs/designs/fleet-conversations.md Attachments, agents send attachments through MCP; risk: only people can pass files to another session)', {timeout: 30_000}, async () => {
  const {sessionId} = ok(await call('POST', '/api/ops/session_create', {}));
  const e = ok(await op('message_send', {sessionId, requestId: randomUUID(), text: 'make a file'}));
  ok(await h('/api/hosts/poll', poll));
  const bytes = Buffer.from('agent output\n'), sha256 = createHash('sha256').update(bytes).digest('hex');
  const r = ok(await h('/api/hosts/artifacts/reserve', {executionId: e.id, name: 'out/report.txt', mediaType: 'text/plain', size: bytes.length, sha256}));
  const {parts} = ok(await h('/api/hosts/artifacts/parts', {artifactId: r.artifactId, partNumbers: [1]}));
  const put = await fetch(parts[0].url, {method: 'PUT', body: bytes});
  const c = ok(await h('/api/hosts/artifacts/commit', {artifactId: r.artifactId, parts: [{partNumber: 1, etag: put.headers.get('etag')}]}));
  const staged = Buffer.from(await (await fetch(c.verify.url)).arrayBuffer());
  ok(await h('/api/hosts/artifacts/verified', {artifactId: r.artifactId, storeId: c.verify.storeId, sha256: createHash('sha256').update(staged).digest('hex')}));
  const {sessionId: other} = ok(await call('POST', '/api/ops/session_create', {}));
  const sent = ok(await call('POST', '/api/tools', {jsonrpc: '2.0', id: 1, method: 'tools/call', params: {name: 'message_send', arguments: {sessionId: other, requestId: randomUUID(), text: 'here is the report', attachments: [r.artifactId]}}}));
  assert.ok(!sent.result.isError, JSON.stringify(sent));
  const got = ok(await op('session_get', {sessionId: other}));
  assert.deepEqual(got.messages.at(-1).attachments.map((a: any) => [a.artifactId, a.name]), [[r.artifactId, 'report.txt']], 'the name the agent and the transcript see is the cleaned base name');
  const start = await hostCommand(async q => ok(await h('/api/hosts/poll' + q, poll)), (x: any) => x.sessionId === other && x.kind === 'start');
  assert.equal(start.payload.attachments[0].path.split('/').pop(), 'report.txt', 'the staged file takes the base name of the output path');
});

test('session_start passes attachments to its first message (requirement: docs/designs/fleet-conversations.md Attachments, session_start; risk: a new conversation loses the files it was started with)', async () => {
  const file = ok((await upload(Buffer.from('start file'), 's.txt', 'text/plain', {projectId: 'desiign'})).last);
  const s = ok(await op('session_start', {projectId: 'desiign', profileId: 'pm', role: 'pm', requestId: randomUUID(), text: 'with a file', attachments: [file.id]}));
  const got = ok(await op('session_get', {sessionId: s.sessionId}));
  assert.equal((await op('session_start', {projectId: 'desiign', profileId: 'pm', role: 'pm', requestId: randomUUID(), attachments: file.id})).status, 400, 'malformed attachments are refused, not dropped');
  assert.deepEqual(got.messages.at(-1).attachments.map((a: any) => a.artifactId), [file.id]);
});
