// Fixed credential-free guest entry. This file is bundled with the signed release.
import { readFileSync } from 'node:fs';
import { isolatedVMJob } from './parity.mjs';
import { DesignBlocked } from './provider.mjs';

try {
  const operation = Number(process.argv[2]);
  if (process.argv.length !== 3 || !Number.isInteger(operation) || operation < 1 || operation > 4)
    throw new Error('Unsupported fixed job');
  const payload = readFileSync(0);
  process.stdout.write(await isolatedVMJob(operation, payload));
} catch (error) {
  process.exitCode = error instanceof DesignBlocked ? 42 : 1;
}
