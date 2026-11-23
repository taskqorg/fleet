import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { executorBinary } from './executor-binary.ts';

// Keep numeric bytes across the JS boundary, including integers outside the safe range.
function parseJSON(json: string) {
  return JSON.parse(json, (_key, value, context) =>
    typeof value === 'number' ? JSON.rawJSON(context.source) : value);
}

// The captures were stored as JSON objects. Compare canonical JSON bytes, ignoring
// object key order, whitespace and equivalent string escapes. No fields are masked.
function canonicalJSON(value: unknown) {
  return JSON.stringify(value, (_key, field) => {
    if (field && typeof field === 'object' && !Array.isArray(field) && !JSON.isRawJSON(field)) {
      return Object.fromEntries(Object.keys(field).sort().map(key => [key, field[key]]));
    }
    return field;
  });
}

type Capture = {
  execution: { id: string; harness: string };
  documents: {
    kind: string;
    data: {
      executionId: string;
      seq: { rawJSON: string };
      timestamp: { rawJSON: string };
      data: Record<string, unknown>;
    };
  }[];
};

// Requirement: FLT-10 / Fleet Conversations captured projection equality.
// Risk: changes to transcript blocks or projection metadata can pass the browser
// check, which reads stored projections without running the current executor.
test('captured Conversations projections match fleet-executor reproject', async t => {
  const binary = executorBinary();
  for (const [name, count] of [
    ['codex-6a55ab8b', 9],
    ['claude-127d6fd7', 4],
    ['lost-receipt-d89cc199', 17],
  ] as const) {
    await t.test(`${name} (${count} projected records)`, () => {
      const url = new URL(`../ui/fixtures/conversations/${name}.json`, import.meta.url);
      const { execution, documents }: Capture = parseJSON(readFileSync(url, 'utf8'));
      const expected = [];
      const input = [];
      let previousSeq = -1n;
      for (const { kind, data } of documents) {
        if (kind !== 'event' && kind !== 'event-delta') continue;
        const { fleetNormalized, ...record } = data.data;
        // Executor bookkeeping events have no stored projection.
        if (fleetNormalized === undefined) continue;
        const { executionId, seq, timestamp } = data;
        assert.equal(executionId, execution.id, 'capture execution identity');
        const order = BigInt(seq.rawJSON);
        assert.ok(order > previousSeq, `capture execution sequence ${order} is out of order`);
        previousSeq = order;
        expected.push({ order, projection: canonicalJSON(fleetNormalized) });
        input.push(JSON.stringify({ executionId, harness: execution.harness, timestamp, record }));
      }
      assert.equal(expected.length, count, 'capture projection count');

      const stdout = execFileSync(binary, ['reproject'], { input: input.join('\n') + '\n' });
      const lines = new TextDecoder('utf-8', { fatal: true }).decode(stdout).split('\n');
      assert.equal(lines.pop(), '', 'reproject output must end with a newline');
      assert.equal(lines.length, expected.length, 'one projection per input record');
      for (const [index, { order, projection }] of expected.entries()) {
        assert.ok(canonicalJSON(parseJSON(lines[index])) === projection,
          `projection differs at execution sequence ${order}`);
      }
    });
  }
});
