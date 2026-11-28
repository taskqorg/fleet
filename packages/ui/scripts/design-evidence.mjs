// Byte binding for the Design-First verifier. Decisions and stored artifact metadata must come
// from the trusted Fleet adapter. A worker's packet is only a comparison document.
import { createHash } from 'node:crypto';
import { captureMatrix } from './capture-scene.mjs';

const digest = value => typeof value === 'string' && /^[0-9a-f]{64}$/.test(value);
const nonempty = value => typeof value === 'string' && !!value.trim();
const invalid = message => { throw new Error('Design evidence: ' + message); };

export function packetDigest({ project, repository, task, scope, liveWalk, options }) {
  if (!nonempty(project) || !nonempty(repository) || !nonempty(task) || !nonempty(liveWalk) ||
      !Array.isArray(scope) || !scope.length || scope.some(key => !nonempty(key)) || new Set(scope).size !== scope.length ||
      !Array.isArray(options) || options.length < 1 || options.length > 3) invalid('missing task scope, live walk or options');

  const seen = new Set();
  const normalized = options.map(({ key, label, cases }) => {
    if (!['A', 'B', 'C'].includes(key) || seen.has(key) || !nonempty(label) || !Array.isArray(cases) || cases.length !== scope.length)
      invalid('invalid option or incomplete option coverage');
    seen.add(key);
    const unique = new Set();
    const casesWithFrames = cases.map(({ scopeKey, recipe, frames }) => {
      if (!scope.includes(scopeKey) || unique.has(scopeKey) || !digest(recipe) ||
          !Array.isArray(frames) || frames.length !== captureMatrix.length) invalid('invalid case or capture matrix');
      unique.add(scopeKey);
      const originals = frames.map((frame, i) => {
        const { viewport, theme, width, height } = captureMatrix[i];
        if (frame.viewport !== viewport || frame.theme !== theme || frame.width !== width || frame.height !== height ||
            !digest(frame.sha256) || !nonempty(frame.artifactId) || !nonempty(frame.critique))
          invalid('wrong frame, missing original or missing critique');
        return { viewport, theme, width, height, artifactId: frame.artifactId, sha256: frame.sha256, critique: frame.critique };
      });
      if (new Set(originals.map(frame => frame.artifactId)).size !== originals.length)
        invalid('an original was reused in more than one cell');
      return { scopeKey, recipe, frames: originals };
    }).sort((a, b) => a.scopeKey < b.scopeKey ? -1 : Number(a.scopeKey > b.scopeKey));
    return { key, label, cases: casesWithFrames };
  });
  const data = { project, repository, task, scope: [...scope].sort(), liveWalk, options: normalized };
  return createHash('sha256').update('fleet:design-packet:v1\0').update(JSON.stringify(data)).digest('hex');
}

// Compare tree-derived recipes with the active selections returned by the owner decision lookup.
// Recompute recipes on the combined candidate; no PR-head success or commit ID establishes equality here.
export function verifyDesignEvidence({ project, repository, cases, unknown }, selections, lookupArtifact) {
  if (unknown.length) invalid(`visual impact is unverified for ${unknown.join(', ')}`);
  if (!cases.length) return { classification: 'nonvisual' };
  const covered = new Map();
  for (const { decision, packet } of selections) {
    if (decision.state !== 'selected' || decision.project !== project || packet.project !== project || decision.repository !== repository || packet.repository !== repository ||
        packet.task !== decision.task || !nonempty(decision.ownerActor) || !nonempty(decision.question) ||
        !nonempty(decision.executionId) || decision.packet !== packetDigest(packet)) invalid('unconfirmed or stale owner decision');

    const selected = packet.options.find(option => option.key === decision.option);
    if (!selected) invalid('selected option is absent');
    for (const option of packet.options) for (const capture of option.cases) for (const frame of capture.frames) {
      const row = lookupArtifact(frame.artifactId);
      if (!row || row.state !== 'stored' || row.mediaType !== 'image/png' || row.sha256 !== frame.sha256 ||
          row.executionId !== decision.executionId || row.task !== packet.task || row.project !== project || row.repository !== repository)
        invalid('missing, changed or foreign original');
    }
    for (const item of selected.cases) {
      if (covered.has(item.scopeKey)) invalid(`overlapping selections for ${item.scopeKey}`);
      covered.set(item.scopeKey, item.recipe);
    }
  }

  for (const item of cases) {
    if (!digest(item.recipe) || covered.get(item.scopeKey) !== item.recipe) invalid(`missing current selection for ${item.scopeKey}`);
  }
  return { classification: 'selected-preview-matches' };
}

// The protected issuer supplies both lookups from Fleet. The candidate supplies neither a decision nor an artifact row.
export async function verifyRetainedDesignEvidence(candidate, taskIds, readReview, readOriginal) {
  if (candidate.unknown.length || !candidate.cases.length) return verifyDesignEvidence(candidate, [], () => undefined);
  const originals = new Map();
  const selections = [];
  const decisions = [];
  for (const task of new Set(taskIds)) {
    const r = await readReview(task);
    if (!r || r.state !== 'selected' || r.taskId !== task || r.projectId !== candidate.project ||
        r.repository !== candidate.repository || !digest(r.digest) || !nonempty(r.owner) || !nonempty(r.selected))
      invalid('Fleet has no active owner selection for this task');
    if (!nonempty(r.questionId) || !nonempty(r.executionId) || !Array.isArray(r.options)) invalid('invalid retained Fleet review');
    const options = r.options.map(o => ({ ...o, cases: o.cases.map(c => ({ ...c, frames: c.frames.map((f, i) => {
      const expected = captureMatrix[i];
      if (!expected || f.theme !== expected.theme || f.viewport !== expected.viewport) invalid('unexpected retained matrix cell');
      return { ...f, width: expected.width, height: expected.height };
    }) })) }));
    const packet = { project: r.projectId, repository: r.repository, task, scope: options[0]?.cases.map(c => c.scopeKey),
      liveWalk: r.walkArtifactId, options };
    const decision = { state: r.state, project: r.projectId, repository: r.repository, task, question: r.questionId,
      executionId: r.executionId, ownerActor: r.owner, option: r.selected, packet: packetDigest(packet) };
    for (const o of options) for (const c of o.cases) for (const f of c.frames) {
      if (!originals.has(f.artifactId)) originals.set(f.artifactId, await readOriginal(f.artifactId));
    }
    selections.push({ decision, packet });
    decisions.push({ task, question: r.questionId, digest: r.digest, option: r.selected });
  }
  const result = verifyDesignEvidence(candidate, selections, id => originals.get(id));
  const selectedFrames = Object.fromEntries(selections.flatMap(({ decision, packet }) =>
    packet.options.find(option => option.key === decision.option).cases.map(item =>
      [item.scopeKey, item.frames.map(frame => frame.sha256)])));
  return { ...result, decisions, selectedFrames };
}
