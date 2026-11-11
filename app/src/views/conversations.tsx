// Conversations (docs/design-system/pages/conversations.md): sessions list and one conversation with its live transcript.
// Actions use the same operations agents use: session_create, message_send, execution_stop, session_fork.
import { useEffect, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import {
  ListDetail, ListFrame, FilterBar, StateCounters, RowCheck, VirtualList, StateBadge, HarnessMark, Button, Thread, Composer, DecisionBlock, SearchField, ActionMenu, DetailSection, type VirtualListHandle,
  Choice, Empty, ErrorBanner, Skeleton, OfflineMarker, harnesses, type Harness, type StateOf,
  writeClip, zone,
} from '../../../packages/ui/src/index.ts';
import tokens from '../../../packages/ui/tokens.json';
import { op, online } from '../api.ts';
import { useList, useDoc } from '../store.ts';
import { useUrlView, useSearchBox, nextUrl } from '../url.ts';
import { useThread, type ThreadKey } from '../threads.ts';
import { click, all, inside, range, type Selection } from '../selection.ts';

const ROW = parseInt(tokens.size['row-2']); // two-line catalogue row
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
  const [newConversation,setNewConversation]=useState(!!new URLSearchParams(location.search).get('project'));
  const create=()=>setNewConversation(true);
  const pane = <>
    <header class="page-h"><h1>Conversations</h1><OfflineMarker state={list.state} now={now} /><span class="grow" />
      <Button label="New conversation" icon="plus" kind="primary" disabledReason={online.value ? undefined : 'offline'} onClick={create} /></header>
    {failure && <ErrorBanner text={failure} />}
    {newConversation&&<NewConversation onDone={sessionId=>{setNewConversation(false);set({focus:sessionId});}}/>}
    <section class="panel">
      <ListFrame pending={list.pending} bar={<FilterBar query={query} placeholder="Search conversations" onQuery={setQuery} chips={[]} fields={[]} onAdd={() => {}} onRemove={() => {}} onClear={() => { setQuery(''); set({ q: undefined, state: [] }); }}
        shown={states.length ? states.reduce((n, s) => n + (list.counts[s] ?? 0), 0) : list.total} total={list.total}
        extra={<StateCounters family="session" label="Conversations by state" pending={list.pending} counts={list.counts as any} active={states} onAll={() => set({ state: [] })}
          onToggle={s => set({ state: states.includes(s) ? states.filter(x => x !== s) : [...states, s] })} />} />}>
        {list.error && !rows.length ? <ErrorBanner text={`Conversations did not load: ${list.error}`} onRetry={list.reload} />
          : list.shownKey === null || (!rows.length && list.pending) ? <Skeleton rows={8} />
          : !rows.length ? <Empty text="No conversations yet." action={<Button label="New conversation" onClick={create} />} />
          : <VirtualList items={rows} getKey={r => r.id} rowHeight={ROW} follow="top" label="Conversations" onEnd={list.more}
            columns="1.25rem minmax(0,1fr) 2rem 1.25rem" rowClass={r => r.id === id ? 'sel' : ''}
            render={r => <>
              <span class="lead"><StateBadge family="session" state={r.state} compact /></span>
              <span class="tt"><a class="title row-link" href={href({ focus: r.id })} onClick={e => { e.preventDefault(); set({ focus: r.id }); }}>{r.title ?? 'New conversation'}</a><span class="id">{r.id.slice(0, 8)}</span>{isHarness(r.harness) && <HarnessMark h={r.harness} />}</span>
              <span class="r">{r.openQuestions ? <span class="chip-age t-attention alert" aria-label={`${r.openQuestions} open questions`}>{r.openQuestions}</span> : ''}</span>
              <RowCheck checked={sel.includes(r.id)} label={'Select ' + (r.title ?? 'conversation')} onToggle={() => toggleSel(r.id)} />
            </>} />}
      </ListFrame>
    </section>
  </>;
  return <ListDetail list={pane} detail={id ? <Conversation key={id} id={id} onClose={() => set({ focus: undefined })} /> : undefined} detailKey={id} />;
}

function Conversation({ id, onClose }: { id: string; onClose: () => void }) {
  const [, go] = useLocation();
  const { data: s, error, state } = useDoc<any>(`session:${id}`, () => op('session_get', { sessionId: id }), c => c.sessionId === id && c.kind !== 'event');
  // A session that got a task shows the task's thread (design "Thread scope"): the first page resolves it and returns the key.
  const thread = useThread({ session: id });
  const key: ThreadKey = thread.win?.key ?? { session: id };
  // A plain click marks where a selection starts (no tint, so reading stays quiet); Shift+click selects up to that row.
  const [sel, setSel] = useState<Selection>(null), pickFrom = useRef<string | null>(null);
  // Cmd+A selects the thread; Cmd+C copies the selected rows from the server as markdown and chat-safe HTML. The clipboard write starts inside the
  // key handler with a promise, so the browser keeps the user gesture while the export loads.
  const copySel = (s0: NonNullable<Selection>) => {
    try {
      // Both formats (formats.md "Copy"); times in the reader's own zone.
      const text = op('thread_export', { key, ...range(s0), tz: zone() }).then(r => r.markdown);
      writeClip(text).catch(e => copyFail(e, () => copySel(s0)));
    } catch (e) { copyFail(e, () => copySel(s0)); } // no async clipboard (plain http): say so instead of failing silently
  };
  // The key handler reads the latest selection through a ref: effects run after paint, and a key can come first.
  const live = useRef({ sel, copySel }); live.current = { sel, copySel };
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      const sel = live.current.sel, copySel = live.current.copySel;
      const t = e.target as HTMLElement;
      // Only inside the conversation (or with nothing focused); code, not key, so other keyboard layouts work too.
      if (!(e.metaKey || e.ctrlKey) || t?.closest?.('input,textarea') || !(t?.closest?.('.conversation') || t === document.body)) return;
      if (e.code === 'KeyA') { e.preventDefault(); setSel(all()); }
      // A native selection inside one row is the browser's own copy.
      if (e.code === 'KeyC' && sel && (getSelection()?.isCollapsed ?? true)) { e.preventDefault(); copySel(sel); }
    };
    addEventListener('keydown', on); return () => removeEventListener('keydown', on);
  }, []);
  const [failure, setFailure] = useState<{ text: string; retry?: () => void } | null>(null);
  const [panel, setPanel] = useState<'switch' | 'handoff' | null>(null);
  const [q, setQ] = useState(''), [hits, setHits] = useState<{ key: string; snippet: string }[] | null>(null), [hit, setHit] = useState(0), [marked, setMarked] = useState<string | null>(null);
  const handle = useRef<VirtualListHandle | null>(null);
  // The hit is scrolled to once the replaced window is on screen.
  const [jumpTo, setJumpTo] = useState<string | null>(null);
  useEffect(() => { if (jumpTo && thread.win?.blocks.some(b => b.key === jumpTo)) { handle.current?.scrollToKey(jumpTo, 'center'); setJumpTo(null); } }, [thread.win, jumpTo]);
  const now = Date.now(), off = !online.value;
  const run = async (fn: () => Promise<unknown>) => { setFailure(null); try { await fn(); } catch (e: any) { setFailure({ text: e.message, retry: () => run(fn) }); } };
  const copyFail = (e: unknown, retry?: () => void) => setFailure({ text: `Copy failed: ${(e as any)?.message ?? e}`, retry });
  // A jump replaces the window with entries around the hit, then puts the hit row in view and marks it.
  const open = async (list: { key: string }[], i: number) => { if (!list[i]) return; setHit(i); setMarked(list[i].key); await thread.jump(list[i].key); setJumpTo(list[i].key); };
  const search = () => run(async () => { if (!q.trim()) { setHits(null); setMarked(null); return; } const r = await op('thread_search', { key, q, limit: 100 }); setHits(r.hits); await open(r.hits, 0); });
  if (error && !s) return <article class="detail" data-ui="detail" tabIndex={-1}><ErrorBanner text={`Conversation did not load: ${error}`} /></article>;
  if (!s) return <article class="detail" data-ui="detail" tabIndex={-1}><Skeleton rows={6} /></article>;
  const running = (e: any) => !['succeeded', 'failed', 'cancelled'].includes(e.state) && e.observation !== 'unconfirmed';
  // Composer target: the newest running execution; with none, a new message starts one (design "Controls").
  const active = (s.executions ?? []).filter(running).at(-1);
  const openQs = (s.questions ?? []).filter((q: any) => q.state === 'open');
  const w = thread.win;
  const lastMessage: string | undefined = (s.messages ?? []).at(-1)?.text;
  return <article class="detail conversation" data-ui="detail" tabIndex={-1} aria-label="Conversation">
    <header class="detail-h">
      <Button label="Close" icon="x" iconOnly onClick={onClose} />
      <span class="muted mono">{id.slice(0, 8)}{s.agentConfig?.id ? ` · ${s.agentConfig.id}` : ''}</span><span class="grow" />
      <SearchField label="Search thread" value={q} onInput={setQ} onSearch={search} />
      <span class="count" aria-live="polite">{hits ? `${hits.length ? hit + 1 : 0} of ${hits.length}` : w ? `${w.blocks.length.toLocaleString('en')} of ${w.total.toLocaleString('en')}` : ''}</span>
      <Button label="Previous hit" icon="arrow-up" iconOnly disabledReason={!hits || hit <= 0 ? 'no earlier hit' : undefined} onClick={() => hits && open(hits, hit - 1)} />
      <Button label="Next hit" icon="arrow-down" iconOnly disabledReason={!hits || hit >= hits.length - 1 ? 'no later hit' : undefined} onClick={() => hits && open(hits, hit + 1)} />
      <OfflineMarker state={state} now={now} />
      <Button label="Copy thread as markdown" icon="file-text" iconOnly disabledReason={off ? 'offline' : undefined} onClick={() => copySel(all()!)} />
      {/* Menu actions (design "Controls"): each calls the existing operation; the result shows as a new boundary in this thread. */}
      <ActionMenu label="Actions" disabledReason={off ? 'offline' : undefined} items={[
        { label: 'Retry last message', icon: 'rotate-ccw', disabledReason: lastMessage ? undefined : 'no message yet', onClick: () => run(() => op('message_send', { sessionId: id, requestId: reqId(), text: lastMessage })) },
        { label: 'Switch profile or model', icon: 'sliders-horizontal', onClick: () => setPanel('switch') },
        { label: 'Hand off to another agent', icon: 'bot', onClick: () => setPanel('handoff') },
        { label: 'Fork', icon: 'git-merge', onClick: () => { const requestId = reqId(); run(async () => { const f = await op('session_fork', { sessionId: id, requestId }); go(nextUrl(location.pathname + location.search, 'conversations', { focus: f.sessionId })); }); } },
      ]} />
    </header>
    {panel && <SwitchPanel sessionId={id} handoff={panel === 'handoff'} current={s.agentConfig?.id} onDone={note => { setPanel(null); if (note) setFailure(null); }} run={run} />}
    <div class="status-line" role="status">{failure ? <ErrorBanner text={failure.text} onRetry={failure.retry} /> : thread.error ? <ErrorBanner text={`Thread did not load: ${thread.error}`} onRetry={thread.newest} /> : null}</div>
    <div class="pane">
      {!w ? <Skeleton rows={6} /> : <Thread blocks={w.blocks} label="Transcript" now={now} marked={marked} selected={k => inside(sel, k)} onRowClick={(k, shift) => { if (!shift) { pickFrom.current = k; setSel(null); return; } setSel(click(click(null, pickFrom.current ?? sel?.start.key ?? k, false), k, true)); pickFrom.current ??= k; }} showSession={!!key && 'task' in key} onOpenResult={r => go('/results/' + encodeURIComponent(r))} handle={handle} onStart={thread.older} onEnd={thread.newer}
        stopReason={off ? 'offline' : undefined} onStop={x => run(() => op('execution_stop', { executionId: x }))}
        onAnswer={(questionId, answer) => run(() => op('questions_answer', { questionId, answer }))} onCopyFail={copyFail}
        pending={{ count: w.fresh, onJump: () => { setMarked(null); setHits(null); thread.newest(); } }} />}
      {/* D3: steer goes to the execution and does not answer; the open question stays in sight above the composer. */}
      {openQs.map((q: any) => <DecisionBlock key={q.id} question={q.question ?? q.text} askedBy="agent" addressedTo="you" since={q.askedAt ? Date.parse(q.askedAt) || now : now} now={now}
        options={(Array.isArray(q.options) ? q.options : []).map((o: string, i: number) => ({ label: String(o), kbd: String(i + 1), primary: i === 0 }))}
        onAnswer={a => run(() => op('questions_answer', { questionId: q.id, answer: a }))} />)}
      <div class="composer-bar">
        <span class="muted mono">{active ? `${active.id.slice(0, 8)}` : ''}</span>
        {active && <Button label="Stop" icon="pause" disabledReason={off ? 'offline' : undefined} onClick={() => run(() => op('execution_stop', { executionId: active.id }))} />}
      </div>
      <Composer draftKey={id} disabledReason={off ? 'offline' : undefined} placeholder={active ? 'Steer' : 'Message'}
        onSend={async text => { setFailure(null); try { await op('message_send', { sessionId: id, requestId: reqId(), text, ...(active ? { delivery: 'steer' } : {}) }); } catch (e: any) { setFailure({ text: e.message }); throw e; } }} /></div>
  </article>;
}

/**
 * Switch profile or model, or hand off to another agent (design "Menu actions"): session_transition decides reuse, a model
 * switch in the session, or a new session with a context handoff. An inline panel, never a floating form (OR-14).
 */
function SwitchPanel({ sessionId, handoff, current, onDone, run }: { sessionId: string; handoff: boolean; current?: string; onDone: (note?: string) => void; run: (fn: () => Promise<unknown>) => Promise<void> }) {
  const { data: profiles } = useDoc<any>('profiles', () => op('profiles_list', {}), () => false);
  const { data: accounts } = useDoc<any>(handoff ? 'accounts' : null, () => op('account_list', {}), c => c.kind === 'host');
  const [profile, setProfile] = useState<string | null>(null), [account, setAccount] = useState<string | null>(null), [done, setDone] = useState<string | null>(null);
  const list: any[] = profiles?.profiles ?? [];
  const named: any[] = (accounts?.accounts ?? []).filter((a: any) => a.enabled);
  const go = () => run(async () => { const r = await op('session_transition', { sessionId, profileId: profile ?? undefined, ...(account ? { account } : {}) }); setDone(`${r.transition}${r.sessionId && r.sessionId !== sessionId ? ` · new session ${String(r.sessionId).slice(0, 8)}` : ''}`); });
  return <DetailSection title={handoff ? 'Hand off to another agent' : 'Switch profile or model'}>
    <div class="answers" role="group" aria-label="Profile">{list.map(p => <Button key={p.id} label={`${p.id} · ${p.harness ?? 'codex'} ${p.model ?? ''}`} pressed={profile === p.id} kind={p.id === current ? 'quiet' : 'default'} onClick={() => setProfile(p.id)} />)}</div>
    {handoff && <div class="answers" role="group" aria-label="Account">{named.map(a => <Button key={a.id} label={`${a.name} · ${a.harness ?? 'codex'}`} pressed={account === a.name} onClick={() => setAccount(account === a.name ? null : a.name)} />)}</div>}
    <div class="answers"><Button label={handoff ? 'Hand off' : 'Switch'} kind="primary" disabledReason={profile ? undefined : 'pick a profile'} onClick={go} /><Button label="Close" kind="quiet" onClick={() => onDone(done ?? undefined)} />{done && <span class="muted" role="status">{done}</span>}</div>
  </DetailSection>;
}

function NewConversation({onDone}:{onDone:(sessionId:string)=>void}){
 const projects=useDoc<any>('conversation-projects',()=>op('project_list',{}),c=>c.kind==='definition');
 const [project,setProject]=useState(new URLSearchParams(location.search).get('project')??''),[role,setRole]=useState('pm'),[error,setError]=useState('');
 const selected=(projects.data?.projects??[]).find((p:any)=>p.id===project);
 const roles=Object.entries(selected?.roles??{}).map(([id,profile])=>({id,label:`${id} · ${String(profile)}`}));
 return <DetailSection title="New conversation"><Choice label="Project" value={project} onChange={p=>{setProject(p);setRole('pm');}} options={(projects.data?.projects??[]).map((p:any)=>({id:p.id,label:p.name}))}/>
 {project&&<Choice label="Role / configuration" value={role} onChange={setRole} options={roles}/>}
 {error&&<ErrorBanner text={error}/>}<Composer draftKey="new-project-conversation" placeholder="Describe the work for this project" disabledReason={!project||!roles.some(r=>r.id===role)?'Choose a project and a mapped role':undefined} onSend={async text=>{try{const r=await op('session_start',{projectId:project,role,requestId:crypto.randomUUID(),text});onDone(r.sessionId);}catch(e:any){setError(e.message);throw e;}}}/>
 <Button label="Projectless conversation" icon="messages-square" onClick={()=>op('session_create',{}).then(r=>onDone(r.sessionId),e=>setError(e.message))}/></DetailSection>;
}
