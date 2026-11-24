// Run failure causes in plain words: the same mapping as contract.PlainCause (contracts/vocabulary.json causes), for rows
// that carry only the stored cause code (executions_runners `problem`). Rows that carry `failure` use its text directly.
import vocabulary from '../../contracts/vocabulary.json' with { type: 'json' };

export type PlainCause = { cause: string; text: string };

/** The first cause whose match the code starts with; the last entry matches everything. */
export function plainCause(code: string): PlainCause {
  for (const c of vocabulary.causes) if (c.match.some(m => code.startsWith(m))) return { cause: c.id, text: c.text };
  const last = vocabulary.causes[vocabulary.causes.length - 1];
  return { cause: last.id, text: last.text };
}
