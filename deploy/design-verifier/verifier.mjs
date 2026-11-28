import { verifyProtectedDesignCandidate } from '../../packages/ui/scripts/design-protected.mjs';
import { DesignBlocked, VerificationWait } from './provider.mjs';

// The protected recipe comparison reads Git blobs and owner-selected originals from Fleet.
// It supplies diagnostics only. Positive visual checks require isolated normal-route parity.
export async function evaluate(event, { provider, git, receipts, checks, fleet }) {
  let candidate;
  let evidence, conclusion;
  const memberReceipts = [];
  const visualEvidence = [];
  const inspect = async () => {
    if (!fleet) throw new DesignBlocked('Protected Fleet design evidence access is unavailable');
    for (const member of candidate.members ?? [candidate]) {
      const measured = await fleet.evidence(member);
      if (!measured) continue;
      const { recipes, ...selection } = measured;
      visualEvidence.push({ ...selection, treeInputs: git.measureInputs(event.head, recipes) });
    }
    throw new DesignBlocked(visualEvidence.length ? 'Independent normal-route render parity is unavailable' :
      'Fleet has no verified current selection and admission for the exact PR head');
  };
  try {
    git.ensure(event);
    candidate = await provider.readCandidate(event, git);
    evidence = await verifyProtectedDesignCandidate(event, {
      repositoryDir: git.dir,
      readCandidate: async () => candidate,
      readPRReceipt: async member => {
        const receipt = await receipts.readPR(member);
        if (!receipt) throw new VerificationWait('Waiting for a protected check on an original PR head');
        memberReceipts.push({ number: member.number, head: member.head, receipt: receipt.externalId });
        return receipt;
      },
      // A changed scene, manifest, spec or other unclassified path reaches protected measurement.
      readSceneInventory: () => [],
      measureImpact: inspect,
    });
    conclusion = 'success';
  } catch (err) {
    conclusion = err instanceof DesignBlocked ? 'failure' : null;
    const reason = err instanceof VerificationWait || err instanceof DesignBlocked ? err.message :
      'Protected verification could not confirm provider or local evidence';
    evidence = { ...event, classification: conclusion === 'failure' ? 'visual-or-unknown' : 'pending', reason };
  }
  if (event.kind === 'pull_request') evidence.source = { branch: event.branch, repositoryId: event.sourceRepositoryId };
  if (event.kind === 'merge_group' && candidate) evidence.queue = {
    ref: event.ref, commits: candidate.members.map(({ number, head, mergeCommit, branch, sourceRepositoryId }) =>
      ({ number, head, mergeCommit, branch, sourceRepositoryId })),
    memberReceipts,
  };
  if (visualEvidence.length) evidence.visualEvidence = visualEvidence;
  const id = await receipts.retain(evidence, conclusion);
  const summary = evidence.reason ?? evidence.classification;
  const check = await checks.publish(event.head, id, conclusion, summary, () => provider.assertCurrent(event, candidate));
  await receipts.recordReadback(id, check);
  return { ...check, kind: event.kind, number: event.number, ref: event.ref, classification: evidence.classification, reason: evidence.reason };
}
