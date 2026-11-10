import type { Entry } from '../../packages/ui/src/index.ts';
import type { HostEvent, NormalizedMessage } from '../../contracts/generated/host-protocol.ts';

// An event as the coordinator stores it: the host's event plus the execution it belongs to.
type StoredEvent = HostEvent & { executionId: string };

// Transcript entries in execution order: the operator's message, then that execution's agent messages and tool calls.
export function entries(s: { executions?: any[]; messages?: any[]; events?: StoredEvent[] }): Entry[] {
  const out: Entry[] = [];
  const execs = new Map<string, any>((s.executions ?? []).map((e: any) => [e.id, e]));
  // Turns in message order; each message's execution events follow it. Events of executions without a message come last.
  const turns = [...(s.messages ?? []).map((m: any) => m.executionId), ...(s.events ?? []).map((e) => e.executionId)].filter((v, i, a) => v && a.indexOf(v) === i);
  for (const id of turns) {
    const e = execs.get(id) ?? {}, t = e.createdAt ?? e.startedAt ?? Date.now();
    for (const m of (s.messages ?? []).filter((m: any) => m.executionId === id)) out.push({ id: 'm' + m.id, t, who: 'You', text: m.text });
    for (const ev of (s.events ?? []).filter((x) => x.executionId === id).sort((a, b) => a.seq - b.seq)) {
      // Executor events carry the native record in `data`; `fleetNormalized.messages` is the harness-neutral reply text.
      const d = ev.data ?? {}, at = ev.timestamp ?? t, item = d.item;
      const said: Array<Partial<NormalizedMessage>> = d.fleetNormalized?.messages ?? (ev.type === 'item.completed' && item?.type === 'agent_message' ? [{ role: 'assistant', text: item.text }] : []);
      said.forEach((m, i) => {
        if (m.role !== 'assistant' || !m.text) return;
        // Streamed chunks (antigravity) join the reply they continue.
        const last: any = out.at(-1);
        if (m.delta && last?.who === 'Agent' && last.exec === id) last.text += m.text;
        else out.push({ id: 'e' + ev.id + ':' + i, t: at, who: 'Agent', text: m.text, exec: id } as any);
      });
      if (ev.type === 'item.completed' && item && !['agent_message', 'userMessage', 'reasoning', 'plan'].includes(item.type ?? '')) out.push({ id: 'e' + ev.id, t: at, call: { name: String(item.command ?? item.name ?? item.type).slice(0, 120), state: item.status ?? 'completed', secs: 0 } });
    }
  }
  return out;
}
