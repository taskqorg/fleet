import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { test } from 'node:test';
import { imageJourney, imageScenarioOK, resolveImages, validateImages } from '../../deploy/design-verifier/worker/image-journey.mjs';
import { openSite } from '../../deploy/design-verifier/worker/site.mjs';

// Invented one-pixel PNG for the transport check, never an owner reference.
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aZ9sAAAAASUVORK5CYII=', 'base64');
const original = { path: '/api/results/invented-original', png: png.toString('base64'), sha256: createHash('sha256').update(png).digest('hex') };
const thumbnail = { ...original, path: '/api/results/invented-thumbnail' };
const images = { original, thumbnail };
const workerImages = { image: original, thumbnail };
const fixtures = {
  conversation: { path: '/conversations/C-TST-1', op: 'session_view', request: { sessionId: 'invented-session' } },
  task: { path: '/projects/TST/tasks/T-TST-1', op: 'task_get', request: { taskId: 'invented-task' } },
};
const resultCode = 'R-TST-1';
const resultTitle = 'Synthetic example';
function scenario(surface, flow, entry = 'thumbnail') {
  const { path, op, request } = fixtures[surface];
  const responses = [
    { op, request, reply: { questions: [] }, required: true },
    { op: 'entity_refs', request: { refs: [{ code: resultCode }] }, reply: { items: [] }, required: true },
  ];
  if (entry === 'entity') responses.push({ op: 'entity_card', request: { code: resultCode }, reply: {}, required: true });
  return { flow, entry, surface, resultCode, resultTitle, start: path, target: path, responses,
    image: 'original', thumbnail: 'thumbnail' };
}

test('protected image cases accept only fixed normal routes and integrity-bound results', () => {
  validateImages({});
  validateImages(images);
  assert.deepEqual(resolveImages(scenario('task', 'image-open'), images), workerImages);
  assert.throws(() => resolveImages(scenario('task', 'image-open'), { original }), /Missing protected image/);
  for (const changed of [
    { path: '/api/results/invented-original?x' }, { path: '/assets/invented.png' }, { sha256: '0'.repeat(64) }, { png: png.toString('base64') + '\n' },
  ]) assert.throws(() => validateImages({ original: { ...original, ...changed } }));
  assert.throws(() => validateImages({ original, duplicate: original }), /Duplicate protected image path/);

  for (const surface of Object.keys(fixtures)) for (const flow of ['image-open', 'image-fit', 'image-zoom', 'image-returned']) {
    for (const entry of ['thumbnail', 'entity']) assert.equal(imageScenarioOK(scenario(surface, flow, entry)), true);
  }
  const s = scenario('task', 'image-open');
  for (const changed of [
    { start: '/projects/TST/tasks/T-TST-1/extra', target: '/projects/TST/tasks/T-TST-1/extra' },
    { start: '/conversations/C-TST-1', target: '/conversations/C-TST-1' },
    { target: '/projects/TST/tasks/T-TST-2' }, { surface: 'constructor' }, { image: '__proto__' },
    { entry: 'entity' }, { flow: 'image-return' }, { script: 'arbitrary.js' }, { landmark: 'body' },
    { resultCode: 'R-TST-1.*' }, { thumbnail: 'original' }, { responses: s.responses.slice(1) },
  ]) assert.equal(imageScenarioOK({ ...s, ...changed }), false, JSON.stringify(changed));
});

test('the isolated normal origin requires exact synthetic operations and both declared PNG reads', async t => {
  const s = { ...scenario('task', 'image-open'), ...workerImages };
  const html = '<div id="app"></div>';
  const files = { '/index.html': Buffer.from(html).toString('base64') };
  await assert.rejects(openSite(files, { ...s, image: { ...original, sha256: '0'.repeat(64) } }), /integrity mismatch/);
  const site = await openSite(files, s);
  t.after(() => site.close());
  const get = (path, options) => fetch(site.origin + path, options);
  assert.equal(await (await get(s.start)).text(), html);
  assert.equal((await get('/projects/TST/tasks/T-TST-2')).status, 404);
  assert.deepEqual(site.unmet(), ['task_get', 'entity_refs', original.path, thumbnail.path]);
  const post = body => ({ method: 'POST', body: JSON.stringify(body) });
  assert.equal((await get('/api/ops/task_get', post({ taskId: 'another-task' }))).status, 404);
  for (const f of s.responses)
    assert.deepEqual(await (await get('/api/ops/' + f.op, post(f.request))).json(), f.reply);
  for (const asset of [thumbnail, original]) {
    if (asset === original) site.beginImageOpen();
    const response = await get(asset.path);
    assert.equal(response.headers.get('content-type'), 'image/png');
    assert.deepEqual(Buffer.from(await response.arrayBuffer()), png);
  }
  assert.deepEqual(site.unmet(), []);
  for (const path of [original.path + '?', original.path + '?size=1', original.path + '/preview', '/api/results/unmapped'])
    assert.equal((await get(path)).status, 404);
  assert.equal((await get(original.path, post({}))).status, 404);
  assert.deepEqual([...site.missing], ['task_get', 'image fixture']);
});

test('media phases reject eager originals and late first thumbnails while allowing a single asset', async () => {
  const s = { ...scenario('task', 'image-open'), responses: [], ...workerImages };
  for (const [fixture, early, expected, missing] of [
    [s, original, 404, 'image original requested before opening'],
    [s, null, null, 'image thumbnail missing before opening'],
    [{ ...s, thumbnail: undefined }, original, 200, null],
  ]) {
    const site = await openSite({}, fixture);
    try {
      if (early) assert.equal((await fetch(site.origin + early.path)).status, expected);
      if (early === original && fixture.thumbnail)
        assert.equal((await fetch(site.origin + thumbnail.path)).status, 200);
      site.beginImageOpen();
      assert.equal((await fetch(site.origin + original.path)).status, 200);
      const late = fixture.thumbnail && !early;
      if (fixture.thumbnail) assert.equal((await fetch(site.origin + thumbnail.path)).status, late ? 404 : 200);
      assert.deepEqual(site.unmet(), late ? [thumbnail.path] : []);
      assert.deepEqual([...site.missing], missing ? [missing] : []);
    } finally { await site.close(); }
  }
});

function journeyPage(flow) {
  const frame = { width: 390, height: 844 };
  const origin = 'http://synthetic.invalid';
  const spec = { ...scenario('task', flow), image: original, thumbnail: undefined };
  const document = { activeElement: null };
  const parent = { scrollLeft: 0, scrollTop: 96 };
  const stageEdges = { left: 0, top: 0, right: frame.width, bottom: frame.height };
  const imageEdges = { left: -480, top: 0, right: 870, bottom: frame.height };
  const trigger = { isConnected: true, ownerDocument: document, parentElement: parent };
  const stage = { isConnected: true, ownerDocument: document, getBoundingClientRect: () => stageEdges };
  const picture = { complete: true, naturalWidth: 1600, naturalHeight: 1000, currentSrc: origin + original.path,
    offsetWidth: 1350, offsetHeight: 844, parentElement: stage, decode: async () => {}, getBoundingClientRect: () => imageEdges };
  const calls = [];
  let opened = false;
  const handles = new Map([
    ['button:Open ' + resultTitle, trigger], ['region:Image. Drag to pan; plus and minus to zoom.', stage],
    ['img:' + resultTitle, picture],
  ]);
  const locator = (key, el = handles.get(key)) => ({
    getByRole: (role, { name, exact, selected }) => {
      assert.equal(exact, true);
      return locator(role + ':' + name + (selected ? ':selected' : ''));
    },
    locator: selector => locator(selector),
    waitFor: async ({ state }) => {
      calls.push(key + ':' + state);
      if (state === 'hidden') assert.equal(opened, false);
    },
    isVisible: async () => key === 'dialog:Image viewer' ? opened : true,
    scrollIntoViewIfNeeded: async () => {},
    click: async ({ trial } = {}) => {
      if (trial) return;
      calls.push(key);
      if (el === trigger) { opened = true; document.activeElement = stage; }
      if (key === 'button:Back to question') { opened = false; document.activeElement = trigger; }
    },
    evaluate: async callback => callback(el),
    elementHandle: async () => ({ evaluate: async callback => callback(el), value: el, dispose: async () => {} }),
  });
  const page = { ...locator('page'), evaluate: async () => true, url: () => origin + spec.start,
    waitForFunction: async (callback, handle) => assert.equal(callback(handle.value), true) };
  const start = () => imageJourney(page, spec, frame, origin, async () => {}, () => {}, () => calls.push('begin opening'));
  return { start, calls, parent, document, stage, stageEdges, imageEdges };
}

test('an open capture rejects a displaced stage or image even at the same dimensions', async () => {
  const { start, stageEdges, imageEdges } = journeyPage('image-open');
  const journey = await start();
  await journey.confirm();
  stageEdges.left += 1;
  stageEdges.right += 1;
  await assert.rejects(journey.confirm(), /viewer does not fill viewport/);
  stageEdges.left -= 1;
  stageEdges.right -= 1;
  imageEdges.top += 1;
  imageEdges.bottom += 1;
  await assert.rejects(journey.confirm(), /compact viewer state changed/);
});

test('returned capture blocks scroll or focus drift without repairing either', async () => {
  const { start, calls, parent, document, stage } = journeyPage('image-returned');
  const journey = await start();
  try {
    assert.equal(calls[0], 'tab:Details');
    assert.equal(calls[calls.indexOf('begin opening') + 1], 'button:Open ' + resultTitle);
    assert.ok(calls.includes('tab:Details:selected:visible'));
    assert.deepEqual(journey.evidence, { sourceScroll: [[0, 96]], focus: 'source opener' });
    parent.scrollTop = 95;
    await assert.rejects(journey.confirm(), /source scroll changed/);
    assert.equal(parent.scrollTop, 95);
    parent.scrollTop = 96;
    document.activeElement = stage;
    await assert.rejects(journey.confirm(), /source opener lost focus/);
    assert.equal(document.activeElement, stage);
  } finally { await journey.dispose(); }
});
