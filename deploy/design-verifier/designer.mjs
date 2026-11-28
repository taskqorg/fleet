import { execFileSync } from 'node:child_process';
import { closeSync, constants, fstatSync, mkdirSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { parseArgs } from 'node:util';
import { fileURLToPath } from 'node:url';
import { appOriginalsFixture } from '../../scripts/app-originals-fixture.ts';
import { captureMatrix } from '../../packages/ui/scripts/capture-scene.mjs';
import { designerSource, packOverlay, hash, maxBaseBundle, maxOverlay, verifyRequestedSource } from './designer-source.mjs';
import { sourcePath } from './git.mjs';
import { readTrusted, designerRuntime } from './parity.mjs';
import { assertAllocation, assertScratch } from './designer-allocation.mjs';
import { checkFixture, designerRenderScenario } from './designer-fixture.mjs';
import { validateReplay } from './designer-replay.mjs';
import { handoffOption, validateSource } from './designer-handoff.mjs';
import { captureRequest, checkCaptureInputs, writeCaptureResult } from './designer-job.mjs';
import { dependencyWire } from './worker/dependencies.mjs';

const maxPNG = 4 * 1024 * 1024;
const maxManifest = 768 * 1024;
const maxFixture = 256 * 1024;
const digest = /^[a-f0-9]{64}$/;
const json = value => JSON.stringify(value) + '\n';

function read(path, limit) {
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const info = fstatSync(fd);
    if (!info.isFile() || info.size > limit) throw new Error('Invalid or oversized input: ' + path);
    const bytes = readFileSync(fd);
    if (bytes.length > limit) throw new Error('Oversized input: ' + path);
    return bytes;
  } finally {
    closeSync(fd);
  }
}


function original(png, width, height) {
  if (png.length < 33 || png.length > maxPNG || png.subarray(0, 8).toString('hex') !== '89504e470d0a1a0a' ||
      png.toString('ascii', 12, 16) !== 'IHDR' || png.readUInt32BE(16) !== width || png.readUInt32BE(20) !== height)
    throw new Error('Isolated renderer returned an invalid PNG or wrong dimensions');
  return png;
}

function appInputs(guestInputs, signed, fingerprints) {
  const paths = new Map(Object.entries(signed).map(([path, fingerprint]) => [path, fingerprint.split(':')[1]]));
  if (!Array.isArray(guestInputs) || !guestInputs.length) throw new Error('The normal build did not report its inputs');
  for (const { path, sha256 } of guestInputs) {
    if (!sourcePath(path) || !digest.test(sha256) || paths.has(path) && paths.get(path) !== sha256)
      throw new Error('The normal build reported an invalid input');
    if (!path.startsWith('node_modules/')) {
      if (fingerprints[path] !== sha256) throw new Error('The build used different bytes for ' + path);
    }
    paths.set(path, sha256);
  }
  const inputs = [...paths.keys()].sort().map(path => ({ path, sha256: paths.get(path) }));
  dependencyWire(inputs.filter(input => input.path.startsWith('node_modules/')));
  return inputs;
}

function makeRecipe(item, app, inputs, clock) {
  const [scene, state] = item.scopeKey.split(':');
  const css = Object.keys(app.files).filter(name => /^\/assets\/app-[a-z0-9]+\.css$/.test(name));
  if (css.length !== 1) throw new Error('The normal app CSS was not identified');
  const recipe = {
    version: 1, scene, state, path: item.scenario.target,
    landmark: item.journey && item.journey.flow !== 'image-returned' ? 'role=dialog[name="Image viewer"]' : item.scenario.landmark,
    clock, timeZone: 'UTC', locale: 'en-US', dpr: 1, reducedMotion: true, matrix: captureMatrix,
    css: hash(Buffer.from(app.files[css[0]], 'base64')), inputs,
  };
  return { ...recipe, digest: hash('fleet:preview-recipe:v1\0' + JSON.stringify(recipe)),
    html: hash(Buffer.from(app.files['/index.html'], 'base64')) };
}

function loadReplay(directory) {
  const manifest = JSON.parse(read(resolve(directory, 'replay.json'), maxManifest));
  validateReplay(manifest);
  checkFixture(manifest.fixture);
  if (manifest.cases.length !== manifest.fixture.cases.length || manifest.fixtureSha256 !== hash(json(manifest.fixture)))
    throw new Error('The frozen fixture differs from the replay manifest');
  const overlayBytes = read(resolve(directory, 'overlay.json'), 3 * 1024 * 1024);
  if (manifest.overlaySha256 !== hash(overlayBytes)) throw new Error('The retained original overlay differs');
  const originalOverlay = JSON.parse(overlayBytes);
  const patch = read(resolve(directory, 'overlay.patch'), maxOverlay);
  if (manifest.patchSha256 !== hash(patch)) throw new Error('The retained overlay digest differs');
  return { manifest, patch, originalOverlay, overlayBytes };
}

async function captureApp(renderer, baseBundle, overlay, { out, fixture, replay, scratchRoot, nonce, session, execution }) {
  const prior = replay ? loadReplay(replay) : undefined;
  const data = prior?.manifest.fixture ?? fixture;
  checkFixture(data);
  const input = prior ? prior.originalOverlay : overlay;
  const release = JSON.parse(readTrusted(fileURLToPath(new URL('../../release.json', import.meta.url)), 8192));
  if (!/^[a-f0-9]{40}$/.test(release.commit) || release.target !== 'linux-amd64' || input?.base !== release.commit)
    throw new Error('Base must match the signed renderer release commit');
  const source = designerSource(baseBundle, input, renderer.build.files, undefined, scratchRoot);
  if (prior && (source.tree !== prior.manifest.tree || !source.patch.equals(prior.patch)))
    throw new Error('Requested overlay differs from retained replay tree or patch');
  const { base, baseTree, tree, patch, archive, paths } = source;
  const same = (name, value) => {
    if (prior && JSON.stringify(prior.manifest[name]) !== JSON.stringify(value))
      throw new Error('Replay differs in ' + name);
  };
  const archiveSha256 = hash(archive);
  same('baseTree', baseTree);
  same('paths', paths);
  same('archiveSha256', archiveSha256);
  const baseBundleSha256 = hash(baseBundle);
  same('baseBundleSha256', baseBundleSha256);
  same('runtime', renderer.runtime);
  const producer = { commit: release.commit, version: release.version, target: release.target };
  same('producer', producer);
  const appBytes = await renderer.run('build', ['[]', '--designer'], archive, 24 * 1024 * 1024, 60_000);
  const app = JSON.parse(appBytes);
  const appSha256 = hash(appBytes);
  same('appSha256', appSha256);
  const inputs = appInputs(app.inputs, renderer.build.files, source.fingerprints);
  const cases = [];
  const files = new Map();
  for (const [index, item] of data.cases.entries()) {
    const { scopeKey } = item;
    const scenario = designerRenderScenario(item, data);
    const recipe = makeRecipe(item, app, inputs, data.clock);
    const { chromium, frames } = JSON.parse(await renderer.run('render', ['--designer'], JSON.stringify({
      files: app.files, main: app.main, scenario, matrix: captureMatrix, clock: data.clock,
    }), 24 * 1024 * 1024, 65_000));
    if (chromium !== renderer.runtime.chromium || !Array.isArray(frames) || frames.length !== captureMatrix.length)
      throw new Error('Expected a complete capture matrix from the pinned Chromium');
    const recipeFile = 'case-' + (index + 1) + '-recipe.json';
    const recipeBytes = json(recipe);
    files.set(recipeFile, recipeBytes);
    cases.push({ scopeKey, recipe: recipe.digest, recipeFile, recipeSha256: hash(recipeBytes), frames: captureMatrix.map((cell, i) => {
      if (frames[i]?.blocked) throw new Error(scopeKey + ' ' + cell.viewport + ' ' + cell.theme + ' ' + JSON.stringify(frames[i].blocked));
      const png = original(Buffer.from(frames[i].png, 'base64'), cell.width, cell.height);
      const file = 'case-' + (index + 1) + '-' + cell.viewport + '-' + cell.theme + '.png';
      files.set(file, png);
      return { ...cell, file, sha256: hash(png), bytes: png.length, routes: frames[i].observed,
        ...(item.journey && { imageJourney: frames[i].imageJourney }) };
    }) });
  }
  same('cases', cases);
  if (!out) return { replay: 'matched', cases };
  const overlayBytes = prior ? prior.overlayBytes : Buffer.from(json(overlay));
  const overlaySha256 = hash(overlayBytes);
  if (!digest.test(overlaySha256 ?? '')) throw new Error('Replay has no original overlay digest');
  const manifest = {
    version: 1, kind: 'fleet-normal-app', base, baseTree, tree, paths, overlaySha256,
    patchSha256: hash(patch), baseBundleSha256, archiveSha256, appSha256, producer, runtime: renderer.runtime,
    fixtureSha256: hash(json(data)), fixture: data, cases,
  };
  validateReplay(manifest);
  mkdirSync(dirname(out), { recursive: true, mode: 0o700 });
  mkdirSync(out, { mode: 0o700 });
  for (const [file, bytes] of files) writeFileSync(resolve(out, file), bytes, { mode: 0o600, flag: 'wx' });
  writeFileSync(resolve(out, 'base.bundle'), baseBundle, { mode: 0o600, flag: 'wx' });
  writeFileSync(resolve(out, 'overlay.json'), overlayBytes, { mode: 0o600, flag: 'wx' });
  writeFileSync(resolve(out, 'overlay.patch'), patch, { mode: 0o600, flag: 'wx' });
  writeFileSync(resolve(out, 'replay.json'), json(manifest), { mode: 0o600, flag: 'wx' });
  const tar = execFileSync('/usr/bin/tar', ['--format=ustar', '--mtime=@0', '--owner=0', '--group=0',
    '-cf', '-', '-C', out, 'base.bundle', 'overlay.json', 'overlay.patch', 'replay.json'],
  { maxBuffer: 64 * 1024 * 1024, timeout: 10_000, env: {} });
  writeFileSync(resolve(out, 'source.tar'), tar, { mode: 0o600, flag: 'wx' });
  if (!digest.test(nonce ?? '')) throw new Error('An execution-owned capture nonce is required');
  const result = { source: 'source.tar', sourceSha256: hash(tar), cases };
  const handoff = { version: 1, nonce, session, execution, producer, base, baseBundleSha256, overlaySha256,
    source: result.source, sourceSha256: result.sourceSha256, cases };
  writeFileSync(resolve(out, 'handoff.json'), json(handoff), { mode: 0o600, flag: 'wx' });
  return result;
}

async function compare(renderer, before, after) {
  const records = [before, after].map(dir => loadReplay(dir).manifest);
  if (records[0].fixtureSha256 !== records[1].fixtureSha256 ||
      JSON.stringify(records[0].runtime) !== JSON.stringify(records[1].runtime))
    throw new Error('Compare requires the same frozen fixture and runtime');
  const result = [];
  for (const [index, first] of records[0].cases.entries()) {
    const second = records[1].cases[index];
    if (first.scopeKey !== second?.scopeKey) throw new Error('Capture case differs');
    for (const [i, { viewport, theme, width, height }] of captureMatrix.entries()) {
      const name = 'case-' + (index + 1) + '-' + viewport + '-' + theme + '.png';
      const [selected, actual] = [before, after].map((dir, n) => {
        const png = original(read(resolve(dir, name), maxPNG), width, height);
        if (hash(png) !== records[n].cases[index].frames[i].sha256) throw new Error('Original digest differs: ' + name);
        return png.toString('base64');
      });
      const evidence = JSON.parse(await renderer.run('compare', [], JSON.stringify({ width, height, original: selected, actual }),
        1024, 10_000));
      result.push({ scopeKey: first.scopeKey, viewport, theme, ...evidence });
    }
  }
  return { matches: result.every(frame => frame.differingPixels === 0), frames: result };
}

async function captureJob(input, out) {
  if (process.platform !== 'linux' || process.arch !== 'x64')
    throw new Error('Designer capture jobs require Linux amd64');
  assertAllocation();
  const scratchRoot = assertScratch();
  const requestBytes = read(join(input, 'request.json'), 8192);
  const request = captureRequest(requestBytes);
  const base = read(join(input, 'base.bundle'), maxBaseBundle);
  const overlayBytes = read(join(input, 'overlay.json'), 3 * 1024 * 1024);
  const { overlay, fixture } = checkCaptureInputs(request, base, overlayBytes, read(join(input, 'fixture.json'), maxFixture));
  const renderer = designerRuntime();
  const scratch = mkdtempSync(join(scratchRoot, 'capture-job-'));
  let ownOutput = false;
  try {
    const produced = join(scratch, 'originals');
    await captureApp(renderer, base, overlay, { out: produced, fixture, scratchRoot,
      nonce: request.nonce, session: request.session, execution: request.execution });
    const handoff = JSON.parse(read(join(produced, 'handoff.json'), maxManifest));
    const option = handoffOption(handoff, request);
    const source = await validateSource(read(join(produced, 'source.tar'), 64 * 1024 * 1024), option, {
      ...request, verifySource: input => verifyRequestedSource(input, renderer.build.files, scratchRoot),
    });
    if (source.fixtureSha256 !== request.fixtureSha256) throw new Error('Captured fixture differs from the Fleet request');
    mkdirSync(out, { mode: 0o700 });
    ownOutput = true;
    await writeCaptureResult(out, produced, request, requestBytes, option, source);
  } catch (error) {
    if (ownOutput) rmSync(out, { recursive: true, force: true });
    throw error;
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}

try {
  const { positionals: [mode, ...extra], values } = parseArgs({ allowPositionals: true, options: {
    checkout: { type: 'string' }, base: { type: 'string' }, paths: { type: 'string' }, nonce: { type: 'string' }, session: { type: 'string' }, execution: { type: 'string' },
    'base-bundle': { type: 'string' }, source: { type: 'string' }, overlay: { type: 'string' }, fixture: { type: 'string' }, out: { type: 'string' },
    'job-input': { type: 'string' },
    replay: { type: 'string' }, before: { type: 'string' }, after: { type: 'string' }, help: { type: 'boolean' },
  } });
  if (!mode || values.help) {
    console.log('Fleet normal-app designer capture (off-production Linux amd64 only)\n' +
      '  fixture\n  pack --checkout <private-worktree> --base <commit> --paths <json-list> --out <new-file>\n' +
      '  capture --base-bundle <bundle> --overlay <json> --nonce <64-hex> --session <uuid> --execution <uuid> [--fixture <synthetic.json>] --out <new-directory>\n' +
      '  capture-job --job-input <private-input-directory> --out <new-output-directory>\n' +
      '  replay --base-bundle <bundle> --replay <source-directory> [--nonce <64-hex> --out <new-directory>]\n' +
      '  compare --before <capture-directory> --after <capture-directory>');
  } else if (mode === 'fixture' && !extra.length && !Object.keys(values).length) {
    checkFixture(appOriginalsFixture);
    console.log(json(appOriginalsFixture).trimEnd());
  } else if (mode === 'pack' && !extra.length && Object.keys(values).sort().join(',') === 'base,checkout,out,paths') {
    const paths = JSON.parse(read(resolve(values.paths), 64 * 1024));
    const overlay = packOverlay(resolve(values.checkout), values.base, paths);
    writeFileSync(resolve(values.out), json(overlay), { mode: 0o600, flag: 'wx' });
    console.log(json({ base: overlay.base, paths: overlay.files.map(file => file.path), sha256: hash(json(overlay)) }).trimEnd());
  } else if (mode === 'validate-replay' && !extra.length && !Object.keys(values).length) {
    const parts = [];
    let total = 0;
    for await (const part of process.stdin) {
      total += part.length;
      if (total > maxManifest) throw new Error('Oversized replay manifest');
      parts.push(part);
    }
    validateReplay(JSON.parse(Buffer.concat(parts)));
  } else if (mode === 'capture-job' && !extra.length && Object.keys(values).sort().join(',') === 'job-input,out') {
    await captureJob(resolve(values['job-input']), resolve(values.out));
  } else {
    const valid = { capture: values.fixture ? 'base-bundle,execution,fixture,nonce,out,overlay,session' : 'base-bundle,execution,nonce,out,overlay,session',
      replay: values.out ? 'base-bundle,execution,nonce,out,replay,session' : 'base-bundle,replay',
      compare: 'after,before' };
    if (extra.length || !Object.hasOwn(valid, mode) || Object.keys(values).sort().join(',') !== valid[mode])
      throw new Error('Invalid arguments. Use --help for supported invocations');
    if (values.out && (!/^[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}$/.test(values.session ?? '') || !/^[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}$/.test(values.execution ?? '')))
      throw new Error('Expected the requesting designer session and execution IDs');
    if (values.out && !digest.test(values.nonce ?? '')) throw new Error('An execution-owned capture nonce is required');
    if (process.platform !== 'linux' || process.arch !== 'x64')
      throw new Error('Normal app capture requires the off-production Linux amd64 renderer');
    assertAllocation();
    const scratchRoot = mode === 'compare' ? undefined : assertScratch();
    const renderer = designerRuntime();
    const output = mode === 'compare' ? await compare(renderer, values.before, values.after) :
      await captureApp(renderer, read(resolve(values['base-bundle']), maxBaseBundle),
        values.overlay ? JSON.parse(read(resolve(values.overlay), 3 * 1024 * 1024)) : undefined, {
        out: values.out && resolve(values.out),
        fixture: values.fixture ? JSON.parse(read(values.fixture, maxFixture)) : appOriginalsFixture,
        replay: values.replay, scratchRoot, nonce: values.nonce, session: values.session, execution: values.execution,
      });
    console.log(json(output).trimEnd());
    if (mode === 'compare' && !output.matches) process.exitCode = 2;
  }
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
