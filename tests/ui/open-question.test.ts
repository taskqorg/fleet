// Requirement: decision-block.md "Open question": a question is answered the same way on every screen. Risk (the six
// copies this replaced): an object option shows "[object Object]", an acceptance has no buttons, a key request offers a
// typed answer, an offline browser or an unknown outcome is silent, a refusal leaves no reason.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { answerLabels, answerChoices, useAnswer, type AnswerIO } from '../../packages/ui/src/answer.ts';

test('answerLabels: strings, objects by label or value, acceptance, key request', () => {
  assert.deepEqual(answerLabels({ options: ['yes', 'no'] }), ['yes', 'no']);
  assert.deepEqual(answerLabels({ options: [{ label: 'Compact', value: 'c' }, { value: 'wide' }, {}] }), ['Compact', 'wide']);
  assert.deepEqual(answerLabels({ kind: 'acceptance', options: null }), ['accepted', 'rejected']);
  assert.deepEqual(answerLabels({ kind: 'secret', options: { connection: 'openai' } }), ['decline']);
  assert.deepEqual(answerLabels({ kind: 'question', options: null }), []);
  assert.deepEqual(answerChoices({ options: ['a', 'b'] }).map(c => !!c.primary), [true, false]);
  assert.equal(answerChoices({ options: { connection: 'openai' } })[0]!.primary, false);
});

const io = (over: Partial<AnswerIO<string>> = {}): AnswerIO<string> => ({
  online: () => true, answer: async (_id, a) => `row:${a}`, addKey: async () => ({}), ...over,
});
const q = { id: 'q1', text: 'Ship it?', options: ['yes', 'no'] };

test('useAnswer: answered with the stored row; offline, unknown and refused say so', async () => {
  const ok = useAnswer(io());
  await ok.send(q, 'yes');
  assert.deepEqual({ ...ok.answered('q1'), at: 0 }, { answer: 'yes', at: 0, reply: 'row:yes' });

  const off = useAnswer(io({ online: () => false }));
  await off.send(q, 'yes');
  assert.equal(off.answered('q1'), undefined);
  assert.match(off.note('q1') ?? '', /^Offline/);

  let release!: () => void;
  const unknown = useAnswer(io({ answer: (_id, a, onUnknown) => { onUnknown(); return new Promise(r => { release = () => r(a); }); } }));
  const pending = unknown.send(q, 'no');
  assert.equal(unknown.answering('q1'), 'no');
  assert.match(unknown.note('q1') ?? '', /outcome unknown/);
  release(); await pending;
  assert.equal(unknown.note('q1'), undefined);

  const refused = useAnswer(io({ answer: async () => { throw new Error('closed'); } }));
  await refused.send(q, 'yes');
  assert.equal(refused.note('q1'), 'The answer was not sent: closed');
});

test('useAnswer: a key goes to addKey only, never to the answer write', async () => {
  const sent: string[] = [];
  const a = useAnswer(io({ answer: async (_id, x) => { sent.push('answer:' + x); return x; }, addKey: async (_id, c, v) => { sent.push(`key:${c}:${v}`); } }));
  await a.addKey({ id: 'k', options: { connection: 'openai' } }, 'sk-test');
  assert.deepEqual(sent, ['key:openai:sk-test']);
  assert.equal(a.answered('k')?.answer, 'key added');
});

test('answerLabels: an acceptance with an empty option list still has its two answers', () => {
  assert.deepEqual(answerLabels({ kind: 'acceptance', options: [] }), ['accepted', 'rejected']);
});
