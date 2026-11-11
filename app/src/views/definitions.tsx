// Agents, Pipelines and Catalogue (docs/designs/fleet-definition-editors.md, patterns/definition-editor.md): definitions in
// the coordinator's store. The control column lists one kind; the work area edits one as a draft and saves revisions.
import { useEffect, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import { ListDetail, DefinitionEditor, DraftBar, DetailSection, KvRow, StateBadge, Empty, SettingField, CodeEditor, DiffText, StepList, AgeChip, Choice, type StepItem } from '../../../packages/ui/src/index.ts';
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
  const pending = useRef<(() => Promise<void>) | null>(null);
  useEffect(() => () => clearTimeout(timer.current), []);
  const load = async () => {
    try {
      const g = await op('definition_get', { kind, id });
      setDef(g);
      try {
        const d = await op('definition_draft_get', { kind, id });
        setDraft({ base: d.draft.baseRevision, text: d.text, changes: d.draft.changes, stale: d.draft.stale, problems: d.problems ?? [] });
        setText(d.text); // the bytes the draft holds, not a re-encoding of its typed content
      } catch (e) {
        if (!(e instanceof OpError && e.status === 404)) throw e;
        setDraft(null); setText(g.text);
      }
      setError(null);
    } catch (e: any) { setError(e.message); }
  };
  useEffect(() => { load(); }, [kind, id]);
  // Typing keeps a draft: written after a pause, so the next execution still uses the saved revision.
  const flush = async () => { clearTimeout(timer.current); const f = pending.current; pending.current = null; if (f) await f(); };
  const edit = (t: string) => {
    setText(t);
    clearTimeout(timer.current);
    pending.current = async () => {
      if (!def) return;
      if (t === def.text) { await op('definition_draft_discard', { kind, id }).catch(() => {}); setDraft(null); return; }
      let content;
      try { content = contentOf(kind, t); } catch (e: any) { setDraft(d => ({ base: d?.base ?? def.head, text: t, changes: d?.changes ?? 0, stale: false, problems: [{ path: '', message: 'JSON: ' + e.message }] })); return; }
      try {
        const r = await op('definition_draft_put', { kind, id, baseRevision: draft?.base ?? def.head, content: content as any });
        setDraft({ base: r.draft.baseRevision, text: t, changes: r.draft.changes, stale: r.draft.stale, problems: r.problems });
      } catch (e: any) { setError(e.message); }
    };
    timer.current = setTimeout(flush, 700) as unknown as number;
  };
  const run = async (f: () => Promise<unknown>) => { setBusy(true); try { await f(); setError(null); } catch (e: any) { setError(e.message); } finally { setBusy(false); } };
  // Save from the draft's own base: the server refuses it (409) when someone else saved meanwhile or the repository file
  // changed, and the error says what to look at.
  const save = () => run(async () => {
    await flush();
    if (!def || !draft) return;
    await op('definition_save', { kind, id, baseRevision: draft.base, content: contentOf(kind, text) as any, message: message.trim() || 'Edited in Fleet' });
    setMessage(''); await load();
  });
  const discard = () => run(async () => { clearTimeout(timer.current); pending.current = null; await op('definition_draft_discard', { kind, id }); await load(); });
  const runDraft = () => run(async () => { await flush(); await op('pipeline_run_create', { pipelineId: id, requestId: crypto.randomUUID(), draft: true }); });
  if (!def) return <article class="detail" data-ui="detail" tabIndex={-1}>{error ? <div class="error" role="alert">{error}</div> : <Empty text="Loading…" />}</article>;
  const tabs = kind === 'agent' ? ['Configuration', 'Effective', 'Diff', 'Compatibility', 'History'] : ['Configuration', 'Diff', 'History'];
  return <DefinitionEditor label={id} crumb={`shared · ${def.path} · r${def.head}${def.state !== 'synced' ? ` · ${def.state}` : ''}`} title={def.id}
    tabs={tabs} tab={tab} onTab={setTab} onClose={onClose}>
    {error && <div class="error" role="alert">{error}</div>}
    {draft && <DraftBar changes={draft.changes} base={`r${draft.base}`} note={draft.stale ? `The saved revision moved to r${def.head}: look at the diff before saving.` : 'New executions keep using the saved revision.'}
      actions={[
        ...(kind === 'pipeline' ? [{ label: 'Run', icon: 'play' as const, onClick: busy || draft.problems.length ? undefined : runDraft }] : []),
        { label: 'Diff', onClick: () => setTab('Diff') },
        { label: 'Discard', icon: 'x' as const, onClick: discard },
        { label: 'Save as commit', icon: 'save' as const, primary: true, onClick: busy || draft.problems.length ? undefined : save },
      ]} />}
    {tab === 'Configuration' && <>
      {draft && <SettingField label="Message" value={message} help="Says what the revision changes; shown in History." now={Date.now()} status={{ kind: 'pending' }} onSave={setMessage} />}
      {kind === 'pipeline' && <Steps text={text} />}
      {kind === 'agent' && <AgentFacts text={text} />}
      <DetailSection title="Definition">
        <CodeEditor label={def.path} value={text} onChange={edit} problems={draft?.problems ?? []} />
      </DetailSection>
    </>}
    {tab === 'Diff' && <DiffTab kind={kind} id={id} hasDraft={!!draft} head={def.head} />}
    {tab === 'History' && <HistoryTab kind={kind} id={id} />}
    {tab === 'Effective' && <EffectiveTab id={id} draft={!!draft} version={draft?.text ?? ''} />}
    {tab === 'Compatibility' && <CompatibilityTab id={id} />}
  </DefinitionEditor>;
}

// reads: what a step's arguments or prompt reference (${example.x}, ${steps.<id>.output…}).
const readsOf = (s: any) => [...new Set([...JSON.stringify(s).matchAll(/\$\{(example|steps\.[\w-]+)/g)].map(m => m[1].replace('steps.', '')))].join(', ') || 'nothing';

function Steps({ text }: { text: string }) {
  let steps: any[] = [];
  try { steps = JSON.parse(text)?.steps; } catch { return null; }
  if (!Array.isArray(steps)) return null;
  steps = steps.filter(s => s && typeof s === 'object');
  const items: StepItem[] = steps.map(s => ({ id: s.id, kind: s.kind === 'api' ? 'tool' : s.kind === 'script' ? 'program' : 'agent', executor: s.profile ?? s.tool ?? s.command ?? '', reads: readsOf(s), writes: 'output' }));
  return <DetailSection title="Steps"><StepList items={items} label="Steps" /></DetailSection>;
}

function AgentFacts({ text }: { text: string }) {
  let a: any;
  try { a = JSON.parse(text); } catch { return null; }
  if (!a || typeof a !== 'object') return null;
  const list = (v: unknown) => Array.isArray(v) ? v.join(', ') : '';
  return <DetailSection title="Identity">
    <KvRow icon="settings" label="Harness" meta={<span class="mono">{a.harness ?? 'codex'}</span>} />
    <KvRow icon="bot" label="Model" meta={<span class="mono">{a.model}{a.effort ? ` · ${a.effort}` : ''}</span>} />
    {list(a.skills) && <KvRow icon="file-text" label="Skills" meta={list(a.skills)} />}
    {list(a.tools) && <KvRow icon="terminal" label="Tools" meta={list(a.tools)} />}
    {list(a.connections) && <KvRow icon="link" label="Connections" meta={`${list(a.connections)} · credentials stay in the connection`} />}
  </DetailSection>;
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
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { op('definition_history', { kind, id }).then(setH, e => setError(e.message)); }, [kind, id]);
  if (error) return <DetailSection title="Revisions"><div class="error" role="alert">{error}</div></DetailSection>;
  const now = Date.now();
  return <DetailSection title="Revisions">
    {!h ? <Empty text="Loading…" /> : h.revisions.map(r => <KvRow key={r.revision} icon={r.source === 'agent' ? 'bot' : r.source === 'repo' ? 'git-merge' : 'file-plus-2'} label={`r${r.revision} · ${r.message}`}
      meta={<span class="mono">{r.actor.person?.name ?? r.actor.executionId ?? 'Fleet'}{r.actor.kind === 'agent' && r.actor.directedBy ? ` for ${r.actor.directedBy.name}` : ''} · {r.contentSha.slice(0, 7)}</span>}
      value={<AgeChip t={Date.parse(r.at)} now={now} />} />)}
  </DetailSection>;
}

function EffectiveTab({ id, draft, version }: { id: string; draft: boolean; version: string }) {
  const [projects, setProjects] = useState<string[]>([]);
  const [project, setProject] = useState('');
  const [e, setE] = useState<Ops['agent_effective']['reply'] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { op('project_list', {}).then(r => { const ids = (r.projects as any[]).map(p => String(p.id)); setProjects(ids); setProject(p => p || ids[0] || ''); }, x => setError(x.message)); }, []);
  useEffect(() => {
    if (!project) return;
    let live = true; // a later project or draft replaces this answer
    const t = setTimeout(() => op('agent_effective', { id, projectId: project, draft }).then(r => { if (live) { setE(r); setError(null); } }, x => { if (live) setError(x.message); }), 800);
    return () => { live = false; clearTimeout(t); };
  }, [id, project, draft, version]);
  return <>
    {projects.length > 0 && <DetailSection title="Project"><Choice label="Project" value={project} onChange={setProject} options={projects.map(p => ({ id: p, label: p }))} /></DetailSection>}
    {error && <div class="error" role="alert">{error}</div>}
    {e && <DetailSection title="Layers, in the order the agent receives them">
      {e.layers.map(l => <KvRow key={l.name} icon={l.name === 'role' ? 'bot' : l.name === 'project' ? 'folder-open' : 'file-plus-2'} label={l.name}
        meta={<span class="mono">{l.source}{l.revision ? ` · ${l.revision}` : ''}{l.fields.length ? ` · ${l.fields.join(', ')}` : ''}</span>} />)}
    </DetailSection>}
    {e && <DetailSection title="Effective settings">
      <KvRow icon="settings" label="Harness" meta={<span class="mono">{(e.result as any).harness ?? 'codex'}</span>} />
      <KvRow icon="bot" label="Model" meta={<span class="mono">{e.result.model}{e.result.effort ? ` · ${e.result.effort}` : ''}</span>} />
    </DetailSection>}
  </>;
}

function CompatibilityTab({ id }: { id: string }) {
  const [c, setC] = useState<Ops['agent_compatibility']['reply'] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { op('agent_compatibility', { id }).then(setC, x => setError(x.message)); }, [id]);
  const now = Date.now();
  return <DetailSection title="Combinations seen in 30 days">
    {error ? <div class="error" role="alert">{error}</div> : !c ? <Empty text="Loading…" /> : c.rows.map(r =>
      <KvRow key={`${r.harness}/${r.harnessVersion}/${r.model}/${r.effort}`} icon={r.state === 'checked' ? 'circle-check' : r.state === 'failed' ? 'circle-x' : 'circle-dashed'}
        label={`${r.harness}${r.harnessVersion ? ' ' + r.harnessVersion : ''} · ${r.model}${r.effort ? ' · ' + r.effort : ''}`}
        meta={<span class="mono">{r.state === 'unchecked' ? 'not checked' : r.state}{r.revision ? ` · r${r.revision}` : ''}{r.error ? ` · ${r.error}` : ''}</span>}
        value={r.evidence ? <AgeChip t={Date.parse(r.evidence.at)} now={now} /> : undefined} />)}
  </DetailSection>;
}
