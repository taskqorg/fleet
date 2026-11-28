import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { lstatSync, readFileSync, realpathSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { captureMatrix } from '../../packages/ui/scripts/capture-scene.mjs';
import { designCheckDigest } from '../../packages/ui/scripts/design-github-checks.mjs';
import { sourcePath } from './git.mjs';
import { DesignBlocked, VerificationWait } from './provider.mjs';
import { imageScenarioOK, isImageFlow, resolveImages, validateImages } from './worker/image-journey.mjs';
import { vmBackend, validateVMConfig, validateVMProbe } from './vm-backend.mjs';

import { dependencyWire } from './worker/dependencies.mjs';

const releaseDir = fileURLToPath(new URL('./', import.meta.url));
const rootfs = '/opt/fleet-design-renderer/rootfs';
const runtimeDir = '/opt/fleet-design-runtime';
const legacyPolicyFile = '/etc/fleet-design-verifier/parity.json';
const imagePolicyFile = '/etc/fleet-design-verifier/parity-v2.json';
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');
const digest = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
const fingerprint = value => value === null || typeof value === 'string' && /^100(?:644|755):[a-f0-9]{64}$/.test(value);
const scopeKey = value => typeof value === 'string' && /^[a-z0-9/-]+:[a-z0-9-]+$/.test(value);
const renderedSource = /^(?:app\/src\/|packages\/(?:ui|view-url)\/|docs\/design-system\/)/;
const conversationRoute = /^\/(?:conversations|home)\/[^/?]+/;
const botRoute = /^\/library\/bots\/[^/?]+/;
const appRoute = path => typeof path === 'string' && /^\/(?:home|live|history|conversations|projects|lab|library|system)(?:[/?]|$)/.test(path) &&
  !/[\\#\x00-\x20]/.test(path) && path === new URL(path, 'https://fleet-parity.invalid').href.slice('https://fleet-parity.invalid'.length);

function trustedPath(path, file) {
  if (realpathSync(path) !== path) throw new Error('The operator path is indirect');
  for (let p = path; p !== '/'; p = dirname(p)) {
    const info = lstatSync(p);
    if (info.uid !== 0 || (info.mode & 0o022) !== 0 || (p === path && file ? !info.isFile() : !info.isDirectory()))
      throw new Error('The operator path is writable or is not a regular file or directory');
  }
  return path;
}

export function readTrusted(path, max) {
  const info = lstatSync(path);
  if (!info.isFile()) throw new Error('The operator input is not a regular file');
  trustedPath(path, true);
  if (info.size > max) throw new Error('The operator input exceeds its size limit');
  return readFileSync(path);
}

function scenarioOK(s) {
  if (!s || !digest(s.recipe) || !digest(s.sourceSha256) || !appRoute(s.start) || !appRoute(s.target) ||
      !Array.isArray(s.responses) || !s.responses.length ||
      s.responses.some(f => !/^[a-z_]{1,60}$/.test(f?.op ?? '') || !Object.hasOwn(f, 'request') || !Object.hasOwn(f, 'reply')) ||
      !s.responses.some(f => f.required === true) ||
      s.forbiddenOps !== undefined && (!Array.isArray(s.forbiddenOps) || s.forbiddenOps.some(op => !/^[a-z_]{1,60}$/.test(op)))) return false;
  if (isImageFlow(s.flow)) return imageScenarioOK(s);
  if (typeof s.landmark !== 'string' || !s.landmark) return false;
  switch (s.flow) {
    case 'bot':
    case 'back':
      return botRoute.test(s.start) && (conversationRoute.test(s.target) || botRoute.test(s.target)) &&
        (s.flow !== 'back' || typeof s.backLandmark === 'string' && !!s.backLandmark);
    case 'direct':
    case 'standalone':
      return conversationRoute.test(s.start) && conversationRoute.test(s.target);
    case 'open':
      return s.start === s.target;
    default:
      return false;
  }
}

function readPolicy(read) {
  try { return { raw: read(imagePolicyFile, 1024 * 1024), expectedVersion: 2 }; }
  catch (err) { if (err.code !== 'ENOENT') throw err; }
  return { raw: read(legacyPolicyFile, 1024 * 1024), expectedVersion: 1 };
}

export function loadPolicy(read = readTrusted) {
  const { raw, expectedVersion } = readPolicy(read);
  const { version, paths, cases, images = {} } = JSON.parse(raw);
  if (version !== expectedVersion || !Array.isArray(paths) || !cases || Array.isArray(cases) || typeof cases !== 'object' ||
      Object.entries(cases).some(([key, scenario]) => !scopeKey(key) || !scenarioOK(scenario) ||
        version === 1 && isImageFlow(scenario.flow)))
    throw new Error('Invalid protected app route policy');
  validateImages(images);
  for (const scenario of Object.values(cases)) if (isImageFlow(scenario.flow)) resolveImages(scenario, images);
  const mapping = new Map();
  for (const row of paths) {
    if (!sourcePath(row?.path) || !fingerprint(row.before) || !fingerprint(row.after) || row.before === row.after ||
        !Array.isArray(row.cases) || row.cases.some(key => !Object.hasOwn(cases, key)) ||
        typeof row.reason !== 'string' || !row.reason.trim())
      throw new Error('Invalid protected impact path');
    const key = JSON.stringify([row.path, row.before, row.after]);
    if (mapping.has(key)) throw new Error('Duplicate protected impact path');
    mapping.set(key, row);
  }
  return { hash: sha256(raw), mapping, cases, images };
}

function worker({ directory, scripts, compareFile }, job, args, bytes, max, timeout, signal, onUnconfirmed) {
  const params = [
    '--unshare-all', '--unshare-user', '--uid', '1000', '--gid', '1000', '--cap-drop', 'ALL',
    '--new-session', '--die-with-parent', '--clearenv',
    '--ro-bind', directory, '/', '--proc', '/proc', '--dev', '/dev',
    '--size', '268435456', '--tmpfs', '/work', '--chmod', '01777', '/work',
    '--size', '134217728', '--tmpfs', '/tmp', '--chmod', '01777', '/tmp',
    '--size', '4194304', '--tmpfs', '/run', '--chmod', '0755', '/run',
    '--setenv', 'HOME', '/tmp', '--setenv', 'TMPDIR', '/tmp', '--setenv', 'TZ', 'UTC',
    '--setenv', 'LANG', 'C.UTF-8', '--setenv', 'PATH', '/usr/local/bin:/usr/bin:/bin',
    '--chdir', '/work',
  ];
  if (job !== 'render') params.push('--disable-userns');
  if (job === 'compare') params.push('--ro-bind', compareFile, '/trusted-compare', '--', '/trusted-compare');
  else params.push('--ro-bind', scripts, '/trusted', '--', runtimeDir + '/node/bin/node', '/trusted/' + job + '.mjs', ...args);

  if (signal?.aborted) return Promise.reject(new VerificationWait('Isolated worker stopped'));
  const child = spawn('/usr/bin/bwrap', params, { env: {}, cwd: '/', stdio: ['pipe', 'pipe', 'ignore'] });
  return collectLocalWorker(child, { job, bytes, max, timeout, signal, onUnconfirmed });
}

export function collectLocalWorker(child, { job, bytes, max, timeout, signal, onUnconfirmed, cleanupTimeout = 3000 }) {
  return new Promise((accept, reject) => {
    let failure, cleanupTimer;
    const clear = () => {
      clearTimeout(timer);
      clearTimeout(cleanupTimer);
      signal?.removeEventListener('abort', abort);
    };
    const kill = reason => {
      if (failure) return;
      failure = reason;
      cleanupTimer = setTimeout(() => {
        clear();
        child.stdin.destroy();
        child.stdout.destroy();
        child.unref();
        reject(new VerificationWait('Isolated ' + job + ' cleanup unconfirmed'));
        onUnconfirmed?.();
      }, cleanupTimeout);
      try { child.kill('SIGKILL'); } catch { /* The cleanup deadline still applies. */ }
    };
    const abort = () => kill('stopped');
    const timer = setTimeout(() => kill('watchdog expired'), timeout);
    const chunks = [];
    let size = 0;
    child.stdin.on('error', () => {}); // Subprocess exit supplies the diagnostic.
    child.stdout.on('data', chunk => {
      size += chunk.length;
      if (size > max) kill('output exceeded its limit');
      else chunks.push(chunk);
    });
    child.on('error', () => kill('could not start'));
    child.on('close', (code) => {
      clear();
      if (!failure && code === 0) return accept(Buffer.concat(chunks));
      if (!failure && job === 'build' && code === 42) return reject(new DesignBlocked('A selected dependency fingerprint differs in the isolated toolchain'));
      if (!failure && job === 'build' && code === 43) return reject(new DesignBlocked('The isolated production build or its output was invalid'));
      reject(new VerificationWait('Isolated ' + job + ' ' + (failure ?? 'exited with status ' + code)));
    });
    signal?.addEventListener('abort', abort, { once: true });
    if (signal?.aborted) abort();
    child.stdin.end(bytes);
  });
}

function localRuntime(build, signal, onUnconfirmed) {
  const directory = trustedPath(rootfs, false);
  const scripts = trustedPath(resolve(releaseDir, 'worker'), false);
  const compareFile = trustedPath(resolve(releaseDir, 'compare-png'), true);
  const runtimeBytes = readTrusted(directory + runtimeDir + '/runtime.json', 8192);
  const runtime = JSON.parse(runtimeBytes);
  if (!digest(runtime.imageSha256) || !digest(runtime.fontSha256) ||
      runtime.lockSha256 !== build.files?.['pnpm-lock.yaml']?.split(':')[1] ||
      runtime.chromium !== build.chromium || runtime.playwright !== build.playwright || runtime.node !== build.node)
    throw new Error('The renderer runtime does not match the signed release');
  const where = { directory, scripts, compareFile };
  return { runtime, runtimeSha256: sha256(runtimeBytes),
    run: (job, args, bytes, max, timeout) => worker(where, job, args, bytes, max, timeout, signal, onUnconfirmed) };
}

// Designer jobs are already inside the independently authenticated guest.
// Their renderer always uses the local sandbox, without opening the issuer's VM socket.
export function designerRuntime() {
  const build = JSON.parse(readTrusted(resolve(releaseDir, 'build-inputs.json'), 8192));
  return { build, ...localRuntime(build) };
}

// Parse before opening the local runtime. The protocol exposes no command, path or binary selector.
export function decodeVMJob(operation, bytes) {
  if (!Buffer.isBuffer(bytes)) throw new VerificationWait('Invalid isolated worker input');
  const MiB = 1024 * 1024;
  if (operation === 1 && bytes.length === 0)
    return { job: 'render', args: ['--probe'], bytes: undefined, max: 1024, timeout: 25_000 };
  if (operation === 2 && bytes.length >= 4) {
    const n = bytes.readUInt32BE(0);
    if (n > 96 * 1024 || n > bytes.length - 4 || bytes.length - 4 - n > 96 * MiB)
      throw new VerificationWait('Invalid isolated build input');
    let dependencies;
    try { dependencies = new TextDecoder('utf-8', { fatal: true }).decode(bytes.subarray(4, 4 + n));
      if (!Array.isArray(JSON.parse(dependencies))) throw new Error('not an array');
    } catch { throw new VerificationWait('Invalid isolated build dependencies'); }
    return { job: 'build', args: [dependencies], bytes: bytes.subarray(4 + n), max: 24 * MiB, timeout: 60_000 };
  }
  if (operation === 3 && bytes.length <= 32 * MiB)
    return { job: 'render', args: [], bytes, max: 32 * MiB, timeout: 65_000 };
  if (operation === 4 && bytes.length <= 64 * MiB + 2048)
    return { job: 'compare', args: [], bytes, max: 1024, timeout: 10_000 };
  throw new VerificationWait('Unsupported or oversized isolated worker operation');
}

// Guest supervisor entry: always the existing local bwrap, regardless of VM config.
export async function isolatedVMJob(operation, bytes) {
  const input = decodeVMJob(operation, bytes);
  const build = JSON.parse(readTrusted(resolve(releaseDir, 'build-inputs.json'), 8192));
  const { runtime, runtimeSha256, run } = localRuntime(build);
  const result = await run(input.job, input.args, input.bytes, input.max, input.timeout);
  if (operation !== 1) return result;
  const probe = JSON.parse(result);
  if (probe.chromium !== runtime.chromium || probe.node !== runtime.node)
    throw new VerificationWait('Isolated Chromium or Node did not match the release pin');
  const release = JSON.parse(readTrusted(resolve(releaseDir, '../../release.json'), 8192));
  if (!/^[a-f0-9]{40}$/.test(release.commit)) throw new VerificationWait('Guest release identity unavailable');
  const workerSha256 = sha256(readTrusted(fileURLToPath(import.meta.url), 2 * 1024 * 1024));
  return Buffer.from(JSON.stringify({ node: probe.node, chromium: probe.chromium, runtime,
    guest: { releaseCommit: release.commit, workerSha256, runtimeSha256 } }));
}

export function vmBackendConfigured() {
  try { lstatSync('/etc/fleet-design-verifier/vm-backend.json'); return true; }
  catch (error) { if (error.code === 'ENOENT') return false; return true; }
}

export function parityRunner({ signal, onUnavailable = () => {}, onUnconfirmed } = {}) {
  const build = JSON.parse(readTrusted(resolve(releaseDir, 'build-inputs.json'), 8192));
  const configPath = '/etc/fleet-design-verifier/vm-backend.json';
  let configured;
  try { lstatSync(configPath); configured = true; }
  catch (error) { if (error.code !== 'ENOENT') throw error; }
  let runtime, run, vm = false, expectedRuntime, manifest;
  if (configured) {
    const release = JSON.parse(readTrusted(resolve(releaseDir, '../../release.json'), 8192));
    const config = validateVMConfig(JSON.parse(readTrusted(configPath, 8192)), build, release);
    expectedRuntime = config.runtime;
    manifest = config.manifest;
    runtime = { ...expectedRuntime, vm: { launchSha256: config.launchSha256, issuerReleaseCommit: config.releaseCommit,
      guestReleaseCommit: manifest.releaseCommit, guestWorkerSha256: manifest.guestWorkerSha256, guestRuntimeSha256: manifest.runtimeSha256 } };
    const backend = vmBackend(config, { signal });
    run = async (job, args, bytes) => {
      try { return await backend.run(job, args, bytes); }
      catch (error) { if (error instanceof VerificationWait) onUnavailable(error); throw error; }
    };
    vm = true;
  } else {
    ({ runtime, run } = localRuntime(build, signal, onUnconfirmed));
  }
  return {
    async probe() {
      const result = await run('render', ['--probe'], undefined, 1024, 25_000);
      if (vm) { validateVMProbe(result, expectedRuntime, manifest); return runtime; }
      const probe = JSON.parse(result);
      if (probe.chromium !== runtime.chromium || probe.node !== runtime.node)
        throw new VerificationWait('Isolated Chromium or Node did not match the release pin');
      return runtime;
    },
    policyIsCurrent(hash) {
      return sha256(readPolicy(readTrusted).raw) === hash;
    },
    async impact(event, paths, git, evidence) {
      let policy;
      try { policy = loadPolicy(); }
      catch (err) {
        if (err.code === 'ENOENT') throw new DesignBlocked('Protected normal-route impact policy is absent');
        throw new VerificationWait('The protected route policy could not be loaded');
      }
      evidence.policy = policy.hash;
      const base = await git.diffBase(event);
      const pathsToCases = Object.create(null);
      const unmapped = [];
      const measured = evidence.impact = [];
      for (const path of paths) {
        const before = await git.fingerprint(base, path), after = await git.fingerprint(event.head, path);
        const row = policy.mapping.get(JSON.stringify([path, before, after]));
        if (!row) unmapped.push(path);
        measured.push({ path, before, after, ...(row && { cases: row.cases, reason: row.reason }) });
        if (row) {
          if (row.cases.length && !renderedSource.test(path))
            throw new DesignBlocked('The normal app fixture does not execute this visual input: ' + path);
          pathsToCases[path] = row.cases;
        }
      }
      if (unmapped.length) throw new DesignBlocked('Protected impact has no matching content mapping for ' + unmapped[0]);
      return { ...event, paths: pathsToCases, policy };
    },
    async measure(event, git, policy, selected, evidence, readOriginal) {
      for (const [path, expected] of Object.entries(build.files)) {
        if (await git.fingerprint(event.head, path) !== expected)
          throw new DesignBlocked('The candidate changed a signed build input: ' + path);
      }
      const scenarios = [...selected].map(([key, { original, sourceSha256, recipe }]) => {
        const s = policy.cases[key];
        if (!s || s.recipe !== original.recipe || s.sourceSha256 !== sourceSha256 ||
            recipe.timeZone !== 'UTC' || recipe.locale !== 'en-US' || !Number.isSafeInteger(recipe.clock))
          throw new DesignBlocked('No protected normal-route fixture matches the selected recipe for ' + key);
        return isImageFlow(s.flow) ? { ...s, ...resolveImages(s, policy.images) } : s;
      });
      const flows = new Set(scenarios.map(s => s.flow));
      if (['bot', 'back'].some(flow => flows.has(flow)) &&
          ['bot', 'standalone', 'back'].some(flow => !flows.has(flow)))
        throw new DesignBlocked('The bot journey requires click, standalone direct entry and Back cases');
      const tree = await git.measureInputs(event.head, [...selected.values()].map(s => s.recipe));
      evidence.treeInputs = tree;
      if (tree.some(item => item.changed.length)) throw new DesignBlocked('The candidate differs from selected recipe inputs');
      let dependencies;
      try { dependencies = dependencyWire(tree.flatMap(item => item.dependencies)); }
      catch (error) { throw new DesignBlocked(error.message); }
      const archive = await git.archive(event.head, build.files);
      evidence.archiveSha256 = sha256(archive);
      const artifacts = await run('build', [dependencies], archive, 24 * 1024 * 1024, 60_000);
      const app = JSON.parse(artifacts);
      evidence.appSha256 = sha256(artifacts);
      evidence.runtime = runtime;
      evidence.rendered = [];
      const parity = [];
      for (const [index, [key, { admission, member, original, recipe }]] of [...selected].entries()) {
        const scenario = scenarios[index];
        const rendered = JSON.parse(await run('render', [], JSON.stringify({
          files: app.files, main: app.main, scenario, matrix: captureMatrix, clock: recipe.clock,
        }), 32 * 1024 * 1024, 65_000));
        if (rendered.chromium !== runtime.chromium || !Array.isArray(rendered.frames) || rendered.frames.length !== captureMatrix.length)
          throw new VerificationWait('Isolated renderer returned no complete capture matrix');
        const measured = { scopeKey: key, recipe: original.recipe, recipeSha256: recipe.sha256, flow: scenario.flow,
          selectedSourceSha256: admission.found.sourceSha256, decision: admission.review.digest, frames: [] };
        if (isImageFlow(scenario.flow)) {
          const { surface, entry, image, thumbnail } = scenario;
          measured.imageJourney = { surface, entry, imageSha256: image.sha256, ...(thumbnail && { thumbnailSha256: thumbnail.sha256 }) };
        }
        evidence.rendered.push(measured);
        for (const [i, { viewport, theme, width, height }] of captureMatrix.entries()) {
          const frame = rendered.frames[i];
          const selectedFrame = original.frames[i];
          const cell = { viewport, theme, width, height, originalSha256: selectedFrame.sha256, routes: frame.observed };
          measured.frames.push(cell);
          if (frame.blocked) {
            cell.routeFailure = frame.blocked;
            continue;
          }
          if (isImageFlow(scenario.flow)) {
            if (!frame.imageJourney) throw new VerificationWait('Isolated renderer omitted the image journey observation');
            cell.imageJourney = frame.imageJourney;
          }
          const actual = Buffer.from(frame.png, 'base64');
          cell.actualSha256 = sha256(actual);
          const bytes = await readOriginal(member, admission, selectedFrame);
          const { differingPixels, bounds } = JSON.parse(await run('compare', [], JSON.stringify({ width, height,
            original: bytes.toString('base64'), actual: frame.png }), 1024, 10_000));
          if (!Number.isSafeInteger(differingPixels) || differingPixels < 0 || differingPixels > width * height)
            throw new VerificationWait('Isolated image comparison did not return a valid pixel count');
          cell.differingPixels = differingPixels;
          if (bounds) cell.bounds = bounds;
        }
        parity.push({ scopeKey: key, recipe: original.recipe, receipt: designCheckDigest({
          candidate: event, policy: policy.hash, archive: evidence.archiveSha256, app: evidence.appSha256, runtime, measured,
        }), appMatches: measured.frames.every(frame => frame.differingPixels === 0),
        originalSha256: measured.frames.map(frame => frame.originalSha256) });
      }
      if (evidence.rendered.some(result => result.frames.some(frame => frame.routeFailure)))
        throw new DesignBlocked('A normal app route or required fixture request did not match the protected journey');
      if (evidence.rendered.some(result => result.frames.some(frame => frame.differingPixels !== 0)))
        throw new DesignBlocked('Normal app PNGs differ from the selected originals');
      return { ...event, cases: parity };
    },
  };
}
