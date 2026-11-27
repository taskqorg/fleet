// Open question: one open question answered in place, the same way on every screen (controls/decision-block.md#open-question,
// OR-53). `answer.ts:useAnswer` owns the write state; `OpenQuestion` shows a `QuestionCard` and the answer's outcome under it.
import { Show } from 'solid-js';
import { ErrorBanner } from './primitives.tsx';
import { QuestionCard } from './question-card.tsx';
import { answerChoices, keyConnection, type Answers, type OpenQuestionRow } from '../answer.ts';

/** The outcome of an answer written under a question that is not a card (a full decision block). */
export function AnswerNote(p: { answers: Answers<unknown>; id: string }) {
  return <Show when={p.answers.note(p.id)} keyed>{t => <ErrorBanner text={t} />}</Show>;
}

/**
 * One open question as a card: its choices, a typed answer, or for a key request a password field with Add key and
 * decline. `title` replaces the question's text (a list names the asking session first).
 */
export function OpenQuestion(p: { q: OpenQuestionRow; answers: Answers<unknown>; title?: string }) {
  const text = () => p.title ?? p.q.text ?? p.q.question ?? '';
  return (
    <>
      <QuestionCard
        code={p.q.code}
        questionId={p.q.id}
        question={text()}
        choices={answerChoices(p.q)}
        answering={p.answers.answering(p.q.id)}
        answered={p.answers.answered(p.q.id)}
        onAnswer={a => void p.answers.send(p.q, a)}
        onSecret={keyConnection(p.q.options) ? v => void p.answers.addKey(p.q, v) : undefined}
        draftKey={'question:' + p.q.id}
      />
      <AnswerNote answers={p.answers} id={p.q.id} />
    </>
  );
}
