import { ProtectedDesignRejection, verifyProtectedDesignCandidate } from '../../packages/ui/scripts/design-protected.mjs';
import { DesignAbsent, DesignBlocked, QueueModeUnconfirmed, VerificationWait } from './provider.mjs';

const decision = ({ found, review, selected }) => ({
  number: found.number, head: found.head, branch: found.branch, writer: found.writer,
  task: review.taskId, question: review.questionId, execution: review.executionId,
  owner: review.owner, digest: review.digest, option: selected.key, sourceSha256: found.sourceSha256,
});

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const publicReason = reason => reason.replace(/[\x00-\x1f\x7f]/g, ' ').slice(0, 400);

export async function evaluate(event, { provider, git, receipts, checks, fleet, runner, rendererPending = false }) {
  let candidate, policy, evidence, conclusion, evidenceState;
  const memberReceipts = [];
  const visual = {};
  const selections = new Map();

  async function readSelection(member) {
    if (!fleet) throw new VerificationWait('Protected Fleet design evidence access is unavailable');
    const selection = await fleet.evidence(member);
    if (!selection) throw new DesignAbsent('Fleet has no current selection and admission for the exact PR head');
    return selection;
  }

  async function select() {
    const members = candidate.members?.filter(member => memberReceipts.some(
      r => r.number === member.number && r.classification === 'selected-preview-matches')) ?? [candidate];
    const all = new Map();
    for (const member of members) {
      const admission = await readSelection(member);
      if (candidate.members) {
        const receipt = memberReceipts.find(r => r.number === member.number);
        if (receipt.decisions.length !== 1 || !same(receipt.decisions[0], decision(admission)))
          throw new DesignBlocked('An original PR receipt identifies an older design decision or writer');
      }
      selections.set(member, admission);
      for (const original of admission.selected.cases) {
        if (all.has(original.scopeKey)) throw new DesignBlocked('Selected queue cases have overlapping scope');
        const recipe = admission.found.recipes.find(r => r.scopeKey === original.scopeKey);
        all.set(original.scopeKey, { member, admission, original, recipe, sourceSha256: admission.found.sourceSha256 });
      }
    }
    return all;
  }

  function failed(err) {
    const blocked = err instanceof DesignBlocked || err instanceof ProtectedDesignRejection;
    conclusion = blocked ? 'failure' : null;
    evidenceState = err instanceof DesignAbsent ? 'absent' : blocked ? 'blocked' : 'unknown';
    const reason = err instanceof VerificationWait || blocked ? err.message :
      'Protected verification could not confirm provider or local evidence';
    evidence = { ...event, classification: blocked ? 'visual-or-unknown' : 'pending', reason: publicReason(reason) };
  }

  try {
    git.ensure(event);
    candidate = await provider.readCandidate(event, git);
    evidence = await verifyProtectedDesignCandidate(event, {
      repositoryDir: git.dir,
      readCandidate: async () => candidate,
      readPRReceipt: async member => {
        const receipt = await receipts.readPR(member);
        if (!receipt) throw new VerificationWait('Waiting for a protected check on an original PR head');
        memberReceipts.push({ number: member.number, head: member.head, receipt: receipt.externalId,
          classification: receipt.classification, decisions: receipt.visual?.decisions ?? [] });
        return receipt;
      },
      // A scene or spec declared by the candidate cannot map its own impact.
      readSceneInventory: () => [],
      measureImpact: async (_event, paths) => {
        if (!runner) {
          if (fleet && event.kind === 'pull_request') await select();
          if (rendererPending) throw new VerificationWait('Protected VM renderer is temporarily unavailable');
          throw new DesignBlocked('Protected normal-route verification is unconfigured for this change');
        }
        const measured = runner.impact(event, paths, git, visual);
        policy = measured.policy;
        return measured;
      },
      measureParity: async (_event, scope) => {
        const selected = await select();
        if (selected.size !== scope.length || scope.some(key => !selected.has(key)))
          throw new DesignBlocked('Protected impact and the selected design cases cover different scope');
        return runner.measure(event, git, policy, selected, visual, fleet.original);
      },
      readAdmittedTasks: () => [...selections.values()].map(({ found, review }) =>
        ({ number: found.number, head: found.head, task: review.taskId })),
      readReview: task => [...selections.values()].find(({ review }) => review.taskId === task)?.review,
      readOriginal: id => {
        // The scoped Fleet response has revalidated metadata and bytes across the seal.
        // Only the selected captures were read by the comparator.
        for (const { review } of selections.values()) for (const option of review.options) {
          const frame = option.cases.flatMap(c => c.frames).find(f => f.artifactId === id);
          if (frame) return { state: 'stored', mediaType: 'image/png', sha256: frame.sha256, executionId: review.executionId,
            task: review.taskId, project: review.projectId, repository: review.repository };
        }
      },
    });
    if (evidence.classification === 'nonvisual' && memberReceipts.some(r => r.classification === 'selected-preview-matches'))
      throw new DesignBlocked('Combined impact cannot omit the visual scope of an original PR');
    conclusion = 'success';
    evidenceState = 'verified';
  } catch (err) {
    failed(err);
  }

  const publication = async (recheck = false) => {
    evidence.evidenceState = evidenceState;
    if (event.kind === 'pull_request') evidence.source = { branch: event.branch, repositoryId: event.sourceRepositoryId };
    if (event.kind === 'merge_group' && candidate) evidence.queue = {
      ref: event.ref, mergeMethod: candidate.mergeMethod,
      commits: candidate.members.map(({ number, head, mergeCommit, branch, sourceRepositoryId, queueEntryId }) =>
        ({ number, head, mergeCommit, branch, sourceRepositoryId, queueEntryId })),
      memberReceipts,
    };
    if (selections.size || Object.keys(visual).length)
      evidence.visual = { ...visual, decisions: [...selections.values()].map(decision) };
    const id = await receipts.retain(evidence, conclusion);
    const summary = evidence.reason ?? evidence.classification;
    const check = await checks.publish(event.head, id, conclusion, summary, async () => {
      if (recheck) {
        try {
          for (const [member, used] of selections) {
            const current = await readSelection(member);
            if (!same(decision(current), decision(used)))
              throw new DesignBlocked('The owner selection or writer changed during the check');
            if (candidate.members) {
              const receipt = await receipts.readPR(member);
              if (receipt?.externalId !== memberReceipts.find(r => r.number === member.number).receipt)
                throw new VerificationWait('The protected original-head receipt changed during the check');
            }
          }
          if (!runner.policyIsCurrent(visual.policy)) throw new DesignBlocked('The protected route policy changed during the check');
        } catch (err) {
          throw new FreshnessError(err);
        }
      }
      await provider.assertCurrent(event, candidate);
    });
    await receipts.recordReadback(id, check);
    return { ...check, kind: event.kind, number: event.number, ref: event.ref, classification: evidence.classification,
      evidenceState, reason: evidence.reason };
  };
  try {
    return await publication(conclusion === 'success' && Boolean(visual.policy));
  } catch (err) {
    if (err instanceof QueueModeUnconfirmed) {
      candidate = undefined;
      failed(err);
    } else if (err instanceof FreshnessError) {
      failed(err.cause);
    } else throw err;
    return publication();
  }
}

class FreshnessError extends Error {
  constructor(cause) { super('The protected inputs changed before check publication', { cause }); }
}
