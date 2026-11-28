// Trusted short-lived helper; it receives only a fixed operation and public Git inputs.
import { openRepository } from './git.mjs';
import { candidateChanges } from '../../packages/ui/scripts/design-protected.mjs';

const input = [];
for await (const chunk of process.stdin) input.push(chunk);
try {
  const { state, method, args } = JSON.parse(Buffer.concat(input).toString('utf8'));
  if (!['ensure', 'diffBase', 'fingerprint', 'archive', 'measureInputs', 'groupChain', 'changes'].includes(method)) throw new Error('Invalid Git operation');
  const git = openRepository(state);
  const value = method === 'changes' ? candidateChanges(git.dir, ...args) : git[method](...args);
  process.stdout.write(JSON.stringify({ ok: true, value: method === 'archive' ? value.toString('base64') : value }));
} catch (error) {
  process.stdout.write(JSON.stringify({ ok: false, type: error.constructor.name, message: error.message }));
}
