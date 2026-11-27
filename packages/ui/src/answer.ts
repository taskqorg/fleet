// Answering open questions, the same way on every screen (controls/decision-block.md#open-question, OR-53): the labels a
// question is answered with and the write state of a view's answers. `controls/open-question.tsx` shows them.
import { createSignal } from 'solid-js';
import type { AcceptanceEvidenceRow } from './controls/acceptance-evidence.tsx';

/** The connection a key request (a secret question) asks a key for: its options are {connection}. */
export const keyConnection = (options: unknown): string | undefined => {
  const c = options && !Array.isArray(options) && typeof options === 'object' ? (options as { connection?: unknown }).connection : undefined;
  return typeof c === 'string' ? c : undefined;
};
/** One answer button of a question card. */
export type QuestionChoice = { label: string; primary?: boolean };

/** A question as the reads carry it: `questions_list` and `session_get` rows (text) and thread blocks (question). */
export type OpenQuestionRow = AcceptanceEvidenceRow & { id: string; code?: string | null; text?: string | null; question?: string | null; kind?: string | null; options?: unknown; deliveryAuthority?: boolean };

/** Delivery decisions keep the same options on the task, the conversation and Telegram. */
export function deliveryAuthorityChoices(options: unknown): string[] | undefined {
  if (!options || typeof options !== 'object' || Array.isArray(options)) return undefined;
  const o = options as { deliveryAuthority?: unknown; choices?: unknown };
  return o.deliveryAuthority === true && Array.isArray(o.choices)
    ? o.choices.filter((choice): choice is string => typeof choice === 'string' && choice.length > 0)
    : undefined;
}

export function deliveryAuthorityContent(q: Pick<OpenQuestionRow, 'text' | 'options' | 'deliveryAuthority'>) {
  if (!(q.deliveryAuthority || deliveryAuthorityChoices(q.options)) || !q.text) return undefined;
  const divider = q.text.indexOf('\n\n');
  if (divider === -1) return undefined;
  const evidence = q.text.slice(divider + 2).split('\n')
    .map(line => /^https:\/\/[^\s<>()]+$/.test(line) ? `[Delivery evidence](${line})` : line)
    .join('  \n');
  return { title: q.text.slice(0, divider), evidence };
}

/**
 * The labels a question can be answered with, in the asker's order: its options (strings, or objects with a label or a
 * value), the two answers of an acceptance, `decline` for a key request (the key itself goes to `addKey`), else none.
 */
export function answerLabels(q: Pick<OpenQuestionRow, 'kind' | 'options'>): string[] {
  if (keyConnection(q.options)) return ['decline'];
  const authority = deliveryAuthorityChoices(q.options);
  if (authority) return authority;
  if (Array.isArray(q.options) && q.options.length) {
    return (q.options as unknown[]).map(o => {
      if (o && typeof o === 'object') {
        const r = o as { label?: unknown; value?: unknown };
        return typeof r.label === 'string' && r.label ? r.label : typeof r.value === 'string' ? r.value : '';
      }
      return o == null ? '' : String(o);
    }).filter(Boolean);
  }
  return q.kind === 'acceptance' ? ['accepted', 'rejected'] : [];
}
/** The answer buttons: `answerLabels`, the first one primary, except a key request's decline. */
export const answerChoices = (q: Pick<OpenQuestionRow, 'kind' | 'options'>): QuestionChoice[] =>
  answerLabels(q).map((label, i) => ({ label, primary: i === 0 && !keyConnection(q.options) }));

/** How the app sends answers: `questions_answer` (retried while its outcome is unknown) and `connections_key_add`. */
export type AnswerIO<R = unknown> = {
  online: () => boolean;
  answer: (questionId: string, answer: string, onUnknown: () => void) => Promise<R>;
  addKey: (questionId: string, connection: string, value: string) => Promise<unknown>;
};
type Outcome<R> = { answering?: string; unknown?: boolean; error?: string; answered?: { answer: string; at: number; reply?: R } };
export type Answers<R = unknown> = {
  /** The label on its way, while the write is pending. */
  answering: (id: string) => string | undefined;
  /** The answer given here, once the write returned; `reply` is the stored question row. */
  answered: (id: string) => { answer: string; at: number; reply?: R } | undefined;
  /** What the person reads about the write: offline, outcome unknown (retrying), or the refusal. */
  note: (id: string) => string | undefined;
  /** Sends an answer; a refusal or an offline browser leaves the question open and sets `note`. */
  send: (q: OpenQuestionRow, answer: string) => Promise<void>;
  /** Sends a requested key's value to its connection; it answers the question with the key's fingerprint only. */
  addKey: (q: OpenQuestionRow, value: string) => Promise<void>;
};

const why = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** The answer state of every question a view shows, keyed by question id. */
export function useAnswer<R = unknown>(io: AnswerIO<R>): Answers<R> {
  const [state, setState] = createSignal<Record<string, Outcome<R>>>({});
  const put = (id: string, o: Outcome<R>) => setState(m => ({ ...m, [id]: o }));
  const write = async (q: OpenQuestionRow, label: string, call: (onUnknown: () => void) => Promise<R | undefined>) => {
    if (!io.online()) { put(q.id, { error: 'Offline: the answer was not sent.' }); return; }
    put(q.id, { answering: label });
    try {
      const reply = await call(() => put(q.id, { answering: label, unknown: true }));
      put(q.id, { answered: { answer: label, at: Date.now(), reply } });
    } catch (e) {
      put(q.id, { error: `The answer was not sent: ${why(e)}` });
    }
  };
  return {
    answering: id => state()[id]?.answering,
    answered: id => state()[id]?.answered,
    note: id => {
      const o = state()[id];
      return o?.error ?? (o?.unknown ? 'Answer outcome unknown, retrying.' : undefined);
    },
    send: (q, a) => write(q, a, onUnknown => io.answer(q.id, a, onUnknown)),
    addKey: (q, value) => {
      const conn = keyConnection(q.options);
      return conn ? write(q, 'key added', async () => { await io.addKey(q.id, conn, value); return undefined; }) : Promise.resolve();
    },
  };
}
