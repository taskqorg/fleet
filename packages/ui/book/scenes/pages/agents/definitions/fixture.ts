import { type Harness, type StateOf } from '../../../../../src/index.ts';

type AgentDef = { id: string; state: StateOf<'definition'>; h: Harness; model: string; role: string; right: string };
export const AGENT_DEFS: AgentDef[] = [
  { id: 'claude-reviewer', state: 'draft', h: 'claude', model: 'sonnet', role: 'reviewer', right: '57' },
  { id: 'pm', state: 'ready', h: 'codex', model: 'gpt-6.1-sol', role: 'PM', right: '412' },
  { id: 'claude-worker', state: 'ready', h: 'claude', model: 'opus', role: 'worker', right: '1,204' },
  { id: 'codex-researcher', state: 'ready', h: 'codex', model: 'gpt-6.1', role: 'worker', right: '96' },
  { id: 'codex-judge', state: 'ready', h: 'codex', model: 'gpt-6.1', role: 'judge', right: '340' },
  { id: 'antigravity-worker', state: 'unchecked', h: 'antigravity', model: 'gemini-3', role: 'worker', right: '0' },
  { id: 'design-rules-data', state: 'incompatible', h: 'antigravity', model: 'gemini-3', role: 'worker', right: '0' },
];
export const HARNESS_TOOLS = ['Read', 'Glob', 'Grep', 'Bash (read-only)', 'Edit', 'Write', 'WebFetch', 'Agent'];
