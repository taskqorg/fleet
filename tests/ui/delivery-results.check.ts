import assert from 'node:assert/strict';
import type { Page } from 'playwright';
import type { Block, ThreadEntriesReply } from '../../contracts/generated/ops.ts';
import type { checkServer } from './seed.ts';

type Server = Awaited<ReturnType<typeof checkServer>>;
const projectId = 'hello-world';
const sessionId = 'delivery-results-session';
const executionId = 'delivery-results-execution';
const url = '/conversations/' + sessionId;

export async function deliveryResultFixture(server: Pick<Server, 'seed' | 'host'>) {
  const { seed } = server;
  const hostId = 'delivery-results-host';
  const host = await server.host(hostId);
  seed.bulk(() => {
    seed.put('session', { id: sessionId, projectId, title: 'Delivery results', createdAt: Date.now() });
    seed.execution({ id: executionId, sessionId, projectId, hostId, state: 'completed', finishedAt: Date.now() });
    seed.put('event', { id: 'ended:' + executionId, executionId, sessionId, projectId, type: 'execution.ended', seq: 1 });
    seed.put('message', { id: 'delivery-result-neighbor', executionId, sessionId, projectId, text: 'Later transcript row' });
  });
  const publish = async (state: string, extra: Record<string, unknown> = {}) => {
    const response = await host('/api/hosts/publishing/record', {
      hostId, executionId, projectId, action: 'status', sourceExecutionId: executionId,
      provider: 'github', host: 'github.com', repo: 'invented/delivery', kind: 'pull_request',
      number: 12, branch: 'fleet/delivery-results', headSha: 'd'.repeat(40), isDraft: false,
      title: 'Delivery fixture', url: 'https://github.com/invented/delivery/pull/12', state, ...extra,
    });
    assert.equal(response.status, 200, await response.text());
  };
  const artifact = () => seed.artifact({
    id: 'late-delivery-file', executionId, name: 'late-file.txt', mediaType: 'text/plain',
    bytes: Buffer.from('Retained result'),
  });
  return { url, key: { session: sessionId }, publish, artifact };
}

// Requirement: FLT-10 Slice 2 shows late publishing and artifacts on a single result at the execution end.
// Risk: lost feed updates, guessed checks, changed row keys, or delivery-only blocks omitted from paged reads.
export async function checkDeliveryResults(page: Page, server: Pick<Server, 'seed' | 'host' | 'origin'>) {
  const fixture = await deliveryResultFixture(server);
  const read = () => page.evaluate(async key => {
    const response = await fetch('/api/ops/thread_entries', {
      method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ key, limit: 100 }),
    });
    if (!response.ok) throw new Error(await response.text());
    return await response.json() as ThreadEntriesReply;
  }, fixture.key);
  const results = (entries: Block[]) => entries.filter(entry => entry.kind === 'result');

  await page.goto(server.origin + fixture.url);
  await page.getByText('Later transcript row', { exact: true }).waitFor();
  const before = await read();
  assert.equal(results(before.entries).length, 0);
  const row = page.locator('.th-result');
  await fixture.publish('UNRECOGNIZED');
  await row.getByText('Delivery stage unknown', { exact: true }).waitFor();
  const rendered = row.locator('xpath=ancestor::*[@data-key][1]');
  const key = await rendered.getAttribute('data-key');
  const delivery = await read();
  assert.deepEqual(results(delivery.entries).map(({ key, deliveryStage, artifacts }) => ({ key, deliveryStage, artifacts })),
    [{ key, deliveryStage: 'unknown', artifacts: [] }]);
  assert.equal(delivery.total, (before.total ?? 0) + 1);
  assert.equal(delivery.entries.at(-1)?.key, before.entries.at(-1)?.key);

  await fixture.publish('OPEN', { autoMergeEnabled: true });
  await row.getByRole('img', { name: 'PR open', exact: true }).waitFor();
  await fixture.publish('OPEN', { result: { statusCheckRollup: [{ status: 'IN_PROGRESS' }] } });
  await row.getByRole('img', { name: 'Checks running', exact: true }).waitFor();
  await fixture.publish('OPEN', {
    sourceExecutionId: undefined,
    result: { statusCheckRollup: [{ status: 'COMPLETED', conclusion: 'FAILURE' }, { status: 'QUEUED' }] },
  });
  await row.getByRole('img', { name: 'Checks failed', exact: true }).waitFor();
  fixture.artifact();
  await row.getByRole('link', { name: 'late-file.txt' }).waitFor();
  await fixture.publish('OPEN', { result: { mergeQueueEntry: { state: 'QUEUED' } } });
  await row.getByRole('img', { name: 'Merging', exact: true }).waitFor();
  await fixture.publish('MERGED', { sourceExecutionId: undefined });
  await row.getByRole('img', { name: 'Merged', exact: true }).waitFor();
  assert.equal(await row.count(), 1);
  assert.equal(await rendered.getAttribute('data-key'), key);
  assert.equal(await row.getByRole('link', { name: 'late-file.txt' }).getAttribute('href'), '/results/late-delivery-file');
}
