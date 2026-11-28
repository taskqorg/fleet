// Run as the verifier service identity. This probe neither loads nor needs credentials.
import { parityRunner } from './parity.mjs';
import { candidateChanges } from '../../packages/ui/scripts/design-protected.mjs';
import { openRepository } from './git.mjs';

try {
  if (process.argv.length === 2) {
    console.log(JSON.stringify({ visualParity: 'ready', runtime: await parityRunner().probe() }));
  } else {
    const [action, state, kind, base, head] = process.argv.slice(2);
    if (action !== '--impact' || !state?.startsWith('/') || process.argv.length !== 7)
      throw new Error('Usage: renderer-probe.mjs [--impact /absolute/verifier/state pull_request|merge_group BASE HEAD]');
    const git = openRepository(state);
    const changes = candidateChanges(git.dir, { kind, base, head });
    const ancestor = git.diffBase({ base, head });
    console.log(JSON.stringify({ kind, base, head, ancestor, paths: changes.map(path => ({
      path, before: git.fingerprint(ancestor, path), after: git.fingerprint(head, path),
    })) }, null, 2));
  }
} catch (err) {
  console.error(JSON.stringify({ visualParity: 'unknown', reason: err.message }));
  process.exitCode = 1;
}
