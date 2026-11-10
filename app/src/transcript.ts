import type { Entry } from '../../packages/ui/src/index.ts';

// Transcript entries in execution order: the operator's message, then that execution's agent messages and tool calls.
export function entries(s: any): Entry[] {
  const out: Entry[] = [];
  const execs = new Map<string, any>((s.executions ?? []).map((e: any) => [e.id, e]));
  // Turns in message order; each message's execution events follow it. Events of executions without a message come last.
  const turns = [...(s.messages ?? []).map((m: any) => m.executionId), ...(s.events ?? []).map((e: any) => e.executionId)].filter((v, i, a) => v && a.indexOf(v) === i);
  for (const id of turns) {
    const e = execs.get(id) ?? {}, t = e.createdAt ?? e.startedAt ?? Date.now();
    for (const m of (s.messages ?? []).filter((m: any) => m.executionId === id)) out.push({ id: 'm' + m.id, t, who: 'You', text: m.text });
    for (const ev of (s.events ?? []).filter((x: any) => x.executionId === id)) {
      // Executor events carry the native record in `data`; `fleetNormalized.messages` is the harness-neutral reply text.
      const d = ev.data ?? {}, at = ev.timestamp ?? t, item = d.item;
      (d.fleetNormalized?.messages ?? []).forEach((m: any, i: number) => { if (m.role === 'assistant' && m.text) out.push({ id: 'e' + ev.id + ':' + i, t: at, who: 'Agent', text: m.text }); });
      if (ev.type === 'item.completed' && item && item.type !== 'agent_message' && item.type !== 'userMessage') out.push({ id: 'e' + ev.id, t: at, call: { name: String(item.command ?? item.name ?? item.type).slice(0, 120), state: item.status ?? 'completed', secs: 0 } });
    }
  }
  return out;
}
