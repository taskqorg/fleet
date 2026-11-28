// Validate a designer-requested handoff before generating any Fleet save calls.
import { captureMatrix } from '../../packages/ui/scripts/capture-scene.mjs';
import { validateReplay, validateProducer } from './designer-replay.mjs';
import { maxDesignerCases } from './designer-fixture.mjs';
const digest = /^[a-f0-9]{64}$/;
const commit = /^[a-f0-9]{40}$/;
const scope = /^[a-z0-9-]+\/[a-z0-9-]+\/[a-z0-9-]+:[a-z0-9-]+$/;
const matrix = captureMatrix.map(({ viewport, theme }) => [viewport, theme]);

export function handoffOption(handoff, { nonce, base, baseSha256, overlaySha256, session, execution }) {
  if (!digest.test(nonce ?? '') || !commit.test(base ?? '') || !digest.test(baseSha256 ?? '') || !digest.test(overlaySha256 ?? '') ||
      !/^[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}$/.test(session ?? '') || !/^[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}$/.test(execution ?? '') ||
      !handoff || Object.keys(handoff).sort().join(',') !== 'base,baseBundleSha256,cases,execution,nonce,overlaySha256,producer,session,source,sourceSha256,version' || handoff.version !== 1 || handoff.nonce !== nonce || handoff.session !== session || handoff.execution !== execution || handoff.base !== base ||
      !validateProducer(handoff.producer, base) || handoff.baseBundleSha256 !== baseSha256 || handoff.overlaySha256 !== overlaySha256 ||
      handoff.source !== 'source.tar' || !digest.test(handoff.sourceSha256 ?? '') ||
      !Array.isArray(handoff.cases) || handoff.cases.length < 1 || handoff.cases.length > maxDesignerCases)
    throw new Error('Capture handoff differs from this designer request');
  const seen = new Set();
  for (const [index, item] of handoff.cases.entries()) {
    if (Object.keys(item ?? {}).sort().join(',') !== 'frames,recipe,recipeFile,recipeSha256,scopeKey' || !scope.test(item.scopeKey ?? '') || seen.has(item.scopeKey) || !digest.test(item.recipe ?? '') ||
        item.recipeFile !== `case-${index + 1}-recipe.json` || !digest.test(item.recipeSha256 ?? '') ||
        !Array.isArray(item.frames) || item.frames.length !== matrix.length)
      throw new Error('Invalid capture handoff cases');
    seen.add(item.scopeKey);
    for (const [i, frame] of item.frames.entries()) {
      const [viewport, theme] = matrix[i];
      if (!['bytes,file,height,routes,sha256,theme,viewport,width', 'bytes,file,height,imageJourney,routes,sha256,theme,viewport,width'].includes(Object.keys(frame ?? {}).sort().join(',')) ||
          frame.viewport !== viewport || frame.theme !== theme ||
          frame.file !== `case-${index + 1}-${viewport}-${theme}.png` || !digest.test(frame.sha256 ?? ''))
        throw new Error('Invalid capture handoff frame');
    }
  }
  return { execution, source: handoff.source, sourceSha256: handoff.sourceSha256, producer: handoff.producer, cases: handoff.cases };
}

// Source archives are untrusted until every retained replay input is bound to the handoff.
export async function validateSource(source, option, request) {
  const { isDeepStrictEqual } = await import('node:util');
  const { hash } = await import('./designer-source.mjs');
  const { checkFixture } = await import('./designer-fixture.mjs');
  if (!Buffer.isBuffer(source) || source.length % 512 !== 0 || hash(source) !== option.sourceSha256)
    throw new Error('Staged source digest differs from handoff');
  const files = new Map();
  const expected = new Set(['base.bundle', 'overlay.json', 'overlay.patch', 'replay.json']);
  let offset = 0, terminated = false;
  const field = bytes => bytes.toString('utf8').replace(/\0.*$/s, '');
  const number = bytes => {
    const value = field(bytes).trim();
    if (!/^[0-7]+$/.test(value)) throw new Error('Invalid source archive header');
    return parseInt(value, 8);
  };
  while (offset + 512 <= source.length) {
    const header = source.subarray(offset, offset + 512);
    if (header.every(byte => byte === 0)) {
      if (offset + 1024 > source.length || !source.subarray(offset).every(byte => byte === 0))
        throw new Error('Invalid source archive ending');
      terminated = true;
      break;
    }
    const name = field(header.subarray(0, 100));
    const checksum = number(header.subarray(148, 156));
    let actual = 0;
    for (let i = 0; i < 512; i++) actual += i >= 148 && i < 156 ? 32 : header[i];
    const size = number(header.subarray(124, 136));
    if (actual !== checksum || !expected.has(name) || files.has(name) || field(header.subarray(345, 500)) ||
        ![0, 48].includes(header[156]) || !Number.isSafeInteger(size) || size < 0 || offset + 512 + size > source.length)
      throw new Error('Invalid source archive member');
    const data = source.subarray(offset + 512, offset + 512 + size);
    const next = offset + 512 + Math.ceil(size / 512) * 512;
    if (next > source.length || !source.subarray(offset + 512 + size, next).every(byte => byte === 0))
      throw new Error('Invalid source archive padding');
    files.set(name, data);
    offset = next;
  }
  if (!terminated || files.size !== expected.size || !files.get('base.bundle').length ||
      files.get('base.bundle').length > 60 * 1024 * 1024 || files.get('overlay.json').length > 3 * 1024 * 1024 || files.get('overlay.patch').length > 2 * 1024 * 1024 ||
      files.get('replay.json').length > 768 * 1024)
    throw new Error('Source archive must contain the complete replay');
  const replay = validateReplay(JSON.parse(files.get('replay.json').toString('utf8')));
  if (replay.version !== 1 || replay.kind !== 'fleet-normal-app' || replay.base !== request.base ||
      !isDeepStrictEqual(replay.producer, option.producer) || replay.baseBundleSha256 !== request.baseSha256 ||
      replay.overlaySha256 !== request.overlaySha256 || hash(files.get('overlay.json')) !== request.overlaySha256 || hash(files.get('base.bundle')) !== replay.baseBundleSha256 ||
      hash(files.get('overlay.patch')) !== replay.patchSha256 ||
      replay.fixtureSha256 !== hash(JSON.stringify(replay.fixture) + '\n') ||
      !isDeepStrictEqual(replay.cases, option.cases))
    throw new Error('Staged source replay differs from this designer handoff');
  if (typeof request.verifySource !== 'function')
    throw new Error('Trusted isolated source verification is unavailable');
  await request.verifySource({ baseBundle: files.get('base.bundle'), overlayBytes: files.get('overlay.json'),
    patch: files.get('overlay.patch'), replay });
  checkFixture(replay.fixture);
  if (!isDeepStrictEqual(replay.fixture.cases.map(item => item.scopeKey), option.cases.map(item => item.scopeKey)))
    throw new Error('Replay cases do not match the captured fixture');
  return replay;
}
