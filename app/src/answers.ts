// The app's side of answering open questions (packages/ui `answer.ts:useAnswer`, controls/decision-block.md#open-question).
import { useAnswer, type Answers } from '../../packages/ui/src/answer.ts';
import { online, writeOnce, addRequestedKey, useAlive, type Ops } from './api.ts';

/**
 * The answer state of a view's open questions: `questions_answer` retried while its outcome is unknown, a key request's
 * value to `connections_key_add`; the writes stop when the view unmounts.
 */
export function useAnswers(): Answers<Ops['questions_answer']['reply']> {
  const alive = useAlive();
  return useAnswer({
    online: () => online.value,
    answer: (questionId, answer, onUnknown) => writeOnce('questions_answer', { questionId, answer }, onUnknown, { signal: alive }),
    addKey: (questionId, connection, value) => addRequestedKey(questionId, connection, value, alive),
  });
}
