import { isDeepStrictEqual } from 'node:util';
import { hash } from './designer-source.mjs';
import { checkFixture } from './designer-fixture.mjs';
import { sourcePath } from './git.mjs';
import { captureMatrix } from '../../packages/ui/scripts/capture-scene.mjs';

const digest = /^[a-f0-9]{64}$/;
const oid = /^[a-f0-9]{40}$/;


export function validateProducer(p, base) {
  return p && Object.keys(p).sort().join(',') === 'commit,target,version' && p.commit === base &&
    p.target === 'linux-amd64' && typeof p.version === 'string' && p.version.length > 0 && p.version.length < 100;
}

export function validateCases(cases, fixture) {
  if (!Array.isArray(cases) || cases.length !== fixture.cases.length) return false;
  return cases.every((c, index) => c && Object.keys(c).sort().join(',') === 'frames,recipe,recipeFile,recipeSha256,scopeKey' && c.scopeKey === fixture.cases[index].scopeKey && digest.test(c.recipe ?? '') &&
    c.recipeFile === `case-${index + 1}-recipe.json` && digest.test(c.recipeSha256 ?? '') &&
    Array.isArray(c.frames) && c.frames.length === captureMatrix.length && c.frames.every((f, i) => {
      const cell = captureMatrix[i];
      const flow = fixture.cases[index].journey?.flow;
      const fields = flow ? 'bytes,file,height,imageJourney,routes,sha256,theme,viewport,width' : 'bytes,file,height,routes,sha256,theme,viewport,width';
      return f && Object.keys(f).sort().join(',') === fields && f.viewport === cell.viewport && f.theme === cell.theme && f.width === cell.width && f.height === cell.height &&
        f.file === `case-${index + 1}-${cell.viewport}-${cell.theme}.png` && digest.test(f.sha256 ?? '') &&
        Number.isSafeInteger(f.bytes) && f.bytes > 0 && f.bytes <= 4 * 1024 * 1024 &&
        Array.isArray(f.routes) && isDeepStrictEqual(f.routes, [fixture.cases[index].scenario.target]) &&
        (!flow || imageEvidence(flow, f.imageJourney));
    }));
}

function imageEvidence(flow, evidence) {
  if (!evidence) return false;
  if (flow === 'image-returned') return Object.keys(evidence).sort().join(',') === 'focus,sourceScroll' &&
    evidence.focus === 'source opener' && Array.isArray(evidence.sourceScroll) &&
    evidence.sourceScroll.every(pair => Array.isArray(pair) && pair.length === 2 && pair.every(Number.isFinite)) &&
    evidence.sourceScroll.some(([, top]) => top > 0);
  return Object.keys(evidence).join(',') === 'focus' && evidence.focus === (flow === 'image-open' ? 'image stage' : 'Zoom in');
}

// One replay contract for capture, retention and replay. Rebuilding still checks
// the exact Git tree, signed inputs, runtime and pixels, not merely this shape.
export function validateReplay(m) {
  if (!m || Object.keys(m).sort().join(',') !== 'appSha256,archiveSha256,base,baseBundleSha256,baseTree,cases,fixture,fixtureSha256,kind,overlaySha256,patchSha256,paths,producer,runtime,tree,version' || m.version !== 1 || m.kind !== 'fleet-normal-app' || !oid.test(m.base ?? '') ||
      !oid.test(m.baseTree ?? '') || !oid.test(m.tree ?? '') ||
      !['overlaySha256','patchSha256','baseBundleSha256','archiveSha256','appSha256','fixtureSha256'].every(k => digest.test(m[k] ?? '')) ||
      !validateProducer(m.producer, m.base) ||
      !m.runtime || Object.keys(m.runtime).sort().join(',') !== 'chromium,fontSha256,imageSha256,lockSha256,node,playwright' || !['imageSha256','fontSha256','lockSha256'].every(k => digest.test(m.runtime[k] ?? '')) ||
      !['node','chromium','playwright'].every(k => typeof m.runtime[k] === 'string' && m.runtime[k].length > 0 && m.runtime[k].length < 100) ||
      !Array.isArray(m.paths) || m.paths.length > 4096 || !m.paths.every(sourcePath) || new Set(m.paths).size !== m.paths.length ||
      !Array.isArray(m.cases)) throw new Error('Invalid complete normal app replay manifest');
  checkFixture(m.fixture);
  if (m.fixtureSha256 !== hash(JSON.stringify(m.fixture) + '\n') ||
      !validateCases(m.cases, m.fixture))
    throw new Error('Replay fixture and cases differ');
  return m;
}
