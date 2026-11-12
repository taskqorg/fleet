// Agents, Pipelines and Catalogue (docs/designs/fleet-definition-editors.md, patterns/definition-editor.md): definitions in
// the coordinator's store. The control column lists one kind; the work area edits one as a draft and saves revisions.
import { useEffect, useRef, useState } from 'preact/hooks';
import { useLocation } from 'wouter-preact';
import { ListDetail, DefinitionEditor, DraftBar, DetailSection, KvRow, StateBadge, Empty, SettingField, CodeEditor, DiffText, StepList, AgeChip, Choice, Button, SearchField, type StepItem } from '../../../packages/ui/src/index.ts';
import { op, OpError, writeOnce, useAlive, type Ops } from '../api.ts';
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
  const [tab, setTab] = useState(kind === 'catalogue' ? 'Methods' : 'Configuration');
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
        if (!g.draft) { setDraft(null); setText(g.text); setError(null); return; }
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
  const alive = useAlive();
  const runDraft = () => run(async () => { await flush(); const requestId = crypto.randomUUID(); await writeOnce('pipeline_run_create', { pipelineId: id, requestId, draft: true }, () => setError('unknown, retrying'), { signal: alive }); });
  if (!def) return <article class="detail" data-ui="detail" tabIndex={-1}>{error ? <div class="error" role="alert">{error}</div> : <Empty text="Loading…" />}</article>;
  const tabs = kind === 'agent' ? ['Configuration', 'Effective', 'Diff', 'Compatibility', 'History'] : kind === 'catalogue' ? ['Methods', 'Configuration', 'Diff', 'History'] : ['Configuration', 'Diff', 'History'];
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
    {tab === 'Methods' && kind === 'catalogue' && <CatalogueMethods />}
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
      meta={<span class="mono">{r.actor.person?.name ?? (r.actor.executionId ? 'agent' : 'Fleet')}{r.actor.kind === 'agent' && r.actor.directedBy ? ` for ${r.actor.directedBy.name}` : ''} · {r.contentSha.slice(0, 7)}</span>}
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


function CatalogueMethods() {
  const [, go] = useLocation();
  const [projects, setProjects] = useState<string[]>([]), [project, setProject] = useState('');
  const [search, setSearch] = useState(''), [scopeSearch, setScopeSearch] = useState(''), [useSearch, setUseSearch] = useState('');
  const [useCases, setUseCases] = useState<{id: string; goal: string}[]>([]), [useCase, setUseCase] = useState('');
  const [selected, setSelected] = useState<string[]>([]), [baseline, setBaseline] = useState('');
  const [busy, setBusy] = useState(false), [error, setError] = useState<string | null>(null);
  const [revision, setRevision] = useState(0);
  const {data: candidate, error: catalogueError, restored} = useDoc(`catalogue:${project}:${revision}`, async () => { if (!project) return null; const reply = await op('catalogue', {projectId: project}); if (reply.projectId !== project) throw new Error('Catalogue response project mismatch'); return reply; }, change => ['definition', 'experiment', 'score', 'execution', 'project-recommendation'].includes(change.kind));
  useEffect(() => { op('project_list', {}).then(r => { const ids = (r.projects as any[]).map(p => String(p.id)); setProjects(ids); setProject(ids[0] ?? ''); }, x => setError(x.message)); op('lab_registry', {}).then(r => setUseCases(r.useCases as any), x => setError(x.message)); }, []);
  const c = candidate?.projectId === project && !catalogueError ? candidate : null;
  const methods = c?.methods ?? [];
  const choose = (id: string) => { if (!c || restored || busy) return; const next = selected.includes(id) ? selected.filter(x => x !== id) : [...selected, id]; setSelected(next); if (!next.includes(baseline)) setBaseline(next.includes(String(c?.recommendedMethod)) ? String(c?.recommendedMethod) : ''); };
  const act = async (run: () => Promise<void>) => { setBusy(true); try { await run(); setError(null); } catch (x: any) {setError(x.message);} finally {setBusy(false);} };
  return <>
    <DetailSection title="Project"><SearchField label="Find project" value={scopeSearch} onInput={setScopeSearch} onSearch={() => {}} /><Choice label="Project" value={project} options={projects.filter(p => p.includes(scopeSearch)).map(p => ({id:p,label:p}))} onChange={p => {setProject(p);setSelected([]);setBaseline('');setUseCase('');setError(null);setRevision(r=>r+1);}} /></DetailSection>
    {error && <div class="error" role="alert">{error}</div>}
    {catalogueError && <div class="error" role="alert">Catalogue unavailable: {catalogueError}. No current evidence loaded for {project}.<Button label="Retry Catalogue" icon="rotate-cw" onClick={() => setRevision(r=>r+1)} /></div>}
    {c && <p class="muted">Evidence project: {c.projectId}{restored ? ' · restored, read-only until current confirmation' : ''}</p>}
    <DetailSection title="Methods"><SearchField label="Find method" value={search} onInput={setSearch} onSearch={() => {}} />
      {methods.filter(m => `${m.name} ${m.id}`.toLowerCase().includes(search.toLowerCase())).map(m => {
        const a = m.assessment;
        return <div key={m.id}>
          <KvRow icon="circle-check" label={`${m.name} · ${m.id}`} meta={`${m.scope} · ${m.pipeline}`} value={<><Choice label={`Select ${m.name}`} value={selected} options={[{id:m.id,label:selected.includes(m.id) ? 'Selected' : 'Select'}]} onChange={choose} /><Button label="Recommend for project" icon="check" iconOnly busy={busy} disabledReason={restored ? 'Await current scoped Catalogue read' : undefined} onClick={() => act(async () => {await op('recommendation_set', {projectId:project,methodId:m.id});setRevision(r=>r+1);})} /></>} />
          <KvRow icon="chart-no-axes-column" label={a.scoreForm === 'typed' ? 'Comparison score' : 'Quality'} meta={a.score == null ? 'Unavailable' : `${a.score.toFixed(3)}${a.partial ? ' · partial/incomplete' : ''}`} value={`${a.basis} · ${a.eligibleCells ?? 0}/${a.expectedCells ?? 0} cells · ${a.examples ?? 0} ${a.examples === 1 ? 'example' : 'examples'}`} />
          <KvRow icon="clock" label="Time / cost" meta={a.wallMsMedian == null ? 'Time unavailable' : `${a.wallMsMedian} ms · ${a.durationSamples == null ? 'pipeline usage window' : `${a.durationSamples} samples`}`} value={`${a.costUsd == null ? 'Cost unavailable' : `$${a.costUsd} · ${a.costBasis}`}`} />
          <KvRow icon="folder-open" label="Recommended for" meta={m.recommendedFor.join(', ') || 'None'} />
          {a.state && <p class="muted">{a.state} · {a.stopReason ?? ''} · {a.failedCells ?? 0} failed · {a.excludedCells ?? 0} excluded</p>}
          {a.reason && <p>{a.reason}</p>}{a.exclusions && <p>{a.exclusions}</p>}
          {a.evaluator && <p class="muted">{a.variant} · {a.split} · {a.evaluator} · {a.evaluatorVersion}</p>}
          {a.experimentId && <a href={`/lab/${a.experimentId}`}>Assessment experiment</a>}
          {a.sourceExperimentId && <a href={`/lab/${a.sourceExperimentId}`}>Source experiment</a>}
          {a.ongoingExperimentId && <a href={`/lab/${a.ongoingExperimentId}`}>Current experiment · {a.ongoingState}</a>}
        </div>;
      })}
    </DetailSection>
    <DetailSection title="Compare methods in Lab">
      <Choice label="Baseline" value={baseline} options={methods.filter(m=>selected.includes(m.id)).map(m=>({id:m.id,label:m.name}))} onChange={setBaseline} />
      <SearchField label="Find use case" value={useSearch} onInput={setUseSearch} onSearch={() => {}} />
      <Choice label="Use case" value={useCase} options={useCases.filter(u=>`${u.id} ${u.goal}`.toLowerCase().includes(useSearch.toLowerCase())).map(u=>({id:u.id,label:u.id}))} onChange={setUseCase} />
      <Button label="Create comparison" icon="play" busy={busy} disabledReason={catalogueError || restored || !c ? 'Await current scoped Catalogue read' : !project || selected.length<2 || !baseline || !selected.includes(baseline) || selected.some(id => !methods.some(m => m.id === id)) || !useCase ? 'Select two methods, a baseline and use case' : undefined} onClick={() => act(async () => {const r = await op('lab_compare_methods', {projectId:project,capability:String(c?.capability),methods:selected,baseline,useCase});go(`/lab/${r.experimentId}`);})} />
    </DetailSection>
  </>;
}
