// Transcript rows of the one session view (docs/design-system/pages/session.md, "Tool lines", OR-52): the thread's blocks
// as messages, folded tool lines, answered questions and results. Pure: it reads only the order of the blocks
// thread_entries returns, so pages join because they are contiguous. `Thread quiet` renders these rows.
import type { Block } from '../../../../contracts/generated/ops.ts';
import type { ToolCall } from './tool-line.tsx';
import type { IconName } from '../registry.ts';
import { openStates } from '../states.ts';

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
const ACTIVE: string[] = openStates('execution');

/** The short name of a tool: an MCP tool `mcp__server__name` is `name`. */
export const toolName = (name: string) => name.split('__').at(-1) || name;

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** What a call acted on, in one short line: a code, a path, a command, a query or a URL, never raw JSON or a bare UUID. */
export function callTarget(input: unknown): string {
  const i = rec(input);
  const readable = (x: unknown) => typeof x === 'string' && x.trim() !== '' && !UUID.test(x.trim());
  const v = ['code', 'file_path', 'path', 'name', 'command', 'cmd', 'pattern', 'query', 'url', 'title', 'description', 'prompt'].map(k => i[k]).find(readable)
    ?? Object.values(i).find(readable)
    ?? (typeof input === 'string' ? input : '');
  const s = String(v).replace(/\s+/g, ' ').trim();
  const short = s.includes('/') && !s.includes(' ') ? s.split('/').slice(-2).join('/') : s;
  return short.length > 80 ? short.slice(0, 79) + '…' : short;
}

/** A call's kind: the chips of a tool line group calls by it (Conversations v2, owner pick c2). */
export type ToolKind = 'shell' | 'files' | 'edits' | 'search' | 'web' | 'results' | 'context' | 'fleet' | 'notice' | 'other';
/** The kind of a tool by its raw name: harness tools by their name, Fleet's MCP tools (snake_case, or mcp__fleet__) as fleet. */
export function toolKind(raw: string): ToolKind {
  const name = toolName(raw);
  if (/^context/i.test(name)) return 'context';
  if (/^artifact_/.test(name)) return 'results';
  if (/^(web|fetch|browse)/i.test(name) || /^web_/.test(name)) return 'web';
  if (/^(bash|shell|exec|run|command)/i.test(name)) return 'shell';
  if (/^(edit|write|multiedit|apply_patch|patch|create|filechange)/i.test(name)) return 'edits';
  if (/^(read|view|cat|open)/i.test(name)) return 'files';
  if (/^(grep|glob|search|find|ls)$/i.test(name) || /^(grep|glob)/i.test(name)) return 'search';
  if (raw.startsWith('mcp__fleet__') || /^[a-z]+(_[a-z]+)+$/.test(name)) return 'fleet';
  return 'other';
}

const KINDS: Record<ToolKind, { label: string; icon: IconName }> = {
  shell: { label: 'Shell', icon: 'terminal' },
  files: { label: 'Files', icon: 'file-text' },
  edits: { label: 'Edits', icon: 'pencil' },
  search: { label: 'Search', icon: 'search' },
  web: { label: 'Web', icon: 'external-link' },
  results: { label: 'Results', icon: 'paperclip' },
  context: { label: 'Context', icon: 'minimize-2' },
  fleet: { label: 'Fleet', icon: 'bot' },
  notice: { label: 'Notice', icon: 'clock' },
  other: { label: 'Tools', icon: 'layers-2' },
};

export type ToolChip = { kind: ToolKind; label: string; icon: IconName; n: number; failed: number; secs: number; running: boolean; calls: ToolCall[] };

/** The chips of a line: one per kind in the order the kinds first appear. */
export function toolChips(calls: ToolCall[]): ToolChip[] {
  const by = new Map<ToolKind, ToolChip>();
  for (const c of calls) {
    const kind = c.kind ?? 'other', n = c.n ?? 1;
    const chip = by.get(kind) ?? { kind, ...KINDS[kind], n: 0, failed: 0, secs: 0, running: false, calls: [] };
    chip.n += n; chip.secs += c.secs; chip.calls.push(c);
    if (c.failed) chip.failed += n;
    if (c.running) chip.running = true;
    by.set(kind, chip);
  }
  return [...by.values()];
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
    out.push({ name, kind: toolKind(t.name), target, error: failed ? errorOf(t) || undefined : undefined, secs, failed, input: t.input, output: t.output, running: RUNNING.includes(t.state) });
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
    // A failure the agent recovered from (a later call of the same kind worked) stays quiet; its chip counts it.
    const failed = tools.some((t, i) => FAILED.includes(t.state) && !tools.slice(i + 1).some(u => toolKind(u.name) === toolKind(t.name) && !FAILED.includes(u.state)));
    const calls: ToolCall[] = [...notices.map(n => ({ name: 'Fleet', kind: 'notice' as const, target: noticeLine(n), secs: 0 })), ...callsOf(tools)];
    const failedCall = failed ? calls.findLast(c => c.failed) : undefined;
    const words = failedCall ? `${failedCall.name} failed${failedCall.error || failedCall.target ? `: ${failedCall.error || failedCall.target}` : ''}` : tools.length ? toolWords(tools.map(t => t.name)) : notices.length ? `Fleet: ${noticeLine(notices.at(-1)!)}` : 'Thought it over';
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
