import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createReadStream, closeSync, fstatSync, lstatSync, openSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { hash } from './designer-source.mjs';

const uuid = /^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/;
const sha256 = /^[a-f0-9]{64}$/;
export const maxCaptureWire = 100 * 1024 * 1024;

// Fleet supplies these identities from the durable job and its originating
// designer execution. A guest result still needs the owning host's job receipt.
export function captureRequest(raw) {
  const request = JSON.parse(raw);
  if (!request || Object.keys(request).sort().join(',') !==
      'base,baseSha256,execution,fixtureSha256,job,nonce,overlaySha256,project,session,task,version' ||
      request.version !== 1 || request.project !== 'fleet' ||
      !['task', 'session', 'execution', 'job'].every(key => typeof request[key] === 'string' && uuid.test(request[key])) ||
      request.job === request.execution || typeof request.base !== 'string' || !/^[a-f0-9]{40}$/.test(request.base) ||
      !['nonce', 'baseSha256', 'overlaySha256', 'fixtureSha256'].every(key => typeof request[key] === 'string' && sha256.test(request[key])))
    throw new Error('Invalid Fleet designer host job request');
  return request;
}

export function checkCaptureInputs(request, baseBundle, overlayBytes, fixtureBytes) {
  if (hash(baseBundle) !== request.baseSha256 || hash(overlayBytes) !== request.overlaySha256 ||
      hash(fixtureBytes) !== request.fixtureSha256)
    throw new Error('Capture job inputs differ from the Fleet request');
  const overlay = JSON.parse(overlayBytes);
  const fixture = JSON.parse(fixtureBytes);
  const canonical = (bytes, value) => bytes.equals(Buffer.from(JSON.stringify(value) + '\n'));
  if (overlay?.base !== request.base || !canonical(overlayBytes, overlay) || !canonical(fixtureBytes, fixture))
    throw new Error('Capture job expects the packed overlay and frozen fixture bytes');
  return { overlay, fixture };
}

// The paths come only from the checked producer handoff. Run in the isolated
// guest's private job directory, after source reconstruction has succeeded.
export async function writeCaptureResult(out, produced, request, requestBytes, option, source) {
  const file = join(out, 'capture.tar');
  const members = ['handoff.json', 'source.tar'];
  for (const item of option.cases) members.push(item.recipeFile, ...item.frames.map(frame => frame.file));
  let occupied = 1024; // two terminator blocks before tar record padding
  for (const member of members) {
    const info = lstatSync(join(produced, member));
    if (!info.isFile() || info.nlink !== 1 || info.size <= 0) throw new Error('Capture job output is not a regular original');
    occupied += 512 + Math.ceil(info.size / 512) * 512;
  }
  if (Math.ceil(occupied / 10240) * 10240 > maxCaptureWire) throw new Error('Capture job exceeded its archive limit');
  const fd = openSync(file, 'wx', 0o600);
  let size;
  try {
    execFileSync('/usr/bin/tar', ['--format=ustar', '--mtime=@0', '--owner=0', '--group=0',
      '-cf', '-', '-C', produced, '--', ...members], { env: {}, timeout: 10_000, stdio: ['ignore', fd, 'pipe'] });
    size = fstatSync(fd).size;
    if (size <= 0 || size > maxCaptureWire) throw new Error('Capture job exceeded its archive limit');
  } finally {
    closeSync(fd);
  }
  const digest = createHash('sha256');
  for await (const bytes of createReadStream(file)) digest.update(bytes);
  const receipt = {
    version: 1, request, requestSha256: hash(requestBytes), producer: option.producer,
    capture: { name: 'capture.tar', bytes: size, sha256: digest.digest('hex') },
    source: { sha256: option.sourceSha256, baseTree: source.baseTree, tree: source.tree,
      patchSha256: source.patchSha256, archiveSha256: source.archiveSha256 },
    runtime: source.runtime,
  };
  writeFileSync(join(out, 'receipt.json'), JSON.stringify(receipt) + '\n', { flag: 'wx', mode: 0o600 });
}
