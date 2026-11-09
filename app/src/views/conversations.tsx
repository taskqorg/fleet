// Conversations (docs/design-system/pages/conversations.md): sessions list and one conversation with its live transcript.
// Actions use the same operations agents use: session_create, message_send, execution_stop, session_fork.
import { useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  ListDetail, ListFrame, FilterBar, StateCounters, RowCheck, VirtualList, StateBadge, HarnessMark, Button, Transcript, Composer, DecisionBlock,
  Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses, type Entry, type Harness, type StateOf,
} from '../../../packages/ui/src/index';
import tokens from '../../../packages/ui/tokens.json';
import { op, online } from '../api';
import { useList, useDoc } from '../store';
import { useUrlView, useSearchBox } from './tasks';

const ROW = parseInt(tokens.size.row);
type SessionRow = { id: string; title: string | null; state: StateOf<'session'>; harness: string | null; openQuestions: number; sortKey: (string | number)[] };
const isHarness = (h: string | null): h is Harness => !!h && h in harnesses;
const reqId = () => crypto.randomUUID();

export function Conversations({ id }: { id?: string }) {
  const [, go] = useLocation();
  const { view, set, href } = useUrlView('conversations');
  const [query, setQuery] = useSearchBox(view.q, q => set({ q: q || undefined }, 'type'));
  const states = (view.state ?? []) as StateOf<'session'>[];
  const [failure, setFailure] = useState<string | null>(null);
  const list = useList('sessions', view);
  const rows = list.rows as SessionRow[];
  const sel = view.sel ?? [], toggleSel = (x: string) => set({ sel: sel.includes(x) ? sel.filter(i => i !== x) : [...sel, x] });
  const now = Date.now();
  const create = async () => { try { const s = await op('session_create', {}); go('/conversations/' + s.sessionId); } catch (e: any) { setFailure(e.message); } };
  const pane = <>
    <header class="page-h"><h1>Conversations</h1><OfflineMarker state={list.state} now={now} /><span class="grow" />
      <Button label="New conversation" icon="plus" kind="primary" disabledReason={online.value ? undefined : 'offline'} onClick={create} /></header>
    {failure && <ErrorBanner text={failure} />}
    <section class="panel">
      <ListFrame pending={list.pending} bar={<FilterBar query={query} placeholder="Search conversations" onQuery={setQuery} chips={[]} fields={[]} onAdd={() => {}} onRemove={() => {}} onClear={() => { setQuery(''); set({ q: undefined, state: [] }); }}
        shown={states.length ? states.reduce((n, s) => n + (list.counts[s] ?? 0), 0) : list.total} total={list.total}
        extra={<StateCounters family="session" label="Conversations by state" pending={list.pending} counts={list.counts as any} active={states} onAll={() => set({ state: [] })}
          onToggle={s => set({ state: states.includes(s) ? states.filter(x => x !== s) : [...states, s] })} />} />}>
        {list.error && !rows.length ? <ErrorBanner text={`Conversations did not load: ${list.error}`} onRetry={list.reload} />
          : list.shownKey === null || (!rows.length && list.pending) ? <Skeleton rows={8} />
          : !rows.length ? <Empty text="No conversations yet." action={<Button label="New conversation" onClick={create} />} />
          : <VirtualList items={rows} getKey={r => r.id} rowHeight={ROW} follow="top" label="Conversations" onEnd={list.more}
            columns="1.25rem minmax(0,1fr) 1.25rem 2rem 1.25rem" rowClass={r => r.id === id ? 'sel' : ''}
            header={<><span /><span>Conversation</span><span /><span class="r">Asks</span><span /></>}
            render={r => <>
              <span class="lead"><StateBadge family="session" state={r.state} compact /></span>
              <span class="tt"><a class="title row-link" href={href({ focus: r.id })} onClick={e => { e.preventDefault(); set({ focus: r.id }); }}>{r.title ?? 'New conversation'}</a><span class="id">{r.id.slice(0, 8)}</span></span>
              <span>{isHarness(r.harness) && <HarnessMark h={r.harness} />}</span>
              <span class="r">{r.openQuestions ? <span class="chip-age t-attention alert" aria-label={`${r.openQuestions} open questions`}>{r.openQuestions}</span> : ''}</span>
              <RowCheck checked={sel.includes(r.id)} label={'Select ' + (r.title ?? 'conversation')} onToggle={() => toggleSel(r.id)} />
            </>} />}
      </ListFrame>
    </section>
  </>;
  return <ListDetail list={pane} detail={id ? <Conversation id={id} onClose={() => set({ focus: undefined })} /> : undefined} detailKey={id} />;
}

// Transcript entries in execution order: the operator's message, then that execution's agent messages and tool calls.
function entries(s: any): Entry[] {
  const out: Entry[] = [];
  const execs = new Map<string, any>((s.executions ?? []).map((e: any) => [e.id, e]));
  // Turns in message order; each message's execution events follow it. Events of executions without a message come last.
  const turns = [...(s.messages ?? []).map((m: any) => m.executionId), ...(s.events ?? []).map((e: any) => e.executionId)].filter((v, i, a) => v && a.indexOf(v) === i);
  for (const id of turns) {
    const e = execs.get(id) ?? {}, t = e.createdAt ?? e.startedAt ?? Date.now();
    for (const m of (s.messages ?? []).filter((m: any) => m.executionId === id)) out.push({ id: 'm' + m.id, t, who: 'You', text: m.text });
    for (const ev of (s.events ?? []).filter((x: any) => x.executionId === id)) {
      const item = ev.item;
      if (ev.type === 'item.completed' && item?.type === 'agent_message') out.push({ id: 'e' + ev.id, t: ev.at ?? t, who: 'Agent', text: item.text });
      else if (item && item.type !== 'agent_message' && ev.type === 'item.completed') out.push({ id: 'e' + ev.id, t: ev.at ?? t, call: { name: String(item.command ?? item.name ?? item.type).slice(0, 120), state: item.status ?? 'completed', secs: 0 } });
    }
  }
  return out;
}

function Conversation({ id, onClose }: { id: string; onClose: () => void }) {
  const [, go] = useLocation();
  const { data: s, error, state } = useDoc<any>(`session:${id}`, () => op('session_get', { sessionId: id }), c => c.sessionId === id);
  const [failure, setFailure] = useState<string | null>(null);
  const now = Date.now(), off = !online.value;
  const run = async (fn: () => Promise<unknown>) => { setFailure(null); try { await fn(); } catch (e: any) { setFailure(e.message); } };
  if (error && !s) return <article class="detail" data-ui="detail" tabIndex={-1}><ErrorBanner text={`Conversation did not load: ${error}`} /></article>;
  if (!s) return <article class="detail" data-ui="detail" tabIndex={-1}><Skeleton rows={6} /></article>;
  const active = (s.executions ?? []).filter((e: any) => !['completed', 'failed', 'stopped', 'unknown', 'succeeded', 'cancelled'].includes(e.state)).at(-1);
  const open = (s.questions ?? []).filter((q: any) => q.state === 'open');
  return <article class="detail conversation" data-ui="detail" tabIndex={-1} aria-label="Conversation">
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
      <span class="muted mono">{id.slice(0, 8)}{s.agentConfig?.id ? ` · ${s.agentConfig.id}` : ''}</span><span class="grow" />
      <OfflineMarker state={state} now={now} />
      {active && <Button label="Stop" icon="pause" disabledReason={off ? 'offline' : undefined} onClick={() => run(() => op('execution_stop', { executionId: active.id }))} />}
      <Button label="Fork" icon="copy" iconOnly disabledReason={off ? 'offline' : undefined} onClick={() => run(async () => { const f = await op('session_fork', { sessionId: id, requestId: reqId() }); go('/conversations/' + f.sessionId); })} />
    </header>
    {failure && <ErrorBanner text={failure} />}
    {open.map((q: any) => <DecisionBlock key={q.id} question={q.question ?? q.text} askedBy="agent" addressedTo="you" since={q.askedAt ? Date.parse(q.askedAt) || now : now} now={now}
      options={(Array.isArray(q.options) ? q.options : []).map((o: string, i: number) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
      onAnswer={a => run(() => op('questions_answer', { questionId: q.id, answer: a }))} />)}
    <div class="pane"><Transcript entries={entries(s)} label="Transcript" now={now} />
      <Composer draftKey={id} disabledReason={off ? 'offline' : undefined} placeholder={active ? 'Steer or queue a message' : 'Message'}
        onSend={async text => { setFailure(null); try { await op('message_send', { sessionId: id, requestId: reqId(), text }); } catch (e: any) { setFailure(e.message); throw e; } }} /></div>
  </article>;
}
