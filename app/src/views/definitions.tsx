// Agents, Pipelines and Catalogue (docs/designs/fleet-definition-editors.md, patterns/definition-editor.md): definitions in
// the coordinator's store. The control column lists one kind; the work area edits one as a draft and saves revisions.
import { useEffect, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import { ListDetail, DefinitionEditor, DraftBar, DetailSection, KvRow, StateBadge, Empty, SettingField, CodeEditor, DiffText, StepList, AgeChip, type StepItem } from '../../../packages/ui/src/index.ts';
import { op, OpError, type Ops } from '../api.ts';
import { useDoc } from '../store.ts';

type Kind = 'agent' | 'pipeline' | 'catalogue';
type Get = Ops['definition_get']['reply'];
type Draft = { base: number; text: string; changes: number; stale: boolean; problems: { path: string; message: string }[] };
const SECTION: Record<Kind, { title: string; route: string }> = {
  agent: { title: 'Agents', route: '/agents' }, pipeline: { title: 'Pipelines', route: '/pipelines' }, catalogue: { title: 'Catalogue', route: '/catalogue' },
};
const contentOf = (kind: Kind, text: string) => ({ [kind]: JSON.parse(text) });

export function Definitions({ kind, id }: { kind: Kind; id?: string }) {
  const [, go] = useLocation();
  const { title, route } = SECTION[kind];
  const { data } = useDoc<Ops['definition_list']['reply']>(`definitions:${kind}`, () => op('definition_list', { kind }), c => c.kind === 'definition');
  const items = data?.items ?? [];
  const list = <>
    <header class="page-h"><h1>{title}</h1><span class="muted mono">{items.length}</span></header>
    <section class="panel">
      <div class="vl-wrap" style={{ '--cols': '1.25rem minmax(0,1fr) auto' }}>
        {data && items.length === 0 && <Empty text={`No ${title.toLowerCase()} yet`} />}
        {!data && <Empty text="Loading…" />}
        {[...items].sort((a, b) => Number(!!b.draft) - Number(!!a.draft)).map(x =>
          <div key={x.id} role="row" class={`vl-row static${x.id === id ? ' sel' : ''}`} onClick={() => go(`${route}/${x.id}`)}>
            <span class="lead"><StateBadge family="definition" state={x.draft ? 'draft' : 'ready'} compact /></span>
            <span class="tt"><a class="title row-link" href={`${route}/${x.id}`} onClick={e => { e.preventDefault(); go(`${route}/${x.id}`); }}>{x.name}</a>
              {x.name !== x.id && <span class="id">{x.id}</span>}{x.agent && <span class="muted">{x.agent.harness} · {x.agent.model}</span>}</span>
            <span class="r mono" data-tip={x.state}>r{x.revision}{x.state !== 'synced' ? ` · ${x.state}` : ''}</span>
          </div>)}
      </div>
    </section>
  </>;
  return <ListDetail list={list} detailKey={id} detail={id && <Editor key={`${kind}/${id}`} kind={kind} id={id} onClose={() => go(route)} />} />;
}

function Editor({ kind, id, onClose }: { kind: Kind; id: string; onClose: () => void }) {
  const [tab, setTab] = useState('Configuration');
  const [def, setDef] = useState<Get | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [text, setText] = useState('');
  const [message, setMessage] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const timer = useRef(0);
  const load = async () => {
    try {
      const g = await op('definition_get', { kind, id });
      setDef(g);
      try {
        const d = await op('definition_draft_get', { kind, id });
        const t = JSON.stringify(d.content?.[kind], null, 2) + '\n';
        setDraft({ base: d.draft.baseRevision, text: t, changes: d.draft.changes, stale: d.draft.stale, problems: d.problems ?? [] });
        setText(t);
      } catch (e) {
        if (!(e instanceof OpError && e.status === 404)) throw e;
        setDraft(null); setText(g.text);
      }
      setError(null);
    } catch (e: any) { setError(e.message); }
  };
  useEffect(() => { load(); }, [kind, id]);
  // Typing keeps a draft: written after a pause, so the next execution still uses the saved revision.
  const edit = (t: string) => {
    setText(t);
    clearTimeout(timer.current);
    timer.current = setTimeout(async () => {
      if (!def) return;
      if (t === def.text) { await op('definition_draft_discard', { kind, id }).catch(() => {}); setDraft(null); return; }
      let content;
      try { content = contentOf(kind, t); } catch (e: any) { setDraft(d => ({ base: d?.base ?? def.head, text: t, changes: d?.changes ?? 0, stale: false, problems: [{ path: '', message: 'JSON: ' + e.message }] })); return; }
      try {
        const r = await op('definition_draft_put', { kind, id, baseRevision: draft?.base ?? def.head, content: content as any });
        setDraft({ base: r.draft.baseRevision, text: t, changes: r.draft.changes, stale: r.draft.stale, problems: r.problems });
      } catch (e: any) { setError(e.message); }
    }, 700) as unknown as number;
  };
  const run = async (f: () => Promise<unknown>) => { setBusy(true); try { await f(); setError(null); } catch (e: any) { setError(e.message); } finally { setBusy(false); } };
  const save = () => run(async () => {
    if (!def || !draft) return;
    await op('definition_save', { kind, id, baseRevision: def.head, content: contentOf(kind, text) as any, message: message.trim() || 'Edited in Fleet', ...(def.state === 'repository-changed' && def.repoSha ? { repoSha: def.repoSha } : {}) });
    setMessage(''); await load();
  });
  const discard = () => run(async () => { await op('definition_draft_discard', { kind, id }); await load(); });
  if (!def) return <article class="detail" data-ui="detail" tabIndex={-1}>{error ? <div class="error" role="alert">{error}</div> : <Empty text="Loading…" />}</article>;
  const tabs = ['Configuration', 'Diff', 'History'];
  return <DefinitionEditor label={id} crumb={`shared · ${def.path} · r${def.head}${def.state !== 'synced' ? ` · ${def.state}` : ''}`} title={def.id}
    tabs={tabs} tab={tab} onTab={setTab} onClose={onClose}>
    {error && <div class="error" role="alert">{error}</div>}
    {draft && <DraftBar changes={draft.changes} base={`r${draft.base}`} note={draft.stale ? `The saved revision moved to r${def.head}: look at the diff before saving.` : 'New executions keep using the saved revision.'}
      actions={[
        ...(kind === 'pipeline' ? [{ label: 'Run', icon: 'play' as const, onClick: () => run(() => op('pipeline_run_create', { pipelineId: id, requestId: crypto.randomUUID(), draft: true })) }] : []),
        { label: 'Diff', onClick: () => setTab('Diff') },
        { label: 'Discard', icon: 'x' as const, onClick: discard },
        { label: 'Save as commit', icon: 'save' as const, primary: true, onClick: busy || draft.problems.length ? undefined : save },
      ]} />}
    {tab === 'Configuration' && <>
      {draft && <SettingField label="Message" value={message} help="Says what the revision changes; shown in History." now={Date.now()} status={{ kind: 'pending' }} onSave={setMessage} />}
      {kind === 'pipeline' && <Steps text={text} />}
      <DetailSection title="Definition">
        <CodeEditor label={def.path} value={text} onChange={edit} problems={draft?.problems ?? []} />
      </DetailSection>
    </>}
    {tab === 'Diff' && <DiffTab kind={kind} id={id} hasDraft={!!draft} head={def.head} />}
    {tab === 'History' && <HistoryTab kind={kind} id={id} />}
  </DefinitionEditor>;
}

function Steps({ text }: { text: string }) {
  let steps: any[] = [];
  try { steps = JSON.parse(text).steps ?? []; } catch { return null; }
  const items: StepItem[] = steps.map(s => ({ id: s.id, kind: s.kind === 'api' ? 'tool' : s.kind === 'script' ? 'program' : 'agent', executor: s.profile ?? s.tool ?? s.command ?? '', reads: '', writes: 'output' }));
  return <DetailSection title="Steps"><StepList items={items} label="Steps" /></DetailSection>;
}

function DiffTab({ kind, id, hasDraft, head }: { kind: Kind; id: string; hasDraft: boolean; head: number }) {
  const [d, setD] = useState<Ops['definition_diff']['reply'] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const from = hasDraft ? { revision: head } : { repository: true }, to = hasDraft ? { draft: true } : { revision: head };
  useEffect(() => { op('definition_diff', { kind, id, from, to }).then(setD, e => setError(e.message)); }, [kind, id, hasDraft, head]);
  return <DetailSection title={hasDraft ? `Draft against r${head}` : `r${head} against the repository file`}>
    {error ? <div class="error" role="alert">{error}</div> : d ? <DiffText label="Diff" text={d.unified} /> : <Empty text="Loading…" />}
  </DetailSection>;
}

function HistoryTab({ kind, id }: { kind: Kind; id: string }) {
  const [h, setH] = useState<Ops['definition_history']['reply'] | null>(null);
  useEffect(() => { op('definition_history', { kind, id }).then(setH, () => {}); }, [kind, id]);
  const now = Date.now();
  return <DetailSection title="Revisions">
    {!h ? <Empty text="Loading…" /> : h.revisions.map(r => <KvRow key={r.revision} icon={r.source === 'agent' ? 'bot' : r.source === 'repo' ? 'git-merge' : 'file-plus-2'} label={`r${r.revision} · ${r.message}`}
      meta={<span class="mono">{r.actor.person?.name ?? r.actor.executionId ?? 'Fleet'}{r.actor.kind === 'agent' && r.actor.directedBy ? ` for ${r.actor.directedBy.name}` : ''} · {r.contentSha.slice(0, 7)}</span>}
      value={<AgeChip t={Date.parse(r.at)} now={now} />} />)}
  </DetailSection>;
}
