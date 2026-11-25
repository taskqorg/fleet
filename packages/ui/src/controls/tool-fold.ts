// Transcript rows of the one session view (docs/design-system/pages/session.md, "Tool lines", OR-52): the thread's blocks
// as messages, folded tool lines, answered questions and results. Pure: it reads only the order of the blocks
// thread_entries returns, so pages join because they are contiguous. `Thread quiet` renders these rows.
import type { Block } from '../../../../contracts/generated/ops.ts';
import type { ToolCall } from './tool-line.tsx';

type Of<K extends Block['kind']> = Extract<Block, { kind: K }>;
export type ToolGroup = {
  kind: 'tools'; key: string; words: string; calls: ToolCall[]; state: 'done' | 'running' | 'failed';
  secs: number; startedAt?: number; executionId: string | null;
};
export type FoldedRow = Of<'text'> | Of<'question'> | Of<'result'> | ToolGroup;

/** Fleet's own notices to an agent (a watch timer, a wake, a run request): system events, folded like tools (OR-52). */
export const isNotice = (b: Block): b is Of<'text'> => b.kind === 'text' && b.role === 'operator' && b.by === 'Fleet';
const noticeLine = (b: Of<'text'>) => (b.plain || b.markdown || '').split('\n')[0].replace(/^Fleet:\s*/, '').trim();

const rec = (v: unknown): Record<string, unknown> => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {});
const RUNNING = ['running', 'started', 'pending', 'preparing'];
const FAILED = ['failed', 'error', 'unfinished'];
const ACTIVE = ['preparing', 'running', 'waiting'];

/** The short name of a tool: an MCP tool `mcp__server__name` is `name`. */
export const toolName = (name: string) => name.split('__').at(-1) || name;

/** What a call acted on, in one short line: a path, a command, a query or a URL, never raw JSON. */
export function callTarget(input: unknown): string {
  const i = rec(input);
  const v = ['file_path', 'path', 'command', 'cmd', 'pattern', 'query', 'url', 'description', 'prompt'].map(k => i[k]).find(x => typeof x === 'string' && x.trim())
    ?? Object.values(i).find(x => typeof x === 'string' && x.trim())
    ?? (typeof input === 'string' ? input : '');
  const s = String(v).replace(/\s+/g, ' ').trim();
  const short = s.includes('/') && !s.includes(' ') ? s.split('/').slice(-2).join('/') : s;
  return short.length > 80 ? short.slice(0, 79) + '…' : short;
}

const VERBS: [RegExp, string, string][] = [
  [/^(read|view|cat|open)/i, 'read', 'file'],
  [/^(edit|write|multiedit|apply_patch|patch|create)/i, 'edited', 'file'],
  [/^(bash|shell|exec|run|command)/i, 'ran', 'command'],
  [/^(grep|glob|search|find|ls|list)/i, 'searched', 'time'],
  [/^(web|fetch|browse)/i, 'looked up', 'page'],
];

/** "Read 5 files, ran 2 commands": what the line's calls did, in the order the kinds first appear. */
export function toolWords(names: string[]): string {
  const counts = new Map<string, { verb: string; noun: string; n: number }>();
  for (const raw of names) {
    const name = toolName(raw);
    const hit = VERBS.find(([re]) => re.test(name));
    const id = hit ? hit[1] : name;
    const cur = counts.get(id) ?? { verb: hit ? hit[1] : 'used', noun: hit ? hit[2] : name, n: 0 };
    cur.n++;
    counts.set(id, cur);
  }
  const all = [...counts.values()];
  const parts = all.filter(c => c.verb !== 'used').map(c => (c.noun === 'time' ? (c.n > 1 ? `${c.verb} ${c.n} times` : c.verb) : `${c.verb} ${c.n} ${c.noun}${c.n > 1 ? 's' : ''}`));
  const used = all.filter(c => c.verb === 'used').map(c => `${c.noun}${c.n > 1 ? ` ×${c.n}` : ''}`);
  if (used.length) parts.push(`used ${used.slice(0, 3).join(', ')}${used.length > 3 ? ` and ${used.length - 3} more` : ''}`);
  const text = parts.join(', ');
  return text ? text[0].toUpperCase() + text.slice(1) : 'Worked';
}

/** Calls of a line; the same tool on the same target in a row aggregates as ×N (OR-58). */
function callsOf(tools: Of<'tool'>[]): ToolCall[] {
  const out: ToolCall[] = [];
  for (const t of tools) {
    const name = toolName(t.name), target = callTarget(t.input), failed = FAILED.includes(t.state);
    const secs = t.startedAt != null && t.endedAt != null ? Math.max(0, (t.endedAt - t.startedAt) / 1000) : 0;
    const prev = out.at(-1);
    if (prev && prev.name === name && prev.target === target && !!prev.failed === failed) { prev.n = (prev.n ?? 1) + 1; prev.secs += secs; continue; }
    out.push({ name, target: failed ? errorOf(t) || target : target, secs, failed });
  }
  return out;
}

/** The first line of a failed call's output: the cause, one click away (UR-63). */
function errorOf(t: Of<'tool'>): string {
  const o = t.output, r = rec(o);
  const text = typeof o === 'string' ? o : typeof r.error === 'string' ? r.error : typeof r.stderr === 'string' ? r.stderr : typeof r.output === 'string' ? r.output : '';
  const line = text.split('\n').map(l => l.trim()).find(Boolean) ?? '';
  return line.length > 120 ? line.slice(0, 119) + '…' : line;
}

/**
 * Folds the blocks: tool and reasoning blocks and Fleet's notices between two other blocks become one line; a text block, a question, a result
 * or a new execution (a boundary) ends it. Open questions are left out: the card above the transcript holds them.
 * `executions` gives each execution's state, so the last line of a failed run is failed and the newest of a working run runs.
 */
export function foldBlocks(blocks: Block[], executions: ReadonlyMap<string, string> = new Map()): FoldedRow[] {
  const rows: FoldedRow[] = [];
  let tools: Of<'tool'>[] = [], thoughts: Of<'reasoning'>[] = [], notices: Of<'text'>[] = [], first: string | null = null, exec: string | null = null;
  const lastOfExec = new Map<string, number>();
  const flush = () => {
    if (!tools.length && !thoughts.length && !notices.length) return;
    const all = [...tools, ...thoughts, ...notices];
    const starts = tools.map(t => t.startedAt ?? t.at).filter((x): x is number => x != null).concat([...thoughts, ...notices].map(t => t.at).filter((x): x is number => x != null));
    const ends = tools.map(t => t.endedAt ?? t.startedAt ?? t.at).filter((x): x is number => x != null).concat([...thoughts, ...notices].map(t => t.at).filter((x): x is number => x != null));
    const startedAt = starts.length ? Math.min(...starts) : undefined;
    const endedAt = ends.length ? Math.max(...ends) : undefined;
    const running = tools.some(t => RUNNING.includes(t.state));
    const failed = tools.some(t => FAILED.includes(t.state));
    const calls: ToolCall[] = [...notices.map(n => ({ name: 'Fleet', target: noticeLine(n), secs: 0 })), ...callsOf(tools)];
    const failedCall = calls.find(c => c.failed);
    const words = failedCall ? `${failedCall.name} failed${failedCall.target ? `: ${failedCall.target}` : ''}` : tools.length ? toolWords(tools.map(t => t.name)) : notices.length ? `Fleet: ${noticeLine(notices.at(-1)!)}` : 'Thought it over';
    const execId = exec ?? all.find(b => b.executionId)?.executionId ?? null;
    rows.push({ kind: 'tools', key: first!, words, calls, state: running ? 'running' : failed ? 'failed' : 'done', secs: startedAt != null && endedAt != null ? (endedAt - startedAt) / 1000 : 0, startedAt, executionId: execId });
    if (execId) lastOfExec.set(execId, rows.length - 1);
    tools = []; thoughts = []; notices = []; first = null;
  };
  for (const b of blocks) {
    if (b.kind === 'tool' || b.kind === 'reasoning' || isNotice(b)) {
      first ??= b.key;
      if (b.kind === 'tool') tools.push(b); else if (b.kind === 'reasoning') thoughts.push(b); else notices.push(b);
      exec = b.executionId ?? exec;
      continue;
    }
    flush();
    if (b.kind === 'boundary') { exec = b.executionId; continue; }
    // Open questions sit in the card above the transcript; withdrawn and expired ones are internal notices.
    if (b.kind === 'question' && b.state !== 'answered') continue;
    rows.push(b);
  }
  flush();
  // A run's state marks its last line: failed names the failure, working ticks (the running form).
  for (const [id, i] of lastOfExec) {
    const st = executions.get(id), row = rows[i] as ToolGroup;
    if (st === 'failed' && row.state !== 'failed') rows[i] = { ...row, state: 'failed', words: `${row.words}, then the run failed` };
    else if (st && ACTIVE.includes(st) && i === rows.length - 1 && row.state === 'done') rows[i] = { ...row, state: 'running' };
  }
  return rows;
}
